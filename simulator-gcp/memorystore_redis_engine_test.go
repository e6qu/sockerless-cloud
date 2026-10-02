package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestMSRedisEngineImageCoversEveryRedisVersion(t *testing.T) {
	for version, tag := range map[string]string{
		"REDIS_3_2": "redis:3.2-alpine", "REDIS_4_0": "redis:4.0-alpine", "REDIS_5_0": "redis:5.0-alpine",
		"REDIS_6_X": "redis:6.2-alpine", "REDIS_7_0": "redis:7.0-alpine", "REDIS_7_2": "redis:7.2-alpine",
	} {
		image, ok := msRedisEngineImage(version)
		if !ok || !strings.HasSuffix(image, "/library/"+tag) {
			t.Errorf("%s runs %q (%v), want library/%s", version, image, ok, tag)
		}
	}
	if image, ok := msRedisEngineImage("REDIS_9_9"); ok {
		t.Errorf("an unknown version runs %q", image)
	}
}

func TestMSRedisEngineDirectives(t *testing.T) {
	directives, err := msRedisEngineDirectives(2, map[string]string{
		"maxmemory-policy": "allkeys-lru", "maxmemory-gb": "1.5", "activedefrag": "yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"maxmemory", "1610612736"}, {"activedefrag", "yes"}, {"maxmemory-policy", "allkeys-lru"}}
	if len(directives) != len(want) {
		t.Fatalf("directives = %v, want %v", directives, want)
	}
	for i := range want {
		if directives[i] != want[i] {
			t.Fatalf("directives = %v, want %v", directives, want)
		}
	}

	directives, err = msRedisEngineDirectives(1, nil)
	if err != nil || len(directives) != 1 || directives[0] != [2]string{"maxmemory", "1073741824"} {
		t.Fatalf("memorySizeGb alone gives %v, %v", directives, err)
	}
	if _, err := msRedisEngineDirectives(1, map[string]string{"maxmemory-gb": "lots"}); err == nil {
		t.Fatal("a maxmemory-gb that is not a number was accepted")
	}
}

func TestMSRedisInstanceReplicas(t *testing.T) {
	for _, tc := range []struct {
		tier, mode string
		requested  int
		want       int
	}{
		{"BASIC", "", 0, 0},
		{"STANDARD_HA", "", 0, 1},
		{"STANDARD_HA", "READ_REPLICAS_DISABLED", 0, 1},
		{"STANDARD_HA", "READ_REPLICAS_ENABLED", 0, 2},
		{"STANDARD_HA", "READ_REPLICAS_ENABLED", 4, 4},
	} {
		if got := msRedisInstanceReplicas(tc.tier, tc.mode, tc.requested); got != tc.want {
			t.Errorf("%s/%s/%d runs %d replicas, want %d", tc.tier, tc.mode, tc.requested, got, tc.want)
		}
	}
}

func TestMSRedisReadReply(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(
		"+OK\r\n-ERR wrong\r\n:42\r\n$5\r\nhello\r\n$-1\r\n*2\r\n$1\r\na\r\n:7\r\n"))
	if reply, err := msRedisReadReply(reader); err != nil || reply != "OK" {
		t.Fatalf("status = %v, %v", reply, err)
	}
	if _, err := msRedisReadReply(reader); err == nil || err.Error() != "ERR wrong" {
		t.Fatalf("error reply = %v", err)
	}
	if reply, err := msRedisReadReply(reader); err != nil || reply != int64(42) {
		t.Fatalf("integer = %v, %v", reply, err)
	}
	if reply, err := msRedisReadReply(reader); err != nil || reply != "hello" {
		t.Fatalf("bulk = %v, %v", reply, err)
	}
	if reply, err := msRedisReadReply(reader); err != nil || reply != nil {
		t.Fatalf("null = %v, %v", reply, err)
	}
	reply, err := msRedisReadReply(reader)
	items, ok := reply.([]any)
	if err != nil || !ok || len(items) != 2 || items[0] != "a" || items[1] != int64(7) {
		t.Fatalf("array = %v, %v", reply, err)
	}
}

// A simulator started API-only runs no engine: an instance reports no host,
// keeps its AUTH string, and refuses to move data it does not hold.
func TestMSRedisAPIOnlyInstanceHasNoEndpoint(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "redis.googleapis.com"
	const base = "/v1/projects/api-only/locations/us-central1/instances"
	op := gcpHostOK(t, srv, host, http.MethodPost, base+"?instanceId=modeled", `{"tier":"BASIC","memorySizeGb":1,"authEnabled":true}`)
	instance, _ := op["response"].(map[string]any)
	if _, present := instance["host"]; present {
		t.Fatalf("an instance no engine serves reports host %v", instance["host"])
	}
	auth := gcpHostOK(t, srv, host, http.MethodGet, base+"/modeled/authString", "")
	if value, _ := auth["authString"].(string); value == "" {
		t.Fatalf("authString = %v", auth)
	}
	code, body := gcpHostCall(t, srv, host, http.MethodPost, base+"/modeled:export",
		`{"outputConfig":{"gcsDestination":{"uri":"gs://bucket/snapshot.rdb"}}}`)
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body), "FAILED_PRECONDITION") {
		t.Fatalf("export without an engine answered %d %v", code, body)
	}
}

