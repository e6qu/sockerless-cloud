package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// These pin the ARN the gate requests for the services BUG-2907 named. The
// assertions are the ARN shapes the AWS Service Reference publishes for each
// resource type, not what the code happens to build: an ARN that is merely
// self-consistent still denies every policy written against the real one.

func iamJSONRequest(target, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/aws/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

func iamQueryRequest(action, version string, params map[string]string) *http.Request {
	form := "Action=" + action + "&Version=" + version
	for k, v := range params {
		form += "&" + k + "=" + v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/iam/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

func assertDerivedARNs(t *testing.T, r *http.Request, wantAction string, want ...string) {
	t.Helper()
	action, ok := iamActionForRequest(r)
	if !ok {
		t.Fatalf("request was not classified as an IAM action")
	}
	if action != wantAction {
		t.Fatalf("action = %q, want %q", action, wantAction)
	}
	got := iamResourceARNsForRequest(r, action)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s resources = %v, want %v", action, got, want)
	}
}

func TestIAMResourceARNs_ECS(t *testing.T) {
	const p = "arn:aws:ecs:us-east-1:123456789012:"
	t.Run("RunTask names its task definition, not its cluster", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.RunTask",
				`{"cluster":"edd","taskDefinition":"edd-control-plane:7"}`),
			"ecs:RunTask", p+"task-definition/edd-control-plane:7")
	})
	t.Run("a service ARN embeds its cluster", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeServices",
				`{"cluster":"edd","services":["control-plane","ssh-gateway"]}`),
			"ecs:DescribeServices", p+"service/edd/control-plane", p+"service/edd/ssh-gateway")
	})
	t.Run("an omitted cluster is the default cluster", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.StopTask", `{"task":"abc123"}`),
			"ecs:StopTask", p+"task/default/abc123")
	})
	t.Run("ExecuteCommand names both the cluster and the task", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.ExecuteCommand",
				`{"cluster":"edd","task":"abc123","command":"sh","interactive":true}`),
			"ecs:ExecuteCommand", p+"task/edd/abc123", p+"cluster/edd")
	})
	t.Run("DescribeContainerInstances does not also demand the cluster", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeContainerInstances",
				`{"cluster":"edd","containerInstances":["i-1"]}`),
			"ecs:DescribeContainerInstances", p+"container-instance/edd/i-1")
	})
	t.Run("an operation AWS declares no resource type for stays *", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition",
				`{"taskDefinition":"edd-control-plane:7"}`),
			"ecs:DescribeTaskDefinition", "*")
	})
	t.Run("tagging names the resource by its own ARN", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.TagResource",
				`{"resourceArn":"`+p+`cluster/edd","tags":[]}`),
			"ecs:TagResource", p+"cluster/edd")
	})
}

// RegisterTaskDefinition authorizes against the task definition it is about to
// create, so the requested ARN carries the revision the call will be assigned.
func TestIAMResourceARNs_ECSRegisterTaskDefinitionCarriesTheNextRevision(t *testing.T) {
	ecsRevisionMu.Lock()
	if ecsRevisions == nil {
		ecsRevisions = map[string]int{}
	}
	ecsRevisions["edd-control-plane"] = 6
	ecsRevisionMu.Unlock()
	t.Cleanup(func() {
		ecsRevisionMu.Lock()
		delete(ecsRevisions, "edd-control-plane")
		ecsRevisionMu.Unlock()
	})
	assertDerivedARNs(t,
		iamJSONRequest("AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition",
			`{"family":"edd-control-plane","containerDefinitions":[]}`),
		"ecs:RegisterTaskDefinition",
		"arn:aws:ecs:us-east-1:123456789012:task-definition/edd-control-plane:7")
}

// The four stream-scoped actions authorize against the log stream; everything
// else that names a group authorizes against the group. The group ARN carries
// no trailing ":*" — that suffix appears in some API responses, never in the
// resource an authorization request names.
func TestIAMResourceARNs_CloudWatchLogs(t *testing.T) {
	const group = "arn:aws:logs:us-east-1:123456789012:log-group:/aws/ecs/edd"
	t.Run("PutLogEvents names the stream", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.PutLogEvents",
				`{"logGroupName":"/aws/ecs/edd","logStreamName":"control-plane/abc","logEvents":[]}`),
			"logs:PutLogEvents", group+":log-stream:control-plane/abc")
	})
	t.Run("FilterLogEvents names the group with no trailing wildcard", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.FilterLogEvents", `{"logGroupName":"/aws/ecs/edd"}`),
			"logs:FilterLogEvents", group)
	})
	t.Run("DescribeLogStreams is group-scoped even though it is about streams", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.DescribeLogStreams",
				`{"logGroupName":"/aws/ecs/edd","logStreamNamePrefix":"control-plane"}`),
			"logs:DescribeLogStreams", group)
	})
	t.Run("DescribeLogGroups supports no resource-level permission", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.DescribeLogGroups", `{}`),
			"logs:DescribeLogGroups", "*")
	})

	// The families beyond log groups each authorize against a resource type of
	// their own, and each ARN is "<type>:<name>" over what the request already
	// carries. The exact string matters more than the fact one was produced: a
	// derivation that builds the wrong ARN authorizes the wrong resource, which
	// is worse than deriving nothing and falling back to "*".
	const logs = "arn:aws:logs:us-east-1:123456789012:"
	t.Run("a subscription destination is named by destinationName", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.PutDestination",
				`{"destinationName":"central","targetArn":"arn:aws:kinesis:us-east-1:123456789012:stream/s","roleArn":"arn:aws:iam::123456789012:role/r"}`),
			"logs:PutDestination", logs+"destination:central")
	})
	t.Run("a delivery is named by its id", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.GetDelivery", `{"id":"AbCdEf123"}`),
			"logs:GetDelivery", logs+"delivery:AbCdEf123")
	})
	t.Run("a delivery destination policy names its destination", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.PutDeliveryDestinationPolicy",
				`{"deliveryDestinationName":"to-s3","deliveryDestinationPolicy":"{}"}`),
			"logs:PutDeliveryDestinationPolicy", logs+"delivery-destination:to-s3")
	})
	t.Run("a delivery source is named by name under its own type", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.GetDeliverySource", `{"name":"from-waf"}`),
			"logs:GetDeliverySource", logs+"delivery-source:from-waf")
	})
	t.Run("an anomaly detector is named by the ARN it carries", func(t *testing.T) {
		detector := logs + "anomaly-detector:1234abcd"
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.GetLogAnomalyDetector",
				`{"anomalyDetectorArn":"`+detector+`"}`),
			"logs:GetLogAnomalyDetector", detector)
	})
	t.Run("creating a delivery names its destination and its source", func(t *testing.T) {
		destination := logs + "delivery-destination:to-s3"
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.CreateDelivery",
				`{"deliveryDestinationArn":"`+destination+`","deliverySourceName":"from-waf"}`),
			"logs:CreateDelivery", destination, logs+"delivery-source:from-waf")
	})
	t.Run("creating an anomaly detector names the log groups it watches", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.CreateLogAnomalyDetector",
				`{"detectorName":"d","logGroupArnList":["`+group+`"]}`),
			"logs:CreateLogAnomalyDetector", group)
	})
	t.Run("an opaque record pointer names nothing", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.GetLogRecord", `{"logRecordPointer":"AYm...opaque"}`),
			"logs:GetLogRecord", "*")
	})
	t.Run("a log group request is unaffected by the named families", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("Logs_20140328.DeleteLogGroup", `{"logGroupName":"/aws/ecs/edd"}`),
			"logs:DeleteLogGroup", group)
	})
}

// The Amazon ECS families that name themselves by ARN — a daemon and its
// deployments and revisions, an Amazon ECS Express Mode service, a service
// deployment or revision — plus the daemon ARNs that are assembled from a
// cluster and a name.
func TestIAMResourceARNs_ECSDaemonAndExpressMode(t *testing.T) {
	const ecs = "arn:aws:ecs:us-east-1:123456789012:"
	t.Run("a daemon is named by the ARN it carries", func(t *testing.T) {
		daemon := ecs + "daemon/prod/collector"
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeDaemon",
				`{"daemonArn":"`+daemon+`"}`),
			"ecs:DescribeDaemon", daemon)
	})
	// The case that decides whether the reader is type-aware: this request
	// carries two ARNs, and only one of them is the resource it authorizes
	// against. Taking the other would let a policy scoped to a task definition
	// permit creating a daemon.
	t.Run("creating a daemon names the daemon, not the task definition it runs", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.CreateDaemon",
				`{"clusterArn":"`+ecs+`cluster/prod","daemonName":"collector",`+
					`"daemonTaskDefinitionArn":"`+ecs+`daemon-task-definition/collector:3"}`),
			"ecs:CreateDaemon", ecs+"daemon/prod/collector")
	})
	t.Run("a daemon task definition is named family:revision", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeDaemonTaskDefinition",
				`{"daemonTaskDefinition":"collector:3"}`),
			"ecs:DescribeDaemonTaskDefinition", ecs+"daemon-task-definition/collector:3")
	})
	t.Run("an Express Mode service is named by its service ARN", func(t *testing.T) {
		service := ecs + "service/prod/checkout"
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeExpressGatewayService",
				`{"serviceArn":"`+service+`"}`),
			"ecs:DescribeExpressGatewayService", service)
	})
	t.Run("service deployments are named by every ARN the request lists", func(t *testing.T) {
		first := ecs + "service-deployment/prod/checkout/1111"
		second := ecs + "service-deployment/prod/checkout/2222"
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeServiceDeployments",
				`{"serviceDeploymentArns":["`+first+`","`+second+`"]}`),
			"ecs:DescribeServiceDeployments", first, second)
	})
	t.Run("a task-definition request is unaffected by the daemon families", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.RunTask",
				`{"cluster":"prod","taskDefinition":"web:7"}`),
			"ecs:RunTask", ecs+"task-definition/web:7")
	})
	// AWS declares no resource type for DescribeTaskDefinition, which is how it
	// says the action takes no resource-level permission. "*" is the right
	// answer, and a derivation that invented a task-definition ARN here would
	// make a policy scoped to one revision appear to restrict a call AWS does
	// not scope at all.
	t.Run("an action AWS scopes to no resource stays unscoped", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DescribeTaskDefinition",
				`{"taskDefinition":"web:7"}`),
			"ecs:DescribeTaskDefinition", "*")
	})
}

