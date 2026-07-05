package peermsg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func msgTestLogger() *log.Logger {
	return log.NewWith(&strings.Builder{}, &strings.Builder{}, false)
}

func gitCmdTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

// TestDefaultLoadStateResolvesOwnerFromChildWorktree guards the regression
// Codex flagged: nudge is run from a child worktree pane, whose own git
// toplevel has no state.json. defaultLoadState must climb to the owner
// (OwnerProjectRoot) that holds the row — cmd/fanout's resolveStateRuntime
// would load the child's empty store and report every recipient "not
// recorded".
func TestDefaultLoadStateResolvesOwnerFromChildWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	owner := t.TempDir()
	gitCmdTest(t, owner, "init", "-b", "main")
	gitCmdTest(t, owner, "config", "user.email", "fanout@example.invalid")
	gitCmdTest(t, owner, "config", "user.name", "fanout test")
	if err := os.WriteFile(filepath.Join(owner, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmdTest(t, owner, "add", "README.md")
	gitCmdTest(t, owner, "commit", "-m", "base")

	child := filepath.Join(owner, ".fanout", "worktrees", "s-71")
	gitCmdTest(t, owner, "worktree", "add", "-b", "s-71", child)

	// The recipient row lives in the OWNER's state.json, never the child's.
	if err := os.MkdirAll(filepath.Dir(state.Path(owner)), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := `{"schemaVersion":1,"panes":[{"parent":"68","issueNum":71,"slug":"s-71","paneId":"%5"}]}` + "\n"
	if err := os.WriteFile(state.Path(owner), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Run from the child pane with no FANOUT_STATE_PATH override (the real
	// in-pane workflow). resolveStateRuntime would resolve the child toplevel
	// and find nothing.
	t.Chdir(child)
	t.Setenv(fanoutStatePathEnv, "")

	st, err := defaultLoadState()
	if err != nil {
		t.Fatalf("defaultLoadState() failed: %v", err)
	}
	if _, ok := st.Find("68", 71); !ok {
		t.Fatalf("defaultLoadState() found %d panes, want the owner's recipient #71 (child-worktree owner resolution regressed)", len(st.Panes))
	}
}
