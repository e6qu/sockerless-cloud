package aws_sdk_test

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmClient is a stock Cloud Map client pointed at the simulator's
// servicediscovery endpoint coordinate. Nothing about the client is
// reconfigured: DiscoverInstances and DiscoverInstancesRevision carry a modeled
// `data-` endpoint host prefix, so the SDK sends and signs them against
// `data-servicediscovery.localhost` exactly as it sends them to
// `data-servicediscovery.us-east-1.amazonaws.com` against real AWS.
func cmClient() *servicediscovery.Client {
	return servicediscovery.NewFromConfig(sdkConfig(), func(o *servicediscovery.Options) {
		o.BaseEndpoint = aws.String(simEndpoint("servicediscovery"))
	})
}

func TestCloudMap_CreateNamespaceAndGetOperation(t *testing.T) {
	client := cmClient()

	// Create a private DNS namespace
	createOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name:        aws.String("test-ns.local"),
		Vpc:         aws.String("vpc-12345"),
		Description: aws.String("test namespace"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.OperationId)

	// Get operation to retrieve namespace ID
	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: createOut.OperationId,
	})
	require.NoError(t, err)
	require.NotNil(t, opOut.Operation)
	assert.Equal(t, sdtypes.OperationStatusSuccess, opOut.Operation.Status)

	// Extract namespace ID from operation targets
	nsID, ok := opOut.Operation.Targets["NAMESPACE"]
	require.True(t, ok, "operation should have NAMESPACE target")
	assert.NotEmpty(t, nsID)

	// Get the namespace
	nsOut, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{
		Id: aws.String(nsID),
	})
	require.NoError(t, err)
	require.NotNil(t, nsOut.Namespace)
	assert.Equal(t, "test-ns.local", *nsOut.Namespace.Name)
	assert.Equal(t, sdtypes.NamespaceTypeDnsPrivate, nsOut.Namespace.Type)

	// Cleanup
	_, err = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{
		Id: aws.String(nsID),
	})
	require.NoError(t, err)
}

func TestCloudMap_CreateServiceAndListWithFilter(t *testing.T) {
	client := cmClient()

	// Create namespace
	createOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("svc-filter-test.local"),
		Vpc:  aws.String("vpc-12345"),
	})
	require.NoError(t, err)

	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: createOut.OperationId,
	})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]

	// Create two namespaces — second for filtering test
	createOut2, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("svc-filter-other.local"),
		Vpc:  aws.String("vpc-12345"),
	})
	require.NoError(t, err)
	opOut2, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: createOut2.OperationId,
	})
	require.NoError(t, err)
	nsID2 := opOut2.Operation.Targets["NAMESPACE"]

	// Create service in first namespace
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("my-service"),
		NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			DnsRecords: []sdtypes.DnsRecord{
				{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, svcOut.Service)
	assert.Equal(t, "my-service", *svcOut.Service.Name)
	svcID := *svcOut.Service.Id

	// Create service in second namespace
	svcOut2, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("other-service"),
		NamespaceId: aws.String(nsID2),
	})
	require.NoError(t, err)

	// List all services (no filter) — should include both
	listAll, err := client.ListServices(ctx, &servicediscovery.ListServicesInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(listAll.Services), 2)

	// List with NAMESPACE_ID filter — should return only first namespace's service
	listFiltered, err := client.ListServices(ctx, &servicediscovery.ListServicesInput{
		Filters: []sdtypes.ServiceFilter{
			{
				Name:      sdtypes.ServiceFilterNameNamespaceId,
				Values:    []string{nsID},
				Condition: sdtypes.FilterConditionEq,
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, listFiltered.Services, 1)
	assert.Equal(t, "my-service", *listFiltered.Services[0].Name)
	// List entries are the ServiceSummary shape (no NamespaceId member —
	// namespace association rides GetService); the namespace filter still
	// works off the stored association.
	assert.Equal(t, svcID, aws.ToString(listFiltered.Services[0].Id))
	assert.NotEmpty(t, aws.ToString(listFiltered.Services[0].Arn))
	require.NotNil(t, listFiltered.Services[0].DnsConfig)
	require.Len(t, listFiltered.Services[0].DnsConfig.DnsRecords, 1)

	// Cleanup
	_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: svcOut2.Service.Id})
	_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID2)})
}

