package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func withRoute53HealthStores(t *testing.T) {
	t.Helper()
	checks, cidrs := r53HealthChecks, r53CidrCollections
	t.Cleanup(func() { r53HealthChecks, r53CidrCollections = checks, cidrs })
	r53HealthChecks = sim.MakeStore[r53StoredHealthCheck](nil, "route53_health_checks")
	r53CidrCollections = sim.MakeStore[r53StoredCidrCollection](nil, "route53_cidr_collections")
}

func putHealthCheck(t *testing.T, id string, cfg R53HealthCheckConfig) R53HealthCheck {
	t.Helper()
	hc := R53HealthCheck{Id: id, HealthCheckConfig: cfg}
	r53HealthChecks.Put(id, r53StoredHealthCheck{HealthCheck: hc})
	t.Cleanup(func() { r53Health.Delete(id) })
	return hc
}

func setHealthy(id string, healthy bool) {
	state := r53HealthStateOf(id)
	state.mu.Lock()
	state.healthy = healthy
	state.mu.Unlock()
}

func routedSet(name, id, ip string) R53ResourceRecordSet {
	ttl := int64(60)
	return R53ResourceRecordSet{Name: name, Type: "A", SetIdentifier: id, TTL: &ttl,
		ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: ip}}}}
}

func values(sets []R53ResourceRecordSet) []string {
	var out []string
	for _, s := range sets {
		for _, r := range s.ResourceRecords.Items {
			out = append(out, r.Value)
		}
	}
	return out
}

func TestRoute53FailoverFollowsThePrimarysHealth(t *testing.T) {
	withRoute53HealthStores(t)
	putHealthCheck(t, "hc-primary", R53HealthCheckConfig{Type: "HTTP"})
	primary := routedSet("www.example.test.", "p", "192.0.2.1")
	primary.Failover, primary.HealthCheckId = "PRIMARY", "hc-primary"
	secondary := routedSet("www.example.test.", "s", "192.0.2.2")
	secondary.Failover = "SECONDARY"
	sets := []R53ResourceRecordSet{primary, secondary}

	if got := values(route53Route(sets, route53Querier{})); len(got) != 1 || got[0] != "192.0.2.1" {
		t.Fatalf("healthy primary: %v", got)
	}
	setHealthy("hc-primary", false)
	if got := values(route53Route(sets, route53Querier{})); len(got) != 1 || got[0] != "192.0.2.2" {
		t.Fatalf("unhealthy primary: %v", got)
	}
}

func TestRoute53WeightedFollowsWeightsAndHealth(t *testing.T) {
	withRoute53HealthStores(t)
	putHealthCheck(t, "hc-heavy", R53HealthCheckConfig{Type: "HTTP"})
	w := func(n int64) *int64 { return &n }
	heavy := routedSet("api.example.test.", "heavy", "192.0.2.10")
	heavy.Weight, heavy.HealthCheckId = w(3), "hc-heavy"
	light := routedSet("api.example.test.", "light", "192.0.2.11")
	light.Weight = w(1)
	never := routedSet("api.example.test.", "never", "192.0.2.12")
	never.Weight = w(0)
	sets := []R53ResourceRecordSet{heavy, light, never}

	counts := map[string]int{}
	for range 4000 {
		got := values(route53Route(sets, route53Querier{}))
		if len(got) != 1 {
			t.Fatalf("a weighted name answers one record set: %v", got)
		}
		counts[got[0]]++
	}
	if counts["192.0.2.12"] != 0 || counts["192.0.2.10"] < 2600 || counts["192.0.2.10"] > 3400 {
		t.Fatalf("weights 3:1:0 answered %v", counts)
	}
	setHealthy("hc-heavy", false)
	for range 100 {
		if got := values(route53Route(sets, route53Querier{})); got[0] == "192.0.2.10" {
			t.Fatal("an unhealthy record set is not answered while another is healthy")
		}
	}
}

