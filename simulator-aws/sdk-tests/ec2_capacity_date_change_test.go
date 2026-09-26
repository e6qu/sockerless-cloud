package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/require"
)

const fourteenDays = int64(14 * 24 * 60 * 60)

func createFutureDatedReservation(t *testing.T, client *ec2.Client, lead time.Duration) *ec2types.CapacityReservation {
	t.Helper()
	out, err := client.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:          aws.String("m5.24xlarge"),
		InstancePlatform:      ec2types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone:      aws.String("us-east-1a"),
		InstanceCount:         aws.Int32(2),
		InstanceMatchCriteria: ec2types.InstanceMatchCriteriaTargeted,
		StartDate:             aws.Time(time.Now().Add(lead).Truncate(time.Second)),
		CommitmentDuration:    aws.Int64(fourteenDays),
	})
	require.NoError(t, err)
	return out.CapacityReservation
}

// TestEC2SDK_FutureDatedCapacityReservation holds a future-dated reservation
// to its documented life: scheduled with no instances until its start date,
// with a commitment, and only targeted matching.
func TestEC2SDK_FutureDatedCapacityReservation(t *testing.T) {
	client := ec2Client()
	cr := createFutureDatedReservation(t, client, 10*24*time.Hour)
	require.Equal(t, ec2types.CapacityReservationStateScheduled, cr.State)
	require.Equal(t, int32(0), aws.ToInt32(cr.TotalInstanceCount), "a scheduled reservation holds no instances yet")
	require.NotNil(t, cr.CommitmentInfo)
	require.Equal(t, int32(2), aws.ToInt32(cr.CommitmentInfo.CommittedInstanceCount))
	require.Equal(t, fourteenDays, aws.ToInt64(cr.CommitmentInfo.CommitmentDuration))
	require.Equal(t, cr.StartDate.Add(14*24*time.Hour), aws.ToTime(cr.CommitmentInfo.CommitmentEndDate))
	require.Equal(t, aws.ToTime(cr.StartDate), aws.ToTime(cr.OriginalStartDate))

	_, err := client.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId: cr.CapacityReservationId, InstanceCount: aws.Int32(4),
	})
	require.Equal(t, "IncorrectState", errCode(t, err), "a scheduled reservation changes only its dates")

	for name, input := range map[string]*ec2.CreateCapacityReservationInput{
		"open matching": {InstanceMatchCriteria: ec2types.InstanceMatchCriteriaOpen,
			StartDate: aws.Time(time.Now().Add(10 * 24 * time.Hour)), CommitmentDuration: aws.Int64(fourteenDays)},
		"too soon": {InstanceMatchCriteria: ec2types.InstanceMatchCriteriaTargeted,
			StartDate: aws.Time(time.Now().Add(2 * 24 * time.Hour)), CommitmentDuration: aws.Int64(fourteenDays)},
		"too short a commitment": {InstanceMatchCriteria: ec2types.InstanceMatchCriteriaTargeted,
			StartDate: aws.Time(time.Now().Add(10 * 24 * time.Hour)), CommitmentDuration: aws.Int64(24 * 60 * 60)},
	} {
		input.InstanceType, input.InstanceCount = aws.String("m5.24xlarge"), aws.Int32(2)
		input.InstancePlatform, input.AvailabilityZone = ec2types.CapacityReservationInstancePlatformLinuxUnix, aws.String("us-east-1a")
		_, err := client.CreateCapacityReservation(ctx, input)
		require.Equal(t, "InvalidParameterValue", errCode(t, err), name)
	}
}

