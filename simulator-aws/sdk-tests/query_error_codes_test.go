package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	ectypes "github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/require"
)

// TestQueryErrorsReachTheSDKAsTheirTypes expects each refusal on the wire
// under the code the model's awsQueryError trait gives it, which is how the
// SDK picks the typed error: a refusal sent under its shape name arrives as a
// generic one instead.
func TestQueryErrorsReachTheSDKAsTheirTypes(t *testing.T) {
	t.Parallel()
	ec := ecClient()
	_, err := ec.DescribeCacheParameterGroups(ctx, &elasticache.DescribeCacheParameterGroupsInput{CacheParameterGroupName: aws.String(uniquename.New("absent-pg"))})
	var pgMissing *ectypes.CacheParameterGroupNotFoundFault
	require.True(t, errors.As(err, &pgMissing), "%v", err)

	pg := uniquename.New("dup-pg")
	createPG := func() error {
		_, err := ec.CreateCacheParameterGroup(ctx, &elasticache.CreateCacheParameterGroupInput{
			CacheParameterGroupName: aws.String(pg), CacheParameterGroupFamily: aws.String("redis7"), Description: aws.String("dup"),
		})
		return err
	}
	require.NoError(t, createPG())
	t.Cleanup(func() {
		_, _ = ec.DeleteCacheParameterGroup(ctx, &elasticache.DeleteCacheParameterGroupInput{CacheParameterGroupName: aws.String(pg)})
	})
	var pgExists *ectypes.CacheParameterGroupAlreadyExistsFault
	require.True(t, errors.As(createPG(), &pgExists))

	sg := uniquename.New("dup-sg")
	createSG := func() error {
		_, err := ec.CreateCacheSubnetGroup(ctx, &elasticache.CreateCacheSubnetGroupInput{
			CacheSubnetGroupName: aws.String(sg), CacheSubnetGroupDescription: aws.String("dup"), SubnetIds: []string{"subnet-aaaa1111"},
		})
		return err
	}
	require.NoError(t, createSG())
	t.Cleanup(func() {
		_, _ = ec.DeleteCacheSubnetGroup(ctx, &elasticache.DeleteCacheSubnetGroupInput{CacheSubnetGroupName: aws.String(sg)})
	})
	var sgExists *ectypes.CacheSubnetGroupAlreadyExistsFault
	require.True(t, errors.As(createSG(), &sgExists))

	db := rdsClient()
	_, err = db.DeleteEventSubscription(ctx, &rds.DeleteEventSubscriptionInput{SubscriptionName: aws.String(uniquename.New("absent-sub"))})
	var subMissing *rdstypes.SubscriptionNotFoundFault
	require.True(t, errors.As(err, &subMissing), "%v", err)
	_, err = db.DeleteDBShardGroup(ctx, &rds.DeleteDBShardGroupInput{DBShardGroupIdentifier: aws.String(uniquename.New("absent-shard"))})
	var shardMissing *rdstypes.DBShardGroupNotFoundFault
	require.True(t, errors.As(err, &shardMissing), "%v", err)
	_, err = db.CancelExportTask(ctx, &rds.CancelExportTaskInput{ExportTaskIdentifier: aws.String(uniquename.New("absent-export"))})
	var exportMissing *rdstypes.ExportTaskNotFoundFault
	require.True(t, errors.As(err, &exportMissing), "%v", err)
}
