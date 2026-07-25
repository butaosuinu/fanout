package state

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
)

var herdrSHA256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	HerdrControlSchemaID = "fanout.herdr-control.v1"

	HerdrOperationActive                HerdrOperationState = "active"
	HerdrOperationManualCleanupRequired HerdrOperationState = "manual_cleanup_required"
	HerdrOperationLaunchAborted         HerdrOperationState = "launch-aborted"

	HerdrPhaseBranchPlanned     HerdrLaunchPhase = "branch-planned"
	HerdrPhaseBranchStarting    HerdrLaunchPhase = "branch-starting"
	HerdrPhaseWorktreePlanned   HerdrLaunchPhase = "worktree-planned"
	HerdrPhaseWorktreeStarting  HerdrLaunchPhase = "worktree-starting"
	HerdrPhaseWorktreeRealized  HerdrLaunchPhase = "worktree-realized"
	HerdrPhaseWorktreeReady     HerdrLaunchPhase = "worktree-ready"
	HerdrPhaseWorkspacePlanned  HerdrLaunchPhase = "workspace-planned"
	HerdrPhaseWorkspaceStarting HerdrLaunchPhase = "workspace-starting"
	HerdrPhaseWorkspaceRealized HerdrLaunchPhase = "workspace-realized"
	HerdrPhaseWorkspaceReady    HerdrLaunchPhase = "workspace-ready"

	HerdrMutationWorkspaceCreate HerdrMutationKind = "workspace-create"
	HerdrMutationWorktreeCreate  HerdrMutationKind = "worktree-create"
	HerdrMutationWorktreeOpen    HerdrMutationKind = "worktree-open"
)

type HerdrOperationState string

type HerdrLaunchPhase string

type HerdrMutationKind string

// HerdrControlStore is the repository-wide Herdr launch authority shared by
// every linked worktree through the physical git common directory. Final pane
// rows remain in state.json until the later row-migration wave; provisional
// launch ownership must not be split across those worktree-local files.
type HerdrControlStore struct {
	SchemaID string               `json:"schema_id"`
	Revision uint64               `json:"revision"`
	Intents  []HerdrLaunchIntent  `json:"intents"`
	Lineages []HerdrBranchLineage `json:"branch_lineages"`
}

// HerdrLaunchIntent records enough exact request and pre-state evidence to
// decide whether a crashed mutation completed. A starting phase is never
// retried; exact post-state can reconcile it to realized, while ambiguous
// state fails closed.
type HerdrLaunchIntent struct {
	IntentID       string              `json:"intent_id"`
	Parent         string              `json:"parent"`
	IssueNum       int                 `json:"issue_num,omitempty"`
	TaskID         string              `json:"task_id,omitempty"`
	Backend        backend.Name        `json:"backend"`
	Operation      string              `json:"operation"`
	OperationState HerdrOperationState `json:"operation_state"`
	Phase          HerdrLaunchPhase    `json:"phase"`

	SourceRootPhysical string `json:"source_root_physical"`
	PlanSpecIdentity   string `json:"plan_spec_identity,omitempty"`
	Slug               string `json:"slug"`
	BranchName         string `json:"branch_name"`
	FullBranchRef      string `json:"full_branch_ref"`
	WorktreePath       string `json:"worktree_path"`
	LineageID          string `json:"lineage_id"`

	ResolvedBaseRef     string `json:"resolved_base_ref"`
	ResolvedBaseName    string `json:"resolved_base_name"`
	EffectiveBaseBranch string `json:"effective_base_branch"`
	PRBaseName          string `json:"pr_base_name,omitempty"`
	LineageBaseSHA      string `json:"lineage_base_sha"`
	LaunchHeadSHA       string `json:"launch_head_sha"`

	HerdrSession           string `json:"herdr_session"`
	HerdrSocketPath        string `json:"herdr_socket_path"`
	HerdrRepoKey           string `json:"herdr_repo_key,omitempty"`
	HerdrRepoRoot          string `json:"herdr_repo_root,omitempty"`
	WorktreeOwnershipNonce string `json:"worktree_ownership_nonce"`
	LaunchNonce            string `json:"launch_nonce"`
	TotalTimeoutMS         int64  `json:"total_timeout_ms"`
	LaunchStartedUnixMS    int64  `json:"launch_started_unix_ms"`
	LaunchExpiresUnixMS    int64  `json:"launch_expires_unix_ms"`

	Coordinator *HerdrCoordinatorBinding `json:"coordinator,omitempty"`

	BranchRequest  *HerdrBranchRequest `json:"branch_request,omitempty"`
	BranchPreState *HerdrGitPreState   `json:"branch_pre_state,omitempty"`
	BranchReceipt  *HerdrBranchReceipt `json:"branch_receipt,omitempty"`

	MutationRequest  *HerdrMutationRequest  `json:"mutation_request,omitempty"`
	MutationPreState *HerdrMutationPreState `json:"mutation_pre_state,omitempty"`
	MutationReceipt  *HerdrMutationReceipt  `json:"mutation_receipt,omitempty"`

	FailureReason string `json:"failure_reason,omitempty"`
}

