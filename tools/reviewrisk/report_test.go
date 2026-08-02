package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// sampleReport is a two-file high report used across the output-format tests.
func sampleReport() Report {
	return Report{
		Level: LevelHigh,
		Files: []FileReport{
			{Path: "internal/infra/state/store.go", Status: "M", Class: ClassH, Level: LevelHigh, RuleID: "infra-state", Note: "state.json と lock の読み書き"},
			{Path: "internal/ui/dashboard/newthing.go", Status: "A", Class: ClassUnknown, Level: LevelHigh, RuleID: "unclassified", Note: "未分類"},
		},
		Reasons: []Reason{
			{Signal: sigInvariantHit, Level: LevelHigh, File: "internal/infra/state/store.go", Detail: "不変条件 requireToken に接触"},
			{Signal: sigUnclassifiedPath, Level: LevelHigh, File: "internal/ui/dashboard/newthing.go", Detail: "未分類パス(fail-closed で high)"},
		},
		Stats: Stats{Files: 2, Added: 30, Deleted: 4},
	}
}

// TestReportMarkdown pins the sticky-comment shape: the marker leads, the level
// and guidance render, reasons keep report order, and the per-file class table
// sits inside the <details>.
func TestReportMarkdown(t *testing.T) {
	md := sampleReport().Markdown()

	if !strings.HasPrefix(md, "<!-- review-risk -->\n") {
		t.Errorf("Markdown() must start with the review-risk marker, got:\n%s", md)
	}
	wants := []string{
		"## Review risk: **HIGH**",
		"人間レビュー必須。AI は補助",
		"### 理由",
		"<details><summary>ファイル別クラス (2 files, +30 −4)</summary>",
		"| File | St | Class | Level | Rule |",
		"| `internal/infra/state/store.go` | M | H | high | infra-state |",
		"| `internal/ui/dashboard/newthing.go` | A | ? | high | unclassified |",
		"</details>",
		"判定ルール: docs/review-risk.ja.md / docs/architecture.ja.md",
	}
	for _, w := range wants {
		if !strings.Contains(md, w) {
			t.Errorf("Markdown() missing %q, got:\n%s", w, md)
		}
	}

	// The table lives inside the <details>, not before it.
	if strings.Index(md, "<details") > strings.Index(md, "| File | St |") {
		t.Errorf("Markdown() renders the table before <details>, got:\n%s", md)
	}
	// Reasons keep report order: the invariant reason precedes the unclassified.
	if strings.Index(md, sigInvariantHit) > strings.Index(md, sigUnclassifiedPath) {
		t.Errorf("Markdown() reason order not preserved, got:\n%s", md)
	}
}

// TestReportText pins the terminal output: the level and guidance headline, the
// reasons list, and the file-table header that carries the diff stats. The stats
// appear once (the old duplicate footer line is gone).
func TestReportText(t *testing.T) {
	txt := sampleReport().Text()
	wants := []string{
		"Review risk: HIGH — 人間レビュー必須。AI は補助",
		sigInvariantHit,
		sigUnclassifiedPath,
		"internal/infra/state/store.go",
		"ファイル (2 files, +30 −4):",
	}
	for _, w := range wants {
		if !strings.Contains(txt, w) {
			t.Errorf("Text() missing %q, got:\n%s", w, txt)
		}
	}
	if strings.Contains(txt, "stats:") {
		t.Errorf("Text() still prints the duplicate stats footer, got:\n%s", txt)
	}
}

