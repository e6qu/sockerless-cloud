package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestBatchConditionKeysReadTheSchedulingPriority(t *testing.T) {
	for _, operation := range []string{"SubmitServiceJob", "UpdateServiceJob"} {
		ctx := populatedConditionContext(httptest.NewRequest(http.MethodPost, "/", nil), "batch", operation,
			`{"jobId":"j","jobQueue":"q","schedulingPriority":40}`)
		assertPopulatedConditionValues(t, ctx, map[string][]string{"batch:SchedulingPriority": {"40"}})
	}
	ctx := populatedConditionContext(httptest.NewRequest(http.MethodPost, "/", nil), "batch", "SubmitServiceJob",
		`{"jobQueue":"q"}`)
	assertConditionKeysAbsent(t, ctx, "batch:SchedulingPriority")
	ctx = populatedConditionContext(httptest.NewRequest(http.MethodPost, "/", nil), "batch", "SubmitJob",
		`{"jobQueue":"q","schedulingPriorityOverride":40}`)
	assertConditionKeysAbsent(t, ctx, "batch:SchedulingPriority")
}

func batchJobDefinitionContexts(t *testing.T, body string) []map[string][]string {
	t.Helper()
	const definition = "arn:aws:batch:us-east-1:123456789012:job-definition/jd"
	r := httptest.NewRequest(http.MethodPost, "/v1/registerjobdefinition", strings.NewReader(body))
	var contexts []map[string][]string
	for _, target := range iamOperationTargets(r, "batch", "RegisterJobDefinition", []string{definition}) {
		if target.action != "batch:RegisterJobDefinition" || target.resource != definition {
			t.Fatalf("target %s on %s, want batch:RegisterJobDefinition on %s", target.action, target.resource, definition)
		}
		contexts = append(contexts, target.context)
	}
	return contexts
}

// AWS Batch evaluates its condition keys on each container, so a job
// definition is authorized once per container, each with its own keys.
func TestBatchJobDefinitionIsAuthorizedPerContainer(t *testing.T) {
	got := batchJobDefinitionContexts(t, `{
		"jobDefinitionName": "jd", "type": "multinode",
		"nodeProperties": {"numNodes": 2, "mainNode": 0, "nodeRangeProperties": [
			{"targetNodes": "0", "container": {"image": "main:1", "privileged": true, "user": "nobody",
				"logConfiguration": {"logDriver": "awslogs", "options": {
					"awslogs-group": "/batch/jd", "awslogs-region": "us-west-2",
					"awslogs-stream-prefix": "jd", "awslogs-create-group": "true"}}}},
			{"targetNodes": "1:", "ecsProperties": {"taskProperties": [{"containers": [
				{"name": "a", "image": "worker:1", "privileged": false,
					"logConfiguration": {"logDriver": "json-file", "options": {"awslogs-group": "ignored"}}}]}]}}
		]}
	}`)
	want := []map[string][]string{
		{
			"batch:Image": {"main:1"}, "batch:Privileged": {"true"}, "batch:User": {"nobody"},
			"batch:LogDriver": {"awslogs"}, "batch:AWSLogsGroup": {"/batch/jd"}, "batch:AWSLogsRegion": {"us-west-2"},
			"batch:AWSLogsStreamPrefix": {"jd"}, "batch:AWSLogsCreateGroup": {"true"},
		},
		{"batch:Image": {"worker:1"}, "batch:Privileged": {"false"}, "batch:LogDriver": {"json-file"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contexts = %v, want %v", got, want)
	}
}

func TestBatchJobDefinitionReadsTheEKSPod(t *testing.T) {
	got := batchJobDefinitionContexts(t, `{
		"jobDefinitionName": "jd", "type": "container",
		"eksProperties": {"podProperties": {
			"serviceAccountName": "runner", "metadata": {"namespace": "jobs"},
			"initContainers": [{"image": "init:1"}],
			"containers": [{"image": "app:1", "securityContext": {"privileged": false, "runAsUser": 1000, "runAsGroup": 2000}}]
		}}
	}`)
	want := []map[string][]string{
		{
			"batch:EKSImage": {"app:1"}, "batch:EKSServiceAccountName": {"runner"}, "batch:EKSNamespace": {"jobs"},
			"batch:EKSPrivileged": {"false"}, "batch:EKSRunAsUser": {"1000"}, "batch:EKSRunAsGroup": {"2000"},
		},
		{"batch:EKSImage": {"init:1"}, "batch:EKSServiceAccountName": {"runner"}, "batch:EKSNamespace": {"jobs"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contexts = %v, want %v", got, want)
	}
}

func TestBatchJobDefinitionWithoutContainersIsAuthorizedOnce(t *testing.T) {
	got := batchJobDefinitionContexts(t, `{"jobDefinitionName": "jd", "type": "container"}`)
	if len(got) != 1 || got[0] != nil {
		t.Fatalf("contexts = %v, want one with no container keys", got)
	}
}
