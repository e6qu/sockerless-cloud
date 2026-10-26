package cacheengine

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"strings"
	"testing"
	"time"
)

func TestReadReply(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"+OK\r\n-ERR wrong\r\n:42\r\n$5\r\nhello\r\n$-1\r\n*2\r\n$1\r\na\r\n:7\r\n"))
	if reply, err := ReadReply(reader); err != nil || reply != "OK" {
		t.Fatalf("status = %v, %v", reply, err)
	}
	if _, err := ReadReply(reader); err == nil || err.Error() != "ERR wrong" {
		t.Fatalf("error reply = %v", err)
	}
	if reply, err := ReadReply(reader); err != nil || reply != int64(42) {
		t.Fatalf("integer = %v, %v", reply, err)
	}
	if reply, err := ReadReply(reader); err != nil || reply != "hello" {
		t.Fatalf("bulk = %v, %v", reply, err)
	}
	if reply, err := ReadReply(reader); err != nil || reply != nil {
		t.Fatalf("null = %v, %v", reply, err)
	}
	reply, err := ReadReply(reader)
	items, ok := reply.([]any)
	if err != nil || !ok || len(items) != 2 || items[0] != "a" || items[1] != int64(7) {
		t.Fatalf("array = %v, %v", reply, err)
	}
}

func TestReadCommand(t *testing.T) {
	stream := "*2\r\n$4\r\nAUTH\r\n$5\r\ntoken\r\nPING\r\n*1\r\n$4\r\nQUIT\r\n"
	reader := bufio.NewReader(strings.NewReader(stream))
	args, raw, err := ReadCommand(reader)
	if err != nil || strings.Join(args, " ") != "AUTH token" || string(raw) != "*2\r\n$4\r\nAUTH\r\n$5\r\ntoken\r\n" {
		t.Fatalf("array command = %q %q %v", args, raw, err)
	}
	args, raw, err = ReadCommand(reader)
	if err != nil || strings.Join(args, " ") != "PING" || string(raw) != "PING\r\n" {
		t.Fatalf("inline command = %q %q %v", args, raw, err)
	}
	if args, _, err = ReadCommand(reader); err != nil || strings.Join(args, " ") != "QUIT" {
		t.Fatalf("last command = %q %v", args, err)
	}
	if _, _, err := ReadCommand(bufio.NewReader(strings.NewReader("*1\r\n:1\r\n"))); err == nil {
		t.Fatal("a command whose argument is not a bulk string was read")
	}
}

func TestExchangeCredential(t *testing.T) {
	valid := func(token string) (string, bool) {
		switch token {
		case "access-token":
			return "default", true
		case "policy-token":
			return "app@example.iam.gserviceaccount.com", true
		}
		return "", false
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"AUTH", "access-token"}, []string{"AUTH", "default", "engine"}},
		{[]string{"auth", "someone", "access-token"}, []string{"auth", "default", "engine"}},
		{[]string{"AUTH", "policy-token"}, []string{"AUTH", "app@example.iam.gserviceaccount.com", "engine"}},
		{[]string{"HELLO", "3", "AUTH", "default", "policy-token"}, []string{"HELLO", "3", "AUTH", "app@example.iam.gserviceaccount.com", "engine"}},
		{[]string{"HELLO", "3", "AUTH", "default", "access-token", "SETNAME", "app"}, []string{"HELLO", "3", "AUTH", "default", "engine", "SETNAME", "app"}},
		{[]string{"AUTH", "forged"}, nil},
		{[]string{"HELLO", "3", "AUTH", "default", "forged"}, nil},
		{[]string{"HELLO", "3"}, nil},
		{[]string{"GET", "access-token"}, nil},
	} {
		got, ok := ExchangeCredential(tc.args, "engine", valid)
		if ok != (tc.want != nil) || strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%v became %v (%v), want %v", tc.args, got, ok, tc.want)
		}
	}
}

