package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_CapacityReservations covers the capacity-reservation and
// capacity-reservation-fleet control planes via the aws CLI: create → describe
// → modify → usage → cancel, and the fleet create → describe → modify → cancel.
func TestEC2CLI_CapacityReservations(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	id := q("ec2", "create-capacity-reservation",
		"--instance-type", "t3.micro",
		"--instance-platform", "Linux/UNIX",
		"--availability-zone", "us-east-1a",
		"--instance-count", "3",
		"--tag-specifications", "ResourceType=capacity-reservation,Tags=[{Key=team,Value=ci}]",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")
	if id == "" {
		t.Fatal("create-capacity-reservation returned empty id")
	}

	got := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].[InstanceType,State,TotalInstanceCount,AvailableInstanceCount]", "--output", "text")
	if f := strings.Fields(got); len(f) != 4 || f[0] != "t3.micro" || f[1] != "active" || f[2] != "3" || f[3] != "3" {
		t.Fatalf("describe-capacity-reservations: got %q, want 't3.micro active 3 3'", got)
	}

	tag := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].Tags[?Key=='team']|[0].Value", "--output", "text")
	if tag != "ci" {
		t.Fatalf("capacity reservation tag: got %q, want ci", tag)
	}

	runCLI(t, awsCLI("ec2", "modify-capacity-reservation", "--capacity-reservation-id", id, "--instance-count", "5"))
	cnt := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].TotalInstanceCount", "--output", "text")
	if cnt != "5" {
		t.Fatalf("after modify, total count: got %q, want 5", cnt)
	}

	usage := q("ec2", "get-capacity-reservation-usage", "--capacity-reservation-id", id,
		"--query", "TotalInstanceCount", "--output", "text")
	if usage != "5" {
		t.Fatalf("get-capacity-reservation-usage total: got %q, want 5", usage)
	}
	// GetGroupsForCapacityReservation read-back (empty).
	q("ec2", "get-groups-for-capacity-reservation", "--capacity-reservation-id", id,
		"--query", "length(CapacityReservationGroups)", "--output", "text")

	runCLI(t, awsCLI("ec2", "cancel-capacity-reservation", "--capacity-reservation-id", id))
	state := q("ec2", "describe-capacity-reservations", "--capacity-reservation-ids", id,
		"--query", "CapacityReservations[0].State", "--output", "text")
	if state != "cancelled" {
		t.Fatalf("after cancel, state: got %q, want cancelled", state)
	}

	// --- Capacity Reservation Fleet ---
	fleetID := q("ec2", "create-capacity-reservation-fleet",
		"--total-target-capacity", "4",
		"--instance-type-specifications", "InstanceType=t3.micro,InstancePlatform=Linux/UNIX,AvailabilityZone=us-east-1a,Weight=1,Priority=1",
		"--query", "CapacityReservationFleetId", "--output", "text")
	if fleetID == "" {
		t.Fatal("create-capacity-reservation-fleet returned empty id")
	}
	ftarget := q("ec2", "describe-capacity-reservation-fleets", "--capacity-reservation-fleet-ids", fleetID,
		"--query", "CapacityReservationFleets[0].[State,TotalTargetCapacity]", "--output", "text")
	if f := strings.Fields(ftarget); len(f) != 2 || f[0] != "active" || f[1] != "4" {
		t.Fatalf("describe-capacity-reservation-fleets: got %q, want 'active 4'", ftarget)
	}
	runCLI(t, awsCLI("ec2", "modify-capacity-reservation-fleet", "--capacity-reservation-fleet-id", fleetID, "--total-target-capacity", "8"))
	cancelState := q("ec2", "cancel-capacity-reservation-fleets", "--capacity-reservation-fleet-ids", fleetID,
		"--query", "SuccessfulFleetCancellations[0].CurrentFleetState", "--output", "text")
	if cancelState != "cancelled" {
		t.Fatalf("cancel-capacity-reservation-fleets state: got %q, want cancelled", cancelState)
	}
}

