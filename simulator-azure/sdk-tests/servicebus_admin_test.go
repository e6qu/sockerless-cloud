package azure_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/admin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sbAdminTransport struct {
	inner *http.Client
}

func TestServiceBusARM_AdvertisedEndpointAndConnectionStrings(t *testing.T) {
	rg := "sb-endpoint-rg"
	ensureRG(t, rg)
	namespace := "sdkshimns"
	createReq, _ := http.NewRequestWithContext(ctx, "PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.ServiceBus/namespaces/"+namespace+"?api-version=2022-10-01-preview",
		strings.NewReader(`{"location":"eastus","sku":{"name":"Standard","tier":"Standard"}}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", simARMBearer)
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)
	var ns map[string]any
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&ns))
	props := ns["properties"].(map[string]any)
	port := strings.TrimPrefix(baseURL, "http://127.0.0.1:")
	assert.Equal(t, fmt.Sprintf("https://%s.servicebus.shim.localhost:%s/", namespace, port), props["serviceBusEndpoint"])

	keysReq, _ := http.NewRequestWithContext(ctx, "POST",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.ServiceBus/namespaces/"+namespace+"/authorizationRules/RootManageSharedAccessKey/listKeys?api-version=2022-10-01-preview",
		nil)
	keysReq.Header.Set("Authorization", simARMBearer)
	keysResp, err := http.DefaultClient.Do(keysReq)
	require.NoError(t, err)
	defer keysResp.Body.Close()
	require.Equal(t, http.StatusOK, keysResp.StatusCode)
	var keys map[string]any
	require.NoError(t, json.NewDecoder(keysResp.Body).Decode(&keys))
	assert.Contains(t, keys["primaryConnectionString"], "Endpoint=sb://"+namespace+".servicebus.shim.localhost:"+port+"/")
}

func (t sbAdminTransport) Do(req *http.Request) (*http.Response, error) {
	rewritten := req.Clone(req.Context())
	u := *req.URL
	rewritten.Host = req.URL.Host
	u.Scheme = "http"
	u.Host = strings.TrimPrefix(baseURL, "http://")
	rewritten.URL = &u
	client := t.inner
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(rewritten)
}

func sbAdminClient(t *testing.T, namespace string) *admin.Client {
	t.Helper()
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	conn := messagingConnectionString(namespace,
		fmt.Sprintf("%s.servicebus.localhost:%s", namespace, port),
		serviceBusNamespaceKey(t, namespace))
	client, err := admin.NewClientFromConnectionString(conn, &admin.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: sbAdminTransport{}},
	})
	require.NoError(t, err)
	return client
}

func sbAdminRawGet(t *testing.T, namespace, target string) *http.Response {
	t.Helper()
	hostPort := strings.TrimPrefix(baseURL, "http://")
	_, port, ok := strings.Cut(hostPort, ":")
	require.True(t, ok, "baseURL must include a port: %s", baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+target, nil)
	require.NoError(t, err)
	req.Host = fmt.Sprintf("%s.servicebus.localhost:%s", namespace, port)
	req.Header.Set("Authorization", serviceBusHTTPAuthorization(t, namespace))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func assertSBAdminRawMissing(t *testing.T, namespace, target string) {
	t.Helper()
	resp := sbAdminRawGet(t, namespace, target)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/xml")
	assert.Contains(t, string(body), "<Error>")
	assert.Contains(t, string(body), "<Code>404</Code>")
}

func TestServiceBusAdmin_QueueSDKLifecycle(t *testing.T) {
	namespace := "sdk-admin-queue"
	client := sbAdminClient(t, namespace)
	ctx := context.Background()
	queueName := "q1"
	userMetadata := "queue metadata"
	maxSize := int32(1024)

	created, err := client.CreateQueue(ctx, queueName, &admin.CreateQueueOptions{
		Properties: &admin.QueueProperties{
			MaxSizeInMegabytes: &maxSize,
			UserMetadata:       &userMetadata,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, queueName, created.QueueName)
	require.NotNil(t, created.UserMetadata)
	assert.Equal(t, userMetadata, *created.UserMetadata)

	got, err := client.GetQueue(ctx, queueName, nil)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, queueName, got.QueueName)
	require.NotNil(t, got.MaxSizeInMegabytes)
	assert.Equal(t, maxSize, *got.MaxSizeInMegabytes)

	pager := client.NewListQueuesPager(&admin.ListQueuesOptions{MaxPageSize: 1})
	require.True(t, pager.More())
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Queues, 1)
	assert.Equal(t, queueName, page.Queues[0].QueueName)

	_, err = client.DeleteQueue(ctx, queueName, nil)
	require.NoError(t, err)
	assertSBAdminRawMissing(t, namespace, "/"+queueName)
	got, err = client.GetQueue(ctx, queueName, nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestServiceBusAdmin_QueueListPagingIsNamespaceScoped(t *testing.T) {
	ctx := context.Background()
	other := sbAdminClient(t, "sdk-admin-queue-other")
	target := sbAdminClient(t, "sdk-admin-queue-target")

	_, err := other.CreateQueue(ctx, "other-q1", nil)
	require.NoError(t, err)
	_, err = target.CreateQueue(ctx, "target-q1", nil)
	require.NoError(t, err)
	_, err = other.CreateQueue(ctx, "other-q2", nil)
	require.NoError(t, err)

	pager := target.NewListQueuesPager(&admin.ListQueuesOptions{MaxPageSize: 1})
	page, err := pager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, page.Queues, 1)
	assert.Equal(t, "target-q1", page.Queues[0].QueueName)
}

func TestServiceBusAdmin_TopicSubscriptionRuleSDKLifecycle(t *testing.T) {
	namespace := "sdk-admin-topic"
	client := sbAdminClient(t, namespace)
	ctx := context.Background()
	topicName := "topic1"
	subName := "sub1"
	ruleName := "important"

	createdTopic, err := client.CreateTopic(ctx, topicName, nil)
	require.NoError(t, err)
	assert.Equal(t, topicName, createdTopic.TopicName)

	gotTopic, err := client.GetTopic(ctx, topicName, nil)
	require.NoError(t, err)
	require.NotNil(t, gotTopic)
	assert.Equal(t, topicName, gotTopic.TopicName)

	topicPager := client.NewListTopicsPager(&admin.ListTopicsOptions{MaxPageSize: 1})
	topicPage, err := topicPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, topicPage.Topics, 1)
	assert.Equal(t, topicName, topicPage.Topics[0].TopicName)

	createdSub, err := client.CreateSubscription(ctx, topicName, subName, nil)
	require.NoError(t, err)
	assert.Equal(t, subName, createdSub.SubscriptionName)

	gotSub, err := client.GetSubscription(ctx, topicName, subName, nil)
	require.NoError(t, err)
	require.NotNil(t, gotSub)
	assert.Equal(t, subName, gotSub.SubscriptionName)

	subPager := client.NewListSubscriptionsPager(topicName, &admin.ListSubscriptionsOptions{MaxPageSize: 1})
	subPage, err := subPager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, subPage.Subscriptions, 1)
	assert.Equal(t, subName, subPage.Subscriptions[0].SubscriptionName)

	createdRule, err := client.CreateRule(ctx, topicName, subName, &admin.CreateRuleOptions{
		Name:   to.Ptr(ruleName),
		Filter: &admin.SQLFilter{Expression: "priority = 'high'"},
	})
	require.NoError(t, err)
	assert.Equal(t, ruleName, createdRule.Name)

	gotRule, err := client.GetRule(ctx, topicName, subName, ruleName, nil)
	require.NoError(t, err)
	require.NotNil(t, gotRule)
	assert.Equal(t, ruleName, gotRule.Name)

	rulePager := client.NewListRulesPager(topicName, subName, &admin.ListRulesOptions{MaxPageSize: 2})
	rulePage, err := rulePager.NextPage(ctx)
	require.NoError(t, err)
	require.Len(t, rulePage.Rules, 2)
	assert.ElementsMatch(t, []string{"$Default", ruleName}, []string{rulePage.Rules[0].Name, rulePage.Rules[1].Name})

	_, err = client.DeleteRule(ctx, topicName, subName, ruleName, nil)
	require.NoError(t, err)
	assertSBAdminRawMissing(t, namespace, "/"+topicName+"/Subscriptions/"+subName+"/Rules/"+ruleName)
	gotRule, err = client.GetRule(ctx, topicName, subName, ruleName, nil)
	require.NoError(t, err)
	assert.Nil(t, gotRule)

	_, err = client.DeleteSubscription(ctx, topicName, subName, nil)
	require.NoError(t, err)
	assertSBAdminRawMissing(t, namespace, "/"+topicName+"/Subscriptions/"+subName)
	gotSub, err = client.GetSubscription(ctx, topicName, subName, nil)
	require.NoError(t, err)
	assert.Nil(t, gotSub)

	_, err = client.DeleteTopic(ctx, topicName, nil)
	require.NoError(t, err)
	assertSBAdminRawMissing(t, namespace, "/"+topicName)
	gotTopic, err = client.GetTopic(ctx, topicName, nil)
	require.NoError(t, err)
	assert.Nil(t, gotTopic)
}
