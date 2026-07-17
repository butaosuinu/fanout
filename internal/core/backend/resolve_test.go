package backend

import (
	"strings"
	"testing"
)

func TestResolveExistingParentStickiness(t *testing.T) {
	tests := []struct {
		name    string
		in      ResolveInput
		want    Selection
		wantErr string
	}{
		{
			name: "legacy final row normalizes to tmux",
			in:   ResolveInput{Parent: "0423", Rows: []Binding{{Parent: "423"}}},
			want: Selection{Name: Tmux, Reason: ReasonExistingParent},
		},
		{
			name: "provisional intent is sticky",
			in:   ResolveInput{Parent: "plan:alpha", ProvisionalIntents: []Binding{{Parent: "plan:alpha", Backend: Herdr}}},
			want: Selection{Name: Herdr, Reason: ReasonExistingParent},
		},
		{
			name:    "provisional intent must name its backend",
			in:      ResolveInput{Parent: "plan:alpha", ProvisionalIntents: []Binding{{Parent: "plan:alpha"}}},
			wantErr: "provisional intent for parent plan:alpha has no backend",
		},
		{
			name: "other parent is ignored",
			in:   ResolveInput{Parent: "423", CLI: "herdr", Rows: []Binding{{Parent: "424", Backend: Tmux}}},
			want: Selection{Name: Herdr, Reason: ReasonCLI},
		},
		{
			name: "mixed rows and intents fail closed",
			in: ResolveInput{
				Parent:             "423",
				Rows:               []Binding{{Parent: "423", Backend: Tmux}},
				ProvisionalIntents: []Binding{{Parent: "423", Backend: Herdr}},
			},
			wantErr: "mixed state",
		},
		{
			name:    "cli contradiction fails closed",
			in:      ResolveInput{Parent: "423", CLI: "tmux", Rows: []Binding{{Parent: "423", Backend: Herdr}}},
			wantErr: "--backend requests tmux",
		},
		{
			name:    "env contradiction fails closed even when cli matches",
			in:      ResolveInput{Parent: "423", CLI: "herdr", Environment: "tmux", Rows: []Binding{{Parent: "423", Backend: Herdr}}},
			wantErr: "FANOUT_BACKEND requests tmux",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Resolve() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolvePriorityChain(t *testing.T) {
	tests := []struct {
		name string
		in   ResolveInput
		want Selection
	}{
		{name: "cli", in: ResolveInput{CLI: "tmux", Environment: "herdr", HerdrEnvironment: true, TmuxEnvironment: true, UserDefault: "herdr"}, want: Selection{Name: Tmux, Reason: ReasonCLI}},
		{name: "cli ignores invalid lower-priority environment", in: ResolveInput{CLI: "tmux", Environment: "screen"}, want: Selection{Name: Tmux, Reason: ReasonCLI}},
		{name: "environment", in: ResolveInput{Environment: "tmux", HerdrEnvironment: true, UserDefault: "herdr"}, want: Selection{Name: Tmux, Reason: ReasonEnvironment}},
		{name: "nested context prefers herdr", in: ResolveInput{HerdrEnvironment: true, TmuxEnvironment: true, UserDefault: "tmux"}, want: Selection{Name: Herdr, Reason: ReasonHerdrContext}},
		{name: "tmux context", in: ResolveInput{TmuxEnvironment: true, UserDefault: "herdr"}, want: Selection{Name: Tmux, Reason: ReasonTmuxContext}},
		{name: "user config", in: ResolveInput{UserDefault: "herdr"}, want: Selection{Name: Herdr, Reason: ReasonUserConfig}},
		{name: "default", in: ResolveInput{}, want: Selection{Name: Tmux, Reason: ReasonDefault}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("Resolve() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveExplicitTmuxOverridesNestedContext(t *testing.T) {
	for _, in := range []ResolveInput{
		{CLI: "tmux", HerdrEnvironment: true, TmuxEnvironment: true},
		{Environment: "tmux", HerdrEnvironment: true, TmuxEnvironment: true},
	} {
		got, err := Resolve(in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != Tmux {
			t.Fatalf("Resolve(%+v) = %#v, want tmux", in, got)
		}
	}
}

func TestResolveRejectsInvalidConfiguredName(t *testing.T) {
	for _, in := range []ResolveInput{{CLI: "screen"}, {Environment: "screen"}, {UserDefault: "screen"}} {
		if _, err := Resolve(in); err == nil {
			t.Fatalf("Resolve(%+v) = nil error, want invalid backend", in)
		}
	}
}
