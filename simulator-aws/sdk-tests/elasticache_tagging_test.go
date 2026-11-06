package aws_sdk_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestElastiCache_TaggingFaults expects the tagging operations to answer the
// faults the model lists: InvalidARN for an ARN that names no ElastiCache
// resource type, the not-found fault of the type an ARN names,
// TagQuotaPerResourceExceeded past 50 tags, TagNotFound for a key the
// resource does not carry, and InvalidReplicationGroupState while a group is
// not available.
func TestElastiCache_TaggingFaults(t *testing.T) {
	t.Parallel()
	c := ecClient()
	pg := uniquename.New("tag-pg")
	created, err := c.CreateCacheParameterGroup(ctx, &elasticache.CreateCacheParameterGroupInput{
		CacheParameterGroupName: aws.String(pg), CacheParameterGroupFamily: aws.String("redis7"), Description: aws.String("tagged"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteCacheParameterGroup(ctx, &elasticache.DeleteCacheParameterGroupInput{CacheParameterGroupName: aws.String(pg)})
	})
	arn := aws.ToString(created.CacheParameterGroup.ARN)
	tag := func(n int) []ectypes.Tag {
		tags := make([]ectypes.Tag, n)
		for i := range tags {
			tags[i] = ectypes.Tag{Key: aws.String(fmt.Sprintf("k%d", i)), Value: aws.String("v")}
		}
		return tags
	}
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{ResourceName: aws.String(arn), Tags: tag(51)})
	var quota *ectypes.TagQuotaPerResourceExceeded
	require.True(t, errors.As(err, &quota), "%v", err)
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{ResourceName: aws.String(arn), Tags: tag(50)})
	require.NoError(t, err)
	_, err = c.RemoveTagsFromResource(ctx, &elasticache.RemoveTagsFromResourceInput{ResourceName: aws.String(arn), TagKeys: []string{"k0", "absent"}})
	var missingTag *ectypes.TagNotFoundFault
	require.True(t, errors.As(err, &missingTag), "%v", err)
	listed, err := c.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{ResourceName: aws.String(arn)})
	require.NoError(t, err)
	assert.Len(t, listed.TagList, 50, "a refused removal removes nothing")

	_, err = c.ListTagsForResource(ctx, &elasticache.ListTagsForResourceInput{ResourceName: aws.String("not-an-arn")})
	var invalid *ectypes.InvalidARNFault
	require.True(t, errors.As(err, &invalid), "%v", err)
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{
		ResourceName: aws.String(strings.Replace(arn, ":parametergroup:"+pg, ":usergroup:absent", 1)), Tags: tag(1),
	})
	var missingGroup *ectypes.UserGroupNotFoundFault
	require.True(t, errors.As(err, &missingGroup), "%v", err)

	id := uniquename.New("tag-rg")
	group, err := c.CreateReplicationGroup(ctx, &elasticache.CreateReplicationGroupInput{
		ReplicationGroupId: aws.String(id), ReplicationGroupDescription: aws.String("tagged while creating"),
		Engine: aws.String("redis"), CacheNodeType: aws.String("cache.t3.micro"), NumCacheClusters: aws.Int32(1),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteReplicationGroup(ctx, &elasticache.DeleteReplicationGroupInput{ReplicationGroupId: aws.String(id)})
	})
	// The group stays creating until its node's container runs, which
	// outlasts the next request.
	groupARN := aws.ToString(group.ReplicationGroup.ARN)
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{ResourceName: aws.String(groupARN), Tags: tag(1)})
	var notAvailable *ectypes.InvalidReplicationGroupStateFault
	require.True(t, errors.As(err, &notAvailable), "%v", err)
	ecAwaitReplGroup(t, c, id)
	_, err = c.AddTagsToResource(ctx, &elasticache.AddTagsToResourceInput{ResourceName: aws.String(groupARN), Tags: tag(1)})
	require.NoError(t, err)
}
