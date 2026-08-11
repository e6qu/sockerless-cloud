package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/servicediscovery"
	sdtypes "github.com/aws/aws-sdk-go-v2/service/servicediscovery/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cmRawCall issues a signed awsJson1.1 Cloud Map request by hand so a test can
// inspect the exact members the simulator puts on the wire — the SDK silently
// drops members its model doesn't know, which is precisely what a wire-shape
// assertion needs to see.
func cmRawCall(t *testing.T, op string, payload map[string]any) (int, map[string]json.RawMessage) {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("X-Amz-Target", "Route53AutoNaming_v20170314."+op)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	signRawSigV4JSON(t, req, "servicediscovery", body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var wire map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&wire))
	return resp.StatusCode, wire
}

// cmAPIErrorCode extracts the modeled error code from a failed SDK call.
func cmAPIErrorCode(t *testing.T, err error) string {
	t.Helper()
	require.Error(t, err)
	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "expected smithy.APIError, got %v", err)
	return ae.ErrorCode()
}

// cmHTTPNamespace creates an HTTP namespace and registers its cleanup.
func cmHTTPNamespace(t *testing.T, client *servicediscovery.Client, name string) string {
	t.Helper()
	out, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String(name),
	})
	require.NoError(t, err)
	id := cmNamespaceIDFromOp(t, client, out.OperationId)
	t.Cleanup(func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(id)})
	})
	return id
}

// cmService creates a service in a namespace and registers its cleanup.
func cmService(t *testing.T, client *servicediscovery.Client, nsID, name string) sdtypes.Service {
	t.Helper()
	out, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String(name),
		NamespaceId: aws.String(nsID),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: out.Service.Id})
	})
	return *out.Service
}

