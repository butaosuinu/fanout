package backend

import (
	"fmt"
	"slices"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/parentref"
)

// SelectionReason identifies the resolver stage that selected a backend.
type SelectionReason string

const (
	ReasonExistingParent SelectionReason = "existing-parent"
	ReasonCLI            SelectionReason = "cli"
	ReasonEnvironment    SelectionReason = "environment"
	ReasonHerdrContext   SelectionReason = "herdr-context"
	ReasonTmuxContext    SelectionReason = "tmux-context"
	ReasonUserConfig     SelectionReason = "user-config"
	ReasonDefault        SelectionReason = "default"
)

// Selection is the resolved backend and the stage that selected it. The reason
// is carried to dry-run/TUI surfaces without making them repeat the resolver.
type Selection struct {
	Name   Name
	Reason SelectionReason
}

// Binding is the backend ownership recorded by a final row or provisional
// intent. Empty Backend is the legacy tmux representation.
type Binding struct {
	Parent  string
	Backend Name
}

// ResolveInput contains every deterministic resolver input. CLI and
// Environment are kept separate even though settings also exposes their final
// value, because selection reason and stickiness conflicts depend on source.
type ResolveInput struct {
	Parent             string
	CLI                string
	Environment        string
	HerdrEnvironment   bool
	TmuxEnvironment    bool
	UserDefault        string
	Rows               []Binding
	ProvisionalIntents []Binding
}

// Resolve applies parent stickiness first. Only a parent with no existing row
// or intent enters the CLI > env > context > user config > tmux default chain.
func Resolve(in ResolveInput) (Selection, error) {
	cli, cliSet, err := optionalName(in.CLI)
	if err != nil {
		return Selection{}, fmt.Errorf("--backend: %w", err)
	}

	sticky, found, err := stickyBackend(in.Parent, in.Rows, in.ProvisionalIntents)
	if err != nil {
		return Selection{}, err
	}
	if found {
		env, envSet, envErr := optionalName(in.Environment)
		if envErr != nil {
			return Selection{}, fmt.Errorf("FANOUT_BACKEND: %w", envErr)
		}
		if cliSet && cli != sticky {
			return Selection{}, fmt.Errorf("runtime backend for parent %s is %s; --backend requests %s (explicit migration is required)", in.Parent, sticky, cli)
		}
		if envSet && env != sticky {
			return Selection{}, fmt.Errorf("runtime backend for parent %s is %s; FANOUT_BACKEND requests %s (explicit migration is required)", in.Parent, sticky, env)
		}
		return Selection{Name: sticky, Reason: ReasonExistingParent}, nil
	}

	if cliSet {
		return Selection{Name: cli, Reason: ReasonCLI}, nil
	}
	env, envSet, err := optionalName(in.Environment)
	if err != nil {
		return Selection{}, fmt.Errorf("FANOUT_BACKEND: %w", err)
	}
	if envSet {
		return Selection{Name: env, Reason: ReasonEnvironment}, nil
	}
	// HERDR_ENV wins when both context markers are present. A nested tmux user
	// can still select tmux explicitly through either higher-priority source.
	if in.HerdrEnvironment {
		return Selection{Name: Herdr, Reason: ReasonHerdrContext}, nil
	}
	if in.TmuxEnvironment {
		return Selection{Name: Tmux, Reason: ReasonTmuxContext}, nil
	}
	if strings.TrimSpace(in.UserDefault) != "" {
		name, parseErr := ParseName(in.UserDefault)
		if parseErr != nil {
			return Selection{}, fmt.Errorf("user config runtimeBackend: %w", parseErr)
		}
		return Selection{Name: name, Reason: ReasonUserConfig}, nil
	}
	return Selection{Name: Tmux, Reason: ReasonDefault}, nil
}

func optionalName(raw string) (Name, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return "", false, nil
	}
	name, err := ParseName(raw)
	return name, true, err
}

func stickyBackend(parent string, rows, intents []Binding) (Name, bool, error) {
	parent = parentref.Canon(parent)
	seen := map[Name]bool{}
	collect := func(binding Binding, legacyRow bool) error {
		if parentref.Canon(binding.Parent) != parent {
			return nil
		}
		name := binding.Backend
		if strings.TrimSpace(string(name)) == "" {
			if !legacyRow {
				return fmt.Errorf("runtime backend provisional intent for parent %s has no backend", parent)
			}
			name = Tmux
		}
		if _, err := ParseName(string(name)); err != nil {
			return fmt.Errorf("runtime backend for parent %s: %w", parent, err)
		}
		seen[name] = true
		return nil
	}
	for _, binding := range rows {
		if err := collect(binding, true); err != nil {
			return "", false, err
		}
	}
	for _, binding := range intents {
		if err := collect(binding, false); err != nil {
			return "", false, err
		}
	}
	if len(seen) == 0 {
		return "", false, nil
	}
	if len(seen) > 1 {
		names := make([]string, 0, len(seen))
		for name := range seen {
			names = append(names, string(name))
		}
		slices.Sort(names)
		return "", false, fmt.Errorf("runtime backend for parent %s has mixed state: %s", parent, strings.Join(names, ", "))
	}
	for name := range seen {
		return name, true, nil
	}
	panic("unreachable")
}
