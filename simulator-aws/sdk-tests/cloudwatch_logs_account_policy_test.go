package aws_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putAccountPolicy(t *testing.T, in *cloudwatchlogs.PutAccountPolicyInput) {
	t.Helper()
	cw := cwLogsClient()
	_, err := cw.PutAccountPolicy(ctx, in)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{PolicyName: in.PolicyName, PolicyType: in.PolicyType})
	})
}

// An account-level subscription filter policy forwards the events of every
// log group its selection criteria admit, in the account-level guide's
// envelope: the policy's name as the filter and policyLevel
// ACCOUNT_LEVEL_POLICY. The forwarding metrics carry the PolicyLevel
// dimension, and an account keeps one such policy per Region.
func TestLogs_AccountSubscriptionPolicyForwards(t *testing.T) {
	cw := cwLogsClient()
	prefix := uniquename.New("/test/account-subscription")
	included, excluded := prefix+"/included", prefix+"/excluded"
	createLogGroupWithStream(t, included, "s")
	createLogGroupWithStream(t, excluded, "s")
	stream := uniquename.New("cwl-account-policy")
	streamARN := createActiveStream(t, stream)
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)
	policy := uniquename.New("ExamplePolicy")
	document := `{"RoleArn":"` + role + `","DestinationArn":"` + streamARN + `","FilterPattern":"Test","Distribution":"Random"}`
	putAccountPolicy(t, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeSubscriptionFilterPolicy,
		PolicyDocument:    aws.String(document),
		SelectionCriteria: aws.String(`LogGroupName NOT IN ["` + excluded + `"]`),
		Scope:             cwlogtypes.ScopeAll,
	})
	described, err := cw.DescribeAccountPolicies(ctx, &cloudwatchlogs.DescribeAccountPoliciesInput{
		PolicyType: cwlogtypes.PolicyTypeSubscriptionFilterPolicy, PolicyName: aws.String(policy),
	})
	require.NoError(t, err)
	require.Len(t, described.AccountPolicies, 1)
	assert.Equal(t, document, aws.ToString(described.AccountPolicies[0].PolicyDocument))
	assert.Equal(t, cwlogtypes.ScopeAll, described.AccountPolicies[0].Scope)

	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy + "-second"), PolicyType: cwlogtypes.PolicyTypeSubscriptionFilterPolicy,
		PolicyDocument: aws.String(document),
	})
	var limit *cwlogtypes.LimitExceededException
	require.True(t, errors.As(err, &limit), "a second account-level subscription filter policy: %v", err)

	start := time.Now()
	putLogLines(t, excluded, "s", "Test from the excluded group")
	putLogLines(t, included, "s", "Test from the included group", "not forwarded")
	messages, _ := awaitStreamDataMessages(t, stream, 1)
	for _, m := range messages {
		if m.LogGroup == excluded {
			t.Fatalf("the selection criteria exclude %s", excluded)
		}
	}
	m := messages[0]
	assert.Equal(t, included, m.LogGroup)
	assert.Equal(t, []string{policy}, m.SubscriptionFilters)
	require.Len(t, m.LogEvents, 1)
	assert.Equal(t, "Test from the included group", m.LogEvents[0].Message)
	assert.Equal(t, "ACCOUNT_LEVEL_POLICY", m.PolicyLevel)

	filtered, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(included), FilterPattern: aws.String("Test")})
	require.NoError(t, err)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, aws.ToString(filtered.Events[0].EventId), m.LogEvents[0].ID, "the forwarded event keeps its stored ID")

	byPolicy := dims("PolicyLevel", "AccountPolicy", "DestinationType", "Kinesis", "FilterName", policy)
	require.Eventually(t, func() bool {
		return logsMetricSum(t, "AWS/Logs", "ForwardedLogEvents", byPolicy, start) == 1
	}, 30*time.Second, 200*time.Millisecond, "ForwardedLogEvents under the PolicyLevel dimension")
	assert.Positive(t, logsMetricSum(t, "AWS/Logs", "ForwardedBytes",
		dims("LogGroupName", included, "DestinationType", "Kinesis", "FilterName", policy), start))
}

