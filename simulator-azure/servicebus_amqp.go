package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/Azure/go-amqp"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/msgq"
	"github.com/gorilla/websocket"
)

const (
	amqpFrameTypeAMQP = 0
	amqpFrameTypeSASL = 1

	amqpDescOpen          = 0x10
	amqpDescBegin         = 0x11
	amqpDescAttach        = 0x12
	amqpDescFlow          = 0x13
	amqpDescTransfer      = 0x14
	amqpDescDisposition   = 0x15
	amqpDescDetach        = 0x16
	amqpDescEnd           = 0x17
	amqpDescClose         = 0x18
	amqpDescSource        = 0x28
	amqpDescTarget        = 0x29
	amqpDescSASLMechanism = 0x40
	amqpDescSASLOutcome   = 0x44
	amqpDescAccepted      = 0x24
	amqpDescRejected      = 0x25
	amqpDescReleased      = 0x26
	amqpDescModified      = 0x27
	amqpDescError         = 0x1d
)

var sbAMQPUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
	Subprotocols: []string{
		"amqp",
	},
}

var sbAMQPActiveConns sync.Map

type sbAMQPConn struct {
	namespace    string
	transport    sbAMQPTransport
	nextHandle   uint32
	nextDelivery uint32
	links        map[uint64]*sbAMQPLink
	// claims holds the audiences this connection has authenticated through the
	// CBS put-token handshake. An entity link may only be attached once a
	// claim covering it has been granted, exactly as the real services require.
	claims []string
	// deliveries maps the delivery id of each unsettled peek-lock transfer to
	// the message lock it carries, for the receiver's disposition.
	deliveries map[uint32]sbAMQPDelivery
	// sessions holds the transfer-id bookkeeping of each begun session, by
	// channel, that every flow frame reports.
	sessions map[uint16]*sbAMQPSession
	// done closes when the connection ends.
	done    chan struct{}
	mu      sync.Mutex
	writeMu sync.Mutex
}

// sbAMQPSession is the session state AMQP 1.0 section 2.5.6 has each endpoint
// report in its flow frames. Guarded by sbAMQPConn.mu.
type sbAMQPSession struct {
	nextIncomingID uint32
	nextOutgoingID uint32
}

// sbAMQPIncomingWindow and sbAMQPOutgoingWindow are the session windows the
// simulator advertises in begin and flow frames.
const (
	sbAMQPIncomingWindow = 5000
	sbAMQPOutgoingWindow = 1000
	// sbAMQPSenderCredit is the link credit granted to a client's sender link;
	// the grant is renewed once half of it is spent.
	sbAMQPSenderCredit = 1000
)

type sbAMQPDelivery struct {
	path      string
	lockToken string
}

type sbAMQPTransport interface {
	Read(context.Context) ([]byte, error)
	Write([]byte) error
	Close() error
}

type sbAMQPLink struct {
	name         string
	address      string
	channel      uint16
	clientHandle uint32
	serverHandle uint32
	clientRole   bool
	settledSend  bool
	// credit and deliveryCount are the link's flow state (AMQP 1.0 section
	// 2.6.7). On a link the simulator sends on, deliveryCount counts the
	// transfers it sent and credit is what the receiver still allows; on a link
	// it receives on, deliveryCount counts the transfers it received and credit
	// is what it last granted.
	credit        uint32
	deliveryCount uint32
	// drain records that the receiver asked for its unused credit back once
	// no more messages are available.
	drain bool
	// session is the message session a session receiver holds the lock of;
	// awaitingSession marks a session receiver still waiting to accept one,
	// which is handed nothing. owner names the link as a session lock holder,
	// and detached closes when the link ends.
	session         string
	awaitingSession bool
	owner           string
	detached        chan struct{}
	// readSeq is the next sequence number an Event Hubs consumer link reads.
	readSeq int64
}

type amqpFrame struct {
	frameType byte
	channel   uint16
	desc      uint64
	fields    []any
	payload   []byte
}

type amqpDescribed struct {
	code  uint64
	value any
}

type amqpSymbol string

func handleSBAMQPWebSocket(w http.ResponseWriter, r *http.Request, namespace string) {
	conn, err := sbAMQPUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newSBAMQPConn(namespace, sbAMQPWebSocketTransport{conn: conn})
	c.serve(r.Context())
}

func startSBAMQPTLSListener(ctx context.Context, listenAddr, certFile, keyFile string) (net.Listener, error) {
	if listenAddr == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("service bus raw AMQP listener %s requires TLS cert and key", listenAddr)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load Service Bus AMQP TLS certificate: %w", err)
	}
	ln, err := tls.Listen("tcp", listenAddr, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	})
	if err != nil {
		return nil, fmt.Errorf("listen for Service Bus raw AMQP on %s: %w", listenAddr, err)
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSBAMQPTLSConn(ctx, conn)
		}
	}()
	return ln, nil
}

func serveSBAMQPTLSConn(ctx context.Context, conn net.Conn) {
	namespace := ""
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return
		}
		namespace = sbAMQPNamespaceFromHost(tlsConn.ConnectionState().ServerName)
	}
	c := newSBAMQPConn(namespace, newSBAMQPRawTransport(conn))
	c.serve(ctx)
}

func newSBAMQPConn(namespace string, transport sbAMQPTransport) *sbAMQPConn {
	return &sbAMQPConn{
		namespace:    namespace,
		transport:    transport,
		nextDelivery: 1,
		links:        map[uint64]*sbAMQPLink{},
		deliveries:   map[uint32]sbAMQPDelivery{},
		sessions:     map[uint16]*sbAMQPSession{},
		done:         make(chan struct{}),
	}
}

// sessionLocked returns the state of the session on channel. The caller holds
// c.mu.
func (c *sbAMQPConn) sessionLocked(channel uint16) *sbAMQPSession {
	s := c.sessions[channel]
	if s == nil {
		s = &sbAMQPSession{nextOutgoingID: 1}
		c.sessions[channel] = s
	}
	return s
}

// linkFlowLocked encodes the flow frame reporting a link's state. The caller
// holds c.mu.
func (c *sbAMQPConn) linkFlowLocked(link *sbAMQPLink, drain bool) []byte {
	s := c.sessionLocked(link.channel)
	return encodeDescribedList(amqpDescFlow, []any{
		s.nextIncomingID,
		uint32(sbAMQPIncomingWindow),
		s.nextOutgoingID,
		uint32(sbAMQPOutgoingWindow),
		link.serverHandle,
		link.deliveryCount,
		link.credit,
		uint32(0),
		drain,
	})
}

func (c *sbAMQPConn) setNamespace(namespace string) {
	c.mu.Lock()
	c.namespace = namespace
	c.mu.Unlock()
}

func (c *sbAMQPConn) currentNamespace() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.namespace
}

// grantClaim records an audience this connection authenticated for.
func (c *sbAMQPConn) grantClaim(audience string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, existing := range c.claims {
		if existing == audience {
			return
		}
	}
	c.claims = append(c.claims, audience)
}

// hasClaimFor reports whether a granted claim authorizes an entity path.
func (c *sbAMQPConn) hasClaimFor(entityPath string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, audience := range c.claims {
		if sasAudienceCoversEntity(audience, entityPath) {
			return true
		}
	}
	return false
}

func cloneSBAMQPLink(link *sbAMQPLink) *sbAMQPLink {
	if link == nil {
		return nil
	}
	out := *link
	return &out
}