// TestCloudMap_ListNamespaces exercises ListNamespaces: the NamespaceSummary
// shape (including ServiceCount), the TYPE / NAME / HTTP_NAME filters the ECS
// backend's network-state recovery relies on, and MaxResults/NextToken paging.
func TestCloudMap_ListNamespaces(t *testing.T) {
	client := cmClient()

	httpID := cmHTTPNamespace(t, client, "list-ns-http")
	privOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("list-ns-a.local"),
		Vpc:  aws.String("vpc-list-ns"),
	})
	require.NoError(t, err)
	privA := cmNamespaceIDFromOp(t, client, privOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(privA)})
	}()
	privOut2, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("list-ns-b.local"),
		Vpc:  aws.String("vpc-list-ns"),
	})
	require.NoError(t, err)
	privB := cmNamespaceIDFromOp(t, client, privOut2.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(privB)})
	}()

	// NAME filter selects exactly the named namespace, with the summary shape.
	byName, err := client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
		Filters: []sdtypes.NamespaceFilter{{
			Name:      sdtypes.NamespaceFilterNameName,
			Values:    []string{"list-ns-a.local"},
			Condition: sdtypes.FilterConditionEq,
		}},
	})
	require.NoError(t, err)
	require.Len(t, byName.Namespaces, 1)
	summary := byName.Namespaces[0]
	assert.Equal(t, privA, aws.ToString(summary.Id))
	assert.Equal(t, sdtypes.NamespaceTypeDnsPrivate, summary.Type)
	assert.NotEmpty(t, aws.ToString(summary.Arn))
	assert.NotNil(t, summary.CreateDate)
	assert.Equal(t, int32(0), aws.ToInt32(summary.ServiceCount))
	require.NotNil(t, summary.Properties)
	require.NotNil(t, summary.Properties.DnsProperties)
	assert.NotEmpty(t, aws.ToString(summary.Properties.DnsProperties.HostedZoneId))

	// A service in the namespace moves ServiceCount.
	cmService(t, client, privA, "list-ns-svc")
	byName, err = client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
		Filters: []sdtypes.NamespaceFilter{{
			Name:   sdtypes.NamespaceFilterNameName,
			Values: []string{"list-ns-a.local"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, byName.Namespaces, 1)
	assert.Equal(t, int32(1), aws.ToInt32(byName.Namespaces[0].ServiceCount))

	// TYPE filter keeps DNS_PRIVATE namespaces only.
	byType, err := client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
		Filters: []sdtypes.NamespaceFilter{{
			Name:      sdtypes.NamespaceFilterNameType,
			Values:    []string{string(sdtypes.NamespaceTypeDnsPrivate)},
			Condition: sdtypes.FilterConditionEq,
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, byType.Namespaces)
	ids := map[string]bool{}
	for _, ns := range byType.Namespaces {
		assert.Equal(t, sdtypes.NamespaceTypeDnsPrivate, ns.Type)
		ids[aws.ToString(ns.Id)] = true
	}
	assert.True(t, ids[privA] && ids[privB], "TYPE=DNS_PRIVATE must return both private namespaces")
	assert.False(t, ids[httpID], "TYPE=DNS_PRIVATE must exclude the HTTP namespace")

	// HTTP_NAME filter matches the HTTP namespace's HttpProperties.
	byHTTPName, err := client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
		Filters: []sdtypes.NamespaceFilter{{
			Name:   sdtypes.NamespaceFilterNameHttpName,
			Values: []string{"list-ns-http"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, byHTTPName.Namespaces, 1)
	assert.Equal(t, httpID, aws.ToString(byHTTPName.Namespaces[0].Id))

	// BEGINS_WITH on NAME matches both private namespaces; MaxResults pages them.
	firstPage, err := client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
		MaxResults: aws.Int32(1),
		Filters: []sdtypes.NamespaceFilter{{
			Name:      sdtypes.NamespaceFilterNameName,
			Values:    []string{"list-ns-"},
			Condition: sdtypes.FilterConditionBeginsWith,
		}},
	})
	require.NoError(t, err)
	require.Len(t, firstPage.Namespaces, 1)
	require.NotEmpty(t, aws.ToString(firstPage.NextToken))

	seen := map[string]bool{aws.ToString(firstPage.Namespaces[0].Id): true}
	token := firstPage.NextToken
	for token != nil {
		page, err := client.ListNamespaces(ctx, &servicediscovery.ListNamespacesInput{
			MaxResults: aws.Int32(1),
			NextToken:  token,
			Filters: []sdtypes.NamespaceFilter{{
				Name:      sdtypes.NamespaceFilterNameName,
				Values:    []string{"list-ns-"},
				Condition: sdtypes.FilterConditionBeginsWith,
			}},
		})
		require.NoError(t, err)
		for _, ns := range page.Namespaces {
			seen[aws.ToString(ns.Id)] = true
		}
		token = page.NextToken
	}
	assert.True(t, seen[privA] && seen[privB] && seen[httpID],
		"paging through BEGINS_WITH must yield every matching namespace")
}

// TestCloudMap_ListInstances exercises ListInstances: per-service attribute
// isolation (the same instance ID may back several services), paging, and the
// ServiceNotFound error for an unknown service.
func TestCloudMap_ListInstances(t *testing.T) {
	client := cmClient()

	nsID := cmHTTPNamespace(t, client, "list-inst-ns")
	svcA := cmService(t, client, nsID, "list-inst-a")
	svcB := cmService(t, client, nsID, "list-inst-b")

	// The same instance ID registered under two services carries different
	// attributes in each registration.
	for _, reg := range []struct {
		svc  *string
		ip   string
		role string
	}{
		{svcA.Id, "10.1.0.1", "alpha"},
		{svcB.Id, "10.1.0.2", "beta"},
	} {
		_, err := client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
			ServiceId:  reg.svc,
			InstanceId: aws.String("shared-inst"),
			Attributes: map[string]string{"AWS_INSTANCE_IPV4": reg.ip, "role": reg.role},
		})
		require.NoError(t, err)
		svcID := reg.svc
		defer func() {
			_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
				ServiceId:  svcID,
				InstanceId: aws.String("shared-inst"),
			})
		}()
	}

	listA, err := client.ListInstances(ctx, &servicediscovery.ListInstancesInput{ServiceId: svcA.Id})
	require.NoError(t, err)
	require.Len(t, listA.Instances, 1)
	assert.Equal(t, "shared-inst", aws.ToString(listA.Instances[0].Id))
	assert.Equal(t, "10.1.0.1", listA.Instances[0].Attributes["AWS_INSTANCE_IPV4"])
	assert.Equal(t, "alpha", listA.Instances[0].Attributes["role"])

	listB, err := client.ListInstances(ctx, &servicediscovery.ListInstancesInput{ServiceId: svcB.Id})
	require.NoError(t, err)
	require.Len(t, listB.Instances, 1)
	assert.Equal(t, "10.1.0.2", listB.Instances[0].Attributes["AWS_INSTANCE_IPV4"])
	assert.Equal(t, "beta", listB.Instances[0].Attributes["role"])

	// A second instance under svcA lets MaxResults page the list.
	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  svcA.Id,
		InstanceId: aws.String("second-inst"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.1.0.3"},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId:  svcA.Id,
			InstanceId: aws.String("second-inst"),
		})
	}()

	page, err := client.ListInstances(ctx, &servicediscovery.ListInstancesInput{
		ServiceId:  svcA.Id,
		MaxResults: aws.Int32(1),
	})
	require.NoError(t, err)
	require.Len(t, page.Instances, 1)
	require.NotEmpty(t, aws.ToString(page.NextToken))
	rest, err := client.ListInstances(ctx, &servicediscovery.ListInstancesInput{
		ServiceId:  svcA.Id,
		MaxResults: aws.Int32(1),
		NextToken:  page.NextToken,
	})
	require.NoError(t, err)
	require.Len(t, rest.Instances, 1)
	assert.NotEqual(t, aws.ToString(page.Instances[0].Id), aws.ToString(rest.Instances[0].Id))
	assert.Empty(t, aws.ToString(rest.NextToken), "the last page carries no token")

	_, err = client.ListInstances(ctx, &servicediscovery.ListInstancesInput{
		ServiceId: aws.String("srv-does-not-exist"),
	})
	assert.Equal(t, "ServiceNotFound", cmAPIErrorCode(t, err))
}

