package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_ReservedInstances covers the Reserved Instances control plane via
// the aws CLI: describe the offering catalog, purchase an offering into an
// active RI, read it back, modify it, list it on the marketplace, read and
// cancel the listing, describe modifications, attempt a queued-deletion (fails
// honestly for an active RI), and run the exchange quote / accept flow.
func TestEC2CLI_ReservedInstances(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	offeringID := q("ec2", "describe-reserved-instances-offerings",
		"--filters", "Name=instance-type,Values=t3.micro",
		"--query", "ReservedInstancesOfferings[?Scope=='Region']|[0].ReservedInstancesOfferingId", "--output", "text")
	if offeringID == "" || offeringID == "None" {
		t.Fatal("describe-reserved-instances-offerings returned no Region offering")
	}

	riID := q("ec2", "purchase-reserved-instances-offering",
		"--reserved-instances-offering-id", offeringID, "--instance-count", "2",
		"--query", "ReservedInstancesId", "--output", "text")
	if riID == "" || riID == "None" {
		t.Fatal("purchase-reserved-instances-offering returned empty id")
	}

	got := q("ec2", "describe-reserved-instances", "--reserved-instances-ids", riID,
		"--query", "ReservedInstances[0].[InstanceType,State,InstanceCount]", "--output", "text")
	if f := strings.Fields(got); len(f) != 3 || f[0] != "t3.micro" || f[1] != "active" || f[2] != "2" {
		t.Fatalf("describe-reserved-instances: got %q, want 't3.micro active 2'", got)
	}

	modID := q("ec2", "modify-reserved-instances", "--reserved-instances-ids", riID,
		"--target-configurations", "AvailabilityZone=us-east-1a,InstanceCount=1,InstanceType=t3.micro", "AvailabilityZone=us-east-1b,InstanceCount=1,InstanceType=t3.micro",
		"--query", "ReservedInstancesModificationId", "--output", "text")
	if modID == "" || modID == "None" {
		t.Fatal("modify-reserved-instances returned empty modification id")
	}

	nResults := q("ec2", "describe-reserved-instances-modifications", "--reserved-instances-modification-ids", modID,
		"--query", "length(ReservedInstancesModifications[0].ModificationResults)", "--output", "text")
	if nResults != "2" {
		t.Fatalf("modification results: got %q, want 2", nResults)
	}

	listingID := q("ec2", "create-reserved-instances-listing", "--reserved-instances-id", riID,
		"--instance-count", "1", "--client-token", "ric-cli-1",
		"--price-schedules", "Term=11,Price=40.0", "Term=5,Price=20.0",
		"--query", "ReservedInstancesListings[0].ReservedInstancesListingId", "--output", "text")
	if listingID == "" || listingID == "None" {
		t.Fatal("create-reserved-instances-listing returned empty id")
	}

	lstatus := q("ec2", "describe-reserved-instances-listings", "--reserved-instances-listing-id", listingID,
		"--query", "ReservedInstancesListings[0].Status", "--output", "text")
	if lstatus != "active" {
		t.Fatalf("listing status: got %q, want active", lstatus)
	}

	cstatus := q("ec2", "cancel-reserved-instances-listing", "--reserved-instances-listing-id", listingID,
		"--query", "ReservedInstancesListings[0].Status", "--output", "text")
	if cstatus != "cancelled" {
		t.Fatalf("after cancel, listing status: got %q, want cancelled", cstatus)
	}

	// An active RI is not queued, so a queued-deletion attempt fails for it.
	nFailed := q("ec2", "delete-queued-reserved-instances", "--reserved-instances-ids", riID,
		"--query", "length(FailedQueuedPurchaseDeletions)", "--output", "text")
	if nFailed != "1" {
		t.Fatalf("delete-queued failed deletions: got %q, want 1", nFailed)
	}

	// --- Convertible RI exchange quote / accept ---
	tgtOffering := q("ec2", "describe-reserved-instances-offerings",
		"--filters", "Name=instance-type,Values=c5.xlarge",
		"--query", "ReservedInstancesOfferings[0].ReservedInstancesOfferingId", "--output", "text")
	if tgtOffering == "" || tgtOffering == "None" {
		t.Fatal("no c5.xlarge offering for exchange target")
	}

	valid := q("ec2", "get-reserved-instances-exchange-quote", "--reserved-instance-ids", riID,
		"--target-configurations", "OfferingId="+tgtOffering+",InstanceCount=1",
		"--query", "IsValidExchange", "--output", "text")
	if valid != "True" {
		t.Fatalf("exchange quote IsValidExchange: got %q, want True", valid)
	}

	exID := q("ec2", "accept-reserved-instances-exchange-quote", "--reserved-instance-ids", riID,
		"--target-configurations", "OfferingId="+tgtOffering+",InstanceCount=1",
		"--query", "ExchangeId", "--output", "text")
	if exID == "" || exID == "None" {
		t.Fatal("accept-reserved-instances-exchange-quote returned empty exchange id")
	}
}