func TestReportQuotesSpecialPathsOnOneLine(t *testing.T) {
	path := "docs/x\ty/line\n\u2028\u2029`|</code>.md"
	report := Report{
		Level: LevelHigh,
		Files: []FileReport{{
			Path: path, Status: "M", Class: ClassUnknown, Level: LevelHigh, RuleID: "unclassified",
		}},
		Reasons: []Reason{
			{Signal: sigUnclassifiedPath, Level: LevelHigh, File: path, Detail: "old\n**path**"},
			{Signal: sigTestDeleted, Level: LevelHigh, File: path, Detail: "rename でテスト形状を喪失(tests/bats/~~old~~.bats)"},
			{Signal: sigSkipAdded, Level: LevelHigh, File: path, Detail: `スキップ追加: skip "~~case~~"`},
		},
		Stats: Stats{Files: 1, Added: 1},
	}

	quoted := strconv.QuoteToGraphic(path)
	text := report.Text()
	if !strings.Contains(text, quoted) || strings.Contains(text, path) {
		t.Errorf("Text() must quote the special path on one line, got:\n%s", text)
	}

	markdown := report.Markdown()
	for _, want := range []string{"<code>", "docs/x\\ty/line\\n\\u2028\\u2029", "&#124;", "&lt;/code&gt;", "old&#92;n&#42;&#42;path&#42;&#42;", "&#126;&#126;old&#126;&#126;", "&#126;&#126;case&#126;&#126;"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown() missing escaped fragment %q, got:\n%s", want, markdown)
		}
	}
	if strings.Contains(markdown, "~~") {
		t.Errorf("Markdown() contains an unescaped strikethrough delimiter, got:\n%s", markdown)
	}
	if strings.Contains(markdown, path) {
		t.Errorf("Markdown() contains the raw special path, got:\n%s", markdown)
	}
}

// TestReportJSON pins the JSON encoding: level and class marshal to their string
// labels and the structure round-trips through a decode.
func TestReportJSON(t *testing.T) {
	b, err := sampleReport().JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var got struct {
		Level string `json:"level"`
		Files []struct {
			Path  string `json:"path"`
			Class string `json:"class"`
			Level string `json:"level"`
			Rule  string `json:"rule"`
		} `json:"files"`
		Reasons []struct {
			Signal string `json:"signal"`
			Level  string `json:"level"`
		} `json:"reasons"`
		Stats struct {
			Files   int `json:"files"`
			Added   int `json:"added"`
			Deleted int `json:"deleted"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal(JSON()) error = %v", err)
	}
	if got.Level != "high" {
		t.Errorf("JSON() level = %q, want %q", got.Level, "high")
	}
	if len(got.Files) != 2 {
		t.Fatalf("JSON() files = %d, want 2", len(got.Files))
	}
	if got.Files[0].Class != "H" || got.Files[0].Level != "high" {
		t.Errorf("JSON() files[0] class/level = %q/%q, want H/high", got.Files[0].Class, got.Files[0].Level)
	}
	if got.Files[1].Class != "?" || got.Files[1].Rule != "unclassified" {
		t.Errorf("JSON() files[1] class/rule = %q/%q, want ?/unclassified", got.Files[1].Class, got.Files[1].Rule)
	}
	if got.Stats.Files != 2 || got.Stats.Added != 30 || got.Stats.Deleted != 4 {
		t.Errorf("JSON() stats = %+v, want {2 30 4}", got.Stats)
	}
	if len(got.Reasons) != 2 || got.Reasons[0].Level != "high" {
		t.Errorf("JSON() reasons = %+v, want 2 high reasons", got.Reasons)
	}
}

// TestSortReasons pins the reason ordering used by every format: level
// descending, then signal id, then file.
func TestSortReasons(t *testing.T) {
	rs := []Reason{
		{Signal: sigUnclassifiedPath, Level: LevelHigh, File: "z.go"},
		{Signal: sigTestDeleted, Level: LevelCritical, File: "b.go"},
		{Signal: sigInvariantHit, Level: LevelHigh, File: "a.go"},
		{Signal: sigTestDeleted, Level: LevelCritical, File: "a.go"},
	}
	sortReasons(rs)
	want := []struct {
		signal string
		file   string
	}{
		{sigTestDeleted, "a.go"},      // critical, S1, a
		{sigTestDeleted, "b.go"},      // critical, S1, b
		{sigInvariantHit, "a.go"},     // high, S10 sorts before S9
		{sigUnclassifiedPath, "z.go"}, // high, S9
	}
	for i, w := range want {
		if rs[i].Signal != w.signal || rs[i].File != w.file {
			t.Errorf("sortReasons()[%d] = %s/%s, want %s/%s", i, rs[i].Signal, rs[i].File, w.signal, w.file)
		}
	}
}
