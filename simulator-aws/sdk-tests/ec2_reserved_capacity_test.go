package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_ReservedInstancesLifecycle covers the Reserved Instances control
// plane: the offering catalog is described, an offering is purchased into an
// "active" Reserved Instance, the RI reads back, it is modified into new RIs,
// listed on the marketplace, the listing is cancelled, and a queued-deletion
// attempt fails honestly because the RI is active.
func TestEC2_ReservedInstancesLifecycle(t *testing.T) {
	c := ec2Client()

	offers, err := c.DescribeReservedInstancesOfferings(ctx, &ec2.DescribeReservedInstancesOfferingsInput{
		InstanceType: types.InstanceTypeT3Micro,
	})
	require.NoError(t, err)
	require.NotEmpty(t, offers.ReservedInstancesOfferings)
	var offeringID string
	for _, o := range offers.ReservedInstancesOfferings {
		assert.Equal(t, types.InstanceTypeT3Micro, o.InstanceType)
		assert.Equal(t, types.CurrencyCodeValuesUsd, o.CurrencyCode)
		assert.NotZero(t, aws.ToFloat32(o.FixedPrice))
		if o.Scope != types.ScopeAvailabilityZone {
			offeringID = aws.ToString(o.ReservedInstancesOfferingId)
		}
	}
	require.NotEmpty(t, offeringID)

	purch, err := c.PurchaseReservedInstancesOffering(ctx, &ec2.PurchaseReservedInstancesOfferingInput{
		ReservedInstancesOfferingId: aws.String(offeringID),
		InstanceCount:               aws.Int32(2),
	})
	require.NoError(t, err)
	riID := aws.ToString(purch.ReservedInstancesId)
	require.NotEmpty(t, riID)

	desc, err := c.DescribeReservedInstances(ctx, &ec2.DescribeReservedInstancesInput{
		ReservedInstancesIds: []string{riID},
	})
	require.NoError(t, err)
	require.Len(t, desc.ReservedInstances, 1)
	ri := desc.ReservedInstances[0]
	assert.Equal(t, types.InstanceTypeT3Micro, ri.InstanceType)
	assert.Equal(t, types.ReservedInstanceStateActive, ri.State)
	assert.Equal(t, int32(2), aws.ToInt32(ri.InstanceCount))
	assert.NotZero(t, aws.ToFloat32(ri.FixedPrice))

	// Modify into two new RIs split across two AZs.
	mod, err := c.ModifyReservedInstances(ctx, &ec2.ModifyReservedInstancesInput{
		ReservedInstancesIds: []string{riID},
		TargetConfigurations: []types.ReservedInstancesConfiguration{
			{InstanceType: types.InstanceTypeT3Micro, AvailabilityZone: aws.String("us-east-1a"), InstanceCount: aws.Int32(1)},
			{InstanceType: types.InstanceTypeT3Micro, AvailabilityZone: aws.String("us-east-1b"), InstanceCount: aws.Int32(1)},
		},
	})
	require.NoError(t, err)
	modID := aws.ToString(mod.ReservedInstancesModificationId)
	require.NotEmpty(t, modID)

	mods, err := c.DescribeReservedInstancesModifications(ctx, &ec2.DescribeReservedInstancesModificationsInput{
		ReservedInstancesModificationIds: []string{modID},
	})
	require.NoError(t, err)
	require.Len(t, mods.ReservedInstancesModifications, 1)
	assert.Len(t, mods.ReservedInstancesModifications[0].ModificationResults, 2)

	// List the RI on the marketplace, read the listing back, then cancel it.
	listOut, err := c.CreateReservedInstancesListing(ctx, &ec2.CreateReservedInstancesListingInput{
		ReservedInstancesId: aws.String(riID),
		InstanceCount:       aws.Int32(1),
		ClientToken:         aws.String("ric-token-1"),
		PriceSchedules: []types.PriceScheduleSpecification{
			{Term: aws.Int64(11), Price: aws.Float64(40.0), CurrencyCode: types.CurrencyCodeValuesUsd},
			{Term: aws.Int64(5), Price: aws.Float64(20.0), CurrencyCode: types.CurrencyCodeValuesUsd},
		},
	})
	require.NoError(t, err)
	require.Len(t, listOut.ReservedInstancesListings, 1)
	listingID := aws.ToString(listOut.ReservedInstancesListings[0].ReservedInstancesListingId)
	require.NotEmpty(t, listingID)
	assert.Equal(t, types.ListingStatusActive, listOut.ReservedInstancesListings[0].Status)
	assert.Len(t, listOut.ReservedInstancesListings[0].PriceSchedules, 2)

	lst, err := c.DescribeReservedInstancesListings(ctx, &ec2.DescribeReservedInstancesListingsInput{
		ReservedInstancesListingId: aws.String(listingID),
	})
	require.NoError(t, err)
	require.Len(t, lst.ReservedInstancesListings, 1)
	assert.Len(t, lst.ReservedInstancesListings[0].InstanceCounts, 2)

	cancel, err := c.CancelReservedInstancesListing(ctx, &ec2.CancelReservedInstancesListingInput{
		ReservedInstancesListingId: aws.String(listingID),
	})
	require.NoError(t, err)
	require.Len(t, cancel.ReservedInstancesListings, 1)
	assert.Equal(t, types.ListingStatusCancelled, cancel.ReservedInstancesListings[0].Status)

	// An active RI is not queued, so a queued-deletion attempt fails for it.
	del, err := c.DeleteQueuedReservedInstances(ctx, &ec2.DeleteQueuedReservedInstancesInput{
		ReservedInstancesIds: []string{riID},
	})
	require.NoError(t, err)
	assert.Len(t, del.FailedQueuedPurchaseDeletions, 1)
	assert.Empty(t, del.SuccessfulQueuedPurchaseDeletions)
}

