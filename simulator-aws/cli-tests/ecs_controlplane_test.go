package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestECSCLI_CapacityProviders exercises create-capacity-provider,
// describe-capacity-providers, update-capacity-provider, delete-capacity-provider.
func TestECSCLI_CapacityProviders(t *testing.T) {
	name := "cli-cap-provider"
	asg := "arn:aws:autoscaling:us-east-1:000000000000:autoScalingGroup:uuid:autoScalingGroupName/asg"

	out := runCLI(t, awsCLI("ecs", "create-capacity-provider",
		"--name", name,
		"--auto-scaling-group-provider", "autoScalingGroupArn="+asg,
		"--output", "json"))
	var created struct {
		CapacityProvider struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"capacityProvider"`
	}
	parseJSON(t, out, &created)
	assert.Equal(t, name, created.CapacityProvider.Name)
	assert.Equal(t, "ACTIVE", created.CapacityProvider.Status)

	runCLI(t, awsCLI("ecs", "describe-capacity-providers", "--capacity-providers", name, "--output", "json"))
	runCLI(t, awsCLI("ecs", "update-capacity-provider",
		"--name", name,
		"--auto-scaling-group-provider", "managedTerminationProtection=ENABLED",
		"--output", "json"))
	runCLI(t, awsCLI("ecs", "delete-capacity-provider", "--capacity-provider", name, "--output", "json"))
}

// TestECSCLI_TaskSets exercises create-task-set, describe-task-sets,
// update-task-set, update-service-primary-task-set, delete-task-set.
func TestECSCLI_TaskSets(t *testing.T) {
	cluster := "cli-ts-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-ts-task",
		"--container-definitions", `[{"name":"app","image":"alpine:latest"}]`))
	runCLI(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", "cli-ts-svc",
		"--task-definition", "cli-ts-task", "--desired-count", "1",
		"--deployment-controller", "type=EXTERNAL"))

	out := runCLI(t, awsCLI("ecs", "create-task-set",
		"--cluster", cluster, "--service", "cli-ts-svc",
		"--task-definition", "cli-ts-task",
		"--scale", "value=50,unit=PERCENT",
		"--output", "json"))
	var created struct {
		TaskSet struct {
			Id string `json:"id"`
		} `json:"taskSet"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.TaskSet.Id)
	id := created.TaskSet.Id

	runCLI(t, awsCLI("ecs", "describe-task-sets",
		"--cluster", cluster, "--service", "cli-ts-svc", "--task-sets", id, "--output", "json"))
	runCLI(t, awsCLI("ecs", "update-task-set",
		"--cluster", cluster, "--service", "cli-ts-svc", "--task-set", id,
		"--scale", "value=100,unit=PERCENT", "--output", "json"))
	runCLI(t, awsCLI("ecs", "update-service-primary-task-set",
		"--cluster", cluster, "--service", "cli-ts-svc", "--primary-task-set", id, "--output", "json"))
	_ = awsCLI("ecs", "delete-task-set",
		"--cluster", cluster, "--service", "cli-ts-svc", "--task-set", id, "--force").Run()
}

// TestECSCLI_ContainerInstances exercises register-container-instance,
// describe-container-instances, list-container-instances,
// update-container-instances-state, update-container-agent,
// deregister-container-instance, and the agent-poll submit ops +
// discover-poll-endpoint.
func TestECSCLI_ContainerInstances(t *testing.T) {
	cluster := "cli-ci-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	out := runCLI(t, awsCLI("ecs", "register-container-instance",
		"--cluster", cluster,
		"--version-info", "agentVersion=1.0.0,dockerVersion=20.10",
		"--output", "json"))
	var reg struct {
		ContainerInstance struct {
			ContainerInstanceArn string `json:"containerInstanceArn"`
		} `json:"containerInstance"`
	}
	parseJSON(t, out, &reg)
	ciArn := reg.ContainerInstance.ContainerInstanceArn
	require.NotEmpty(t, ciArn)

	runCLI(t, awsCLI("ecs", "list-container-instances", "--cluster", cluster, "--output", "json"))
	runCLI(t, awsCLI("ecs", "describe-container-instances",
		"--cluster", cluster, "--container-instances", ciArn, "--output", "json"))
	runCLI(t, awsCLI("ecs", "update-container-instances-state",
		"--cluster", cluster, "--container-instances", ciArn, "--status", "DRAINING", "--output", "json"))
	runCLI(t, awsCLI("ecs", "update-container-agent",
		"--cluster", cluster, "--container-instance", ciArn, "--output", "json"))

	runCLI(t, awsCLI("ecs", "discover-poll-endpoint",
		"--cluster", cluster, "--container-instance", ciArn, "--output", "json"))
	runCLI(t, awsCLI("ecs", "submit-container-state-change",
		"--cluster", cluster, "--task", "task-1", "--container-name", "app", "--status", "RUNNING", "--output", "json"))
	runCLI(t, awsCLI("ecs", "submit-task-state-change",
		"--cluster", cluster, "--task", "task-1", "--status", "RUNNING", "--output", "json"))
	runCLI(t, awsCLI("ecs", "submit-attachment-state-changes",
		"--cluster", cluster, "--attachments", "attachmentArn=att-1,status=ATTACHED", "--output", "json"))

	runCLI(t, awsCLI("ecs", "deregister-container-instance",
		"--cluster", cluster, "--container-instance", ciArn, "--force", "--output", "json"))
}

