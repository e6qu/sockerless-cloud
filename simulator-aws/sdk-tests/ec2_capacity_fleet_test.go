package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createFleetLaunchTemplate creates a launch template the EC2 Fleet / Spot
// Fleet tests reference, returning its id.
func createFleetLaunchTemplate(t *testing.T, c *ec2.Client, name string) string {
	t.Helper()
	out, err := c.CreateLaunchTemplate(ctx, &ec2.CreateLaunchTemplateInput{
		LaunchTemplateName: aws.String(name),
		LaunchTemplateData: &types.RequestLaunchTemplateData{
			ImageId:      aws.String("ami-12345678"),
			InstanceType: types.InstanceTypeT3Micro,
		},
	})
	require.NoError(t, err)
	return aws.ToString(out.LaunchTemplate.LaunchTemplateId)
}

// TestEC2_CapacityReservationLifecycle covers the capacity-reservation control
// plane: a reservation reserves N instances of a type/AZ and settles "active"
// with AvailableInstanceCount == count; Describe reads it back; Modify changes
// the count; usage and groups read back; Cancel transitions it to "cancelled".
func TestEC2_CapacityReservationLifecycle(t *testing.T) {
	c := ec2Client()

	cr, err := c.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:     aws.String("t3.micro"),
		InstancePlatform: types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone: aws.String("us-east-1a"),
		InstanceCount:    aws.Int32(4),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeCapacityReservation,
			Tags:         []types.Tag{{Key: aws.String("team"), Value: aws.String("ci")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, cr.CapacityReservation)
	id := aws.ToString(cr.CapacityReservation.CapacityReservationId)
	require.NotEmpty(t, id)
	assert.Equal(t, types.CapacityReservationStateActive, cr.CapacityReservation.State)
	assert.Equal(t, int32(4), aws.ToInt32(cr.CapacityReservation.TotalInstanceCount))
	assert.Equal(t, int32(4), aws.ToInt32(cr.CapacityReservation.AvailableInstanceCount))
	assert.NotEmpty(t, aws.ToString(cr.CapacityReservation.CapacityReservationArn))

	desc, err := c.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{
		CapacityReservationIds: []string{id},
	})
	require.NoError(t, err)
	require.Len(t, desc.CapacityReservations, 1)
	got := desc.CapacityReservations[0]
	assert.Equal(t, "t3.micro", aws.ToString(got.InstanceType))
	assert.Equal(t, "us-east-1a", aws.ToString(got.AvailabilityZone))
	require.Len(t, got.Tags, 1)
	assert.Equal(t, "team", aws.ToString(got.Tags[0].Key))

	// Filter by instance-type.
	byType, err := c.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{
		Filters: []types.Filter{{Name: aws.String("instance-type"), Values: []string{"t3.micro"}}},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, byType.CapacityReservations)

	_, err = c.ModifyCapacityReservation(ctx, &ec2.ModifyCapacityReservationInput{
		CapacityReservationId: aws.String(id),
		InstanceCount:         aws.Int32(6),
	})
	require.NoError(t, err)
	desc2, err := c.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{
		CapacityReservationIds: []string{id},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(6), aws.ToInt32(desc2.CapacityReservations[0].TotalInstanceCount))

	usage, err := c.GetCapacityReservationUsage(ctx, &ec2.GetCapacityReservationUsageInput{
		CapacityReservationId: aws.String(id),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(usage.CapacityReservationId))
	assert.Equal(t, int32(6), aws.ToInt32(usage.TotalInstanceCount))
	require.Len(t, usage.InstanceUsages, 1)

	groups, err := c.GetGroupsForCapacityReservation(ctx, &ec2.GetGroupsForCapacityReservationInput{
		CapacityReservationId: aws.String(id),
	})
	require.NoError(t, err)
	assert.Empty(t, groups.CapacityReservationGroups)

	_, err = c.CancelCapacityReservation(ctx, &ec2.CancelCapacityReservationInput{
		CapacityReservationId: aws.String(id),
	})
	require.NoError(t, err)
	desc3, err := c.DescribeCapacityReservations(ctx, &ec2.DescribeCapacityReservationsInput{
		CapacityReservationIds: []string{id},
	})
	require.NoError(t, err)
	assert.Equal(t, types.CapacityReservationStateCancelled, desc3.CapacityReservations[0].State)
}

// TestEC2_CapacityReservationFleetLifecycle covers the capacity-reservation
// fleet control plane: a fleet aggregates per-instance-type reservations toward
// a target, settles "active", reads back, modifies, and cancels.
func TestEC2_CapacityReservationFleetLifecycle(t *testing.T) {
	c := ec2Client()

	crf, err := c.CreateCapacityReservationFleet(ctx, &ec2.CreateCapacityReservationFleetInput{
		TotalTargetCapacity: aws.Int32(4),
		InstanceTypeSpecifications: []types.ReservationFleetInstanceSpecification{{
			InstanceType:     types.InstanceTypeT3Micro,
			InstancePlatform: types.CapacityReservationInstancePlatformLinuxUnix,
			AvailabilityZone: aws.String("us-east-1a"),
			Weight:           aws.Float64(1),
			Priority:         aws.Int32(1),
		}},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeCapacityReservationFleet,
			Tags:         []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}},
		}},
	})
	require.NoError(t, err)
	fleetID := aws.ToString(crf.CapacityReservationFleetId)
	require.NotEmpty(t, fleetID)
	assert.Equal(t, types.CapacityReservationFleetStateActive, crf.State)
	assert.Equal(t, int32(4), aws.ToInt32(crf.TotalTargetCapacity))
	require.Len(t, crf.FleetCapacityReservations, 1)
	assert.Equal(t, types.InstanceTypeT3Micro, crf.FleetCapacityReservations[0].InstanceType)

	desc, err := c.DescribeCapacityReservationFleets(ctx, &ec2.DescribeCapacityReservationFleetsInput{
		CapacityReservationFleetIds: []string{fleetID},
	})
	require.NoError(t, err)
	require.Len(t, desc.CapacityReservationFleets, 1)
	assert.Equal(t, int32(4), aws.ToInt32(desc.CapacityReservationFleets[0].TotalTargetCapacity))
	require.Len(t, desc.CapacityReservationFleets[0].InstanceTypeSpecifications, 1)

	_, err = c.ModifyCapacityReservationFleet(ctx, &ec2.ModifyCapacityReservationFleetInput{
		CapacityReservationFleetId: aws.String(fleetID),
		TotalTargetCapacity:        aws.Int32(8),
	})
	require.NoError(t, err)

	cancel, err := c.CancelCapacityReservationFleets(ctx, &ec2.CancelCapacityReservationFleetsInput{
		CapacityReservationFleetIds: []string{fleetID},
	})
	require.NoError(t, err)
	require.Len(t, cancel.SuccessfulFleetCancellations, 1)
	assert.Equal(t, fleetID, aws.ToString(cancel.SuccessfulFleetCancellations[0].CapacityReservationFleetId))
}

