package main

import (
	"reflect"
	"testing"
)

// TestParseNameStatus pins the git diff --name-status -z -M parser: A/M/D carry
// one exact path and R<score>/C<score> carry exact old and new paths.
func TestParseNameStatus(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []FileChange
	}{
		{
			name: "A/M/D each carry a single path",
			in:   "A\x00cmd/fanout/new.go\x00M\x00internal/core/naming/slug.go\x00D\x00tests/bats/old.bats\x00",
			want: []FileChange{
				{Status: 'A', Path: "cmd/fanout/new.go"},
				{Status: 'M', Path: "internal/core/naming/slug.go"},
				{Status: 'D', Path: "tests/bats/old.bats"},
			},
		},
		{
			name: "R100 records old and new paths",
			in:   "R100\x00internal/old/foo.go\x00internal/new/foo.go\x00",
			want: []FileChange{{Status: 'R', OldPath: "internal/old/foo.go", Path: "internal/new/foo.go"}},
		},
		{
			name: "C75 copy records old and new paths",
			in:   "C75\x00a.go\x00b.go\x00",
			want: []FileChange{{Status: 'C', OldPath: "a.go", Path: "b.go"}},
		},
		{
			name: "tabs and newlines remain in paths",
			in:   "M\x00docs/x\ty/AGENTS.md\x00R100\x00old\nname\x00new\nname\x00",
			want: []FileChange{
				{Status: 'M', Path: "docs/x\ty/AGENTS.md"},
				{Status: 'R', OldPath: "old\nname", Path: "new\nname"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNameStatus(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNameStatus(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseNumstat pins the git diff --numstat -z parser: integer columns parse
// as counts, a binary file's "-" columns become -1/-1, and a rename's separate
// old/new paths key on the exact new path.
func TestParseNumstat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]numstatEntry
	}{
		{
			name: "text file columns parse as integers",
			in:   "5\t3\tinternal/core/naming/slug.go\x00",
			want: map[string]numstatEntry{"internal/core/naming/slug.go": {added: 5, deleted: 3}},
		},
		{
			name: "binary file dashes become -1",
			in:   "-\t-\tweb/public/logo.png\x00",
			want: map[string]numstatEntry{"web/public/logo.png": {added: -1, deleted: -1}},
		},
		{
			name: "multiple rows accumulate",
			in:   "1\t0\ta.go\x000\t2\tb.go\x00",
			want: map[string]numstatEntry{"a.go": {added: 1}, "b.go": {deleted: 2}},
		},
		{
			name: "rename keys on the exact new path",
			in:   "2\t1\t\x00internal/old\tname/foo.go\x00internal/new\tname/foo.go\x00",
			want: map[string]numstatEntry{"internal/new\tname/foo.go": {added: 2, deleted: 1}},
		},
		{
			name: "tabs and newlines remain in ordinary paths",
			in:   "3\t1\tdir/x\ty/file.go\x000\t2\tdir/x\ny/file.go\x00",
			want: map[string]numstatEntry{
				"dir/x\ty/file.go": {added: 3, deleted: 1},
				"dir/x\ny/file.go": {deleted: 2},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseNumstat(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNumstat(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

// TestParseUnified pins the -U0 content extraction: added lines key to the
// `+++ b/` path and removed lines to the `--- a/` path with the marker
// stripped — so a whole-file delete and a rename onto a different path (even
// code renamed to .md) keep dropped content attributed to the old code path
// for S10. Crucially, a content line that reads `++ x` (raw `+++ x`) or
// `-- x` (raw `--- x`) is counted as content, not mistaken for a header.
func TestParseUnified(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantAdded   map[string][]string
		wantRemoved map[string][]string
	}{
		{
			name: "modified file records both added and removed lines",
			in: "diff --git a/foo.go b/foo.go\n" +
				"index 111..222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -1,0 +2 @@\n" +
				"+added one\n" +
				"@@ -5 +6,0 @@\n" +
				"-removed one\n",
			wantAdded:   map[string][]string{"foo.go": {"added one"}},
			wantRemoved: map[string][]string{"foo.go": {"removed one"}},
		},
		{
			name: "deleted file keys its removed lines to the old path",
			in: "diff --git a/gone.go b/gone.go\n" +
				"deleted file mode 100644\n" +
				"index 333..000\n" +
				"--- a/gone.go\n" +
				"+++ /dev/null\n" +
				"@@ -1,2 +0,0 @@\n" +
				"-old line a\n" +
				"-old line b\n",
			wantAdded:   map[string][]string{},
			wantRemoved: map[string][]string{"gone.go": {"old line a", "old line b"}},
		},
		{
			name: "rename onto .md keys removed lines to the old code path",
			in: "diff --git a/internal/infra/settings/x.go b/docs/x-notes.md\n" +
				"similarity index 60%\n" +
				"rename from internal/infra/settings/x.go\n" +
				"rename to docs/x-notes.md\n" +
				"--- a/internal/infra/settings/x.go\n" +
				"+++ b/docs/x-notes.md\n" +
				"@@ -3 +3 @@\n" +
				"-if requireToken(r) {\n" +
				"+prose replacement\n",
			wantAdded:   map[string][]string{"docs/x-notes.md": {"prose replacement"}},
			wantRemoved: map[string][]string{"internal/infra/settings/x.go": {"if requireToken(r) {"}},
		},
		{
			name: "added file uses the new path from +++ b/",
			in: "diff --git a/new.go b/new.go\n" +
				"new file mode 100644\n" +
				"index 000..444\n" +
				"--- /dev/null\n" +
				"+++ b/new.go\n" +
				"@@ -0,0 +1,2 @@\n" +
				"+brand new a\n" +
				"+brand new b\n",
			wantAdded:   map[string][]string{"new.go": {"brand new a", "brand new b"}},
			wantRemoved: map[string][]string{},
		},
		{
			// Content lines whose text begins with + or - become raw +++/--- lines.
			// Once past the @@ hunk they must be attributed to foo.go, not parsed
			// as headers (the old prefix-only check switched files or dropped them).
			name: "plus/minus-leading content is not mistaken for a header",
			in: "diff --git a/foo.go b/foo.go\n" +
				"index 111..222 100644\n" +
				"--- a/foo.go\n" +
				"+++ b/foo.go\n" +
				"@@ -1,0 +2 @@\n" +
				"+++ requireToken junk\n" +
				"@@ -5 +6,0 @@\n" +
				"--- FANOUT_X removed\n",
			wantAdded:   map[string][]string{"foo.go": {"++ requireToken junk"}},
			wantRemoved: map[string][]string{"foo.go": {"-- FANOUT_X removed"}},
		},
		{
			name: "Git C-quoted paths decode to exact path keys",
			in: "diff --git \"a/internal/core/x\\ty/foo_test.go\" \"b/internal/core/x\\ty/foo_test.go\"\n" +
				"index 111..222 100644\n" +
				"--- \"a/internal/core/x\\ty/foo_test.go\"\n" +
				"+++ \"b/internal/core/x\\ty/foo_test.go\"\n" +
				"@@ -1 +1 @@\n" +
				"-requireToken(old)\n" +
				"+t.Skip(\"wip\")\n",
			wantAdded:   map[string][]string{"internal/core/x\ty/foo_test.go": {`t.Skip("wip")`}},
			wantRemoved: map[string][]string{"internal/core/x\ty/foo_test.go": {"requireToken(old)"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdded, gotRemoved := parseUnified(tt.in)
			if !reflect.DeepEqual(gotAdded, tt.wantAdded) {
				t.Errorf("parseUnified(...) added = %#v, want %#v", gotAdded, tt.wantAdded)
			}
			if !reflect.DeepEqual(gotRemoved, tt.wantRemoved) {
				t.Errorf("parseUnified(...) removed = %#v, want %#v", gotRemoved, tt.wantRemoved)
			}
		})
	}
}
