package sim

import (
	"errors"

	"golang.org/x/sys/unix"
)

// processExit returns a wait that blocks until process pid has exited, through
// a kqueue EVFILT_PROC filter for NOTE_EXIT. A process already gone yields a
// wait that returns at once.
func processExit(pid int) (wait func(), err error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, err
	}
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kq)
		if errors.Is(err, unix.ESRCH) {
			return func() {}, nil
		}
		return nil, err
	}
	return func() {
		defer func() { _ = unix.Close(kq) }()
		events := make([]unix.Kevent_t, 1)
		for {
			n, err := unix.Kevent(kq, nil, events, nil)
			if n > 0 || !errors.Is(err, unix.EINTR) {
				return
			}
		}
	}, nil
}