func sbAMQPDeliverAvailableMessages(namespace string, paths []string) error {
	var firstErr error
	sbAMQPActiveConns.Range(func(key, _ any) bool {
		conn, ok := key.(*sbAMQPConn)
		if !ok || conn.currentNamespace() != namespace {
			return true
		}
		if err := conn.deliverAvailableMessages(paths); err != nil && firstErr == nil {
			firstErr = err
		}
		return true
	})
	return firstErr
}

func (c *sbAMQPConn) serve(ctx context.Context) {
	sbAMQPActiveConns.Store(c, struct{}{})
	defer func() {
		sbAMQPActiveConns.Delete(c)
		_ = c.transport.Close()
		c.mu.Lock()
		ended := make([]*sbAMQPLink, 0, len(c.links))
		for key, link := range c.links {
			ended = append(ended, link)
			delete(c.links, key)
		}
		c.mu.Unlock()
		c.endLinks(ended)
		close(c.done)
	}()
	// AMQP is a byte stream: a read can end inside a protocol header or a
	// frame, so bytes carry over until the rest arrives.
	var pending []byte
	for {
		data, err := c.transport.Read(ctx)
		if err != nil {
			return
		}
		pending = append(pending, data...)
		consumed, err := c.handleFrames(ctx, pending)
		if err != nil {
			return
		}
		pending = append(pending[:0], pending[consumed:]...)
	}
}

// handleFrames handles every complete protocol header and frame at the start
// of data and returns how many bytes they took.
func (c *sbAMQPConn) handleFrames(ctx context.Context, data []byte) (int, error) {
	consumed := 0
	for len(data)-consumed >= 8 {
		rest := data[consumed:]
		if bytes.Equal(rest[:4], []byte{'A', 'M', 'Q', 'P'}) {
			if err := c.handleProto(rest[:8]); err != nil {
				return consumed, err
			}
			consumed += 8
			continue
		}
		size := int(binary.BigEndian.Uint32(rest[:4]))
		if size < 8 {
			return consumed, fmt.Errorf("AMQP frame declares size %d, below the 8-byte frame header", size)
		}
		if size > len(rest) {
			break
		}
		frame, err := parseAMQPFrame(rest[:size])
		if err != nil {
			return consumed, err
		}
		if err := c.handleFrame(ctx, frame); err != nil {
			return consumed, err
		}
		consumed += size
	}
	return consumed, nil
}

func (c *sbAMQPConn) handleProto(header []byte) error {
	switch header[4] {
	case 3:
		if err := c.writeBytes([]byte{'A', 'M', 'Q', 'P', 3, 1, 0, 0}); err != nil {
			return err
		}
		return c.writeFrame(amqpFrameTypeSASL, 0, encodeDescribedList(amqpDescSASLMechanism, []any{amqpSymbol("ANONYMOUS")}))
	case 0:
		return c.writeBytes([]byte{'A', 'M', 'Q', 'P', 0, 1, 0, 0})
	default:
		return fmt.Errorf("unsupported AMQP protocol id %d", header[4])
	}
}

func (c *sbAMQPConn) handleFrame(ctx context.Context, frame amqpFrame) error {
	switch frame.desc {
	case 0x41: // sasl-init
		return c.writeFrame(amqpFrameTypeSASL, 0, encodeDescribedList(amqpDescSASLOutcome, []any{uint8(0)}))
	case amqpDescOpen:
		if namespace := sbAMQPNamespaceFromHost(asString(field(frame.fields, 1))); namespace != "" {
			c.setNamespace(namespace)
		}
		return c.writeFrame(amqpFrameTypeAMQP, 0, encodeDescribedList(amqpDescOpen, []any{
			"sockerless-servicebus",
			nil,
			uint32(math.MaxUint32),
			uint16(math.MaxUint16),
			uint32((time.Minute / time.Millisecond) / 2),
		}))
	case amqpDescBegin:
		c.mu.Lock()
		s := &sbAMQPSession{nextIncomingID: asUint32(field(frame.fields, 1)), nextOutgoingID: 1}
		c.sessions[frame.channel] = s
		c.mu.Unlock()
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescBegin, []any{
			frame.channel,
			uint32(1),
			uint32(sbAMQPIncomingWindow),
			uint32(sbAMQPOutgoingWindow),
			uint32(math.MaxInt16),
		}))
	case amqpDescAttach:
		return c.handleAttach(frame)
	case amqpDescFlow:
		return c.handleFlow(ctx, frame)
	case amqpDescTransfer:
		c.mu.Lock()
		c.sessionLocked(frame.channel).nextIncomingID++
		c.mu.Unlock()
		if err := c.grantSenderCredit(frame.channel, asUint32(field(frame.fields, 0))); err != nil {
			return err
		}
		return c.handleTransfer(ctx, frame)
	case amqpDescDisposition:
		return c.handleDisposition(frame)
	case amqpDescDetach:
		handle := asUint32(field(frame.fields, 0))
		c.mu.Lock()
		var ended []*sbAMQPLink
		link := c.links[sbAMQPLinkKey(frame.channel, handle)]
		if link == nil {
			// The link was refused, and the refusal already detached it.
			c.mu.Unlock()
			return nil
		}
		ended = append(ended, link)
		delete(c.links, sbAMQPLinkKey(frame.channel, handle))
		c.mu.Unlock()
		c.endLinks(ended)
		// The detach names the link by the handle this end assigned it.
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDetach, []any{link.serverHandle, true}))
	case amqpDescEnd:
		// Ending a session ends its links (AMQP 1.0 section 2.5.4): a link
		// left behind would be handed messages its receiver can never see.
		c.mu.Lock()
		delete(c.sessions, frame.channel)
		var ended []*sbAMQPLink
		for key, link := range c.links {
			if link.channel == frame.channel {
				ended = append(ended, link)
				delete(c.links, key)
			}
		}
		c.mu.Unlock()
		c.endLinks(ended)
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescEnd, nil))
	case amqpDescClose:
		return c.writeFrame(amqpFrameTypeAMQP, 0, encodeDescribedList(amqpDescClose, nil))
	default:
		return nil
	}
}

