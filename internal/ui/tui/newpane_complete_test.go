package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	files := []string{"cmd/main.go", "internal/ui/tui/newpane.go"}

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
	files := []string{"cmd/main.go", "internal/ui/tui/newpane.go"}
	m := openCompletionModel(t, files)

	for _, r := range []string{"@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	if len(m.newPane.compResults) == 0 || m.newPane.compResults[0] != "internal/ui/tui/newpane.go" {
		t.Fatalf("expected newpane.go ranked first, got %v", m.newPane.compResults)
	}

	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	got := m.newPane.prompt.Value()
	if want := "@internal/ui/tui/newpane.go "; !strings.HasSuffix(got, want) {
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
		"internal/ui/tui/newpane.go",
		"internal/ui/tui/new_test.go",
		"cmd/fanout/newpane.go",
		"docs/news.md",
		"cmd/main.go",
	}
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{
			name:  "basename-prefix matches rank by path length",
			query: "new",
			want:  []string{"docs/news.md", "cmd/fanout/newpane.go", "internal/ui/tui/newpane.go", "internal/ui/tui/new_test.go"},
		},
		{
			name:  "basename substring matches rank by path length",
			query: "pane",
			want:  []string{"cmd/fanout/newpane.go", "internal/ui/tui/newpane.go"},
		},
		{
			name:  "path substring matches",
			query: "tui",
			want:  []string{"internal/ui/tui/newpane.go", "internal/ui/tui/new_test.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, total := rankRepoFiles(files, tt.query, 8)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rankRepoFiles(%q) = %v, want %v", tt.query, got, tt.want)
			}
			if total != len(tt.want) {
				t.Fatalf("rankRepoFiles(%q) total = %d, want %d", tt.query, total, len(tt.want))
			}
		})
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
	m := openCompletionModel(t, []string{"internal/ui/tui/newpane.go"})
	for _, r := range []string{"f", "i", "x", " ", "@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.newPane.prompt.Value(), "fix @internal/ui/tui/newpane.go "; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestAcceptCompletionRefusesWhenOverCharLimit(t *testing.T) {
	m := openCompletionModel(t, []string{"internal/ui/tui/newpane.go"})
	// Fill the prompt to just under CharLimit, then start an @-token.
	limit := m.newPane.prompt.CharLimit
	m.newPane.prompt.SetValue(strings.Repeat("x", limit-3) + " ")
	for _, r := range []string{"@", "n"} {
		m = applyMsg(m, keyRunes(r))
	}
	if len(m.newPane.compResults) == 0 {
		t.Fatal("expected a candidate for '@n'")
	}

	before := m.newPane.prompt.Value()
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.newPane.prompt.Value() != before {
		t.Fatalf("over-limit accept must not modify the prompt:\n  before=%q\n  after =%q", before, m.newPane.prompt.Value())
	}
	if m.newPane.err == "" {
		t.Fatal("over-limit accept should surface an error")
	}
	if !strings.Contains(m.newPane.prompt.Value(), "@n") {
		t.Fatal("the typed @-token should remain intact after a refused accept")
	}
}

