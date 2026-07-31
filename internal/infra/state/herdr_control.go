package state

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/execx"
)

const HerdrControlSchemaVersion = 1

var herdrControlCommitSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type HerdrIntentKind string

const (
	HerdrIntentCoordinator HerdrIntentKind = "coordinator"
	HerdrIntentWorktree    HerdrIntentKind = "worktree"
)

type HerdrIntentStatus string

const (
	HerdrIntentPlanned               HerdrIntentStatus = "planned"
	HerdrIntentIssued                HerdrIntentStatus = "issued"
	HerdrIntentRealized              HerdrIntentStatus = "realized"
	HerdrIntentManualCleanupRequired HerdrIntentStatus = "manual_cleanup_required"
)

// HerdrResource is the runtime and Git identity established by one Herdr
// workspace mutation. The workspace label is the ownership nonce.
type HerdrResource struct {
	WorkspaceID string `json:"workspaceId"`
	Label       string `json:"label"`
	PaneID      string `json:"paneId"`
	TerminalID  string `json:"terminalId"`
	CurrentPath string `json:"currentPath"`
	RepoKey     string `json:"repoKey,omitempty"`
	RepoRoot    string `json:"repoRoot,omitempty"`
}

// HerdrIntent is one minimal mutation journal row. Status records whether the
// socket request may have been issued; recovery never reissues an issued
// request and instead classifies the current workspace and checkout state.
type HerdrIntent struct {
	ID               string            `json:"id"`
	Kind             HerdrIntentKind   `json:"kind"`
	Status           HerdrIntentStatus `json:"status"`
	Parent           string            `json:"parent"`
	RuntimeParent    string            `json:"runtimeParent"`
	OwnerProjectRoot string            `json:"ownerProjectRoot,omitempty"`
	IssueNum         int               `json:"issueNum,omitempty"`
	TaskID           string            `json:"taskId,omitempty"`
	Backend          backend.Name      `json:"backend"`

	Slug          string `json:"slug,omitempty"`
	BranchName    string `json:"branchName,omitempty"`
	FullBranchRef string `json:"fullBranchRef,omitempty"`
	BaseBranch    string `json:"baseBranch,omitempty"`
	BaseSHA       string `json:"baseSha,omitempty"`
	ExpectedHead  string `json:"expectedHead,omitempty"`
	WorktreePath  string `json:"worktreePath"`
	BranchExisted bool   `json:"branchExisted,omitempty"`
	BranchCreated bool   `json:"branchCreated,omitempty"`

	WorkspaceLabel string        `json:"workspaceLabel"`
	Resource       HerdrResource `json:"resource"`
	Coordinator    HerdrResource `json:"coordinator"`
	Session        string        `json:"session"`
	SocketPath     string        `json:"socketPath"`
	TimeoutMS      int64         `json:"timeoutMs"`
	ExpiresUnixMS  int64         `json:"expiresUnixMs"`

	Failure string `json:"failure,omitempty"`
}

// HerdrRow is the final shared-registry record populated after launcher
// finalization. Issue #527 provides its persistence and backend-binding shape;
// issue #528 owns the transition from a realized intent to this row.
type HerdrRow struct {
	ID               string          `json:"id"`
	Kind             HerdrIntentKind `json:"kind"`
	Parent           string          `json:"parent"`
	RuntimeParent    string          `json:"runtimeParent"`
	OwnerProjectRoot string          `json:"ownerProjectRoot,omitempty"`
	IssueNum         int             `json:"issueNum,omitempty"`
	TaskID           string          `json:"taskId,omitempty"`
	Backend          backend.Name    `json:"backend"`

	Slug          string `json:"slug,omitempty"`
	BranchName    string `json:"branchName,omitempty"`
	FullBranchRef string `json:"fullBranchRef,omitempty"`
	BaseBranch    string `json:"baseBranch,omitempty"`
	BaseSHA       string `json:"baseSha,omitempty"`
	ExpectedHead  string `json:"expectedHead,omitempty"`
	WorktreePath  string `json:"worktreePath"`
	BranchExisted bool   `json:"branchExisted,omitempty"`
	BranchCreated bool   `json:"branchCreated,omitempty"`

	Resource   HerdrResource `json:"resource"`
	Session    string        `json:"session"`
	SocketPath string        `json:"socketPath"`
}

