package gcp_sdk_test

import (
	"encoding/json"
	"net/http"
	"testing"

	eventarc "cloud.google.com/go/eventarc/apiv1"
	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	locationpb "google.golang.org/genproto/googleapis/cloud/location"
)

func eventarcClient(t *testing.T) *eventarc.Client {
	t.Helper()
	client, err := eventarc.NewRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestEventarc_TriggerLifecycleSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	name := parent + "/triggers/sdk-trigger"

	create, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "sdk-trigger",
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{{
				Attribute: "type",
				Value:     "google.cloud.pubsub.topic.v1.messagePublished",
			}},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_CloudRun{
					CloudRun: &eventarcpb.CloudRun{Service: "svc", Region: "us-central1"},
				},
			},
			Transport: &eventarcpb.Transport{
				Intermediary: &eventarcpb.Transport_Pubsub{
					Pubsub: &eventarcpb.Pubsub{Topic: "projects/test-project/topics/eventarc-topic"},
				},
			},
			Labels: map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	created, err := create.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, name, created.GetName())
	assert.Equal(t, "test", created.GetLabels()["env"])
	t.Cleanup(func() {
		op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
		if err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	got, err := client.GetTrigger(ctx, &eventarcpb.GetTriggerRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, name, got.GetName())
	assert.Equal(t, "svc", got.GetDestination().GetCloudRun().GetService())

	rawResp, err := http.Get(baseURL + "/v1/" + name)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rawResp.StatusCode)
	var raw map[string]any
	require.NoError(t, json.NewDecoder(rawResp.Body).Decode(&raw))
	rawResp.Body.Close()
	assertProtoJSONTimestamp(t, raw["createTime"].(string))
	assertProtoJSONTimestamp(t, raw["updateTime"].(string))

	iter := client.ListTriggers(ctx, &eventarcpb.ListTriggersRequest{Parent: parent})
	listed, err := iter.Next()
	require.NoError(t, err)
	assert.Equal(t, name, listed.GetName())
	_, err = iter.Next()
	assert.ErrorIs(t, err, iterator.Done)
}