// PutAttributes and DeleteAttributes authorize against the container instance
// each attribute targets, which the request carries as the attribute's
// targetId rather than as a container-instance member of its own.
//
// The derivation-coverage probe cannot express this: it sends a list member as
// a list of strings, so the attributes it sends carry no targetId and these
// two are still counted as underived. A real caller sends objects, and this is
// what a real caller gets — the same situation the floor comment records for
// the Amazon RDS tagging operations.
func TestIAMResourceARNs_ECSAttributesNameTheirContainerInstance(t *testing.T) {
	const ecs = "arn:aws:ecs:us-east-1:123456789012:"
	t.Run("an attribute names the instance it targets", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.PutAttributes",
				`{"cluster":"prod","attributes":[{"name":"rack","value":"r1","targetId":"abc123"}]}`),
			"ecs:PutAttributes", ecs+"container-instance/prod/abc123")
	})
	t.Run("several attributes name every instance once", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.DeleteAttributes",
				`{"cluster":"prod","attributes":[{"name":"rack","targetId":"abc123"},`+
					`{"name":"zone","targetId":"abc123"},{"name":"rack","targetId":"def456"}]}`),
			"ecs:DeleteAttributes",
			ecs+"container-instance/prod/abc123", ecs+"container-instance/prod/def456")
	})
	t.Run("a target given as an ARN is taken as it stands", func(t *testing.T) {
		instance := ecs + "container-instance/prod/abc123"
		assertDerivedARNs(t,
			iamJSONRequest("AmazonEC2ContainerServiceV20141113.PutAttributes",
				`{"cluster":"prod","attributes":[{"name":"rack","targetId":"`+instance+`"}]}`),
			"ecs:PutAttributes", instance)
	})
}

// AWS Systems Manager's tagging operations name the resource type they are
// about, and that type decides which ARN the identifier fills.
//
// The coverage probe cannot express this: it fills every member with a
// placeholder, and "probe" is not a ResourceTypeForTagging value, so these
// still measure as underived. A real caller sends one of the ten the service
// declares — and a type the service does not declare derives nothing rather
// than guessing, because filling all eleven declared types from a bare
// ResourceId would authorize against ten resources the request is not about.
func TestIAMResourceARNs_SSMTaggingNamesTheTypeItTags(t *testing.T) {
	const ssm = "arn:aws:ssm:us-east-1:123456789012:"
	for _, tc := range []struct{ resourceType, id, want string }{
		{"Parameter", "/db/password", ssm + "parameter/db/password"},
		{"Document", "My-Doc", ssm + "document/My-Doc"},
		{"ManagedInstance", "mi-0123456789abcdef0", ssm + "managed-instance/mi-0123456789abcdef0"},
		{"MaintenanceWindow", "mw-0123456789abcdef0", ssm + "maintenancewindow/mw-0123456789abcdef0"},
		{"PatchBaseline", "pb-0123456789abcdef0", ssm + "patchbaseline/pb-0123456789abcdef0"},
		{"OpsItem", "oi-0123456789ab", ssm + "opsitem/oi-0123456789ab"},
		{"Automation", "exec-1234", ssm + "automation-execution/exec-1234"},
		{"Association", "assoc-1234", ssm + "association/assoc-1234"},
	} {
		t.Run(tc.resourceType, func(t *testing.T) {
			assertDerivedARNs(t,
				iamJSONRequest("AmazonSSM.AddTagsToResource",
					`{"ResourceType":"`+tc.resourceType+`","ResourceId":"`+tc.id+`","Tags":[]}`),
				"ssm:AddTagsToResource", tc.want)
		})
	}
	t.Run("a resource type the service does not declare derives nothing", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AmazonSSM.AddTagsToResource",
				`{"ResourceType":"NotAThing","ResourceId":"x","Tags":[]}`),
			"ssm:AddTagsToResource", "*")
	})
	t.Run("an identifier already an ARN is taken as it stands", func(t *testing.T) {
		arn := ssm + "parameter/db/password"
		assertDerivedARNs(t,
			iamJSONRequest("AmazonSSM.RemoveTagsFromResource",
				`{"ResourceType":"Parameter","ResourceId":"`+arn+`","TagKeys":[]}`),
			"ssm:RemoveTagsFromResource", arn)
	})
}

// PutEvents names its event bus per entry, so a flat read finds none and the
// call authorized against "*" — which denies every policy written for a
// particular bus. AWS authorizes each entry against the bus it targets, the
// same way it authorizes each item of an Amazon DynamoDB transaction against
// its own table.
//
// The coverage probe sends a list member as a list of strings while Entries
// takes a list of objects, so these still measure as underived; the behaviour
// is what a real caller gets.
func TestIAMResourceARNs_EventBridgePutEventsNamesItsBuses(t *testing.T) {
	const events = "arn:aws:events:us-east-1:123456789012:event-bus/"
	t.Run("an entry names the bus it writes to", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSEvents.PutEvents",
				`{"Entries":[{"EventBusName":"orders","Detail":"{}"}]}`),
			"events:PutEvents", events+"orders")
	})
	t.Run("every bus a batch writes to is authorized, once each", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSEvents.PutEvents",
				`{"Entries":[{"EventBusName":"orders"},{"EventBusName":"audit"},{"EventBusName":"orders"}]}`),
			"events:PutEvents", events+"orders", events+"audit")
	})
	t.Run("an entry naming no bus writes to the default one", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSEvents.PutEvents", `{"Entries":[{"Detail":"{}"}]}`),
			"events:PutEvents", events+"default")
	})
	t.Run("a bus named by ARN is taken as it stands", func(t *testing.T) {
		bus := events + "orders"
		assertDerivedARNs(t,
			iamJSONRequest("AWSEvents.PutEvents",
				`{"Entries":[{"EventBusName":"`+bus+`"}]}`),
			"events:PutEvents", bus)
	})
	t.Run("a rule request is unaffected", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSEvents.DescribeRule", `{"Name":"nightly"}`),
			"events:DescribeRule", "arn:aws:events:us-east-1:123456789012:rule/nightly")
	})
}

func TestIAMResourceARNs_CodeBuild(t *testing.T) {
	const p = "arn:aws:codebuild:us-east-1:123456789012:"
	t.Run("StartBuild names its project", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("CodeBuild_20161006.StartBuild", `{"projectName":"edd-image-source"}`),
			"codebuild:StartBuild", p+"project/edd-image-source")
	})
	t.Run("a build id resolves to the project that owns it", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("CodeBuild_20161006.BatchGetBuilds",
				`{"ids":["edd-image-source:0e3a1f2c-1111-2222-3333-444455556666"]}`),
			"codebuild:BatchGetBuilds", p+"project/edd-image-source")
	})
	t.Run("ListProjects supports no resource-level permission", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("CodeBuild_20161006.ListProjects", `{}`),
			"codebuild:ListProjects", "*")
	})
}

func TestIAMResourceARNs_WAFv2(t *testing.T) {
	t.Run("GetIPSet names the ip set, not the web ACL", func(t *testing.T) {
		r := iamJSONRequest("AWSWAF_20190729.GetIPSet",
			`{"Name":"edd-allow","Scope":"REGIONAL","Id":"aaaa-bbbb-cccc"}`)
		assertDerivedARNs(t, r, "wafv2:GetIPSet",
			"arn:aws:wafv2:us-east-1:123456789012:regional/ipset/edd-allow/aaaa-bbbb-cccc")
	})
	t.Run("a CLOUDFRONT-scoped web ACL is global", func(t *testing.T) {
		r := iamJSONRequest("AWSWAF_20190729.GetWebACL",
			`{"Name":"edd-edge","Scope":"CLOUDFRONT","Id":"dddd-eeee-ffff"}`)
		assertDerivedARNs(t, r, "wafv2:GetWebACL",
			"arn:aws:wafv2:us-east-1:123456789012:global/webacl/edd-edge/dddd-eeee-ffff")
	})
}

func TestIAMResourceARNs_IAMIsGlobalAndCarriesNoRegion(t *testing.T) {
	assertDerivedARNs(t,
		iamQueryRequest("GetRole", "2010-05-08", map[string]string{"RoleName": "edd-control-plane"}),
		"iam:GetRole", "arn:aws:iam::123456789012:role/edd-control-plane")
	assertDerivedARNs(t,
		iamQueryRequest("GetUser", "2010-05-08", map[string]string{"UserName": "deployer"}),
		"iam:GetUser", "arn:aws:iam::123456789012:user/deployer")
}

