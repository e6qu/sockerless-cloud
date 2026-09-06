package gcp_sdk_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A build's docker steps pull from Artifact Registry as the build's service
// account, the way Cloud Build's docker builder does through its credential
// helper: a Dockerfile whose base image lives in a repository the registry
// refuses anonymously builds, and the built image pushes into the registry
// under the same credential.
func TestCloudBuild_DockerStepsPullAndPushAsTheBuildServiceAccount(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform gate: the container engine pulls the base image itself, and on a host whose engine runs inside its own virtual machine it has no route to this host's loopback, where the simulator's registry listens. Linux hosts — and the repository's Linux container path, `make docker-test` — share one loopback between engine, simulator and client.")
	}
	const (
		project  = "cb-credential-project"
		location = "us-central1"
	)
	arCreateRepository(t, project, location, "docker-hub")
	arCreateRepository(t, project, location, "sockerless-overlay")

	pullImageWithRetry(t, "public.ecr.aws/docker/library/alpine:3.20")
	_, err := dockerCLIWithTimeout(60*time.Second, "tag", "public.ecr.aws/docker/library/alpine:3.20", "alpine:3.20")
	require.NoError(t, err)

	registry := strings.TrimPrefix(baseURL, "http://")
	base := fmt.Sprintf("%s/%s/docker-hub/library/alpine:3.20", registry, project)
	_, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", base)
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", base) })

	// The registry refuses the anonymous pull a step without the credential
	// would make.
	resp, body := arRawDo(t, http.MethodGet, fmt.Sprintf("%s/v2/%s/docker-hub/library/alpine/manifests/3.20", baseURL, project), "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)

	built := fmt.Sprintf("%s/%s/sockerless-overlay/cloudrun:cred-%d", registry, project, time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerCLIWithTimeout(60*time.Second, "image", "rm", "-f", built) })

	bucket := "cb-credential-bucket"
	createBucket(t, project, bucket)
	objectName := fmt.Sprintf("cb-credential-%d.tar.gz", time.Now().UnixNano())
	uploadGCSObject(t, bucket, objectName, makeTarGz(t, map[string]string{
		"Dockerfile": "FROM " + base + "\nRUN echo pulled-as-the-build-service-account > /opt/payload\n",
	}))

	buildCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	reqBody := fmt.Sprintf(`{
		"source":{"storageSource":{"bucket":%q,"object":%q}},
		"steps":[
			{"name":"gcr.io/cloud-builders/docker","args":["build","-t",%q,"."]},
			{"name":"gcr.io/cloud-builders/docker","args":["push",%q]}
		],
		"images":[%q]
	}`, bucket, objectName, built, built, built)
	req, _ := http.NewRequestWithContext(buildCtx, http.MethodPost, fmt.Sprintf("%s/v1/projects/%s/builds", baseURL, project), strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	bresp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer bresp.Body.Close()
	out, _ := io.ReadAll(bresp.Body)
	require.Contains(t, string(out), `"status":"SUCCESS"`, "build must pull its base image and push its result as the build's service account: %s", string(out))

	// The pushed image is in the registry, which still refuses the anonymous
	// read of it.
	tag := built[strings.LastIndex(built, ":")+1:]
	resp, body = arRawDo(t, http.MethodGet, fmt.Sprintf("%s/v2/%s/sockerless-overlay/cloudrun/manifests/%s", baseURL, project, tag), "", nil, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, body)
}
