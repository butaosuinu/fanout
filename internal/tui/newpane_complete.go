package tui

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// completionMax caps how many file matches the @-completion popup shows at once.
const completionMax = 8

// filesLoadedMsg delivers the one-time repository file listing used by the
// prompt field's @-mention completion.
type filesLoadedMsg struct {
	files []string
	err   error
}

// fileEntry caches the lowercase path/basename so ranking does not re-lowercase
// the whole repo on every keystroke.
type fileEntry struct {
	path      string
	lowerPath string
	lowerBase string
}

func buildFileIndex(files []string) []fileEntry {
	idx := make([]fileEntry, 0, len(files))
	for _, f := range files {
		// A whitespace-containing path cannot be a single @file mention (the
		// mention ends at the first space), so completing it would silently
		// yield a dead reference. Drop it from the candidates.
		if strings.ContainsAny(f, " \t") {
			continue
		}
		idx = append(idx, fileEntry{
			path:      f,
			lowerPath: strings.ToLower(f),
			lowerBase: strings.ToLower(path.Base(f)),
		})
	}
	return idx
}

// maybeLoadRepoFilesCmd returns a command that loads the repository file list.
// It is a no-op once a load has succeeded or is in flight, so reopening the
// new-pane form never re-runs git; a failed load leaves both gates clear so the
// next open retries.
func (m *model) maybeLoadRepoFilesCmd() tea.Cmd {
	if m.repoFilesLoaded || m.repoFilesLoading || m.opts.ListRepoFiles == nil {
		return nil
	}
	m.repoFilesLoading = true
	return m.loadRepoFilesCmd()
}

func (m model) loadRepoFilesCmd() tea.Cmd {
	root := m.opts.ProjectRoot
	list := m.opts.ListRepoFiles
	if list == nil {
		return nil
	}
	return func() tea.Msg {
		files, err := list(root)
		return filesLoadedMsg{files: files, err: err}
	}
}

func (m *model) beginCompletion() {
	m.newPane.completing = true
	m.newPane.compQuery = ""
	m.newPane.compIndex = 0
	m.recomputeCompletion()
}

func (m *model) endCompletion() {
	m.newPane.completing = false
	m.newPane.compQuery = ""
	m.newPane.compResults = nil
	m.newPane.compIndex = 0
	m.newPane.compTotal = 0
}

// recomputeCompletion re-ranks results for the current query and resets the
// highlight to the top match, since the prior selection refers to the old,
// now-reordered result set.
func (m *model) recomputeCompletion() {
	results, total := rankFileEntries(m.repoFileIndex, m.newPane.compQuery, completionMax)
	m.newPane.compResults = results
	m.newPane.compTotal = total
	m.newPane.compIndex = 0
}

func (m *model) moveCompletion(delta int) {
	n := len(m.newPane.compResults)
	if n == 0 {
		return
	}
	m.newPane.compIndex = (m.newPane.compIndex + delta + n) % n
}

