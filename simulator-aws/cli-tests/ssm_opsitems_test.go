package aws_cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmAWSJSON posts an awsJson1.1 request to Systems Manager directly. The CLI's
// start-session and resume-session hand the WebSocket data plane to the
// session-manager-plugin, so the test starts the session as an awsJson client
// would and the CLI covers the metadata reads; the SDK test covers
// StartSession and ResumeSession.
func ssmAWSJSON(t *testing.T, action string, in, out any) {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AmazonSSM."+action)
	signRawSigV4(t, req, "ssm", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "SSM %s", action)
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
}

func TestSSMCLI_OpsItemLifecycle(t *testing.T) {
	t.Parallel()
	createOut := runCLI(t, awsCLI("ssm", "create-ops-item",
		"--title", "cli disk full",
		"--description", "root volume at 95%",
		"--source", "EC2",
		"--priority", "2",
		"--output", "json"))
	var created struct {
		OpsItemId  string `json:"OpsItemId"`
		OpsItemArn string `json:"OpsItemArn"`
	}
	parseJSON(t, createOut, &created)
	require.Regexp(t, `^oi-[0-9a-f]{12}$`, created.OpsItemId)
	require.NotEmpty(t, created.OpsItemArn)
	id := created.OpsItemId

	getOut := runCLI(t, awsCLI("ssm", "get-ops-item", "--ops-item-id", id, "--output", "json"))
	var got struct {
		OpsItem struct {
			Title   string `json:"Title"`
			Status  string `json:"Status"`
			Version string `json:"Version"`
		} `json:"OpsItem"`
	}
	parseJSON(t, getOut, &got)
	require.Equal(t, "cli disk full", got.OpsItem.Title)
	require.Equal(t, "Open", got.OpsItem.Status)

	runCLI(t, awsCLI("ssm", "update-ops-item",
		"--ops-item-id", id, "--status", "Resolved", "--output", "json"))
	getOut2 := runCLI(t, awsCLI("ssm", "get-ops-item", "--ops-item-id", id, "--output", "json"))
	parseJSON(t, getOut2, &got)
	require.Equal(t, "Resolved", got.OpsItem.Status)

	descOut := runCLI(t, awsCLI("ssm", "describe-ops-items", "--output", "json"))
	var desc struct {
		OpsItemSummaries []struct {
			OpsItemId string `json:"OpsItemId"`
		} `json:"OpsItemSummaries"`
	}
	parseJSON(t, descOut, &desc)
	found := false
	for _, s := range desc.OpsItemSummaries {
		if s.OpsItemId == id {
			found = true
		}
	}
	require.True(t, found)

	assocOut := runCLI(t, awsCLI("ssm", "associate-ops-item-related-item",
		"--ops-item-id", id,
		"--association-type", "RelatesTo",
		"--resource-type", "AWS::SSMIncidents::IncidentRecord",
		"--resource-uri", "arn:aws:ssm-incidents::123456789012:incident-record/x/y",
		"--output", "json"))
	var assoc struct {
		AssociationId string `json:"AssociationId"`
	}
	parseJSON(t, assocOut, &assoc)
	require.NotEmpty(t, assoc.AssociationId)

	relOut := runCLI(t, awsCLI("ssm", "list-ops-item-related-items", "--ops-item-id", id, "--output", "json"))
	var rel struct {
		Summaries []struct {
			AssociationId string `json:"AssociationId"`
		} `json:"Summaries"`
	}
	parseJSON(t, relOut, &rel)
	require.Len(t, rel.Summaries, 1)

	evOut := runCLI(t, awsCLI("ssm", "list-ops-item-events",
		"--filters", "Key=OpsItemId,Values="+id+",Operator=Equal",
		"--output", "json"))
	var evs struct {
		Summaries []struct {
			OpsItemId string `json:"OpsItemId"`
		} `json:"Summaries"`
	}
	parseJSON(t, evOut, &evs)
	require.GreaterOrEqual(t, len(evs.Summaries), 2)

	runCLI(t, awsCLI("ssm", "disassociate-ops-item-related-item",
		"--ops-item-id", id, "--association-id", assoc.AssociationId, "--output", "json"))
	relOut2 := runCLI(t, awsCLI("ssm", "list-ops-item-related-items", "--ops-item-id", id, "--output", "json"))
	parseJSON(t, relOut2, &rel)
	require.Empty(t, rel.Summaries)
}

