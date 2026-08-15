package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// TestBriefingAndTeamDBAgreeOnRepoSlug compares the two slug derivations
// against each other, not against frozen literals: operators correlate
// .fanout/briefings/fanout-<repo>-<N>.md with /tmp/fanout-<repo>-<parent>.db,
// and briefing (app) and team (infra) derive <repo> independently. The
// composition root is the one package allowed to import both, so the
// cross-implementation pin lives here.
func TestBriefingAndTeamDBAgreeOnRepoSlug(t *testing.T) {
	t.Setenv(team.DBPathEnv, "")
	for _, tt := range []struct {
		name string
		root string
	}{
		{name: "plain repo directory name", root: "/some/where/myrepo"},
		{name: "name with dots and dashes", root: "/srv/repos/my.repo-v2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			briefingBase := filepath.Base(briefing.Path(tt.root, 68))
			briefingSlug := strings.TrimSuffix(strings.TrimPrefix(briefingBase, "fanout-"), "-68.md")
			dbSlug := strings.TrimSuffix(strings.TrimPrefix(team.DBPath(tt.root, "99"), "/tmp/fanout-"), "-99.db")
			if briefingSlug != dbSlug {
				t.Fatalf("briefing.Path(%q) slug = %q, team.DBPath slug = %q; the two derivations diverged",
					tt.root, briefingSlug, dbSlug)
			}
		})
	}
}
