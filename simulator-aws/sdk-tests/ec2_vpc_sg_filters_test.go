package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_DescribeVpcsFiltering covers vpc-id filter / multi-id
// return the right VPC(s), and CidrBlockAssociationSet is populated.
func TestEC2_DescribeVpcsFiltering(t *testing.T) {
	c := ec2Client()
	a, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.61.0.0/16")})
	require.NoError(t, err)
	aID := aws.ToString(a.Vpc.VpcId)
	b, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.62.0.0/16")})
	require.NoError(t, err)
	bID := aws.ToString(b.Vpc.VpcId)

	// vpc-id filter returns exactly the requested VPC.
	byFilter, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{bID}}},
	})
	require.NoError(t, err)
	require.Len(t, byFilter.Vpcs, 1)
	assert.Equal(t, bID, aws.ToString(byFilter.Vpcs[0].VpcId))
	assert.Equal(t, "10.62.0.0/16", aws.ToString(byFilter.Vpcs[0].CidrBlock))

	// CidrBlockAssociationSet must carry the primary CIDR (data.aws_vpc reads it).
	require.NotEmpty(t, byFilter.Vpcs[0].CidrBlockAssociationSet)
	assoc := byFilter.Vpcs[0].CidrBlockAssociationSet[0]
	assert.Equal(t, "10.62.0.0/16", aws.ToString(assoc.CidrBlock))
	require.NotNil(t, assoc.CidrBlockState)
	assert.Equal(t, types.VpcCidrBlockStateCodeAssociated, assoc.CidrBlockState.State)

	// --vpc-ids with both returns both.
	byIDs, err := c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{aID, bID}})
	require.NoError(t, err)
	require.Len(t, byIDs.Vpcs, 2)
}

// TestEC2_DescribeSecurityGroupsVpcFilter covers the vpc-id filter
// must not leak SGs from other VPCs.
func TestEC2_DescribeSecurityGroupsVpcFilter(t *testing.T) {
	c := ec2Client()
	a, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.63.0.0/16")})
	require.NoError(t, err)
	aID := aws.ToString(a.Vpc.VpcId)
	b, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.64.0.0/16")})
	require.NoError(t, err)
	bID := aws.ToString(b.Vpc.VpcId)

	sgA, err := c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("filter-sg-a"), Description: aws.String("a"), VpcId: aws.String(aID),
	})
	require.NoError(t, err)
	_, err = c.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName: aws.String("filter-sg-b"), Description: aws.String("b"), VpcId: aws.String(bID),
	})
	require.NoError(t, err)

	out, err := c.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{{Name: aws.String("vpc-id"), Values: []string{aID}}},
	})
	require.NoError(t, err)
	for _, sg := range out.SecurityGroups {
		assert.Equal(t, aID, aws.ToString(sg.VpcId), "vpc-id filter must not return SGs from other VPCs")
	}
	// The VPC-A SG is present...
	var names []string
	for _, sg := range out.SecurityGroups {
		names = append(names, aws.ToString(sg.GroupName))
	}
	assert.Contains(t, names, "filter-sg-a")
	assert.NotContains(t, names, "filter-sg-b")
	_ = sgA
}