// TestECSCLI_AccountSettings exercises put-account-setting,
// put-account-setting-default, list-account-settings, delete-account-setting.
func TestECSCLI_AccountSettings(t *testing.T) {
	runCLI(t, awsCLI("ecs", "put-account-setting",
		"--name", "serviceLongArnFormat", "--value", "enabled", "--output", "json"))
	runCLI(t, awsCLI("ecs", "put-account-setting-default",
		"--name", "taskLongArnFormat", "--value", "enabled", "--output", "json"))
	out := runCLI(t, awsCLI("ecs", "list-account-settings",
		"--name", "serviceLongArnFormat", "--output", "json"))
	var list struct {
		Settings []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"settings"`
	}
	parseJSON(t, out, &list)
	require.NotEmpty(t, list.Settings)
	_ = awsCLI("ecs", "delete-account-setting", "--name", "serviceLongArnFormat").Run()
}

// TestECSCLI_Attributes exercises put-attributes, list-attributes, delete-attributes.
func TestECSCLI_Attributes(t *testing.T) {
	cluster := "cli-attr-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })

	attr := "name=stack,value=prod,targetType=container-instance,targetId=ci-123"
	runCLI(t, awsCLI("ecs", "put-attributes", "--cluster", cluster, "--attributes", attr, "--output", "json"))
	out := runCLI(t, awsCLI("ecs", "list-attributes",
		"--cluster", cluster, "--target-type", "container-instance", "--attribute-name", "stack", "--output", "json"))
	var list struct {
		Attributes []struct {
			Value string `json:"value"`
		} `json:"attributes"`
	}
	parseJSON(t, out, &list)
	require.Len(t, list.Attributes, 1)
	assert.Equal(t, "prod", list.Attributes[0].Value)
	_ = awsCLI("ecs", "delete-attributes", "--cluster", cluster, "--attributes", attr).Run()
}

// TestECSCLI_TaskProtection exercises update-task-protection and get-task-protection.
func TestECSCLI_TaskProtection(t *testing.T) {
	cluster := "cli-prot-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-prot-task",
		"--container-definitions", `[{"name":"app","image":"alpine:latest"}]`))

	out := runCLI(t, awsCLI("ecs", "run-task",
		"--cluster", cluster, "--task-definition", "cli-prot-task", "--output", "json"))
	var run struct {
		Tasks []struct {
			TaskArn string `json:"taskArn"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &run)
	require.NotEmpty(t, run.Tasks)
	taskArn := run.Tasks[0].TaskArn
	cleanupCLIECSTask(t, cluster, taskArn)

	runCLI(t, awsCLI("ecs", "update-task-protection",
		"--cluster", cluster, "--tasks", taskArn, "--protection-enabled", "--expires-in-minutes", "60", "--output", "json"))
	getOut := runCLI(t, awsCLI("ecs", "get-task-protection",
		"--cluster", cluster, "--tasks", taskArn, "--output", "json"))
	var got struct {
		ProtectedTasks []struct {
			ProtectionEnabled bool `json:"protectionEnabled"`
		} `json:"protectedTasks"`
	}
	parseJSON(t, getOut, &got)
	require.Len(t, got.ProtectedTasks, 1)
	assert.True(t, got.ProtectedTasks[0].ProtectionEnabled)
}

