// Package state owns fanout's persistent pane state under .fanout/state.json.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

const SchemaVersion = 1

const (
	PaneKindAttachedAgent = "attached-agent"
	PaneKindShell         = "shell"
)

type Store struct {
	SchemaVersion int    `json:"schemaVersion"`
	Panes         []Pane `json:"panes"`
}

// Pane records one launched agent pane. Parent and IssueNum form the legacy
// idempotency key: issue fanout uses the parent issue or Project ref plus the
// GitHub issue number, while synthetic launches use a reserved parent such as
// @manual with non-GitHub numbers that only need to be unique under that parent.
// TaskID is an additive key for issue-less task rows.
type Pane struct {
	Parent     string `json:"parent"`
	IssueNum   int    `json:"issueNum"`
	TaskID     string `json:"taskId,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Slug       string `json:"slug"`
	BranchName string `json:"branchName"`
	// BaseBranch is the resolved base branch the worktree branched from
	// (e.g. "main"). Legacy rows recorded before this field load as "".
	BaseBranch string `json:"baseBranch,omitempty"`
	PaneID     string `json:"paneId"`
	// ShellKey is a tmux pane user-option token for TUI shell terminals. Shell
	// panes can share WorktreePath with the repo root or an agent worktree, so
	// liveness uses this marker instead of path-prefix matching.
	ShellKey string `json:"shellKey,omitempty"`
	// SourceParent/SourceIssueNum/SourceTaskID point at the pane whose worktree an
	// attached-agent pane shares. They are informational; the attached pane keeps
	// its own synthetic identity so it never overwrites the source pane's row.
	SourceParent   string `json:"sourceParent,omitempty"`
	SourceIssueNum int    `json:"sourceIssueNum,omitempty"`
	SourceTaskID   string `json:"sourceTaskId,omitempty"`
	Agent          string `json:"agent"`
	// CodexPlanMode は解決済みの Codex Plan Mode 設定(app-server Plan Mode thread +
	// 対話 Codex TUI)で起動したペインかどうか。ダッシュボードの GET /api/plan が
	// plan 抽出の対象ペインを限定するために参照する。additive なフィールドなので
	// SchemaVersion は据え置き(旧版 fanout は未知キーとして無視して読める)。
	CodexPlanMode bool `json:"codexPlanMode,omitempty"`
	// CodexThreadID/CodexSessionID preserve the app-server-backed Codex thread
	// created for Plan Mode panes so a recreated pane can resume the exact same
	// agent session instead of falling back to codex resume --last.
	CodexThreadID  string `json:"codexThreadId,omitempty"`
	CodexSessionID string `json:"codexSessionId,omitempty"`
	Wave           string `json:"wave,omitempty"`
	DisplayName    string `json:"displayName"`
	WorktreePath   string `json:"worktreePath"`
	Prompt         string `json:"prompt"`
	CreatedAt      string `json:"createdAt"`
	// AgentStatus は起動時に "running" を記録する。終了検知デーモンは無いので
	// 表示側は tmux の動的判定(起動ラッパーが設定する pane user option
	// @fanout_agent_state)を優先し、tmux 不通時のみこの記録値に fallback する。
	AgentStatus string `json:"agentStatus,omitempty"`
	// SourceProjectRoot はこの pane を読み込んだ worktree の root
	// (<root>/.fanout/state.json の <root>)。非永続(json:"-")で、複数 worktree を
	// またいで集約する MergedStateLoader だけが設定する。表示面が write
	// (close/merge/cleanup) を所有元の state.json へルーティングするために使う。
	// 単一 root のロードでは常に空。
	SourceProjectRoot string `json:"-"`
	// SourceProjectRoots は同一 identity((parent,issueNum)/(parent,taskId))が
	// 複数 worktree の state.json に記録されていた場合の、その全 root。
	// MergedStateLoader は表示上は 1 行に畳むが、close/cleanup が取りこぼした
	// sibling ストアを残さないよう全 root をここに集約する。非永続(json:"-")で、
	// 通常は [SourceProjectRoot] の 1 要素。
	SourceProjectRoots []string `json:"-"`
}

func (p Pane) IsShell() bool {
	return p.Kind == PaneKindShell
}

func (p Pane) IsAttachedAgent() bool {
	return p.Kind == PaneKindAttachedAgent
}

// LockedStore holds .fanout/state.json.lock while fanout plans and launches.
// The deliberately coarse lock serializes parallel fanout invocations so the
// (parent, issueNum) idempotency check and state update happen in one critical
// section instead of racing through independent read-modify-write cycles.
type LockedStore struct {
	path string
	file *os.File
	Store
}

func Path(projectRoot string) string {
	return filepath.Join(projectRoot, ".fanout", "state.json")
}

func LoadProject(projectRoot string) (Store, error) {
	return Load(Path(projectRoot))
}

func Load(path string) (Store, error) {
	var store Store
	found, err := atomicfs.ReadJSON(path, &store)
	if err != nil {
		if found {
			return Store{}, fmt.Errorf("parse fanout state %s: %w", path, err)
		}
		return Store{}, fmt.Errorf("read fanout state %s: %w", path, err)
	}
	if !found {
		return emptyStore(), nil
	}
	store.normalize()
	return store, nil
}

func LockProject(projectRoot string) (*LockedStore, error) {
	return Lock(Path(projectRoot))
}

func Lock(path string) (*LockedStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create fanout state directory: %w", err)
	}
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open fanout state lock %s: %w", lockPath, err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		// Cleanup of the never-locked handle; the flock error is the one to report.
		_ = f.Close()
		return nil, fmt.Errorf("lock fanout state %s: %w", lockPath, err)
	}
	store, err := Load(path)
	if err != nil {
		// Best-effort unwind; the Load error is the one to report.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return &LockedStore{path: path, file: f, Store: store}, nil
}

func (l *LockedStore) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if err != nil {
		return err
	}
	return closeErr
}

func (l *LockedStore) RecordPane(p Pane) error {
	l.UpsertTask(p)
	return save(l.path, l.Store)
}

func (l *LockedStore) RemovePane(parent string, issueNum int) error {
	if !l.Remove(parent, issueNum) {
		return nil
	}
	return save(l.path, l.Store)
}

func (l *LockedStore) RemoveTaskPane(parent, taskID string) error {
	if !l.removeTask(parent, taskID) {
		return nil
	}
	return save(l.path, l.Store)
}

// Save writes the currently locked store back to disk.
func (l *LockedStore) Save() error {
	return save(l.path, l.Store)
}

func (s Store) FannedNumbersForParent(parent string) map[int]bool {
	out := map[int]bool{}
	for _, pane := range s.Panes {
		if pane.IssueNum > 0 && parentMatches(pane.Parent, parent) {
			out[pane.IssueNum] = true
		}
	}
	return out
}

func (s Store) FannedNumbersForOtherParents(parent string) map[int]bool {
	out := map[int]bool{}
	for _, pane := range s.Panes {
		if pane.IssueNum > 0 && !parentMatches(pane.Parent, parent) {
			out[pane.IssueNum] = true
		}
	}
	return out
}

func (s Store) FannedTaskIDsForParent(parent string) map[string]bool {
	out := map[string]bool{}
	for _, pane := range s.Panes {
		if pane.TaskID != "" && parentMatches(pane.Parent, parent) {
			out[pane.TaskID] = true
		}
	}
	return out
}

func (s Store) Find(parent string, issueNum int) (Pane, bool) {
	for _, pane := range s.Panes {
		if pane.IssueNum == issueNum && parentMatches(pane.Parent, parent) {
			return pane, true
		}
	}
	return Pane{}, false
}

func (s Store) FindTask(parent, taskID string) (Pane, bool) {
	if taskID == "" {
		return Pane{}, false
	}
	for _, pane := range s.Panes {
		if pane.TaskID == taskID && parentMatches(pane.Parent, parent) {
			return pane, true
		}
	}
	return Pane{}, false
}

func (s Store) PanesForParent(parent string) []Pane {
	var out []Pane
	for _, pane := range s.Panes {
		if parentMatches(pane.Parent, parent) {
			out = append(out, pane)
		}
	}
	return out
}

func (s *Store) Upsert(p Pane) {
	s.normalize()
	for i := range s.Panes {
		if s.Panes[i].IssueNum == p.IssueNum && parentMatches(s.Panes[i].Parent, p.Parent) {
			s.Panes[i] = p
			return
		}
	}
	s.Panes = append(s.Panes, p)
}

func (s *Store) UpsertTask(p Pane) {
	s.normalize()
	for i := range s.Panes {
		if taskPaneMatches(s.Panes[i], p) {
			s.Panes[i] = p
			return
		}
	}
	s.Panes = append(s.Panes, p)
}

func (s *Store) Remove(parent string, issueNum int) bool {
	kept := s.Panes[:0]
	removed := false
	for _, pane := range s.Panes {
		if pane.IssueNum == issueNum && parentMatches(pane.Parent, parent) {
			removed = true
			continue
		}
		kept = append(kept, pane)
	}
	s.Panes = kept
	return removed
}

func (s *Store) removeTask(parent, taskID string) bool {
	if taskID == "" {
		return false
	}
	kept := s.Panes[:0]
	removed := false
	for _, pane := range s.Panes {
		if pane.TaskID == taskID && parentMatches(pane.Parent, parent) {
			removed = true
			continue
		}
		kept = append(kept, pane)
	}
	s.Panes = kept
	return removed
}

func save(path string, store Store) error {
	store.normalize()
	// MkdirAll runs here (not just inside WriteJSON) so its failure keeps this
	// wrapped message; WriteJSON's own MkdirAll is then a no-op.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create fanout state directory: %w", err)
	}
	return atomicfs.WriteJSON(path, store, 0o644)
}

func emptyStore() Store {
	return Store{SchemaVersion: SchemaVersion, Panes: []Pane{}}
}

func (s *Store) normalize() {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	if s.Panes == nil {
		s.Panes = []Pane{}
	}
}

func parentMatches(stored, filter string) bool {
	if stored == filter {
		return true
	}
	storedNum, storedErr := strconv.Atoi(stored)
	filterNum, filterErr := strconv.Atoi(filter)
	return storedErr == nil && filterErr == nil && storedNum == filterNum
}

func taskPaneMatches(stored, incoming Pane) bool {
	if !parentMatches(stored.Parent, incoming.Parent) {
		return false
	}
	if stored.TaskID != "" && incoming.TaskID != "" {
		return stored.TaskID == incoming.TaskID
	}
	return stored.IssueNum == incoming.IssueNum
}
