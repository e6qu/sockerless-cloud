package sim

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processExit returns a wait that blocks until process pid has exited, through
// a pidfd the kernel makes readable when the process ends. A process already
// gone yields a wait that returns at once.
func processExit(pid int) (wait func(), err error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return func() {}, nil
	}
	if err != nil {
		return nil, err
	}
	return func() {
		defer func() { _ = unix.Close(fd) }()
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			if _, err := unix.Poll(fds, -1); !errors.Is(err, unix.EINTR) {
				return
			}
		}
	}, nil
}