// TestECSCLI_StartTask runs a task on a named registered container instance.
func TestECSCLI_StartTask(t *testing.T) {
	cluster := "cli-start-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-start-task",
		"--container-definitions", `[{"name":"app","image":"alpine:latest","privileged":true,"command":["sh","-c","mkdir -p /tmp/start-task-mount && mount -t tmpfs tmpfs /tmp/start-task-mount && umount /tmp/start-task-mount"]}]`))
	regOut := runCLI(t, awsCLI("ecs", "register-container-instance", "--cluster", cluster, "--output", "json"))
	var reg struct {
		ContainerInstance struct {
			ContainerInstanceArn string `json:"containerInstanceArn"`
		} `json:"containerInstance"`
	}
	parseJSON(t, regOut, &reg)
	ciArn := reg.ContainerInstance.ContainerInstanceArn

	out := runCLI(t, awsCLI("ecs", "start-task",
		"--cluster", cluster, "--container-instances", ciArn,
		"--task-definition", "cli-start-task", "--output", "json"))
	var start struct {
		Tasks []struct {
			TaskArn    string `json:"taskArn"`
			LastStatus string `json:"lastStatus"`
		} `json:"tasks"`
	}
	parseJSON(t, out, &start)
	require.Len(t, start.Tasks, 1)
	assert.Equal(t, "PROVISIONING", start.Tasks[0].LastStatus)
	waitCLITaskStatus(t, cluster, start.Tasks[0].TaskArn, "STOPPED")
	exitCode := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-tasks",
		"--cluster", cluster,
		"--tasks", start.Tasks[0].TaskArn,
		"--query", "tasks[0].containers[0].exitCode",
		"--output", "text")))
	assert.Equal(t, "0", exitCode)
}

// TestECSCLI_DeleteTaskDefinitions deletes an INACTIVE revision.
func TestECSCLI_DeleteTaskDefinitions(t *testing.T) {
	out := runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-del-task",
		"--container-definitions", `[{"name":"app","image":"alpine:latest"}]`,
		"--output", "json"))
	var reg struct {
		TaskDefinition struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	parseJSON(t, out, &reg)
	arn := reg.TaskDefinition.TaskDefinitionArn

	runCLI(t, awsCLI("ecs", "deregister-task-definition", "--task-definition", arn))
	delOut := runCLI(t, awsCLI("ecs", "delete-task-definitions", "--task-definitions", arn, "--output", "json"))
	var del struct {
		TaskDefinitions []struct {
			TaskDefinitionArn string `json:"taskDefinitionArn"`
		} `json:"taskDefinitions"`
	}
	parseJSON(t, delOut, &del)
	require.Len(t, del.TaskDefinitions, 1)
}

// TestECSCLI_ServiceDeployments exercises list-service-deployments,
// describe-service-deployments, describe-service-revisions, list-services-by-namespace.
// (stop/continue-service-deployment have no public CLI surface — SDK-tested only.)
func TestECSCLI_ServiceDeployments(t *testing.T) {
	cluster := "cli-sd-cluster"
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", cluster))
	t.Cleanup(func() { _ = awsCLI("ecs", "delete-cluster", "--cluster", cluster).Run() })
	runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-sd-task",
		"--container-definitions", `[{"name":"app","image":"`+containerCommandImage+`","command":["hold"]}]`))

	namespace := "arn:aws:servicediscovery:us-east-1:000000000000:namespace/ns-cli-sd"
	runCLI(t, awsCLI("ecs", "create-service",
		"--cluster", cluster, "--service-name", "cli-sd-svc",
		"--task-definition", "cli-sd-task", "--desired-count", "1",
		"--service-connect-configuration", `{"enabled":true,"namespace":"`+namespace+`"}`))
	cleanupCLIService(t, cluster, "cli-sd-svc")

	listOut := runCLI(t, awsCLI("ecs", "list-service-deployments",
		"--cluster", cluster, "--service", "cli-sd-svc", "--output", "json"))
	var list struct {
		ServiceDeployments []struct {
			ServiceDeploymentArn     string `json:"serviceDeploymentArn"`
			TargetServiceRevisionArn string `json:"targetServiceRevisionArn"`
			ServiceArn               string `json:"serviceArn"`
		} `json:"serviceDeployments"`
	}
	parseJSON(t, listOut, &list)
	require.NotEmpty(t, list.ServiceDeployments)
	depArn := list.ServiceDeployments[0].ServiceDeploymentArn
	revArn := list.ServiceDeployments[0].TargetServiceRevisionArn

	runCLI(t, awsCLI("ecs", "describe-service-deployments", "--service-deployment-arns", depArn, "--output", "json"))
	runCLI(t, awsCLI("ecs", "describe-service-revisions", "--service-revision-arns", revArn, "--output", "json"))

	nsOut := runCLI(t, awsCLI("ecs", "list-services-by-namespace", "--namespace", namespace, "--output", "json"))
	var ns struct {
		ServiceArns []string `json:"serviceArns"`
	}
	parseJSON(t, nsOut, &ns)
	require.Contains(t, ns.ServiceArns, list.ServiceDeployments[0].ServiceArn)
}