// acceptCompletion replaces the typed '@<query>' token with '@<path> '. It
// derives the token from the textarea's actual content (not the compQuery
// shadow, which can drift when CharLimit rejects inserts), so a near-full prompt
// never over-deletes preceding text.
func (m *model) acceptCompletion() {
	if m.newPane.compIndex < 0 || m.newPane.compIndex >= len(m.newPane.compResults) {
		m.endCompletion()
		return
	}
	selected := m.newPane.compResults[m.newPane.compIndex]
	insertion := "@" + selected + " "
	tokenRunes := m.promptTokenRunesBeforeCursor()
	// The textarea silently truncates inserts at CharLimit, which would leave a
	// partial @path mention. If the replacement would not fit, keep the popup
	// open and surface why instead of corrupting the mention.
	if limit := m.newPane.prompt.CharLimit; limit > 0 {
		current := len([]rune(m.newPane.prompt.Value()))
		if current-tokenRunes+len([]rune(insertion)) > limit {
			m.newPane.err = "prompt too long to insert that path"
			return
		}
	}
	m.newPane.err = ""
	for range tokenRunes {
		m.newPane.prompt, _ = m.newPane.prompt.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m.newPane.prompt.InsertString(insertion)
	m.endCompletion()
}

// updatePromptCompletion routes a key while the popup is open. The bool reports
// whether the key was consumed; when false the caller keeps editing the textarea
// with the returned (possibly mutated) model.
func (m model) updatePromptCompletion(msg tea.KeyMsg) (model, bool) {
	switch msg.String() {
	case "esc":
		m.endCompletion()
		return m, true
	case "up", "ctrl+p":
		m.moveCompletion(-1)
		return m, true
	case "down", "ctrl+n":
		m.moveCompletion(1)
		return m, true
	case "enter", "tab":
		if len(m.newPane.compResults) > 0 {
			m.acceptCompletion()
			return m, true
		}
		if !m.repoFilesLoaded && m.repoFilesErr == "" {
			// The file list is still loading (empty results != no match). Consume
			// the key so a premature Enter does not submit a prompt with an
			// unexpanded @token; results appear once the load resolves.
			return m, true
		}
		// Loaded with no match (or the load failed): dismiss the popup and let
		// the key reach the form's submit / field-nav handlers in one press.
		m.endCompletion()
		return m, false
	case "left", "right", " ", "ctrl+j":
		// Cursor moves, space, and newline leave completion and pass through so
		// the key edits the textarea normally.
		m.endCompletion()
		return m, false
	case "backspace", "ctrl+h":
		if m.newPane.compQuery == "" {
			// Backspacing over the '@' itself ends completion; let the textarea
			// delete the '@'.
			m.endCompletion()
			return m, false
		}
		m.newPane.compQuery = trimLastRune(m.newPane.compQuery)
		m.recomputeCompletion()
		return m, false
	default:
		if runes := msg.Runes; len(runes) > 0 && allCompletionRunes(runes) {
			m.newPane.compQuery += string(runes)
			m.recomputeCompletion()
			return m, false
		}
		m.endCompletion()
		return m, false
	}
}

// promptLineRunesBeforeCursor returns the current logical line's runes and the
// cursor column within it. The textarea exposes no column accessor, so it is
// reconstructed from LineInfo.
func (m model) promptLineRunesBeforeCursor() ([]rune, int) {
	ta := m.newPane.prompt
	lines := strings.Split(ta.Value(), "\n")
	row := ta.Line()
	if row < 0 || row >= len(lines) {
		return nil, 0
	}
	li := ta.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	runes := []rune(lines[row])
	col = clampInt(col, 0, len(runes))
	return runes, col
}

// atWordBoundaryBeforeCursor reports whether the '@' just inserted at the prompt
// cursor sits at the start of a word (line start or preceded by whitespace), so
// email-like text (foo@bar) does not trigger completion.
func (m model) atWordBoundaryBeforeCursor() bool {
	runes, col := m.promptLineRunesBeforeCursor()
	idx := col - 2 // the '@' is at col-1; inspect the rune before it
	if idx < 0 || idx >= len(runes) {
		return idx < 0
	}
	return unicode.IsSpace(runes[idx])
}

// promptTokenRunesBeforeCursor returns the rune count of the '@'-token (a run of
// non-space chars ending at the cursor and starting with '@'). It returns 0 when
// the run does not start with '@', so acceptCompletion never deletes unrelated
// text.
func (m model) promptTokenRunesBeforeCursor() int {
	runes, col := m.promptLineRunesBeforeCursor()
	start := col
	for start > 0 && !unicode.IsSpace(runes[start-1]) {
		start--
	}
	if start >= col || runes[start] != '@' {
		return 0
	}
	return col - start
}

func allCompletionRunes(runes []rune) bool {
	for _, r := range runes {
		if !isCompletionRune(r) {
			return false
		}
	}
	return true
}

// isCompletionRune reports whether r may extend the @-query. '@' is excluded so
// a second '@' ends completion rather than building an unmatchable query.
func isCompletionRune(r rune) bool {
	return r != '@' && unicode.IsPrint(r) && !unicode.IsSpace(r)
}

// rankFileEntries returns the top matches for query plus the total match count.
// Ranking: basename prefix, then basename substring, then full-path substring;
// ties break by shorter path then lexical order for deterministic output.
func rankFileEntries(files []fileEntry, query string, maxResults int) (top []string, total int) {
	q := strings.ToLower(strings.TrimSpace(query))
	type scored struct {
		path string
		rank int
	}
	matches := make([]scored, 0, len(files))
	for _, e := range files {
		rank := entryMatchRank(e, q)
		if rank < 0 {
			continue
		}
		matches = append(matches, scored{path: e.path, rank: rank})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if len(a.path) != len(b.path) {
			return len(a.path) < len(b.path)
		}
		return a.path < b.path
	})
	total = len(matches)
	if maxResults > 0 && len(matches) > maxResults {
		matches = matches[:maxResults]
	}
	top = make([]string, len(matches))
	for i, s := range matches {
		top[i] = s.path
	}
	return top, total
}

// rankRepoFiles ranks raw paths; it builds a one-shot index and is the
// test-facing entry point. Production ranks the cached model.repoFileIndex.
func rankRepoFiles(files []string, query string, maxResults int) (top []string, total int) {
	return rankFileEntries(buildFileIndex(files), query, maxResults)
}

func entryMatchRank(e fileEntry, q string) int {
	if q == "" {
		return 0 // no query: every file matches equally, ordered by len/path
	}
	switch {
	case strings.HasPrefix(e.lowerBase, q):
		return 0
	case strings.Contains(e.lowerBase, q):
		return 1
	case strings.Contains(e.lowerPath, q):
		return 2
	default:
		return -1
	}
}

func (m model) completionPopupView() string {
	if !m.repoFilesLoaded {
		if m.repoFilesErr != "" {
			return warnStyle.Render("  file list unavailable: " + m.repoFilesErr)
		}
		return dimStyle.Render("  loading files…")
	}
	if len(m.newPane.compResults) == 0 {
		return dimStyle.Render("  no match")
	}
	width := m.inputContentWidth()
	lines := make([]string, 0, len(m.newPane.compResults)+1)
	for i, p := range m.newPane.compResults {
		text := truncatePathTail(p, width-2)
		if i == m.newPane.compIndex {
			lines = append(lines, "> "+titleStyle.Render(text))
		} else {
			lines = append(lines, "  "+dimStyle.Render(text))
		}
	}
	if m.newPane.compTotal > len(m.newPane.compResults) {
		more := m.newPane.compTotal - len(m.newPane.compResults)
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  +%d more (type to narrow)", more)))
	}
	return strings.Join(lines, "\n")
}

// truncatePathTail keeps the tail (basename side) of a path visible, eliding the
// head with an ellipsis when it would exceed limit columns.
func truncatePathTail(p string, limit int) string {
	if limit <= 1 {
		return p
	}
	runes := []rune(p)
	if len(runes) <= limit {
		return p
	}
	return "…" + string(runes[len(runes)-(limit-1):])
}
