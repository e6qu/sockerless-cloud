package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_S3TableIntegrationSourceRoundTrip covers
// AssociateSourceToS3TableIntegration, ListSourcesForS3TableIntegration, and
// DisassociateSourceFromS3TableIntegration for an S3 Table Integration
// data-source association.
func TestLogs_S3TableIntegrationSourceRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	integrationArn := "arn:aws:logs:us-east-1:123456789012:integration:test-s3-table"
	var identifier string
	defer func() {
		if identifier != "" {
			cw.DisassociateSourceFromS3TableIntegration(ctx, &cloudwatchlogs.DisassociateSourceFromS3TableIntegrationInput{
				Identifier: aws.String(identifier),
			})
		}
	}()

	assoc, err := cw.AssociateSourceToS3TableIntegration(ctx, &cloudwatchlogs.AssociateSourceToS3TableIntegrationInput{
		IntegrationArn: aws.String(integrationArn),
		DataSource: &cwlogtypes.DataSource{
			Name: aws.String("my-data-source"),
			Type: aws.String("VENDED_LOGS"),
		},
	})
	require.NoError(t, err)
	identifier = aws.ToString(assoc.Identifier)
	require.NotEmpty(t, identifier)

	list, err := cw.ListSourcesForS3TableIntegration(ctx, &cloudwatchlogs.ListSourcesForS3TableIntegrationInput{
		IntegrationArn: aws.String(integrationArn),
	})
	require.NoError(t, err)
	found := false
	for _, s := range list.Sources {
		if aws.ToString(s.Identifier) == identifier {
			found = true
			require.NotNil(t, s.DataSource)
			assert.Equal(t, "my-data-source", aws.ToString(s.DataSource.Name))
			assert.Equal(t, cwlogtypes.S3TableIntegrationSourceStatusActive, s.Status)
		}
	}
	assert.True(t, found, "the associated data source must be listed")

	dis, err := cw.DisassociateSourceFromS3TableIntegration(ctx, &cloudwatchlogs.DisassociateSourceFromS3TableIntegrationInput{
		Identifier: aws.String(identifier),
	})
	require.NoError(t, err)
	assert.Equal(t, identifier, aws.ToString(dis.Identifier))
	identifier = ""

	// A second list returns no sources for this integration.
	after, err := cw.ListSourcesForS3TableIntegration(ctx, &cloudwatchlogs.ListSourcesForS3TableIntegrationInput{
		IntegrationArn: aws.String(integrationArn),
	})
	require.NoError(t, err)
	for _, s := range after.Sources {
		assert.NotEqual(t, identifier, aws.ToString(s.Identifier))
	}
}

// TestLogs_GetLogFields derives the field set from a log group's stored JSON
// events; GetLogFields exposes the structural @-fields plus the parsed keys.
func TestLogs_GetLogFields(t *testing.T) {
	cw := cwLogsClient()
	group, stream := "/test/getlogfields", "s1"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(now), Message: aws.String(`{"level":"ERROR","code":500,"ok":true}`)},
		},
	})
	require.NoError(t, err)

	fields, err := cw.GetLogFields(ctx, &cloudwatchlogs.GetLogFieldsInput{
		DataSourceName: aws.String(group),
		DataSourceType: aws.String("LOG_GROUP"),
	})
	require.NoError(t, err)
	byName := map[string]string{}
	for _, f := range fields.LogFields {
		require.NotNil(t, f.LogFieldType)
		byName[aws.ToString(f.LogFieldName)] = aws.ToString(f.LogFieldType.Type)
	}
	assert.Contains(t, byName, "@timestamp")
	assert.Contains(t, byName, "@message")
	assert.Equal(t, "string", byName["level"])
	assert.Equal(t, "double", byName["code"])
	assert.Equal(t, "boolean", byName["ok"])

	// An unknown data source yields an honestly-empty field set.
	empty, err := cw.GetLogFields(ctx, &cloudwatchlogs.GetLogFieldsInput{
		DataSourceName: aws.String("/test/getlogfields-missing"),
		DataSourceType: aws.String("LOG_GROUP"),
	})
	require.NoError(t, err)
	assert.Empty(t, empty.LogFields)
}

