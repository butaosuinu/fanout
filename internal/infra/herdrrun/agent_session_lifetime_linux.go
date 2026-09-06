//go:build linux

package herdrrun

import (
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

type agentSessionRelayProcessLifetime struct {
	fd int
}

func newAgentSessionRelayProcessLifetime(pid int) (*agentSessionRelayProcessLifetime, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, fmt.Errorf("watch relay workload process: %w", err)
	}
	return &agentSessionRelayProcessLifetime{fd: fd}, nil
}

func (lifetime *agentSessionRelayProcessLifetime) Read(_ []byte) (int, error) {
	events := []unix.PollFd{{Fd: int32(lifetime.fd), Events: unix.POLLIN}}
	for {
		ready, err := unix.Poll(events, -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("wait for relay workload process: %w", err)
		}
		if ready == 1 && events[0].Revents&unix.POLLIN != 0 {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("wait for relay workload process: unexpected poll event")
	}
}

func (lifetime *agentSessionRelayProcessLifetime) Close() error {
	return unix.Close(lifetime.fd)
}
