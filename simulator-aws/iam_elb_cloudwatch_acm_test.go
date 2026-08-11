package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Elastic Load Balancing, Amazon CloudWatch and AWS Certificate Manager address
// their resources three different ways, and each is pinned here against a
// request shaped the way a real client sends it.

func iamDeriveQuery(operation, version, form string) []string {
	return iamDeriveQueryFor("elasticloadbalancing", operation, version, form)
}

func iamDeriveQueryFor(service, operation, version, form string) []string {
	r := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader("Action="+operation+"&Version="+version+"&"+form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return iamDerivedResourceARNs(r, service, operation, "us-east-1", "123456789012")
}

func iamDeriveJSON(service, operation, body string) []string {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	return iamDerivedResourceARNs(r, service, operation, "us-east-1", "123456789012")
}

func iamAssertDerived(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("derived %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("derived %v, want %v", got, want)
		}
	}
}

// A rule's ARN arrives inside a priority entry rather than under a member of
// its own, so the parameter name is a path. The coverage probe fills each
// member with one scalar and therefore cannot express that shape at all — it
// counts this operation as underived — which is why the real shape is pinned
// here rather than by tuning the probe to know about one operation.
func TestIAMResourceARNs_ELBReadsARuleARNNestedInAPriority(t *testing.T) {
	const rule = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/lb/0123456789abcdef/fedcba9876543210/1122334455667788"
	iamAssertDerived(t, iamDeriveQuery("SetRulePriorities", "2015-12-01",
		"RulePriorities.member.1.RuleArn="+rule+"&RulePriorities.member.1.Priority=1"), rule)
}

// A request that names several resources is authorized against all of them:
// creating a listener on a load balancer, forwarding to a target group, is a
// call about both.
func TestIAMResourceARNs_ELBAuthorizesEveryResourceARequestNames(t *testing.T) {
	const lb = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/probe/0123456789abcdef"
	const tg = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/probe/fedcba9876543210"
	iamAssertDerived(t, iamDeriveQuery("CreateListener", "2015-12-01",
		"LoadBalancerArn="+lb+"&TargetGroupArn="+tg), lb, tg)
}

// The previous-generation load balancer is addressed by name, and its ARN
// carries nothing else.
func TestIAMResourceARNs_ELBBuildsTheClassicLoadBalancerFromItsName(t *testing.T) {
	iamAssertDerived(t, iamDeriveQuery("ConfigureHealthCheck", "2012-06-01",
		"LoadBalancerName=classic-lb"),
		"arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/classic-lb")
}

// Amazon CloudWatch names its resources, and each type's name arrives under a
// member of its own.
func TestIAMResourceARNs_CloudWatchFillsTheNamedResource(t *testing.T) {
	iamAssertDerived(t, iamDeriveJSON("cloudwatch", "DescribeAlarms", `{"AlarmNames":["cpu-high"]}`),
		"arn:aws:cloudwatch:us-east-1:123456789012:alarm:cpu-high")
	iamAssertDerived(t, iamDeriveJSON("cloudwatch", "GetDashboard", `{"DashboardName":"ops"}`),
		"arn:aws:cloudwatch::123456789012:dashboard/ops")
	// An insight rule's ${InsightRuleName} arrives as RuleName.
	iamAssertDerived(t, iamDeriveJSON("cloudwatch", "DeleteInsightRules", `{"RuleNames":["contributors"]}`),
		"arn:aws:cloudwatch:us-east-1:123456789012:insight-rule/contributors")
}

// A tagging call names its target by ARN, and the reference lists every
// taggable type for it — which one the call is about is what the ARN says, so
// there is nothing to choose between them.
func TestIAMResourceARNs_TaggingTakesTheARNTheRequestNames(t *testing.T) {
	const alarm = "arn:aws:cloudwatch:us-east-1:123456789012:alarm:cpu-high"
	iamAssertDerived(t, iamDeriveJSON("cloudwatch", "TagResource",
		`{"ResourceARN":"`+alarm+`","Tags":[]}`), alarm)

	const certificate = "arn:aws:acm:us-east-1:123456789012:certificate/0123abcd-ef45-6789-abcd-ef0123456789"
	iamAssertDerived(t, iamDeriveJSON("acm", "AddTagsToCertificate",
		`{"CertificateArn":"`+certificate+`","Tags":[]}`), certificate)
}

