package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// ErrRetryableRead marks a read-only Herdr CLI timeout or non-zero exit.
// Callers may retry only these failures within their existing launch budget.
var ErrRetryableRead = errors.New("retryable herdr read command failure")

type WorktreeMutationKind string

const (
	WorkspaceCreate WorktreeMutationKind = "workspace-create"
	WorktreeCreate  WorktreeMutationKind = "worktree-create"
	WorktreeOpen    WorktreeMutationKind = "worktree-open"
)

type WorktreeMutationRequest struct {
	Kind                      WorktreeMutationKind
	WorkspaceID               string
	CoordinatorWorkspaceLabel string
	CoordinatorPaneID         string
	CoordinatorTerminalID     string
	CoordinatorWorkspaceCWD   string
	ExpectedRepoKey           string
	ExpectedRepoRoot          string
	SourceRootPhysical        string
	SourceGitDirPhysical      string
	SourceGitDirDevice        uint64
	SourceGitDirInode         uint64
	SourceRepoKey             string
	ProjectRoot               string
	ResolvedBaseRef           string
	FullBranchRef             string
	CWD                       string
	Branch                    string
	Base                      string
	Path                      string
	Label                     string
	NoFocus                   bool
}

type WorkspaceObservation struct {
	WorkspaceID string
	Label       string
	Path        string
	RepoKey     string
	RepoRoot    string
	Pane        corebackend.PaneRef
	TerminalID  string
	CWD         string
}

type WorktreeMutationResult struct {
	WorkspaceObservation
	AlreadyOpen bool
}

type worktreeMutationEnvelope struct {
	ID     string                  `json:"id"`
	Result *worktreeMutationResult `json:"result"`
}

type worktreeMutationResult struct {
	Type        string        `json:"type"`
	Workspace   workspaceJSON `json:"workspace"`
	AlreadyOpen *bool         `json:"already_open"`
}

type pluginListEnvelope struct {
	ID     string            `json:"id"`
	Result *pluginListResult `json:"result"`
}

type pluginListResult struct {
	Type    string             `json:"type"`
	Plugins *[]json.RawMessage `json:"plugins"`
}

// VerifyWorktreeSetupPolicy enforces the measured tmux-parity tier: the owned
// control environment must expose an empty plugin registry before a workspace
// or worktree mutation. A non-empty or undecodable registry fails closed.
func (s *OwnedSession) VerifyWorktreeSetupPolicy(ctx context.Context) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return markRetryableRead(err)
	}
	return s.backend.verifyEmptyPluginRegistry(ctx, probed)
}

func (b *Backend) verifyEmptyPluginRegistry(ctx context.Context, probed probeResult) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "plugin", "list", "--json")
	if err != nil {
		return classifyReadCommandError("plugin.list", err)
	}
	return validateEmptyPluginList(out)
}

func validateEmptyPluginList(data []byte) error {
	var envelope pluginListEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return methodUnavailable("plugin.list")
	}
	if envelope.ID != "cli:plugin:list" || envelope.Result == nil ||
		envelope.Result.Type != "plugin_list" || envelope.Result.Plugins == nil {
		return methodUnavailable("plugin.list")
	}
	if len(*envelope.Result.Plugins) != 0 {
		return fmt.Errorf("herdr owned plugin registry is not empty; setup hooks are not admitted")
	}
	return nil
}

// ObserveWorkspaces returns the fully validated owned-session snapshot used as
// the per-step pre-state for workspace/worktree mutations.
func (s *OwnedSession) ObserveWorkspaces(ctx context.Context) ([]WorkspaceObservation, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	return s.backend.observeOwnedWorkspaces(ctx, admission)
}