// TestCloudMap_ResourceTags exercises TagResource, UntagResource and
// ListTagsForResource against namespaces and services, including the tags a
// Create* request carries — the path the ECS backend's network-state recovery
// uses to find its namespace by the sockerless:network-id tag.
func TestCloudMap_ResourceTags(t *testing.T) {
	client := cmClient()

	nsOut, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("tagged-ns.local"),
		Vpc:  aws.String("vpc-tagged"),
		Tags: []sdtypes.Tag{
			{Key: aws.String("sockerless:network-id"), Value: aws.String("net-abc123")},
			{Key: aws.String("env"), Value: aws.String("test")},
		},
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, client, nsOut.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	}()

	got, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(nsID)})
	require.NoError(t, err)
	nsArn := aws.ToString(got.Namespace.Arn)
	require.NotEmpty(t, nsArn)

	tagsOut, err := client.ListTagsForResource(ctx, &servicediscovery.ListTagsForResourceInput{
		ResourceARN: aws.String(nsArn),
	})
	require.NoError(t, err)
	tags := map[string]string{}
	for _, tag := range tagsOut.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "net-abc123", tags["sockerless:network-id"],
		"the tags CreatePrivateDnsNamespace carried must be readable back")
	assert.Equal(t, "test", tags["env"])

	_, err = client.TagResource(ctx, &servicediscovery.TagResourceInput{
		ResourceARN: aws.String(nsArn),
		Tags:        []sdtypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)
	tagsOut, err = client.ListTagsForResource(ctx, &servicediscovery.ListTagsForResourceInput{
		ResourceARN: aws.String(nsArn),
	})
	require.NoError(t, err)
	require.Len(t, tagsOut.Tags, 3)

	_, err = client.UntagResource(ctx, &servicediscovery.UntagResourceInput{
		ResourceARN: aws.String(nsArn),
		TagKeys:     []string{"env"},
	})
	require.NoError(t, err)
	tagsOut, err = client.ListTagsForResource(ctx, &servicediscovery.ListTagsForResourceInput{
		ResourceARN: aws.String(nsArn),
	})
	require.NoError(t, err)
	remaining := map[string]string{}
	for _, tag := range tagsOut.Tags {
		remaining[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.NotContains(t, remaining, "env")
	assert.Equal(t, "platform", remaining["owner"])

	// A service carries its own tags on its own ARN.
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("tagged-svc"),
		NamespaceId: aws.String(nsID),
		Tags:        []sdtypes.Tag{{Key: aws.String("tier"), Value: aws.String("web")}},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: svcOut.Service.Id})
	}()
	svcTags, err := client.ListTagsForResource(ctx, &servicediscovery.ListTagsForResourceInput{
		ResourceARN: svcOut.Service.Arn,
	})
	require.NoError(t, err)
	require.Len(t, svcTags.Tags, 1)
	assert.Equal(t, "tier", aws.ToString(svcTags.Tags[0].Key))
	assert.Equal(t, "web", aws.ToString(svcTags.Tags[0].Value))

	// An ARN that addresses nothing is a ResourceNotFoundException.
	_, err = client.ListTagsForResource(ctx, &servicediscovery.ListTagsForResourceInput{
		ResourceARN: aws.String("arn:aws:servicediscovery:us-east-1:000000000000:namespace/ns-missing"),
	})
	assert.Equal(t, "ResourceNotFoundException", cmAPIErrorCode(t, err))

	_, err = client.TagResource(ctx, &servicediscovery.TagResourceInput{
		ResourceARN: aws.String("arn:aws:servicediscovery:us-east-1:000000000000:service/srv-missing"),
		Tags:        []sdtypes.Tag{{Key: aws.String("k"), Value: aws.String("v")}},
	})
	assert.Equal(t, "ResourceNotFoundException", cmAPIErrorCode(t, err))
}

