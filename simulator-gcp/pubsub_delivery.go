package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/delivery"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Cloud Pub/Sub delivery, shared by the REST and gRPC surfaces: publish
// fan-out with subscription filters, leased pull with ack deadlines, message
// ordering by ordering key, retry backoff, and dead-letter forwarding.

// psQueue holds a subscription's unacknowledged messages, pending and
// outstanding alike.
type psQueue struct {
	Subscription string
	Queue        msgq.Queue[PSMessage]
	// Acked holds the acknowledged messages a subscription with
	// retainAckedMessages keeps for its messageRetentionDuration, the ones a
	// seek to a time can mark unacknowledged again.
	Acked []PSMessage `json:",omitempty"`
}

const (
	psDefaultMaxDeliveryAttempts = 5
	psDefaultMinimumBackoff      = 10 * time.Second
	psDefaultMaximumBackoff      = 600 * time.Second
	psDefaultMessageRetention    = 7 * 24 * time.Hour
	psMinAckDeadlineSeconds      = 10
	psMaxAckDeadlineSeconds      = 600
	psPushMinimumBackoff         = 100 * time.Millisecond
	psPushMaximumBackoff         = 60 * time.Second
)

// psParseDuration reads the protobuf JSON duration spelling ("600s", "0.5s").
func psParseDuration(s string) (time.Duration, error) {
	if !strings.HasSuffix(s, "s") {
		return 0, fmt.Errorf("duration %q does not end in s", s)
	}
	secs, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
	if err != nil || secs < 0 || math.IsInf(secs, 0) || secs > math.MaxInt64/float64(time.Second) {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

func psMaxDeliveryAttempts(p *PSDeadLetterPolicy) int {
	if p.MaxDeliveryAttempts == 0 {
		return psDefaultMaxDeliveryAttempts
	}
	return p.MaxDeliveryAttempts
}

// psValidateSubscription applies the create-time checks Pub/Sub makes on the
// delivery settings the simulator enforces.
func psValidateSubscription(s PSSubscription) error {
	switch {
	case s.AckDeadlineSeconds < psMinAckDeadlineSeconds:
		return fmt.Errorf("the value for ack_deadline_seconds is too small: you passed %d in the request, but the minimum value is %d",
			s.AckDeadlineSeconds, psMinAckDeadlineSeconds)
	case s.AckDeadlineSeconds > psMaxAckDeadlineSeconds:
		return fmt.Errorf("the value for ack_deadline_seconds is too large: you passed %d in the request, but the maximum value is %d",
			s.AckDeadlineSeconds, psMaxAckDeadlineSeconds)
	}
	if s.MessageRetentionDuration != "" {
		d, err := psParseDuration(s.MessageRetentionDuration)
		if err != nil || d < 10*time.Minute || d > 31*24*time.Hour {
			return fmt.Errorf("message_retention_duration %q must be between 10 minutes and 31 days", s.MessageRetentionDuration)
		}
	}
	if _, err := psParseFilter(s.Filter); err != nil {
		return err
	}
	if p := s.DeadLetterPolicy; p != nil {
		if n := p.MaxDeliveryAttempts; n != 0 && (n < 5 || n > 100) {
			return fmt.Errorf("dead_letter_policy.max_delivery_attempts must be between 5 and 100")
		}
		if p.DeadLetterTopic != "" {
			if _, ok := psTopics.Get(p.DeadLetterTopic); !ok {
				return fmt.Errorf("dead_letter_policy.dead_letter_topic %s does not exist", p.DeadLetterTopic)
			}
		}
	}
	if p := s.RetryPolicy; p != nil {
		min, max := psDefaultMinimumBackoff, psDefaultMaximumBackoff
		var err error
		if p.MinimumBackoff != "" {
			if min, err = psParseDuration(p.MinimumBackoff); err != nil || min > psDefaultMaximumBackoff {
				return fmt.Errorf("retry_policy.minimum_backoff must be between 0 and 600 seconds")
			}
		}
		if p.MaximumBackoff != "" {
			if max, err = psParseDuration(p.MaximumBackoff); err != nil || max > psDefaultMaximumBackoff {
				return fmt.Errorf("retry_policy.maximum_backoff must be between 0 and 600 seconds")
			}
		}
		if min > max {
			return fmt.Errorf("retry_policy.minimum_backoff must not exceed maximum_backoff")
		}
	}
	return nil
}

// psPolicy is the delivery policy a subscription configures. An expired ack
// id still acknowledges until the message is delivered again.
func psPolicy(s PSSubscription) msgq.Policy {
	pol := msgq.Policy{Ordered: s.EnableMessageOrdering, ReceiptOutlivesLease: true}
	if d, err := psParseDuration(s.MessageRetentionDuration); err == nil {
		pol.Retention = d
	}
	if p := s.DeadLetterPolicy; p != nil && p.DeadLetterTopic != "" {
		pol.MaxDeliveries = psMaxDeliveryAttempts(p)
	}
	// Pub/Sub doubles the backoff from the minimum with each failed delivery,
	// up to the maximum. Without a retry policy a pull subscription redelivers
	// at once, while a push subscription still backs off from 100ms to 60s.
	switch p := s.RetryPolicy; {
	case p != nil:
		min, max := psDefaultMinimumBackoff, psDefaultMaximumBackoff
		if d, err := psParseDuration(p.MinimumBackoff); err == nil {
			min = d
		}
		if d, err := psParseDuration(p.MaximumBackoff); err == nil {
			max = d
		}
		pol.Backoff = delivery.Exponential(min, max)
	case psPushes(s):
		pol.Backoff = delivery.Exponential(psPushMinimumBackoff, psPushMaximumBackoff)
	}
	return pol
}

// psPublishMessages assigns each message an id and publish time and fans it
// out to every attached subscription of the topic whose filter it passes.
// Returns the ids in publish order.
func psPublishMessages(tName string, msgs []PSMessage) ([]string, error) {
	if _, ok := psTopics.Get(tName); !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", tName)
	}
	for i := range msgs {
		msgs[i].MessageId = generateUUIDLocal()
		msgs[i].PublishTime = nowTimestamp()
	}
	now := time.Now()
	wakePush := false
	for _, sub := range psSubscriptions.Filter(func(s PSSubscription) bool { return s.Topic == tName && !s.Detached }) {
		filter, err := psParseFilter(sub.Filter)
		if err != nil {
			continue
		}
		wakePush = wakePush || psPushes(sub)
		pol := psPolicy(sub)
		psQueues.Upsert(sub.Name, func(q *psQueue) {
			q.Subscription = sub.Name
			for _, m := range msgs {
				// A message the filter rejects is acknowledged on arrival.
				if !filter.Eval(psFilterDoc(m.Attributes)) {
					continue
				}
				opts := msgq.EnqueueOpts{ID: m.MessageId}
				if sub.EnableMessageOrdering {
					opts.Group = m.OrderingKey
				}
				q.Queue.Enqueue(m, opts, pol, now)
			}
		})
		psSignalSubscription(sub.Name)
	}
	if wakePush {
		psWakePush()
	}
	ids := make([]string, 0, len(msgs))
	for _, m := range msgs {
		ids = append(ids, m.MessageId)
	}
	return ids, nil
}

type psDelivered struct {
	AckID   string
	Message PSMessage
	// DeliveryAttempt is set only on a subscription with a dead-letter policy.
	DeliveryAttempt int
}

// psDequeue leases up to max messages for the ack deadline — the stream's
// own when lease is non-zero, else the subscription's — and forwards the
// messages that exhausted their delivery attempts to the dead-letter topic.
func psDequeue(subName string, max int, lease time.Duration) ([]psDelivered, error) {
	s, ok := psSubscriptions.Get(subName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "Resource not found (resource=%s)", subName)
	}
	if s.Detached {
		return nil, status.Errorf(codes.FailedPrecondition, "subscription %s is detached", subName)
	}
	if max <= 0 {
		max = 1
	}
	if lease == 0 {
		lease = time.Duration(s.AckDeadlineSeconds) * time.Second
	}
	var got msgq.Received[PSMessage]
	psQueues.Upsert(subName, func(q *psQueue) {
		q.Subscription = subName
		got = q.Queue.Receive(msgq.ReceiveOpts[PSMessage]{
			Max:     max,
			Lease:   lease,
			Receipt: func(msgq.Message[PSMessage]) string { return generateUUIDLocal() },
		}, psPolicy(s), time.Now())
	})
	psForwardDeadLetters(s, got.DeadLettered)
	out := make([]psDelivered, 0, len(got.Leased))
	for _, m := range got.Leased {
		d := psDelivered{AckID: m.Receipt, Message: m.Payload}
		if s.DeadLetterPolicy != nil && s.DeadLetterPolicy.DeadLetterTopic != "" {
			d.DeliveryAttempt = m.Deliveries
		}
		out = append(out, d)
	}
	return out, nil
}