// MutateWorktree issues one exact workspace/worktree request. The caller owns
// the persistent phase transition and no-blind-retry rule; this adapter only
// accepts a complete response and a matching fresh owned-session snapshot.
func (s *OwnedSession) MutateWorktree(ctx context.Context, req WorktreeMutationRequest) (WorktreeMutationResult, error) {
	if s == nil || s.backend == nil {
		return WorktreeMutationResult{}, fmt.Errorf("herdr owned session is nil")
	}
	if err := validateWorktreeMutationRequest(req); err != nil {
		return WorktreeMutationResult{}, err
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return WorktreeMutationResult{}, err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return WorktreeMutationResult{}, err
	}
	if req.Kind == WorktreeCreate || req.Kind == WorktreeOpen {
		coordinatorObserved, observeErr := s.backend.observeOwnedWorkspaces(ctx, admission)
		if observeErr != nil {
			return WorktreeMutationResult{}, fmt.Errorf("observe bound herdr coordinator: %w", observeErr)
		}
		if validateErr := validateBoundCoordinator(req, coordinatorObserved); validateErr != nil {
			return WorktreeMutationResult{}, validateErr
		}
	}
	if policyErr := s.backend.verifyEmptyPluginRegistry(ctx, probed); policyErr != nil {
		return WorktreeMutationResult{}, fmt.Errorf(
			"recheck herdr owned plugin registry before worktree mutation: %w",
			policyErr,
		)
	}

	args, envelopeID, resultType := worktreeMutationArgs(req)
	if admissionErr := validateMutationAdmission(req); admissionErr != nil {
		return WorktreeMutationResult{}, fmt.Errorf(
			"recheck herdr mutation admission under operation lock: %w",
			admissionErr,
		)
	}
	out, err := s.backend.runMutationContext(ctx, probed.binary, probed.route, args...)
	if err != nil {
		return WorktreeMutationResult{}, err
	}
	response, err := decodeWorktreeMutationResponse(out, envelopeID, resultType)
	if err != nil {
		return WorktreeMutationResult{}, err
	}
	if provenanceErr := validateMutationResponseProvenance(req, response.Workspace); provenanceErr != nil {
		return WorktreeMutationResult{}, provenanceErr
	}
	observed, err := s.backend.observeOwnedWorkspaces(ctx, admission)
	if err != nil {
		return WorktreeMutationResult{}, fmt.Errorf("observe herdr worktree mutation result: %w", err)
	}
	var matches []WorkspaceObservation
	for _, workspace := range observed {
		if workspace.WorkspaceID == response.Workspace.WorkspaceID {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation response workspace %q has %d matching live observations", response.Workspace.WorkspaceID, len(matches))
	}
	got := matches[0]
	if response.Workspace.Label != req.Label || got.Label != req.Label {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation workspace label = %q, want %q", got.Label, req.Label)
	}
	if *response.Workspace.Focused {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation focused a no-focus workspace")
	}
	if err := validateMutationResultProvenance(req, got); err != nil {
		return WorktreeMutationResult{}, err
	}
	alreadyOpen := response.AlreadyOpen != nil && *response.AlreadyOpen
	if req.Kind != WorktreeOpen && alreadyOpen {
		return WorktreeMutationResult{}, fmt.Errorf("herdr %s unexpectedly returned already_open", req.Kind)
	}
	return WorktreeMutationResult{WorkspaceObservation: got, AlreadyOpen: alreadyOpen}, nil
}

func validateMutationAdmission(req WorktreeMutationRequest) error {
	sourceIdentity, err := worktree.ResolveHerdrRepoIdentity(req.SourceRootPhysical)
	if err != nil {
		return fmt.Errorf("resolve saved herdr source identity: %w", err)
	}
	if sourceIdentity.RepoRoot != req.SourceRootPhysical ||
		sourceIdentity.GitDir != req.SourceGitDirPhysical ||
		sourceIdentity.GitDirDevice != req.SourceGitDirDevice ||
		sourceIdentity.GitDirInode != req.SourceGitDirInode ||
		sourceIdentity.RepoKey != req.SourceRepoKey {
		return fmt.Errorf("herdr source identity changed before mutation command")
	}

	switch req.Kind {
	case WorkspaceCreate:
		cwdIdentity, resolveErr := worktree.ResolveHerdrRepoIdentity(req.CWD)
		if resolveErr != nil {
			return fmt.Errorf("resolve coordinator cwd identity: %w", resolveErr)
		}
		cwdPhysical, physicalErr := filepath.EvalSymlinks(req.CWD)
		if physicalErr != nil {
			return fmt.Errorf("canonicalize coordinator cwd: %w", physicalErr)
		}
		if filepath.Clean(cwdPhysical) != req.SourceRootPhysical ||
			cwdIdentity.RepoRoot != req.SourceRootPhysical ||
			cwdIdentity.GitDir != req.SourceGitDirPhysical ||
			cwdIdentity.GitDirDevice != req.SourceGitDirDevice ||
			cwdIdentity.GitDirInode != req.SourceGitDirInode ||
			cwdIdentity.RepoKey != req.SourceRepoKey {
			return fmt.Errorf("herdr coordinator cwd identity changed before mutation command")
		}
		return nil
	case WorktreeCreate, WorktreeOpen:
		if err := worktree.VerifyHerdrWorktreeParent(req.ProjectRoot, req.Path); err != nil {
			return err
		}
		if err := worktree.VerifyReservedBranchGitDir(
			req.SourceGitDirPhysical,
			req.ResolvedBaseRef,
			req.Base,
			req.FullBranchRef,
		); err != nil {
			return err
		}
		pathAbsent, registered, headSHA, err := worktree.CheckoutGitStateGitDir(
			req.SourceGitDirPhysical,
			req.Path,
			req.FullBranchRef,
		)
		if err != nil {
			return err
		}
		if req.Kind == WorktreeCreate && (!pathAbsent || registered) {
			return fmt.Errorf("fresh herdr worktree path exists or is registered before mutation command")
		}
		if req.Kind == WorktreeOpen &&
			(pathAbsent || !registered || headSHA != req.Base) {
			return fmt.Errorf("saved herdr worktree is not the exact registered checkout before open")
		}
		return nil
	default:
		return fmt.Errorf("unknown herdr worktree mutation %q", req.Kind)
	}
}

func validateMutationResultProvenance(req WorktreeMutationRequest, got WorkspaceObservation) error {
	switch req.Kind {
	case WorkspaceCreate:
		if got.Path != "" || got.RepoKey != "" || got.RepoRoot != "" || got.CWD != req.CWD {
			return fmt.Errorf("herdr coordinator workspace provenance does not match request")
		}
	case WorktreeCreate, WorktreeOpen:
		if got.Path != req.Path ||
			got.RepoKey != req.ExpectedRepoKey ||
			got.RepoRoot != req.ExpectedRepoRoot ||
			got.CWD != req.Path {
			return fmt.Errorf("herdr worktree workspace provenance does not match request")
		}
	default:
		return fmt.Errorf("unknown herdr worktree mutation %q", req.Kind)
	}
	return nil
}

func validateMutationResponseProvenance(req WorktreeMutationRequest, workspace workspaceJSON) error {
	switch req.Kind {
	case WorkspaceCreate:
		if workspace.Worktree != nil {
			return fmt.Errorf("herdr coordinator response unexpectedly has worktree provenance")
		}
	case WorktreeCreate, WorktreeOpen:
		if workspace.Worktree == nil ||
			workspace.Worktree.CheckoutPath != req.Path ||
			workspace.Worktree.RepoKey != req.ExpectedRepoKey ||
			workspace.Worktree.RepoRoot != req.ExpectedRepoRoot {
			return fmt.Errorf("herdr worktree response provenance does not match request")
		}
	default:
		return fmt.Errorf("unknown herdr worktree mutation %q", req.Kind)
	}
	return nil
}

func validateBoundCoordinator(req WorktreeMutationRequest, observed []WorkspaceObservation) error {
	matches := 0
	for _, workspace := range observed {
		if workspace.WorkspaceID != req.WorkspaceID {
			continue
		}
		matches++
		if workspace.Label != req.CoordinatorWorkspaceLabel ||
			workspace.Path != "" ||
			workspace.RepoKey != "" ||
			workspace.RepoRoot != "" ||
			workspace.Pane.Pane != req.CoordinatorPaneID ||
			workspace.TerminalID != req.CoordinatorTerminalID ||
			workspace.CWD != req.CoordinatorWorkspaceCWD {
			return fmt.Errorf("herdr coordinator workspace identity does not match saved child request")
		}
	}
	if matches != 1 {
		return fmt.Errorf("herdr coordinator workspace %q has %d matching live observations", req.WorkspaceID, matches)
	}
	return nil
}

func validateWorktreeMutationRequest(req WorktreeMutationRequest) error {
	if strings.TrimSpace(req.Label) == "" || strings.ContainsAny(req.Label, "\x00\r\n") {
		return fmt.Errorf("herdr worktree mutation requires a safe ownership label")
	}
	if !req.NoFocus {
		return fmt.Errorf("herdr worktree mutation must use no-focus")
	}
	if req.SourceRootPhysical == "" ||
		req.SourceGitDirPhysical == "" ||
		req.SourceGitDirDevice == 0 ||
		req.SourceGitDirInode == 0 ||
		req.SourceRepoKey == "" {
		return fmt.Errorf("herdr worktree mutation requires saved source identity")
	}
	switch req.Kind {
	case WorkspaceCreate:
		if req.CWD == "" || req.WorkspaceID != "" ||
			req.CoordinatorWorkspaceLabel != "" || req.CoordinatorPaneID != "" ||
			req.CoordinatorTerminalID != "" || req.CoordinatorWorkspaceCWD != "" ||
			req.ExpectedRepoKey != "" || req.ExpectedRepoRoot != "" ||
			req.ProjectRoot != "" || req.ResolvedBaseRef != "" ||
			req.FullBranchRef != "" ||
			req.Branch != "" || req.Base != "" || req.Path != "" {
			return fmt.Errorf("invalid herdr workspace create request")
		}
	case WorktreeCreate:
		if req.WorkspaceID == "" || req.CoordinatorWorkspaceLabel == "" ||
			req.CoordinatorPaneID == "" || req.CoordinatorTerminalID == "" ||
			req.CoordinatorWorkspaceCWD == "" ||
			req.ExpectedRepoKey == "" || req.ExpectedRepoRoot == "" ||
			req.ProjectRoot == "" || req.ResolvedBaseRef == "" ||
			req.FullBranchRef == "" ||
			req.Branch == "" || req.Base == "" || req.Path == "" || req.CWD != "" {
			return fmt.Errorf("invalid herdr worktree create request")
		}
	case WorktreeOpen:
		if req.WorkspaceID == "" || req.CoordinatorWorkspaceLabel == "" ||
			req.CoordinatorPaneID == "" || req.CoordinatorTerminalID == "" ||
			req.CoordinatorWorkspaceCWD == "" ||
			req.ExpectedRepoKey == "" || req.ExpectedRepoRoot == "" ||
			req.ProjectRoot == "" || req.ResolvedBaseRef == "" ||
			req.FullBranchRef == "" ||
			req.Path == "" || req.CWD != "" || req.Branch != "" || req.Base == "" {
			return fmt.Errorf("invalid herdr worktree open request")
		}
	default:
		return fmt.Errorf("unknown herdr worktree mutation %q", req.Kind)
	}
	return nil
}

func worktreeMutationArgs(req WorktreeMutationRequest) (args []string, envelopeID, resultType string) {
	switch req.Kind {
	case WorkspaceCreate:
		args = []string{"workspace", "create", "--cwd", req.CWD, "--label", req.Label, "--no-focus"}
		return args, "cli:workspace:create", "workspace_created"
	case WorktreeCreate:
		args = []string{
			"worktree", "create",
			"--workspace", req.WorkspaceID,
			"--branch", req.Branch,
			"--base", req.Base,
			"--path", req.Path,
			"--label", req.Label,
			"--no-focus",
			"--json",
		}
		return args, "cli:worktree:create", "worktree_created"
	case WorktreeOpen:
		args = []string{
			"worktree", "open",
			"--workspace", req.WorkspaceID,
			"--path", req.Path,
			"--label", req.Label,
			"--no-focus",
			"--json",
		}
		return args, "cli:worktree:open", "worktree_opened"
	default:
		panic("validated worktree mutation kind")
	}
}

func decodeWorktreeMutationResponse(data []byte, envelopeID, resultType string) (worktreeMutationResult, error) {
	var envelope worktreeMutationEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return worktreeMutationResult{}, err
	}
	if envelope.ID != envelopeID || envelope.Result == nil || envelope.Result.Type != resultType {
		return worktreeMutationResult{}, fmt.Errorf("unexpected herdr %s envelope", resultType)
	}
	result := *envelope.Result
	if result.Workspace.WorkspaceID == "" || result.Workspace.Label == "" || result.Workspace.Focused == nil {
		return worktreeMutationResult{}, fmt.Errorf("herdr %s response has incomplete workspace identity", resultType)
	}
	if resultType == "worktree_opened" && result.AlreadyOpen == nil {
		return worktreeMutationResult{}, fmt.Errorf("herdr worktree_opened response is missing already_open")
	}
	return result, nil
}

