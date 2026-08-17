package main

import (
	"context"
	"errors"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
)

func TestRuntimeEmitterObserverUsesOwnerGitCommonDir(t *testing.T) {
	wantErr := errors.New("stop after admission input")
	var got paneruntime.ObservationRequest
	observer := runtimeEmitterObserver{observe: func(
		_ context.Context,
		req paneruntime.ObservationRequest,
	) (paneruntime.Observation, error) {
		got = req
		return paneruntime.Observation{}, wantErr
	}}

	_, err := observer.Observe(context.Background(), stateemitter.RuntimeTarget{
		RepoKey: "", GitCommonDir: "/repo/.git", PaneID: "w1:p1",
	})
	if !errors.Is(err, wantErr) || got.GitCommonDir != "/repo/.git" || got.PaneID != "w1:p1" {
		t.Fatalf("Observe() = (%+v, %v), want owner Git common dir and sentinel", got, err)
	}
}
