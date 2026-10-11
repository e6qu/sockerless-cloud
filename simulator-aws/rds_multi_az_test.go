package main

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

func rdsMultiAZTestStores(t *testing.T) {
	t.Helper()
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsEvents = sim.MakeStore[RDSEvent](nil, "rds_events")
}

// rdsEventMessages lists the messages DescribeEvents answers for query.
func rdsEventMessages(t *testing.T, query url.Values) []string {
	t.Helper()
	call := rdsFormCall(t, handleRDSDescribeEvents, query)
	if call.Code != http.StatusOK {
		t.Fatalf("DescribeEvents = %d %s", call.Code, call.Body.String())
	}
	var messages []string
	for _, part := range strings.Split(call.Body.String(), "<Message>")[1:] {
		message, _, _ := strings.Cut(part, "</Message>")
		messages = append(messages, message)
	}
	return messages
}

// RebootDBInstance with ForceFailover answers InvalidDBInstanceState for an
// instance that is not Multi-AZ, and leaves it as it was.
func TestRDSForceFailoverRefusesASingleAZInstance(t *testing.T) {
	rdsMultiAZTestStores(t)
	id := "single-az-db"
	rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "oracle-ee", DBInstanceStatus: "available", AvailabilityZone: awsRegion() + "a"})
	call := rdsFormCall(t, handleRDSReboot, url.Values{"DBInstanceIdentifier": {id}, "ForceFailover": {"true"}})
	if call.Code != http.StatusBadRequest || !strings.Contains(call.Body.String(), "<Code>InvalidDBInstanceState</Code>") {
		t.Fatalf("RebootDBInstance with ForceFailover = %d %s, want 400 InvalidDBInstanceState", call.Code, call.Body.String())
	}
	if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != "available" || stored.AvailabilityZone != awsRegion()+"a" {
		t.Fatalf("a refused failover changed the instance: %+v", stored)
	}
}

// A failover of a Multi-AZ instance whose engine the simulator does not run
// swaps its Availability Zones and records the failover's events.
func TestRDSForceFailoverSwapsTheZones(t *testing.T) {
	rdsMultiAZTestStores(t)
	id := "multi-az-db"
	primary, secondary := awsRegion()+"a", awsRegion()+"b"
	rdsInstances.Put(id, RDSInstance{
		DBInstanceIdentifier: id, Engine: "oracle-ee", DBInstanceStatus: "available", ARN: rdsInstanceARN(id),
		MultiAZ: true, AvailabilityZone: primary, SecondaryAvailabilityZone: secondary,
	})
	call := rdsFormCall(t, handleRDSReboot, url.Values{"DBInstanceIdentifier": {id}, "ForceFailover": {"true"}})
	if call.Code != http.StatusOK {
		t.Fatalf("RebootDBInstance with ForceFailover = %d %s", call.Code, call.Body.String())
	}
	for _, want := range []string{"<AvailabilityZone>" + secondary + "</AvailabilityZone>", "<SecondaryAvailabilityZone>" + primary + "</SecondaryAvailabilityZone>"} {
		if !strings.Contains(call.Body.String(), want) {
			t.Fatalf("the failover answered %s, want it to hold %s", call.Body.String(), want)
		}
	}
	messages := rdsEventMessages(t, url.Values{"SourceIdentifier": {id}, "SourceType": {"db-instance"}, "EventCategories.EventCategory.1": {"failover"}})
	if want := []string{rdsEventFailoverStarted, rdsEventFailoverFinished}; !slices.Equal(messages, want) {
		t.Fatalf("failover events = %q, want %q", messages, want)
	}
}

