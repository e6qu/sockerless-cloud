package gcp_sdk_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"cloud.google.com/go/compute/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// metadataDefaultAccount is the account a metadata read from outside every
// workload runs as: the Compute Engine default service account of the
// simulator's default project, sockerless, whose number is 123456789012.
const metadataDefaultAccount = "123456789012-compute@developer.gserviceaccount.com"

// SDK-driven metadata client tests. Validates our handlers match what
// cloud.google.com/go/compute/metadata expects (header casing, JSON
// shape, error semantics). Raw-HTTP coverage lives in metadata_test.go;
// this file proves the real GCP SDK client accepts our responses
// end-to-end (external-validation principle).
//
// metadata.NewClient lets us point the SDK at the sim's listener.
func gcpMetadataClient(t *testing.T) *metadata.Client {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	// metadata.NewWithOptions takes a transport-aware shape; the simpler
	// path is GCE_METADATA_HOST env which the package honours.
	t.Setenv("GCE_METADATA_HOST", u.Host)
	t.Setenv("GCE_METADATA_IP", u.Host)
	return metadata.NewClient(nil)
}

func TestMetadataSDK_ProjectID(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.ProjectIDWithContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sockerless", got)

	number, err := c.NumericProjectIDWithContext(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "123456789012", number, "the number Cloud Resource Manager holds for the project")
}

func TestMetadataSDK_Email(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.EmailWithContext(context.Background(), "default")
	require.NoError(t, err)
	assert.Equal(t, metadataDefaultAccount, got)
}

func TestMetadataSDK_Zone(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.ZoneWithContext(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, got)
	// SDK strips the projects/<p>/zones/ prefix and returns just the
	// zone name.
	assert.False(t, strings.HasPrefix(got, "projects/"),
		"SDK should strip the projects/.../zones/ prefix; got %q", got)
}

func TestMetadataSDK_InstanceName(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.InstanceNameWithContext(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, got)
}

func TestMetadataSDK_Hostname(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.HostnameWithContext(context.Background())
	require.NoError(t, err)
	assert.Contains(t, got, ".internal",
		"SDK Hostname() expects the GCE-style FQDN; got %q", got)
}

func TestMetadataSDK_GenericGet(t *testing.T) {
	c := gcpMetadataClient(t)
	got, err := c.GetWithContext(context.Background(),
		"instance/service-accounts/default/scopes")
	require.NoError(t, err)
	assert.Contains(t, got, "cloud-platform")
}
