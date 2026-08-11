package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEventBridge_ConnectionApiDestinationSDK round-trips a connection and an
// API destination bound to it: create → describe → list → update → delete. The
// connection's secret (the API key value) is stored but never echoed on read —
// DescribeConnection returns only the non-secret descriptor (ApiKeyName) plus a
// SecretArn, matching real EventBridge.
func TestEventBridge_ConnectionApiDestinationSDK(t *testing.T) {
	eb := eventbridgeClient()

	connName := "eb-sdk-conn"
	createConn, err := eb.CreateConnection(ctx, &eventbridge.CreateConnectionInput{
		Name:              aws.String(connName),
		AuthorizationType: ebtypes.ConnectionAuthorizationTypeApiKey,
		Description:       aws.String("sdk connection"),
		AuthParameters: &ebtypes.CreateConnectionAuthRequestParameters{
			ApiKeyAuthParameters: &ebtypes.CreateConnectionApiKeyAuthRequestParameters{
				ApiKeyName:  aws.String("x-api-key"),
				ApiKeyValue: aws.String("super-secret-value"),
			},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(createConn.ConnectionArn))
	t.Cleanup(func() {
		_, _ = eb.DeleteApiDestination(ctx, &eventbridge.DeleteApiDestinationInput{Name: aws.String("eb-sdk-dest")})
		_, _ = eb.DeleteConnection(ctx, &eventbridge.DeleteConnectionInput{Name: aws.String(connName)})
	})

	descConn, err := eb.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String(connName)})
	require.NoError(t, err)
	assert.Equal(t, connName, aws.ToString(descConn.Name))
	assert.Equal(t, ebtypes.ConnectionAuthorizationTypeApiKey, descConn.AuthorizationType)
	require.NotNil(t, descConn.AuthParameters)
	require.NotNil(t, descConn.AuthParameters.ApiKeyAuthParameters)
	assert.Equal(t, "x-api-key", aws.ToString(descConn.AuthParameters.ApiKeyAuthParameters.ApiKeyName))
	assert.NotEmpty(t, aws.ToString(descConn.SecretArn))

	conns, err := eb.ListConnections(ctx, &eventbridge.ListConnectionsInput{NamePrefix: aws.String("eb-sdk-conn")})
	require.NoError(t, err)
	require.Len(t, conns.Connections, 1)
	assert.Equal(t, connName, aws.ToString(conns.Connections[0].Name))

	_, err = eb.UpdateConnection(ctx, &eventbridge.UpdateConnectionInput{
		Name:        aws.String(connName),
		Description: aws.String("updated connection"),
	})
	require.NoError(t, err)
	descConn, err = eb.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String(connName)})
	require.NoError(t, err)
	assert.Equal(t, "updated connection", aws.ToString(descConn.Description))

	deauth, err := eb.DeauthorizeConnection(ctx, &eventbridge.DeauthorizeConnectionInput{Name: aws.String(connName)})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.ConnectionStateDeauthorized, deauth.ConnectionState)

	// API destination bound to the connection.
	destName := "eb-sdk-dest"
	rate := int32(10)
	createDest, err := eb.CreateApiDestination(ctx, &eventbridge.CreateApiDestinationInput{
		Name:                         aws.String(destName),
		ConnectionArn:                createConn.ConnectionArn,
		InvocationEndpoint:           aws.String("https://example.com/hook"),
		HttpMethod:                   ebtypes.ApiDestinationHttpMethodPost,
		Description:                  aws.String("sdk destination"),
		InvocationRateLimitPerSecond: aws.Int32(rate),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(createDest.ApiDestinationArn))
	assert.Equal(t, ebtypes.ApiDestinationStateActive, createDest.ApiDestinationState)

	descDest, err := eb.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String(destName)})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/hook", aws.ToString(descDest.InvocationEndpoint))
	assert.Equal(t, ebtypes.ApiDestinationHttpMethodPost, descDest.HttpMethod)
	assert.Equal(t, aws.ToString(createConn.ConnectionArn), aws.ToString(descDest.ConnectionArn))
	require.NotNil(t, descDest.InvocationRateLimitPerSecond)
	assert.EqualValues(t, 10, *descDest.InvocationRateLimitPerSecond)

	dests, err := eb.ListApiDestinations(ctx, &eventbridge.ListApiDestinationsInput{NamePrefix: aws.String("eb-sdk-dest")})
	require.NoError(t, err)
	require.Len(t, dests.ApiDestinations, 1)
	assert.Equal(t, destName, aws.ToString(dests.ApiDestinations[0].Name))

	_, err = eb.UpdateApiDestination(ctx, &eventbridge.UpdateApiDestinationInput{
		Name:               aws.String(destName),
		InvocationEndpoint: aws.String("https://example.com/hook2"),
	})
	require.NoError(t, err)
	descDest, err = eb.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String(destName)})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/hook2", aws.ToString(descDest.InvocationEndpoint))

	_, err = eb.DeleteApiDestination(ctx, &eventbridge.DeleteApiDestinationInput{Name: aws.String(destName)})
	require.NoError(t, err)
	_, err = eb.DescribeApiDestination(ctx, &eventbridge.DescribeApiDestinationInput{Name: aws.String(destName)})
	require.Error(t, err)

	_, err = eb.DeleteConnection(ctx, &eventbridge.DeleteConnectionInput{Name: aws.String(connName)})
	require.NoError(t, err)
	_, err = eb.DescribeConnection(ctx, &eventbridge.DescribeConnectionInput{Name: aws.String(connName)})
	require.Error(t, err)
}

