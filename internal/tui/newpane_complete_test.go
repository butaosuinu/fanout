package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func openCompletionModel(t *testing.T, files []string) model {
	t.Helper()
	m := newModel(Options{ProjectRoot: "/repo"})
	m.openNewPaneForm()
	m.repoFiles = files
	m.repoFileIndex = buildFileIndex(files)
	m.repoFilesLoaded = true
	return m
}

func applyMsg(m model, msg tea.Msg) model {
	updated, _ := m.Update(msg)
	return updated.(model)
}

func TestPromptCompletionTriggersAtWordBoundary(t *testing.T) {
	files := []string{"cmd/main.go", "internal/tui/newpane.go"}

	m := openCompletionModel(t, files)
	m = applyMsg(m, keyRunes("@"))
	if !m.newPane.completing {
		t.Fatalf("expected completion to start after '@' at line start")
	}

	m2 := openCompletionModel(t, files)
	m2 = applyMsg(m2, keyRunes("a"))
	m2 = applyMsg(m2, keyRunes("@"))
	if m2.newPane.completing {
		t.Fatalf("mid-word '@' (a@) must not trigger completion")
	}
}

func TestPromptCompletionAcceptInsertsAtMentionPath(t *testing.T) {
	files := []string{"cmd/main.go", "internal/tui/newpane.go"}
	m := openCompletionModel(t, files)

	for _, r := range []string{"@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	if len(m.newPane.compResults) == 0 || m.newPane.compResults[0] != "internal/tui/newpane.go" {
		t.Fatalf("expected newpane.go ranked first, got %v", m.newPane.compResults)
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	got := m.newPane.prompt.Value()
	if want := "@internal/tui/newpane.go "; !strings.HasSuffix(got, want) {
		t.Fatalf("prompt = %q, want suffix %q", got, want)
	}
	if m.newPane.completing {
		t.Fatalf("completion should close after accept")
	}
}

func TestPromptCompletionEscCancelsOnlyCompletion(t *testing.T) {
	m := openCompletionModel(t, []string{"cmd/main.go"})
	m = applyMsg(m, keyRunes("@"))
	if !m.newPane.completing {
		t.Fatal("expected completing after '@'")
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.newPane.completing {
		t.Fatal("esc should cancel completion")
	}
	if m.mode != modeNewPane {
		t.Fatalf("esc with popup open must NOT close modal, mode = %v", m.mode)
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != modeMonitor {
		t.Fatalf("second esc should close modal, mode = %v", m.mode)
	}
}

func TestPromptCompletionCursorMoveLeavesCompletion(t *testing.T) {
	m := openCompletionModel(t, []string{"cmd/main.go"})
	m = applyMsg(m, keyRunes("@"))
	m = applyMsg(m, keyRunes("m"))
	if !m.newPane.completing {
		t.Fatal("expected completing after '@m'")
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.newPane.completing {
		t.Fatal("left arrow should leave completion")
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.newPane.prompt.Value(), "cmd/main.go") {
		t.Fatalf("enter after leaving completion must not insert a path: %q", m.newPane.prompt.Value())
	}
}

func TestPromptCompletionBackspaceOverAtExits(t *testing.T) {
	m := openCompletionModel(t, []string{"cmd/main.go"})
	m = applyMsg(m, keyRunes("@"))
	m = applyMsg(m, keyRunes("n"))

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyBackspace}) // deletes 'n'; query empty, still completing
	if !m.newPane.completing {
		t.Fatal("still completing after deleting last query char")
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyBackspace}) // deletes '@'; exits
	if m.newPane.completing {
		t.Fatal("backspace over '@' should exit completion")
	}
	if strings.Contains(m.newPane.prompt.Value(), "@") {
		t.Fatalf("prompt should no longer contain '@': %q", m.newPane.prompt.Value())
	}
}

func TestRankRepoFiles(t *testing.T) {
	files := []string{
		"internal/tui/newpane.go",
		"internal/tui/new_test.go",
		"cmd/fanout/newpane.go",
		"docs/news.md",
		"cmd/main.go",
	}
	cases := []struct {
		query string
		want  []string
	}{
		{
			// all basename-prefix matches -> shorter path first
			query: "new",
			want:  []string{"docs/news.md", "cmd/fanout/newpane.go", "internal/tui/newpane.go", "internal/tui/new_test.go"},
		},
		{
			// basename substring (not prefix) -> shorter path first
			query: "pane",
			want:  []string{"cmd/fanout/newpane.go", "internal/tui/newpane.go"},
		},
		{
			// path substring only
			query: "tui",
			want:  []string{"internal/tui/newpane.go", "internal/tui/new_test.go"},
		},
	}
	for _, tc := range cases {
		got, total := rankRepoFiles(files, tc.query, 8)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("rankRepoFiles(%q) = %v, want %v", tc.query, got, tc.want)
		}
		if total != len(tc.want) {
			t.Fatalf("rankRepoFiles(%q) total = %d, want %d", tc.query, total, len(tc.want))
		}
	}
}

func TestRankRepoFilesCapsResultsButCountsTotal(t *testing.T) {
	files := []string{"a/x.go", "b/x.go", "c/x.go", "d/x.go", "e/x.go"}
	got, total := rankRepoFiles(files, "x", 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 capped results, got %d (%v)", len(got), got)
	}
	if total != 5 {
		t.Fatalf("expected total 5, got %d", total)
	}
}

