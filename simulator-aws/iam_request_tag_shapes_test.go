package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// aws:RequestTag/${TagKey} and aws:TagKeys are the facts a policy tests to
// force a tag onto a resource at create time. The gate used to read one wire
// shape — the awsQuery member prefix `Tag`, which only Amazon EC2's CreateTags
// sends — so for every other service both keys were silently unset and the
// documented tag-on-create restriction could not be expressed. These tests hold
// the gate to the shape each service's own Smithy model declares.

// queryTagRequest builds the awsQuery POST a service's SDK would send.
func queryTagRequest(operation string, form url.Values) *http.Request {
	form.Set("Action", operation)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// jsonTagRequest builds the awsJson POST a service's SDK would send.
func jsonTagRequest(target, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	return r
}

func requestTagContext(r *http.Request, service string) map[string][]string {
	ctx := map[string][]string{}
	iamPopulateRequestTags(r, service, ctx)
	return ctx
}

// TestIAMRequestTagsPerServiceWireShape walks one create/tagging request per
// wire shape the simulator serves and asserts the gate built
// aws:RequestTag/<k> and aws:TagKeys from it — and nothing else.
func TestIAMRequestTagsPerServiceWireShape(t *testing.T) {
	cases := []struct {
		name    string
		service string
		request *http.Request
		want    map[string][]string
	}{
		// Amazon RDS: TagList's member carries xmlName "Tag", so Tags.Tag.N.
		{"an Amazon RDS instance created with tags", "rds",
			queryTagRequest("CreateDBInstance", url.Values{
				"DBInstanceIdentifier": {"orders-db"},
				"Engine":               {"postgres"},
				"Tags.Tag.1.Key":       {"owner"}, "Tags.Tag.1.Value": {"platform"},
				"Tags.Tag.2.Key": {"env"}, "Tags.Tag.2.Value": {"prod"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"platform"},
				"aws:RequestTag/env":   {"prod"},
				"aws:TagKeys":          {"owner", "env"},
			}},
		// Amazon ElastiCache: the same xmlName "Tag" list member as RDS.
		{"an Amazon ElastiCache cluster created with tags", "elasticache",
			queryTagRequest("CreateCacheCluster", url.Values{
				"CacheClusterId": {"sessions"},
				"Tags.Tag.1.Key": {"owner"}, "Tags.Tag.1.Value": {"caching"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"caching"},
				"aws:TagKeys":          {"owner"},
			}},
		// Elastic Load Balancing v2: no xmlName on the list member, so the
		// awsQuery default `member` applies.
		{"an Application Load Balancer created with tags", "elasticloadbalancing",
			queryTagRequest("CreateLoadBalancer", url.Values{
				"Name":              {"edge"},
				"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"networking"},
				"Tags.member.2.Key": {"tier"}, "Tags.member.2.Value": {"public"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"networking"},
				"aws:RequestTag/tier":  {"public"},
				"aws:TagKeys":          {"owner", "tier"},
			}},
		{"tags added to a target group", "elasticloadbalancing",
			queryTagRequest("AddTags", url.Values{
				"ResourceArns.member.1": {"arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/tg/1"},
				"Tags.member.1.Key":     {"owner"}, "Tags.member.1.Value": {"networking"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"networking"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS IAM, AWS STS, Amazon SNS, Auto Scaling and Amazon CloudWatch all
		// take the same awsQuery default.
		{"an IAM role created with tags", "iam",
			queryTagRequest("CreateRole", url.Values{
				"RoleName":          {"deployer"},
				"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"identity"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"identity"},
				"aws:TagKeys":          {"owner"},
			}},
		{"session tags on a role assumption", "sts",
			queryTagRequest("AssumeRole", url.Values{
				"RoleArn":           {"arn:aws:iam::123456789012:role/deployer"},
				"RoleSessionName":   {"ci"},
				"Tags.member.1.Key": {"project"}, "Tags.member.1.Value": {"ledger"},
			}),
			map[string][]string{
				"aws:RequestTag/project": {"ledger"},
				"aws:TagKeys":            {"project"},
			}},
		{"an Amazon SNS topic created with tags", "sns",
			queryTagRequest("CreateTopic", url.Values{
				"Name":              {"alerts"},
				"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"observability"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"observability"},
				"aws:TagKeys":          {"owner"},
			}},
		{"an Auto Scaling group created with tags", "autoscaling",
			queryTagRequest("CreateAutoScalingGroup", url.Values{
				"AutoScalingGroupName": {"web"},
				"Tags.member.1.Key":    {"owner"}, "Tags.member.1.Value": {"web"},
				"Tags.member.1.PropagateAtLaunch": {"true"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"web"},
				"aws:TagKeys":          {"owner"},
			}},
		{"a CloudWatch alarm created with tags", "cloudwatch",
			queryTagRequest("PutMetricAlarm", url.Values{
				"AlarmName":         {"cpu-high"},
				"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"observability"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"observability"},
				"aws:TagKeys":          {"owner"},
			}},
		// Amazon EC2 (ec2Query): CreateTags sends the flattened `Tag.N` the
		// gate already read — that behaviour must not regress.
		{"an Amazon EC2 CreateTags call", "ec2",
			queryTagRequest("CreateTags", url.Values{
				"ResourceId.1": {"i-0123456789abcdef0"},
				"Tag.1.Key":    {"owner"}, "Tag.1.Value": {"compute"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"compute"},
				"aws:TagKeys":          {"owner"},
			}},
		// …while EC2 tag-on-create sends TagSpecification.N.Tag.M, which the
		// single hard-coded prefix never saw.
		{"an Amazon EC2 volume created with a tag specification", "ec2",
			queryTagRequest("CreateVolume", url.Values{
				"AvailabilityZone":                {"us-east-1a"},
				"Size":                            {"10"},
				"TagSpecification.1.ResourceType": {"volume"},
				"TagSpecification.1.Tag.1.Key":    {"owner"}, "TagSpecification.1.Tag.1.Value": {"compute"},
			}),
			map[string][]string{
				"aws:RequestTag/owner": {"compute"},
				"aws:TagKeys":          {"owner"},
			}},

		// JSON protocols. Amazon ECS spells the tag structure's members in
		// lower case.
		{"an Amazon ECS cluster created with tags", "ecs",
			jsonTagRequest("AmazonEC2ContainerServiceV20141113.CreateCluster",
				`{"clusterName":"edd","tags":[{"key":"owner","value":"platform"},{"key":"env","value":"dev"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"platform"},
				"aws:RequestTag/env":   {"dev"},
				"aws:TagKeys":          {"owner", "env"},
			}},
		// Amazon DynamoDB uses Key/Value.
		{"an Amazon DynamoDB table created with tags", "dynamodb",
			jsonTagRequest("DynamoDB_20120810.CreateTable",
				`{"TableName":"orders","Tags":[{"Key":"owner","Value":"data"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"data"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS KMS is the one service whose tag structure is TagKey/TagValue.
		{"an AWS KMS key created with tags", "kms",
			jsonTagRequest("TrentService.CreateKey",
				`{"Description":"app","Tags":[{"TagKey":"owner","TagValue":"security"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"security"},
				"aws:TagKeys":          {"owner"},
			}},
		// Amazon SQS carries tags as a map, and names the member `tags` on
		// CreateQueue and `Tags` on TagQueue.
		{"an Amazon SQS queue created with tags", "sqs",
			jsonTagRequest("AmazonSQS.CreateQueue",
				`{"QueueName":"jobs","tags":{"owner":"messaging","env":"prod"}}`),
			map[string][]string{
				"aws:RequestTag/owner": {"messaging"},
				"aws:RequestTag/env":   {"prod"},
				"aws:TagKeys":          {"env", "owner"},
			}},
		{"tags added to an Amazon SQS queue", "sqs",
			jsonTagRequest("AmazonSQS.TagQueue",
				`{"QueueUrl":"http://localhost/000000000000/jobs","Tags":{"owner":"messaging"}}`),
			map[string][]string{
				"aws:RequestTag/owner": {"messaging"},
				"aws:TagKeys":          {"owner"},
			}},
		// Amazon CloudWatch Logs carries tags as a map under a lower-case
		// member name.
		{"an Amazon CloudWatch Logs group created with tags", "logs",
			jsonTagRequest("Logs_20140328.CreateLogGroup",
				`{"logGroupName":"/edd/app","tags":{"owner":"observability"}}`),
			map[string][]string{
				"aws:RequestTag/owner": {"observability"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS Step Functions spells the tag structure's members in lower case.
		{"an AWS Step Functions state machine created with tags", "states",
			jsonTagRequest("AWSStepFunctions.CreateStateMachine",
				`{"name":"pipeline","tags":[{"key":"owner","value":"orchestration"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"orchestration"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS CloudTrail names the member TagsList.
		{"an AWS CloudTrail trail created with tags", "cloudtrail",
			jsonTagRequest("CloudTrail_20131101.CreateTrail",
				`{"Name":"audit","TagsList":[{"Key":"owner","Value":"security"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"security"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS Budgets names the member ResourceTags.
		{"an AWS Budgets budget created with tags", "budgets",
			jsonTagRequest("AWSBudgetServiceGateway.CreateBudget",
				`{"AccountId":"123456789012","ResourceTags":[{"Key":"owner","Value":"finance"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"finance"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS Glue declares Tags as a map on its 28 classic create operations…
		{"an AWS Glue job created with tags", "glue",
			jsonTagRequest("AWSGlue.CreateJob",
				`{"Name":"etl","Tags":{"owner":"analytics"}}`),
			map[string][]string{
				"aws:RequestTag/owner": {"analytics"},
				"aws:TagKeys":          {"owner"},
			}},
		// …and as a list of key/value on its integration operations.
		{"an AWS Glue integration created with tags", "glue",
			jsonTagRequest("AWSGlue.CreateIntegration",
				`{"IntegrationName":"zero-etl","Tags":[{"key":"owner","value":"analytics"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"analytics"},
				"aws:TagKeys":          {"owner"},
			}},
		// AWS Secrets Manager uses Key/Value.
		{"an AWS Secrets Manager secret created with tags", "secretsmanager",
			jsonTagRequest("secretsmanager.CreateSecret",
				`{"Name":"db/password","Tags":[{"Key":"owner","Value":"security"}]}`),
			map[string][]string{
				"aws:RequestTag/owner": {"security"},
				"aws:TagKeys":          {"owner"},
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertExactConditionContext(t, requestTagContext(c.request, c.service), c.want)
		})
	}
}

// TestIAMRequestTagsAreUnsetWithoutTags holds the no-fallback rule: a request
// that carries no tags leaves aws:RequestTag/<k> and aws:TagKeys absent rather
// than writing an empty value — a condition on a key AWS would not have set
// must not be satisfied here.
func TestIAMRequestTagsAreUnsetWithoutTags(t *testing.T) {
	cases := []struct {
		name    string
		service string
		request *http.Request
	}{
		{"an Amazon RDS create with no tags", "rds",
			queryTagRequest("CreateDBInstance", url.Values{
				"DBInstanceIdentifier": {"orders-db"}, "Engine": {"postgres"}})},
		{"an Amazon ElastiCache create with no tags", "elasticache",
			queryTagRequest("CreateCacheCluster", url.Values{"CacheClusterId": {"sessions"}})},
		{"a load balancer created with no tags", "elasticloadbalancing",
			queryTagRequest("CreateLoadBalancer", url.Values{"Name": {"edge"}})},
		{"an Amazon EC2 volume created with no tags", "ec2",
			queryTagRequest("CreateVolume", url.Values{"AvailabilityZone": {"us-east-1a"}, "Size": {"10"}})},
		{"an Amazon ECS cluster created with no tags", "ecs",
			jsonTagRequest("AmazonEC2ContainerServiceV20141113.CreateCluster", `{"clusterName":"edd"}`)},
		{"an Amazon ECS cluster created with an empty tag list", "ecs",
			jsonTagRequest("AmazonEC2ContainerServiceV20141113.CreateCluster", `{"clusterName":"edd","tags":[]}`)},
		{"an Amazon DynamoDB table created with no tags", "dynamodb",
			jsonTagRequest("DynamoDB_20120810.CreateTable", `{"TableName":"orders"}`)},
		{"an Amazon SQS queue created with an empty tag map", "sqs",
			jsonTagRequest("AmazonSQS.CreateQueue", `{"QueueName":"jobs","tags":{}}`)},
		{"an AWS KMS request with no body at all", "kms",
			jsonTagRequest("TrentService.ListKeys", ``)},
		// A service this simulator serves but for which no tag member is
		// declared settles nothing, and so does one that is not in the table.
		{"a service with no request-tag shape", "route53",
			jsonTagRequest("Route53.CreateHostedZone", `{"Name":"example.com","Tags":[{"Key":"owner","Value":"dns"}]}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertExactConditionContext(t, requestTagContext(c.request, c.service), map[string][]string{})
		})
	}
}

// TestIAMRequestTagsDoNotCrossWireShapes is the half that keeps the table
// honest: reading is per service, not a chain of guesses, so a request that
// carries tags in another service's spelling settles nothing. Without that,
// a policy conditioned on aws:TagKeys would match on a shape the service never
// sends.
func TestIAMRequestTagsDoNotCrossWireShapes(t *testing.T) {
	cases := []struct {
		name    string
		service string
		request *http.Request
	}{
		{"Amazon RDS does not read the Elastic Load Balancing spelling", "rds",
			queryTagRequest("CreateDBInstance", url.Values{
				"DBInstanceIdentifier": {"orders-db"},
				"Tags.member.1.Key":    {"owner"}, "Tags.member.1.Value": {"platform"}})},
		{"Elastic Load Balancing does not read the Amazon RDS spelling", "elasticloadbalancing",
			queryTagRequest("CreateLoadBalancer", url.Values{
				"Name":           {"edge"},
				"Tags.Tag.1.Key": {"owner"}, "Tags.Tag.1.Value": {"networking"}})},
		{"Amazon RDS does not read the Amazon EC2 spelling", "rds",
			queryTagRequest("CreateDBInstance", url.Values{
				"DBInstanceIdentifier": {"orders-db"},
				"Tag.1.Key":            {"owner"}, "Tag.1.Value": {"platform"}})},
		{"Amazon DynamoDB does not read the Amazon ECS spelling", "dynamodb",
			jsonTagRequest("DynamoDB_20120810.CreateTable",
				`{"TableName":"orders","tags":[{"key":"owner","value":"data"}]}`)},
		{"AWS KMS does not read a Key/Value tag structure", "kms",
			jsonTagRequest("TrentService.CreateKey",
				`{"Description":"app","Tags":[{"Key":"owner","Value":"security"}]}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertExactConditionContext(t, requestTagContext(c.request, c.service), map[string][]string{})
		})
	}
}