func TestEventarc_ChannelProviderConnectionSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	channelName := parent + "/channels/sdk-channel"
	connectionName := parent + "/channelConnections/sdk-connection"

	providers := client.ListProviders(ctx, &eventarcpb.ListProvidersRequest{Parent: parent})
	provider, err := providers.Next()
	require.NoError(t, err)
	assert.Equal(t, parent+"/providers/cloud.pubsub", provider.GetName())
	require.NotEmpty(t, provider.GetEventTypes())

	gotProvider, err := client.GetProvider(ctx, &eventarcpb.GetProviderRequest{Name: parent + "/providers/cloud.pubsub"})
	require.NoError(t, err)
	assert.Equal(t, "Cloud Pub/Sub", gotProvider.GetDisplayName())

	createChannel, err := client.CreateChannel(ctx, &eventarcpb.CreateChannelRequest{
		Parent:    parent,
		ChannelId: "sdk-channel",
		Channel: &eventarcpb.Channel{
			Provider: parent + "/providers/cloud.pubsub",
			Transport: &eventarcpb.Channel_PubsubTopic{
				PubsubTopic: "projects/test-project/topics/sdk-channel-topic",
			},
			Labels: map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	channel, err := createChannel.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, channelName, channel.GetName())
	assert.Equal(t, eventarcpb.Channel_ACTIVE, channel.GetState())
	require.NotEmpty(t, channel.GetActivationToken())
	t.Cleanup(func() {
		op, err := client.DeleteChannel(ctx, &eventarcpb.DeleteChannelRequest{Name: channelName})
		if err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotChannel, err := client.GetChannel(ctx, &eventarcpb.GetChannelRequest{Name: channelName})
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/topics/sdk-channel-topic", gotChannel.GetPubsubTopic())

	channels := client.ListChannels(ctx, &eventarcpb.ListChannelsRequest{Parent: parent})
	listedChannel, err := channels.Next()
	require.NoError(t, err)
	assert.Equal(t, channelName, listedChannel.GetName())
	_, err = channels.Next()
	assert.ErrorIs(t, err, iterator.Done)

	createConnection, err := client.CreateChannelConnection(ctx, &eventarcpb.CreateChannelConnectionRequest{
		Parent:              parent,
		ChannelConnectionId: "sdk-connection",
		ChannelConnection: &eventarcpb.ChannelConnection{
			Channel:         channelName,
			ActivationToken: channel.GetActivationToken(),
			Labels:          map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	connection, err := createConnection.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, connectionName, connection.GetName())
	assert.Equal(t, channelName, connection.GetChannel())
	t.Cleanup(func() {
		op, err := client.DeleteChannelConnection(ctx, &eventarcpb.DeleteChannelConnectionRequest{Name: connectionName})
		if err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotConnection, err := client.GetChannelConnection(ctx, &eventarcpb.GetChannelConnectionRequest{Name: connectionName})
	require.NoError(t, err)
	assert.Equal(t, channelName, gotConnection.GetChannel())

	connections := client.ListChannelConnections(ctx, &eventarcpb.ListChannelConnectionsRequest{Parent: parent})
	listedConnection, err := connections.Next()
	require.NoError(t, err)
	assert.Equal(t, connectionName, listedConnection.GetName())
	_, err = connections.Next()
	assert.ErrorIs(t, err, iterator.Done)
}

func TestEventarc_MessageBusAndEnrollmentSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	busName := parent + "/messageBuses/sdk-bus"
	enrollmentName := parent + "/enrollments/sdk-enrollment"

	createBus, err := client.CreateMessageBus(ctx, &eventarcpb.CreateMessageBusRequest{
		Parent:       parent,
		MessageBusId: "sdk-bus",
		MessageBus: &eventarcpb.MessageBus{
			DisplayName: "SDK Bus",
			Labels:      map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	bus, err := createBus.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, busName, bus.GetName())
	t.Cleanup(func() {
		if op, err := client.DeleteMessageBus(ctx, &eventarcpb.DeleteMessageBusRequest{Name: busName}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotBus, err := client.GetMessageBus(ctx, &eventarcpb.GetMessageBusRequest{Name: busName})
	require.NoError(t, err)
	assert.Equal(t, "SDK Bus", gotBus.GetDisplayName())

	updateBus, err := client.UpdateMessageBus(ctx, &eventarcpb.UpdateMessageBusRequest{
		MessageBus: &eventarcpb.MessageBus{Name: busName, DisplayName: "SDK Bus v2"},
	})
	require.NoError(t, err)
	updatedBus, err := updateBus.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SDK Bus v2", updatedBus.GetDisplayName())

	buses := client.ListMessageBuses(ctx, &eventarcpb.ListMessageBusesRequest{Parent: parent})
	listedBus, err := buses.Next()
	require.NoError(t, err)
	assert.Equal(t, busName, listedBus.GetName())

	createEnrollment, err := client.CreateEnrollment(ctx, &eventarcpb.CreateEnrollmentRequest{
		Parent:       parent,
		EnrollmentId: "sdk-enrollment",
		Enrollment: &eventarcpb.Enrollment{
			CelMatch:    "message.type == 'google.cloud.pubsub.topic.v1.messagePublished'",
			MessageBus:  busName,
			Destination: parent + "/pipelines/sdk-pipeline",
			Labels:      map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	enrollment, err := createEnrollment.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, enrollmentName, enrollment.GetName())
	assert.Equal(t, busName, enrollment.GetMessageBus())
	t.Cleanup(func() {
		if op, err := client.DeleteEnrollment(ctx, &eventarcpb.DeleteEnrollmentRequest{Name: enrollmentName}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotEnrollment, err := client.GetEnrollment(ctx, &eventarcpb.GetEnrollmentRequest{Name: enrollmentName})
	require.NoError(t, err)
	assert.Equal(t, parent+"/pipelines/sdk-pipeline", gotEnrollment.GetDestination())

	enrollments := client.ListEnrollments(ctx, &eventarcpb.ListEnrollmentsRequest{Parent: parent})
	listedEnrollment, err := enrollments.Next()
	require.NoError(t, err)
	assert.Equal(t, enrollmentName, listedEnrollment.GetName())

	// messageBuses:listEnrollments returns the names of enrollments bound to the bus.
	busEnrollments := client.ListMessageBusEnrollments(ctx, &eventarcpb.ListMessageBusEnrollmentsRequest{Parent: busName})
	firstName, err := busEnrollments.Next()
	require.NoError(t, err)
	assert.Equal(t, enrollmentName, firstName)
	_, err = busEnrollments.Next()
	assert.ErrorIs(t, err, iterator.Done)
}

func TestEventarc_PipelineSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	pipelineName := parent + "/pipelines/sdk-pipeline"

	createPipeline, err := client.CreatePipeline(ctx, &eventarcpb.CreatePipelineRequest{
		Parent:     parent,
		PipelineId: "sdk-pipeline",
		Pipeline: &eventarcpb.Pipeline{
			DisplayName: "SDK Pipeline",
			Destinations: []*eventarcpb.Pipeline_Destination{{
				DestinationDescriptor: &eventarcpb.Pipeline_Destination_HttpEndpoint_{
					HttpEndpoint: &eventarcpb.Pipeline_Destination_HttpEndpoint{
						Uri: "https://svc.us-central1.p.local:8080/route",
					},
				},
			}},
			Labels: map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	pipeline, err := createPipeline.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, pipelineName, pipeline.GetName())
	t.Cleanup(func() {
		if op, err := client.DeletePipeline(ctx, &eventarcpb.DeletePipelineRequest{Name: pipelineName}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotPipeline, err := client.GetPipeline(ctx, &eventarcpb.GetPipelineRequest{Name: pipelineName})
	require.NoError(t, err)
	require.NotEmpty(t, gotPipeline.GetDestinations())
	assert.Equal(t, "https://svc.us-central1.p.local:8080/route",
		gotPipeline.GetDestinations()[0].GetHttpEndpoint().GetUri())

	updatePipeline, err := client.UpdatePipeline(ctx, &eventarcpb.UpdatePipelineRequest{
		Pipeline: &eventarcpb.Pipeline{Name: pipelineName, DisplayName: "SDK Pipeline v2"},
	})
	require.NoError(t, err)
	updatedPipeline, err := updatePipeline.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SDK Pipeline v2", updatedPipeline.GetDisplayName())

	pipelines := client.ListPipelines(ctx, &eventarcpb.ListPipelinesRequest{Parent: parent})
	listed, err := pipelines.Next()
	require.NoError(t, err)
	assert.Equal(t, pipelineName, listed.GetName())
}

func TestEventarc_GoogleApiSourceSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	sourceName := parent + "/googleApiSources/sdk-source"

	createSource, err := client.CreateGoogleApiSource(ctx, &eventarcpb.CreateGoogleApiSourceRequest{
		Parent:            parent,
		GoogleApiSourceId: "sdk-source",
		GoogleApiSource: &eventarcpb.GoogleApiSource{
			DisplayName: "SDK Source",
			Destination: parent + "/messageBuses/sdk-bus",
			Labels:      map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err)
	source, err := createSource.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, sourceName, source.GetName())
	t.Cleanup(func() {
		if op, err := client.DeleteGoogleApiSource(ctx, &eventarcpb.DeleteGoogleApiSourceRequest{Name: sourceName}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	gotSource, err := client.GetGoogleApiSource(ctx, &eventarcpb.GetGoogleApiSourceRequest{Name: sourceName})
	require.NoError(t, err)
	assert.Equal(t, parent+"/messageBuses/sdk-bus", gotSource.GetDestination())

	updateSource, err := client.UpdateGoogleApiSource(ctx, &eventarcpb.UpdateGoogleApiSourceRequest{
		GoogleApiSource: &eventarcpb.GoogleApiSource{Name: sourceName, DisplayName: "SDK Source v2"},
	})
	require.NoError(t, err)
	updatedSource, err := updateSource.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, "SDK Source v2", updatedSource.GetDisplayName())

	sources := client.ListGoogleApiSources(ctx, &eventarcpb.ListGoogleApiSourcesRequest{Parent: parent})
	listed, err := sources.Next()
	require.NoError(t, err)
	assert.Equal(t, sourceName, listed.GetName())
}

func TestEventarc_GoogleChannelConfigSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	configName := parent + "/googleChannelConfig"

	updated, err := client.UpdateGoogleChannelConfig(ctx, &eventarcpb.UpdateGoogleChannelConfigRequest{
		GoogleChannelConfig: &eventarcpb.GoogleChannelConfig{
			Name:          configName,
			CryptoKeyName: "projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/k",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, configName, updated.GetName())
	assert.Equal(t, "projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/k", updated.GetCryptoKeyName())

	got, err := client.GetGoogleChannelConfig(ctx, &eventarcpb.GetGoogleChannelConfigRequest{Name: configName})
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/locations/us-central1/keyRings/kr/cryptoKeys/k", got.GetCryptoKeyName())
}

func TestEventarc_IamPolicySDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	triggerName := parent + "/triggers/iam-trigger"

	create, err := client.CreateTrigger(ctx, &eventarcpb.CreateTriggerRequest{
		Parent:    parent,
		TriggerId: "iam-trigger",
		Trigger: &eventarcpb.Trigger{
			EventFilters: []*eventarcpb.EventFilter{{
				Attribute: "type",
				Value:     "google.cloud.pubsub.topic.v1.messagePublished",
			}},
			Destination: &eventarcpb.Destination{
				Descriptor_: &eventarcpb.Destination_CloudRun{
					CloudRun: &eventarcpb.CloudRun{Service: "svc", Region: "us-central1"},
				},
			},
		},
	})
	require.NoError(t, err)
	_, err = create.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		if op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: triggerName}); err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	set, err := client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{
		Resource: triggerName,
		Policy: &iampb.Policy{
			Bindings: []*iampb.Binding{{
				Role:    "roles/eventarc.eventReceiver",
				Members: []string{"user:alice@example.com"},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, set.GetBindings(), 1)
	assert.Equal(t, "roles/eventarc.eventReceiver", set.GetBindings()[0].GetRole())

	got, err := client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: triggerName})
	require.NoError(t, err)
	require.Len(t, got.GetBindings(), 1)
	assert.Equal(t, []string{"user:alice@example.com"}, got.GetBindings()[0].GetMembers())

	test, err := client.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{
		Resource:    triggerName,
		Permissions: []string{"eventarc.triggers.get"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"eventarc.triggers.get"}, test.GetPermissions())
}

func TestEventarc_LocationsSDK(t *testing.T) {
	client := eventarcClient(t)

	locations := client.ListLocations(ctx, &locationpb.ListLocationsRequest{
		Name: "projects/test-project",
	})
	first, err := locations.Next()
	require.NoError(t, err)
	require.NotEmpty(t, first.GetName())
	require.NotEmpty(t, first.GetLocationId())

	got, err := client.GetLocation(ctx, &locationpb.GetLocationRequest{
		Name: "projects/test-project/locations/us-central1",
	})
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/locations/us-central1", got.GetName())
	assert.Equal(t, "us-central1", got.GetLocationId())
}
