package lbplane

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// IsUpgradeRequest reports whether r asks to leave HTTP behind — `Connection:
// Upgrade` naming a protocol in `Upgrade:` (WebSocket in practice, but the check is
// protocol-agnostic because the tunnelling below is too).
//
// A forwarding data plane must know this BEFORE it sends the request: an upgraded
// connection is long-lived, so the request timeout a normal proxied request needs
// would sever it mid-session.
func IsUpgradeRequest(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// tunnelUpgradedResponse finishes a 101 Switching Protocols by introducing the
// two connections to each other and copying bytes until one of them ends.
//
// Relaying the handshake alone fails while looking like success: the client
// gets a correct 101 — right `Sec-WebSocket-Accept`, right extensions — and
// everything it then sends goes nowhere, since a ResponseWriter has no path
// back to the target, until the peer reports a handshake timeout. So this
// runs for every 101, whatever the protocol.
//
// A failure before the client connection is hijacked leaves the caller free
// to write an error response. Once the hijack succeeds the ResponseWriter is
// spent, so a failure to write the handshake wraps ErrAnswerStarted; a tunnel
// either peer tears down is a normal ending and reports nil.
//
// A positive idle closes the tunnel once that long passes with no byte read
// from either peer, and activity runs whenever a byte is.
func tunnelUpgradedResponse(w http.ResponseWriter, resp *http.Response, idle time.Duration, activity func()) error {
	// Go's transport exposes a 101's connection as a ReadWriteCloser precisely so it
	// can be tunnelled; anything else means the response never really upgraded.
	target, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return fmt.Errorf("target returned %d but its connection cannot be written to", resp.StatusCode)
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("cannot hijack the client connection to tunnel a %d", resp.StatusCode)
	}
	client, clientBuf, err := hijacker.Hijack()
	if err != nil {
		return fmt.Errorf("hijack client connection: %w", err)
	}
	defer client.Close()
	// An upgraded connection lives until a peer closes it. Whatever deadline covered
	// the handshake must not outlive the handshake.
	_ = client.SetDeadline(time.Time{})

	var handshake bytes.Buffer
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&handshake, "HTTP/1.1 %s\r\n", status)
	if err := resp.Header.Write(&handshake); err != nil {
		return fmt.Errorf("%w: serialize upgrade response headers: %w", ErrAnswerStarted, err)
	}
	handshake.WriteString("\r\n")
	if _, err := clientBuf.Write(handshake.Bytes()); err != nil {
		return fmt.Errorf("%w: write upgrade response: %w", ErrAnswerStarted, err)
	}
	if err := clientBuf.Flush(); err != nil {
		return fmt.Errorf("%w: flush upgrade response: %w", ErrAnswerStarted, err)
	}

	// Read the client through clientBuf, never through the raw connection: a client
	// that speaks first has its opening bytes sitting in that reader already, and
	// reading past it drops them.
	watch := newIdleWatch(idle, activity, func() {
		client.Close()
		target.Close()
	})
	defer watch.stop()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, watch.reader(clientBuf)); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, watch.reader(target)); done <- struct{}{} }()
	<-done
	// One direction ended, so the session is over; closing both unblocks the other
	// copy, which would otherwise hold this handler open for the connection's life.
	client.Close()
	target.Close()
	<-done
	return nil
}
