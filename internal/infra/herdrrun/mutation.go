package herdrrun

// Socket mutations against the fanout-owned Herdr session. This adapter owns
// the CLI argv contract, the response envelope contract, and the one
// post-mutation snapshot projection; Git preconditions and postconditions are
// the caller's (app/panelaunch) responsibility under the combined launch lock.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type WorktreeMutationKind string

const (
	WorkspaceCreate WorktreeMutationKind = "workspace-create"
	WorktreeCreate  WorktreeMutationKind = "worktree-create"
	WorktreeOpen    WorktreeMutationKind = "worktree-open"
)

// Mutation errors distinguish a complete server rejection from a failure
// before the socket command was dispatched.
var (
	ErrMutationRejected  = errors.New("herdr mutation rejected")
	ErrMutationNotIssued = errors.New("herdr mutation was not issued")
)

type MutationRejectedError struct {
	Code    string
	Message string
}

func (e MutationRejectedError) Error() string {
	return fmt.Sprintf("herdr mutation rejected: %s: %s", e.Code, e.Message)
}

func (e MutationRejectedError) Unwrap() error { return ErrMutationRejected }

// MutationNotIssuedError identifies a failure before the socket command was
// dispatched, proving the mutation never reached the server.
type MutationNotIssuedError struct {
	Cause error
}

func (e MutationNotIssuedError) Error() string {
	return fmt.Sprintf("herdr mutation was not issued: %v", e.Cause)
}

func (e MutationNotIssuedError) Unwrap() error { return e.Cause }

func (e MutationNotIssuedError) Is(target error) bool {
	return target == ErrMutationNotIssued
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
	Panes       []WorkspacePaneObservation
}

type WorkspacePaneObservation struct {
	Pane       corebackend.PaneRef
	TerminalID string
	CWD        string
}

type OwnedWorktreeRoute struct {
	GitCommonDir string
	Session      string
	SocketPath   string
}

// WorkspaceCreateRequest creates the coordinator workspace at the repository
// root. Creation is always --no-focus.
type WorkspaceCreateRequest struct {
	CWD           string
	SourceRepoKey string
	Label         string
}

// WorktreeCreateRequest creates the child checkout workspace. An empty Base
// adopts the existing branch.
type WorktreeCreateRequest struct {
	Coordinator    WorkspaceObservation
	SourceRepoKey  string
	SourceRepoRoot string
	Branch         string
	Base           string
	Path           string
	Label          string
}

// WorktreeOpenRequest re-registers an existing checkout. already_open:true is
// accepted only when the response matches the intent-bound workspace identity.
type WorktreeOpenRequest struct {
	Coordinator              WorkspaceObservation
	SourceRepoKey            string
	SourceRepoRoot           string
	Path                     string
	Label                    string
	ExpectedAlreadyOpenID    string
	ExpectedAlreadyOpenLabel string
}

type WorktreeMutationResult struct {
	WorkspaceObservation
	AlreadyOpen bool
}

type worktreeMutationEnvelope struct {
	ID     string                  `json:"id"`
	Result *worktreeMutationResult `json:"result"`
	Error  *worktreeMutationError  `json:"error"`
}

type worktreeMutationResult struct {
	Type        string        `json:"type"`
	Workspace   workspaceJSON `json:"workspace"`
	AlreadyOpen *bool         `json:"already_open"`
}

type worktreeMutationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type pluginListEnvelope struct {
	ID     string            `json:"id"`
	Result *pluginListResult `json:"result"`
}

type pluginListResult struct {
	Type    string             `json:"type"`
	Plugins *[]json.RawMessage `json:"plugins"`
}

// mutationSpec is the per-kind shape one issueMutation call executes.
type mutationSpec struct {
	kind          WorktreeMutationKind
	sourceRepoKey string
	label         string

	// worktree kinds only: the response provenance the workspace must carry.
	path     string
	repoKey  string
	repoRoot string

	// open only.
	expectedAlreadyOpenID    string
	expectedAlreadyOpenLabel string
}

// VerifyWorktreeSetupPolicy rejects owned plugin registries that could run
// unreviewed setup hooks during workspace or worktree creation. This is the
// once-per-launch preflight; mutations do not repeat it.
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
		return err
	}
	return s.backend.verifyEmptyPluginRegistry(ctx, probed)
}

