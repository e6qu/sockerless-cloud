package gcp_sdk_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Cloud Run front end removes the X-Google-* headers a client sends,
// appends the client's IP to X-Forwarded-For, names the client's protocol in
// X-Forwarded-Proto, and adds an X-Cloud-Trace-Context to a request that
// carries none while passing on one that does.
func TestSDK_CloudRunV2Services_ForwardedRequestHeaders(t *testing.T) {
	t.Parallel()
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-xff", &runpb.Service{
		Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{{
			Image: httpProbeImageName, Args: []string{"echo-headers"},
		}}},
	})
	token := invokerIDToken(t, svc.Uri)

	got := cloudRunEchoedHeaders(t, svc.Uri, http.Header{
		"Authorization":   {"Bearer " + token},
		"X-Forwarded-For": {"203.0.113.7"},
		"X-Google-Secret": {"spoofed"},
	})
	assertCloudRunForwardedFor(t, got.Get("X-Forwarded-For"), "203.0.113.7")
	assert.Equal(t, mustParseURL(t, baseURL).Scheme, got.Get("X-Forwarded-Proto"))
	assert.Empty(t, got.Values("X-Google-Secret"))
	assert.Regexp(t, `^[0-9a-f]{32}/[0-9]+;o=[01]$`, got.Get("X-Cloud-Trace-Context"))

	const trace = "105445aa7843bc8bf206b12000100000/1;o=1"
	got = cloudRunEchoedHeaders(t, svc.Uri, http.Header{
		"Authorization":         {"Bearer " + token},
		"X-Cloud-Trace-Context": {trace},
	})
	assertCloudRunForwardedFor(t, got.Get("X-Forwarded-For"))
	assert.Equal(t, trace, got.Get("X-Cloud-Trace-Context"))
}

func cloudRunEchoedHeaders(t *testing.T, uri string, header http.Header) http.Header {
	t.Helper()
	answer := invokeServiceRaw(t, uri, "/", header)
	require.Equal(t, http.StatusOK, answer.status, "body=%q", answer.body)
	var echoed http.Header
	require.NoError(t, json.Unmarshal([]byte(answer.body), &echoed), answer.body)
	return echoed
}

// assertCloudRunForwardedFor requires the client-supplied values followed by
// the loopback address the test connected from.
func assertCloudRunForwardedFor(t *testing.T, got string, supplied ...string) {
	t.Helper()
	parts := strings.Split(got, ",")
	require.Len(t, parts, len(supplied)+1, "X-Forwarded-For %q", got)
	if len(supplied) > 0 {
		assert.Equal(t, supplied, parts[:len(supplied)])
	}
	ip := net.ParseIP(parts[len(supplied)])
	require.NotNil(t, ip, "X-Forwarded-For %q", got)
	assert.True(t, ip.IsLoopback(), "X-Forwarded-For %q", got)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}