// TestSSMCLI_MaintenanceWindowExecution schedules a window a few seconds
// ahead with a Run Command task on a managed node, waits for the window to
// run, and reads the execution, task and invocation, whose command wrote its
// output on the node.
func TestSSMCLI_MaintenanceWindowExecution(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.243.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.243.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	instanceID := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--iam-instance-profile", "Name="+ssmManagedInstanceProfileCLI(t),
		"--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instanceID)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))

	role := cliRoleAllowing(t, "ssm.amazonaws.com", "ssm:SendCommand", "ssm:CancelCommand")
	// Each aws CLI call starts a Python process, so the run lies far enough
	// ahead for the reads of the schedule before it.
	runAt := time.Now().UTC().Add(45 * time.Second).Truncate(time.Second)
	windowID := q("ssm", "create-maintenance-window",
		"--name", "cli-mwexec-"+ssmStamp(),
		"--schedule", "at("+runAt.Format("2006-01-02T15:04:05")+")",
		"--duration", "1", "--cutoff", "0",
		"--allow-unassociated-targets", "--query", "WindowId", "--output", "text")
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-maintenance-window", "--window-id", windowID).Run()
	})
	require.Equal(t, runAt.Format("2006-01-02T15:04:05Z"), q("ssm", "get-maintenance-window",
		"--window-id", windowID, "--query", "NextExecutionTime", "--output", "text"))

	targetID := q("ssm", "register-target-with-maintenance-window",
		"--window-id", windowID, "--resource-type", "INSTANCE",
		"--targets", "Key=InstanceIds,Values="+instanceID, "--owner-information", "cli-owner",
		"--query", "WindowTargetId", "--output", "text")
	windowTaskID := q("ssm", "register-task-with-maintenance-window",
		"--window-id", windowID, "--task-arn", "AWS-RunShellScript",
		"--task-type", "RUN_COMMAND", "--priority", "1",
		"--max-concurrency", "1", "--max-errors", "1", "--service-role-arn", role,
		"--targets", "Key=WindowTargetIds,Values="+targetID,
		"--task-invocation-parameters", `{"RunCommand":{"Parameters":{"commands":["echo cli window ran"]}}}`,
		"--query", "WindowTaskId", "--output", "text")
	require.Equal(t, "AWS-RunShellScript", q("ssm", "get-maintenance-window-task",
		"--window-id", windowID, "--window-task-id", windowTaskID, "--query", "TaskArn", "--output", "text"))

	execID, status := ssmAwaitWindowExecutionCLI(t, windowID)
	require.Equal(t, "SUCCESS", status)

	var ge struct {
		TaskIds []string `json:"TaskIds"`
		Status  string   `json:"Status"`
	}
	parseJSON(t, runCLI(t, awsCLI("ssm", "get-maintenance-window-execution",
		"--window-execution-id", execID, "--output", "json")), &ge)
	require.Len(t, ge.TaskIds, 1)
	taskExecID := ge.TaskIds[0]
	require.Equal(t, "SUCCESS", q("ssm", "get-maintenance-window-execution-task",
		"--window-execution-id", execID, "--task-id", taskExecID, "--query", "Status", "--output", "text"))
	require.Equal(t, "1", q("ssm", "describe-maintenance-window-execution-tasks",
		"--window-execution-id", execID, "--query", "length(WindowExecutionTaskIdentities)", "--output", "text"))

	var dei struct {
		WindowExecutionTaskInvocationIdentities []struct {
			InvocationId     string `json:"InvocationId"`
			ExecutionId      string `json:"ExecutionId"`
			WindowTargetId   string `json:"WindowTargetId"`
			OwnerInformation string `json:"OwnerInformation"`
		} `json:"WindowExecutionTaskInvocationIdentities"`
	}
	parseJSON(t, runCLI(t, awsCLI("ssm", "describe-maintenance-window-execution-task-invocations",
		"--window-execution-id", execID, "--task-id", taskExecID, "--output", "json")), &dei)
	require.Len(t, dei.WindowExecutionTaskInvocationIdentities, 1)
	invocation := dei.WindowExecutionTaskInvocationIdentities[0]
	require.Equal(t, targetID, invocation.WindowTargetId)
	require.Equal(t, "cli-owner", invocation.OwnerInformation)
	require.Equal(t, "SUCCESS", q("ssm", "get-maintenance-window-execution-task-invocation",
		"--window-execution-id", execID, "--task-id", taskExecID, "--invocation-id", invocation.InvocationId,
		"--query", "Status", "--output", "text"))
	require.Equal(t, "cli window ran", q("ssm", "get-command-invocation",
		"--command-id", invocation.ExecutionId, "--instance-id", instanceID,
		"--query", "StandardOutputContent", "--output", "text"))

	require.Equal(t, "0", q("ssm", "describe-maintenance-window-schedule",
		"--window-id", windowID, "--query", "length(ScheduledWindowExecutions)", "--output", "text"),
		"an at() window runs once")
	require.Equal(t, windowID, q("ssm", "describe-maintenance-windows-for-target",
		"--resource-type", "INSTANCE", "--targets", "Key=InstanceIds,Values="+instanceID,
		"--query", "WindowIdentities[0].WindowId", "--output", "text"))

	require.Equal(t, "renamed-target", q("ssm", "update-maintenance-window-target",
		"--window-id", windowID, "--window-target-id", targetID,
		"--name", "renamed-target",
		"--targets", "Key=tag:Env,Values=staging", "--query", "Name", "--output", "text"))
	var utask struct {
		Priority       int    `json:"Priority"`
		MaxConcurrency string `json:"MaxConcurrency"`
	}
	parseJSON(t, runCLI(t, awsCLI("ssm", "update-maintenance-window-task",
		"--window-id", windowID, "--window-task-id", windowTaskID,
		"--priority", "5", "--max-concurrency", "2", "--output", "json")), &utask)
	require.Equal(t, 5, utask.Priority)
	require.Equal(t, "2", utask.MaxConcurrency)

	require.Equal(t, execID, q("ssm", "cancel-maintenance-window-execution",
		"--window-execution-id", execID, "--query", "WindowExecutionId", "--output", "text"))
}

