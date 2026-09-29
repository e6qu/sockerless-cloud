package gcp_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"
	logging "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
)

func requireInvalidArgument(t *testing.T, err error) {
	t.Helper()
	var gerr *googleapi.Error
	require.Truef(t, errors.As(err, &gerr), "expected a Google API error, got %v", err)
	require.Equal(t, http.StatusBadRequest, gerr.Code, "message: %s", gerr.Message)
}

// Google Cloud answers a list filter its grammar does not admit with
// INVALID_ARGUMENT rather than listing everything.
func TestSecretManagerListRejectsMalformedFilter(t *testing.T) {
	svc := secretManagerService(t)
	_, err := svc.Projects.Secrets.List("projects/sdk-filter-project").Filter(`labels.env="dev`).Do()
	requireInvalidArgument(t, err)
}

func TestLoggingEntriesListRejectsMalformedQueryAndToken(t *testing.T) {
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = svc.Entries.List(&logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/sdk-filter-project"},
		Filter:        `(severity>=WARNING`,
	}).Do()
	requireInvalidArgument(t, err)
	_, err = svc.Entries.List(&logging.ListLogEntriesRequest{
		ResourceNames: []string{"projects/sdk-filter-project"},
		PageToken:     "not-a-token",
	}).Do()
	requireInvalidArgument(t, err)
}
