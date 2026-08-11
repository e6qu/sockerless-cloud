//go:build liveaws

package aws_sdk_test

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	sfntypes "github.com/aws/aws-sdk-go-v2/service/sfn/types"
	"github.com/stretchr/testify/require"
)

// TestSFNTestStateDifferentialAgainstAWS sends the same Amazon States Language
// states through the simulator and the real AWS Step Functions TestState API.
// The clients differ only in endpoint and credentials. This suite is compiled
// only by the live-aws-conformance workflow, where credentials are mandatory.
func TestSFNTestStateDifferentialAgainstAWS(t *testing.T) {
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err, "live AWS credentials must load")
	_, err = liveConfig.Credentials.Retrieve(ctx)
	require.NoError(t, err, "live AWS credentials must resolve")
	live := sfn.NewFromConfig(liveConfig)
	simulator := sfnClient()

	vectors := []struct {
		name       string
		definition string
		input      string
		variables  string
		stateName  string
	}{
		{
			name:       "pass-result",
			definition: `{"Type":"Pass","Result":{"ok":true},"End":true}`,
			input:      `{"ignored":true}`,
		},
		{
			name: "jsonpath-data-flow",
			definition: `{"Type":"Pass","InputPath":"$.order",` +
				`"Parameters":{"id.$":"$.id","sum.$":"States.MathAdd($.a,$.b)"},` +
				`"ResultPath":"$.selected","OutputPath":"$.selected","End":true}`,
			input: `{"order":{"id":"A","a":2,"b":3},"keep":true}`,
		},
		{
			name: "jsonpath-intrinsics",
			definition: `{"Type":"Pass","Parameters":{` +
				`"partition.$":"States.ArrayPartition($.items,2)",` +
				`"contains.$":"States.ArrayContains($.items,3)",` +
				`"encoded.$":"States.Base64Encode($.text)",` +
				`"digest.$":"States.Hash($.text,'SHA-256')",` +
				`"formatted.$":"States.Format('{}-{}',$.prefix,$.suffix)"},` +
				`"End":true}`,
			input: `{"items":[1,2,3,4,5],"text":"hello","prefix":"left","suffix":"right"}`,
		},
		{
			name:       "jsonpath-wildcard-selection",
			definition: `{"Type":"Pass","Parameters":{"names.$":"$.items[*].name"},"End":true}`,
			input:      `{"items":[{"name":"Ada"},{"name":"Grace"},{"name":"Linus"}]}`,
		},
		{
			name: "jsonata-output-and-assignment",
			definition: `{"QueryLanguage":"JSONata","Type":"Pass",` +
				`"Assign":{"saved":"{% $states.input.name %}"},` +
				`"Output":{"greeting":"{% \"hello \" & $states.input.name %}"},"End":true}`,
			input:     `{"name":"Ada"}`,
			variables: `{}`,
		},
		{
			name: "jsonata-variable-input",
			definition: `{"QueryLanguage":"JSONata","Type":"Pass",` +
				`"Output":{"total":"{% $base + $states.input.delta %}"},"End":true}`,
			input:     `{"delta":7}`,
			variables: `{"base":35}`,
		},
		{
			name:       "result-path-discard",
			definition: `{"Type":"Pass","Result":{"discarded":true},"ResultPath":null,"End":true}`,
			input:      `{"kept":true}`,
		},
		{
			name:      "full-definition-selected-state",
			stateName: "Selected",
			definition: `{"StartAt":"Ignored","States":{` +
				`"Ignored":{"Type":"Pass","Result":{"wrong":true},"Next":"Selected"},` +
				`"Selected":{"Type":"Pass","Parameters":{"selected.$":"$.value"},"End":true}}}`,
			input: `{"value":"external"}`,
		},
	}

	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			t.Parallel()
			testContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			request := &sfn.TestStateInput{
				Definition:      aws.String(vector.definition),
				Input:           aws.String(vector.input),
				InspectionLevel: sfntypes.InspectionLevelDebug,
			}
			if vector.variables != "" {
				request.Variables = aws.String(vector.variables)
			}
			if vector.stateName != "" {
				request.StateName = aws.String(vector.stateName)
			}
			expected, err := live.TestState(testContext, request)
			require.NoError(t, err, "real AWS Step Functions TestState")
			actual, err := simulator.TestState(testContext, request)
			require.NoError(t, err, "simulator AWS Step Functions TestState")
			require.Equalf(t, expected.Status, actual.Status, "simulator error=%q cause=%q", aws.ToString(actual.Error), aws.ToString(actual.Cause))
			require.Equal(t, aws.ToString(expected.Error), aws.ToString(actual.Error))
			require.JSONEq(t, aws.ToString(expected.Output), aws.ToString(actual.Output))
		})
	}
}

func TestSFNExecutionHistoryDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_SFN_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_SFN_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	live := sfn.NewFromConfig(liveConfig)
	simulator := sfnClient()
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	stateMachineName := "sockerless-diff-" + suffix
	definition := `{"StartAt":"Choose","States":{` +
		`"Choose":{"Type":"Choice","Choices":[{"Variable":"$.value","NumericGreaterThan":5,"Next":"Large"}],"Default":"Small"},` +
		`"Large":{"Type":"Pass","Result":{"class":"large"},"End":true},` +
		`"Small":{"Type":"Pass","Result":{"class":"small"},"End":true}}}`

	type target struct {
		name   string
		client *sfn.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/sfn-role"},
	}
	type observed struct {
		output string
		types  []sfntypes.HistoryEventType
	}
	results := map[string]observed{}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			created, err := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String(stateMachineName),
				Definition: aws.String(definition),
				RoleArn:    aws.String(target.role),
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
					StateMachineArn: created.StateMachineArn,
				})
			})
			started, err := target.client.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: created.StateMachineArn,
				Name:            aws.String("external-history"),
				Input:           aws.String(`{"value":9}`),
			})
			require.NoError(t, err)
			var described *sfn.DescribeExecutionOutput
			require.Eventually(t, func() bool {
				described, err = target.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
					ExecutionArn: started.ExecutionArn,
				})
				return err == nil && described.Status != sfntypes.ExecutionStatusRunning
			}, 30*time.Second, 300*time.Millisecond)
			require.Equal(t, sfntypes.ExecutionStatusSucceeded, described.Status)
			history, err := target.client.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{
				ExecutionArn: started.ExecutionArn,
			})
			require.NoError(t, err)
			eventTypes := make([]sfntypes.HistoryEventType, 0, len(history.Events))
			for _, event := range history.Events {
				eventTypes = append(eventTypes, event.Type)
			}
			results[target.name] = observed{
				output: aws.ToString(described.Output),
				types:  eventTypes,
			}
		})
	}
	require.JSONEq(t, results["real-aws"].output, results["simulator"].output)
	require.Equal(t, results["real-aws"].types, results["simulator"].types)
}

