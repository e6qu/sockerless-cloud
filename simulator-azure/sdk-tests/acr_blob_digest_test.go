package azure_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestACR_BlobUploadRejectsWrongDigest covers the monolithic blob
// upload completion must reject a content/digest mismatch with DIGEST_INVALID
// (400) instead of storing the bytes under the asserted (wrong) digest.
func TestACR_BlobUploadRejectsWrongDigest(t *testing.T) {
	const repo = "audit-blob-repo"
	body := []byte("audit-blob-content")

	initUpload := func() string {
		resp, err := http.Post(baseURL+"/v2/"+repo+"/blobs/uploads/", "", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		loc := resp.Header.Get("Location")
		require.NotEmpty(t, loc, "blob upload init must return a Location")
		return loc
	}

	putBlob := func(loc, digest string) int {
		req, err := http.NewRequest(http.MethodPut, baseURL+loc+"?digest="+digest, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
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