// An account-level transformer policy transforms the events of the log groups
// whose names start with its prefix, unless a group has a transformer of its
// own; its metrics carry PolicyLevel rather than the log group. Two
// transformer policies cannot cover overlapping prefixes.
func TestLogs_AccountTransformerPolicyApplies(t *testing.T) {
	cw := cwLogsClient()
	prefix := uniquename.New("/test/account-transformer")
	covered, own := prefix+"/covered", prefix+"/own"
	createLogGroupWithStream(t, covered, "s")
	createLogGroupWithStream(t, own, "s")
	_, err := cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String(own),
		TransformerConfig:  []cwlogtypes.Processor{{ParseJSON: &cwlogtypes.ParseJSON{}}},
	})
	require.NoError(t, err)
	policy := uniquename.New("ExampleTransformer")
	putAccountPolicy(t, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeTransformerPolicy,
		PolicyDocument: aws.String(`[{"parseJSON":{}},{"addKeys":{"entries":[{"key":"metadata.transformed_in","value":"CloudWatchLogs"}]}},` +
			`{"trimString":{"withKeys":["status"]}},{"lowerCaseString":{"withKeys":["status"]}}]`),
		SelectionCriteria: aws.String(`LogGroupNamePrefix = "` + prefix + `"`),
	})

	var invalid *cwlogtypes.InvalidParameterException
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy + "-overlap"), PolicyType: cwlogtypes.PolicyTypeTransformerPolicy,
		PolicyDocument:    aws.String(`[{"parseJSON":{}}]`),
		SelectionCriteria: aws.String(`LogGroupNamePrefix = "` + covered + `"`),
	})
	require.True(t, errors.As(err, &invalid), "a prefix within another policy's: %v", err)
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy + "-bad"), PolicyType: cwlogtypes.PolicyTypeTransformerPolicy,
		PolicyDocument:    aws.String(`[{"lowerCaseString":{"withKeys":["status"]}}]`),
		SelectionCriteria: aws.String(`LogGroupNamePrefix = "/test/unrelated"`),
	})
	require.True(t, errors.As(err, &invalid), "a transformer must start with a parser: %v", err)

	start := time.Now()
	putLogLines(t, covered, "s", `{"status":"  OK  "}`)
	putLogLines(t, own, "s", `{"status":"  OK  "}`)
	query := func(group string) string {
		out, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
			LogGroupName: aws.String(group), QueryString: aws.String("fields @message"),
			StartTime: aws.Int64(start.Add(-time.Minute).Unix()), EndTime: aws.Int64(time.Now().Add(time.Minute).Unix()),
		})
		require.NoError(t, err)
		res, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: out.QueryId})
		require.NoError(t, err)
		require.Len(t, res.Results, 1)
		for _, f := range res.Results[0] {
			if aws.ToString(f.Field) == "@message" {
				return aws.ToString(f.Value)
			}
		}
		return ""
	}
	assert.JSONEq(t, `{"status":"ok","metadata":{"transformed_in":"CloudWatchLogs"}}`, query(covered))
	assert.JSONEq(t, `{"status":"  OK  "}`, query(own), "a log group's own transformer overrides the account-level one")
	assert.Equal(t, 1.0, logsMetricSum(t, "AWS/Logs", "TransformedLogEvents", dims("PolicyLevel", "AccountPolicy"), start))
	assert.Equal(t, 1.0, logsMetricSum(t, "AWS/Logs", "TransformedLogEvents", dims("LogGroupname", own), start))
}