// Amazon ECR addresses a repository by name, and every name a request carries
// is authorized — a filtered call is a call about each of them.
func TestIAMResourceARNs_ECRAuthorizesEveryRepositoryNamed(t *testing.T) {
	iamAssertDerived(t, iamDeriveJSON("ecr", "BatchGetImage",
		`{"repositoryName":"edd-dev/golden"}`),
		"arn:aws:ecr:us-east-1:123456789012:repository/edd-dev/golden")
	iamAssertDerived(t, iamDeriveJSON("ecr", "DescribeRepositories",
		`{"repositoryNames":["one","two"]}`),
		"arn:aws:ecr:us-east-1:123456789012:repository/one",
		"arn:aws:ecr:us-east-1:123456789012:repository/two")
}

// Amazon Kinesis addresses a stream by name or by ARN, and a consumer only by
// ARN — a consumer's ARN ends in the timestamp at which it was registered,
// which no request supplies and nothing can reconstruct.
func TestIAMResourceARNs_KinesisTakesTheStreamNameOrTheARN(t *testing.T) {
	iamAssertDerived(t, iamDeriveJSON("kinesis", "PutRecord", `{"StreamName":"events"}`),
		"arn:aws:kinesis:us-east-1:123456789012:stream/events")
	const consumer = "arn:aws:kinesis:us-east-1:123456789012:stream/events/consumer/reader:1700000000"
	iamAssertDerived(t, iamDeriveJSON("kinesis", "SubscribeToShard",
		`{"ConsumerARN":"`+consumer+`"}`), consumer)
}

// AWS Step Functions addresses every resource by its own ARN, and those ARNs
// carry parts no request supplies separately — an execution's assigned id, a
// labelled execution's map-run label inside the state machine segment — so
// there is nothing to assemble.
func TestIAMResourceARNs_StepFunctionsTakesTheARNTheRequestNames(t *testing.T) {
	const execution = "arn:aws:states:us-east-1:123456789012:execution:orders:0123abcd"
	iamAssertDerived(t, iamDeriveJSON("states", "DescribeExecution",
		`{"executionArn":"`+execution+`"}`), execution)

	const machine = "arn:aws:states:us-east-1:123456789012:stateMachine:orders"
	iamAssertDerived(t, iamDeriveJSON("states", "StartExecution",
		`{"stateMachineArn":"`+machine+`","input":"{}"}`), machine)
}

// A topic is addressed by its ARN, by ResourceArn on the tagging and
// data-protection operations, and by name only at creation.
func TestIAMResourceARNs_SNSTakesTheTopicHoweverItIsNamed(t *testing.T) {
	const topic = "arn:aws:sns:us-east-1:123456789012:orders"
	iamAssertDerived(t, iamDeriveQueryFor("sns", "Publish", "2010-03-31", "TopicArn="+topic), topic)
	iamAssertDerived(t, iamDeriveQueryFor("sns", "ListTagsForResource", "2010-03-31", "ResourceArn="+topic), topic)
	iamAssertDerived(t, iamDeriveQueryFor("sns", "CreateTopic", "2010-03-31", "Name=orders"), topic)
	// A subscription's ARN is the topic's with the subscription id appended,
	// and the reference declares the topic as what these authorize against.
	iamAssertDerived(t, iamDeriveQueryFor("sns", "Unsubscribe", "2010-03-31",
		"SubscriptionArn="+topic+":8a21d249-4329-4871-acc6-7be709c6ea7f"), topic)
}

