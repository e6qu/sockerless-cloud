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
	claims  []string
	mu      sync.Mutex
	writeMu sync.Mutex
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
	credit       uint32
	readIndex    int
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
	}
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
	}()
	for {
		data, err := c.transport.Read(ctx)
		if err != nil {
			return
		}
		for len(data) > 0 {
			if len(data) >= 8 && bytes.Equal(data[:4], []byte{'A', 'M', 'Q', 'P'}) {
				if err := c.handleProto(data[:8]); err != nil {
					return
				}
				data = data[8:]
				continue
			}
			if len(data) < 8 {
				return
			}
			size := int(binary.BigEndian.Uint32(data[:4]))
			if size < 8 || size > len(data) {
				return
			}
			frame, err := parseAMQPFrame(data[:size])
			if err != nil {
				return
			}
			if err := c.handleFrame(ctx, frame); err != nil {
				return
			}
			data = data[size:]
		}
	}
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
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescBegin, []any{
			frame.channel,
			uint32(1),
			uint32(5000),
			uint32(1000),
			uint32(math.MaxInt16),
		}))
	case amqpDescAttach:
		return c.handleAttach(frame)
	case amqpDescFlow:
		return c.handleFlow(ctx, frame)
	case amqpDescTransfer:
		return c.handleTransfer(ctx, frame)
	case amqpDescDetach:
		handle := asUint32(field(frame.fields, 0))
		c.mu.Lock()
		delete(c.links, sbAMQPLinkKey(frame.channel, handle))
		c.mu.Unlock()
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDetach, []any{handle, true}))
	case amqpDescEnd:
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
		!c.hasClaimFor(sbAMQPEntityPath(entityAddress)) {
		return c.refuseAttach(frame, name, clientHandle, clientRole, sourceAddress, entityAddress)
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
	c.links[sbAMQPLinkKey(frame.channel, clientHandle)] = link
	c.mu.Unlock()

	if clientRole {
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, []any{
			name,
			serverHandle,
			false,
			uint8(1),
			field(frame.fields, 4),
			encodeSource(sourceAddress),
			nil,
			nil,
			nil,
			nil,
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
	return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescFlow, []any{
		uint32(0),
		uint32(5000),
		uint32(1),
		uint32(1000),
		serverHandle,
		uint32(0),
		uint32(1000),
		uint32(0),
	}))
}