// TestCloudMap_OperationsLifecycle exercises GetOperation and ListOperations
// over the operations every mutating Cloud Map call records — including the
// instance operations a caller polls after RegisterInstance.
func TestCloudMap_OperationsLifecycle(t *testing.T) {
	client := cmClient()

	nsID := cmHTTPNamespace(t, client, "ops-ns")
	svc := cmService(t, client, nsID, "ops-svc")

	regOut, err := client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  svc.Id,
		InstanceId: aws.String("ops-inst"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.2.0.1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(regOut.OperationId))

	regOp, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: regOut.OperationId,
	})
	require.NoError(t, err)
	require.NotNil(t, regOp.Operation)
	assert.Equal(t, sdtypes.OperationTypeRegisterInstance, regOp.Operation.Type)
	assert.Equal(t, sdtypes.OperationStatusSuccess, regOp.Operation.Status)
	assert.NotNil(t, regOp.Operation.CreateDate)
	assert.NotNil(t, regOp.Operation.UpdateDate)
	assert.Equal(t, "ops-inst", regOp.Operation.Targets[string(sdtypes.OperationTargetTypeInstance)])
	assert.Equal(t, aws.ToString(svc.Id), regOp.Operation.Targets[string(sdtypes.OperationTargetTypeService)])
	assert.Equal(t, nsID, regOp.Operation.Targets[string(sdtypes.OperationTargetTypeNamespace)])

	deregOut, err := client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
		ServiceId:  svc.Id,
		InstanceId: aws.String("ops-inst"),
	})
	require.NoError(t, err)
	deregOp, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: deregOut.OperationId,
	})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.OperationTypeDeregisterInstance, deregOp.Operation.Type)

	// An unknown operation ID is an error, not an invented SUCCESS.
	_, err = client.GetOperation(ctx, &servicediscovery.GetOperationInput{
		OperationId: aws.String("op-does-not-exist"),
	})
	assert.Equal(t, "OperationNotFound", cmAPIErrorCode(t, err))

	// The service's operations are filterable and pageable.
	svcOps, err := client.ListOperations(ctx, &servicediscovery.ListOperationsInput{
		Filters: []sdtypes.OperationFilter{{
			Name:   sdtypes.OperationFilterNameServiceId,
			Values: []string{aws.ToString(svc.Id)},
		}},
	})
	require.NoError(t, err)
	require.Len(t, svcOps.Operations, 2, "register + deregister")

	firstPage, err := client.ListOperations(ctx, &servicediscovery.ListOperationsInput{
		MaxResults: aws.Int32(1),
		Filters: []sdtypes.OperationFilter{{
			Name:   sdtypes.OperationFilterNameServiceId,
			Values: []string{aws.ToString(svc.Id)},
		}},
	})
	require.NoError(t, err)
	require.Len(t, firstPage.Operations, 1)
	require.NotEmpty(t, aws.ToString(firstPage.NextToken))
	secondPage, err := client.ListOperations(ctx, &servicediscovery.ListOperationsInput{
		MaxResults: aws.Int32(1),
		NextToken:  firstPage.NextToken,
		Filters: []sdtypes.OperationFilter{{
			Name:   sdtypes.OperationFilterNameServiceId,
			Values: []string{aws.ToString(svc.Id)},
		}},
	})
	require.NoError(t, err)
	require.Len(t, secondPage.Operations, 1)
	assert.NotEqual(t, aws.ToString(firstPage.Operations[0].Id), aws.ToString(secondPage.Operations[0].Id))

	// Deleting a namespace records a DELETE_NAMESPACE operation.
	throwaway := cmHTTPNamespace(t, client, "ops-delete-ns")
	delOut, err := client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{
		Id: aws.String(throwaway),
	})
	require.NoError(t, err)
	delOp, err := client.GetOperation(ctx, &servicediscovery.GetOperationInput{OperationId: delOut.OperationId})
	require.NoError(t, err)
	assert.Equal(t, sdtypes.OperationTypeDeleteNamespace, delOp.Operation.Type)
	assert.Equal(t, throwaway, delOp.Operation.Targets[string(sdtypes.OperationTargetTypeNamespace)])
}