// TestEC2CLI_CapacityReservationBilling covers the Capacity Reservation
// billing-ownership transfer and the splitting / moving ops via the aws CLI.
// The topology and cancellation-quote ops are SDK-only (missing from aws CLI
// 2.26.6); the SDK test covers their contract hook.
func TestEC2CLI_CapacityReservationBilling(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	crID := q("ec2", "create-capacity-reservation",
		"--instance-type", "t3.medium", "--instance-platform", "Linux/UNIX",
		"--availability-zone", "us-east-1a", "--instance-count", "6",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")
	if crID == "" {
		t.Fatal("create-capacity-reservation returned empty id")
	}

	ret := q("ec2", "associate-capacity-reservation-billing-owner",
		"--capacity-reservation-id", crID, "--unused-reservation-billing-owner-id", "210987654321",
		"--query", "Return", "--output", "text")
	if ret != "True" {
		t.Fatalf("associate billing owner Return: got %q, want True", ret)
	}

	status := q("ec2", "describe-capacity-reservation-billing-requests",
		"--role", "odcr-owner",
		"--query", "CapacityReservationBillingRequests[?CapacityReservationId=='"+crID+"']|[0].Status", "--output", "text")
	if status != "pending" {
		t.Fatalf("billing request status: got %q, want pending", status)
	}

	acc := q("ec2", "accept-capacity-reservation-billing-ownership", "--capacity-reservation-id", crID,
		"--query", "Return", "--output", "text")
	if acc != "True" {
		t.Fatalf("accept billing ownership Return: got %q, want True", acc)
	}

	// Reject + disassociate on a fresh reservation.
	cr2 := q("ec2", "create-capacity-reservation",
		"--instance-type", "t3.medium", "--instance-platform", "Linux/UNIX",
		"--availability-zone", "us-east-1a", "--instance-count", "2",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")
	runCLI(t, awsCLI("ec2", "associate-capacity-reservation-billing-owner",
		"--capacity-reservation-id", cr2, "--unused-reservation-billing-owner-id", "210987654321"))
	rej := q("ec2", "reject-capacity-reservation-billing-ownership", "--capacity-reservation-id", cr2,
		"--query", "Return", "--output", "text")
	if rej != "True" {
		t.Fatalf("reject billing ownership Return: got %q, want True", rej)
	}
	dis := q("ec2", "disassociate-capacity-reservation-billing-owner",
		"--capacity-reservation-id", cr2, "--unused-reservation-billing-owner-id", "210987654321",
		"--query", "Return", "--output", "text")
	if dis != "True" {
		t.Fatalf("disassociate billing owner Return: got %q, want True", dis)
	}

	// --- splitting / moving ---
	src := q("ec2", "create-capacity-reservation",
		"--instance-type", "m5.large", "--instance-platform", "Linux/UNIX",
		"--availability-zone", "us-east-1a", "--instance-count", "10",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")

	splitCount := q("ec2", "create-capacity-reservation-by-splitting",
		"--source-capacity-reservation-id", src, "--instance-count", "4",
		"--query", "InstanceCount", "--output", "text")
	if splitCount != "4" {
		t.Fatalf("split InstanceCount: got %q, want 4", splitCount)
	}
	destID := q("ec2", "create-capacity-reservation-by-splitting",
		"--source-capacity-reservation-id", src, "--instance-count", "1",
		"--query", "DestinationCapacityReservation.CapacityReservationId", "--output", "text")
	if destID == "" || destID == "None" {
		t.Fatal("split returned empty destination id")
	}

	moveCount := q("ec2", "move-capacity-reservation-instances",
		"--source-capacity-reservation-id", src, "--destination-capacity-reservation-id", destID,
		"--instance-count", "2", "--query", "InstanceCount", "--output", "text")
	if moveCount != "2" {
		t.Fatalf("move InstanceCount: got %q, want 2", moveCount)
	}

	modRet := q("ec2", "modify-instance-capacity-reservation-attributes",
		"--instance-id", "i-0123456789abcdef0",
		"--capacity-reservation-specification", "CapacityReservationPreference=open",
		"--query", "Return", "--output", "text")
	if modRet != "True" {
		t.Fatalf("modify-instance-capacity-reservation-attributes Return: got %q, want True", modRet)
	}
}

// TestEC2CLI_CapacityBlock covers the Capacity Block (ML) flow via the aws CLI:
// describe offerings, purchase a block, then the extension offering / purchase /
// history. describe-capacity-blocks and describe-capacity-block-status are
// SDK-only (missing from aws CLI 2.26.6); the SDK test covers their hook.
func TestEC2CLI_CapacityBlock(t *testing.T) {
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	offeringID := q("ec2", "describe-capacity-block-offerings",
		"--instance-type", "p5.48xlarge", "--instance-count", "2", "--capacity-duration-hours", "48",
		"--query", "CapacityBlockOfferings[0].CapacityBlockOfferingId", "--output", "text")
	if offeringID == "" || offeringID == "None" {
		t.Fatal("describe-capacity-block-offerings returned no offering")
	}

	crID := q("ec2", "purchase-capacity-block",
		"--capacity-block-offering-id", offeringID, "--instance-platform", "Linux/UNIX",
		"--query", "CapacityReservation.CapacityReservationId", "--output", "text")
	if crID == "" || crID == "None" {
		t.Fatal("purchase-capacity-block returned empty reservation id")
	}

	extOffering := q("ec2", "describe-capacity-block-extension-offerings",
		"--capacity-reservation-id", crID, "--capacity-block-extension-duration-hours", "24",
		"--query", "CapacityBlockExtensionOfferings[0].CapacityBlockExtensionOfferingId", "--output", "text")
	if extOffering == "" || extOffering == "None" {
		t.Fatal("describe-capacity-block-extension-offerings returned no offering")
	}

	extCR := q("ec2", "purchase-capacity-block-extension",
		"--capacity-reservation-id", crID, "--capacity-block-extension-offering-id", extOffering,
		"--query", "CapacityBlockExtensions[0].CapacityReservationId", "--output", "text")
	if extCR != crID {
		t.Fatalf("purchase extension CapacityReservationId: got %q, want %q", extCR, crID)
	}

	histCR := q("ec2", "describe-capacity-block-extension-history",
		"--capacity-reservation-ids", crID,
		"--query", "CapacityBlockExtensions[0].CapacityReservationId", "--output", "text")
	if histCR != crID {
		t.Fatalf("extension history CapacityReservationId: got %q, want %q", histCR, crID)
	}
}