// An account-level retention policy takes effect 24 hours after it is put, so
// DescribeLogGroups keeps the group's own retention until then, while a dry
// run reports the impact as if the policy were already in effect.
func TestLogs_AccountRetentionPolicy(t *testing.T) {
	cw := cwLogsClient()
	prefix := uniquename.New("/test/account-retention")
	never, longer := prefix+"/never", prefix+"/longer"
	createLogGroupWithStream(t, never, "s")
	createLogGroupWithStream(t, longer, "s")
	_, err := cw.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(longer), RetentionInDays: aws.Int32(3653)})
	require.NoError(t, err)
	document := `{"retentionInDays": 90, "enforcementMode": "RETROACTIVE"}`
	selection := `LogGroupNamePrefix="` + prefix + `"`
	policy := uniquename.New("baseline")

	var invalid *cwlogtypes.InvalidParameterException
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeRetentionPolicy,
		PolicyDocument: aws.String(`{"retentionInDays": 7, "enforcementMode": "RETROACTIVE"}`),
	})
	require.True(t, errors.As(err, &invalid), "account-level retention starts at 30 days: %v", err)
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeTransformerPolicy,
		PolicyDocument: aws.String(`[{"parseJSON":{}}]`), DryRun: true,
	})
	require.True(t, errors.As(err, &invalid), "a dry run is valid only for a retention policy: %v", err)

	preview, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeRetentionPolicy,
		PolicyDocument: aws.String(document), SelectionCriteria: aws.String(selection), DryRun: true,
	})
	require.NoError(t, err)
	assert.Nil(t, preview.AccountPolicy, "a dry run saves nothing")
	require.NotNil(t, preview.DryRunResult)
	result := preview.DryRunResult
	assert.Equal(t, int32(2), result.TotalMatchedLogGroups)
	assert.Equal(t, int32(2), result.DecreasedRetentionLogGroupCount)
	impacts := map[string][2]string{}
	for _, impact := range result.TopLogGroupsImpacted {
		impacts[aws.ToString(impact.LogGroupName)] = [2]string{aws.ToString(impact.CurrentRetention), aws.ToString(impact.ProposedRetention)}
	}
	assert.Equal(t, [2]string{"NEVER_EXPIRE", "90"}, impacts[never])
	assert.Equal(t, [2]string{"3653", "90"}, impacts[longer])
	described, err := cw.DescribeAccountPolicies(ctx, &cloudwatchlogs.DescribeAccountPoliciesInput{
		PolicyType: cwlogtypes.PolicyTypeRetentionPolicy, PolicyName: aws.String(policy),
	})
	require.NoError(t, err)
	assert.Empty(t, described.AccountPolicies)

	putAccountPolicy(t, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeRetentionPolicy,
		PolicyDocument: aws.String(document), SelectionCriteria: aws.String(selection),
	})
	groups, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(prefix)})
	require.NoError(t, err)
	require.Len(t, groups.LogGroups, 2)
	for _, g := range groups.LogGroups {
		assert.Empty(t, aws.ToString(g.RetentionPolicyName), "the policy takes effect 24 hours after it is put")
	}
	removal, err := cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{
		PolicyName: aws.String(policy), PolicyType: cwlogtypes.PolicyTypeRetentionPolicy, DryRun: true,
	})
	require.NoError(t, err)
	assert.Zero(t, removal.DryRunResult.TotalMatchedLogGroups, "deleting a policy not yet in effect changes nothing")
}

