package cacheengine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Diversion sends the connections a plane's endpoints accept to another
// cluster, the source of a migration, until it ends: each command reaches the
// source node that owns its key's slot, and the source's redirections are
// followed rather than answered, so a client sees the endpoint own every slot
// as it does once the plane serves it.
type Diversion struct {
	// Seed is the host:port of one source node.
	Seed string
	// Dial opens a connection to the source node at a host:port.
	Dial func(address string) (net.Conn, error)
}

type guard struct {
	mu        sync.Mutex
	diversion *Diversion
	// diverted holds the client connections a diversion serves, which end
	// with it.
	diverted map[net.Conn]struct{}
	refuse   bool
	// plain holds the client connections relayed without a guard, which a
	// refusal of writes ends so each client reconnects to a guarded relay.
	plain map[net.Conn]struct{}
	// writes is every command the engine flags write, read once.
	writes map[string]bool
}

// Divert relays every connection the endpoints accept from now on to the
// cluster d names; nil ends the diversion and closes the connections it
// served, so each client reconnects to the plane.
func (p *Plane) Divert(d *Diversion) {
	p.guard.mu.Lock()
	p.guard.diversion = d
	var ended []net.Conn
	if d == nil {
		for conn := range p.guard.diverted {
			ended = append(ended, conn)
		}
		p.guard.diverted = nil
	}
	p.guard.mu.Unlock()
	for _, conn := range ended {
		_ = conn.Close()
	}
}

// Diverted reports whether the endpoints relay to another cluster.
func (p *Plane) Diverted() bool {
	p.guard.mu.Lock()
	defer p.guard.mu.Unlock()
	return p.guard.diversion != nil
}

// RefuseWrites has the endpoints answer every command the engine flags write
// with the error a read-only replica answers, as a cluster that follows
// another does.
func (p *Plane) RefuseWrites(refuse bool) {
	p.guard.mu.Lock()
	p.guard.refuse = refuse
	var ended []net.Conn
	if refuse {
		for conn := range p.guard.plain {
			ended = append(ended, conn)
		}
		p.guard.plain = nil
	}
	p.guard.mu.Unlock()
	for _, conn := range ended {
		_ = conn.Close()
	}
}

// relayedPlain records a client connection relayed without a guard until
// done runs.
func (p *Plane) relayedPlain(client net.Conn) (done func()) {
	p.guard.mu.Lock()
	defer p.guard.mu.Unlock()
	if p.guard.plain == nil {
		p.guard.plain = map[net.Conn]struct{}{}
	}
	p.guard.plain[client] = struct{}{}
	return func() {
		p.guard.mu.Lock()
		delete(p.guard.plain, client)
		p.guard.mu.Unlock()
	}
}

func (p *Plane) refusesWrites() bool {
	p.guard.mu.Lock()
	defer p.guard.mu.Unlock()
	return p.guard.refuse
}

func (p *Plane) divertedTo(client net.Conn) *Diversion {
	p.guard.mu.Lock()
	defer p.guard.mu.Unlock()
	if p.guard.diversion == nil {
		return nil
	}
	if p.guard.diverted == nil {
		p.guard.diverted = map[net.Conn]struct{}{}
	}
	p.guard.diverted[client] = struct{}{}
	return p.guard.diversion
}

func (p *Plane) undivert(client net.Conn) {
	p.guard.mu.Lock()
	delete(p.guard.diverted, client)
	p.guard.mu.Unlock()
}

// isWrite reports whether the engine flags a command write, from COMMAND's
// answer the plane reads once.
func (p *Plane) isWrite(name string) (bool, error) {
	p.guard.mu.Lock()
	writes := p.guard.writes
	p.guard.mu.Unlock()
	if writes == nil {
		reply, err := p.Command(p.Primary(), "COMMAND")
		if err != nil {
			return false, err
		}
		commands, _ := reply.([]any)
		writes = map[string]bool{}
		for _, command := range commands {
			fields, _ := command.([]any)
			if len(fields) < 3 {
				continue
			}
			flags, _ := fields[2].([]any)
			for _, flag := range flags {
				if fmt.Sprint(flag) == "write" {
					writes[strings.ToLower(fmt.Sprint(fields[0]))] = true
				}
			}
		}
		p.guard.mu.Lock()
		p.guard.writes = writes
		p.guard.mu.Unlock()
	}
	return writes[strings.ToLower(name)], nil
}

// Dial opens a connection to a node as the user the plane administers it as,
// with no deadline, for a caller that sends it commands over time.
func (p *Plane) Dial(node int) (*Conn, error) {
	address, err := p.nodeAddress(node)
	if err != nil {
		return nil, err
	}
	conn, err := p.dial(address, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to node %d: %w", node, err)
	}
	return conn, nil
}

