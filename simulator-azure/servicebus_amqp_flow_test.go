package main

import (
	"context"
	"encoding/binary"
	"math"
	"testing"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim"
)

// sbFlowTestConn is a connection in namespace "ns" whose client authenticated
// for the whole namespace and began a session on channel 0.
func sbFlowTestConn(t *testing.T) (*sbAMQPConn, *chunkedAMQPTransport) {
	t.Helper()
	transport := &chunkedAMQPTransport{}
	c := newSBAMQPConn("ns", transport)
	c.grantClaim("sb://ns.servicebus.windows.net/")
	sbFlowTestSend(t, c, amqpDescBegin, []any{nil, uint32(1), uint32(5000), uint32(5000)})
	return c, transport
}

// sbFlowTestSend hands the connection one frame from the client.
func sbFlowTestSend(t *testing.T, c *sbAMQPConn, desc uint64, fields []any, payload ...byte) {
	t.Helper()
	body := append(encodeDescribedList(desc, fields), payload...)
	frame := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(8+len(body)))
	frame[4] = 2
	frame = append(frame, body...)
	parsed, err := parseAMQPFrame(frame)
	if err != nil {
		t.Fatalf("parse client frame: %v", err)
	}
	if err := c.handleFrame(context.Background(), parsed); err != nil {
		t.Fatalf("handle frame %#x: %v", desc, err)
	}
}

// sbFlowTestFrames returns the frames the connection wrote since the last call.
func sbFlowTestFrames(t *testing.T, transport *chunkedAMQPTransport) []amqpFrame {
	t.Helper()
	data := transport.wrote.Bytes()
	transport.wrote.Reset()
	var frames []amqpFrame
	for len(data) > 0 {
		size := int(binary.BigEndian.Uint32(data[:4]))
		frame, err := parseAMQPFrame(data[:size])
		if err != nil {
			t.Fatalf("parse server frame: %v", err)
		}
		frames = append(frames, frame)
		data = data[size:]
	}
	return frames
}

func sbFlowTestOfType(frames []amqpFrame, desc uint64) []amqpFrame {
	var out []amqpFrame
	for _, f := range frames {
		if f.desc == desc {
			out = append(out, f)
		}
	}
	return out
}

// sbFlowTestAttachReceiver attaches a receive-and-delete receiver link and
// returns the handle the simulator assigned it.
func sbFlowTestAttachReceiver(t *testing.T, c *sbAMQPConn, transport *chunkedAMQPTransport, name string, handle uint32, source string) uint32 {
	t.Helper()
	sbFlowTestSend(t, c, amqpDescAttach, []any{name, handle, true, uint8(1), uint8(0), encodeSource(source), encodeTarget(name)})
	attaches := sbFlowTestOfType(sbFlowTestFrames(t, transport), amqpDescAttach)
	if len(attaches) != 1 {
		t.Fatalf("attach of %s answered with %d attach frames", source, len(attaches))
	}
	if got := asUint32(field(attaches[0].fields, 9)); got != 0 {
		t.Fatalf("attach of %s: initial-delivery-count %d, want 0", source, got)
	}
	return asUint32(field(attaches[0].fields, 1))
}

func sbFlowTestFlow(t *testing.T, c *sbAMQPConn, handle, deliveryCount, credit uint32, drain bool) {
	t.Helper()
	sbFlowTestSend(t, c, amqpDescFlow, []any{uint32(1), uint32(5000), uint32(1), uint32(5000), handle, deliveryCount, credit, nil, drain, false})
}

func sbFlowTestBody(t *testing.T, transfer amqpFrame) string {
	t.Helper()
	var msg amqp.Message
	if err := msg.UnmarshalBinary(transfer.payload); err != nil {
		t.Fatalf("decode transferred message: %v", err)
	}
	return string(msg.GetData())
}

