package aws_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudWatch_MetricsCBORErrorShape expects a malformed GetMetricData or
// PutMetricData body to fail in the rpc-v2-cbor error shape with the
// `Smithy-Protocol: rpc-v2-cbor` header; the Go SDK rejects an awsJson error
// as "unexpected smithy-protocol response header".
func TestCloudWatch_MetricsCBORErrorShape(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"GetMetricData", "PutMetricData"} {
		url := baseURL + "/service/GraniteServiceVersion20100801/operation/" + op
		req, err := http.NewRequest("POST", url, strings.NewReader("not-valid-cbor"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/cbor")
		req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.GreaterOrEqual(t, resp.StatusCode, 400, "%s: malformed body must error", op)
		assert.Equal(t, "rpc-v2-cbor", resp.Header.Get("Smithy-Protocol"),
			"%s: cbor error must carry the Smithy-Protocol header", op)
	}
}
