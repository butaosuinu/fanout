package state

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// compatFixture is a two-row store: one legacy tmux pane written before the
// backend/herdr fields existed, and one herdr pane populating every persisted
// field including the nested backend types.
const compatFixture = "state-compat.json"

// compatPersistedKeys lists every .fanout/state.json key Pane persists, in
// struct order. It is written out by hand on purpose: deriving it from the
// struct tags would rename itself alongside a tag rename and pin nothing.
var compatPersistedKeys = []string{
	"parent",
	"runtimeParent",
	"issueNum",
	"taskId",
	"kind",
	"slug",
	"branchName",
	"baseBranch",
	"backend",
	"paneId",
	"herdrWorkspaceId",
	"herdrWorkspaceLabel",
	"herdrTerminalId",
	"herdrRepoKey",
	"herdrRepoRoot",
	"herdrBranchCreated",
	"herdrAgentId",
	"herdrAgentSession",
	"herdrProcessIdentity",
	"herdrSession",
	"herdrSocketPath",
	"reported_state",
	"state_refinement",
	"emitterRowKey",
	"launchNonce",
	"emitterNonce",
	"herdrLaunchExecutable",
	"herdrLaunchArgs",
	"herdrDirectAgentLaunch",
	"shellKey",
	"sourceParent",
	"sourceIssueNum",
	"sourceTaskId",
	"agent",
	"codexPlanMode",
	"codexThreadId",
	"codexSessionId",
	"wave",
	"displayName",
	"worktreePath",
	"prompt",
	"createdAt",
	"agentStatus",
}

// fieldCase pins one persisted field. name is the JSON key it is stored under,
// so a failing subtest names the exact tag that drifted.
type fieldCase struct {
	name string
	got  any
	want any
}

