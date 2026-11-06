package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// An online migration reads the customer's source the way a replica does:
// PSYNC hands over an RDB file of the dataset and then the stream of every
// write the source takes, which the migration applies to the target until
// cutover.

const (
	ecSourceDialTimeout = 10 * time.Second
	// A full synchronisation waits while the source forks and writes its RDB
	// file.
	ecFullSyncDeadline = 10 * time.Minute
	// A replica acknowledges its offset once a second, as redis-server's
	// replication cron does.
	ecReplicaAckPeriod = time.Second
)

// How the migration reaches one node of the customer's source.
type ecMigrationSource struct {
	Address        string
	Port           int
	TLSMode        string
	TLSCertificate string
	User           string
	Password       string
}

func (s ecMigrationSource) endpoint() string {
	port := s.Port
	if port == 0 {
		port = ecRedisPort
	}
	return net.JoinHostPort(s.Address, strconv.Itoa(port))
}

func (s ecMigrationSource) at(address string, port int) ecMigrationSource {
	s.Address, s.Port = address, port
	return s
}

// ElastiCache checks that the source's certificate chains to a CA the
// customer named; it does not check the host name.
func (s ecMigrationSource) tlsConfig() (*tls.Config, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(s.TLSCertificate)) {
		return nil, errors.New("SourceTlsCertificate is not a PEM certificate")
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection verifies the chain; ElastiCache skips the host name
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("the source presented no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}
			_, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates})
			return err
		},
	}, nil
}

func (s ecMigrationSource) dialRaw() (net.Conn, error) {
	if s.TLSMode != "verify-ca" {
		return net.DialTimeout("tcp", s.endpoint(), ecSourceDialTimeout)
	}
	config, err := s.tlsConfig()
	if err != nil {
		return nil, err
	}
	return tls.DialWithDialer(&net.Dialer{Timeout: ecSourceDialTimeout}, "tcp", s.endpoint(), config)
}