func TestCloudMap_RegisterAndDiscoverInstances(t *testing.T) {
	client := cmClient()

	// Create namespace
	createOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("discover-test.local"),
		Vpc:  aws.String("vpc-12345"),
	})
	require.NoError(t, err)
	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: createOut.OperationId,
	})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]

	// Create service
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("web"),
		NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			DnsRecords: []sdtypes.DnsRecord{
				{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)},
			},
		},
	})
	require.NoError(t, err)
	svcID := *svcOut.Service.Id

	// Register two instances
	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-001"),
		Attributes: map[string]string{
			"AWS_INSTANCE_IPV4": "10.0.0.1",
			"HOSTNAME":          "web-1",
		},
	})
	require.NoError(t, err)

	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-002"),
		Attributes: map[string]string{
			"AWS_INSTANCE_IPV4": "10.0.0.2",
			"HOSTNAME":          "web-2",
		},
	})
	require.NoError(t, err)

	// Discover instances
	discoverOut, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-test.local"),
		ServiceName:   aws.String("web"),
	})
	require.NoError(t, err)
	require.Len(t, discoverOut.Instances, 2)

	// Verify attributes are present
	ips := map[string]bool{}
	for _, inst := range discoverOut.Instances {
		if ip, ok := inst.Attributes["AWS_INSTANCE_IPV4"]; ok {
			ips[ip] = true
		}
	}
	assert.True(t, ips["10.0.0.1"])
	assert.True(t, ips["10.0.0.2"])

	// Deregister one instance
	_, err = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-001"),
	})
	require.NoError(t, err)

	// Discover again — should only have one
	discoverOut2, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-test.local"),
		ServiceName:   aws.String("web"),
	})
	require.NoError(t, err)
	require.Len(t, discoverOut2.Instances, 1)
	assert.Equal(t, "10.0.0.2", discoverOut2.Instances[0].Attributes["AWS_INSTANCE_IPV4"])

	_, err = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	assertAWSAPIErrorCode(t, err, "ResourceInUse")

	// Cleanup
	_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-002"),
	})
	_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
}

// TestCloudMap_DataPlaneUsesModeledHostPrefix proves the two Cloud Map
// data-plane operations reach the simulator over the `data-` prefixed host the
// servicediscovery model puts on them, and that the control plane keeps the
// bare service host. The captured Authorization header shows the SigV4
// signature covers that host, and the simulator verifies the signature before
// answering — so a 200 here is only reachable if client and simulator agree on
// `data-servicediscovery.<endpoint>`, exactly as they would on
// `data-servicediscovery.us-east-1.amazonaws.com`.
func TestCloudMap_DataPlaneUsesModeledHostPrefix(t *testing.T) {
	client := cmClient()

	createOut, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String("host-prefix-ns"),
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, client, createOut.OperationId)
	t.Cleanup(func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})

	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("host-prefix-svc"),
		NamespaceId: aws.String(nsID),
	})
	require.NoError(t, err)
	svcID := aws.ToString(svcOut.Service.Id)
	t.Cleanup(func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	})

	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("host-prefix-inst"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.9.0.1"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId: aws.String(svcID), InstanceId: aws.String("host-prefix-inst"),
		})
	})

	dataHost := fmt.Sprintf("data-servicediscovery.localhost:%d", simPort)
	capture := func(rec *capturedRequest) func(*servicediscovery.Options) {
		return func(o *servicediscovery.Options) {
			o.APIOptions = append(o.APIOptions, captureSignedRequest(rec))
		}
	}

	var discovered capturedRequest
	discoverOut, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("host-prefix-ns"),
		ServiceName:   aws.String("host-prefix-svc"),
	}, capture(&discovered))
	require.NoError(t, err)
	require.Len(t, discoverOut.Instances, 1)
	assert.Equal(t, dataHost, discovered.host, "DiscoverInstances must address the modeled data- host")
	assert.Contains(t, discovered.signedHeaders(), "host", "the SigV4 signature must cover the data- host")
	assert.Contains(t, discovered.authorization, "/us-east-1/servicediscovery/aws4_request")

	var revised capturedRequest
	revOut, err := client.DiscoverInstancesRevision(ctx, &servicediscovery.DiscoverInstancesRevisionInput{
		NamespaceName: aws.String("host-prefix-ns"),
		ServiceName:   aws.String("host-prefix-svc"),
	}, capture(&revised))
	require.NoError(t, err)
	assert.Equal(t, int64(1), aws.ToInt64(revOut.InstancesRevision))
	assert.Equal(t, dataHost, revised.host, "DiscoverInstancesRevision must address the modeled data- host")
	assert.Contains(t, revised.signedHeaders(), "host")

	// A control-plane operation carries no host prefix.
	var controlPlane capturedRequest
	_, err = client.GetService(ctx, &servicediscovery.GetServiceInput{Id: aws.String(svcID)}, capture(&controlPlane))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("servicediscovery.localhost:%d", simPort), controlPlane.host)
}