func (b *Backend) verifyEmptyPluginRegistry(ctx context.Context, probed probeResult) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "plugin", "list", "--json")
	if err != nil {
		return methodUnavailable("plugin.list")
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

// ObserveWorkspaces returns the validated owned-session workspace inventory
// used for response-loss classification.
func (s *OwnedSession) ObserveWorkspaces(ctx context.Context) ([]WorkspaceObservation, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
	return s.backend.observeOwnedWorkspaces(ctx, probed)
}

// WorktreeRoute returns the repository and route sealed by the current owned
// admission. Realization persists only this binding.
func (s *OwnedSession) WorktreeRoute(ctx context.Context) (OwnedWorktreeRoute, error) {
	if s == nil || s.backend == nil {
		return OwnedWorktreeRoute{}, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return OwnedWorktreeRoute{}, err
	}
	defer unlockPrivateFile(lock)
	return OwnedWorktreeRoute{
		GitCommonDir: admission.marker.GitCommonDir,
		Session:      admission.marker.Session,
		SocketPath:   admission.marker.SocketPath,
	}, nil
}

// CreateWorkspace issues one coordinator workspace create.
func (s *OwnedSession) CreateWorkspace(
	ctx context.Context,
	req WorkspaceCreateRequest,
) (WorktreeMutationResult, error) {
	if err := validateMutationLabel(req.Label); err != nil {
		return WorktreeMutationResult{}, mutationNotIssued(err)
	}
	if req.CWD == "" || req.SourceRepoKey == "" {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr workspace create request is incomplete"),
		)
	}
	return s.issueMutation(ctx, mutationSpec{
		kind:          WorkspaceCreate,
		sourceRepoKey: req.SourceRepoKey,
		label:         req.Label,
	}, workspaceCreateArgs(req), "cli:workspace:create", "workspace_created")
}

func workspaceCreateArgs(req WorkspaceCreateRequest) []string {
	return []string{
		"workspace", "create",
		"--cwd", req.CWD,
		"--label", req.Label,
		"--no-focus",
	}
}

// CreateWorktree issues one child checkout workspace create.
func (s *OwnedSession) CreateWorktree(
	ctx context.Context,
	req WorktreeCreateRequest,
) (WorktreeMutationResult, error) {
	if err := validateMutationLabel(req.Label); err != nil {
		return WorktreeMutationResult{}, mutationNotIssued(err)
	}
	if err := validateChildMutation(req.Coordinator, req.SourceRepoKey, req.SourceRepoRoot, req.Path); err != nil {
		return WorktreeMutationResult{}, mutationNotIssued(err)
	}
	if req.Branch == "" {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr worktree create request is incomplete"),
		)
	}
	return s.issueMutation(ctx, mutationSpec{
		kind:          WorktreeCreate,
		sourceRepoKey: req.SourceRepoKey,
		label:         req.Label,
		path:          req.Path,
		repoKey:       req.SourceRepoKey,
		repoRoot:      req.SourceRepoRoot,
	}, worktreeCreateArgs(req), "cli:worktree:create", "worktree_created")
}

func worktreeCreateArgs(req WorktreeCreateRequest) []string {
	args := []string{
		"worktree", "create",
		"--workspace", req.Coordinator.WorkspaceID,
		"--branch", req.Branch,
	}
	if req.Base != "" {
		args = append(args, "--base", req.Base)
	}
	return append(args,
		"--path", req.Path,
		"--label", req.Label,
		"--no-focus",
		"--json",
	)
}

// OpenWorktree re-registers one existing checkout workspace.
func (s *OwnedSession) OpenWorktree(
	ctx context.Context,
	req WorktreeOpenRequest,
) (WorktreeMutationResult, error) {
	if err := validateMutationLabel(req.Label); err != nil {
		return WorktreeMutationResult{}, mutationNotIssued(err)
	}
	if err := validateChildMutation(req.Coordinator, req.SourceRepoKey, req.SourceRepoRoot, req.Path); err != nil {
		return WorktreeMutationResult{}, mutationNotIssued(err)
	}
	if (req.ExpectedAlreadyOpenID == "") != (req.ExpectedAlreadyOpenLabel == "") {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr worktree open request is incomplete"),
		)
	}
	return s.issueMutation(ctx, mutationSpec{
		kind:                     WorktreeOpen,
		sourceRepoKey:            req.SourceRepoKey,
		label:                    req.Label,
		path:                     req.Path,
		repoKey:                  req.SourceRepoKey,
		repoRoot:                 req.SourceRepoRoot,
		expectedAlreadyOpenID:    req.ExpectedAlreadyOpenID,
		expectedAlreadyOpenLabel: req.ExpectedAlreadyOpenLabel,
	}, worktreeOpenArgs(req), "cli:worktree:open", "worktree_opened")
}

