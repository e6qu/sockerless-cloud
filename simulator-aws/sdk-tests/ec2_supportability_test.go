package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scores read the capacity the simulator's host has left, which no test
// fixes, so these assertions hold on any host: an amount past any machine
// scores low, zones on one host score alike, and the aggregate is the best
// combination's score.
func TestEC2_CapacityManagerSupportabilityScores(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	zones, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(zones.AvailabilityZones), 2)
	ids := []string{aws.ToString(zones.AvailabilityZones[0].ZoneId), aws.ToString(zones.AvailabilityZones[1].ZoneId)}

	huge, err := c.GetCapacityManagerSupportabilityScores(ctx, &ec2.GetCapacityManagerSupportabilityScoresInput{
		CapacityAmount:              aws.Int32(1_000_000),
		CapacityUnit:                types.CapacityUnitVcpus,
		MarketType:                  types.SupportabilityMarketTypeOnDemand,
		CapacityAvailabilityZoneIds: ids,
		InstanceTypes:               []string{"t3.micro", "m5.large"},
	})
	require.NoError(t, err)
	require.Len(t, huge.CapacityManagerSupportabilityScores, 4, "one score per instance type and zone")
	for _, s := range huge.CapacityManagerSupportabilityScores {
		assert.Equal(t, types.SupportabilityScoreLow, s.Score, "%s in %s", aws.ToString(s.InstanceType), aws.ToString(s.AvailabilityZoneId))
		assert.Contains(t, ids, aws.ToString(s.AvailabilityZoneId))
	}
	assert.Equal(t, types.SupportabilityScoreLow, huge.AggregateScore)

	one, err := c.GetCapacityManagerSupportabilityScores(ctx, &ec2.GetCapacityManagerSupportabilityScoresInput{
		CapacityAmount:              aws.Int32(1),
		CapacityUnit:                types.CapacityUnitInstances,
		MarketType:                  types.SupportabilityMarketTypeSpot,
		CapacityAvailabilityZoneIds: ids,
		InstanceTypes:               []string{"t3.nano"},
	})
	require.NoError(t, err)
	require.Len(t, one.CapacityManagerSupportabilityScores, 2)
	assert.Equal(t, one.CapacityManagerSupportabilityScores[0].Score, one.CapacityManagerSupportabilityScores[1].Score,
		"zones that share one host score alike")
	assert.Equal(t, one.CapacityManagerSupportabilityScores[0].Score, one.AggregateScore)

	byRequirements, err := c.GetCapacityManagerSupportabilityScores(ctx, &ec2.GetCapacityManagerSupportabilityScoresInput{
		CapacityAmount:              aws.Int32(1_000_000),
		CapacityUnit:                types.CapacityUnitInstances,
		MarketType:                  types.SupportabilityMarketTypeOnDemand,
		CapacityAvailabilityZoneIds: ids[:1],
		InstanceRequirementsWithMetadata: &types.InstanceRequirementsWithMetadataRequest{
			ArchitectureTypes: []types.ArchitectureType{types.ArchitectureTypeArm64},
			InstanceRequirements: &types.InstanceRequirementsRequest{
				VCpuCount: &types.VCpuCountRangeRequest{Min: aws.Int32(2), Max: aws.Int32(2)},
				MemoryMiB: &types.MemoryMiBRequest{Min: aws.Int32(4096), Max: aws.Int32(4096)},
			},
		},
	})
	require.NoError(t, err)
	matching, err := c.GetInstanceTypesFromInstanceRequirements(ctx, &ec2.GetInstanceTypesFromInstanceRequirementsInput{
		ArchitectureTypes:   []types.ArchitectureType{types.ArchitectureTypeArm64},
		VirtualizationTypes: []types.VirtualizationType{types.VirtualizationTypeHvm},
		InstanceRequirements: &types.InstanceRequirementsRequest{
			VCpuCount: &types.VCpuCountRangeRequest{Min: aws.Int32(2), Max: aws.Int32(2)},
			MemoryMiB: &types.MemoryMiBRequest{Min: aws.Int32(4096), Max: aws.Int32(4096)},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, matching.InstanceTypes)
	var scored, resolved []string
	for _, s := range byRequirements.CapacityManagerSupportabilityScores {
		scored = append(scored, aws.ToString(s.InstanceType))
	}
	for _, it := range matching.InstanceTypes {
		resolved = append(resolved, aws.ToString(it.InstanceType))
	}
	assert.Equal(t, resolved, scored, "the requirements resolve to the instance types scored")

	_, err = c.GetCapacityManagerSupportabilityScores(ctx, &ec2.GetCapacityManagerSupportabilityScoresInput{
		CapacityAmount:              aws.Int32(1),
		CapacityUnit:                types.CapacityUnitInstances,
		MarketType:                  types.SupportabilityMarketTypeOnDemand,
		CapacityAvailabilityZoneIds: []string{"zzz9-az9"},
		InstanceTypes:               []string{"t3.micro"},
	})
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "an unknown zone ID is refused: %v", err)
	assert.Equal(t, "InvalidParameterValue", apiErr.ErrorCode())
}

// An instance type whose memory no host holds is refused with the server
// error Amazon EC2 answers when it has no capacity for a launch.
func TestEC2_RunInstancesRefusesATypeTheHostCannotHold(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String(ec2AmazonLinuxAMI(t)),
		InstanceType: types.InstanceType("u7in-32tb.224xlarge"),
		MinCount:     aws.Int32(1), MaxCount: aws.Int32(1),
	})
	terminateEC2InstancesOnCleanup(t, c, run)
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "a 32 TiB instance launched: %v", err)
	assert.Equal(t, "InsufficientInstanceCapacity", apiErr.ErrorCode())
}

// Spot placement scores read the host's capacity: a target past any host
// scores 1, a Region the simulator does not serve scores 1, and a
// single-zone request scores each zone.
func TestEC2_SpotPlacementScoresReadTheHost(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	huge, err := c.GetSpotPlacementScores(ctx, &ec2.GetSpotPlacementScoresInput{
		TargetCapacity: aws.Int32(1_000_000),
		InstanceTypes:  []string{"t3.micro"},
		RegionNames:    []string{"us-east-1", "ap-southeast-7"},
	})
	require.NoError(t, err)
	require.Len(t, huge.SpotPlacementScores, 2)
	for _, s := range huge.SpotPlacementScores {
		assert.Equal(t, int32(1), aws.ToInt32(s.Score), aws.ToString(s.Region))
	}

	zones, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	require.NoError(t, err)
	single, err := c.GetSpotPlacementScores(ctx, &ec2.GetSpotPlacementScoresInput{
		TargetCapacity:         aws.Int32(1),
		InstanceTypes:          []string{"t3.nano"},
		SingleAvailabilityZone: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, single.SpotPlacementScores, len(zones.AvailabilityZones))
	for i, s := range single.SpotPlacementScores {
		assert.Equal(t, aws.ToString(zones.AvailabilityZones[i].ZoneId), aws.ToString(s.AvailabilityZoneId))
		assert.Equal(t, aws.ToInt32(single.SpotPlacementScores[0].Score), aws.ToInt32(s.Score), "zones on one host score alike")
	}
}