func TestSFNLambdaTaskHistoryDifferentialAgainstAWS(t *testing.T) {
	stateMachineRoleARN := os.Getenv("AWS_LIVE_SFN_ROLE_ARN")
	lambdaRoleARN := os.Getenv("AWS_LIVE_LAMBDA_ROLE_ARN")
	require.NotEmpty(t, stateMachineRoleARN, "AWS_LIVE_SFN_ROLE_ARN is required")
	require.NotEmpty(t, lambdaRoleARN, "AWS_LIVE_LAMBDA_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	functionName := "sockerless-sfn-task-" + suffix
	stateMachineName := "sockerless-sfn-task-history-" + suffix
	deployment := liveZIP(t, map[string]string{
		"index.js": `exports.handler = async (event) => ({called:true,input:event});`,
	})
	type target struct {
		name       string
		states     *sfn.Client
		lambda     *lambda.Client
		lambdaRole string
		statesRole string
	}
	targets := []target{
		{
			name:       "real-aws",
			states:     sfn.NewFromConfig(liveConfig),
			lambda:     lambda.NewFromConfig(liveConfig),
			lambdaRole: lambdaRoleARN,
			statesRole: stateMachineRoleARN,
		},
		{
			name:       "simulator",
			states:     sfnClient(),
			lambda:     lambdaClient(),
			lambdaRole: "arn:aws:iam::123456789012:role/lambda-role",
			statesRole: "arn:aws:iam::123456789012:role/sfn-role",
		},
	}
	type observed struct {
		types  []sfntypes.HistoryEventType
		input  string
		output string
	}
	results := make(map[string]observed, len(targets))
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			createdFunction, createErr := target.lambda.CreateFunction(ctx, &lambda.CreateFunctionInput{
				FunctionName: aws.String(functionName),
				Role:         aws.String(target.lambdaRole),
				Runtime:      lambdatypes.RuntimeNodejs20x,
				Handler:      aws.String("index.handler"),
				Code:         &lambdatypes.FunctionCode{ZipFile: deployment},
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.lambda.DeleteFunction(ctx, &lambda.DeleteFunctionInput{
					FunctionName: aws.String(functionName),
				})
			})
			require.Eventually(t, func() bool {
				configuration, getErr := target.lambda.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
					FunctionName: aws.String(functionName),
				})
				return getErr == nil && configuration.State == lambdatypes.StateActive
			}, 45*time.Second, time.Second)

			definition := fmt.Sprintf(
				`{"StartAt":"Invoke","States":{"Invoke":{"Type":"Task","Resource":%q,"End":true}}}`,
				aws.ToString(createdFunction.FunctionArn),
			)
			createdMachine, createErr := target.states.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String(stateMachineName),
				Definition: aws.String(definition),
				RoleArn:    aws.String(target.statesRole),
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.states.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
					StateMachineArn: createdMachine.StateMachineArn,
				})
			})
			started, startErr := target.states.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: createdMachine.StateMachineArn,
				Input:           aws.String(`{"request":"external-history"}`),
			})
			require.NoError(t, startErr)
			require.Eventually(t, func() bool {
				described, describeErr := target.states.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
					ExecutionArn: started.ExecutionArn,
				})
				return describeErr == nil && described.Status == sfntypes.ExecutionStatusSucceeded
			}, 60*time.Second, time.Second)
			history, historyErr := target.states.GetExecutionHistory(ctx, &sfn.GetExecutionHistoryInput{
				ExecutionArn: started.ExecutionArn,
			})
			require.NoError(t, historyErr)
			observation := observed{types: make([]sfntypes.HistoryEventType, 0, len(history.Events))}
			for _, event := range history.Events {
				observation.types = append(observation.types, event.Type)
				if event.LambdaFunctionScheduledEventDetails != nil {
					observation.input = aws.ToString(event.LambdaFunctionScheduledEventDetails.Input)
				}
				if event.LambdaFunctionSucceededEventDetails != nil {
					observation.output = aws.ToString(event.LambdaFunctionSucceededEventDetails.Output)
				}
			}
			results[target.name] = observation
		})
	}
	require.Equal(t, results["real-aws"].types, results["simulator"].types)
	require.JSONEq(t, results["real-aws"].input, results["simulator"].input)
	require.JSONEq(t, results["real-aws"].output, results["simulator"].output)
}

func TestSFNNestedWorkflowDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_SFN_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_SFN_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	live := sfn.NewFromConfig(liveConfig)
	simulator := sfnClient()
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	type target struct {
		name   string
		client *sfn.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/sfn-role"},
	}
	type observed struct {
		parentOutput string
		parentStatus sfntypes.ExecutionStatus
		childCount   int
		childStatus  sfntypes.ExecutionStatus
	}
	results := map[string]observed{}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			child, createErr := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String("sockerless-nested-child-" + suffix),
				Definition: aws.String(`{"StartAt":"Complete","States":{"Complete":{"Type":"Pass","Parameters":{"child.$":"$"},"End":true}}}`),
				RoleArn:    aws.String(target.role),
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: child.StateMachineArn})
			})
			parentDefinition := fmt.Sprintf(
				`{"StartAt":"Child","States":{"Child":{"Type":"Task","Resource":"arn:aws:states:::states:startExecution.sync:2","Parameters":{"StateMachineArn":%q,"Input":{"order":"external"}},"OutputPath":"$.Output","End":true}}}`,
				aws.ToString(child.StateMachineArn),
			)
			parent, createErr := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String("sockerless-nested-parent-" + suffix),
				Definition: aws.String(parentDefinition),
				RoleArn:    aws.String(target.role),
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{StateMachineArn: parent.StateMachineArn})
			})
			started, startErr := target.client.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: parent.StateMachineArn,
			})
			require.NoError(t, startErr)
			var parentExecution *sfn.DescribeExecutionOutput
			require.Eventually(t, func() bool {
				parentExecution, err = target.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
					ExecutionArn: started.ExecutionArn,
				})
				return err == nil && parentExecution.Status != sfntypes.ExecutionStatusRunning
			}, 30*time.Second, 250*time.Millisecond)
			children, listErr := target.client.ListExecutions(ctx, &sfn.ListExecutionsInput{
				StateMachineArn: child.StateMachineArn,
			})
			require.NoError(t, listErr)
			require.Len(t, children.Executions, 1)
			results[target.name] = observed{
				parentOutput: aws.ToString(parentExecution.Output),
				parentStatus: parentExecution.Status,
				childCount:   len(children.Executions),
				childStatus:  children.Executions[0].Status,
			}
		})
	}
	require.Equal(t, results["real-aws"], results["simulator"])
	require.JSONEq(t, `{"child":{"order":"external"}}`, results["simulator"].parentOutput)
}

func TestSFNDistributedMapDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_SFN_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_SFN_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	live := sfn.NewFromConfig(liveConfig)
	simulator := sfnClient()
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	definition := `{
		"StartAt":"Distributed",
		"States":{"Distributed":{
			"Type":"Map",
			"MaxConcurrency":2,
			"ItemProcessor":{
				"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},
				"StartAt":"Complete",
				"States":{"Complete":{"Type":"Pass","End":true}}
			},
			"End":true
		}}
	}`
	type target struct {
		name   string
		client *sfn.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/sfn-role"},
	}
	type observed struct {
		status             sfntypes.MapRunStatus
		itemTotal          int64
		itemSucceeded      int64
		executionTotal     int64
		executionSucceeded int64
		resultsWritten     int64
		childCount         int
		childrenSucceeded  bool
	}
	results := make(map[string]observed, len(targets))
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			created, createErr := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String("sockerless-map-" + suffix),
				Definition: aws.String(definition),
				RoleArn:    aws.String(target.role),
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
					StateMachineArn: created.StateMachineArn,
				})
			})
			started, startErr := target.client.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: created.StateMachineArn,
				Input:           aws.String(`[{"id":1},{"id":2},{"id":3}]`),
			})
			require.NoError(t, startErr)
			var mapRunArn *string
			require.Eventually(t, func() bool {
				listed, listErr := target.client.ListMapRuns(ctx, &sfn.ListMapRunsInput{
					ExecutionArn: started.ExecutionArn,
				})
				if listErr != nil || len(listed.MapRuns) != 1 {
					return false
				}
				mapRunArn = listed.MapRuns[0].MapRunArn
				return aws.ToString(mapRunArn) != ""
			}, 30*time.Second, 250*time.Millisecond)
			var mapRun *sfn.DescribeMapRunOutput
			require.Eventually(t, func() bool {
				mapRun, err = target.client.DescribeMapRun(ctx, &sfn.DescribeMapRunInput{MapRunArn: mapRunArn})
				return err == nil && mapRun.Status != sfntypes.MapRunStatusRunning
			}, 30*time.Second, 250*time.Millisecond)
			children, listErr := target.client.ListExecutions(ctx, &sfn.ListExecutionsInput{MapRunArn: mapRunArn})
			require.NoError(t, listErr)
			childrenSucceeded := len(children.Executions) == 3
			for _, child := range children.Executions {
				childrenSucceeded = childrenSucceeded && child.Status == sfntypes.ExecutionStatusSucceeded
			}
			results[target.name] = observed{
				status:             mapRun.Status,
				itemTotal:          mapRun.ItemCounts.Total,
				itemSucceeded:      mapRun.ItemCounts.Succeeded,
				executionTotal:     mapRun.ExecutionCounts.Total,
				executionSucceeded: mapRun.ExecutionCounts.Succeeded,
				resultsWritten:     mapRun.ItemCounts.ResultsWritten,
				childCount:         len(children.Executions),
				childrenSucceeded:  childrenSucceeded,
			}
		})
	}
	require.Equal(t, results["real-aws"], results["simulator"])
}

func TestSFNControlPlaneDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_SFN_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_SFN_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	live := sfn.NewFromConfig(liveConfig)
	simulator := sfnClient()
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")

	type target struct {
		name   string
		client *sfn.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/sfn-role"},
	}
	type observed struct {
		versionCount      int
		aliasCount        int
		tagCount          int
		executionStatus   sfntypes.ExecutionStatus
		executionOutput   string
		usedAlias         bool
		usedVersion       bool
		activityStatus    sfntypes.ExecutionStatus
		activityOutput    string
		activityTagCount  int
		invalidDefinition sfntypes.ValidateStateMachineDefinitionResultCode
	}
	results := map[string]observed{}

	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			stateMachineName := "sockerless-control-" + suffix
			created, err := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:               aws.String(stateMachineName),
				Definition:         aws.String(`{"StartAt":"Initial","States":{"Initial":{"Type":"Pass","Result":{"version":1},"End":true}}}`),
				RoleArn:            aws.String(target.role),
				Publish:            true,
				VersionDescription: aws.String("initial externally validated version"),
				Tags:               []sfntypes.Tag{{Key: aws.String("oracle"), Value: aws.String("external")}},
			})
			require.NoError(t, err)
			require.NotEmpty(t, aws.ToString(created.StateMachineVersionArn))
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
					StateMachineArn: created.StateMachineArn,
				})
			})

			described, err := target.client.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
				StateMachineArn: created.StateMachineArn,
			})
			require.NoError(t, err)
			require.Equal(t, stateMachineName, aws.ToString(described.Name))
			version, err := target.client.DescribeStateMachine(ctx, &sfn.DescribeStateMachineInput{
				StateMachineArn: created.StateMachineVersionArn,
			})
			require.NoError(t, err)
			require.JSONEq(t, `{"StartAt":"Initial","States":{"Initial":{"Type":"Pass","Result":{"version":1},"End":true}}}`, aws.ToString(version.Definition))

			alias, err := target.client.CreateStateMachineAlias(ctx, &sfn.CreateStateMachineAliasInput{
				Name: aws.String("PROD"),
				RoutingConfiguration: []sfntypes.RoutingConfigurationListItem{{
					StateMachineVersionArn: created.StateMachineVersionArn,
					Weight:                 100,
				}},
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachineAlias(ctx, &sfn.DeleteStateMachineAliasInput{
					StateMachineAliasArn: alias.StateMachineAliasArn,
				})
			})

			started, err := target.client.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: alias.StateMachineAliasArn,
				Name:            aws.String("alias-execution"),
				Input:           aws.String(`{}`),
			})
			require.NoError(t, err)
			var execution *sfn.DescribeExecutionOutput
			require.Eventually(t, func() bool {
				execution, err = target.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
					ExecutionArn: started.ExecutionArn,
				})
				return err == nil && execution.Status != sfntypes.ExecutionStatusRunning
			}, 30*time.Second, 250*time.Millisecond)

			updated, err := target.client.UpdateStateMachine(ctx, &sfn.UpdateStateMachineInput{
				StateMachineArn:    created.StateMachineArn,
				Definition:         aws.String(`{"StartAt":"Updated","States":{"Updated":{"Type":"Pass","Result":{"version":2},"End":true}}}`),
				Publish:            true,
				VersionDescription: aws.String("updated externally validated version"),
			})
			require.NoError(t, err)
			require.NotEmpty(t, aws.ToString(updated.StateMachineVersionArn))
			versions, err := target.client.ListStateMachineVersions(ctx, &sfn.ListStateMachineVersionsInput{
				StateMachineArn: created.StateMachineArn,
			})
			require.NoError(t, err)
			aliases, err := target.client.ListStateMachineAliases(ctx, &sfn.ListStateMachineAliasesInput{
				StateMachineArn: created.StateMachineArn,
			})
			require.NoError(t, err)
			tags, err := target.client.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{
				ResourceArn: created.StateMachineArn,
			})
			require.NoError(t, err)
			invalid, err := target.client.ValidateStateMachineDefinition(ctx, &sfn.ValidateStateMachineDefinitionInput{
				Definition: aws.String(`{"StartAt":"Missing","States":{"Done":{"Type":"Succeed"}}}`),
			})
			require.NoError(t, err)
			require.NotEmpty(t, invalid.Diagnostics)

			activity, err := target.client.CreateActivity(ctx, &sfn.CreateActivityInput{
				Name: aws.String("sockerless-activity-" + suffix),
				Tags: []sfntypes.Tag{{Key: aws.String("oracle"), Value: aws.String("external")}},
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = target.client.DeleteActivity(ctx, &sfn.DeleteActivityInput{ActivityArn: activity.ActivityArn})
			})
			activityMachine, err := target.client.CreateStateMachine(ctx, &sfn.CreateStateMachineInput{
				Name:       aws.String("sockerless-activity-sm-" + suffix),
				Definition: aws.String(`{"StartAt":"Work","States":{"Work":{"Type":"Task","Resource":` + strconv.Quote(aws.ToString(activity.ActivityArn)) + `,"End":true}}}`),
				RoleArn:    aws.String(target.role),
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = target.client.DeleteStateMachine(ctx, &sfn.DeleteStateMachineInput{
					StateMachineArn: activityMachine.StateMachineArn,
				})
			})
			activityExecution, err := target.client.StartExecution(ctx, &sfn.StartExecutionInput{
				StateMachineArn: activityMachine.StateMachineArn,
				Input:           aws.String(`{"source":"external-worker"}`),
			})
			require.NoError(t, err)
			task, err := target.client.GetActivityTask(ctx, &sfn.GetActivityTaskInput{
				ActivityArn: activity.ActivityArn,
				WorkerName:  aws.String("external-worker"),
			})
			require.NoError(t, err)
			require.NotEmpty(t, aws.ToString(task.TaskToken))
			require.JSONEq(t, `{"source":"external-worker"}`, aws.ToString(task.Input))
			_, err = target.client.SendTaskHeartbeat(ctx, &sfn.SendTaskHeartbeatInput{TaskToken: task.TaskToken})
			require.NoError(t, err)
			_, err = target.client.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{
				TaskToken: task.TaskToken,
				Output:    aws.String(`{"worker":"completed"}`),
			})
			require.NoError(t, err)
			var activityResult *sfn.DescribeExecutionOutput
			require.Eventually(t, func() bool {
				activityResult, err = target.client.DescribeExecution(ctx, &sfn.DescribeExecutionInput{
					ExecutionArn: activityExecution.ExecutionArn,
				})
				return err == nil && activityResult.Status != sfntypes.ExecutionStatusRunning
			}, 30*time.Second, 250*time.Millisecond)
			activityTags, err := target.client.ListTagsForResource(ctx, &sfn.ListTagsForResourceInput{
				ResourceArn: activity.ActivityArn,
			})
			require.NoError(t, err)

			results[target.name] = observed{
				versionCount:      len(versions.StateMachineVersions),
				aliasCount:        len(aliases.StateMachineAliases),
				tagCount:          len(tags.Tags),
				executionStatus:   execution.Status,
				executionOutput:   aws.ToString(execution.Output),
				usedAlias:         aws.ToString(execution.StateMachineAliasArn) != "",
				usedVersion:       aws.ToString(execution.StateMachineVersionArn) != "",
				activityStatus:    activityResult.Status,
				activityOutput:    aws.ToString(activityResult.Output),
				activityTagCount:  len(activityTags.Tags),
				invalidDefinition: invalid.Result,
			}
		})
	}
	require.Equal(t, results["real-aws"], results["simulator"])
}