// A role handed to a service is authorized separately, against the role's own
// ARN. Without it a PassRole statement scoped to specific roles means nothing,
// because nothing ever evaluates it.
func TestIAMPassedRoleARNsAreFoundWhereverTheRequestCarriesThem(t *testing.T) {
	cases := []struct {
		name, target, body string
		want               []string
	}{
		{
			"Amazon ECS task and execution roles, both at the top level",
			"AmazonEC2ContainerServiceV20141113.RegisterTaskDefinition",
			`{"family":"edd","taskRoleArn":"arn:aws:iam::123456789012:role/edd-task",
			  "executionRoleArn":"arn:aws:iam::123456789012:role/edd-exec"}`,
			[]string{"arn:aws:iam::123456789012:role/edd-exec", "arn:aws:iam::123456789012:role/edd-task"},
		},
		{
			"a role nested in an overrides object",
			"AmazonEC2ContainerServiceV20141113.RunTask",
			`{"taskDefinition":"edd:1","overrides":{"taskRoleArn":"arn:aws:iam::123456789012:role/edd-task"}}`,
			[]string{"arn:aws:iam::123456789012:role/edd-task"},
		},
		{
			"AWS CodeBuild names it serviceRole",
			"CodeBuild_20161006.CreateProject",
			`{"name":"edd","serviceRole":"arn:aws:iam::123456789012:role/edd-build"}`,
			[]string{"arn:aws:iam::123456789012:role/edd-build"},
		},
		{
			"a request that passes no role carries none",
			"CodeBuild_20161006.StartBuild", `{"projectName":"edd"}`,
			nil,
		},
		{
			"an ARN that is not a role is not a passed role",
			"CodeBuild_20161006.CreateProject",
			`{"name":"edd","encryptionKey":"arn:aws:kms:us-east-1:123456789012:key/abc"}`,
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := iamPassedRoleARNs(iamJSONRequest(tc.target, tc.body))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("passed roles = %v, want %v", got, tc.want)
			}
		})
	}
}

// The operations that require PassRole are AWS's list, and the ones that read
// rather than create pass nothing.
func TestIAMPassRoleOperationsCarryTheServicePrincipal(t *testing.T) {
	for _, tc := range []struct {
		action string
		want   string
	}{
		{"ecs:RegisterTaskDefinition", "ecs-tasks.amazonaws.com"},
		{"codebuild:CreateProject", "codebuild.amazonaws.com"},
	} {
		principals, ok := iamPassRoleOperations[tc.action]
		if !ok {
			t.Fatalf("%s should require iam:PassRole", tc.action)
		}
		found := false
		for _, p := range principals {
			if p == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s iam:PassedToService = %v, want it to include %q", tc.action, principals, tc.want)
		}
	}
	if _, ok := iamPassRoleOperations["ecs:DescribeServices"]; ok {
		t.Error("a read operation passes no role and must not require iam:PassRole")
	}
}

// ===== Amazon Elastic Compute Cloud =====

func iamEC2Request(action string, params map[string]string) *http.Request {
	form := "Action=" + action + "&Version=2016-11-15"
	for k, v := range params {
		form += "&" + k + "=" + v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/ec2/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// Amazon EC2's derivation is driven by the ARN format the Service Reference
// publishes for each of its 112 resource types, so the assertions here are
// about the published shapes rather than one hand-written path per type. The
// three that differ from the ordinary regional form are the ones a hand-written
// assembler gets wrong: an image and a snapshot carry no account, the IP
// Address Manager types carry no region, and a certificate or a role is another
// service's ARN entirely.
func TestIAMResourceARNs_EC2FollowsThePublishedARNFormat(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		params       map[string]string
		want         []string
	}{
		{
			name:   "the ordinary regional form",
			action: "DeleteVolume", params: map[string]string{"VolumeId": "vol-0abc"},
			want: []string{"arn:aws:ec2:us-east-1:123456789012:volume/vol-0abc"},
		},
		{
			name:   "an image carries no account",
			action: "DeregisterImage", params: map[string]string{"ImageId": "ami-0abc"},
			want: []string{"arn:aws:ec2:us-east-1::image/ami-0abc"},
		},
		{
			name:   "a snapshot carries no account",
			action: "DeleteSnapshot", params: map[string]string{"SnapshotId": "snap-0abc"},
			want: []string{"arn:aws:ec2:us-east-1::snapshot/snap-0abc"},
		},
		{
			name:   "an IP Address Manager pool carries no region",
			action: "DeleteIpamPool", params: map[string]string{"IpamPoolId": "ipam-pool-0abc"},
			want: []string{"arn:aws:ec2::123456789012:ipam-pool/ipam-pool-0abc"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamEC2Request(tc.action, tc.params), "ec2:"+tc.action, tc.want...)
		})
	}
}

// Amazon EC2 serializes a list by repeating the member's singular name with a
// 1-based index, and AWS authorizes every element. Terminating three instances
// under a policy naming only the first must not be allowed by deriving only
// the first.
func TestIAMResourceARNs_EC2AuthorizesEveryElementOfAList(t *testing.T) {
	const p = "arn:aws:ec2:us-east-1:123456789012:instance/"
	assertDerivedARNs(t,
		iamEC2Request("TerminateInstances", map[string]string{
			"InstanceId.1": "i-aaa", "InstanceId.2": "i-bbb", "InstanceId.3": "i-ccc",
		}),
		"ec2:TerminateInstances", p+"i-aaa", p+"i-bbb", p+"i-ccc")
}

// Where the request parameter is spelled differently from the ARN format's
// variable, both kinds of difference resolve: the mechanical prefix drop
// (${SecurityGroupId} arriving as GroupId) and the genuine renamings the
// reference did not follow.
func TestIAMResourceARNs_EC2ResolvesTheParameterRenamings(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		params       map[string]string
		want         string
	}{
		{
			"a security group's id arrives as GroupId", "DeleteSecurityGroup",
			map[string]string{"GroupId": "sg-0abc"},
			"arn:aws:ec2:us-east-1:123456789012:security-group/sg-0abc",
		},
		{
			"a dedicated host's id arrives as HostId", "ReleaseHosts",
			map[string]string{"HostId.1": "h-0abc"},
			"arn:aws:ec2:us-east-1:123456789012:dedicated-host/h-0abc",
		},
		{
			"an endpoint service is addressed as ServiceId", "DescribeVpcEndpointServicePermissions",
			map[string]string{"ServiceId": "vpce-svc-0abc"},
			"arn:aws:ec2:us-east-1:123456789012:vpc-endpoint-service/vpce-svc-0abc",
		},
		{
			"a network ACL's ${NaclId} arrives as NetworkAclId", "DeleteNetworkAcl",
			map[string]string{"NetworkAclId": "acl-0abc"},
			"arn:aws:ec2:us-east-1:123456789012:network-acl/acl-0abc",
		},
		{
			"a key pair is named by KeyName", "DeleteKeyPair",
			map[string]string{"KeyName": "deploy"},
			"arn:aws:ec2:us-east-1:123456789012:key-pair/deploy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamEC2Request(tc.action, tc.params), "ec2:"+tc.action, tc.want)
		})
	}
}

// A resource named by another service's ARN is that ARN, not an Amazon EC2 one
// wrapped around it.
func TestIAMResourceARNs_EC2PassesThroughAnotherServicesARN(t *testing.T) {
	const cert = "arn:aws:acm:us-east-1:123456789012:certificate/9a1e-4f2b"
	assertDerivedARNs(t,
		iamEC2Request("GetAssociatedEnclaveCertificateIamRoles", map[string]string{"CertificateArn": cert}),
		"ec2:GetAssociatedEnclaveCertificateIamRoles", cert)
}

// A filter names no resource. Reading the members of a nested structure would
// authorize against whatever a filter happened to be matching on, which is the
// caller's search, not the caller's target.
func TestIAMResourceARNs_EC2IgnoresNestedStructureMembers(t *testing.T) {
	assertDerivedARNs(t,
		iamEC2Request("DescribeVolumes", map[string]string{
			"Filter.1.Name": "status", "Filter.1.Value.1": "available",
		}),
		"ec2:DescribeVolumes", "*")
}

// An operation that creates its resource carries no identifier for it, so there
// is nothing to derive and the request falls back to "*". This is the honest
// answer rather than a guessed ARN — but it is also why a policy scoping
// CreateVpc to a resource is not yet evaluated against one.
func TestIAMResourceARNs_EC2CreateNamesNoResourceYet(t *testing.T) {
	assertDerivedARNs(t,
		iamEC2Request("CreateVpc", map[string]string{"CidrBlock": "10.0.0.0/16"}),
		"ec2:CreateVpc", "*")
}

