package main

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	pspb "cloud.google.com/go/pubsub/apiv1/pubsubpb"
	smpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"golang.org/x/oauth2"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/e6qu/sockerless-cloud/sim"
)

// grpcAuthTestServer builds the simulator and serves its gRPC services on a
// plaintext loopback listener, returning a connection that carries no
// credential of its own.
func grpcAuthTestServer(t *testing.T) (*sim.Server, *grpc.Server, *grpc.ClientConn) {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)

	gs := newGRPCServer()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go gs.Serve(lis)
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return srv, gs, conn
}

func withBearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func requireUnauthenticated(t *testing.T, err error, message string) *status.Status {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unauthenticated {
		t.Fatalf("got %v, want code Unauthenticated", err)
	}
	if st.Message() != message {
		t.Fatalf("message %q, want %q", st.Message(), message)
	}
	return st
}

func TestGRPCRefusesACallWithoutACredential(t *testing.T) {
	_, _, conn := grpcAuthTestServer(t)
	_, err := smpb.NewSecretManagerServiceClient(conn).ListSecrets(context.Background(),
		&smpb.ListSecretsRequest{Parent: "projects/" + gcpTestProjectID})
	st := requireUnauthenticated(t, err, gcpMissingCredentialMessage)
	var info *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if e, ok := d.(*errdetails.ErrorInfo); ok {
			info = e
		}
	}
	if info == nil {
		t.Fatalf("no ErrorInfo detail in %v", st.Details())
	}
	if info.Reason != "CREDENTIALS_MISSING" || info.Domain != "googleapis.com" ||
		info.Metadata["service"] != "secretmanager.googleapis.com" ||
		info.Metadata["method"] != "google.cloud.secretmanager.v1.SecretManagerService.ListSecrets" {
		t.Fatalf("ErrorInfo = %v", info)
	}
}

func TestGRPCRefusesAnUnverifiableCredential(t *testing.T) {
	_, _, conn := grpcAuthTestServer(t)
	client := smpb.NewSecretManagerServiceClient(conn)
	req := &smpb.ListSecretsRequest{Parent: "projects/" + gcpTestProjectID}
	now := time.Now()
	for name, token := range map[string]string{
		"not a JWT": "ya29.not-a-token",
		"expired":   signAccessToken(gcpBootstrapServiceAccountEmail, now.Add(-2*time.Hour), now.Add(-time.Hour)),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.ListSecrets(withBearer(context.Background(), token), req)
			requireUnauthenticated(t, err, gcpInvalidCredentialMessage)
		})
	}
	t.Run("not a bearer", func(t *testing.T) {
		ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Basic dXNlcjpwYXNz")
		_, err := client.ListSecrets(ctx, req)
		requireUnauthenticated(t, err, gcpMissingCredentialMessage)
	})
}

func TestGRPCRefusesAStreamWithoutACredential(t *testing.T) {
	_, _, conn := grpcAuthTestServer(t)
	stream, err := pspb.NewSubscriberClient(conn).StreamingPull(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pspb.StreamingPullRequest{
		Subscription:             "projects/" + gcpTestProjectID + "/subscriptions/none",
		StreamAckDeadlineSeconds: 10,
	}); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	requireUnauthenticated(t, err, gcpMissingCredentialMessage)
}

func TestGRPCServesACallWithAMintedToken(t *testing.T) {
	_, _, conn := grpcAuthTestServer(t)
	now := time.Now()
	token := signAccessToken(gcpBootstrapServiceAccountEmail, now, now.Add(time.Hour))
	if _, err := smpb.NewSecretManagerServiceClient(conn).ListSecrets(withBearer(context.Background(), token),
		&smpb.ListSecretsRequest{Parent: "projects/" + gcpTestProjectID}); err != nil {
		t.Fatalf("ListSecrets with a minted token: %v", err)
	}
}

// TestGRPCTLSFrontEndServesBothProtocols drives the front end the port runs
// with a certificate: a gRPC call with per-RPC OAuth credentials, which
// gRPC sends only over TLS, and a REST request on the same address.
func TestGRPCTLSFrontEndServesBothProtocols(t *testing.T) {
	srv, gs, _ := grpcAuthTestServer(t)
	ts := httptest.NewUnstartedServer(grpcFrontEnd(srv, gs))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)

	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	now := time.Now()
	token := signAccessToken(gcpBootstrapServiceAccountEmail, now, now.Add(time.Hour))
	conn, err := grpc.NewClient(ts.Listener.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewClientTLSFromCert(roots, "example.com")),
		grpc.WithPerRPCCredentials(oauth.TokenSource{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})}))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := smpb.NewSecretManagerServiceClient(conn).ListSecrets(context.Background(),
		&smpb.ListSecretsRequest{Parent: "projects/" + gcpTestProjectID}); err != nil {
		t.Fatalf("ListSecrets over TLS: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/projects/"+gcpTestProjectID+"/secrets", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("REST secrets.list on the front end: %d", resp.StatusCode)
	}
}