func (c *sbAMQPConn) handleAttach(frame amqpFrame) error {
	name := asString(field(frame.fields, 0))
	clientHandle := asUint32(field(frame.fields, 1))
	clientRole := asBool(field(frame.fields, 2))
	sourceAddress := describedAddress(field(frame.fields, 5))
	targetAddress := describedAddress(field(frame.fields, 6))
	var address string
	if !clientRole {
		address = targetAddress
	} else {
		address = sourceAddress
		if address == "" {
			address = targetAddress
		}
	}
	if address == "" || address == "test" {
		address = name
	}
	entityAddress := strings.Trim(address, "/")
	// The CBS and management endpoints carry the handshake itself and are
	// reachable before any claim exists; every entity link requires one.
	if entityAddress != "$cbs" && entityAddress != "$management" &&
		!c.hasClaimFor(entityAddress) {
		return c.refuseAttach(frame, name, clientRole, entityAddress)
	}
	c.mu.Lock()
	serverHandle := c.nextHandle
	c.nextHandle++
	link := &sbAMQPLink{
		name:         name,
		address:      strings.Trim(address, "/"),
		channel:      frame.channel,
		clientHandle: clientHandle,
		serverHandle: serverHandle,
		clientRole:   clientRole,
		settledSend:  clientRole && asUint8(field(frame.fields, 3)) == 1,
	}
	if clientRole && ehAMQPIsReceiverAddress(c.namespace, link.address) {
		link.readSeq = ehStartPosition(c.namespace, link.address, sbAMQPSelectorFilter(field(frame.fields, 5)))
	}
	namespace := c.namespace
	c.mu.Unlock()

	requested, sessionful := sbAMQPSessionFilter(field(frame.fields, 5))
	if clientRole {
		if path, entity := sbAMQPReceiverPath(link.address); entity {
			if requires := sbSettings(namespace, path).requiresSession; requires != sessionful {
				description := errSBSessionfulEntity.Error()
				if !requires {
					description = "It is not possible for an entity that does not require sessions to create a sessionful message receiver."
				}
				return c.refuseAttachWith(frame, name, clientRole, "amqp:not-allowed", description)
			}
		}
	}
	c.mu.Lock()
	c.links[sbAMQPLinkKey(frame.channel, clientHandle)] = link
	if clientRole && sessionful {
		link.awaitingSession = true
		link.owner = fmt.Sprintf("%p/%d", c, serverHandle)
		link.detached = make(chan struct{})
	}
	c.mu.Unlock()

	if clientRole && sessionful {
		bg.Go(func() { c.acceptSession(frame, link, requested, sbAMQPSessionTimeout(field(frame.fields, 13))) })
		return nil
	}
	if clientRole {
		// Echo the sender settle mode the receiver asked for: settled is
		// receive-and-delete, anything else peek-lock. AMQP's default is mixed.
		sndSettleMode := uint8(2)
		if field(frame.fields, 3) != nil {
			sndSettleMode = asUint8(field(frame.fields, 3))
		}
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, []any{
			name,
			serverHandle,
			false,
			sndSettleMode,
			field(frame.fields, 4),
			encodeSource(sourceAddress),
			nil,
			nil,
			nil,
			uint32(0),
			uint64(math.MaxUint32),
		}))
	}
	if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, []any{
		name,
		serverHandle,
		true,
		uint8(2),
		field(frame.fields, 4),
		nil,
		encodeTarget(link.address),
		nil,
		nil,
		nil,
		uint64(math.MaxUint32),
	})); err != nil {
		return err
	}
	c.mu.Lock()
	link.deliveryCount = asUint32(field(frame.fields, 9))
	link.credit = sbAMQPSenderCredit
	flow := c.linkFlowLocked(link, false)
	c.mu.Unlock()
	return c.writeFrame(amqpFrameTypeAMQP, frame.channel, flow)
}

// grantSenderCredit renews a client sender link's credit once half of it is
// spent, so a sender never runs dry however many messages it sends.
func (c *sbAMQPConn) grantSenderCredit(channel uint16, handle uint32) error {
	c.mu.Lock()
	link := c.links[sbAMQPLinkKey(channel, handle)]
	if link == nil || link.clientRole {
		c.mu.Unlock()
		return nil
	}
	link.deliveryCount++
	if link.credit > 0 {
		link.credit--
	}
	if link.credit > sbAMQPSenderCredit/2 {
		c.mu.Unlock()
		return nil
	}
	link.credit = sbAMQPSenderCredit
	flow := c.linkFlowLocked(link, false)
	c.mu.Unlock()
	return c.writeFrame(amqpFrameTypeAMQP, channel, flow)
}

// refuseAttach answers an attach for an entity the connection has not
// authenticated for. Real Service Bus and Event Hubs complete the attach
// handshake and immediately detach the link with the amqp:unauthorized-access
// error condition, which is what the AMQP clients surface as an auth failure.
func (c *sbAMQPConn) refuseAttach(frame amqpFrame, name string, clientRole bool, address string) error {
	return c.refuseAttachWith(frame, name, clientRole, errSASInvalidSignature.Condition,
		fmt.Sprintf("Unauthorized access. %q claim(s) are required to perform this operation.", address))
}

// refuseAttachWith completes an attach and detaches the link at once with an
// error, the way the services refuse a link.
func (c *sbAMQPConn) refuseAttachWith(frame amqpFrame, name string, clientRole bool, condition, description string) error {
	c.mu.Lock()
	serverHandle := c.nextHandle
	c.nextHandle++
	c.mu.Unlock()

	attachFields := []any{
		name,
		serverHandle,
		!clientRole,
		uint8(1),
		field(frame.fields, 4),
		nil,
		nil,
		nil,
		nil,
		nil,
		uint64(math.MaxUint32),
	}
	// A refused link's attach carries no terminus (AMQP 1.0 section 2.6.3),
	// which tells the client to wait for the detach that says why.
	if !clientRole {
		attachFields[3] = uint8(2)
	}
	if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, attachFields)); err != nil {
		return err
	}
	return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDetach, []any{
		serverHandle,
		true,
		amqpDescribed{code: amqpDescError, value: []any{amqpSymbol(condition), description}},
	}))
}

func (c *sbAMQPConn) handleTransfer(ctx context.Context, frame amqpFrame) error {
	handle := asUint32(field(frame.fields, 0))
	deliveryID := asUint32(field(frame.fields, 1))
	c.mu.Lock()
	link := c.links[sbAMQPLinkKey(frame.channel, handle)]
	if link == nil {
		c.mu.Unlock()
		return nil
	}
	link = cloneSBAMQPLink(link)
	c.mu.Unlock()
	var msg amqp.Message
	if err := msg.UnmarshalBinary(frame.payload); err != nil {
		return err
	}
	if link.address == "$cbs" || link.address == "$management" || sbAMQPIsManagementAddress(link.address) {
		if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
			true,
			deliveryID,
			nil,
			true,
			amqpDescribed{code: amqpDescAccepted, value: []any{}},
		})); err != nil {
			return err
		}
		return c.respondRPC(frame.channel, link.address, &msg)
	}
	namespace := c.currentNamespace()
	if ehAMQPIsSenderAddress(namespace, link.address) {
		ehAMQPEnqueue(namespace, link.address, &msg)
		if err := sbAMQPDeliverEventHubEvents(namespace); err != nil {
			return err
		}
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
			true,
			deliveryID,
			nil,
			true,
			amqpDescribed{code: amqpDescAccepted, value: []any{}},
		}))
	}
	messages := []*amqp.Message{&msg}
	raws := [][]byte{frame.payload}
	if asUint32(field(frame.fields, 3)) == sbAMQPBatchFormat {
		messages, raws = nil, nil
		for _, data := range msg.Data {
			var one amqp.Message
			if err := one.UnmarshalBinary(data); err != nil {
				return err
			}
			messages = append(messages, &one)
			raws = append(raws, data)
		}
	}
	path := sbAMQPEntityPath(link.address)
	outs := make([]sbOutgoing, len(messages))
	for i, m := range messages {
		outs[i] = sbOutgoingFromAMQP(m, raws[i])
	}
	if err := sbRequireSessionIDs(namespace, path, outs); err != nil {
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
			true,
			deliveryID,
			nil,
			true,
			amqpDescribed{code: amqpDescRejected, value: []any{
				amqpDescribed{code: amqpDescError, value: []any{amqpSymbol("amqp:not-allowed"), err.Error()}},
			}},
		}))
	}
	var reached []string
	for _, out := range outs {
		reached = append(reached, sbSend(namespace, path, out)...)
	}
	if err := sbAMQPDeliverAvailableMessages(namespace, reached); err != nil {
		return err
	}
	return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
		true,
		deliveryID,
		nil,
		true,
		amqpDescribed{code: amqpDescAccepted, value: []any{}},
	}))
}

// sbAMQPBatchFormat is the message-format of a transfer whose data sections
// are each an encoded message, as a batch send produces.
const sbAMQPBatchFormat = 0x80013700