type HerdrControlStore struct {
	SchemaVersion   int           `json:"schemaVersion"`
	GitCommonDir    string        `json:"gitCommonDir"`
	GitCommonDevice uint64        `json:"gitCommonDevice"`
	GitCommonInode  uint64        `json:"gitCommonInode"`
	Rows            []HerdrRow    `json:"rows"`
	Intents         []HerdrIntent `json:"intents"`
}

type herdrControlCommonIdentity struct {
	path   string
	device uint64
	inode  uint64
}

type LockedHerdrControl struct {
	path string
	file *os.File
	HerdrControlStore
}

// HerdrControlPath returns the repository-common registry path shared by every
// linked worktree.
func HerdrControlPath(projectRoot string) (string, error) {
	out, err := execx.Combined(projectRoot, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve Herdr control git common directory: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return "", fmt.Errorf("resolve Herdr control git common directory: invalid path %q", commonDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(projectRoot, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize Herdr control git common directory: %w", err)
	}
	commonDir = filepath.Clean(commonDir)
	if err := validateHerdrControlCommonDir(commonDir); err != nil {
		return "", err
	}
	return filepath.Join(commonDir, "fanout", "herdr-control.json"), nil
}

func LoadHerdrControl(projectRoot string) (HerdrControlStore, error) {
	path, err := HerdrControlPath(projectRoot)
	if err != nil {
		return HerdrControlStore{}, err
	}
	return loadHerdrControl(path)
}

func LockHerdrControl(projectRoot string) (*LockedHerdrControl, error) {
	path, pathErr := HerdrControlPath(projectRoot)
	if pathErr != nil {
		return nil, pathErr
	}
	file, openErr := lockHerdrControlPath(path)
	if openErr != nil {
		return nil, openErr
	}
	store, err := loadHerdrControl(path)
	if err != nil {
		// The load error is authoritative while the private lock is unwound.
		_ = unlockStateFile(file)
		return nil, err
	}
	return &LockedHerdrControl{path: path, file: file, HerdrControlStore: store}, nil
}

func lockHerdrControlPath(path string) (*os.File, error) {
	if err := ensurePrivateHerdrControlDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create Herdr control directory: %w", err)
	}
	lockPath := path + ".lock"
	file, openErr := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW,
		0o600,
	)
	if openErr != nil {
		return nil, fmt.Errorf("open Herdr control lock %s: %w", lockPath, openErr)
	}
	info, statErr := file.Stat()
	if statErr == nil {
		statErr = validatePrivateHerdrControlFile(lockPath, info)
	}
	if statErr != nil {
		_ = file.Close() // The namespace validation error is authoritative.
		return nil, statErr
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close() // The flock error is authoritative.
		return nil, fmt.Errorf("lock Herdr control %s: %w", lockPath, err)
	}
	return file, nil
}

func (l *LockedHerdrControl) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockStateFile(l.file)
	l.file = nil
	return unlockErr
}

func (l *LockedHerdrControl) Save() error {
	if l == nil || l.file == nil {
		return fmt.Errorf("save Herdr control without a held lock")
	}
	l.normalize()
	identity, identityErr := herdrControlIdentity(l.path)
	if identityErr != nil {
		return identityErr
	}
	if bindingErr := validateHerdrControlIdentity(l.HerdrControlStore, identity); bindingErr != nil {
		return bindingErr
	}
	if validateErr := validateHerdrControl(l.HerdrControlStore); validateErr != nil {
		return validateErr
	}
	if writeErr := atomicfs.WriteJSON(l.path, l.HerdrControlStore, 0o600); writeErr != nil {
		return fmt.Errorf("write Herdr control %s: %w", l.path, writeErr)
	}
	info, err := os.Lstat(l.path)
	if err != nil {
		return fmt.Errorf("validate written Herdr control %s: %w", l.path, err)
	}
	if err := validatePrivateHerdrControlFile(l.path, info); err != nil {
		return err
	}
	return nil
}