// ssmAwaitWindowExecutionCLI polls describe-maintenance-window-executions,
// the only view Systems Manager gives of a window's runs, until the first run
// finished.
func ssmAwaitWindowExecutionCLI(t *testing.T, windowID string) (string, string) {
	t.Helper()
	var execID, status string
	require.Eventually(t, func() bool {
		var de struct {
			WindowExecutions []struct {
				WindowExecutionId string `json:"WindowExecutionId"`
				Status            string `json:"Status"`
			} `json:"WindowExecutions"`
		}
		parseJSON(t, runCLI(t, awsCLI("ssm", "describe-maintenance-window-executions",
			"--window-id", windowID, "--output", "json")), &de)
		if len(de.WindowExecutions) == 0 {
			return false
		}
		execID, status = de.WindowExecutions[0].WindowExecutionId, de.WindowExecutions[0].Status
		return status != "PENDING" && status != "IN_PROGRESS" && status != "CANCELLING"
	}, 2*time.Minute, time.Second, "window %s never finished a run", windowID)
	return execID, status
}

// TestSSMCLI_Session covers GetConnectionStatus, DescribeSessions and
// TerminateSession on a session ssmAWSJSON starts.
func TestSSMCLI_Session(t *testing.T) {
	t.Parallel()
	target := "i-cli" + ssmStamp()
	var start struct {
		SessionId  string `json:"SessionId"`
		TokenValue string `json:"TokenValue"`
		StreamUrl  string `json:"StreamUrl"`
	}
	ssmAWSJSON(t, "StartSession", map[string]any{"Target": target}, &start)
	require.NotEmpty(t, start.SessionId)
	require.NotEmpty(t, start.TokenValue)
	require.Contains(t, start.StreamUrl, start.SessionId)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "terminate-session", "--session-id", start.SessionId).Run()
	})

	csOut := runCLI(t, awsCLI("ssm", "get-connection-status", "--target", target, "--output", "json"))
	var cs struct {
		Target string `json:"Target"`
		Status string `json:"Status"`
	}
	parseJSON(t, csOut, &cs)
	require.Equal(t, "connected", cs.Status)
	require.Equal(t, target, cs.Target)

	dsOut := runCLI(t, awsCLI("ssm", "describe-sessions", "--state", "Active", "--output", "json"))
	var ds struct {
		Sessions []struct {
			SessionId string `json:"SessionId"`
			Status    string `json:"Status"`
		} `json:"Sessions"`
	}
	parseJSON(t, dsOut, &ds)
	foundActive := false
	for _, s := range ds.Sessions {
		if s.SessionId == start.SessionId {
			foundActive = true
			assert.Equal(t, "Connected", s.Status)
		}
	}
	require.True(t, foundActive)

	termOut := runCLI(t, awsCLI("ssm", "terminate-session", "--session-id", start.SessionId, "--output", "json"))
	var term struct {
		SessionId string `json:"SessionId"`
	}
	parseJSON(t, termOut, &term)
	require.Equal(t, start.SessionId, term.SessionId)

	histOut := runCLI(t, awsCLI("ssm", "describe-sessions", "--state", "History", "--output", "json"))
	parseJSON(t, histOut, &ds)
	foundHist := false
	for _, s := range ds.Sessions {
		if s.SessionId == start.SessionId {
			foundHist = true
			assert.Equal(t, "Terminated", s.Status)
		}
	}
	require.True(t, foundHist)
}