// An authenticated connection that answered PING.
func (s ecMigrationSource) dial() (*cacheengine.Conn, error) {
	var conn *cacheengine.Conn
	var err error
	if s.TLSMode == "verify-ca" {
		config, configErr := s.tlsConfig()
		if configErr != nil {
			return nil, configErr
		}
		conn, err = cacheengine.DialTLS(s.endpoint(), config, s.User, s.Password, ecSourceDialTimeout)
	} else {
		conn, err = cacheengine.Dial(s.endpoint(), "", ecSourceDialTimeout)
		if err == nil && s.Password != "" {
			args := []string{"AUTH", s.Password}
			if s.User != "" {
				args = []string{"AUTH", s.User, s.Password}
			}
			if _, err = conn.Do(args...); err != nil {
				_ = conn.Close()
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if reply, err := conn.Do("PING"); err != nil || reply != "PONG" {
		_ = conn.Close()
		return nil, fmt.Errorf("PING answered %v, %v", reply, err)
	}
	return conn, nil
}

// The primary of every shard of a cluster-mode source, as CLUSTER SLOTS
// names them.
func ecSourcePrimaries(conn *cacheengine.Conn, seed ecMigrationSource) ([]ecMigrationSource, error) {
	reply, err := conn.Do("CLUSTER", "SLOTS")
	if err != nil {
		return nil, err
	}
	ranges, _ := reply.([]any)
	seen := map[string]bool{}
	var primaries []ecMigrationSource
	for _, item := range ranges {
		entry, _ := item.([]any)
		if len(entry) < 3 {
			return nil, fmt.Errorf("the source answered CLUSTER SLOTS with %v", item)
		}
		node, _ := entry[2].([]any)
		if len(node) < 2 {
			return nil, fmt.Errorf("the source answered CLUSTER SLOTS with %v", item)
		}
		host := fmt.Sprint(node[0])
		port, _ := node[1].(int64)
		if host == "" || host == "?" {
			host = seed.Address
		}
		primary := seed.at(host, int(port))
		if !seen[primary.endpoint()] {
			seen[primary.endpoint()] = true
			primaries = append(primaries, primary)
		}
	}
	if len(primaries) == 0 {
		return nil, errors.New("the source's hash slots are unassigned")
	}
	return primaries, nil
}

// ecSourceSlotLayout is the hash slots each primary of a cluster-mode source
// owns, ordered by each primary's lowest slot.
func ecSourceSlotLayout(conn *cacheengine.Conn) ([][]int, error) {
	reply, err := conn.Do("CLUSTER", "SLOTS")
	if err != nil {
		return nil, err
	}
	ranges, _ := reply.([]any)
	byPrimary := map[string][]int{}
	for _, item := range ranges {
		entry, _ := item.([]any)
		if len(entry) < 3 {
			return nil, fmt.Errorf("the source answered CLUSTER SLOTS with %v", item)
		}
		first, _ := entry[0].(int64)
		last, _ := entry[1].(int64)
		node, _ := entry[2].([]any)
		if len(node) < 2 {
			return nil, fmt.Errorf("the source answered CLUSTER SLOTS with %v", item)
		}
		owner := fmt.Sprint(node[0], ":", node[1])
		for slot := int(first); slot <= int(last); slot++ {
			byPrimary[owner] = append(byPrimary[owner], slot)
		}
	}
	layout := make([][]int, 0, len(byPrimary))
	for _, slots := range byPrimary {
		slices.Sort(slots)
		layout = append(layout, slots)
	}
	slices.SortFunc(layout, func(a, b []int) int { return a[0] - b[0] })
	return layout, nil
}

// One replication link to one source primary.
type ecReplicationLink struct {
	source  ecMigrationSource
	conn    net.Conn
	reader  *bufio.Reader
	writeMu sync.Mutex
	offset  atomic.Int64
}

func (l *ecReplicationLink) send(args ...string) error {
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_, err := l.conn.Write(cacheengine.EncodeCommand(args))
	return err
}

// Opens the link with a full synchronisation and returns the RDB file the
// source sent.
func ecOpenReplicationLink(source ecMigrationSource) (*ecReplicationLink, []byte, error) {
	conn, err := source.dialRaw()
	if err != nil {
		return nil, nil, err
	}
	link := &ecReplicationLink{source: source, conn: conn, reader: bufio.NewReader(conn)}
	fail := func(err error) (*ecReplicationLink, []byte, error) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("replicate from %s: %w", source.endpoint(), err)
	}
	if err := conn.SetDeadline(time.Now().Add(ecFullSyncDeadline)); err != nil {
		return fail(err)
	}
	request := func(args ...string) (any, error) {
		if err := link.send(args...); err != nil {
			return nil, err
		}
		return cacheengine.ReadReply(link.reader)
	}
	if source.Password != "" {
		args := []string{"AUTH", source.Password}
		if source.User != "" {
			args = []string{"AUTH", source.User, source.Password}
		}
		if _, err := request(args...); err != nil {
			return fail(err)
		}
	}
	if _, err := request("REPLCONF", "capa", "psync2"); err != nil {
		return fail(err)
	}
	reply, err := request("PSYNC", "?", "-1")
	if err != nil {
		return fail(err)
	}
	fields := strings.Fields(fmt.Sprint(reply))
	if len(fields) != 3 || fields[0] != "FULLRESYNC" {
		return fail(fmt.Errorf("PSYNC answered %v", reply))
	}
	offset, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return fail(fmt.Errorf("PSYNC answered offset %q", fields[2]))
	}
	rdb, err := ecReadRDBTransfer(link.reader)
	if err != nil {
		return fail(err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	link.offset.Store(offset)
	return link, rdb, nil
}

// The source sends newlines while it writes the file, then $<length> and the
// file's bytes with no trailing CRLF.
func ecReadRDBTransfer(reader *bufio.Reader) ([]byte, error) {
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "$") {
			return nil, fmt.Errorf("the source opened its RDB transfer with %q", line)
		}
		size, err := strconv.Atoi(line[1:])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("the source announced an RDB file of %q bytes", line[1:])
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		if !cacheengine.IsRDB(data) {
			return nil, errors.New("the source sent no RDB file")
		}
		return data, nil
	}
}

// Applies every write the source propagates until the connection ends;
// progress runs after each one.
func (l *ecReplicationLink) stream(apply func([]string) error, progress func()) error {
	ticker := time.NewTicker(ecReplicaAckPeriod)
	done := make(chan struct{})
	defer close(done)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if l.send("REPLCONF", "ACK", strconv.FormatInt(l.offset.Load(), 10)) != nil {
					return
				}
			}
		}
	}()
	db := "0"
	for {
		args, raw, err := cacheengine.ReadCommand(l.reader)
		if err != nil {
			return err
		}
		if len(args) > 0 {
			switch strings.ToUpper(args[0]) {
			case "PING":
			case "REPLCONF":
				if len(args) > 1 && strings.EqualFold(args[1], "GETACK") {
					if err := l.send("REPLCONF", "ACK", strconv.FormatInt(l.offset.Load(), 10)); err != nil {
						return err
					}
				}
			case "SELECT":
				if len(args) > 1 {
					db = args[1]
				}
			case "MULTI", "EXEC":
			default:
				if db != "0" {
					return fmt.Errorf("the source wrote to database %s; a cluster-mode target holds database 0 only", db)
				}
				if err := apply(args); err != nil {
					return err
				}
			}
		}
		l.offset.Add(int64(len(raw)))
		progress()
	}
}