func TestMSRedisParseObjectURI(t *testing.T) {
	if bucket, object, ok := msRedisParseObjectURI("gs://backups/cache/snap.rdb"); !ok || bucket != "backups" || object != "cache/snap.rdb" {
		t.Fatalf("parsed %q %q %v", bucket, object, ok)
	}
	for _, uri := range []string{"gs://backups", "gs:///snap.rdb", "https://example.com/snap.rdb", "gs://backups/"} {
		if _, _, ok := msRedisParseObjectURI(uri); ok {
			t.Errorf("%q parsed as an object URI", uri)
		}
	}
}

func TestMSRedisSlotRangesCoverTheKeyspace(t *testing.T) {
	for shards := 1; shards <= 7; shards++ {
		next := 0
		for shard := 0; shard < shards; shard++ {
			first, last := msRedisSlotRange(shard, shards)
			if first != next || last < first {
				t.Fatalf("%d shards: shard %d owns %d-%d after %d", shards, shard, first, last, next-1)
			}
			next = last + 1
		}
		if next != msRedisClusterSlots {
			t.Fatalf("%d shards cover slots up to %d", shards, next-1)
		}
	}
}

func TestMSRedisInstanceNodeScripts(t *testing.T) {
	plane := &msRedisPlane{
		volume: "sockerless-memorystore-0123", password: "s3cret", primary: 1,
		nodes:       map[int]*msRedisNode{0: {index: 0}, 1: {index: 1}, 2: {index: 2}},
		persistence: msRedisPersistence{Mode: msRedisPersistenceDisabled},
	}
	primary := plane.script(1)
	if strings.Contains(primary, "rm -rf") || strings.Contains(primary, "slaveof") {
		t.Fatalf("the primary wipes its directory, which may hold a staged snapshot, or replicates:\n%s", primary)
	}
	if !strings.Contains(primary, "exec 'redis-server' '--port' '6379'") || !strings.Contains(primary, "'--requirepass' 's3cret' '--masterauth' 's3cret'") {
		t.Fatalf("the primary does not serve Redis's port with the AUTH string:\n%s", primary)
	}
	if !strings.Contains(primary, "'--save' '' '--appendonly' 'no'") {
		t.Fatalf("the primary persists on the engine's own schedule:\n%s", primary)
	}
	replica := plane.script(2)
	if !strings.Contains(replica, "rm -rf '/data/node-2'") {
		t.Fatalf("a replica keeps a dataset it resynchronises anyway:\n%s", replica)
	}
	if !strings.Contains(replica, "'--slaveof' 'sockerless-memorystore-0123-node-1' '6379'") {
		t.Fatalf("the replica does not follow the primary by its alias:\n%s", replica)
	}
}

func TestMSRedisClusterNodeScripts(t *testing.T) {
	plane := &msRedisPlane{
		cluster: true, volume: "sockerless-memorystore-0123",
		nodes: map[int]*msRedisNode{0: {index: 0, announce: "127.0.0.9"}},
	}
	for mode, want := range map[msRedisPersistence][]string{
		{Mode: msRedisPersistenceDisabled}: {
			"rm -rf '/data/node-0/appendonlydir'", "rm -f '/data/node-0/dump.rdb'", "'--appendonly' 'no'",
		},
		{Mode: msRedisPersistenceRDB, Period: "ONE_HOUR"}: {
			"rm -rf '/data/node-0/appendonlydir'", "'--appendonly' 'no'",
		},
		{Mode: msRedisPersistenceAOF, AppendFsync: "ALWAYS"}: {
			"'--appendonly' 'yes' '--appendfsync' 'always'",
		},
	} {
		plane.persistence = mode
		script := plane.script(0)
		for _, fragment := range append(want,
			"'--cluster-enabled' 'yes'",
			"'--cluster-config-file' '/data/node-0/nodes.conf'",
			"'--cluster-announce-hostname' '127.0.0.9' '--cluster-preferred-endpoint-type' 'hostname'",
		) {
			if !strings.Contains(script, fragment) {
				t.Fatalf("%s cluster node script lacks %s:\n%s", mode.Mode, fragment, script)
			}
		}
		if strings.Contains(script, "rm -rf '/data/node-0'\n") {
			t.Fatalf("a cluster node forgets its identity:\n%s", script)
		}
		if mode.Mode != msRedisPersistenceDisabled && strings.Contains(script, "dump.rdb") {
			t.Fatalf("%s removes the snapshot it restarts from:\n%s", mode.Mode, script)
		}
	}
}

