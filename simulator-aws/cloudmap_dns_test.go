package main

import (
	"testing"
	"time"
)

func TestCMCustomHealthWaitsThirtySeconds(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	inst := CMInstance{Id: "i"}
	cmRequestCustomHealth(&inst, "UNHEALTHY", start)
	if inst.PendingHealthStatus != "UNHEALTHY" || inst.PendingHealthSince != start.UnixMilli() {
		t.Fatalf("a change waits: %+v", inst)
	}

	fresh := CMInstance{Id: "j"}
	now := time.Now()
	cmRequestCustomHealth(&fresh, "UNHEALTHY", now)
	if cmInstanceHealth(fresh) != "HEALTHY" {
		t.Fatal("the status in force holds for 30 seconds")
	}
	cmRequestCustomHealth(&fresh, "UNHEALTHY", now.Add(10*time.Second))
	if fresh.PendingHealthSince != now.UnixMilli() {
		t.Fatal("repeating the change does not restart or speed up the wait")
	}
	cmRequestCustomHealth(&fresh, "HEALTHY", now.Add(20*time.Second))
	if fresh.PendingHealthStatus != "" || cmInstanceHealth(fresh) != "HEALTHY" {
		t.Fatalf("a request for the status in force cancels the change: %+v", fresh)
	}

	if cmInstanceHealth(inst) != "UNHEALTHY" {
		t.Fatal("after 30 seconds the requested status is in force")
	}
	cmRequestCustomHealth(&inst, "HEALTHY", time.Now())
	if inst.CustomHealthStatus != "UNHEALTHY" || inst.PendingHealthStatus != "HEALTHY" || cmInstanceHealth(inst) != "UNHEALTHY" {
		t.Fatalf("the settled status stays until the next change applies: %+v", inst)
	}
}

func TestRoute53LinkedHealthKeepsUnhealthyOutWhileAHealthyOneRemains(t *testing.T) {
	yes := true
	set := func(id, ip string) R53ResourceRecordSet {
		return R53ResourceRecordSet{Name: "api.example.", Type: "A", SetIdentifier: id, MultiValueAnswer: &yes,
			TTL: ptrInt64(10), ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: ip}}}}
	}
	sets := []R53ResourceRecordSet{set("a1", "192.0.2.1"), set("a2", "192.0.2.2")}
	down := map[string]bool{"a1": true}
	q := route53Querier{healthy: func(s R53ResourceRecordSet) bool { return !down[s.SetIdentifier] }}
	if got := route53Route(sets, q); len(got) != 1 || got[0].SetIdentifier != "a2" {
		t.Fatalf("answered %+v", got)
	}
	down["a2"] = true
	if got := route53Route(sets, q); len(got) != 2 {
		t.Fatalf("with none healthy every record set answers: %+v", got)
	}
}
