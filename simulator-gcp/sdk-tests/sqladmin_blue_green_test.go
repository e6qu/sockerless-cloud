package gcp_sdk_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// Cloud SQL blue-green deployments, through the same authenticated transport
// the generated client uses.
//
//	POST   /v1/projects/{p}/locations/{l}/blueGreenDeployments
//	GET    /v1/projects/{p}/locations/{l}/blueGreenDeployments
//	GET    /v1/projects/{p}/locations/{l}/blueGreenDeployments/{d}
//	POST   /v1/projects/{p}/locations/{l}/blueGreenDeployments/{d}:switchover
//	DELETE /v1/projects/{p}/locations/{l}/blueGreenDeployments/{d}
//
// Google published the collection in the Discovery document ahead of the
// generated Go client, which carries no BlueGreenDeployment type at
// google.golang.org/api v0.295.0. The requests therefore go over the wire
// directly, with the same bearer the client presents — the coordinate and the
// credential are unchanged, only the typed wrapper is missing. Swap these for
// svc.Projects.Locations.BlueGreenDeployments once the client generates it.

func sqlBlueGreenCall(t *testing.T, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var payload []byte
	var reader *bytes.Reader
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, baseURL+path, reader)
	require.NoError(t, err)
	token, err := simTokenSource().Token()
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out := new(bytes.Buffer)
	_, err = out.ReadFrom(resp.Body)
	require.NoError(t, err)
	return resp, out.Bytes()
}

// seedCloudSQLInstance creates the source instance a deployment copies, through
// the generated client, so the blue-green surface acts on real instance state.
func seedCloudSQLInstance(t *testing.T, project, name string) *sqladmin.Service {
	t.Helper()
	svc, err := sqladmin.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = svc.Instances.Insert(project, &sqladmin.DatabaseInstance{
		Name:            name,
		Region:          "us-central1",
		DatabaseVersion: "POSTGRES_15",
		Settings:        &sqladmin.Settings{Tier: "db-custom-1-3840"},
	}).Do()
	require.NoError(t, err)
	return svc
}

func TestCloudSQL_BlueGreenDeploymentSwitchover(t *testing.T) {
	const project, location, source = "bg-project", "us-central1", "orders-db"
	svc := seedCloudSQLInstance(t, project, source)
	base := fmt.Sprintf("/v1/projects/%s/locations/%s/blueGreenDeployments", project, location)

	resp, body := sqlBlueGreenCall(t, http.MethodPost, base+"?blueGreenDeploymentId=upgrade-16", map[string]any{
		"sourceInstance":  source,
		"description":     "PostgreSQL 16 upgrade",
		"requestedConfig": map[string]any{"databaseVersion": "POSTGRES_16"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// The green instance is real: every other Cloud SQL read sees it, and it
	// carries the database version the request asked for.
	green, err := svc.Instances.Get(project, "upgrade-16-green").Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_16", green.DatabaseVersion)
	assert.Equal(t, "us-central1", green.Region, "the green copies the source's configuration")

	resp, body = sqlBlueGreenCall(t, http.MethodGet, base+"/upgrade-16", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var deployment struct {
		Name, SourceInstance, State, Description string
	}
	require.NoError(t, json.Unmarshal(body, &deployment))
	assert.Equal(t,
		fmt.Sprintf("projects/%s/locations/%s/blueGreenDeployments/upgrade-16", project, location),
		deployment.Name)
	assert.Equal(t, "SWITCHOVER_READY", deployment.State)
	assert.Equal(t, "PostgreSQL 16 upgrade", deployment.Description)

	resp, body = sqlBlueGreenCall(t, http.MethodGet, base, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var listed struct {
		BlueGreenDeployments []struct{ Name string } `json:"blueGreenDeployments"`
	}
	require.NoError(t, json.Unmarshal(body, &listed))
	require.Len(t, listed.BlueGreenDeployments, 1)

	// Switchover promotes the green into the source's name.
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base+"/upgrade-16:switchover", map[string]any{})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	promoted, err := svc.Instances.Get(project, source).Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_16", promoted.DatabaseVersion,
		"the source name now serves the green instance")

	// The instance the switchover replaced is retired, not destroyed.
	retired, err := svc.Instances.Get(project, source+"-old").Do()
	require.NoError(t, err)
	assert.Equal(t, "POSTGRES_15", retired.DatabaseVersion)

	// The green name no longer resolves: it became the source.
	_, err = svc.Instances.Get(project, "upgrade-16-green").Do()
	require.Error(t, err)

	resp, body = sqlBlueGreenCall(t, http.MethodGet, base+"/upgrade-16", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NoError(t, json.Unmarshal(body, &deployment))
	assert.Equal(t, "SWITCHOVER_COMPLETED", deployment.State)

	// deleteOldSource retires the replaced instance with the deployment.
	resp, body = sqlBlueGreenCall(t, http.MethodDelete, base+"/upgrade-16?deleteOldSource=true", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	_, err = svc.Instances.Get(project, source+"-old").Do()
	require.Error(t, err)
	// The promoted instance survives the deployment that produced it.
	_, err = svc.Instances.Get(project, source).Do()
	require.NoError(t, err)
}

func TestCloudSQL_BlueGreenDeploymentReportsWhatIsWrong(t *testing.T) {
	const project, location = "bg-errors", "us-central1"
	seedCloudSQLInstance(t, project, "present-db")
	base := fmt.Sprintf("/v1/projects/%s/locations/%s/blueGreenDeployments", project, location)

	// A source instance that does not exist.
	resp, body := sqlBlueGreenCall(t, http.MethodPost, base+"?blueGreenDeploymentId=d1",
		map[string]any{"sourceInstance": "absent-db"})
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))

	// A missing deployment id.
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base,
		map[string]any{"sourceInstance": "present-db"})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	assert.Contains(t, string(body), "blueGreenDeploymentId")

	// The same id twice.
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base+"?blueGreenDeploymentId=d2",
		map[string]any{"sourceInstance": "present-db"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base+"?blueGreenDeploymentId=d2",
		map[string]any{"sourceInstance": "present-db"})
	require.Equal(t, http.StatusConflict, resp.StatusCode, string(body))

	// Switching over twice: the second call has nothing ready to switch.
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base+"/d2:switchover", map[string]any{})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	resp, body = sqlBlueGreenCall(t, http.MethodPost, base+"/d2:switchover", map[string]any{})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
	assert.Contains(t, string(body), "SWITCHOVER_READY")

	// A deployment that does not exist.
	resp, body = sqlBlueGreenCall(t, http.MethodGet, base+"/absent", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, string(body))
}