// The whole point of deriving the resource: a policy scoped to one volume
// allows that volume and denies another, end to end — request, derived ARN,
// policy decision. Before the derivation existed both were denied, because a
// request authorized against a literal "*" matches only a policy whose Resource
// is itself "*".
func TestIAMEnforce_EC2VolumeScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow",
		"Action":["ec2:DeleteVolume","ec2:AttachVolume"],
		"Resource":"arn:aws:ec2:us-east-1:123456789012:volume/vol-granted"}]}`)
	for _, tc := range []struct{ name, volume, want string }{
		{"the granted volume", "vol-granted", "allowed"},
		{"any other volume", "vol-other", "implicitDeny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := iamEC2Request("DeleteVolume", map[string]string{"VolumeId": tc.volume})
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatal("request was not classified as an IAM action")
			}
			resources := iamResourceARNsForRequest(r, action)
			if len(resources) != 1 {
				t.Fatalf("derived %v, want exactly one volume ARN", resources)
			}
			got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resources[0], nil)
			if got != tc.want {
				t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.volume, resources[0], got, tc.want)
			}
		})
	}
}

// Two resource types can resolve to the same request parameter, and then the
// identifier belongs to one of them without the request saying which.
// AssociateRouteTable authorizes against an internet gateway and a virtual
// private gateway and takes a single GatewayId; building both would invent an
// ARN for a gateway that does not exist and then require it to be allowed,
// denying a policy that named the real one. Neither is derived — and the
// parameters that are unambiguous still are.
func TestIAMResourceARNs_EC2SkipsAnIdentifierTwoTypesBothClaim(t *testing.T) {
	assertDerivedARNs(t,
		iamEC2Request("AssociateRouteTable", map[string]string{
			"GatewayId": "igw-0abc", "RouteTableId": "rtb-0abc", "SubnetId": "subnet-0abc",
		}),
		"ec2:AssociateRouteTable",
		"arn:aws:ec2:us-east-1:123456789012:route-table/rtb-0abc",
		"arn:aws:ec2:us-east-1:123456789012:subnet/subnet-0abc")

	// CancelImportTask has only the ambiguous one — an ImportTaskId that is
	// either an image-import or a snapshot-import task — so it derives nothing.
	assertDerivedARNs(t,
		iamEC2Request("CancelImportTask", map[string]string{"ImportTaskId": "import-i-0abc"}),
		"ec2:CancelImportTask", "*")
}

// An action authorizing against several resources requires every one of them,
// which is how AWS evaluates it: attaching a volume to an instance is allowed
// only by a policy covering both.
func TestIAMResourceARNs_EC2DerivesEveryResourceAnActionNames(t *testing.T) {
	assertDerivedARNs(t,
		iamEC2Request("AttachVolume", map[string]string{
			"InstanceId": "i-0abc", "VolumeId": "vol-0abc", "Device": "/dev/sdf",
		}),
		"ec2:AttachVolume",
		"arn:aws:ec2:us-east-1:123456789012:instance/i-0abc",
		"arn:aws:ec2:us-east-1:123456789012:volume/vol-0abc")
}

// ===== AWS Glue =====

func iamGlueRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AWSGlue."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/glue/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// AWS Glue nests its ARNs, and GetTable is where every part of the derivation
// shows at once: the database it names, the table underneath it (whose ARN
// carries the database), the catalog the request addresses by an identifier the
// reference calls a name, and the root catalog, whose format names nothing
// because a request authorizes against it by existing.
func TestIAMResourceARNs_GlueNestsAnARNUnderItsParents(t *testing.T) {
	const p = "arn:aws:glue:us-east-1:123456789012:"
	assertDerivedARNs(t,
		iamGlueRequest("GetTable", `{"CatalogId":"123456789012","DatabaseName":"sales","Name":"orders"}`),
		"glue:GetTable",
		p+"catalog/123456789012",
		p+"database/sales",
		p+"table/sales/orders",
		p+"catalog")
}

// A batch operation names its resources in a list and every one is authorized,
// under the plural of whichever spelling the singular uses.
func TestIAMResourceARNs_GlueAuthorizesEveryJobOfABatch(t *testing.T) {
	const p = "arn:aws:glue:us-east-1:123456789012:job/"
	assertDerivedARNs(t,
		iamGlueRequest("BatchGetJobs", `{"JobNames":["ingest","transform","publish"]}`),
		"glue:BatchGetJobs", p+"ingest", p+"transform", p+"publish")
}

// Most of AWS Glue abbreviates the identifier the reference publishes: a
// crawler's ${CrawlerName} arrives as Name, which the prefix drop resolves
// without a per-resource case.
func TestIAMResourceARNs_GlueResolvesTheAbbreviatedIdentifier(t *testing.T) {
	assertDerivedARNs(t,
		iamGlueRequest("GetCrawler", `{"Name":"nightly"}`),
		"glue:GetCrawler", "arn:aws:glue:us-east-1:123456789012:crawler/nightly")
}

// A user-defined function's ARN carries its database and its own name, and the
// name arrives under a spelling the prefix drop does not reach — the one case
// AWS Glue needs an alias for.
func TestIAMResourceARNs_GlueUserDefinedFunctionCarriesItsDatabase(t *testing.T) {
	const p = "arn:aws:glue:us-east-1:123456789012:"
	assertDerivedARNs(t,
		iamGlueRequest("GetUserDefinedFunction",
			`{"CatalogId":"123456789012","DatabaseName":"sales","FunctionName":"to_cents"}`),
		"glue:GetUserDefinedFunction",
		p+"catalog/123456789012",
		p+"database/sales",
		p+"userDefinedFunction/sales/to_cents",
		p+"catalog")
}

// A request that names no part of a nested ARN derives nothing for it rather
// than a half-filled one: an ARN carrying a literal ${DatabaseName} matches no
// policy, and emitting it would deny a grant that named the real table.
// GetUserDefinedFunction names the function but not the database its ARN needs.
// The function is the only thing any type here claims, so nothing else can
// account for the absence: either the parent is required or the identifier
// slides into the database's place and the ARN names a function that does not
// exist, under a database that does not either.
func TestIAMResourceARNs_GlueNeverEmitsAHalfFilledARN(t *testing.T) {
	r := iamGlueRequest("GetUserDefinedFunction", `{"FunctionName":"to_cents"}`)
	for _, a := range iamResourceARNsForRequest(r, "glue:GetUserDefinedFunction") {
		if strings.Contains(a, "${") {
			t.Fatalf("derived %q, which carries an unfilled variable", a)
		}
		if strings.Contains(a, "userDefinedFunction/") {
			t.Fatalf("derived %q for a request that never named the database its ARN nests under", a)
		}
	}
}

// The whole point, end to end: a policy scoped to one AWS Glue job allows that
// job and denies another.
func TestIAMEnforce_GlueJobScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow",
		"Action":["glue:GetJob","glue:StartJobRun"],
		"Resource":"arn:aws:glue:us-east-1:123456789012:job/ingest"}]}`)
	for _, tc := range []struct{ name, job, want string }{
		{"the granted job", "ingest", "allowed"},
		{"any other job", "exfiltrate", "implicitDeny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := iamGlueRequest("GetJob", `{"JobName":"`+tc.job+`"}`)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatal("request was not classified as an IAM action")
			}
			resources := iamResourceARNsForRequest(r, action)
			if len(resources) != 1 {
				t.Fatalf("derived %v, want exactly one job ARN", resources)
			}
			got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resources[0], nil)
			if got != tc.want {
				t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.job, resources[0], got, tc.want)
			}
		})
	}
}

// ===== Amazon Relational Database Service =====

func iamRDSRequest(action string, params map[string]string) *http.Request {
	form := "Action=" + action + "&Version=2014-10-31"
	for k, v := range params {
		form += "&" + k + "=" + v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/rds/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// Amazon RDS is where the reference and the API disagree about spelling on
// almost every resource, so each of these is an alias doing the work: the
// reference publishes ${DbInstanceName}, ${ClusterParameterGroupName} and
// ${SnapshotName} where the API sends DBInstanceIdentifier,
// DBClusterParameterGroupName and DBSnapshotIdentifier.
func TestIAMResourceARNs_RDSResolvesTheAPIsOwnSpelling(t *testing.T) {
	const p = "arn:aws:rds:us-east-1:123456789012:"
	for _, tc := range []struct {
		name, action string
		params       map[string]string
		want         string
	}{
		{"a database instance", "DeleteDBInstance",
			map[string]string{"DBInstanceIdentifier": "orders-1"}, p + "db:orders-1"},
		{"a cluster", "DeleteDBCluster",
			map[string]string{"DBClusterIdentifier": "orders"}, p + "cluster:orders"},
		{"a snapshot", "DeleteDBSnapshot",
			map[string]string{"DBSnapshotIdentifier": "orders-nightly"}, p + "snapshot:orders-nightly"},
		{"a cluster parameter group", "DeleteDBClusterParameterGroup",
			map[string]string{"DBClusterParameterGroupName": "pg16"}, p + "cluster-pg:pg16"},
		{"a subnet group", "DeleteDBSubnetGroup",
			map[string]string{"DBSubnetGroupName": "private"}, p + "subgrp:private"},
		{"a proxy", "DeleteDBProxy",
			map[string]string{"DBProxyName": "orders-proxy"}, p + "db-proxy:orders-proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamRDSRequest(tc.action, tc.params), "rds:"+tc.action, tc.want)
		})
	}
}

// A global cluster's ARN carries no region, exactly as the reference publishes
// it, which is the sort of thing filling the published format gets right and
// assembling a path by hand does not.
func TestIAMResourceARNs_RDSGlobalClusterCarriesNoRegion(t *testing.T) {
	assertDerivedARNs(t,
		iamRDSRequest("DeleteGlobalCluster", map[string]string{"GlobalClusterIdentifier": "worldwide"}),
		"rds:DeleteGlobalCluster", "arn:aws:rds::123456789012:global-cluster:worldwide")
}

// The tagging, activity-stream and maintenance operations name their resource
// by ARN rather than by identifier, under three different parameters. The ARN
// needs no assembly, and reading it is what lets a tag call be authorized
// against the database it tags rather than against "*".
func TestIAMResourceARNs_RDSTakesTheARNTheRequestNames(t *testing.T) {
	const db = "arn:aws:rds:us-east-1:123456789012:db:orders-1"
	for _, tc := range []struct{ name, action, param string }{
		{"tagging sends ResourceName", "AddTagsToResource", "ResourceName"},
		{"listing tags sends ResourceName", "ListTagsForResource", "ResourceName"},
		{"an activity stream sends ResourceArn", "StartActivityStream", "ResourceArn"},
		{"a maintenance action sends ResourceIdentifier", "ApplyPendingMaintenanceAction", "ResourceIdentifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamRDSRequest(tc.action, map[string]string{tc.param: db}),
				"rds:"+tc.action, db)
		})
	}
}

