package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAzureARMRequiresAPIVersion(t *testing.T) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups",
		nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, "InvalidApiVersionParameter", out.Error.Code)
	assert.Contains(t, out.Error.Message, "api-version query parameter")
}

func TestAzureARMListResponsesUseEmptyArrays(t *testing.T) {
	rg := "sdk-empty-list-rg"
	ensureRG(t, rg)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/containerApps?api-version=2024-03-01",
		nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", simARMBearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.Contains(t, strings.TrimSpace(string(body)), `"value":[]`)
}
