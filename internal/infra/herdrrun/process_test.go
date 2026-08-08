package herdrrun

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestInspectPaneProcessRelationsReadsCurrentProcess(t *testing.T) {
	pid := os.Getpid()
	processes, err := inspectPaneProcessRelations(context.Background(), []PaneProcess{{PID: pid}})
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 1 || processes[0].PID != pid || processes[0].ParentPID <= 0 ||
		processes[0].ProcessGroup <= 1 || processes[0].Executable == "" {
		t.Fatalf("current process relation = %+v", processes)
	}
}

func TestParseAndBindProcessRelations(t *testing.T) {
	relations, err := parseProcessRelations("42 1 42 /usr/bin/node\n43 42 42 /opt/lib/codex\n")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindProcessRelations([]PaneProcess{{PID: 42}, {PID: 43}}, relations)
	if err != nil {
		t.Fatal(err)
	}
	if bound[0].ParentPID != 1 || bound[0].ProcessGroup != 42 || bound[0].Executable != "/usr/bin/node" {
		t.Fatalf("root relation = %+v", bound[0])
	}
	if bound[1].ParentPID != 42 || bound[1].ProcessGroup != 42 || bound[1].Executable != "/opt/lib/codex" {
		t.Fatalf("child relation = %+v", bound[1])
	}
}

func TestProcessRelationInspectionFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "duplicate pane pid",
			run: func() error {
				_, err := uniquePaneProcessIDs([]PaneProcess{{PID: 42}, {PID: 42}})
				return err
			},
		},
		{
			name: "missing ps row",
			run: func() error {
				_, err := bindProcessRelations([]PaneProcess{{PID: 42}}, map[int]processRelation{})
				return err
			},
		},
		{
			name: "duplicate ps row",
			run: func() error {
				_, err := parseProcessRelations("42 1 42 node\n42 1 42 node\n")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil || !strings.Contains(err.Error(), "pid") && !strings.Contains(err.Error(), "disappeared") {
				t.Fatalf("error = %v, want fail-closed process identity error", err)
			}
		})
	}
}

func TestMatchAgentProcessReturnsExactDirectAndInterpreterIdentity(t *testing.T) {
	direct := PaneProcessInfo{
		ShellPID: 42, ForegroundProcessGroup: 42,
		ForegroundProcesses: []PaneProcess{{
			PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: "/opt/codex",
			CWD: "/repo", Argv0: "/opt/codex", Argv: []string{"resume", "session-a"},
		}},
	}
	identity, err := MatchAgentProcess(direct, "/opt/codex", []string{"resume", "session-a"}, "/repo")
	if err != nil || identity.ShellPID != 42 || identity.ForegroundProcessGroup != 42 || identity.AgentPID != 42 {
		t.Fatalf("direct identity = %+v, %v", identity, err)
	}

	interpreter := direct
	interpreter.ForegroundProcesses = []PaneProcess{
		{
			PID: 42, ParentPID: 1, ProcessGroup: 42, Executable: "/usr/bin/node",
			CWD: "/repo", Argv0: "/usr/bin/node", Argv: []string{"/opt/codex", "resume", "session-a"},
		},
		{
			PID: 43, ParentPID: 42, ProcessGroup: 42, Executable: "/opt/lib/codex",
			CWD: "/repo", Argv0: "/opt/lib/codex", Argv: []string{"resume", "session-a"},
		},
	}
	identity, err = MatchAgentProcess(interpreter, "/opt/codex", []string{"resume", "session-a"}, "/repo")
	if err != nil || identity.AgentPID != 43 {
		t.Fatalf("interpreter identity = %+v, %v", identity, err)
	}
}
