package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// The Service Bus REST data plane, routed by Host
// (`{namespace}.servicebus.<sim-host>:<port>`):
//
//   POST   /{entity}/messages                           Send Message          → 201
//   DELETE /{entity}/messages/head                      Receive and Delete    → 200 (body) or 204
//   POST   /{entity}/messages/head                      Peek-Lock             → 201 (body+Location) or 204
//   DELETE /{entity}/messages/{id}/{lockToken}          Delete (complete)     → 204
//   PUT    /{entity}/messages/{id}/{lockToken}          Unlock (abandon)      → 200
//   POST   /{entity}/messages/{id}/{lockToken}          Renew-Lock            → 200
//
// {entity} is a queue, a topic (send only), `{topic}/subscriptions/{sub}`, or
// either one's `$DeadLetterQueue`. AMQP is served as raw AMQP/TLS on the
// Service Bus AMQP listener and over WebSocket at `/$servicebus/websocket`.

func registerServiceBusDataPlane(srv *sim.Server) {
	sbQueueDurable = sim.MakeStore[sbQueueRecord](srv.DB(), "servicebus_queue_messages")
	if err := sbMigrateQueues(srv.DB()); err != nil {
		log.Fatalf("servicebus: %v", err)
	}
	sbArmStoredQueues()
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
				// The AMQP transport authenticates through CBS put-token once connected.
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

// Every Service Bus HTTP caller, REST and ATOM admin alike, presents a SAS
// signed with the current key of a rule at the namespace or entity, so a
// rotated key takes effect at once.
func authorizeSBHTTPRequest(w http.ResponseWriter, r *http.Request, namespace string) bool {
	// A trusted Microsoft service writing on its own behalf, as Event Grid
	// delivers to a queue or topic, is admitted without a signature.
	if azureTrustedServiceCall(r) {
		return true
	}
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

// The `<Error><Code>401</Code><Detail>…</Detail></Error>` body Service Bus
// returns for a refused token.
func writeSBUnauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, "<Error><Code>401</Code><Detail>%s</Detail></Error>", detail)
}

func handleSBRESTDataPlane(w http.ResponseWriter, r *http.Request, namespace string) {
	path := strings.Trim(r.URL.Path, "/")
	segs := strings.Split(path, "/")
	if len(segs) == 0 || segs[0] == "" {
		AzureError(w, "BadRequest", "Missing path", http.StatusBadRequest)
		return
	}
	// The entity path runs up to `messages`: a queue or topic, a topic's
	// subscriptions/{sub}, and either one's $DeadLetterQueue.
	i := 0
	for i < len(segs) && !strings.EqualFold(segs[i], "messages") {
		i++
	}
	if i == len(segs) {
		AzureError(w, "ResourceNotFound", "Unknown REST path: "+r.URL.Path, http.StatusNotFound)
		return
	}
	entity := segs[:i]
	var dataPath string
	switch {
	case len(entity) == 1:
		dataPath = entity[0]
	case len(entity) == 2 && strings.EqualFold(entity[1], sbDeadLetterSuffix):
		dataPath = sbDeadLetterPath(entity[0])
	case len(entity) == 3 && strings.EqualFold(entity[1], "subscriptions"):
		dataPath = entity[0] + "/" + entity[2]
	case len(entity) == 4 && strings.EqualFold(entity[1], "subscriptions") && strings.EqualFold(entity[3], sbDeadLetterSuffix):
		dataPath = sbDeadLetterPath(entity[0] + "/" + entity[2])
	default:
		AzureError(w, "ResourceNotFound", "Unknown REST path: "+r.URL.Path, http.StatusNotFound)
		return
	}
	dispatchSBMessagesOp(w, r, namespace, dataPath, segs[i+1:])
}

// tail is the path segments after `messages`.
func dispatchSBMessagesOp(w http.ResponseWriter, r *http.Request, namespace, path string, tail []string) {
	switch {
	case r.Method == http.MethodPost && len(tail) == 0:
		handleSBSendMessage(w, r, namespace, path)
	case r.Method == http.MethodDelete && len(tail) == 1 && tail[0] == "head":
		handleSBReceive(w, r, namespace, path, false)
	case r.Method == http.MethodPost && len(tail) == 1 && tail[0] == "head":
		handleSBReceive(w, r, namespace, path, true)
	case len(tail) == 2 && (r.Method == http.MethodDelete || r.Method == http.MethodPut || r.Method == http.MethodPost):
		handleSBLockedMessage(w, r, namespace, path, tail[0], tail[1])
	default:
		AzureError(w, "MethodNotAllowed",
			fmt.Sprintf("Unsupported %s on %s", r.Method, r.URL.Path),
			http.StatusMethodNotAllowed)
	}
}

