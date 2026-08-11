package aws_cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatch_ComputeEnvironment_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-compute-environment",
		"--compute-environment-name", "batch-cli-ce",
		"--type", "MANAGED",
		"--state", "ENABLED",
		"--compute-resources", `{"type":"EC2","minvCpus":0,"maxvCpus":256,"subnets":["subnet-00000001"],"instanceRole":"arn:aws:iam::123456789012:instance-profile/ecsInstanceRole","instanceTypes":["m5.large"]}`,
	))
	var created struct {
		ComputeEnvironmentArn  string `json:"computeEnvironmentArn"`
		ComputeEnvironmentName string `json:"computeEnvironmentName"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.ComputeEnvironmentArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-compute-environment",
			"--compute-environment", "batch-cli-ce"))
	})

	out = runCLI(t, awsCLI("batch", "describe-compute-environments",
		"--compute-environments", "batch-cli-ce"))
	var described struct {
		ComputeEnvironments []struct {
			ComputeEnvironmentName string `json:"computeEnvironmentName"`
			State                  string `json:"state"`
			Status                 string `json:"status"`
		} `json:"computeEnvironments"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.ComputeEnvironments, 1)
	ce := described.ComputeEnvironments[0]
	assert.Equal(t, "batch-cli-ce", ce.ComputeEnvironmentName)
	assert.Equal(t, "ENABLED", ce.State)
	assert.Equal(t, "VALID", ce.Status)

	runCLI(t, awsCLI("batch", "update-compute-environment",
		"--compute-environment", "batch-cli-ce",
		"--state", "DISABLED"))
	out = runCLI(t, awsCLI("batch", "describe-compute-environments",
		"--compute-environments", "batch-cli-ce"))
	parseJSON(t, out, &described)
	assert.Equal(t, "DISABLED", described.ComputeEnvironments[0].State)
}

func TestBatch_JobQueue_CLI(t *testing.T) {
	runCLI(t, awsCLI("batch", "create-compute-environment",
		"--compute-environment-name", "batch-cli-ce-q",
		"--type", "MANAGED",
		"--state", "ENABLED",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-compute-environment",
			"--compute-environment", "batch-cli-ce-q"))
	})

	ceArn := "arn:aws:batch:us-east-1:123456789012:compute-environment/batch-cli-ce-q"
	out := runCLI(t, awsCLI("batch", "create-job-queue",
		"--job-queue-name", "batch-cli-jq",
		"--state", "ENABLED",
		"--priority", "10",
		"--compute-environment-order", `[{"order":1,"computeEnvironment":"`+ceArn+`"}]`,
	))
	var created struct {
		JobQueueArn  string `json:"jobQueueArn"`
		JobQueueName string `json:"jobQueueName"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.JobQueueArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-job-queue", "--job-queue", "batch-cli-jq"))
	})

	out = runCLI(t, awsCLI("batch", "describe-job-queues",
		"--job-queues", "batch-cli-jq"))
	var described struct {
		JobQueues []struct {
			JobQueueName string `json:"jobQueueName"`
			State        string `json:"state"`
		} `json:"jobQueues"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.JobQueues, 1)
	assert.Equal(t, "batch-cli-jq", described.JobQueues[0].JobQueueName)
	assert.Equal(t, "ENABLED", described.JobQueues[0].State)
}

