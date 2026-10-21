package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsCapacityTestStores(t *testing.T) {
	t.Helper()
	bg.Await()
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsEvents = sim.MakeStore[RDSEvent](nil, "rds_events")
	rdsCapacityReservations = sim.MakeStore[RDSCapacityReservation](nil, "rds_capacity_reservations")
}

func rdsCreateTestReservation(t *testing.T, form url.Values) RDSCapacityReservation {
	t.Helper()
	call := rdsFormCall(t, handleRDSCreateCapacityReservation, form)
	if call.Code != http.StatusOK {
		t.Fatalf("CreateDBCapacityReservation = %d %s", call.Code, call.Body.String())
	}
	res, ok := rdsFindCapacityReservation(form.Get("DBCapacityReservationName"))
	if !ok {
		t.Fatalf("CreateDBCapacityReservation stored no reservation named %s", form.Get("DBCapacityReservationName"))
	}
	return res
}

func rdsReservationForm(name string, capacity int, zones ...string) url.Values {
	form := url.Values{
		"DBCapacityReservationName":   {name},
		"DBInstanceClass":             {"db.m5.large"},
		"Engine":                      {"oracle-ee"},
		"CapacityPerAvailabilityZone": {fmt.Sprint(capacity)},
	}
	for i, zone := range zones {
		form.Set(fmt.Sprintf("AvailabilityZones.AvailabilityZone.%d", i+1), zone)
	}
	return form
}

func rdsWantError(t *testing.T, call *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if call.Code != status || !strings.Contains(call.Body.String(), "<Code>"+code+"</Code>") {
		t.Fatalf("answered %d %s, want %d %s", call.Code, call.Body.String(), status, code)
	}
}

func rdsLaunchRequest(form url.Values) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request
}

// A reservation reserves its capacity in each of its Availability Zones, a
// launch into it takes a unit in the zone it lands in, and a Multi-AZ
// launch takes one in each of two zones; once a zone is full a launch there
// fails with InsufficientDBInstanceCapacity.
func TestRDSCapacityReservationLaunchesTakeUnits(t *testing.T) {
	rdsCapacityTestStores(t)
	zoneA, zoneB := awsRegion()+"a", awsRegion()+"b"
	res := rdsCreateTestReservation(t, rdsReservationForm("launches", 1, zoneA, zoneB))
	if res.State != rdsCapacityReservationActive || res.LicenseModel != "bring-your-own-license" {
		t.Fatalf("reservation = %+v, want an active bring-your-own-license reservation", res)
	}

	launch := func(id string, form url.Values) (RDSInstance, string) {
		t.Helper()
		instance := RDSInstance{DBInstanceIdentifier: id, DBInstanceClass: "db.m5.large", Engine: "oracle-ee", DBInstanceStatus: "creating", AvailabilityZone: awsRegion() + "a"}
		instance.MultiAZ = form.Get("MultiAZ") == "true"
		form.Set("DBCapacityReservationIdentifier", res.Name)
		release, code, _ := rdsTakeLaunchCapacity(rdsLaunchRequest(form), &instance, form.Get("LicenseModel"))
		if code != "" {
			return instance, code
		}
		rdsInstances.Put(id, instance)
		release()
		return instance, ""
	}
	if _, code := launch("mismatched-license", url.Values{"LicenseModel": {"license-included"}}); code != "InvalidParameterCombination" {
		t.Fatalf("a launch with another license model answered %q, want InvalidParameterCombination", code)
	}
	single, code := launch("single-az", url.Values{"AvailabilityZone": {zoneB}})
	if code != "" || single.AvailabilityZone != zoneB || single.DBCapacityReservationIdentifier != res.Identifier {
		t.Fatalf("Single-AZ launch = %+v %q, want it in %s on %s", single, code, zoneB, res.Identifier)
	}
	if _, code := launch("multi-az", url.Values{"MultiAZ": {"true"}}); code != "InsufficientDBInstanceCapacity" {
		t.Fatalf("a Multi-AZ launch with %s full answered %q, want InsufficientDBInstanceCapacity", zoneB, code)
	}
	if _, code := launch("other-zone", url.Values{}); code != "" {
		t.Fatalf("a launch with room in %s answered %q", zoneA, code)
	}
	described := rdsFormCall(t, handleRDSDescribeCapacityReservations, url.Values{"DBCapacityReservationIdentifier": {res.Identifier}})
	for _, zone := range []string{zoneA, zoneB} {
		want := "<AvailabilityZone>" + zone + "</AvailabilityZone><AvailableCapacity>0</AvailableCapacity>"
		if !strings.Contains(described.Body.String(), want) {
			t.Fatalf("DescribeDBCapacityReservations = %s, want %s", described.Body.String(), want)
		}
	}

	// A stopped instance releases its unit and keeps its reservation.
	rdsInstances.Update("single-az", func(i *RDSInstance) { i.DBInstanceStatus = "stopped" })
	stopped, _ := rdsInstances.Get("single-az")
	release, code, message := rdsTakeStartCapacity(rdsLaunchRequest(url.Values{}), &stopped)
	if code != "" {
		t.Fatalf("StartDBInstance into the kept reservation answered %s %s", code, message)
	}
	release()
	if stopped.DBCapacityReservationIdentifier != res.Identifier {
		t.Fatalf("the started instance left its reservation: %+v", stopped)
	}

	// A cancelled reservation reports no capacity, and its instances no
	// longer report it.
	cancelled := rdsFormCall(t, handleRDSCancelCapacityReservation, url.Values{"DBCapacityReservationIdentifier": {res.Name}})
	if cancelled.Code != http.StatusOK || !strings.Contains(cancelled.Body.String(), "<State>cancelled</State>") {
		t.Fatalf("CancelDBCapacityReservation = %d %s", cancelled.Code, cancelled.Body.String())
	}
	instance, _ := rdsInstances.Get("other-zone")
	if strings.Contains(renderRDSInstance(instance), "DBCapacityReservationIdentifier") {
		t.Fatalf("an instance of a cancelled reservation still reports it: %s", renderRDSInstance(instance))
	}
	if _, code := launch("after-cancel", url.Values{}); code != "InvalidDBCapacityReservationState" {
		t.Fatalf("a launch into a cancelled reservation answered %q, want InvalidDBCapacityReservationState", code)
	}
}

