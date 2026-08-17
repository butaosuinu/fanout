package backendtest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// capabilities probes one shape through the same backend.As* helpers the
// product uses, so the table below pins what each shape does and does not
// satisfy.
type capabilities struct {
	decorator bool
	stamper   bool
	previewer bool
	layout    bool
	owned     bool
	fresh     bool
}

func probe(b backend.Backend) capabilities {
	_, decorator := backend.AsPaneDecorator(b)
	_, stamper := backend.AsLivenessStamper(b)
	_, previewer := backend.AsDryRunPreviewer(b)
	_, layout := backend.AsLayoutManager(b)
	_, owned := b.(backend.OwnedCloser)
	_, fresh := b.(backend.FreshCloser)
	return capabilities{decorator, stamper, previewer, layout, owned, fresh}
}

// TestShapeCapabilities is the reason the shapes exist: capability detection is
// a type assertion, so every shape must satisfy exactly the capabilities it
// names and no more. A shape that grows a stray method set silently disables an
// "absent capability" branch in the product.
func TestShapeCapabilities(t *testing.T) {
	tests := []struct {
		name    string
		backend backend.Backend
		want    capabilities
	}{
		{name: "bare fake has no capability", backend: New()},
		{name: "decorator fake decorates only", backend: NewDecorator(), want: capabilities{decorator: true}},
		{name: "fresh closer fake cannot stamp liveness", backend: NewFreshCloser(), want: capabilities{fresh: true}},
		{name: "liveness fake stamps and rolls back", backend: NewLiveness(), want: capabilities{stamper: true, fresh: true}},
		{
			name:    "tmux fake carries every capability",
			backend: NewTmux(),
			want: capabilities{
				decorator: true, stamper: true, previewer: true,
				layout: true, owned: true, fresh: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probe(tt.backend); got != tt.want {
				t.Fatalf("probe(%T) = %+v, want %+v", tt.backend, got, tt.want)
			}
		})
	}
}

