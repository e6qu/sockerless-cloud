package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// psLegacyQueue is a subscription's backlog as builds before the shared
// message queue stored it: the pending messages only, with each outstanding
// message a row of its own in pubsub_inflight.
type psLegacyQueue struct {
	Subscription string
	Messages     json.RawMessage
	Queue        json.RawMessage
}

type psLegacyInFlight struct {
	AckId        string
	Subscription string
	Message      PSMessage
	DeliveredAt  time.Time
	AckDeadline  time.Time
}

// psMigrateQueues folds the backlogs and outstanding messages an earlier
// build stored into each subscription's queue. An outstanding message keeps
// its ack id and ack deadline, and counts the delivery it is under.
func psMigrateQueues(db *sql.DB) error {
	return sim.Migrate(db, "pubsub_queues/msgq", func() error {
		_, queues, err := sim.LegacyRows[psLegacyQueue](db, "pubsub_queues")
		if err != nil {
			return err
		}
		inflightStore, inflight, err := sim.LegacyRows[psLegacyInFlight](db, "pubsub_inflight")
		if err != nil {
			return err
		}
		migrated := map[string]*psQueue{}
		queueFor := func(sub string) *psQueue {
			if q, ok := migrated[sub]; ok {
				return q
			}
			q := &psQueue{Subscription: sub}
			migrated[sub] = q
			return q
		}
		for _, row := range inflight {
			m := row.Item
			published, err := psPublishMillis(m.Message)
			if err != nil {
				return err
			}
			q := queueFor(m.Subscription)
			q.Queue.NextSeq++
			q.Queue.Messages = append(q.Queue.Messages, msgq.Message[PSMessage]{
				ID:               m.Message.MessageId,
				Seq:              q.Queue.NextSeq,
				Payload:          m.Message,
				EnqueuedAt:       published,
				AvailableAt:      m.AckDeadline.UnixMilli(),
				Leased:           true,
				Receipt:          m.AckId,
				Deliveries:       1,
				FirstDeliveredAt: m.DeliveredAt.UnixMilli(),
			})
		}
		for _, row := range queues {
			if len(row.Item.Queue) > 0 && string(row.Item.Queue) != "null" {
				continue
			}
			var pending []PSMessage
			if len(row.Item.Messages) > 0 {
				if err := json.Unmarshal(row.Item.Messages, &pending); err != nil {
					return err
				}
			}
			q := queueFor(row.ID)
			for _, m := range pending {
				q.Queue.NextSeq++
				at, err := psPublishMillis(m)
				if err != nil {
					return err
				}
				q.Queue.Messages = append(q.Queue.Messages, msgq.Message[PSMessage]{
					ID: m.MessageId, Seq: q.Queue.NextSeq, Payload: m, EnqueuedAt: at, AvailableAt: at,
				})
			}
		}
		for sub, q := range migrated {
			psQueues.Put(sub, *q)
		}
		for _, row := range inflight {
			inflightStore.Delete(row.ID)
		}
		return nil
	})
}

func psPublishMillis(m PSMessage) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, m.PublishTime)
	if err != nil {
		return 0, fmt.Errorf("message %s has publish time %q: %w", m.MessageId, m.PublishTime, err)
	}
	return t.UnixMilli(), nil
}
