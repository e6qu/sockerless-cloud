package azure_sdk_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/stretchr/testify/require"
)

// TestACR_BlobUploadRejectsWrongDigest covers the monolithic blob
// upload completion must reject a content/digest mismatch with DIGEST_INVALID
// (400) instead of storing the bytes under the asserted (wrong) digest.
func TestACR_BlobUploadRejectsWrongDigest(t *testing.T) {
	const repo = "audit-blob-repo"
	body := []byte("audit-blob-content")
	fixture := newACRRegistryFixture(t, "acr-auth-rg", "acrblobdigestregistry",
		&armcontainerregistry.RegistryProperties{AdminUserEnabled: to.Ptr(true)})
	auth := fixture.basic(fixture.passwords[0])

	initUpload := func() string {
		resp := acrDo(t, http.MethodPost, fixture.endpoint()+"/v2/"+repo+"/blobs/uploads/", nil, "", auth)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		loc := resp.Header.Get("Location")
		require.NotEmpty(t, loc, "blob upload init must return a Location")
		return loc
	}

	putBlob := func(loc, digest string) int {
		resp := acrDo(t, http.MethodPut, fixture.endpoint()+loc+"?digest="+digest, body, "application/octet-stream", auth)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// Wrong digest (valid format, wrong value) → 400.
	wrong := "sha256:" + fmt.Sprintf("%064d", 0)
	require.Equal(t, http.StatusBadRequest, putBlob(initUpload(), wrong),
		"a content/digest mismatch must be rejected with DIGEST_INVALID")

	// Correct digest → 201 Created.
	correct := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	require.Equal(t, http.StatusCreated, putBlob(initUpload(), correct))
}