func (b *Backend) observeOwnedWorkspaces(ctx context.Context, admission ownedAdmission) ([]WorkspaceObservation, error) {
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return nil, markRetryableRead(err)
	}
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return nil, classifyReadCommandError("session.snapshot", err)
	}
	var envelope snapshotEnvelope
	if err := decodeOne(out, &envelope); err != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	if _, err := projectSnapshot(envelope, probed); err != nil {
		return nil, methodUnavailable("session.snapshot")
	}
	snapshot := envelope.Result.Snapshot
	panes := make(map[string][]paneJSON, len(*snapshot.Workspaces))
	for _, pane := range *snapshot.Panes {
		panes[pane.WorkspaceID] = append(panes[pane.WorkspaceID], pane)
	}
	result := make([]WorkspaceObservation, 0, len(*snapshot.Workspaces))
	for _, workspace := range *snapshot.Workspaces {
		workspacePanes := panes[workspace.WorkspaceID]
		observation, err := workspaceObservation(workspace, workspacePanes)
		if err != nil {
			return nil, err
		}
		result = append(result, observation)
	}
	return result, nil
}

func (b *Backend) runMutationContext(
	ctx context.Context,
	binary string,
	target route,
	args ...string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, fmt.Errorf("herdr mutation requires a caller deadline")
	}
	return b.output(ctx, binary, routeEnvironment(target, b.control), args...)
}