func TestSSMCLI_Activation(t *testing.T) {
	t.Parallel()
	createOut := runCLI(t, awsCLI("ssm", "create-activation",
		"--iam-role", "SSMServiceRole",
		"--description", "cli on-prem fleet",
		"--default-instance-name", "edge-node",
		"--registration-limit", "5", "--output", "json"))
	var created struct {
		ActivationId   string `json:"ActivationId"`
		ActivationCode string `json:"ActivationCode"`
	}
	parseJSON(t, createOut, &created)
	require.NotEmpty(t, created.ActivationId)
	require.NotEmpty(t, created.ActivationCode)
	actID := created.ActivationId
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-activation", "--activation-id", actID).Run()
	})

	daOut := runCLI(t, awsCLI("ssm", "describe-activations", "--output", "json"))
	var da struct {
		ActivationList []struct {
			ActivationId      string `json:"ActivationId"`
			IamRole           string `json:"IamRole"`
			RegistrationLimit int    `json:"RegistrationLimit"`
		} `json:"ActivationList"`
	}
	parseJSON(t, daOut, &da)
	found := false
	for _, a := range da.ActivationList {
		if a.ActivationId == actID {
			found = true
			assert.Equal(t, "SSMServiceRole", a.IamRole)
			assert.Equal(t, 5, a.RegistrationLimit)
		}
	}
	require.True(t, found)

	runCLI(t, awsCLI("ssm", "delete-activation", "--activation-id", actID, "--output", "json"))
	daOut2 := runCLI(t, awsCLI("ssm", "describe-activations", "--output", "json"))
	parseJSON(t, daOut2, &da)
	for _, a := range da.ActivationList {
		assert.NotEqual(t, actID, a.ActivationId)
	}
}
