package herdrprocess

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

const (
	testWorktree = "/repo/worktree"
	testFanout   = "/opt/fanout"
	testCodex    = "/opt/codex"
)

func TestVerifyAgentAcceptsExactCodexPlanController(t *testing.T) {
	identity, info := codexPlanProcessFixture()
	if err := VerifyAgent(info, identity); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentAcceptsCodexPlanInterpreterChain(t *testing.T) {
	identity, info := codexPlanProcessFixture()
	remoteArgs := append([]string{}, info.ForegroundProcesses[1].Argv...)
	remoteArgs = append(remoteArgs, "resume", "thread-554")
	info.ForegroundProcesses[1] = testProcess(20, 10, "/usr/bin/node", "/usr/bin/node", append([]string{testCodex}, remoteArgs...))
	info.ForegroundProcesses = append(info.ForegroundProcesses, testProcess(21, 20, "/opt/lib/codex", "/opt/lib/codex", remoteArgs))
	if err := VerifyAgent(info, identity); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAgentRejectsInexactCodexPlanProcessTrees(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*herdrrun.PaneProcessInfo)
	}{
		{name: "missing TUI", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses = info.ForegroundProcesses[:1]
		}},
		{name: "duplicate TUI", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses = append(info.ForegroundProcesses, testProcess(
				21, 10, testCodex, testCodex, []string{"--remote", "ws://127.0.0.1:1234"},
			))
		}},
		{name: "nested duplicate TUI", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses = append(info.ForegroundProcesses, testProcess(
				21, 20, testCodex, testCodex, []string{"--remote", "ws://127.0.0.1:1234"},
			))
		}},
		{name: "wrong remote host", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[1].Argv[1] = "ws://localhost:1234"
		}},
		{name: "extra TUI argument", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[1].Argv = append(info.ForegroundProcesses[1].Argv, "--dangerously-bypass-approvals-and-sandbox")
		}},
		{name: "indirect TUI root", mutate: func(info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[1].ParentPID = 19
			info.ForegroundProcesses = append(info.ForegroundProcesses, testProcess(19, 10, "/bin/sh", "/bin/sh", nil))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, info := codexPlanProcessFixture()
			test.mutate(&info)
			if err := VerifyAgent(info, identity); err == nil {
				t.Fatal("VerifyAgent() accepted an inexact Codex Plan process tree")
			}
		})
	}
}

func TestVerifyAgentRejectsDuplicateCodexPlanExecutableBinding(t *testing.T) {
	identity, info := codexPlanProcessFixture()
	identity.Args = append(identity.Args, "--codex", "/other/codex")
	info.ForegroundProcesses[0].Argv = identity.Args
	if err := VerifyAgent(info, identity); err == nil {
		t.Fatal("VerifyAgent() accepted duplicate --codex launch identity")
	}
}

func TestMatchAgentReturnsExactCodexResumeProcessIdentity(t *testing.T) {
	identity := Identity{
		WorktreePath: testWorktree, Executable: testCodex,
		Args: []string{"resume", "019f-session"}, Agent: "codex",
	}
	info := herdrrun.PaneProcessInfo{
		ShellPID: 10, ForegroundProcessGroup: 99,
		ForegroundProcesses: []herdrrun.PaneProcess{
			testProcess(10, 1, "/usr/bin/node", "/usr/bin/node", []string{testCodex, "resume", "019f-session"}),
			testProcess(20, 10, "/opt/lib/codex", "/opt/lib/codex", []string{"resume", "019f-session"}),
		},
	}

	got, err := MatchAgent(info, identity)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShellPID != 10 || got.ForegroundProcessGroup != 99 || got.AgentPID != 20 {
		t.Fatalf("process identity = %+v", got)
	}
}

func TestMatchAgentRejectsInexactCodexResumeProcess(t *testing.T) {
	baseIdentity := Identity{
		WorktreePath: testWorktree, Executable: testCodex,
		Args: []string{"resume", "019f-session"}, Agent: "codex",
	}
	baseInfo := herdrrun.PaneProcessInfo{
		ShellPID: 10, ForegroundProcessGroup: 99,
		ForegroundProcesses: []herdrrun.PaneProcess{
			testProcess(10, 1, testCodex, testCodex, []string{"resume", "019f-session"}),
		},
	}
	for _, test := range []struct {
		name   string
		mutate func(*Identity, *herdrrun.PaneProcessInfo)
	}{
		{name: "extra arg", mutate: func(_ *Identity, info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[0].Argv = append(info.ForegroundProcesses[0].Argv, "--full-auto")
		}},
		{name: "wrong cwd", mutate: func(_ *Identity, info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[0].CWD = "/repo/other"
		}},
		{name: "wrong process group", mutate: func(_ *Identity, info *herdrrun.PaneProcessInfo) {
			info.ForegroundProcesses[0].ProcessGroup = 100
		}},
		{name: "relative saved executable", mutate: func(identity *Identity, _ *herdrrun.PaneProcessInfo) {
			identity.Executable = "codex"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, info := baseIdentity, baseInfo
			info.ForegroundProcesses = append([]herdrrun.PaneProcess(nil), baseInfo.ForegroundProcesses...)
			info.ForegroundProcesses[0].Argv = append([]string(nil), baseInfo.ForegroundProcesses[0].Argv...)
			test.mutate(&identity, &info)
			if _, err := MatchAgent(info, identity); err == nil {
				t.Fatal("MatchAgent() accepted an inexact resume process")
			}
		})
	}
}

func codexPlanProcessFixture() (Identity, herdrrun.PaneProcessInfo) {
	args := []string{
		codexapp.PlanTUICommand, "--codex", testCodex,
		"--prompt", "plan it", "--status-file", "/tmp/status.json",
	}
	identity := Identity{WorktreePath: testWorktree, Executable: testFanout, Args: args, Agent: "codex"}
	info := herdrrun.PaneProcessInfo{
		ShellPID: 10, ForegroundProcessGroup: 99,
		ForegroundProcesses: []herdrrun.PaneProcess{
			testProcess(10, 1, testFanout, testFanout, args),
			testProcess(20, 10, testCodex, testCodex, []string{"--remote", "ws://127.0.0.1:1234"}),
		},
	}
	return identity, info
}

func testProcess(pid, parent int, executable, argv0 string, args []string) herdrrun.PaneProcess {
	return herdrrun.PaneProcess{
		PID: pid, ParentPID: parent, ProcessGroup: 99,
		Executable: executable, Argv0: argv0, Argv: args, CWD: testWorktree,
	}
}
