package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/parentref"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/atomicfs"
	"github.com/butaosuinu/fanout/internal/infra/execx"
)

const LaunchJournalSchemaVersion = 1

var (
	commitSHAPattern   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	launchNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	agentNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	ownerTokenPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type LaunchIntentKind string

const (
	IntentCoordinator LaunchIntentKind = "coordinator"
	IntentWorktree    LaunchIntentKind = "worktree"
	IntentRollback    LaunchIntentKind = "rollback"
	IntentCleanup     LaunchIntentKind = "cleanup"
	IntentResume      LaunchIntentKind = "agent-resume"
	IntentRestart     LaunchIntentKind = "server-restart"
	IntentShutdown    LaunchIntentKind = "server-shutdown"
)

type CleanupPhase string

const (
	CleanupReopen         CleanupPhase = "reopen"
	CleanupRemove         CleanupPhase = "remove"
	CleanupWorkspaceClose CleanupPhase = "workspace-close"
)

type LaunchIntentStatus string

const (
	IntentPlanned               LaunchIntentStatus = "planned"
	IntentIssued                LaunchIntentStatus = "issued"
	IntentRealized              LaunchIntentStatus = "realized"
	IntentManualCleanupRequired LaunchIntentStatus = "manual_cleanup_required"
)

// RuntimeResource is the runtime and Git identity established by one Herdr
// workspace mutation. The workspace label is the ownership nonce.
type RuntimeResource struct {
	WorkspaceID string `json:"workspaceId"`
	Label       string `json:"label"`
	PaneID      string `json:"paneId"`
	TerminalID  string `json:"terminalId"`
	CurrentPath string `json:"currentPath"`
	RepoKey     string `json:"repoKey,omitempty"`
	RepoRoot    string `json:"repoRoot,omitempty"`
}

// RuntimeServerIdentity is the persisted owner marker and supervisor lease
// identity used to fence one explicit restart or shutdown.
type RuntimeServerIdentity struct {
	GitCommonDir         string `json:"gitCommonDir"`
	RuntimeDir           string `json:"runtimeDir"`
	Session              string `json:"session"`
	SocketPath           string `json:"socketPath"`
	ClientSocketPath     string `json:"clientSocketPath"`
	OwnerNonce           string `json:"ownerNonce"`
	SupervisorPID        int    `json:"supervisorPid"`
	SupervisorStartToken string `json:"supervisorStartToken"`
	ServerPID            int    `json:"serverPid,omitempty"`
	BinaryPath           string `json:"binaryPath"`
	BinarySHA256         string `json:"binarySha256"`
	BinaryVersion        string `json:"binaryVersion"`
	LauncherPath         string `json:"launcherPath"`
	LauncherSHA256       string `json:"launcherSha256"`
}

// LaunchIntent is one minimal mutation journal row. Status records whether the
// socket request may have been issued; recovery never reissues an issued
// request and instead classifies the current workspace and checkout state.
type LaunchIntent struct {
	ID               string             `json:"id"`
	Kind             LaunchIntentKind   `json:"kind"`
	Status           LaunchIntentStatus `json:"status"`
	Parent           string             `json:"parent"`
	RuntimeParent    string             `json:"runtimeParent"`
	OwnerProjectRoot string             `json:"ownerProjectRoot,omitempty"`
	IssueNum         int                `json:"issueNum,omitempty"`
	TaskID           string             `json:"taskId,omitempty"`

	Slug          string `json:"slug,omitempty"`
	BranchName    string `json:"branchName,omitempty"`
	FullBranchRef string `json:"fullBranchRef,omitempty"`
	BaseBranch    string `json:"baseBranch,omitempty"`
	BaseSHA       string `json:"baseSha,omitempty"`
	ExpectedHead  string `json:"expectedHead,omitempty"`
	WorktreePath  string `json:"worktreePath"`
	BranchExisted bool   `json:"branchExisted,omitempty"`
	BranchCreated bool   `json:"branchCreated,omitempty"`

	WorkspaceLabel string          `json:"workspaceLabel"`
	Resource       RuntimeResource `json:"resource"`
	Coordinator    RuntimeResource `json:"coordinator"`
	Session        string          `json:"session"`
	SocketPath     string          `json:"socketPath"`
	ExpiresUnixMS  int64           `json:"expiresUnixMs"`

	Launch *LaunchCapsule         `json:"launch,omitempty"`
	Server *RuntimeServerIdentity `json:"server,omitempty"`
	// ResumeAgentSession binds a cold-restart launch to one exact persisted
	// Codex conversation. Other intent kinds must leave it empty.
	ResumeAgentSession *backend.AgentSessionRef `json:"resumeAgentSession,omitempty"`

	CleanupPhase        CleanupPhase `json:"cleanupPhase,omitempty"`
	CleanupDeleteBranch bool         `json:"cleanupDeleteBranch,omitempty"`
	// CleanupDeleteBranchRequested is nil only for intents saved before this field existed.
	CleanupDeleteBranchRequested *bool `json:"cleanupDeleteBranchRequested,omitempty"`

	Failure string `json:"failure,omitempty"`
}

// LaunchCapsule is the non-secret launch capsule recorded before a coordinator
// workspace mutation or after child-worktree realization. The environment
// itself lives in a one-shot owner-only file.
type LaunchCapsule struct {
	Nonce                string                   `json:"nonce"`
	EmitterNonce         string                   `json:"emitterNonce,omitempty"`
	PendingReportedState string                   `json:"pendingReportedState,omitempty"`
	PendingAgentSession  *backend.AgentSessionRef `json:"pendingAgentSession,omitempty"`
	Agent                string                   `json:"agent"`
	AgentName            string                   `json:"agentName"`
	Executable           string                   `json:"executable"`
	Args                 []string                 `json:"args"`
	TeamDBPath           string                   `json:"teamDbPath,omitempty"`
	CodexTeamStatusPath  string                   `json:"codexTeamStatusPath,omitempty"`
	CodexPlanStatusPath  string                   `json:"codexPlanStatusPath,omitempty"`
	EnvFilePath          string                   `json:"envFilePath"`
	EnvNameCount         int                      `json:"envNameCount"`
	LauncherReady        bool                     `json:"launcherReady,omitempty"`
	TokenIssued          bool                     `json:"tokenIssued,omitempty"`
}

// LaunchJournal is the repository-common intent journal. It holds intents
// only; a successful launch consumes the intent into the owning worktree's
// state.json pane row.
type LaunchJournal struct {
	SchemaVersion int            `json:"schemaVersion"`
	Intents       []LaunchIntent `json:"intents"`
}

// LockedLaunchJournal is a journal view backed by the combined launch lock.
// The view has no unlock of its own: the lock file is owned by the
// LockedStore that produced it.
type LockedLaunchJournal struct {
	path string
	LaunchJournal
}

// LaunchJournalPath returns the repository-common journal path shared by every
// linked worktree.
func LaunchJournalPath(projectRoot string) (string, error) {
	return RepoCommonPath(projectRoot, "herdr-intents.json")
}

// RepoCommonPath resolves one file under the repository-common fanout directory
// — the git common dir, which every linked worktree shares.
//
// State that must be one thing per repository goes here rather than in a
// worktree's own .fanout, because sibling worktrees are separate directories
// and a guard written to one of them does not exist for the others.
func RepoCommonPath(projectRoot, name string) (string, error) {
	return repoCommonPathContext(context.Background(), projectRoot, name)
}

func repoCommonPathContext(ctx context.Context, projectRoot, name string) (string, error) {
	out, err := execx.OutputContext(ctx, projectRoot, nil, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git common directory for %s: %w", name, err)
	}
	// Strip exactly the newline git appends; a path's own whitespace is data.
	commonDir := strings.TrimSuffix(string(out), "\n")
	if commonDir == "" {
		return "", fmt.Errorf("resolve git common directory for %s: invalid path %q", name, commonDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(projectRoot, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize git common directory for %s: %w", name, err)
	}
	return filepath.Join(filepath.Clean(commonDir), "fanout", name), nil
}

func LoadLaunchJournal(projectRoot string) (LaunchJournal, error) {
	path, err := LaunchJournalPath(projectRoot)
	if err != nil {
		return LaunchJournal{}, err
	}
	return loadLaunchJournal(path)
}

// LoadLaunchJournalPath reads a journal without taking its launch lock. It is
// reserved for the pane launcher, which cannot acquire the lock held by its
// parent launch operation while waiting for readiness.
func LoadLaunchJournalPath(path string) (LaunchJournal, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(path) != "herdr-intents.json" {
		return LaunchJournal{}, fmt.Errorf("invalid Herdr intents path")
	}
	return loadLaunchJournal(path)
}

func lockLaunchJournalPath(ctx context.Context, path string, blocking bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create Herdr intents directory: %w", err)
	}
	lockPath := path + ".lock"
	file, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		return nil, fmt.Errorf("open Herdr intents lock %s: %w", lockPath, openErr)
	}
	if err := lockFileExclusive(ctx, file, blocking); err != nil {
		_ = file.Close() // The flock error is authoritative.
		return nil, fmt.Errorf("lock Herdr intents %s: %w", lockPath, err)
	}
	return file, nil
}

func (l *LockedLaunchJournal) Save() error {
	if l == nil || l.path == "" {
		return fmt.Errorf("save Herdr intents without a held lock")
	}
	l.normalize()
	if validateErr := validateLaunchJournal(l.LaunchJournal); validateErr != nil {
		return validateErr
	}
	if writeErr := atomicfs.WriteJSON(l.path, l.LaunchJournal, 0o600); writeErr != nil {
		return fmt.Errorf("write Herdr intents %s: %w", l.path, writeErr)
	}
	return nil
}

func (s LaunchJournal) FindIntent(id string) (LaunchIntent, bool) {
	for _, intent := range s.Intents {
		if intent.ID == id {
			return intent, true
		}
	}
	return LaunchIntent{}, false
}

// ServerLifecycleIntent returns the single explicit server lifecycle row.
func (s LaunchJournal) ServerLifecycleIntent() (LaunchIntent, bool, error) {
	var found LaunchIntent
	hasFound := false
	for _, intent := range s.Intents {
		if !IsServerLifecycleKind(intent.Kind) {
			continue
		}
		if hasFound {
			return LaunchIntent{}, false, fmt.Errorf("multiple Herdr server lifecycle intents are pending")
		}
		found = intent
		hasFound = true
	}
	return found, hasFound, nil
}

// IsServerLifecycleKind reports whether kind fences the owned server.
func IsServerLifecycleKind(kind LaunchIntentKind) bool {
	return kind == IntentRestart || kind == IntentShutdown
}

// ServerIntentID returns the singleton ID for one server lifecycle kind.
func ServerIntentID(kind LaunchIntentKind) (string, error) {
	if !IsServerLifecycleKind(kind) {
		return "", fmt.Errorf("invalid Herdr server lifecycle intent kind %q", kind)
	}
	return "server:" + string(kind), nil
}

func ResumeIntentID(session, socketPath, workspaceID, paneID string) (string, error) {
	parts := []string{session, socketPath, workspaceID, paneID}
	if slices.Contains(parts, "") {
		return "", fmt.Errorf("herdr resume intent requires a complete route")
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "resume:" + hex.EncodeToString(digest[:]), nil
}

func (s *LaunchJournal) UpsertIntent(intent LaunchIntent) {
	for i := range s.Intents {
		if s.Intents[i].ID == intent.ID {
			s.Intents[i] = intent
			return
		}
	}
	s.Intents = append(s.Intents, intent)
}

func (s *LaunchJournal) RemoveIntent(id string) bool {
	for i := range s.Intents {
		if s.Intents[i].ID != id {
			continue
		}
		s.Intents = append(s.Intents[:i], s.Intents[i+1:]...)
		return true
	}
	return false
}

func (s LaunchJournal) ProvisionalBindings(
	ownerProjectRoot string,
) []backend.Binding {
	bindings := make([]backend.Binding, 0, len(s.Intents))
	for _, intent := range s.Intents {
		if IsServerLifecycleKind(intent.Kind) {
			continue
		}
		parent, ok := bindingParent(
			intent.RuntimeParent,
			intent.IssueNum,
			intent.OwnerProjectRoot,
			ownerProjectRoot,
		)
		if !ok {
			continue
		}
		bindings = append(bindings, backend.Binding{Parent: parent, Backend: backend.Herdr})
	}
	return bindings
}

func bindingParent(
	parent string,
	issueNum int,
	storedRoot, projectRoot string,
) (string, bool) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if parent == "@manual" || parent == "@console" {
		return "", false
	}
	if parent == "@watch" {
		if issueNum <= 0 {
			return "", false
		}
		return strconv.Itoa(issueNum), true
	}
	if !strings.HasPrefix(parent, "plan:") {
		return parent, true
	}
	return parent, storedRoot == filepath.Clean(projectRoot)
}

func IntentOwnerProjectRoot(parent, projectRoot string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if parent != "@manual" && !strings.HasPrefix(parent, "plan:") {
		return "", nil
	}
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return "", fmt.Errorf("herdr scoped owner project root must be a canonical absolute path")
	}
	return projectRoot, nil
}

