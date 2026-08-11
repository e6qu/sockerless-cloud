package aws_cli_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudMap_CreateAndListNamespaces(t *testing.T) {
	out := runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "cli-test.local",
		"--vpc", "vpc-12345678",
		"--output", "json",
	))

	var createResult struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &createResult)
	require.NotEmpty(t, createResult.OperationId)

	// List namespaces to find the created one
	out = runCLI(t, awsCLI("servicediscovery", "list-namespaces", "--output", "json"))

	var listResult struct {
		Namespaces []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
			Type string `json:"Type"`
		} `json:"Namespaces"`
	}
	parseJSON(t, out, &listResult)

	var nsId string
	for _, ns := range listResult.Namespaces {
		if ns.Name == "cli-test.local" {
			nsId = ns.Id
			assert.Equal(t, "DNS_PRIVATE", ns.Type)
		}
	}
	require.NotEmpty(t, nsId, "Expected to find namespace cli-test.local")

	// Cleanup
	runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsId))
}

func TestCloudMap_CreateService(t *testing.T) {
	// Create namespace first
	runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "svc-test.local",
		"--vpc", "vpc-12345678",
	))

	// Get namespace ID
	out := runCLI(t, awsCLI("servicediscovery", "list-namespaces", "--output", "json"))
	var nsList struct {
		Namespaces []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Namespaces"`
	}
	parseJSON(t, out, &nsList)
	var nsId string
	for _, ns := range nsList.Namespaces {
		if ns.Name == "svc-test.local" {
			nsId = ns.Id
		}
	}
	require.NotEmpty(t, nsId)

	// Create service
	out = runCLI(t, awsCLI("servicediscovery", "create-service",
		"--name", "my-service",
		"--namespace-id", nsId,
		"--dns-config", `NamespaceId=`+nsId+`,RoutingPolicy=MULTIVALUE,DnsRecords=[{Type=A,TTL=60}]`,
		"--output", "json",
	))

	var svcResult struct {
		Service struct {
			Id          string `json:"Id"`
			Name        string `json:"Name"`
			NamespaceId string `json:"NamespaceId"`
		} `json:"Service"`
	}
	parseJSON(t, out, &svcResult)
	assert.Equal(t, "my-service", svcResult.Service.Name)
	assert.Equal(t, nsId, svcResult.Service.NamespaceId)
	require.NotEmpty(t, svcResult.Service.Id)

	// Cleanup
	runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", svcResult.Service.Id))
	runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsId))
}

func TestCloudMap_RegisterAndListInstances(t *testing.T) {
	// Setup: namespace + service
	runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "discover-test.local",
		"--vpc", "vpc-12345678",
	))

	out := runCLI(t, awsCLI("servicediscovery", "list-namespaces", "--output", "json"))
	var nsList struct {
		Namespaces []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Namespaces"`
	}
	parseJSON(t, out, &nsList)
	var nsId string
	for _, ns := range nsList.Namespaces {
		if ns.Name == "discover-test.local" {
			nsId = ns.Id
		}
	}
	require.NotEmpty(t, nsId)

	out = runCLI(t, awsCLI("servicediscovery", "create-service",
		"--name", "web",
		"--namespace-id", nsId,
		"--output", "json",
	))
	var svcResult struct {
		Service struct {
			Id string `json:"Id"`
		} `json:"Service"`
	}
	parseJSON(t, out, &svcResult)
	svcId := svcResult.Service.Id

	// Register instance
	out = runCLI(t, awsCLI("servicediscovery", "register-instance",
		"--service-id", svcId,
		"--instance-id", "instance-1",
		"--attributes", "AWS_INSTANCE_IPV4=10.0.0.1,AWS_INSTANCE_PORT=8080",
		"--output", "json",
	))
	var regResult struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &regResult)
	require.NotEmpty(t, regResult.OperationId)

	// List instances (discover-instances uses a separate data-plane endpoint
	// with a data- hostname prefix, so we use list-instances instead)
	out = runCLI(t, awsCLI("servicediscovery", "list-instances",
		"--service-id", svcId,
		"--output", "json",
	))

	var listResult struct {
		Instances []struct {
			Id         string            `json:"Id"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"Instances"`
	}
	parseJSON(t, out, &listResult)
	require.Len(t, listResult.Instances, 1)
	assert.Equal(t, "instance-1", listResult.Instances[0].Id)
	assert.Equal(t, "10.0.0.1", listResult.Instances[0].Attributes["AWS_INSTANCE_IPV4"])

	// Cleanup
	runCLI(t, awsCLI("servicediscovery", "deregister-instance",
		"--service-id", svcId,
		"--instance-id", "instance-1",
	))
	runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", svcId))
	runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsId))
}

