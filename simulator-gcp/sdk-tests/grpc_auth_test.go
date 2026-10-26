package gcp_sdk_test

import (
	"errors"
	"testing"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/googleapis/gax-go/v2/apierror"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/e6qu/sockerless-cloud/testutil/gcpbootstrap"
)

const (
	grpcMissingCredential = "Request is missing required authentication credential. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
	grpcInvalidCredential = "Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
)

// TestGRPC_RefusesACallWithoutACredential sends a Secret Manager call over a
// TLS connection that carries no credential, as Google's front end receives
// one from a client configured with option.WithoutAuthentication.
func TestGRPC_RefusesACallWithoutACredential(t *testing.T) {
	t.Parallel()
	cli, err := secretmanager.NewClient(ctx,
		option.WithEndpoint(grpcAddr),
		option.WithoutAuthentication(),
		option.WithGRPCDialOption(simGRPCTransport()))
	require.NoError(t, err)
	defer cli.Close()

	_, err = cli.ListSecrets(ctx, &smpb.ListSecretsRequest{Parent: "projects/" + harnessProject}).Next()
	require.Equal(t, codes.Unauthenticated, status.Code(err), "ListSecrets without a credential: %v", err)
	var apiErr *apierror.APIError
	require.True(t, errors.As(err, &apiErr), "the client surfaces an APIError: %v", err)
	require.Equal(t, grpcMissingCredential, apiErr.GRPCStatus().Message())
	require.Equal(t, "CREDENTIALS_MISSING", apiErr.Reason())
	require.Equal(t, "googleapis.com", apiErr.Domain())
	require.Equal(t, "secretmanager.googleapis.com", apiErr.Metadata()["service"])
	require.Equal(t, "google.cloud.secretmanager.v1.SecretManagerService.ListSecrets", apiErr.Metadata()["method"])
}

// TestGRPC_RefusesAnUnverifiableCredential presents bearer tokens the
// simulator did not mint: an opaque string and an unsigned JWT.
func TestGRPC_RefusesAnUnverifiableCredential(t *testing.T) {
	t.Parallel()
	for name, ts := range map[string]oauth2.TokenSource{
		"opaque":        oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "ya29.not-a-token"}),
		"forged header": oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0."}),
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(ts)...)
			require.NoError(t, err)
			defer conn.Close()
			_, err = smpb.NewSecretManagerServiceClient(conn).ListSecrets(ctx,
				&smpb.ListSecretsRequest{Parent: "projects/" + harnessProject})
			st, _ := status.FromError(err)
			require.Equal(t, codes.Unauthenticated, st.Code(), "ListSecrets with a %s token: %v", name, err)
			require.Equal(t, grpcInvalidCredential, st.Message())
		})
	}
}

// TestGRPC_RefusesAStreamWithoutACredential opens a Bigtable ReadRows server
// stream with no credential; the refusal arrives on the stream.
func TestGRPC_RefusesAStreamWithoutACredential(t *testing.T) {
	t.Parallel()
	conn, err := grpc.NewClient(grpcAddr, simGRPCTransport())
	require.NoError(t, err)
	defer conn.Close()
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, "/google.bigtable.v2.Bigtable/ReadRows")
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	err = stream.RecvMsg(new(smpb.Secret))
	require.Equal(t, codes.Unauthenticated, status.Code(err), "ReadRows without a credential: %v", err)
}

// TestGRPC_ServesTheBootstrapCredential lists secrets through the client
// library configured as against Google: an endpoint and a credential.
func TestGRPC_ServesTheBootstrapCredential(t *testing.T) {
	t.Parallel()
	cli, err := secretmanager.NewClient(ctx, simGRPCClientOptions()...)
	require.NoError(t, err)
	defer cli.Close()
	_, err = cli.ListSecrets(ctx, &smpb.ListSecretsRequest{Parent: "projects/" + harnessProject}).Next()
	if !errors.Is(err, iterator.Done) {
		require.NoError(t, err)
	}
}

// TestSelfSignedJWT_ServesRESTAndGRPC authenticates with the bootstrap key the
// way Google's client libraries do without an OAuth token exchange: a JWT the
// service account signs itself, carrying the API's service URL as audience or
// the requested scope in its place.
func TestSelfSignedJWT_ServesRESTAndGRPC(t *testing.T) {
	t.Parallel()
	key := gcpbootstrap.CredentialJSON(baseURL + "/token")
	scoped, err := google.JWTAccessTokenSourceWithScope(key, "https://www.googleapis.com/auth/cloud-platform")
	require.NoError(t, err)
	audienced, err := google.JWTAccessTokenSourceFromJSON(key, "https://run.googleapis.com/")
	require.NoError(t, err)

	for name, ts := range map[string]oauth2.TokenSource{"scope": scoped, "audience": audienced} {
		t.Run(name, func(t *testing.T) {
			jobs, err := run.NewJobsRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(ts))
			require.NoError(t, err)
			defer jobs.Close()
			_, err = jobs.ListJobs(ctx, &runpb.ListJobsRequest{Parent: "projects/" + harnessProject + "/locations/us-central1"}).Next()
			if !errors.Is(err, iterator.Done) {
				require.NoError(t, err, "Cloud Run jobs.list with a self-signed JWT")
			}

			secrets, err := secretmanager.NewClient(ctx, simGRPCClientOptionsAt(grpcAddr, ts)...)
			require.NoError(t, err)
			defer secrets.Close()
			_, err = secrets.ListSecrets(ctx, &smpb.ListSecretsRequest{Parent: "projects/" + harnessProject}).Next()
			if !errors.Is(err, iterator.Done) {
				require.NoError(t, err, "Secret Manager ListSecrets over gRPC with a self-signed JWT")
			}
		})
	}
}
