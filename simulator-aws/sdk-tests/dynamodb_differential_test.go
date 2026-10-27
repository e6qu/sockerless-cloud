package aws_sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/baseimage"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	smithy "github.com/aws/smithy-go"
)

// These tests run each scenario's operations against the simulator and
// against Amazon's DynamoDB Local and expect the same observable outcome, so a
// dropped field, a mis-evaluated expression or a wrong write diverges from the
// oracle.
//
// DynamoDB Local is a reference, not a ceiling: where the simulator is more
// faithful to DynamoDB, record the divergence in diffKnownDivergences with the
// reason rather than regress the simulator. The test then asserts exactly the
// recorded divergence, and any other fails.

// diffResult is one scenario's outcome against one client: a JSON-comparable
// value and the AWS error code if the operation failed. Both are compared, so
// an errored-versus-succeeded divergence shows.
type diffResult struct {
	value   any
	errCode string
}

// diffDivergence records a case where the simulator is the more faithful of the
// two; the observed results must match it exactly.
type diffDivergence struct {
	sim    diffResult
	local  diffResult
	reason string
}

// diffKnownDivergences holds, by scenario name, each case where the simulator is
// intentionally more faithful than DynamoDB Local, with the reason.
var diffKnownDivergences = map[string]diffDivergence{}

func TestDynamoDB_DifferentialVsLocal(t *testing.T) {
	t.Parallel()
	endpoint, stop := startDynamoDBLocal(t)
	defer stop()

	simC := ddbClient()
	localC := dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	scenarios := dynamoDifferentialScenarios()
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			simRes := captureDiff(func() (any, error) { return sc.run(simC, sc.name+"-sim") })
			localRes := captureDiff(func() (any, error) { return sc.run(localC, sc.name+"-local") })

			if div, ok := diffKnownDivergences[sc.name]; ok {
				// A recorded divergence must hold exactly on both sides.
				assertDiffEqual(t, "sim (documented divergence: "+div.reason+")", div.sim, simRes)
				assertDiffEqual(t, "DynamoDB Local (documented divergence: "+div.reason+")", div.local, localRes)
				return
			}
			if simRes.errCode != localRes.errCode || canonJSON(simRes.value) != canonJSON(localRes.value) {
				t.Errorf("differential mismatch for %q:\n  sim:   err=%q value=%s\n  local: err=%q value=%s\n"+
					"If the sim is the MORE faithful one here, add a diffKnownDivergences entry with a justification "+
					"(do not regress the sim to match DynamoDB Local).",
					sc.name, simRes.errCode, canonJSON(simRes.value), localRes.errCode, canonJSON(localRes.value))
			}
		})
	}
}

type diffScenario struct {
	name string
	// run performs the scenario's setup, operation and read-back against one
	// client under the given table name, and returns the outcome.
	run func(c *dynamodb.Client, table string) (any, error)
}

func captureDiff(fn func() (any, error)) diffResult {
	v, err := fn()
	if err != nil {
		return diffResult{errCode: awsErrCode(err)}
	}
	return diffResult{value: v}
}

func assertDiffEqual(t *testing.T, side string, want, got diffResult) {
	t.Helper()
	if want.errCode != got.errCode || canonJSON(want.value) != canonJSON(got.value) {
		t.Errorf("%s: want err=%q value=%s, got err=%q value=%s",
			side, want.errCode, canonJSON(want.value), got.errCode, canonJSON(got.value))
	}
}