// psForwardDeadLetters publishes messages that exhausted their delivery
// attempts to the subscription's dead-letter topic, carrying the attributes
// Pub/Sub adds to a forwarded message.
func psForwardDeadLetters(s PSSubscription, msgs []msgq.Message[PSMessage]) {
	if len(msgs) == 0 || s.DeadLetterPolicy == nil || s.DeadLetterPolicy.DeadLetterTopic == "" {
		return
	}
	project, short := "", s.Name
	if parts := strings.Split(s.Name, "/"); len(parts) == 4 {
		project, short = parts[1], parts[3]
	}
	forward := make([]PSMessage, 0, len(msgs))
	for _, m := range msgs {
		attrs := make(map[string]string, len(m.Payload.Attributes)+4)
		for k, v := range m.Payload.Attributes {
			attrs[k] = v
		}
		attrs["CloudPubSubDeadLetterSourceDeliveryCount"] = strconv.Itoa(m.Deliveries)
		attrs["CloudPubSubDeadLetterSourceSubscription"] = short
		attrs["CloudPubSubDeadLetterSourceSubscriptionProject"] = project
		attrs["CloudPubSubDeadLetterSourceTopicPublishTime"] = m.Payload.PublishTime
		forward = append(forward, PSMessage{Data: m.Payload.Data, Attributes: attrs, OrderingKey: m.Payload.OrderingKey})
	}
	// A dead-letter topic deleted after the policy was set loses the
	// messages, as Pub/Sub cannot publish to it either.
	_, _ = psPublishMessages(s.DeadLetterPolicy.DeadLetterTopic, forward)
}