type HerdrBranchLineage struct {
	LineageID           string `json:"lineage_id"`
	IntentID            string `json:"intent_id"`
	Parent              string `json:"parent"`
	IssueNum            int    `json:"issue_num,omitempty"`
	TaskID              string `json:"task_id,omitempty"`
	FullBranchRef       string `json:"full_branch_ref"`
	WorktreePath        string `json:"worktree_path"`
	ResolvedBaseRef     string `json:"resolved_base_ref"`
	ResolvedBaseName    string `json:"resolved_base_name"`
	EffectiveBaseBranch string `json:"effective_base_branch"`
	PRBaseName          string `json:"pr_base_name,omitempty"`
	LineageBaseSHA      string `json:"lineage_base_sha"`
	LastOwnedHeadSHA    string `json:"last_owned_head_sha"`
	State               string `json:"state"`
}

type HerdrCoordinatorBinding struct {
	IntentID       string `json:"intent_id"`
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceLabel string `json:"workspace_label"`
	PaneID         string `json:"pane_id"`
	TerminalID     string `json:"terminal_id"`
	CWD            string `json:"cwd"`
}

type HerdrBranchRequest struct {
	FullRef string `json:"full_ref"`
	NewOID  string `json:"new_oid"`
	OldOID  string `json:"old_oid"`
}

type HerdrGitPreState struct {
	BranchAbsent       bool   `json:"branch_absent"`
	ObservedBranchOID  string `json:"observed_branch_oid,omitempty"`
	PathAbsent         bool   `json:"path_absent"`
	CheckoutRegistered bool   `json:"checkout_registered"`
	ObservedHeadSHA    string `json:"observed_head_sha,omitempty"`
}

type HerdrBranchReceipt struct {
	FullRef string `json:"full_ref"`
	NewOID  string `json:"new_oid"`
	OldOID  string `json:"old_oid"`
}

type HerdrMutationRequest struct {
	Kind                      HerdrMutationKind `json:"kind"`
	WorkspaceID               string            `json:"workspace_id,omitempty"`
	CoordinatorWorkspaceLabel string            `json:"coordinator_workspace_label,omitempty"`
	CoordinatorPaneID         string            `json:"coordinator_pane_id,omitempty"`
	CoordinatorTerminalID     string            `json:"coordinator_terminal_id,omitempty"`
	CoordinatorWorkspaceCWD   string            `json:"coordinator_workspace_cwd,omitempty"`
	ExpectedRepoKey           string            `json:"expected_repo_key,omitempty"`
	ExpectedRepoRoot          string            `json:"expected_repo_root,omitempty"`
	CWD                       string            `json:"cwd,omitempty"`
	Branch                    string            `json:"branch,omitempty"`
	Base                      string            `json:"base,omitempty"`
	Path                      string            `json:"path,omitempty"`
	Label                     string            `json:"label"`
	NoFocus                   bool              `json:"no_focus"`
}

type HerdrWorkspaceBinding struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Path        string `json:"path,omitempty"`
	RepoKey     string `json:"repo_key,omitempty"`
	RepoRoot    string `json:"repo_root,omitempty"`
}

type HerdrMutationPreState struct {
	Workspaces               []HerdrWorkspaceBinding `json:"workspaces"`
	ExpectedAlreadyOpenID    string                  `json:"expected_already_open_id,omitempty"`
	ExpectedAlreadyOpenLabel string                  `json:"expected_already_open_label,omitempty"`
	Git                      HerdrGitPreState        `json:"git"`
}