func dynamoDifferentialScenarios() []diffScenario {
	return []diffScenario{
		{"put-get-roundtrip", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			item := map[string]ddbtypes.AttributeValue{
				"PK":   &ddbtypes.AttributeValueMemberS{Value: "k"},
				"name": &ddbtypes.AttributeValueMemberS{Value: "alice"},
				"age":  &ddbtypes.AttributeValueMemberN{Value: "30"},
			}
			if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: item}); err != nil {
				return nil, err
			}
			return diffGet(c, table, "k")
		}},

		{"update-set-arithmetic", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: diffKey("k", map[string]ddbtypes.AttributeValue{
				"n": &ddbtypes.AttributeValueMemberN{Value: "10"},
			})}); err != nil {
				return nil, err
			}
			if _, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName:                 &table,
				Key:                       diffKey("k", nil),
				UpdateExpression:          aws.String("SET n = n + :v"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberN{Value: "5"}},
			}); err != nil {
				return nil, err
			}
			return diffGet(c, table, "k")
		}},

		{"update-if-not-exists-parenthesized-subtract", func(c *dynamodb.Client, table string) (any, error) {
			// The ElectroDB decrement always parenthesizes the RHS.
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: diffKey("k", map[string]ddbtypes.AttributeValue{
				"c": &ddbtypes.AttributeValueMemberN{Value: "100"},
			})}); err != nil {
				return nil, err
			}
			if _, err := c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName:        &table,
				Key:              diffKey("k", nil),
				UpdateExpression: aws.String("SET c = (if_not_exists(c, :z) - :v)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":z": &ddbtypes.AttributeValueMemberN{Value: "0"},
					":v": &ddbtypes.AttributeValueMemberN{Value: "30"},
				},
			}); err != nil {
				return nil, err
			}
			return diffGet(c, table, "k")
		}},

		{"condition-put-if-absent", func(c *dynamodb.Client, table string) (any, error) {
			// Terraform's state-lock guard: the second put fails.
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			put := &dynamodb.PutItemInput{
				TableName:           &table,
				Item:                diffKey("lock", nil),
				ConditionExpression: aws.String("attribute_not_exists(PK)"),
			}
			if _, err := c.PutItem(ctx, put); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, put)
			return "second-put", err
		}},

		{"scan-filter-expression", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			for i, age := range []string{"20", "40", "60"} {
				if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: diffKey(fmt.Sprintf("k%d", i), map[string]ddbtypes.AttributeValue{
					"age": &ddbtypes.AttributeValueMemberN{Value: age},
				})}); err != nil {
					return nil, err
				}
			}
			out, err := c.Scan(ctx, &dynamodb.ScanInput{
				TableName:                 &table,
				FilterExpression:          aws.String("age > :min"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":min": &ddbtypes.AttributeValueMemberN{Value: "30"}},
			})
			if err != nil {
				return nil, err
			}
			return diffNormItems(out.Items), nil
		}},

		{"query-begins-with", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSort(c, table); err != nil {
				return nil, err
			}
			for _, sk := range []string{"order#1", "order#2", "cust#1"} {
				if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{
					"PK": &ddbtypes.AttributeValueMemberS{Value: "p"},
					"SK": &ddbtypes.AttributeValueMemberS{Value: sk},
				}}); err != nil {
					return nil, err
				}
			}
			out, err := c.Query(ctx, &dynamodb.QueryInput{
				TableName:              &table,
				KeyConditionExpression: aws.String("PK = :p AND begins_with(SK, :pre)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":p":   &ddbtypes.AttributeValueMemberS{Value: "p"},
					":pre": &ddbtypes.AttributeValueMemberS{Value: "order#"},
				},
			})
			if err != nil {
				return nil, err
			}
			return diffNormItems(out.Items), nil
		}},

		{"transact-write-update", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: diffKey("k", map[string]ddbtypes.AttributeValue{
				"n": &ddbtypes.AttributeValueMemberN{Value: "1"},
			})}); err != nil {
				return nil, err
			}
			if _, err := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
				TransactItems: []ddbtypes.TransactWriteItem{{
					Update: &ddbtypes.Update{
						TableName:                 &table,
						Key:                       diffKey("k", nil),
						UpdateExpression:          aws.String("SET n = n + :v"),
						ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberN{Value: "41"}},
					},
				}},
			}); err != nil {
				return nil, err
			}
			return diffGet(c, table, "k")
		}},

		{"delete-then-get-absent", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: diffKey("k", nil)}); err != nil {
				return nil, err
			}
			if _, err := c.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &table, Key: diffKey("k", nil)}); err != nil {
				return nil, err
			}
			return diffGet(c, table, "k")
		}},

		{"malformed-filter-fails-loud", func(c *dynamodb.Client, table string) (any, error) {
			// Both reject a malformed expression with ValidationException.
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			_, err := c.Scan(ctx, &dynamodb.ScanInput{
				TableName:        &table,
				FilterExpression: aws.String("PK ="),
			})
			return "scan", err
		}},

		{"query-orders-numeric-sort-keys-by-value", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			for _, n := range []string{"10", "9", "-2", "-2.5", "0", "100", "0.5", "1e3"} {
				if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{
					"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}, "SK": &ddbtypes.AttributeValueMemberN{Value: n},
				}}); err != nil {
					return nil, err
				}
			}
			return diffQuerySortKeys(c, table, nil, true)
		}},

		{"query-orders-binary-sort-keys-by-bytes", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeB); err != nil {
				return nil, err
			}
			for _, b := range [][]byte{{0xff}, {0x00, 0x01}, {0x7f}, {0x80}, {0x00}} {
				if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{
					"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}, "SK": &ddbtypes.AttributeValueMemberB{Value: b},
				}}); err != nil {
					return nil, err
				}
			}
			return diffQuerySortKeys(c, table, nil, false)
		}},

		{"query-resumes-after-a-deleted-start-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSort(c, table); err != nil {
				return nil, err
			}
			for _, sk := range []string{"a", "b", "c", "d"} {
				if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{
					"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}, "SK": &ddbtypes.AttributeValueMemberS{Value: sk},
				}}); err != nil {
					return nil, err
				}
			}
			first, err := c.Query(ctx, &dynamodb.QueryInput{
				TableName: &table, KeyConditionExpression: aws.String("PK = :p"), Limit: aws.Int32(2),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": &ddbtypes.AttributeValueMemberS{Value: "p"}},
			})
			if err != nil {
				return nil, err
			}
			if _, err := c.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &table, Key: first.LastEvaluatedKey}); err != nil {
				return nil, err
			}
			return diffQuerySortKeys(c, table, first.LastEvaluatedKey, true)
		}},

		{"put-without-the-sort-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}}})
			return diffRefusal(err)
		}},

		{"put-with-a-key-of-the-wrong-type", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberN{Value: "1"}, "SK": &ddbtypes.AttributeValueMemberN{Value: "1"}}})
			return diffRefusal(err)
		}},

		{"put-with-a-number-that-is-not-one", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}, "SK": &ddbtypes.AttributeValueMemberN{Value: "1"}, "count": &ddbtypes.AttributeValueMemberN{Value: "many"}}})
			return diffRefusal(err)
		}},

		{"put-with-an-empty-string-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: ""}, "SK": &ddbtypes.AttributeValueMemberN{Value: "1"}}})
			return diffRefusal(err)
		}},

		{"get-with-an-extra-key-attribute", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: &table, Key: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}, "SK": &ddbtypes.AttributeValueMemberN{Value: "1"}, "extra": &ddbtypes.AttributeValueMemberS{Value: "x"}}})
			return diffRefusal(err)
		}},

		{"get-without-the-sort-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.GetItem(ctx, &dynamodb.GetItemInput{TableName: &table, Key: map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: "p"}}})
			return diffRefusal(err)
		}},

		{"gsi-query-reads-index-order-and-projection", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithIndex(c, table); err != nil {
				return nil, err
			}
			if err := diffPutIndexed(c, table); err != nil {
				return nil, err
			}
			out, err := c.Query(ctx, &dynamodb.QueryInput{
				TableName: &table, IndexName: aws.String("byGroup"),
				KeyConditionExpression:    aws.String("grp = :g"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":g": &ddbtypes.AttributeValueMemberS{Value: "g1"}},
			})
			if err != nil {
				return nil, err
			}
			return diffNormItems(out.Items), nil
		}},

		{"gsi-query-pages-by-the-index-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithIndex(c, table); err != nil {
				return nil, err
			}
			if err := diffPutIndexed(c, table); err != nil {
				return nil, err
			}
			var pages []any
			var start map[string]ddbtypes.AttributeValue
			for {
				out, err := c.Query(ctx, &dynamodb.QueryInput{
					TableName: &table, IndexName: aws.String("byGroup"), Limit: aws.Int32(2), ExclusiveStartKey: start,
					KeyConditionExpression:    aws.String("grp = :g"),
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":g": &ddbtypes.AttributeValueMemberS{Value: "g1"}},
				})
				if err != nil {
					return nil, err
				}
				pages = append(pages, map[string]any{"items": diffNormItems(out.Items), "last": diffNormItem(out.LastEvaluatedKey)})
				if out.LastEvaluatedKey == nil {
					return pages, nil
				}
				start = out.LastEvaluatedKey
			}
		}},

		{"sparse-index-scan-sees-only-indexed-items", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithIndex(c, table); err != nil {
				return nil, err
			}
			if err := diffPutIndexed(c, table); err != nil {
				return nil, err
			}
			out, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: &table, IndexName: aws.String("byGroup")})
			if err != nil {
				return nil, err
			}
			ids := []string{}
			for _, item := range out.Items {
				ids = append(ids, item["PK"].(*ddbtypes.AttributeValueMemberS).Value)
			}
			sort.Strings(ids)
			return ids, nil
		}},

		{"put-with-an-index-key-of-the-wrong-type", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithIndex(c, table); err != nil {
				return nil, err
			}
			_, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: map[string]ddbtypes.AttributeValue{
				"PK": &ddbtypes.AttributeValueMemberS{Value: "x"}, "grp": &ddbtypes.AttributeValueMemberN{Value: "1"},
			}})
			return diffRefusal(err)
		}},

		{"transaction-with-a-failing-update-writes-nothing", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			_, txErr := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []ddbtypes.TransactWriteItem{
				{Put: &ddbtypes.Put{TableName: &table, Item: diffKey("first", nil)}},
				{Update: &ddbtypes.Update{
					TableName: &table, Key: diffKey("second", nil),
					UpdateExpression:          aws.String("SET n = n + :v"),
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":v": &ddbtypes.AttributeValueMemberN{Value: "1"}},
				}},
			}})
			got, err := diffGet(c, table, "first")
			if err != nil {
				return nil, err
			}
			return map[string]any{"refused": txErr != nil, "first": got}, nil
		}},

		{"partiql-insert-without-the-sort-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
				Statement: aws.String(fmt.Sprintf("INSERT INTO \"%s\" VALUE {'PK': 'p'}", table)),
			})
			return diffRefusal(err)
		}},

		{"partiql-insert-with-a-key-of-the-wrong-type", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithSortType(c, table, ddbtypes.ScalarAttributeTypeN); err != nil {
				return nil, err
			}
			_, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
				Statement: aws.String(fmt.Sprintf("INSERT INTO \"%s\" VALUE {'PK': 'p', 'SK': 'one'}", table)),
			})
			return diffRefusal(err)
		}},

		{"partiql-insert-with-an-index-key-of-the-wrong-type", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTableWithIndex(c, table); err != nil {
				return nil, err
			}
			_, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
				Statement: aws.String(fmt.Sprintf("INSERT INTO \"%s\" VALUE {'PK': 'x', 'grp': 1}", table)),
			})
			return diffRefusal(err)
		}},

		{"partiql-insert-with-an-empty-string-key", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			_, err := c.ExecuteStatement(ctx, &dynamodb.ExecuteStatementInput{
				Statement: aws.String(fmt.Sprintf("INSERT INTO \"%s\" VALUE {'PK': ''}", table)),
			})
			return diffRefusal(err)
		}},

		{"undefined-value-ref-fails-loud", func(c *dynamodb.Client, table string) (any, error) {
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			_, err := c.Scan(ctx, &dynamodb.ScanInput{
				TableName:        &table,
				FilterExpression: aws.String("PK = :missing"),
			})
			return "scan", err
		}},
	}
}

