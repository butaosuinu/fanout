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

// MutationNotIssuedError identifies a failure before runWorktreeMutation.
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

type WorktreeMutationRequest struct {
	Kind        WorktreeMutationKind
	Coordinator WorkspaceObservation

	SourceRoot      string
	SourceRepoKey   string
	SourceRepoRoot  string
	ProjectRoot     string
	FullBranchRef   string
	ExpectedHeadSHA string

	CWD    string
	Branch string
	Base   string
	Path   string
	Label  string

	ExpectedAlreadyOpenID    string
	ExpectedAlreadyOpenLabel string
	NoFocus                  bool
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

// VerifyWorktreeSetupPolicy rejects owned plugin registries that could run
// unreviewed setup hooks during workspace or worktree creation.
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
	return s.backend.observeOwnedWorkspaces(ctx, admission)
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

// MutateWorktree issues one workspace/worktree mutation after rechecking the
// owned session, plugin policy, coordinator, Git ref, and checkout path under
// the owned-operation lock.
func (s *OwnedSession) MutateWorktree(
	ctx context.Context,
	req WorktreeMutationRequest,
) (WorktreeMutationResult, error) {
	if s == nil || s.backend == nil {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr owned session is nil"),
		)
	}
	if requestErr := validateWorktreeMutationRequest(req); requestErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(requestErr)
	}
	admission, lock, admissionErr := s.backend.acquireOwnedOperation(ctx)
	if admissionErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(admissionErr)
	}
	defer unlockPrivateFile(lock)
	if admission.marker.GitCommonDir != req.SourceRepoKey {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("herdr mutation source repository does not match owned session"),
		)
	}
	probed, probeErr := s.backend.probeOwned(ctx, admission)
	if probeErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(probeErr)
	}
	alreadyOpenPrebound := false
	if req.Kind == WorktreeCreate || req.Kind == WorktreeOpen {
		workspaces, observeErr := s.backend.observeOwnedWorkspaces(ctx, admission)
		if observeErr != nil {
			return WorktreeMutationResult{}, mutationNotIssued(
				fmt.Errorf("observe bound Herdr coordinator: %w", observeErr),
			)
		}
		if coordinatorErr := validateBoundCoordinator(req.Coordinator, workspaces); coordinatorErr != nil {
			return WorktreeMutationResult{}, mutationNotIssued(coordinatorErr)
		}
		alreadyOpenPrebound = hasExpectedAlreadyOpenBinding(req, workspaces)
	}
	if policyErr := s.backend.verifyEmptyPluginRegistry(ctx, probed); policyErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("recheck Herdr plugin policy: %w", policyErr),
		)
	}
	if mutationAdmissionErr := validateMutationAdmission(req); mutationAdmissionErr != nil {
		return WorktreeMutationResult{}, mutationNotIssued(
			fmt.Errorf("recheck Herdr mutation admission: %w", mutationAdmissionErr),
		)
	}

	args, envelopeID, resultType := worktreeMutationArgs(req)
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
	if responseErr := validateMutationResponse(req, response.Workspace); responseErr != nil {
		return WorktreeMutationResult{}, responseErr
	}

	workspaces, observeErr := s.backend.observeOwnedWorkspaces(ctx, admission)
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
	got := matches[0]
	if got.Label != req.Label || response.Workspace.Label != req.Label {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation workspace label does not match request")
	}
	if *response.Workspace.Focused {
		return WorktreeMutationResult{}, fmt.Errorf("herdr mutation focused a no-focus workspace")
	}
	if err := validateMutationObservation(req, got); err != nil {
		return WorktreeMutationResult{}, err
	}
	alreadyOpen := response.AlreadyOpen != nil && *response.AlreadyOpen
	if err := validateAlreadyOpen(req, got, alreadyOpen, alreadyOpenPrebound); err != nil {
		return WorktreeMutationResult{}, err
	}
	return WorktreeMutationResult{WorkspaceObservation: got, AlreadyOpen: alreadyOpen}, nil
}

func mutationNotIssued(err error) error {
	return MutationNotIssuedError{Cause: err}
}

func validateAlreadyOpen(
	req WorktreeMutationRequest,
	got WorkspaceObservation,
	alreadyOpen bool,
	prebound bool,
) error {
	switch {
	case req.Kind != WorktreeOpen && alreadyOpen:
		return fmt.Errorf("herdr %s unexpectedly returned already_open", req.Kind)
	case req.Kind == WorktreeOpen && alreadyOpen &&
		(!prebound || got.WorkspaceID != req.ExpectedAlreadyOpenID ||
			got.Label != req.ExpectedAlreadyOpenLabel):
		return fmt.Errorf("herdr already_open workspace is not bound to this intent")
	case req.Kind == WorktreeOpen && !alreadyOpen && prebound:
		return fmt.Errorf("herdr worktree open replaced a prebound workspace")
	}
	return nil
}