func (s HerdrControlStore) FindIntent(id string) (HerdrIntent, bool) {
	for _, intent := range s.Intents {
		if intent.ID == id {
			return intent, true
		}
	}
	return HerdrIntent{}, false
}

func (s *HerdrControlStore) UpsertIntent(intent HerdrIntent) {
	for i := range s.Intents {
		if s.Intents[i].ID == intent.ID {
			s.Intents[i] = intent
			return
		}
	}
	s.Intents = append(s.Intents, intent)
}

func (s *HerdrControlStore) RemoveIntent(id string) bool {
	for i := range s.Intents {
		if s.Intents[i].ID != id {
			continue
		}
		s.Intents = append(s.Intents[:i], s.Intents[i+1:]...)
		return true
	}
	return false
}

func (s HerdrControlStore) FindRow(id string) (HerdrRow, bool) {
	for _, row := range s.Rows {
		if row.ID == id {
			return row, true
		}
	}
	return HerdrRow{}, false
}

func (s *HerdrControlStore) UpsertRow(row HerdrRow) {
	for i := range s.Rows {
		if s.Rows[i].ID == row.ID {
			s.Rows[i] = row
			return
		}
	}
	s.Rows = append(s.Rows, row)
}

func (s HerdrControlStore) RowBindings(
	ownerProjectRoot string,
) []backend.Binding {
	bindings := make([]backend.Binding, 0, len(s.Rows))
	for _, row := range s.Rows {
		parent, ok := herdrBindingParent(
			row.RuntimeParent,
			row.OwnerProjectRoot,
			ownerProjectRoot,
		)
		if !ok {
			continue
		}
		bindings = append(bindings, backend.Binding{Parent: parent, Backend: row.Backend})
	}
	return bindings
}

func (s HerdrControlStore) ProvisionalBindings(
	ownerProjectRoot string,
) []backend.Binding {
	bindings := make([]backend.Binding, 0, len(s.Intents))
	for _, intent := range s.Intents {
		parent, ok := herdrBindingParent(
			intent.RuntimeParent,
			intent.OwnerProjectRoot,
			ownerProjectRoot,
		)
		if !ok {
			continue
		}
		bindings = append(bindings, backend.Binding{Parent: parent, Backend: intent.Backend})
	}
	return bindings
}

func herdrBindingParent(
	parent, storedRoot, projectRoot string,
) (string, bool) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if !strings.HasPrefix(parent, "plan:") {
		return parent, true
	}
	return parent, storedRoot == filepath.Clean(projectRoot)
}

func HerdrOwnerProjectRoot(parent, projectRoot string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if !strings.HasPrefix(parent, "plan:") {
		return "", nil
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return "", fmt.Errorf("herdr plan owner project root must be a canonical absolute path")
	}
	return projectRoot, nil
}

func HerdrCoordinatorIntentID(parent, ownerProjectRoot string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if parent == "" {
		return "", fmt.Errorf("herdr coordinator intent requires a parent")
	}
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, ownerProjectRoot)
	if err != nil {
		return "", err
	}
	return "coordinator:" + herdrOwnerTuple(parent, ownerProjectRoot), nil
}

// HerdrWorktreeIntentID uses the same issue/task key as the tmux state store:
// (parent, issue number) or (plan parent, task id).
func HerdrWorktreeIntentID(parent, ownerProjectRoot string, issueNum int, taskID string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	taskID = strings.TrimSpace(taskID)
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, ownerProjectRoot)
	if err != nil {
		return "", err
	}
	owner := herdrOwnerTuple(parent, ownerProjectRoot)
	switch {
	case parent == "":
		return "", fmt.Errorf("herdr worktree intent requires a parent")
	case taskID != "" && issueNum == 0:
		return "task:" + owner + ":" + tuplePart(taskID), nil
	case taskID == "" && issueNum > 0:
		return "issue:" + owner + ":" + strconv.Itoa(issueNum), nil
	default:
		return "", fmt.Errorf("herdr worktree intent requires exactly one issue number or task id")
	}
}