func CoordinatorIntentID(parent, ownerProjectRoot string, issueNum int) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if parent == "" {
		return "", fmt.Errorf("herdr coordinator intent requires a parent")
	}
	switch parent {
	case "@manual":
		if issueNum >= 0 {
			return "", fmt.Errorf("manual Herdr coordinator requires a negative synthetic issue number")
		}
	case "@watch":
		if issueNum <= 0 {
			return "", fmt.Errorf("watch Herdr coordinator requires a positive issue number")
		}
	default:
		if issueNum != 0 {
			return "", fmt.Errorf("non-synthetic Herdr coordinator cannot use a synthetic issue number")
		}
	}
	ownerProjectRoot, err := IntentOwnerProjectRoot(parent, ownerProjectRoot)
	if err != nil {
		return "", err
	}
	id := "coordinator:" + intentOwnerTuple(parent, ownerProjectRoot)
	if issueNum != 0 {
		id += ":" + strconv.Itoa(issueNum)
	}
	return id, nil
}

// WorktreeIntentID uses the same issue/task key as the tmux state store:
// (parent, issue number) or (plan parent, task id).
func WorktreeIntentID(parent, ownerProjectRoot string, issueNum int, taskID string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	taskID = strings.TrimSpace(taskID)
	ownerProjectRoot, err := IntentOwnerProjectRoot(parent, ownerProjectRoot)
	if err != nil {
		return "", err
	}
	owner := intentOwnerTuple(parent, ownerProjectRoot)
	switch {
	case parent == "":
		return "", fmt.Errorf("herdr worktree intent requires a parent")
	case taskID != "" && issueNum == 0:
		return "task:" + owner + ":" + tuplePart(taskID), nil
	case taskID == "" && (issueNum > 0 || parent == "@manual" && issueNum < 0):
		return "issue:" + owner + ":" + strconv.Itoa(issueNum), nil
	default:
		return "", fmt.Errorf("herdr worktree intent requires exactly one issue number or task id")
	}
}

