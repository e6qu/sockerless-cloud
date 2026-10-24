package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ssmCLIEnsureDoc creates a Command document for the association / automation /
// command CLI flows and registers tolerant cleanup.
func ssmCLIEnsureDoc(t *testing.T, name string) {
	t.Helper()
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`
	runCLI(t, awsCLI("ssm", "create-document",
		"--name", name,
		"--content", content,
		"--document-type", "Command",
		"--document-format", "JSON",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-document", "--name", name).Run()
	})
}

// TestSSMCLI_AssociationLifecycle pins the State Manager association control
// plane through the aws CLI: create, describe (settled Success), list, version
// via update, list-versions, execution history, targets, effective-instance,
// status, run-once, delete.
func TestSSMCLI_AssociationLifecycle(t *testing.T) {
	t.Parallel()
	docName := "cli-assoc-doc-" + ssmStamp()
	ssmCLIEnsureDoc(t, docName)
	instanceID := "i-0123456789abcdef0"

	createOut := runCLI(t, awsCLI("ssm", "create-association",
		"--name", docName,
		"--association-name", "cli-assoc",
		"--targets", "Key=InstanceIds,Values="+instanceID,
		"--schedule-expression", "rate(30 minutes)",
		"--output", "json"))
	var createResult struct {
		AssociationDescription struct {
			AssociationId      string `json:"AssociationId"`
			AssociationVersion string `json:"AssociationVersion"`
		} `json:"AssociationDescription"`
	}
	parseJSON(t, createOut, &createResult)
	assocID := createResult.AssociationDescription.AssociationId
	require.NotEmpty(t, assocID)
	require.Equal(t, "1", createResult.AssociationDescription.AssociationVersion)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-association", "--association-id", assocID).Run()
	})

	descOut := runCLI(t, awsCLI("ssm", "describe-association",
		"--association-id", assocID, "--output", "json"))
	var descResult struct {
		AssociationDescription struct {
			Status struct {
				Name string `json:"Name"`
			} `json:"Status"`
		} `json:"AssociationDescription"`
	}
	parseJSON(t, descOut, &descResult)
	require.Equal(t, "Success", descResult.AssociationDescription.Status.Name)

	listOut := runCLI(t, awsCLI("ssm", "list-associations", "--output", "json"))
	var listResult struct {
		Associations []struct {
			AssociationId string `json:"AssociationId"`
		} `json:"Associations"`
	}
	parseJSON(t, listOut, &listResult)
	found := false
	for _, a := range listResult.Associations {
		if a.AssociationId == assocID {
			found = true
		}
	}
	require.True(t, found, "list-associations must include the created association")

	updOut := runCLI(t, awsCLI("ssm", "update-association",
		"--association-id", assocID,
		"--schedule-expression", "rate(60 minutes)",
		"--output", "json"))
	var updResult struct {
		AssociationDescription struct {
			AssociationVersion string `json:"AssociationVersion"`
		} `json:"AssociationDescription"`
	}
	parseJSON(t, updOut, &updResult)
	require.Equal(t, "2", updResult.AssociationDescription.AssociationVersion)

	lvOut := runCLI(t, awsCLI("ssm", "list-association-versions",
		"--association-id", assocID, "--output", "json"))
	var lvResult struct {
		AssociationVersions []struct {
			AssociationVersion string `json:"AssociationVersion"`
		} `json:"AssociationVersions"`
	}
	parseJSON(t, lvOut, &lvResult)
	require.Len(t, lvResult.AssociationVersions, 2)

	runCLI(t, awsCLI("ssm", "start-associations-once",
		"--association-ids", assocID, "--output", "json"))

	execOut := runCLI(t, awsCLI("ssm", "describe-association-executions",
		"--association-id", assocID, "--output", "json"))
	var execResult struct {
		AssociationExecutions []struct {
			ExecutionId string `json:"ExecutionId"`
		} `json:"AssociationExecutions"`
	}
	parseJSON(t, execOut, &execResult)
	require.NotEmpty(t, execResult.AssociationExecutions)
	execID := execResult.AssociationExecutions[0].ExecutionId

	tgtOut := runCLI(t, awsCLI("ssm", "describe-association-execution-targets",
		"--association-id", assocID, "--execution-id", execID, "--output", "json"))
	var tgtResult struct {
		AssociationExecutionTargets []struct {
			ResourceId string `json:"ResourceId"`
		} `json:"AssociationExecutionTargets"`
	}
	parseJSON(t, tgtOut, &tgtResult)
	require.NotEmpty(t, tgtResult.AssociationExecutionTargets)
	require.Equal(t, instanceID, tgtResult.AssociationExecutionTargets[0].ResourceId)

	effOut := runCLI(t, awsCLI("ssm", "describe-effective-instance-associations",
		"--instance-id", instanceID, "--output", "json"))
	var effResult struct {
		Associations []struct {
			AssociationId string `json:"AssociationId"`
		} `json:"Associations"`
	}
	parseJSON(t, effOut, &effResult)
	require.NotEmpty(t, effResult.Associations)

	statusOut := runCLI(t, awsCLI("ssm", "describe-instance-associations-status",
		"--instance-id", instanceID, "--output", "json"))
	var statusResult struct {
		InstanceAssociationStatusInfos []struct {
			AssociationId string `json:"AssociationId"`
		} `json:"InstanceAssociationStatusInfos"`
	}
	parseJSON(t, statusOut, &statusResult)
	require.NotEmpty(t, statusResult.InstanceAssociationStatusInfos)
}

func TestSSMCLI_AssociationBatch(t *testing.T) {
	t.Parallel()
	docName := "cli-assoc-batch-doc-" + ssmStamp()
	ssmCLIEnsureDoc(t, docName)

	entries := `[{"Name":"` + docName + `","Targets":[{"Key":"InstanceIds","Values":["i-aaaa1111bbbb2222"]}]},{"Name":"nonexistent-document-xyz"}]`
	out := runCLI(t, awsCLI("ssm", "create-association-batch",
		"--entries", entries, "--output", "json"))
	var result struct {
		Successful []struct {
			AssociationId string `json:"AssociationId"`
		} `json:"Successful"`
		Failed []struct {
			Message string `json:"Message"`
		} `json:"Failed"`
	}
	parseJSON(t, out, &result)
	require.Len(t, result.Successful, 1)
	require.Len(t, result.Failed, 1)
	t.Cleanup(func() {
		for _, a := range result.Successful {
			_ = awsCLI("ssm", "delete-association", "--association-id", a.AssociationId).Run()
		}
	})
}

// TestSSMCLI_AutomationLifecycle pins the Automation control plane through the
// CLI plus the execution-preview surface.
func TestSSMCLI_AutomationLifecycle(t *testing.T) {
	t.Parallel()
	docName := "cli-automation-doc-" + ssmStamp()
	ssmCLIEnsureDoc(t, docName)

	startOut := runCLI(t, awsCLI("ssm", "start-automation-execution",
		"--document-name", docName, "--output", "json"))
	var startResult struct {
		AutomationExecutionId string `json:"AutomationExecutionId"`
	}
	parseJSON(t, startOut, &startResult)
	execID := startResult.AutomationExecutionId
	require.NotEmpty(t, execID)

	getOut := runCLI(t, awsCLI("ssm", "get-automation-execution",
		"--automation-execution-id", execID, "--output", "json"))
	var getResult struct {
		AutomationExecution struct {
			AutomationExecutionStatus string `json:"AutomationExecutionStatus"`
			StepExecutions            []struct {
				StepStatus string `json:"StepStatus"`
			} `json:"StepExecutions"`
		} `json:"AutomationExecution"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, "Success", getResult.AutomationExecution.AutomationExecutionStatus)
	require.NotEmpty(t, getResult.AutomationExecution.StepExecutions)

	descOut := runCLI(t, awsCLI("ssm", "describe-automation-executions", "--output", "json"))
	var descResult struct {
		AutomationExecutionMetadataList []struct {
			AutomationExecutionId string `json:"AutomationExecutionId"`
		} `json:"AutomationExecutionMetadataList"`
	}
	parseJSON(t, descOut, &descResult)
	require.NotEmpty(t, descResult.AutomationExecutionMetadataList)

	stepOut := runCLI(t, awsCLI("ssm", "describe-automation-step-executions",
		"--automation-execution-id", execID, "--output", "json"))
	var stepResult struct {
		StepExecutions []struct {
			StepName string `json:"StepName"`
		} `json:"StepExecutions"`
	}
	parseJSON(t, stepOut, &stepResult)
	require.NotEmpty(t, stepResult.StepExecutions)

	runCLI(t, awsCLI("ssm", "send-automation-signal",
		"--automation-execution-id", execID,
		"--signal-type", "Resume", "--output", "json"))

	runCLI(t, awsCLI("ssm", "stop-automation-execution",
		"--automation-execution-id", execID, "--output", "json"))

	// Execution preview.
	previewOut := runCLI(t, awsCLI("ssm", "start-execution-preview",
		"--document-name", docName, "--output", "json"))
	var previewResult struct {
		ExecutionPreviewId string `json:"ExecutionPreviewId"`
	}
	parseJSON(t, previewOut, &previewResult)
	require.NotEmpty(t, previewResult.ExecutionPreviewId)

	getPreviewOut := runCLI(t, awsCLI("ssm", "get-execution-preview",
		"--execution-preview-id", previewResult.ExecutionPreviewId, "--output", "json"))
	var getPreviewResult struct {
		Status string `json:"Status"`
	}
	parseJSON(t, getPreviewOut, &getPreviewResult)
	require.Equal(t, "Success", getPreviewResult.Status)
}

