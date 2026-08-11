package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.ServiceBus REST data plane. Routed by Host header
// (`{namespace}.servicebus.<sim-host>:<port>`). Real Azure exposes:
//
//   POST   /{queue}/messages                            SendMessage           → 201
//   DELETE /{queue}/messages/head                       ReceiveAndDelete      → 200 (body) or 204
//   POST   /{queue}/messages/head                       PeekLock              → 201 (body+Location) or 204
//   DELETE /{queue}/messages/{guid}/{lockToken}         CompleteLock          → 204
//   POST   /{topic}/messages                            SendMessage (topic)   → 201
//   DELETE /{topic}/subscriptions/{sub}/messages/head   Sub ReceiveAndDelete  → 200 or 204
//   POST   /{topic}/subscriptions/{sub}/messages/head   Sub PeekLock          → 201 or 204
//   DELETE /{topic}/subscriptions/{sub}/messages/{guid}/{lockToken}  Complete → 204
//
// The AMQP data plane is exposed as raw AMQP/TLS on the configured
// Service Bus AMQP listener and as AMQP-over-WebSocket on
// `/$servicebus/websocket` for clients that opt into that transport.
// The AMQP slice implements SASL anonymous, CBS claim negotiation,
// entity sender/receiver links, and accepted delivery dispositions.

// sbMessage is a single enqueued message. Lock semantics: when a
// PeekLock returns a message, LockedUntilUtc is set + LockToken is
// generated; CompleteLock with the matching token removes the message
// from the queue. ReceiveAndDelete atomically removes without lock.
type sbMessage struct {
	MessageID      string
	Body           []byte
	ContentType    string
	BrokerHeader   string
	EnqueuedTime   time.Time
	LockedUntilUtc time.Time
	LockToken      string
	SequenceNumber int64
}

// sbQueueState is the per-queue message log. Topic subscriptions
// share the same shape; one sbQueueState per `{namespace}/{topic}/{sub}`.
// It is the in-process working copy of the queue; sbQueueDurable holds the
// durable copy, written through under mu on every mutation so messages and
// sequence numbers survive a SIM_PERSIST restart (a restart that rewound
// sequence numbers would break consumer checkpoints, exactly as it would
// against real Service Bus, which never reissues a sequence number).
type sbQueueState struct {
	mu       sync.Mutex
	key      string
	messages []sbMessage
	nextSeq  int64
}

// sbQueueRecord is the durable snapshot of one queue (or topic
// subscription): its pending messages and the next sequence number.
type sbQueueRecord struct {
	Messages []sbMessage `json:"messages"`
	NextSeq  int64       `json:"nextSeq"`
}

var (
	sbQueueMessages = sync.Map{} // key: "{namespace}/{queue}" or "{namespace}/{topic}/{sub}" → *sbQueueState
	sbQueueDurable  sim.Store[sbQueueRecord]
)

func sbQueueKey(namespace, path string) string {
	return namespace + "/" + path
}

// sbQueueStateFor returns the working copy for a queue key, loading the
// durable record lazily on the first access after a restart.
func sbQueueStateFor(key string) *sbQueueState {
	if v, ok := sbQueueMessages.Load(key); ok {
		if st, ok := v.(*sbQueueState); ok {
			return st
		}
	}
	st := &sbQueueState{key: key}
	if rec, ok := sbQueueDurable.Get(key); ok {
		st.messages = rec.Messages
		st.nextSeq = rec.NextSeq
	}
	actual, _ := sbQueueMessages.LoadOrStore(key, st)
	loaded, ok := actual.(*sbQueueState)
	if !ok {
		sbQueueMessages.Store(key, st)
		return st
	}
	return loaded
}

// persistLocked writes the queue's durable snapshot. Callers hold st.mu, so
// snapshots are serialized per queue and never interleave out of order.
func (st *sbQueueState) persistLocked() {
	sbQueueDurable.Put(st.key, sbQueueRecord{Messages: st.messages, NextSeq: st.nextSeq})
}

// sbQueueCounts reports the total and currently-deliverable (unlocked or
// lock-expired) message counts for a data-plane queue or subscription path —
// the numbers the admin plane's MessageCount / ActiveMessageCount reflect on
// real Service Bus.
func sbQueueCounts(namespace, path string) (total int64, active int32) {
	st := sbQueueStateFor(sbQueueKey(namespace, path))
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now().UTC()
	for i := range st.messages {
		if st.messages[i].LockToken == "" || st.messages[i].LockedUntilUtc.Before(now) {
			active++
		}
	}
	return int64(len(st.messages)), active
}