// TestCloudMap_DiscoverInstancesFiltering exercises DiscoverInstances: the
// InstancesRevision it returns alongside the instances, the QueryParameters /
// OptionalParameters attribute filters, MaxResults, and the custom health
// status a caller sets through UpdateInstanceCustomHealthStatus.
func TestCloudMap_DiscoverInstancesFiltering(t *testing.T) {
	client := cmClient()

	nsID := cmHTTPNamespace(t, client, "discover-filter-ns")
	svcOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:                    aws.String("discover-filter-svc"),
		NamespaceId:             aws.String(nsID),
		HealthCheckCustomConfig: &sdtypes.HealthCheckCustomConfig{FailureThreshold: aws.Int32(1)},
	})
	require.NoError(t, err)
	svcID := svcOut.Service.Id
	defer func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: svcID})
	}()

	for _, reg := range []struct{ id, ip, stage string }{
		{"disc-1", "10.3.0.1", "prod"},
		{"disc-2", "10.3.0.2", "dev"},
	} {
		_, err := client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
			ServiceId:  svcID,
			InstanceId: aws.String(reg.id),
			Attributes: map[string]string{"AWS_INSTANCE_IPV4": reg.ip, "stage": reg.stage},
		})
		require.NoError(t, err)
		id := reg.id
		defer func() {
			_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
				ServiceId:  svcID,
				InstanceId: aws.String(id),
			})
		}()
	}

	all, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-filter-ns"),
		ServiceName:   aws.String("discover-filter-svc"),
		HealthStatus:  sdtypes.HealthStatusFilterAll,
	})
	require.NoError(t, err)
	require.Len(t, all.Instances, 2)
	assert.Equal(t, int64(2), aws.ToInt64(all.InstancesRevision),
		"DiscoverInstances reports the service's instance-list revision")

	// QueryParameters keep only instances carrying the attribute value.
	prod, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName:   aws.String("discover-filter-ns"),
		ServiceName:     aws.String("discover-filter-svc"),
		HealthStatus:    sdtypes.HealthStatusFilterAll,
		QueryParameters: map[string]string{"stage": "prod"},
	})
	require.NoError(t, err)
	require.Len(t, prod.Instances, 1)
	assert.Equal(t, "disc-1", aws.ToString(prod.Instances[0].InstanceId))

	// OptionalParameters narrow the result only when something matches.
	optional, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName:      aws.String("discover-filter-ns"),
		ServiceName:        aws.String("discover-filter-svc"),
		HealthStatus:       sdtypes.HealthStatusFilterAll,
		OptionalParameters: map[string]string{"stage": "dev"},
	})
	require.NoError(t, err)
	require.Len(t, optional.Instances, 1)
	assert.Equal(t, "disc-2", aws.ToString(optional.Instances[0].InstanceId))

	unmatched, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName:      aws.String("discover-filter-ns"),
		ServiceName:        aws.String("discover-filter-svc"),
		HealthStatus:       sdtypes.HealthStatusFilterAll,
		OptionalParameters: map[string]string{"stage": "staging"},
	})
	require.NoError(t, err)
	assert.Len(t, unmatched.Instances, 2, "unmatched OptionalParameters keep the full result")

	capped, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-filter-ns"),
		ServiceName:   aws.String("discover-filter-svc"),
		HealthStatus:  sdtypes.HealthStatusFilterAll,
		MaxResults:    aws.Int32(1),
	})
	require.NoError(t, err)
	assert.Len(t, capped.Instances, 1)

	// A custom health status is what DiscoverInstances reports.
	_, err = client.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId:  svcID,
		InstanceId: aws.String("disc-1"),
		Status:     sdtypes.CustomHealthStatusUnhealthy,
	})
	require.NoError(t, err)

	afterUpdate, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-filter-ns"),
		ServiceName:   aws.String("discover-filter-svc"),
		HealthStatus:  sdtypes.HealthStatusFilterAll,
	})
	require.NoError(t, err)
	health := map[string]sdtypes.HealthStatus{}
	for _, inst := range afterUpdate.Instances {
		health[aws.ToString(inst.InstanceId)] = inst.HealthStatus
	}
	assert.Equal(t, sdtypes.HealthStatusUnhealthy, health["disc-1"])
	assert.Equal(t, sdtypes.HealthStatusHealthy, health["disc-2"])

	healthyOnly, err := client.DiscoverInstances(ctx, &servicediscovery.DiscoverInstancesInput{
		NamespaceName: aws.String("discover-filter-ns"),
		ServiceName:   aws.String("discover-filter-svc"),
		HealthStatus:  sdtypes.HealthStatusFilterHealthy,
	})
	require.NoError(t, err)
	require.Len(t, healthyOnly.Instances, 1)
	assert.Equal(t, "disc-2", aws.ToString(healthyOnly.Instances[0].InstanceId))
}

