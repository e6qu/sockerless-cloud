package main

import (
	"errors"
	"sync"
	"time"
)

// Service Bus message sessions. A session-enabled queue or subscription
// (requiresSession) delivers a session's messages only to the receiver that
// holds the session's lock, which it takes when it accepts the session — by
// name, or as the next session with a message to receive — and keeps for the
// entity's lockDuration, renewing it with renew-session-lock. Each session
// carries a state its receivers read and write.

var (
	errSBSessionLocked   = errors.New("the requested session cannot be accepted: it is locked by another receiver")
	errSBSessionLockLost = errors.New("the session lock has expired or was lost")
	// Service Bus reports both as an InvalidOperationException: amqp:not-allowed
	// over AMQP, 400 Bad Request over REST.
	errSBSessionIDRequired = sbInvalidOperation("The SessionId was not set on a message, and it cannot be sent to the entity. Entities that have session support enabled can only receive messages that have the SessionId set to a valid value.")
	errSBSessionfulEntity  = sbInvalidOperation("It is not possible for an entity that requires sessions to create a non-sessionful message receiver.")
)

// sbInvalidOperation is a refusal whose text is the service's own sentence.
type sbInvalidOperation string

func (e sbInvalidOperation) Error() string { return string(e) }

// sbRequireSessionIDs refuses a send whose messages lack a session id when the
// address is, or fans out to, a session-enabled queue or subscription.
func sbRequireSessionIDs(namespace, address string, outs []sbOutgoing) error {
	for _, path := range sbSendPaths(namespace, address) {
		if !sbSettings(namespace, path).requiresSession {
			continue
		}
		for _, out := range outs {
			if out.sessionID == "" {
				return errSBSessionIDRequired
			}
		}
	}
	return nil
}

// sbAcceptSession locks a session of a session-enabled entity for owner: the
// named one, or with requested empty the first session holding a receivable
// message whose lock is free. It reports false when no such session exists.
func sbAcceptSession(namespace, path, requested, owner string) (string, time.Time, bool, error) {
	settings := sbSettings(namespace, path)
	now := time.Now()
	var session string
	var until time.Time
	var lockErr error
	sbQueueDurable.Upsert(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		free := func(id string) bool {
			s := rec.Sessions[id]
			return s.Owner == "" || s.Owner == owner || s.LockedUntil <= now.UnixMilli()
		}
		if requested != "" {
			if !free(requested) {
				lockErr = errSBSessionLocked
				return
			}
			session = requested
		} else {
			for _, m := range rec.Queue.Messages {
				if m.Group != "" && !m.Payload.Deferred && rec.Queue.Available(m, settings.policy(), now) && free(m.Group) {
					session = m.Group
					break
				}
			}
			if session == "" {
				return
			}
		}
		if rec.Sessions == nil {
			rec.Sessions = map[string]sbSessionRecord{}
		}
		s := rec.Sessions[session]
		until = now.Add(settings.lock)
		s.Owner, s.LockedUntil = owner, until.UnixMilli()
		rec.Sessions[session] = s
	})
	if lockErr != nil {
		return "", time.Time{}, false, lockErr
	}
	return session, until, session != "", nil
}

// sbReleaseSession gives up owner's lock on a session, as closing a session
// receiver does.
func sbReleaseSession(namespace, path, session, owner string) {
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		s, ok := rec.Sessions[session]
		if !ok || s.Owner != owner {
			return
		}
		s.Owner, s.LockedUntil = "", 0
		rec.Sessions[session] = s
	})
	sbSignalEnqueue(namespace, path)
}

// sbSessionHeld reports whether owner still holds a session's lock.
func sbSessionHeld(namespace, path, session, owner string) bool {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	s := rec.Sessions[session]
	return s.Owner == owner && s.LockedUntil > time.Now().UnixMilli()
}

// sbRenewSessionLock extends the lock of a session its holder still holds.
func sbRenewSessionLock(namespace, path, session string) (time.Time, error) {
	settings := sbSettings(namespace, path)
	now := time.Now()
	var until time.Time
	held := false
	sbQueueDurable.Update(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		s, ok := rec.Sessions[session]
		if !ok || s.Owner == "" || s.LockedUntil <= now.UnixMilli() {
			return
		}
		held = true
		until = now.Add(settings.lock)
		s.LockedUntil = until.UnixMilli()
		rec.Sessions[session] = s
	})
	if !held {
		return time.Time{}, errSBSessionLockLost
	}
	return until, nil
}

func sbSessionState(namespace, path, session string) []byte {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	return rec.Sessions[session].State
}

func sbSetSessionState(namespace, path, session string, state []byte) {
	sbQueueDurable.Upsert(sbQueueKey(namespace, path), func(rec *sbQueueRecord) {
		if rec.Sessions == nil {
			rec.Sessions = map[string]sbSessionRecord{}
		}
		s := rec.Sessions[session]
		s.State = append([]byte(nil), state...)
		rec.Sessions[session] = s
	})
}

// sbScheduledCount counts the messages of an entity waiting for their
// scheduled enqueue time.
func sbScheduledCount(namespace, path string) int32 {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	now := time.Now()
	var n int32
	for _, m := range rec.Queue.Messages {
		if m.Delayed(now) {
			n++
		}
	}
	return n
}

// sbTopicScheduledCount counts the messages scheduled on a topic that still
// wait for their enqueue time in any of its subscriptions.
func sbTopicScheduledCount(namespace, topic string, subs []string) int32 {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, topic))
	if len(rec.Scheduled) == 0 {
		return 0
	}
	scheduled := map[string]bool{}
	for _, id := range rec.Scheduled {
		scheduled[id] = true
	}
	waiting := map[string]bool{}
	now := time.Now()
	for _, path := range subs {
		sub, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
		for _, m := range sub.Queue.Messages {
			if scheduled[m.ID] && m.Delayed(now) {
				waiting[m.ID] = true
			}
		}
	}
	return int32(len(waiting))
}

// sbEnqueueSignals wakes whatever waits for an entity to gain a receivable
// message or a free session: each waiter holds the channel current when it
// looked, and the next signal closes it.
var sbEnqueueSignals = struct {
	sync.Mutex
	ch map[string]chan struct{}
}{ch: map[string]chan struct{}{}}

func sbEnqueueSignal(namespace, path string) <-chan struct{} {
	key := sbQueueKey(namespace, path)
	sbEnqueueSignals.Lock()
	defer sbEnqueueSignals.Unlock()
	ch := sbEnqueueSignals.ch[key]
	if ch == nil {
		ch = make(chan struct{})
		sbEnqueueSignals.ch[key] = ch
	}
	return ch
}

func sbSignalEnqueue(namespace, path string) {
	key := sbQueueKey(namespace, path)
	sbEnqueueSignals.Lock()
	defer sbEnqueueSignals.Unlock()
	if ch := sbEnqueueSignals.ch[key]; ch != nil {
		close(ch)
		delete(sbEnqueueSignals.ch, key)
	}
}