// sbOutgoingFromAMQP reads what the broker acts on from an AMQP message:
// its id, time to live and scheduled enqueue time.
func sbOutgoingFromAMQP(msg *amqp.Message, raw []byte) sbOutgoing {
	out := sbOutgoing{payload: sbPayload{Body: msg.GetData(), AMQP: append([]byte(nil), raw...)}}
	if msg.Properties != nil {
		if id, ok := msg.Properties.MessageID.(string); ok {
			out.messageID = id
		}
		if msg.Properties.ContentType != nil {
			out.payload.ContentType = *msg.Properties.ContentType
		}
		if msg.Properties.GroupID != nil {
			out.sessionID = *msg.Properties.GroupID
		}
	}
	if msg.Header != nil && msg.Header.TTL > 0 {
		out.ttl = msg.Header.TTL
	}
	if at, ok := msg.Annotations["x-opt-scheduled-enqueue-time"].(time.Time); ok {
		out.delay = time.Until(at)
	}
	return out
}

// sbAMQPPutTokenOutcome verifies a CBS put-token request against the
// addressed namespace's authorization rules. On success the audience is
// recorded as a claim on the connection; on failure the caller answers with
// the refusal the real services return.
func (c *sbAMQPConn) sbAMQPPutTokenOutcome(req *amqp.Message) (statusCode int32, description string) {
	token, _ := req.Value.(string)
	audience, err := verifyMessagingSAS(c.currentNamespace(), token)
	if err != nil {
		authErr, ok := err.(*sasAuthError)
		if !ok {
			authErr = errSASInvalidSignature
		}
		return 401, authErr.Condition + ": " + authErr.Description
	}
	c.grantClaim(audience)
	return 202, "Accepted"
}

// sbAMQPManagementEntity returns the entity a management RPC addresses, when
// the request names one. Event Hubs metadata reads carry the hub in the `name`
// application property; Service Bus operations such as peek and renew-lock are
// scoped by the entity management link they arrive on, whose attach already
// required a claim, so those report no entity and are authorized by holding
// any claim on the connection.
func sbAMQPManagementEntity(req *amqp.Message) (entity string, named bool) {
	if req.ApplicationProperties == nil {
		return "", false
	}
	if name, ok := req.ApplicationProperties["name"].(string); ok && name != "" {
		return name, true
	}
	return "", false
}

// authorizedForManagement reports whether the connection may run a management
// operation: a claim covering the named entity, or — for a link-scoped
// operation — any claim at all.
func (c *sbAMQPConn) authorizedForManagement(req *amqp.Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entity, named := sbAMQPManagementEntity(req)
	if !named {
		return len(c.claims) > 0
	}
	for _, audience := range c.claims {
		if sasAudienceCoversManagement(audience, entity) {
			return true
		}
	}
	return false
}

func (c *sbAMQPConn) respondRPC(channel uint16, address string, req *amqp.Message) error {
	replyTo := ""
	corr := any(nil)
	if req.Properties != nil {
		if req.Properties.ReplyTo != nil {
			replyTo = *req.Properties.ReplyTo
		}
		corr = req.Properties.MessageID
	}
	link := c.receiverForAddressOnChannel(address, channel)
	if link == nil {
		link = c.receiverForAddressOnChannel(replyTo, channel)
	}
	if link == nil {
		link = c.receiverForAddressOnChannel("$cbs", channel)
	}
	if link == nil {
		link = c.receiverForAddressOnChannel("$management", channel)
	}
	if link == nil {
		link = c.receiverForAddress(replyTo)
	}
	if link == nil {
		link = c.receiverForAddress("$cbs")
	}
	if link == nil {
		link = c.receiverForAddress("$management")
	}
	if link == nil {
		return nil
	}
	// CBS put-token is the services' authentication handshake: verify the
	// Shared Access Signature before anything else on this connection is
	// allowed to address an entity.
	if req.ApplicationProperties != nil && fmt.Sprint(req.ApplicationProperties["operation"]) == "put-token" {
		statusCode, description := c.sbAMQPPutTokenOutcome(req)
		resp := &amqp.Message{
			Properties: &amqp.MessageProperties{CorrelationID: corr},
			ApplicationProperties: map[string]any{
				"status-code":        statusCode,
				"status-description": description,
			},
		}
		body, err := resp.MarshalBinary()
		if err != nil {
			return err
		}
		return c.writeReply(link, body)
	}
	// Every other management operation addresses an entity and therefore
	// requires a claim, even when it arrives over the $cbs link.
	if !c.authorizedForManagement(req) {
		resp := &amqp.Message{
			Properties: &amqp.MessageProperties{CorrelationID: corr},
			ApplicationProperties: map[string]any{
				"status-code": int32(401),
				"status-description": errSASInvalidSignature.Condition +
					": Unauthorized access. A claim is required to perform this operation.",
			},
		}
		body, err := resp.MarshalBinary()
		if err != nil {
			return err
		}
		return c.writeReply(link, body)
	}
	resp, ok := ehAMQPHandleRPC(c.currentNamespace(), req)
	if !ok && sbAMQPIsManagementAddress(address) {
		resp = sbAMQPHandleRPC(c.currentNamespace(), sbAMQPEntityPath(strings.TrimSuffix(address, "/$management")), req)
		ok = true
	}
	if !ok {
		resp = sbAMQPRPCStatus(req, 501, fmt.Sprintf("The operation %v is not supported.", req.ApplicationProperties["operation"]))
	}
	body, err := resp.MarshalBinary()
	if err != nil {
		return err
	}
	return c.writeReply(link, body)
}

// handleFlow applies a receiver's flow frame to the link the simulator sends
// on. AMQP 1.0 section 2.6.7 defines link-credit relative to the receiver's
// delivery-count, so the credit left is the receiver's delivery-count plus its
// link-credit minus the transfers already sent; a receiver that re-issues a
// flow restates its allowance rather than adding to it.
func (c *sbAMQPConn) handleFlow(_ context.Context, frame amqpFrame) error {
	handlePtr := field(frame.fields, 4)
	if handlePtr == nil {
		return nil
	}
	handle := asUint32(handlePtr)
	namespace := c.currentNamespace()
	c.mu.Lock()
	link := c.links[sbAMQPLinkKey(frame.channel, handle)]
	if link == nil || !link.clientRole {
		c.mu.Unlock()
		return nil
	}
	if credit := field(frame.fields, 6); credit != nil {
		receiverCount := link.deliveryCount
		if count := field(frame.fields, 5); count != nil {
			receiverCount = asUint32(count)
		}
		link.credit = sbAMQPSenderCreditFrom(receiverCount, asUint32(credit), link.deliveryCount)
	}
	link.drain = asBool(field(frame.fields, 8))
	echo := asBool(field(frame.fields, 9))
	if ehAMQPIsReceiverAddress(namespace, link.address) {
		err := c.deliverEventHubEventsLocked(namespace, link)
		c.mu.Unlock()
		if err != nil {
			return err
		}
	} else {
		path, entity := sbAMQPReceiverPath(link.address)
		c.mu.Unlock()
		if entity {
			if err := c.deliverAvailableMessages([]string{path}); err != nil {
				return err
			}
		}
	}
	return c.completeFlow(frame.channel, handle, echo)
}

// sbAMQPSenderCreditFrom computes a sender's link-credit from a receiver's
// flow. Delivery counts are RFC 1982 serial numbers, so the difference is
// taken modulo 2^32; a receiver whose flow crossed transfers still in flight
// can name a delivery-count behind the sender's, and is then owed nothing.
func sbAMQPSenderCreditFrom(receiverCount, receiverCredit, senderCount uint32) uint32 {
	if remaining := int32(receiverCount + receiverCredit - senderCount); remaining > 0 {
		return uint32(remaining)
	}
	return 0
}