type HerdrMutationReceipt struct {
	WorkspaceID      string `json:"workspace_id"`
	WorkspaceLabel   string `json:"workspace_label"`
	PaneID           string `json:"pane_id"`
	TerminalID       string `json:"terminal_id"`
	CWD              string `json:"cwd"`
	Path             string `json:"path,omitempty"`
	RepoKey          string `json:"repo_key,omitempty"`
	RepoRoot         string `json:"repo_root,omitempty"`
	AlreadyOpen      bool   `json:"already_open,omitempty"`
	GitDirMarkerPath string `json:"git_dir_marker_path,omitempty"`
}

type LockedHerdrControl struct {
	path string
	file *os.File
	HerdrControlStore
}

func HerdrControlPath(projectRoot string) (string, error) {
	commonDir, err := gitCommonDir(projectRoot)
	if err != nil {
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
	path, err := HerdrControlPath(projectRoot)
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	if ensureErr := ensureHerdrControlDir(parent); ensureErr != nil {
		return nil, ensureErr
	}
	lockPath := path + ".lock"
	f, err := openHerdrControlLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("open herdr control lock %s: %w", lockPath, err)
	}
	if lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); lockErr != nil {
		// The lock error is the actionable failure; the descriptor was never
		// admitted as a held registry lock.
		_ = f.Close()
		return nil, fmt.Errorf("lock herdr control %s: %w", lockPath, lockErr)
	}
	store, err := loadHerdrControl(path)
	if err != nil {
		// Loading failed before the handle escaped. Preserve that error while
		// unwinding the private lock handle.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, err
	}
	return &LockedHerdrControl{path: path, file: f, HerdrControlStore: store}, nil
}