// TestEC2_FleetLifecycle covers the EC2 Fleet control plane: CreateFleet
// requests target capacity from a launch template and settles active with
// backing instances; Describe / DescribeFleetInstances / DescribeFleetHistory
// read it back; ModifyFleet adjusts target; DeleteFleets removes it.
func TestEC2_FleetLifecycle(t *testing.T) {
	c := ec2Client()
	ltID := createFleetLaunchTemplate(t, c, "fleet-lt-sdk")

	created, err := c.CreateFleet(ctx, &ec2.CreateFleetInput{
		Type: types.FleetTypeMaintain,
		TargetCapacitySpecification: &types.TargetCapacitySpecificationRequest{
			TotalTargetCapacity:       aws.Int32(2),
			DefaultTargetCapacityType: types.DefaultTargetCapacityTypeOnDemand,
		},
		LaunchTemplateConfigs: []types.FleetLaunchTemplateConfigRequest{{
			LaunchTemplateSpecification: &types.FleetLaunchTemplateSpecificationRequest{
				LaunchTemplateId: aws.String(ltID),
				Version:          aws.String("1"),
			},
			Overrides: []types.FleetLaunchTemplateOverridesRequest{{
				InstanceType: types.InstanceTypeT3Micro,
			}},
		}},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeFleet,
			Tags:         []types.Tag{{Key: aws.String("name"), Value: aws.String("sdk-fleet")}},
		}},
	})
	require.NoError(t, err)
	fleetID := aws.ToString(created.FleetId)
	require.NotEmpty(t, fleetID)

	desc, err := c.DescribeFleets(ctx, &ec2.DescribeFleetsInput{FleetIds: []string{fleetID}})
	require.NoError(t, err)
	require.Len(t, desc.Fleets, 1)
	f := desc.Fleets[0]
	assert.Equal(t, types.FleetStateCodeActive, f.FleetState)
	assert.Equal(t, int32(2), aws.ToInt32(f.TargetCapacitySpecification.TotalTargetCapacity))
	require.Len(t, f.Tags, 1)

	insts, err := c.DescribeFleetInstances(ctx, &ec2.DescribeFleetInstancesInput{FleetId: aws.String(fleetID)})
	require.NoError(t, err)
	assert.Len(t, insts.ActiveInstances, 2)

	hist, err := c.DescribeFleetHistory(ctx, &ec2.DescribeFleetHistoryInput{
		FleetId:   aws.String(fleetID),
		StartTime: aws.Time(time.Now().Add(-time.Hour)),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, hist.HistoryRecords)

	_, err = c.ModifyFleet(ctx, &ec2.ModifyFleetInput{
		FleetId: aws.String(fleetID),
		TargetCapacitySpecification: &types.TargetCapacitySpecificationRequest{
			TotalTargetCapacity: aws.Int32(3),
		},
	})
	require.NoError(t, err)

	del, err := c.DeleteFleets(ctx, &ec2.DeleteFleetsInput{
		FleetIds:           []string{fleetID},
		TerminateInstances: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, del.SuccessfulFleetDeletions, 1)
	assert.Equal(t, fleetID, aws.ToString(del.SuccessfulFleetDeletions[0].FleetId))
}

// TestEC2_SpotInstanceRequestLifecycle covers spot instance requests: a request
// settles active/fulfilled with a backing instance and spot price; Describe
// reads it back; Cancel transitions it to cancelled.
func TestEC2_SpotInstanceRequestLifecycle(t *testing.T) {
	c := ec2Client()

	req, err := c.RequestSpotInstances(ctx, &ec2.RequestSpotInstancesInput{
		SpotPrice:     aws.String("0.0035"),
		InstanceCount: aws.Int32(2),
		Type:          types.SpotInstanceTypeOneTime,
		LaunchSpecification: &types.RequestSpotLaunchSpecification{
			ImageId:      aws.String("ami-12345678"),
			InstanceType: types.InstanceTypeT3Micro,
		},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeSpotInstancesRequest,
			Tags:         []types.Tag{{Key: aws.String("job"), Value: aws.String("build")}},
		}},
	})
	require.NoError(t, err)
	require.Len(t, req.SpotInstanceRequests, 2)
	sir := req.SpotInstanceRequests[0]
	sirID := aws.ToString(sir.SpotInstanceRequestId)
	require.NotEmpty(t, sirID)
	assert.Equal(t, types.SpotInstanceStateActive, sir.State)
	assert.Equal(t, "0.0035", aws.ToString(sir.SpotPrice))
	assert.NotEmpty(t, aws.ToString(sir.InstanceId))

	desc, err := c.DescribeSpotInstanceRequests(ctx, &ec2.DescribeSpotInstanceRequestsInput{
		SpotInstanceRequestIds: []string{sirID},
	})
	require.NoError(t, err)
	require.Len(t, desc.SpotInstanceRequests, 1)
	assert.Equal(t, "fulfilled", aws.ToString(desc.SpotInstanceRequests[0].Status.Code))

	cancel, err := c.CancelSpotInstanceRequests(ctx, &ec2.CancelSpotInstanceRequestsInput{
		SpotInstanceRequestIds: []string{sirID},
	})
	require.NoError(t, err)
	require.Len(t, cancel.CancelledSpotInstanceRequests, 1)
	assert.Equal(t, types.CancelSpotInstanceRequestStateCancelled, cancel.CancelledSpotInstanceRequests[0].State)
}