// Where a migration writes: the target's primaries and the slots each owns.
type ecMigrationTarget struct {
	engine    *ecEngine
	primaries []int
	owners    [cacheengine.ClusterSlots]int

	mu    sync.Mutex
	conns map[int]*cacheengine.Conn
}

func ecNewMigrationTarget(engine *ecEngine) (*ecMigrationTarget, error) {
	target := &ecMigrationTarget{engine: engine}
	slots, err := engine.redis.ShardSlots()
	if err != nil {
		return nil, err
	}
	for node, fields := range slots {
		list, err := cacheengine.SlotList(fields)
		if err != nil {
			return nil, err
		}
		target.primaries = append(target.primaries, node)
		for _, slot := range list {
			target.owners[slot] = node
		}
	}
	if len(target.primaries) == 0 {
		return nil, errors.New("the target owns no hash slots")
	}
	return target, nil
}

// do runs commands on node over one connection the target keeps open, and
// reopens it once when it has broken.
func (t *ecMigrationTarget) do(node int, commands [][]string) ([]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for attempt := 0; ; attempt++ {
		conn, ok := t.conns[node]
		if !ok {
			var err error
			if conn, err = t.engine.redis.Dial(node); err != nil {
				return nil, err
			}
			if t.conns == nil {
				t.conns = map[int]*cacheengine.Conn{}
			}
			t.conns[node] = conn
		}
		replies, err := conn.Pipeline(commands)
		var refused cacheengine.Error
		if err == nil || errors.As(err, &refused) || attempt > 0 {
			return replies, err
		}
		_ = conn.Close()
		delete(t.conns, node)
	}
}

func (t *ecMigrationTarget) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for node, conn := range t.conns {
		_ = conn.Close()
		delete(t.conns, node)
	}
}

// owner is the primary that owns the first key of a command, or false for a
// keyless command.
func (t *ecMigrationTarget) owner(args []string) (int, bool, error) {
	if len(t.primaries) == 1 {
		return t.primaries[0], true, nil
	}
	replies, err := t.do(t.primaries[0], [][]string{append([]string{"COMMAND", "GETKEYS"}, args...)})
	var refused cacheengine.Error
	if errors.As(err, &refused) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	keys, _ := replies[0].([]any)
	if len(keys) == 0 {
		return 0, false, nil
	}
	return t.owners[cacheengine.KeySlot(fmt.Sprint(keys[0]))], true, nil
}

// apply runs a keyed command on the primary that owns its first key's slot
// and a keyless one on every primary.
func (t *ecMigrationTarget) apply(args []string) error {
	node, keyed, err := t.owner(args)
	if err != nil {
		return err
	}
	nodes := t.primaries
	if keyed {
		nodes = []int{node}
	}
	for _, node := range nodes {
		if _, err := t.do(node, [][]string{args}); err != nil {
			return err
		}
	}
	return nil
}