// TestEventBridge_EndpointSDK round-trips a global endpoint: create → describe →
// list → update → delete, asserting the RoutingConfig / EventBuses round-trip
// byte-exact through the describe shape.
func TestEventBridge_EndpointSDK(t *testing.T) {
	eb := eventbridgeClient()

	bus, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String("eb-sdk-endpoint-bus")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String("eb-sdk-endpoint-bus")})
	})

	name := "eb-sdk-endpoint"
	created, err := eb.CreateEndpoint(ctx, &eventbridge.CreateEndpointInput{
		Name:        aws.String(name),
		Description: aws.String("sdk endpoint"),
		EventBuses: []ebtypes.EndpointEventBus{
			{EventBusArn: bus.EventBusArn},
			{EventBusArn: bus.EventBusArn},
		},
		RoutingConfig: &ebtypes.RoutingConfig{
			FailoverConfig: &ebtypes.FailoverConfig{
				Primary: &ebtypes.Primary{
					HealthCheck: aws.String("arn:aws:route53:::healthcheck/abcdef01-2345-6789-abcd-ef0123456789"),
				},
				Secondary: &ebtypes.Secondary{
					Route: aws.String("us-east-2"),
				},
			},
		},
		ReplicationConfig: &ebtypes.ReplicationConfig{State: ebtypes.ReplicationStateDisabled},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(created.Arn))
	t.Cleanup(func() {
		_, _ = eb.DeleteEndpoint(ctx, &eventbridge.DeleteEndpointInput{Name: aws.String(name)})
	})

	desc, err := eb.DescribeEndpoint(ctx, &eventbridge.DescribeEndpointInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(desc.Name))
	require.NotNil(t, desc.RoutingConfig)
	require.NotNil(t, desc.RoutingConfig.FailoverConfig)
	require.NotNil(t, desc.RoutingConfig.FailoverConfig.Primary)
	assert.Equal(t, "arn:aws:route53:::healthcheck/abcdef01-2345-6789-abcd-ef0123456789",
		aws.ToString(desc.RoutingConfig.FailoverConfig.Primary.HealthCheck))
	require.Len(t, desc.EventBuses, 2)
	assert.Equal(t, aws.ToString(bus.EventBusArn), aws.ToString(desc.EventBuses[0].EventBusArn))
	assert.NotEmpty(t, aws.ToString(desc.EndpointId))

	list, err := eb.ListEndpoints(ctx, &eventbridge.ListEndpointsInput{NamePrefix: aws.String("eb-sdk-endpoint")})
	require.NoError(t, err)
	require.Len(t, list.Endpoints, 1)
	assert.Equal(t, name, aws.ToString(list.Endpoints[0].Name))

	_, err = eb.UpdateEndpoint(ctx, &eventbridge.UpdateEndpointInput{
		Name:        aws.String(name),
		Description: aws.String("updated endpoint"),
	})
	require.NoError(t, err)
	desc, err = eb.DescribeEndpoint(ctx, &eventbridge.DescribeEndpointInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "updated endpoint", aws.ToString(desc.Description))

	_, err = eb.DeleteEndpoint(ctx, &eventbridge.DeleteEndpointInput{Name: aws.String(name)})
	require.NoError(t, err)
	_, err = eb.DescribeEndpoint(ctx, &eventbridge.DescribeEndpointInput{Name: aws.String(name)})
	require.Error(t, err)
}

