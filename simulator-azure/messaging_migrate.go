package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

// Conversions of the Service Bus, Queue Storage and Event Hubs rows builds
// before the shared message queue and stream log wrote.

type sbLegacyRecord struct {
	Messages []struct {
		MessageID      string
		Body           []byte
		ContentType    string
		BrokerHeader   string
		EnqueuedTime   time.Time
		LockedUntilUtc time.Time
		LockToken      string
		SequenceNumber int64
	} `json:"messages"`
	NextSeq int64           `json:"nextSeq"`
	Queue   json.RawMessage `json:"queue"`
}

// sbMigrateQueues converts each entity's stored messages, keeping their ids,
// bodies, broker properties, sequence numbers and live locks. A locked message
// counts the delivery it is under.
func sbMigrateQueues(db *sql.DB) error {
	return sim.Migrate(db, "servicebus_queue_messages/msgq", func() error {
		_, rows, err := sim.LegacyRows[sbLegacyRecord](db, "servicebus_queue_messages")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if len(row.Item.Queue) > 0 && string(row.Item.Queue) != "null" {
				continue
			}
			var rec sbQueueRecord
			rec.Queue.NextSeq = uint64(row.Item.NextSeq)
			for _, m := range row.Item.Messages {
				nm := msgq.Message[sbPayload]{
					ID:          m.MessageID,
					Seq:         uint64(m.SequenceNumber),
					Payload:     sbPayload{Body: m.Body, ContentType: m.ContentType, BrokerHeader: m.BrokerHeader},
					EnqueuedAt:  m.EnqueuedTime.UnixMilli(),
					AvailableAt: m.EnqueuedTime.UnixMilli(),
				}
				if m.LockToken != "" {
					nm.Leased = true
					nm.Receipt = m.LockToken
					nm.AvailableAt = m.LockedUntilUtc.UnixMilli()
					nm.Deliveries = 1
					nm.FirstDeliveredAt = m.EnqueuedTime.UnixMilli()
				}
				rec.Queue.NextSeq = max(rec.Queue.NextSeq, nm.Seq)
				rec.Queue.Messages = append(rec.Queue.Messages, nm)
			}
			sbQueueDurable.Put(row.ID, rec)
		}
		return nil
	})
}

type queueLegacyData struct {
	Account  string
	Name     string
	Created  string
	Metadata map[string]string
	Messages json.RawMessage
	ACLs     []TableSignedIdentifier
}

type queueLegacyMessage struct {
	MessageID      string
	MessageText    string
	InsertionTime  string
	ExpirationTime string
	PopReceipt     string
	VisibleAt      int64 // Unix seconds
	DequeueCount   int
}

// queueMigrateMessages converts each queue's stored messages, keeping their
// ids, text, insertion and expiration times, pop receipts, visibility and
// dequeue counts.
func queueMigrateMessages(db *sql.DB) error {
	return sim.Migrate(db, "queue_data/msgq", func() error {
		_, rows, err := sim.LegacyRows[queueLegacyData](db, "queue_data")
		if err != nil {
			return err
		}
		for _, row := range rows {
			old := row.Item
			if len(old.Messages) > 0 && old.Messages[0] == '{' {
				continue
			}
			var msgs []queueLegacyMessage
			if len(old.Messages) > 0 {
				if err := json.Unmarshal(old.Messages, &msgs); err != nil {
					return err
				}
			}
			q := QueueData{Account: old.Account, Name: old.Name, Created: old.Created, Metadata: old.Metadata, ACLs: old.ACLs}
			for _, m := range msgs {
				inserted, err := time.Parse(time.RFC1123, m.InsertionTime)
				if err != nil {
					return fmt.Errorf("queue %s message %s: insertion time %q: %w", row.ID, m.MessageID, m.InsertionTime, err)
				}
				expires, err := time.Parse(time.RFC1123, m.ExpirationTime)
				if err != nil {
					return fmt.Errorf("queue %s message %s: expiration time %q: %w", row.ID, m.MessageID, m.ExpirationTime, err)
				}
				q.Messages.NextSeq++
				q.Messages.Messages = append(q.Messages.Messages, msgq.Message[queuePayload]{
					ID:          m.MessageID,
					Seq:         q.Messages.NextSeq,
					Payload:     queuePayload{Text: m.MessageText},
					EnqueuedAt:  inserted.UnixMilli(),
					AvailableAt: m.VisibleAt * 1000,
					ExpiresAt:   expires.UnixMilli(),
					Leased:      m.PopReceipt != "",
					Receipt:     m.PopReceipt,
					Deliveries:  m.DequeueCount,
				})
			}
			queueData.Put(row.ID, q)
		}
		return nil
	})
}

type ehLegacyPartition struct {
	Records []struct {
		SequenceNumber int64
		EnqueuedTime   time.Time
		Body           []byte
		Properties     map[string]any
	}
	NextSeq int64
}

// ehMigratePartitions moves each partition an earlier build stored whole
// into the partition log, keeping every event's sequence number, enqueued
// time, body and application properties.
func ehMigratePartitions(db *sql.DB) error {
	return sim.Migrate(db, "eventhub_partition_events/streamlog", func() error {
		legacy, rows, err := sim.LegacyRows[ehLegacyPartition](db, "eventhub_partition_events")
		if err != nil {
			return err
		}
		for _, row := range rows {
			recs := make([]streamlog.Record[ehEvent], 0, len(row.Item.Records))
			for _, r := range row.Item.Records {
				raw, err := (&amqp.Message{Data: [][]byte{r.Body}, ApplicationProperties: r.Properties}).MarshalBinary()
				if err != nil {
					return fmt.Errorf("partition %s event %d: %w", row.ID, r.SequenceNumber, err)
				}
				recs = append(recs, streamlog.Record[ehEvent]{Seq: r.SequenceNumber, Time: r.EnqueuedTime.UnixMilli(), Value: ehEvent{AMQP: raw}})
			}
			if err := ehLog.Import(row.ID, row.Item.NextSeq, recs); err != nil {
				return err
			}
			legacy.Delete(row.ID)
		}
		return nil
	})
}
