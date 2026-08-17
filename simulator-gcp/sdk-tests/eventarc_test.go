package gcp_sdk_test

import (
	"encoding/json"
	"errors"
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

// eventarcCollect drains a list iterator and returns the identity of every item
// it yielded. Every collection in this file lives in one shared location, so a
// test asserts MEMBERSHIP of the resource it created: "exactly one item, then
// iterator.Done" would make each test's result depend on which other tests have
// already run and on whether their cleanups succeeded.
func eventarcCollect[T any](t *testing.T, next func() (T, error), id func(T) string) []string {
	t.Helper()
	var out []string
	for {
		item, err := next()
		if errors.Is(err, iterator.Done) {
			return out
		}
		require.NoError(t, err)
		out = append(out, id(item))
	}
}

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
	// A cleanup that swallowed its error would leave the trigger behind, and
	// the location is shared with every other test in this file.
	t.Cleanup(func() {
		op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: name})
		if assert.NoError(t, err, "delete %s", name) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", name)
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

	triggers := eventarcCollect(t,
		client.ListTriggers(ctx, &eventarcpb.ListTriggersRequest{Parent: parent}).Next,
		(*eventarcpb.Trigger).GetName)
	assert.Contains(t, triggers, name, "the trigger just created must be listed")
}

func TestEventarc_ChannelProviderConnectionSDK(t *testing.T) {
	client := eventarcClient(t)
	parent := "projects/test-project/locations/us-central1"
	channelName := parent + "/channels/sdk-channel"
	connectionName := parent + "/channelConnections/sdk-connection"

	providers := eventarcCollect(t,
		client.ListProviders(ctx, &eventarcpb.ListProvidersRequest{Parent: parent}).Next,
		(*eventarcpb.Provider).GetName)
	require.Contains(t, providers, parent+"/providers/cloud.pubsub")

	gotProvider, err := client.GetProvider(ctx, &eventarcpb.GetProviderRequest{Name: parent + "/providers/cloud.pubsub"})
	require.NoError(t, err)
	assert.Equal(t, "Cloud Pub/Sub", gotProvider.GetDisplayName())
	require.NotEmpty(t, gotProvider.GetEventTypes())

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
		if assert.NoError(t, err, "delete %s", channelName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", channelName)
		}
	})

	gotChannel, err := client.GetChannel(ctx, &eventarcpb.GetChannelRequest{Name: channelName})
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/topics/sdk-channel-topic", gotChannel.GetPubsubTopic())

	channels := eventarcCollect(t,
		client.ListChannels(ctx, &eventarcpb.ListChannelsRequest{Parent: parent}).Next,
		(*eventarcpb.Channel).GetName)
	assert.Contains(t, channels, channelName, "the channel just created must be listed")

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
		if assert.NoError(t, err, "delete %s", connectionName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", connectionName)
		}
	})

	gotConnection, err := client.GetChannelConnection(ctx, &eventarcpb.GetChannelConnectionRequest{Name: connectionName})
	require.NoError(t, err)
	assert.Equal(t, channelName, gotConnection.GetChannel())

	connections := eventarcCollect(t,
		client.ListChannelConnections(ctx, &eventarcpb.ListChannelConnectionsRequest{Parent: parent}).Next,
		(*eventarcpb.ChannelConnection).GetName)
	assert.Contains(t, connections, connectionName, "the channel connection just created must be listed")
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
		op, err := client.DeleteMessageBus(ctx, &eventarcpb.DeleteMessageBusRequest{Name: busName})
		if assert.NoError(t, err, "delete %s", busName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", busName)
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

	buses := eventarcCollect(t,
		client.ListMessageBuses(ctx, &eventarcpb.ListMessageBusesRequest{Parent: parent}).Next,
		(*eventarcpb.MessageBus).GetName)
	assert.Contains(t, buses, busName, "the message bus just created must be listed")

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
		op, err := client.DeleteEnrollment(ctx, &eventarcpb.DeleteEnrollmentRequest{Name: enrollmentName})
		if assert.NoError(t, err, "delete %s", enrollmentName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", enrollmentName)
		}
	})

	gotEnrollment, err := client.GetEnrollment(ctx, &eventarcpb.GetEnrollmentRequest{Name: enrollmentName})
	require.NoError(t, err)
	assert.Equal(t, parent+"/pipelines/sdk-pipeline", gotEnrollment.GetDestination())

	enrollments := eventarcCollect(t,
		client.ListEnrollments(ctx, &eventarcpb.ListEnrollmentsRequest{Parent: parent}).Next,
		(*eventarcpb.Enrollment).GetName)
	assert.Contains(t, enrollments, enrollmentName, "the enrollment just created must be listed")

	// messageBuses:listEnrollments returns the names of enrollments bound to the
	// bus. The bus belongs to this test, so the binding it made is the whole of
	// that list — an enrollment bound to another bus leaking in would be a
	// scoping defect.
	busEnrollments := eventarcCollect(t,
		client.ListMessageBusEnrollments(ctx, &eventarcpb.ListMessageBusEnrollmentsRequest{Parent: busName}).Next,
		func(name string) string { return name })
	assert.Equal(t, []string{enrollmentName}, busEnrollments)
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
		op, err := client.DeletePipeline(ctx, &eventarcpb.DeletePipelineRequest{Name: pipelineName})
		if assert.NoError(t, err, "delete %s", pipelineName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", pipelineName)
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

	pipelines := eventarcCollect(t,
		client.ListPipelines(ctx, &eventarcpb.ListPipelinesRequest{Parent: parent}).Next,
		(*eventarcpb.Pipeline).GetName)
	assert.Contains(t, pipelines, pipelineName, "the pipeline just created must be listed")
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
		op, err := client.DeleteGoogleApiSource(ctx, &eventarcpb.DeleteGoogleApiSourceRequest{Name: sourceName})
		if assert.NoError(t, err, "delete %s", sourceName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", sourceName)
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

	sources := eventarcCollect(t,
		client.ListGoogleApiSources(ctx, &eventarcpb.ListGoogleApiSourcesRequest{Parent: parent}).Next,
		(*eventarcpb.GoogleApiSource).GetName)
	assert.Contains(t, sources, sourceName, "the Google API source just created must be listed")
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
		op, err := client.DeleteTrigger(ctx, &eventarcpb.DeleteTriggerRequest{Name: triggerName})
		if assert.NoError(t, err, "delete %s", triggerName) {
			_, err = op.Wait(ctx)
			assert.NoError(t, err, "await deletion of %s", triggerName)
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

	locations := eventarcCollect(t,
		client.ListLocations(ctx, &locationpb.ListLocationsRequest{Name: "projects/test-project"}).Next,
		(*locationpb.Location).GetName)
	require.Contains(t, locations, "projects/test-project/locations/us-central1")

	got, err := client.GetLocation(ctx, &locationpb.GetLocationRequest{
		Name: "projects/test-project/locations/us-central1",
	})
	require.NoError(t, err)
	assert.Equal(t, "projects/test-project/locations/us-central1", got.GetName())
	assert.Equal(t, "us-central1", got.GetLocationId())
}