func herdrOwnerTuple(parent, ownerProjectRoot string) string {
	identity := tuplePart(parent)
	if ownerProjectRoot != "" {
		identity += ":" + tuplePart(ownerProjectRoot)
	}
	return identity
}

func loadHerdrControl(path string) (HerdrControlStore, error) {
	identity, identityErr := herdrControlIdentity(path)
	if identityErr != nil {
		return HerdrControlStore{}, identityErr
	}
	var store HerdrControlStore
	found, err := readPrivateHerdrControlJSON(path, &store)
	if err != nil {
		if found {
			return HerdrControlStore{}, fmt.Errorf("parse Herdr control %s: %w", path, err)
		}
		return HerdrControlStore{}, fmt.Errorf("read Herdr control %s: %w", path, err)
	}
	if !found {
		store = emptyHerdrControl(identity)
	} else if store.SchemaVersion == 0 {
		return HerdrControlStore{}, fmt.Errorf(
			"validate Herdr control %s: unsupported Herdr control schema version 0",
			path,
		)
	}
	store.normalize()
	if err := validateHerdrControlIdentity(store, identity); err != nil {
		return HerdrControlStore{}, fmt.Errorf("validate Herdr control %s: %w", path, err)
	}
	if err := validateHerdrControl(store); err != nil {
		return HerdrControlStore{}, fmt.Errorf("validate Herdr control %s: %w", path, err)
	}
	return store, nil
}

func herdrControlIdentity(path string) (herdrControlCommonIdentity, error) {
	commonDir := filepath.Dir(filepath.Dir(path))
	resolved, resolveErr := filepath.EvalSymlinks(commonDir)
	if resolveErr != nil {
		return herdrControlCommonIdentity{}, fmt.Errorf(
			"canonicalize Herdr control git common directory: %w",
			resolveErr,
		)
	}
	resolved = filepath.Clean(resolved)
	if validateErr := validateHerdrControlCommonDir(resolved); validateErr != nil {
		return herdrControlCommonIdentity{}, validateErr
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return herdrControlCommonIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return herdrControlCommonIdentity{}, fmt.Errorf(
			"herdr control common directory %s has no physical identity",
			resolved,
		)
	}
	return herdrControlCommonIdentity{
		path: resolved, device: normalizeHerdrControlStatDevice(stat.Dev), inode: stat.Ino,
	}, nil
}

func normalizeHerdrControlStatDevice[T ~int32 | ~uint32 | ~uint64](device T) uint64 {
	return uint64(device)
}

func validateHerdrControlIdentity(
	store HerdrControlStore,
	want herdrControlCommonIdentity,
) error {
	if store.GitCommonDir != want.path || store.GitCommonDevice != want.device ||
		store.GitCommonInode != want.inode {
		return fmt.Errorf("herdr control belongs to a different git common directory")
	}
	return nil
}

func ensurePrivateHerdrControlDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return fmt.Errorf("herdr control directory %s is not an owner-only real directory", path)
	}
	if err := validateHerdrControlOwner(path, info); err != nil {
		return err
	}
	return validateHerdrControlACL(path)
}

func validateHerdrControlCommonDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("herdr control common directory %s is not a real directory", path)
	}
	if err := validateHerdrControlOwner(path, info); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("herdr control common directory %s is writable by another uid", path)
	}
	return validateHerdrControlACL(path)
}

func readPrivateHerdrControlJSON(path string, target any) (bool, error) {
	dir := filepath.Dir(path)
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		return false, nil
	}
	if err := ensurePrivateHerdrControlDir(dir); err != nil {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		_ = file.Close() // The read or decode result is authoritative.
	}()
	info, err := file.Stat()
	if err == nil {
		err = validatePrivateHerdrControlFile(path, info)
	}
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return true, err
	}
	return true, nil
}

