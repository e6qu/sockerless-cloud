package main

import (
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

// Microsoft.ServiceBus REST data plane. Routed by Host header
// (`{namespace}.servicebus.<sim-host>:<port>`). Real Azure exposes:
//
//   POST   /{entity}/messages                           Send Message          → 201
//   DELETE /{entity}/messages/head                      Receive and Delete    → 200 (body) or 204
//   POST   /{entity}/messages/head                      Peek-Lock             → 201 (body+Location) or 204
//   DELETE /{entity}/messages/{id}/{lockToken}          Delete (complete)     → 204
//   PUT    /{entity}/messages/{id}/{lockToken}          Unlock (abandon)      → 200
//   POST   /{entity}/messages/{id}/{lockToken}          Renew-Lock            → 200
//
// where {entity} is a queue, a topic (send only), `{topic}/subscriptions/{sub}`,
// or either one's `$DeadLetterQueue`.
//
// The AMQP data plane is exposed as raw AMQP/TLS on the configured
// Service Bus AMQP listener and as AMQP-over-WebSocket on
// `/$servicebus/websocket` for clients that opt into that transport.
// The AMQP slice implements SASL anonymous, CBS claim negotiation,
// entity sender/receiver links, and accepted delivery dispositions.

// registerServiceBusDataPlane wires the subdomain dispatcher. Requests
// arriving with a `{namespace}.servicebus.<host>` Host route here.
func registerServiceBusDataPlane(srv *sim.Server) {
	sbQueueDurable = sim.MakeStore[sbQueueRecord](srv.DB(), "servicebus_queue_messages")
	if err := sbMigrateQueues(srv.DB()); err != nil {
		log.Fatalf("servicebus: %v", err)
	}
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
		AzureError(w, "BadRequest", "Missing path", http.StatusBadRequest)
		return
	}
	// The entity path runs up to the `messages` segment: a queue or topic, a
	// topic's subscriptions/{sub}, and either one's $DeadLetterQueue.
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

// dispatchSBMessagesOp handles the /messages/... tail. `tail` is the
// remaining path segments after `messages`.
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

// sbSenderProperties is the part of a REST sender's BrokerProperties header
// the broker acts on.
type sbSenderProperties struct {
	MessageID               string  `json:"MessageId"`
	TimeToLive              float64 `json:"TimeToLive"`
	ScheduledEnqueueTimeUtc string  `json:"ScheduledEnqueueTimeUtc"`
}

func handleSBSendMessage(w http.ResponseWriter, r *http.Request, namespace, path string) {
	defer r.Body.Close()
	// The message body is opaque; its metadata travels in the
	// BrokerProperties header.
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
	reached := sbSend(namespace, path, out)
	if err := sbAMQPDeliverAvailableMessages(namespace, reached); err != nil {
		AzureError(w, "InternalServerError", "deliver to AMQP receivers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// handleSBReceive answers Receive and Delete (DELETE …/messages/head) and
// Peek-Lock (POST …/messages/head).
func handleSBReceive(w http.ResponseWriter, r *http.Request, namespace, path string, peekLock bool) {
	got, _ := sbReceive(namespace, path, 1, peekLock)
	if len(got) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	m := got[0]
	if !peekLock {
		writeSBMessageResponse(w, m, "", http.StatusOK)
		return
	}
	// Service Bus emits Location as `https://{ns}/{entity}/messages/{messageID}/{lockToken}`;
	// the client DELETEs it verbatim to complete the message.
	location := fmt.Sprintf("https://%s/%s/messages/%s/%s", r.Host, sbRESTEntityPath(path), m.ID, m.Receipt)
	writeSBMessageResponse(w, m, location, http.StatusCreated)
}

// sbRESTEntityPath spells a store path the way the REST plane addresses it.
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

// handleSBLockedMessage settles a locked message: DELETE completes it, PUT
// unlocks it, POST renews its lock.
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

// sbLockNamesMessage reports whether lockToken is the live lock of the
// message with the given id (or sequence number, which the REST API also
// accepts in that position).
func sbLockNamesMessage(namespace, path, messageID, lockToken string) bool {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	m := rec.Queue.ByReceipt(lockToken, sbSettings(namespace, path).policy(), time.Now())
	if m == nil {
		return false
	}
	return m.ID == messageID || strconv.FormatUint(m.Seq, 10) == messageID
}

// writeSBMessageResponse emits the Receive response: the BrokerProperties
// header — the sender's properties with the broker's own over them — the
// Location header for a Peek-Lock, and the body.
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
	// response is committed and the request logger records the close.
	_, _ = w.Write(m.Payload.Body)
}