func TestMSRedisParseClusterNodes(t *testing.T) {
	view := msRedisParseClusterNodes(strings.Join([]string{
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
	if replica, ok := view.byID("c"); !ok || replica.PrimaryID != "a" || replica.primary() {
		t.Fatalf("replica = %+v, %v", replica, ok)
	}
}

func TestMSRedisPersistence(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC)
	persistence, err := msRedisInstancePersistence(msRedisPersistence{}, &MSRedisPersistenceConfig{
		PersistenceMode: "RDB", RdbSnapshotPeriod: "SIX_HOURS", RdbSnapshotStartTime: "2026-05-01T06:45:00Z",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if next := persistence.nextSnapshot(now); !next.Equal(time.Date(2026, 5, 1, 12, 45, 0, 0, time.UTC)) {
		t.Fatalf("next snapshot at %s", next)
	}
	future := persistence
	future.Start = now.Add(time.Hour)
	if next := future.nextSnapshot(now); !next.Equal(future.Start) {
		t.Fatalf("a start time still ahead is not the first snapshot: %s", next)
	}
	config := persistence.instanceConfig(now)
	if config.PersistenceMode != "RDB" || config.RdbSnapshotPeriod != "SIX_HOURS" ||
		config.RdbSnapshotStartTime != "2026-05-01T06:45:00Z" || config.RdbNextSnapshotTime != "2026-05-01T12:45:00Z" {
		t.Fatalf("instance reports %+v", config)
	}
	kept, err := msRedisInstancePersistence(persistence, &MSRedisPersistenceConfig{RdbSnapshotPeriod: "ONE_HOUR"}, now)
	if err != nil || kept.Mode != "RDB" || kept.Period != "ONE_HOUR" || !kept.Start.Equal(persistence.Start) {
		t.Fatalf("a period-only update gives %+v, %v", kept, err)
	}
	if defaults, err := msRedisInstancePersistence(msRedisPersistence{}, &MSRedisPersistenceConfig{PersistenceMode: "RDB"}, now); err != nil ||
		defaults.Period != msRedisDefaultSnapshotPeriod || !defaults.Start.Equal(now) {
		t.Fatalf("RDB without a schedule gives %+v, %v", defaults, err)
	}
	if _, err := msRedisInstancePersistence(msRedisPersistence{}, &MSRedisPersistenceConfig{PersistenceMode: "AOF"}, now); err == nil {
		t.Fatal("an instance accepted AOF persistence")
	}
	if _, err := msRedisInstancePersistence(msRedisPersistence{}, &MSRedisPersistenceConfig{PersistenceMode: "RDB", RdbSnapshotPeriod: "TWO_HOURS"}, now); err == nil {
		t.Fatal("an unknown snapshot period was accepted")
	}

	aof, err := msRedisClusterPersistence(msRedisPersistence{}, &MSRedisClusterPersistenceConfig{Mode: "AOF"}, now)
	if err != nil || aof.AppendFsync != "EVERYSEC" {
		t.Fatalf("AOF without fsync gives %+v, %v", aof, err)
	}
	if got := aof.clusterConfig(); got.Mode != "AOF" || got.AofConfig == nil || got.AofConfig.AppendFsync != "EVERYSEC" || got.RdbConfig != nil {
		t.Fatalf("cluster reports %+v", got)
	}
	if _, err := msRedisClusterPersistence(msRedisPersistence{}, &MSRedisClusterPersistenceConfig{Mode: "AOF", AofConfig: &MSRedisAOFConfig{AppendFsync: "SOMETIMES"}}, now); err == nil {
		t.Fatal("an unknown appendFsync was accepted")
	}
	if got := (msRedisPersistence{}).instanceConfig(now); got.PersistenceMode != "DISABLED" {
		t.Fatalf("no persistence reports %+v", got)
	}
}

func TestMSRedisExchangeCredential(t *testing.T) {
	valid := func(token string) bool { return token == "access-token" }
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"AUTH", "access-token"}, []string{"AUTH", "default", "engine"}},
		{[]string{"auth", "someone", "access-token"}, []string{"auth", "default", "engine"}},
		{[]string{"HELLO", "3", "AUTH", "default", "access-token", "SETNAME", "app"}, []string{"HELLO", "3", "AUTH", "default", "engine", "SETNAME", "app"}},
		{[]string{"AUTH", "forged"}, nil},
		{[]string{"HELLO", "3", "AUTH", "default", "forged"}, nil},
		{[]string{"HELLO", "3"}, nil},
		{[]string{"GET", "access-token"}, nil},
	} {
		got, ok := msRedisExchangeCredential(tc.args, "engine", valid)
		if ok != (tc.want != nil) || strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%v became %v (%v), want %v", tc.args, got, ok, tc.want)
		}
	}
}