// Tagging carries identifiers of mixed types under one member, and each id
// authorizes against its own resource: the type is stated by the id's
// prefix, longest match first, and an unknown prefix derives nothing rather
// than guessing.
func TestIAMResourceARNs_EC2TagsDeriveEachIdFromItsPrefix(t *testing.T) {
	r := iamEC2Request("CreateTags", map[string]string{
		"ResourceId.1": "i-0123456789abcdef0",
		"ResourceId.2": "vol-0123456789abcdef0",
		"ResourceId.3": "tgw-attach-0123456789abcdef0",
	})
	assertDerivedARNs(t, r, "ec2:CreateTags",
		"arn:aws:ec2:us-east-1:123456789012:instance/i-0123456789abcdef0",
		"arn:aws:ec2:us-east-1:123456789012:transit-gateway-attachment/tgw-attach-0123456789abcdef0",
		"arn:aws:ec2:us-east-1:123456789012:volume/vol-0123456789abcdef0")
}

// The Disassociate/Detach family names an association, and the resource the
// reference authorizes is the parent it belongs to — resolved through the
// simulator's own state, so the derived ARN is the one the resource actually
// has.
func TestIAMResourceARNs_EC2ResolvesAssociationsToTheirParents(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2RouteTables = sim.MakeStore[EC2RouteTable](nil, "ec2_route_tables")
	ec2ElasticIPs = sim.MakeStore[EC2ElasticIP](nil, "ec2_elastic_ips")
	ec2NetworkInterfaces = sim.MakeStore[EC2NetworkInterface](nil, "ec2_network_interfaces")

	ec2RouteTables.Put("rtb-0a1b2c3d4e5f60718", EC2RouteTable{
		RouteTableId: "rtb-0a1b2c3d4e5f60718",
		Associations: []EC2RouteTableAssociation{{
			AssociationId: "rtbassoc-0f9e8d7c6b5a41320",
			RouteTableId:  "rtb-0a1b2c3d4e5f60718",
			SubnetId:      "subnet-00112233445566778",
		}},
	})
	ec2ElasticIPs.Put("eipalloc-0aabbccddeeff0011", EC2ElasticIP{
		AllocationId:  "eipalloc-0aabbccddeeff0011",
		AssociationId: "eipassoc-09876543210fedcba",
	})
	ec2NetworkInterfaces.Put("eni-0123456789abcdef0", EC2NetworkInterface{
		NetworkInterfaceId: "eni-0123456789abcdef0",
		AttachmentId:       "eni-attach-0123456789abcdef0",
	})

	t.Run("a route table association resolves to its table", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEC2Request("DisassociateRouteTable", map[string]string{"AssociationId": "rtbassoc-0f9e8d7c6b5a41320"}),
			"ec2:DisassociateRouteTable",
			"arn:aws:ec2:us-east-1:123456789012:route-table/rtb-0a1b2c3d4e5f60718")
	})
	t.Run("an address association resolves to its allocation", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEC2Request("DisassociateAddress", map[string]string{"AssociationId": "eipassoc-09876543210fedcba"}),
			"ec2:DisassociateAddress",
			"arn:aws:ec2:us-east-1:123456789012:elastic-ip/eipalloc-0aabbccddeeff0011")
	})
	t.Run("an attachment resolves to its network interface", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEC2Request("DetachNetworkInterface", map[string]string{"AttachmentId": "eni-attach-0123456789abcdef0"}),
			"ec2:DetachNetworkInterface",
			"arn:aws:ec2:us-east-1:123456789012:network-interface/eni-0123456789abcdef0")
	})
}

// The AWS Glue tagging operations name their target by ResourceArn outright;
// the ARN the caller sent is the one authorized, with nothing assembled.
func TestIAMResourceARNs_GlueTaggingTakesTheARNTheRequestNames(t *testing.T) {
	const job = "arn:aws:glue:us-east-1:123456789012:job/nightly-etl"
	assertDerivedARNs(t,
		iamGlueRequest("TagResource", `{"ResourceArn":"`+job+`","TagsToAdd":{"team":"data"}}`),
		"glue:TagResource", job)
}

// AWS Budgets is global — its ARNs carry no region — and a budget action is
// named by the budget it belongs to plus the ActionId the request carries.
func TestIAMResourceARNs_BudgetsAssemblesTheGlobalActionARN(t *testing.T) {
	r := iamJSONRequest("AWSBudgetServiceGateway.DescribeBudgetAction",
		`{"AccountId":"123456789012","BudgetName":"team-spend","ActionId":"3f1e9d2c-7b64-4a10-9e5f-8c2d1a0b4e77"}`)
	assertDerivedARNs(t, r, "budgets:DescribeBudgetAction",
		"arn:aws:budgets::123456789012:budget/team-spend/action/3f1e9d2c-7b64-4a10-9e5f-8c2d1a0b4e77")
}

// A copy authorizes both of its ends: the target's ARN is fully determined by
// the name the request supplies before the snapshot exists, and a source
// named by ARN — the cross-region form — is authorized as sent rather than
// reassembled.
func TestIAMResourceARNs_RDSCopyAuthorizesSourceAndTarget(t *testing.T) {
	t.Run("both ends by name", func(t *testing.T) {
		assertDerivedARNs(t, iamRDSRequest("CopyDBSnapshot", map[string]string{
			"SourceDBSnapshotIdentifier": "nightly",
			"TargetDBSnapshotIdentifier": "nightly-copy",
		}), "rds:CopyDBSnapshot",
			"arn:aws:rds:us-east-1:123456789012:snapshot:nightly",
			"arn:aws:rds:us-east-1:123456789012:snapshot:nightly-copy")
	})
	t.Run("an ARN source is taken as sent", func(t *testing.T) {
		const source = "arn:aws:rds:eu-west-1:123456789012:snapshot:nightly"
		assertDerivedARNs(t, iamRDSRequest("CopyDBSnapshot", map[string]string{
			"SourceDBSnapshotIdentifier": source,
			"TargetDBSnapshotIdentifier": "nightly-copy",
		}), "rds:CopyDBSnapshot",
			source,
			"arn:aws:rds:us-east-1:123456789012:snapshot:nightly-copy")
	})
}

