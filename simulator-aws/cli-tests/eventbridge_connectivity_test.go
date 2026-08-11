package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventBridgeCLI_ConnectionApiDestination round-trips a connection and an
// API destination bound to it via the CLI: create-connection / describe / list /
// update / deauthorize, then create-api-destination / describe / list / update /
// delete. The connection's API key value is stored but never echoed on read.
func TestEventBridgeCLI_ConnectionApiDestination(t *testing.T) {
	out := runCLI(t, awsCLI("events", "create-connection",
		"--name", "eb-cli-conn",
		"--authorization-type", "API_KEY",
		"--auth-parameters", `{"ApiKeyAuthParameters":{"ApiKeyName":"x-api-key","ApiKeyValue":"secret-value"}}`))
	var conn struct {
		ConnectionArn string `json:"ConnectionArn"`
	}
	parseJSON(t, out, &conn)
	require.NotEmpty(t, conn.ConnectionArn)
	t.Cleanup(func() {
		// Tolerant: the test body also deletes these (exercising the Delete ops).
		_ = awsCLI("events", "delete-api-destination", "--name", "eb-cli-dest").Run()
		_ = awsCLI("events", "delete-connection", "--name", "eb-cli-conn").Run()
	})

	out = runCLI(t, awsCLI("events", "describe-connection", "--name", "eb-cli-conn"))
	var descConn struct {
		Name           string `json:"Name"`
		SecretArn      string `json:"SecretArn"`
		AuthParameters struct {
			ApiKeyAuthParameters struct {
				ApiKeyName string `json:"ApiKeyName"`
			} `json:"ApiKeyAuthParameters"`
		} `json:"AuthParameters"`
	}
	parseJSON(t, out, &descConn)
	assert.Equal(t, "eb-cli-conn", descConn.Name)
	assert.Equal(t, "x-api-key", descConn.AuthParameters.ApiKeyAuthParameters.ApiKeyName)
	assert.NotEmpty(t, descConn.SecretArn)
	// The secret value must never appear in a read-back.
	assert.NotContains(t, out, "secret-value")

	out = runCLI(t, awsCLI("events", "list-connections", "--name-prefix", "eb-cli-conn"))
	var conns struct {
		Connections []struct {
			Name string `json:"Name"`
		} `json:"Connections"`
	}
	parseJSON(t, out, &conns)
	require.Len(t, conns.Connections, 1)

	runCLI(t, awsCLI("events", "update-connection", "--name", "eb-cli-conn", "--description", "updated"))
	out = runCLI(t, awsCLI("events", "describe-connection", "--name", "eb-cli-conn"))
	var updatedConn struct {
		Description string `json:"Description"`
	}
	parseJSON(t, out, &updatedConn)
	assert.Equal(t, "updated", updatedConn.Description)

	out = runCLI(t, awsCLI("events", "deauthorize-connection", "--name", "eb-cli-conn"))
	var deauth struct {
		ConnectionState string `json:"ConnectionState"`
	}
	parseJSON(t, out, &deauth)
	assert.Equal(t, "DEAUTHORIZED", deauth.ConnectionState)

	out = runCLI(t, awsCLI("events", "create-api-destination",
		"--name", "eb-cli-dest",
		"--connection-arn", conn.ConnectionArn,
		"--invocation-endpoint", "https://example.com/hook",
		"--http-method", "POST",
		"--invocation-rate-limit-per-second", "10"))
	var dest struct {
		ApiDestinationArn   string `json:"ApiDestinationArn"`
		ApiDestinationState string `json:"ApiDestinationState"`
	}
	parseJSON(t, out, &dest)
	require.NotEmpty(t, dest.ApiDestinationArn)
	assert.Equal(t, "ACTIVE", dest.ApiDestinationState)

	out = runCLI(t, awsCLI("events", "describe-api-destination", "--name", "eb-cli-dest"))
	var descDest struct {
		InvocationEndpoint string `json:"InvocationEndpoint"`
		HttpMethod         string `json:"HttpMethod"`
	}
	parseJSON(t, out, &descDest)
	assert.Equal(t, "https://example.com/hook", descDest.InvocationEndpoint)
	assert.Equal(t, "POST", descDest.HttpMethod)

	out = runCLI(t, awsCLI("events", "list-api-destinations", "--name-prefix", "eb-cli-dest"))
	var dests struct {
		ApiDestinations []struct {
			Name string `json:"Name"`
		} `json:"ApiDestinations"`
	}
	parseJSON(t, out, &dests)
	require.Len(t, dests.ApiDestinations, 1)

	runCLI(t, awsCLI("events", "update-api-destination",
		"--name", "eb-cli-dest",
		"--invocation-endpoint", "https://example.com/hook2"))
	out = runCLI(t, awsCLI("events", "describe-api-destination", "--name", "eb-cli-dest"))
	parseJSON(t, out, &descDest)
	assert.Equal(t, "https://example.com/hook2", descDest.InvocationEndpoint)

	runCLI(t, awsCLI("events", "delete-api-destination", "--name", "eb-cli-dest"))
	runCLI(t, awsCLI("events", "delete-connection", "--name", "eb-cli-conn"))
}