func TestLaunchReturnsConfiguredPanesInOrder(t *testing.T) {
	fake := New(WithName(backend.Herdr), WithPanes("w1:p1", "w1:p2"))

	var got []backend.PaneRef
	for range 3 {
		ref, err := fake.Launch(backend.LaunchRequest{Workspace: "w1"})
		if err != nil {
			t.Fatalf("Launch() error = %v", err)
		}
		got = append(got, ref)
	}

	// The last configured id repeats, so a single-id fake serves any launch count.
	want := []backend.PaneRef{
		{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p2"},
		{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Launch() refs = %+v, want %+v", got, want)
	}
	if len(fake.Launches()) != 3 {
		t.Fatalf("Launches() = %d, want 3", len(fake.Launches()))
	}
}

// Name and mutation model are independent properties: naming a fake herdr must
// not silently move it onto the journaled launch lane, because the tests that
// pin the atomic lane's fail-closed branches use a non-tmux name on purpose.
func TestMutationModelDefaultsToAtomicAndIsIndependentOfName(t *testing.T) {
	tests := []struct {
		name string
		fake *Fake
		want backend.MutationModel
	}{
		{name: "unconfigured fake", fake: New(), want: backend.MutationAtomic},
		{name: "herdr-named fake keeps the atomic default", fake: New(WithName(backend.Herdr)), want: backend.MutationAtomic},
		{name: "explicitly journaled fake", fake: New(WithMutationModel(backend.MutationJournaled)), want: backend.MutationJournaled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fake.MutationModel(); got != tt.want {
				t.Fatalf("MutationModel() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLaunchErrorLeavesNoPaneRef(t *testing.T) {
	wantErr := errors.New("no workspace")
	fake := New(WithLaunchError(wantErr))

	ref, err := fake.Launch(backend.LaunchRequest{})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Launch() error = %v, want %v", err, wantErr)
	}
	if ref != (backend.PaneRef{}) {
		t.Fatalf("Launch() ref = %+v, want zero ref", ref)
	}
}

// TestMixinsShareOneCallLog pins the composition contract: a shape's capability
// methods record into the same ordered log as its base backend methods, so an
// assertion can read a whole launch sequence from one place.
func TestMixinsShareOneCallLog(t *testing.T) {
	fake := NewTmux(WithPanes("%314"))

	ref, err := fake.Launch(backend.LaunchRequest{Target: "%caller"})
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if err := fake.StampPaneShellKey(ref.Pane, "shell-key"); err != nil {
		t.Fatalf("StampPaneShellKey() error = %v", err)
	}
	if err := fake.SetPaneTitle(ref.Pane, "child"); err != nil {
		t.Fatalf("SetPaneTitle() error = %v", err)
	}
	if err := fake.ReleaseStartGate("gate"); err != nil {
		t.Fatalf("ReleaseStartGate() error = %v", err)
	}

	wantMethods := []string{MethodLaunch, MethodStampPaneShellKey, MethodSetPaneTitle, MethodReleaseStartGate}
	if got := fake.Methods(); !reflect.DeepEqual(got, wantMethods) {
		t.Fatalf("Methods() = %v, want %v", got, wantMethods)
	}
	wantStamps := []PaneValue{{PaneID: "%314", Value: "shell-key"}}
	if got := fake.PaneValues(MethodStampPaneShellKey); !reflect.DeepEqual(got, wantStamps) {
		t.Fatalf("PaneValues(StampPaneShellKey) = %+v, want %+v", got, wantStamps)
	}
	if got := fake.ReleasedGates(); !reflect.DeepEqual(got, []string{"gate"}) {
		t.Fatalf("ReleasedGates() = %v, want [gate]", got)
	}
}

func TestConfiguredCapabilityFailures(t *testing.T) {
	stampErr := errors.New("stamp failed")
	freshErr := errors.New("pane still live")
	ownedErr := errors.New("tmux unreachable")
	fake := NewTmux(
		WithStampError(stampErr),
		WithFreshCloseError(freshErr),
		WithOwnedClose(backend.CloseResult{Status: backend.CloseStale, ContainerID: "@7"}, ownedErr),
	)

	if err := fake.StampPaneShellKey("%1", "key"); !errors.Is(err, stampErr) {
		t.Fatalf("StampPaneShellKey() error = %v, want %v", err, stampErr)
	}
	if err := fake.CloseFresh(backend.PaneRef{Pane: "%1"}); !errors.Is(err, freshErr) {
		t.Fatalf("CloseFresh() error = %v, want %v", err, freshErr)
	}
	req := backend.CloseRequest{Ref: backend.PaneRef{Pane: "%1"}, ShellKey: "key"}
	result, err := fake.CloseOwned(req)
	if !errors.Is(err, ownedErr) || result.Status != backend.CloseStale || result.ContainerID != "@7" {
		t.Fatalf("CloseOwned() = %+v, %v; want stale/@7 and %v", result, err, ownedErr)
	}
	if got := fake.CloseRequests(); !reflect.DeepEqual(got, []backend.CloseRequest{req}) {
		t.Fatalf("CloseRequests() = %+v, want %+v", got, []backend.CloseRequest{req})
	}
	if got := fake.ClosedRefs(); !reflect.DeepEqual(got, []backend.PaneRef{{Pane: "%1"}}) {
		t.Fatalf("ClosedRefs() = %+v, want the CloseFresh ref only", got)
	}
}

func TestPreviewLaunchReturnsConfiguredLines(t *testing.T) {
	fake := NewTmux(WithPreviewLines("$ fake split", "# would re-layout"))

	got := fake.PreviewLaunch(backend.LaunchPreview{Target: "%caller"})

	want := []string{"$ fake split", "# would re-layout"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PreviewLaunch() = %v, want %v", got, want)
	}
}