// The whole point, end to end: a policy scoped to one database instance allows
// that instance and denies another.
func TestIAMEnforce_RDSInstanceScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow",
		"Action":["rds:DeleteDBInstance","rds:ModifyDBInstance"],
		"Resource":"arn:aws:rds:us-east-1:123456789012:db:orders-1"}]}`)
	for _, tc := range []struct{ name, instance, want string }{
		{"the granted instance", "orders-1", "allowed"},
		{"any other instance", "billing-1", "implicitDeny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := iamRDSRequest("DeleteDBInstance", map[string]string{"DBInstanceIdentifier": tc.instance})
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatal("request was not classified as an IAM action")
			}
			resources := iamResourceARNsForRequest(r, action)
			if len(resources) != 1 {
				t.Fatalf("derived %v, want exactly one instance ARN", resources)
			}
			got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resources[0], nil)
			if got != tc.want {
				t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.instance, resources[0], got, tc.want)
			}
		})
	}
}

// ===== AWS Systems Manager =====

func iamSSMRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/ssm/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// A parameter is named "/db/password" in every request and its ARN is
// "…:parameter/db/password". Keeping the slash would build an ARN with an empty
// first path segment, which is what the variable AWS publishes it under —
// ${ParameterNameWithoutLeadingSlash} — is telling the reader.
func TestIAMResourceARNs_SSMParameterLosesItsLeadingSlash(t *testing.T) {
	assertDerivedARNs(t,
		iamSSMRequest("GetParameter", `{"Name":"/db/password"}`),
		"ssm:GetParameter", "arn:aws:ssm:us-east-1:123456789012:parameter/db/password")
}

// A parameter without a leading slash is a top-level parameter and keeps its
// name exactly.
func TestIAMResourceARNs_SSMTopLevelParameterKeepsItsName(t *testing.T) {
	assertDerivedARNs(t,
		iamSSMRequest("GetParameter", `{"Name":"region"}`),
		"ssm:GetParameter", "arn:aws:ssm:us-east-1:123456789012:parameter/region")
}

// Four AWS Systems Manager resource types are published under one variable
// name, ${ResourceId}, and the request names each of them differently. Keying
// their aliases by resource type is what stops them resolving to one another's
// member and cancelling out as an ambiguity.
func TestIAMResourceARNs_SSMTellsItsResourceIdTypesApart(t *testing.T) {
	const p = "arn:aws:ssm:us-east-1:123456789012:"
	for _, tc := range []struct{ name, operation, body, want string }{
		{"a maintenance window", "GetMaintenanceWindow", `{"WindowId":"mw-0abc"}`, p + "maintenancewindow/mw-0abc"},
		{"an OpsItem", "GetOpsItem", `{"OpsItemId":"oi-0abc"}`, p + "opsitem/oi-0abc"},
		{"a service setting", "GetServiceSetting", `{"SettingId":"/ssm/parameter-store/high-throughput-enabled"}`,
			p + "servicesetting//ssm/parameter-store/high-throughput-enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamSSMRequest(tc.operation, tc.body), "ssm:"+tc.operation, tc.want)
		})
	}
}

// A document's ${DocumentName} arrives as Name, which the prefix drop resolves
// without a per-resource case.
func TestIAMResourceARNs_SSMDocumentIsNamedByName(t *testing.T) {
	assertDerivedARNs(t,
		iamSSMRequest("DeleteDocument", `{"Name":"AWS-RunShellScript"}`),
		"ssm:DeleteDocument", "arn:aws:ssm:us-east-1:123456789012:document/AWS-RunShellScript")
}

// The whole point, end to end: a policy scoped to one parameter prefix allows
// the parameters under it and denies the rest.
func TestIAMEnforce_SSMParameterScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow",
		"Action":["ssm:GetParameter","ssm:GetParameters"],
		"Resource":"arn:aws:ssm:us-east-1:123456789012:parameter/app/*"}]}`)
	for _, tc := range []struct{ name, parameter, want string }{
		{"a parameter under the granted prefix", "/app/db-password", "allowed"},
		{"a nested parameter under the granted prefix", "/app/tier/secret", "allowed"},
		{"a parameter outside the prefix", "/other/db-password", "implicitDeny"},
		{"the prefix is not a bare substring match", "/app-staging/db-password", "implicitDeny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := iamSSMRequest("GetParameter", `{"Name":"`+tc.parameter+`"}`)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatal("request was not classified as an IAM action")
			}
			resources := iamResourceARNsForRequest(r, action)
			if len(resources) != 1 {
				t.Fatalf("derived %v, want exactly one parameter ARN", resources)
			}
			got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resources[0], nil)
			if got != tc.want {
				t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.parameter, resources[0], got, tc.want)
			}
		})
	}
}

// Two Amazon RDS ARNs carry an identifier no request names: a custom engine
// version's own id, published as the third part of "cev:<engine>/<version>/<id>",
// and a proxy target group's, which is the whole of "target-group:<id>". A
// caller addresses the first by engine and version and the second by proxy and
// group name, so the gate resolves both through the simulator's own state.
//
// What both assert is the same thing, and it is the only thing that matters:
// the ARN the gate requests is the ARN the resource actually has. An ARN that
// is merely well-shaped would still deny every policy written against the real
// one.
func TestIAMResourceARNs_RDSResolvesTheIdentifiersNoRequestNames(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	rdsCustomEngineVersions = sim.MakeStore[RDSCustomEngineVersion](nil, "rds_custom_engine_versions")
	rdsProxyTargetGroups = sim.MakeStore[RDSProxyTargetGroup](nil, "rds_proxy_target_groups")

	const engine, version = "custom-oracle-ee", "19.cdb_cev1"
	cevARN := rdsCustomEngineVersionARN(engine, version, "9a1e4f2b-0c37-4d55-8b21-6e0f7a2c9d84")
	rdsCustomEngineVersions.Put(rdsCEVKey(engine, version), RDSCustomEngineVersion{
		Engine: engine, EngineVersion: version,
		CustomDBEngineVersionId: "9a1e4f2b-0c37-4d55-8b21-6e0f7a2c9d84", ARN: cevARN,
	})
	groupARN := rdsTargetGroupARN("prx-tg-0a1b2c3d4e5f60718")
	rdsProxyTargetGroups.Put("orders-proxy/default", RDSProxyTargetGroup{
		TargetGroupId: "prx-tg-0a1b2c3d4e5f60718", TargetGroupName: "default",
		DBProxyName: "orders-proxy", ARN: groupARN,
	})

	t.Run("a custom engine version", func(t *testing.T) {
		assertDerivedARNs(t,
			iamRDSRequest("DeleteCustomDBEngineVersion",
				map[string]string{"Engine": engine, "EngineVersion": version}),
			"rds:DeleteCustomDBEngineVersion", cevARN)
	})
	t.Run("a proxy target group", func(t *testing.T) {
		got := iamResourceARNsForRequest(
			iamRDSRequest("ModifyDBProxyTargetGroup",
				map[string]string{"DBProxyName": "orders-proxy", "TargetGroupName": "default"}),
			"rds:ModifyDBProxyTargetGroup")
		found := false
		for _, a := range got {
			if a == groupARN {
				found = true
			}
		}
		if !found {
			t.Fatalf("derived %v, none of which is the target group's own ARN %s", got, groupARN)
		}
	})
}

// The published shapes, pinned. A custom engine version's ARN carries three
// parts and a proxy target group's carries one; the simulator built two and two
// before, so a policy written against either real resource matched nothing.
func TestIAMResourceARNs_RDSARNsTakeTheirPublishedShape(t *testing.T) {
	const cev = "arn:aws:rds:us-east-1:123456789012:cev:custom-oracle-ee/19.cdb_cev1/9a1e-4f2b"
	if got := rdsCustomEngineVersionARN("custom-oracle-ee", "19.cdb_cev1", "9a1e-4f2b"); got != cev {
		t.Errorf("custom engine version ARN = %q, want %q", got, cev)
	}
	const group = "arn:aws:rds:us-east-1:123456789012:target-group:prx-tg-0a1b2c3d4e5f60718"
	if got := rdsTargetGroupARN("prx-tg-0a1b2c3d4e5f60718"); got != group {
		t.Errorf("proxy target group ARN = %q, want %q", got, group)
	}
	if id := rdsTargetGroupID(); !strings.HasPrefix(id, "prx-tg-") || len(id) != len("prx-tg-")+17 {
		t.Errorf("minted target group id %q does not take the shape AWS assigns", id)
	}
}

// ===== Amazon ElastiCache and Amazon DynamoDB =====

func iamElastiCacheRequest(action string, params map[string]string) *http.Request {
	form := "Action=" + action + "&Version=2015-02-02"
	for k, v := range params {
		form += "&" + k + "=" + v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/elasticache/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// Amazon ElastiCache is the service that needed no renamings: every one of its
// twelve resource types is published under the parameter the API sends, so
// these pass through the published format with nothing hand-written between
// the request and the ARN.
func TestIAMResourceARNs_ElastiCacheNeedsNoRenamings(t *testing.T) {
	const p = "arn:aws:elasticache:us-east-1:123456789012:"
	for _, tc := range []struct {
		name, action string
		params       map[string]string
		want         string
	}{
		{"a cache cluster", "DeleteCacheCluster",
			map[string]string{"CacheClusterId": "orders"}, p + "cluster:orders"},
		{"a replication group", "DeleteReplicationGroup",
			map[string]string{"ReplicationGroupId": "orders-rg"}, p + "replicationgroup:orders-rg"},
		{"a parameter group", "DeleteCacheParameterGroup",
			map[string]string{"CacheParameterGroupName": "redis7"}, p + "parametergroup:redis7"},
		{"a subnet group", "DeleteCacheSubnetGroup",
			map[string]string{"CacheSubnetGroupName": "private"}, p + "subnetgroup:private"},
		{"a user", "DeleteUser",
			map[string]string{"UserId": "app-reader"}, p + "user:app-reader"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamElastiCacheRequest(tc.action, tc.params), "elasticache:"+tc.action, tc.want)
		})
	}
}

func iamDynamoDBRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.0")
	r.Header.Set("X-Amz-Target", "DynamoDB_20120810."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/dynamodb/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// An Amazon DynamoDB index nests under its table, which the published format
// says and the request supplies both halves of. A table stands alone. Both are
// requested for a query against an index, because AWS authorizes it against
// both, and they arrive in the order the reference declares its types.
func TestIAMResourceARNs_DynamoDBIndexNestsUnderItsTable(t *testing.T) {
	const p = "arn:aws:dynamodb:us-east-1:123456789012:"
	assertDerivedARNs(t,
		iamDynamoDBRequest("Query", `{"TableName":"orders","IndexName":"by-customer"}`),
		"dynamodb:Query", p+"table/orders/index/by-customer", p+"table/orders")
	assertDerivedARNs(t,
		iamDynamoDBRequest("PutItem", `{"TableName":"orders"}`),
		"dynamodb:PutItem", p+"table/orders")
}

// A backup, an export and an import are named by their own ARN rather than by
// a table and a name, so the ARN is taken as it stands. The export and import
// family names the table itself that way too — TableArn where every other
// table operation sends TableName — a shape the coverage probe cannot express,
// since it fills every member with one placeholder, so it is pinned here.
func TestIAMResourceARNs_DynamoDBTakesTheARNTheRequestNames(t *testing.T) {
	const backup = "arn:aws:dynamodb:us-east-1:123456789012:table/orders/backup/01700000000000-a1b2c3d4"
	assertDerivedARNs(t,
		iamDynamoDBRequest("DeleteBackup", `{"BackupArn":"`+backup+`"}`),
		"dynamodb:DeleteBackup", backup)

	const table = "arn:aws:dynamodb:us-east-1:123456789012:table/orders"
	assertDerivedARNs(t,
		iamDynamoDBRequest("ExportTableToPointInTime", `{"TableArn":"`+table+`","S3Bucket":"exports"}`),
		"dynamodb:ExportTableToPointInTime", table)
	assertDerivedARNs(t,
		iamDynamoDBRequest("ListExports", `{"TableArn":"`+table+`"}`),
		"dynamodb:ListExports", table)
	assertDerivedARNs(t,
		iamDynamoDBRequest("ListImports", `{"TableArn":"`+table+`"}`),
		"dynamodb:ListImports", table)
}

// A transaction names its tables per item, and the AWS Service Reference lists
// neither TransactWriteItems nor TransactGetItems — it declares no resource
// type for either — so nothing table-driven can reach them. Every table a
// transaction touches is still authorized, because deriving none would deny a
// single-table transaction its own table.
func TestIAMResourceARNs_DynamoDBTransactionNamesEveryTableItTouches(t *testing.T) {
	const p = "arn:aws:dynamodb:us-east-1:123456789012:table/"
	if _, declared := iamActionResourceTypes["dynamodb:TransactWriteItems"]; declared {
		t.Fatal("the reference now declares a type for TransactWriteItems — the derivation can move to the table-driven path")
	}
	assertDerivedARNs(t,
		iamDynamoDBRequest("TransactWriteItems", `{"TransactItems":[
			{"Put":{"TableName":"orders","Item":{}}},
			{"Update":{"TableName":"customers","Key":{}}}]}`),
		"dynamodb:TransactWriteItems", p+"customers", p+"orders")
}

// ===== AWS CloudTrail and Amazon EventBridge =====

func iamCloudTrailRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "CloudTrail_20131101."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/cloudtrail/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// AWS CloudTrail publishes three of its four identifiers under names the API
// does not use. A trail is the exception, and the prefix drop resolves it.
func TestIAMResourceARNs_CloudTrailResolvesItsRenamings(t *testing.T) {
	const p = "arn:aws:cloudtrail:us-east-1:123456789012:"
	for _, tc := range []struct{ name, operation, body, want string }{
		{"a trail arrives as Name", "DeleteTrail", `{"Name":"org-audit"}`, p + "trail/org-audit"},
		{"a channel is addressed as Channel", "DeleteChannel", `{"Channel":"ch-0abc"}`, p + "channel/ch-0abc"},
		{"an event data store as EventDataStore", "DeleteEventDataStore", `{"EventDataStore":"eds-0abc"}`, p + "eventdatastore/eds-0abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDerivedARNs(t, iamCloudTrailRequest(tc.operation, tc.body), "cloudtrail:"+tc.operation, tc.want)
		})
	}
}

func iamEventBridgeRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AWSEvents."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/events/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// Amazon EventBridge publishes a rule twice — once on the default event bus and
// once on a custom one — and both end in the same variable, so both would claim
// the same request member and the ambiguity rule would discard the pair. Which
// applies is decidable from the request rather than from spelling: naming a
// custom bus means the nested ARN, naming none or naming "default" means the
// flat one. DeleteRule authorizes against the rule alone, so the bus it names
// steers the ARN's shape without becoming a resource of its own.
func TestIAMResourceARNs_EventBridgeTellsApartTheTwoRuleARNs(t *testing.T) {
	const p = "arn:aws:events:us-east-1:123456789012:"
	t.Run("no event bus named is the default bus", func(t *testing.T) {
		assertDerivedARNs(t, iamEventBridgeRequest("DeleteRule", `{"Name":"nightly"}`),
			"events:DeleteRule", p+"rule/nightly")
	})
	t.Run("the default bus named explicitly is still the flat ARN", func(t *testing.T) {
		assertDerivedARNs(t, iamEventBridgeRequest("DeleteRule", `{"Name":"nightly","EventBusName":"default"}`),
			"events:DeleteRule", p+"rule/nightly")
	})
	t.Run("a custom bus nests the rule under it", func(t *testing.T) {
		assertDerivedARNs(t, iamEventBridgeRequest("DeleteRule", `{"Name":"nightly","EventBusName":"orders"}`),
			"events:DeleteRule", p+"rule/orders/nightly")
	})
}

// A connection's ${ConnectionName} arrives as Name, which the prefix drop
// resolves without a per-resource case.
func TestIAMResourceARNs_EventBridgeResolvesTheAbbreviatedIdentifier(t *testing.T) {
	assertDerivedARNs(t,
		iamEventBridgeRequest("DeleteConnection", `{"Name":"github"}`),
		"events:DeleteConnection", "arn:aws:events:us-east-1:123456789012:connection/github")
}

// An event bus, an event source and an API destination are all addressed
// simply as Name — a spelling the prefix drop cannot reach, so each is a
// declared alias scoped to its resource type. An event source's ARN carries no
// account, which filling the published format preserves.
func TestIAMResourceARNs_EventBridgeResolvesTheNameEachTypeAbbreviatesTo(t *testing.T) {
	const p = "arn:aws:events:us-east-1:123456789012:"
	t.Run("an event bus is created and described by Name", func(t *testing.T) {
		assertDerivedARNs(t, iamEventBridgeRequest("CreateEventBus", `{"Name":"orders"}`),
			"events:CreateEventBus", p+"event-bus/orders")
		assertDerivedARNs(t, iamEventBridgeRequest("DescribeEventBus", `{"Name":"orders"}`),
			"events:DescribeEventBus", p+"event-bus/orders")
	})
	t.Run("a partner event source's ARN carries no account", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEventBridgeRequest("ActivateEventSource", `{"Name":"aws.partner/example.com/orders"}`),
			"events:ActivateEventSource",
			"arn:aws:events:us-east-1::event-source/aws.partner/example.com/orders")
		assertDerivedARNs(t,
			iamEventBridgeRequest("DescribePartnerEventSource", `{"Name":"aws.partner/example.com/orders"}`),
			"events:DescribePartnerEventSource",
			"arn:aws:events:us-east-1::event-source/aws.partner/example.com/orders")
	})
	t.Run("an API destination is deleted and updated by Name", func(t *testing.T) {
		assertDerivedARNs(t, iamEventBridgeRequest("DeleteApiDestination", `{"Name":"webhook"}`),
			"events:DeleteApiDestination", p+"api-destination/webhook")
		assertDerivedARNs(t, iamEventBridgeRequest("UpdateApiDestination", `{"Name":"webhook"}`),
			"events:UpdateApiDestination", p+"api-destination/webhook")
	})
}

// CreateApiDestination authorizes against both the destination it creates and
// the connection it references; the destination arrives as Name and the
// connection by its own ARN, so both derive without contesting one member.
// DescribeApiDestination declares both types too but names only the
// destination, and the declared alias outranks the prefix drop that would have
// read Name as the connection's — the call derives the resource it names and
// invents nothing for the one it does not.
func TestIAMResourceARNs_EventBridgeApiDestinationNamesItsConnectionByARN(t *testing.T) {
	const p = "arn:aws:events:us-east-1:123456789012:"
	const connection = p + "connection/github/1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"
	assertDerivedARNs(t,
		iamEventBridgeRequest("CreateApiDestination",
			`{"Name":"webhook","ConnectionArn":"`+connection+`","InvocationEndpoint":"https://example.com","HttpMethod":"POST"}`),
		"events:CreateApiDestination", p+"api-destination/webhook", connection)
	assertDerivedARNs(t,
		iamEventBridgeRequest("DescribeApiDestination", `{"Name":"webhook"}`),
		"events:DescribeApiDestination", p+"api-destination/webhook")
}

// The rule-target operations address the rule as Rule rather than Name, and
// the bus the request names steers which of the two published rule ARNs the
// derivation fills, exactly as it does for the operations that send Name.
func TestIAMResourceARNs_EventBridgeRuleTargetsAddressTheRuleAsRule(t *testing.T) {
	const p = "arn:aws:events:us-east-1:123456789012:"
	t.Run("no bus named is the flat default-bus ARN", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEventBridgeRequest("PutTargets", `{"Rule":"nightly","Targets":[]}`),
			"events:PutTargets", p+"rule/nightly")
		assertDerivedARNs(t,
			iamEventBridgeRequest("ListTargetsByRule", `{"Rule":"nightly"}`),
			"events:ListTargetsByRule", p+"rule/nightly")
	})
	t.Run("a custom bus nests the rule under it", func(t *testing.T) {
		assertDerivedARNs(t,
			iamEventBridgeRequest("RemoveTargets", `{"Rule":"nightly","EventBusName":"orders","Ids":["t1"]}`),
			"events:RemoveTargets", p+"rule/orders/nightly")
	})
}

