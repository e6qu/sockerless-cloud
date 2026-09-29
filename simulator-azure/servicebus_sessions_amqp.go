package main

import (
	"math"
	"strings"
	"time"
)

const (
	sbAMQPSessionFilterName = "com.microsoft:session-filter"
	// sbAMQPSessionFilterCode is the descriptor of the session filter the
	// Service Bus SDKs put on a session receiver's source.
	sbAMQPSessionFilterCode = uint64(0x00000137000000C)
	// sbDotNetEpochTicks is 1970-01-01 in .NET ticks, the unit of the
	// com.microsoft:locked-until-utc link property.
	sbDotNetEpochTicks = int64(621355968000000000)
)

// sbAMQPSessionFilter reads a receiver's session filter: whether the source
// carries one, and the session it names — empty to accept the next session
// with a message to receive.
func sbAMQPSessionFilter(source any) (string, bool) {
	d, ok := source.(amqpDescribed)
	if !ok {
		return "", false
	}
	fields, _ := d.value.([]any)
	filters, _ := field(fields, 7).(map[any]any)
	for k, v := range filters {
		var name string
		switch key := k.(type) {
		case amqpSymbol:
			name = string(key)
		case string:
			name = key
		}
		if !strings.EqualFold(name, sbAMQPSessionFilterName) {
			continue
		}
		if f, ok := v.(amqpDescribed); ok {
			session, _ := f.value.(string)
			return session, true
		}
		session, _ := v.(string)
		return session, true
	}
	return "", false
}

// sbAMQPDefaultSessionTimeout is how long Service Bus lets an accept of the
// next session wait when the receiver names no timeout: "up to one minute", as
// the azservicebus SDK records where it maps the resulting com.microsoft:timeout.
const sbAMQPDefaultSessionTimeout = time.Minute

// sbAMQPSessionTimeout reads how long an accept of the next session may wait,
// the com.microsoft:timeout attach property in milliseconds.
func sbAMQPSessionTimeout(properties any) time.Duration {
	props, _ := properties.(map[any]any)
	for k, v := range props {
		var name string
		switch key := k.(type) {
		case amqpSymbol:
			name = string(key)
		case string:
			name = key
		}
		if name == "com.microsoft:timeout" {
			return time.Duration(asUint32(v)) * time.Millisecond
		}
	}
	return sbAMQPDefaultSessionTimeout
}

// acceptSession completes a session receiver's attach once it holds a
// session's lock: a named session at once unless another receiver holds it,
// the next session as soon as one has a message to receive. An accept of the
// next session that outlasts its timeout is refused with com.microsoft:timeout,
// and a named session another receiver holds with
// com.microsoft:session-cannot-be-locked.
func (c *sbAMQPConn) acceptSession(frame amqpFrame, link *sbAMQPLink, requested string, timeout time.Duration) {
	namespace := c.currentNamespace()
	path, _ := sbAMQPReceiverPath(link.address)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	expired := timer.C
	for {
		signal := sbEnqueueSignal(namespace, path)
		session, until, ok, err := sbAcceptSession(namespace, path, requested, link.owner)
		if err != nil {
			c.refuseSession(frame, link, "com.microsoft:session-cannot-be-locked", err.Error())
			return
		}
		if ok {
			c.completeSessionAttach(frame, link, session, until)
			return
		}
		select {
		case <-signal:
		case <-expired:
			c.refuseSession(frame, link, "com.microsoft:timeout",
				"No session with a message to receive became available within "+timeout.String()+".")
			return
		case <-link.detached:
			return
		case <-c.done:
			return
		}
	}
}

func (c *sbAMQPConn) completeSessionAttach(frame amqpFrame, link *sbAMQPLink, session string, until time.Time) {
	c.mu.Lock()
	current := c.links[sbAMQPLinkKey(link.channel, link.clientHandle)]
	if current != link {
		c.mu.Unlock()
		path, _ := sbAMQPReceiverPath(link.address)
		sbReleaseSession(c.currentNamespace(), path, session, link.owner)
		return
	}
	link.session = session
	link.awaitingSession = false
	c.mu.Unlock()

	sndSettleMode := uint8(2)
	if field(frame.fields, 3) != nil {
		sndSettleMode = asUint8(field(frame.fields, 3))
	}
	source := amqpDescribed{code: amqpDescSource, value: []any{
		link.address, uint32(0), amqpSymbol("session-end"), nil, nil, nil, nil,
		map[any]any{amqpSymbol(sbAMQPSessionFilterName): amqpDescribed{code: sbAMQPSessionFilterCode, value: session}},
	}}
	ticks := sbDotNetEpochTicks + until.UnixMilli()*10000
	if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, []any{
		link.name, link.serverHandle, false, sndSettleMode, field(frame.fields, 4),
		source, nil, nil, nil, uint32(0), uint64(math.MaxUint32), nil, nil,
		map[any]any{amqpSymbol("com.microsoft:locked-until-utc"): ticks},
	})); err != nil {
		return
	}
	path, _ := sbAMQPReceiverPath(link.address)
	_ = c.deliverAvailableMessages([]string{path})
}

// refuseSession answers a session receiver's attach with the refusal and
// forgets the link.
func (c *sbAMQPConn) refuseSession(frame amqpFrame, link *sbAMQPLink, condition, description string) {
	c.mu.Lock()
	if c.links[sbAMQPLinkKey(link.channel, link.clientHandle)] == link {
		delete(c.links, sbAMQPLinkKey(link.channel, link.clientHandle))
	}
	c.mu.Unlock()
	_ = c.refuseAttachWith(frame, link.name, true, condition, description)
}

// endLinks lets go of what the ended links held: a session receiver's lock
// and any accept still waiting for a session.
func (c *sbAMQPConn) endLinks(links []*sbAMQPLink) {
	namespace := c.currentNamespace()
	for _, link := range links {
		c.mu.Lock()
		session, owner, detached := link.session, link.owner, link.detached
		c.mu.Unlock()
		if detached != nil {
			close(detached)
		}
		if session != "" {
			path, _ := sbAMQPReceiverPath(link.address)
			sbReleaseSession(namespace, path, session, owner)
		}
	}
}