func TestBatch_JobDefinition_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "register-job-definition",
		"--job-definition-name", "batch-cli-jd",
		"--type", "container",
		"--container-properties", `{"image":"public.ecr.aws/docker/library/alpine:3","vcpus":1,"memory":512}`,
	))
	var reg struct {
		JobDefinitionArn  string `json:"jobDefinitionArn"`
		JobDefinitionName string `json:"jobDefinitionName"`
		Revision          int    `json:"revision"`
	}
	parseJSON(t, out, &reg)
	require.NotEmpty(t, reg.JobDefinitionArn)
	assert.Equal(t, 1, reg.Revision)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "deregister-job-definition",
			"--job-definition", reg.JobDefinitionArn))
	})

	out = runCLI(t, awsCLI("batch", "describe-job-definitions",
		"--job-definition-name", "batch-cli-jd",
		"--status", "ACTIVE"))
	var described struct {
		JobDefinitions []struct {
			JobDefinitionName string `json:"jobDefinitionName"`
			Revision          int    `json:"revision"`
			Status            string `json:"status"`
		} `json:"jobDefinitions"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.JobDefinitions, 1)
	assert.Equal(t, "batch-cli-jd", described.JobDefinitions[0].JobDefinitionName)
	assert.Equal(t, "ACTIVE", described.JobDefinitions[0].Status)
	assert.Equal(t, 1, described.JobDefinitions[0].Revision)

	// --job-definitions <name:rev> filters to only that definition.
	out = runCLI(t, awsCLI("batch", "describe-job-definitions",
		"--job-definitions", "batch-cli-jd:1"))
	var filtered struct {
		JobDefinitions []struct {
			JobDefinitionName string `json:"jobDefinitionName"`
		} `json:"jobDefinitions"`
	}
	parseJSON(t, out, &filtered)
	require.Len(t, filtered.JobDefinitions, 1)
	assert.Equal(t, "batch-cli-jd", filtered.JobDefinitions[0].JobDefinitionName)
}

func TestBatch_SubmitJob_CLI(t *testing.T) {
	// Setup
	runCLI(t, awsCLI("batch", "create-compute-environment",
		"--compute-environment-name", "batch-cli-ce-job",
		"--type", "MANAGED",
		"--state", "ENABLED",
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-compute-environment",
			"--compute-environment", "batch-cli-ce-job"))
	})

	ceArn := "arn:aws:batch:us-east-1:123456789012:compute-environment/batch-cli-ce-job"
	runCLI(t, awsCLI("batch", "create-job-queue",
		"--job-queue-name", "batch-cli-jq-job",
		"--state", "ENABLED",
		"--priority", "10",
		"--compute-environment-order", `[{"order":1,"computeEnvironment":"`+ceArn+`"}]`,
	))
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-job-queue", "--job-queue", "batch-cli-jq-job"))
	})

	out := runCLI(t, awsCLI("batch", "register-job-definition",
		"--job-definition-name", "batch-cli-jd-job",
		"--type", "container",
		"--container-properties", `{"image":"`+containerCommandImage+`","command":["log","batch-cli-ready"],"vcpus":1,"memory":512}`,
	))
	var reg struct {
		JobDefinitionArn string `json:"jobDefinitionArn"`
	}
	parseJSON(t, out, &reg)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "deregister-job-definition",
			"--job-definition", reg.JobDefinitionArn))
	})

	out = runCLI(t, awsCLI("batch", "submit-job",
		"--job-name", "batch-cli-job",
		"--job-queue", "batch-cli-jq-job",
		"--job-definition", reg.JobDefinitionArn,
	))
	var submitted struct {
		JobID   string `json:"jobId"`
		JobName string `json:"jobName"`
	}
	parseJSON(t, out, &submitted)
	require.NotEmpty(t, submitted.JobID)
	assert.Equal(t, "batch-cli-job", submitted.JobName)

	var described struct {
		Jobs []struct {
			JobID  string `json:"jobId"`
			Status string `json:"status"`
		} `json:"jobs"`
	}
	require.Eventually(t, func() bool {
		out = runCLI(t, awsCLI("batch", "describe-jobs",
			"--jobs", submitted.JobID))
		parseJSON(t, out, &described)
		require.Len(t, described.Jobs, 1)
		return described.Jobs[0].Status == "SUCCEEDED"
	}, 10*time.Second, 100*time.Millisecond)
	assert.Equal(t, submitted.JobID, described.Jobs[0].JobID)
	assert.Equal(t, "SUCCEEDED", described.Jobs[0].Status)

	out = runCLI(t, awsCLI("batch", "list-jobs",
		"--job-queue", "batch-cli-jq-job"))
	var listed struct {
		JobSummaryList []struct {
			JobID string `json:"jobId"`
		} `json:"jobSummaryList"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, j := range listed.JobSummaryList {
		if j.JobID == submitted.JobID {
			found = true
		}
	}
	assert.True(t, found)
}