// diffMakeTableWithIndex is a table keyed by PK with a KEYS_ONLY global index
// byGroup on grp (hash) and rank (numeric range).
func diffMakeTableWithIndex(c *dynamodb.Client, table string) error {
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: &table,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("grp"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("rank"), AttributeType: ddbtypes.ScalarAttributeTypeN},
		},
		KeySchema: []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		GlobalSecondaryIndexes: []ddbtypes.GlobalSecondaryIndex{{
			IndexName: aws.String("byGroup"),
			KeySchema: []ddbtypes.KeySchemaElement{
				{AttributeName: aws.String("grp"), KeyType: ddbtypes.KeyTypeHash},
				{AttributeName: aws.String("rank"), KeyType: ddbtypes.KeyTypeRange},
			},
			Projection: &ddbtypes.Projection{ProjectionType: ddbtypes.ProjectionTypeKeysOnly},
		}},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	return err
}

// diffPutIndexed writes items in and out of byGroup: ranks out of order, one
// item with no grp, and one attribute the KEYS_ONLY index does not project.
func diffPutIndexed(c *dynamodb.Client, table string) error {
	for _, row := range []struct{ pk, grp, rank string }{
		{"a", "g1", "10"}, {"b", "g1", "9"}, {"c", "g1", "-1"}, {"d", "g2", "5"}, {"e", "", ""}, {"f", "g1", "100"},
	} {
		item := map[string]ddbtypes.AttributeValue{
			"PK":    &ddbtypes.AttributeValueMemberS{Value: row.pk},
			"extra": &ddbtypes.AttributeValueMemberS{Value: "not projected"},
		}
		if row.grp != "" {
			item["grp"] = &ddbtypes.AttributeValueMemberS{Value: row.grp}
			item["rank"] = &ddbtypes.AttributeValueMemberN{Value: row.rank}
		}
		if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: &table, Item: item}); err != nil {
			return err
		}
	}
	return nil
}

