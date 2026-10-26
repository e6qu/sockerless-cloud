package gcp_sdk_test

import (
	"crypto/x509"

	"cloud.google.com/go/bigtable"
	"cloud.google.com/go/spanner"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/oauth"

	"github.com/e6qu/sockerless-cloud/testutil/loopbacktls"
)

// grpcTLS is the certificate TestMain serves the simulator's gRPC port with,
// and grpcRoots the authority a client trusts it through. Google's client
// libraries send an access token only over TLS, as they do to
// *.googleapis.com:443.
var (
	grpcTLS   loopbacktls.Files
	grpcRoots *x509.CertPool
)

// simGRPCEnv is the environment a simulator process serves its gRPC port over
// TLS with.
func simGRPCEnv() []string {
	return []string{"SIM_GCP_GRPC_TLS_CERT=" + grpcTLS.Cert, "SIM_GCP_GRPC_TLS_KEY=" + grpcTLS.Key}
}

// simGRPCTransport trusts the gRPC port's certificate, the one coordinate a
// TLS client swaps for the public roots it trusts Google with.
func simGRPCTransport() grpc.DialOption {
	return grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(grpcRoots, ""))
}

// simGRPCDialOptions are the options of a raw gRPC connection that presents
// the access tokens ts mints on every call, as a connection a client library
// dials carries its credentials.
func simGRPCDialOptions(ts oauth2.TokenSource) []grpc.DialOption {
	return []grpc.DialOption{simGRPCTransport(), grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: ts})}
}

// simGRPCClientOptions point a client library at the simulator's gRPC port
// with the bootstrap service account's credential.
func simGRPCClientOptions() []option.ClientOption {
	return simGRPCClientOptionsAt(grpcAddr, simTokenSource())
}

// simGRPCClientOptionsAt is simGRPCClientOptions for a dedicated simulator
// process, whose own key signs the tokens it verifies.
func simGRPCClientOptionsAt(addr string, ts oauth2.TokenSource) []option.ClientOption {
	return []option.ClientOption{
		option.WithEndpoint(addr),
		option.WithTokenSource(ts),
		option.WithGRPCDialOption(simGRPCTransport()),
	}
}

// The simulator serves no Cloud Monitoring, so the Bigtable and Spanner
// clients export none of their built-in metrics.
var (
	bigtableClientConfig = bigtable.ClientConfig{MetricsProvider: bigtable.NoopMetricsProvider{}}
	spannerClientConfig  = spanner.ClientConfig{DisableNativeMetrics: true}
)
