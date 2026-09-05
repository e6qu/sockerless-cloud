package sim

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The point of the registry is to name a request that is still hanging, at the
// moment it hangs. A snapshot that only lists finished work would be useless
// for the case it exists to diagnose.
func TestInFlightMiddlewareNamesARequestWhileItIsStillRunning(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})

	handler := InFlightMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))

	req := httptest.NewRequest("POST", "/?Action=CreateSnapshot", nil)
	req.Header.Set("X-Amz-Target", "AmazonEC2.CreateSnapshot")
	go handler.ServeHTTP(httptest.NewRecorder(), req)

	<-entered
	snapshot := InFlightSnapshot()
	require.Len(t, snapshot, 1, "a request being served must appear in the registry")
	require.Equal(t, "POST", snapshot[0].Method)
	require.Equal(t, "AmazonEC2.CreateSnapshot", snapshot[0].Target,
		"the operation must be named: the path alone does not say what AWS was asked to do")

	close(release)
	require.Eventually(t, func() bool { return len(InFlightSnapshot()) == 0 },
		5*time.Second, 10*time.Millisecond, "a finished request must leave the registry")
}

// A form-encoded EC2 call carries its operation in Action rather than a header.
func TestRequestOperationReadsTheFormAction(t *testing.T) {
	req := httptest.NewRequest("POST", "/?Action=DescribeSnapshots", nil)
	require.Equal(t, "DescribeSnapshots", requestOperation(req))
}