func TestPromptCompletionEnterDuringLoadDoesNotSubmit(t *testing.T) {
	var launched bool
	m := newModel(Options{
		ProjectRoot: "/repo",
		LaunchPane:  func(LaunchRequest) (string, error) { launched = true; return "", nil },
	})
	m.openNewPaneForm()
	// repoFilesLoaded stays false (still loading); no index set.
	for _, r := range []string{"d", "o", " ", "@", "x"} {
		m = applyMsg(m, keyRunes(r))
	}
	if !m.newPane.completing {
		t.Fatal("expected completing during load")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if launched || cmd != nil {
		t.Fatal("enter while the file list is loading must not submit")
	}
	if !m.newPane.completing {
		t.Fatal("enter while loading should keep the popup open")
	}
}

func TestAcceptCompletionHandlesFullWidthPrefix(t *testing.T) {
	// A full-width rune before the @token exercises the rune-index path math:
	// bubbles LineInfo.StartColumn+ColumnOffset is a rune offset, so the token
	// is found and replaced correctly despite the double-width character.
	m := openCompletionModel(t, []string{"internal/ui/tui/newpane.go"})
	for _, r := range []string{"あ", " ", "@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.newPane.prompt.Value(), "あ @internal/ui/tui/newpane.go "; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestFileIndexExcludesSpacePaths(t *testing.T) {
	idx := buildFileIndex([]string{"docs/readme.md", "docs/my file.md", "a\tb.go"})
	for _, e := range idx {
		if strings.ContainsAny(e.path, " \t") {
			t.Fatalf("whitespace path should be excluded from candidates: %q", e.path)
		}
	}
	top, total := rankFileEntries(idx, "", 8)
	if total != 1 || len(top) != 1 || top[0] != "docs/readme.md" {
		t.Fatalf("only the clean path should remain, got %v (total %d)", top, total)
	}
}

func TestAcceptCompletionRefusesOverCharLimitFullWidth(t *testing.T) {
	m := openCompletionModel(t, []string{"internal/ui/tui/newpane.go"})
	// Full-width fill: rune count (499) is far under CharLimit but display width
	// (~997) is near it, so a rune-count guard would wrongly allow the insert.
	m.newPane.prompt.SetValue(strings.Repeat("あ", 498) + " ")
	for _, r := range []string{"@", "n"} {
		m = applyMsg(m, keyRunes(r))
	}
	if len(m.newPane.compResults) == 0 {
		t.Fatal("expected a candidate for '@n'")
	}
	before := m.newPane.prompt.Value()
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.newPane.prompt.Value() != before {
		t.Fatal("over-limit (display-width) accept must not modify the prompt")
	}
	if m.newPane.err == "" {
		t.Fatal("expected an over-limit error for a full-width prompt")
	}
}

func TestAcceptCompletionFullWidthPrefixWithTrailingText(t *testing.T) {
	// Full-width rune before the token AND text after the cursor: exercises the
	// rune-index path math (StartColumn+ColumnOffset is a rune offset).
	m := openCompletionModel(t, []string{"internal/ui/tui/newpane.go"})
	for _, r := range []string{"あ", " ", "t", "a", "i", "l"} {
		m = applyMsg(m, keyRunes(r))
	}
	for range 4 { // move the cursor back to just after "あ "
		m = applyMsg(m, tea.KeyMsg{Type: tea.KeyLeft})
	}
	for _, r := range []string{"@", "n", "e", "w"} {
		m = applyMsg(m, keyRunes(r))
	}
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got, want := m.newPane.prompt.Value(), "あ @internal/ui/tui/newpane.go tail"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestFileIndexExcludesNewlinePaths(t *testing.T) {
	idx := buildFileIndex([]string{"clean.go", "weird\nname.go", "cr\rfile.go"})
	if len(idx) != 1 || idx[0].path != "clean.go" {
		t.Fatalf("only clean.go should survive newline/CR exclusion, got %v", idx)
	}
}

func TestTruncatePathTailUsesDisplayWidth(t *testing.T) {
	// 10 full-width runes = 20 display columns; a 12-column limit must truncate
	// even though the rune count (10) is under the limit.
	out := truncatePathTail(strings.Repeat("あ", 10), 12)
	if w := lipgloss.Width(out); w > 12 {
		t.Fatalf("truncated display width = %d, want <= 12 (%q)", w, out)
	}
	if !strings.HasPrefix(out, "…") {
		t.Fatalf("expected an ellipsis prefix, got %q", out)
	}
}

func TestNewPaneViewRendersCompletionPopup(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 40
	m.openNewPaneForm()
	m.repoFiles = []string{"internal/ui/tui/newpane.go", "cmd/main.go"}
	m.repoFileIndex = buildFileIndex(m.repoFiles)
	m.repoFilesLoaded = true

	m = applyMsg(m, keyRunes("@"))
	m = applyMsg(m, keyRunes("n"))

	view := m.newPaneView()
	if !strings.Contains(view, "newpane.go") {
		t.Fatalf("completion popup should list a candidate path:\n%s", view)
	}
}

func TestRepoFilesReloadOnEachFormOpen(t *testing.T) {
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

	// Reopening refreshes the list so a long-lived TUI sees base-branch changes;
	// the prior result stays cached and visible while the refresh runs.
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc}) // close form
	if !m.repoFilesLoaded || len(m.repoFiles) != 1 {
		t.Fatalf("cached files lost after close: loaded=%v files=%v", m.repoFilesLoaded, m.repoFiles)
	}
	updated, cmd2 := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd2 == nil {
		t.Fatal("expected a refresh load command on reopen")
	}
	m = applyMsg(m, cmd2())
	if calls != 2 {
		t.Fatalf("ListRepoFiles called %d times, want 2 (one per form open)", calls)
	}
	if !m.repoFilesLoaded || len(m.repoFiles) != 1 {
		t.Fatalf("repo files not cached after reload: loaded=%v files=%v", m.repoFilesLoaded, m.repoFiles)
	}
}

func TestRepoFilesReloadSkippedWhileLoadInFlight(t *testing.T) {
	calls := 0
	m := newModel(Options{
		ProjectRoot: "/repo",
		ListRepoFiles: func(string) ([]string, error) {
			calls++
			return []string{"a.go"}, nil
		},
	})

	// First open kicks a load but the filesLoadedMsg has not arrived yet
	// (repoFilesLoading stays true). Reopening must not stack a second git call.
	updated, cmd := m.Update(keyRunes("n"))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected a load command on first form open")
	}
	m = applyMsg(m, tea.KeyMsg{Type: tea.KeyEsc})
	_, cmd2 := m.Update(keyRunes("n"))
	if cmd2 != nil {
		t.Fatal("expected no second load command while one is already in flight")
	}
}
