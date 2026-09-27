package herdrrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type statusJSON struct {
	Client struct {
		Version string  `json:"version"`
		Channel string  `json:"channel"`
		Session *string `json:"session"`
	} `json:"client"`
	Server struct {
		Status        string  `json:"status"`
		Running       bool    `json:"running"`
		Version       *string `json:"version"`
		Socket        string  `json:"socket"`
		Session       *string `json:"session"`
		RestartNeeded *bool   `json:"restart_needed"`
	} `json:"server"`
	Update struct {
		RestartNeeded *bool `json:"restart_needed"`
	} `json:"update"`
}

type snapshotEnvelope struct {
	ID     string          `json:"id"`
	Result *snapshotResult `json:"result"`
}

type snapshotResult struct {
	Type     string       `json:"type"`
	Snapshot snapshotJSON `json:"snapshot"`
}

type snapshotJSON struct {
	Version    string             `json:"version"`
	Workspaces *[]workspaceJSON   `json:"workspaces"`
	Tabs       *[]json.RawMessage `json:"tabs"`
	Panes      *[]paneJSON        `json:"panes"`
	Layouts    *[]json.RawMessage `json:"layouts"`
	Agents     *[]agentJSON       `json:"agents"`
}

type workspaceJSON struct {
	WorkspaceID string            `json:"workspace_id"`
	Label       string            `json:"label"`
	Focused     *bool             `json:"focused"`
	Worktree    *worktreeInfoJSON `json:"worktree"`
}

type worktreeInfoJSON struct {
	RepoKey      string `json:"repo_key"`
	CheckoutPath string `json:"checkout_path"`
	RepoRoot     string `json:"repo_root"`
	IsLinked     bool   `json:"is_linked_worktree"`
}

type paneJSON struct {
	PaneID       string            `json:"pane_id"`
	TerminalID   string            `json:"terminal_id"`
	WorkspaceID  string            `json:"workspace_id"`
	TabID        string            `json:"tab_id"`
	CWD          *string           `json:"cwd"`
	Title        *string           `json:"title"`
	Focused      *bool             `json:"focused"`
	AgentStatus  string            `json:"agent_status"`
	AgentSession *agentSessionJSON `json:"agent_session"`
	Revision     *uint64           `json:"revision"`
}

type agentJSON struct {
	TerminalID   string            `json:"terminal_id"`
	Name         *string           `json:"name"`
	Agent        *string           `json:"agent"`
	AgentStatus  string            `json:"agent_status"`
	WorkspaceID  string            `json:"workspace_id"`
	TabID        string            `json:"tab_id"`
	PaneID       string            `json:"pane_id"`
	Focused      *bool             `json:"focused"`
	AgentSession *agentSessionJSON `json:"agent_session"`
	Revision     *uint64           `json:"revision"`
}

type agentSessionJSON struct {
	Source *string `json:"source"`
	Agent  *string `json:"agent"`
	Kind   *string `json:"kind"`
	Value  *string `json:"value"`
}

func decodeOne(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
