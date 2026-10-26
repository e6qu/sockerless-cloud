package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudTrail_LookupEventsTimeWindowSDK expects StartTime and EndTime to
// bound the returned events.
func TestCloudTrail_LookupEventsTimeWindowSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	ec2c := ec2Client()

	before := time.Now().Add(-1 * time.Minute)
	_, err := ec2c.CreateVolume(ctx, &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
	})
	require.NoError(t, err)
	after := time.Now().Add(1 * time.Minute)

	inWindow, err := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
		StartTime: aws.Time(before), EndTime: aws.Time(after),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, inWindow.Events, "events created now must fall in [before, after]")

	futureStart := time.Now().Add(1 * time.Hour)
	futureEnd := time.Now().Add(2 * time.Hour)
	future, err := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
		StartTime: aws.Time(futureStart), EndTime: aws.Time(futureEnd),
	})
	require.NoError(t, err)
	assert.Empty(t, future.Events, "no events should fall in a future-only window")
}

// TestCloudTrail_LookupEventsPaginationStableCursorSDK holds NextToken to a
// cursor after the last-seen (EventTime, EventId): an event ingested at the
// head mid-pagination must not bring an earlier page's EventId back on a later
// page, as an absolute offset would.
func TestCloudTrail_LookupEventsPaginationStableCursorSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	ec2c := ec2Client()

	for i := 0; i < 8; i++ {
		_, err := ec2c.CreateVolume(ctx, &ec2.CreateVolumeInput{
			AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
		})
		require.NoError(t, err)
	}

	seen := map[string]int{}
	var token *string
	pages := 0
	for pages < 12 {
		out, err := ct.LookupEvents(ctx, &cloudtrail.LookupEventsInput{
			MaxResults: aws.Int32(3),
			NextToken:  token,
		})
		require.NoError(t, err)
		for _, e := range out.Events {
			seen[aws.ToString(e.EventId)]++
		}
		pages++

		// Insert one event at the head after the first page.
		if pages == 1 {
			_, err := ec2c.CreateVolume(ctx, &ec2.CreateVolumeInput{
				AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1),
			})
			require.NoError(t, err)
		}

		if out.NextToken == nil || len(out.Events) == 0 {
			break
		}
		token = out.NextToken
	}

	require.GreaterOrEqual(t, pages, 2, "test must span multiple pages")
	for id, n := range seen {
		assert.Equalf(t, 1, n, "EventId %s returned on %d pages — the cursor must yield each event exactly once", id, n)
	}
}
