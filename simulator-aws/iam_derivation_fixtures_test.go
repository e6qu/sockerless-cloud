package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// The resources the state-resolving derivations look up, created through the
// simulator's own API.
//
// Some derivations answer by resolving an identifier to the resource it belongs
// to — Amazon EC2's DisassociateRouteTable names an association and the route
// table is what the call authorizes against. The coverage probe used to send
// those requests against an empty simulator, so a reader that works measured as
// absent, and the number understated the derivation.
//
// Writing rows into a store to make the number move would measure the fixture.
// Creating the resource the way a client creates it and then probing the
// derivation against it measures the reader, which is the question the ratchet
// is asking. So these go through the same handlers an SDK test drives.

// iamSeedDerivationFixtures creates those resources and returns the identifiers
// a request would name them by, keyed "<service>:<operation>:<member>" — per
// operation, because two calls can name different things through the same
// member: DisassociateRouteTable and DisassociateAddress both say
// AssociationId and mean associations of different kinds.
func iamSeedDerivationFixtures(t *testing.T,
	queryRouter *sim.AWSQueryRouter, jsonRouter *sim.AWSRouter,
) map[string]string {
	t.Helper()
	out := map[string]string{}

	// Amazon EC2's route-table association: a VPC to hold it, a subnet to
	// attach to, and the association the disassociate names.
	vpc := iamFixtureEC2(t, queryRouter, "CreateVpc",
		map[string]string{"CidrBlock": "10.0.0.0/16"}, `<vpcId>([^<]+)</vpcId>`)
	subnet := iamFixtureEC2(t, queryRouter, "CreateSubnet",
		map[string]string{"VpcId": vpc, "CidrBlock": "10.0.1.0/24"}, `<subnetId>([^<]+)</subnetId>`)
	routeTable := iamFixtureEC2(t, queryRouter, "CreateRouteTable",
		map[string]string{"VpcId": vpc}, `<routeTableId>([^<]+)</routeTableId>`)
	association := iamFixtureEC2(t, queryRouter, "AssociateRouteTable",
		map[string]string{"RouteTableId": routeTable, "SubnetId": subnet},
		`<associationId>([^<]+)</associationId>`)
	out["ec2:DisassociateRouteTable:AssociationId"] = association

	// An elastic IP's association, which the disassociate names the same way.
	// It is attached to an interface rather than an instance, because an
	// interface is a resource this simulator makes without running anything.
	allocation := iamFixtureEC2(t, queryRouter, "AllocateAddress",
		map[string]string{"Domain": "vpc"}, `<allocationId>([^<]+)</allocationId>`)
	networkInterface := iamFixtureEC2(t, queryRouter, "CreateNetworkInterface",
		map[string]string{"SubnetId": subnet}, `<networkInterfaceId>([^<]+)</networkInterfaceId>`)
	addressAssociation := iamFixtureEC2(t, queryRouter, "AssociateAddress",
		map[string]string{"AllocationId": allocation, "NetworkInterfaceId": networkInterface},
		`<associationId>([^<]+)</associationId>`)
	out["ec2:DisassociateAddress:AssociationId"] = addressAssociation

	// An interface's attachment, which the detach names. Attaching one needs a
	// machine to attach it to.
	instance := iamFixtureEC2(t, queryRouter, "RunInstances",
		map[string]string{
			"ImageId": "ami-0123456789abcdef0", "MinCount": "1", "MaxCount": "1",
			"InstanceType": "t3.micro", "SubnetId": subnet,
		}, `<instanceId>([^<]+)</instanceId>`)
	detachable := iamFixtureEC2(t, queryRouter, "CreateNetworkInterface",
		map[string]string{"SubnetId": subnet}, `<networkInterfaceId>([^<]+)</networkInterfaceId>`)
	attachment := iamFixtureEC2(t, queryRouter, "AttachNetworkInterface",
		map[string]string{
			"NetworkInterfaceId": detachable, "InstanceId": instance, "DeviceIndex": "1",
		}, `<attachmentId>([^<]+)</attachmentId>`)
	out["ec2:DetachNetworkInterface:AttachmentId"] = attachment

	// An instance profile bound to a machine, which the disassociate and the
	// replace both name by the association rather than by the machine.
	profileAssociation := iamFixtureEC2(t, queryRouter, "AssociateIamInstanceProfile",
		map[string]string{
			"InstanceId": instance, "IamInstanceProfile.Name": "probe-profile",
		}, `<associationId>([^<]+)</associationId>`)
	out["ec2:DisassociateIamInstanceProfile:AssociationId"] = profileAssociation
	out["ec2:ReplaceIamInstanceProfileAssociation:AssociationId"] = profileAssociation

	// A permission granted on an interface, which the delete names by the
	// permission rather than by the interface.
	permission := iamFixtureEC2(t, queryRouter, "CreateNetworkInterfacePermission",
		map[string]string{
			"NetworkInterfaceId": detachable, "AwsAccountId": iamProbeAccount,
			"Permission": "INSTANCE-ATTACH",
		}, `<networkInterfacePermissionId>([^<]+)</networkInterfacePermissionId>`)
	out["ec2:DeleteNetworkInterfacePermission:NetworkInterfacePermissionId"] = permission

	// The CIDR associations, one on the VPC and one on a subnet, each named by
	// the association rather than by what it is on.
	vpcCidr := iamFixtureEC2(t, queryRouter, "AssociateVpcCidrBlock",
		map[string]string{"VpcId": vpc, "CidrBlock": "10.2.0.0/16"},
		`<associationId>([^<]+)</associationId>`)
	out["ec2:DisassociateVpcCidrBlock:AssociationId"] = vpcCidr
	subnetCidr := iamFixtureEC2(t, queryRouter, "AssociateSubnetCidrBlock",
		map[string]string{"SubnetId": subnet}, `<associationId>([^<]+)</associationId>`)
	out["ec2:DisassociateSubnetCidrBlock:AssociationId"] = subnetCidr

	// A CloudWatch Logs Insights query, which the results read names by the
	// query rather than by the groups it ran over.
	query := iamFixtureJSON(t, jsonRouter, "Logs_20140328.StartQuery",
		`{"logGroupNames":["probe-group"],"queryString":"fields @message",`+
			`"startTime":0,"endTime":1}`,
		`"queryId"\s*:\s*"([^"]+)"`)
	out["logs:GetQueryResults:queryId"] = query

	// AWS Glue's data-quality runs, which its derivation resolves to the
	// ruleset each is about. A recommendation run creates a ruleset; an
	// evaluation run evaluates one and settles a result row per ruleset.
	recommendation := iamFixtureJSON(t, jsonRouter, "AWSGlue.StartDataQualityRuleRecommendationRun",
		`{"Role":"probe","CreatedRulesetName":"probe-ruleset","DataSource":{}}`,
		`"RunId"\s*:\s*"([^"]+)"`)
	out["glue:GetDataQualityRuleRecommendationRun:RunId"] = recommendation
	out["glue:CancelDataQualityRuleRecommendationRun:RunId"] = recommendation

	evaluation := iamFixtureJSON(t, jsonRouter, "AWSGlue.StartDataQualityRulesetEvaluationRun",
		`{"Role":"probe","RulesetNames":["probe-ruleset"],"DataSource":{}}`,
		`"RunId"\s*:\s*"([^"]+)"`)
	out["glue:GetDataQualityRulesetEvaluationRun:RunId"] = evaluation
	out["glue:CancelDataQualityRulesetEvaluationRun:RunId"] = evaluation

	results := iamFixtureJSON(t, jsonRouter, "AWSGlue.ListDataQualityResults",
		`{}`, `"ResultId"\s*:\s*"([^"]+)"`)
	out["glue:GetDataQualityResult:ResultId"] = results

	// An access key, which AWS Identity and Access Management resolves to the
	// user it was created for — the key being the only thing the request names.
	iamFixtureQuery(t, queryRouter, "2010-05-08", "CreateUser",
		map[string]string{"UserName": "probe-key-owner"}, `<UserName>([^<]+)</UserName>`)
	accessKey := iamFixtureQuery(t, queryRouter, "2010-05-08", "CreateAccessKey",
		map[string]string{"UserName": "probe-key-owner"}, `<AccessKeyId>([^<]+)</AccessKeyId>`)
	out["iam:GetAccessKeyLastUsed:AccessKeyId"] = accessKey

	// An Auto Scaling group and the launch configuration it runs from, named
	// the way the probe names them. Every Auto Scaling derivation resolves the
	// group through the simulator's own record of it, so the record has to be
	// one the service made rather than a row written into the store.
	iamFixtureQuery(t, queryRouter, "2011-01-01", "CreateLaunchConfiguration",
		map[string]string{
			"LaunchConfigurationName": "probe", "ImageId": "ami-0123456789abcdef0",
			"InstanceType": "t3.micro",
		}, `(CreateLaunchConfigurationResponse)`)
	iamFixtureQuery(t, queryRouter, "2011-01-01", "CreateAutoScalingGroup",
		map[string]string{
			"AutoScalingGroupName": "probe", "MinSize": "0", "MaxSize": "1",
			"AvailabilityZones.member.1": "us-east-1a",
			"LaunchConfigurationName":    "probe",
		}, `(CreateAutoScalingGroupResponse)`)
	// A database cluster, whose automated backup Amazon RDS keeps under the
	// cluster's own resource id — which is all the delete carries.
	cluster := iamFixtureQuery(t, queryRouter, "2014-10-31", "CreateDBCluster",
		map[string]string{
			"DBClusterIdentifier": "probe-cluster", "Engine": "aurora-postgresql",
			"MasterUsername": "probe", "MasterUserPassword": "probe-password",
		}, `<DbClusterResourceId>([^<]+)</DbClusterResourceId>`)
	// The backup row is materialised when the backups are described, which is
	// what a client does before deleting one.
	iamFixtureQuery(t, queryRouter, "2014-10-31", "DescribeDBClusterAutomatedBackups",
		map[string]string{}, `<DbClusterResourceId>([^<]+)</DbClusterResourceId>`)
	out["rds:DeleteDBClusterAutomatedBackup:DbClusterResourceId"] = cluster

	// A maintenance window, named by the execution id AWS Systems Manager
	// derives from it — which is all a cancellation carries.
	window := iamFixtureJSON(t, jsonRouter, "AmazonSSM.CreateMaintenanceWindow",
		`{"Name":"probe-window","Schedule":"rate(1 day)","Duration":1,"Cutoff":0,`+
			`"AllowUnassociatedTargets":true}`,
		`"WindowId"\s*:\s*"([^"]+)"`)
	out["ssm:CancelMaintenanceWindowExecution:WindowExecutionId"] = ssmWindowExecID(window)

	return out
}