// TestEventBridgeCLI_Endpoint round-trips a global endpoint via the CLI.
func TestEventBridgeCLI_Endpoint(t *testing.T) {
	out := runCLI(t, awsCLI("events", "create-event-bus", "--name", "eb-cli-endpoint-bus"))
	var bus struct {
		EventBusArn string `json:"EventBusArn"`
	}
	parseJSON(t, out, &bus)
	t.Cleanup(func() {
		// Tolerant: the test body also deletes the endpoint (exercising DeleteEndpoint).
		_ = awsCLI("events", "delete-endpoint", "--name", "eb-cli-endpoint").Run()
		_ = awsCLI("events", "delete-event-bus", "--name", "eb-cli-endpoint-bus").Run()
	})

	// A global endpoint spans two Regions, so EventBuses must list the bus in
	// both (the same ARN with the primary/secondary Region differing on real
	// AWS); botocore enforces the min length of 2.
	routing := `{"FailoverConfig":{"Primary":{"HealthCheck":"arn:aws:route53:::healthcheck/abcdef01-2345-6789-abcd-ef0123456789"},"Secondary":{"Route":"us-east-2"}}}`
	out = runCLI(t, awsCLI("events", "create-endpoint",
		"--name", "eb-cli-endpoint",
		"--description", "cli endpoint",
		"--event-buses", `[{"EventBusArn":"`+bus.EventBusArn+`"},{"EventBusArn":"`+bus.EventBusArn+`"}]`,
		"--routing-config", routing,
		"--replication-config", `{"State":"DISABLED"}`))
	var endpoint struct {
		Arn  string `json:"Arn"`
		Name string `json:"Name"`
	}
	parseJSON(t, out, &endpoint)
	require.NotEmpty(t, endpoint.Arn)

	out = runCLI(t, awsCLI("events", "describe-endpoint", "--name", "eb-cli-endpoint"))
	var desc struct {
		Name          string `json:"Name"`
		EndpointId    string `json:"EndpointId"`
		RoutingConfig struct {
			FailoverConfig struct {
				Primary struct {
					HealthCheck string `json:"HealthCheck"`
				} `json:"Primary"`
			} `json:"FailoverConfig"`
		} `json:"RoutingConfig"`
	}
	parseJSON(t, out, &desc)
	assert.Equal(t, "eb-cli-endpoint", desc.Name)
	assert.NotEmpty(t, desc.EndpointId)
	assert.Equal(t, "arn:aws:route53:::healthcheck/abcdef01-2345-6789-abcd-ef0123456789", desc.RoutingConfig.FailoverConfig.Primary.HealthCheck)

	out = runCLI(t, awsCLI("events", "list-endpoints", "--name-prefix", "eb-cli-endpoint"))
	var list struct {
		Endpoints []struct {
			Name string `json:"Name"`
		} `json:"Endpoints"`
	}
	parseJSON(t, out, &list)
	require.Len(t, list.Endpoints, 1)

	runCLI(t, awsCLI("events", "update-endpoint", "--name", "eb-cli-endpoint", "--description", "updated endpoint"))
	out = runCLI(t, awsCLI("events", "describe-endpoint", "--name", "eb-cli-endpoint"))
	var updated struct {
		Description string `json:"Description"`
	}
	parseJSON(t, out, &updated)
	assert.Equal(t, "updated endpoint", updated.Description)

	runCLI(t, awsCLI("events", "delete-endpoint", "--name", "eb-cli-endpoint"))
}