// TestEC2CLI_Fleets covers the EC2 Fleet control plane via the aws CLI: a launch
// template, create-fleet → describe-fleets → describe-fleet-instances →
// describe-fleet-history → modify-fleet → delete-fleets.
func TestEC2CLI_Fleets(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	ltID := q("ec2", "create-launch-template", "--launch-template-name", "cli-fleet-lt",
		"--launch-template-data", "ImageId=ami-12345678,InstanceType=t3.micro",
		"--query", "LaunchTemplate.LaunchTemplateId", "--output", "text")
	if ltID == "" {
		t.Fatal("create-launch-template returned empty id")
	}

	fleetID := q("ec2", "create-fleet",
		"--type", "maintain",
		"--target-capacity-specification", "TotalTargetCapacity=2,DefaultTargetCapacityType=on-demand",
		"--launch-template-configs", "[{\"LaunchTemplateSpecification\":{\"LaunchTemplateId\":\""+ltID+"\",\"Version\":\"1\"},\"Overrides\":[{\"InstanceType\":\"t3.micro\"}]}]",
		"--query", "FleetId", "--output", "text")
	if fleetID == "" {
		t.Fatal("create-fleet returned empty FleetId")
	}

	state := q("ec2", "describe-fleets", "--fleet-ids", fleetID,
		"--query", "Fleets[0].[FleetState,TargetCapacitySpecification.TotalTargetCapacity]", "--output", "text")
	if f := strings.Fields(state); len(f) != 2 || f[0] != "active" || f[1] != "2" {
		t.Fatalf("describe-fleets: got %q, want 'active 2'", state)
	}

	ninst := q("ec2", "describe-fleet-instances", "--fleet-id", fleetID,
		"--query", "length(ActiveInstances)", "--output", "text")
	if ninst != "2" {
		t.Fatalf("describe-fleet-instances active count: got %q, want 2", ninst)
	}

	nhist := q("ec2", "describe-fleet-history", "--fleet-id", fleetID, "--start-time", "2020-01-01T00:00:00Z",
		"--query", "length(HistoryRecords)", "--output", "text")
	if nhist == "0" || nhist == "" {
		t.Fatalf("describe-fleet-history records: got %q, want >0", nhist)
	}

	runCLI(t, awsCLI("ec2", "modify-fleet", "--fleet-id", fleetID,
		"--target-capacity-specification", "TotalTargetCapacity=3"))

	delState := q("ec2", "delete-fleets", "--fleet-ids", fleetID, "--terminate-instances",
		"--query", "SuccessfulFleetDeletions[0].CurrentFleetState", "--output", "text")
	if !strings.HasPrefix(delState, "deleted") {
		t.Fatalf("delete-fleets state: got %q, want deleted-*", delState)
	}
}

// TestEC2CLI_Spot covers the Spot families via the aws CLI: request-spot-instances
// → describe → cancel; request-spot-fleet → describe → instances → history →
// modify → cancel; the data feed subscription; and the read-only price-history /
// placement-scores ops.
func TestEC2CLI_Spot(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	sirID := q("ec2", "request-spot-instances",
		"--spot-price", "0.0035",
		"--instance-count", "1",
		"--type", "one-time",
		"--launch-specification", "{\"ImageId\":\"ami-12345678\",\"InstanceType\":\"t3.micro\"}",
		"--query", "SpotInstanceRequests[0].SpotInstanceRequestId", "--output", "text")
	if sirID == "" {
		t.Fatal("request-spot-instances returned empty id")
	}
	sstate := q("ec2", "describe-spot-instance-requests", "--spot-instance-request-ids", sirID,
		"--query", "SpotInstanceRequests[0].[State,SpotPrice]", "--output", "text")
	if f := strings.Fields(sstate); len(f) != 2 || f[0] != "active" || f[1] != "0.0035" {
		t.Fatalf("describe-spot-instance-requests: got %q, want 'active 0.0035'", sstate)
	}
	cstate := q("ec2", "cancel-spot-instance-requests", "--spot-instance-request-ids", sirID,
		"--query", "CancelledSpotInstanceRequests[0].State", "--output", "text")
	if cstate != "cancelled" {
		t.Fatalf("cancel-spot-instance-requests state: got %q, want cancelled", cstate)
	}

	// --- Spot fleet ---
	sfrID := q("ec2", "request-spot-fleet",
		"--spot-fleet-request-config", "{\"IamFleetRole\":\"arn:aws:iam::123456789012:role/spot-fleet\",\"TargetCapacity\":2,\"SpotPrice\":\"0.0035\",\"LaunchSpecifications\":[{\"ImageId\":\"ami-12345678\",\"InstanceType\":\"t3.micro\"}]}",
		"--query", "SpotFleetRequestId", "--output", "text")
	if sfrID == "" {
		t.Fatal("request-spot-fleet returned empty id")
	}
	fstate := q("ec2", "describe-spot-fleet-requests", "--spot-fleet-request-ids", sfrID,
		"--query", "SpotFleetRequestConfigs[0].[SpotFleetRequestState,SpotFleetRequestConfig.TargetCapacity]", "--output", "text")
	if f := strings.Fields(fstate); len(f) != 2 || f[0] != "active" || f[1] != "2" {
		t.Fatalf("describe-spot-fleet-requests: got %q, want 'active 2'", fstate)
	}
	ninst := q("ec2", "describe-spot-fleet-instances", "--spot-fleet-request-id", sfrID,
		"--query", "length(ActiveInstances)", "--output", "text")
	if ninst != "2" {
		t.Fatalf("describe-spot-fleet-instances count: got %q, want 2", ninst)
	}
	nhist := q("ec2", "describe-spot-fleet-request-history", "--spot-fleet-request-id", sfrID,
		"--start-time", "2020-01-01T00:00:00Z", "--query", "length(HistoryRecords)", "--output", "text")
	if nhist == "0" || nhist == "" {
		t.Fatalf("describe-spot-fleet-request-history records: got %q, want >0", nhist)
	}
	runCLI(t, awsCLI("ec2", "modify-spot-fleet-request", "--spot-fleet-request-id", sfrID, "--target-capacity", "3"))
	fcancel := q("ec2", "cancel-spot-fleet-requests", "--spot-fleet-request-ids", sfrID, "--terminate-instances",
		"--query", "SuccessfulFleetRequests[0].CurrentSpotFleetRequestState", "--output", "text")
	if !strings.HasPrefix(fcancel, "cancelled") {
		t.Fatalf("cancel-spot-fleet-requests state: got %q, want cancelled*", fcancel)
	}

	// --- Spot data feed subscription ---
	bucket := q("ec2", "create-spot-datafeed-subscription", "--bucket", "cli-spot-logs", "--prefix", "feed/",
		"--query", "SpotDatafeedSubscription.Bucket", "--output", "text")
	if bucket != "cli-spot-logs" {
		t.Fatalf("create-spot-datafeed-subscription bucket: got %q, want cli-spot-logs", bucket)
	}
	descBucket := q("ec2", "describe-spot-datafeed-subscription",
		"--query", "SpotDatafeedSubscription.Bucket", "--output", "text")
	if descBucket != "cli-spot-logs" {
		t.Fatalf("describe-spot-datafeed-subscription bucket: got %q, want cli-spot-logs", descBucket)
	}
	runCLI(t, awsCLI("ec2", "delete-spot-datafeed-subscription"))

	// --- Read-only price / placement scores ---
	price := q("ec2", "describe-spot-price-history", "--instance-types", "t3.micro",
		"--product-descriptions", "Linux/UNIX",
		"--query", "SpotPriceHistory[0].InstanceType", "--output", "text")
	if price != "t3.micro" {
		t.Fatalf("describe-spot-price-history instance type: got %q, want t3.micro", price)
	}
	score := q("ec2", "get-spot-placement-scores", "--instance-types", "t3.micro",
		"--target-capacity", "10", "--region-names", "us-east-1",
		"--query", "SpotPlacementScores[0].Region", "--output", "text")
	if score != "us-east-1" {
		t.Fatalf("get-spot-placement-scores region: got %q, want us-east-1", score)
	}
}