func TestBatch_SchedulingPolicy_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-scheduling-policy",
		"--name", "batch-cli-sp",
		"--fairshare-policy", `{"shareDecaySeconds":3600,"computeReservation":50,"shareDistribution":[{"shareIdentifier":"teamA","weightFactor":0.5}]}`,
		"--tags", "env=cli",
	))
	var created struct {
		Name string `json:"name"`
		Arn  string `json:"arn"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.Arn)
	require.Equal(t, "batch-cli-sp", created.Name)
	arn := created.Arn
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-scheduling-policy", "--arn", arn))
	})

	out = runCLI(t, awsCLI("batch", "describe-scheduling-policies", "--arns", arn))
	var described struct {
		SchedulingPolicies []struct {
			Name            string `json:"name"`
			Arn             string `json:"arn"`
			FairsharePolicy struct {
				ShareDecaySeconds  int `json:"shareDecaySeconds"`
				ComputeReservation int `json:"computeReservation"`
				ShareDistribution  []struct {
					ShareIdentifier string  `json:"shareIdentifier"`
					WeightFactor    float64 `json:"weightFactor"`
				} `json:"shareDistribution"`
			} `json:"fairsharePolicy"`
		} `json:"schedulingPolicies"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.SchedulingPolicies, 1)
	sp := described.SchedulingPolicies[0]
	assert.Equal(t, "batch-cli-sp", sp.Name)
	assert.Equal(t, 3600, sp.FairsharePolicy.ShareDecaySeconds)
	assert.Equal(t, 50, sp.FairsharePolicy.ComputeReservation)
	require.Len(t, sp.FairsharePolicy.ShareDistribution, 1)
	assert.Equal(t, "teamA", sp.FairsharePolicy.ShareDistribution[0].ShareIdentifier)

	out = runCLI(t, awsCLI("batch", "list-scheduling-policies"))
	var listed struct {
		SchedulingPolicies []struct {
			Arn string `json:"arn"`
		} `json:"schedulingPolicies"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, lp := range listed.SchedulingPolicies {
		if lp.Arn == arn {
			found = true
		}
	}
	assert.True(t, found, "created scheduling policy should appear in list")

	runCLI(t, awsCLI("batch", "update-scheduling-policy",
		"--arn", arn,
		"--fairshare-policy", `{"shareDecaySeconds":7200,"computeReservation":25}`,
	))
	out = runCLI(t, awsCLI("batch", "describe-scheduling-policies", "--arns", arn))
	parseJSON(t, out, &described)
	require.Len(t, described.SchedulingPolicies, 1)
	assert.Equal(t, 7200, described.SchedulingPolicies[0].FairsharePolicy.ShareDecaySeconds)

	runCLI(t, awsCLI("batch", "tag-resource", "--resource-arn", arn, "--tags", "team=platform"))
	out = runCLI(t, awsCLI("batch", "list-tags-for-resource", "--resource-arn", arn))
	var tagged struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &tagged)
	assert.Equal(t, "platform", tagged.Tags["team"])
	assert.Equal(t, "cli", tagged.Tags["env"])

	runCLI(t, awsCLI("batch", "untag-resource", "--resource-arn", arn, "--tag-keys", "team"))
	out = runCLI(t, awsCLI("batch", "list-tags-for-resource", "--resource-arn", arn))
	var afterUntag struct {
		Tags map[string]string `json:"tags"`
	}
	parseJSON(t, out, &afterUntag)
	_, has := afterUntag.Tags["team"]
	assert.False(t, has)
	assert.Equal(t, "cli", afterUntag.Tags["env"])

	runCLI(t, awsCLI("batch", "delete-scheduling-policy", "--arn", arn))
	out = runCLI(t, awsCLI("batch", "describe-scheduling-policies", "--arns", arn))
	parseJSON(t, out, &described)
	assert.Empty(t, described.SchedulingPolicies)
}

func TestBatch_ConsumableResource_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-consumable-resource",
		"--consumable-resource-name", "batch-cli-cr",
		"--resource-type", "REPLENISHABLE",
		"--total-quantity", "100",
	))
	var created struct {
		ConsumableResourceArn  string `json:"consumableResourceArn"`
		ConsumableResourceName string `json:"consumableResourceName"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.ConsumableResourceArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-consumable-resource", "--consumable-resource", "batch-cli-cr"))
	})

	out = runCLI(t, awsCLI("batch", "describe-consumable-resource", "--consumable-resource", "batch-cli-cr"))
	var described struct {
		TotalQuantity     int64 `json:"totalQuantity"`
		AvailableQuantity int64 `json:"availableQuantity"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, int64(100), described.TotalQuantity)
	assert.Equal(t, int64(100), described.AvailableQuantity)

	out = runCLI(t, awsCLI("batch", "update-consumable-resource",
		"--consumable-resource", "batch-cli-cr",
		"--operation", "ADD",
		"--quantity", "25"))
	var updated struct {
		TotalQuantity int64 `json:"totalQuantity"`
	}
	parseJSON(t, out, &updated)
	assert.Equal(t, int64(125), updated.TotalQuantity)

	out = runCLI(t, awsCLI("batch", "list-consumable-resources"))
	var listed struct {
		ConsumableResources []struct {
			ConsumableResourceName string `json:"consumableResourceName"`
		} `json:"consumableResources"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, cr := range listed.ConsumableResources {
		if cr.ConsumableResourceName == "batch-cli-cr" {
			found = true
		}
	}
	assert.True(t, found)

	out = runCLI(t, awsCLI("batch", "list-jobs-by-consumable-resource", "--consumable-resource", "batch-cli-cr"))
	var byRes struct {
		Jobs []any `json:"jobs"`
	}
	parseJSON(t, out, &byRes)
	assert.Empty(t, byRes.Jobs)
}