func TestParseClusterNodes(t *testing.T) {
	view := ParseClusterNodes(strings.Join([]string{
		"a 172.18.0.2:6379@16379,127.0.0.9 myself,master - 0 0 1 connected 0-8191",
		"b 172.18.0.3:6379@16379,127.0.0.10 master - 0 0 2 connected 8192-16383",
		"c 172.18.0.4:6379@16379,127.0.0.11 slave a 0 0 1 connected",
		"d 172.18.0.5:6379@16379,127.0.0.12 slave b 0 0 2 disconnected",
		"e 172.18.0.6:6379@16379 master,fail - 0 0 3 connected",
	}, "\n"))
	if primaries, replicas := view.countRoles(); primaries != 2 || replicas != 1 {
		t.Fatalf("counted %d primaries and %d replicas", primaries, replicas)
	}
	self, ok := view.self()
	if !ok || self.ID != "a" || self.IP != "172.18.0.2" || self.Hostname != "127.0.0.9" || len(self.Slots) != 1 {
		t.Fatalf("self = %+v, %v", self, ok)
	}
	if replica, ok := view.byID("c"); !ok || replica.PrimaryID != "a" || replica.Primary() {
		t.Fatalf("replica = %+v, %v", replica, ok)
	}
}

func TestSlotRangesCoverTheKeyspace(t *testing.T) {
	for shards := 1; shards <= 7; shards++ {
		next := 0
		for shard := 0; shard < shards; shard++ {
			first, last := SlotRange(shard, shards)
			if first != next || last < first {
				t.Fatalf("%d shards: shard %d owns %d-%d after %d", shards, shard, first, last, next-1)
			}
			next = last + 1
		}
		if next != ClusterSlots {
			t.Fatalf("%d shards cover slots up to %d", shards, next-1)
		}
	}
}

// The slots Redis Cluster's own specification gives for its examples.
func TestKeySlotHashesTheKeyOrItsHashTag(t *testing.T) {
	for key, want := range map[string]int{
		"123456789":            12739,
		"foo":                  12182,
		"{user1000}.following": KeySlot("user1000"),
		"foo{}{bar}":           KeySlot("foo{}{bar}"),
		"{}foo":                KeySlot("{}foo"),
	} {
		if got := KeySlot(key); got != want {
			t.Errorf("KeySlot(%q) = %d, want %d", key, got, want)
		}
	}
	if KeySlot("foo{}{bar}") == KeySlot("bar") {
		t.Error("an empty first hash tag hashed the second one")
	}
}

func TestPlanSlotMovesGivesEveryPrimaryAnEqualShare(t *testing.T) {
	all := func(low, high int) []int {
		var slots []int
		for s := low; s <= high; s++ {
			slots = append(slots, s)
		}
		return slots
	}
	cases := []struct {
		name      string
		primaries []int
		donors    []int
		owned     map[int][]int
	}{
		{"grow 1 to 2", []int{0, 1}, nil, map[int][]int{0: all(0, 16383)}},
		{"grow 2 to 3", []int{0, 1, 2}, nil, map[int][]int{0: all(0, 8191), 1: all(8192, 16383)}},
		{"shrink 3 to 2", []int{0, 1}, []int{2}, map[int][]int{0: all(0, 5461), 1: all(5462, 10922), 2: all(10923, 16383)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			owner := map[int]int{}
			for node, slots := range c.owned {
				for _, s := range slots {
					owner[s] = node
				}
			}
			for _, move := range PlanSlotMoves(c.primaries, c.donors, c.owned) {
				for _, s := range move.Slots {
					if owner[s] != move.From {
						t.Fatalf("slot %d moves from node %d, which does not own it", s, move.From)
					}
					owner[s] = move.To
				}
			}
			count := map[int]int{}
			for s := 0; s < ClusterSlots; s++ {
				count[owner[s]]++
			}
			for _, d := range c.donors {
				if count[d] != 0 {
					t.Fatalf("donor %d still owns %d slots", d, count[d])
				}
			}
			for i, node := range c.primaries {
				want := ClusterSlots / len(c.primaries)
				if i < ClusterSlots%len(c.primaries) {
					want++
				}
				if count[node] != want {
					t.Fatalf("node %d owns %d slots, want %d", node, count[node], want)
				}
			}
		})
	}
}

