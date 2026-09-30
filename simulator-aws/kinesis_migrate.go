package main

import (
	"database/sql"
	"fmt"
	"math"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/streamlog"
)

// kinesisLegacyRecord is a record as builds before the shard log stored it:
// every record of a shard in one row, keyed stream/shard.
type kinesisLegacyRecord struct {
	SequenceNumber              string
	ApproximateArrivalTimestamp float64
	Data                        []byte
	PartitionKey                string
	ExplicitHashKey             string
}

// kinesisLegacyIterator is a shard iterator as those builds stored it, by
// position in the shard's record list.
type kinesisLegacyIterator struct {
	StreamName string
	ShardID    string
	Index      int
	Next       int64
}

// kinesisMigrateRecords moves the shards an earlier build stored whole into
// the shard log, keeping each record's sequence number, arrival time, data and
// keys, and re-points the outstanding shard iterators at the same records.
func kinesisMigrateRecords(db *sql.DB) error {
	return sim.Migrate(db, "kinesis_records/streamlog", func() error {
		legacy, shards, err := sim.LegacyRows[[]kinesisLegacyRecord](db, "kinesis_records")
		if err != nil {
			return err
		}
		for _, shard := range shards {
			recs := make([]streamlog.Record[kinesisRecord], 0, len(shard.Item))
			for _, r := range shard.Item {
				seq, ok := kinesisParseSequenceNumber(r.SequenceNumber)
				if !ok {
					return fmt.Errorf("shard %s holds record %q with an invalid sequence number", shard.ID, r.SequenceNumber)
				}
				recs = append(recs, streamlog.Record[kinesisRecord]{
					Seq:   seq,
					Time:  int64(math.Round(r.ApproximateArrivalTimestamp * 1000)),
					Value: kinesisRecord{Data: r.Data, PartitionKey: r.PartitionKey, ExplicitHashKey: r.ExplicitHashKey},
				})
			}
			next := int64(0)
			if len(recs) > 0 {
				next = recs[len(recs)-1].Seq + 1
			}
			if err := kinesisLog.Import(shard.ID, next, recs); err != nil {
				return err
			}
			legacy.Delete(shard.ID)
		}
		_, iterators, err := sim.LegacyRows[kinesisLegacyIterator](db, "kinesis_iterators")
		if err != nil {
			return err
		}
		migratedAt := time.Now().UnixMilli()
		for _, it := range iterators {
			if it.Item.Index == 0 {
				continue
			}
			// Those builds recorded no issue time, so the iterator's five
			// minutes run from the migration.
			kinesisIterators.Put(it.ID, kinesisIterator{
				StreamName: it.Item.StreamName, ShardID: it.Item.ShardID, Next: int64(it.Item.Index),
				IssuedAt: migratedAt,
			})
		}
		return nil
	})
}