// A filter's fieldSelectionCriteria admits events by their system fields, and
// emitSystemFields adds those fields to each forwarded event.
func TestLogs_SubscriptionFilterSystemFields(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := uniquename.New("/test/subscription-system-fields")
	createLogGroupWithStream(t, group, "s")
	stream := uniquename.New("cwl-system-fields")
	streamARN := createActiveStream(t, stream)
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)
	var invalid *cwlogtypes.InvalidParameterException
	_, err := cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("bad"), FilterPattern: aws.String(""),
		DestinationArn: aws.String(streamARN), RoleArn: aws.String(role),
		EmitSystemFields: []string{"@aws.partition"},
	})
	require.True(t, errors.As(err, &invalid), "an unknown system field: %v", err)
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("Centralized"), FilterPattern: aws.String(""),
		DestinationArn: aws.String(streamARN), RoleArn: aws.String(role),
		FieldSelectionCriteria: aws.String(`@aws.region NOT IN ["cn-north-1"] AND @aws.account = "123456789012"`),
		EmitSystemFields:       []string{"@aws.account", "@aws.region", "@source.log"},
	})
	require.NoError(t, err)
	described, err := cw.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, described.SubscriptionFilters, 1)
	assert.Equal(t, []string{"@aws.account", "@aws.region", "@source.log"}, described.SubscriptionFilters[0].EmitSystemFields)
	assert.NotEmpty(t, aws.ToString(described.SubscriptionFilters[0].FieldSelectionCriteria))

	putLogLines(t, group, "s", "centralized line")
	messages, _ := awaitStreamDataMessages(t, stream, 1)
	require.Len(t, messages[0].LogEvents, 1)
	e := messages[0].LogEvents[0]
	assert.Equal(t, "123456789012", e.Account)
	assert.Equal(t, "us-east-1", e.Region)
	assert.Equal(t, group, e.SourceLog)
}

// Without a role that may write to the stream, a delivery is refused, counted
// in DeliveryErrors, and the filter stops forwarding for ten minutes.
func TestLogs_SubscriptionDeliveryErrorsMetric(t *testing.T) {
	t.Parallel()
	cw, iamC := cwLogsClient(), iamClient()
	group := uniquename.New("/test/subscription-errors")
	createLogGroupWithStream(t, group, "s")
	streamARN := createActiveStream(t, uniquename.New("cwl-errors"))
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)
	_, err := cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("Refused"), FilterPattern: aws.String(""),
		DestinationArn: aws.String(streamARN), RoleArn: aws.String(role),
	})
	require.NoError(t, err)
	roleName := role[len("arn:aws:iam::123456789012:role/"):]
	_, err = iamC.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("Permissions-Policy-For-CWL")})
	require.NoError(t, err)

	start := time.Now()
	putLogLines(t, group, "s", "refused", "also refused")
	require.Eventually(t, func() bool {
		return logsMetricSum(t, "AWS/Logs", "DeliveryErrors",
			dims("LogGroupName", group, "DestinationType", "Kinesis", "FilterName", "Refused"), start) == 2
	}, 30*time.Second, 200*time.Millisecond, "DeliveryErrors counts the refused batch's events")
}

// A destination's access policy is the resource policy that admits callers
// in other accounts: a member account's PutSubscriptionFilter on the
// management account's destination is denied until the policy names it.
func TestLogs_DestinationAccessPolicyAdmitsOtherAccounts(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	stream := uniquename.New("cwl-cross-account")
	streamARN := createActiveStream(t, stream)
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)
	destination := uniquename.New("recipient")
	put, err := cw.PutDestination(ctx, &cloudwatchlogs.PutDestinationInput{
		DestinationName: aws.String(destination), TargetArn: aws.String(streamARN), RoleArn: aws.String(role),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteDestination(ctx, &cloudwatchlogs.DeleteDestinationInput{DestinationName: aws.String(destination)})
	})
	destinationARN := aws.ToString(put.Destination.Arn)

	member := createMemberAccount(t, "cwl-sender", "")
	sender := cloudwatchlogs.NewFromConfig(sessionConfig(memberAdmin(t, member, "OrganizationAccountAccessRole")), func(o *cloudwatchlogs.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	group := uniquename.New("/test/cross-account-sender")
	_, err = sender.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	_, err = sender.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s")})
	require.NoError(t, err)
	subscribe := func() error {
		_, err := sender.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
			LogGroupName: aws.String(group), FilterName: aws.String("RecipientStream"), FilterPattern: aws.String(""),
			DestinationArn: aws.String(destinationARN),
		})
		return err
	}
	assertDenied(t, subscribe(), "a destination without an access policy refuses another account")

	_, err = cw.PutDestinationPolicy(ctx, &cloudwatchlogs.PutDestinationPolicyInput{
		DestinationName: aws.String(destination),
		AccessPolicy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + member + `"},` +
			`"Action":"logs:PutSubscriptionFilter","Resource":"` + destinationARN + `"}]}`),
	})
	require.NoError(t, err)
	require.NoError(t, subscribe(), "the access policy admits the member account")

	putLogLines(t, group, "s", "shared across accounts")
	messages, _ := awaitStreamDataMessages(t, stream, 1)
	assert.Equal(t, group, messages[0].LogGroup)
	require.Len(t, messages[0].LogEvents, 1)
	assert.Equal(t, "shared across accounts", messages[0].LogEvents[0].Message)
}