// diffRefusal turns a refusal into the scenario's value (code and message), so
// the wording is compared with DynamoDB Local's; captureDiff keeps only an
// error's code.
func diffRefusal(err error) (any, error) {
	if err == nil {
		return "accepted", nil
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return nil, err
	}
	return apiErr.ErrorCode() + ": " + apiErr.ErrorMessage(), nil
}

func diffMakeTableWithSortType(c *dynamodb.Client, table string, sortType ddbtypes.ScalarAttributeType) error {
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: &table,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: sortType},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	return err
}

// diffQuerySortKeys queries partition "p" from start and returns its sort keys
// in the order they came back, with the value reported as text.
func diffQuerySortKeys(c *dynamodb.Client, table string, start map[string]ddbtypes.AttributeValue, _ bool) (any, error) {
	out, err := c.Query(ctx, &dynamodb.QueryInput{
		TableName: &table, KeyConditionExpression: aws.String("PK = :p"), ExclusiveStartKey: start,
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":p": &ddbtypes.AttributeValueMemberS{Value: "p"}},
	})
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, item := range out.Items {
		switch v := item["SK"].(type) {
		case *ddbtypes.AttributeValueMemberN:
			keys = append(keys, canonNum(v.Value))
		case *ddbtypes.AttributeValueMemberS:
			keys = append(keys, v.Value)
		case *ddbtypes.AttributeValueMemberB:
			keys = append(keys, fmt.Sprintf("%x", v.Value))
		}
	}
	return keys, nil
}