// TestSSMCLI_CommandLifecycle pins Run Command through the CLI: send to a
// managed instance, list, list-invocations, get-command-invocation with the
// command's output, cancel. A target that is no managed node fails the send.
func TestSSMCLI_CommandLifecycle(t *testing.T) {
	t.Parallel()
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }
	docName := "cli-command-doc-" + ssmStamp()
	ssmCLIEnsureDoc(t, docName)
	refused := runCLIExpectError(t, awsCLI("ssm", "send-command",
		"--document-name", docName, "--instance-ids", "i-cccc3333dddd44440"))
	require.Contains(t, refused, "InvalidInstanceId")

	vpcID := q("ec2", "create-vpc", "--cidr-block", "10.217.0.0/16", "--query", "Vpc.VpcId", "--output", "text")
	subnetID := q("ec2", "create-subnet", "--vpc-id", vpcID, "--cidr-block", "10.217.1.0/24",
		"--query", "Subnet.SubnetId", "--output", "text")
	// A running instance whose profile does not let SSM Agent register it is
	// no managed node: Run Command refuses it and describe-instance-information
	// does not list it.
	unmanaged := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, unmanaged)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", unmanaged))
	require.Contains(t, runCLIExpectError(t, awsCLI("ssm", "send-command",
		"--document-name", docName, "--instance-ids", unmanaged)), "InvalidInstanceId")
	require.Contains(t, runCLIExpectError(t, awsCLI("ssm", "describe-instance-information",
		"--filters", "Key=InstanceIds,Values="+unmanaged)), "InvalidInstanceId")

	instanceID := q("ec2", "run-instances", "--image-id", ec2AmazonLinuxAMI(t), "--instance-type", "t3.micro",
		"--subnet-id", subnetID, "--iam-instance-profile", "Name="+ssmManagedInstanceProfileCLI(t),
		"--query", "Instances[0].InstanceId", "--output", "text")
	terminateCLIInstanceOnCleanup(t, instanceID)
	runCLI(t, awsCLI("ec2", "wait", "instance-running", "--instance-ids", instanceID))
	privateIP := q("ec2", "describe-instances", "--instance-ids", instanceID,
		"--query", "Reservations[0].Instances[0].PrivateIpAddress", "--output", "text")
	node := q("ssm", "describe-instance-information", "--filters", "Key=InstanceIds,Values="+instanceID,
		"--query", "InstanceInformationList[0].[PingStatus,ResourceType,PlatformType,PlatformName,PlatformVersion,IPAddress]", "--output", "text")
	require.Equal(t, strings.Join([]string{"Online", "EC2Instance", "Linux", "Amazon Linux", "2023", privateIP}, "\t"), node,
		"SSM Agent registers the instance with the platform the AMI runs")

	sendOut := runCLI(t, awsCLI("ssm", "send-command",
		"--document-name", docName,
		"--instance-ids", instanceID,
		"--comment", "cli run",
		"--output", "json"))
	var sendResult struct {
		Command struct {
			CommandId string `json:"CommandId"`
			Status    string `json:"Status"`
		} `json:"Command"`
	}
	parseJSON(t, sendOut, &sendResult)
	cmdID := sendResult.Command.CommandId
	require.NotEmpty(t, cmdID)
	require.Equal(t, "Pending", sendResult.Command.Status)
	runCLI(t, awsCLI("ssm", "wait", "command-executed", "--command-id", cmdID, "--instance-id", instanceID))

	listOut := runCLI(t, awsCLI("ssm", "list-commands",
		"--command-id", cmdID, "--output", "json"))
	var listResult struct {
		Commands []struct {
			CommandId string `json:"CommandId"`
		} `json:"Commands"`
	}
	parseJSON(t, listOut, &listResult)
	require.Len(t, listResult.Commands, 1)

	invOut := runCLI(t, awsCLI("ssm", "list-command-invocations",
		"--command-id", cmdID, "--details", "--output", "json"))
	var invResult struct {
		CommandInvocations []struct {
			InstanceId string `json:"InstanceId"`
		} `json:"CommandInvocations"`
	}
	parseJSON(t, invOut, &invResult)
	require.Len(t, invResult.CommandInvocations, 1)
	require.Equal(t, instanceID, invResult.CommandInvocations[0].InstanceId)

	getInvOut := runCLI(t, awsCLI("ssm", "get-command-invocation",
		"--command-id", cmdID, "--instance-id", instanceID, "--output", "json"))
	var getInvResult struct {
		Status       string `json:"Status"`
		ResponseCode int    `json:"ResponseCode"`
		Output       string `json:"StandardOutputContent"`
	}
	parseJSON(t, getInvOut, &getInvResult)
	require.Equal(t, "Success", getInvResult.Status)
	require.Equal(t, 0, getInvResult.ResponseCode)
	require.Equal(t, "hi\n", getInvResult.Output, "the document's echo ran on the instance")

	runCLI(t, awsCLI("ssm", "cancel-command",
		"--command-id", cmdID, "--output", "json"))
}
