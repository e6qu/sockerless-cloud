package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// resetTagStores re-creates the in-memory stores the resolvers read, so each
// case starts clean without a *sim.Server.
func resetTagStores() {
	lambdaFunctions = sim.MakeStore[LambdaFunction](nil, "lambda_functions")
	sqsQueues = sim.MakeStore[SQSQueue](nil, "sqs_queues")
	snsTopics = sim.MakeStore[SNSTopic](nil, "sns_topics")
	rdsInstances = sim.MakeStore[RDSInstance](nil, "rds_instances")
	rdsSnapshots = sim.MakeStore[RDSSnapshot](nil, "rds_snapshots")
	elbv2LoadBalancers = sim.MakeStore[ELBv2LoadBalancer](nil, "elbv2_load_balancers")
	elbv2TargetGroups = sim.MakeStore[ELBv2TargetGroup](nil, "elbv2_target_groups")
	ecClusters = sim.MakeStore[ECCluster](nil, "elasticache_clusters")
	ddbTables = sim.MakeStore[DDBTable](nil, "ddb_tables")
	ddbTableSettings = sim.MakeStore[DDBTableSettings](nil, "ddb_table_settings")
	ecrRepositories = sim.MakeStore[ECRRepository](nil, "ecr_repositories")
	cwLogGroups = sim.MakeStore[CWLogGroup](nil, "cw_log_groups")
	sfnStateMachines = sim.MakeStore[SFNStateMachine](nil, "sfn_state_machines")
	kmsKeys = sim.MakeStore[KMSKey](nil, "kms_keys")
	kmsAliases = sim.MakeStore[string](nil, "kms_aliases")
	smSecrets = sim.MakeStore[SMSecret](nil, "sm_secrets")
	kinesisStreams = sim.MakeStore[KinesisStream](nil, "kinesis_streams")
	glueDatabases = sim.MakeStore[GlueDatabase](nil, "glue_databases")
	glueJobs = sim.MakeStore[GlueJob](nil, "glue_jobs")
	batchComputeEnvs = sim.MakeStore[BatchComputeEnvironment](nil, "batch_compute_envs")
}

// jsonRequest builds an awsJson POST request carrying body.
func jsonRequest(body string) *http.Request {
	return httptest.NewRequest("POST", "/", strings.NewReader(body))
}

// formRequest builds an awsQuery POST request with the given form fields.
func formRequest(fields map[string]string) *http.Request {
	form := make([]string, 0, len(fields))
	for k, v := range fields {
		form = append(form, k+"="+v)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Join(form, "&")))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r
}

// assertResolved runs the dispatcher and asserts both aws:ResourceTag/<k> and
// <service>:ResourceTag/<k> for key=val.
func assertResolved(t *testing.T, service string, r *http.Request, key, val string) {
	t.Helper()
	ctx := map[string][]string{}
	if !iamPopulateServiceResourceTags(r, service, ctx) {
		t.Fatalf("%s: dispatcher did not handle service", service)
	}
	if got := ctx["aws:ResourceTag/"+key]; len(got) != 1 || got[0] != val {
		t.Fatalf("%s: aws:ResourceTag/%s = %v, want [%s]", service, key, got, val)
	}
	if got := ctx[service+":ResourceTag/"+key]; len(got) != 1 || got[0] != val {
		t.Fatalf("%s: %s:ResourceTag/%s = %v, want [%s]", service, service, key, got, val)
	}
}

