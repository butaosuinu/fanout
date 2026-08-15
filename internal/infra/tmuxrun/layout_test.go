package tmuxrun

import (
	"os"
	"strings"
	"testing"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

// printArgsShim records argv and prints stdout via the given printf body.
func printArgsShim(t *testing.T, stdout string) string {
	t.Helper()
	script := `printf '%s\n' "$@" > "$TMUXRUN_ARGS"
` + stdout + "\n"
	return installTmuxShim(t, script)
}

func TestWindowGeometryParsesAndTargets(t *testing.T) {
	argsPath := printArgsShim(t, `printf '@5\t208\t60\n'`)
	geom, err := WindowGeometry("%1")
	if err != nil {
		t.Fatalf("WindowGeometry: %v", err)
	}
	if geom != (corebackend.Geometry{WindowID: "@5", Width: 208, Height: 60}) {
		t.Fatalf("geom = %+v", geom)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%1", "-F", windowGeomFormat})
}

func TestWindowGeometryRejectsMalformedOutput(t *testing.T) {
	printArgsShim(t, `printf 'not-three-fields\n'`)
	if _, err := WindowGeometry("%1"); err == nil {
		t.Fatal("expected error for malformed geometry output")
	}
}

func TestWindowOfPane(t *testing.T) {
	argsPath := printArgsShim(t, `printf '@9\n'`)
	got, err := WindowOfPane("%3")
	if err != nil {
		t.Fatalf("WindowOfPane: %v", err)
	}
	if got != "@9" {
		t.Fatalf("WindowOfPane = %q, want @9", got)
	}
	assertTmuxArgs(t, argsPath, []string{"display-message", "-p", "-t", "%3", "#{window_id}"})
}

func TestApplyLayoutPassesLayoutAsSingleArg(t *testing.T) {
	argsPath := printArgsShim(t, "")
	layout := "fab2,200x50,0,0{40x50,0,0,1,159x50,41,0{79x50,41,0,2,79x50,121,0,3}}"
	if err := ApplyLayout("@5", layout); err != nil {
		t.Fatalf("ApplyLayout: %v", err)
	}
	got := readTmuxArgs(t, argsPath)
	want := []string{"select-layout", "-t", "@5", layout}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	// The comma/brace-laden layout must arrive as exactly one argv element.
	if len(got) != 4 || got[3] != layout {
		t.Fatalf("layout string was split: %#v", got)
	}
}

func TestApplyLayoutRequiresLayout(t *testing.T) {
	printArgsShim(t, "")
	if err := ApplyLayout("@5", "  "); err == nil {
		t.Fatal("expected error for empty layout string")
	}
}

func TestSelectMainVerticalOrdersCommands(t *testing.T) {
	script := `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf -- '---\n' >> "$TMUXRUN_ARGS"
`
	argsPath := installTmuxShim(t, script)
	if err := SelectMainVertical("@5", 40); err != nil {
		t.Fatalf("SelectMainVertical: %v", err)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimRight(string(body), "\n"), "---")
	if len(calls) < 2 {
		t.Fatalf("expected two tmux calls, got %q", string(body))
	}
	if !strings.Contains(calls[0], "set-window-option") || !strings.Contains(calls[0], "main-pane-width") || !strings.Contains(calls[0], "40") {
		t.Errorf("first call not set-window-option main-pane-width 40: %q", calls[0])
	}
	if !strings.Contains(calls[1], "select-layout") || !strings.Contains(calls[1], "main-vertical") {
		t.Errorf("second call not select-layout main-vertical: %q", calls[1])
	}
}

func TestSetPaneRoleSetAndClear(t *testing.T) {
	argsPath := printArgsShim(t, "")
	if err := SetPaneRole("%2", corebackend.RoleConsole); err != nil {
		t.Fatalf("SetPaneRole set: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"set-option", "-p", "-t", "%2", roleOption, "console"})

	argsPath = printArgsShim(t, "")
	if err := SetPaneRole("%2", ""); err != nil {
		t.Fatalf("SetPaneRole clear: %v", err)
	}
	assertTmuxArgs(t, argsPath, []string{"set-option", "-pu", "-t", "%2", roleOption})
}

func TestSplitSpacerPane(t *testing.T) {
	// Two tmux calls (split-window, then set-option marker); record both.
	script := `printf '%s\n' "$@" >> "$TMUXRUN_ARGS"
printf -- '---\n' >> "$TMUXRUN_ARGS"
printf '%%77\n'
`
	argsPath := installTmuxShim(t, script)
	id, err := SplitSpacerPane("@5")
	if err != nil {
		t.Fatalf("SplitSpacerPane: %v", err)
	}
	if id != "%77" {
		t.Fatalf("id = %q, want %%77", id)
	}
	body, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimRight(string(body), "\n"), "---")
	if len(calls) < 2 {
		t.Fatalf("expected split + set-option calls, got %q", string(body))
	}
	split := strings.Fields(calls[0])
	wantSplit := []string{"split-window", "-t", "@5", "-d", "-h", "-P", "-F", "#{pane_id}", spacerIdleCommand()}
	// spacerIdleCommand has spaces, so compare the prefix args and the trailing command separately.
	if strings.Join(split[:len(wantSplit)-1], "\x00") != strings.Join(wantSplit[:len(wantSplit)-1], "\x00") {
		t.Errorf("split args = %#v", calls[0])
	}
	if !strings.Contains(calls[0], "split-window") || !strings.Contains(calls[0], "#{pane_id}") {
		t.Errorf("first call not a split-window: %q", calls[0])
	}
	// The marker is stamped from the parent against the explicit pane id.
	if !strings.Contains(calls[1], "set-option") || !strings.Contains(calls[1], "%77") || !strings.Contains(calls[1], spacerOption) {
		t.Errorf("second call did not stamp %s on %%77: %q", spacerOption, calls[1])
	}
}

func TestWindowPanesParsing(t *testing.T) {
	argsPath := printArgsShim(t, `printf '%%1\t0\t1\tconsole\t\n%%2\t1\t0\t\t\n%%3\t2\t0\t\t1\n'`)
	panes, err := WindowPanes("@5")
	if err != nil {
		t.Fatalf("WindowPanes: %v", err)
	}
	want := []corebackend.WindowPane{
		{ID: "%1", NumericID: "1", Index: 0, Active: true, Role: "console", Spacer: false},
		{ID: "%2", NumericID: "2", Index: 1, Active: false, Role: "", Spacer: false},
		{ID: "%3", NumericID: "3", Index: 2, Active: false, Role: "", Spacer: true},
	}
	if len(panes) != len(want) {
		t.Fatalf("got %d panes, want %d (%+v)", len(panes), len(want), panes)
	}
	for i := range want {
		if panes[i] != want[i] {
			t.Errorf("pane %d = %+v, want %+v", i, panes[i], want[i])
		}
	}
	assertTmuxArgs(t, argsPath, []string{"list-panes", "-t", "@5", "-F", windowPaneFormat})
}

func TestWindowPanesRejectsMalformedPaneID(t *testing.T) {
	printArgsShim(t, `printf 'bogus\t0\t1\t\t\n'`)
	if _, err := WindowPanes("@5"); err == nil {
		t.Fatal("expected error for malformed pane id")
	}
}
