package team

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/briefing"
)

func TestParentDBSlug(t *testing.T) {
	tests := []struct {
		name      string
		parentRef string
		want      string
	}{
		{name: "issue number", parentRef: "68", want: "68"},
		{name: "issue number collapses leading zeros", parentRef: "0068", want: "68"},
		{name: "project URL", parentRef: "https://github.com/orgs/x/projects/1", want: "https-github-com-orgs-x-projects-1"},
		{name: "manual parent", parentRef: "@manual", want: "manual"},
		{name: "mixed case lowercased", parentRef: "FooBar", want: "foobar"},
		{name: "dashes trimmed", parentRef: "--x--", want: "x"},
		{name: "only punctuation falls back", parentRef: "!!!", want: "parent"},
		{name: "empty falls back", parentRef: "", want: "parent"},
		{name: "leading plus normalizes to digits", parentRef: "+68", want: "68"},
		{name: "leading minus normalizes to digits", parentRef: "-5", want: "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParentDBSlug(tt.parentRef); got != tt.want {
				t.Errorf("ParentDBSlug(%q) = %q, want %q", tt.parentRef, got, tt.want)
			}
		})
	}
}

func TestDBPath(t *testing.T) {
	tests := []struct {
		name        string
		projectRoot string
		parentRef   string
		want        string
	}{
		{name: "issue mode", projectRoot: "/x/y/myrepo", parentRef: "68", want: "/tmp/fanout-myrepo-68.db"},
		{
			name:        "project mode",
			projectRoot: "/home/u/proj",
			parentRef:   "https://github.com/orgs/x/projects/1",
			want:        "/tmp/fanout-proj-https-github-com-orgs-x-projects-1.db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(DBPathEnv, "")
			if got := DBPath(tt.projectRoot, tt.parentRef); got != tt.want {
				t.Errorf("DBPath(%q, %q) = %q, want %q", tt.projectRoot, tt.parentRef, got, tt.want)
			}
		})
	}
}

func TestDBPathOverrideWins(t *testing.T) {
	t.Setenv(DBPathEnv, "/elsewhere/custom.db")
	if got := DBPath("/x/y/myrepo", "68"); got != "/elsewhere/custom.db" {
		t.Errorf("DBPath with %s = %q, want /elsewhere/custom.db", DBPathEnv, got)
	}
}

// The DB and the briefing must agree on the repo slug so that operators can
// correlate /tmp/fanout-<repo>-<N>.md with /tmp/fanout-<repo>-<parent>.db.
func TestDBPathMatchesBriefingRepoSlug(t *testing.T) {
	t.Setenv(DBPathEnv, "")
	root := "/some/where/myrepo"
	briefingSlug := strings.TrimSuffix(strings.TrimPrefix(briefing.Path(root, 68), "/tmp/fanout-"), "-68.md")
	dbSlug := strings.TrimSuffix(strings.TrimPrefix(DBPath(root, "99"), "/tmp/fanout-"), "-99.db")
	if briefingSlug != dbSlug {
		t.Errorf("repo slug mismatch: briefing %q vs db %q", briefingSlug, dbSlug)
	}
	if briefingSlug != filepath.Base(root) {
		t.Errorf("repo slug = %q, want filepath.Base(root) = %q", briefingSlug, filepath.Base(root))
	}
}