// refuseAttach answers an attach for an entity the connection has not
// authenticated for. Real Service Bus and Event Hubs complete the attach
// handshake and immediately detach the link with the amqp:unauthorized-access
// error condition, which is what the AMQP clients surface as an auth failure.
func (c *sbAMQPConn) refuseAttach(frame amqpFrame, name string, clientHandle uint32, clientRole bool, sourceAddress, address string) error {
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
	if clientRole {
		attachFields[5] = encodeSource(sourceAddress)
	} else {
		attachFields[3] = uint8(2)
	}
	if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescAttach, attachFields)); err != nil {
		return err
	}
	return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDetach, []any{
		serverHandle,
		true,
		amqpDescribed{code: amqpDescError, value: []any{
			amqpSymbol(errSASInvalidSignature.Condition),
			fmt.Sprintf("Unauthorized access. %q claim(s) are required to perform this operation.", address),
		}},
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
	if link.address == "$cbs" || link.address == "$management" {
		if err := c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
			true,
			deliveryID,
			nil,
			true,
			amqpDescribed{code: amqpDescAccepted, value: []any{}},
		})); err != nil {
			return err
		}
		return c.respondRPC(frame.channel, &msg)
	}
	namespace := c.currentNamespace()
	if ehAMQPIsSenderAddress(namespace, link.address) {
		ehAMQPEnqueue(namespace, link.address, &msg)
		return c.writeFrame(amqpFrameTypeAMQP, frame.channel, encodeDescribedList(amqpDescDisposition, []any{
			true,
			deliveryID,
			nil,
			true,
			amqpDescribed{code: amqpDescAccepted, value: []any{}},
		}))
	}
	msgID := generateUUID()
	if msg.Properties != nil {
		if id, ok := msg.Properties.MessageID.(string); ok && id != "" {
			msgID = id
		}
	}
	if err := c.enqueue(link.address, sbMessage{
		MessageID:    msgID,
		Body:         msg.GetData(),
		EnqueuedTime: time.Now().UTC(),
	}); err != nil {
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

func (c *sbAMQPConn) enqueue(address string, msg sbMessage) error {
	namespace := c.currentNamespace()
	paths := c.enqueuePaths(namespace, address)
	for _, path := range paths {
		st := sbQueueStateFor(sbQueueKey(namespace, path))
		st.mu.Lock()
		st.nextSeq++
		msg.SequenceNumber = st.nextSeq
		st.messages = append(st.messages, msg)
		st.persistLocked()
		st.mu.Unlock()
	}
	return sbAMQPDeliverAvailableMessages(namespace, paths)
}

func (c *sbAMQPConn) enqueuePaths(namespace, address string) []string {
	path := sbAMQPEntityPath(address)
	if strings.Contains(path, "/") {
		return []string{path}
	}
	subs := sbAMQPTopicSubscriptions(namespace, path)
	if len(subs) == 0 {
		return []string{path}
	}
	return subs
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

func (c *sbAMQPConn) respondRPC(channel uint16, req *amqp.Message) error {
	replyTo := ""
	corr := any(nil)
	if req.Properties != nil {
		if req.Properties.ReplyTo != nil {
			replyTo = *req.Properties.ReplyTo
		}
		corr = req.Properties.MessageID
	}
	link := c.receiverForAddressOnChannel(replyTo, channel)
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
		return c.writeTransfer(channel, link, body, true)
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
		return c.writeTransfer(channel, link, body, true)
	}
	if resp, ok := ehAMQPHandleRPC(c.currentNamespace(), req); ok {
		body, err := resp.MarshalBinary()
		if err != nil {
			return err
		}
		return c.writeTransfer(channel, link, body, true)
	}
	resp := &amqp.Message{
		Properties: &amqp.MessageProperties{CorrelationID: corr},
		ApplicationProperties: map[string]any{
			"status-code":        int32(202),
			"status-description": "Accepted",
		},
	}
	body, err := resp.MarshalBinary()
	if err != nil {
		return err
	}
	return c.writeTransfer(channel, link, body, true)
}

func (c *sbAMQPConn) handleFlow(ctx context.Context, frame amqpFrame) error {
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
	credit := asUint32(field(frame.fields, 6))
	if credit == 0 {
		c.mu.Unlock()
		return nil
	}
	link.credit += credit
	if ehAMQPIsReceiverAddress(namespace, link.address) {
		for link.credit > 0 {
			msg, ok := ehAMQPNextEvent(namespace, link.address, link.readIndex)
			if !ok {
				c.mu.Unlock()
				return nil
			}
			link.readIndex++
			link.credit--
			if err := c.writeTransfer(channelOrDefault(frame.channel), link, msg, link.settledSend); err != nil {
				c.mu.Unlock()
				return err
			}
		}
		c.mu.Unlock()
		_ = ctx
		return nil
	}
	path := sbAMQPEntityPath(link.address)
	c.mu.Unlock()
	_ = ctx
	return c.deliverAvailableMessages([]string{path})
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
		if !link.clientRole || link.credit == 0 {
			continue
		}
		if _, ok := pathSet[sbAMQPEntityPath(link.address)]; !ok {
			continue
		}
		for link.credit > 0 {
			msg, ok := c.popMessage(namespace, link.address)
			if !ok {
				break
			}
			link.credit--
			if err := c.writeTransfer(link.channel, link, msg, link.settledSend); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *sbAMQPConn) popMessage(namespace, address string) ([]byte, bool) {
	st := sbQueueStateFor(sbQueueKey(namespace, sbAMQPEntityPath(address)))
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.messages) == 0 {
		return nil, false
	}
	msg := st.messages[0]
	st.messages = st.messages[1:]
	st.persistLocked()
	out := &amqp.Message{
		DeliveryTag: []byte(generateUUID()),
		Properties:  &amqp.MessageProperties{MessageID: msg.MessageID},
		Annotations: amqp.Annotations{
			"x-opt-sequence-number": msg.SequenceNumber,
			"x-opt-enqueued-time":   msg.EnqueuedTime,
		},
		Data: [][]byte{msg.Body},
	}
	body, err := out.MarshalBinary()
	if err != nil {
		return nil, false
	}
	return body, true
}

func sbAMQPEntityPath(address string) string {
	address = strings.Trim(strings.TrimSpace(address), "/")
	if address == "" {
		return ""
	}
	parts := strings.Split(address, "/")
	if len(parts) == 3 && strings.EqualFold(parts[1], "subscriptions") {
		return parts[0] + "/" + parts[2]
	}
	return address
}

func sbAMQPTopicSubscriptions(namespace, topic string) []string {
	if sbSubscriptions == nil {
		return nil
	}
	prefix := sbAdminTopicID(namespace, topic) + "/subscriptions/"
	subs := sbSubscriptions.Filter(func(sub SBSubscription) bool {
		return strings.HasPrefix(sub.ID, prefix)
	})
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

func (c *sbAMQPConn) writeTransfer(channel uint16, link *sbAMQPLink, payload []byte, settled bool) error {
	deliveryID := atomic.AddUint32(&c.nextDelivery, 1) - 1
	return c.writeFrame(amqpFrameTypeAMQP, channel, append(encodeDescribedList(amqpDescTransfer, []any{
		link.serverHandle,
		deliveryID,
		[]byte(fmt.Sprintf("tag-%d", deliveryID)),
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
	_, err := io.ReadFull(r, frame[8:])
	return frame, err
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
	case 0x60:
		return r.u16()
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
		return append([]byte{0x00, 0x53, byte(t.code)}, encodeAMQPValue(t.value)...)
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

func encodeMap(m map[any]any) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, fmt.Sprint(k))
	}
	sort.Strings(keys)
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

func channelOrDefault(channel uint16) uint16 {
	return channel
}

func sbAMQPLinkKey(channel uint16, handle uint32) uint64 {
	return uint64(channel)<<32 | uint64(handle)
}
