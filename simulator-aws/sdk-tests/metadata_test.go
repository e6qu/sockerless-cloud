package aws_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS host-metadata services.
//
// IMDSv2 (PUT /latest/api/token + GET /latest/meta-data/...) and ECS
// task metadata v4 (/v4/{id}/task) served on the sim's main listener.
// Workloads in the sim's Docker hosts reach these endpoints via env
// vars (AWS_EC2_METADATA_SERVICE_ENDPOINT, ECS_CONTAINER_METADATA_URI_V4)
// pointing at host.docker.internal:<sim-port>.

func imdsToken(t *testing.T) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "PUT", baseURL+"/latest/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	tok := strings.TrimSpace(string(body))
	require.NotEmpty(t, tok)
	return tok
}

func imdsRead(t *testing.T, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+path, nil)
	req.Header.Set("X-aws-ec2-metadata-token", token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func TestIMDS_TokenRequired(t *testing.T) {
	resp, _ := imdsRead(t, "/latest/meta-data/instance-id", "")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIMDS_UnknownTokenRejected(t *testing.T) {
	resp, _ := imdsRead(t, "/latest/meta-data/instance-id", "AQbogus")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIMDS_InstanceID(t *testing.T) {
	tok := imdsToken(t)
	resp, body := imdsRead(t, "/latest/meta-data/instance-id", tok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(string(body), "i-"), "expected i-* prefix, got %q", string(body))
}

func TestIMDS_PlacementRegion(t *testing.T) {
	tok := imdsToken(t)
	resp, body := imdsRead(t, "/latest/meta-data/placement/region", tok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.NotEmpty(t, string(body))
}

func TestIMDS_IAMSecurityCredentials(t *testing.T) {
	tok := imdsToken(t)

	resp, body := imdsRead(t, "/latest/meta-data/iam/security-credentials/", tok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	role := strings.TrimSpace(string(body))
	require.NotEmpty(t, role)

	resp, body = imdsRead(t, "/latest/meta-data/iam/security-credentials/"+role, tok)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var creds struct {
		Code            string
		AccessKeyId     string
		SecretAccessKey string
		Token           string
		Expiration      string
	}
	require.NoError(t, json.Unmarshal(body, &creds))
	assert.Equal(t, "Success", creds.Code)
	assert.NotEmpty(t, creds.AccessKeyId)
	assert.NotEmpty(t, creds.SecretAccessKey)
	assert.NotEmpty(t, creds.Token)
	assert.NotEmpty(t, creds.Expiration)
}

func TestECSTaskMetadataV4(t *testing.T) {
	resp, err := http.Get(baseURL + "/v4/abc123/task")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
