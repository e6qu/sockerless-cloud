package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"
)

// requireAWSErrorCode names the error an operation returns. A bare
// `require.Error(t, err)` also passes on a refused connection, a 500 from a
// panicking handler, a signature mismatch or a deserialisation failure; the
// modeled code is what an SDK maps to a typed error and callers switch on.
//
// The comparison ignores case because each protocol carries the code its own
// way: awsQuery in an XML `<Code>`, awsJson in an `__type` that may carry a
// namespace prefix, REST in the `x-amzn-ErrorType` header, and the SDK
// normalises none of them.
func requireAWSErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err, "the operation was expected to fail with %s", want)
	var apiErr smithy.APIError
	require.Truef(t, errors.As(err, &apiErr),
		"expected an API error carrying code %s, got %T: %v", want, err, err)
	require.Truef(t, strings.EqualFold(apiErr.ErrorCode(), want),
		"expected error code %s, got %s (message: %s)",
		want, apiErr.ErrorCode(), apiErr.ErrorMessage())
}
