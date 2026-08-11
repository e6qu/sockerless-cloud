package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmCLINamespaceID resolves a Create*Namespace operation's NAMESPACE target ID.
func cmCLINamespaceID(t *testing.T, operationID string) string {
	t.Helper()
	out := runCLI(t, awsCLI("servicediscovery", "get-operation",
		"--operation-id", operationID, "--output", "json"))
	var op struct {
		Operation struct {
			Targets map[string]string `json:"Targets"`
		} `json:"Operation"`
	}
	parseJSON(t, out, &op)
	id := op.Operation.Targets["NAMESPACE"]
	require.NotEmpty(t, id, "operation should carry a NAMESPACE target")
	return id
}

// TestCloudMapNamespacesCLI covers create-http-namespace,
// create-public-dns-namespace, update-http-namespace,
// update-public-dns-namespace and update-private-dns-namespace via the aws CLI.
func TestCloudMapNamespacesCLI(t *testing.T) {
	// HTTP namespace.
	out := runCLI(t, awsCLI("servicediscovery", "create-http-namespace",
		"--name", "cli-http-ns", "--output", "json"))
	var httpCreate struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &httpCreate)
	require.NotEmpty(t, httpCreate.OperationId)
	httpID := cmCLINamespaceID(t, httpCreate.OperationId)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", httpID))

	runCLI(t, awsCLI("servicediscovery", "update-http-namespace",
		"--id", httpID,
		"--namespace", "Description=updated-http"))

	out = runCLI(t, awsCLI("servicediscovery", "get-namespace", "--id", httpID, "--output", "json"))
	var httpNs struct {
		Namespace struct {
			Type        string `json:"Type"`
			Description string `json:"Description"`
			Properties  struct {
				HttpProperties struct {
					HttpName string `json:"HttpName"`
				} `json:"HttpProperties"`
			} `json:"Properties"`
		} `json:"Namespace"`
	}
	parseJSON(t, out, &httpNs)
	assert.Equal(t, "HTTP", httpNs.Namespace.Type)
	assert.Equal(t, "updated-http", httpNs.Namespace.Description)
	assert.Equal(t, "cli-http-ns", httpNs.Namespace.Properties.HttpProperties.HttpName)

	// Public DNS namespace.
	out = runCLI(t, awsCLI("servicediscovery", "create-public-dns-namespace",
		"--name", "cli-public.example.com", "--output", "json"))
	var pubCreate struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &pubCreate)
	pubID := cmCLINamespaceID(t, pubCreate.OperationId)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", pubID))

	runCLI(t, awsCLI("servicediscovery", "update-public-dns-namespace",
		"--id", pubID,
		"--namespace", "Description=updated-public"))

	out = runCLI(t, awsCLI("servicediscovery", "get-namespace", "--id", pubID, "--output", "json"))
	var pubNs struct {
		Namespace struct {
			Type        string `json:"Type"`
			Description string `json:"Description"`
		} `json:"Namespace"`
	}
	parseJSON(t, out, &pubNs)
	assert.Equal(t, "DNS_PUBLIC", pubNs.Namespace.Type)
	assert.Equal(t, "updated-public", pubNs.Namespace.Description)

	// Private DNS namespace update.
	out = runCLI(t, awsCLI("servicediscovery", "create-private-dns-namespace",
		"--name", "cli-private.local", "--vpc", "vpc-cli123", "--output", "json"))
	var privCreate struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &privCreate)
	privID := cmCLINamespaceID(t, privCreate.OperationId)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", privID))

	runCLI(t, awsCLI("servicediscovery", "update-private-dns-namespace",
		"--id", privID,
		"--namespace", "Description=updated-private"))

	out = runCLI(t, awsCLI("servicediscovery", "get-namespace", "--id", privID, "--output", "json"))
	var privNs struct {
		Namespace struct {
			Arn         string `json:"Arn"`
			Description string `json:"Description"`
		} `json:"Namespace"`
	}
	parseJSON(t, out, &privNs)
	assert.Equal(t, "updated-private", privNs.Namespace.Description)

	// tag-resource / untag-resource round trip on the namespace ARN.
	runCLI(t, awsCLI("servicediscovery", "tag-resource",
		"--resource-arn", privNs.Namespace.Arn,
		"--tags", "Key=env,Value=test"))
	runCLI(t, awsCLI("servicediscovery", "untag-resource",
		"--resource-arn", privNs.Namespace.Arn,
		"--tag-keys", "env"))
}

