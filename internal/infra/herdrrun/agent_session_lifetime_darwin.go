//go:build darwin

package herdrrun

import (
	"fmt"
	"io"

	"golang.org/x/sys/unix"
)

type agentSessionRelayProcessLifetime struct {
	fd  int
	pid uint64
}

func newAgentSessionRelayProcessLifetime(pid int) (*agentSessionRelayProcessLifetime, error) {
	fd, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("watch relay workload process: %w", err)
	}
	event := unix.Kevent_t{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC,
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT,
	}
	if _, err := unix.Kevent(fd, []unix.Kevent_t{event}, nil, nil); err != nil {
		_ = unix.Close(fd) // The failed watcher has no caller owner.
		return nil, fmt.Errorf("watch relay workload process: %w", err)
	}
	return &agentSessionRelayProcessLifetime{fd: fd, pid: uint64(pid)}, nil
}

func (lifetime *agentSessionRelayProcessLifetime) Read(_ []byte) (int, error) {
	events := make([]unix.Kevent_t, 1)
	for {
		ready, err := unix.Kevent(lifetime.fd, nil, events, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("wait for relay workload process: %w", err)
		}
		if ready == 1 && events[0].Ident == lifetime.pid && events[0].Fflags&unix.NOTE_EXIT != 0 {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("wait for relay workload process: unexpected process event")
	}
}

func (lifetime *agentSessionRelayProcessLifetime) Close() error {
	return unix.Close(lifetime.fd)
}
