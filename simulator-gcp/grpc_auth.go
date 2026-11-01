package main

import (
	"context"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Google's front end answers a call without a credential, and one whose
// credential it cannot verify, with these UNAUTHENTICATED descriptions, over
// REST (specs/cloud-api/gcp/googleapis-cors.http.gz records the first) and
// gRPC alike.
const (
	gcpMissingCredentialMessage = "Request is missing required authentication credential. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
	gcpInvalidCredentialMessage = "Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project."
)

// grpcServiceHosts maps a gRPC method's proto package to the API host that
// serves it, the service an ErrorInfo names.
var grpcServiceHosts = []struct{ pkg, host string }{
	{"google.bigtable.admin.", "bigtableadmin.googleapis.com"},
	{"google.bigtable.", "bigtable.googleapis.com"},
	{"google.cloud.kms.", "cloudkms.googleapis.com"},
	{"google.cloud.secretmanager.", "secretmanager.googleapis.com"},
	{"google.firestore.", "firestore.googleapis.com"},
	{"google.logging.", "logging.googleapis.com"},
	{"google.pubsub.", "pubsub.googleapis.com"},
	{"google.spanner.", "spanner.googleapis.com"},
}

func authUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := verifyCallCredential(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func authStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := verifyCallCredential(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

// verifyCallCredential checks the call's authorization metadata against the
// key the simulator issued its access tokens with, as bearerAuthMiddleware
// checks a REST request's Authorization header.
func verifyCallCredential(ctx context.Context, fullMethod string) error {
	var authorization string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("authorization"); len(values) > 0 {
			authorization = strings.TrimSpace(values[0])
		}
	}
	const prefix = "bearer "
	if !strings.HasPrefix(strings.ToLower(authorization), prefix) {
		return grpcUnauthenticated(gcpMissingCredentialMessage, "CREDENTIALS_MISSING", fullMethod)
	}
	var services []string
	if host := grpcServiceHost(fullMethod); host != "" {
		services = []string{host}
	}
	if _, err := verifiedAccessTokenClaimsFor(strings.TrimSpace(authorization[len(prefix):]), services); err != nil {
		return grpcUnauthenticated(gcpInvalidCredentialMessage, gcpCredentialReason(err), fullMethod)
	}
	return nil
}

// grpcUnauthenticated is the UNAUTHENTICATED status with the ErrorInfo detail
// Google attaches, naming the API host and the method the call reached.
func grpcUnauthenticated(message, reason, fullMethod string) error {
	method := strings.ReplaceAll(strings.TrimPrefix(fullMethod, "/"), "/", ".")
	info := &errdetails.ErrorInfo{Reason: reason, Domain: "googleapis.com", Metadata: map[string]string{"method": method}}
	if host := grpcServiceHost(fullMethod); host != "" {
		info.Metadata["service"] = host
	}
	st, err := status.New(codes.Unauthenticated, message).WithDetails(info)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return st.Err()
}

// grpcServiceHost is the API host serving a gRPC method ("/pkg.Service/Method").
func grpcServiceHost(fullMethod string) string {
	method := strings.TrimPrefix(fullMethod, "/")
	for _, s := range grpcServiceHosts {
		if strings.HasPrefix(method, s.pkg) {
			return s.host
		}
	}
	return ""
}