// registerServiceBusDataPlane wires the subdomain dispatcher. Requests
// arriving with a `{namespace}.servicebus.<host>` Host route here.
func registerServiceBusDataPlane(srv *sim.Server) {
	sbQueueDurable = sim.MakeStore[sbQueueRecord](srv.DB(), "servicebus_queue_messages")
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if i := strings.LastIndex(host, ":"); i >= 0 {
				host = host[:i]
			}
			parts := strings.SplitN(host, ".servicebus.", 2)
			if len(parts) != 2 {
				next.ServeHTTP(w, r)
				return
			}
			if strings.Trim(r.URL.Path, "/") == "$servicebus/websocket" {
				// The AMQP transport authenticates through the CBS
				// put-token handshake once the connection is established.
				handleSBAMQPWebSocket(w, r, parts[0])
				return
			}
			if !authorizeSBHTTPRequest(w, r, parts[0]) {
				return
			}
			if handleSBAdminDataPlane(w, r, parts[0]) {
				return
			}
			handleSBRESTDataPlane(w, r, parts[0])
		})
	})
}

// authorizeSBHTTPRequest verifies the Shared Access Signature every Service
// Bus HTTP caller — REST data plane and ATOM admin plane alike — presents in
// the Authorization header, and writes the service's 401 when it does not
// hold. The token must be signed with the current key of an authorization
// rule at the addressed namespace or entity, so a rotated key takes effect
// immediately.
func authorizeSBHTTPRequest(w http.ResponseWriter, r *http.Request, namespace string) bool {
	audience, err := verifyMessagingSAS(namespace, r.Header.Get("Authorization"))
	if err != nil {
		authErr, ok := err.(*sasAuthError)
		if !ok {
			authErr = errSASInvalidSignature
		}
		writeSBUnauthorized(w, authErr.Description)
		return false
	}
	entity := strings.Trim(r.URL.Path, "/")
	if !sasAudienceCoversEntity(audience, entity) {
		writeSBUnauthorized(w,
			fmt.Sprintf("Unauthorized access. The provided token does not grant access to %q.", entity))
		return false
	}
	return true
}

// writeSBUnauthorized emits the `<Error><Code>401</Code><Detail>…</Detail></Error>`
// body Service Bus returns for a refused token.
func writeSBUnauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, "<Error><Code>401</Code><Detail>%s</Detail></Error>", detail)
}

func handleSBRESTDataPlane(w http.ResponseWriter, r *http.Request, namespace string) {
	path := strings.Trim(r.URL.Path, "/")
	segs := strings.Split(path, "/")
	if len(segs) == 0 || segs[0] == "" {
		sim.AzureError(w, "BadRequest", "Missing path", http.StatusBadRequest)
		return
	}

	// Topic + subscription routes: /{topic}/subscriptions/{sub}/messages/...
	if len(segs) >= 4 && segs[1] == "subscriptions" && segs[3] == "messages" {
		topic, sub := segs[0], segs[2]
		key := sbQueueKey(namespace, topic+"/"+sub)
		dispatchSBMessagesOp(w, r, key, segs[4:])
		return
	}

	// Queue routes (or topic-Send): /{queue}/messages/...
	if len(segs) >= 2 && segs[1] == "messages" {
		queue := segs[0]
		key := sbQueueKey(namespace, queue)
		dispatchSBMessagesOp(w, r, key, segs[2:])
		return
	}

	sim.AzureError(w, "ResourceNotFound", "Unknown REST path: "+r.URL.Path, http.StatusNotFound)
}

// dispatchSBMessagesOp handles the /messages/... tail. `tail` is the
// remaining path segments after `messages`.
func dispatchSBMessagesOp(w http.ResponseWriter, r *http.Request, key string, tail []string) {
	switch {
	case r.Method == http.MethodPost && len(tail) == 0:
		// POST .../messages — SendMessage.
		handleSBSendMessage(w, r, key)

	case r.Method == http.MethodDelete && len(tail) == 1 && tail[0] == "head":
		// DELETE .../messages/head — ReceiveAndDelete.
		handleSBReceiveAndDelete(w, r, key)

	case r.Method == http.MethodPost && len(tail) == 1 && tail[0] == "head":
		// POST .../messages/head — PeekLock.
		handleSBPeekLock(w, r, key)

	case r.Method == http.MethodDelete && len(tail) == 2:
		// DELETE .../messages/{guid}/{lockToken} — CompleteLock.
		handleSBCompleteLock(w, r, key, tail[0], tail[1])

	default:
		sim.AzureError(w, "MethodNotAllowed",
			fmt.Sprintf("Unsupported %s on %s", r.Method, r.URL.Path),
			http.StatusMethodNotAllowed)
	}
}

