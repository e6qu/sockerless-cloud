package azure_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureStorageDataPlaneStateSurvivesSimulatorRestart_SDK covers the state
// the Files and Queues data planes keep beyond a share's bytes — directory
// metadata, share snapshots, leases, stored security descriptors, queue access
// policies and the service configuration. None of it is expressible in a POSIX
// directory tree, so all of it lives in the simulator's persistent stores; a
// restart that reattaches the same data directory must answer exactly as the
// process that wrote it did.
func TestAzureStorageDataPlaneStateSurvivesSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	port := freeAzureSimPort(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := startPersistentAzureSimulator(t, stateDir, port)
	t.Cleanup(func() { _ = shutdownAzureSimulator(cmd) })

	const (
		filesHost  = "persiststorage.file.localhost"
		queuesHost = "persiststorage.queue.localhost"
		share      = "persist-state-share"
		queue      = "persist-state-queue"
	)
	payload := []byte("bytes in a nested directory")

	// ---- Files: a nested directory carrying metadata, holding a file ----
	resp := restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share", filesHost, "", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"/build?restype=directory", filesHost, "", nil,
		map[string]string{"x-ms-meta-stage": "compile"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"/build/bundle.tar", filesHost, "", nil, map[string]string{
		"x-ms-type":           "file",
		"x-ms-content-length": fmt.Sprintf("%d", len(payload)),
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"/build/bundle.tar?comp=range", filesHost, "", payload,
		map[string]string{"x-ms-write": "update", "x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ---- Files: a snapshot, a stored permission and a share lease ----
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share&comp=snapshot", filesHost, "", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	snapshot := resp.Header.Get("x-ms-snapshot")
	resp.Body.Close()
	require.NotEmpty(t, snapshot)

	const sddl = "O:S-1-5-21-9G:S-1-5-21-9D:(A;;FA;;;SY)"
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share&comp=filepermission", filesHost, "",
		[]byte(`{"permission":"`+sddl+`"}`), map[string]string{"Content-Type": "application/json"})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	permissionKey := resp.Header.Get("x-ms-file-permission-key")
	resp.Body.Close()
	require.NotEmpty(t, permissionKey)

	const leaseID = "22222222-3333-4444-5555-666666666666"
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share&comp=lease", filesHost, "", nil,
		map[string]string{"x-ms-lease-action": "acquire", "x-ms-proposed-lease-id": leaseID})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// ---- Queues: a stored access policy and the service configuration ----
	resp = restartRawReq(t, "PUT", endpoint, "/"+queue, queuesHost, "", nil, nil)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = restartRawReq(t, "PUT", endpoint, "/"+queue+"?comp=acl", queuesHost, "",
		[]byte(`<SignedIdentifiers><SignedIdentifier><Id>persisted</Id><AccessPolicy>`+
			`<Permission>rap</Permission></AccessPolicy></SignedIdentifier></SignedIdentifiers>`), nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	resp = restartRawReq(t, "PUT", endpoint, "/?restype=service&comp=properties", queuesHost, "",
		[]byte(`<StorageServiceProperties><Logging><Version>1.0</Version><Delete>true</Delete>`+
			`<Read>true</Read><Write>true</Write><RetentionPolicy><Enabled>true</Enabled>`+
			`<Days>11</Days></RetentionPolicy></Logging></StorageServiceProperties>`), nil)
	require.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// ---- restart ----
	require.NoError(t, shutdownAzureSimulator(cmd))
	cmd = startPersistentAzureSimulator(t, stateDir, port)
	t.Cleanup(func() { _ = shutdownAzureSimulator(cmd) })

	// The nested directory is still a directory, and still carries its metadata.
	resp = restartRawReq(t, "GET", endpoint, "/"+share+"/build?restype=directory", filesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "a nested directory must survive the restart")
	assert.Equal(t, "compile", resp.Header.Get("x-ms-meta-stage"),
		"directory metadata must survive the restart")
	resp.Body.Close()

	// The file inside it reads back, and its bytes are on disk beside the
	// SQLite database that describes them.
	resp = restartRawReq(t, "GET", endpoint, "/"+share+"/build/bundle.tar", filesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, payload, body)
	onDisk, err := os.ReadFile(filepath.Join(stateDir, "files", "persiststorage", share, "build", "bundle.tar"))
	require.NoError(t, err, "a nested file's bytes must live under SIM_DATA_DIR/files")
	assert.Equal(t, payload, onDisk)

	// The snapshot still answers with the bytes it froze.
	resp = restartRawReq(t, "GET", endpoint,
		"/"+share+"/build/bundle.tar?sharesnapshot="+snapshot, filesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "a share snapshot must survive the restart")
	snapshotBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, payload, snapshotBody)

	// The stored security descriptor is still resolvable by its key.
	resp = restartRawReq(t, "GET", endpoint, "/"+share+"?restype=share&comp=filepermission", filesHost, "", nil,
		map[string]string{"x-ms-file-permission-key": permissionKey})
	require.Equal(t, http.StatusOK, resp.StatusCode, "a stored permission must survive the restart")
	permissionBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, string(permissionBody), sddl)

	// The share is still leased, so a write without the lease id is still
	// refused and the holder's write still goes through.
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share&comp=metadata", filesHost, "", nil,
		map[string]string{"x-ms-meta-owner": "intruder"})
	require.Equal(t, http.StatusPreconditionFailed, resp.StatusCode,
		"a share lease must still lock the share after the restart")
	resp.Body.Close()
	resp = restartRawReq(t, "PUT", endpoint, "/"+share+"?restype=share&comp=metadata", filesHost, "", nil,
		map[string]string{"x-ms-meta-owner": "sockerless", "x-ms-lease-id": leaseID})
	require.Equal(t, http.StatusOK, resp.StatusCode, "the lease holder must still be able to write")
	resp.Body.Close()

	// The queue's stored access policy and the service configuration read back.
	resp = restartRawReq(t, "GET", endpoint, "/"+queue+"?comp=acl", queuesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	aclBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Contains(t, string(aclBody), "<Id>persisted</Id>",
		"a queue's stored access policy must survive the restart")

	resp = restartRawReq(t, "GET", endpoint, "/?restype=service&comp=properties", queuesHost, "", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	propsBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	assert.True(t, strings.Contains(string(propsBody), "<Days>11</Days>"),
		"the Queue service configuration must survive the restart: %s", propsBody)
}