// A batch whose gzipped envelope exceeds AWS Lambda's 1 MB asynchronous
// payload goes to the function in parts that each fit, between them carrying
// every event once.
func TestLogs_SubscriptionSplitsOversizedBatches(t *testing.T) {
	t.Parallel()
	cw, lc := cwLogsClient(), lambdaClient()
	group := uniquename.New("/test/subscription-split")
	createLogGroupWithStream(t, group, "s")

	ln, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	payloads := make(chan []byte, 16)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		payloads <- body
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })

	functionName := uniquename.New("cwl-split")
	created, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(functionName),
		Role:          aws.String(lambdaRole(t)),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
		Timeout:       aws.Int32(60),
		Environment: &lambdatypes.Environment{Variables: map[string]string{
			"INVOCATION_CALLBACK_URL": fmt.Sprintf("http://host.docker.internal:%d/invoke", ln.Addr().(*net.TCPAddr).Port),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})
	_, err = lc.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(functionName), StatementId: aws.String("cloudwatch-logs"),
		Principal: aws.String("logs.amazonaws.com"), Action: aws.String("lambda:InvokeFunction"),
		SourceArn: aws.String("arn:aws:logs:us-east-1:123456789012:log-group:" + group + ":*"),
	})
	require.NoError(t, err)
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("all"), FilterPattern: aws.String(""),
		DestinationArn: created.FunctionArn,
	})
	require.NoError(t, err)

	// Printable characters other than '"' and '\' that gzip cannot shrink much.
	alphabet := []byte("!#$%&'()*+,-./0123456789:;<=>?@ABCDEFGHIJKLMNOPQRSTUVWXYZ[]^_`abcdefghijklmnopqrstuvwxyz{|}~")
	random := mathrand.New(mathrand.NewPCG(1, 2))
	lines := make([]string, 8)
	for i := range lines {
		b := make([]byte, 130000)
		for j := range b {
			b[j] = alphabet[random.IntN(len(alphabet))]
		}
		lines[i] = string(b)
	}
	putLogLines(t, group, "s", lines...)

	seen := map[string]bool{}
	invocations := 0
	for len(seen) < len(lines) {
		var payload []byte
		select {
		case payload = <-payloads:
		case <-time.After(2 * time.Minute):
			t.Fatalf("the function received %d of %d events", len(seen), len(lines))
		}
		assert.LessOrEqual(t, len(payload), 1024*1024, "each invocation fits the asynchronous payload limit")
		var event struct {
			Awslogs struct {
				Data string `json:"data"`
			} `json:"awslogs"`
		}
		require.NoError(t, json.Unmarshal(payload, &event))
		data, err := base64.StdEncoding.DecodeString(event.Awslogs.Data)
		require.NoError(t, err)
		invocations++
		for _, m := range decodeLogsSubscriptionData(t, data) {
			for _, e := range m.LogEvents {
				assert.False(t, seen[e.Message], "each event is forwarded once")
				seen[e.Message] = true
			}
		}
	}
	assert.Greater(t, invocations, 1, "the batch is split across invocations")
}
