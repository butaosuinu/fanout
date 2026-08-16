package herdrrun

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

func TestNormalizePaneProcessArgsSeparatesRawExecutable(t *testing.T) {
	tests := []struct {
		name string
		raw  corebackend.PaneProcess
		args []string
	}{
		{name: "launcher", raw: corebackend.PaneProcess{Argv0: "fanout", Argv: []string{"/opt/fanout"}}},
		{name: "codex", raw: corebackend.PaneProcess{
			Argv0: "codex", Argv: []string{"/opt/codex", "resume", "session-id"},
		}, args: []string{"resume", "session-id"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []corebackend.PaneProcess{test.raw}
			got, err := normalizePaneProcessArgs(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got[0].Argv0 != test.raw.Argv[0] || !slices.Equal(got[0].Argv, test.args) {
				t.Fatalf("normalized process = %+v", got[0])
			}
			if raw[0].Argv0 != test.raw.Argv0 || !slices.Equal(raw[0].Argv, test.raw.Argv) {
				t.Fatalf("raw process was mutated: %+v", raw[0])
			}
		})
	}
}

func TestNormalizePaneProcessArgsRejectsInconsistentArgv0(t *testing.T) {
	for _, process := range []corebackend.PaneProcess{
		{Argv0: "codex"},
		{Argv0: "claude", Argv: []string{"/opt/codex", "resume", "session-id"}},
	} {
		if _, err := normalizePaneProcessArgs([]corebackend.PaneProcess{process}); err == nil {
			t.Fatalf("inconsistent process accepted: %+v", process)
		}
	}
}

func TestInspectPaneProcessRelationsReadsCurrentProcess(t *testing.T) {
	pid := os.Getpid()
	processes, err := inspectPaneProcessRelations(context.Background(), []corebackend.PaneProcess{{PID: pid}})
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
	bound, err := bindProcessRelations([]corebackend.PaneProcess{{PID: 42}, {PID: 43}}, relations)
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
				_, err := uniquePaneProcessIDs([]corebackend.PaneProcess{{PID: 42}, {PID: 42}})
				return err
			},
		},
		{
			name: "missing ps row",
			run: func() error {
				_, err := bindProcessRelations([]corebackend.PaneProcess{{PID: 42}}, map[int]processRelation{})
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