// A subscription's management node sources from
// `<topic>/Subscriptions/<sub>/$management`. The Service Bus SDKs attach a
// reply link there beside every receiver and give it credit first; the
// subscription's messages go to the receiver, never to that reply link, which
// would drop them.
func TestSBAMQPDeliversASubscriptionsMessageOnlyToItsReceiver(t *testing.T) {
	newServiceBusQueueTestStores(t)
	topicID := sbAdminTopicID("ns", "t")
	sbTopics.Put(topicID, SBTopic{ID: topicID, Name: "t"})
	subID := sbAdminSubscriptionID("ns", "t", "s")
	sbSubscriptions.Put(subID, SBSubscription{ID: subID, Name: "s"})
	if reached := sbSend("ns", "t", sbOutgoing{payload: sbPayload{Body: []byte("to the subscription")}}); len(reached) != 1 || reached[0] != "t/s" {
		t.Fatalf("the topic fanned out to %v, want [t/s]", reached)
	}

	c, transport := sbFlowTestConn(t)
	receiver := sbFlowTestAttachReceiver(t, c, transport, "receiver", 0, "t/Subscriptions/s")
	reply := sbFlowTestAttachReceiver(t, c, transport, "reply", 1, "t/Subscriptions/s/$management")

	sbFlowTestFlow(t, c, 1, 0, 50, false)
	if transfers := sbFlowTestOfType(sbFlowTestFrames(t, transport), amqpDescTransfer); len(transfers) != 0 {
		t.Fatalf("credit on the management reply link (handle %d) drew %d transfers", reply, len(transfers))
	}
	sbFlowTestFlow(t, c, 0, 0, 1, false)
	transfers := sbFlowTestOfType(sbFlowTestFrames(t, transport), amqpDescTransfer)
	if len(transfers) != 1 || asUint32(field(transfers[0].fields, 0)) != receiver {
		t.Fatalf("receiver credit drew %d transfers, want 1 on handle %d: %+v", len(transfers), receiver, transfers)
	}
	if got := sbFlowTestBody(t, transfers[0]); got != "to the subscription" {
		t.Fatalf("receiver got %q", got)
	}
}

// AMQP 1.0 section 2.6.7: link-credit is relative to the receiver's
// delivery-count, so a flow restates the receiver's allowance instead of
// adding to it, and a drain returns the unused credit by advancing the
// delivery count, answered with a flow carrying drain.
func TestSBAMQPLinkCreditFollowsDeliveryCount(t *testing.T) {
	newServiceBusQueueTestStores(t)
	sbTestQueue("q", nil)
	for _, body := range []string{"one", "two", "three", "four"} {
		sbSend("ns", "q", sbOutgoing{payload: sbPayload{Body: []byte(body)}})
	}
	c, transport := sbFlowTestConn(t)
	handle := sbFlowTestAttachReceiver(t, c, transport, "receiver", 0, "q")

	var got []string
	receive := func(deliveryCount, credit uint32, drain bool) []amqpFrame {
		t.Helper()
		sbFlowTestFlow(t, c, 0, deliveryCount, credit, drain)
		frames := sbFlowTestFrames(t, transport)
		for _, transfer := range sbFlowTestOfType(frames, amqpDescTransfer) {
			if asUint32(field(transfer.fields, 0)) != handle {
				t.Fatalf("transfer on handle %d, want %d", asUint32(field(transfer.fields, 0)), handle)
			}
			got = append(got, sbFlowTestBody(t, transfer))
		}
		return sbFlowTestOfType(frames, amqpDescFlow)
	}

	receive(0, 1, false)
	receive(0, 1, false)
	if len(got) != 1 {
		t.Fatalf("two flows granting one credit from delivery-count 0 drew %v, want one message", got)
	}
	receive(1, 1, false)
	if len(got) != 2 {
		t.Fatalf("a flow from delivery-count 1 drew %v, want a second message", got)
	}
	flows := receive(2, 5, true)
	if len(got) != 4 || got[2] != "three" || got[3] != "four" {
		t.Fatalf("a draining flow for five drew %v, want the two messages left", got)
	}
	if len(flows) != 1 {
		t.Fatalf("a drain was answered with %d flow frames, want 1", len(flows))
	}
	answer := flows[0].fields
	if asUint32(field(answer, 4)) != handle || asUint32(field(answer, 5)) != 7 ||
		asUint32(field(answer, 6)) != 0 || !asBool(field(answer, 8)) {
		t.Fatalf("drain answer: handle %v delivery-count %v link-credit %v drain %v; want handle %d, 7, 0, true",
			field(answer, 4), field(answer, 5), field(answer, 6), field(answer, 8), handle)
	}
	if next := asUint32(field(answer, 2)); next != 5 {
		t.Fatalf("drain answer next-outgoing-id %d, want 5 after four transfers", next)
	}

	sbSend("ns", "q", sbOutgoing{payload: sbPayload{Body: []byte("five")}})
	receive(4, 0, false)
	if len(got) != 4 {
		t.Fatalf("a flow behind the transfers already sent drew %v", got[4:])
	}
}

