package gcp_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventarcCLI_TriggerLifecycle(t *testing.T) {
	runCLI(t, gcloudCLI("eventarc", "triggers", "create", "cli-trigger",
		"--location", location,
		"--destination-run-service", "cli-service",
		"--destination-run-region", location,
		"--event-filters", "type=google.cloud.pubsub.topic.v1.messagePublished",
		"--transport-topic", "projects/"+project+"/topics/cli-topic",
		"--format", "json"))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("eventarc", "triggers", "delete", "cli-trigger",
			"--location", location,
			"--quiet"))
	})

	out := runCLI(t, gcloudCLI("eventarc", "triggers", "describe", "cli-trigger",
		"--location", location,
		"--format", "json"))
	var trigger struct {
		Name         string            `json:"name"`
		Labels       map[string]string `json:"labels"`
		EventFilters []struct {
			Attribute string `json:"attribute"`
			Value     string `json:"value"`
		} `json:"eventFilters"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &trigger))
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/triggers/cli-trigger", trigger.Name)
	require.Len(t, trigger.EventFilters, 1)
	assert.Equal(t, "type", trigger.EventFilters[0].Attribute)

	out = runCLI(t, gcloudCLI("eventarc", "triggers", "list",
		"--location", location,
		"--format", "json"))
	var triggers []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &triggers))
	require.Len(t, triggers, 1)
	assert.Equal(t, trigger.Name, triggers[0].Name)
}

func TestEventarcCLI_ChannelProviderConnection(t *testing.T) {
	out := runCLI(t, gcloudCLI("eventarc", "providers", "list",
		"--location", location,
		"--format", "json"))
	var providers []struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &providers))
	require.NotEmpty(t, providers)
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/providers/cloud.pubsub", providers[0].Name)

	runCLI(t, gcloudCLI("eventarc", "channels", "create", "cli-channel",
		"--location", location,
		"--provider", "projects/"+project+"/locations/"+location+"/providers/cloud.pubsub",
		"--format", "json"))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("eventarc", "channels", "delete", "cli-channel",
			"--location", location,
			"--quiet"))
	})

	out = runCLI(t, gcloudCLI("eventarc", "channels", "describe", "cli-channel",
		"--location", location,
		"--format", "json"))
	var channel struct {
		Name            string `json:"name"`
		State           string `json:"state"`
		ActivationToken string `json:"activationToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &channel))
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/channels/cli-channel", channel.Name)
	require.NotEmpty(t, channel.ActivationToken)

	runCLI(t, gcloudCLI("eventarc", "channel-connections", "create", "cli-connection",
		"--location", location,
		"--channel", channel.Name,
		"--activation-token", channel.ActivationToken,
		"--format", "json"))
	t.Cleanup(func() {
		runCLI(t, gcloudCLI("eventarc", "channel-connections", "delete", "cli-connection",
			"--location", location,
			"--quiet"))
	})

	out = runCLI(t, gcloudCLI("eventarc", "channel-connections", "list",
		"--location", location,
		"--format", "json"))
	var connections []struct {
		Name    string `json:"name"`
		Channel string `json:"channel"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &connections))
	require.Len(t, connections, 1)
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/channelConnections/cli-connection", connections[0].Name)
	assert.Equal(t, channel.Name, connections[0].Channel)
}