func hasExpectedAlreadyOpenBinding(
	req WorktreeMutationRequest,
	workspaces []WorkspaceObservation,
) bool {
	if req.ExpectedAlreadyOpenID == "" || req.ExpectedAlreadyOpenLabel == "" {
		return false
	}
	matches := 0
	for _, workspace := range workspaces {
		if workspace.WorkspaceID != req.ExpectedAlreadyOpenID ||
			workspace.Label != req.ExpectedAlreadyOpenLabel {
			continue
		}
		if workspace.Path != req.Path || workspace.RepoKey != req.SourceRepoKey ||
			workspace.RepoRoot != req.SourceRepoRoot ||
			!workspaceHasPaneCWD(workspace, req.Path) {
			return false
		}
		matches++
	}
	return matches == 1
}

func validateMutationAdmission(req WorktreeMutationRequest) error {
	source, sourceErr := worktree.ResolveHerdrRepoIdentity(req.SourceRoot)
	if sourceErr != nil {
		return sourceErr
	}
	if source.RepoKey != req.SourceRepoKey || source.RepoRoot != req.SourceRepoRoot {
		return fmt.Errorf("herdr source repository identity changed")
	}
	switch req.Kind {
	case WorkspaceCreate:
		cwd, err := filepath.EvalSymlinks(req.CWD)
		if err != nil {
			return fmt.Errorf("canonicalize Herdr coordinator cwd: %w", err)
		}
		if filepath.Clean(cwd) != source.RepoRoot {
			return fmt.Errorf("herdr coordinator cwd changed")
		}
	case WorktreeCreate:
		if parentErr := worktree.VerifyHerdrWorktreeParent(req.ProjectRoot, req.Path); parentErr != nil {
			return parentErr
		}
		branch, found, branchErr := worktree.ObserveHerdrBranch(req.SourceRoot, req.FullBranchRef)
		if branchErr != nil {
			return branchErr
		}
		if !found || branch != req.ExpectedHeadSHA {
			return fmt.Errorf("herdr branch %s no longer points at %s", req.FullBranchRef, req.ExpectedHeadSHA)
		}
		if availableErr := worktree.HerdrBranchAvailable(req.SourceRoot, req.FullBranchRef); availableErr != nil {
			return availableErr
		}
		checkout, checkoutErr := worktree.ObserveHerdrCheckout(req.SourceRoot, req.Path)
		if checkoutErr != nil {
			return checkoutErr
		}
		if !checkout.PathAbsent || checkout.Registered {
			return fmt.Errorf("herdr worktree path exists or is registered before create")
		}
	case WorktreeOpen:
		if _, err := worktree.VerifyHerdrCheckout(
			req.SourceRoot,
			req.Path,
			req.FullBranchRef,
			req.ExpectedHeadSHA,
			req.SourceRepoKey,
			req.SourceRepoRoot,
		); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown Herdr worktree mutation %q", req.Kind)
	}
	return nil
}

func validateBoundCoordinator(expected WorkspaceObservation, observed []WorkspaceObservation) error {
	matches := 0
	for _, workspace := range observed {
		if workspace.WorkspaceID != expected.WorkspaceID {
			continue
		}
		matches++
		if !sameWorkspaceObservation(workspace, expected) {
			return fmt.Errorf("herdr coordinator workspace identity changed")
		}
	}
	if matches != 1 {
		return fmt.Errorf("herdr coordinator workspace %q has %d live matches", expected.WorkspaceID, matches)
	}
	return nil
}

func validateWorktreeMutationRequest(req WorktreeMutationRequest) error {
	if req.SourceRoot == "" || req.SourceRepoKey == "" || req.SourceRepoRoot == "" ||
		req.Label == "" || strings.ContainsAny(req.Label, "\x00\r\n") || !req.NoFocus {
		return fmt.Errorf("herdr worktree mutation request is incomplete")
	}
	switch req.Kind {
	case WorkspaceCreate:
		if req.CWD == "" || req.Coordinator.WorkspaceID != "" || req.ProjectRoot != "" ||
			req.FullBranchRef != "" || req.ExpectedHeadSHA != "" || req.Branch != "" ||
			req.Base != "" || req.Path != "" {
			return fmt.Errorf("invalid Herdr workspace create request")
		}
	case WorktreeCreate:
		if err := validateChildMutationRequest(req); err != nil {
			return err
		}
		if req.Branch == "" || req.CWD != "" ||
			req.ExpectedAlreadyOpenID != "" || req.ExpectedAlreadyOpenLabel != "" {
			return fmt.Errorf("invalid Herdr worktree create request")
		}
	case WorktreeOpen:
		if err := validateChildMutationRequest(req); err != nil {
			return err
		}
		if req.Branch != "" || req.Base != "" || req.CWD != "" ||
			(req.ExpectedAlreadyOpenID == "") != (req.ExpectedAlreadyOpenLabel == "") {
			return fmt.Errorf("invalid Herdr worktree open request")
		}
	default:
		return fmt.Errorf("unknown Herdr worktree mutation %q", req.Kind)
	}
	return nil
}

