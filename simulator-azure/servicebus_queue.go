package main

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// Azure Service Bus message store, shared by the REST and AMQP data planes.
// Every queue, topic subscription and dead-letter sub-queue is one msgq.Queue
// under `{namespace}/{path}`, where a subscription's path is `{topic}/{sub}`
// and a dead-letter sub-queue's is `{entity}/$DeadLetterQueue`. The entity's
// own settings — lockDuration, maxDeliveryCount, duplicate detection,
// defaultMessageTimeToLive, deadLetteringOnMessageExpiration — drive it.

// sbPayload is a stored message. AMQP carries the message exactly as an AMQP
// sender sent it, so a receiver gets back every section the sender set.
type sbPayload struct {
	Body         []byte `json:"body,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	BrokerHeader string `json:"brokerHeader,omitempty"`
	AMQP         []byte `json:"amqp,omitempty"`
	// Deferred messages stay in the entity but are only received by
	// sequence number.
	Deferred                   bool           `json:"deferred,omitempty"`
	DeadLetterReason           string         `json:"deadLetterReason,omitempty"`
	DeadLetterErrorDescription string         `json:"deadLetterErrorDescription,omitempty"`
	DeadLetterSource           string         `json:"deadLetterSource,omitempty"`
	Properties                 map[string]any `json:"properties,omitempty"`
}

type sbQueueRecord struct {
	Queue msgq.Queue[sbPayload] `json:"queue"`
}

var sbQueueDurable sim.Store[sbQueueRecord]

const (
	sbDeadLetterSuffix        = "$DeadLetterQueue"
	sbDefaultLockDuration     = time.Minute
	sbDefaultMaxDeliveryCount = 10
	sbDefaultDedupWindow      = 10 * time.Minute
)

var errSBLockLost = errors.New("the lock supplied is invalid: either the lock expired or the message has already been removed from the queue")

func sbQueueKey(namespace, path string) string {
	return namespace + "/" + path
}

func sbIsDeadLetterPath(path string) bool {
	return strings.EqualFold(path[strings.LastIndex(path, "/")+1:], sbDeadLetterSuffix)
}

func sbDeadLetterPath(path string) string {
	return path + "/" + sbDeadLetterSuffix
}

var sbDurationPattern = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// sbParseDuration reads the xs:duration day-time spelling Service Bus uses
// (PT1M, P10675199DT2H48M5.4775807S). A duration past what time.Duration
// holds — the service's "never" — is math.MaxInt64.
func sbParseDuration(s string) (time.Duration, bool) {
	m := sbDurationPattern.FindStringSubmatch(s)
	if m == nil || s == "P" || s == "PT" {
		return 0, false
	}
	var secs float64
	for i, unit := range []float64{86400, 3600, 60} {
		if m[i+1] != "" {
			n, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				return 0, false
			}
			secs += n * unit
		}
	}
	if m[4] != "" {
		n, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return 0, false
		}
		secs += n
	}
	if secs*float64(time.Second) >= math.MaxInt64 {
		return math.MaxInt64, true
	}
	return time.Duration(secs * float64(time.Second)), true
}

// sbEntitySettings are the settings of one queue, subscription or
// dead-letter sub-queue that delivery honours.
type sbEntitySettings struct {
	lock               time.Duration
	maxDelivery        int
	dedup              time.Duration
	ttl                time.Duration
	deadLetterOnExpiry bool
}

func (s sbEntitySettings) policy() msgq.Policy {
	return msgq.Policy{MaxDeliveries: s.maxDelivery, DedupWindow: s.dedup}
}

func sbSettingsFrom(props map[string]any) sbEntitySettings {
	s := sbEntitySettings{lock: sbDefaultLockDuration, maxDelivery: sbDefaultMaxDeliveryCount}
	if v := stringProp(props, "lockDuration"); v != nil {
		if d, ok := sbParseDuration(*v); ok && d > 0 {
			s.lock = d
		}
	}
	if v := int32Prop(props, "maxDeliveryCount"); v != nil && *v > 0 {
		s.maxDelivery = int(*v)
	}
	if v := boolProp(props, "requiresDuplicateDetection"); v != nil && *v {
		s.dedup = sbDefaultDedupWindow
		if w := stringProp(props, "duplicateDetectionHistoryTimeWindow"); w != nil {
			if d, ok := sbParseDuration(*w); ok && d > 0 {
				s.dedup = d
			}
		}
	}
	if v := stringProp(props, "defaultMessageTimeToLive"); v != nil {
		if d, ok := sbParseDuration(*v); ok && d > 0 && d != math.MaxInt64 {
			s.ttl = d
		}
	}
	if v := boolProp(props, "deadLetteringOnMessageExpiration"); v != nil {
		s.deadLetterOnExpiry = *v
	}
	return s
}

// sbSettings resolves the entity a data-plane path addresses. An entity the
// namespace does not define delivers with the service defaults.
func sbSettings(namespace, path string) sbEntitySettings {
	if sbIsDeadLetterPath(path) {
		parent := sbSettings(namespace, path[:strings.LastIndex(path, "/")])
		return sbEntitySettings{lock: parent.lock}
	}
	if topic, sub, ok := strings.Cut(path, "/"); ok {
		if s, found := sbSubscriptions.Get(sbAdminSubscriptionID(namespace, topic, sub)); found {
			return sbSettingsFrom(s.Properties)
		}
		return sbSettingsFrom(nil)
	}
	if q, found := sbQueues.Get(sbAdminQueueID(namespace, path)); found {
		return sbSettingsFrom(q.Properties)
	}
	return sbSettingsFrom(nil)
}

// sbTopicDedup returns the duplicate-detection window of a topic, zero when
// the path is not a topic that detects duplicates.
func sbTopicDedup(namespace, path string) time.Duration {
	t, ok := sbTopics.Get(sbAdminTopicID(namespace, path))
	if !ok {
		return 0
	}
	return sbSettingsFrom(t.Properties).dedup
}

// sbSendPaths resolves a send address to the stores it lands in: a topic fans
// out to its subscriptions.
func sbSendPaths(namespace, path string) []string {
	if strings.Contains(path, "/") {
		return []string{path}
	}
	subs := sbAMQPTopicSubscriptions(namespace, path)
	if len(subs) == 0 {
		return []string{path}
	}
	return subs
}

// sbOutgoing is a message a sender hands the service.
type sbOutgoing struct {
	payload   sbPayload
	messageID string
	ttl       time.Duration
	delay     time.Duration
}

// sbSend enqueues a message at an address and returns the stores it reached;
// a duplicate inside the detection window reaches none.
func sbSend(namespace, address string, out sbOutgoing) []string {
	if out.messageID == "" {
		out.messageID = sim.NewUUID()
	}
	now := time.Now()
	if window := sbTopicDedup(namespace, address); window > 0 {
		dup := false
		sbQueueDurable.Upsert(sbQueueKey(namespace, address), func(rec *sbQueueRecord) {
			if _, dup = rec.Queue.Dedup.Lookup(out.messageID, now); !dup {
				rec.Queue.Dedup.Remember(out.messageID, out.messageID, 0, window, now)
			}
		})
		if dup {
			return nil
		}
	}
	var reached []string
	for _, path := range sbSendPaths(namespace, address) {
		settings := sbSettings(namespace, path)
		opts := msgq.EnqueueOpts{ID: out.messageID, Delay: out.delay, TTL: settings.ttl}
		if out.ttl > 0 && (opts.TTL == 0 || out.ttl < opts.TTL) {
			opts.TTL = out.ttl
		}
		if settings.dedup > 0 {
			opts.DedupKey = out.messageID
		}
		var dup bool
		sbQueueDurable.Upsert(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
			_, dup = rec.Queue.Enqueue(out.payload, opts, settings.policy(), now)
		})
		if !dup {
			reached = append(reached, path)
		}
	}
	return reached
}

// sbReceive takes up to max messages from an entity. peekLock locks them for
// the entity's lockDuration; otherwise they are removed as they are handed
// over. Messages past maxDeliveryCount, and expired ones when the entity
// dead-letters on expiration, move to the dead-letter sub-queue.
func sbReceive(namespace, path string, max int, peekLock bool) ([]msgq.Message[sbPayload], time.Duration) {
	settings := sbSettings(namespace, path)
	pol := settings.policy()
	now := time.Now()
	var got msgq.Received[sbPayload]
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		got = rec.Queue.Receive(msgq.ReceiveOpts[sbPayload]{
			Max:    max,
			Lease:  settings.lock,
			Accept: func(m msgq.Message[sbPayload]) bool { return !m.Payload.Deferred },
		}, pol, now)
		if !peekLock {
			for _, m := range got.Leased {
				rec.Queue.Settle(m.Receipt, pol, now)
			}
		}
	})
	if peekLock && len(got.Leased) > 0 {
		// Hand the messages to waiting receivers again once their locks run out.
		bg.AfterFunc(settings.lock, func() { sbNotifyReceivers(namespace, path) })
	}
	sbDeadLetter(namespace, path, got.DeadLettered, "MaxDeliveryCountExceeded",
		"Message could not be consumed after "+strconv.Itoa(settings.maxDelivery)+" delivery attempts.")
	if settings.deadLetterOnExpiry {
		sbDeadLetter(namespace, path, got.Expired, "TTLExpiredException", "The message expired and was dead lettered.")
	}
	return got.Leased, settings.lock
}

// sbDeadLetter moves messages into the entity's dead-letter sub-queue with
// the reason Service Bus records on them.
func sbDeadLetter(namespace, path string, msgs []msgq.Message[sbPayload], reason, description string) {
	if len(msgs) == 0 || sbIsDeadLetterPath(path) {
		return
	}
	now := time.Now()
	sbQueueDurable.Upsert(sbQueueKey(namespace, sbDeadLetterPath(path)), func(rec *sbQueueRecord) {
		for _, m := range msgs {
			p := m.Payload
			p.Deferred = false
			if p.DeadLetterReason == "" && p.DeadLetterErrorDescription == "" {
				p.DeadLetterReason, p.DeadLetterErrorDescription = reason, description
			}
			rec.Queue.Enqueue(p, msgq.EnqueueOpts{ID: m.ID}, msgq.Policy{}, now)
		}
	})
	sbNotifyReceivers(namespace, sbDeadLetterPath(path))
}

// sbNotifyReceivers hands the messages available at path to the AMQP
// receivers holding credit for it. It runs on a goroutine of its own because
// a receive can make messages available while its connection's lock is held.
func sbNotifyReceivers(namespace, path string) {
	bg.Go(func() {
		// A failed transfer means the receiver's connection is going away;
		// its serve loop tears the connection down and the messages stay.
		_ = sbAMQPDeliverAvailableMessages(namespace, []string{path})
	})
}

// sbSettlement is a receiver's verdict on a locked message.
type sbSettlement struct {
	kind                       sbSettleKind
	deadLetterReason           string
	deadLetterErrorDescription string
	properties                 map[string]any
}

type sbSettleKind int

const (
	sbComplete sbSettleKind = iota
	sbAbandon
	sbRelease
	sbDefer
	sbDeadLetterIt
)

// sbSettle applies a settlement to the message a lock token names. It
// returns errSBLockLost when the lock expired or never existed.
func sbSettle(namespace, path, lockToken string, s sbSettlement) error {
	settings := sbSettings(namespace, path)
	pol, now := settings.policy(), time.Now()
	found := false
	var dead msgq.Message[sbPayload]
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		m := rec.Queue.ByReceipt(lockToken, pol, now)
		if m == nil {
			return
		}
		found = true
		if len(s.properties) > 0 {
			if m.Payload.Properties == nil {
				m.Payload.Properties = map[string]any{}
			}
			for k, v := range s.properties {
				m.Payload.Properties[k] = v
			}
		}
		switch s.kind {
		case sbComplete:
			rec.Queue.Settle(lockToken, pol, now)
		case sbAbandon:
			rec.Queue.Abandon(lockToken, pol, now)
		case sbRelease:
			rec.Queue.Release(lockToken, pol, now)
		case sbDefer:
			m.Payload.Deferred = true
			rec.Queue.Abandon(lockToken, pol, now)
		case sbDeadLetterIt:
			m.Payload.DeadLetterReason = s.deadLetterReason
			m.Payload.DeadLetterErrorDescription = s.deadLetterErrorDescription
			dead, _ = rec.Queue.DeadLetter(lockToken, pol, now)
		}
	})
	if !found {
		return errSBLockLost
	}
	switch s.kind {
	case sbDeadLetterIt:
		sbDeadLetter(namespace, path, []msgq.Message[sbPayload]{dead}, s.deadLetterReason, s.deadLetterErrorDescription)
	case sbAbandon, sbRelease:
		sbNotifyReceivers(namespace, path)
	}
	return nil
}

// sbRenewLock extends a lock by the entity's lockDuration from now.
func sbRenewLock(namespace, path, lockToken string) (time.Time, error) {
	settings := sbSettings(namespace, path)
	now := time.Now()
	var held bool
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		_, _, held = rec.Queue.Extend(lockToken, settings.lock, settings.policy(), now)
	})
	if !held {
		return time.Time{}, errSBLockLost
	}
	return now.Add(settings.lock), nil
}

// sbPeek returns up to n messages from fromSeq on, locked or not, without
// locking them.
func sbPeek(namespace, path string, fromSeq uint64, n int) []msgq.Message[sbPayload] {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	return rec.Queue.Peek(fromSeq, n, false, sbSettings(namespace, path).policy(), time.Now())
}

// sbReceiveDeferred locks the deferred messages with the given sequence
// numbers.
func sbReceiveDeferred(namespace, path string, seqs []uint64) []msgq.Message[sbPayload] {
	settings := sbSettings(namespace, path)
	want := map[uint64]bool{}
	for _, s := range seqs {
		want[s] = true
	}
	var got msgq.Received[sbPayload]
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		got = rec.Queue.Receive(msgq.ReceiveOpts[sbPayload]{
			Max:    len(seqs),
			Lease:  settings.lock,
			Accept: func(m msgq.Message[sbPayload]) bool { return m.Payload.Deferred && want[m.Seq] },
		}, msgq.Policy{}, time.Now())
	})
	return got.Leased
}

// sbQueueCounts reports the total and currently deliverable message counts
// of an entity, and the count of its dead-letter sub-queue — the numbers the
// admin plane's MessageCount, ActiveMessageCount and DeadLetterMessageCount
// reflect on real Service Bus.
func sbQueueCounts(namespace, path string) (total int64, active, dead int32) {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	counts := rec.Queue.Count(sbSettings(namespace, path).policy(), time.Now())
	dl, _ := sbQueueDurable.Get(sbQueueKey(namespace, sbDeadLetterPath(path)))
	return int64(len(rec.Queue.Messages)), int32(counts.Available), int32(len(dl.Queue.Messages))
}