type sbSenderProperties struct {
	MessageID               string  `json:"MessageId"`
	TimeToLive              float64 `json:"TimeToLive"`
	ScheduledEnqueueTimeUtc string  `json:"ScheduledEnqueueTimeUtc"`
	SessionID               string  `json:"SessionId"`
}

func handleSBSendMessage(w http.ResponseWriter, r *http.Request, namespace, path string) {
	defer r.Body.Close()
	// The body is opaque; its metadata travels in BrokerProperties.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		AzureError(w, "BadRequest", "Failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	out := sbOutgoing{payload: sbPayload{
		Body:         body,
		ContentType:  r.Header.Get("Content-Type"),
		BrokerHeader: r.Header.Get("BrokerProperties"),
	}}
	if raw := out.payload.BrokerHeader; raw != "" {
		var props sbSenderProperties
		if err := json.Unmarshal([]byte(raw), &props); err != nil {
			AzureError(w, "BadRequest", "The BrokerProperties header is not valid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		out.messageID = props.MessageID
		out.sessionID = props.SessionID
		if props.TimeToLive > 0 {
			out.ttl = time.Duration(props.TimeToLive * float64(time.Second))
		}
		if props.ScheduledEnqueueTimeUtc != "" {
			at, err := http.ParseTime(props.ScheduledEnqueueTimeUtc)
			if err != nil {
				AzureError(w, "BadRequest", "ScheduledEnqueueTimeUtc is not an HTTP date: "+err.Error(), http.StatusBadRequest)
				return
			}
			out.delay = time.Until(at)
		}
	}
	if err := sbRequireSessionIDs(namespace, path, []sbOutgoing{out}); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	reached := sbSend(namespace, path, out)
	if err := sbAMQPDeliverAvailableMessages(namespace, reached); err != nil {
		AzureError(w, "InternalServerError", "deliver to AMQP receivers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

const sbRESTReceiveDefaultTimeout = 60 * time.Second

// In whole seconds.
func sbRESTReceiveTimeout(r *http.Request) (time.Duration, bool) {
	v := r.URL.Query().Get("timeout")
	if v == "" {
		return sbRESTReceiveDefaultTimeout, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// Both wait up to `timeout` for a receivable message and answer 204 when none
// arrives.
func handleSBReceive(w http.ResponseWriter, r *http.Request, namespace, path string, peekLock bool) {
	// The REST plane cannot accept a session, so it cannot receive from an entity
	// that only a session lock holder may take from.
	if !sbIsDeadLetterPath(path) && sbSettings(namespace, path).requiresSession {
		AzureError(w, "BadRequest", errSBSessionfulEntity.Error(), http.StatusBadRequest)
		return
	}
	timeout, ok := sbRESTReceiveTimeout(r)
	if !ok {
		AzureError(w, "BadRequest", "The value '"+r.URL.Query().Get("timeout")+
			"' of the timeout query parameter is not a non-negative whole number of seconds.", http.StatusBadRequest)
		return
	}
	got := sbAwaitReceive(r.Context(), namespace, path, timeout, peekLock)
	if len(got) == 0 {
		if r.Context().Err() == nil {
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	m := got[0]
	if !peekLock {
		writeSBMessageResponse(w, m, "", http.StatusOK)
		return
	}
	// Service Bus emits Location as
	// `https://{ns}/{entity}/messages/{messageID}/{lockToken}`; the client DELETEs
	// it verbatim to complete the message.
	location := fmt.Sprintf("https://%s/%s/messages/%s/%s", r.Host, sbRESTEntityPath(path), m.ID, m.Receipt)
	writeSBMessageResponse(w, m, location, http.StatusCreated)
}

// Waits for a message to be sent, scheduled into view, abandoned or freed by an
// expired lock; returns nothing on timeout or when the caller leaves.
func sbAwaitReceive(ctx context.Context, namespace, path string, timeout time.Duration, peekLock bool) []msgq.Message[sbPayload] {
	sim.DeclareWait(ctx, timeout)
	var got []msgq.Message[sbPayload]
	if sbReceivers.Poll(ctx, sbQueueKey(namespace, path), time.Now().Add(timeout), func() bool {
		got, _ = sbReceive(namespace, path, "", 1, peekLock)
		return len(got) > 0
	}) != nil {
		return nil
	}
	return got
}

// The timers of the process that stored them are gone, so arm each entity's
// next change for its AMQP receivers.
func sbArmStoredQueues() {
	for _, kv := range sbQueueDurable.ListPrefix("") {
		sbReceivers.Arm(kv.ID)
	}
}

func sbRESTEntityPath(path string) string {
	dead := sbIsDeadLetterPath(path)
	if dead {
		path = path[:strings.LastIndex(path, "/")]
	}
	if topic, sub, ok := strings.Cut(path, "/"); ok {
		path = topic + "/subscriptions/" + sub
	}
	if dead {
		path += "/" + sbDeadLetterSuffix
	}
	return path
}

// DELETE completes, PUT unlocks, POST renews the lock.
func handleSBLockedMessage(w http.ResponseWriter, r *http.Request, namespace, path, messageID, lockToken string) {
	if !sbLockNamesMessage(namespace, path, messageID, lockToken) {
		AzureError(w, "MessageLockLost", errSBLockLost.Error(), http.StatusGone)
		return
	}
	switch r.Method {
	case http.MethodPost:
		until, err := sbRenewLock(namespace, path, lockToken)
		if err != nil {
			AzureError(w, "MessageLockLost", err.Error(), http.StatusGone)
			return
		}
		b, _ := json.Marshal(map[string]any{"LockedUntilUtc": until.UTC().Format(http.TimeFormat)})
		w.Header().Set("BrokerProperties", string(b))
		w.WriteHeader(http.StatusOK)
		return
	case http.MethodPut:
		if err := sbSettle(namespace, path, lockToken, sbSettlement{kind: sbAbandon}); err != nil {
			AzureError(w, "MessageLockLost", err.Error(), http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := sbSettle(namespace, path, lockToken, sbSettlement{kind: sbComplete}); err != nil {
		AzureError(w, "MessageLockLost", err.Error(), http.StatusGone)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The REST API also accepts the sequence number in the id position.
func sbLockNamesMessage(namespace, path, messageID, lockToken string) bool {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	m := rec.Queue.ByReceipt(lockToken, sbSettings(namespace, path).policy(), time.Now())
	if m == nil {
		return false
	}
	return m.ID == messageID || strconv.FormatUint(m.Seq, 10) == messageID
}

// BrokerProperties is the sender's properties with the broker's own over them;
// Location only for a Peek-Lock.
func writeSBMessageResponse(w http.ResponseWriter, m msgq.Message[sbPayload], location string, status int) {
	brokerProps := map[string]any{}
	if m.Payload.BrokerHeader != "" {
		// handleSBSendMessage refused a header that is not a JSON object.
		_ = json.Unmarshal([]byte(m.Payload.BrokerHeader), &brokerProps)
	}
	brokerProps["MessageId"] = m.ID
	brokerProps["DeliveryCount"] = m.Deliveries
	brokerProps["EnqueuedTimeUtc"] = time.UnixMilli(m.EnqueuedAt).UTC().Format(http.TimeFormat)
	brokerProps["SequenceNumber"] = m.Seq
	brokerProps["State"] = "Active"
	if m.Payload.DeadLetterReason != "" {
		brokerProps["DeadLetterReason"] = m.Payload.DeadLetterReason
		brokerProps["DeadLetterErrorDescription"] = m.Payload.DeadLetterErrorDescription
	}
	if location != "" {
		brokerProps["LockedUntilUtc"] = time.UnixMilli(m.AvailableAt).UTC().Format(http.TimeFormat)
		brokerProps["LockToken"] = m.Receipt
	}
	b, err := json.Marshal(brokerProps)
	if err != nil {
		AzureError(w, "InternalServerError", "marshal BrokerProperties: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("BrokerProperties", string(b))
	if m.Payload.ContentType != "" {
		w.Header().Set("Content-Type", m.Payload.ContentType)
	}
	if location != "" {
		w.Header().Set("Location", location)
	}
	w.WriteHeader(status)
	// A failed write after the status line means the client went away; the
	// request logger records the close.
	_, _ = w.Write(m.Payload.Body)
}