func TestMSRedisReadCommand(t *testing.T) {
	stream := "*2\r\n$4\r\nAUTH\r\n$5\r\ntoken\r\nPING\r\n*1\r\n$4\r\nQUIT\r\n"
	reader := bufio.NewReader(strings.NewReader(stream))
	args, raw, err := msRedisReadCommand(reader)
	if err != nil || strings.Join(args, " ") != "AUTH token" || string(raw) != "*2\r\n$4\r\nAUTH\r\n$5\r\ntoken\r\n" {
		t.Fatalf("array command = %q %q %v", args, raw, err)
	}
	args, raw, err = msRedisReadCommand(reader)
	if err != nil || strings.Join(args, " ") != "PING" || string(raw) != "PING\r\n" {
		t.Fatalf("inline command = %q %q %v", args, raw, err)
	}
	if args, _, err = msRedisReadCommand(reader); err != nil || strings.Join(args, " ") != "QUIT" {
		t.Fatalf("last command = %q %v", args, err)
	}
	if _, _, err := msRedisReadCommand(bufio.NewReader(strings.NewReader("*1\r\n:1\r\n"))); err == nil {
		t.Fatal("a command whose argument is not a bulk string was read")
	}
}

// The certificate an endpoint presents chains to the CA the API reports and
// names the address the client reached.
func TestMSRedisServerCertificateChainsToReportedCA(t *testing.T) {
	if msRedisCAs == nil {
		msRedisCAs = sim.MakeStore[msRedisCertificateAuthority](nil, "memorystore_redis_certificate_authorities")
	}
	const owner = "projects/p/locations/us-central1/instances/tls"
	config, err := msRedisServerTLSConfig(owner)
	if err != nil {
		t.Fatal(err)
	}
	ca, ok := msRedisCAs.Get(owner)
	if !ok {
		t.Fatal("no CA recorded")
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(ca.tlsCertificate().Cert)) {
		t.Fatal("the reported CA is not PEM")
	}
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("a client trusting the reported CA refused the endpoint: %v", err)
	}
	conn.Close()
	reported := ca.tlsCertificate()
	if reported.SerialNumber == "" || len(reported.SHA1Fingerprint) != 40 || reported.CreateTime == "" || reported.ExpireTime == "" {
		t.Fatalf("serverCaCerts entry = %+v", reported)
	}
	again, err := msRedisEnsureCA(owner)
	if err != nil || again.CertPEM != ca.CertPEM {
		t.Fatal("the CA changed between reads")
	}
}

// A Basic Tier instance has no replica to fail over to.
func TestMSRedisBasicTierFailoverRefused(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	const host = "redis.googleapis.com"
	const base = "/v1/projects/api-only/locations/us-central1/instances"
	gcpHostOK(t, srv, host, http.MethodPost, base+"?instanceId=basic", `{"tier":"BASIC","memorySizeGb":1}`)
	code, body := gcpHostCall(t, srv, host, http.MethodPost, base+"/basic:failover", `{"dataProtectionMode":"LIMITED_DATA_LOSS"}`)
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body), "FAILED_PRECONDITION") {
		t.Fatalf("a Basic Tier failover answered %d %v", code, body)
	}
	code, body = gcpHostCall(t, srv, host, http.MethodPost, base+"?instanceId=extra", `{"tier":"BASIC","memorySizeGb":1,"replicaCount":1}`)
	if code != http.StatusBadRequest || !strings.Contains(fmt.Sprint(body), "INVALID_ARGUMENT") {
		t.Fatalf("a Basic Tier instance with a replica answered %d %v", code, body)
	}
}

func TestMSRedisUpdateMaskReadsSnakeCaseAndNestedPaths(t *testing.T) {
	r := httptest.NewRequest(http.MethodPatch, "/x?updateMask=replica_count,persistence_config.rdb_config.rdb_snapshot_period", nil)
	wants := msRedisUpdateMask(r)
	for _, field := range []string{"replicaCount", "persistenceConfig"} {
		if !wants(field) {
			t.Errorf("the mask does not name %s", field)
		}
	}
	if wants("shardCount") {
		t.Error("the mask names shardCount")
	}
}