// completeFlow answers a flow once the deliveries it made possible are sent:
// a drain gives the unused credit back by advancing the delivery count, and
// both a drain and an echo request are answered with the link's state.
func (c *sbAMQPConn) completeFlow(channel uint16, handle uint32, echo bool) error {
	c.mu.Lock()
	link := c.links[sbAMQPLinkKey(channel, handle)]
	if link == nil || (!link.drain && !echo) {
		c.mu.Unlock()
		return nil
	}
	drained := link.drain
	if drained {
		link.deliveryCount += link.credit
		link.credit = 0
		link.drain = false
	}
	flow := c.linkFlowLocked(link, drained)
	c.mu.Unlock()
	return c.writeFrame(amqpFrameTypeAMQP, channel, flow)
}

// deliverEventHubEventsLocked sends an Event Hubs consumer link the events
// its credit covers. The caller holds c.mu.
func (c *sbAMQPConn) deliverEventHubEventsLocked(namespace string, link *sbAMQPLink) error {
	for link.credit > 0 {
		msg, next, ok := ehAMQPNextEvent(namespace, link.address, link.readSeq)
		if !ok {
			return nil
		}
		link.readSeq = next
		if _, err := c.writeTransferLocked(link, msg, link.settledSend, nil); err != nil {
			return err
		}
	}
	return nil
}

// sbAMQPDeliverEventHubEvents pushes newly published events to the Event Hubs
// consumer links of the namespace that still hold credit.
func sbAMQPDeliverEventHubEvents(namespace string) error {
	var firstErr error
	sbAMQPActiveConns.Range(func(key, _ any) bool {
		conn, ok := key.(*sbAMQPConn)
		if !ok || conn.currentNamespace() != namespace {
			return true
		}
		conn.mu.Lock()
		for _, link := range conn.links {
			if link.clientRole && link.credit > 0 && ehAMQPIsReceiverAddress(namespace, link.address) {
				if err := conn.deliverEventHubEventsLocked(namespace, link); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
		conn.mu.Unlock()
		return true
	})
	return firstErr
}

func (c *sbAMQPConn) deliverAvailableMessages(paths []string) error {
	pathSet := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		pathSet[path] = struct{}{}
	}
	namespace := c.currentNamespace()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, link := range c.links {
		if !link.clientRole || link.credit == 0 || link.awaitingSession {
			continue
		}
		path, entity := sbAMQPReceiverPath(link.address)
		if !entity {
			continue
		}
		if _, ok := pathSet[path]; !ok {
			continue
		}
		if link.session != "" && !sbSessionHeld(namespace, path, link.session, link.owner) {
			continue
		}
		// A receiver whose transfers arrive settled is receive-and-delete;
		// otherwise every message is delivered under a peek-lock.
		peekLock := !link.settledSend
		msgs, _ := sbReceive(namespace, path, link.session, int(link.credit), peekLock)
		for _, m := range msgs {
			body, err := sbAMQPMessage(m, peekLock).MarshalBinary()
			if err != nil {
				return err
			}
			var tag []byte
			if peekLock {
				tag = sbLockTokenTag(m.Receipt)
			}
			id, err := c.writeTransferLocked(link, body, link.settledSend, tag)
			if err != nil {
				return err
			}
			if peekLock {
				c.deliveries[id] = sbAMQPDelivery{path: path, lockToken: m.Receipt}
			}
		}
	}
	return nil
}

// sbAMQPMessage renders a stored message for an AMQP receiver: the message
// as its sender sent it, with the broker's annotations over it.
func sbAMQPMessage(m msgq.Message[sbPayload], peekLock bool) *amqp.Message {
	out := &amqp.Message{}
	if len(m.Payload.AMQP) > 0 {
		if err := out.UnmarshalBinary(m.Payload.AMQP); err != nil {
			out = &amqp.Message{}
		}
	}
	if len(out.Data) == 0 && out.Value == nil && out.Sequence == nil {
		out.Data = [][]byte{m.Payload.Body}
	}
	if out.Properties == nil {
		out.Properties = &amqp.MessageProperties{}
	}
	out.Properties.MessageID = m.ID
	if m.Group != "" && out.Properties.GroupID == nil {
		group := m.Group
		out.Properties.GroupID = &group
	}
	if out.Header == nil {
		out.Header = &amqp.MessageHeader{}
	}
	// AMQP counts the failed deliveries before this one.
	if m.Deliveries > 0 {
		out.Header.DeliveryCount = uint32(m.Deliveries - 1)
	}
	if out.Annotations == nil {
		out.Annotations = amqp.Annotations{}
	}
	out.Annotations["x-opt-sequence-number"] = int64(m.Seq)
	out.Annotations["x-opt-enqueued-time"] = time.UnixMilli(m.EnqueuedAt).UTC()
	if peekLock {
		out.Annotations["x-opt-locked-until"] = time.UnixMilli(m.AvailableAt).UTC()
	}
	if m.Payload.DeadLetterSource != "" {
		out.Annotations["x-opt-deadletter-source"] = m.Payload.DeadLetterSource
	}
	if m.Payload.Deferred {
		out.Annotations["x-opt-message-state"] = int32(1)
	}
	if len(m.Payload.Properties) > 0 || m.Payload.DeadLetterReason != "" {
		if out.ApplicationProperties == nil {
			out.ApplicationProperties = map[string]any{}
		}
		for k, v := range m.Payload.Properties {
			out.ApplicationProperties[k] = v
		}
		if m.Payload.DeadLetterReason != "" {
			out.ApplicationProperties["DeadLetterReason"] = m.Payload.DeadLetterReason
			out.ApplicationProperties["DeadLetterErrorDescription"] = m.Payload.DeadLetterErrorDescription
		}
	}
	return out
}

// handleDisposition settles the peek-locked messages a receiver's
// disposition names, and confirms each outcome the way Service Bus does when
// the receiver settles second.
func (c *sbAMQPConn) handleDisposition(frame amqpFrame) error {
	if !asBool(field(frame.fields, 0)) {
		return nil
	}
	first := asUint32(field(frame.fields, 1))
	last := first
	if field(frame.fields, 2) != nil {
		last = asUint32(field(frame.fields, 2))
	}
	receiverSettled := asBool(field(frame.fields, 3))
	settlement, ok := sbSettlementFromState(field(frame.fields, 4))
	namespace := c.currentNamespace()
	for id := first; ; id++ {
		c.mu.Lock()
		d, tracked := c.deliveries[id]
		delete(c.deliveries, id)
		c.mu.Unlock()
		outcome := field(frame.fields, 4)
		if tracked && ok {
			if err := sbSettle(namespace, d.path, d.lockToken, settlement); err != nil {
				outcome = amqpDescribed{code: amqpDescRejected, value: []any{
					amqpDescribed{code: amqpDescError, value: []any{amqpSymbol("com.microsoft:message-lock-lost"), err.Error()}},
				}}
			}
		}
		if !receiverSettled {
			if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
				false, id, nil, true, outcome,
			})); err != nil {
				return err
			}
		}
		if id == last {
			return nil
		}
	}
}