func TestInstanceNodeScripts(t *testing.T) {
	plane := New(Config{Volume: "sockerless-memorystore-0123", Password: "s3cret"},
		Record{Nodes: []int{0, 1, 2}, Primary: 1})
	primary := plane.script(1)
	if strings.Contains(primary, "rm -rf") || strings.Contains(primary, "slaveof") {
		t.Fatalf("the primary wipes its directory, which may hold a staged snapshot, or replicates:\n%s", primary)
	}
	if !strings.Contains(primary, "exec 'redis-server' '--port' '6379'") || !strings.Contains(primary, "'--requirepass' 's3cret' '--masterauth' 's3cret'") {
		t.Fatalf("the primary does not serve Redis's port with the AUTH string:\n%s", primary)
	}
	if !strings.Contains(primary, "'--save' ''") || !strings.Contains(primary, "'--appendonly' 'no'") {
		t.Fatalf("the primary persists on the engine's own schedule:\n%s", primary)
	}
	if !strings.Contains(primary, "'--repl-diskless-sync-delay' '0'") {
		t.Fatalf("the primary holds a replica's synchronisation for others to join:\n%s", primary)
	}
	replica := plane.script(2)
	if !strings.Contains(replica, "rm -rf '/data/node-2'") {
		t.Fatalf("a replica keeps a dataset it resynchronises anyway:\n%s", replica)
	}
	if !strings.Contains(replica, "'--slaveof' 'sockerless-memorystore-0123-node-1' '6379'") {
		t.Fatalf("the replica does not follow the primary by its alias:\n%s", replica)
	}
}

func TestClusterNodeScripts(t *testing.T) {
	for name, tc := range map[string]struct {
		persistence Persistence
		want        []string
	}{
		"disabled": {Persistence{}, []string{
			"rm -rf '/data/node-0/appendonlydir'", "rm -f '/data/node-0/dump.rdb'", "'--appendonly' 'no'",
		}},
		"rdb": {Persistence{RDB: true}, []string{"rm -rf '/data/node-0/appendonlydir'", "'--appendonly' 'no'"}},
		"aof": {Persistence{AOF: true, AppendFsync: "always"}, []string{"'--appendonly' 'yes' '--appendfsync' 'always'"}},
	} {
		plane := New(Config{Cluster: true, Volume: "sockerless-memorystore-0123", Persistence: tc.persistence},
			Record{Nodes: []int{0}, Endpoints: []Endpoint{{Role: NodeEndpoint, Address: "127.0.0.9", Node: 0}}})
		script := plane.script(0)
		for _, fragment := range append(tc.want,
			"'--cluster-enabled' 'yes'",
			"'--cluster-config-file' '/data/node-0/nodes.conf'",
			"'--cluster-announce-hostname' '127.0.0.9' '--cluster-preferred-endpoint-type' 'hostname'",
		) {
			if !strings.Contains(script, fragment) {
				t.Fatalf("%s cluster node script lacks %s:\n%s", name, fragment, script)
			}
		}
		if strings.Contains(script, "rm -rf '/data/node-0'\n") {
			t.Fatalf("a cluster node forgets its identity:\n%s", script)
		}
		if name != "disabled" && strings.Contains(script, "dump.rdb") {
			t.Fatalf("%s removes the snapshot it restarts from:\n%s", name, script)
		}
	}
}

// The certificate an endpoint presents chains to its authority and names the
// address the client reached, and the name it asked for.
func TestServerTLSChainsToTheAuthority(t *testing.T) {
	authority, err := NewAuthority(pkix.Name{CommonName: "Cache CA"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ServerTLS(authority.CertPEM, authority.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(authority.CertPEM)) {
		t.Fatal("the authority is not PEM")
	}
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("a client trusting the authority refused the address: %v", err)
	}
	conn.Close()
	conn, err = tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, ServerName: "cache.example.net", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("a client trusting the authority refused the name it asked for: %v", err)
	}
	conn.Close()
}

// An endpoint that prefers TLS serves a client that opens with a TLS
// handshake over TLS and one that opens with a command in plaintext.
func TestTerminateTLSPreferredServesBoth(t *testing.T) {
	authority, err := NewAuthority(pkix.Name{CommonName: "Cache CA"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ServerTLS(authority.CertPEM, authority.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				served, ok := terminateTLS(conn, config, true)
				if !ok {
					return
				}
				line, err := bufio.NewReader(served).ReadString('\n')
				if err == nil {
					_, _ = served.Write([]byte("+" + strings.TrimSpace(line) + "\r\n"))
				}
			}()
		}
	}()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(authority.CertPEM))
	secured, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer secured.Close()
	plain, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	for name, conn := range map[string]net.Conn{"tls": secured, "plaintext": plain} {
		if _, err := conn.Write([]byte("PING " + name + "\r\n")); err != nil {
			t.Fatal(err)
		}
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil || reply != "+PING "+name+"\r\n" {
			t.Fatalf("%s client read %q, %v", name, reply, err)
		}
	}
}