// TestStateCompatDecodesEveryHerdrField pins the .fanout/state.json schema
// during the tmux/herdr abstraction refactor: the JSON tags are frozen, so a
// renamed Go identifier must keep decoding the same key bytes an older binary
// wrote.
func TestStateCompatDecodesEveryHerdrField(t *testing.T) {
	_, herdr := loadCompatFixture(t, stageCompatFixture(t))

	for _, tt := range herdrRowFields(herdr) {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("LoadProject(testdata/%s).Panes[1].%s = %#v, want %#v",
					compatFixture, tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestStateCompatDecodesLegacyTmuxRow pins the additive-schema contract: a row
// written before the backend and herdr fields existed still loads, and the
// absent keys stay zero instead of being defaulted on read.
func TestStateCompatDecodesLegacyTmuxRow(t *testing.T) {
	legacy, _ := loadCompatFixture(t, stageCompatFixture(t))

	for _, tt := range []fieldCase{
		{name: "parent", got: legacy.Parent, want: "81"},
		{name: "issueNum", got: legacy.IssueNum, want: 83},
		{name: "paneId", got: legacy.PaneID, want: "%42"},
		{name: "agent", got: legacy.Agent, want: "claude"},
		// Load must not normalize the absent backend to tmux; that keeps the
		// legacy row byte-identical through a round trip.
		{name: "backend stays empty when the key is absent", got: legacy.Backend, want: backend.Name("")},
		{name: "baseBranch stays empty when the key is absent", got: legacy.BaseBranch, want: ""},
		{name: "shellKey stays empty when the key is absent", got: legacy.ShellKey, want: ""},
		{name: "herdrWorkspaceId stays empty when the key is absent", got: legacy.HerdrWorkspaceID, want: ""},
		{name: "herdrAgentSession stays nil when the key is absent", got: legacy.HerdrAgentSession, want: (*backend.AgentSessionRef)(nil)},
		{name: "herdrProcessIdentity stays nil when the key is absent", got: legacy.HerdrProcessIdentity, want: (*backend.ProcessIdentity)(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("LoadProject(testdata/%s).Panes[0].%s = %#v, want %#v",
					compatFixture, tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestStateCompatRoundTripPreservesEveryHerdrField pins that the write path
// gives back everything the read path accepted, so an upgraded binary rewriting
// state.json cannot silently drop a field an older binary still reads.
func TestStateCompatRoundTripPreservesEveryHerdrField(t *testing.T) {
	root := stageCompatFixture(t)
	saveCompatFixture(t, root)
	_, herdr := loadCompatFixture(t, root)

	for _, tt := range herdrRowFields(herdr) {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.got, tt.want) {
				t.Fatalf("LoadProject(save(testdata/%s)).Panes[1].%s = %#v, want %#v",
					compatFixture, tt.name, tt.got, tt.want)
			}
		})
	}
}

// TestStateCompatRoundTripWritesEveryJSONKey pins the written key bytes, not
// just a symmetric encode/decode: renaming a tag on both sides at once would
// still strand every state.json an older binary wrote.
func TestStateCompatRoundTripWritesEveryJSONKey(t *testing.T) {
	root := stageCompatFixture(t)
	saveCompatFixture(t, root)
	rows := writtenPaneKeys(t, root)

	for _, key := range compatPersistedKeys {
		t.Run(key, func(t *testing.T) {
			if _, ok := rows[1][key]; !ok {
				t.Fatalf("save(state.json) panes[1] keys = %v, want %q",
					slices.Sorted(maps.Keys(rows[1])), key)
			}
		})
	}
}

// TestStateCompatRoundTripKeepsLegacyRowMinimal pins omitempty on the additive
// fields: rewriting an old store must not grow keys an older binary would
// reject or misread.
func TestStateCompatRoundTripKeepsLegacyRowMinimal(t *testing.T) {
	root := stageCompatFixture(t)
	saveCompatFixture(t, root)
	rows := writtenPaneKeys(t, root)

	for _, key := range []string{
		"backend", "shellKey", "taskId", "baseBranch", "runtimeParent", "kind",
		"herdrWorkspaceId", "herdrAgentSession", "herdrProcessIdentity",
		"herdrLaunchArgs", "codexPlanMode", "reported_state",
	} {
		t.Run(key, func(t *testing.T) {
			if _, ok := rows[0][key]; ok {
				t.Fatalf("save(state.json) panes[0] keys = %v, did not want %q",
					slices.Sorted(maps.Keys(rows[0])), key)
			}
		})
	}
}

// TestPaneJSONTagsAreFrozen catches a tag rename or a new persisted field even
// when no fixture covers it. Updating compatPersistedKeys is the deliberate
// acknowledgement that .fanout/state.json changed shape across binary versions.
func TestPaneJSONTagsAreFrozen(t *testing.T) {
	paneType := reflect.TypeOf(Pane{})
	got := make([]string, 0, paneType.NumField())
	for i := range paneType.NumField() {
		key, _, _ := strings.Cut(paneType.Field(i).Tag.Get("json"), ",")
		got = append(got, key)
	}
	// SourceProjectRoot and SourceProjectRoots are the two non-persisted
	// aggregation fields MergedStateLoader fills in; they stay json:"-".
	want := append(slices.Clone(compatPersistedKeys), "-", "-")

	if !slices.Equal(got, want) {
		t.Fatalf("json tags of Pane = %v, want %v", got, want)
	}
}

func stageCompatFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", compatFixture))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func loadCompatFixture(t *testing.T, root string) (legacy, herdr Pane) {
	t.Helper()
	store, err := LoadProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if store.SchemaVersion != SchemaVersion || len(store.Panes) != 2 {
		t.Fatalf("LoadProject(%s) = schemaVersion %d with %d panes, want %d with 2",
			root, store.SchemaVersion, len(store.Panes), SchemaVersion)
	}
	return store.Panes[0], store.Panes[1]
}

// saveCompatFixture rewrites the staged store through the production write
// path (LockProject then Save).
func saveCompatFixture(t *testing.T, root string) {
	t.Helper()
	locked, err := LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	saveErr := locked.Save()
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	if saveErr != nil {
		t.Fatal(saveErr)
	}
}

// writtenPaneKeys decodes the on-disk rows as raw key maps so assertions see
// the key bytes the writer emitted rather than the struct that produced them.
func writtenPaneKeys(t *testing.T, root string) []map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(Path(root))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		SchemaVersion int                          `json:"schemaVersion"`
		Panes         []map[string]json.RawMessage `json:"panes"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("save(%s) wrote invalid JSON: %v\n%s", Path(root), err, data)
	}
	if raw.SchemaVersion != SchemaVersion || len(raw.Panes) != 2 {
		t.Fatalf("save(%s) = schemaVersion %d with %d panes, want %d with 2",
			Path(root), raw.SchemaVersion, len(raw.Panes), SchemaVersion)
	}
	return raw.Panes
}

// herdrRowFields carries one case per persisted field; the length is the
// coverage, so keep it exhaustive rather than short.
func herdrRowFields(p Pane) []fieldCase {
	return []fieldCase{
		{name: "parent", got: p.Parent, want: "524"},
		{name: "runtimeParent", got: p.RuntimeParent, want: "500"},
		{name: "issueNum", got: p.IssueNum, want: 531},
		{name: "taskId", got: p.TaskID, want: "task-herdr-1"},
		{name: "kind", got: p.Kind, want: PaneKindAttachedAgent},
		{name: "slug", got: p.Slug, want: "herdr-backend-531"},
		{name: "branchName", got: p.BranchName, want: "fanout/herdr-backend-531"},
		{name: "baseBranch", got: p.BaseBranch, want: "main"},
		{name: "backend", got: p.Backend, want: backend.Herdr},
		{name: "paneId", got: p.PaneID, want: "w1:p1"},
		{name: "herdrWorkspaceId", got: p.HerdrWorkspaceID, want: "w1"},
		{name: "herdrWorkspaceLabel", got: p.HerdrWorkspaceLabel, want: "fanout-herdr-backend-531"},
		{name: "herdrTerminalId", got: p.HerdrTerminalID, want: "t1"},
		{name: "herdrRepoKey", got: p.HerdrRepoKey, want: "github.com/butaosuinu/fanout"},
		{name: "herdrRepoRoot", got: p.HerdrRepoRoot, want: "/repo"},
		{name: "herdrBranchCreated", got: p.HerdrBranchCreated, want: true},
		{name: "herdrAgentId", got: p.HerdrAgentID, want: "codex-1"},
		{name: "herdrAgentSession", got: p.HerdrAgentSession, want: &backend.AgentSessionRef{
			Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "01JZ0000000000000000000000",
		}},
		{name: "herdrProcessIdentity", got: p.HerdrProcessIdentity, want: &backend.ProcessIdentity{
			ShellPID: 4242, ForegroundProcessGroup: 4243, AgentPID: 4244,
		}},
		{name: "herdrSession", got: p.HerdrSession, want: "fhr-1a2b3c4d"},
		{name: "herdrSocketPath", got: p.HerdrSocketPath, want: "/tmp/fhr-501/fhr-1a2b3c4d/herdr.sock"},
		{name: "reported_state", got: p.ReportedState, want: "working"},
		{name: "state_refinement", got: p.StateRefinement, want: true},
		{name: "emitterRowKey", got: p.EmitterRowKey, want: "524/531"},
		{name: "launchNonce", got: p.LaunchNonce, want: "0123456789abcdef0123456789abcdef"},
		{name: "emitterNonce", got: p.EmitterNonce, want: "fedcba9876543210fedcba9876543210"},
		{name: "herdrLaunchExecutable", got: p.HerdrLaunchExecutable, want: "/usr/local/bin/codex"},
		{name: "herdrLaunchArgs", got: p.HerdrLaunchArgs, want: []string{
			"--cd", "/repo/.fanout/worktrees/herdr-backend-531",
		}},
		{name: "herdrDirectAgentLaunch", got: p.HerdrDirectAgentLaunch, want: true},
		{name: "shellKey", got: p.ShellKey, want: "fanout-shell-531"},
		{name: "sourceParent", got: p.SourceParent, want: "524"},
		{name: "sourceIssueNum", got: p.SourceIssueNum, want: 530},
		{name: "sourceTaskId", got: p.SourceTaskID, want: "task-herdr-0"},
		{name: "agent", got: p.Agent, want: "codex"},
		{name: "codexPlanMode", got: p.PlanMode, want: true},
		{name: "codexThreadId", got: p.CodexThreadID, want: "thread-531"},
		{name: "codexSessionId", got: p.CodexSessionID, want: "session-531"},
		{name: "wave", got: p.Wave, want: "wave-2"},
		{name: "displayName", got: p.DisplayName, want: "Herdr backend"},
		{name: "worktreePath", got: p.WorktreePath, want: "/repo/.fanout/worktrees/herdr-backend-531"},
		{name: "prompt", got: p.Prompt, want: "[fanout #531 of #524] herdr-backend-531: read /tmp/fanout-fanout-531.md and begin."},
		{name: "createdAt", got: p.CreatedAt, want: "2026-08-15T09:30:00Z"},
		{name: "agentStatus", got: p.AgentStatus, want: "running"},
	}
}