func TestBatch_ServiceEnvironment_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-service-environment",
		"--service-environment-name", "batch-cli-se",
		"--service-environment-type", "SAGEMAKER_TRAINING",
		"--state", "ENABLED",
		"--capacity-limits", `[{"capacityUnit":"NUM_INSTANCES","maxCapacity":10}]`,
	))
	var created struct {
		ServiceEnvironmentArn  string `json:"serviceEnvironmentArn"`
		ServiceEnvironmentName string `json:"serviceEnvironmentName"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.ServiceEnvironmentArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-service-environment", "--service-environment", "batch-cli-se"))
	})

	out = runCLI(t, awsCLI("batch", "describe-service-environments", "--service-environments", "batch-cli-se"))
	var described struct {
		ServiceEnvironments []struct {
			State          string `json:"state"`
			Status         string `json:"status"`
			CapacityLimits []struct {
				MaxCapacity int32 `json:"maxCapacity"`
			} `json:"capacityLimits"`
		} `json:"serviceEnvironments"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.ServiceEnvironments, 1)
	assert.Equal(t, "ENABLED", described.ServiceEnvironments[0].State)
	assert.Equal(t, "VALID", described.ServiceEnvironments[0].Status)
	require.Len(t, described.ServiceEnvironments[0].CapacityLimits, 1)
	assert.Equal(t, int32(10), described.ServiceEnvironments[0].CapacityLimits[0].MaxCapacity)

	runCLI(t, awsCLI("batch", "update-service-environment",
		"--service-environment", "batch-cli-se",
		"--state", "DISABLED"))
	out = runCLI(t, awsCLI("batch", "describe-service-environments", "--service-environments", "batch-cli-se"))
	parseJSON(t, out, &described)
	assert.Equal(t, "DISABLED", described.ServiceEnvironments[0].State)
}

