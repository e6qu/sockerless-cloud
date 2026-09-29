package main

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// Azure Queue Storage messages: Put Message, Get Messages, Peek Messages,
// Update Message, Delete Message and Clear Messages on a msgq.Queue. A pop
// receipt stays valid after its visibility timeout runs out until the message
// is dequeued again, and each message expires at its own time to live.

type queuePayload struct {
	Text string `json:"text"`
}

const (
	queueDefaultTTL        = 7 * 24 * time.Hour
	queueDefaultVisibility = 30 * time.Second
	queueMaxVisibility     = 7 * 24 * time.Hour
	queueMaxMessages       = 32
)

// queueNeverExpires is the ExpirationTime Queue Storage reports for a message
// put with messagettl=-1.
var queueNeverExpires = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

var queuePolicy = msgq.Policy{ReceiptOutlivesLease: true}

// QueueMessageRequest is the XML request body for Put Message.
type QueueMessageRequest struct {
	XMLName     xml.Name `xml:"QueueMessage"`
	MessageText string   `xml:"MessageText"`
}

// QueueMessageResponse is the XML response shape for Put, Get and Peek.
type QueueMessageResponse struct {
	XMLName         xml.Name `xml:"QueueMessage"`
	MessageID       string   `xml:"MessageId,omitempty"`
	InsertionTime   string   `xml:"InsertionTime,omitempty"`
	ExpirationTime  string   `xml:"ExpirationTime,omitempty"`
	PopReceipt      string   `xml:"PopReceipt,omitempty"`
	TimeNextVisible string   `xml:"TimeNextVisible,omitempty"`
	DequeueCount    int      `xml:"DequeueCount,omitempty"`
	MessageText     string   `xml:"MessageText,omitempty"`
}

type queueMessagesList struct {
	XMLName  xml.Name               `xml:"QueueMessagesList"`
	Messages []QueueMessageResponse `xml:"QueueMessage"`
}

func queueTime(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(http.TimeFormat)
}

func queueExpiration(m msgq.Message[queuePayload]) string {
	if m.ExpiresAt == 0 {
		return queueNeverExpires.Format(http.TimeFormat)
	}
	return queueTime(m.ExpiresAt)
}

// queueSecondsParam reads an integer query parameter within [min, max],
// reporting whether it was present and valid.
func queueSecondsParam(r *http.Request, name string, min, max int64) (int64, bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, false, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		return 0, true, false
	}
	return n, true, true
}

func writeQueueOutOfRange(w http.ResponseWriter, name string) {
	writeStorageError(w, "OutOfRangeQueryParameterValue",
		"One of the query parameters specified in the request URI is outside the permissible range: "+name+".",
		http.StatusBadRequest)
}

func handleQueuePutMessage(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	ttl := queueDefaultTTL
	if n, present, ok := queueSecondsParam(r, "messagettl", -1, 1<<40); !ok || (present && n == 0) {
		writeQueueOutOfRange(w, "messagettl")
		return
	} else if present {
		ttl = time.Duration(n) * time.Second
	}
	visibility, _, ok := queueSecondsParam(r, "visibilitytimeout", 0, int64(queueMaxVisibility/time.Second))
	if !ok || (ttl > 0 && time.Duration(visibility)*time.Second >= ttl) {
		writeQueueOutOfRange(w, "visibilitytimeout")
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "RequestBodyInvalid", "Failed to read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req QueueMessageRequest
	if err := xml.Unmarshal(data, &req); err != nil {
		writeStorageError(w, "InvalidXmlDocument",
			"The specified XML is not syntactically valid: "+err.Error(), http.StatusBadRequest)
		return
	}
	opts := msgq.EnqueueOpts{Delay: time.Duration(visibility) * time.Second}
	if ttl > 0 {
		opts.TTL = ttl
	}
	var m msgq.Message[queuePayload]
	queueData.Update(key, func(q *QueueData) {
		m, _ = q.Messages.Enqueue(queuePayload{Text: req.MessageText}, opts, queuePolicy, time.Now())
		// Put Message hands out a pop receipt so the sender can update or
		// delete the message before anyone dequeues it.
		stored := q.Messages.ByID(m.ID)
		stored.Receipt = sim.NewUUID()
		m = *stored
	})
	writeStorageXML(w, http.StatusCreated, queueMessagesList{Messages: []QueueMessageResponse{{
		MessageID:       m.ID,
		InsertionTime:   queueTime(m.EnqueuedAt),
		ExpirationTime:  queueExpiration(m),
		PopReceipt:      m.Receipt,
		TimeNextVisible: queueTime(m.AvailableAt),
	}}})
}

func handleQueueGetMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	visibility, present, ok := queueSecondsParam(r, "visibilitytimeout", 1, int64(queueMaxVisibility/time.Second))
	if !ok {
		writeQueueOutOfRange(w, "visibilitytimeout")
		return
	}
	lease := queueDefaultVisibility
	if present {
		lease = time.Duration(visibility) * time.Second
	}
	n, present, ok := queueSecondsParam(r, "numofmessages", 1, queueMaxMessages)
	if !ok {
		writeQueueOutOfRange(w, "numofmessages")
		return
	}
	if !present {
		n = 1
	}
	var got msgq.Received[queuePayload]
	queueData.Update(key, func(q *QueueData) {
		got = q.Messages.Receive(msgq.ReceiveOpts[queuePayload]{Max: int(n), Lease: lease}, queuePolicy, time.Now())
	})
	out := queueMessagesList{}
	for _, m := range got.Leased {
		out.Messages = append(out.Messages, QueueMessageResponse{
			MessageID:       m.ID,
			InsertionTime:   queueTime(m.EnqueuedAt),
			ExpirationTime:  queueExpiration(m),
			PopReceipt:      m.Receipt,
			TimeNextVisible: queueTime(m.AvailableAt),
			DequeueCount:    m.Deliveries,
			MessageText:     m.Payload.Text,
		})
	}
	writeStorageXML(w, http.StatusOK, out)
}

func handleQueuePeekMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	q, ok := queueData.Get(queueKey(account, queue))
	if !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	n, present, ok := queueSecondsParam(r, "numofmessages", 1, queueMaxMessages)
	if !ok {
		writeQueueOutOfRange(w, "numofmessages")
		return
	}
	if !present {
		n = 1
	}
	now := time.Now()
	out := queueMessagesList{}
	for _, m := range q.Messages.Peek(0, len(q.Messages.Messages), true, queuePolicy, now) {
		if len(out.Messages) == int(n) {
			break
		}
		if m.ExpiresAt > 0 && m.ExpiresAt <= now.UnixMilli() {
			continue
		}
		out.Messages = append(out.Messages, QueueMessageResponse{
			MessageID:      m.ID,
			InsertionTime:  queueTime(m.EnqueuedAt),
			ExpirationTime: queueExpiration(m),
			DequeueCount:   m.Deliveries,
			MessageText:    m.Payload.Text,
		})
	}
	writeStorageXML(w, http.StatusOK, out)
}

// queueHolder finds the message a request names and checks the caller holds
// its pop receipt, writing the service's error when it does not.
func queueHolder(w http.ResponseWriter, q *QueueData, messageID, popReceipt string) *msgq.Message[queuePayload] {
	q.Messages.Expire(queuePolicy, time.Now())
	m := q.Messages.ByID(messageID)
	if m == nil {
		writeStorageError(w, "MessageNotFound", "The specified message does not exist.", http.StatusNotFound)
		return nil
	}
	if m.Receipt == "" || m.Receipt != popReceipt {
		writeStorageError(w, "PopReceiptMismatch",
			"The specified pop receipt did not match the pop receipt for a dequeued message.",
			http.StatusBadRequest)
		return nil
	}
	return m
}

func handleQueueDeleteMessage(w http.ResponseWriter, r *http.Request, account, queue, messageID string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	popReceipt := r.URL.Query().Get("popreceipt")
	deleted := false
	queueData.Update(key, func(q *QueueData) {
		if queueHolder(w, q, messageID, popReceipt) == nil {
			return
		}
		_, deleted = q.Messages.Settle(popReceipt, queuePolicy, time.Now())
	})
	if deleted {
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleQueueUpdateMessage is Update Message: the holder of a pop receipt
// moves the message's visibility and may replace its content, and receives a
// fresh pop receipt that supersedes the one it presented.
func handleQueueUpdateMessage(w http.ResponseWriter, r *http.Request, account, queue, messageID string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	popReceipt := r.URL.Query().Get("popreceipt")
	if popReceipt == "" {
		writeStorageError(w, "InvalidQueryParameterValue",
			"Value for one of the query parameters specified in the request URI is invalid: popreceipt.",
			http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("visibilitytimeout") == "" {
		writeStorageError(w, "MissingRequiredQueryParameter",
			"A query parameter that's mandatory for this request is not specified: visibilitytimeout.",
			http.StatusBadRequest)
		return
	}
	visibility, _, ok := queueSecondsParam(r, "visibilitytimeout", 0, int64(queueMaxVisibility/time.Second))
	if !ok {
		writeQueueOutOfRange(w, "visibilitytimeout")
		return
	}

	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStorageError(w, "RequestBodyInvalid", "Failed to read request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var replacement QueueMessageRequest
	hasReplacement := false
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := xml.Unmarshal(body, &replacement); err != nil {
			writeStorageError(w, "InvalidXmlDocument",
				"The specified XML is not syntactically valid: "+err.Error(), http.StatusBadRequest)
			return
		}
		hasReplacement = true
	}

	var updated *msgq.Message[queuePayload]
	queueData.Update(key, func(q *QueueData) {
		m := queueHolder(w, q, messageID, popReceipt)
		if m == nil {
			return
		}
		m.Receipt = sim.NewUUID()
		m.Leased = true
		m.AvailableAt = time.Now().Add(time.Duration(visibility) * time.Second).UnixMilli()
		if hasReplacement {
			m.Payload.Text = replacement.MessageText
		}
		copied := *m
		updated = &copied
	})
	if updated == nil {
		return
	}
	w.Header().Set("x-ms-popreceipt", updated.Receipt)
	w.Header().Set("x-ms-time-next-visible", queueTime(updated.AvailableAt))
	w.WriteHeader(http.StatusNoContent)
}

func handleQueueClearMessages(w http.ResponseWriter, r *http.Request, account, queue string) {
	key := queueKey(account, queue)
	if _, ok := queueData.Get(key); !ok {
		writeStorageError(w, "QueueNotFound", "The specified queue does not exist.", http.StatusNotFound)
		return
	}
	queueData.Update(key, func(q *QueueData) { q.Messages.Purge() })
	w.WriteHeader(http.StatusNoContent)
}

// queueApproximateCount is what Get Queue Metadata reports: every unexpired
// message, visible or not.
func queueApproximateCount(q QueueData) int {
	c := q.Messages.Count(queuePolicy, time.Now())
	return c.Available + c.Held + c.Delayed
}