func TestIAMServiceResourceTags(t *testing.T) {
	resetTagStores()

	// ── awsJson + Lambda REST path ──

	lambdaFunctions.Put("fn1", LambdaFunction{FunctionName: "fn1", Tags: map[string]string{"team": "blue"}})
	t.Run("lambda_name_path", func(t *testing.T) {
		r := jsonRequest("")
		r.SetPathValue("name", "fn1")
		assertResolved(t, "lambda", r, "team", "blue")
	})
	t.Run("lambda_arn_path", func(t *testing.T) {
		r := jsonRequest("")
		r.SetPathValue("arn", "arn:aws:lambda:us-east-1:123456789012:function:fn1")
		assertResolved(t, "lambda", r, "team", "blue")
	})

	sqsQueues.Put("q1", SQSQueue{Tags: map[string]string{"qt": "fast"}})
	t.Run("sqs_queueurl", func(t *testing.T) {
		assertResolved(t, "sqs",
			jsonRequest(`{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/123456789012/q1"}`),
			"qt", "fast")
	})

	ddbTables.Put("orders", DDBTable{TableName: "orders"})
	ddbTableSettings.Put("orders", DDBTableSettings{Tags: []SMTag{{Key: "env", Value: "prod"}}})
	t.Run("dynamodb_arn", func(t *testing.T) {
		assertResolved(t, "dynamodb",
			jsonRequest(`{"ResourceArn":"arn:aws:dynamodb:us-east-1:123456789012:table/orders"}`),
			"env", "prod")
	})

	ecrRepositories.Put("app", ECRRepository{RepositoryName: "app", Tags: []SMTag{{Key: "owner", Value: "x"}}})
	t.Run("ecr_arn", func(t *testing.T) {
		assertResolved(t, "ecr",
			jsonRequest(`{"resourceArn":"arn:aws:ecr:us-east-1:123456789012:repository/app"}`),
			"owner", "x")
	})

	cwLogGroups.Put("/svc/log", CWLogGroup{LogGroupName: "/svc/log", Tags: map[string]string{"k": "v"}})
	t.Run("logs_arn", func(t *testing.T) {
		assertResolved(t, "logs",
			jsonRequest(`{"resourceArn":"arn:aws:logs:us-east-1:123456789012:log-group:/svc/log:*"}`),
			"k", "v")
	})

	sfnStateMachines.Put("flow", SFNStateMachine{Name: "flow", Tags: []SFNTag{{Key: "tier", Value: "1"}}})
	t.Run("states_arn", func(t *testing.T) {
		assertResolved(t, "states",
			jsonRequest(`{"resourceArn":"arn:aws:states:us-east-1:123456789012:stateMachine:flow"}`),
			"tier", "1")
	})

	kmsKeys.Put("key-1", KMSKey{KeyId: "key-1", Tags: []KMSTag{{TagKey: "purpose", TagValue: "enc"}}})
	t.Run("kms_keyid", func(t *testing.T) {
		assertResolved(t, "kms", jsonRequest(`{"KeyId":"key-1"}`), "purpose", "enc")
	})
	t.Run("kms_alias", func(t *testing.T) {
		kmsAliases.Put("alias/mine", "key-1")
		assertResolved(t, "kms", jsonRequest(`{"KeyId":"alias/mine"}`), "purpose", "enc")
	})

	smSecrets.Put("db-pass", SMSecret{Name: "db-pass", ARN: "arn:aws:secretsmanager:us-east-1:123456789012:secret:db-pass-AbC",
		Tags: []SMTag{{Key: "scope", Value: "db"}}})
	t.Run("secretsmanager_name", func(t *testing.T) {
		assertResolved(t, "secretsmanager", jsonRequest(`{"SecretId":"db-pass"}`), "scope", "db")
	})
	t.Run("secretsmanager_arn", func(t *testing.T) {
		assertResolved(t, "secretsmanager",
			jsonRequest(`{"SecretId":"arn:aws:secretsmanager:us-east-1:123456789012:secret:db-pass-AbC"}`),
			"scope", "db")
	})

	kinesisStreams.Put("events", KinesisStream{StreamName: "events", StreamARN: "arn:aws:kinesis:us-east-1:123456789012:stream/events",
		Tags: map[string]string{"src": "web"}})
	t.Run("kinesis_name", func(t *testing.T) {
		assertResolved(t, "kinesis", jsonRequest(`{"StreamName":"events"}`), "src", "web")
	})
	t.Run("kinesis_arn", func(t *testing.T) {
		assertResolved(t, "kinesis",
			jsonRequest(`{"StreamARN":"arn:aws:kinesis:us-east-1:123456789012:stream/events"}`),
			"src", "web")
	})

	glueDatabases.Put("analytics", GlueDatabase{Tags: map[string]string{"dom": "data"}})
	t.Run("glue_database_arn", func(t *testing.T) {
		assertResolved(t, "glue",
			jsonRequest(`{"ResourceArn":"arn:aws:glue:us-east-1:123456789012:database/analytics"}`),
			"dom", "data")
	})
	glueJobs.Put("etl", GlueJob{Tags: map[string]string{"job": "nightly"}})
	t.Run("glue_job_arn", func(t *testing.T) {
		assertResolved(t, "glue",
			jsonRequest(`{"ResourceArn":"arn:aws:glue:us-east-1:123456789012:job/etl"}`),
			"job", "nightly")
	})

	// ── awsQuery ──

	snsTopics.Put("alerts", SNSTopic{Tags: map[string]string{"crit": "yes"}})
	t.Run("sns_arn", func(t *testing.T) {
		assertResolved(t, "sns",
			formRequest(map[string]string{"ResourceArn": "arn:aws:sns:us-east-1:123456789012:alerts"}),
			"crit", "yes")
	})

	rdsInstances.Put("db1", RDSInstance{ARN: "arn:aws:rds:us-east-1:123456789012:db:db1", Tags: map[string]string{"app": "core"}})
	t.Run("rds_resourcename", func(t *testing.T) {
		assertResolved(t, "rds",
			formRequest(map[string]string{"ResourceName": "arn:aws:rds:us-east-1:123456789012:db:db1"}),
			"app", "core")
	})

	lbArn := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/abc"
	elbv2LoadBalancers.Put(lbArn, ELBv2LoadBalancer{Tags: map[string]string{"net": "edge"}})
	t.Run("elbv2_resourcearns", func(t *testing.T) {
		assertResolved(t, "elasticloadbalancing",
			formRequest(map[string]string{"ResourceArns.member.1": lbArn}),
			"net", "edge")
	})

	ecClusters.Put("cache1", ECCluster{ARN: "arn:aws:elasticache:us-east-1:123456789012:cluster:cache1", Tags: map[string]string{"ttl": "60"}})
	t.Run("elasticache_resourcename", func(t *testing.T) {
		assertResolved(t, "elasticache",
			formRequest(map[string]string{"ResourceName": "arn:aws:elasticache:us-east-1:123456789012:cluster:cache1"}),
			"ttl", "60")
	})

	// ── REST: Batch path arn, S3 bucket/object tags ──

	ceArn := "arn:aws:batch:us-east-1:123456789012:compute-environment/ce1"
	batchComputeEnvs.Put("ce1", BatchComputeEnvironment{ComputeEnvironmentName: "ce1", ComputeEnvironmentArn: ceArn,
		Tags: map[string]string{"pool": "spot"}})
	t.Run("batch_path_arn", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/v1/tags/"+ceArn, nil)
		r.SetPathValue("resourceArn", ceArn)
		assertResolved(t, "batch", r, "pool", "spot")
	})

	t.Run("s3_object_tags", func(t *testing.T) {
		s3ObjectTags = sim.MakeStore[map[string]string](nil, "s3_object_tags")
		s3ObjectTags.Put("bkt/key.txt", map[string]string{"cls": "warm"})
		r := httptest.NewRequest("GET", "/bkt/key.txt", nil)
		r.SetPathValue("bucket", "bkt")
		r.SetPathValue("key", "key.txt")
		assertResolved(t, "s3", r, "cls", "warm")
	})

	// ── negative: unknown resource yields no keys (gate leaves them unset) ──

	t.Run("unknown_resource_no_keys", func(t *testing.T) {
		ctx := map[string][]string{}
		r := jsonRequest(`{"ResourceArn":"arn:aws:dynamodb:us-east-1:123456789012:table/nope"}`)
		iamPopulateServiceResourceTags(r, "dynamodb", ctx)
		if len(ctx) != 0 {
			t.Fatalf("expected no condition keys for an unknown table, got %v", ctx)
		}
	})

	// ── a non-tag-storing service is reported as not-handled ──

	t.Run("unhandled_service", func(t *testing.T) {
		if iamPopulateServiceResourceTags(jsonRequest(""), "sts", map[string][]string{}) {
			t.Fatal("sts must not be handled by the service tag dispatcher")
		}
	})
}
