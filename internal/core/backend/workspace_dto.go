package backend

// Workspace and worktree mutation vocabulary: the requests orchestration
// issues, the observation it gets back, and the error contract that separates
// a complete server rejection from a mutation that never left the caller. The
// socket transport that executes them lives in infra.

import (
	"errors"
	"fmt"
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
	Pane        PaneRef
	TerminalID  string
	CWD         string
	Panes       []WorkspacePaneObservation
}

type WorkspacePaneObservation struct {
	Pane       PaneRef
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