func TestRoute53MultivalueLatencyAndIPBased(t *testing.T) {
	withRoute53HealthStores(t)
	putHealthCheck(t, "hc-down", R53HealthCheckConfig{Type: "HTTP"})
	setHealthy("hc-down", false)
	yes := true
	var multi []R53ResourceRecordSet
	for i := range 10 {
		set := routedSet("mv.example.test.", "m"+strconv.Itoa(i), "192.0.2."+strconv.Itoa(20+i))
		set.MultiValueAnswer = &yes
		if i == 0 {
			set.HealthCheckId = "hc-down"
		}
		multi = append(multi, set)
	}
	got := values(route53Route(multi, route53Querier{}))
	if len(got) != route53MaxMultivalue || got[0] == "192.0.2.20" {
		t.Fatalf("multivalue answers up to eight healthy record sets: %v", got)
	}

	east := routedSet("lat.example.test.", "east", "192.0.2.40")
	east.Region = "us-east-1"
	west := routedSet("lat.example.test.", "west", "192.0.2.41")
	west.Region = "us-west-2"
	if got := values(route53Route([]R53ResourceRecordSet{east, west}, route53Querier{region: "us-west-2"})); got[0] != "192.0.2.41" {
		t.Fatalf("latency answers the querier's Region: %v", got)
	}

	r53CidrCollections.Put("col", r53StoredCidrCollection{Blocks: map[string][]string{
		"office": {"10.20.0.0/16"},
		"lab":    {"10.20.5.0/24"},
	}})
	ipSet := func(id, location, ip string) R53ResourceRecordSet {
		set := routedSet("ip.example.test.", id, ip)
		set.CidrRoutingConfig = &R53CidrRouting{CollectionId: "col", LocationName: location}
		return set
	}
	ipSets := []R53ResourceRecordSet{ipSet("o", "office", "192.0.2.50"), ipSet("l", "lab", "192.0.2.51"), ipSet("d", "*", "192.0.2.52")}
	for addr, want := range map[string]string{"10.20.5.7": "192.0.2.51", "10.20.9.1": "192.0.2.50", "172.16.0.1": "192.0.2.52"} {
		if got := values(route53Route(ipSets, route53Querier{addr: netip.MustParseAddr(addr)})); len(got) != 1 || got[0] != want {
			t.Errorf("IP-based from %s: %v, want %s", addr, got, want)
		}
	}
}

func TestRoute53HealthCheckProbesAndThresholds(t *testing.T) {
	withRoute53HealthStores(t)
	status := http.StatusOK
	body := "service ready"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	host, portText, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	two := 2
	hc := putHealthCheck(t, "hc-http", R53HealthCheckConfig{Type: "HTTP_STR_MATCH", IPAddress: host, Port: &port, SearchString: "ready", FailureThreshold: &two})
	probe := func() {
		r53Observe(hc, r53Probe(context.Background(), hc.HealthCheckConfig), time.Now())
	}

	probe()
	if !r53HealthCheckHealthy("hc-http") || r53HealthObservations("hc-http", false)[0].StatusReport.Status != "Success: HTTP Status Code 200, OK" {
		t.Fatalf("a matching 200 passes: %+v", r53HealthObservations("hc-http", false))
	}
	body = "warming up"
	probe()
	if !r53HealthCheckHealthy("hc-http") {
		t.Fatal("one failure below the threshold keeps the endpoint healthy")
	}
	probe()
	if r53HealthCheckHealthy("hc-http") {
		t.Fatal("two consecutive failures make the endpoint unhealthy")
	}
	if got := r53HealthObservations("hc-http", true); len(got) != 1 || got[0].StatusReport.Status == "" {
		t.Fatalf("the last failure is reported: %+v", got)
	}
	body, status = "ready", http.StatusServiceUnavailable
	probe()
	if got := r53HealthObservations("hc-http", false)[0].StatusReport.Status; got != "Failure: HTTP Status Code 503, Service Unavailable" {
		t.Fatalf("a 503 fails: %s", got)
	}

	tcp := putHealthCheck(t, "hc-tcp", R53HealthCheckConfig{Type: "TCP", IPAddress: host, Port: &port})
	if !r53Probe(context.Background(), tcp.HealthCheckConfig).ok {
		t.Fatal("a listening port passes a TCP check")
	}

	putHealthCheck(t, "hc-calc", R53HealthCheckConfig{Type: "CALCULATED", ChildHealthChecks: []string{"hc-http", "hc-tcp"}, HealthThreshold: &two})
	setHealthy("hc-tcp", true)
	if r53HealthCheckHealthy("hc-calc") {
		t.Fatal("a calculated check with one of two healthy children below a threshold of two is unhealthy")
	}
	setHealthy("hc-http", true)
	if !r53HealthCheckHealthy("hc-calc") {
		t.Fatal("a calculated check meets its threshold")
	}
}
