package main

import (
	"fmt"
	"maps"
	"math/big"
	"sort"
	"time"
)

// kinesisIteratorLifetime is how long a shard iterator stays valid: "A shard
// iterator expires 5 minutes after it is returned to the requester."
const kinesisIteratorLifetime = 5 * time.Minute

func kinesisShardClosed(shard KinesisShard) bool {
	_, closed := shard.SequenceNumberRange["EndingSequenceNumber"]
	return closed
}

func kinesisHashRange(shard KinesisShard) (*big.Int, *big.Int) {
	start, _ := new(big.Int).SetString(shard.HashKeyRange["StartingHashKey"], 10)
	end, _ := new(big.Int).SetString(shard.HashKeyRange["EndingHashKey"], 10)
	return start, end
}

// kinesisOpenShards lists the stream's open shards in hash key order.
func kinesisOpenShards(stream KinesisStream) []KinesisShard {
	var open []KinesisShard
	for _, shard := range stream.Shards {
		if !kinesisShardClosed(shard) {
			open = append(open, shard)
		}
	}
	sort.Slice(open, func(a, b int) bool {
		startA, _ := kinesisHashRange(open[a])
		startB, _ := kinesisHashRange(open[b])
		return startA.Cmp(startB) < 0
	})
	return open
}

// kinesisCloseShard closes a shard: it takes no further records, and its
// EndingSequenceNumber is its last record's. The caller holds kinesisMu.
func kinesisCloseShard(stream *KinesisStream, shardID string) {
	for i := range stream.Shards {
		if stream.Shards[i].ShardId != shardID || kinesisShardClosed(stream.Shards[i]) {
			continue
		}
		head := kinesisLog.Head(kinesisShardRecordKey(stream.StreamName, shardID))
		sequenceRange := maps.Clone(stream.Shards[i].SequenceNumberRange)
		if sequenceRange == nil {
			sequenceRange = map[string]string{}
		}
		sequenceRange["EndingSequenceNumber"] = kinesisSequenceNumber(max(head.Next-1, 0))
		stream.Shards[i].SequenceNumberRange = sequenceRange
	}
}

// kinesisSplitShard closes parent and opens two children dividing its hash key
// range at newStart. The caller holds kinesisMu.
func kinesisSplitShard(stream *KinesisStream, parent KinesisShard, newStart *big.Int) {
	low := KinesisShard{
		ShardId: kinesisNextShardID(*stream),
		HashKeyRange: map[string]string{
			"StartingHashKey": parent.HashKeyRange["StartingHashKey"],
			"EndingHashKey":   new(big.Int).Sub(newStart, big.NewInt(1)).String(),
		},
		SequenceNumberRange: map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:       parent.ShardId,
	}
	stream.Shards = append(stream.Shards, low)
	high := KinesisShard{
		ShardId: kinesisNextShardID(*stream),
		HashKeyRange: map[string]string{
			"StartingHashKey": newStart.String(),
			"EndingHashKey":   parent.HashKeyRange["EndingHashKey"],
		},
		SequenceNumberRange: map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:       parent.ShardId,
	}
	stream.Shards = append(stream.Shards, high)
	kinesisCloseShard(stream, parent.ShardId)
	stream.OpenShardCount = kinesisOpenShardCount(stream.Shards)
}

// kinesisMergeShards closes two adjacent open shards and opens the child that
// spans both ranges; the child names parent as its ParentShardId and adjacent
// as its AdjacentParentShardId. The caller holds kinesisMu and has checked the
// two ranges are contiguous.
func kinesisMergeShards(stream *KinesisStream, parent, adjacent KinesisShard) {
	lower, upper := parent, adjacent
	lowerStart, _ := kinesisHashRange(lower)
	upperStart, _ := kinesisHashRange(upper)
	if lowerStart.Cmp(upperStart) > 0 {
		lower, upper = upper, lower
	}
	child := KinesisShard{
		ShardId: kinesisNextShardID(*stream),
		HashKeyRange: map[string]string{
			"StartingHashKey": lower.HashKeyRange["StartingHashKey"],
			"EndingHashKey":   upper.HashKeyRange["EndingHashKey"],
		},
		SequenceNumberRange:   map[string]string{"StartingSequenceNumber": "1"},
		ParentShardId:         parent.ShardId,
		AdjacentParentShardId: adjacent.ShardId,
	}
	stream.Shards = append(stream.Shards, child)
	kinesisCloseShard(stream, parent.ShardId)
	kinesisCloseShard(stream, adjacent.ShardId)
	stream.OpenShardCount = kinesisOpenShardCount(stream.Shards)
}

// kinesisAdjacent reports whether two shards' hash key ranges are contiguous.
func kinesisAdjacent(a, b KinesisShard) bool {
	aStart, aEnd := kinesisHashRange(a)
	bStart, bEnd := kinesisHashRange(b)
	one := big.NewInt(1)
	return new(big.Int).Add(aEnd, one).Cmp(bStart) == 0 || new(big.Int).Add(bEnd, one).Cmp(aStart) == 0
}

// kinesisReshardUniformly reaches target open shards of equal size the way
// UpdateShardCount does: it splits each open shard a uniform boundary falls
// inside, then merges the pieces each uniform range holds, leaving the
// short-lived shards in between closed. The caller holds kinesisMu.
func kinesisReshardUniformly(stream *KinesisStream, target int64) {
	uniform := kinesisMakeShards(target)
	for _, targetShard := range uniform[1:] {
		boundary, _ := kinesisHashRange(targetShard)
		for _, shard := range kinesisOpenShards(*stream) {
			start, end := kinesisHashRange(shard)
			if start.Cmp(boundary) < 0 && boundary.Cmp(end) <= 0 {
				kinesisSplitShard(stream, shard, boundary)
				break
			}
		}
	}
	for _, targetShard := range uniform {
		targetStart, targetEnd := kinesisHashRange(targetShard)
		for {
			var pieces []KinesisShard
			for _, shard := range kinesisOpenShards(*stream) {
				start, end := kinesisHashRange(shard)
				if start.Cmp(targetStart) >= 0 && end.Cmp(targetEnd) <= 0 {
					pieces = append(pieces, shard)
				}
			}
			if len(pieces) < 2 {
				break
			}
			kinesisMergeShards(stream, pieces[0], pieces[1])
		}
	}
}

// kinesisChildShards lists the children of a closed shard as GetRecords and
// SubscribeToShard report them at the shard's end.
func kinesisChildShards(stream KinesisStream, shardID string) []map[string]any {
	children := []map[string]any{}
	for _, shard := range stream.Shards {
		if shard.ParentShardId != shardID && shard.AdjacentParentShardId != shardID {
			continue
		}
		parents := []string{shard.ParentShardId}
		if shard.AdjacentParentShardId != "" {
			parents = append(parents, shard.AdjacentParentShardId)
		}
		children = append(children, map[string]any{
			"ShardId":      shard.ShardId,
			"ParentShards": parents,
			"HashKeyRange": shard.HashKeyRange,
		})
	}
	return children
}

// kinesisExpiredIteratorMessage is Amazon Kinesis Data Streams' message for an
// iterator older than its five-minute lifetime.
func kinesisExpiredIteratorMessage(issued, now time.Time) string {
	return fmt.Sprintf("Iterator expired. The iterator was created at time %s while right now it is %s which is further in the future than the tolerated delay of %d milliseconds.",
		issued.UTC().Format(time.UnixDate), now.UTC().Format(time.UnixDate), kinesisIteratorLifetime.Milliseconds())
}
