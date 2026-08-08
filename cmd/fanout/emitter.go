package main

import (
	"context"
	"fmt"

	"github.com/butaosuinu/fanout/internal/app/stateemitter"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

type herdrEmitterObserver struct{}

func (herdrEmitterObserver) Observe(
	ctx context.Context,
	target stateemitter.RuntimeTarget,
) (stateemitter.Observation, error) {
	owned, err := herdrrun.OpenOwned(ctx, herdrrun.OwnedOptions{GitCommonDir: target.RepoKey})
	if err != nil {
		return stateemitter.Observation{}, err
	}
	if owned.Session != target.Session || owned.SocketPath != target.SocketPath {
		return stateemitter.Observation{}, fmt.Errorf("current Herdr owner route does not match launch binding")
	}
	panes, err := owned.LivePanes(ctx)
	if err != nil {
		return stateemitter.Observation{}, err
	}
	process, err := owned.ProcessInfo(ctx, target.PaneID)
	return stateemitter.Observation{
		Panes: panes, ProcessInfo: process, ProcessError: err,
	}, nil
}