// AWS Glue groups an identifier into a structure of its own rather than naming
// it at the top level: a registry is addressed as RegistryId{RegistryName} and
// a schema as SchemaId{SchemaName}. The member inside is the resource's name
// however it is wrapped, so the derivation reads one level down.
func TestIAMResourceARNs_GlueReadsANestedIdentifier(t *testing.T) {
	const p = "arn:aws:glue:us-east-1:123456789012:"
	t.Run("a registry names itself inside RegistryId", func(t *testing.T) {
		got := iamResourceARNsForRequest(
			iamGlueRequest("DeleteRegistry", `{"RegistryId":{"RegistryName":"events"}}`),
			"glue:DeleteRegistry")
		if !slicesContain(got, p+"registry/events") {
			t.Fatalf("derived %v, want it to include the registry named inside RegistryId", got)
		}
	})
	t.Run("a schema names itself inside SchemaId", func(t *testing.T) {
		got := iamResourceARNsForRequest(
			iamGlueRequest("DeleteSchema", `{"SchemaId":{"SchemaName":"orders","RegistryName":"events"}}`),
			"glue:DeleteSchema")
		if !slicesContain(got, p+"schema/orders") {
			t.Fatalf("derived %v, want it to include the schema named inside SchemaId", got)
		}
	})
}

// A member at the top level is the more direct statement of what a request
// names, so it wins over one found inside a structure — otherwise the ARN would
// depend on which the JSON happened to carry.
func TestIAMResourceARNs_TopLevelMemberWinsOverANestedOne(t *testing.T) {
	got := iamResourceARNsForRequest(
		iamGlueRequest("GetTable", `{"DatabaseName":"top","Name":"orders","Wrapper":{"DatabaseName":"nested"}}`),
		"glue:GetTable")
	for _, a := range got {
		if strings.Contains(a, "nested") {
			t.Fatalf("derived %q from a nested member while the top level named one", a)
		}
	}
	if !slicesContain(got, "arn:aws:glue:us-east-1:123456789012:database/top") {
		t.Fatalf("derived %v, want the top-level database", got)
	}
}

func slicesContain(haystack []string, want string) bool {
	for _, got := range haystack {
		if got == want {
			return true
		}
	}
	return false
}

// ===== Amazon EC2 Auto Scaling =====

func iamAutoScalingRequest(action string, params map[string]string) *http.Request {
	form := "Action=" + action + "&Version=2011-01-01"
	for k, v := range params {
		form += "&" + k + "=" + v
	}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/autoscaling/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// Both Amazon EC2 Auto Scaling ARNs carry two identifiers — one AWS assigns and
// one the caller chose — and a request supplies only the second, so neither can
// be assembled from the request. The gate reads the ARN the resource was
// actually given, which is the only thing a policy written against that resource
// can match.
func TestIAMResourceARNs_AutoScalingResolvesTheARNTheResourceHas(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	autoScalingGroups = sim.MakeStore[AutoScalingGroup](nil, "autoscaling_groups")
	asLaunchConfigurations = sim.MakeStore[ASLaunchConfiguration](nil, "autoscaling_launch_configurations")

	groupARN := autoScalingGroupARN("orders")
	autoScalingGroups.Put("orders", AutoScalingGroup{Name: "orders", ARN: groupARN})
	configARN := launchConfigurationARN("orders-lc")
	asLaunchConfigurations.Put("orders-lc", ASLaunchConfiguration{Name: "orders-lc", ARN: configARN})

	t.Run("a group", func(t *testing.T) {
		assertDerivedARNs(t,
			iamAutoScalingRequest("DeleteAutoScalingGroup", map[string]string{"AutoScalingGroupName": "orders"}),
			"autoscaling:DeleteAutoScalingGroup", groupARN)
	})
	t.Run("a launch configuration", func(t *testing.T) {
		assertDerivedARNs(t,
			iamAutoScalingRequest("DeleteLaunchConfiguration", map[string]string{"LaunchConfigurationName": "orders-lc"}),
			"autoscaling:DeleteLaunchConfiguration", configARN)
	})
	t.Run("a group that does not exist derives nothing rather than a guess", func(t *testing.T) {
		assertDerivedARNs(t,
			iamAutoScalingRequest("DeleteAutoScalingGroup", map[string]string{"AutoScalingGroupName": "absent"}),
			"autoscaling:DeleteAutoScalingGroup", "*")
	})
}

// The published shape carries an assigned identifier and then the name the
// resource is addressed by. A launch configuration's identifier slot held the
// name itself, which made its ARN a restatement of the name rather than the
// resource's own — so a policy written against the real ARN matched nothing.
func TestIAMResourceARNs_AutoScalingARNsTakeTheirPublishedShape(t *testing.T) {
	group := autoScalingGroupARN("orders")
	if !strings.HasSuffix(group, ":autoScalingGroupName/orders") {
		t.Errorf("group ARN = %q, want it to end in the name it is addressed by", group)
	}
	config := launchConfigurationARN("orders-lc")
	if !strings.HasSuffix(config, ":launchConfigurationName/orders-lc") {
		t.Errorf("launch configuration ARN = %q, want it to end in the name", config)
	}
	// The identifier slot is an assigned one, not the name repeated.
	if strings.Contains(config, ":launchConfiguration:orders-lc:") {
		t.Errorf("launch configuration ARN = %q, want an assigned identifier where the name is repeated", config)
	}
	if a, b := launchConfigurationARN("same"), launchConfigurationARN("same"); a == b {
		t.Error("two launch configurations of the same name got the same assigned identifier")
	}
}

// ===== AWS Key Management Service =====

func iamKMSRequest(operation, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "TrentService."+operation)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/kms/aws4_request, SignedHeaders=host, Signature=00")
	return r
}

// An AWS KMS alias is created and deleted as "alias/my-key" — the prefix is
// part of the name a caller passes — and its ARN is
// "…:alias/my-key". Filling the published format with the name unchanged would
// produce "alias/alias/my-key", which names nothing, so the prefix is carried
// once rather than twice.
func TestIAMResourceARNs_KMSAliasCarriesItsPrefixOnce(t *testing.T) {
	const want = "arn:aws:kms:us-east-1:123456789012:alias/orders"
	assertDerivedARNs(t,
		iamKMSRequest("DeleteAlias", `{"AliasName":"alias/orders"}`),
		"kms:DeleteAlias", want)

	// A name given without the prefix still yields one ARN with it, so the two
	// spellings a caller might use do not produce two different resources.
	assertDerivedARNs(t,
		iamKMSRequest("DeleteAlias", `{"AliasName":"orders"}`),
		"kms:DeleteAlias", want)
}

// A key is named by KeyId, which may be a bare identifier or an ARN already.
func TestIAMResourceARNs_KMSKeyTakesEitherSpelling(t *testing.T) {
	const arn = "arn:aws:kms:us-east-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"
	assertDerivedARNs(t,
		iamKMSRequest("DescribeKey", `{"KeyId":"1234abcd-12ab-34cd-56ef-1234567890ab"}`),
		"kms:DescribeKey", arn)
	assertDerivedARNs(t,
		iamKMSRequest("DescribeKey", `{"KeyId":"`+arn+`"}`),
		"kms:DescribeKey", arn)
}

// The whole point, end to end: a policy scoped to one key allows that key and
// denies another.
func TestIAMEnforce_KMSKeyScopedGrant(t *testing.T) {
	const granted = "arn:aws:kms:us-east-1:123456789012:key/granted-key"
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow","Action":["kms:DescribeKey","kms:Decrypt"],
		"Resource":"`+granted+`"}]}`)
	for _, tc := range []struct{ name, key, want string }{
		{"the granted key", "granted-key", "allowed"},
		{"any other key", "other-key", "implicitDeny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := iamKMSRequest("DescribeKey", `{"KeyId":"`+tc.key+`"}`)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatal("request was not classified as an IAM action")
			}
			resources := iamResourceARNsForRequest(r, action)
			if len(resources) != 1 {
				t.Fatalf("derived %v, want exactly one key ARN", resources)
			}
			got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resources[0], nil)
			if got != tc.want {
				t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.key, resources[0], got, tc.want)
			}
		})
	}
}

// AWS Step Functions names a state machine and an activity by ARNs that end in
// the name the create request supplies, so a create authorizes against the
// resource it is about rather than against "*". An alias create does not: its
// Name is the alias's own, while the type it authorizes against is the state
// machine behind it.
func TestIAMResourceARNs_StepFunctionsCreateNamesTheARNItWillHave(t *testing.T) {
	const p = "arn:aws:states:us-east-1:123456789012:"
	t.Run("a state machine create names the state machine", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSStepFunctions.CreateStateMachine",
				`{"name":"order-pipeline","roleArn":"arn:aws:iam::123456789012:role/sfn","definition":"{}"}`),
			"states:CreateStateMachine", p+"stateMachine:order-pipeline")
	})
	t.Run("an activity create names the activity", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSStepFunctions.CreateActivity", `{"name":"human-review"}`),
			"states:CreateActivity", p+"activity:human-review")
	})
	t.Run("an alias create names no state machine", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSStepFunctions.CreateStateMachineAlias",
				`{"name":"live","routingConfiguration":[{"weight":100}]}`),
			"states:CreateStateMachineAlias", "*")
	})
	t.Run("an execution start still names the state machine it was sent", func(t *testing.T) {
		assertDerivedARNs(t,
			iamJSONRequest("AWSStepFunctions.StartExecution",
				`{"stateMachineArn":"`+p+`stateMachine:order-pipeline","name":"run-1"}`),
			"states:StartExecution", p+"stateMachine:order-pipeline")
	})
}