// TestLogs_ListLogGroupsForQuery reads the log groups a stored Insights query
// was started against back through ListLogGroupsForQuery.
func TestLogs_ListLogGroupsForQuery(t *testing.T) {
	cw := cwLogsClient()
	group, stream := "/test/lgforquery", "s1"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(now), Message: aws.String(`{"level":"INFO"}`)},
		},
	})
	require.NoError(t, err)

	start, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(group),
		QueryString:  aws.String(`fields @message`),
		StartTime:    aws.Int64(now/1000 - 3600),
		EndTime:      aws.Int64(now/1000 + 3600),
	})
	require.NoError(t, err)

	listed, err := cw.ListLogGroupsForQuery(ctx, &cloudwatchlogs.ListLogGroupsForQueryInput{
		QueryId: start.QueryId,
	})
	require.NoError(t, err)
	assert.Contains(t, listed.LogGroupIdentifiers, group)
}

// TestLogs_PutBearerTokenAuthentication enables bearer-token authentication for
// an existing log group.
func TestLogs_PutBearerTokenAuthentication(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/bearer-token"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	_, err = cw.PutBearerTokenAuthentication(ctx, &cloudwatchlogs.PutBearerTokenAuthenticationInput{
		LogGroupIdentifier:               aws.String(group),
		BearerTokenAuthenticationEnabled: aws.Bool(true),
	})
	require.NoError(t, err)

	// An unknown log group is rejected, not silently accepted.
	_, err = cw.PutBearerTokenAuthentication(ctx, &cloudwatchlogs.PutBearerTokenAuthenticationInput{
		LogGroupIdentifier:               aws.String("/test/bearer-token-missing"),
		BearerTokenAuthenticationEnabled: aws.Bool(true),
	})
	require.Error(t, err)
}

// TestLogs_UpdateDeliveryConfiguration creates a vended-log delivery and then
// updates its record fields and field delimiter via
// UpdateDeliveryConfiguration.
func TestLogs_UpdateDeliveryConfiguration(t *testing.T) {
	cw := cwLogsClient()
	srcName := "test-update-delivery-source"
	dstName := "test-update-delivery-destination"
	defer cw.DeleteDeliverySource(ctx, &cloudwatchlogs.DeleteDeliverySourceInput{Name: aws.String(srcName)})
	defer cw.DeleteDeliveryDestination(ctx, &cloudwatchlogs.DeleteDeliveryDestinationInput{Name: aws.String(dstName)})

	_, err := cw.PutDeliverySource(ctx, &cloudwatchlogs.PutDeliverySourceInput{
		Name:        aws.String(srcName),
		LogType:     aws.String("APPLICATION_LOGS"),
		ResourceArn: aws.String("arn:aws:bedrock:us-east-1:123456789012:provisioned-model/abc"),
	})
	require.NoError(t, err)

	dstPut, err := cw.PutDeliveryDestination(ctx, &cloudwatchlogs.PutDeliveryDestinationInput{
		Name:                    aws.String(dstName),
		DeliveryDestinationType: cwlogtypes.DeliveryDestinationTypeS3,
		OutputFormat:            cwlogtypes.OutputFormatPlain,
		DeliveryDestinationConfiguration: &cwlogtypes.DeliveryDestinationConfiguration{
			DestinationResourceArn: aws.String("arn:aws:s3:::my-update-delivery-bucket"),
		},
	})
	require.NoError(t, err)
	dstArn := aws.ToString(dstPut.DeliveryDestination.Arn)

	created, err := cw.CreateDelivery(ctx, &cloudwatchlogs.CreateDeliveryInput{
		DeliverySourceName:     aws.String(srcName),
		DeliveryDestinationArn: aws.String(dstArn),
	})
	require.NoError(t, err)
	deliveryID := aws.ToString(created.Delivery.Id)
	require.NotEmpty(t, deliveryID)
	defer cw.DeleteDelivery(ctx, &cloudwatchlogs.DeleteDeliveryInput{Id: aws.String(deliveryID)})

	_, err = cw.UpdateDeliveryConfiguration(ctx, &cloudwatchlogs.UpdateDeliveryConfigurationInput{
		Id:             aws.String(deliveryID),
		RecordFields:   []string{"timestamp", "message"},
		FieldDelimiter: aws.String("|"),
	})
	require.NoError(t, err)

	got, err := cw.GetDelivery(ctx, &cloudwatchlogs.GetDeliveryInput{Id: aws.String(deliveryID)})
	require.NoError(t, err)
	assert.Equal(t, []string{"timestamp", "message"}, got.Delivery.RecordFields)
	assert.Equal(t, "|", aws.ToString(got.Delivery.FieldDelimiter))

	// Updating an unknown delivery is rejected.
	_, err = cw.UpdateDeliveryConfiguration(ctx, &cloudwatchlogs.UpdateDeliveryConfigurationInput{
		Id:           aws.String("00000000-0000-0000-0000-000000000000"),
		RecordFields: []string{"timestamp"},
	})
	require.Error(t, err)
}