func validatePrivateHerdrControlFile(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 {
		return fmt.Errorf("herdr control file %s is not an owner-only regular file", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return fmt.Errorf("herdr control file %s has an invalid link identity", path)
	}
	if err := validateHerdrControlOwner(path, info); err != nil {
		return err
	}
	return validateHerdrControlACL(path)
}

func validateHerdrControlOwner(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("herdr control path %s is not owned by the current uid", path)
	}
	return nil
}

func validateHerdrControlACL(path string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	cmd := exec.Command("/bin/ls", "-lde", path)
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("inspect ACL on Herdr control path %s: %w", path, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	mode, _, ok := strings.Cut(first, " ")
	if !ok || strings.Contains(mode, "+") {
		return fmt.Errorf("herdr control path %s has an extended ACL", path)
	}
	return nil
}

func emptyHerdrControl(identity herdrControlCommonIdentity) HerdrControlStore {
	return HerdrControlStore{
		SchemaVersion:   HerdrControlSchemaVersion,
		GitCommonDir:    identity.path,
		GitCommonDevice: identity.device,
		GitCommonInode:  identity.inode,
		Rows:            []HerdrRow{},
		Intents:         []HerdrIntent{},
	}
}

func (s *HerdrControlStore) normalize() {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = HerdrControlSchemaVersion
	}
	if s.Rows == nil {
		s.Rows = []HerdrRow{}
	}
	if s.Intents == nil {
		s.Intents = []HerdrIntent{}
	}
}

