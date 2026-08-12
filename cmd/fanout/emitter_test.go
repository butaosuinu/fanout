package main

import (
	"context"
	"errors"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

func TestHerdrEmitterObserverUsesOwnerGitCommonDir(t *testing.T) {
	wantErr := errors.New("stop after admission input")
	var got string
	observer := herdrEmitterObserver{openOwned: func(
		_ context.Context,
		opts herdrrun.OwnedOptions,
	) (*herdrrun.OwnedSession, error) {
		got = opts.GitCommonDir
		return nil, wantErr
	}}

	_, err := observer.Observe(context.Background(), stateemitter.RuntimeTarget{
		RepoKey: "", GitCommonDir: "/repo/.git",
	})
	if !errors.Is(err, wantErr) || got != "/repo/.git" {
		t.Fatalf("Observe() = (%q, %v), want owner Git common dir and sentinel", got, err)
	}
}
