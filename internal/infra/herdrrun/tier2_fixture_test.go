package herdrrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

const tier2LaunchFixtureEnv = "FANOUT_TEST_HERDR_LAUNCH_FIXTURE"

func TestTier2LaunchFixture(t *testing.T) {
	if os.Getenv(tier2LaunchFixtureEnv) != "1" {
		t.Skip("run by the Tier 2 Herdr launch scenario")
	}

	h := newOwnedHarness(t)
	fixture := materializeTier2LaunchFixture(t, h)
	installTier2LaunchShim(t, h, fixture)

	ctx := context.Background()
	coordinator, err := h.session.CreateWorkspace(ctx, corebackend.WorkspaceCreateRequest{
		CWD: h.root, SourceRepoKey: h.commonDir, Label: "fixture-coordinator",
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := h.session.CreateWorktree(ctx, corebackend.WorktreeCreateRequest{
		Coordinator:   coordinator.WorkspaceObservation,
		SourceRepoKey: h.commonDir, SourceRepoRoot: h.root,
		Branch: "fanout/fixture-child", Path: h.checkout, Label: "fixture-child",
	})
	if err != nil {
		t.Fatal(err)
	}

	const nonce = "0123456789abcdef0123456789abcdef"
	paneID := child.Pane.Pane
	err = h.session.WaitForLauncher(ctx, paneID, nonce, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = h.session.SendLaunchToken(ctx, paneID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	process, err := h.session.ProcessInfo(ctx, paneID)
	if err != nil {
		t.Fatal(err)
	}
	if process.PaneID != paneID || len(process.ForegroundProcesses) != 1 {
		t.Fatalf("process info = %+v, want one process for %s", process, paneID)
	}
	if err := h.session.RenameAgent(ctx, paneID, "fanout-fixture-agent"); err != nil {
		t.Fatal(err)
	}
}

func materializeTier2LaunchFixture(t *testing.T, h *ownedHarness) string {
	t.Helper()
	source := requiredTier2Path(t, "FIXTURE_DIR")
	destination := t.TempDir()
	replacer := strings.NewReplacer(
		"{{SESSION}}", h.session.Session,
		"{{SOCKET_PATH}}", h.session.SocketPath,
		"{{REPO_KEY}}", h.commonDir,
		"{{REPO_ROOT}}", h.root,
		"{{WORKTREE_PATH}}", h.checkout,
	)
	for _, name := range []string{
		"herdr-version.txt", "herdr-status.json", "herdr-snapshot.json",
		"herdr-workspace-create.json", "herdr-worktree-create.json",
		"herdr-pane-wait-output.json", "herdr-pane-run.json",
		"herdr-pane-process-info.json", "herdr-agent-rename.json",
	} {
		data, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(replacer.Replace(string(data)))
		if err := os.WriteFile(filepath.Join(destination, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return destination
}

func installTier2LaunchShim(t *testing.T, h *ownedHarness, fixture string) {
	t.Helper()
	shim := requiredTier2Path(t, "FANOUT_TEST_HERDR_SHIM")
	logPath := requiredTier2Path(t, "HERDR_SHIM_LOG")
	path := os.Getenv("PATH")
	h.session.backend.output = func(
		ctx context.Context,
		_ string,
		environment []string,
		args ...string,
	) ([]byte, error) {
		environment = append(environment,
			"FIXTURE_DIR="+fixture,
			"HERDR_SHIM_LOG="+logPath,
			"PATH="+path,
		)
		return runCommand(ctx, shim, environment, args...)
	}
	h.session.processInspector = func(
		_ context.Context,
		processes []corebackend.PaneProcess,
	) ([]corebackend.PaneProcess, error) {
		return processes, nil
	}
}

func requiredTier2Path(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv(name)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s must be a clean absolute path", name)
	}
	return path
}