func TestSBAMQPSenderCreditFromSerialArithmetic(t *testing.T) {
	for _, c := range []struct{ rcvCount, rcvCredit, sndCount, want uint32 }{
		{0, 10, 0, 10},
		{3, 2, 4, 1},
		{1, 1, 5, 0},
		{math.MaxUint32, 3, 1, 1},
		{2, 0, math.MaxUint32, 3},
	} {
		if got := sbAMQPSenderCreditFrom(c.rcvCount, c.rcvCredit, c.sndCount); got != c.want {
			t.Errorf("credit(%d + %d - %d) = %d, want %d", c.rcvCount, c.rcvCredit, c.sndCount, got, c.want)
		}
	}
}

// A client sender link starts with the credit the simulator grants and has it
// renewed once half is spent, so a long-running sender never stalls.
func TestSBAMQPRenewsASendersCredit(t *testing.T) {
	newServiceBusQueueTestStores(t)
	savedHubs := ehEventHubs
	ehEventHubs = sim.MakeStore[EHEventHub](nil, "test_eh_hubs")
	t.Cleanup(func() { ehEventHubs = savedHubs })
	sbTestQueue("q", nil)
	c, transport := sbFlowTestConn(t)
	sbFlowTestSend(t, c, amqpDescAttach, []any{"sender", uint32(0), false, uint8(2), uint8(0), encodeSource("sender"), encodeTarget("q")})
	flows := sbFlowTestOfType(sbFlowTestFrames(t, transport), amqpDescFlow)
	if len(flows) != 1 || asUint32(field(flows[0].fields, 6)) != sbAMQPSenderCredit {
		t.Fatalf("attach of a sender granted %+v, want one flow with credit %d", flows, sbAMQPSenderCredit)
	}
	payload, err := (&amqp.Message{Data: [][]byte{[]byte("m")}}).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	const sent = sbAMQPSenderCredit/2 + 1
	var renewals []amqpFrame
	for i := range uint32(sent) {
		sbFlowTestSend(t, c, amqpDescTransfer, []any{uint32(0), i, []byte{byte(i), byte(i >> 8)}, uint32(0), true}, payload...)
		renewals = append(renewals, sbFlowTestOfType(sbFlowTestFrames(t, transport), amqpDescFlow)...)
	}
	if len(renewals) != 1 {
		t.Fatalf("%d transfers drew %d credit renewals, want 1", sent, len(renewals))
	}
	const half = sbAMQPSenderCredit / 2
	renewal := renewals[0].fields
	if asUint32(field(renewal, 5)) != half || asUint32(field(renewal, 6)) != sbAMQPSenderCredit || asUint32(field(renewal, 0)) != 1+half {
		t.Fatalf("renewal delivery-count %v link-credit %v next-incoming-id %v; want %d, %d, %d",
			field(renewal, 5), field(renewal, 6), field(renewal, 0), half, sbAMQPSenderCredit, 1+half)
	}
	if total, _, _ := sbQueueCounts("ns", "q"); total != sent {
		t.Fatalf("the queue holds %d messages, want %d", total, sent)
	}
}