func markRetryableRead(err error) error {
	if retryableCommandError(err) {
		return fmt.Errorf("%w: %w", ErrRetryableRead, err)
	}
	return err
}

func classifyReadCommandError(method string, err error) error {
	unavailable := methodUnavailable(method)
	if retryableCommandError(err) {
		return fmt.Errorf("%w: %w", ErrRetryableRead, unavailable)
	}
	return unavailable
}

func workspaceObservation(workspace workspaceJSON, panes []paneJSON) (WorkspaceObservation, error) {
	observation := WorkspaceObservation{
		WorkspaceID: workspace.WorkspaceID,
		Label:       workspace.Label,
	}
	if workspace.Worktree != nil {
		observation.Path = workspace.Worktree.CheckoutPath
		observation.RepoKey = workspace.Worktree.RepoKey
		observation.RepoRoot = workspace.Worktree.RepoRoot
	}
	switch len(panes) {
	case 0:
		return WorkspaceObservation{}, fmt.Errorf("herdr workspace %q has no pane", workspace.WorkspaceID)
	case 1:
		pane := panes[0]
		observation.Pane = corebackend.PaneRef{
			Backend:   corebackend.Herdr,
			Workspace: workspace.WorkspaceID,
			Pane:      pane.PaneID,
		}
		observation.TerminalID = pane.TerminalID
		observation.CWD = optionalString(pane.CWD)
	}
	// Multiple panes are valid in an established workspace. Leave root-pane
	// identity empty so pre-state inventory can proceed, while any attempt to
	// adopt that workspace as a fresh mutation result still fails closed.
	return observation, nil
}