// Converting an instance to Multi-AZ gives it a standby in another
// Availability Zone of the Region, and converting it back removes it; each
// conversion records its start and its end.
func TestRDSModifyDBInstanceConvertsBetweenSingleAZAndMultiAZ(t *testing.T) {
	rdsMultiAZTestStores(t)
	id := "converted-db"
	rdsInstances.Put(id, RDSInstance{
		DBInstanceIdentifier: id, Engine: "oracle-ee", DBInstanceStatus: "available", ARN: rdsInstanceARN(id),
		AvailabilityZone: awsRegion() + "c", PreferredMaintenanceWindow: "sun:05:00-sun:05:30",
	})
	modify := func(multiAZ string) RDSInstance {
		t.Helper()
		call := rdsFormCall(t, handleRDSModify, url.Values{"DBInstanceIdentifier": {id}, "MultiAZ": {multiAZ}, "ApplyImmediately": {"true"}})
		if call.Code != http.StatusOK {
			t.Fatalf("ModifyDBInstance MultiAZ=%s = %d %s", multiAZ, call.Code, call.Body.String())
		}
		bg.Await()
		stored, _ := rdsInstances.Get(id)
		return stored
	}
	converted := modify("true")
	if !converted.MultiAZ || converted.SecondaryAvailabilityZone == "" || converted.SecondaryAvailabilityZone == converted.AvailabilityZone ||
		!slices.Contains(rdsRegionAvailabilityZones(), converted.SecondaryAvailabilityZone) || converted.AvailabilityConversion {
		t.Fatalf("a Multi-AZ conversion left %+v", converted)
	}
	reverted := modify("false")
	if reverted.MultiAZ || reverted.SecondaryAvailabilityZone != "" || reverted.AvailabilityConversion {
		t.Fatalf("a Single-AZ conversion left %+v", reverted)
	}
	messages := rdsEventMessages(t, url.Values{"SourceIdentifier": {id}, "SourceType": {"db-instance"},
		"EventCategories.EventCategory.1": {"configuration change"}})
	want := []string{rdsEventMultiAZConversionStarted, rdsEventMultiAZConversionFinished,
		rdsEventSingleAZConversionStarted, rdsEventSingleAZConversionFinished}
	if !slices.Equal(messages, want) {
		t.Fatalf("conversion events = %q, want %q", messages, want)
	}
}

// DescribeEvents answers the events of the last Duration minutes, 60 by
// default, or of the StartTime to EndTime interval.
func TestRDSDescribeEventsReadsItsTimeWindow(t *testing.T) {
	rdsMultiAZTestStores(t)
	now := time.Now().UTC()
	for _, event := range []struct {
		message string
		at      time.Time
	}{{"two hours ago", now.Add(-2 * time.Hour)}, {"ten minutes ago", now.Add(-10 * time.Minute)}} {
		rdsEvents.Put(event.message, RDSEvent{SourceIdentifier: "db", SourceType: "db-instance", Message: event.message,
			EventCategories: []string{"notification"}, Recorded: event.at.UnixNano()})
	}
	for _, testCase := range []struct {
		query url.Values
		want  []string
	}{
		{url.Values{}, []string{"ten minutes ago"}},
		{url.Values{"Duration": {"180"}}, []string{"two hours ago", "ten minutes ago"}},
		{url.Values{"StartTime": {now.Add(-3 * time.Hour).Format(time.RFC3339)}, "EndTime": {now.Add(-time.Hour).Format(time.RFC3339)}},
			[]string{"two hours ago"}},
		{url.Values{"Duration": {"180"}, "EventCategories.EventCategory.1": {"failover"}}, nil},
	} {
		if got := rdsEventMessages(t, testCase.query); !slices.Equal(got, testCase.want) {
			t.Fatalf("DescribeEvents %v = %q, want %q", testCase.query, got, testCase.want)
		}
	}
}

// A standby runs its primary's engine with the settings it replicates under:
// PostgreSQL archives the log it receives, and MySQL and MariaDB run
// read-only under a server ID of their own and acknowledge semisynchronously.
func TestRDSStandbyEngineSettings(t *testing.T) {
	instance := RDSInstance{DbiResourceId: "db-STANDBYTEST"}
	postgres := rdsStandbyEngine(dbengine.Engine{Family: dbengine.Postgres, Client: dbengine.Postgres16.Client,
		Args: []string{"-c", "archive_mode=on"}}, instance)
	if !slices.Equal(postgres.Args, []string{"-c", "archive_mode=always"}) {
		t.Fatalf("PostgreSQL standby args = %q", postgres.Args)
	}
	for client, semiSync := range map[string]string{
		dbengine.MySQL80.Client:    "--rpl-semi-sync-replica-enabled=ON",
		dbengine.MariaDB114.Client: "--rpl-semi-sync-slave-enabled=ON",
	} {
		args := rdsStandbyEngine(dbengine.Engine{Family: dbengine.MySQL, Client: client}, instance).Args
		if !slices.Contains(args, semiSync) || !slices.Contains(args, "--read-only=ON") ||
			!slices.Contains(args, "--server-id="+rdsStandbyServerID(instance)) || rdsStandbyServerID(instance) == "1" {
			t.Fatalf("%s standby args = %q", client, args)
		}
	}
	if zone := rdsStandbyAvailabilityZone(awsRegion() + "a"); zone == awsRegion()+"a" || !slices.Contains(rdsRegionAvailabilityZones(), zone) {
		t.Fatalf("standby zone = %q", zone)
	}
}