// TestEC2SDK_CapacityReservationDateChangeQuote postpones a scheduled
// reservation by quote: the quote states the terms, applying it moves the
// start date once, and the 30-day and commitment rules hold.
func TestEC2SDK_CapacityReservationDateChangeQuote(t *testing.T) {
	client := ec2Client()
	cr := createFutureDatedReservation(t, client, 20*24*time.Hour)
	start := aws.ToTime(cr.StartDate)

	// More than two weeks before the start: no additional commitment.
	quoted, err := client.CreateCapacityReservationDateChangeQuote(ctx, &ec2.CreateCapacityReservationDateChangeQuoteInput{
		CapacityReservationId: cr.CapacityReservationId,
		NewStartDate:          aws.Time(start.Add(7 * 24 * time.Hour)),
	})
	require.NoError(t, err)
	quote := quoted.CapacityReservationModificationQuote
	require.Equal(t, ec2types.CapacityReservationModificationQuoteStateActive, quote.QuoteState)
	require.Equal(t, start, aws.ToTime(quote.CurrentConfiguration.StartDate))
	update := quote.ModificationTerms.ReservationUpdate
	require.Equal(t, fourteenDays, int64(aws.ToInt32(update.NewCommitmentDuration)))
	require.Equal(t, start.Add(7*24*time.Hour), aws.ToTime(update.NewStartDate))
	require.Equal(t, start.Add(21*24*time.Hour), aws.ToTime(update.NewCommitmentEndDate))
	require.WithinDuration(t, time.Now().Add(24*time.Hour), aws.ToTime(quote.ExpirationTime), time.Minute)

	_, err = client.CreateCapacityReservationDateChangeQuote(ctx, &ec2.CreateCapacityReservationDateChangeQuoteInput{
		CapacityReservationId: cr.CapacityReservationId,
		NewStartDate:          aws.Time(start.Add(31 * 24 * time.Hour)),
	})
	require.Equal(t, "InvalidParameterValue", errCode(t, err), "past 30 days from the original start")

	described, err := client.DescribeCapacityReservationDateChangeQuotes(ctx, &ec2.DescribeCapacityReservationDateChangeQuotesInput{
		CapacityReservationModificationQuoteIds: []string{aws.ToString(quote.CapacityReservationModificationQuoteId)},
	})
	require.NoError(t, err)
	require.Len(t, described.CapacityReservationModificationQuotes, 1)

	_, err = client.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId: cr.CapacityReservationId,
		StartDate:             aws.Time(start.Add(7 * 24 * time.Hour)),
	})
	require.Equal(t, "InvalidParameterCombination", errCode(t, err), "a start date moves only by quote")

	_, err = client.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId: cr.CapacityReservationId,
		QuoteId:               quote.CapacityReservationModificationQuoteId,
	})
	require.Equal(t, "InvalidParameterValue", errCode(t, err), "a quote applies only with its terms accepted")

	_, err = client.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId:   cr.CapacityReservationId,
		QuoteId:                 quote.CapacityReservationModificationQuoteId,
		AcceptModificationTerms: aws.Bool(true),
	})
	require.NoError(t, err)
	after, err := client.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{
		CapacityReservationIds: []string{aws.ToString(cr.CapacityReservationId)},
	})
	require.NoError(t, err)
	moved := after.CapacityReservations[0]
	require.Equal(t, start.Add(7*24*time.Hour), aws.ToTime(moved.StartDate))
	require.Equal(t, start, aws.ToTime(moved.OriginalStartDate), "the original start date never moves")
	require.Equal(t, ec2types.CapacityReservationAdjustmentStatusApplied, moved.AdjustmentStatus)

	_, err = client.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId:   cr.CapacityReservationId,
		QuoteId:                 quote.CapacityReservationModificationQuoteId,
		AcceptModificationTerms: aws.Bool(true),
	})
	require.Equal(t, "InvalidParameterValue", errCode(t, err), "a quote is used once")

	// Within two weeks of the start, each day of postponement adds a day of
	// commitment.
	soon := createFutureDatedReservation(t, client, 6*24*time.Hour)
	soonQuote, err := client.CreateCapacityReservationDateChangeQuote(ctx, &ec2.CreateCapacityReservationDateChangeQuoteInput{
		CapacityReservationId: soon.CapacityReservationId,
		NewStartDate:          aws.Time(aws.ToTime(soon.StartDate).Add(3 * 24 * time.Hour)),
	})
	require.NoError(t, err)
	require.Equal(t, fourteenDays+3*24*60*60,
		int64(aws.ToInt32(soonQuote.CapacityReservationModificationQuote.ModificationTerms.ReservationUpdate.NewCommitmentDuration)))

	// Pagination walks every quote once.
	seen := map[string]bool{}
	var token *string
	for {
		page, err := client.DescribeCapacityReservationDateChangeQuotes(ctx, &ec2.DescribeCapacityReservationDateChangeQuotesInput{
			MaxResults: aws.Int32(1), NextToken: token,
		})
		require.NoError(t, err)
		for _, q := range page.CapacityReservationModificationQuotes {
			require.False(t, seen[aws.ToString(q.CapacityReservationModificationQuoteId)], "a quote listed twice")
			seen[aws.ToString(q.CapacityReservationModificationQuoteId)] = true
		}
		if page.NextToken == nil {
			break
		}
		token = page.NextToken
	}
	require.True(t, seen[aws.ToString(quote.CapacityReservationModificationQuoteId)])
	require.True(t, seen[aws.ToString(soonQuote.CapacityReservationModificationQuote.CapacityReservationModificationQuoteId)])

	immediate, err := client.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType: aws.String("m5.large"), InstancePlatform: ec2types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone: aws.String("us-east-1a"), InstanceCount: aws.Int32(1),
	})
	require.NoError(t, err)
	_, err = client.CreateCapacityReservationDateChangeQuote(ctx, &ec2.CreateCapacityReservationDateChangeQuoteInput{
		CapacityReservationId: immediate.CapacityReservation.CapacityReservationId,
		NewStartDate:          aws.Time(time.Now().Add(24 * time.Hour)),
	})
	require.Equal(t, "IncorrectState", errCode(t, err), "an active reservation has no start date to postpone")
}

// TestEC2SDK_DescribeCapacityReservationsPaginates walks every reservation one
// page at a time with the SDK's own paginator, each exactly once.
func TestEC2SDK_DescribeCapacityReservationsPaginates(t *testing.T) {
	client := ec2Client()
	created := map[string]bool{}
	for range 3 {
		out, err := client.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
			InstanceType: aws.String("m5.large"), InstancePlatform: ec2types.CapacityReservationInstancePlatformLinuxUnix,
			AvailabilityZone: aws.String("us-east-1a"), InstanceCount: aws.Int32(1),
		})
		require.NoError(t, err)
		created[aws.ToString(out.CapacityReservation.CapacityReservationId)] = true
	}
	seen := map[string]bool{}
	pages := ec2.NewDescribeCapacityReservationsPaginator(client, &ec2.DescribeCapacityReservationsInput{MaxResults: aws.Int32(1)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.CapacityReservations), 1)
		for _, cr := range page.CapacityReservations {
			id := aws.ToString(cr.CapacityReservationId)
			require.False(t, seen[id], "reservation %s listed twice", id)
			seen[id] = true
		}
	}
	for id := range created {
		require.True(t, seen[id], "reservation %s never listed", id)
	}
}