func TestPromptCompletionEnterSubmitsWhenNoResults(t *testing.T) {
	var launched bool
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchPane: func(LaunchRequest) (string, error) {
			launched = true
			return "", nil
		},
	})
	m.openNewPaneForm()
	m.repoFiles = []string{"cmd/main.go"}
	m.repoFileIndex = buildFileIndex(m.repoFiles)
	m.repoFilesLoaded = true

	for _, r := range []string{"d", "o", " ", "@", "z", "z", "z"} { // '@zzz' has no match
		m = applyMsg(m, keyRunes(r))
	}
	if len(m.newPane.compResults) != 0 {
		t.Fatalf("expected no results for '@zzz', got %v", m.newPane.compResults)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if m.newPane.completing {
		t.Fatal("enter with no results should dismiss the popup")
	}
	if cmd == nil {
		t.Fatal("enter with no results should submit the form in one press")
	}
	_ = cmd()
	if !launched {
		t.Fatal("expected submit to invoke LaunchPane")
	}
}

func TestPromptCompletionResetsIndexOnQueryEdit(t *testing.T) {
	files := []string{"a/alpha.go", "a/album.go", "a/alabama.go", "a/alarm.go"}
	m := openCompletionModel(t, files)

	m = applyMsg(m, keyRunes("@"))
	m = applyMsg(m, keyRunes("al"))
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.newPane.compIndex == 0 {
		t.Fatal("expected non-zero compIndex after navigating down twice")
	}

	m = applyMsg(m, keyRunes("a")) // edit query -> re-rank
	if m.newPane.compIndex != 0 {
		t.Fatalf("compIndex should reset to 0 on query edit, got %d", m.newPane.compIndex)
	}
}

func TestPromptCompletionSecondAtEndsCompletion(t *testing.T) {
	m := openCompletionModel(t, []string{"cmd/main.go"})
	m = applyMsg(m, keyRunes("@"))
	if !m.newPane.completing {
		t.Fatal("expected completing after first '@'")
	}
	m = applyMsg(m, keyRunes("@"))
	if m.newPane.completing {
		t.Fatal("a second '@' should end completion, not build an unmatchable query")
	}
}

func TestRepoFilesLoadRetriesAfterError(t *testing.T) {
	calls := 0
	m := newModel(Options{
		ProjectRoot: "/repo",
		ListRepoFiles: func(string) ([]string, error) {
			calls++
			if calls == 1 {
				return nil, errBoom
			}
			return []string{"a.go"}, nil
		},
	})

	// first open -> load fails
	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	m = applyMsg(m, cmd())
	if m.repoFilesLoaded {
		t.Fatal("a failed load must not mark repoFilesLoaded")
	}
	if m.repoFilesErr == "" {
		t.Fatal("a failed load should record repoFilesErr")
	}

	// reopen -> retries and succeeds
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	updated, cmd2 := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd2 == nil {
		t.Fatal("expected a retry load command after a prior failure")
	}
	m = applyMsg(m, cmd2())
	if !m.repoFilesLoaded || len(m.repoFiles) != 1 {
		t.Fatalf("retry should load files: loaded=%v files=%v", m.repoFilesLoaded, m.repoFiles)
	}
	if calls != 2 {
		t.Fatalf("ListRepoFiles called %d times, want 2 (initial + retry)", calls)
	}
}

func TestAcceptCompletionPreservesPrecedingText(t *testing.T) {
	m := openCompletionModel(t, []string{"internal/tui/newpane.go"})
	for _, r := range []string{"f", "i", "x", " ", "@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.newPane.prompt.Value(), "fix @internal/tui/newpane.go "; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestNewPaneViewRendersCompletionPopup(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 40
	m.openNewPaneForm()
	m.repoFiles = []string{"internal/tui/newpane.go", "cmd/main.go"}
	m.repoFileIndex = buildFileIndex(m.repoFiles)
	m.repoFilesLoaded = true

	m = applyMsg(m, keyRunes("@"))
	m = applyMsg(m, keyRunes("n"))

	view := m.newPaneView()
	if !strings.Contains(view, "newpane.go") {
		t.Fatalf("completion popup should list a candidate path:\n%s", view)
	}
}

func TestRepoFilesLoadedOncePerProcess(t *testing.T) {
	calls := 0
	m := newModel(Options{
		ProjectRoot: "/repo",
		ListRepoFiles: func(string) ([]string, error) {
			calls++
			return []string{"a.go"}, nil
		},
	})

	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected a load command on first form open")
	}
	m = applyMsg(m, cmd()) // runs ListRepoFiles, delivers filesLoadedMsg

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc}) // close form
	updated, cmd2 := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd2 != nil {
		t.Fatal("expected no load command on reopen")
	}
	if calls != 1 {
		t.Fatalf("ListRepoFiles called %d times, want 1", calls)
	}
	if !m.repoFilesLoaded || len(m.repoFiles) != 1 {
		t.Fatalf("repo files not cached: loaded=%v files=%v", m.repoFilesLoaded, m.repoFiles)
	}
}
