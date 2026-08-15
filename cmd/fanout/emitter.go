package main

import (
	"context"

	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
)

// runtimeEmitterObserver adapts the runtime observation to the app's telemetry
// port. The adapter lives in the composition root because the port is named in
// app types the runtime layer must not import; observe carries only the
// core-typed request paneruntime accepts.
type runtimeEmitterObserver struct {
	observe func(context.Context, paneruntime.ObservationRequest) (paneruntime.Observation, error)
}

func (o runtimeEmitterObserver) Observe(
	ctx context.Context,
	target stateemitter.RuntimeTarget,
) (stateemitter.Observation, error) {
	observe := o.observe
	if observe == nil {
		observe = paneruntime.ObserveManaged
	}
	got, err := observe(ctx, paneruntime.ObservationRequest{
		GitCommonDir: target.GitCommonDir,
		Session:      target.Session,
		SocketPath:   target.SocketPath,
		PaneID:       target.PaneID,
	})
	if err != nil {
		return stateemitter.Observation{}, err
	}
	return stateemitter.Observation{
		Panes: got.Panes, ProcessInfo: got.ProcessInfo, ProcessError: got.ProcessError,
	}, nil
}
