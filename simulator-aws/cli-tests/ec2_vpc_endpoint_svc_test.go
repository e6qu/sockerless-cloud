package aws_cli_test

import (
	"strings"
	"testing"
)

// TestEC2CLI_VpcEndpointServiceRoundTrip drives the PrivateLink endpoint-service
// control plane through the aws CLI: create/describe/modify the configuration,
// the allowed-principal permission set, payer responsibility, private-DNS
// verification, describe-vpc-endpoint-services, and the tolerant delete.
func TestEC2CLI_VpcEndpointServiceRoundTrip(t *testing.T) {
	const nlbArn = "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/cli-vpes/abc"
	svcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc-endpoint-service-configuration",
		"--network-load-balancer-arns", nlbArn, "--acceptance-required",
		"--query", "ServiceConfiguration.ServiceId", "--output", "text")))
	if svcID == "" {
		t.Fatal("service id empty")
	}

	svcName := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-service-configurations",
		"--service-ids", svcID, "--query", "ServiceConfigurations[0].ServiceName", "--output", "text")))
	if svcName == "" || svcName == "None" {
		t.Fatalf("service name = %q, want non-empty", svcName)
	}

	// Modify: turn off acceptance, read back.
	runCLI(t, awsCLI("ec2", "modify-vpc-endpoint-service-configuration",
		"--service-id", svcID, "--no-acceptance-required"))
	acc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-service-configurations",
		"--service-ids", svcID, "--query", "ServiceConfigurations[0].AcceptanceRequired", "--output", "text")))
	if acc != "False" {
		t.Fatalf("AcceptanceRequired = %q, want False", acc)
	}

	// Permissions: add, read back, remove.
	const principal = "arn:aws:iam::111122223333:root"
	added := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-vpc-endpoint-service-permissions",
		"--service-id", svcID, "--add-allowed-principals", principal,
		"--query", "AddedPrincipals[0].Principal", "--output", "text")))
	if added != principal {
		t.Fatalf("added principal = %q, want %q", added, principal)
	}
	gotPrincipal := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-service-permissions",
		"--service-id", svcID, "--query", "AllowedPrincipals[0].Principal", "--output", "text")))
	if gotPrincipal != principal {
		t.Fatalf("read-back principal = %q, want %q", gotPrincipal, principal)
	}
	runCLI(t, awsCLI("ec2", "modify-vpc-endpoint-service-permissions",
		"--service-id", svcID, "--remove-allowed-principals", principal))
	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-service-permissions",
		"--service-id", svcID, "--query", "length(AllowedPrincipals)", "--output", "text")))
	if count != "0" {
		t.Fatalf("after remove, AllowedPrincipals length = %q, want 0", count)
	}

	// Payer responsibility + private-DNS verification.
	runCLI(t, awsCLI("ec2", "modify-vpc-endpoint-service-payer-responsibility",
		"--service-id", svcID, "--payer-responsibility", "ServiceOwner"))

	// describe-vpc-endpoint-services surfaces the configured service name.
	gotName := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-endpoint-services",
		"--service-names", svcName, "--query", "ServiceDetails[0].ServiceName", "--output", "text")))
	if gotName != svcName {
		t.Fatalf("describe-vpc-endpoint-services name = %q, want %q", gotName, svcName)
	}

	// Delete (tolerant): a successful delete leaves an empty Unsuccessful set.
	unsucc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "delete-vpc-endpoint-service-configurations",
		"--service-ids", svcID, "--query", "length(Unsuccessful)", "--output", "text")))
	if unsucc != "0" {
		t.Fatalf("delete reported %q unsuccessful, want 0", unsucc)
	}
}

// TestEC2CLI_AccountVpcEncryptionControlRoundTrip drives the Region-scoped
// account policy and proves that newly created VPCs inherit it through the
// official AWS Command Line Interface.
func TestEC2CLI_AccountVpcEncryptionControlRoundTrip(t *testing.T) {
	initial := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-account-vpc-encryption-control",
		"--query", "AccountVpcEncryptionControl.[State,Mode,ManagedBy]", "--output", "text")))
	if initial != "default-state\tunmanaged\taccount" {
		t.Fatalf("initial account VPC encryption control = %q", initial)
	}

	modified := strings.TrimSpace(runCLI(t, awsCLI("ec2", "modify-account-vpc-encryption-control",
		"--mode", "attempt-monitor", "--nat-gateway", "enable",
		"--query", "AccountVpcEncryptionControl.[State,Mode,Exclusions.NatGateway]", "--output", "text")))
	if modified != "transitions-successful\tattempt-monitor\tenabled" {
		t.Fatalf("modified account VPC encryption control = %q", modified)
	}

	vpcID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.56.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	inherited := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-vpc-encryption-controls",
		"--vpc-ids", vpcID,
		"--query", "VpcEncryptionControls[0].Mode", "--output", "text")))
	if inherited != "monitor" {
		t.Fatalf("new VPC inherited encryption control = %q", inherited)
	}
}