// RollbackIntentID binds a one-shot launch rollback to the worktree
// intent whose realized resource it removes.
func RollbackIntentID(worktreeIntentID string) (string, error) {
	worktreeIntentID = strings.TrimSpace(worktreeIntentID)
	if worktreeIntentID == "" || strings.HasPrefix(worktreeIntentID, "rollback:") {
		return "", fmt.Errorf("herdr rollback intent requires a worktree intent id")
	}
	return "rollback:" + worktreeIntentID, nil
}

// CleanupIntentID binds lifecycle cleanup to the worktree row it removes.
func CleanupIntentID(worktreeIntentID string) (string, error) {
	worktreeIntentID = strings.TrimSpace(worktreeIntentID)
	if worktreeIntentID == "" || strings.HasPrefix(worktreeIntentID, "cleanup:") {
		return "", fmt.Errorf("herdr cleanup intent requires a worktree intent id")
	}
	return "cleanup:" + worktreeIntentID, nil
}

func intentOwnerTuple(parent, ownerProjectRoot string) string {
	identity := tuplePart(parent)
	if ownerProjectRoot != "" {
		identity += ":" + tuplePart(ownerProjectRoot)
	}
	return identity
}

func loadLaunchJournal(path string) (LaunchJournal, error) {
	// Intents decodes through a pointer so a truncated journal (missing or
	// null intents) is distinguishable from a valid empty list.
	var raw struct {
		SchemaVersion int             `json:"schemaVersion"`
		Intents       *[]LaunchIntent `json:"intents"`
	}
	found, err := atomicfs.ReadJSON(path, &raw)
	if err != nil {
		if found {
			return LaunchJournal{}, fmt.Errorf("parse Herdr intents %s: %w", path, err)
		}
		return LaunchJournal{}, fmt.Errorf("read Herdr intents %s: %w", path, err)
	}
	if !found {
		return emptyLaunchJournal(), nil
	}
	// Only a missing file starts a fresh v1 journal. An existing file missing
	// its schema version or intents array is corrupt and must not be adopted
	// as empty ownership.
	if raw.SchemaVersion != LaunchJournalSchemaVersion {
		return LaunchJournal{}, fmt.Errorf(
			"validate Herdr intents %s: unsupported Herdr intents schema version %d", path, raw.SchemaVersion,
		)
	}
	if raw.Intents == nil {
		return LaunchJournal{}, fmt.Errorf("validate Herdr intents %s: journal is missing intents", path)
	}
	store := LaunchJournal{SchemaVersion: raw.SchemaVersion, Intents: *raw.Intents}
	if err := validateLaunchJournal(store); err != nil {
		return LaunchJournal{}, fmt.Errorf("validate Herdr intents %s: %w", path, err)
	}
	return store, nil
}

