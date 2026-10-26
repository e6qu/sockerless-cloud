package aws_sdk_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Amazon ECR private-registry credential contract, pushed and pulled with
// go-containerregistry, the registry client behind crane, ko and kaniko. It
// speaks the Docker Registry HTTP API v2 as a Docker engine does (probe /v2/,
// follow WWW-Authenticate, replay the Basic credential `docker login` stores),
// so its refusal wording matches what it reports against Amazon ECR:
// `unexpected status code 401 Unauthorized: Not Authorized`
// (google/go-containerregistry#861).
//
// It runs in the test process so the coverage holds on every host: an engine in
// its own virtual machine (Podman on macOS) cannot dial the simulator on this
// host's loopback. `TestECRCLI_DockerLoginPushPull` covers the engine path,
// with its platform gate, against the registry behind the HTTPS gateway.

// ecrRegistryAuthenticator returns what a Docker client holds after
// `aws ecr get-login-password | docker login --username AWS --password-stdin`:
// the user `AWS` and a GetAuthorizationToken response's password half.
func ecrRegistryAuthenticator(t *testing.T) authn.Authenticator {
	t.Helper()
	return &authn.Basic{Username: "AWS", Password: ecrRegistryPassword(t)}
}

func ecrRegistryTag(t *testing.T, repo, tag string) name.Tag {
	t.Helper()
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s",
		strings.TrimPrefix(baseURL, "http://"), repo, tag), name.Insecure)
	require.NoError(t, err)
	return ref
}

// TestECR_RegistryClientPushPullWithAuthorizationToken expects the control
// plane's credential to authenticate a push and the pull after it, while no
// credential, a wrong password, a user other than AWS, or a credential no
// longer presented is refused.
func TestECR_RegistryClientPushPullWithAuthorizationToken(t *testing.T) {
	t.Parallel()
	const repo = "ecr-registry-client/app"
	client := ecrClient()
	ecrCreateRepository(t, repo)

	image, err := random.Image(2048, 3)
	require.NoError(t, err)
	wantDigest, err := image.Digest()
	require.NoError(t, err)

	ref := ecrRegistryTag(t, repo, fmt.Sprintf("v%d", time.Now().UnixNano()))

	// The client starts a push with a manifest HEAD, so its refusal carries the
	// status alone; the pull below, a GET, carries the body.
	err = remote.Write(ref, image, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	require.Error(t, err, "an unauthenticated push must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	err = remote.Write(ref, image, remote.WithContext(ctx),
		remote.WithAuth(&authn.Basic{Username: "AWS", Password: "not-the-password"}))
	require.Error(t, err, "a push with the wrong password must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	err = remote.Write(ref, image, remote.WithContext(ctx),
		remote.WithAuth(&authn.Basic{Username: "root", Password: ecrRegistryPassword(t)}))
	require.Error(t, err, "a push as a user other than AWS must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	require.NoError(t, remote.Write(ref, image,
		remote.WithContext(ctx), remote.WithAuth(ecrRegistryAuthenticator(t))))

	described, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	require.NotEmpty(t, described.ImageDetails)

	pulled, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(ecrRegistryAuthenticator(t)))
	require.NoError(t, err)
	gotDigest, err := pulled.Digest()
	require.NoError(t, err)
	assert.Equal(t, wantDigest, gotDigest, "the pulled image must be the pushed one")
	layers, err := pulled.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 3)

	// Dropping the credential, as `docker logout` does, takes the pull away.
	_, err = remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	require.Error(t, err, "a pull without a credential must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")
	assert.Contains(t, err.Error(), "Not Authorized")
}