// TestCloudMap_UniquenessAndCustomHealthErrors covers the modeled errors real
// Cloud Map raises for duplicate names and for a custom health update against
// a service that has no HealthCheckCustomConfig.
func TestCloudMap_UniquenessAndCustomHealthErrors(t *testing.T) {
	client := cmClient()

	nsID := cmHTTPNamespace(t, client, "unique-ns")
	_, err := client.CreateHttpNamespace(ctx, &servicediscovery.CreateHttpNamespaceInput{
		Name: aws.String("unique-ns"),
	})
	assert.Equal(t, "NamespaceAlreadyExists", cmAPIErrorCode(t, err))

	cmService(t, client, nsID, "unique-svc")
	_, err = client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:        aws.String("unique-svc"),
		NamespaceId: aws.String(nsID),
	})
	assert.Equal(t, "ServiceAlreadyExists", cmAPIErrorCode(t, err))

	// The same service name in a different namespace is fine.
	otherNs := cmHTTPNamespace(t, client, "unique-ns-other")
	cmService(t, client, otherNs, "unique-svc")

	// A service without HealthCheckCustomConfig can't take a custom status.
	plain := cmService(t, client, nsID, "plain-health-svc")
	_, err = client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  plain.Id,
		InstanceId: aws.String("plain-inst"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.4.0.1"},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId:  plain.Id,
			InstanceId: aws.String("plain-inst"),
		})
	}()
	_, err = client.UpdateInstanceCustomHealthStatus(ctx, &servicediscovery.UpdateInstanceCustomHealthStatusInput{
		ServiceId:  plain.Id,
		InstanceId: aws.String("plain-inst"),
		Status:     sdtypes.CustomHealthStatusUnhealthy,
	})
	assert.Equal(t, "CustomHealthNotFound", cmAPIErrorCode(t, err))

	// A service configured for custom health reports AWS Cloud Map's fixed
	// threshold of one and accepts the status update.
	customOut, err := client.CreateService(ctx, &servicediscovery.CreateServiceInput{
		Name:                    aws.String("custom-health-svc"),
		NamespaceId:             aws.String(nsID),
		HealthCheckCustomConfig: &sdtypes.HealthCheckCustomConfig{FailureThreshold: aws.Int32(2)},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeleteService(ctx, &servicediscovery.DeleteServiceInput{Id: customOut.Service.Id})
	}()
	require.NotNil(t, customOut.Service.HealthCheckCustomConfig)
	assert.Equal(t, int32(1), aws.ToInt32(customOut.Service.HealthCheckCustomConfig.FailureThreshold))

	gotSvc, err := client.GetService(ctx, &servicediscovery.GetServiceInput{Id: customOut.Service.Id})
	require.NoError(t, err)
	require.NotNil(t, gotSvc.Service.HealthCheckCustomConfig,
		"GetService must return the HealthCheckCustomConfig CreateService accepted")
	assert.Equal(t, int32(1), aws.ToInt32(gotSvc.Service.HealthCheckCustomConfig.FailureThreshold))

	listed, err := client.ListServices(ctx, &servicediscovery.ListServicesInput{
		Filters: []sdtypes.ServiceFilter{{
			Name:   sdtypes.ServiceFilterNameNamespaceId,
			Values: []string{nsID},
		}},
	})
	require.NoError(t, err)
	var customSummary *sdtypes.ServiceSummary
	for i, svc := range listed.Services {
		if aws.ToString(svc.Id) == aws.ToString(customOut.Service.Id) {
			customSummary = &listed.Services[i]
		}
	}
	require.NotNil(t, customSummary, "ListServices must include the service")
	require.NotNil(t, customSummary.HealthCheckCustomConfig)
	assert.Equal(t, int32(1), aws.ToInt32(customSummary.HealthCheckCustomConfig.FailureThreshold))
}

