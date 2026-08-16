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

// The Amazon ECR private-registry credential contract, proven with a real
// registry client doing a real push and a real pull.
//
// The client is go-containerregistry, the registry client behind crane, ko and
// kaniko. It speaks the Docker Registry HTTP API v2 the way a Docker engine
// does — it probes /v2/, follows the WWW-Authenticate challenge, and replays
// the Basic credential a `docker login` would have stored — which is why the
// refusal wording this file asserts is the wording that client reports against
// real Amazon ECR: `unexpected status code 401 Unauthorized: Not Authorized`
// (google/go-containerregistry#861).
//
// It runs in the test process rather than in a container engine so that this
// coverage holds on every host. A container engine living in its own virtual
// machine — Podman on macOS — cannot dial the simulator's listener on this
// host's loopback address, so an engine-driven test cannot run there at all.
// An in-process client reaches the simulator's coordinate everywhere and
// exercises the same credential and the same wire exchange.
//
// The engine path is covered too, and separately: `TestECRCLI_DockerLoginPushPull`
// drives a real `docker login`, push and pull against the registry served over
// TLS by the HTTPS gateway, which is what makes the engine's own login endpoint
// workable. That test carries the platform gate this one does not need.

// ecrRegistryAuthenticator returns the credential a Docker client holds after
// `aws ecr get-login-password | docker login --username AWS --password-stdin`:
// the user `AWS` and the password half of a real GetAuthorizationToken response.
func ecrRegistryAuthenticator(t *testing.T) authn.Authenticator {
	t.Helper()
	return &authn.Basic{Username: "AWS", Password: ecrRegistryPassword(t)}
}

// ecrRegistryTag builds the reference a client pushes to, on the registry
// coordinate the simulator serves.
func ecrRegistryTag(t *testing.T, repo, tag string) name.Tag {
	t.Helper()
	ref, err := name.NewTag(fmt.Sprintf("%s/%s:%s",
		strings.TrimPrefix(baseURL, "http://"), repo, tag), name.Insecure)
	require.NoError(t, err)
	return ref
}

// TestECR_RegistryClientPushPullWithAuthorizationToken drives the whole
// documented flow with a real registry client: the credential Amazon ECR's
// control plane mints authenticates a push and the pull that follows it, while
// a client with no credential, a wrong password, a user other than AWS, or a
// credential it stopped presenting is refused.
func TestECR_RegistryClientPushPullWithAuthorizationToken(t *testing.T) {
	const repo = "ecr-registry-client/app"
	client := ecrClient()
	ecrCreateRepository(t, repo)

	image, err := random.Image(2048, 3)
	require.NoError(t, err)
	wantDigest, err := image.Digest()
	require.NoError(t, err)

	ref := ecrRegistryTag(t, repo, fmt.Sprintf("v%d", time.Now().UnixNano()))

	// An anonymous client cannot push. The client's first move is a manifest
	// HEAD, so the refusal it reports is the status alone; the body is asserted
	// on the pull below, where the client issues a GET.
	err = remote.Write(ref, image, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	require.Error(t, err, "an unauthenticated push must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	// Neither can one holding the wrong password.
	err = remote.Write(ref, image, remote.WithContext(ctx),
		remote.WithAuth(&authn.Basic{Username: "AWS", Password: "not-the-password"}))
	require.Error(t, err, "a push with the wrong password must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	// Nor one presenting the right password under another user.
	err = remote.Write(ref, image, remote.WithContext(ctx),
		remote.WithAuth(&authn.Basic{Username: "root", Password: ecrRegistryPassword(t)}))
	require.Error(t, err, "a push as a user other than AWS must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")

	// The authorization token does push.
	require.NoError(t, remote.Write(ref, image,
		remote.WithContext(ctx), remote.WithAuth(ecrRegistryAuthenticator(t))))

	// The ECR control plane sees what the data plane accepted.
	described, err := client.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
	})
	require.NoError(t, err)
	require.NotEmpty(t, described.ImageDetails)

	// And an independently issued token pulls the same bytes back.
	pulled, err := remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(ecrRegistryAuthenticator(t)))
	require.NoError(t, err)
	gotDigest, err := pulled.Digest()
	require.NoError(t, err)
	assert.Equal(t, wantDigest, gotDigest, "the pulled image must be the pushed one")
	layers, err := pulled.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 3)

	// Dropping the credential — what `docker logout` does — takes the pull away
	// again, so nothing the push stored is readable without one.
	_, err = remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous))
	require.Error(t, err, "a pull without a credential must be refused")
	assert.Contains(t, err.Error(), "401 Unauthorized")
	assert.Contains(t, err.Error(), "Not Authorized")
}