// applier is the apply of one replication link: a MULTI ... EXEC block the
// source propagates runs as one transaction on one connection.
func (t *ecMigrationTarget) applier() func([]string) error {
	var queued [][]string
	inTx := false
	return func(args []string) error {
		if len(args) == 0 {
			return nil
		}
		switch strings.ToUpper(args[0]) {
		case "MULTI":
			inTx, queued = true, nil
			return nil
		case "EXEC":
			if !inTx {
				return nil
			}
			inTx = false
			return t.transact(queued)
		}
		if inTx {
			queued = append(queued, args)
			return nil
		}
		return t.apply(args)
	}
}

func (t *ecMigrationTarget) transact(commands [][]string) error {
	if len(commands) == 0 {
		return nil
	}
	node := t.primaries[0]
	for _, args := range commands {
		owner, keyed, err := t.owner(args)
		if err != nil {
			return err
		}
		if keyed {
			node = owner
			break
		}
	}
	block := append(append([][]string{{"MULTI"}}, commands...), []string{"EXEC"})
	_, err := t.do(node, block)
	return err
}

func (t *ecMigrationTarget) empty() (bool, error) {
	for _, node := range t.primaries {
		reply, err := t.engine.redis.Command(node, "DBSIZE")
		if err != nil {
			return false, err
		}
		if size, _ := reply.(int64); size > 0 {
			return false, nil
		}
	}
	return true, nil
}

// One running data sync: a link to each source primary.
type ecMigrationRun struct {
	target   *ecMigrationTarget
	links    []*ecReplicationLink
	stopping atomic.Bool
	mu       sync.RWMutex
	progress chan struct{}
	failed   chan struct{}
	failure  error
	watches  []*bg.Watch
}

var ecMigrationRuns sync.Map // engine key -> *ecMigrationRun

func ecLoadMigrationRun(key string) (*ecMigrationRun, bool) {
	value, ok := ecMigrationRuns.Load(key)
	if !ok {
		return nil, false
	}
	run, ok := value.(*ecMigrationRun)
	return run, ok
}

// Opens a link to every primary of the source, loads each one's RDB file
// into the target and then follows its writes. onFail runs once when a link
// breaks before Stop.
func ecStartMigrationRun(key string, target *ecMigrationTarget, seed ecMigrationSource, onFail func(error)) (*ecMigrationRun, error) {
	control, err := seed.dial()
	if err != nil {
		return nil, fmt.Errorf("connect to the source: %w", err)
	}
	primaries, err := ecSourcePrimaries(control, seed)
	_ = control.Close()
	if err != nil {
		return nil, err
	}
	run := &ecMigrationRun{target: target, progress: make(chan struct{}), failed: make(chan struct{})}
	// Each link follows its primary as soon as its file has loaded, so the
	// source's replication timeout never runs out while the next shard loads.
	for _, primary := range primaries {
		link, rdb, err := ecOpenReplicationLink(primary)
		if err == nil {
			run.links = append(run.links, link)
			err = target.engine.redis.ImportRDB(cacheengine.ImportSource{Name: primary.endpoint(), Data: rdb})
		}
		if err != nil {
			run.Stop()
			return nil, err
		}
		var streamErr error
		apply := target.applier()
		watch := bg.WatchThen(func() {
			streamErr = link.stream(apply, run.advanced)
		}, func() {
			if run.stopping.Load() {
				return
			}
			run.fail(streamErr)
			onFail(streamErr)
		})
		run.watches = append(run.watches, watch)
	}
	ecMigrationRuns.Store(key, run)
	return run, nil
}

func (m *ecMigrationRun) advanced() {
	m.mu.Lock()
	close(m.progress)
	m.progress = make(chan struct{})
	m.mu.Unlock()
}

func (m *ecMigrationRun) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failure == nil {
		m.failure = err
		close(m.failed)
	}
}

func (m *ecMigrationRun) closeLinks() {
	for _, link := range m.links {
		_ = link.conn.Close()
	}
}

// Ends every link and waits for its stream to return.
func (m *ecMigrationRun) Stop() {
	m.stopping.Store(true)
	m.closeLinks()
	for _, watch := range m.watches {
		if watch != nil {
			<-watch.Done()
		}
	}
	m.target.close()
}

func ecStopMigrationRun(key string) {
	if value, ok := ecMigrationRuns.LoadAndDelete(key); ok {
		if run, ok := value.(*ecMigrationRun); ok {
			run.Stop()
		}
	}
}

