package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResourceTags_CreateTimeRoundTrip verifies tags set at resource creation
// are returned by the per-service tag-list APIs for CloudWatch Logs, DynamoDB,
// and ECR. Dropping create-time tags makes every terraform plan re-add them.
func TestResourceTags_CreateTimeRoundTrip(t *testing.T) {
	t.Run("CloudWatchLogs", func(t *testing.T) {
		c := cwLogsClient()
		name := "/tags/cwlogs-app"
		_, err := c.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
			LogGroupName: aws.String(name),
			Tags:         map[string]string{"Name": "test", "env": "ci"},
		})
		require.NoError(t, err)

		dg, err := c.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
			LogGroupNamePrefix: aws.String(name),
		})
		require.NoError(t, err)
		require.NotEmpty(t, dg.LogGroups)
		arn := aws.ToString(dg.LogGroups[0].Arn)

		lt, err := c.ListTagsForResource(ctx, &cloudwatchlogs.ListTagsForResourceInput{
			ResourceArn: aws.String(arn),
		})
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"Name": "test", "env": "ci"}, lt.Tags)
	})

	t.Run("DynamoDB", func(t *testing.T) {
		c := ddbClient()
		_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
			TableName:            aws.String("tags-tbl"),
			BillingMode:          ddbtypes.BillingModePayPerRequest,
			AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
			KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
			Tags:                 []ddbtypes.Tag{{Key: aws.String("Name"), Value: aws.String("test")}},
		})
		require.NoError(t, err)

		desc, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String("tags-tbl")})
		require.NoError(t, err)
		arn := aws.ToString(desc.Table.TableArn)

		lt, err := c.ListTagsOfResource(ctx, &dynamodb.ListTagsOfResourceInput{ResourceArn: aws.String(arn)})
		require.NoError(t, err)
		require.Len(t, lt.Tags, 1)
		assert.Equal(t, "Name", aws.ToString(lt.Tags[0].Key))
		assert.Equal(t, "test", aws.ToString(lt.Tags[0].Value))
	})

	t.Run("ECR", func(t *testing.T) {
		c := ecrClient()
		_, err := c.CreateRepository(ctx, &ecr.CreateRepositoryInput{
			RepositoryName: aws.String("tags-repo"),
			Tags:           []ecrtypes.Tag{{Key: aws.String("Name"), Value: aws.String("test")}},
		})
		require.NoError(t, err)

		desc, err := c.DescribeRepositories(ctx, &ecr.DescribeRepositoriesInput{
			RepositoryNames: []string{"tags-repo"},
		})
		require.NoError(t, err)
		arn := aws.ToString(desc.Repositories[0].RepositoryArn)

		lt, err := c.ListTagsForResource(ctx, &ecr.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
		require.NoError(t, err)
		require.Len(t, lt.Tags, 1)
		assert.Equal(t, "Name", aws.ToString(lt.Tags[0].Key))
		assert.Equal(t, "test", aws.ToString(lt.Tags[0].Value))
	})
}