// ModifyDBCapacityReservation refuses a capacity below the units in use, and
// moves the end date.
func TestRDSModifyCapacityReservation(t *testing.T) {
	rdsCapacityTestStores(t)
	zone := awsRegion() + "c"
	res := rdsCreateTestReservation(t, rdsReservationForm("modified", 2, zone))
	rdsInstances.Put("user-1", RDSInstance{DBInstanceIdentifier: "user-1", DBInstanceStatus: "available", AvailabilityZone: zone, DBCapacityReservationIdentifier: res.Identifier})
	rdsInstances.Put("user-2", RDSInstance{DBInstanceIdentifier: "user-2", DBInstanceStatus: "available", AvailabilityZone: zone, DBCapacityReservationIdentifier: res.Identifier})

	modify := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("DBCapacityReservationIdentifier", res.Identifier)
		return rdsFormCall(t, handleRDSModifyCapacityReservation, form)
	}
	rdsWantError(t, modify(url.Values{}), http.StatusBadRequest, "InvalidParameterCombination")
	rdsWantError(t, modify(url.Values{"CapacityPerAvailabilityZone": {"1"}}), http.StatusBadRequest, "InvalidParameterValue")
	rdsWantError(t, modify(url.Values{"EndDate": {"2099-01-01T00:00:00Z"}}), http.StatusBadRequest, "InvalidParameterCombination")

	end := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	call := modify(url.Values{"CapacityPerAvailabilityZone": {"3"}, "EndDateType": {"limited"}, "EndDate": {end.Format(time.RFC3339)}})
	if call.Code != http.StatusOK || !strings.Contains(call.Body.String(), "<AvailableCapacity>1</AvailableCapacity>") ||
		!strings.Contains(call.Body.String(), "<EndDate>"+rdsTimestamp(end)+"</EndDate>") {
		t.Fatalf("ModifyDBCapacityReservation = %d %s", call.Code, call.Body.String())
	}
	call = modify(url.Values{"EndDateType": {"unlimited"}})
	if call.Code != http.StatusOK || strings.Contains(call.Body.String(), "<EndDate>") {
		t.Fatalf("EndDateType unlimited = %d %s, want no EndDate", call.Code, call.Body.String())
	}
}

