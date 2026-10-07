package main

import (
	"errors"
	"net/http"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
)

// forwardStarted reports whether a forward failed after the target's status
// and headers had been written to the client. A *lbplane.SendError,
// ErrClientWentAway or ErrDeclined means nothing had been.
func forwardStarted(err error) bool {
	return err != nil && !errors.As(err, new(*lbplane.SendError)) &&
		!errors.Is(err, lbplane.ErrClientWentAway) && !errors.Is(err, lbplane.ErrDeclined)
}

// abortStartedForward ends the client's response when the forward failed after
// it started: the front end can no longer answer with an error status, so it
// sends what the target sent and closes the stream, as a proxy that streams
// its upstream's answer and then loses it does. Any other failure leaves the
// answer to the caller.
func abortStartedForward(w http.ResponseWriter, err error) {
	if !forwardStarted(err) {
		return
	}
	_ = http.NewResponseController(w).Flush()
	panic(http.ErrAbortHandler)
}