func psAcknowledge(subName string, ackIDs []string) {
	s, _ := psSubscriptions.Get(subName)
	pol, now := psPolicy(s), time.Now()
	psQueues.Update(subName, func(q *psQueue) {
		for _, id := range ackIDs {
			if m, ok := q.Queue.Settle(id, pol, now); ok && s.RetainAckedMessages {
				q.Acked = append(q.Acked, m.Payload)
			}
		}
		q.Acked = psRetained(s, q.Acked, now)
	})
	psSignalSubscription(subName)
}

// psRetained keeps the acknowledged messages still inside the subscription's
// messageRetentionDuration, measured from each message's publish time.
func psRetained(s PSSubscription, acked []PSMessage, now time.Time) []PSMessage {
	if !s.RetainAckedMessages {
		return nil
	}
	retention := psDefaultMessageRetention
	if s.MessageRetentionDuration != "" {
		d, err := psParseDuration(s.MessageRetentionDuration)
		if err != nil {
			return nil
		}
		retention = d
	}
	kept := make([]PSMessage, 0, len(acked))
	for _, m := range acked {
		published, err := time.Parse(time.RFC3339Nano, m.PublishTime)
		if err == nil && now.Sub(published) <= retention {
			kept = append(kept, m)
		}
	}
	return kept
}

// psModifyAckDeadline moves each named message's ack deadline to seconds from
// now; zero is a negative acknowledgement that redelivers it after the retry
// backoff. A negative acknowledgement of a message's last permitted delivery
// attempt forwards it to the dead-letter topic there and then.
func psModifyAckDeadline(subName string, ackIDs []string, seconds int32) {
	s, _ := psSubscriptions.Get(subName)
	pol, now := psPolicy(s), time.Now()
	deadLetters := seconds == 0 && s.DeadLetterPolicy != nil && s.DeadLetterPolicy.DeadLetterTopic != ""
	var exhausted []msgq.Message[PSMessage]
	psQueues.Update(subName, func(q *psQueue) {
		for _, id := range ackIDs {
			if seconds == 0 {
				q.Queue.Abandon(id, pol, now)
				continue
			}
			q.Queue.Extend(id, time.Duration(seconds)*time.Second, pol, now)
		}
		if deadLetters {
			exhausted = q.Queue.Exhausted(pol, now)
		}
	})
	psSignalSubscription(subName)
	psForwardDeadLetters(s, exhausted)
}

// psRelease gives back a leased message without counting the delivery, for a
// push the subscription stopped making before it was sent.
func psRelease(subName, ackID string) {
	s, _ := psSubscriptions.Get(subName)
	pol, now := psPolicy(s), time.Now()
	psQueues.Update(subName, func(q *psQueue) {
		q.Queue.Release(ackID, pol, now)
	})
	psSignalSubscription(subName)
}

func psValidAckDeadline(seconds int32) bool {
	return seconds >= 0 && seconds <= psMaxAckDeadlineSeconds
}