// TestEC2_SpotFleetLifecycle covers spot fleets: RequestSpotFleet settles
// active/fulfilled against its target; Describe / instances / history read it
// back; Modify adjusts target; Cancel transitions it to cancelled.
func TestEC2_SpotFleetLifecycle(t *testing.T) {
	c := ec2Client()

	req, err := c.RequestSpotFleet(ctx, &ec2.RequestSpotFleetInput{
		SpotFleetRequestConfig: &types.SpotFleetRequestConfigData{
			IamFleetRole:   aws.String("arn:aws:iam::123456789012:role/spot-fleet"),
			TargetCapacity: aws.Int32(2),
			SpotPrice:      aws.String("0.0035"),
			LaunchSpecifications: []types.SpotFleetLaunchSpecification{{
				ImageId:      aws.String("ami-12345678"),
				InstanceType: types.InstanceTypeT3Micro,
			}},
		},
	})
	require.NoError(t, err)
	sfrID := aws.ToString(req.SpotFleetRequestId)
	require.NotEmpty(t, sfrID)

	desc, err := c.DescribeSpotFleetRequests(ctx, &ec2.DescribeSpotFleetRequestsInput{
		SpotFleetRequestIds: []string{sfrID},
	})
	require.NoError(t, err)
	require.Len(t, desc.SpotFleetRequestConfigs, 1)
	cfg := desc.SpotFleetRequestConfigs[0]
	assert.Equal(t, types.BatchStateActive, cfg.SpotFleetRequestState)
	require.NotNil(t, cfg.SpotFleetRequestConfig)
	assert.Equal(t, int32(2), aws.ToInt32(cfg.SpotFleetRequestConfig.TargetCapacity))

	insts, err := c.DescribeSpotFleetInstances(ctx, &ec2.DescribeSpotFleetInstancesInput{
		SpotFleetRequestId: aws.String(sfrID),
	})
	require.NoError(t, err)
	assert.Len(t, insts.ActiveInstances, 2)

	hist, err := c.DescribeSpotFleetRequestHistory(ctx, &ec2.DescribeSpotFleetRequestHistoryInput{
		SpotFleetRequestId: aws.String(sfrID),
		StartTime:          aws.Time(time.Now()),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, hist.HistoryRecords)

	_, err = c.ModifySpotFleetRequest(ctx, &ec2.ModifySpotFleetRequestInput{
		SpotFleetRequestId: aws.String(sfrID),
		TargetCapacity:     aws.Int32(3),
	})
	require.NoError(t, err)

	cancel, err := c.CancelSpotFleetRequests(ctx, &ec2.CancelSpotFleetRequestsInput{
		SpotFleetRequestIds: []string{sfrID},
		TerminateInstances:  aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, cancel.SuccessfulFleetRequests, 1)
	assert.Equal(t, sfrID, aws.ToString(cancel.SuccessfulFleetRequests[0].SpotFleetRequestId))
}

// TestEC2_SpotDatafeedAndReadOnly covers the spot data feed subscription
// (create/describe/delete) plus the read-only spot price history and placement
// scores, and the scheduled-instance + host-reservation read/purchase ops.
func TestEC2_SpotDatafeedAndReadOnly(t *testing.T) {
	c := ec2Client()

	sub, err := c.CreateSpotDatafeedSubscription(ctx, &ec2.CreateSpotDatafeedSubscriptionInput{
		Bucket: aws.String("my-spot-logs"),
		Prefix: aws.String("feeds/"),
	})
	require.NoError(t, err)
	require.NotNil(t, sub.SpotDatafeedSubscription)
	assert.Equal(t, "my-spot-logs", aws.ToString(sub.SpotDatafeedSubscription.Bucket))

	got, err := c.DescribeSpotDatafeedSubscription(ctx, &ec2.DescribeSpotDatafeedSubscriptionInput{})
	require.NoError(t, err)
	assert.Equal(t, "my-spot-logs", aws.ToString(got.SpotDatafeedSubscription.Bucket))

	_, err = c.DeleteSpotDatafeedSubscription(ctx, &ec2.DeleteSpotDatafeedSubscriptionInput{})
	require.NoError(t, err)

	prices, err := c.DescribeSpotPriceHistory(ctx, &ec2.DescribeSpotPriceHistoryInput{
		InstanceTypes:       []types.InstanceType{types.InstanceTypeT3Micro},
		ProductDescriptions: []string{"Linux/UNIX"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, prices.SpotPriceHistory)
	assert.Equal(t, types.InstanceTypeT3Micro, prices.SpotPriceHistory[0].InstanceType)

	scores, err := c.GetSpotPlacementScores(ctx, &ec2.GetSpotPlacementScoresInput{
		TargetCapacity: aws.Int32(10),
		RegionNames:    []string{"us-east-1"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, scores.SpotPlacementScores)
	assert.Equal(t, "us-east-1", aws.ToString(scores.SpotPlacementScores[0].Region))
}

// TestEC2_ScheduledInstances covers the scheduled-instance ops: availability,
// purchase, describe read-back, and run.
func TestEC2_ScheduledInstances(t *testing.T) {
	c := ec2Client()

	avail, err := c.DescribeScheduledInstanceAvailability(ctx, &ec2.DescribeScheduledInstanceAvailabilityInput{
		FirstSlotStartTimeRange: &types.SlotDateTimeRangeRequest{
			EarliestTime: aws.Time(time.Now()),
			LatestTime:   aws.Time(time.Now().Add(24 * time.Hour)),
		},
		Recurrence: &types.ScheduledInstanceRecurrenceRequest{
			Frequency: aws.String("Weekly"),
			Interval:  aws.Int32(1),
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, avail.ScheduledInstanceAvailabilitySet)
	purchaseToken := aws.ToString(avail.ScheduledInstanceAvailabilitySet[0].PurchaseToken)

	purchase, err := c.PurchaseScheduledInstances(ctx, &ec2.PurchaseScheduledInstancesInput{
		PurchaseRequests: []types.PurchaseRequest{{
			InstanceCount: aws.Int32(1),
			PurchaseToken: aws.String(purchaseToken),
		}},
	})
	require.NoError(t, err)
	require.Len(t, purchase.ScheduledInstanceSet, 1)
	sciID := aws.ToString(purchase.ScheduledInstanceSet[0].ScheduledInstanceId)
	require.NotEmpty(t, sciID)

	desc, err := c.DescribeScheduledInstances(ctx, &ec2.DescribeScheduledInstancesInput{
		ScheduledInstanceIds: []string{sciID},
	})
	require.NoError(t, err)
	require.Len(t, desc.ScheduledInstanceSet, 1)
	assert.Equal(t, sciID, aws.ToString(desc.ScheduledInstanceSet[0].ScheduledInstanceId))

	run, err := c.RunScheduledInstances(ctx, &ec2.RunScheduledInstancesInput{
		ScheduledInstanceId: aws.String(sciID),
		InstanceCount:       aws.Int32(1),
		LaunchSpecification: &types.ScheduledInstancesLaunchSpecification{
			ImageId:      aws.String("ami-12345678"),
			InstanceType: aws.String("t3.micro"),
		},
	})
	require.NoError(t, err)
	require.Len(t, run.InstanceIdSet, 1)
}

// TestEC2_HostReservations covers the Dedicated Host Reservation ops: offerings,
// purchase preview, purchase, and describe read-back.
func TestEC2_HostReservations(t *testing.T) {
	c := ec2Client()

	offerings, err := c.DescribeHostReservationOfferings(ctx, &ec2.DescribeHostReservationOfferingsInput{})
	require.NoError(t, err)
	require.NotEmpty(t, offerings.OfferingSet)
	offeringID := aws.ToString(offerings.OfferingSet[0].OfferingId)

	preview, err := c.GetHostReservationPurchasePreview(ctx, &ec2.GetHostReservationPurchasePreviewInput{
		OfferingId: aws.String(offeringID),
		HostIdSet:  []string{"h-0123456789abcdef0"},
	})
	require.NoError(t, err)
	require.NotNil(t, preview.Purchase)
	require.Len(t, preview.Purchase, 1)

	purchase, err := c.PurchaseHostReservation(ctx, &ec2.PurchaseHostReservationInput{
		OfferingId: aws.String(offeringID),
		HostIdSet:  []string{"h-0123456789abcdef0"},
	})
	require.NoError(t, err)
	require.Len(t, purchase.Purchase, 1)
	hrID := aws.ToString(purchase.Purchase[0].HostReservationId)
	require.NotEmpty(t, hrID)

	desc, err := c.DescribeHostReservations(ctx, &ec2.DescribeHostReservationsInput{
		HostReservationIdSet: []string{hrID},
	})
	require.NoError(t, err)
	require.Len(t, desc.HostReservationSet, 1)
	assert.Equal(t, hrID, aws.ToString(desc.HostReservationSet[0].HostReservationId))
}