// TestAWSLiveEnvironmentIsExplicit prevents an accidental local invocation
// from silently using an unintended account or Region.
func TestAWSLiveEnvironmentIsExplicit(t *testing.T) {
	require.NotEmpty(t, os.Getenv("AWS_REGION"), "AWS_REGION is required for live AWS conformance")
	require.NotEmpty(t, os.Getenv("AWS_LIVE_LAMBDA_ROLE_ARN"), "AWS_LIVE_LAMBDA_ROLE_ARN is required for live AWS Lambda conformance")
}

func TestLambdaZIPAndLayerDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_LAMBDA_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_LAMBDA_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	_, err = liveConfig.Credentials.Retrieve(ctx)
	require.NoError(t, err)
	live := lambda.NewFromConfig(liveConfig)
	simulator := lambdaClient()

	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	functionName := "sockerless-diff-" + suffix
	layerName := "sockerless-diff-layer-" + suffix
	deployment := liveZIP(t, map[string]string{
		"index.js": `exports.handler = async (event) => ({event, layer: require("sockerless-diff-layer")});`,
	})
	layerArchive := liveZIP(t, map[string]string{
		"nodejs/node_modules/sockerless-diff-layer/index.js": `module.exports = "loaded-from-real-layer";`,
	})

	type target struct {
		name   string
		client *lambda.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/test-role"},
	}
	type observed struct {
		codeSize int64
		codeSHA  string
		output   string
		code     []byte
		layer    []byte
	}
	results := map[string]observed{}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			published, err := target.client.PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{
				LayerName: aws.String(layerName),
				Content:   &lambdatypes.LayerVersionContentInput{ZipFile: layerArchive},
				CompatibleRuntimes: []lambdatypes.Runtime{
					lambdatypes.RuntimeNodejs20x,
				},
			})
			require.NoError(t, err)
			require.NotEmpty(t, aws.ToString(published.LayerVersionArn))
			t.Cleanup(func() {
				_, _ = target.client.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{
					LayerName:     aws.String(layerName),
					VersionNumber: aws.Int64(published.Version),
				})
			})

			created, err := target.client.CreateFunction(ctx, &lambda.CreateFunctionInput{
				FunctionName: aws.String(functionName),
				Role:         aws.String(target.role),
				Runtime:      lambdatypes.RuntimeNodejs20x,
				Handler:      aws.String("index.handler"),
				Code:         &lambdatypes.FunctionCode{ZipFile: deployment},
				Layers:       []string{aws.ToString(published.LayerVersionArn)},
				MemorySize:   aws.Int32(192),
				Timeout:      aws.Int32(10),
				Environment: &lambdatypes.Environment{
					Variables: map[string]string{"DIFFERENTIAL": "true"},
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = target.client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
			})

			require.Eventually(t, func() bool {
				configuration, getErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
					FunctionName: aws.String(functionName),
				})
				return getErr == nil && configuration.State == lambdatypes.StateActive
			}, 30*time.Second, 500*time.Millisecond)
			function, err := target.client.GetFunction(ctx, &lambda.GetFunctionInput{
				FunctionName: aws.String(functionName),
			})
			require.NoError(t, err)
			require.NotNil(t, function.Code)
			codeDownload, err := http.Get(aws.ToString(function.Code.Location))
			require.NoError(t, err)
			defer codeDownload.Body.Close()
			require.Equal(t, http.StatusOK, codeDownload.StatusCode)
			codeBytes, err := io.ReadAll(codeDownload.Body)
			require.NoError(t, err)
			layerVersion, err := target.client.GetLayerVersion(ctx, &lambda.GetLayerVersionInput{
				LayerName:     aws.String(layerName),
				VersionNumber: aws.Int64(published.Version),
			})
			require.NoError(t, err)
			require.NotNil(t, layerVersion.Content)
			layerDownload, err := http.Get(aws.ToString(layerVersion.Content.Location))
			require.NoError(t, err)
			defer layerDownload.Body.Close()
			require.Equal(t, http.StatusOK, layerDownload.StatusCode)
			layerBytes, err := io.ReadAll(layerDownload.Body)
			require.NoError(t, err)
			invoked, err := target.client.Invoke(ctx, &lambda.InvokeInput{
				FunctionName: aws.String(functionName),
				Payload:      []byte(`{"source":"external-differential"}`),
			})
			require.NoError(t, err)
			require.Empty(t, aws.ToString(invoked.FunctionError), string(invoked.Payload))
			results[target.name] = observed{
				codeSize: created.CodeSize,
				codeSHA:  aws.ToString(created.CodeSha256),
				output:   string(invoked.Payload),
				code:     codeBytes,
				layer:    layerBytes,
			}
		})
	}
	require.Equal(t, results["real-aws"].codeSize, results["simulator"].codeSize)
	require.Equal(t, results["real-aws"].codeSHA, results["simulator"].codeSHA)
	require.Equal(t, results["real-aws"].code, results["simulator"].code)
	require.Equal(t, results["real-aws"].layer, results["simulator"].layer)
	require.JSONEq(t, results["real-aws"].output, results["simulator"].output)
}

