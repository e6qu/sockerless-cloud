package main

import (
	"errors"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// Service Bus message sessions: a requiresSession entity delivers a session's
// messages only to the receiver holding its lock, taken on accepting the
// session — by name, or as the next session with a message — and kept for the
// lockDuration, renewed with renew-session-lock. Each session carries a state
// its receivers read and write.

var (
	errSBSessionLocked   = errors.New("the requested session cannot be accepted: it is locked by another receiver")
	errSBSessionLockLost = errors.New("the session lock has expired or was lost")
	// Service Bus reports both as an InvalidOperationException: amqp:not-allowed
	// over AMQP, 400 Bad Request over REST.
	errSBSessionIDRequired = sbInvalidOperation("The SessionId was not set on a message, and it cannot be sent to the entity. Entities that have session support enabled can only receive messages that have the SessionId set to a valid value.")
	errSBSessionfulEntity  = sbInvalidOperation("It is not possible for an entity that requires sessions to create a non-sessionful message receiver.")
)

// The text is the service's own sentence.
type sbInvalidOperation string

func (e sbInvalidOperation) Error() string { return string(e) }

// Refuse a send lacking session ids when the address is, or fans out to, a
// session-enabled entity.
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

// The named session or, with requested empty, the first session holding a
// receivable message whose lock is free. False when none exists.
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

// As closing a session receiver does.
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

func sbSessionHeld(namespace, path, session, owner string) bool {
	rec, _ := sbQueueDurable.Get(sbQueueKey(namespace, path))
	s := rec.Sessions[session]
	return s.Owner == owner && s.LockedUntil > time.Now().UnixMilli()
}

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

// Messages scheduled on a topic still waiting in any of its subscriptions.
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

// Keyed by sbQueueKey: wakes what waits for an entity to gain a receivable
// message or a free session, and hands a scheduled or locked message to the
// AMQP receivers holding credit the instant it becomes receivable.
var sbReceivers = msgq.NewWaiter(func(key string, now time.Time) (time.Time, bool) {
	namespace, path, ok := strings.Cut(key, "/")
	if !ok {
		return time.Time{}, false
	}
	rec, ok := sbQueueDurable.Get(key)
	if !ok {
		return time.Time{}, false
	}
	return rec.Queue.NextChange(sbSettings(namespace, path).policy(), now)
})

func sbSignalEnqueue(namespace, path string) { sbReceivers.Signal(sbQueueKey(namespace, path)) }

// The hook names functions that reach sbReceivers, which its initializer
// cannot without an initialization cycle.
func init() {
	sbReceivers.OnDue(func(key string) {
		if namespace, path, ok := strings.Cut(key, "/"); ok {
			sbHandToAMQPReceivers(namespace, path)
		}
	})
}
