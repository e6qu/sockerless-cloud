package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmNamespaceIDFromOp resolves a Create*Namespace operation's NAMESPACE target.
func cmNamespaceIDFromOp(t *testing.T, client *servicediscovery.Client, opID *string) string {
	t.Helper()
	opOut, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: opID})
	require.NoError(t, err)
	require.NotNil(t, opOut.Operation)
	id, ok := opOut.Operation.Targets["NAMESPACE"]
	require.True(t, ok, "operation should carry a NAMESPACE target")
	require.NotEmpty(t, id)
	return id
}

// TestCloudMap_HttpAndPublicNamespaceLifecycle exercises CreateHttpNamespace,
// CreatePublicDnsNamespace, UpdateHttpNamespace, UpdatePublicDnsNamespace and
// UpdatePrivateDnsNamespace round-tripped through GetNamespace.
func TestCloudMap_HttpAndPublicNamespaceLifecycle(t *testing.T) {
	client := cmClient()

	// HTTP namespace.
	httpOut, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name:        aws.String("extra-http-ns"),
		Description: aws.String("http ns"),
	})
	require.NoError(t, err)
	httpID := cmNamespaceIDFromOp(t, client, httpOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(httpID)})
	}()

	httpNs, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(httpID)})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.NamespaceTypeHttp, httpNs.Namespace.Type)
	require.NotNil(t, httpNs.Namespace.Properties)
	require.NotNil(t, httpNs.Namespace.Properties.HttpProperties)
	assert.Equal(t, "extra-http-ns", aws.ToString(httpNs.Namespace.Properties.HttpProperties.HttpName))

	_, err = client.UpdateHttpNamespace(ctx, &servicediscovery.UpdateHttpNamespaceInput{
		Id:        aws.String(httpID),
		Namespace: &sdtypes.HttpNamespaceChange{Description: aws.String("updated http")},
	})
	require.NoError(t, err)
	httpNs, err = client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(httpID)})
	require.NoError(t, err)
	assert.Equal(t, "updated http", aws.ToString(httpNs.Namespace.Description))

	// Public DNS namespace.
	pubOut, err := client.CreatePublicDnsNamespace(ctx, &servicediscovery.CreatePublicDnsNamespaceInput{
		Name:        aws.String("extra-public.example.com"),
		Description: aws.String("public ns"),
	})
	require.NoError(t, err)
	pubID := cmNamespaceIDFromOp(t, client, pubOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(pubID)})
	}()

	pubNs, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(pubID)})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.NamespaceTypeDnsPublic, pubNs.Namespace.Type)

	_, err = client.UpdatePublicDnsNamespace(ctx, &servicediscovery.UpdatePublicDnsNamespaceInput{
		Id: aws.String(pubID),
		Namespace: &sdtypes.PublicDnsNamespaceChange{
			Description: aws.String("updated public"),
			Properties: &sdtypes.PublicDnsNamespacePropertiesChange{
				DnsProperties: &sdtypes.PublicDnsPropertiesMutableChange{
					SOA: &sdtypes.SOAChange{TTL: aws.Int64(120)},
				},
			},
		},
	})
	require.NoError(t, err)
	pubNs, err = client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(pubID)})
	require.NoError(t, err)
	assert.Equal(t, "updated public", aws.ToString(pubNs.Namespace.Description))

	// Private DNS namespace update.
	privOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("extra-private.local"),
		Vpc:  aws.String("vpc-extra123"),
	})
	require.NoError(t, err)
	privID := cmNamespaceIDFromOp(t, client, privOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(privID)})
	}()

	_, err = client.UpdatePrivateDnsNamespace(ctx, &servicediscovery.UpdatePrivateDnsNamespaceInput{
		Id: aws.String(privID),
		Namespace: &sdtypes.PrivateDnsNamespaceChange{
			Description: aws.String("updated private"),
		},
	})
	require.NoError(t, err)
	privNs, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(privID)})
	require.NoError(t, err)
	assert.Equal(t, "updated private", aws.ToString(privNs.Namespace.Description))
}