// Pauses the source's writers, waits until every link has applied what its
// primary had taken by then and stops the sync; switchOver then moves the
// clients to the target while the writers still wait, and the source's
// clients are disconnected and its writers released. The cutover fails when
// a link does not catch up within pause.
func (m *ecMigrationRun) cutover(pause time.Duration, switchOver func()) error {
	type paused struct {
		conn      *cacheengine.Conn
		writeOnly bool
		offset    int64
	}
	var sources []paused
	release := func() {
		for _, source := range sources {
			if source.writeOnly {
				_, _ = source.conn.Do("CLIENT", "UNPAUSE")
			}
			_ = source.conn.Close()
		}
	}
	for _, link := range m.links {
		conn, err := link.source.dial()
		if err != nil {
			release()
			return err
		}
		info, err := conn.Do("INFO", "server")
		if err != nil {
			_ = conn.Close()
			release()
			return err
		}
		writeOnly := ecVersionAtLeast(ecInfoVersion(fmt.Sprint(info)), "6.2")
		pauseArgs := []string{"CLIENT", "PAUSE", strconv.FormatInt(pause.Milliseconds(), 10)}
		if writeOnly {
			pauseArgs = append(pauseArgs, "WRITE")
		}
		replies, err := conn.Pipeline([][]string{{"MULTI"}, pauseArgs, {"INFO", "replication"}, {"EXEC"}})
		if err != nil {
			_ = conn.Close()
			release()
			return err
		}
		executed, _ := replies[3].([]any)
		if len(executed) != 2 {
			_ = conn.Close()
			release()
			return fmt.Errorf("the source answered EXEC with %v", replies[3])
		}
		offset, err := strconv.ParseInt(cacheengine.ParseInfo(fmt.Sprint(executed[1]))["master_repl_offset"], 10, 64)
		if err != nil {
			_ = conn.Close()
			release()
			return fmt.Errorf("the source reports no master_repl_offset: %w", err)
		}
		sources = append(sources, paused{conn: conn, writeOnly: writeOnly, offset: offset})
	}
	deadline := time.NewTimer(pause)
	defer deadline.Stop()
	for i, link := range m.links {
		for link.offset.Load() < sources[i].offset {
			m.mu.RLock()
			progress := m.progress
			m.mu.RUnlock()
			if link.offset.Load() >= sources[i].offset {
				break
			}
			select {
			case <-progress:
			case <-m.failed:
				release()
				return m.failure
			case <-deadline.C:
				release()
				return errors.New("a fully synced window to finalize the migration was not found")
			}
		}
	}
	m.Stop()
	switchOver()
	for _, source := range sources {
		if source.writeOnly {
			_, _ = source.conn.Do("CLIENT", "KILL", "TYPE", "normal")
		}
	}
	release()
	return nil
}

// valkey_version where the engine reports one, else redis_version.
func ecInfoVersion(info string) string {
	fields := cacheengine.ParseInfo(info)
	if version := fields["valkey_version"]; version != "" {
		return version
	}
	return fields["redis_version"]
}

func ecVersionAtLeast(version, floor string) bool {
	return ecCompareVersions(version, floor) >= 0
}

func ecCompareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < max(len(as), len(bs)); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// A listener the engine's nodes reach through the address workloads call
// the simulator back on, relaying each connection to the source: the
// source's address is the customer's, which the engine network does not
// route.
type ecSourceRelay struct {
	listener net.Listener
	host     string
	port     int
}

func ecOpenSourceRelay(source ecMigrationSource) (*ecSourceRelay, error) {
	host, err := workloadhost.CallbackHost()
	if err != nil {
		return nil, err
	}
	bind := host
	if net.ParseIP(host) == nil {
		bind = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, fmt.Errorf("open the migration relay: %w", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("the migration relay listens at %v", listener.Addr())
	}
	relay := &ecSourceRelay{listener: listener, host: host, port: address.Port}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				upstream, err := source.dialRaw()
				if err != nil {
					log.Printf("Amazon ElastiCache migration: reach the source %s: %v", source.endpoint(), err)
					return
				}
				defer upstream.Close()
				cacheengine.Relay(client, upstream)
			}()
		}
	}()
	return relay, nil
}

func (r *ecSourceRelay) Close() { _ = r.listener.Close() }