// TestECS_CrossTaskDNS exercises cross-task DNS: Cloud Map registrations for
// awsvpc/Fargate-style tasks resolve to the registered task ENI address, so
// tasks in the namespace can resolve each other by service name.
func TestECS_CrossTaskDNS(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-task DNS test (no fallback): %v", err)
	}

	cm := cmClient()
	ecsCli := ecsClient()
	vpcID, subnetID := createECSTestVPCSubnet(t, "xtask-dns")

	// Namespace control-plane CRUD is independent from local Docker resources.
	createNs, err := cm.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("xtask-dns.local"),
		Vpc:  aws.String(vpcID),
	})
	require.NoError(t, err)
	opOut, err := cm.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: createNs.OperationId})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]

	// Two services — alpha, beta
	svcAlpha, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("alpha"),
		NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			DnsRecords: []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}},
		},
	})
	require.NoError(t, err)
	svcBeta, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("beta"),
		NamespaceId: aws.String(nsID),
		DnsConfig: &sdtypes.DnsConfig{
			DnsRecords: []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}},
		},
	})
	require.NoError(t, err)
	alphaCID := strings.Repeat("a", 64)
	betaCID := strings.Repeat("b", 64)
	t.Cleanup(func() {
		_, _ = cm.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId: svcAlpha.Service.Id, InstanceId: aws.String(alphaCID[:12]),
		})
		_, _ = cm.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId: svcBeta.Service.Id, InstanceId: aws.String(betaCID[:12]),
		})
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: svcAlpha.Service.Id})
		_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: svcBeta.Service.Id})
		_, _ = cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})

	// Cluster + task def running long enough for real container startup and
	// Cloud Map resolver updates on slower runtimes.
	_, err = ecsCli.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String("xtask-dns")})
	require.NoError(t, err)
	tdOut, err := ecsCli.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("xtask-dns-td"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:       aws.String("app"),
			Image:      aws.String("alpine:latest"),
			EntryPoint: []string{"sh", "-c"},
			Command:    []string{"sleep 120"},
			LogConfiguration: &ecstypes.LogConfiguration{
				LogDriver: ecstypes.LogDriverAwslogs,
				Options: map[string]string{
					"awslogs-group":         "/ecs/xtask-dns",
					"awslogs-stream-prefix": "ecs",
				},
			},
		}},
	})
	require.NoError(t, err)
	runTask := func(containerID string) string {
		runOut, err := ecsCli.RunTask(ctx, &ecs.RunTaskInput{
			Cluster:        aws.String("xtask-dns"),
			TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
			Count:          aws.Int32(1),
			LaunchType:     ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{
				AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}},
			},
			Tags: []ecstypes.Tag{
				{Key: aws.String("sockerless-container-id"), Value: aws.String(containerID)},
			},
		})
		require.NoError(t, err)
		require.Len(t, runOut.Tasks, 1)
		return *runOut.Tasks[0].TaskArn
	}

	waitTasksRunning := func(tasks ...string) {
		t.Helper()
		var taskStatuses []string
		require.Eventually(t, func() bool {
			out, err := ecsCli.DescribeTasks(ctx, &ecs.DescribeTasksInput{
				Cluster: aws.String("xtask-dns"),
				Tasks:   tasks,
			})
			if err != nil {
				taskStatuses = []string{"DescribeTasks: " + err.Error()}
				return false
			}
			if len(out.Tasks) < len(tasks) {
				taskStatuses = []string{fmt.Sprintf("got %d tasks", len(out.Tasks))}
				return false
			}
			taskStatuses = taskStatuses[:0]
			for _, tk := range out.Tasks {
				status := aws.ToString(tk.LastStatus)
				taskStatuses = append(taskStatuses, aws.ToString(tk.TaskArn)+"="+status)
				if status != "RUNNING" {
					return false
				}
			}
			return true
		}, 45*time.Second, 500*time.Millisecond, "tasks should reach RUNNING: %s", strings.Join(taskStatuses, ", "))
	}
	containerName := func(taskArn string) string {
		taskID := taskArn[strings.LastIndex(taskArn, "/")+1:]
		return "sockerless-sim-aws-task-" + taskID[:12]
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
	cleanupECSTask(t, ecsCli, "xtask-dns", alphaTask)
	waitTasksRunning(alphaTask)
	alphaContainer := containerName(alphaTask)
	waitContainer(alphaContainer)

	betaTask := runTask(betaCID)
	cleanupECSTask(t, ecsCli, "xtask-dns", betaTask)
	waitTasksRunning(alphaTask, betaTask)
	waitContainer(containerName(betaTask))

	// Register each task as an instance in its service. The registered
	// AWS_INSTANCE_IPV4 is the task ENI address clients should resolve.
	alphaIP := ecsTaskPrivateIPv4(t, ecsCli, "xtask-dns", alphaTask)
	betaIP := ecsTaskPrivateIPv4(t, ecsCli, "xtask-dns", betaTask)
	registerInstance := func(serviceID, instanceID, ip string) {
		t.Helper()
		var registerErr error
		require.Eventually(t, func() bool {
			_, registerErr = cm.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
				ServiceId:  aws.String(serviceID),
				InstanceId: aws.String(instanceID),
				Attributes: map[string]string{"AWS_INSTANCE_IPV4": ip},
			})
			return registerErr == nil
		}, 45*time.Second, 500*time.Millisecond, "RegisterInstance should update task DNS state: %v", registerErr)
	}
	registerInstance(aws.ToString(svcAlpha.Service.Id), alphaCID[:12], alphaIP)
	registerInstance(aws.ToString(svcBeta.Service.Id), betaCID[:12], betaIP)

	// Exec into alpha's container and resolve "beta" through the task's
	// normal libc resolver.
	var getent []byte
	var execErr error
	var hosts []byte
	require.Eventually(t, func() bool {
		cmd := exec.Command("docker", "exec", alphaContainer, "getent", "hosts", "beta")
		getent, execErr = cmd.CombinedOutput()
		hosts, _ = exec.Command("docker", "exec", alphaContainer, "cat", "/etc/hosts").CombinedOutput()
		return execErr == nil && len(getent) > 0
	}, 10*time.Second, 500*time.Millisecond, "alpha task should resolve 'beta' via Cloud Map DNS: getent=%q err=%v hosts=%q", getent, execErr, hosts)

	assert.Contains(t, string(getent), "beta", "getent output should mention beta hostname: %s", getent)

}

