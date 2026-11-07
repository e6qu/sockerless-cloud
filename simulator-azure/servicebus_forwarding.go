package main

import (
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/msgq"
)

// Auto-forwarding as service-bus-auto-forwarding describes it: a queue or
// subscription with forwardTo hands every message it receives to the queue or
// topic it names in the same namespace, and one with
// forwardDeadLetteredMessagesTo hands its dead letters on the same way. A
// message that would exceed four hops, or that the destination cannot take,
// is dead-lettered on the source.

const sbMaxForwardHops = 4

// The dead-letter reasons the simulator records on a message it could not
// forward.
const (
	sbForwardHopsReason     = "MaxTransferHopCountExceeded"
	sbForwardRefusedReason  = "TransferFailed"
	sbForwardSessionReason  = "SessionIdIsMissing"
	sbForwardDisabledStatus = "Disabled"
)

// sbForwardEntity is the entity name a forwardTo value names: Service Bus
// accepts the name, or the entity's URL in the namespace.
func sbForwardEntity(value string) string {
	if i := strings.Index(value, "://"); i >= 0 {
		rest := value[i+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			return strings.Trim(rest[slash+1:], "/")
		}
	}
	return strings.Trim(value, "/")
}

// sbForwardDestinationError is why a namespace cannot take forwardTo or
// forwardDeadLetteredMessagesTo on an entity: the destination must already
// exist, and a session-enabled entity cannot forward.
func sbForwardDestinationError(namespaceID string, props map[string]any) string {
	for _, key := range []string{"forwardTo", "forwardDeadLetteredMessagesTo"} {
		value := stringProp(props, key)
		if value == nil || *value == "" {
			continue
		}
		if key == "forwardTo" {
			if session := boolProp(props, "requiresSession"); session != nil && *session {
				return "A session-enabled entity cannot forward its messages; remove forwardTo or requiresSession."
			}
		}
		dest := sbForwardEntity(*value)
		_, queue := sbQueues.Get(armChildID(namespaceID, "queues", dest))
		_, topic := sbTopics.Get(armChildID(namespaceID, "topics", dest))
		if !queue && !topic {
			return "The " + key + " destination '" + dest + "' does not exist in the namespace."
		}
	}
	return ""
}

func sbEntityProperties(namespace, path string) map[string]any {
	if topic, sub, ok := strings.Cut(path, "/"); ok {
		if s, found := sbSubscriptions.Get(sbAdminSubscriptionID(namespace, topic, sub)); found {
			return s.Properties
		}
		return nil
	}
	if q, found := sbQueues.Get(sbAdminQueueID(namespace, path)); found {
		return q.Properties
	}
	if t, found := sbTopics.Get(sbAdminTopicID(namespace, path)); found {
		return t.Properties
	}
	return nil
}

// sbForwardsTo is the destination a queue or subscription forwards to, or "".
func sbForwardsTo(namespace, path, key string) string {
	if sbIsDeadLetterPath(path) {
		return ""
	}
	if v := stringProp(sbEntityProperties(namespace, path), key); v != nil {
		return sbForwardEntity(*v)
	}
	return ""
}

// sbForward moves one message from source to dest, counting the hop; a
// message the destination cannot take goes to the source's dead-letter queue.
func sbForward(namespace, source, dest string, out sbOutgoing, now time.Time) {
	out.payload.HopCount++
	deadLetter := func(reason, description string) {
		p := out.payload
		p.DeadLetterReason, p.DeadLetterErrorDescription = reason, description
		sbDeadLetterPayload(namespace, source, out.messageID, out.sessionID, p, now)
	}
	if out.payload.HopCount > sbMaxForwardHops {
		deadLetter(sbForwardHopsReason, "The message exceeded the four forwarding hops Service Bus allows.")
		return
	}
	props := sbEntityProperties(namespace, dest)
	if props == nil {
		deadLetter(sbForwardRefusedReason, "The forwarding destination '"+dest+"' does not exist.")
		return
	}
	if status := stringProp(props, "status"); status != nil && strings.EqualFold(*status, sbForwardDisabledStatus) {
		deadLetter(sbForwardRefusedReason, "The forwarding destination '"+dest+"' is disabled.")
		return
	}
	paths := sbSendPaths(namespace, dest)
	if out.sessionID == "" {
		for _, path := range paths {
			if sbSettings(namespace, path).requiresSession {
				deadLetter(sbForwardSessionReason, "The forwarding destination '"+path+"' requires a session ID the message does not carry.")
				return
			}
		}
	}
	for _, path := range paths {
		sbEnqueue(namespace, path, out, now)
	}
}

// sbDeadLetterPayload puts one message in an entity's dead-letter queue, or
// forwards it where the entity sends its dead letters.
func sbDeadLetterPayload(namespace, path, messageID, sessionID string, p sbPayload, now time.Time) {
	p.Deferred = false
	if dest := sbForwardsTo(namespace, path, "forwardDeadLetteredMessagesTo"); dest != "" {
		sbForward(namespace, path, dest, sbOutgoing{payload: p, messageID: messageID, sessionID: sessionID}, now)
		return
	}
	sbQueueDurable.Upsert(sbQueueKey(namespace, sbDeadLetterPath(path)), func(rec *sbQueueRecord) {
		rec.Queue.Enqueue(p, msgq.EnqueueOpts{ID: messageID}, msgq.Policy{}, now)
	})
	sbNotifyReceivers(namespace, sbDeadLetterPath(path))
}