// TestEventBridge_PartnerEventSourceSDK round-trips a partner event source and
// its consumer-side event source: a SaaS partner creates an offer
// (CreatePartnerEventSource) to a customer account, which surfaces as a PENDING
// consumer-side event source (DescribeEventSource/ListEventSources). The account
// activates it (ACTIVE) then deactivates it (PENDING). PutPartnerEvents emits
// events, and DeletePartnerEventSource removes the offer.
func TestEventBridge_PartnerEventSourceSDK(t *testing.T) {
	eb := eventbridgeClient()

	account := "123456789012"
	name := "sockerlesspartner/jobs/example"
	created, err := eb.CreatePartnerEventSource(ctx, &eventbridge.CreatePartnerEventSourceInput{
		Name:    aws.String(name),
		Account: aws.String(account),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(created.EventSourceArn))
	t.Cleanup(func() {
		_, _ = eb.DeletePartnerEventSource(ctx, &eventbridge.DeletePartnerEventSourceInput{
			Name:    aws.String(name),
			Account: aws.String(account),
		})
	})

	descPartner, err := eb.DescribePartnerEventSource(ctx, &eventbridge.DescribePartnerEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(descPartner.Name))

	partners, err := eb.ListPartnerEventSources(ctx, &eventbridge.ListPartnerEventSourcesInput{NamePrefix: aws.String("sockerlesspartner")})
	require.NoError(t, err)
	require.Len(t, partners.PartnerEventSources, 1)
	assert.Equal(t, name, aws.ToString(partners.PartnerEventSources[0].Name))

	accounts, err := eb.ListPartnerEventSourceAccounts(ctx, &eventbridge.ListPartnerEventSourceAccountsInput{EventSourceName: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, accounts.PartnerEventSourceAccounts, 1)
	assert.Equal(t, account, aws.ToString(accounts.PartnerEventSourceAccounts[0].Account))
	assert.Equal(t, ebtypes.EventSourceStatePending, accounts.PartnerEventSourceAccounts[0].State)

	// Consumer-side event source view.
	descSrc, err := eb.DescribeEventSource(ctx, &eventbridge.DescribeEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(descSrc.Name))
	assert.Equal(t, ebtypes.EventSourceStatePending, descSrc.State)

	sources, err := eb.ListEventSources(ctx, &eventbridge.ListEventSourcesInput{NamePrefix: aws.String("sockerlesspartner")})
	require.NoError(t, err)
	require.Len(t, sources.EventSources, 1)
	assert.Equal(t, ebtypes.EventSourceStatePending, sources.EventSources[0].State)

	_, err = eb.ActivateEventSource(ctx, &eventbridge.ActivateEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	descSrc, err = eb.DescribeEventSource(ctx, &eventbridge.DescribeEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.EventSourceStateActive, descSrc.State)

	_, err = eb.DeactivateEventSource(ctx, &eventbridge.DeactivateEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	descSrc, err = eb.DescribeEventSource(ctx, &eventbridge.DescribeEventSourceInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, ebtypes.EventSourceStatePending, descSrc.State)

	putPartner, err := eb.PutPartnerEvents(ctx, &eventbridge.PutPartnerEventsInput{
		Entries: []ebtypes.PutPartnerEventsRequestEntry{{
			Source:     aws.String(name),
			DetailType: aws.String("example"),
			Detail:     aws.String(`{"ok":true}`),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 0, putPartner.FailedEntryCount)
	require.Len(t, putPartner.Entries, 1)
	assert.NotEmpty(t, aws.ToString(putPartner.Entries[0].EventId))

	// A partner event entry missing Source/DetailType/Detail fails that entry.
	badPartner, err := eb.PutPartnerEvents(ctx, &eventbridge.PutPartnerEventsInput{
		Entries: []ebtypes.PutPartnerEventsRequestEntry{{
			DetailType: aws.String("example"),
			Detail:     aws.String(`{"ok":true}`),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, badPartner.FailedEntryCount)

	_, err = eb.DeletePartnerEventSource(ctx, &eventbridge.DeletePartnerEventSourceInput{
		Name:    aws.String(name),
		Account: aws.String(account),
	})
	require.NoError(t, err)
	_, err = eb.DescribePartnerEventSource(ctx, &eventbridge.DescribePartnerEventSourceInput{Name: aws.String(name)})
	require.Error(t, err)
}

// TestEventBridge_UpdateArchiveCancelReplaySDK exercises UpdateArchive (mutating
// an existing archive's description / pattern / retention) and CancelReplay. A
// COMPLETED replay cannot be cancelled — CancelReplay faithfully returns
// IllegalStatusException, exactly as real EventBridge.
func TestEventBridge_UpdateArchiveCancelReplaySDK(t *testing.T) {
	eb := eventbridgeClient()

	busName := "eb-sdk-uacr-bus"
	bus, err := eb.CreateEventBus(ctx, &eventbridge.CreateEventBusInput{Name: aws.String(busName)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = eb.DeleteArchive(ctx, &eventbridge.DeleteArchiveInput{ArchiveName: aws.String("eb-sdk-uacr-archive")})
		_, _ = eb.DeleteEventBus(ctx, &eventbridge.DeleteEventBusInput{Name: aws.String(busName)})
	})

	archiveName := "eb-sdk-uacr-archive"
	archive, err := eb.CreateArchive(ctx, &eventbridge.CreateArchiveInput{
		ArchiveName:    aws.String(archiveName),
		EventSourceArn: bus.EventBusArn,
		Description:    aws.String("before"),
		EventPattern:   aws.String(`{"source":["sockerless.uacr"]}`),
	})
	require.NoError(t, err)

	updated, err := eb.UpdateArchive(ctx, &eventbridge.UpdateArchiveInput{
		ArchiveName:   aws.String(archiveName),
		Description:   aws.String("after"),
		EventPattern:  aws.String(`{"source":["sockerless.uacr2"]}`),
		RetentionDays: aws.Int32(30),
	})
	require.NoError(t, err)
	assert.Equal(t, aws.ToString(archive.ArchiveArn), aws.ToString(updated.ArchiveArn))

	descArchive, err := eb.DescribeArchive(ctx, &eventbridge.DescribeArchiveInput{ArchiveName: aws.String(archiveName)})
	require.NoError(t, err)
	assert.Equal(t, "after", aws.ToString(descArchive.Description))
	assert.JSONEq(t, `{"source":["sockerless.uacr2"]}`, aws.ToString(descArchive.EventPattern))
	require.NotNil(t, descArchive.RetentionDays)
	assert.EqualValues(t, 30, *descArchive.RetentionDays)

	// Start a replay over the archive — it completes synchronously in the sim.
	replay, err := eb.StartReplay(ctx, &eventbridge.StartReplayInput{
		ReplayName:     aws.String("eb-sdk-uacr-replay"),
		EventSourceArn: archive.ArchiveArn,
		EventStartTime: aws.Time(time.Now().Add(-time.Hour)),
		EventEndTime:   aws.Time(time.Now()),
		Destination:    &ebtypes.ReplayDestination{Arn: bus.EventBusArn},
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(replay.ReplayArn))

	// A completed replay cannot be cancelled — real EventBridge raises
	// IllegalStatusException.
	_, err = eb.CancelReplay(ctx, &eventbridge.CancelReplayInput{ReplayName: aws.String("eb-sdk-uacr-replay")})
	require.Error(t, err)
	var illegal *ebtypes.IllegalStatusException
	assert.ErrorAs(t, err, &illegal)
}
