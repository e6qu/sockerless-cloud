package aws_sdk_test

import (
	"fmt"
	"regexp"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The REPORT entry closing an AWS Lambda invocation states what the
// environment consumed:
//
//	REPORT RequestId: … Duration: … ms Billed Duration: … ms Memory Size: 2048 MB
//	Max Memory Used: 466 MB
//
// Memory Size is configuration; Max Memory Used is a measurement. Two functions
// with the same memory size, one holding a large allocation through the
// invocation and one holding nothing, report figures about the allocation
// apart, which a figure derived from the configuration could not.

func lambdaReportMemoryMegabytes(t *testing.T, report string) int {
	t.Helper()
	match := regexp.MustCompile(`\tMax Memory Used: ([0-9]+) MB`).FindStringSubmatch(report)
	require.Len(t, match, 2, "REPORT must carry Max Memory Used:\n%s", report)
	value, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return value
}

// lambdaInvokeForMemoryReport creates a Node function with the given
// module-level source, invokes it once, and returns its REPORT's memory figure.
func lambdaInvokeForMemoryReport(t *testing.T, name, source string, memorySize int32) int {
	t.Helper()
	lc := lambdaClient()
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs18x,
		Handler:      aws.String("index.handler"),
		Timeout:      aws.Int32(30),
		MemorySize:   aws.Int32(memorySize),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t, source)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)})
	})

	out, err := lc.Invoke(ctx, &lambda.InvokeInput{
		FunctionName: aws.String(name),
		Payload:      []byte(`{}`),
	})
	require.NoError(t, err)
	require.Empty(t, aws.ToString(out.FunctionError), "invocation failed: %s", out.Payload)

	report := lambdaReportEntry(t, lambdaInvocationLog(t, name))
	assert.Contains(t, report, fmt.Sprintf("\tMemory Size: %d MB", memorySize))
	return lambdaReportMemoryMegabytes(t, report)
}

// TestLambda_ReportMaxMemoryUsedIsMeasured expects two functions with the same
// memory size, one holding a large allocation and one nothing, to report memory
// differing by about the allocation.
func TestLambda_ReportMaxMemoryUsedIsMeasured(t *testing.T) {
	t.Parallel()
	// The configured memory size is far above what either function reaches, so a
	// figure derived from it cannot pass for a measurement.
	const memorySize int32 = 2048
	const allocationMB = 384

	idle := lambdaInvokeForMemoryReport(t, "memory-report-idle",
		"exports.handler = async () => ({ held: 0 });\n", memorySize)

	// The module-level buffer, referenced from the handler, lives through the
	// invocation. A non-zero fill writes every page: a zero-filled allocation is an
	// untouched anonymous mapping the kernel charges to the cgroup only as pages
	// fault in.
	held := lambdaInvokeForMemoryReport(t, "memory-report-holding", fmt.Sprintf(
		"const held = Buffer.alloc(%d * 1024 * 1024, 1);\n"+
			"exports.handler = async () => ({ held: held.length });\n", allocationMB), memorySize)

	t.Logf("Max Memory Used: idle %d MB, holding %d MB (allocation %d MB)", idle, held, allocationMB)

	require.Positive(t, idle, "an execution environment that ran used memory")
	require.Positive(t, held, "an execution environment that ran used memory")

	// The holding function used at least the allocation, the idle one did not, and
	// neither exceeded its environment.
	assert.GreaterOrEqual(t, held, allocationMB,
		"an environment holding a %d MB allocation used at least that much (holding %d MB)",
		allocationMB, held)
	assert.Less(t, idle, allocationMB,
		"an environment that allocated nothing cannot reach the size of an allocation it never made "+
			"(idle %d MB)", idle)
	assert.Greater(t, held-idle, allocationMB/2,
		"the function holding %d MB must report substantially more than the one holding nothing "+
			"(idle %d MB, holding %d MB)", allocationMB, idle, held)
	assert.Less(t, held, int(memorySize),
		"a function cannot use more than the memory its environment is limited to")

	// Neither figure is the configured size or a share of it: both sit far below
	// half of it.
	assert.Less(t, idle, int(memorySize)/2,
		"the reported memory must be a measurement, not a share of the configured memory size")
	assert.Less(t, held, int(memorySize)/2,
		"the reported memory must be a measurement, not a share of the configured memory size")
}
