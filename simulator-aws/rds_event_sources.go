package main

import "github.com/e6qu/sockerless-cloud/sim"

// rdsObservedStore is a store whose every write reports the item before and
// after it (nil when absent), so the events a state change raises are
// recorded where the state changes, whichever code path made it.
type rdsObservedStore[T any] struct {
	sim.Store[T]
	observe func(before, after *T)
}

func rdsObserve[T any](store sim.Store[T], observe func(before, after *T)) sim.Store[T] {
	return rdsObservedStore[T]{Store: store, observe: observe}
}

func (s rdsObservedStore[T]) Put(id string, item T) {
	before, had := s.Get(id)
	s.Store.Put(id, item)
	if had {
		s.observe(&before, &item)
	} else {
		s.observe(nil, &item)
	}
}

func (s rdsObservedStore[T]) Update(id string, fn func(*T)) bool {
	var before, after T
	ok := s.Store.Update(id, func(item *T) {
		before = *item
		fn(item)
		after = *item
	})
	if ok {
		s.observe(&before, &after)
	}
	return ok
}

func (s rdsObservedStore[T]) Upsert(id string, fn func(*T)) {
	before, had := s.Get(id)
	var after T
	s.Store.Upsert(id, func(item *T) {
		fn(item)
		after = *item
	})
	if had {
		s.observe(&before, &after)
	} else {
		s.observe(nil, &after)
	}
}

func (s rdsObservedStore[T]) Delete(id string) bool {
	before, had := s.Get(id)
	ok := s.Store.Delete(id)
	if ok && had {
		s.observe(&before, nil)
	}
	return ok
}

func (s rdsObservedStore[T]) Prune(expired func(T) bool) int {
	var pruned []T
	n := s.Store.Prune(func(item T) bool {
		if expired(item) {
			pruned = append(pruned, item)
			return true
		}
		return false
	})
	for i := range pruned {
		s.observe(&pruned[i], nil)
	}
	return n
}

// rdsObserveInstance records the DB instance events a status or class change
// raises: creation as the instance leaves creating, a backup while it is
// backing-up, a stop and a start as they finish, and deletion as the instance
// goes.
func rdsObserveInstance(before, after *RDSInstance) {
	switch {
	case after == nil:
		if before != nil && !rdsResourceHeldElsewhere(*before) {
			rdsRecordInstanceEvent(*before, rdsEventInstanceDeleted)
		}
		return
	case before == nil:
		if after.DBInstanceStatus == "available" && !rdsResourceHeldElsewhere(*after) {
			rdsRecordInstanceEvent(*after, rdsEventInstanceCreated)
		}
		return
	}
	was, is := before.DBInstanceStatus, after.DBInstanceStatus
	if was != is {
		if was == "creating" && is != "failed" && is != "deleting" {
			rdsRecordInstanceEvent(*after, rdsEventInstanceCreated)
		}
		if was == "starting" && is != "stopping" && is != "stopped" {
			rdsRecordInstanceEvent(*after, rdsEventInstanceStarted)
		}
		if was == "stopping" && is == "stopped" {
			rdsRecordInstanceEvent(*after, rdsEventInstanceStopped)
		}
		if was == "backing-up" {
			rdsRecordInstanceEvent(*after, rdsEventBackupFinished)
		}
		if is == "backing-up" {
			rdsRecordInstanceEvent(*after, rdsEventBackupStarted)
		}
	}
	if before.DBInstanceClass != "" && after.DBInstanceClass != "" && before.DBInstanceClass != after.DBInstanceClass {
		rdsRecordInstanceEvent(*after, rdsEventInstanceClassModified)
	}
}

// rdsResourceHeldElsewhere reports whether another record holds the
// instance's DbiResourceId: a rename and a blue/green switchover move an
// instance between identifiers, which creates and deletes nothing.
func rdsResourceHeldElsewhere(instance RDSInstance) bool {
	if instance.DbiResourceId == "" {
		return false
	}
	return len(rdsInstances.Filter(func(other RDSInstance) bool {
		return other.DbiResourceId == instance.DbiResourceId && other.DBInstanceIdentifier != instance.DBInstanceIdentifier
	})) > 0
}

// rdsObserveSnapshot records the DB snapshot events of a snapshot's creation
// and of a manual snapshot's deletion.
func rdsObserveSnapshot(before, after *RDSSnapshot) {
	automated := func(s *RDSSnapshot) bool { return s.SnapshotType == "automated" }
	switch {
	case after == nil:
		if before != nil && !automated(before) {
			rdsRecordSnapshotEvent(*before, rdsEventSnapshotDeleted)
		}
	case before == nil || before.Status != after.Status:
		if after.Status == "creating" && (before == nil || before.Status != "creating") {
			kind := rdsEventManualSnapshotStarted
			if automated(after) {
				kind = rdsEventAutomatedSnapshotStarted
			}
			rdsRecordSnapshotEvent(*after, kind)
		}
		if before != nil && before.Status == "creating" && after.Status == "available" {
			kind := rdsEventManualSnapshotCreated
			if automated(after) {
				kind = rdsEventAutomatedSnapshotCreated
			}
			rdsRecordSnapshotEvent(*after, kind)
		}
	}
}

func rdsRecordSnapshotEvent(snapshot RDSSnapshot, kind rdsEventKind) {
	rdsRecordEvent(RDSEvent{
		SourceIdentifier: snapshot.DBSnapshotIdentifier,
		SourceType:       "db-snapshot",
		SourceArn:        snapshot.ARN,
	}, kind)
}