// TestLambdaVersionAliasDifferentialAgainstAWS verifies that published
// versions are immutable executable snapshots, aliases invoke the selected
// version, and current configuration fields round-trip identically through
// the same official SDK calls against real AWS Lambda and the simulator.
func TestLambdaVersionAliasDifferentialAgainstAWS(t *testing.T) {
	roleARN := os.Getenv("AWS_LIVE_LAMBDA_ROLE_ARN")
	require.NotEmpty(t, roleARN, "AWS_LIVE_LAMBDA_ROLE_ARN is required")
	liveConfig, err := config.LoadDefaultConfig(ctx)
	require.NoError(t, err)
	live := lambda.NewFromConfig(liveConfig)
	simulator := lambdaClient()
	suffix := strings.ReplaceAll(time.Now().UTC().Format("150405.000000000"), ".", "")
	functionName := "sockerless-version-diff-" + suffix
	firstCode := liveZIP(t, map[string]string{
		"index.js": `exports.handler = async () => ({release:"one"});`,
	})
	secondCode := liveZIP(t, map[string]string{
		"index.js": `exports.handler = async () => ({release:"two"});`,
	})

	type target struct {
		name   string
		client *lambda.Client
		role   string
	}
	targets := []target{
		{name: "real-aws", client: live, role: roleARN},
		{name: "simulator", client: simulator, role: "arn:aws:iam::123456789012:role/test-role"},
	}
	type observed struct {
		versionPayload string
		latestPayload  string
		aliasPayload   string
		aliasVersion   string
		memory         int32
		timeout        int32
		ephemeral      int32
		environment    string
	}
	results := map[string]observed{}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			_, createErr := target.client.CreateFunction(ctx, &lambda.CreateFunctionInput{
				FunctionName: aws.String(functionName),
				Role:         aws.String(target.role),
				Runtime:      lambdatypes.RuntimeNodejs20x,
				Handler:      aws.String("index.handler"),
				Code:         &lambdatypes.FunctionCode{ZipFile: firstCode},
				MemorySize:   aws.Int32(192),
				Timeout:      aws.Int32(10),
				EphemeralStorage: &lambdatypes.EphemeralStorage{
					Size: aws.Int32(1024),
				},
				LoggingConfig: &lambdatypes.LoggingConfig{
					LogFormat:           lambdatypes.LogFormatJson,
					ApplicationLogLevel: lambdatypes.ApplicationLogLevelInfo,
					SystemLogLevel:      lambdatypes.SystemLogLevelInfo,
				},
			})
			require.NoError(t, createErr)
			t.Cleanup(func() {
				_, _ = target.client.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
			})
			require.Eventually(t, func() bool {
				configuration, getErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
					FunctionName: aws.String(functionName),
				})
				return getErr == nil && configuration.State == lambdatypes.StateActive &&
					configuration.LastUpdateStatus == lambdatypes.LastUpdateStatusSuccessful
			}, 30*time.Second, 500*time.Millisecond)
			published, publishErr := target.client.PublishVersion(ctx, &lambda.PublishVersionInput{
				FunctionName: aws.String(functionName),
				Description:  aws.String("immutable first release"),
			})
			require.NoError(t, publishErr)
			require.Equal(t, "1", aws.ToString(published.Version))

			_, updateCodeErr := target.client.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
				FunctionName: aws.String(functionName),
				ZipFile:      secondCode,
			})
			require.NoError(t, updateCodeErr)
			require.Eventually(t, func() bool {
				configuration, getErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
					FunctionName: aws.String(functionName),
				})
				return getErr == nil && configuration.LastUpdateStatus == lambdatypes.LastUpdateStatusSuccessful
			}, 30*time.Second, 500*time.Millisecond)
			current, getErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
				FunctionName: aws.String(functionName),
			})
			require.NoError(t, getErr)
			_, updateConfigErr := target.client.UpdateFunctionConfiguration(ctx, &lambda.UpdateFunctionConfigurationInput{
				FunctionName: aws.String(functionName),
				RevisionId:   current.RevisionId,
				MemorySize:   aws.Int32(256),
				Timeout:      aws.Int32(12),
				Environment: &lambdatypes.Environment{
					Variables: map[string]string{"RELEASE": "current"},
				},
			})
			require.NoError(t, updateConfigErr)
			require.Eventually(t, func() bool {
				configuration, configErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
					FunctionName: aws.String(functionName),
				})
				return configErr == nil && configuration.LastUpdateStatus == lambdatypes.LastUpdateStatusSuccessful
			}, 30*time.Second, 500*time.Millisecond)
			_, aliasErr := target.client.CreateAlias(ctx, &lambda.CreateAliasInput{
				FunctionName:    aws.String(functionName),
				Name:            aws.String("prod"),
				FunctionVersion: published.Version,
			})
			require.NoError(t, aliasErr)

			invoke := func(qualifier string) *lambda.InvokeOutput {
				t.Helper()
				output, invokeErr := target.client.Invoke(ctx, &lambda.InvokeInput{
					FunctionName: aws.String(functionName),
					Qualifier:    aws.String(qualifier),
					Payload:      []byte(`{}`),
				})
				require.NoError(t, invokeErr)
				require.Empty(t, aws.ToString(output.FunctionError), string(output.Payload))
				return output
			}
			versionResult := invoke("1")
			latestResult := invoke("$LATEST")
			aliasResult := invoke("prod")
			configuration, configErr := target.client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{
				FunctionName: aws.String(functionName),
			})
			require.NoError(t, configErr)
			results[target.name] = observed{
				versionPayload: string(versionResult.Payload),
				latestPayload:  string(latestResult.Payload),
				aliasPayload:   string(aliasResult.Payload),
				aliasVersion:   aws.ToString(aliasResult.ExecutedVersion),
				memory:         aws.ToInt32(configuration.MemorySize),
				timeout:        aws.ToInt32(configuration.Timeout),
				ephemeral:      aws.ToInt32(configuration.EphemeralStorage.Size),
				environment:    configuration.Environment.Variables["RELEASE"],
			}
		})
	}
	require.Equal(t, results["real-aws"], results["simulator"])
	require.JSONEq(t, `{"release":"one"}`, results["simulator"].versionPayload)
	require.JSONEq(t, `{"release":"two"}`, results["simulator"].latestPayload)
	require.JSONEq(t, `{"release":"one"}`, results["simulator"].aliasPayload)
}

func liveZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for name, content := range files {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return out.Bytes()
}