// sbSettlementFromState maps an AMQP delivery outcome to the Service Bus
// settlement it stands for: accepted completes, released returns the message
// uncounted, modified abandons it (or defers it when undeliverable-here), and
// rejected dead-letters it.
func sbSettlementFromState(state any) (sbSettlement, bool) {
	d, ok := state.(amqpDescribed)
	if !ok {
		return sbSettlement{}, false
	}
	fields, _ := d.value.([]any)
	switch d.code {
	case amqpDescAccepted:
		return sbSettlement{kind: sbComplete}, true
	case amqpDescReleased:
		return sbSettlement{kind: sbRelease}, true
	case amqpDescModified:
		s := sbSettlement{kind: sbAbandon, properties: sbAMQPStringMap(field(fields, 2))}
		if asBool(field(fields, 1)) {
			s.kind = sbDefer
		}
		return s, true
	case amqpDescRejected:
		s := sbSettlement{kind: sbDeadLetterIt}
		if e, ok := field(fields, 0).(amqpDescribed); ok {
			errFields, _ := e.value.([]any)
			info := sbAMQPStringMap(field(errFields, 2))
			if v, ok := info["DeadLetterReason"].(string); ok {
				s.deadLetterReason = v
			}
			if v, ok := info["DeadLetterErrorDescription"].(string); ok {
				s.deadLetterErrorDescription = v
			}
			delete(info, "DeadLetterReason")
			delete(info, "DeadLetterErrorDescription")
			s.properties = info
		}
		return s, true
	}
	return sbSettlement{}, false
}

func sbAMQPStringMap(v any) map[string]any {
	m, _ := v.(map[any]any)
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		switch key := k.(type) {
		case string:
			out[key] = val
		case amqpSymbol:
			out[string(key)] = val
		}
	}
	return out
}

func sbAMQPEntityPath(address string) string {
	address = strings.Trim(strings.TrimSpace(address), "/")
	if address == "" {
		return ""
	}
	parts := strings.Split(address, "/")
	if len(parts) >= 3 && strings.EqualFold(parts[1], "subscriptions") {
		path := parts[0] + "/" + parts[2]
		switch {
		case len(parts) == 3:
			return path
		case len(parts) == 4 && strings.EqualFold(parts[3], sbDeadLetterSuffix):
			return sbDeadLetterPath(path)
		}
		return address
	}
	if len(parts) == 2 && strings.EqualFold(parts[1], sbDeadLetterSuffix) {
		return sbDeadLetterPath(parts[0])
	}
	return address
}

// sbAMQPReceiverPath returns the message store a receiver link reads, and
// false for a link that reads no entity: the CBS node, a management node, or
// any other address that names no queue, subscription or dead-letter queue. A
// management node's reply link sources from `<entity>/$management`; handing
// it an entity's message loses the message.
func sbAMQPReceiverPath(address string) (string, bool) {
	address = strings.Trim(address, "/")
	if address == "" || address == "$cbs" || strings.EqualFold(address, "$management") || sbAMQPIsManagementAddress(address) {
		return "", false
	}
	path := sbAMQPEntityPath(address)
	return path, !strings.Contains(path, "$") || sbIsDeadLetterPath(path)
}

// sbAMQPSelectorFilter returns the selector-filter expression of an attach's
// source terminus, empty when it sets none.
func sbAMQPSelectorFilter(source any) string {
	d, ok := source.(amqpDescribed)
	if !ok {
		return ""
	}
	fields, _ := d.value.([]any)
	filters, _ := field(fields, 7).(map[any]any)
	for _, v := range filters {
		if f, ok := v.(amqpDescribed); ok {
			if expr, ok := f.value.(string); ok {
				return expr
			}
		}
	}
	return ""
}

// sbAMQPIsManagementAddress reports whether an address is an entity's
// management node, `<entity>/$management`.
func sbAMQPIsManagementAddress(address string) bool {
	return strings.HasSuffix(strings.ToLower(address), "/$management")
}

func sbAMQPTopicSubscriptions(namespace, topic string) []string {
	if sbSubscriptions == nil {
		return nil
	}
	prefix := sbAdminTopicID(namespace, topic) + "/subscriptions/"
	subs := sbSubscriptionsUnder(prefix)
	paths := make([]string, 0, len(subs))
	for _, sub := range subs {
		name := strings.TrimPrefix(sub.ID, prefix)
		if name == "" || strings.Contains(name, "/") {
			continue
		}
		paths = append(paths, topic+"/"+name)
	}
	sort.Strings(paths)
	return paths
}

func (c *sbAMQPConn) receiverForAddress(address string) *sbAMQPLink {
	return c.receiverForAddressMatch(address, nil)
}

func (c *sbAMQPConn) receiverForAddressOnChannel(address string, channel uint16) *sbAMQPLink {
	return c.receiverForAddressMatch(address, &channel)
}

func (c *sbAMQPConn) receiverForAddressMatch(address string, channel *uint16) *sbAMQPLink {
	address = strings.Trim(address, "/")
	var links []*sbAMQPLink
	c.mu.Lock()
	for _, link := range c.links {
		if channel != nil && link.channel != *channel {
			continue
		}
		if link.clientRole && (link.address == address || address == "" && (link.address == "$cbs" || link.address == "$management")) {
			links = append(links, cloneSBAMQPLink(link))
		}
	}
	c.mu.Unlock()
	sort.Slice(links, func(i, j int) bool { return links[i].serverHandle < links[j].serverHandle })
	if len(links) == 0 {
		return nil
	}
	return links[0]
}

// writeReply sends a settled management or CBS reply on the receiver link a
// lookup returned a copy of.
func (c *sbAMQPConn) writeReply(reply *sbAMQPLink, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	link := c.links[sbAMQPLinkKey(reply.channel, reply.clientHandle)]
	if link == nil {
		return nil
	}
	_, err := c.writeTransferLocked(link, payload, true, nil)
	return err
}

// writeTransferLocked sends one transfer on a link and returns its delivery
// id; a nil tag gets a unique one. The transfer spends a unit of the link's
// credit and advances its delivery count. The caller holds c.mu.
func (c *sbAMQPConn) writeTransferLocked(link *sbAMQPLink, payload []byte, settled bool, tag []byte) (uint32, error) {
	deliveryID := atomic.AddUint32(&c.nextDelivery, 1) - 1
	if tag == nil {
		tag = []byte(fmt.Sprintf("tag-%d", deliveryID))
	}
	link.deliveryCount++
	if link.credit > 0 {
		link.credit--
	}
	c.sessionLocked(link.channel).nextOutgoingID++
	return deliveryID, c.writeFrame(amqpFrameTypeAMQP, link.channel, append(encodeDescribedList(amqpDescTransfer, []any{
		link.serverHandle,
		deliveryID,
		tag,
		uint32(0),
		settled,
	}), payload...))
}

func (c *sbAMQPConn) writeFrame(frameType byte, channel uint16, body []byte) error {
	frame := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(frame[:4], uint32(8+len(body)))
	frame[4] = 2
	frame[5] = frameType
	binary.BigEndian.PutUint16(frame[6:8], channel)
	frame = append(frame, body...)
	return c.writeBytes(frame)
}

func (c *sbAMQPConn) writeBytes(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.transport.Write(data)
}

type sbAMQPWebSocketTransport struct {
	conn *websocket.Conn
}

func (t sbAMQPWebSocketTransport) Read(context.Context) ([]byte, error) {
	for {
		mt, data, err := t.conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt == websocket.BinaryMessage {
			return data, nil
		}
	}
}