// iamFixtureEC2 performs one EC2 query call and reads the identifier the
// service assigned out of the response.
//
// It reaches the action's own handler through the router rather than the
// server's front door, which skips only the signature check: what makes the
// fixture real is that the resource is built by the service's own creation
// logic, and whether the request was signed is a question about a different
// layer.
func iamFixtureEC2(t *testing.T, queryRouter *sim.AWSQueryRouter, action string,
	params map[string]string, pattern string,
) string {
	t.Helper()
	return iamFixtureQuery(t, queryRouter, "2016-11-15", action, params, pattern)
}

// iamFixtureQuery performs one query-protocol call at a service's own API
// version and reads the identifier the service assigned out of the response.
func iamFixtureQuery(t *testing.T, queryRouter *sim.AWSQueryRouter,
	version, action string, params map[string]string, pattern string,
) string {
	t.Helper()
	form := url.Values{"Action": {action}, "Version": {version}}
	for name, value := range params {
		form.Set(name, value)
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=00")
	handler, mounted := queryRouter.Handler(version, action)
	if !mounted {
		t.Fatalf("%s: no handler mounted for the EC2 query action", action)
	}
	rec := httptest.NewRecorder()
	handler(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", action, rec.Code, rec.Body.String())
	}
	found := regexp.MustCompile(pattern).FindStringSubmatch(rec.Body.String())
	if found == nil {
		t.Fatalf("%s: no %s in %s", action, pattern, rec.Body.String())
	}
	return found[1]
}

// iamFixtureJSON performs one awsJson call and reads an identifier out of the
// response, reaching the target's handler the same way iamFixtureEC2 does.
func iamFixtureJSON(t *testing.T, jsonRouter *sim.AWSRouter,
	target, body, pattern string,
) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	handler, mounted := jsonRouter.Handler(target)
	if !mounted {
		t.Fatalf("%s: no handler mounted for the target", target)
	}
	rec := httptest.NewRecorder()
	handler(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d: %s", target, rec.Code, rec.Body.String())
	}
	found := regexp.MustCompile(pattern).FindStringSubmatch(rec.Body.String())
	if found == nil {
		t.Fatalf("%s: no %s in %s", target, pattern, rec.Body.String())
	}
	return found[1]
}