func diffKey(pk string, extra map[string]ddbtypes.AttributeValue) map[string]ddbtypes.AttributeValue {
	m := map[string]ddbtypes.AttributeValue{"PK": &ddbtypes.AttributeValueMemberS{Value: pk}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func diffMakeTable(c *dynamodb.Client, table string) error {
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &table,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS}},
		KeySchema:            []ddbtypes.KeySchemaElement{{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash}},
		BillingMode:          ddbtypes.BillingModePayPerRequest,
	})
	return err
}

func diffMakeTableWithSort(c *dynamodb.Client, table string) error {
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: &table,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: ddbtypes.KeyTypeRange},
		},
		BillingMode: ddbtypes.BillingModePayPerRequest,
	})
	return err
}

func diffGet(c *dynamodb.Client, table, pk string) (any, error) {
	out, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      &table,
		Key:            diffKey(pk, nil),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return "<absent>", nil
	}
	return diffNormItem(out.Item), nil
}

func diffNormItems(items []map[string]ddbtypes.AttributeValue) any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, diffNormItem(it))
	}
	// Sort by canonical JSON so result order cannot cause a mismatch; where order
	// matters the key compares it.
	sort.Slice(out, func(i, j int) bool { return canonJSON(out[i]) < canonJSON(out[j]) })
	return out
}

func diffNormItem(item map[string]ddbtypes.AttributeValue) any {
	m := make(map[string]any, len(item))
	for k, v := range item {
		m[k] = diffNormAV(v)
	}
	return m
}