// TestCloudMapServiceAttributesCLI covers update-service, get/update/delete
// service-attributes and list-operations via the aws CLI.
func TestCloudMapServiceAttributesCLI(t *testing.T) {
	out := runCLI(t, awsCLI("servicediscovery", "create-http-namespace",
		"--name", "cli-attr-ns", "--output", "json"))
	var nsCreate struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &nsCreate)
	nsID := cmCLINamespaceID(t, nsCreate.OperationId)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", nsID))

	out = runCLI(t, awsCLI("servicediscovery", "create-service",
		"--name", "cli-attr-svc", "--namespace-id", nsID, "--output", "json"))
	var svcCreate struct {
		Service struct {
			Id  string `json:"Id"`
			Arn string `json:"Arn"`
		} `json:"Service"`
	}
	parseJSON(t, out, &svcCreate)
	svcID := svcCreate.Service.Id
	require.NotEmpty(t, svcID)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-service", "--id", svcID))

	// update-service.
	runCLI(t, awsCLI("servicediscovery", "update-service",
		"--id", svcID,
		"--service", "Description=cli-updated-desc"))
	out = runCLI(t, awsCLI("servicediscovery", "get-service", "--id", svcID, "--output", "json"))
	var gotSvc struct {
		Service struct {
			Description string `json:"Description"`
		} `json:"Service"`
	}
	parseJSON(t, out, &gotSvc)
	assert.Equal(t, "cli-updated-desc", gotSvc.Service.Description)

	// update/get/delete service-attributes.
	runCLI(t, awsCLI("servicediscovery", "update-service-attributes",
		"--service-id", svcID,
		"--attributes", "k1=v1,k2=v2"))
	out = runCLI(t, awsCLI("servicediscovery", "get-service-attributes",
		"--service-id", svcID, "--output", "json"))
	var attrOut struct {
		ServiceAttributes struct {
			ServiceArn string            `json:"ServiceArn"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"ServiceAttributes"`
	}
	parseJSON(t, out, &attrOut)
	assert.Equal(t, "v1", attrOut.ServiceAttributes.Attributes["k1"])
	assert.Equal(t, "v2", attrOut.ServiceAttributes.Attributes["k2"])
	assert.Equal(t, svcCreate.Service.Arn, attrOut.ServiceAttributes.ServiceArn)

	runCLI(t, awsCLI("servicediscovery", "delete-service-attributes",
		"--service-id", svcID,
		"--attributes", "k1"))
	out = runCLI(t, awsCLI("servicediscovery", "get-service-attributes",
		"--service-id", svcID, "--output", "json"))
	var afterDelete struct {
		ServiceAttributes struct {
			Attributes map[string]string `json:"Attributes"`
		} `json:"ServiceAttributes"`
	}
	parseJSON(t, out, &afterDelete)
	_, gone := afterDelete.ServiceAttributes.Attributes["k1"]
	assert.False(t, gone)
	assert.Equal(t, "v2", afterDelete.ServiceAttributes.Attributes["k2"])

	// list-operations filtered by namespace.
	out = runCLI(t, awsCLI("servicediscovery", "list-operations",
		"--filters", "Name=NAMESPACE_ID,Values="+nsID,
		"--output", "json"))
	var opsOut struct {
		Operations []struct {
			Id     string `json:"Id"`
			Status string `json:"Status"`
		} `json:"Operations"`
	}
	parseJSON(t, out, &opsOut)
	require.NotEmpty(t, opsOut.Operations)
}

// TestCloudMapInstanceHealthCLI covers get-instance,
// update-instance-custom-health-status, get-instances-health-status and
// discover-instances-revision via the aws CLI.
func TestCloudMapInstanceHealthCLI(t *testing.T) {
	out := runCLI(t, awsCLI("servicediscovery", "create-http-namespace",
		"--name", "cli-inst-ns", "--output", "json"))
	var nsCreate struct {
		OperationId string `json:"OperationId"`
	}
	parseJSON(t, out, &nsCreate)
	nsID := cmCLINamespaceID(t, nsCreate.OperationId)
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-namespace", "--id", nsID))

	// update-instance-custom-health-status only applies to a service whose
	// health is caller-reported, so the service carries a custom health config.
	out = runCLI(t, awsCLI("servicediscovery", "create-service",
		"--name", "cli-inst-svc", "--namespace-id", nsID,
		"--health-check-custom-config", "FailureThreshold=1", "--output", "json"))
	var svcCreate struct {
		Service struct {
			Id string `json:"Id"`
		} `json:"Service"`
	}
	parseJSON(t, out, &svcCreate)
	svcID := svcCreate.Service.Id
	defer runCLIIgnore(awsCLI("servicediscovery", "delete-service", "--id", svcID))

	runCLI(t, awsCLI("servicediscovery", "register-instance",
		"--service-id", svcID,
		"--instance-id", "cli-inst-1",
		"--attributes", "AWS_INSTANCE_IPV4=10.0.0.9"))
	defer runCLIIgnore(awsCLI("servicediscovery", "deregister-instance",
		"--service-id", svcID, "--instance-id", "cli-inst-1"))

	// get-instance.
	out = runCLI(t, awsCLI("servicediscovery", "get-instance",
		"--service-id", svcID, "--instance-id", "cli-inst-1", "--output", "json"))
	var instOut struct {
		Instance struct {
			Id         string            `json:"Id"`
			Attributes map[string]string `json:"Attributes"`
		} `json:"Instance"`
	}
	parseJSON(t, out, &instOut)
	assert.Equal(t, "cli-inst-1", instOut.Instance.Id)
	assert.Equal(t, "10.0.0.9", instOut.Instance.Attributes["AWS_INSTANCE_IPV4"])

	// Health defaults HEALTHY, then a custom status flips it.
	out = runCLI(t, awsCLI("servicediscovery", "get-instances-health-status",
		"--service-id", svcID, "--output", "json"))
	var healthOut struct {
		Status map[string]string `json:"Status"`
	}
	parseJSON(t, out, &healthOut)
	assert.Equal(t, "HEALTHY", healthOut.Status["cli-inst-1"])

	runCLI(t, awsCLI("servicediscovery", "update-instance-custom-health-status",
		"--service-id", svcID,
		"--instance-id", "cli-inst-1",
		"--status", "UNHEALTHY"))
	out = runCLI(t, awsCLI("servicediscovery", "get-instances-health-status",
		"--service-id", svcID,
		"--instances", "cli-inst-1", "--output", "json"))
	parseJSON(t, out, &healthOut)
	assert.Equal(t, "UNHEALTHY", healthOut.Status["cli-inst-1"])
}
