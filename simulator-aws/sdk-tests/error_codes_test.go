package aws_sdk_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/require"
)

// An error-path assertion has to name the error the operation returns.
//
// `require.Error(t, err)` on an AWS call is satisfied by a connection refused,
// a 500 from a panicking handler, a signature mismatch and a deserialisation
// failure — none of which shows the service refused anything. Several of these
// assertions were left over from before their operation was implemented and
// passed for the whole time it did not exist. Asserting the modeled code is
// what makes the test fail when the simulator answers with the wrong refusal,
// which is the failure worth catching: a real SDK maps the code to a typed
// error, and callers switch on it.
//
// The comparison is case-insensitive because a code travels differently on
// different protocols — the awsQuery services spell it in an XML `<Code>`
// element, awsJson in an `__type` that may carry a namespace prefix, and REST
// services in the `x-amzn-ErrorType` header — and the SDK normalises none of
// them for the caller.
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