func validateHerdrControl(store HerdrControlStore) error {
	if store.SchemaVersion != HerdrControlSchemaVersion {
		return fmt.Errorf("unsupported Herdr control schema version %d", store.SchemaVersion)
	}
	if store.GitCommonDir == "" || !filepath.IsAbs(store.GitCommonDir) ||
		filepath.Clean(store.GitCommonDir) != store.GitCommonDir ||
		store.GitCommonDevice == 0 || store.GitCommonInode == 0 {
		return fmt.Errorf("herdr control git common directory identity is incomplete")
	}
	ids := map[string]string{}
	reservations := map[string]string{}
	for _, row := range store.Rows {
		if err := validateHerdrRow(row); err != nil {
			return err
		}
		if previous := ids[row.ID]; previous != "" {
			return fmt.Errorf("duplicate Herdr control id %q in %s and row", row.ID, previous)
		}
		ids[row.ID] = "row"
		if row.Kind == HerdrIntentWorktree {
			if err := reserveHerdrControlIdentity(
				reservations,
				row.ID,
				row.FullBranchRef,
				row.WorktreePath,
			); err != nil {
				return err
			}
		}
	}
	for _, intent := range store.Intents {
		if err := validateHerdrIntent(intent); err != nil {
			return err
		}
		if previous := ids[intent.ID]; previous != "" {
			return fmt.Errorf("duplicate Herdr control id %q in %s and intent", intent.ID, previous)
		}
		ids[intent.ID] = "intent"
		if intent.Kind != HerdrIntentWorktree {
			continue
		}
		if err := reserveHerdrControlIdentity(
			reservations,
			intent.ID,
			intent.FullBranchRef,
			intent.WorktreePath,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateHerdrRow(row HerdrRow) error {
	parent := parentref.Canon(strings.TrimSpace(row.Parent))
	runtimeParent := parentref.Canon(strings.TrimSpace(row.RuntimeParent))
	if row.ID == "" || parent == "" || row.Parent != parent ||
		runtimeParent == "" || row.RuntimeParent != runtimeParent ||
		row.Backend != backend.Herdr || row.WorktreePath == "" ||
		row.Session == "" || row.SocketPath == "" {
		return fmt.Errorf("herdr control row %q is incomplete", row.ID)
	}
	if err := validateHerdrRuntimeParent(parent, runtimeParent); err != nil {
		return fmt.Errorf("herdr row %s: %w", row.ID, err)
	}
	ownerProjectRoot, ownerErr := HerdrOwnerProjectRoot(parent, row.OwnerProjectRoot)
	if ownerErr != nil || row.OwnerProjectRoot != ownerProjectRoot {
		return fmt.Errorf("herdr row %s has invalid owner project root", row.ID)
	}
	var expectedID string
	var err error
	switch row.Kind {
	case HerdrIntentCoordinator:
		expectedID, err = HerdrCoordinatorIntentID(
			runtimeParent,
			herdrRuntimeOwnerProjectRoot(runtimeParent, ownerProjectRoot),
		)
		if row.IssueNum != 0 || row.TaskID != "" || row.Slug != "" ||
			row.BranchName != "" || row.FullBranchRef != "" ||
			row.BaseBranch != "" || row.BaseSHA != "" || row.ExpectedHead != "" ||
			row.BranchExisted || row.BranchCreated {
			return fmt.Errorf("herdr coordinator row %s contains child fields", row.ID)
		}
	case HerdrIntentWorktree:
		expectedID, err = HerdrWorktreeIntentID(parent, ownerProjectRoot, row.IssueNum, row.TaskID)
		if row.Slug == "" || row.BranchName == "" ||
			row.FullBranchRef != "refs/heads/"+row.BranchName ||
			row.BaseBranch == "" || !herdrControlCommitSHA.MatchString(row.BaseSHA) ||
			!herdrControlCommitSHA.MatchString(row.ExpectedHead) ||
			row.BranchExisted == row.BranchCreated {
			return fmt.Errorf("herdr worktree row %s is incomplete", row.ID)
		}
	default:
		return fmt.Errorf("herdr row %s has unknown kind %q", row.ID, row.Kind)
	}
	if err != nil || row.ID != expectedID {
		return fmt.Errorf("herdr row %s has inconsistent identity", row.ID)
	}
	if err := validateHerdrResource(row.Resource, row.Kind == HerdrIntentWorktree); err != nil {
		return fmt.Errorf("herdr row %s: %w", row.ID, err)
	}
	if filepath.Clean(row.Resource.CurrentPath) != filepath.Clean(row.WorktreePath) {
		return fmt.Errorf("herdr row %s resource path does not match row", row.ID)
	}
	return nil
}

func validateHerdrIntent(intent HerdrIntent) error {
	parent := parentref.Canon(strings.TrimSpace(intent.Parent))
	runtimeParent := parentref.Canon(strings.TrimSpace(intent.RuntimeParent))
	if intent.ID == "" || parent == "" || intent.Parent != parent ||
		runtimeParent == "" || intent.RuntimeParent != runtimeParent ||
		intent.Backend != backend.Herdr || intent.WorkspaceLabel == "" ||
		intent.WorktreePath == "" || intent.Session == "" || intent.SocketPath == "" ||
		intent.TimeoutMS < 3000 || intent.TimeoutMS > 300000 || intent.ExpiresUnixMS <= 0 {
		return fmt.Errorf("herdr intent %q is incomplete", intent.ID)
	}
	if err := validateHerdrRuntimeParent(parent, runtimeParent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	ownerProjectRoot, ownerErr := HerdrOwnerProjectRoot(parent, intent.OwnerProjectRoot)
	if ownerErr != nil || intent.OwnerProjectRoot != ownerProjectRoot {
		return fmt.Errorf("herdr intent %s has invalid owner project root", intent.ID)
	}
	var expectedID string
	var err error
	switch intent.Kind {
	case HerdrIntentCoordinator:
		expectedID, err = HerdrCoordinatorIntentID(
			runtimeParent,
			herdrRuntimeOwnerProjectRoot(runtimeParent, ownerProjectRoot),
		)
		if intent.IssueNum != 0 || intent.TaskID != "" || intent.BranchName != "" ||
			intent.FullBranchRef != "" || intent.BaseSHA != "" || intent.Coordinator.WorkspaceID != "" {
			return fmt.Errorf("herdr coordinator intent %s contains child fields", intent.ID)
		}
	case HerdrIntentWorktree:
		expectedID, err = HerdrWorktreeIntentID(
			parent,
			ownerProjectRoot,
			intent.IssueNum,
			intent.TaskID,
		)
		if intent.Slug == "" || intent.BranchName == "" || intent.FullBranchRef == "" ||
			intent.FullBranchRef != "refs/heads/"+intent.BranchName ||
			intent.BaseBranch == "" || !herdrControlCommitSHA.MatchString(intent.BaseSHA) ||
			!herdrControlCommitSHA.MatchString(intent.ExpectedHead) ||
			intent.Coordinator.WorkspaceID == "" {
			return fmt.Errorf("herdr worktree intent %s is incomplete", intent.ID)
		}
	default:
		return fmt.Errorf("herdr intent %s has unknown kind %q", intent.ID, intent.Kind)
	}
	if err != nil || intent.ID != expectedID {
		return fmt.Errorf("herdr intent %s has inconsistent identity", intent.ID)
	}
	switch intent.Status {
	case HerdrIntentPlanned:
		if intent.Resource != (HerdrResource{}) {
			return fmt.Errorf("herdr intent %s has a resource before realization", intent.ID)
		}
	case HerdrIntentIssued:
		if intent.Resource != (HerdrResource{}) {
			// worktree open recovery retains the previously realized resource
			// while the replacement workspace mutation is in flight.
			if intent.Kind != HerdrIntentWorktree {
				return fmt.Errorf("herdr intent %s has a resource before realization", intent.ID)
			}
			if err := validateHerdrResource(intent.Resource, true); err != nil {
				return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
			}
		}
	case HerdrIntentRealized:
		if err := validateHerdrResource(intent.Resource, intent.Kind == HerdrIntentWorktree); err != nil {
			return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
		}
	case HerdrIntentManualCleanupRequired:
		if strings.TrimSpace(intent.Failure) == "" {
			return fmt.Errorf("herdr intent %s requires a failure reason", intent.ID)
		}
	default:
		return fmt.Errorf("herdr intent %s has unknown status %q", intent.ID, intent.Status)
	}
	if intent.BranchCreated && (intent.Kind != HerdrIntentWorktree || intent.BranchExisted) {
		return fmt.Errorf("herdr intent %s has an invalid branch ownership record", intent.ID)
	}
	return nil
}

func validateHerdrRuntimeParent(parent, runtimeParent string) error {
	switch {
	case strings.HasPrefix(runtimeParent, "plan:") && runtimeParent != parent:
		return fmt.Errorf("runtime parent does not match plan parent")
	case !strings.HasPrefix(parent, "plan:") && runtimeParent != parent:
		return fmt.Errorf("runtime parent does not match parent")
	default:
		return nil
	}
}

func herdrRuntimeOwnerProjectRoot(runtimeParent, ownerProjectRoot string) string {
	if strings.HasPrefix(runtimeParent, "plan:") {
		return ownerProjectRoot
	}
	return ""
}

func reserveHerdrControlIdentity(
	reservations map[string]string,
	id, fullBranchRef, worktreePath string,
) error {
	for _, reservation := range []string{
		"branch:" + fullBranchRef,
		"path:" + filepath.Clean(worktreePath),
	} {
		if previous := reservations[reservation]; previous != "" {
			return fmt.Errorf("herdr entries %s and %s reserve the same %s", previous, id, reservation)
		}
		reservations[reservation] = id
	}
	return nil
}

func validateHerdrResource(resource HerdrResource, worktree bool) error {
	if resource.WorkspaceID == "" || resource.Label == "" || resource.PaneID == "" ||
		resource.TerminalID == "" || resource.CurrentPath == "" {
		return fmt.Errorf("herdr resource is incomplete")
	}
	if worktree && (resource.RepoKey == "" || resource.RepoRoot == "") {
		return fmt.Errorf("herdr worktree resource has incomplete Git provenance")
	}
	if !worktree && (resource.RepoKey != "" || resource.RepoRoot != "") {
		return fmt.Errorf("herdr coordinator resource unexpectedly has Git provenance")
	}
	return nil
}

func tuplePart(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}