// TestCloudMap_ServiceUpdateAndAttributes exercises UpdateService,
// GetServiceAttributes, UpdateServiceAttributes and DeleteServiceAttributes.
func TestCloudMap_ServiceUpdateAndAttributes(t *testing.T) {
	client := cmClient()

	nsOut, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String("svc-attr-ns"),
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, client, nsOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	}()

	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("attr-svc"),
		NamespaceId: aws.String(nsID),
	})
	require.NoError(t, err)
	svcID := aws.ToString(svcOut.Service.Id)
	defer func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	}()

	// UpdateService changes the description.
	updOut, err := client.UpdateService(ctx, &servicediscovery.UpdateServiceInput{
		Id: aws.String(svcID),
		Service: &sdtypes.ServiceChange{
			Description: aws.String("updated svc desc"),
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(updOut.OperationId))
	gotSvc, err := client.GetService(ctx, &servicediscovery.GetServiceInput{Id: aws.String(svcID)})
	require.NoError(t, err)
	assert.Equal(t, "updated svc desc", aws.ToString(gotSvc.Service.Description))

	// Update + Get + Delete service attributes.
	_, err = client.UpdateServiceAttributes(ctx, &servicediscovery.UpdateServiceAttributesInput{
		ServiceId:  aws.String(svcID),
		Attributes: map[string]string{"k1": "v1", "k2": "v2"},
	})
	require.NoError(t, err)

	attrOut, err := client.GetServiceAttributes(ctx, &servicediscovery.GetServiceAttributesInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)
	require.NotNil(t, attrOut.ServiceAttributes)
	assert.Equal(t, "v1", attrOut.ServiceAttributes.Attributes["k1"])
	assert.Equal(t, "v2", attrOut.ServiceAttributes.Attributes["k2"])
	assert.Equal(t, aws.ToString(svcOut.Service.Arn), aws.ToString(attrOut.ServiceAttributes.ServiceArn))

	_, err = client.DeleteServiceAttributes(ctx, &servicediscovery.DeleteServiceAttributesInput{
		ServiceId:  aws.String(svcID),
		Attributes: []string{"k1"},
	})
	require.NoError(t, err)
	attrOut, err = client.GetServiceAttributes(ctx, &servicediscovery.GetServiceAttributesInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)
	_, gone := attrOut.ServiceAttributes.Attributes["k1"]
	assert.False(t, gone, "deleted attribute should be absent")
	assert.Equal(t, "v2", attrOut.ServiceAttributes.Attributes["k2"])
}

// TestCloudMap_InstanceHealthAndRevision exercises GetInstance,
// UpdateInstanceCustomHealthStatus, GetInstancesHealthStatus,
// DiscoverInstancesRevision and ListOperations.
func TestCloudMap_InstanceHealthAndRevision(t *testing.T) {
	client := cmClient()

	nsOut, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String("inst-health-ns"),
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, client, nsOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	}()

	// UpdateInstanceCustomHealthStatus only applies to a service whose health
	// is caller-reported, so the service carries a HealthCheckCustomConfig.
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:                    aws.String("health-svc"),
		NamespaceId:             aws.String(nsID),
		HealthCheckCustomConfig: &sdtypes.HealthCheckCustomConfig{FailureThreshold: aws.Int32(1)},
	})
	require.NoError(t, err)
	svcID := aws.ToString(svcOut.Service.Id)
	defer func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: aws.String(svcID)})
	}()

	// Revision starts at 0 (no instances yet).
	revOut, err := client.DiscoverInstancesRevision(ctx, &servicediscovery.DiscoverInstancesRevisionInput{
		NamespaceName: aws.String("inst-health-ns"),
		ServiceName:   aws.String("health-svc"),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), aws.ToInt64(revOut.InstancesRevision))

	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-1"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.0.0.5"},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId:  aws.String(svcID),
			InstanceId: aws.String("inst-1"),
		})
	}()

	// Revision bumped after a registration.
	revOut, err = client.DiscoverInstancesRevision(ctx, &servicediscovery.DiscoverInstancesRevisionInput{
		NamespaceName: aws.String("inst-health-ns"),
		ServiceName:   aws.String("health-svc"),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), aws.ToInt64(revOut.InstancesRevision))

	// GetInstance.
	instOut, err := client.GetInstance(ctx, &servicediscovery.GetInstanceInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-1"),
	})
	require.NoError(t, err)
	require.NotNil(t, instOut.Instance)
	assert.Equal(t, "inst-1", aws.ToString(instOut.Instance.Id))
	assert.Equal(t, "10.0.0.5", instOut.Instance.Attributes["AWS_INSTANCE_IPV4"])

	// Health defaults HEALTHY, then a custom status flips it.
	healthOut, err := client.GetInstancesHealthStatus(ctx, &servicediscovery.GetInstancesHealthStatusInput{
		ServiceId: aws.String(svcID),
	})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.HealthStatusHealthy, healthOut.Status["inst-1"])

	_, err = client.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId:  aws.String(svcID),
		InstanceId: aws.String("inst-1"),
		Status:     sdtypes.CustomHealthStatusUnhealthy,
	})
	require.NoError(t, err)
	healthOut, err = client.GetInstancesHealthStatus(ctx, &servicediscovery.GetInstancesHealthStatusInput{
		ServiceId: aws.String(svcID),
		Instances: []string{"inst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.HealthStatusUnhealthy, healthOut.Status["inst-1"])

	// Health updates do NOT bump the revision.
	revOut, err = client.DiscoverInstancesRevision(ctx, &servicediscovery.DiscoverInstancesRevisionInput{
		NamespaceName: aws.String("inst-health-ns"),
		ServiceName:   aws.String("health-svc"),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), aws.ToInt64(revOut.InstancesRevision))

	// ListOperations with a NAMESPACE_ID filter returns this namespace's ops.
	opsOut, err := client.ListOperations(ctx, &servicediscovery.ListOperationsInput{
		Filters: []sdtypes.OperationFilter{{
			Name:   sdtypes.OperationFilterNameNamespaceId,
			Values: []string{nsID},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, opsOut.Operations)
	for _, op := range opsOut.Operations {
		assert.NotEmpty(t, aws.ToString(op.Id))
	}
}
