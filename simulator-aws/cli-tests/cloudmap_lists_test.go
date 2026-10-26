package aws_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmDataEndpoint returns an endpoint shaped like the real
// `servicediscovery.us-east-1.amazonaws.com`, so botocore prepends the `data-`
// host prefix DiscoverInstances and DiscoverInstancesRevision carry. Pair it
// with awsCLIHostPrefixed, which makes that name reach the simulator.
func cmDataEndpoint(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("http://servicediscovery.localhost:%s", portFromBaseURL(t))
}

func cmCLIHTTPNamespace(t *testing.T, name string, extra ...string) string {
	t.Helper()
	args := append([]string{"servicediscovery", "create-http-namespace",
		"--name", name, "--output", "json"}, extra...)
	out := runCLI(t, awsCLI(args...))
	var created struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.OperationId)
	id := cmCLINamespaceID(t, created.OperationId)
	t.Cleanup(func() { runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", id)) })
	return id
}

func cmCLIService(t *testing.T, nsID, name string, extra ...string) (string, string) {
	t.Helper()
	args := append([]string{"servicediscovery", "create-service",
		"--name", name, "--namespace-id", nsID, "--output", "json"}, extra...)
	out := runCLI(t, awsCLI(args...))
	var created struct {
		Service struct {
			Id  string `json:"Id"`
			Arn string `json:"Arn"`
		} `json:"Service"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.Service.Id)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("servicediscovery", "delete-service", "--id", created.Service.Id))
	})
	return created.Service.Id, created.Service.Arn
}

func TestCloudMapListServicesCLI(t *testing.T) {
	t.Parallel()
	nsID := cmCLIHTTPNamespace(t, "cli-list-svc-ns")
	svcA, _ := cmCLIService(t, nsID, "cli-list-svc-a")
	svcB, _ := cmCLIService(t, nsID, "cli-list-svc-b")

	out := runCLI(t, awsCLI("servicediscovery", "list-services",
		"--filters", "Name=NAMESPACE_ID,Values="+nsID, "--output", "json"))
	var listed struct {
		Services []struct {
			Id            string `json:"Id"`
			Arn           string `json:"Arn"`
			Name          string `json:"Name"`
			Type          string `json:"Type"`
			InstanceCount int    `json:"InstanceCount"`
			CreateDate    string `json:"CreateDate"`
		} `json:"Services"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.Services, 2)
	ids := map[string]bool{}
	for _, svc := range listed.Services {
		ids[svc.Id] = true
		assert.NotEmpty(t, svc.Arn)
		assert.Equal(t, "HTTP", svc.Type)
		assert.Equal(t, 0, svc.InstanceCount)
		assert.NotEmpty(t, svc.CreateDate)
	}
	assert.True(t, ids[svcA] && ids[svcB])

	otherNs := cmCLIHTTPNamespace(t, "cli-list-svc-empty-ns")
	out = runCLI(t, awsCLI("servicediscovery", "list-services",
		"--filters", "Name=NAMESPACE_ID,Values="+otherNs, "--output", "json"))
	parseJSON(t, out, &listed)
	assert.Empty(t, listed.Services)

	out = runCLI(t, awsCLI("servicediscovery", "list-services",
		"--filters", "Name=NAMESPACE_ID,Values="+nsID,
		"--page-size", "1", "--max-items", "1", "--output", "json"))
	var paged struct {
		Services []struct {
			Id string `json:"Id"`
		} `json:"Services"`
		NextToken string `json:"NextToken"`
	}
	parseJSON(t, out, &paged)
	require.Len(t, paged.Services, 1)
	require.NotEmpty(t, paged.NextToken)

	out = runCLI(t, awsCLI("servicediscovery", "list-services",
		"--filters", "Name=NAMESPACE_ID,Values="+nsID,
		"--page-size", "1", "--max-items", "1",
		"--starting-token", paged.NextToken, "--output", "json"))
	var second struct {
		Services []struct {
			Id string `json:"Id"`
		} `json:"Services"`
	}
	parseJSON(t, out, &second)
	require.Len(t, second.Services, 1)
	assert.NotEqual(t, paged.Services[0].Id, second.Services[0].Id)
}

func TestCloudMapListNamespacesFiltersCLI(t *testing.T) {
	t.Parallel()
	nsID := cmCLIHTTPNamespace(t, "cli-filter-http-ns")
	cmCLIService(t, nsID, "cli-filter-svc")

	out := runCLI(t, awsCLI("servicediscovery", "list-namespaces",
		"--filters", "Name=NAME,Values=cli-filter-http-ns", "--output", "json"))
	var listed struct {
		Namespaces []struct {
			Id           string `json:"Id"`
			Type         string `json:"Type"`
			ServiceCount int    `json:"ServiceCount"`
			Properties   struct {
				HttpProperties struct {
					HttpName string `json:"HttpName"`
				} `json:"HttpProperties"`
			} `json:"Properties"`
		} `json:"Namespaces"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.Namespaces, 1)
	assert.Equal(t, nsID, listed.Namespaces[0].Id)
	assert.Equal(t, "HTTP", listed.Namespaces[0].Type)
	assert.Equal(t, 1, listed.Namespaces[0].ServiceCount)
	assert.Equal(t, "cli-filter-http-ns", listed.Namespaces[0].Properties.HttpProperties.HttpName)

	out = runCLI(t, awsCLI("servicediscovery", "list-namespaces",
		"--filters", "Name=TYPE,Values=DNS_PRIVATE", "--output", "json"))
	parseJSON(t, out, &listed)
	for _, ns := range listed.Namespaces {
		assert.Equal(t, "DNS_PRIVATE", ns.Type)
		assert.NotEqual(t, nsID, ns.Id)
	}
}

func TestCloudMapTagsCLI(t *testing.T) {
	t.Parallel()
	nsID := cmCLIHTTPNamespace(t, "cli-tags-ns", "--tags", "Key=env,Value=cli", "Key=owner,Value=platform")

	out := runCLI(t, awsCLI("servicediscovery", "get-namespace", "--id", nsID, "--output", "json"))
	var ns struct {
		Namespace struct {
			Arn string `json:"Arn"`
		} `json:"Namespace"`
	}
	parseJSON(t, out, &ns)
	require.NotEmpty(t, ns.Namespace.Arn)

	readTags := func() map[string]string {
		out := runCLI(t, awsCLI("servicediscovery", "list-tags-for-resource",
			"--resource-arn", ns.Namespace.Arn, "--output", "json"))
		var tagsOut struct {
			Tags []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"Tags"`
		}
		parseJSON(t, out, &tagsOut)
		tags := map[string]string{}
		for _, tag := range tagsOut.Tags {
			tags[tag.Key] = tag.Value
		}
		return tags
	}

	tags := readTags()
	assert.Equal(t, "cli", tags["env"])
	assert.Equal(t, "platform", tags["owner"])

	runCLI(t, awsCLI("servicediscovery", "tag-resource",
		"--resource-arn", ns.Namespace.Arn, "--tags", "Key=tier,Value=edge"))
	tags = readTags()
	assert.Equal(t, "edge", tags["tier"])

	runCLI(t, awsCLI("servicediscovery", "untag-resource",
		"--resource-arn", ns.Namespace.Arn, "--tag-keys", "env"))
	tags = readTags()
	assert.NotContains(t, tags, "env")
	assert.Equal(t, "platform", tags["owner"])

	_, svcArn := cmCLIService(t, nsID, "cli-tags-svc", "--tags", "Key=role,Value=api")
	out = runCLI(t, awsCLI("servicediscovery", "list-tags-for-resource",
		"--resource-arn", svcArn, "--output", "json"))
	var svcTags struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	}
	parseJSON(t, out, &svcTags)
	require.Len(t, svcTags.Tags, 1)
	assert.Equal(t, "role", svcTags.Tags[0].Key)
	assert.Equal(t, "api", svcTags.Tags[0].Value)

	errOut := runCLIExpectError(t, awsCLI("servicediscovery", "list-tags-for-resource",
		"--resource-arn", "arn:aws:servicediscovery:us-east-1:123456789012:namespace/ns-missing"))
	assert.Contains(t, errOut, "ResourceNotFoundException")
}