func (t sbAMQPWebSocketTransport) Write(data []byte) error {
	return t.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (t sbAMQPWebSocketTransport) Close() error {
	return t.conn.Close()
}

type sbAMQPRawTransport struct {
	conn net.Conn
	r    *bufio.Reader
}

func newSBAMQPRawTransport(conn net.Conn) *sbAMQPRawTransport {
	return &sbAMQPRawTransport{conn: conn, r: bufio.NewReader(conn)}
}

// sbAMQPMaxFrameSize bounds the length-prefixed frame the raw transport will
// allocate. The 4-byte size prefix is attacker-controlled (the TLS listener is
// pre-auth), so without a cap a header declaring 0xFFFFFFFF forces a ~4GB
// make([]byte, size) → OOM. 16 MiB is far above any real Service Bus control or
// transfer frame.
const sbAMQPMaxFrameSize = 16 * 1024 * 1024

func (t *sbAMQPRawTransport) Read(context.Context) ([]byte, error) {
	return sbAMQPReadFrame(t.r)
}

// sbAMQPReadFrame reads one length-prefixed AMQP frame (or the 8-byte AMQP
// protocol-id header) from r, bounding the declared size before allocating.
func sbAMQPReadFrame(r *bufio.Reader) ([]byte, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	if bytes.Equal(header[:4], []byte{'A', 'M', 'Q', 'P'}) {
		return header, nil
	}
	size := int(binary.BigEndian.Uint32(header[:4]))
	if size < 8 || size > sbAMQPMaxFrameSize {
		return nil, fmt.Errorf("invalid AMQP frame size %d", size)
	}
	frame := make([]byte, size)
	copy(frame, header)
	if _, err := io.ReadFull(r, frame[8:]); err != nil {
		// A frame that did not arrive whole is not a frame. Returning the
		// partly filled buffer beside the error handed a caller that checked
		// only one of the two a body the peer never sent — the size it claimed,
		// zero-padded — on a pre-authentication path where the peer chooses
		// both the size and where to stop sending.
		return nil, err
	}
	return frame, nil
}

func (t *sbAMQPRawTransport) Write(data []byte) error {
	_, err := t.conn.Write(data)
	return err
}

func (t *sbAMQPRawTransport) Close() error {
	return t.conn.Close()
}

func sbAMQPNamespaceFromHost(host string) string {
	host = strings.Trim(strings.TrimSpace(host), ".")
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if i := strings.Index(host, ".servicebus."); i > 0 {
		return host[:i]
	}
	if i := strings.Index(host, "."); i > 0 {
		return host[:i]
	}
	return host
}

func parseAMQPFrame(data []byte) (amqpFrame, error) {
	// The frame header is 8 bytes (size:4, doff:1, type:1, channel:2);
	// the size field is attacker-controlled so guard it against a short
	// buffer and a size that overruns the data before slicing.
	if len(data) < 8 {
		return amqpFrame{}, errors.New("short AMQP frame")
	}
	size := int(binary.BigEndian.Uint32(data[:4]))
	if size == 8 {
		return amqpFrame{}, nil
	}
	if size < 8 || size > len(data) {
		return amqpFrame{}, errors.New("invalid AMQP frame size")
	}
	doff := int(data[4]) * 4
	if doff < 8 || doff > size {
		return amqpFrame{}, errors.New("invalid AMQP data offset")
	}
	p := &amqpValueReader{data: data[doff:size]}
	v, err := p.readValue()
	if err != nil {
		return amqpFrame{}, err
	}
	desc, ok := v.(amqpDescribed)
	if !ok {
		return amqpFrame{}, errors.New("missing AMQP performative")
	}
	fields, _ := desc.value.([]any)
	return amqpFrame{
		frameType: data[5],
		channel:   binary.BigEndian.Uint16(data[6:8]),
		desc:      desc.code,
		fields:    fields,
		payload:   p.data[p.off:],
	}, nil
}

// amqpMaxValues / amqpMaxDepth bound the work a single (attacker-controlled)
// frame can cause: a wire-encoded list/map/array carries its element count as a
// u32, and an element can re-trigger decoding, so without a budget a crafted
// frame loops billions of times (OOM/hang) or nests until the stack overflows.
// Real Service Bus control frames are tiny, so these caps never bite legitimately.
const (
	amqpMaxValues = 100000
	amqpMaxDepth  = 1024
)

type amqpValueReader struct {
	data   []byte
	off    int
	values int
	depth  int
}

func (r *amqpValueReader) readValue() (any, error) {
	r.values++
	if r.values > amqpMaxValues {
		return nil, errors.New("AMQP frame has too many values")
	}
	r.depth++
	if r.depth > amqpMaxDepth {
		r.depth--
		return nil, errors.New("AMQP frame nesting too deep")
	}
	defer func() { r.depth-- }()

	code, err := r.byte()
	if err != nil {
		return nil, err
	}
	switch code {
	case 0x00:
		d, err := r.readValue()
		if err != nil {
			return nil, err
		}
		v, err := r.readValue()
		return amqpDescribed{code: asUint64(d), value: v}, err
	case 0x40:
		return nil, nil
	case 0x41:
		return true, nil
	case 0x42:
		return false, nil
	case 0x43:
		return uint32(0), nil
	case 0x44:
		return uint64(0), nil
	case 0x50:
		b, err := r.byte()
		return b, err
	case 0x52:
		b, err := r.byte()
		return uint32(b), err
	case 0x53:
		b, err := r.byte()
		return uint64(b), err
	case 0x56:
		b, err := r.byte()
		return b != 0, err
	case 0x51:
		b, err := r.byte()
		return int8(b), err
	case 0x54:
		b, err := r.byte()
		return int32(int8(b)), err
	case 0x55:
		b, err := r.byte()
		return int64(int8(b)), err
	case 0x60:
		return r.u16()
	case 0x61:
		v, err := r.u16()
		return int16(v), err
	case 0x71:
		return int32(r.u32()), nil
	case 0x72:
		return math.Float32frombits(r.u32()), nil
	case 0x81:
		return int64(r.u64()), nil
	case 0x82:
		return math.Float64frombits(r.u64()), nil
	case 0x70:
		return r.u32(), nil
	case 0x80:
		return r.u64(), nil
	case 0x83:
		return time.UnixMilli(int64(r.u64())), nil
	case 0x98:
		return r.take(16)
	case 0xa0:
		n, _ := r.byte()
		return r.take(int(n))
	case 0xb0:
		return r.take(int(r.u32()))
	case 0xa1:
		n, _ := r.byte()
		b, err := r.take(int(n))
		return string(b), err
	case 0xb1:
		b, err := r.take(int(r.u32()))
		return string(b), err
	case 0xa3:
		n, _ := r.byte()
		b, err := r.take(int(n))
		return amqpSymbol(b), err
	case 0xb3:
		b, err := r.take(int(r.u32()))
		return amqpSymbol(b), err
	case 0x45:
		return []any{}, nil
	case 0xc0:
		size, _ := r.byte()
		count, _ := r.byte()
		return r.readList(r.off+int(size)-1, int(count))
	case 0xd0:
		size := r.u32()
		count := r.u32()
		return r.readList(r.off+int(size)-4, int(count))
	case 0xc1:
		size, _ := r.byte()
		count, _ := r.byte()
		return r.readMap(int(size), int(count))
	case 0xd1:
		size := r.u32()
		count := r.u32()
		return r.readMap(int(size), int(count))
	case 0xe0:
		size, _ := r.byte()
		count, _ := r.byte()
		return r.readArray(int(size), int(count))
	case 0xf0:
		size := r.u32()
		count := r.u32()
		return r.readArray(int(size), int(count))
	default:
		return nil, fmt.Errorf("unsupported AMQP type 0x%x", code)
	}
}

func (r *amqpValueReader) readList(end, count int) ([]any, error) {
	// count is attacker-controlled (decoded from the wire) — do not
	// pre-size the slice with it or a huge value OOMs the process; the
	// loop is bounded by the available data.
	var out []any
	for i := 0; i < count && r.off < len(r.data); i++ {
		v, err := r.readValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if end > r.off && end <= len(r.data) {
		r.off = end
	}
	return out, nil
}

func (r *amqpValueReader) readMap(_ int, count int) (out map[any]any, err error) {
	out = map[any]any{}
	// A decoded key can be a composite (list/array → []any, or a described
	// value wrapping one), which is not a valid Go map key and panics
	// "hash of unhashable type" on insert. Real AMQP map keys are scalars
	// (symbols/strings); a composite key means a malformed frame, so
	// recover the insert panic into an error rather than crashing.
	defer func() {
		if recover() != nil {
			out, err = nil, errors.New("invalid AMQP map key")
		}
	}()
	for i := 0; i < count/2; i++ {
		k, rerr := r.readValue()
		if rerr != nil {
			return nil, rerr
		}
		v, rerr := r.readValue()
		if rerr != nil {
			return nil, rerr
		}
		out[k] = v
	}
	return out, nil
}

func (r *amqpValueReader) readArray(_ int, count int) ([]any, error) {
	elemType, err := r.byte()
	if err != nil {
		return nil, err
	}
	// count is attacker-controlled — don't pre-size with it (OOM). The
	// per-element re-injection of elemType decrements r.off, so guard
	// against underflowing below 0 (which would index r.data[-1]).
	var out []any
	for i := 0; i < count; i++ {
		if r.off <= 0 {
			return nil, io.ErrUnexpectedEOF
		}
		r.off--
		r.data[r.off] = elemType
		v, err := r.readValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (r *amqpValueReader) byte() (byte, error) {
	if r.off >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	b := r.data[r.off]
	r.off++
	return b, nil
}

func (r *amqpValueReader) take(n int) ([]byte, error) {
	if r.off+n > len(r.data) {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.data[r.off : r.off+n]
	r.off += n
	return b, nil
}

func (r *amqpValueReader) u16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *amqpValueReader) u32() uint32 {
	b, err := r.take(4)
	if err != nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (r *amqpValueReader) u64() uint64 {
	b, err := r.take(8)
	if err != nil {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

func encodeDescribedList(code uint64, fields []any) []byte {
	out := []byte{0x00, 0x53, byte(code)}
	if len(fields) == 0 {
		return append(out, 0x45)
	}
	var body []byte
	for _, f := range fields {
		body = append(body, encodeAMQPValue(f)...)
	}
	out = append(out, 0xd0)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, uint32(4+len(body)))
	out = append(out, size...)
	binary.BigEndian.PutUint32(size, uint32(len(fields)))
	out = append(out, size...)
	return append(out, body...)
}

func encodeAMQPValue(v any) []byte {
	switch t := v.(type) {
	case nil:
		return []byte{0x40}
	case bool:
		if t {
			return []byte{0x41}
		}
		return []byte{0x42}
	case uint8:
		return []byte{0x50, t}
	case uint16:
		b := []byte{0x60, 0, 0}
		binary.BigEndian.PutUint16(b[1:], t)
		return b
	case uint32:
		if t == 0 {
			return []byte{0x43}
		}
		if t <= math.MaxUint8 {
			return []byte{0x52, byte(t)}
		}
		b := []byte{0x70, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], t)
		return b
	case uint64:
		if t <= math.MaxUint8 {
			return []byte{0x53, byte(t)}
		}
		b := []byte{0x80, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(b[1:], t)
		return b
	case int32:
		b := []byte{0x71, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[1:], uint32(t))
		return b
	case int64:
		b := []byte{0x81, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(b[1:], uint64(t))
		return b
	case string:
		return encodeString(0xa1, 0xb1, []byte(t))
	case amqpSymbol:
		return encodeString(0xa3, 0xb3, []byte(t))
	case []byte:
		return encodeString(0xa0, 0xb0, t)
	case time.Time:
		b := []byte{0x83, 0, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint64(b[1:], uint64(t.UnixMilli()))
		return b
	case amqpDescribed:
		return append(append([]byte{0x00}, encodeAMQPValue(t.code)...), encodeAMQPValue(t.value)...)
	case []any:
		return encodeList(t)
	case []string:
		values := make([]any, 0, len(t))
		for _, v := range t {
			values = append(values, v)
		}
		return encodeList(values)
	case map[string]any:
		m := map[any]any{}
		for k, v := range t {
			m[k] = v
		}
		return encodeMap(m)
	case map[any]any:
		return encodeMap(t)
	default:
		return []byte{0x40}
	}
}

func encodeString(small, large byte, b []byte) []byte {
	if len(b) <= math.MaxUint8 {
		return append([]byte{small, byte(len(b))}, b...)
	}
	out := []byte{large, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(len(b)))
	return append(out, b...)
}

func encodeList(fields []any) []byte {
	if len(fields) == 0 {
		return []byte{0x45}
	}
	var body []byte
	for _, f := range fields {
		body = append(body, encodeAMQPValue(f)...)
	}
	out := []byte{0xd0, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(4+len(body)))
	binary.BigEndian.PutUint32(out[5:], uint32(len(fields)))
	return append(out, body...)
}

// encodeMap encodes each key as the type it has, so a symbol key stays a
// symbol on the wire, in an order independent of map iteration.
func encodeMap(m map[any]any) []byte {
	keys := make([]any, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	var body []byte
	for _, k := range keys {
		body = append(body, encodeAMQPValue(k)...)
		body = append(body, encodeAMQPValue(m[k])...)
	}
	out := []byte{0xd1, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(out[1:], uint32(4+len(body)))
	binary.BigEndian.PutUint32(out[5:], uint32(len(keys)*2))
	return append(out, body...)
}

func encodeSource(address string) amqpDescribed {
	return amqpDescribed{code: amqpDescSource, value: []any{address, uint32(0), amqpSymbol("session-end")}}
}

func encodeTarget(address string) amqpDescribed {
	return amqpDescribed{code: amqpDescTarget, value: []any{address, uint32(0), amqpSymbol("session-end")}}
}

func field(fields []any, idx int) any {
	if idx < 0 || idx >= len(fields) {
		return nil
	}
	return fields[idx]
}

func describedAddress(v any) string {
	d, ok := v.(amqpDescribed)
	if !ok {
		return ""
	}
	fields, _ := d.value.([]any)
	return asString(field(fields, 0))
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case amqpSymbol:
		return string(t)
	default:
		return ""
	}
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asUint8(v any) uint8 {
	switch t := v.(type) {
	case uint8:
		return t
	case uint32:
		return uint8(t)
	case uint64:
		return uint8(t)
	default:
		return 0
	}
}

func asUint32(v any) uint32 {
	switch t := v.(type) {
	case uint8:
		return uint32(t)
	case uint16:
		return uint32(t)
	case uint32:
		return t
	case uint64:
		return uint32(t)
	case nil:
		return 0
	default:
		return 0
	}
}

func asUint64(v any) uint64 {
	switch t := v.(type) {
	case uint8:
		return uint64(t)
	case uint16:
		return uint64(t)
	case uint32:
		return uint64(t)
	case uint64:
		return t
	default:
		return 0
	}
}

func sbAMQPLinkKey(channel uint16, handle uint32) uint64 {
	return uint64(channel)<<32 | uint64(handle)
}
