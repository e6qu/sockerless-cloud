package main

import (
	"errors"
	"net/http"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
)

// abortStartedForward ends the client's response when a forward failed after
// the target's status and headers had been written to it: the front end can no
// longer answer with an error status, so it sends what the target sent and
// closes the stream, as a proxy that streams its upstream's answer and then
// loses it does. A failure before that — a *lbplane.SendError,
// ErrClientWentAway or ErrDeclined — leaves the answer to the caller.
func abortStartedForward(w http.ResponseWriter, err error) {
	if err == nil || errors.As(err, new(*lbplane.SendError)) ||
		errors.Is(err, lbplane.ErrClientWentAway) || errors.Is(err, lbplane.ErrDeclined) {
		return
	}
	_ = http.NewResponseController(w).Flush()
	panic(http.ErrAbortHandler)
}
