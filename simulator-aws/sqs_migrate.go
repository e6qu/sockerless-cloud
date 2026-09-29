package main

import (
	"database/sql"
	"encoding/json"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// sqsLegacyQueue is an Amazon SQS queue row as builds before the shared
// message queue stored it: messages as a list with their receive state inline,
// deduplication records beside them.
type sqsLegacyQueue struct {
	Name              string
	URL               string
	ARN               string
	CreatedTimestamp  int64
	VisibilityTimeout int
	Tags              map[string]string
	Messages          json.RawMessage
	Attributes        map[string]string
	Deduplication     map[string]struct {
		MessageID      string
		SequenceNumber string
		ExpiresAt      int64
	}
	NextSequence uint64
}

type sqsLegacyMessage struct {
	MessageId               string
	Body                    string
	MD5OfBody               string
	ReceiptHandle           string
	SentTimestamp           int64
	VisibleAt               int64
	ApproximateReceiveCount int
	FirstReceivedAt         int64
	DelayedUntil            int64
	MessageGroupID          string
	MessageDeduplicationID  string
	SequenceNumber          string
	MessageAttributes       map[string]SQSMessageAttribute
	MD5OfMessageAttributes  string
}

// sqsMigrateQueues converts the queue rows an earlier build wrote into the
// current shape, keeping every message's body, attributes, receive count,
// receipt handle and visibility.
func sqsMigrateQueues(db *sql.DB) error {
	return sim.Migrate(db, "sqs_queues/msgq", func() error {
		_, rows, err := sim.LegacyRows[sqsLegacyQueue](db, "sqs_queues")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if len(row.Item.Messages) > 0 && row.Item.Messages[0] == '{' {
				continue
			}
			q, err := sqsQueueFromLegacy(row.Item)
			if err != nil {
				return err
			}
			sqsQueues.Put(row.ID, q)
		}
		return nil
	})
}

func sqsQueueFromLegacy(old sqsLegacyQueue) (SQSQueue, error) {
	q := SQSQueue{
		Name: old.Name, URL: old.URL, ARN: old.ARN, CreatedTimestamp: old.CreatedTimestamp,
		VisibilityTimeout: old.VisibilityTimeout, Tags: old.Tags, Attributes: old.Attributes,
	}
	var msgs []sqsLegacyMessage
	if len(old.Messages) > 0 {
		if err := json.Unmarshal(old.Messages, &msgs); err != nil {
			return q, err
		}
	}
	seq, last := old.NextSequence, uint64(0)
	for _, m := range msgs {
		n, err := strconv.ParseUint(m.SequenceNumber, 10, 64)
		if err != nil {
			// Only FIFO messages carried a sequence number; a standard
			// queue's take their place in its order.
			n = last + 1
		}
		last = n
		seq = max(seq, n)
		q.Messages.Messages = append(q.Messages.Messages, msgq.Message[sqsPayload]{
			ID:  m.MessageId,
			Seq: n,
			Payload: sqsPayload{
				Body: m.Body, MD5OfBody: m.MD5OfBody,
				MessageAttributes: m.MessageAttributes, MD5OfMessageAttributes: m.MD5OfMessageAttributes,
			},
			EnqueuedAt:       m.SentTimestamp,
			AvailableAt:      m.VisibleAt,
			DelayedUntil:     m.DelayedUntil,
			Leased:           m.ReceiptHandle != "",
			Receipt:          m.ReceiptHandle,
			Deliveries:       m.ApproximateReceiveCount,
			FirstDeliveredAt: m.FirstReceivedAt,
			Group:            m.MessageGroupID,
			DedupID:          m.MessageDeduplicationID,
		})
	}
	q.Messages.NextSeq = seq
	for key, rec := range old.Deduplication {
		n, _ := strconv.ParseUint(rec.SequenceNumber, 10, 64)
		if q.Messages.Dedup == nil {
			q.Messages.Dedup = msgq.Dedup{}
		}
		q.Messages.Dedup[key] = msgq.DedupRecord{ID: rec.MessageID, Seq: n, ExpiresAt: rec.ExpiresAt}
	}
	return q, nil
}
