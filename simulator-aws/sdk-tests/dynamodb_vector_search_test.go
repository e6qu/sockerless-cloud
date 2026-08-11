package aws_sdk_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/require"
)

// Amazon DynamoDB vector search through the official SDK: a table declares a
// vector index, items carry vectors, and SearchVectors answers with the nearest
// neighbours in order, each with the score the comparison produced.
//
// The documents are placed so the right answer is decidable by hand and differs
// from the order they were written in — under EUCLIDEAN the query [0,0] is
// nearest to "near" (1 away), then "mid" (5), then "far" (13), and they are
// written furthest-first. Storage order and the correct answer disagree, so
// only a real search can produce this result.
func TestDynamoDB_VectorSearchReturnsNearestNeighbours_SDK(t *testing.T) {
	ctx := context.Background()
	c := ddbClient()
	table := "vector-search-sdk"

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		BillingMode:          types.BillingModePayPerRequest,
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		VectorIndexes: []types.VectorIndex{{
			IndexName:        aws.String("by-embedding"),
			VectorAttribute:  &types.VectorAttributeDefinition{AttributeName: aws.String("embedding")},
			Dimensions:       aws.Int64(2),
			DistanceFunction: types.VectorDistanceFunctionEuclidean,
			Projection:       &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	// DescribeTable reports what is searchable, so a client can discover the
	// index rather than having to remember creating it.
	described, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	require.NoError(t, err)
	require.Len(t, described.Table.VectorIndexes, 1)
	require.Equal(t, "by-embedding", aws.ToString(described.Table.VectorIndexes[0].IndexName))
	require.Equal(t, types.VectorDistanceFunctionEuclidean, described.Table.VectorIndexes[0].DistanceFunction)

	for _, doc := range []struct{ pk, x, y, kind string }{
		{"far", "5", "12", "archived"},
		{"mid", "3", "4", "live"},
		{"near", "1", "0", "live"},
	} {
		_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(table),
			Item: map[string]types.AttributeValue{
				"pk":   &types.AttributeValueMemberS{Value: doc.pk},
				"kind": &types.AttributeValueMemberS{Value: doc.kind},
				"embedding": &types.AttributeValueMemberL{Value: []types.AttributeValue{
					&types.AttributeValueMemberN{Value: doc.x},
					&types.AttributeValueMemberN{Value: doc.y},
				}},
			},
		})
		require.NoError(t, err)
	}

	out, err := c.SearchVectors(ctx, &dynamodb.SearchVectorsInput{
		TableName: aws.String(table),
		IndexName: aws.String("by-embedding"),
		SearchVector: []types.AttributeValue{
			&types.AttributeValueMemberN{Value: "0"},
			&types.AttributeValueMemberN{Value: "0"},
		},
		TopK: aws.Int32(2),
	})
	require.NoError(t, err)
	require.Len(t, out.SearchResults, 2, "TopK asked for two neighbours")

	first := out.SearchResults[0].Item["pk"].(*types.AttributeValueMemberS)
	second := out.SearchResults[1].Item["pk"].(*types.AttributeValueMemberS)
	require.Equal(t, "near", first.Value, "nearest first, not the order the items were written")
	require.Equal(t, "mid", second.Value)
	require.InDelta(t, 1.0, out.SearchResults[0].Score, 1e-9, "the score is the distance itself")
	require.InDelta(t, 5.0, out.SearchResults[1].Score, 1e-9)
}

// SearchConditionExpression narrows what is searched, so an item it excludes is
// not a neighbour however near it lies. The nearest document here is archived,
// and asking for live ones must answer with the next nearest instead.
func TestDynamoDB_VectorSearchHonoursItsCondition_SDK(t *testing.T) {
	ctx := context.Background()
	c := ddbClient()
	table := "vector-condition-sdk"

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		BillingMode:          types.BillingModePayPerRequest,
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		VectorIndexes: []types.VectorIndex{{
			IndexName:        aws.String("by-embedding"),
			VectorAttribute:  &types.VectorAttributeDefinition{AttributeName: aws.String("embedding")},
			Dimensions:       aws.Int64(2),
			DistanceFunction: types.VectorDistanceFunctionEuclidean,
			Projection:       &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	for _, doc := range []struct{ pk, x, kind string }{
		{"nearest-archived", "1", "archived"},
		{"next-live", "2", "live"},
	} {
		_, err = c.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(table),
			Item: map[string]types.AttributeValue{
				"pk":   &types.AttributeValueMemberS{Value: doc.pk},
				"kind": &types.AttributeValueMemberS{Value: doc.kind},
				"embedding": &types.AttributeValueMemberL{Value: []types.AttributeValue{
					&types.AttributeValueMemberN{Value: doc.x},
					&types.AttributeValueMemberN{Value: "0"},
				}},
			},
		})
		require.NoError(t, err)
	}

	out, err := c.SearchVectors(ctx, &dynamodb.SearchVectorsInput{
		TableName:                 aws.String(table),
		IndexName:                 aws.String("by-embedding"),
		SearchVector:              []types.AttributeValue{&types.AttributeValueMemberN{Value: "0"}, &types.AttributeValueMemberN{Value: "0"}},
		TopK:                      aws.Int32(5),
		SearchConditionExpression: aws.String("kind = :live"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":live": &types.AttributeValueMemberS{Value: "live"}},
	})
	require.NoError(t, err)
	require.Len(t, out.SearchResults, 1, "only the live document is searchable")
	pk := out.SearchResults[0].Item["pk"].(*types.AttributeValueMemberS)
	require.Equal(t, "next-live", pk.Value, "the nearer document is excluded by the condition")
}

// A vector index can be added to and dropped from a live table through
// UpdateTable, which is how one arrives on a table that did not declare it.
func TestDynamoDB_VectorIndexLifecycleThroughUpdateTable_SDK(t *testing.T) {
	ctx := context.Background()
	c := ddbClient()
	table := "vector-lifecycle-sdk"

	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		BillingMode:          types.BillingModePayPerRequest,
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)}) })

	_, err = c.UpdateTable(ctx, &dynamodb.UpdateTableInput{
		TableName: aws.String(table),
		VectorIndexUpdates: []types.VectorIndexUpdate{{Create: &types.CreateVectorIndexAction{
			IndexName:        aws.String("added-later"),
			VectorAttribute:  &types.VectorAttributeDefinition{AttributeName: aws.String("embedding")},
			Dimensions:       aws.Int64(4),
			DistanceFunction: types.VectorDistanceFunctionCosine,
			Projection:       &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}}},
	})
	require.NoError(t, err)

	described, err := c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	require.NoError(t, err)
	require.Len(t, described.Table.VectorIndexes, 1)
	require.Equal(t, types.VectorDistanceFunctionCosine, described.Table.VectorIndexes[0].DistanceFunction)

	_, err = c.UpdateTable(ctx, &dynamodb.UpdateTableInput{
		TableName:          aws.String(table),
		VectorIndexUpdates: []types.VectorIndexUpdate{{Delete: &types.DeleteVectorIndexAction{IndexName: aws.String("added-later")}}},
	})
	require.NoError(t, err)

	described, err = c.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)})
	require.NoError(t, err)
	require.Empty(t, described.Table.VectorIndexes, "the index is gone once it is dropped")
}
