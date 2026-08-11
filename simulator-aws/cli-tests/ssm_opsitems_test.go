package aws_cli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmAWSJSON posts an awsJson1.1 request to the SSM router directly. The
// Session Manager StartSession/ResumeSession CLI commands shell out to the
// session-manager-plugin (the WebSocket data plane, out of scope), so a
// session is created here exactly as a faithful awsJson client would, then the
// CLI exercises the metadata-only DescribeSessions/TerminateSession/
// GetConnectionStatus reads. StartSession/ResumeSession's contract hook is
// satisfied by the SDK test.
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

// TestSSMCLI_OpsItemLifecycle pins the OpsCenter slice through the aws CLI.
func TestSSMCLI_OpsItemLifecycle(t *testing.T) {
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

// TestSSMCLI_MaintenanceWindowExecution pins the execution-side reads + updates.
func TestSSMCLI_MaintenanceWindowExecution(t *testing.T) {
	cwOut := runCLI(t, awsCLI("ssm", "create-maintenance-window",
		"--name", "cli-mwexec-"+ssmStamp(),
		"--schedule", "cron(0 16 ? * TUE *)",
		"--duration", "4", "--cutoff", "1",
		"--allow-unassociated-targets", "--output", "json"))
	var cw struct {
		WindowId string `json:"WindowId"`
	}
	parseJSON(t, cwOut, &cw)
	windowID := cw.WindowId
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-maintenance-window", "--window-id", windowID).Run()
	})

	rtOut := runCLI(t, awsCLI("ssm", "register-target-with-maintenance-window",
		"--window-id", windowID, "--resource-type", "INSTANCE",
		"--targets", "Key=tag:Env,Values=prod", "--output", "json"))
	var rt struct {
		WindowTargetId string `json:"WindowTargetId"`
	}
	parseJSON(t, rtOut, &rt)
	targetID := rt.WindowTargetId

	rtaskOut := runCLI(t, awsCLI("ssm", "register-task-with-maintenance-window",
		"--window-id", windowID, "--task-arn", "AWS-RunShellScript",
		"--task-type", "RUN_COMMAND", "--priority", "1",
		"--max-concurrency", "1", "--max-errors", "1",
		"--targets", "Key=WindowTargetIds,Values="+targetID, "--output", "json"))
	var rtask struct {
		WindowTaskId string `json:"WindowTaskId"`
	}
	parseJSON(t, rtaskOut, &rtask)
	windowTaskID := rtask.WindowTaskId

	gtOut := runCLI(t, awsCLI("ssm", "get-maintenance-window-task",
		"--window-id", windowID, "--window-task-id", windowTaskID, "--output", "json"))
	var gt struct {
		TaskArn string `json:"TaskArn"`
	}
	parseJSON(t, gtOut, &gt)
	require.Equal(t, "AWS-RunShellScript", gt.TaskArn)

	deOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-executions",
		"--window-id", windowID, "--output", "json"))
	var de struct {
		WindowExecutions []struct {
			WindowExecutionId string `json:"WindowExecutionId"`
			Status            string `json:"Status"`
		} `json:"WindowExecutions"`
	}
	parseJSON(t, deOut, &de)
	require.Len(t, de.WindowExecutions, 1)
	execID := de.WindowExecutions[0].WindowExecutionId
	require.Equal(t, "SUCCESS", de.WindowExecutions[0].Status)

	geOut := runCLI(t, awsCLI("ssm", "get-maintenance-window-execution",
		"--window-execution-id", execID, "--output", "json"))
	var ge struct {
		TaskIds []string `json:"TaskIds"`
		Status  string   `json:"Status"`
	}
	parseJSON(t, geOut, &ge)
	require.Len(t, ge.TaskIds, 1)
	taskExecID := ge.TaskIds[0]

	detOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-execution-tasks",
		"--window-execution-id", execID, "--output", "json"))
	var det struct {
		WindowExecutionTaskIdentities []struct {
			TaskArn string `json:"TaskArn"`
		} `json:"WindowExecutionTaskIdentities"`
	}
	parseJSON(t, detOut, &det)
	require.Len(t, det.WindowExecutionTaskIdentities, 1)

	getOut := runCLI(t, awsCLI("ssm", "get-maintenance-window-execution-task",
		"--window-execution-id", execID, "--task-id", taskExecID, "--output", "json"))
	var get struct {
		TaskArn string `json:"TaskArn"`
		Status  string `json:"Status"`
	}
	parseJSON(t, getOut, &get)
	require.Equal(t, "AWS-RunShellScript", get.TaskArn)

	deiOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-execution-task-invocations",
		"--window-execution-id", execID, "--task-id", taskExecID, "--output", "json"))
	var dei struct {
		WindowExecutionTaskInvocationIdentities []struct {
			InvocationId string `json:"InvocationId"`
		} `json:"WindowExecutionTaskInvocationIdentities"`
	}
	parseJSON(t, deiOut, &dei)
	require.Len(t, dei.WindowExecutionTaskInvocationIdentities, 1)
	invID := dei.WindowExecutionTaskInvocationIdentities[0].InvocationId

	geiOut := runCLI(t, awsCLI("ssm", "get-maintenance-window-execution-task-invocation",
		"--window-execution-id", execID, "--task-id", taskExecID, "--invocation-id", invID, "--output", "json"))
	var gei struct {
		Status string `json:"Status"`
	}
	parseJSON(t, geiOut, &gei)
	require.Equal(t, "SUCCESS", gei.Status)

	dsOut := runCLI(t, awsCLI("ssm", "describe-maintenance-window-schedule",
		"--window-id", windowID, "--output", "json"))
	var ds struct {
		ScheduledWindowExecutions []struct {
			WindowId string `json:"WindowId"`
		} `json:"ScheduledWindowExecutions"`
	}
	parseJSON(t, dsOut, &ds)
	require.GreaterOrEqual(t, len(ds.ScheduledWindowExecutions), 1)

	dftOut := runCLI(t, awsCLI("ssm", "describe-maintenance-windows-for-target",
		"--resource-type", "INSTANCE",
		"--targets", "Key=tag:Env,Values=prod", "--output", "json"))
	var dft struct {
		WindowIdentities []struct {
			WindowId string `json:"WindowId"`
		} `json:"WindowIdentities"`
	}
	parseJSON(t, dftOut, &dft)
	foundWin := false
	for _, wi := range dft.WindowIdentities {
		if wi.WindowId == windowID {
			foundWin = true
		}
	}
	require.True(t, foundWin)

	utOut := runCLI(t, awsCLI("ssm", "update-maintenance-window-target",
		"--window-id", windowID, "--window-target-id", targetID,
		"--name", "renamed-target",
		"--targets", "Key=tag:Env,Values=staging", "--output", "json"))
	var ut struct {
		Name string `json:"Name"`
	}
	parseJSON(t, utOut, &ut)
	require.Equal(t, "renamed-target", ut.Name)

	utaskOut := runCLI(t, awsCLI("ssm", "update-maintenance-window-task",
		"--window-id", windowID, "--window-task-id", windowTaskID,
		"--priority", "5", "--max-concurrency", "2", "--output", "json"))
	var utask struct {
		Priority       int    `json:"Priority"`
		MaxConcurrency string `json:"MaxConcurrency"`
	}
	parseJSON(t, utaskOut, &utask)
	require.Equal(t, 5, utask.Priority)
	require.Equal(t, "2", utask.MaxConcurrency)

	cancelOut := runCLI(t, awsCLI("ssm", "cancel-maintenance-window-execution",
		"--window-execution-id", execID, "--output", "json"))
	var cancel struct {
		WindowExecutionId string `json:"WindowExecutionId"`
	}
	parseJSON(t, cancelOut, &cancel)
	require.Equal(t, execID, cancel.WindowExecutionId)
}

// TestSSMCLI_Session pins the metadata-only Session Manager reads. The session
// is created via a faithful awsJson StartSession call (the CLI start-session
// shells out to the session-manager-plugin for the WebSocket, out of scope);
// the CLI then exercises GetConnectionStatus / DescribeSessions / TerminateSession.
func TestSSMCLI_Session(t *testing.T) {
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

// TestSSMCLI_Activation pins the hybrid-activation slice.
func TestSSMCLI_Activation(t *testing.T) {
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