func TestCloudMapNotFoundErrorsCLI(t *testing.T) {
	t.Parallel()
	errOut := runCLIExpectError(t, awsCLI("servicediscovery", "get-operation",
		"--operation-id", "op-does-not-exist"))
	assert.Contains(t, errOut, "OperationNotFound")

	errOut = runCLIExpectError(t, awsCLI("servicediscovery", "list-instances",
		"--service-id", "srv-does-not-exist"))
	assert.Contains(t, errOut, "ServiceNotFound")

	nsID := cmCLIHTTPNamespace(t, "cli-dup-ns")
	errOut = runCLIExpectError(t, awsCLI("servicediscovery", "create-http-namespace",
		"--name", "cli-dup-ns"))
	assert.Contains(t, errOut, "NamespaceAlreadyExists")

	cmCLIService(t, nsID, "cli-dup-svc")
	errOut = runCLIExpectError(t, awsCLI("servicediscovery", "create-service",
		"--name", "cli-dup-svc", "--namespace-id", nsID))
	assert.Contains(t, errOut, "ServiceAlreadyExists")
}

// TestCloudMapDiscoverInstancesCLI covers the two Cloud Map data-plane
// operations, which the CLI sends to the `data-` prefixed host.
func TestCloudMapDiscoverInstancesCLI(t *testing.T) {
	t.Parallel()
	endpoint := cmDataEndpoint(t)

	nsID := cmCLIHTTPNamespace(t, "cli-discover-ns")
	svcID, _ := cmCLIService(t, nsID, "cli-discover-svc")

	for _, inst := range []struct{ id, ip, stage string }{
		{"cli-disc-1", "10.7.0.1", "prod"},
		{"cli-disc-2", "10.7.0.2", "dev"},
	} {
		runCLI(t, awsCLI("servicediscovery", "register-instance",
			"--service-id", svcID,
			"--instance-id", inst.id,
			"--attributes", "AWS_INSTANCE_IPV4="+inst.ip+",stage="+inst.stage))
		id := inst.id
		t.Cleanup(func() {
			runCLIIgnore(awsCLI("servicediscovery", "deregister-instance",
				"--service-id", svcID, "--instance-id", id))
		})
	}

	out := runCLI(t, awsCLIHostPrefixed("servicediscovery", "discover-instances",
		"--endpoint-url", endpoint,
		"--namespace-name", "cli-discover-ns",
		"--service-name", "cli-discover-svc",
		"--health-status", "ALL", "--output", "json"))
	var discovered struct {
		Instances []struct {
			InstanceId    string            `json:"InstanceId"`
			NamespaceName string            `json:"NamespaceName"`
			ServiceName   string            `json:"ServiceName"`
			HealthStatus  string            `json:"HealthStatus"`
			Attributes    map[string]string `json:"Attributes"`
		} `json:"Instances"`
		InstancesRevision int64 `json:"InstancesRevision"`
	}
	parseJSON(t, out, &discovered)
	require.Len(t, discovered.Instances, 2)
	assert.Equal(t, int64(2), discovered.InstancesRevision)
	for _, inst := range discovered.Instances {
		assert.Equal(t, "cli-discover-ns", inst.NamespaceName)
		assert.Equal(t, "cli-discover-svc", inst.ServiceName)
		assert.Equal(t, "HEALTHY", inst.HealthStatus)
		assert.NotEmpty(t, inst.Attributes["AWS_INSTANCE_IPV4"])
	}

	out = runCLI(t, awsCLIHostPrefixed("servicediscovery", "discover-instances",
		"--endpoint-url", endpoint,
		"--namespace-name", "cli-discover-ns",
		"--service-name", "cli-discover-svc",
		"--health-status", "ALL",
		"--query-parameters", "stage=prod", "--output", "json"))
	parseJSON(t, out, &discovered)
	require.Len(t, discovered.Instances, 1)
	assert.Equal(t, "cli-disc-1", discovered.Instances[0].InstanceId)

	out = runCLI(t, awsCLIHostPrefixed("servicediscovery", "discover-instances-revision",
		"--endpoint-url", endpoint,
		"--namespace-name", "cli-discover-ns",
		"--service-name", "cli-discover-svc", "--output", "json"))
	var revision struct {
		InstancesRevision int64 `json:"InstancesRevision"`
	}
	parseJSON(t, out, &revision)
	assert.Equal(t, int64(2), revision.InstancesRevision)
}