// TestEventBridgeCLI_PartnerEventSource round-trips a partner event source and
// its consumer-side event source via the CLI.
func TestEventBridgeCLI_PartnerEventSource(t *testing.T) {
	account := "123456789012"
	name := "sockerlesspartnercli/jobs/example"
	out := runCLI(t, awsCLI("events", "create-partner-event-source",
		"--name", name,
		"--account", account))
	var created struct {
		EventSourceArn string `json:"EventSourceArn"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.EventSourceArn)
	t.Cleanup(func() {
		// Tolerant: the test body also deletes it (exercising DeletePartnerEventSource).
		_ = awsCLI("events", "delete-partner-event-source", "--name", name, "--account", account).Run()
	})

	out = runCLI(t, awsCLI("events", "describe-partner-event-source", "--name", name))
	var descPartner struct {
		Name string `json:"Name"`
	}
	parseJSON(t, out, &descPartner)
	assert.Equal(t, name, descPartner.Name)

	out = runCLI(t, awsCLI("events", "list-partner-event-sources", "--name-prefix", "sockerlesspartnercli"))
	var partners struct {
		PartnerEventSources []struct {
			Name string `json:"Name"`
		} `json:"PartnerEventSources"`
	}
	parseJSON(t, out, &partners)
	require.Len(t, partners.PartnerEventSources, 1)

	out = runCLI(t, awsCLI("events", "list-partner-event-source-accounts", "--event-source-name", name))
	var accounts struct {
		PartnerEventSourceAccounts []struct {
			Account string `json:"Account"`
			State   string `json:"State"`
		} `json:"PartnerEventSourceAccounts"`
	}
	parseJSON(t, out, &accounts)
	require.Len(t, accounts.PartnerEventSourceAccounts, 1)
	assert.Equal(t, account, accounts.PartnerEventSourceAccounts[0].Account)
	assert.Equal(t, "PENDING", accounts.PartnerEventSourceAccounts[0].State)

	out = runCLI(t, awsCLI("events", "describe-event-source", "--name", name))
	var descSrc struct {
		State string `json:"State"`
	}
	parseJSON(t, out, &descSrc)
	assert.Equal(t, "PENDING", descSrc.State)

	out = runCLI(t, awsCLI("events", "list-event-sources", "--name-prefix", "sockerlesspartnercli"))
	var sources struct {
		EventSources []struct {
			State string `json:"State"`
		} `json:"EventSources"`
	}
	parseJSON(t, out, &sources)
	require.Len(t, sources.EventSources, 1)

	runCLI(t, awsCLI("events", "activate-event-source", "--name", name))
	out = runCLI(t, awsCLI("events", "describe-event-source", "--name", name))
	parseJSON(t, out, &descSrc)
	assert.Equal(t, "ACTIVE", descSrc.State)

	runCLI(t, awsCLI("events", "deactivate-event-source", "--name", name))
	out = runCLI(t, awsCLI("events", "describe-event-source", "--name", name))
	parseJSON(t, out, &descSrc)
	assert.Equal(t, "PENDING", descSrc.State)

	out = runCLI(t, awsCLI("events", "put-partner-events",
		"--entries", `[{"Source":"`+name+`","DetailType":"example","Detail":"{\"ok\":true}"}]`))
	var putPartner struct {
		FailedEntryCount int `json:"FailedEntryCount"`
		Entries          []struct {
			EventId string `json:"EventId"`
		} `json:"Entries"`
	}
	parseJSON(t, out, &putPartner)
	assert.Equal(t, 0, putPartner.FailedEntryCount)
	require.Len(t, putPartner.Entries, 1)
	assert.NotEmpty(t, putPartner.Entries[0].EventId)

	runCLI(t, awsCLI("events", "delete-partner-event-source", "--name", name, "--account", account))
}

// TestEventBridgeCLI_UpdateArchiveCancelReplay exercises update-archive and
// cancel-replay via the CLI. A completed replay cannot be cancelled —
// cancel-replay fails with IllegalStatusException, matching real EventBridge.
func TestEventBridgeCLI_UpdateArchiveCancelReplay(t *testing.T) {
	out := runCLI(t, awsCLI("events", "create-event-bus", "--name", "eb-cli-uacr-bus"))
	var bus struct {
		EventBusArn string `json:"EventBusArn"`
	}
	parseJSON(t, out, &bus)
	t.Cleanup(func() {
		runCLI(t, awsCLI("events", "delete-archive", "--archive-name", "eb-cli-uacr-archive"))
		runCLI(t, awsCLI("events", "delete-event-bus", "--name", "eb-cli-uacr-bus"))
	})

	out = runCLI(t, awsCLI("events", "create-archive",
		"--archive-name", "eb-cli-uacr-archive",
		"--event-source-arn", bus.EventBusArn,
		"--description", "before",
		"--event-pattern", `{"source":["sockerless.cli.uacr"]}`))
	var archive struct {
		ArchiveArn string `json:"ArchiveArn"`
	}
	parseJSON(t, out, &archive)
	require.NotEmpty(t, archive.ArchiveArn)

	runCLI(t, awsCLI("events", "update-archive",
		"--archive-name", "eb-cli-uacr-archive",
		"--description", "after",
		"--retention-days", "30"))
	out = runCLI(t, awsCLI("events", "describe-archive", "--archive-name", "eb-cli-uacr-archive"))
	var descArchive struct {
		Description   string `json:"Description"`
		RetentionDays int    `json:"RetentionDays"`
	}
	parseJSON(t, out, &descArchive)
	assert.Equal(t, "after", descArchive.Description)
	assert.Equal(t, 30, descArchive.RetentionDays)

	runCLI(t, awsCLI("events", "start-replay",
		"--replay-name", "eb-cli-uacr-replay",
		"--event-source-arn", archive.ArchiveArn,
		"--event-start-time", "2026-05-27T00:00:00Z",
		"--event-end-time", "2026-05-29T00:00:00Z",
		"--destination", `{"Arn":"`+bus.EventBusArn+`"}`))

	// A completed replay cannot be cancelled.
	errOut := runCLIExpectError(t, awsCLI("events", "cancel-replay", "--replay-name", "eb-cli-uacr-replay"))
	assert.Contains(t, errOut, "IllegalStatusException")
}