// diffNormAV renders an attribute value comparably: numbers canonical ("5"
// equals "5.0") and set members sorted.
func diffNormAV(v ddbtypes.AttributeValue) any {
	switch t := v.(type) {
	case *ddbtypes.AttributeValueMemberS:
		return map[string]any{"S": t.Value}
	case *ddbtypes.AttributeValueMemberN:
		return map[string]any{"N": canonNum(t.Value)}
	case *ddbtypes.AttributeValueMemberBOOL:
		return map[string]any{"BOOL": t.Value}
	case *ddbtypes.AttributeValueMemberNULL:
		return map[string]any{"NULL": true}
	case *ddbtypes.AttributeValueMemberB:
		return map[string]any{"B": t.Value}
	case *ddbtypes.AttributeValueMemberM:
		mm := make(map[string]any, len(t.Value))
		for k, vv := range t.Value {
			mm[k] = diffNormAV(vv)
		}
		return map[string]any{"M": mm}
	case *ddbtypes.AttributeValueMemberL:
		l := make([]any, 0, len(t.Value))
		for _, vv := range t.Value {
			l = append(l, diffNormAV(vv))
		}
		return map[string]any{"L": l}
	case *ddbtypes.AttributeValueMemberSS:
		ss := append([]string(nil), t.Value...)
		sort.Strings(ss)
		return map[string]any{"SS": ss}
	case *ddbtypes.AttributeValueMemberNS:
		ns := make([]string, len(t.Value))
		for i, n := range t.Value {
			ns[i] = canonNum(n)
		}
		sort.Strings(ns)
		return map[string]any{"NS": ns}
	default:
		return fmt.Sprintf("%T", v)
	}
}

// canonNum reduces a numeric string to a canonical form so equal numbers
// compare equal whatever their formatting; both sides take the same transform.
func canonNum(s string) string {
	if r, ok := new(big.Rat).SetString(s); ok {
		return r.RatString()
	}
	return s
}

func canonJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func awsErrCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	if err != nil {
		return "error:" + err.Error()
	}
	return ""
}

// dynamoDBLocalLabel marks the DynamoDB Local containers this suite starts, so
// the next run finds and removes one a killed run left behind.
const dynamoDBLocalLabel = "sockerless-aws-sdk-test=dynamodb-local"

// reapStaleDynamoDBLocal removes DynamoDB Local containers an earlier run left
// behind, which hold the published port and the container name.
func reapStaleDynamoDBLocal(t *testing.T) {
	t.Helper()
	listCtx, cancelList := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelList()
	listed, err := exec.CommandContext(listCtx, "docker", "ps", "--all", "--quiet",
		"--filter", "label="+dynamoDBLocalLabel).CombinedOutput()
	if err != nil {
		t.Fatalf("list stale DynamoDB Local containers: %v\n%s", err, listed)
	}
	for _, id := range strings.Fields(string(listed)) {
		removeCtx, cancelRemove := context.WithTimeout(context.Background(), 30*time.Second)
		out, removeErr := exec.CommandContext(removeCtx, "docker", "rm", "-f", id).CombinedOutput()
		cancelRemove()
		if removeErr != nil {
			t.Fatalf("remove stale DynamoDB Local container %s: %v\n%s", id, removeErr, out)
		}
	}
}

