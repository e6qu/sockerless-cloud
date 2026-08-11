package simulator

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

// TunnelUpgradedResponse finishes a 101 Switching Protocols by introducing the two
// connections to each other and copying bytes until one of them ends.
//
// Relaying the handshake is not enough, and getting only that half right fails in a
// way that looks like success. `client.Do` + `io.Copy(w, resp.Body)` hands the client
// a correct-looking 101 — right `Sec-WebSocket-Accept`, right negotiated extensions —
// and then silently drops everything the client sends, because a ResponseWriter has
// no path back to the target. The client's frames go nowhere, the target never
// answers something it never received, and the peer reports a HANDSHAKE TIMEOUT
// rather than a connection error. So this runs for every 101, whatever the protocol.
//
// Returns an error only for failures that happen BEFORE the client connection is
// hijacked, so the caller can still write an error response. Once the hijack
// succeeds the ResponseWriter is spent and nothing may be written to it; a tunnel
// torn down by either peer is a normal ending, not a proxy error, so it reports nil.
func TunnelUpgradedResponse(w http.ResponseWriter, resp *http.Response) error {
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
		return fmt.Errorf("serialize upgrade response headers: %w", err)
	}
	handshake.WriteString("\r\n")
	if _, err := clientBuf.Write(handshake.Bytes()); err != nil {
		return fmt.Errorf("write upgrade response: %w", err)
	}
	if err := clientBuf.Flush(); err != nil {
		return fmt.Errorf("flush upgrade response: %w", err)
	}

	// Read the client through clientBuf, never through the raw connection: a client
	// that speaks first has its opening bytes sitting in that reader already, and
	// reading past it drops them.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, clientBuf); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
	// One direction ended, so the session is over; closing both unblocks the other
	// copy, which would otherwise hold this handler open for the connection's life.
	client.Close()
	target.Close()
	<-done
	return nil
}
