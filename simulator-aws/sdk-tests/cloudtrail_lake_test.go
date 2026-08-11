package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudTrail_EventDataStoreLifecycleSDK exercises the CloudTrail Lake event
// data store control plane: CreateEventDataStore → GetEventDataStore /
// ListEventDataStores → UpdateEventDataStore → StopEventDataStoreIngestion /
// StartEventDataStoreIngestion → EnableFederation / DisableFederation →
// DeleteEventDataStore → RestoreEventDataStore.
func TestCloudTrail_EventDataStoreLifecycleSDK(t *testing.T) {
	ct := cloudTrailClient()
	const name = "ct-eds-lifecycle"

	created, err := ct.CreateEventDataStore(ctx, &cloudtrail.CreateEventDataStoreInput{
		Name:            aws.String(name),
		RetentionPeriod: aws.Int32(90),
	})
	require.NoError(t, err)
	arn := aws.ToString(created.EventDataStoreArn)
	require.NotEmpty(t, arn)
	assert.Equal(t, name, aws.ToString(created.Name))
	assert.Equal(t, cttypes.EventDataStoreStatusEnabled, created.Status)
	t.Cleanup(func() {
		_, _ = ct.DeleteEventDataStore(ctx, &cloudtrail.DeleteEventDataStoreInput{EventDataStore: aws.String(arn)})
	})

	got, err := ct.GetEventDataStore(ctx, &cloudtrail.GetEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(got.EventDataStoreArn))
	assert.Equal(t, int32(90), aws.ToInt32(got.RetentionPeriod))

	list, err := ct.ListEventDataStores(ctx, &cloudtrail.ListEventDataStoresInput{})
	require.NoError(t, err)
	found := false
	for _, eds := range list.EventDataStores {
		if aws.ToString(eds.EventDataStoreArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "ListEventDataStores must include the new store")

	upd, err := ct.UpdateEventDataStore(ctx, &cloudtrail.UpdateEventDataStoreInput{
		EventDataStore:  aws.String(arn),
		RetentionPeriod: aws.Int32(120),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(120), aws.ToInt32(upd.RetentionPeriod))

	_, err = ct.StopEventDataStoreIngestion(ctx, &cloudtrail.StopEventDataStoreIngestionInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	got, err = ct.GetEventDataStore(ctx, &cloudtrail.GetEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.EventDataStoreStatusStoppedIngestion, got.Status)

	_, err = ct.StartEventDataStoreIngestion(ctx, &cloudtrail.StartEventDataStoreIngestionInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	got, err = ct.GetEventDataStore(ctx, &cloudtrail.GetEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.EventDataStoreStatusEnabled, got.Status)

	// Federation enable/disable round-trips the FederationStatus + role.
	fed, err := ct.EnableFederation(ctx, &cloudtrail.EnableFederationInput{
		EventDataStore:    aws.String(arn),
		FederationRoleArn: aws.String("arn:aws:iam::123456789012:role/ct-federation"),
	})
	require.NoError(t, err)
	assert.Equal(t, cttypes.FederationStatusEnabled, fed.FederationStatus)
	got, err = ct.GetEventDataStore(ctx, &cloudtrail.GetEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.FederationStatusEnabled, got.FederationStatus)

	disabled, err := ct.DisableFederation(ctx, &cloudtrail.DisableFederationInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.FederationStatusDisabled, disabled.FederationStatus)

	// Delete moves the store to PENDING_DELETION; Restore brings it back ENABLED.
	_, err = ct.DeleteEventDataStore(ctx, &cloudtrail.DeleteEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	got, err = ct.GetEventDataStore(ctx, &cloudtrail.GetEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.EventDataStoreStatusPendingDeletion, got.Status)

	restored, err := ct.RestoreEventDataStore(ctx, &cloudtrail.RestoreEventDataStoreInput{EventDataStore: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.EventDataStoreStatusEnabled, restored.Status)
}

// TestCloudTrail_LakeQuerySDK exercises the Lake query surface over the sim's
// recorded events: StartQuery → DescribeQuery / GetQueryResults / ListQueries →
// CancelQuery, plus GenerateQuery and SearchSampleQueries.
func TestCloudTrail_LakeQuerySDK(t *testing.T) {
	ct := cloudTrailClient()
	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/lake-query-eds"

	started, err := ct.StartQuery(ctx, &cloudtrail.StartQueryInput{
		QueryStatement: aws.String("SELECT eventName, eventSource FROM " + eds + " LIMIT 10"),
	})
	require.NoError(t, err)
	qid := aws.ToString(started.QueryId)
	require.NotEmpty(t, qid)

	desc, err := ct.DescribeQuery(ctx, &cloudtrail.DescribeQueryInput{QueryId: aws.String(qid)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.QueryStatusFinished, desc.QueryStatus)
	require.NotNil(t, desc.QueryStatistics)

	res, err := ct.GetQueryResults(ctx, &cloudtrail.GetQueryResultsInput{QueryId: aws.String(qid)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.QueryStatusFinished, res.QueryStatus)

	queries, err := ct.ListQueries(ctx, &cloudtrail.ListQueriesInput{EventDataStore: aws.String(eds)})
	require.NoError(t, err)
	listed := false
	for _, q := range queries.Queries {
		if aws.ToString(q.QueryId) == qid {
			listed = true
		}
	}
	assert.True(t, listed, "ListQueries must include the started query")

	cancelled, err := ct.CancelQuery(ctx, &cloudtrail.CancelQueryInput{QueryId: aws.String(qid)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.QueryStatusCancelled, cancelled.QueryStatus)

	gen, err := ct.GenerateQuery(ctx, &cloudtrail.GenerateQueryInput{
		EventDataStores: []string{eds},
		Prompt:          aws.String("list recent events"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(gen.QueryStatement))

	sample, err := ct.SearchSampleQueries(ctx, &cloudtrail.SearchSampleQueriesInput{
		SearchPhrase: aws.String("CreateBucket"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, sample.SearchResults)
}

// TestCloudTrail_DashboardLifecycleSDK exercises CreateDashboard →
// UpdateDashboard → StartDashboardRefresh → DeleteDashboard.
func TestCloudTrail_DashboardLifecycleSDK(t *testing.T) {
	ct := cloudTrailClient()
	const name = "ct-dashboard"

	created, err := ct.CreateDashboard(ctx, &cloudtrail.CreateDashboardInput{
		Name: aws.String(name),
		Widgets: []cttypes.RequestWidget{{
			QueryStatement: aws.String("SELECT eventName FROM $EDS_ID LIMIT 10"),
			ViewProperties: map[string]string{"type": "table"},
		}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.DashboardArn)
	require.NotEmpty(t, arn)
	assert.Equal(t, name, aws.ToString(created.Name))
	t.Cleanup(func() {
		_, _ = ct.DeleteDashboard(ctx, &cloudtrail.DeleteDashboardInput{DashboardId: aws.String(name)})
	})

	upd, err := ct.UpdateDashboard(ctx, &cloudtrail.UpdateDashboardInput{
		DashboardId: aws.String(arn),
		Widgets: []cttypes.RequestWidget{{
			QueryStatement: aws.String("SELECT eventSource FROM $EDS_ID LIMIT 5"),
			ViewProperties: map[string]string{"type": "table"},
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(upd.DashboardArn))

	got, err := ct.GetDashboard(ctx, &cloudtrail.GetDashboardInput{DashboardId: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(got.DashboardArn))

	dashboards, err := ct.ListDashboards(ctx, &cloudtrail.ListDashboardsInput{})
	require.NoError(t, err)
	listed := false
	for _, d := range dashboards.Dashboards {
		if aws.ToString(d.DashboardArn) == arn {
			listed = true
		}
	}
	assert.True(t, listed, "ListDashboards must include the new dashboard")

	refresh, err := ct.StartDashboardRefresh(ctx, &cloudtrail.StartDashboardRefreshInput{
		DashboardId: aws.String(arn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(refresh.RefreshId))

	_, err = ct.DeleteDashboard(ctx, &cloudtrail.DeleteDashboardInput{DashboardId: aws.String(name)})
	require.NoError(t, err)
}

// TestCloudTrail_ImportLifecycleSDK exercises StartImport → GetImport →
// ListImports / ListImportFailures → StopImport.
func TestCloudTrail_ImportLifecycleSDK(t *testing.T) {
	ct := cloudTrailClient()
	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/import-eds"

	started, err := ct.StartImport(ctx, &cloudtrail.StartImportInput{
		Destinations: []string{eds},
		ImportSource: &cttypes.ImportSource{
			S3: &cttypes.S3ImportSource{
				S3BucketAccessRoleArn: aws.String("arn:aws:iam::123456789012:role/ct-import"),
				S3BucketRegion:        aws.String("us-east-1"),
				S3LocationUri:         aws.String("s3://ct-import-bucket/AWSLogs/"),
			},
		},
		StartEventTime: aws.Time(time.Now().Add(-24 * time.Hour)),
		EndEventTime:   aws.Time(time.Now()),
	})
	require.NoError(t, err)
	importID := aws.ToString(started.ImportId)
	require.NotEmpty(t, importID)

	got, err := ct.GetImport(ctx, &cloudtrail.GetImportInput{ImportId: aws.String(importID)})
	require.NoError(t, err)
	assert.Equal(t, importID, aws.ToString(got.ImportId))
	require.NotNil(t, got.ImportStatistics)

	imports, err := ct.ListImports(ctx, &cloudtrail.ListImportsInput{})
	require.NoError(t, err)
	listed := false
	for _, imp := range imports.Imports {
		if aws.ToString(imp.ImportId) == importID {
			listed = true
		}
	}
	assert.True(t, listed, "ListImports must include the started import")

	failures, err := ct.ListImportFailures(ctx, &cloudtrail.ListImportFailuresInput{ImportId: aws.String(importID)})
	require.NoError(t, err)
	assert.Empty(t, failures.Failures)

	stopped, err := ct.StopImport(ctx, &cloudtrail.StopImportInput{ImportId: aws.String(importID)})
	require.NoError(t, err)
	assert.Equal(t, cttypes.ImportStatusStopped, stopped.ImportStatus)
}

// TestCloudTrail_GovernanceSDK exercises the resource-policy, organization
// delegated-admin, event-configuration, public-keys, and channel-update
// surfaces: PutResourcePolicy / GetResourcePolicy / DeleteResourcePolicy,
// RegisterOrganizationDelegatedAdmin / DeregisterOrganizationDelegatedAdmin,
// PutEventConfiguration / GetEventConfiguration, ListPublicKeys,
// ListInsightsMetricData / ListInsightsData, UpdateChannel.
func TestCloudTrail_GovernanceSDK(t *testing.T) {
	ct := cloudTrailClient()

	// Resource policy round-trip against a Lake channel ARN.
	const channelArn = "arn:aws:cloudtrail:us-east-1:123456789012:channel/gov-channel"
	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},"Action":"cloudtrail:GetChannel","Resource":"*"}]}`
	put, err := ct.PutResourcePolicy(ctx, &cloudtrail.PutResourcePolicyInput{
		ResourceArn:    aws.String(channelArn),
		ResourcePolicy: aws.String(policy),
	})
	require.NoError(t, err)
	assert.Equal(t, channelArn, aws.ToString(put.ResourceArn))
	gotPol, err := ct.GetResourcePolicy(ctx, &cloudtrail.GetResourcePolicyInput{ResourceArn: aws.String(channelArn)})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(gotPol.ResourcePolicy))
	_, err = ct.DeleteResourcePolicy(ctx, &cloudtrail.DeleteResourcePolicyInput{ResourceArn: aws.String(channelArn)})
	require.NoError(t, err)
	_, err = ct.GetResourcePolicy(ctx, &cloudtrail.GetResourcePolicyInput{ResourceArn: aws.String(channelArn)})
	assert.Error(t, err, "GetResourcePolicy after delete must error")

	// Organization delegated admin register/deregister.
	_, err = ct.RegisterOrganizationDelegatedAdmin(ctx, &cloudtrail.RegisterOrganizationDelegatedAdminInput{
		MemberAccountId: aws.String("210987654321"),
	})
	require.NoError(t, err)
	_, err = ct.DeregisterOrganizationDelegatedAdmin(ctx, &cloudtrail.DeregisterOrganizationDelegatedAdminInput{
		DelegatedAdminAccountId: aws.String("210987654321"),
	})
	require.NoError(t, err)

	// Event configuration round-trip against an event data store.
	edsName := "ct-gov-eds"
	created, err := ct.CreateEventDataStore(ctx, &cloudtrail.CreateEventDataStoreInput{Name: aws.String(edsName)})
	require.NoError(t, err)
	edsArn := aws.ToString(created.EventDataStoreArn)
	t.Cleanup(func() {
		_, _ = ct.DeleteEventDataStore(ctx, &cloudtrail.DeleteEventDataStoreInput{EventDataStore: aws.String(edsArn)})
	})
	putCfg, err := ct.PutEventConfiguration(ctx, &cloudtrail.PutEventConfigurationInput{
		EventDataStore: aws.String(edsArn),
		MaxEventSize:   cttypes.MaxEventSizeLarge,
	})
	require.NoError(t, err)
	assert.Equal(t, cttypes.MaxEventSizeLarge, putCfg.MaxEventSize)
	getCfg, err := ct.GetEventConfiguration(ctx, &cloudtrail.GetEventConfigurationInput{
		EventDataStore: aws.String(edsArn),
	})
	require.NoError(t, err)
	assert.Equal(t, edsArn, aws.ToString(getCfg.EventDataStoreArn))

	// ListPublicKeys returns a real (empty) key list.
	keys, err := ct.ListPublicKeys(ctx, &cloudtrail.ListPublicKeysInput{})
	require.NoError(t, err)
	assert.Empty(t, keys.PublicKeyList)

	// Insight data/metric reads return real (empty) results.
	_, err = ct.ListInsightsMetricData(ctx, &cloudtrail.ListInsightsMetricDataInput{
		EventName:   aws.String("PutBucketPolicy"),
		EventSource: aws.String("s3.amazonaws.com"),
		InsightType: cttypes.InsightTypeApiCallRateInsight,
	})
	require.NoError(t, err)

	// UpdateChannel changes a Lake channel's name.
	ch, err := ct.CreateChannel(ctx, &cloudtrail.CreateChannelInput{
		Name:   aws.String("ct-gov-channel"),
		Source: aws.String("Custom"),
		Destinations: []cttypes.Destination{{
			Type:     cttypes.DestinationTypeEventDataStore,
			Location: aws.String(edsArn),
		}},
	})
	require.NoError(t, err)
	chArn := aws.ToString(ch.ChannelArn)
	t.Cleanup(func() { _, _ = ct.DeleteChannel(ctx, &cloudtrail.DeleteChannelInput{Channel: aws.String(chArn)}) })
	updCh, err := ct.UpdateChannel(ctx, &cloudtrail.UpdateChannelInput{
		Channel: aws.String(chArn),
		Name:    aws.String("ct-gov-channel-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, "ct-gov-channel-renamed", aws.ToString(updCh.Name))
}
