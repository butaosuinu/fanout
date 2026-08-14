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

const HerdrIntentsSchemaVersion = 1

var (
	herdrCommitSHA   = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	herdrLaunchNonce = regexp.MustCompile(`^[0-9a-f]{32}$`)
	herdrAgentName   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	herdrOwnerToken  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type HerdrIntentKind string

const (
	HerdrIntentCoordinator HerdrIntentKind = "coordinator"
	HerdrIntentWorktree    HerdrIntentKind = "worktree"
	HerdrIntentRollback    HerdrIntentKind = "rollback"
	HerdrIntentCleanup     HerdrIntentKind = "cleanup"
	HerdrIntentResume      HerdrIntentKind = "agent-resume"
	HerdrIntentRestart     HerdrIntentKind = "server-restart"
	HerdrIntentShutdown    HerdrIntentKind = "server-shutdown"
)

type HerdrCleanupPhase string

const (
	HerdrCleanupReopen         HerdrCleanupPhase = "reopen"
	HerdrCleanupRemove         HerdrCleanupPhase = "remove"
	HerdrCleanupWorkspaceClose HerdrCleanupPhase = "workspace-close"
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

// HerdrServerIdentity is the persisted owner marker and supervisor lease
// identity used to fence one explicit restart or shutdown.
type HerdrServerIdentity struct {
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
	ExpiresUnixMS  int64         `json:"expiresUnixMs"`

	Launch *HerdrLaunch         `json:"launch,omitempty"`
	Server *HerdrServerIdentity `json:"server,omitempty"`
	// ResumeAgentSession binds a cold-restart launch to one exact persisted
	// Codex conversation. Other intent kinds must leave it empty.
	ResumeAgentSession *backend.AgentSessionRef `json:"resumeAgentSession,omitempty"`

	CleanupPhase        HerdrCleanupPhase `json:"cleanupPhase,omitempty"`
	CleanupDeleteBranch bool              `json:"cleanupDeleteBranch,omitempty"`

	Failure string `json:"failure,omitempty"`
}

// HerdrLaunch is the non-secret launch capsule recorded before a coordinator
// workspace mutation or after child-worktree realization. The environment
// itself lives in a one-shot owner-only file.
type HerdrLaunch struct {
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

// HerdrIntents is the repository-common intent journal. It holds intents
// only; a successful launch consumes the intent into the owning worktree's
// state.json pane row.
type HerdrIntents struct {
	SchemaVersion int           `json:"schemaVersion"`
	Intents       []HerdrIntent `json:"intents"`
}

// LockedHerdrIntents is a journal view backed by the combined launch lock.
// The view has no unlock of its own: the lock file is owned by the
// LockedStore that produced it.
type LockedHerdrIntents struct {
	path string
	HerdrIntents
}

// HerdrIntentsPath returns the repository-common journal path shared by every
// linked worktree.
func HerdrIntentsPath(projectRoot string) (string, error) {
	return herdrIntentsPathContext(context.Background(), projectRoot)
}

func herdrIntentsPathContext(ctx context.Context, projectRoot string) (string, error) {
	out, err := execx.OutputContext(ctx, projectRoot, nil, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve Herdr intents git common directory: %w", err)
	}
	// Strip exactly the newline git appends; a path's own whitespace is data.
	commonDir := strings.TrimSuffix(string(out), "\n")
	if commonDir == "" {
		return "", fmt.Errorf("resolve Herdr intents git common directory: invalid path %q", commonDir)
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(projectRoot, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(commonDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize Herdr intents git common directory: %w", err)
	}
	return filepath.Join(filepath.Clean(commonDir), "fanout", "herdr-intents.json"), nil
}

func LoadHerdrIntents(projectRoot string) (HerdrIntents, error) {
	path, err := HerdrIntentsPath(projectRoot)
	if err != nil {
		return HerdrIntents{}, err
	}
	return loadHerdrIntents(path)
}

// LoadHerdrIntentsPath reads a journal without taking its launch lock. It is
// reserved for the pane launcher, which cannot acquire the lock held by its
// parent launch operation while waiting for readiness.
func LoadHerdrIntentsPath(path string) (HerdrIntents, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		filepath.Base(path) != "herdr-intents.json" {
		return HerdrIntents{}, fmt.Errorf("invalid Herdr intents path")
	}
	return loadHerdrIntents(path)
}

func lockHerdrIntentsPath(ctx context.Context, path string, blocking bool) (*os.File, error) {
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

func (l *LockedHerdrIntents) Save() error {
	if l == nil || l.path == "" {
		return fmt.Errorf("save Herdr intents without a held lock")
	}
	l.normalize()
	if validateErr := validateHerdrIntents(l.HerdrIntents); validateErr != nil {
		return validateErr
	}
	if writeErr := atomicfs.WriteJSON(l.path, l.HerdrIntents, 0o600); writeErr != nil {
		return fmt.Errorf("write Herdr intents %s: %w", l.path, writeErr)
	}
	return nil
}

func (s HerdrIntents) FindIntent(id string) (HerdrIntent, bool) {
	for _, intent := range s.Intents {
		if intent.ID == id {
			return intent, true
		}
	}
	return HerdrIntent{}, false
}

// ServerLifecycleIntent returns the single explicit server lifecycle row.
func (s HerdrIntents) ServerLifecycleIntent() (HerdrIntent, bool, error) {
	var found HerdrIntent
	hasFound := false
	for _, intent := range s.Intents {
		if !IsHerdrServerLifecycleKind(intent.Kind) {
			continue
		}
		if hasFound {
			return HerdrIntent{}, false, fmt.Errorf("multiple Herdr server lifecycle intents are pending")
		}
		found = intent
		hasFound = true
	}
	return found, hasFound, nil
}

// IsHerdrServerLifecycleKind reports whether kind fences the owned server.
func IsHerdrServerLifecycleKind(kind HerdrIntentKind) bool {
	return kind == HerdrIntentRestart || kind == HerdrIntentShutdown
}

// HerdrServerIntentID returns the singleton ID for one server lifecycle kind.
func HerdrServerIntentID(kind HerdrIntentKind) (string, error) {
	if !IsHerdrServerLifecycleKind(kind) {
		return "", fmt.Errorf("invalid Herdr server lifecycle intent kind %q", kind)
	}
	return "server:" + string(kind), nil
}

func HerdrResumeIntentID(session, socketPath, workspaceID, paneID string) (string, error) {
	parts := []string{session, socketPath, workspaceID, paneID}
	if slices.Contains(parts, "") {
		return "", fmt.Errorf("herdr resume intent requires a complete route")
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "resume:" + hex.EncodeToString(digest[:]), nil
}

func (s *HerdrIntents) UpsertIntent(intent HerdrIntent) {
	for i := range s.Intents {
		if s.Intents[i].ID == intent.ID {
			s.Intents[i] = intent
			return
		}
	}
	s.Intents = append(s.Intents, intent)
}

func (s *HerdrIntents) RemoveIntent(id string) bool {
	for i := range s.Intents {
		if s.Intents[i].ID != id {
			continue
		}
		s.Intents = append(s.Intents[:i], s.Intents[i+1:]...)
		return true
	}
	return false
}

func (s HerdrIntents) ProvisionalBindings(
	ownerProjectRoot string,
) []backend.Binding {
	bindings := make([]backend.Binding, 0, len(s.Intents))
	for _, intent := range s.Intents {
		if IsHerdrServerLifecycleKind(intent.Kind) {
			continue
		}
		parent, ok := herdrBindingParent(
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

func herdrBindingParent(
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

func HerdrOwnerProjectRoot(parent, projectRoot string) (string, error) {
	parent = parentref.Canon(strings.TrimSpace(parent))
	if parent != "@manual" && !strings.HasPrefix(parent, "plan:") {
		return "", nil
	}
	if projectRoot == "" || !filepath.IsAbs(projectRoot) || filepath.Clean(projectRoot) != projectRoot {
		return "", fmt.Errorf("herdr scoped owner project root must be a canonical absolute path")
	}
	return projectRoot, nil
}

func HerdrCoordinatorIntentID(parent, ownerProjectRoot string, issueNum int) (string, error) {
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
	ownerProjectRoot, err := HerdrOwnerProjectRoot(parent, ownerProjectRoot)
	if err != nil {
		return "", err
	}
	id := "coordinator:" + herdrOwnerTuple(parent, ownerProjectRoot)
	if issueNum != 0 {
		id += ":" + strconv.Itoa(issueNum)
	}
	return id, nil
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
	case taskID == "" && (issueNum > 0 || parent == "@manual" && issueNum < 0):
		return "issue:" + owner + ":" + strconv.Itoa(issueNum), nil
	default:
		return "", fmt.Errorf("herdr worktree intent requires exactly one issue number or task id")
	}
}

// HerdrRollbackIntentID binds a one-shot launch rollback to the worktree
// intent whose realized resource it removes.
func HerdrRollbackIntentID(worktreeIntentID string) (string, error) {
	worktreeIntentID = strings.TrimSpace(worktreeIntentID)
	if worktreeIntentID == "" || strings.HasPrefix(worktreeIntentID, "rollback:") {
		return "", fmt.Errorf("herdr rollback intent requires a worktree intent id")
	}
	return "rollback:" + worktreeIntentID, nil
}

// HerdrCleanupIntentID binds lifecycle cleanup to the worktree row it removes.
func HerdrCleanupIntentID(worktreeIntentID string) (string, error) {
	worktreeIntentID = strings.TrimSpace(worktreeIntentID)
	if worktreeIntentID == "" || strings.HasPrefix(worktreeIntentID, "cleanup:") {
		return "", fmt.Errorf("herdr cleanup intent requires a worktree intent id")
	}
	return "cleanup:" + worktreeIntentID, nil
}

func herdrOwnerTuple(parent, ownerProjectRoot string) string {
	identity := tuplePart(parent)
	if ownerProjectRoot != "" {
		identity += ":" + tuplePart(ownerProjectRoot)
	}
	return identity
}

func loadHerdrIntents(path string) (HerdrIntents, error) {
	// Intents decodes through a pointer so a truncated journal (missing or
	// null intents) is distinguishable from a valid empty list.
	var raw struct {
		SchemaVersion int            `json:"schemaVersion"`
		Intents       *[]HerdrIntent `json:"intents"`
	}
	found, err := atomicfs.ReadJSON(path, &raw)
	if err != nil {
		if found {
			return HerdrIntents{}, fmt.Errorf("parse Herdr intents %s: %w", path, err)
		}
		return HerdrIntents{}, fmt.Errorf("read Herdr intents %s: %w", path, err)
	}
	if !found {
		return emptyHerdrIntents(), nil
	}
	// Only a missing file starts a fresh v1 journal. An existing file missing
	// its schema version or intents array is corrupt and must not be adopted
	// as empty ownership.
	if raw.SchemaVersion != HerdrIntentsSchemaVersion {
		return HerdrIntents{}, fmt.Errorf(
			"validate Herdr intents %s: unsupported Herdr intents schema version %d", path, raw.SchemaVersion,
		)
	}
	if raw.Intents == nil {
		return HerdrIntents{}, fmt.Errorf("validate Herdr intents %s: journal is missing intents", path)
	}
	store := HerdrIntents{SchemaVersion: raw.SchemaVersion, Intents: *raw.Intents}
	if err := validateHerdrIntents(store); err != nil {
		return HerdrIntents{}, fmt.Errorf("validate Herdr intents %s: %w", path, err)
	}
	return store, nil
}

func emptyHerdrIntents() HerdrIntents {
	return HerdrIntents{
		SchemaVersion: HerdrIntentsSchemaVersion,
		Intents:       []HerdrIntent{},
	}
}

func (s *HerdrIntents) normalize() {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = HerdrIntentsSchemaVersion
	}
	if s.Intents == nil {
		s.Intents = []HerdrIntent{}
	}
}

func validateHerdrIntents(store HerdrIntents) error {
	if store.SchemaVersion != HerdrIntentsSchemaVersion {
		return fmt.Errorf("unsupported Herdr intents schema version %d", store.SchemaVersion)
	}
	if _, _, err := store.ServerLifecycleIntent(); err != nil {
		return err
	}
	ids := map[string]bool{}
	reservations := map[string]string{}
	for _, intent := range store.Intents {
		if err := validateHerdrIntent(intent); err != nil {
			return err
		}
		if ids[intent.ID] {
			return fmt.Errorf("duplicate Herdr intent id %q", intent.ID)
		}
		ids[intent.ID] = true
		if intent.Kind != HerdrIntentWorktree {
			continue
		}
		if err := reserveHerdrIntentIdentity(
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

func validateHerdrIntent(intent HerdrIntent) error {
	if IsHerdrServerLifecycleKind(intent.Kind) {
		return validateHerdrServerIntent(intent)
	}
	if intent.Server != nil {
		return fmt.Errorf("herdr intent %s has an unrelated server identity", intent.ID)
	}
	if intent.Kind != HerdrIntentResume && intent.ResumeAgentSession != nil {
		return fmt.Errorf("herdr intent %s has an unrelated resume session", intent.ID)
	}
	if err := validateHerdrIntentIdentity(intent); err != nil {
		return err
	}
	if err := validateHerdrIntentKind(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	if err := validateHerdrIntentStatus(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	if err := validateHerdrIntentOwnership(intent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	return nil
}

func validateHerdrServerIntent(intent HerdrIntent) error {
	wantID, err := HerdrServerIntentID(intent.Kind)
	if err != nil {
		return err
	}
	validStatus := intent.Status == HerdrIntentPlanned ||
		intent.Kind == HerdrIntentShutdown && intent.Status == HerdrIntentIssued
	if intent.ID != wantID || !validStatus || intent.Server == nil {
		return fmt.Errorf("herdr server lifecycle intent %q is incomplete", intent.ID)
	}
	empty := []bool{
		intent.Parent == "", intent.RuntimeParent == "", intent.OwnerProjectRoot == "",
		intent.IssueNum == 0, intent.TaskID == "", intent.Slug == "", intent.BranchName == "",
		intent.FullBranchRef == "", intent.BaseBranch == "", intent.BaseSHA == "",
		intent.ExpectedHead == "", intent.WorktreePath == "", !intent.BranchExisted,
		!intent.BranchCreated, intent.WorkspaceLabel == "", intent.Resource == (HerdrResource{}),
		intent.Coordinator == (HerdrResource{}), intent.Session == "", intent.SocketPath == "",
		intent.ExpiresUnixMS == 0, intent.Launch == nil, intent.CleanupPhase == "",
		intent.ResumeAgentSession == nil, !intent.CleanupDeleteBranch, intent.Failure == "",
	}
	if slices.Contains(empty, false) {
		return fmt.Errorf("herdr server lifecycle intent %s has unrelated fields", intent.ID)
	}
	return validateHerdrServerIdentity(intent.Kind, *intent.Server)
}

func validateHerdrServerIdentity(kind HerdrIntentKind, identity HerdrServerIdentity) error {
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
		identity.Session != "", herdrOwnerToken.MatchString(identity.OwnerNonce),
		identity.SupervisorPID > 1, herdrOwnerToken.MatchString(identity.SupervisorStartToken),
		herdrOwnerToken.MatchString(identity.BinarySHA256), identity.BinaryVersion != "",
		herdrOwnerToken.MatchString(identity.LauncherSHA256),
		identity.ServerPID == 0 || identity.ServerPID > 1,
		kind != HerdrIntentRestart || identity.ServerPID > 1,
	}
	if slices.Contains(required, false) {
		return fmt.Errorf("herdr server lifecycle identity is incomplete")
	}
	return nil
}

func validateHerdrIntentIdentity(intent HerdrIntent) error {
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
	if err := validateHerdrRuntimeParent(intent, parent, runtimeParent); err != nil {
		return fmt.Errorf("herdr intent %s: %w", intent.ID, err)
	}
	ownerProjectRoot, ownerErr := HerdrOwnerProjectRoot(parent, intent.OwnerProjectRoot)
	if ownerErr != nil || intent.OwnerProjectRoot != ownerProjectRoot {
		return fmt.Errorf("herdr intent %s has invalid owner project root", intent.ID)
	}
	return nil
}

func validateHerdrIntentKind(intent HerdrIntent) error {
	validators := map[HerdrIntentKind]func(HerdrIntent) error{
		HerdrIntentCoordinator: validateHerdrCoordinatorFields,
		HerdrIntentWorktree:    validateHerdrWorktreeFields,
		HerdrIntentRollback:    validateHerdrRollbackFields,
		HerdrIntentCleanup:     validateHerdrCleanupFields,
		HerdrIntentResume:      validateHerdrResumeFields,
	}
	validate, ok := validators[intent.Kind]
	if !ok {
		return fmt.Errorf("unknown kind %q", intent.Kind)
	}
	return validate(intent)
}

func validateHerdrCoordinatorFields(intent HerdrIntent) error {
	return requireHerdrIntentFields(intent.Kind, []bool{
		intent.TaskID == "", intent.BranchName == "", intent.FullBranchRef == "",
		intent.BaseSHA == "", intent.Coordinator.WorkspaceID == "",
	})
}

func validateHerdrWorktreeFields(intent HerdrIntent) error {
	return requireHerdrIntentFields(intent.Kind, []bool{
		intent.Slug != "", intent.BranchName != "", intent.FullBranchRef != "",
		intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.BaseBranch != "", herdrCommitSHA.MatchString(intent.BaseSHA),
		herdrCommitSHA.MatchString(intent.ExpectedHead), intent.Coordinator.WorkspaceID != "",
	})
}

func validateHerdrRollbackFields(intent HerdrIntent) error {
	return requireHerdrIntentFields(intent.Kind, []bool{
		strings.HasPrefix(intent.ID, "rollback:"), intent.Launch == nil,
		intent.Slug != "", intent.BranchName != "", intent.FullBranchRef != "",
		intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.BaseBranch != "", herdrCommitSHA.MatchString(intent.BaseSHA),
		herdrCommitSHA.MatchString(intent.ExpectedHead), intent.Coordinator.WorkspaceID != "",
	})
}

func validateHerdrCleanupFields(intent HerdrIntent) error {
	validPhases := map[HerdrCleanupPhase]bool{
		HerdrCleanupReopen:         true,
		HerdrCleanupRemove:         true,
		HerdrCleanupWorkspaceClose: true,
	}
	if err := requireHerdrIntentFields(intent.Kind, []bool{
		strings.HasPrefix(intent.ID, "cleanup:"), intent.Launch == nil,
		intent.BranchName != "", intent.FullBranchRef == "refs/heads/"+intent.BranchName,
		intent.Resource.WorkspaceID != "", intent.Resource.CurrentPath != "",
		validPhases[intent.CleanupPhase],
		intent.CleanupPhase != HerdrCleanupReopen || intent.Coordinator.WorkspaceID != "",
		!intent.CleanupDeleteBranch || herdrCommitSHA.MatchString(intent.ExpectedHead),
	}); err != nil {
		return err
	}
	if intent.CleanupPhase == HerdrCleanupReopen {
		return validateHerdrResource(intent.Coordinator, false)
	}
	return nil
}

func validateHerdrResumeFields(intent HerdrIntent) error {
	ref := intent.ResumeAgentSession
	expectedID, _ := HerdrResumeIntentID(
		intent.Session, intent.SocketPath, intent.Resource.WorkspaceID, intent.Resource.PaneID,
	)
	return requireHerdrIntentFields(intent.Kind, []bool{
		intent.ID == expectedID,
		validHerdrResumeLaunch(intent.Launch, ref),
		intent.Resource.WorkspaceID != "", intent.Resource.PaneID != "",
		intent.Resource.TerminalID != "", intent.Resource.CurrentPath != "",
		intent.Coordinator == (HerdrResource{}), intent.CleanupPhase == "",
		!intent.CleanupDeleteBranch, intent.Server == nil,
	})
}

func validHerdrResumeLaunch(launch *HerdrLaunch, ref *backend.AgentSessionRef) bool {
	return validHerdrCodexSessionRef(ref) && launch != nil && launch.Agent == "codex" &&
		launch.AgentName == "" && len(launch.Args) == 2 && launch.Args[0] == "resume" &&
		launch.Args[1] == ref.Value
}

func validHerdrCodexSessionRef(ref *backend.AgentSessionRef) bool {
	return ref != nil && ref.Valid() && ref.Source == "herdr:codex" &&
		ref.Agent == "codex" && ref.Kind == "id"
}

func requireHerdrIntentFields(kind HerdrIntentKind, requirements []bool) error {
	if slices.Contains(requirements, false) {
		return fmt.Errorf("%s fields are incomplete", kind)
	}
	return nil
}

func validateHerdrIntentStatus(intent HerdrIntent) error {
	validators := map[HerdrIntentStatus]func(HerdrIntent) error{
		HerdrIntentPlanned:               validateHerdrPlanned,
		HerdrIntentIssued:                validateHerdrIssued,
		HerdrIntentRealized:              validateHerdrRealized,
		HerdrIntentManualCleanupRequired: validateHerdrManual,
	}
	validate, ok := validators[intent.Status]
	if !ok {
		return fmt.Errorf("has unknown status %q", intent.Status)
	}
	return validate(intent)
}

func validateHerdrPlanned(intent HerdrIntent) error {
	if intent.Kind == HerdrIntentRollback || intent.Kind == HerdrIntentCleanup {
		return validateHerdrResource(intent.Resource, true)
	}
	if intent.Resource != (HerdrResource{}) {
		return fmt.Errorf("has a resource before realization")
	}
	return nil
}

func validateHerdrIssued(intent HerdrIntent) error {
	if intent.Resource == (HerdrResource{}) {
		return nil
	}
	// Worktree open recovery retains the realized resource while the
	// replacement workspace mutation is in flight.
	if intent.Kind != HerdrIntentWorktree && intent.Kind != HerdrIntentRollback && intent.Kind != HerdrIntentCleanup {
		return fmt.Errorf("has a resource before realization")
	}
	return validateHerdrResource(intent.Resource, true)
}

func validateHerdrRealized(intent HerdrIntent) error {
	return validateHerdrResource(intent.Resource, intent.Kind != HerdrIntentCoordinator)
}

func validateHerdrManual(intent HerdrIntent) error {
	if strings.TrimSpace(intent.Failure) == "" {
		return fmt.Errorf("requires a failure reason")
	}
	return nil
}

func validateHerdrIntentOwnership(intent HerdrIntent) error {
	branchKinds := map[HerdrIntentKind]bool{
		HerdrIntentWorktree: true,
		HerdrIntentRollback: true,
		HerdrIntentCleanup:  true,
	}
	if intent.BranchCreated && (!branchKinds[intent.Kind] || intent.BranchExisted) {
		return fmt.Errorf("has an invalid branch ownership record")
	}
	if intent.Resource != (HerdrResource{}) &&
		filepath.Clean(intent.Resource.CurrentPath) != filepath.Clean(intent.WorktreePath) {
		return fmt.Errorf("resource current path does not match worktree path")
	}
	if err := validateHerdrLaunch(intent); err != nil {
		return err
	}
	return nil
}

func validateHerdrLaunch(intent HerdrIntent) error {
	launch := intent.Launch
	if launch == nil {
		return nil
	}
	if !herdrLaunchAllowed(intent.Kind, intent.Status) {
		return fmt.Errorf("launch fields require an issued coordinator or realized worktree")
	}
	requirements := []bool{
		herdrLaunchNonce.MatchString(launch.Nonce),
		cleanAbsolute(launch.Executable),
		validHerdrCodexPaths(launch),
		cleanAbsolute(launch.EnvFilePath),
		launch.EnvNameCount > 0,
	}
	if slices.Contains(requirements, false) {
		return fmt.Errorf("launch fields are incomplete")
	}
	if err := validateHerdrLaunchArgs(launch.Args); err != nil {
		return err
	}
	if err := validateHerdrLaunchAgentIdentity(intent.Kind, launch); err != nil {
		return err
	}
	if err := validateHerdrEmitter(launch); err != nil {
		return err
	}
	if launch.TokenIssued && !launch.LauncherReady {
		return fmt.Errorf("launch token was issued before launcher readiness")
	}
	return nil
}

func validateHerdrLaunchAgentIdentity(kind HerdrIntentKind, launch *HerdrLaunch) error {
	if kind == HerdrIntentResume && launch.Agent == "codex" && launch.AgentName == "" {
		return nil
	}
	if (launch.Agent == "") != (launch.AgentName == "") {
		return fmt.Errorf("launch agent identity is partial")
	}
	if launch.AgentName != "" && !herdrAgentName.MatchString(launch.AgentName) {
		return fmt.Errorf("launch agent name is invalid")
	}
	return nil
}

func validateHerdrEmitter(launch *HerdrLaunch) error {
	if launch.EmitterNonce == "" {
		if launch.PendingReportedState != "" {
			return fmt.Errorf("pending telemetry requires an emitter nonce")
		}
		return nil
	}
	if !validHerdrEmitterAgent(launch) || !telemetry.ValidNonce(launch.EmitterNonce) {
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

func validHerdrEmitterAgent(launch *HerdrLaunch) bool {
	return launch.Agent == "claude" || launch.Agent == "codex" && launch.CodexPlanStatusPath != ""
}

func validHerdrCodexPaths(launch *HerdrLaunch) bool {
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

func validateHerdrLaunchArgs(args []string) error {
	for _, arg := range args {
		if strings.ContainsRune(arg, '\x00') {
			return fmt.Errorf("launch argv contains NUL")
		}
	}
	return nil
}

func herdrLaunchAllowed(kind HerdrIntentKind, status HerdrIntentStatus) bool {
	switch kind {
	case HerdrIntentWorktree:
		return status == HerdrIntentRealized || status == HerdrIntentManualCleanupRequired
	case HerdrIntentCoordinator:
		return status == HerdrIntentIssued || status == HerdrIntentRealized ||
			status == HerdrIntentManualCleanupRequired
	case HerdrIntentResume:
		return status == HerdrIntentRealized
	default:
		return false
	}
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, '\x00')
}

func validateHerdrRuntimeParent(intent HerdrIntent, parent, runtimeParent string) error {
	switch {
	case strings.HasPrefix(runtimeParent, "plan:") && runtimeParent != parent:
		if validGenericHerdrRuntimeParent(intent, parent, runtimeParent) {
			return nil
		}
		return fmt.Errorf("runtime parent does not match plan parent")
	case !strings.HasPrefix(parent, "plan:") && runtimeParent != parent:
		if validGenericHerdrRuntimeParent(intent, parent, runtimeParent) {
			return nil
		}
		return fmt.Errorf("runtime parent does not match parent")
	default:
		return nil
	}
}

func validGenericHerdrRuntimeParent(intent HerdrIntent, parent, runtimeParent string) bool {
	validKind := intent.Kind == HerdrIntentCoordinator || intent.Kind == HerdrIntentResume
	if !validKind || parent != "@manual" || intent.IssueNum >= 0 {
		return false
	}
	_, ok := herdrBindingParent(
		runtimeParent, intent.IssueNum, intent.OwnerProjectRoot, intent.OwnerProjectRoot,
	)
	return ok
}

func reserveHerdrIntentIdentity(
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