func validateChildMutationRequest(req WorktreeMutationRequest) error {
	if req.Coordinator.WorkspaceID == "" || req.Coordinator.Label == "" ||
		req.Coordinator.Pane.Pane == "" || req.Coordinator.TerminalID == "" ||
		req.Coordinator.CWD == "" || req.ProjectRoot == "" ||
		req.FullBranchRef == "" || req.ExpectedHeadSHA == "" || req.Path == "" {
		return fmt.Errorf("herdr child worktree mutation request is incomplete")
	}
	return nil
}

func worktreeMutationArgs(req WorktreeMutationRequest) (args []string, envelopeID, resultType string) {
	switch req.Kind {
	case WorkspaceCreate:
		return []string{
			"workspace", "create",
			"--cwd", req.CWD,
			"--label", req.Label,
			"--no-focus",
		}, "cli:workspace:create", "workspace_created"
	case WorktreeCreate:
		args = []string{
			"worktree", "create",
			"--workspace", req.Coordinator.WorkspaceID,
			"--branch", req.Branch,
		}
		if req.Base != "" {
			args = append(args, "--base", req.Base)
		}
		args = append(args,
			"--path", req.Path,
			"--label", req.Label,
			"--no-focus",
			"--json",
		)
		return args, "cli:worktree:create", "worktree_created"
	case WorktreeOpen:
		return []string{
			"worktree", "open",
			"--workspace", req.Coordinator.WorkspaceID,
			"--path", req.Path,
			"--label", req.Label,
			"--no-focus",
			"--json",
		}, "cli:worktree:open", "worktree_opened"
	default:
		panic("validated Herdr worktree mutation kind")
	}
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

func validateMutationResponse(req WorktreeMutationRequest, workspace workspaceJSON) error {
	switch req.Kind {
	case WorkspaceCreate:
		if workspace.Worktree != nil {
			return fmt.Errorf("herdr coordinator response unexpectedly has worktree provenance")
		}
	case WorktreeCreate, WorktreeOpen:
		if workspace.Worktree == nil ||
			workspace.Worktree.CheckoutPath != req.Path ||
			workspace.Worktree.RepoKey != req.SourceRepoKey ||
			workspace.Worktree.RepoRoot != req.SourceRepoRoot {
			return fmt.Errorf("herdr worktree response provenance does not match request")
		}
	default:
		return fmt.Errorf("unknown Herdr mutation %q", req.Kind)
	}
	return nil
}

func validateMutationObservation(req WorktreeMutationRequest, got WorkspaceObservation) error {
	if got.Pane.Pane == "" || got.TerminalID == "" {
		return fmt.Errorf("herdr mutation result has no unique root pane")
	}
	switch req.Kind {
	case WorkspaceCreate:
		if got.Path != "" || got.RepoKey != "" || got.RepoRoot != "" || got.CWD != req.CWD {
			return fmt.Errorf("herdr coordinator provenance does not match request")
		}
	case WorktreeCreate, WorktreeOpen:
		if got.Path != req.Path || got.RepoKey != req.SourceRepoKey ||
			got.RepoRoot != req.SourceRepoRoot || got.CWD != req.Path {
			return fmt.Errorf("herdr worktree provenance does not match request")
		}
	}
	return nil
}

func sameWorkspaceObservation(left, right WorkspaceObservation) bool {
	return left.WorkspaceID == right.WorkspaceID &&
		left.Label == right.Label &&
		left.Path == right.Path &&
		left.RepoKey == right.RepoKey &&
		left.RepoRoot == right.RepoRoot &&
		workspaceHasPane(left, WorkspacePaneObservation{
			Pane: right.Pane, TerminalID: right.TerminalID, CWD: right.CWD,
		})
}

func (b *Backend) observeOwnedWorkspaces(
	ctx context.Context,
	admission ownedAdmission,
) ([]WorkspaceObservation, error) {
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
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

func workspaceHasPane(observation WorkspaceObservation, expected WorkspacePaneObservation) bool {
	if observation.Pane == expected.Pane &&
		observation.TerminalID == expected.TerminalID &&
		observation.CWD == expected.CWD {
		return true
	}
	for _, pane := range observation.Panes {
		if pane == expected {
			return true
		}
	}
	return false
}

func workspaceHasPaneCWD(observation WorkspaceObservation, cwd string) bool {
	if observation.CWD == cwd {
		return true
	}
	for _, pane := range observation.Panes {
		if pane.CWD == cwd {
			return true
		}
	}
	return false
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
	if _, ok := ctx.Deadline(); !ok {
		return nil, mutationNotIssued(
			fmt.Errorf("herdr mutation requires a caller deadline"),
		)
	}
	return b.output(ctx, binary, routeEnvironment(target, b.control), args...)
}