// TestEC2_ReservedInstancesExchangeQuote covers the Convertible Reserved
// Instance exchange quote / accept flow: a quote is computed against a target
// offering, then the exchange is accepted and yields an exchange id.
func TestEC2_ReservedInstancesExchangeQuote(t *testing.T) {
	c := ec2Client()

	offers, err := c.DescribeReservedInstancesOfferings(ctx, &ec2.DescribeReservedInstancesOfferingsInput{
		InstanceType: types.InstanceTypeM5Large,
	})
	require.NoError(t, err)
	require.NotEmpty(t, offers.ReservedInstancesOfferings)
	srcOffering := aws.ToString(offers.ReservedInstancesOfferings[0].ReservedInstancesOfferingId)

	purch, err := c.PurchaseReservedInstancesOffering(ctx, &ec2.PurchaseReservedInstancesOfferingInput{
		ReservedInstancesOfferingId: aws.String(srcOffering),
		InstanceCount:               aws.Int32(1),
	})
	require.NoError(t, err)
	riID := aws.ToString(purch.ReservedInstancesId)

	tgtOffers, err := c.DescribeReservedInstancesOfferings(ctx, &ec2.DescribeReservedInstancesOfferingsInput{
		InstanceType: types.InstanceTypeC5Xlarge,
	})
	require.NoError(t, err)
	require.NotEmpty(t, tgtOffers.ReservedInstancesOfferings)
	tgtOffering := aws.ToString(tgtOffers.ReservedInstancesOfferings[0].ReservedInstancesOfferingId)

	quote, err := c.GetReservedInstancesExchangeQuote(ctx, &ec2.GetReservedInstancesExchangeQuoteInput{
		ReservedInstanceIds: []string{riID},
		TargetConfigurations: []types.TargetConfigurationRequest{
			{OfferingId: aws.String(tgtOffering), InstanceCount: aws.Int32(1)},
		},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(quote.IsValidExchange))
	assert.Equal(t, "USD", aws.ToString(quote.CurrencyCode))
	require.Len(t, quote.ReservedInstanceValueSet, 1)
	require.Len(t, quote.TargetConfigurationValueSet, 1)

	accept, err := c.AcceptReservedInstancesExchangeQuote(ctx, &ec2.AcceptReservedInstancesExchangeQuoteInput{
		ReservedInstanceIds: []string{riID},
		TargetConfigurations: []types.TargetConfigurationRequest{
			{OfferingId: aws.String(tgtOffering), InstanceCount: aws.Int32(1)},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(accept.ExchangeId))
}

// TestEC2_CapacityReservationBillingAndTopology covers the Capacity Reservation
// billing-ownership transfer (associate → describe → accept), the topology
// read-back, and the cancellation-quote ops, all operating on a real capacity
// reservation.
func TestEC2_CapacityReservationBillingAndTopology(t *testing.T) {
	c := ec2Client()

	cr, err := c.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:     aws.String("t3.medium"),
		InstancePlatform: types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone: aws.String("us-east-1a"),
		InstanceCount:    aws.Int32(6),
	})
	require.NoError(t, err)
	crID := aws.ToString(cr.CapacityReservation.CapacityReservationId)
	require.NotEmpty(t, crID)

	assoc, err := c.AssociateCapacityReservationBillingOwner(ctx, &ec2.AssociateCapacityReservationBillingOwnerInput{
		CapacityReservationId:           aws.String(crID),
		UnusedReservationBillingOwnerId: aws.String("210987654321"),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(assoc.Return))

	reqs, err := c.DescribeCapacityReservationBillingRequests(ctx, &ec2.DescribeCapacityReservationBillingRequestsInput{
		CapacityReservationIds: []string{crID},
		Role:                   types.CallerRoleOdcrOwner,
	})
	require.NoError(t, err)
	require.NotEmpty(t, reqs.CapacityReservationBillingRequests)
	var found bool
	for _, br := range reqs.CapacityReservationBillingRequests {
		if aws.ToString(br.CapacityReservationId) == crID {
			found = true
			assert.Equal(t, types.CapacityReservationBillingRequestStatusPending, br.Status)
			assert.Equal(t, "210987654321", aws.ToString(br.UnusedReservationBillingOwnerId))
		}
	}
	assert.True(t, found)

	accept, err := c.AcceptCapacityReservationBillingOwnership(ctx, &ec2.AcceptCapacityReservationBillingOwnershipInput{
		CapacityReservationId: aws.String(crID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(accept.Return))

	topo, err := c.DescribeCapacityReservationTopology(ctx, &ec2.DescribeCapacityReservationTopologyInput{
		CapacityReservationIds: []string{crID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, topo.CapacityReservations)
	assert.Equal(t, "t3.medium", aws.ToString(topo.CapacityReservations[0].InstanceType))
	assert.NotEmpty(t, topo.CapacityReservations[0].NetworkNodes)

	quote, err := c.CreateCapacityReservationCancellationQuote(ctx, &ec2.CreateCapacityReservationCancellationQuoteInput{
		CapacityReservationId: aws.String(crID),
	})
	require.NoError(t, err)
	require.NotNil(t, quote.CapacityReservationCancellationQuote)
	assert.Equal(t, crID, aws.ToString(quote.CapacityReservationCancellationQuote.CapacityReservationId))
	quoteID := aws.ToString(quote.CapacityReservationCancellationQuote.CapacityReservationCancellationQuoteId)
	require.NotEmpty(t, quoteID)

	quotes, err := c.DescribeCapacityReservationCancellationQuotes(ctx, &ec2.DescribeCapacityReservationCancellationQuotesInput{
		CapacityReservationCancellationQuoteIds: []string{quoteID},
	})
	require.NoError(t, err)
	require.Len(t, quotes.CapacityReservationCancellationQuotes, 1)
	assert.Equal(t, crID, aws.ToString(quotes.CapacityReservationCancellationQuotes[0].CapacityReservationId))

	// Disassociate / reject paths on a fresh reservation.
	cr2, err := c.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:     aws.String("t3.medium"),
		InstancePlatform: types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone: aws.String("us-east-1a"),
		InstanceCount:    aws.Int32(2),
	})
	require.NoError(t, err)
	cr2ID := aws.ToString(cr2.CapacityReservation.CapacityReservationId)
	_, err = c.AssociateCapacityReservationBillingOwner(ctx, &ec2.AssociateCapacityReservationBillingOwnerInput{
		CapacityReservationId:           aws.String(cr2ID),
		UnusedReservationBillingOwnerId: aws.String("210987654321"),
	})
	require.NoError(t, err)
	rej, err := c.RejectCapacityReservationBillingOwnership(ctx, &ec2.RejectCapacityReservationBillingOwnershipInput{
		CapacityReservationId: aws.String(cr2ID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(rej.Return))
	disc, err := c.DisassociateCapacityReservationBillingOwner(ctx, &ec2.DisassociateCapacityReservationBillingOwnerInput{
		CapacityReservationId:           aws.String(cr2ID),
		UnusedReservationBillingOwnerId: aws.String("210987654321"),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(disc.Return))
}

// TestEC2_CapacityReservationSplitAndMove covers splitting a capacity
// reservation into a new one and moving instances between two reservations,
// plus the per-instance attribute modify.
func TestEC2_CapacityReservationSplitAndMove(t *testing.T) {
	c := ec2Client()

	src, err := c.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:     aws.String("m5.large"),
		InstancePlatform: types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone: aws.String("us-east-1a"),
		InstanceCount:    aws.Int32(10),
	})
	require.NoError(t, err)
	srcID := aws.ToString(src.CapacityReservation.CapacityReservationId)

	split, err := c.CreateCapacityReservationBySplitting(ctx, &ec2.CreateCapacityReservationBySplittingInput{
		SourceCapacityReservationId: aws.String(srcID),
		InstanceCount:               aws.Int32(4),
	})
	require.NoError(t, err)
	require.NotNil(t, split.SourceCapacityReservation)
	require.NotNil(t, split.DestinationCapacityReservation)
	assert.Equal(t, int32(4), aws.ToInt32(split.InstanceCount))
	assert.Equal(t, int32(6), aws.ToInt32(split.SourceCapacityReservation.TotalInstanceCount))
	assert.Equal(t, int32(4), aws.ToInt32(split.DestinationCapacityReservation.TotalInstanceCount))
	destID := aws.ToString(split.DestinationCapacityReservation.CapacityReservationId)

	move, err := c.MoveCapacityReservationInstances(ctx, &ec2.MoveCapacityReservationInstancesInput{
		SourceCapacityReservationId:      aws.String(srcID),
		DestinationCapacityReservationId: aws.String(destID),
		InstanceCount:                    aws.Int32(2),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(move.InstanceCount))
	assert.Equal(t, int32(4), aws.ToInt32(move.SourceCapacityReservation.TotalInstanceCount))
	assert.Equal(t, int32(6), aws.ToInt32(move.DestinationCapacityReservation.TotalInstanceCount))

	modAttr, err := c.ModifyInstanceCapacityReservationAttributes(ctx, &ec2.ModifyInstanceCapacityReservationAttributesInput{
		InstanceId: aws.String("i-0123456789abcdef0"),
		CapacityReservationSpecification: &types.CapacityReservationSpecification{
			CapacityReservationPreference: types.CapacityReservationPreferenceOpen,
		},
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(modAttr.Return))
}

// TestEC2_CapacityBlockLifecycle covers the Capacity Block (ML) flow: offerings
// are described, an offering is purchased into a capacity-block-backed capacity
// reservation, the block reads back via describe + status, and it is extended.
func TestEC2_CapacityBlockLifecycle(t *testing.T) {
	c := ec2Client()

	offers, err := c.DescribeCapacityBlockOfferings(ctx, &ec2.DescribeCapacityBlockOfferingsInput{
		InstanceType:          aws.String("p5.48xlarge"),
		InstanceCount:         aws.Int32(2),
		CapacityDurationHours: aws.Int32(48),
	})
	require.NoError(t, err)
	require.NotEmpty(t, offers.CapacityBlockOfferings)
	offering := offers.CapacityBlockOfferings[0]
	assert.Equal(t, "p5.48xlarge", aws.ToString(offering.InstanceType))
	assert.Equal(t, int32(2), aws.ToInt32(offering.InstanceCount))
	assert.NotEmpty(t, aws.ToString(offering.UpfrontFee))
	offeringID := aws.ToString(offering.CapacityBlockOfferingId)

	purch, err := c.PurchaseCapacityBlock(ctx, &ec2.PurchaseCapacityBlockInput{
		CapacityBlockOfferingId: aws.String(offeringID),
		InstancePlatform:        types.CapacityReservationInstancePlatformLinuxUnix,
	})
	require.NoError(t, err)
	require.NotNil(t, purch.CapacityReservation)
	require.NotEmpty(t, purch.CapacityBlocks)
	crID := aws.ToString(purch.CapacityReservation.CapacityReservationId)
	cbID := aws.ToString(purch.CapacityBlocks[0].CapacityBlockId)
	require.NotEmpty(t, crID)
	require.NotEmpty(t, cbID)

	desc, err := c.DescribeCapacityBlocks(ctx, &ec2.DescribeCapacityBlocksInput{
		CapacityBlockIds: []string{cbID},
	})
	require.NoError(t, err)
	require.Len(t, desc.CapacityBlocks, 1)
	assert.Contains(t, desc.CapacityBlocks[0].CapacityReservationIds, crID)

	status, err := c.DescribeCapacityBlockStatus(ctx, &ec2.DescribeCapacityBlockStatusInput{
		CapacityBlockIds: []string{cbID},
	})
	require.NoError(t, err)
	require.Len(t, status.CapacityBlockStatuses, 1)
	assert.Equal(t, int32(2), aws.ToInt32(status.CapacityBlockStatuses[0].TotalCapacity))

	extOffers, err := c.DescribeCapacityBlockExtensionOfferings(ctx, &ec2.DescribeCapacityBlockExtensionOfferingsInput{
		CapacityReservationId:               aws.String(crID),
		CapacityBlockExtensionDurationHours: aws.Int32(24),
	})
	require.NoError(t, err)
	require.NotEmpty(t, extOffers.CapacityBlockExtensionOfferings)
	extOfferingID := aws.ToString(extOffers.CapacityBlockExtensionOfferings[0].CapacityBlockExtensionOfferingId)

	pExt, err := c.PurchaseCapacityBlockExtension(ctx, &ec2.PurchaseCapacityBlockExtensionInput{
		CapacityReservationId:            aws.String(crID),
		CapacityBlockExtensionOfferingId: aws.String(extOfferingID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, pExt.CapacityBlockExtensions)
	assert.Equal(t, crID, aws.ToString(pExt.CapacityBlockExtensions[0].CapacityReservationId))

	hist, err := c.DescribeCapacityBlockExtensionHistory(ctx, &ec2.DescribeCapacityBlockExtensionHistoryInput{
		CapacityReservationIds: []string{crID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hist.CapacityBlockExtensions)
	assert.Equal(t, crID, aws.ToString(hist.CapacityBlockExtensions[0].CapacityReservationId))
}