func worktreeOpenArgs(req WorktreeOpenRequest) []string {
	return []string{
		"worktree", "open",
		"--workspace", req.Coordinator.WorkspaceID,
		"--path", req.Path,
		"--label", req.Label,
		"--no-focus",
		"--json",
	}
}

// issueMutation dispatches one validated socket mutation and projects the
// created workspace from a single post-mutation snapshot (the response
// envelope carries no pane identity).
func (s *OwnedSession) issueMutation(
	ctx context.Context,
	spec mutationSpec,
	args []string,
	envelopeID, resultType string,
) (WorktreeMutationResult, error) {
	if s == nil || s.backend == nil {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr owned session is nil"),
		)
	}
	admission, lock, admissionErr := s.backend.acquireOwnedOperation(ctx)
	if admissionErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(admissionErr)
	}
	defer unlockPrivateFile(lock)
	if admission.marker.GitCommonDir != spec.sourceRepoKey {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr mutation source repository does not match owned session"),
		)
	}
	probed, probeErr := s.backend.probeOwned(ctx, admission)
	if probeErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(probeErr)
	}

	out, commandErr := s.backend.runWorktreeMutation(ctx, probed.binary, probed.route, args...)
	if commandErr != nil {
		if rejected, ok := decodeMutationRejection(out, envelopeID); ok {
			return WorktreeMutationResult{}, rejected
		}
		return WorktreeMutationResult{}, commandErr
	}
	response, decodeErr := decodeWorktreeMutationResponse(out, envelopeID, resultType)
	if decodeErr != nil {
		return WorktreeMutationResult{}, decodeErr
	}
	if responseErr := validateMutationResponse(spec, response.Workspace); responseErr != nil {
		return WorktreeMutationResult{}, responseErr
	}
	if *response.Workspace.Focused {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation focused a no-focus workspace")
	}
	alreadyOpen := response.AlreadyOpen != nil && *response.AlreadyOpen
	if err := validateAlreadyOpen(spec, response.Workspace, alreadyOpen); err != nil {
		return WorktreeMutationResult{}, err
	}

	workspaces, observeErr := s.backend.observeOwnedWorkspaces(ctx, probed)
	if observeErr != nil {
		return WorktreeMutationResult{}, fmt.Errorf("observe Herdr mutation result: %w", observeErr)
	}
	var matches []WorkspaceObservation
	for _, workspace := range workspaces {
		if workspace.WorkspaceID == response.Workspace.WorkspaceID {
			matches = append(matches, workspace)
		}
	}
	if len(matches) != 1 {
		return WorktreeMutationResult{}, fmt.Errorf(
			"herdr mutation response workspace %q has %d live matches",
			response.Workspace.WorkspaceID,
			len(matches),
		)
	}
	return WorktreeMutationResult{WorkspaceObservation: matches[0], AlreadyOpen: alreadyOpen}, nil
}

func mutationNotIssued(err error) error {
	return MutationNotIssuedError{Cause: err}
}

func validateMutationLabel(label string) error {
	if label == "" || strings.ContainsAny(label, "\x00\r\n") {
		return fmt.Errorf("herdr mutation label is invalid")
	}
	return nil
}

func validateChildMutation(
	coordinator WorkspaceObservation,
	sourceRepoKey, sourceRepoRoot, path string,
) error {
	if coordinator.WorkspaceID == "" || coordinator.Label == "" ||
		sourceRepoKey == "" || sourceRepoRoot == "" || path == "" {
		return fmt.Errorf("herdr child worktree mutation request is incomplete")
	}
	return nil
}

// validateAlreadyOpen accepts already_open:true only when the response is
// bound to the intent-recorded workspace identity.
func validateAlreadyOpen(
	spec mutationSpec,
	workspace workspaceJSON,
	alreadyOpen bool,
) error {
	switch {
	case spec.kind != WorktreeOpen && alreadyOpen:
		return fmt.Errorf("herdr %s unexpectedly returned already_open", spec.kind)
	case spec.kind == WorktreeOpen && alreadyOpen &&
		(spec.expectedAlreadyOpenID == "" ||
			workspace.WorkspaceID != spec.expectedAlreadyOpenID ||
			workspace.Label != spec.expectedAlreadyOpenLabel):
		return fmt.Errorf("herdr already_open workspace is not bound to this intent")
	}
	return nil
}