// TestEC2CLI_ScheduledAndHostReservations covers scheduled-instance availability
// → purchase → describe → run, and the dedicated-host reservation offerings →
// purchase-preview → purchase → describe.
func TestEC2CLI_ScheduledAndHostReservations(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	token := q("ec2", "describe-scheduled-instance-availability",
		"--first-slot-start-time-range", "EarliestTime=2020-01-01T00:00:00Z,LatestTime=2030-01-01T00:00:00Z",
		"--recurrence", "Frequency=Weekly,Interval=1,OccurrenceDays=[1]",
		"--query", "ScheduledInstanceAvailabilitySet[0].PurchaseToken", "--output", "text")
	if token == "" {
		t.Fatal("describe-scheduled-instance-availability returned empty purchase token")
	}
	sciID := q("ec2", "purchase-scheduled-instances",
		"--purchase-requests", "InstanceCount=1,PurchaseToken="+token,
		"--query", "ScheduledInstanceSet[0].ScheduledInstanceId", "--output", "text")
	if sciID == "" {
		t.Fatal("purchase-scheduled-instances returned empty id")
	}
	descSci := q("ec2", "describe-scheduled-instances", "--scheduled-instance-ids", sciID,
		"--query", "ScheduledInstanceSet[0].ScheduledInstanceId", "--output", "text")
	if descSci != sciID {
		t.Fatalf("describe-scheduled-instances: got %q, want %q", descSci, sciID)
	}
	nrun := q("ec2", "run-scheduled-instances", "--scheduled-instance-id", sciID, "--instance-count", "1",
		"--launch-specification", "{\"ImageId\":\"ami-12345678\",\"InstanceType\":\"t3.micro\"}",
		"--query", "length(InstanceIdSet)", "--output", "text")
	if nrun != "1" {
		t.Fatalf("run-scheduled-instances instance count: got %q, want 1", nrun)
	}

	// --- Dedicated host reservations ---
	offeringID := q("ec2", "describe-host-reservation-offerings",
		"--query", "OfferingSet[0].OfferingId", "--output", "text")
	if offeringID == "" {
		t.Fatal("describe-host-reservation-offerings returned empty offering id")
	}
	preview := q("ec2", "get-host-reservation-purchase-preview",
		"--offering-id", offeringID, "--host-id-set", "h-0123456789abcdef0",
		"--query", "length(Purchase)", "--output", "text")
	if preview != "1" {
		t.Fatalf("get-host-reservation-purchase-preview purchase count: got %q, want 1", preview)
	}
	hrID := q("ec2", "purchase-host-reservation",
		"--offering-id", offeringID, "--host-id-set", "h-0123456789abcdef0",
		"--query", "Purchase[0].HostReservationId", "--output", "text")
	if hrID == "" {
		t.Fatal("purchase-host-reservation returned empty id")
	}
	descHr := q("ec2", "describe-host-reservations", "--host-reservation-id-set", hrID,
		"--query", "HostReservationSet[0].HostReservationId", "--output", "text")
	if descHr != hrID {
		t.Fatalf("describe-host-reservations: got %q, want %q", descHr, hrID)
	}
}
