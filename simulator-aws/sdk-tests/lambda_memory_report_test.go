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

// The REPORT entry closing an AWS Lambda invocation states what the execution
// environment consumed:
//
//	REPORT RequestId: … Duration: … ms Billed Duration: … ms Memory Size: 2048 MB
//	Max Memory Used: 466 MB
//
// Memory Size is the function's configuration; Max Memory Used is a
// measurement, and the two are independent — two functions configured with the
// same memory size report whatever each of them actually used. This drives that
// difference: one function holds a large allocation for the whole invocation,
// the other holds nothing, and their reports must differ by roughly the
// allocation. A figure derived from the configured memory size could not.

// lambdaReportMemoryMegabytes reads `Max Memory Used: <n> MB` out of a REPORT
// entry.
func lambdaReportMemoryMegabytes(t *testing.T, report string) int {
	t.Helper()
	match := regexp.MustCompile(`\tMax Memory Used: ([0-9]+) MB`).FindStringSubmatch(report)
	require.Len(t, match, 2, "REPORT must carry Max Memory Used:\n%s", report)
	value, err := strconv.Atoi(match[1])
	require.NoError(t, err)
	return value
}

// lambdaInvokeForMemoryReport creates a Node function with the given
// module-level source, invokes it once, and returns the memory its REPORT
// entry reported.
func lambdaInvokeForMemoryReport(t *testing.T, name, source string, memorySize int32) int {
	t.Helper()
	lc := lambdaClient()
	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
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

// TestLambda_ReportMaxMemoryUsedIsMeasured proves the reported figure comes
// from the execution environment rather than from the function's configuration:
// two functions with an identical memory size, one holding a large allocation
// for the whole invocation and one holding nothing, report memory that differs
// by about that allocation.
func TestLambda_ReportMaxMemoryUsedIsMeasured(t *testing.T) {
	// The configured memory size is far above what either function can reach,
	// so a figure derived from it — the configured size, or a share of it —
	// cannot be mistaken for either measurement.
	const memorySize int32 = 2048
	const allocationMB = 384

	idle := lambdaInvokeForMemoryReport(t, "memory-report-idle",
		"exports.handler = async () => ({ held: 0 });\n", memorySize)

	// The buffer is allocated at module level and referenced from the handler,
	// so it is live for the whole invocation. It is filled with a non-zero byte
	// so every page is written: a large zero-filled allocation is served by an
	// untouched anonymous mapping, which the kernel charges to the cgroup only
	// as its pages are faulted in.
	held := lambdaInvokeForMemoryReport(t, "memory-report-holding", fmt.Sprintf(
		"const held = Buffer.alloc(%d * 1024 * 1024, 1);\n"+
			"exports.handler = async () => ({ held: held.length });\n", allocationMB), memorySize)

	t.Logf("Max Memory Used: idle %d MB, holding %d MB (allocation %d MB)", idle, held, allocationMB)

	require.Positive(t, idle, "an execution environment that ran used memory")
	require.Positive(t, held, "an execution environment that ran used memory")

	// What each figure must be, from what each environment did: the one holding
	// the allocation used at least the allocation, the one holding nothing did
	// not, and neither used more than its environment allows.
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

	// Neither figure is the configured memory size, nor a share of it: both sit
	// far below the half a configuration-derived number would produce.
	assert.Less(t, idle, int(memorySize)/2,
		"the reported memory must be a measurement, not a share of the configured memory size")
	assert.Less(t, held, int(memorySize)/2,
		"the reported memory must be a measurement, not a share of the configured memory size")
}