// TestCloudMap_PrivateNamespaceSOARoundTrip proves the SOA a client configures
// on a private DNS namespace survives into GetNamespace.
func TestCloudMap_PrivateNamespaceSOARoundTrip(t *testing.T) {
	client := cmClient()

	out, err := client.CreatePrivateDnsNamespace(ctx, &servicediscovery.CreatePrivateDnsNamespaceInput{
		Name: aws.String("soa-ns.local"),
		Vpc:  aws.String("vpc-soa"),
		Properties: &sdtypes.PrivateDnsNamespaceProperties{
			DnsProperties: &sdtypes.PrivateDnsPropertiesMutable{
				SOA: &sdtypes.SOA{TTL: aws.Int64(60)},
			},
		},
	})
	require.NoError(t, err)
	nsID := cmNamespaceIDFromOp(t, client, out.OperationId)
	defer func() {
		_, _ = client.DeleteNamespace(ctx, &servicediscovery.DeleteNamespaceInput{Id: aws.String(nsID)})
	}()

	got, err := client.GetNamespace(ctx, &servicediscovery.GetNamespaceInput{Id: aws.String(nsID)})
	require.NoError(t, err)
	require.NotNil(t, got.Namespace.Properties)
	require.NotNil(t, got.Namespace.Properties.DnsProperties)
	require.NotNil(t, got.Namespace.Properties.DnsProperties.SOA,
		"the SOA CreatePrivateDnsNamespace accepted must be readable back")
	assert.Equal(t, int64(60), aws.ToInt64(got.Namespace.Properties.DnsProperties.SOA.TTL))
}

// TestCloudMap_WireShape inspects the raw awsJson1.1 bodies: the responses must
// carry exactly the modeled members — no simulator bookkeeping — and a private
// namespace created without the required Vpc must be rejected.
func TestCloudMap_WireShape(t *testing.T) {
	client := cmClient()

	nsID := cmHTTPNamespace(t, client, "wire-shape-ns")
	svc := cmService(t, client, nsID, "wire-shape-svc")
	_, err := client.RegisterInstance(ctx, &servicediscovery.RegisterInstanceInput{
		ServiceId:  svc.Id,
		InstanceId: aws.String("wire-inst"),
		Attributes: map[string]string{"AWS_INSTANCE_IPV4": "10.5.0.1"},
	})
	require.NoError(t, err)
	defer func() {
		_, _ = client.DeregisterInstance(ctx, &servicediscovery.DeregisterInstanceInput{
			ServiceId:  svc.Id,
			InstanceId: aws.String("wire-inst"),
		})
	}()

	status, wire := cmRawCall(t, "GetNamespace", map[string]any{"Id": nsID})
	require.Equal(t, http.StatusOK, status)
	var ns map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wire["Namespace"], &ns))
	assert.Contains(t, ns, "ServiceCount")
	assert.NotContains(t, ns, "DockerNetworkName",
		"the Docker network backing private DNS is a simulator detail, not a Namespace member")

	status, wire = cmRawCall(t, "ListInstances", map[string]any{"ServiceId": aws.ToString(svc.Id)})
	require.Equal(t, http.StatusOK, status)
	var instances []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(wire["Instances"], &instances))
	require.Len(t, instances, 1)
	assert.Contains(t, instances[0], "Id")
	assert.Contains(t, instances[0], "Attributes")
	assert.NotContains(t, instances[0], "ServiceId")
	assert.NotContains(t, instances[0], "CustomHealthStatus")

	// Vpc is required on a private DNS namespace.
	status, wire = cmRawCall(t, "CreatePrivateDnsNamespace", map[string]any{"Name": "wire-no-vpc.local"})
	require.Equal(t, http.StatusBadRequest, status)
	var errType string
	require.NoError(t, json.Unmarshal(wire["__type"], &errType))
	assert.Contains(t, errType, "InvalidInput")
}