// psOutstanding counts the subscription's messages under a live ack deadline.
func psOutstanding(s PSSubscription) int {
	q, _ := psQueues.Get(s.Name)
	return q.Queue.Count(psPolicy(s), time.Now()).Held
}

// psBacklog returns the subscription's unacknowledged messages.
func psBacklog(subName string) []PSMessage {
	q, _ := psQueues.Get(subName)
	out := make([]PSMessage, 0, len(q.Queue.Messages))
	for _, m := range q.Queue.Messages {
		out = append(out, m.Payload)
	}
	return out
}

// psSeekSnapshot marks the snapshot's captured backlog unacknowledged again.
func psSeekSnapshot(subName string, backlog []PSMessage) {
	psRestore(subName, backlog)
}

// psSeekTime acknowledges the messages published before t and marks the
// retained ones published at or after it unacknowledged again: the
// acknowledged messages a subscription with retainAckedMessages keeps, and
// the snapshot backlogs captured on the subscription's topic.
func psSeekTime(subName string, t time.Time) {
	s, _ := psSubscriptions.Get(subName)
	cutoff := t.UnixMilli()
	var replay []PSMessage
	psQueues.Update(subName, func(q *psQueue) {
		acked := q.Queue.Remove(func(m msgq.Message[PSMessage]) bool { return m.EnqueuedAt < cutoff })
		if !s.RetainAckedMessages {
			return
		}
		var retained []PSMessage
		for _, m := range psRetained(s, q.Acked, time.Now()) {
			if published, err := time.Parse(time.RFC3339Nano, m.PublishTime); err == nil && !published.Before(t) {
				replay = append(replay, m)
				continue
			}
			retained = append(retained, m)
		}
		for _, m := range acked {
			retained = append(retained, m.Payload)
		}
		q.Acked = retained
	})
	for _, snap := range psSnapshots.List() {
		if snap.Topic != s.Topic {
			continue
		}
		backlog, _ := psSnapshotBacklogs.Get(psSnapshotKeyFromName(snap.Name))
		for _, m := range backlog {
			if published, err := time.Parse(time.RFC3339Nano, m.PublishTime); err == nil && !published.Before(t) {
				replay = append(replay, m)
			}
		}
	}
	psRestore(subName, replay)
	psSignalSubscription(subName)
}

func psRestore(subName string, msgs []PSMessage) {
	if len(msgs) == 0 {
		return
	}
	s, _ := psSubscriptions.Get(subName)
	pol, now := psPolicy(s), time.Now()
	psQueues.Upsert(subName, func(q *psQueue) {
		q.Subscription = subName
		for _, m := range msgs {
			if q.Queue.ByID(m.MessageId) != nil {
				continue
			}
			opts := msgq.EnqueueOpts{ID: m.MessageId}
			if s.EnableMessageOrdering {
				opts.Group = m.OrderingKey
			}
			q.Queue.Enqueue(m, opts, pol, now)
		}
	})
	psSignalSubscription(subName)
}

// pubsubDeadLetterSweeper forwards the messages whose last permitted delivery
// attempt ran out of ack deadline, without waiting for another pull.
func pubsubDeadLetterSweeper(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var now time.Time
		select {
		case <-ctx.Done():
			return
		case now = <-ticker.C:
		}
		psSweepDeadLetters(now)
	}
}

func psSweepDeadLetters(now time.Time) {
	for _, s := range psSubscriptions.Filter(func(s PSSubscription) bool {
		return s.DeadLetterPolicy != nil && s.DeadLetterPolicy.DeadLetterTopic != ""
	}) {
		var exhausted []msgq.Message[PSMessage]
		psQueues.Update(s.Name, func(q *psQueue) {
			exhausted = q.Queue.Exhausted(psPolicy(s), now)
		})
		psForwardDeadLetters(s, exhausted)
	}
}

// psWriteRPCError answers a REST call with the status a shared delivery
// function returned.
func psWriteRPCError(w http.ResponseWriter, err error) {
	st, _ := status.FromError(err)
	httpStatus, reason := http.StatusInternalServerError, "INTERNAL"
	switch st.Code() {
	case codes.NotFound:
		httpStatus, reason = http.StatusNotFound, "NOT_FOUND"
	case codes.FailedPrecondition:
		httpStatus, reason = http.StatusBadRequest, "FAILED_PRECONDITION"
	case codes.InvalidArgument:
		httpStatus, reason = http.StatusBadRequest, "INVALID_ARGUMENT"
	}
	gcpError(w, httpStatus, reason, st.Message())
}