// A queue is addressed by its URL, whose last segment is its name — which is
// what the queue's ARN ends in. The message-move operations name their queues
// by ARN instead, and a move is a call about both ends of it.
func TestIAMResourceARNs_SQSTakesTheQueueFromItsURLOrARN(t *testing.T) {
	const queue = "arn:aws:sqs:us-east-1:123456789012:orders"
	iamAssertDerived(t, iamDeriveQueryFor("sqs", "SendMessage", "2012-11-05",
		"QueueUrl=http://localhost:4566/123456789012/orders"), queue)
	const dead = "arn:aws:sqs:us-east-1:123456789012:orders-dlq"
	iamAssertDerived(t, iamDeriveQueryFor("sqs", "StartMessageMoveTask", "2012-11-05",
		"SourceArn="+dead+"&DestinationArn="+queue), dead, queue)
}

// A secret is addressed by SecretId, which the API accepts as either its name
// or its full ARN, and named in Name at creation — where SecretId does not
// exist yet (GitHub issue #889).
func TestIAMResourceARNs_SecretsManagerTakesTheNameOrTheARN(t *testing.T) {
	const secret = "arn:aws:secretsmanager:us-east-1:123456789012:secret:db-password"
	iamAssertDerived(t, iamDeriveJSON("secretsmanager", "GetSecretValue",
		`{"SecretId":"db-password"}`), secret)
	iamAssertDerived(t, iamDeriveJSON("secretsmanager", "GetSecretValue",
		`{"SecretId":"`+secret+`"}`), secret)
	iamAssertDerived(t, iamDeriveJSON("secretsmanager", "CreateSecret",
		`{"Name":"db-password"}`), secret)
}

// A certificate authority's ARN carries an identifier AWS assigned, which no
// request supplies as a part — what a request carries is the whole ARN.
func TestIAMResourceARNs_ACMPCATakesTheAuthorityARN(t *testing.T) {
	const ca = "arn:aws:acm-pca:us-east-1:123456789012:certificate-authority/0123abcd-ef45-6789-abcd-ef0123456789"
	iamAssertDerived(t, iamDeriveJSON("acm-pca", "DescribeCertificateAuthority",
		`{"CertificateAuthorityArn":"`+ca+`"}`), ca)
}

// AWS Cloud Map addresses a namespace and a service by an assigned id, under
// the resource's own member or simply as Id where no qualifier is needed. The
// discovery operations address them by name instead, and the name-to-ARN
// mapping lives in the simulator's state because the ARN carries the id.
func TestIAMResourceARNs_CloudMapTakesTheIdOrResolvesTheName(t *testing.T) {
	buildConformanceSimulator(t)
	iamAssertDerived(t, iamDeriveJSON("servicediscovery", "GetNamespace", `{"Id":"ns-1234"}`),
		"arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-1234")
	iamAssertDerived(t, iamDeriveJSON("servicediscovery", "ListInstances", `{"ServiceId":"srv-1234"}`),
		"arn:aws:servicediscovery:us-east-1:123456789012:service/srv-1234")

	namespace := CMNamespace{Id: "ns-live", Name: "prod",
		Arn: "arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-live"}
	cmNamespaces.Put(namespace.Id, namespace)
	service := CMService{Id: "srv-live", Name: "api", NamespaceId: namespace.Id,
		Arn: "arn:aws:servicediscovery:us-east-1:123456789012:service/srv-live"}
	cmServices.Put(service.Id, service)
	t.Cleanup(func() {
		cmNamespaces.Delete(namespace.Id)
		cmServices.Delete(service.Id)
	})

	// Discovering a service is a call about the namespace and the service, and
	// the ARNs are the ones those resources actually have.
	iamAssertDerived(t, iamDeriveJSON("servicediscovery", "DiscoverInstances",
		`{"NamespaceName":"prod","ServiceName":"api"}`), namespace.Arn, service.Arn)

	// A name no namespace has derives nothing rather than an invented ARN.
	iamAssertDerived(t, iamDeriveJSON("servicediscovery", "DiscoverInstances",
		`{"NamespaceName":"absent","ServiceName":"absent"}`))
}