// startDynamoDBLocal runs DynamoDB Local in a throwaway Docker container and
// returns its endpoint and a stop func. Docker is required: a missing binary
// or a failed pull or start fails the test.
func startDynamoDBLocal(t *testing.T) (endpoint string, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for DynamoDB Local differential test: %v", err)
	}

	// mirror.gcr.io serves Docker Hub's copy of DynamoDB Local without the
	// anonymous rate limits that throttle a shared CI runner. The digest pins the
	// oracle, and both registries serve it.
	const pinned = "mirror.gcr.io/amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab"
	image, err := baseimage.EnsureRef(pinned)
	if err != nil {
		t.Fatalf("docker is present but %s could not be pulled after retries: %v", pinned, err)
	}
	// A digest copied from one machine can name its platform manifest rather than
	// the index, which another architecture cannot execute.
	archCtx, cancelArch := context.WithTimeout(context.Background(), 30*time.Second)
	engineArch, engineErr := exec.CommandContext(archCtx, "docker", "version", "--format", "{{.Server.Arch}}").CombinedOutput()
	imageArch, imageErr := exec.CommandContext(archCtx, "docker", "image", "inspect", "--format", "{{.Architecture}}", image).CombinedOutput()
	cancelArch()
	if engineErr != nil || imageErr != nil {
		t.Fatalf("read the engine and image architectures: %v %v\n%s\n%s", engineErr, imageErr, engineArch, imageArch)
	}
	if string(trimNL(engineArch)) != string(trimNL(imageArch)) {
		t.Fatalf("%s is built for %s and the engine runs %s: the pin names one platform's manifest, not the multi-platform index",
			image, trimNL(imageArch), trimNL(engineArch))
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// -inMemory keeps each run clean.
	containerName := fmt.Sprintf("sockerless-dynamodb-local-%d", port)
	// A killed test binary leaves DynamoDB Local running with its port held, so
	// each run first reaps labelled survivors; only this helper sets the label,
	// one container per run.
	reapStaleDynamoDBLocal(t)
	runCtx, cancelRun := context.WithTimeout(context.Background(), 2*time.Minute)
	// No --rm: an exited DynamoDB Local stays inspectable with its exit code and
	// log. stop removes it.
	runOut, err := exec.CommandContext(runCtx, "docker", "run", "-d", "--name", containerName,
		"--label", dynamoDBLocalLabel,
		"-p", fmt.Sprintf("127.0.0.1:%d:8000", port),
		image, "-jar", "DynamoDBLocal.jar", "-inMemory").CombinedOutput()
	cancelRun()
	if err != nil {
		inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 10*time.Second)
		stateOut, stateErr := exec.CommandContext(
			inspectCtx,
			"docker", "inspect", "--format", "{{json .State}}", containerName,
		).CombinedOutput()
		cancelInspect()
		if stateErr != nil {
			stateOut = []byte(fmt.Sprintf("unavailable: %v\n%s", stateErr, stateOut))
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerName).Run()
		cancelCleanup()
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("start DynamoDB Local exceeded 2 minutes\ncontainer state: %s\n%s", stateOut, runOut)
		}
		t.Fatalf("start DynamoDB Local: %v\ncontainer state: %s\n%s", err, stateOut, runOut)
	}
	if len(trimNL(runOut)) == 0 {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerName).Run()
		cancelCleanup()
		t.Fatalf("start DynamoDB Local returned no container ID")
	}
	stop = func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", containerName).Run()
	}

	endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
	probe := dynamodb.NewFromConfig(sdkConfig(), func(o *dynamodb.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})
	// DynamoDB Local is a JVM whose cold start on a loaded runner can pass 30s.
	// The deadline is wall-clock and each probe has its own timeout, so SDK
	// retries cannot stretch the wait.
	deadline := time.Now().Add(120 * time.Second)
	var probeErr error
	for {
		probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
		_, probeErr = probe.ListTables(probeCtx, &dynamodb.ListTablesInput{}, func(o *dynamodb.Options) {
			o.RetryMaxAttempts = 1
		})
		cancelProbe()
		if probeErr == nil || time.Now().After(deadline) {
			break
		}
		runningCtx, cancelRunning := context.WithTimeout(context.Background(), 10*time.Second)
		running, runningErr := exec.CommandContext(runningCtx, "docker", "inspect", "--format", "{{.State.Running}}", containerName).CombinedOutput()
		cancelRunning()
		if runningErr != nil || string(trimNL(running)) != "true" {
			probeErr = fmt.Errorf("the container stopped before answering (last probe: %w)", probeErr)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if probeErr != nil {
		inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 30*time.Second)
		stateOut, _ := exec.CommandContext(inspectCtx, "docker", "inspect", "--format", "{{json .State}} {{json .HostConfig.Memory}}", containerName).CombinedOutput()
		logsOut, _ := exec.CommandContext(inspectCtx, "docker", "logs", "--tail", "60", containerName).CombinedOutput()
		imageOut, _ := exec.CommandContext(inspectCtx, "docker", "image", "inspect", "--format",
			"{{.Id}} {{.Os}}/{{.Architecture}} entrypoint={{json .Config.Entrypoint}} workdir={{json .Config.WorkingDir}} digests={{json .RepoDigests}}",
			image).CombinedOutput()
		cancelInspect()
		stop()
		t.Fatalf("DynamoDB Local did not become ready at %s within 120s: %v\ncontainer state: %s\nimage: %s\ncontainer log:\n%s",
			endpoint, probeErr, stateOut, imageOut, logsOut)
	}
	return endpoint, stop
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