func handleSBSendMessage(w http.ResponseWriter, r *http.Request, key string) {
	defer r.Body.Close()
	// Service Bus REST sends the message body opaquely with
	// Content-Length framing; metadata travels in BrokerProperties
	// header. azservicebus's REST transcoder does not gzip-encode
	// or chunk-envelope, so a raw read is safe (no openStreamingBody
	// wrap required).
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sim.AzureError(w, "BadRequest", "Failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	st := sbQueueStateFor(key)
	st.mu.Lock()
	st.nextSeq++
	msg := sbMessage{
		MessageID:      generateUUID(),
		Body:           body,
		ContentType:    r.Header.Get("Content-Type"),
		BrokerHeader:   r.Header.Get("BrokerProperties"),
		EnqueuedTime:   time.Now().UTC(),
		SequenceNumber: st.nextSeq,
	}
	st.messages = append(st.messages, msg)
	st.persistLocked()
	st.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func handleSBReceiveAndDelete(w http.ResponseWriter, r *http.Request, key string) {
	st := sbQueueStateFor(key)
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.messages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	msg := st.messages[0]
	st.messages = st.messages[1:]
	st.persistLocked()
	if err := writeSBMessageResponse(w, msg, "", http.StatusOK); err != nil {
		// Headers may already be on the wire; can't switch to a 500
		// envelope at this point. Log via the request logger.
		sim.AzureError(w, "InternalServerError",
			"emit Service Bus response: "+err.Error(),
			http.StatusInternalServerError)
	}
}

func handleSBPeekLock(w http.ResponseWriter, r *http.Request, key string) {
	st := sbQueueStateFor(key)
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.messages) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Find the first unlocked message (lock not yet expired).
	now := time.Now().UTC()
	idx := -1
	for i := range st.messages {
		if st.messages[i].LockToken == "" || st.messages[i].LockedUntilUtc.Before(now) {
			idx = i
			break
		}
	}
	if idx == -1 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	st.messages[idx].LockToken = generateUUID()
	st.messages[idx].LockedUntilUtc = now.Add(60 * time.Second)
	st.persistLocked()
	msg := st.messages[idx]
	// Real Service Bus emits Location as
	// `https://{ns}/{queue}/messages/{messageID}/{lockToken}` (or
	// `…/{topic}/subscriptions/{sub}/messages/{messageID}/{lockToken}`
	// for subscriptions). The path-after-namespace lives in
	// `key`'s tail (everything after the first "/"); inserting
	// `/messages/` here aligns with handleSBRESTDataPlane's dispatch,
	// so the client can DELETE the Location verbatim to CompleteLock.
	pathTail := strings.SplitN(key, "/", 2)[1]
	location := fmt.Sprintf("https://%s/%s/messages/%s/%s",
		r.Host, pathTail, msg.MessageID, msg.LockToken)
	if err := writeSBMessageResponse(w, msg, location, http.StatusCreated); err != nil {
		sim.AzureError(w, "InternalServerError",
			"emit Service Bus PeekLock response: "+err.Error(),
			http.StatusInternalServerError)
	}
}

func handleSBCompleteLock(w http.ResponseWriter, r *http.Request, key, msgID, lockToken string) {
	st := sbQueueStateFor(key)
	st.mu.Lock()
	defer st.mu.Unlock()
	idx := -1
	for i := range st.messages {
		if st.messages[i].MessageID == msgID && st.messages[i].LockToken == lockToken {
			idx = i
			break
		}
	}
	if idx == -1 {
		sim.AzureError(w, "MessageLockLost",
			"Message lock not found or expired", http.StatusGone)
		return
	}
	st.messages = append(st.messages[:idx], st.messages[idx+1:]...)
	st.persistLocked()
	w.WriteHeader(http.StatusNoContent)
}

// writeSBMessageResponse emits the canonical wire shape Service Bus
// uses for Receive responses: BrokerProperties header (JSON-encoded
// metadata), optional Location header (for PeekLock), and the raw
// body bytes as the response body. Status is caller-supplied (200 for
// ReceiveAndDelete, 201 for PeekLock). Returns an error when the
// BrokerProperties JSON marshal fails BEFORE the status line goes on
// the wire — the metadata is load-bearing (SDK reads MessageId /
// SequenceNumber / LockToken from it), and the caller must respond
// with a 500 instead of a header-less response that "looks 200" to
// the client.
func writeSBMessageResponse(w http.ResponseWriter, msg sbMessage, location string, status int) error {
	brokerProps := map[string]any{
		"MessageId":       msg.MessageID,
		"DeliveryCount":   1,
		"EnqueuedTimeUtc": msg.EnqueuedTime.Format(time.RFC3339Nano),
		"SequenceNumber":  msg.SequenceNumber,
		"State":           "Active",
	}
	if !msg.LockedUntilUtc.IsZero() {
		brokerProps["LockedUntilUtc"] = msg.LockedUntilUtc.Format(time.RFC3339Nano)
		brokerProps["LockToken"] = msg.LockToken
	}
	b, err := json.Marshal(brokerProps)
	if err != nil {
		return fmt.Errorf("marshal BrokerProperties: %w", err)
	}
	w.Header().Set("BrokerProperties", string(b))
	if msg.BrokerHeader != "" {
		w.Header().Set("X-Sender-BrokerProperties", msg.BrokerHeader)
	}
	if msg.ContentType != "" {
		w.Header().Set("Content-Type", msg.ContentType)
	}
	if location != "" {
		w.Header().Set("Location", location)
	}
	w.WriteHeader(status)
	// Post-status write failures mean the client disconnected mid-
	// response — the response envelope is already committed, so we
	// can't switch to a 500 here. Drop the error: the request logger
	// (sim.Server) records the connection close.
	_, _ = w.Write(msg.Body)
	return nil
}