// A future-dated reservation waits scheduled for its start date, holds its
// commitment, and is active from its start date until its end date.
func TestRDSFutureDatedCapacityReservation(t *testing.T) {
	rdsCapacityTestStores(t)
	now := time.Now().UTC()
	form := rdsReservationForm("future", 8, awsRegion()+"a")
	form.Set("DBInstanceClass", "db.m7g.xlarge")
	form.Set("Engine", "postgres")
	form.Set("StartDate", now.Add(10*24*time.Hour).Format(time.RFC3339))
	form.Set("CommitmentDuration", "1209600")

	small := url.Values{}
	for k, v := range form {
		small[k] = v
	}
	small.Set("CapacityPerAvailabilityZone", "7")
	rdsWantError(t, rdsFormCall(t, handleRDSCreateCapacityReservation, small), http.StatusBadRequest, "DBCapacityReservationUnsupported")
	small.Set("CapacityPerAvailabilityZone", "8")
	small.Set("DBInstanceClass", "db.m5.4xlarge")
	rdsWantError(t, rdsFormCall(t, handleRDSCreateCapacityReservation, small), http.StatusBadRequest, "DBCapacityReservationUnsupported")

	res := rdsCreateTestReservation(t, form)
	if state := rdsCapacityReservationState(res, now); state != rdsCapacityReservationScheduled {
		t.Fatalf("a future-dated reservation is %s, want scheduled", state)
	}
	rdsWantError(t, rdsFormCall(t, handleRDSCancelCapacityReservation, url.Values{"DBCapacityReservationIdentifier": {res.Identifier}}),
		http.StatusBadRequest, "InvalidDBCapacityReservationState")
	rdsWantError(t, rdsFormCall(t, handleRDSModifyCapacityReservation, url.Values{"DBCapacityReservationIdentifier": {res.Identifier}, "CapacityPerAvailabilityZone": {"9"}}),
		http.StatusBadRequest, "InvalidDBCapacityReservationState")
	if !strings.Contains(renderRDSCapacityReservation(res, now), "<CommittedCapacityPerAvailabilityZone>8</CommittedCapacityPerAvailabilityZone>") {
		t.Fatalf("a future-dated reservation reports no commitment: %s", renderRDSCapacityReservation(res, now))
	}

	started := res.StartDate.Add(time.Hour)
	if state := rdsCapacityReservationState(res, started); state != rdsCapacityReservationActive {
		t.Fatalf("after its start date the reservation is %s, want active", state)
	}
	res.EndDate = res.CommitmentEndDate.Add(time.Hour)
	if state := rdsCapacityReservationState(res, res.EndDate); state != rdsCapacityReservationExpired {
		t.Fatalf("at its end date the reservation is %s, want expired", state)
	}
}

// DescribeDBCapacityReservations pages with Marker and MaxRecords, filters
// by state, and refuses the expired state filter Amazon RDS refuses.
func TestRDSDescribeCapacityReservationsPagesAndFilters(t *testing.T) {
	rdsCapacityTestStores(t)
	for n := range 25 {
		rdsCreateTestReservation(t, rdsReservationForm(fmt.Sprintf("paged-%02d", n), 1, awsRegion()+"b"))
	}
	seen := map[string]bool{}
	marker := ""
	for page := 0; ; page++ {
		form := url.Values{"MaxRecords": {"20"}}
		if marker != "" {
			form.Set("Marker", marker)
		}
		call := rdsFormCall(t, handleRDSDescribeCapacityReservations, form)
		body := call.Body.String()
		for _, part := range strings.Split(body, "<DBCapacityReservationName>")[1:] {
			name, _, _ := strings.Cut(part, "<")
			if seen[name] {
				t.Fatalf("page %d repeats %s", page, name)
			}
			seen[name] = true
		}
		_, after, found := strings.Cut(body, "<Marker>")
		if !found {
			break
		}
		marker, _, _ = strings.Cut(after, "</Marker>")
	}
	if len(seen) != 25 {
		t.Fatalf("pages held %d reservations, want 25", len(seen))
	}
	rdsWantError(t, rdsFormCall(t, handleRDSDescribeCapacityReservations, url.Values{"MaxRecords": {"10"}}), http.StatusBadRequest, "InvalidParameterValue")
	rdsWantError(t, rdsFormCall(t, handleRDSDescribeCapacityReservations, url.Values{
		"Filters.Filter.1.Name": {"state"}, "Filters.Filter.1.Values.Value.1": {"expired"},
	}), http.StatusBadRequest, "InvalidParameterValue")
	rdsWantError(t, rdsFormCall(t, handleRDSDescribeCapacityReservations, url.Values{"DBCapacityReservationIdentifier": {"dbcr-missing"}}),
		http.StatusNotFound, "DBCapacityReservationNotFound")
	rdsWantError(t, rdsFormCall(t, handleRDSCreateCapacityReservation, rdsReservationForm("paged-00", 1, awsRegion()+"b")),
		http.StatusBadRequest, "DBCapacityReservationAlreadyExists")
}
