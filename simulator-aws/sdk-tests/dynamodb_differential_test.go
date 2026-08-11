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
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	smithy "github.com/aws/smithy-go"
)

// Lever 5b of the #652 "silent incompleteness" prevention work: differential
// testing of the sockerless DynamoDB slice against a reference oracle (Amazon's
// own DynamoDB Local). Each scenario runs the SAME sequence of operations
// against the sim and against DynamoDB Local and asserts the observable outcome
// is identical. This catches the "silent-wrong" class automatically: a dropped
// field, a mis-evaluated expression, or an accepted-but-wrong write diverges
// from the oracle and fails the test.
//
// IMPORTANT (per project direction): DynamoDB Local is a reference, not a
// ceiling. The sockerless sim may legitimately become *more* faithful to real
// AWS than DynamoDB Local is — and where it does, we must NOT regress the sim to
// match the oracle. Such a divergence is recorded in diffKnownDivergences with a
// justification (ideally citing real AWS behavior), and the test asserts the
// divergence is exactly the documented one (so a regression on either side still
// fails). Every other divergence is a real bug and fails the test.

// diffResult is the observable outcome of one scenario against one client: a
// normalized value (JSON-comparable) plus the AWS error code, if the operation
// failed. Exactly one of value / errCode is meaningful per scenario, but both
// are compared so an "errored vs succeeded" divergence is caught.
type diffResult struct {
	value   any
	errCode string
}

// diffDivergence documents a case where the sim and DynamoDB Local legitimately
// differ because the sim is the more faithful of the two. The test asserts the
// observed results match these exactly.
type diffDivergence struct {
	sim    diffResult
	local  diffResult
	reason string
}

// diffKnownDivergences is empty today: every scenario below agrees with DynamoDB
// Local. When the sim is intentionally more faithful than DynamoDB Local on some
// behavior, add an entry here (keyed by scenario name) with the reason — do not
// change the sim to match the oracle.
var diffKnownDivergences = map[string]diffDivergence{}

func TestDynamoDB_DifferentialVsLocal(t *testing.T) {
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
				// Documented sim-superiority divergence: assert it's exactly the
				// known shape on both sides (a change on either side still fails).
				assertDiffEqual(t, "sim (documented divergence: "+div.reason+")", div.sim, simRes)
				assertDiffEqual(t, "DynamoDB Local (documented divergence: "+div.reason+")", div.local, localRes)
				return
			}
			// Default: the sim must agree with the oracle.
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
	// run performs the scenario's full setup→operation→read-back against one
	// client, using the given unique table name, and returns the observable.
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
			// The ElectroDB-style decrement (#648): the RHS is always parenthesized.
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
			// terraform's state-lock guard: the second put must fail.
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
			// Second put with the same condition must be rejected.
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
			// #641: TransactWriteItems with an Update action.
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
			return diffGet(c, table, "k") // expect the absent sentinel on both
		}},

		{"malformed-filter-fails-loud", func(c *dynamodb.Client, table string) (any, error) {
			// Both the sim and DynamoDB Local must reject a malformed expression
			// with ValidationException (lever 2, validated against the oracle).
			if err := diffMakeTable(c, table); err != nil {
				return nil, err
			}
			_, err := c.Scan(ctx, &dynamodb.ScanInput{
				TableName:        &table,
				FilterExpression: aws.String("PK ="),
			})
			return "scan", err
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

// ── scenario helpers ─────────────────────────────────────────────────────────

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

// ── normalization ────────────────────────────────────────────────────────────

func diffNormItems(items []map[string]ddbtypes.AttributeValue) any {
	out := make([]any, 0, len(items))
	for _, it := range items {
		out = append(out, diffNormItem(it))
	}
	// Sort by canonical JSON so result ordering doesn't cause false mismatches
	// (Scan/Query ordering is compared separately where it matters via the key).
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

// diffNormAV renders a DynamoDB attribute value into a canonical, comparable
// shape, normalizing numbers (so "5" and "5.0" compare equal) and sorting set
// members.
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

// canonNum reduces a DynamoDB numeric string to a canonical form so that
// numerically-equal values compare equal regardless of formatting. The same
// transform is applied to both sides, so the exact canonical text is irrelevant
// — only its determinism and equality semantics matter.
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

// ── DynamoDB Local lifecycle ─────────────────────────────────────────────────

// startDynamoDBLocal launches Amazon's DynamoDB Local in a throwaway Docker
// container and returns its endpoint plus a stop func. Docker is a required
// dependency for this oracle-backed test; a missing binary or pull/start failure
// fails loud instead of reporting a green test that exercised nothing.
func startDynamoDBLocal(t *testing.T) (endpoint string, stop func()) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for DynamoDB Local differential test: %v", err)
	}

	// Amazon publishes DynamoDB Local on its own public registry as well as on
	// Docker Hub. Pulling it from Amazon avoids Docker Hub's anonymous rate
	// limit, which times the pull out on a shared CI runner and fails this
	// oracle-backed test for a reason that has nothing to do with DynamoDB.
	const image = "public.ecr.aws/aws-dynamodb-local/aws-dynamodb-local:latest"
	if !diffDockerPull(image) {
		t.Fatalf("docker is present but %s could not be pulled after retries", image)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// -inMemory keeps it fast and stateless; each run starts clean.
	containerName := fmt.Sprintf("sockerless-dynamodb-local-%d", port)
	runCtx, cancelRun := context.WithTimeout(context.Background(), 2*time.Minute)
	runOut, err := exec.CommandContext(runCtx, "docker", "run", "-d", "--rm", "--name", containerName,
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
	// DynamoDB Local is a JVM; a cold start on a loaded CI runner can take well
	// over 30s, so wait up to 120s (was 30s — the tight readiness window was a
	// flake source when the runner was busy).
	ok := false
	for i := 0; i < 240; i++ {
		if _, err := probe.ListTables(ctx, &dynamodb.ListTablesInput{}); err == nil {
			ok = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !ok {
		stop()
		t.Fatalf("DynamoDB Local did not become ready at %s", endpoint)
	}
	return endpoint, stop
}

func diffDockerPull(image string) bool {
	// A locally-present image (CI pre-pull or a previous run) needs no network.
	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 30*time.Second)
	inspectErr := exec.CommandContext(inspectCtx, "docker", "image", "inspect", image).Run()
	cancelInspect()
	if inspectErr == nil {
		return true
	}
	for attempt := 1; attempt <= 5; attempt++ {
		pullCtx, cancelPull := context.WithTimeout(context.Background(), 2*time.Minute)
		pullErr := exec.CommandContext(pullCtx, "docker", "pull", image).Run()
		cancelPull()
		if pullErr == nil {
			return true
		}
		time.Sleep(time.Duration(attempt*attempt) * time.Second)
	}
	return false
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
