package main

import (
	"bufio"
	"fmt"
	"net/http"
	"strings"
	"testing"
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

func TestMSRedisTopologySlotsCoverTheKeyspace(t *testing.T) {
	for shards := 1; shards <= 7; shards++ {
		topology := msRedisTopology{Cluster: true, Shards: shards, Replicas: 2}
		next := 0
		for shard := 0; shard < shards; shard++ {
			first, last := topology.slotRange(shard)
			if first != next || last < first {
				t.Fatalf("%d shards: shard %d owns %d-%d after %d", shards, shard, first, last, next-1)
			}
			next = last + 1
		}
		if next != msRedisClusterSlots {
			t.Fatalf("%d shards cover slots up to %d", shards, next-1)
		}
		if topology.nodes() != shards*3 {
			t.Fatalf("%d shards with two replicas run %d nodes", shards, topology.nodes())
		}
		for node := shards; node < topology.nodes(); node++ {
			if primary := topology.replicaOf(node); primary != (node-shards)/2 {
				t.Fatalf("replica node %d follows %d", node, primary)
			}
		}
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

func TestMSRedisScriptRunsEveryNode(t *testing.T) {
	plane := &msRedisPlane{topology: msRedisTopology{Replicas: 2}, password: "s3cret"}
	script := plane.script(1)
	lines := strings.Split(strings.TrimSpace(script), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "exec 'redis-server' '--port' '7001'") {
		t.Fatalf("the primary does not replace the shell:\n%s", script)
	}
	if strings.Contains(last, "slaveof") {
		t.Fatalf("the primary replicates:\n%s", last)
	}
	for _, port := range []string{"7000", "7002"} {
		if !strings.Contains(script, "'--port' '"+port+"'") {
			t.Fatalf("no node on %s:\n%s", port, script)
		}
	}
	if strings.Count(script, "'--slaveof' '127.0.0.1' '7001'") != 2 {
		t.Fatalf("the replicas do not follow the primary:\n%s", script)
	}
	if strings.Count(script, "'--requirepass' 's3cret' '--masterauth' 's3cret'") != 3 {
		t.Fatalf("not every node requires the AUTH string:\n%s", script)
	}
	if strings.Contains(script, "rm -rf '/data/node-1'") {
		t.Fatalf("the primary's directory, which may hold a staged snapshot, is wiped:\n%s", script)
	}

	cluster := &msRedisPlane{topology: msRedisTopology{Cluster: true, Shards: 1, Replicas: 1, Announce: []string{"127.0.0.9", "127.0.0.10"}}}
	script = cluster.script(0)
	for _, want := range []string{
		"'--cluster-announce-ip' '127.0.0.10' '--cluster-announce-port' '7001' '--cluster-announce-bus-port' '17001'",
		"'--cluster-enabled' 'yes'",
		"rm -rf '/data/node-0'",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("cluster script lacks %s:\n%s", want, script)
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

func TestMSRedisCountRoles(t *testing.T) {
	nodes := strings.Join([]string{
		"a 127.0.0.2:7000@17000 myself,master - 0 0 1 connected 0-8191",
		"b 127.0.0.3:7001@17001 master - 0 0 2 connected 8192-16383",
		"c 127.0.0.4:7002@17002 slave a 0 0 1 connected",
		"d 127.0.0.5:7003@17003 slave b 0 0 2 disconnected",
	}, "\n")
	if primaries, replicas := msRedisCountRoles(nodes); primaries != 2 || replicas != 1 {
		t.Fatalf("counted %d primaries and %d replicas", primaries, replicas)
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