func ecsTaskPrivateIPv4(t *testing.T, client *ecs.Client, clusterName, taskArn string) string {
	t.Helper()
	out, err := client.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(clusterName),
		Tasks:   []string{taskArn},
	})
	require.NoError(t, err)
	require.Len(t, out.Tasks, 1)
	for _, attachment := range out.Tasks[0].Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "privateIPv4Address" {
				ip := aws.ToString(detail.Value)
				require.NotEmpty(t, ip)
				return ip
			}
		}
	}
	t.Fatalf("task %s did not include an ElasticNetworkInterface privateIPv4Address", taskArn)
	return ""
}

func TestCloudMap_DeleteServiceAndNamespace(t *testing.T) {
	client := cmClient()

	// Create namespace
	createOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("delete-test.local"),
		Vpc:  aws.String("vpc-12345"),
	})
	require.NoError(t, err)
	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: createOut.OperationId,
	})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]

	// Create service
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("deletable-svc"),
		NamespaceId: aws.String(nsID),
	})
	require.NoError(t, err)
	svcID := *svcOut.Service.Id

	_, err = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{
		Id: aws.String(nsID),
	})
	assertAWSAPIErrorCode(t, err, "ResourceInUse")

	// Delete service
	delSvcOut, err := client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{
		Id: aws.String(svcID),
	})
	require.NoError(t, err)
	_ = delSvcOut

	// Verify service is gone — GetService should fail
	_, err = client.GetService(ctx, &servicediscovery.GetServiceInput{
		Id: aws.String(svcID),
	})
	assert.Error(t, err, "GetService should fail after deletion")

	// Delete namespace
	delNsOut, err := client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{
		Id: aws.String(nsID),
	})
	require.NoError(t, err)
	require.NotNil(t, delNsOut.OperationId)

	// Verify namespace is gone
	_, err = client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{
		Id: aws.String(nsID),
	})
	assert.Error(t, err, "GetNamespace should fail after deletion")
}