// AssignSlots moves slots between the cluster's primaries until each primary
// owns the slots want gives it, the way balanceSlots moves them.
func (p *Plane) AssignSlots(want map[int][]int) error {
	primaries := make([]int, 0, len(want))
	for node := range want {
		primaries = append(primaries, node)
	}
	if len(primaries) == 0 {
		return errors.New("no primary to assign slots to")
	}
	view, err := p.clusterView(primaries[0])
	if err != nil {
		return err
	}
	ids := map[int]string{}
	owner := map[int]int{}
	for _, node := range primaries {
		if ids[node], err = p.nodeID(node); err != nil {
			return err
		}
		entry, ok := view.byID(ids[node])
		if !ok {
			return fmt.Errorf("node %d (%s) is not in the cluster's view", node, ids[node])
		}
		slots, err := SlotList(entry.Slots)
		if err != nil {
			return err
		}
		for _, slot := range slots {
			owner[slot] = node
		}
	}
	type pair struct{ from, to int }
	moves := map[pair][]int{}
	var order []pair
	for _, node := range primaries {
		for _, slot := range want[node] {
			from, ok := owner[slot]
			if !ok {
				return fmt.Errorf("no primary owns slot %d", slot)
			}
			if from == node {
				continue
			}
			key := pair{from, node}
			if _, seen := moves[key]; !seen {
				order = append(order, key)
			}
			moves[key] = append(moves[key], slot)
		}
	}
	for _, move := range order {
		slots := moves[move]
		for start := 0; start < len(slots); start += slotBatch {
			batch := slots[start:min(start+slotBatch, len(slots))]
			if err := p.migrateSlots(move.from, move.to, batch, primaries, ids); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionCommands are the commands that set up a connection rather than act
// on data: a diversion repeats them on each source node it opens for the
// client.
var sessionCommands = map[string]bool{"AUTH": true, "HELLO": true, "SELECT": true, "READONLY": true, "READWRITE": true}

// streamCommands turn a connection into a stream that no longer pairs
// replies with commands.
var streamCommands = map[string]bool{"SUBSCRIBE": true, "PSUBSCRIBE": true, "SSUBSCRIBE": true, "MONITOR": true}

type backendConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (b *backendConn) send(raw []byte) error {
	_, err := b.conn.Write(raw)
	return err
}

func (b *backendConn) exchange(raw []byte) (respValue, error) {
	if err := b.send(raw); err != nil {
		return respValue{}, err
	}
	return readValue(b.reader)
}

// sequentialRelay answers a client's commands one at a time, which lets it
// refuse a command or send it to another node and still keep the replies in
// the order of the commands.
type sequentialRelay struct {
	p         *Plane
	client    net.Conn
	accepted  net.Conn
	writer    *bufio.Writer
	diversion *Diversion
	rewrite   bool

	backends map[string]*backendConn
	current  string
	owners   map[int]string
	session  [][]byte
	protocol string
	// plane answers the topology commands of a diverted connection.
	plane *backendConn

	inTx       bool
	txAborted  bool
	txPending  bool
	txQueued   [][]byte
	txBackend  string
	expiring   *expiringConn
	exchangeOn bool
}

// relaySequential serves a client the plane refuses writes for, or one a
// diversion sends to another cluster. backend is the node a plane connection
// relays to; a diverted connection has none.
func (p *Plane) relaySequential(client net.Conn, backend net.Conn, diversion *Diversion, rewrite bool) {
	r := &sequentialRelay{
		p: p, client: client, accepted: client, diversion: diversion, rewrite: rewrite,
		backends: map[string]*backendConn{}, owners: map[int]string{}, protocol: "2",
	}
	defer r.close()
	r.expiring = &expiringConn{Conn: client}
	r.client = r.expiring
	r.writer = bufio.NewWriter(r.client)
	if diversion != nil {
		r.current = diversion.Seed
	} else {
		r.current = "plane"
		r.backends["plane"] = &backendConn{conn: backend, reader: bufio.NewReader(backend)}
		r.exchangeOn = p.cfg.ExchangeCredential != nil
	}
	reader := bufio.NewReader(r.client)
	clientAddress := addrPort(client.RemoteAddr()).Addr()
	for {
		args, raw, err := ReadCommand(reader)
		if err != nil {
			return
		}
		if len(args) == 0 {
			continue
		}
		if r.exchangeOn {
			if rewritten, grant, ok := ExchangeCredential(args, p.Password(), clientAddress, p.cfg.ExchangeCredential); ok {
				raw = EncodeCommand(rewritten)
				r.expiring.extend(grant.Lifetime)
			}
		}
		name := strings.ToUpper(args[0])
		if streamCommands[name] {
			r.stream(raw, reader)
			return
		}
		reply, err := r.answer(name, args, raw)
		if err != nil {
			log.Printf("%s data plane: %v", p.cfg.Name, err)
			return
		}
		var b bytes.Buffer
		reply.encode(&b)
		if _, err := r.writer.Write(b.Bytes()); err != nil {
			return
		}
		if reader.Buffered() == 0 {
			if err := r.writer.Flush(); err != nil {
				return
			}
		}
	}
}

func (r *sequentialRelay) close() {
	for _, backend := range r.backends {
		_ = backend.conn.Close()
	}
	if r.plane != nil {
		_ = r.plane.conn.Close()
	}
	if r.diversion != nil {
		r.p.undivert(r.accepted)
	}
}

func errorValue(text string) respValue { return respValue{kind: '-', text: text} }

// answer is the reply to one command.
func (r *sequentialRelay) answer(name string, args []string, raw []byte) (respValue, error) {
	if name == "HELLO" && len(args) > 1 {
		r.protocol = args[1]
	}
	switch name {
	case "MULTI":
		if r.inTx {
			return errorValue("ERR MULTI calls can not be nested"), nil
		}
		r.inTx, r.txAborted, r.txQueued = true, false, nil
		if r.diversion != nil {
			// The transaction opens on the node its first command's key names.
			r.txPending = true
			return respValue{kind: '+', text: "OK"}, nil
		}
	case "EXEC", "DISCARD":
		if !r.inTx {
			return errorValue("ERR " + name + " without MULTI"), nil
		}
		return r.endTransaction(name)
	}
	if r.p.refusesWrites() {
		write, err := r.p.isWrite(name)
		if err != nil {
			return respValue{}, err
		}
		if write {
			if r.inTx {
				r.txAborted = true
			}
			return errorValue("READONLY You can't write against a read only replica."), nil
		}
	}
	if r.diversion == nil {
		reply, err := r.backends[r.current].exchange(raw)
		if err != nil {
			return respValue{}, err
		}
		r.rewriteReply(reply.kind, args, &reply)
		return reply, nil
	}
	return r.divert(name, args, raw)
}

func (r *sequentialRelay) rewriteReply(kind byte, args []string, reply *respValue) {
	if !r.rewrite {
		return
	}
	if kind == '-' && (strings.HasPrefix(reply.text, "MOVED ") || strings.HasPrefix(reply.text, "ASK ")) || namesNodes(args) {
		reply.rewrite(r.p.endpointAddresses())
	}
}

func (r *sequentialRelay) endTransaction(name string) (respValue, error) {
	r.inTx = false
	pending, aborted := r.txPending, r.txAborted
	r.txPending, r.txAborted = false, false
	backend := r.current
	if r.diversion != nil && r.txBackend != "" {
		backend = r.txBackend
	}
	r.txBackend = ""
	if pending {
		// No command reached a node: the transaction ran nothing.
		if name == "EXEC" && !aborted {
			return respValue{kind: '*', items: []respValue{}}, nil
		}
		if aborted {
			return errorValue("EXECABORT Transaction discarded because of previous errors."), nil
		}
		return respValue{kind: '+', text: "OK"}, nil
	}
	conn, err := r.backend(backend)
	if err != nil {
		return respValue{}, err
	}
	if aborted && name == "EXEC" {
		if _, err := conn.exchange(EncodeCommand([]string{"DISCARD"})); err != nil {
			return respValue{}, err
		}
		return errorValue("EXECABORT Transaction discarded because of previous errors."), nil
	}
	return conn.exchange(EncodeCommand([]string{name}))
}

// backend is the connection to a source node, opened with the client's
// session commands repeated on it.
func (r *sequentialRelay) backend(address string) (*backendConn, error) {
	if conn, ok := r.backends[address]; ok {
		return conn, nil
	}
	if r.diversion == nil {
		return nil, fmt.Errorf("no connection to %s", address)
	}
	conn, err := r.diversion.Dial(address)
	if err != nil {
		return nil, fmt.Errorf("dial the source node %s: %w", address, err)
	}
	b := &backendConn{conn: conn, reader: bufio.NewReader(conn)}
	for _, raw := range r.session {
		if _, err := b.exchange(raw); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	r.backends[address] = b
	return b, nil
}

// owner is the source node that owns slot, from the source's CLUSTER SLOTS.
func (r *sequentialRelay) owner(slot int) (string, error) {
	if address, ok := r.owners[slot]; ok {
		return address, nil
	}
	conn, err := r.backend(r.current)
	if err != nil {
		return "", err
	}
	reply, err := conn.exchange(EncodeCommand([]string{"CLUSTER", "SLOTS"}))
	if err != nil {
		return "", err
	}
	for _, entry := range reply.items {
		if len(entry.items) < 3 || len(entry.items[2].items) < 2 {
			continue
		}
		first, err1 := strconv.Atoi(entry.items[0].text)
		last, err2 := strconv.Atoi(entry.items[1].text)
		host, port := entry.items[2].items[0].text, entry.items[2].items[1].text
		if err1 != nil || err2 != nil || host == "" {
			continue
		}
		for s := first; s <= last; s++ {
			r.owners[s] = net.JoinHostPort(host, port)
		}
	}
	if address, ok := r.owners[slot]; ok {
		return address, nil
	}
	return r.current, nil
}

func redirection(reply respValue) (kind, address string, slot int, ok bool) {
	if reply.kind != '-' {
		return "", "", 0, false
	}
	fields := strings.Fields(reply.text)
	if len(fields) != 3 || (fields[0] != "MOVED" && fields[0] != "ASK") {
		return "", "", 0, false
	}
	slot, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", "", 0, false
	}
	return fields[0], fields[2], slot, true
}

// divert sends one command to the source node that owns its key, following
// the source's redirections; the cluster topology commands answer from the
// plane, whose topology the endpoint keeps.
func (r *sequentialRelay) divert(name string, args []string, raw []byte) (respValue, error) {
	if namesNodes(args) {
		return r.planeAnswer(args, raw)
	}
	if sessionCommands[name] {
		if _, err := r.backend(r.current); err != nil {
			return respValue{}, err
		}
		var reply respValue
		for address, b := range r.backends {
			got, err := b.exchange(raw)
			if err != nil {
				return respValue{}, err
			}
			if address == r.current {
				reply = got
			}
		}
		if reply.kind != '-' {
			r.session = append(r.session, raw)
		}
		return reply, nil
	}
	address := r.current
	if r.inTx && r.txBackend != "" {
		address = r.txBackend
	} else if len(args) > 1 {
		owner, err := r.owner(KeySlot(args[1]))
		if err != nil {
			return respValue{}, err
		}
		address = owner
	}
	if r.inTx && r.txPending {
		conn, err := r.backend(address)
		if err != nil {
			return respValue{}, err
		}
		if _, err := conn.exchange(EncodeCommand([]string{"MULTI"})); err != nil {
			return respValue{}, err
		}
		r.txPending, r.txBackend = false, address
	}
	for range 5 {
		conn, err := r.backend(address)
		if err != nil {
			return respValue{}, err
		}
		reply, err := conn.exchange(raw)
		if err != nil {
			return respValue{}, err
		}
		kind, target, slot, redirected := redirection(reply)
		if !redirected || r.inTx {
			return reply, nil
		}
		if kind == "MOVED" {
			r.owners[slot] = target
			address = target
			continue
		}
		asked, err := r.backend(target)
		if err != nil {
			return respValue{}, err
		}
		if _, err := asked.exchange(EncodeCommand([]string{"ASKING"})); err != nil {
			return respValue{}, err
		}
		return asked.exchange(raw)
	}
	return errorValue("TRYAGAIN the source redirected the command too many times"), nil
}

// planeAnswer reads a topology command's answer from the plane's own node,
// in the client's protocol, naming the plane's endpoints.
func (r *sequentialRelay) planeAnswer(args []string, raw []byte) (respValue, error) {
	if r.plane == nil {
		address, err := r.p.nodeAddress(r.p.Primary())
		if err != nil {
			return respValue{}, err
		}
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			return respValue{}, err
		}
		b := &backendConn{conn: conn, reader: bufio.NewReader(conn)}
		hello := []string{"HELLO", r.protocol}
		if password := r.p.Password(); password != "" {
			user := r.p.cfg.AdminUser
			if user == "" {
				user = "default"
			}
			hello = append(hello, "AUTH", user, password)
		}
		if reply, err := b.exchange(EncodeCommand(hello)); err != nil || reply.kind == '-' {
			_ = conn.Close()
			if err == nil {
				err = errors.New(reply.text)
			}
			return respValue{}, fmt.Errorf("authenticate to the plane: %w", err)
		}
		r.plane = b
	}
	reply, err := r.plane.exchange(raw)
	if err != nil {
		return respValue{}, err
	}
	if r.p.cfg.Cluster && !r.p.announcesHostnames.Load() {
		reply.rewrite(r.p.endpointAddresses())
	}
	return reply, nil
}

// stream hands the connection to a subscription or MONITOR: the command goes
// to the current node and both directions copy from then on.
func (r *sequentialRelay) stream(raw []byte, reader *bufio.Reader) {
	if err := r.writer.Flush(); err != nil {
		return
	}
	backend, err := r.backend(r.current)
	if err != nil {
		return
	}
	if err := backend.send(raw); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(r.client, backend.reader)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(backend.conn, reader)
		done <- struct{}{}
	}()
	<-done
}
