package main

import (
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
	} else if next := defaults.nextSnapshot(now.Add(time.Second)); !next.Equal(now) {
		t.Fatalf("a schedule starting at the current time owes its first snapshot at once, not at %s", next)
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

func TestMSRedisSplitAclRule(t *testing.T) {
	args, err := msRedisSplitAclRule("on  >secret ~app:* (~log:* +get) +@read")
	if err != nil || strings.Join(args, "|") != "on|>secret|~app:*|(~log:* +get)|+@read" {
		t.Fatalf("split = %q, %v", args, err)
	}
	for _, rule := range []string{"on (~a +get", "on ~a) +get"} {
		if _, err := msRedisSplitAclRule(rule); err == nil {
			t.Errorf("rule %q with unbalanced selectors was split", rule)
		}
	}
}

func TestMSRedisValidateAclRules(t *testing.T) {
	if err := msRedisValidateAclRules([]MSRedisAclRule{{Username: "app", Rule: "on >pw ~* +@all"}}); err != nil {
		t.Fatal(err)
	}
	for _, rules := range [][]MSRedisAclRule{
		nil,
		{{Username: "default", Rule: "on nopass"}},
		{{Username: "app", Rule: ""}},
		{{Username: "App", Rule: "on"}, {Username: "app", Rule: "off"}},
		{{Username: "two words", Rule: "on"}},
	} {
		if err := msRedisValidateAclRules(rules); err == nil {
			t.Errorf("rules %+v were accepted", rules)
		}
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
