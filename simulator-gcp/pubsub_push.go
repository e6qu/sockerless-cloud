package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// psPushBatch bounds how many messages one sweep leases to a push endpoint.
const psPushBatch = 1000

// startPubSubPush delivers every push subscription's backlog as soon as a
// publish or a push-config change wakes it, and once a second for the messages
// whose pull lease ran out, until the server stops. A push leases messages
// from the same queue a pull does, so delivery attempts, ack deadlines, retry
// backoff and dead-lettering stay one mechanism whichever way the subscription
// is configured.
func startPubSubPush(srv *sim.Server) {
	srv.StartBackground("Pub/Sub push", func(ctx context.Context) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-psPushWake:
			}
			// A wake and a stop can be ready together; select picks at random.
			if ctx.Err() != nil {
				return
			}
			psPushSweep(ctx)
		}
	})
}

// psPushWake holds at most one pending request for the push loop to deliver
// now: Pub/Sub pushes a message when it is published, not on a schedule.
var psPushWake = make(chan struct{}, 1)

func psWakePush() {
	select {
	case psPushWake <- struct{}{}:
	default:
	}
}

func psPushSweep(ctx context.Context) {
	for _, sub := range psSubscriptions.List() {
		if psPushes(sub) {
			psPushSubscription(ctx, sub.Name)
		}
	}
}

func psPushes(sub PSSubscription) bool {
	return sub.PushConfig != nil && sub.PushConfig.PushEndpoint != "" && !sub.Detached
}

// psPushAckDeadline is how long Pub/Sub waits for a push endpoint's answer
// before it treats the delivery as a negative acknowledgement.
func psPushAckDeadline(sub PSSubscription) time.Duration {
	return time.Duration(max(sub.AckDeadlineSeconds, 10)) * time.Second
}

// psPushSubscription leases the subscription's available messages for the
// push ack deadline and POSTs each to the push endpoint.
func psPushSubscription(ctx context.Context, subName string) {
	sub, ok := psSubscriptions.Get(subName)
	if !ok || !psPushes(sub) {
		return
	}
	delivered, err := psDequeue(sub.Name, psPushBatch, psPushAckDeadline(sub))
	if err != nil {
		return
	}
	// An ordering key's messages go one at a time, each after the previous
	// one was acknowledged; a message the endpoint does not acknowledge
	// gives the rest of its key back to be delivered after it.
	chains := map[string][]psDelivered{}
	for _, d := range delivered {
		if key := d.Message.OrderingKey; sub.EnableMessageOrdering && key != "" {
			chains[key] = append(chains[key], d)
			continue
		}
		bg.Go(func() { psPush(ctx, sub.Name, d) })
	}
	for _, chain := range chains {
		bg.Go(func() {
			for i, d := range chain {
				if !psPush(ctx, sub.Name, d) {
					for _, rest := range chain[i+1:] {
						psRelease(sub.Name, rest.AckID)
					}
					return
				}
			}
		})
	}
}

func psPushAckCode(status int) delivery.Class {
	switch status {
	case http.StatusProcessing, http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return delivery.Accept
	}
	return delivery.Retry
}

// psPush makes one push delivery. An acknowledging answer acknowledges the
// message; anything else, including no answer within the ack deadline, is a
// negative acknowledgement. It reports whether the endpoint acknowledged.
func psPush(ctx context.Context, subName string, d psDelivered) bool {
	sub, ok := psSubscriptions.Get(subName)
	if !ok {
		return false
	}
	if !psPushes(sub) {
		psRelease(subName, d.AckID)
		return false
	}
	header := http.Header{}
	var body []byte
	var err error
	if trigger, ok := eventarcDeliveringThrough(sub.Name); ok {
		header, body, err = eventarcCloudEvent(trigger, sub, d.Message)
	} else {
		header.Set("Content-Type", "application/json")
		body, err = json.Marshal(psPushEnvelope(sub, d.Message, d.DeliveryAttempt))
	}
	if err != nil {
		psNackPush(ctx, sub.Name, d)
		return false
	}
	if oidc := sub.PushConfig.OidcToken; oidc != nil && oidc.ServiceAccountEmail != "" {
		audience := oidc.Audience
		if audience == "" {
			audience = sub.PushConfig.PushEndpoint
		}
		now := time.Now()
		header.Set("Authorization", "Bearer "+signServiceAccountIDToken(oidc.ServiceAccountEmail, audience, true, now, now.Add(time.Hour)))
	}
	req := delivery.Request{URL: sub.PushConfig.PushEndpoint, Header: header, Body: body, Timeout: psPushAckDeadline(sub)}
	outcome := deliverPush(ctx, req, psPushAckCode)
	if ctx.Err() != nil {
		// The server is stopping; the lease runs out and a restarted
		// simulator redelivers the message.
		return false
	}
	if outcome.OK() {
		psAcknowledge(sub.Name, []string{d.AckID})
		return true
	}
	psNackPush(ctx, sub.Name, d)
	return false
}

// psNackPush returns a message the endpoint did not acknowledge to its
// subscription and pushes again once the retry backoff has passed; that next
// lease dead-letters the message instead when it used its last attempt.
func psNackPush(ctx context.Context, subName string, d psDelivered) {
	psModifyAckDeadline(subName, []string{d.AckID}, 0)
	var wait time.Duration
	if q, ok := psQueues.Get(subName); ok {
		if m := q.Queue.ByID(d.Message.MessageId); m != nil {
			wait = time.Until(time.UnixMilli(m.AvailableAt))
		}
	}
	bg.AfterFunc(max(wait, 0), func() {
		if ctx.Err() == nil {
			psPushSubscription(ctx, subName)
		}
	})
}

// psPushEnvelope is the JSON body Pub/Sub POSTs to a push endpoint. Only a
// subscription with a dead-letter policy reports the delivery attempt.
func psPushEnvelope(sub PSSubscription, message PSMessage, deliveryAttempt int) map[string]any {
	wrapped := map[string]any{
		"data":         message.Data,
		"messageId":    message.MessageId,
		"message_id":   message.MessageId,
		"publishTime":  message.PublishTime,
		"publish_time": message.PublishTime,
	}
	if len(message.Attributes) > 0 {
		wrapped["attributes"] = message.Attributes
	}
	envelope := map[string]any{"message": wrapped, "subscription": sub.Name}
	if deliveryAttempt > 0 {
		envelope["deliveryAttempt"] = deliveryAttempt
	}
	return envelope
}