func (l *LockedHerdrControl) Unlock() error {
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

func (l *LockedHerdrControl) Save() error {
	if l == nil || l.file == nil {
		return fmt.Errorf("herdr control store is not locked")
	}
	l.normalize()
	if err := validateHerdrControlStore(l.HerdrControlStore); err != nil {
		return fmt.Errorf("validate herdr control before save: %w", err)
	}
	l.Revision++
	if err := atomicfs.WriteJSON(l.path, l.HerdrControlStore, 0o600); err != nil {
		l.Revision--
		return fmt.Errorf("write herdr control %s: %w", l.path, err)
	}
	return nil
}

func (s HerdrControlStore) FindIntent(intentID string) (HerdrLaunchIntent, bool) {
	for _, intent := range s.Intents {
		if intent.IntentID == intentID {
			return intent, true
		}
	}
	return HerdrLaunchIntent{}, false
}

func (s HerdrControlStore) FindIntentFor(parent string, issueNum int, taskID string) (HerdrLaunchIntent, bool) {
	parent = parentref.Canon(parent)
	for _, intent := range s.Intents {
		if parentref.Canon(intent.Parent) != parent {
			continue
		}
		if taskID != "" && intent.TaskID == taskID {
			return intent, true
		}
		if taskID == "" && issueNum > 0 && intent.IssueNum == issueNum {
			return intent, true
		}
	}
	return HerdrLaunchIntent{}, false
}

func (s *HerdrControlStore) UpsertIntent(intent HerdrLaunchIntent) {
	s.normalize()
	for i := range s.Intents {
		if s.Intents[i].IntentID == intent.IntentID {
			s.Intents[i] = intent
			return
		}
	}
	s.Intents = append(s.Intents, intent)
}

func (s *HerdrControlStore) UpsertLineage(lineage HerdrBranchLineage) {
	s.normalize()
	for i := range s.Lineages {
		if s.Lineages[i].LineageID == lineage.LineageID {
			s.Lineages[i] = lineage
			return
		}
	}
	s.Lineages = append(s.Lineages, lineage)
}

type HerdrBindingScope struct {
	SourceRootPhysical string
	PlanSpecIdentity   string
}

func (s HerdrControlStore) ProvisionalBindings(scope HerdrBindingScope) []backend.Binding {
	out := make([]backend.Binding, 0, len(s.Intents))
	for _, intent := range s.Intents {
		if intent.OperationState != HerdrOperationActive &&
			intent.OperationState != HerdrOperationManualCleanupRequired {
			continue
		}
		if strings.TrimSpace(intent.Parent) == "" {
			continue
		}
		if strings.HasPrefix(parentref.Canon(intent.Parent), "plan:") &&
			(intent.SourceRootPhysical != scope.SourceRootPhysical ||
				intent.PlanSpecIdentity != scope.PlanSpecIdentity) {
			continue
		}
		out = append(out, backend.Binding{Parent: intent.Parent, Backend: intent.Backend})
	}
	return out
}

func HerdrIntentID(
	parent string,
	issueNum int,
	taskID, sourceRootPhysical, planSpecIdentity string,
) (string, error) {
	parent = parentref.Canon(parent)
	switch {
	case parent == "":
		return "", fmt.Errorf("herdr intent requires a parent")
	case taskID != "":
		if strings.TrimSpace(sourceRootPhysical) == "" || !herdrSHA256Hex.MatchString(planSpecIdentity) {
			return "", fmt.Errorf("herdr task intent requires source root and lowercase SHA-256 planspec identity")
		}
		return "task:" +
			tuplePart(parent) + ":" +
			tuplePart(sourceRootPhysical) + ":" +
			planSpecIdentity + ":" +
			tuplePart(taskID), nil
	case issueNum > 0:
		return "issue:" + strconv.Itoa(len(parent)) + ":" + parent + ":" + strconv.Itoa(issueNum), nil
	default:
		return "", fmt.Errorf("herdr intent requires an issue number or task id")
	}
}

func HerdrCoordinatorIntentID(parent, sourceRootPhysical, planSpecIdentity string) (string, error) {
	parent = parentref.Canon(parent)
	if parent == "" {
		return "", fmt.Errorf("herdr coordinator requires a parent")
	}
	if !strings.HasPrefix(parent, "plan:") {
		return "coordinator-parent:" + tuplePart(parent), nil
	}
	if strings.TrimSpace(sourceRootPhysical) == "" || !herdrSHA256Hex.MatchString(planSpecIdentity) {
		return "", fmt.Errorf("herdr plan coordinator requires source root and lowercase SHA-256 planspec identity")
	}
	return "coordinator-plan:" +
		tuplePart(parent) + ":" +
		tuplePart(sourceRootPhysical) + ":" +
		planSpecIdentity, nil
}

func tuplePart(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}

func loadHerdrControl(path string) (HerdrControlStore, error) {
	if err := validateExistingHerdrControlDir(filepath.Dir(path)); err != nil {
		return HerdrControlStore{}, err
	}
	if err := validateHerdrControlFile(path); err != nil {
		return HerdrControlStore{}, err
	}
	store := emptyHerdrControl()
	found, err := atomicfs.ReadJSON(path, &store)
	if err != nil {
		if found {
			return HerdrControlStore{}, fmt.Errorf("parse herdr control %s: %w", path, err)
		}
		return HerdrControlStore{}, fmt.Errorf("read herdr control %s: %w", path, err)
	}
	store.normalize()
	if store.SchemaID != HerdrControlSchemaID {
		return HerdrControlStore{}, fmt.Errorf("herdr control %s has unsupported schema %q", path, store.SchemaID)
	}
	if err := validateHerdrControlStore(store); err != nil {
		return HerdrControlStore{}, fmt.Errorf("validate herdr control %s: %w", path, err)
	}
	return store, nil
}

func ensureHerdrControlDir(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if mkdirErr := os.Mkdir(path, 0o700); mkdirErr != nil {
			return fmt.Errorf("create herdr control directory: %w", mkdirErr)
		}
		info, err = os.Lstat(path)
	case err != nil:
		return fmt.Errorf("inspect herdr control directory: %w", err)
	}
	if err != nil {
		return fmt.Errorf("inspect created herdr control directory: %w", err)
	}
	return validateHerdrControlDirInfo(path, info)
}

func validateExistingHerdrControlDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect herdr control directory: %w", err)
	}
	return validateHerdrControlDirInfo(path, info)
}

func validateHerdrControlDirInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("herdr control directory %s must be a non-symlink directory with mode 0700", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("herdr control directory %s is not owned by the current user", path)
	}
	return nil
}

func openHerdrControlLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		// NewFile supplied no handle that could own fd, so close the raw fd.
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("create lock file handle")
	}
	info, err := f.Stat()
	if err != nil {
		// Stat is the actionable admission failure; closing only unwinds fd.
		_ = f.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		// The identity mismatch is the actionable failure; closing only unwinds.
		_ = f.Close()
		return nil, fmt.Errorf("lock must be a current-user 0600 regular file with one link")
	}
	return f, nil
}

func validateHerdrControlFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect herdr control %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 || int(stat.Uid) != os.Getuid() || stat.Nlink != 1 {
		return fmt.Errorf("herdr control %s must be a current-user 0600 regular file with one link", path)
	}
	return nil
}

func emptyHerdrControl() HerdrControlStore {
	return HerdrControlStore{
		SchemaID: HerdrControlSchemaID,
		Intents:  []HerdrLaunchIntent{},
		Lineages: []HerdrBranchLineage{},
	}
}

func (s *HerdrControlStore) normalize() {
	if s.SchemaID == "" {
		s.SchemaID = HerdrControlSchemaID
	}
	if s.Intents == nil {
		s.Intents = []HerdrLaunchIntent{}
	}
	if s.Lineages == nil {
		s.Lineages = []HerdrBranchLineage{}
	}
	slices.SortFunc(s.Intents, func(a, b HerdrLaunchIntent) int {
		return strings.Compare(a.IntentID, b.IntentID)
	})
	slices.SortFunc(s.Lineages, func(a, b HerdrBranchLineage) int {
		return strings.Compare(a.LineageID, b.LineageID)
	})
}

func validateHerdrControlStore(store HerdrControlStore) error {
	intentIDs := make(map[string]bool, len(store.Intents))
	for _, intent := range store.Intents {
		if intent.IntentID == "" || intentIDs[intent.IntentID] {
			return fmt.Errorf("intent ids must be non-empty and unique")
		}
		intentIDs[intent.IntentID] = true
		if intent.Backend != backend.Herdr {
			return fmt.Errorf("intent %s has backend %q, want herdr", intent.IntentID, intent.Backend)
		}
		if strings.TrimSpace(intent.SourceRootPhysical) == "" {
			return fmt.Errorf("intent %s has no physical source root", intent.IntentID)
		}
		if (intent.TaskID != "" || strings.HasPrefix(parentref.Canon(intent.Parent), "plan:")) &&
			!herdrSHA256Hex.MatchString(intent.PlanSpecIdentity) {
			return fmt.Errorf("intent %s has no lowercase SHA-256 planspec identity", intent.IntentID)
		}
		switch intent.OperationState {
		case HerdrOperationActive, HerdrOperationManualCleanupRequired, HerdrOperationLaunchAborted:
		default:
			return fmt.Errorf("intent %s has unknown operation state %q", intent.IntentID, intent.OperationState)
		}
		switch intent.Phase {
		case HerdrPhaseBranchPlanned,
			HerdrPhaseBranchStarting,
			HerdrPhaseWorktreePlanned,
			HerdrPhaseWorktreeStarting,
			HerdrPhaseWorktreeRealized,
			HerdrPhaseWorkspacePlanned,
			HerdrPhaseWorkspaceStarting,
			HerdrPhaseWorkspaceRealized:
		case HerdrPhaseWorktreeReady, HerdrPhaseWorkspaceReady:
			return fmt.Errorf("intent %s phase %q is deferred to launcher readiness issue #528", intent.IntentID, intent.Phase)
		default:
			return fmt.Errorf("intent %s has unknown phase %q", intent.IntentID, intent.Phase)
		}
	}
	lineageIDs := make(map[string]bool, len(store.Lineages))
	for _, lineage := range store.Lineages {
		if lineage.LineageID == "" || lineageIDs[lineage.LineageID] {
			return fmt.Errorf("branch lineage ids must be non-empty and unique")
		}
		lineageIDs[lineage.LineageID] = true
		if lineage.IntentID == "" || lineage.FullBranchRef == "" || lineage.WorktreePath == "" {
			return fmt.Errorf("branch lineage %s is incomplete", lineage.LineageID)
		}
	}
	return nil
}

func gitCommonDir(projectRoot string) (string, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return "", fmt.Errorf("resolve git common directory: project root is empty")
	}
	cmd := exec.Command("git", "-C", projectRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git common directory: %w", err)
	}
	path := filepath.Clean(strings.TrimSpace(string(out)))
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("resolve git common directory: git returned %q", strings.TrimSpace(string(out)))
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize git common directory: %w", err)
	}
	return filepath.Clean(resolved), nil
}