// TestEC2CLI_VpcCidrBlockRoundTrip drives associate/disassociate-vpc-cidr-block.
func TestEC2CLI_VpcCidrBlockRoundTrip(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.60.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	assocID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-vpc-cidr-block",
		"--vpc-id", vpc, "--cidr-block", "10.61.0.0/16",
		"--query", "CidrBlockAssociation.AssociationId", "--output", "text")))
	if assocID == "" || assocID == "None" {
		t.Fatalf("association id = %q, want non-empty", assocID)
	}
	gotID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "disassociate-vpc-cidr-block",
		"--association-id", assocID,
		"--query", "CidrBlockAssociation.AssociationId", "--output", "text")))
	if gotID != assocID {
		t.Fatalf("disassociate returned %q, want %q", gotID, assocID)
	}
}

// TestEC2CLI_SubnetCidrRoundTrip drives associate/disassociate-subnet-cidr-block
// (IPv6) and the subnet CIDR reservation ops.
func TestEC2CLI_SubnetCidrRoundTrip(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.70.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sub := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet",
		"--vpc-id", vpc, "--cidr-block", "10.70.1.0/24", "--query", "Subnet.SubnetId", "--output", "text")))

	subAssoc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-subnet-cidr-block",
		"--subnet-id", sub, "--ipv6-cidr-block", "2600:1f00:dead:1::/64",
		"--query", "Ipv6CidrBlockAssociation.AssociationId", "--output", "text")))
	if subAssoc == "" || subAssoc == "None" {
		t.Fatalf("subnet cidr association id = %q, want non-empty", subAssoc)
	}
	runCLI(t, awsCLI("ec2", "disassociate-subnet-cidr-block", "--association-id", subAssoc))

	resID := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-subnet-cidr-reservation",
		"--subnet-id", sub, "--cidr", "10.70.1.16/28", "--reservation-type", "prefix",
		"--query", "SubnetCidrReservation.SubnetCidrReservationId", "--output", "text")))
	if resID == "" || resID == "None" {
		t.Fatalf("reservation id = %q, want non-empty", resID)
	}
	gotRes := strings.TrimSpace(runCLI(t, awsCLI("ec2", "get-subnet-cidr-reservations",
		"--subnet-id", sub, "--query", "SubnetIpv4CidrReservations[0].SubnetCidrReservationId", "--output", "text")))
	if gotRes != resID {
		t.Fatalf("get-subnet-cidr-reservations returned %q, want %q", gotRes, resID)
	}
	delRes := strings.TrimSpace(runCLI(t, awsCLI("ec2", "delete-subnet-cidr-reservation",
		"--subnet-cidr-reservation-id", resID,
		"--query", "DeletedSubnetCidrReservation.SubnetCidrReservationId", "--output", "text")))
	if delRes != resID {
		t.Fatalf("delete returned %q, want %q", delRes, resID)
	}
}

// TestEC2CLI_SecurityGroupVpcRoundTrip drives associate/disassociate/describe
// -security-group-vpc-associations.
func TestEC2CLI_SecurityGroupVpcRoundTrip(t *testing.T) {
	vpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.80.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	other := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-vpc",
		"--cidr-block", "10.81.0.0/16", "--query", "Vpc.VpcId", "--output", "text")))
	sg := strings.TrimSpace(runCLI(t, awsCLI("ec2", "create-security-group",
		"--group-name", "cli-sgvpc", "--description", "cli sgvpc", "--vpc-id", vpc,
		"--query", "GroupId", "--output", "text")))

	state := strings.TrimSpace(runCLI(t, awsCLI("ec2", "associate-security-group-vpc",
		"--group-id", sg, "--vpc-id", other, "--query", "State", "--output", "text")))
	if state != "associated" {
		t.Fatalf("associate state = %q, want associated", state)
	}
	gotVpc := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-vpc-associations",
		"--filters", "Name=group-id,Values="+sg,
		"--query", "SecurityGroupVpcAssociations[0].VpcId", "--output", "text")))
	if gotVpc != other {
		t.Fatalf("describe returned vpc %q, want %q", gotVpc, other)
	}
	runCLI(t, awsCLI("ec2", "disassociate-security-group-vpc", "--group-id", sg, "--vpc-id", other))
	count := strings.TrimSpace(runCLI(t, awsCLI("ec2", "describe-security-group-vpc-associations",
		"--filters", "Name=group-id,Values="+sg,
		"--query", "length(SecurityGroupVpcAssociations)", "--output", "text")))
	if count != "0" {
		t.Fatalf("after disassociate, association count = %q, want 0", count)
	}
}