func decodeMutationRejection(data []byte, expectedID string) (MutationRejectedError, bool) {
	var envelope worktreeMutationEnvelope
	if err := decodeOne(data, &envelope); err != nil || envelope.ID != expectedID ||
		envelope.Result != nil || envelope.Error == nil ||
		strings.TrimSpace(envelope.Error.Code) == "" || strings.TrimSpace(envelope.Error.Message) == "" {
		return MutationRejectedError{}, false
	}
	return MutationRejectedError{Code: envelope.Error.Code, Message: envelope.Error.Message}, true
}

func decodeWorktreeMutationResponse(
	data []byte,
	envelopeID, resultType string,
) (worktreeMutationResult, error) {
	var envelope worktreeMutationEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return worktreeMutationResult{}, err
	}
	if envelope.ID != envelopeID || envelope.Result == nil || envelope.Error != nil ||
		envelope.Result.Type != resultType {
		return worktreeMutationResult{}, fmt.Errorf("unexpected Herdr %s envelope", resultType)
	}
	result := *envelope.Result
	if result.Workspace.WorkspaceID == "" || result.Workspace.Label == "" ||
		result.Workspace.Focused == nil {
		return worktreeMutationResult{}, fmt.Errorf("herdr %s response has incomplete workspace identity", resultType)
	}
	if resultType == "worktree_opened" && result.AlreadyOpen == nil {
		return worktreeMutationResult{}, fmt.Errorf("herdr worktree_opened response is missing already_open")
	}
	return result, nil
}

func validateMutationResponse(spec mutationSpec, workspace workspaceJSON) error {
	if workspace.Label != spec.label {
		return fmt.Errorf("herdr mutation workspace label does not match request")
	}
	switch spec.kind {
	case WorkspaceCreate:
		if workspace.Worktree != nil {
			return fmt.Errorf("herdr coordinator response unexpectedly has worktree provenance")
		}
	case WorktreeCreate, WorktreeOpen:
		if workspace.Worktree == nil ||
			workspace.Worktree.CheckoutPath != spec.path ||
			workspace.Worktree.RepoKey != spec.repoKey ||
			workspace.Worktree.RepoRoot != spec.repoRoot {
			return fmt.Errorf("herdr worktree response provenance does not match request")
		}
	default:
		return fmt.Errorf("unknown Herdr mutation %q", spec.kind)
	}
	return nil
}

func (b *Backend) observeOwnedWorkspaces(
	ctx context.Context,
	probed probeResult,
) ([]WorkspaceObservation, error) {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return nil, methodUnavailable("session.snapshot")
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
		observation, err := workspaceObservation(workspace, panes[workspace.WorkspaceID])
		if err != nil {
			return nil, err
		}
		result = append(result, observation)
	}
	return result, nil
}

func workspaceObservation(
	workspace workspaceJSON,
	panes []paneJSON,
) (WorkspaceObservation, error) {
	observation := WorkspaceObservation{
		WorkspaceID: workspace.WorkspaceID,
		Label:       workspace.Label,
	}
	if workspace.Worktree != nil {
		observation.Path = workspace.Worktree.CheckoutPath
		observation.RepoKey = workspace.Worktree.RepoKey
		observation.RepoRoot = workspace.Worktree.RepoRoot
	}
	observation.Panes = make([]WorkspacePaneObservation, 0, len(panes))
	for _, pane := range panes {
		observation.Panes = append(observation.Panes, WorkspacePaneObservation{
			Pane: corebackend.PaneRef{
				Backend:   corebackend.Herdr,
				Workspace: workspace.WorkspaceID,
				Pane:      pane.PaneID,
			},
			TerminalID: pane.TerminalID,
			CWD:        optionalString(pane.CWD),
		})
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
	// Established workspaces may have multiple panes. Keep every pane so a
	// saved root identity can be verified without guessing a new root.
	return observation, nil
}

func (b *Backend) runWorktreeMutation(
	ctx context.Context,
	binary string,
	target route,
	args ...string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, mutationNotIssued(err)
	}
	out, err := b.output(ctx, binary, routeEnvironment(target, b.control), args...)
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// Only a provable start failure (binary resolution or fork/exec) is
		// unissued. A cancellation after start and a nonzero exit stay
		// ambiguous and go through existence classification.
		var execErr *exec.Error
		var pathErr *os.PathError
		if errors.As(err, &execErr) || errors.As(err, &pathErr) {
			return out, mutationNotIssued(err)
		}
	}
	return out, err
}