func emptyLaunchJournal() LaunchJournal {
	return LaunchJournal{
		SchemaVersion: LaunchJournalSchemaVersion,
		Intents:       []LaunchIntent{},
	}
}

func (s *LaunchJournal) normalize() {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = LaunchJournalSchemaVersion
	}
	if s.Intents == nil {
		s.Intents = []LaunchIntent{}
	}
}

func validateLaunchJournal(store LaunchJournal) error {
	if store.SchemaVersion != LaunchJournalSchemaVersion {
		return fmt.Errorf("unsupported Herdr intents schema version %d", store.SchemaVersion)
	}
	if _, _, err := store.ServerLifecycleIntent(); err != nil {
		return err
	}
	ids := map[string]bool{}
	reservations := map[string]string{}
	for _, intent := range store.Intents {
		if err := validateIntent(intent); err != nil {
			return err
		}
		if ids[intent.ID] {
			return fmt.Errorf("duplicate Herdr intent id %q", intent.ID)
		}
		ids[intent.ID] = true
		if intent.Kind != IntentWorktree {
			continue
		}
		if err := reserveIntentIdentity(
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

func validateIntent(intent LaunchIntent) error {
	if IsServerLifecycleKind(intent.Kind) {
		return validateServerIntent(intent)
	}
	if intent.Server != nil {
		return fmt.Errorf("herdr intent %s has an unrelated server identity", intent.ID)
	}
	if intent.Kind != IntentResume && intent.ResumeAgentSession != nil {
		return fmt.Errorf("herdr intent %s has an unrelated resume session", intent.ID)
	}
	if err := validateIntentIdentity(intent); err != nil {
		return err
	}
	if err := validateIntentKind(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	if err := validateIntentStatus(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	if err := validateIntentOwnership(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	return nil
}

func validateServerIntent(intent LaunchIntent) error {
	wantID, err := ServerIntentID(intent.Kind)
	if err != nil {
		return err
	}
	validStatus := intent.Status == IntentPlanned ||
		intent.Kind == IntentShutdown && intent.Status == IntentIssued
	if intent.ID != wantID || !validStatus || intent.Server == nil {
		return fmt.Errorf("herdr server lifecycle intent %q is incomplete", intent.ID)
	}
	empty := []bool{
		intent.Parent == "", intent.RuntimeParent == "", intent.OwnerProjectRoot == "",
		intent.IssueNum == 0, intent.TaskID == "", intent.Slug == "", intent.BranchName == "",
		intent.FullBranchRef == "", intent.BaseBranch == "", intent.BaseSHA == "",
		intent.ExpectedHead == "", intent.WorktreePath == "", !intent.BranchExisted,
		!intent.BranchCreated, intent.WorkspaceLabel == "", intent.Resource == (RuntimeResource{}),
		intent.Coordinator == (RuntimeResource{}), intent.Session == "", intent.SocketPath == "",
		intent.ExpiresUnixMS == 0, intent.Launch == nil, intent.CleanupPhase == "",
		intent.ResumeAgentSession == nil, !intent.CleanupDeleteBranch,
		intent.CleanupDeleteBranchRequested == nil, intent.Failure == "",
	}
	if slices.Contains(empty, false) {
		return fmt.Errorf("herdr server lifecycle intent %s has unrelated fields", intent.ID)
	}
	return validateServerIdentity(intent.Kind, *intent.Server)
}

func validateServerIdentity(kind LaunchIntentKind, identity RuntimeServerIdentity) error {
	paths := []string{
		identity.GitCommonDir, identity.RuntimeDir, identity.SocketPath, identity.ClientSocketPath,
		identity.BinaryPath, identity.LauncherPath,
	}
	for _, path := range paths {
		if !cleanAbsolute(path) {
			return fmt.Errorf("herdr server lifecycle identity has an invalid path")
		}
	}
	required := []bool{
		identity.Session != "", ownerTokenPattern.MatchString(identity.OwnerNonce),
		identity.SupervisorPID > 1, ownerTokenPattern.MatchString(identity.SupervisorStartToken),
		ownerTokenPattern.MatchString(identity.BinarySHA256), identity.BinaryVersion != "",
		ownerTokenPattern.MatchString(identity.LauncherSHA256),
		identity.ServerPID == 0 || identity.ServerPID > 1,
		kind != IntentRestart || identity.ServerPID > 1,
	}
	if slices.Contains(required, false) {
		return fmt.Errorf("herdr server lifecycle identity is incomplete")
	}
	return nil
}

func validateIntentIdentity(intent LaunchIntent) error {
	parent := parentref.Canon(strings.TrimSpace(intent.Parent))
	runtimeParent := parentref.Canon(strings.TrimSpace(intent.RuntimeParent))
	requirements := []bool{
		intent.ID != "", parent != "", intent.Parent == parent,
		runtimeParent != "", intent.RuntimeParent == runtimeParent,
		intent.WorkspaceLabel != "", intent.WorktreePath != "",
		intent.Session != "", intent.SocketPath != "", intent.ExpiresUnixMS > 0,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("herdr intent %q is incomplete", intent.ID)
	}
	if err := validateRuntimeParent(intent, parent, runtimeParent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	ownerProjectRoot, ownerErr := IntentOwnerProjectRoot(parent, intent.OwnerProjectRoot)
	if ownerErr != nil || intent.OwnerProjectRoot != ownerProjectRoot {
		return fmt.Errorf("herdr intent %s has invalid owner project root", intent.ID)
	}
	return nil
}

func validateIntentKind(intent LaunchIntent) error {
	validators := map[LaunchIntentKind]func(LaunchIntent) error{
		IntentCoordinator: validateCoordinatorFields,
		IntentWorktree:    validateWorktreeFields,
		IntentRollback:    validateRollbackFields,
		IntentCleanup:     validateCleanupFields,
		IntentResume:      validateResumeFields,
	}
	validate, ok := validators[intent.Kind]
	if !ok {
		return fmt.Errorf("unknown kind %q", intent.Kind)
	}
	return validate(intent)
}

func validateCoordinatorFields(intent LaunchIntent) error {
	return requireIntentFields(intent.Kind, []bool{
		intent.TaskID == "", intent.BranchName == "", intent.FullBranchRef == "",
		intent.BaseSHA == "", intent.Coordinator.WorkspaceID == "",
	})
}

func validateWorktreeFields(intent LaunchIntent) error {
	return requireIntentFields(intent.Kind, []bool{
		intent.Slug != "", intent.BranchName != "", intent.FullBranchRef != "",
		intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.BaseBranch != "", commitSHAPattern.MatchString(intent.BaseSHA),
		commitSHAPattern.MatchString(intent.ExpectedHead), intent.Coordinator.WorkspaceID != "",
	})
}

func validateRollbackFields(intent LaunchIntent) error {
	return requireIntentFields(intent.Kind, []bool{
		strings.HasPrefix(intent.ID, "rollback:"), intent.Launch == nil,
		intent.Slug != "", intent.BranchName != "", intent.FullBranchRef != "",
		intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.BaseBranch != "", commitSHAPattern.MatchString(intent.BaseSHA),
		commitSHAPattern.MatchString(intent.ExpectedHead), intent.Coordinator.WorkspaceID != "",
	})
}

func validateCleanupFields(intent LaunchIntent) error {
	validPhases := map[CleanupPhase]bool{
		CleanupReopen:         true,
		CleanupRemove:         true,
		CleanupWorkspaceClose: true,
	}
	if err := requireIntentFields(intent.Kind, []bool{
		strings.HasPrefix(intent.ID, "cleanup:"), intent.Launch == nil,
		intent.BranchName != "", intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.Resource.WorkspaceID != "", intent.Resource.CurrentPath != "",
		validPhases[intent.CleanupPhase],
		intent.CleanupPhase != CleanupReopen || intent.Coordinator.WorkspaceID != "",
		!intent.CleanupDeleteBranch || commitSHAPattern.MatchString(intent.ExpectedHead),
	}); err != nil {
		return err
	}
	if intent.CleanupPhase == CleanupReopen {
		return validateRuntimeResource(intent.Coordinator, false)
	}
	return nil
}

func validateResumeFields(intent LaunchIntent) error {
	ref := intent.ResumeAgentSession
	expectedID, _ := ResumeIntentID(
		intent.Session, intent.SocketPath, intent.Resource.WorkspaceID, intent.Resource.PaneID,
	)
	return requireIntentFields(intent.Kind, []bool{
		intent.ID == expectedID,
		validResumeLaunch(intent.Launch, ref),
		intent.Resource.WorkspaceID != "", intent.Resource.PaneID != "",
		intent.Resource.TerminalID != "", intent.Resource.CurrentPath != "",
		intent.Coordinator == (RuntimeResource{}), intent.CleanupPhase == "",
		!intent.CleanupDeleteBranch, intent.CleanupDeleteBranchRequested == nil, intent.Server == nil,
	})
}

func validResumeLaunch(launch *LaunchCapsule, ref *backend.AgentSessionRef) bool {
	return validCodexSessionRef(ref) && launch != nil && launch.Agent == "codex" &&
		launch.AgentName == "" && len(launch.Args) == 2 && launch.Args[0] == "resume" &&
		launch.Args[1] == ref.Value
}

func validCodexSessionRef(ref *backend.AgentSessionRef) bool {
	return ref != nil && ref.Valid() && ref.Source == "herdr:codex" &&
		ref.Agent == "codex" && ref.Kind == "id"
}

func requireIntentFields(kind LaunchIntentKind, requirements []bool) error {
	if slices.Contains(requirements, false) {
		return fmt.Errorf("%s fields are incomplete", kind)
	}
	return nil
}

func validateIntentStatus(intent LaunchIntent) error {
	validators := map[LaunchIntentStatus]func(LaunchIntent) error{
		IntentPlanned:               validatePlanned,
		IntentIssued:                validateIssued,
		IntentRealized:              validateRealized,
		IntentManualCleanupRequired: validateManual,
	}
	validate, ok := validators[intent.Status]
	if !ok {
		return fmt.Errorf("has unknown status %q", intent.Status)
	}
	return validate(intent)
}

func validatePlanned(intent LaunchIntent) error {
	if intent.Kind == IntentRollback || intent.Kind == IntentCleanup {
		return validateRuntimeResource(intent.Resource, true)
	}
	if intent.Resource != (RuntimeResource{}) {
		return fmt.Errorf("has a resource before realization")
	}
	return nil
}

func validateIssued(intent LaunchIntent) error {
	if intent.Resource == (RuntimeResource{}) {
		return nil
	}
	// Worktree open recovery retains the realized resource while the
	// replacement workspace mutation is in flight.
	if intent.Kind != IntentWorktree && intent.Kind != IntentRollback && intent.Kind != IntentCleanup {
		return fmt.Errorf("has a resource before realization")
	}
	return validateRuntimeResource(intent.Resource, true)
}

func validateRealized(intent LaunchIntent) error {
	return validateRuntimeResource(intent.Resource, intent.Kind != IntentCoordinator)
}

func validateManual(intent LaunchIntent) error {
	if strings.TrimSpace(intent.Failure) == "" {
		return fmt.Errorf("requires a failure reason")
	}
	return nil
}

func validateIntentOwnership(intent LaunchIntent) error {
	branchKinds := map[LaunchIntentKind]bool{
		IntentWorktree: true,
		IntentRollback: true,
		IntentCleanup:  true,
	}
	if intent.BranchCreated && (!branchKinds[intent.Kind] || intent.BranchExisted) {
		return fmt.Errorf("has an invalid branch ownership record")
	}
	if intent.Resource != (RuntimeResource{}) &&
		filepath.Clean(intent.Resource.CurrentPath) != filepath.Clean(intent.WorktreePath) {
		return fmt.Errorf("resource current path does not match worktree path")
	}
	if err := validateLaunch(intent); err != nil {
		return err
	}
	return nil
}

func validateLaunch(intent LaunchIntent) error {
	launch := intent.Launch
	if launch == nil {
		return nil
	}
	if !launchAllowed(intent.Kind, intent.Status) {
		return fmt.Errorf("launch fields require an issued coordinator or realized worktree")
	}
	requirements := []bool{
		launchNoncePattern.MatchString(launch.Nonce),
		cleanAbsolute(launch.Executable),
		validCodexPaths(launch),
		cleanAbsolute(launch.EnvFilePath),
		launch.EnvNameCount > 0,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("launch fields are incomplete")
	}
	if err := validateLaunchArgs(launch.Args); err != nil {
		return err
	}
	if err := validateLaunchAgentIdentity(intent.Kind, launch); err != nil {
		return err
	}
	if err := validateEmitter(launch); err != nil {
		return err
	}
	if launch.TokenIssued && !launch.LauncherReady {
		return fmt.Errorf("launch token was issued before launcher readiness")
	}
	return nil
}

func validateLaunchAgentIdentity(kind LaunchIntentKind, launch *LaunchCapsule) error {
	if kind == IntentResume && launch.Agent == "codex" && launch.AgentName == "" {
		return nil
	}
	if (launch.Agent == "") != (launch.AgentName == "") {
		return fmt.Errorf("launch agent identity is partial")
	}
	if launch.AgentName != "" && !agentNamePattern.MatchString(launch.AgentName) {
		return fmt.Errorf("launch agent name is invalid")
	}
	return nil
}

func validateEmitter(launch *LaunchCapsule) error {
	if launch.EmitterNonce == "" {
		if launch.PendingReportedState != "" {
			return fmt.Errorf("pending telemetry requires an emitter nonce")
		}
		return nil
	}
	if !validEmitterAgent(launch) || !telemetry.ValidNonce(launch.EmitterNonce) {
		return fmt.Errorf("emitter fields require a Claude or Codex Plan launch and valid nonce")
	}
	if launch.PendingReportedState == "" {
		return nil
	}
	state, ok := backend.ParseAgentState(launch.PendingReportedState)
	if !ok || state == backend.AgentRunning {
		return fmt.Errorf("pending telemetry has an invalid provider state")
	}
	return nil
}

func validEmitterAgent(launch *LaunchCapsule) bool {
	return launch.Agent == "claude" || launch.Agent == "codex" && launch.CodexPlanStatusPath != ""
}

func validCodexPaths(launch *LaunchCapsule) bool {
	checks := []bool{
		launch.TeamDBPath == "" || cleanAbsolute(launch.TeamDBPath),
		launch.CodexTeamStatusPath == "" ||
			(launch.TeamDBPath != "" && cleanAbsolute(launch.CodexTeamStatusPath)),
		launch.CodexPlanStatusPath == "" || cleanAbsolute(launch.CodexPlanStatusPath),
		launch.CodexTeamStatusPath == "" || launch.CodexPlanStatusPath == "",
		launch.CodexTeamStatusPath == "" || launch.Agent == "codex",
		launch.CodexPlanStatusPath == "" || launch.Agent == "codex",
	}
	return !slices.Contains(checks, false)
}

func validateLaunchArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("launch argv contains NUL")
		}
	}
	return nil
}

func launchAllowed(kind LaunchIntentKind, status LaunchIntentStatus) bool {
	switch kind {
	case IntentWorktree:
		return status == IntentRealized || status == IntentManualCleanupRequired
	case IntentCoordinator:
		return status == IntentIssued || status == IntentRealized ||
			status == IntentManualCleanupRequired
	case IntentResume:
		return status == IntentRealized
	default:
		return false
	}
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func validateRuntimeParent(intent LaunchIntent, parent, runtimeParent string) error {
	switch {
	case strings.HasPrefix(runtimeParent, "plan:") && runtimeParent != parent:
		if validGenericRuntimeParent(intent, parent, runtimeParent) {
			return nil
		}
		return fmt.Errorf("runtime parent does not match plan parent")
	case !strings.HasPrefix(parent, "plan:") && runtimeParent != parent:
		if validGenericRuntimeParent(intent, parent, runtimeParent) {
			return nil
		}
		return fmt.Errorf("runtime parent does not match parent")
	default:
		return nil
	}
}

func validGenericRuntimeParent(intent LaunchIntent, parent, runtimeParent string) bool {
	validKind := intent.Kind == IntentCoordinator || intent.Kind == IntentResume
	if !validKind || parent != "@manual" || intent.IssueNum >= 0 {
		return false
	}
	_, ok := bindingParent(
		runtimeParent, intent.IssueNum, intent.OwnerProjectRoot, intent.OwnerProjectRoot,
	)
	return ok
}

func reserveIntentIdentity(
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

func validateRuntimeResource(resource RuntimeResource, worktree bool) error {
	if runtimeResourceIncomplete(resource) {
		return fmt.Errorf("herdr resource is incomplete")
	}
	hasProvenance := resource.RepoKey != "" && resource.RepoRoot != ""
	if worktree && !hasProvenance {
		return fmt.Errorf("herdr worktree resource has incomplete Git provenance")
	}
	if !worktree && (resource.RepoKey != "" || resource.RepoRoot != "") {
		return fmt.Errorf("herdr coordinator resource unexpectedly has Git provenance")
	}
	return nil
}

// runtimeResourceIncomplete reports a resource missing any identity component
// every runtime row must carry, regardless of worktree/coordinator kind.
func runtimeResourceIncomplete(resource RuntimeResource) bool {
	return resource.WorkspaceID == "" || resource.Label == "" || resource.PaneID == "" ||
		resource.TerminalID == "" || resource.CurrentPath == ""
}

func tuplePart(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}