func TestBatch_ServiceJob_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-job-queue",
		"--job-queue-name", "batch-cli-svc-jq",
		"--priority", "1",
		"--compute-environment-order", "[]"))
	var cq struct {
		JobQueueArn string `json:"jobQueueArn"`
	}
	parseJSON(t, out, &cq)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-job-queue", "--job-queue", "batch-cli-svc-jq"))
	})

	out = runCLI(t, awsCLI("batch", "submit-service-job",
		"--job-name", "batch-cli-svc-job",
		"--job-queue", cq.JobQueueArn,
		"--service-job-type", "SAGEMAKER_TRAINING",
		"--service-request-payload", `{"trainingJobName":"t1"}`,
	))
	var sub struct {
		JobID string `json:"jobId"`
	}
	parseJSON(t, out, &sub)
	require.NotEmpty(t, sub.JobID)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "terminate-service-job", "--job-id", sub.JobID, "--reason", "cleanup"))
	})

	out = runCLI(t, awsCLI("batch", "describe-service-job", "--job-id", sub.JobID))
	var desc struct {
		JobName        string `json:"jobName"`
		ServiceJobType string `json:"serviceJobType"`
	}
	parseJSON(t, out, &desc)
	assert.Equal(t, "batch-cli-svc-job", desc.JobName)
	assert.Equal(t, "SAGEMAKER_TRAINING", desc.ServiceJobType)

	runCLI(t, awsCLI("batch", "update-service-job", "--job-id", sub.JobID, "--scheduling-priority", "7"))

	out = runCLI(t, awsCLI("batch", "list-service-jobs", "--job-queue", cq.JobQueueArn))
	var listed struct {
		JobSummaryList []struct {
			JobID string `json:"jobId"`
		} `json:"jobSummaryList"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, s := range listed.JobSummaryList {
		if s.JobID == sub.JobID {
			found = true
		}
	}
	assert.True(t, found)
}

func TestBatch_QuotaShare_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-job-queue",
		"--job-queue-name", "batch-cli-qs-jq",
		"--priority", "1",
		"--compute-environment-order", "[]"))
	var cq struct {
		JobQueueArn string `json:"jobQueueArn"`
	}
	parseJSON(t, out, &cq)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-job-queue", "--job-queue", "batch-cli-qs-jq"))
	})

	out = runCLI(t, awsCLI("batch", "create-quota-share",
		"--quota-share-name", "batch-cli-qs",
		"--job-queue", cq.JobQueueArn,
		"--state", "ENABLED",
		"--capacity-limits", `[{"capacityUnit":"vCPU","maxCapacity":64}]`,
		"--resource-sharing-configuration", `{"strategy":"LEND_AND_BORROW","borrowLimit":10}`,
		"--preemption-configuration", `{"inSharePreemption":"ENABLED"}`,
	))
	var created struct {
		QuotaShareArn  string `json:"quotaShareArn"`
		QuotaShareName string `json:"quotaShareName"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.QuotaShareArn)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-quota-share", "--quota-share-arn", created.QuotaShareArn))
	})

	out = runCLI(t, awsCLI("batch", "describe-quota-share", "--quota-share-arn", created.QuotaShareArn))
	var described struct {
		State          string `json:"state"`
		Status         string `json:"status"`
		CapacityLimits []struct {
			MaxCapacity int32 `json:"maxCapacity"`
		} `json:"capacityLimits"`
	}
	parseJSON(t, out, &described)
	assert.Equal(t, "ENABLED", described.State)
	assert.Equal(t, "VALID", described.Status)
	require.Len(t, described.CapacityLimits, 1)
	assert.Equal(t, int32(64), described.CapacityLimits[0].MaxCapacity)

	runCLI(t, awsCLI("batch", "update-quota-share",
		"--quota-share-arn", created.QuotaShareArn,
		"--state", "DISABLED"))
	out = runCLI(t, awsCLI("batch", "describe-quota-share", "--quota-share-arn", created.QuotaShareArn))
	parseJSON(t, out, &described)
	assert.Equal(t, "DISABLED", described.State)

	out = runCLI(t, awsCLI("batch", "list-quota-shares", "--job-queue", cq.JobQueueArn))
	var listed struct {
		QuotaShares []struct {
			QuotaShareName string `json:"quotaShareName"`
		} `json:"quotaShares"`
	}
	parseJSON(t, out, &listed)
	found := false
	for _, qs := range listed.QuotaShares {
		if qs.QuotaShareName == "batch-cli-qs" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestBatch_JobQueueSnapshot_CLI(t *testing.T) {
	out := runCLI(t, awsCLI("batch", "create-job-queue",
		"--job-queue-name", "batch-cli-snap-jq",
		"--priority", "1",
		"--compute-environment-order", "[]"))
	var cq struct {
		JobQueueArn string `json:"jobQueueArn"`
	}
	parseJSON(t, out, &cq)
	t.Cleanup(func() {
		runCLI(t, awsCLI("batch", "delete-job-queue", "--job-queue", "batch-cli-snap-jq"))
	})

	out = runCLI(t, awsCLI("batch", "get-job-queue-snapshot", "--job-queue", cq.JobQueueArn))
	var snap struct {
		FrontOfQueue struct {
			Jobs []any `json:"jobs"`
		} `json:"frontOfQueue"`
	}
	parseJSON(t, out, &snap)
	assert.NotNil(t, snap.FrontOfQueue.Jobs)
}