// TestECS_MultiServiceDNS verifies the Cloud Map model where one task (instance)
// backs MULTIPLE services — i.e. resolves under several DNS names that all point
// at its IP. This is what GitLab CI `services:` needs: a service container
// reachable by its alias AND its task hostname. Real Cloud Map registers an
// instance per service (ServiceId+InstanceId), so the same instance may join
// several services; the simulator must realize every such name into DNS. Before
// the fix, the second registration's Docker network-connect failed ("already
// connected") and only one name resolved.
func TestECS_MultiServiceDNS(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for multi-service DNS test (no fallback): %v", err)
	}
	cm := cmClient()
	ecsCli := ecsClient()
	vpcID, subnetID := createECSTestVPCSubnet(t, "multi-svc-dns")

	createNs, err := cm.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("multi-svc.local"), Vpc: aws.String(vpcID),
	})
	require.NoError(t, err)
	opOut, err := cm.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: createNs.OperationId})
	require.NoError(t, err)
	nsID := opOut.Operation.Targets["NAMESPACE"]

	mkSvc := func(name string) string {
		out, err := cm.CreateService(ctx, &servicediscovery.CreateServiceInput{
			Name: aws.String(name), NamespaceId: aws.String(nsID),
			DnsConfig: &sdtypes.DnsConfig{DnsRecords: []sdtypes.DnsRecord{{Type: sdtypes.RecordTypeA, TTL: aws.Int64(10)}}},
		})
		require.NoError(t, err)
		return aws.ToString(out.Service.Id)
	}
	svcWeb := mkSvc("web")        // the server's primary name
	svcAlias := mkSvc("webalias") // the server's alias — the multi-name case
	svcClient := mkSvc("client")  // puts the client task on the namespace network

	serverCID := strings.Repeat("c", 64)
	clientCID := strings.Repeat("d", 64)
	t.Cleanup(func() {
		for _, s := range []struct{ id, cid string }{{svcWeb, serverCID}, {svcAlias, serverCID}, {svcClient, clientCID}} {
			_, _ = cm.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{ServiceId: aws.String(s.id), InstanceId: aws.String(s.cid[:12])})
			_, _ = cm.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(s.id)})
		}
		_, _ = cm.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	})

	_, err = ecsCli.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String("multi-svc-dns")})
	require.NoError(t, err)
	tdOut, err := ecsCli.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String("multi-svc-td"),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc, Cpu: aws.String("256"), Memory: aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String("alpine:latest"),
			EntryPoint: []string{"sh", "-c"}, Command: []string{"sleep 120"},
		}},
	})
	require.NoError(t, err)

	runTask := func(cid string) string {
		out, err := ecsCli.RunTask(ctx, &ecs.RunTaskInput{
			Cluster: aws.String("multi-svc-dns"), TaskDefinition: tdOut.TaskDefinition.TaskDefinitionArn,
			Count: aws.Int32(1), LaunchType: ecstypes.LaunchTypeFargate,
			NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{Subnets: []string{subnetID}}},
			Tags:                 []ecstypes.Tag{{Key: aws.String("sockerless-container-id"), Value: aws.String(cid)}},
		})
		require.NoError(t, err)
		require.Len(t, out.Tasks, 1)
		return aws.ToString(out.Tasks[0].TaskArn)
	}
	containerName := func(taskArn string) string {
		taskID := taskArn[strings.LastIndex(taskArn, "/")+1:]
		return "sockerless-sim-aws-task-" + taskID[:12]
	}
	waitRunning := func(tasks ...string) {
		t.Helper()
		require.Eventually(t, func() bool {
			out, err := ecsCli.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String("multi-svc-dns"), Tasks: tasks})
			if err != nil || len(out.Tasks) < len(tasks) {
				return false
			}
			for _, tk := range out.Tasks {
				if aws.ToString(tk.LastStatus) != "RUNNING" {
					return false
				}
			}
			return true
		}, 45*time.Second, 500*time.Millisecond, "tasks should reach RUNNING")
	}
	waitContainer := func(name string) {
		t.Helper()
		require.Eventually(t, func() bool {
			return exec.Command("docker", "inspect", name).Run() == nil
		}, 45*time.Second, 500*time.Millisecond, "container %s should exist", name)
	}

	// Start the server first and wait for RUNNING so it provisions the shared
	// VPC network before the client task races to create the same one.
	serverTask := runTask(serverCID)
	cleanupECSTask(t, ecsCli, "multi-svc-dns", serverTask)
	waitRunning(serverTask)
	waitContainer(containerName(serverTask))
	clientTask := runTask(clientCID)
	cleanupECSTask(t, ecsCli, "multi-svc-dns", clientTask)
	waitRunning(serverTask, clientTask)
	clientContainer := containerName(clientTask)
	waitContainer(clientContainer)

	serverIP := ecsTaskPrivateIPv4(t, ecsCli, "multi-svc-dns", serverTask)
	clientIP := ecsTaskPrivateIPv4(t, ecsCli, "multi-svc-dns", clientTask)

	register := func(svcID, cid, ip string) {
		t.Helper()
		var rerr error
		require.Eventually(t, func() bool {
			_, rerr = cm.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
				ServiceId: aws.String(svcID), InstanceId: aws.String(cid[:12]),
				Attributes: map[string]string{"AWS_INSTANCE_IPV4": ip},
			})
			return rerr == nil
		}, 45*time.Second, 500*time.Millisecond, "RegisterInstance: %v", rerr)
	}
	// The server task backs BOTH services under the SAME instance ID — the
	// multi-name registration that previously failed on the second connect.
	register(svcWeb, serverCID, serverIP)
	register(svcAlias, serverCID, serverIP)
	register(svcClient, clientCID, clientIP)

	// From the client, BOTH of the server's names must resolve.
	for _, name := range []string{"web", "webalias"} {
		var getent []byte
		var execErr error
		require.Eventually(t, func() bool {
			getent, execErr = exec.Command("docker", "exec", clientContainer, "getent", "hosts", name).CombinedOutput()
			return execErr == nil && len(getent) > 0
		}, 15*time.Second, 500*time.Millisecond, "client should resolve %q via Cloud Map: getent=%q err=%v", name, getent, execErr)
		assert.Contains(t, string(getent), name, "getent for %s should resolve", name)
	}
}
