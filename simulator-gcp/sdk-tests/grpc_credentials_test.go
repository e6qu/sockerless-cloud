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

// TestMain serves the gRPC port with grpcTLS, which grpcRoots trusts: Google's
// client libraries send an access token only over TLS, as to
// *.googleapis.com:443.
var (
	grpcTLS   loopbacktls.Files
	grpcRoots *x509.CertPool
)

func simGRPCEnv() []string {
	return []string{"SIM_GCP_GRPC_TLS_CERT=" + grpcTLS.Cert, "SIM_GCP_GRPC_TLS_KEY=" + grpcTLS.Key}
}

// simGRPCTransport trusts the gRPC port's certificate, the one coordinate a
// TLS client swaps for the public roots it trusts Google with.
func simGRPCTransport() grpc.DialOption {
	return grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(grpcRoots, ""))
}

// simGRPCDialOptions present the access tokens ts mints on every call, as a
// client library's connection does.
func simGRPCDialOptions(ts oauth2.TokenSource) []grpc.DialOption {
	return []grpc.DialOption{simGRPCTransport(), grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: ts})}
}

func simGRPCClientOptions() []option.ClientOption {
	return simGRPCClientOptionsAt(grpcAddr, simTokenSource())
}

// simGRPCClientOptionsAt targets a dedicated simulator process, whose own key
// signs the tokens it verifies.
func simGRPCClientOptionsAt(addr string, ts oauth2.TokenSource) []option.ClientOption {
	return []option.ClientOption{
		option.WithEndpoint(addr),
		option.WithTokenSource(ts),
		option.WithGRPCDialOption(simGRPCTransport()),
	}
}

// The simulator serves no Cloud Monitoring, so the Bigtable and Spanner
// clients export no built-in metrics.
var (
	bigtableClientConfig = bigtable.ClientConfig{MetricsProvider: bigtable.NoopMetricsProvider{}}
	spannerClientConfig  = spanner.ClientConfig{DisableNativeMetrics: true}
)
