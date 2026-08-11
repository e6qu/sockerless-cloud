package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudTrail_TrailLifecycleSDK exercises the full trail control plane:
// CreateTrail → GetTrail / DescribeTrails / ListTrails → StartLogging /
// GetTrailStatus / StopLogging → UpdateTrail → DeleteTrail, plus the tag and
// event/insight-selector surfaces hung off a trail.
func TestCloudTrail_TrailLifecycleSDK(t *testing.T) {
	ct := cloudTrailClient()
	s3c := s3Client()
	const bucket = "ct-ext-bucket"
	const trail = "ct-ext-trail"

	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}) })
	t.Cleanup(func() { _, _ = ct.DeleteTrail(ctx, &cloudtrail.DeleteTrailInput{Name: aws.String(trail)}) })

	created, err := ct.CreateTrail(ctx, &cloudtrail.CreateTrailInput{
		Name:         aws.String(trail),
		S3BucketName: aws.String(bucket),
	})
	require.NoError(t, err)
	assert.Equal(t, trail, aws.ToString(created.Name))
	assert.Equal(t, bucket, aws.ToString(created.S3BucketName))
	require.NotEmpty(t, aws.ToString(created.TrailARN))
	arn := aws.ToString(created.TrailARN)

	// GetTrail returns the full trail summary.
	got, err := ct.GetTrail(ctx, &cloudtrail.GetTrailInput{Name: aws.String(trail)})
	require.NoError(t, err)
	require.NotNil(t, got.Trail)
	assert.Equal(t, arn, aws.ToString(got.Trail.TrailARN))

	// DescribeTrails lists the trail.
	desc, err := ct.DescribeTrails(ctx, &cloudtrail.DescribeTrailsInput{})
	require.NoError(t, err)
	assert.True(t, ctTrailInDescribe(desc.TrailList, trail), "DescribeTrails must include the new trail")

	// ListTrails returns name/ARN/home-region for the trail.
	list, err := ct.ListTrails(ctx, &cloudtrail.ListTrailsInput{})
	require.NoError(t, err)
	found := false
	for _, ti := range list.Trails {
		if aws.ToString(ti.Name) == trail {
			found = true
			assert.Equal(t, arn, aws.ToString(ti.TrailARN))
			assert.NotEmpty(t, aws.ToString(ti.HomeRegion))
		}
	}
	assert.True(t, found, "ListTrails must include the new trail")

	// StartLogging flips IsLogging true; StopLogging flips it back.
	_, err = ct.StartLogging(ctx, &cloudtrail.StartLoggingInput{Name: aws.String(trail)})
	require.NoError(t, err)
	status, err := ct.GetTrailStatus(ctx, &cloudtrail.GetTrailStatusInput{Name: aws.String(trail)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(status.IsLogging))

	_, err = ct.StopLogging(ctx, &cloudtrail.StopLoggingInput{Name: aws.String(trail)})
	require.NoError(t, err)
	status, err = ct.GetTrailStatus(ctx, &cloudtrail.GetTrailStatusInput{Name: aws.String(trail)})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(status.IsLogging))

	// UpdateTrail changes the S3 key prefix.
	_, err = ct.UpdateTrail(ctx, &cloudtrail.UpdateTrailInput{
		Name:        aws.String(trail),
		S3KeyPrefix: aws.String("logs"),
	})
	require.NoError(t, err)
	got, err = ct.GetTrail(ctx, &cloudtrail.GetTrailInput{Name: aws.String(trail)})
	require.NoError(t, err)
	assert.Equal(t, "logs", aws.ToString(got.Trail.S3KeyPrefix))

	// Tags round-trip: Add → List → Remove.
	_, err = ct.AddTags(ctx, &cloudtrail.AddTagsInput{
		ResourceId: aws.String(arn),
		TagsList:   []cttypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	tags, err := ct.ListTags(ctx, &cloudtrail.ListTagsInput{ResourceIdList: []string{arn}})
	require.NoError(t, err)
	require.Len(t, tags.ResourceTagList, 1)
	require.Len(t, tags.ResourceTagList[0].TagsList, 1)
	assert.Equal(t, "env", aws.ToString(tags.ResourceTagList[0].TagsList[0].Key))

	_, err = ct.RemoveTags(ctx, &cloudtrail.RemoveTagsInput{
		ResourceId: aws.String(arn),
		TagsList:   []cttypes.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	tags, err = ct.ListTags(ctx, &cloudtrail.ListTagsInput{ResourceIdList: []string{arn}})
	require.NoError(t, err)
	assert.Empty(t, tags.ResourceTagList[0].TagsList)

	// Event selectors round-trip.
	_, err = ct.PutEventSelectors(ctx, &cloudtrail.PutEventSelectorsInput{
		TrailName: aws.String(trail),
		EventSelectors: []cttypes.EventSelector{{
			ReadWriteType:           cttypes.ReadWriteTypeAll,
			IncludeManagementEvents: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	es, err := ct.GetEventSelectors(ctx, &cloudtrail.GetEventSelectorsInput{TrailName: aws.String(trail)})
	require.NoError(t, err)
	require.Len(t, es.EventSelectors, 1)
	assert.Equal(t, cttypes.ReadWriteTypeAll, es.EventSelectors[0].ReadWriteType)
	assert.Equal(t, arn, aws.ToString(es.TrailARN))

	// Insight selectors round-trip.
	_, err = ct.PutInsightSelectors(ctx, &cloudtrail.PutInsightSelectorsInput{
		TrailName: aws.String(trail),
		InsightSelectors: []cttypes.InsightSelector{
			{InsightType: cttypes.InsightTypeApiCallRateInsight},
		},
	})
	require.NoError(t, err)
	is, err := ct.GetInsightSelectors(ctx, &cloudtrail.GetInsightSelectorsInput{TrailName: aws.String(trail)})
	require.NoError(t, err)
	require.Len(t, is.InsightSelectors, 1)
	assert.Equal(t, cttypes.InsightTypeApiCallRateInsight, is.InsightSelectors[0].InsightType)
	assert.Equal(t, arn, aws.ToString(is.TrailARN))
}

func ctTrailInDescribe(trails []cttypes.Trail, name string) bool {
	for _, tr := range trails {
		if aws.ToString(tr.Name) == name {
			return true
		}
	}
	return false
}

// TestCloudTrail_ChannelLifecycleSDK exercises CloudTrail Lake channel CRUD:
// CreateChannel → GetChannel / ListChannels → DeleteChannel.
func TestCloudTrail_ChannelLifecycleSDK(t *testing.T) {
	ct := cloudTrailClient()
	const name = "ct-ext-channel"
	const eds = "arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/EXAMPLE-eds"

	created, err := ct.CreateChannel(ctx, &cloudtrail.CreateChannelInput{
		Name:   aws.String(name),
		Source: aws.String("Custom"),
		Destinations: []cttypes.Destination{{
			Type:     cttypes.DestinationTypeEventDataStore,
			Location: aws.String(eds),
		}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.ChannelArn)
	require.NotEmpty(t, arn)
	assert.Equal(t, name, aws.ToString(created.Name))
	assert.Equal(t, "Custom", aws.ToString(created.Source))
	t.Cleanup(func() { _, _ = ct.DeleteChannel(ctx, &cloudtrail.DeleteChannelInput{Channel: aws.String(arn)}) })

	got, err := ct.GetChannel(ctx, &cloudtrail.GetChannelInput{Channel: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(got.ChannelArn))
	assert.Equal(t, name, aws.ToString(got.Name))
	require.Len(t, got.Destinations, 1)
	assert.Equal(t, eds, aws.ToString(got.Destinations[0].Location))

	list, err := ct.ListChannels(ctx, &cloudtrail.ListChannelsInput{})
	require.NoError(t, err)
	found := false
	for _, ch := range list.Channels {
		if aws.ToString(ch.ChannelArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "ListChannels must include the new channel")

	_, err = ct.DeleteChannel(ctx, &cloudtrail.DeleteChannelInput{Channel: aws.String(arn)})
	require.NoError(t, err)
	_, err = ct.GetChannel(ctx, &cloudtrail.GetChannelInput{Channel: aws.String(arn)})
	require.Error(t, err, "GetChannel on a deleted channel must error")
}