// TestCloudMap_CrossTaskDNS_CLI exercises cross-task DNS end-to-end through
// the aws CLI. awsvpc task registrations resolve to the registered task ENI
// address, and one task can resolve the other's service name.
func TestCloudMap_CrossTaskDNS_CLI(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-task DNS test (no fallback): %v", err)
	}
	q := func(args ...string) string { return strings.TrimSpace(runCLI(t, awsCLI(args...))) }

	octet := unusedDockerVPCOctet(t, 130, nil)
	vpcID, subnetID := mkVPCSubnet(t, q, vpcCIDR(octet), subnetCIDR(octet))
	t.Cleanup(func() {
		q("ec2", "delete-subnet", "--subnet-id", subnetID)
		q("ec2", "delete-vpc", "--vpc-id", vpcID)
		rmDockerNetworks(ecsVPCNet(vpcID), ecsVPCNet(vpcID)+"-egress")
	})

	out := runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "cli-xtask-dns.local",
		"--vpc", vpcID,
		"--output", "json",
	))
	var createNs struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &createNs)

	out = runCLI(t, awsCLI("servicediscovery", "list-namespaces", "--output", "json"))
	var nsList struct {
		Namespaces []struct{ Id, Name string }
	}
	parseJSON(t, out, &nsList)
	var nsId string
	for _, ns := range nsList.Namespaces {
		if ns.Name == "cli-xtask-dns.local" {
			nsId = ns.Id
		}
	}
	require.NotEmpty(t, nsId)

	// Services alpha + beta
	createService := func(name string) string {
		out := runCLI(t, awsCLI("servicediscovery", "create-service",
			"--name", name,
			"--namespace-id", nsId,
			"--dns-config", "NamespaceId="+nsId+",DnsRecords=[{Type=A,TTL=10}]",
			"--output", "json",
		))
		var svcResult struct {
			Service struct{ Id string } `json:"Service"`
		}
		parseJSON(t, out, &svcResult)
		require.NotEmpty(t, svcResult.Service.Id)
		return svcResult.Service.Id
	}
	alphaSvc := createService("alpha")
	betaSvc := createService("beta")
	t.Cleanup(func() {
		runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", alphaSvc))
		runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", betaSvc))
		runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsId))
	})

	// Cluster + task def with awslogs config (sim only spawns Docker
	// containers when awslogs is configured) + sleep command so the
	// container stays alive through real resolver updates.
	runCLI(t, awsCLI("ecs", "create-cluster", "--cluster-name", "cli-xtask-dns"))
	containerDef := `[{"name":"app","image":"alpine:latest","entryPoint":["sh","-c"],"command":["sleep 120"],"logConfiguration":{"logDriver":"awslogs","options":{"awslogs-group":"/ecs/cli-xtask-dns","awslogs-stream-prefix":"ecs"}}}]`
	out = runCLI(t, awsCLI("ecs", "register-task-definition",
		"--family", "cli-xtask-dns-td",
		"--requires-compatibilities", "FARGATE",
		"--network-mode", "awsvpc",
		"--cpu", "256",
		"--memory", "512",
		"--container-definitions", containerDef,
		"--output", "json",
	))
	var regTd struct {
		TaskDefinition struct{ TaskDefinitionArn string } `json:"taskDefinition"`
	}
	parseJSON(t, out, &regTd)

	runTask := func(cid string) string {
		out := runCLI(t, awsCLI("ecs", "run-task",
			"--cluster", "cli-xtask-dns",
			"--task-definition", regTd.TaskDefinition.TaskDefinitionArn,
			"--count", "1",
			"--launch-type", "FARGATE",
			"--network-configuration", "awsvpcConfiguration={subnets=["+subnetID+"]}",
			"--tags", "key=sockerless-container-id,value="+cid,
			"--output", "json",
		))
		var runRes struct {
			Tasks []struct{ TaskArn string } `json:"tasks"`
		}
		parseJSON(t, out, &runRes)
		require.Len(t, runRes.Tasks, 1)
		return runRes.Tasks[0].TaskArn
	}
	alphaCID := strings.Repeat("a", 64)
	betaCID := strings.Repeat("b", 64)

	waitTasksRunning := func(tasks ...string) {
		t.Helper()
		var taskStatuses []string
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			taskStatuses = taskStatuses[:0]
			allRunning := true
			for _, taskArn := range tasks {
				status := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-tasks",
					"--cluster", "cli-xtask-dns",
					"--tasks", taskArn,
					"--query", "tasks[0].lastStatus",
					"--output", "text",
				)))
				taskStatuses = append(taskStatuses, taskArn+"="+status)
				if status == "STOPPED" {
					reason := strings.TrimSpace(runCLI(t, awsCLI("ecs", "describe-tasks",
						"--cluster", "cli-xtask-dns",
						"--tasks", taskArn,
						"--query", "tasks[0].stoppedReason",
						"--output", "text",
					)))
					taskStatuses[len(taskStatuses)-1] += " reason=" + reason
					t.Fatalf("task stopped before RUNNING: %s", strings.Join(taskStatuses, ", "))
				}
				if status != "RUNNING" {
					allRunning = false
				}
			}
			if allRunning {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("tasks should reach RUNNING: %s", strings.Join(taskStatuses, ", "))
	}
	containerName := func(taskArn string) string {
		return "sockerless-sim-aws-task-" + taskArn[strings.LastIndex(taskArn, "/")+1:][:12]
	}
	waitContainer := func(name string) {
		t.Helper()
		var inspect []byte
		var inspectErr error
		require.Eventually(t, func() bool {
			inspect, inspectErr = exec.Command("docker", "inspect", name).CombinedOutput()
			return inspectErr == nil
		}, 45*time.Second, 500*time.Millisecond, "task container %s should exist before Cloud Map registration: err=%v output=%q", name, inspectErr, inspect)
	}

	alphaTask := runTask(alphaCID)
	cleanupCLIECSTask(t, "cli-xtask-dns", alphaTask)
	waitTasksRunning(alphaTask)
	alphaName := containerName(alphaTask)
	waitContainer(alphaName)

	betaTask := runTask(betaCID)
	cleanupCLIECSTask(t, "cli-xtask-dns", betaTask)
	waitTasksRunning(alphaTask, betaTask)
	waitContainer(containerName(betaTask))

	taskPrivateIPv4 := func(taskArn string) string {
		out := runCLI(t, awsCLI("ecs", "describe-tasks",
			"--cluster", "cli-xtask-dns",
			"--tasks", taskArn,
			"--output", "json",
		))
		var desc struct {
			Tasks []struct {
				Attachments []struct {
					Type    string `json:"type"`
					Details []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"details"`
				} `json:"attachments"`
			} `json:"tasks"`
		}
		parseJSON(t, out, &desc)
		require.Len(t, desc.Tasks, 1)
		for _, attachment := range desc.Tasks[0].Attachments {
			if attachment.Type != "ElasticNetworkInterface" {
				continue
			}
			for _, detail := range attachment.Details {
				if detail.Name == "privateIPv4Address" {
					require.NotEmpty(t, detail.Value)
					return detail.Value
				}
			}
		}
		t.Fatalf("task %s did not include an ElasticNetworkInterface privateIPv4Address", taskArn)
		return ""
	}
	alphaIP := taskPrivateIPv4(alphaTask)
	betaIP := taskPrivateIPv4(betaTask)

	// Register instances with the real task ENI addresses.
	registerInstance := func(serviceID, instanceID, ip string) {
		t.Helper()
		var out []byte
		var err error
		require.Eventually(t, func() bool {
			out, err = awsCLI("servicediscovery", "register-instance",
				"--service-id", serviceID,
				"--instance-id", instanceID,
				"--attributes", "AWS_INSTANCE_IPV4="+ip,
			).CombinedOutput()
			return err == nil
		}, 45*time.Second, 500*time.Millisecond, "register-instance should update task DNS state: %v %s", err, out)
	}
	registerInstance(alphaSvc, alphaCID[:12], alphaIP)
	t.Cleanup(func() {
		runCLI(t, awsCLI("servicediscovery", "deregister-instance",
			"--service-id", alphaSvc, "--instance-id", alphaCID[:12]))
	})
	registerInstance(betaSvc, betaCID[:12], betaIP)
	t.Cleanup(func() {
		runCLI(t, awsCLI("servicediscovery", "deregister-instance",
			"--service-id", betaSvc, "--instance-id", betaCID[:12]))
	})

	// Resolve beta from alpha through the task's normal libc resolver.
	var getent []byte
	var hosts []byte
	require.Eventually(t, func() bool {
		var err error
		getent, err = exec.Command("docker", "exec", alphaName, "getent", "hosts", "beta").CombinedOutput()
		hosts, _ = exec.Command("docker", "exec", alphaName, "cat", "/etc/hosts").CombinedOutput()
		return err == nil && len(getent) > 0
	}, 10*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via Cloud Map DNS; getent=%q hosts=%q", getent, hosts)
	assert.Contains(t, string(getent), "beta", "getent output should mention beta: %s", getent)

}

func TestCloudMap_DeregisterInstance(t *testing.T) {
	// Setup
	runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "dereg-test.local",
		"--vpc", "vpc-12345678",
	))

	out := runCLI(t, awsCLI("servicediscovery", "list-namespaces", "--output", "json"))
	var nsList struct {
		Namespaces []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"Namespaces"`
	}
	parseJSON(t, out, &nsList)
	var nsId string
	for _, ns := range nsList.Namespaces {
		if ns.Name == "dereg-test.local" {
			nsId = ns.Id
		}
	}
	require.NotEmpty(t, nsId)

	out = runCLI(t, awsCLI("servicediscovery", "create-service",
		"--name", "api",
		"--namespace-id", nsId,
		"--output", "json",
	))
	var svcResult struct {
		Service struct {
			Id string `json:"Id"`
		} `json:"Service"`
	}
	parseJSON(t, out, &svcResult)
	svcId := svcResult.Service.Id

	// Register then deregister
	runCLI(t, awsCLI("servicediscovery", "register-instance",
		"--service-id", svcId,
		"--instance-id", "temp-instance",
		"--attributes", "AWS_INSTANCE_IPV4=10.0.0.2",
	))

	runCLI(t, awsCLI("servicediscovery", "deregister-instance",
		"--service-id", svcId,
		"--instance-id", "temp-instance",
	))

	// Verify it's gone
	out = runCLI(t, awsCLI("servicediscovery", "list-instances",
		"--service-id", svcId,
		"--output", "json",
	))

	var listResult struct {
		Instances []struct {
			Id string `json:"Id"`
		} `json:"Instances"`
	}
	parseJSON(t, out, &listResult)
	assert.Empty(t, listResult.Instances)

	// Cleanup
	runCLI(t, awsCLI("servicediscovery", "delete-service", "--id", svcId))
	runCLI(t, awsCLI("servicediscovery", "delete-namespace", "--id", nsId))
}
