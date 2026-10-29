package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// The IAM gate decides every Google Cloud API call before the service serves
// it, as Google's front ends do: it resolves the call to the method it
// reaches, derives the permissions the method requires and the resource each
// is checked on, and admits the caller only when the policies on that
// resource and its ancestors grant them all. A refused call answers 403
// PERMISSION_DENIED in the API's own error shape, and gRPC PERMISSION_DENIED.

// gateCall is a call the gate decides, in the REST form a client would send:
// a gRPC call is decided as the REST call its binding spells.
type gateCall struct {
	httpMethod string
	path       string
	host       string
	query      url.Values
	// fields are the request's own fields: a REST call's JSON body, a gRPC
	// call's message. A REST body is read only when a check needs it.
	fields func() map[string]any
	// rpc is the RPC a gRPC call invoked, nil for a REST call.
	rpc *auditRPC
}

// gateCheck is one permission the caller must hold on one resource.
type gateCheck struct {
	permission string
	// resource is the relative name of the resource the permission is
	// checked on.
	resource string
	// bucket and object name the Cloud Storage resource the permission is
	// checked on, through its bucket's and object's own policies and ACLs.
	bucket, object string
	// unsourced marks a method no sourced table gives a permission for: only
	// an Owner holds it, since Owner holds every permission.
	unsourced bool
}

// gateDecision is what a call requires. A call whose path two APIs publish,
// with nothing to tell them apart, is admitted on either API's requirement.
type gateDecision struct {
	api     string
	host    string
	methods []string
	checks  [][]gateCheck
}

// iamGateMiddleware decides each REST call the authentication layer admitted.
func iamGateMiddleware(srv *sim.Server) func(http.Handler) http.Handler {
	mux := srv.Mux()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !gateDecides(mux, r) {
				next.ServeHTTP(w, r)
				return
			}
			decision, ok := gateResolve(gateRESTCall(r))
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			principal := gcpRequestPrincipal(r)
			if failed, denied := gateAuthorize(principal, decision); denied {
				gateWriteDenied(w, principal, decision, failed)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// gateDecides reports whether a request is a Google API method call the gate
// decides: the authenticated surface bearerAuthMiddleware verifies, less the
// host-addressed data planes that decide their own callers.
func gateDecides(mux *http.ServeMux, r *http.Request) bool {
	if isAuthExempt(r) || !publishesAPIMethod(mux, r) {
		return false
	}
	hostname := lbplane.Hostname(r.Host)
	return !isCloudRunHost(r.Host) && !strings.HasSuffix(hostname, cloudFunctionsDomain)
}

// gateBodyLimit bounds the request body the gate reads for the fields a
// check names.
const gateBodyLimit = 1 << 20

func gateRESTCall(r *http.Request) *gateCall {
	call := &gateCall{httpMethod: r.Method, path: r.URL.EscapedPath(), host: r.Host, query: r.URL.Query()}
	var fields map[string]any
	read := false
	call.fields = func() map[string]any {
		if read {
			return fields
		}
		read = true
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Body == nil || (mediaType != "" && mediaType != "application/json") {
			return nil
		}
		original := r.Body
		head, err := io.ReadAll(io.LimitReader(original, gateBodyLimit+1))
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), original), original}
		if err != nil || len(head) > gateBodyLimit {
			return nil
		}
		_ = json.Unmarshal(head, &fields)
		return fields
	}
	return call
}

// gateResolve resolves a call to what it requires. A call no Discovery
// method or RPC describes is not a Google API method and is left to its
// handler.
func gateResolve(call *gateCall) (gateDecision, bool) {
	r := &http.Request{Method: call.httpMethod, Host: call.host, URL: &url.URL{Path: call.path, RawQuery: call.query.Encode()}}
	if unescaped, err := url.PathUnescape(call.path); err == nil {
		r.URL.Path = unescaped
		r.URL.RawPath = call.path
	}
	candidates := gateMatchDiscovery(call.httpMethod, call.path)
	if len(candidates) == 0 {
		if call.rpc == nil {
			return gateDecision{}, false
		}
		return gateRPCDecision(call), true
	}
	if chosen, ok := gateChooseDiscovery(r, candidates); ok {
		candidates = []gateDiscoveryMatch{chosen}
	}
	decision := gateDecision{api: candidates[0].method.api.name, host: candidates[0].method.api.host}
	for _, c := range candidates {
		decision.methods = append(decision.methods, c.method.id)
		decision.checks = append(decision.checks, gateChecks(call, c))
	}
	return decision, true
}

// gateRPCDecision decides an RPC no Discovery document describes, such as
// Cloud Bigtable's data plane, from the RPC itself.
func gateRPCDecision(call *gateCall) gateDecision {
	rpc := call.rpc
	request := call.fields()
	resource := rpc.resourceName(request)
	check := gateCheck{resource: resource}
	if p, ok := gcpCanonicalPermission(rpc.permission(request, resource)); ok {
		check.permission = p
	} else {
		check.unsourced = true
	}
	api := strings.TrimSuffix(rpc.serviceName, ".googleapis.com")
	return gateDecision{api: api, host: rpc.serviceName, methods: []string{string(rpc.method.FullName())}, checks: [][]gateCheck{{check}}}
}

// gateAuthorize evaluates a decision for the principal: it admits the call
// when every check of any alternative holds, and otherwise names the first
// check of the first alternative that failed.
func gateAuthorize(principal string, d gateDecision) (gateCheck, bool) {
	var failed gateCheck
	for i, checks := range d.checks {
		ok := true
		for _, check := range checks {
			if gateAnswersUnknownProject[d.api] && gateUnknownProject(check.resource) {
				continue
			}
			if !gateHolds(principal, check) {
				if i == 0 {
					failed = check
				}
				ok = false
				break
			}
		}
		if ok {
			return gateCheck{}, false
		}
	}
	return failed, true
}

// gateAnswersUnknownProject are the APIs that resolve the project a call
// names before they authorize it, and answer one that does not exist with
// their own error: Compute Engine's and Pub/Sub's 404, Cloud Storage's 400.
var gateAnswersUnknownProject = map[string]bool{"compute": true, "storage": true, "pubsub": true}

func gateUnknownProject(resource string) bool {
	project := resourceProject(resource)
	if project == "" || project == "_" {
		return false
	}
	_, ok := crmResolveProject(project)
	return !ok
}

func gateHolds(principal string, check gateCheck) bool {
	if check.bucket != "" {
		// Bucket names are global: Cloud Storage answers a bucket that does
		// not exist with 404 whoever asks.
		if _, ok := gcsBuckets.Get(check.bucket); !ok {
			return true
		}
	}
	policies, resource := gatePolicies(check)
	if check.unsourced {
		return gcpPrincipalHoldsRole(principal, gcpRoleOwner, policies, resource, time.Now())
	}
	return len(gcpPermissionsHeldUnder(principal, policies, []string{check.permission}, resource)) == 1
}

// gatePolicies are the policies that govern a check's resource, outermost
// first, and the resource as a binding's condition sees it.
func gatePolicies(check gateCheck) ([]IAMPolicy, gcpIAMResource) {
	if check.bucket != "" {
		if check.object != "" {
			return gcsObjectPolicies(check.bucket, check.object),
				gcsIAMResource(check.bucket, "projects/_/buckets/"+check.bucket+"/objects/"+check.object, "storage.googleapis.com/Object")
		}
		return gcsBucketPolicies(check.bucket),
			gcsIAMResource(check.bucket, "projects/_/buckets/"+check.bucket, "storage.googleapis.com/Bucket")
	}
	return gcpPolicyChain(check.resource), gcpIAMResourceOf(check.resource)
}

// gateWriteDenied answers a refused REST call in the API's own shape.
func gateWriteDenied(w http.ResponseWriter, principal string, d gateDecision, failed gateCheck) {
	if failed.unsourced {
		GCPError(w, http.StatusForbidden, "The caller does not have permission", "PERMISSION_DENIED")
		return
	}
	message := gateDeniedMessage(principal, d, failed)
	switch d.api {
	case "storage", "compute":
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":    http.StatusForbidden,
			"message": message,
			"errors":  []map[string]string{{"message": message, "domain": "global", "reason": "forbidden"}},
		}})
	case "bigquery":
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":    http.StatusForbidden,
			"message": message,
			"errors":  []map[string]string{{"message": message, "domain": "global", "reason": "accessDenied"}},
			"status":  "PERMISSION_DENIED",
		}})
	case "pubsub", "spanner", "firestore", "cloudresourcemanager":
		GCPError(w, http.StatusForbidden, message, "PERMISSION_DENIED")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code":    http.StatusForbidden,
			"message": message,
			"status":  "PERMISSION_DENIED",
			"details": []any{gateErrorInfo(failed)},
		}})
	}
}

func gateErrorInfo(failed gateCheck) map[string]any {
	return map[string]any{
		"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
		"reason": "IAM_PERMISSION_DENIED",
		"domain": "iam.googleapis.com",
		"metadata": map[string]string{
			"permission": failed.permission,
			"resource":   failed.resource,
		},
	}
}

// gateDeniedMessage is the message each API refuses a call with.
func gateDeniedMessage(principal string, d gateDecision, failed gateCheck) string {
	switch d.api {
	case "storage":
		caller := strings.TrimPrefix(principal, "serviceAccount:")
		kind := "Google Cloud Storage bucket"
		switch {
		case failed.object != "":
			kind = "Google Cloud Storage object"
		case failed.bucket == "":
			kind = "Google Cloud project"
		}
		return caller + " does not have " + failed.permission + " access to the " + kind + ". Permission '" +
			failed.permission + "' denied on resource (or it may not exist)."
	case "compute":
		return "Required '" + failed.permission + "' permission for '" + failed.resource + "'"
	case "pubsub":
		return "User not authorized to perform this action."
	case "spanner":
		return "Caller is missing IAM permission " + failed.permission + " on resource " + failed.resource + "."
	case "firestore":
		return "Missing or insufficient permissions."
	case "cloudresourcemanager":
		return "The caller does not have permission"
	case "bigquery":
		return gateBigQueryDeniedMessage(failed)
	}
	return "Permission '" + failed.permission + "' denied on resource '" + failed.resource + "' (or it may not exist)."
}

// gateBigQueryDeniedMessage names the BigQuery resource the way BigQuery
// spells it, project:dataset.table.
func gateBigQueryDeniedMessage(failed gateCheck) string {
	project := resourceProject(failed.resource)
	dataset := auditPathSegmentAfter(failed.resource, "datasets")
	if dataset == "" {
		return "Access Denied: Project " + project + ": User does not have " + failed.permission +
			" permission in project " + project + "."
	}
	kind, name := "Dataset", project+":"+dataset
	if table := auditPathSegmentAfter(failed.resource, "tables"); table != "" {
		kind, name = "Table", name+"."+table
	}
	return "Access Denied: " + kind + " " + name + ": Permission " + failed.permission + " denied on " +
		strings.ToLower(kind) + " " + name + " (or it may not exist)."
}

// The gRPC side of the gate: a unary call is decided on its request, a
// streaming call on the first message it receives.

func iamGateUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := gateDecideGRPC(ctx, info.FullMethod, req); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func iamGateStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, &gateServerStream{ServerStream: ss, method: info.FullMethod})
}

type gateServerStream struct {
	grpc.ServerStream
	method  string
	decided bool
}

func (s *gateServerStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	if s.decided {
		return nil
	}
	s.decided = true
	return gateDecideGRPC(s.Context(), s.method, m)
}

func gateDecideGRPC(ctx context.Context, fullMethod string, req any) error {
	idx, err := gateRPCs()
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	name := strings.ReplaceAll(strings.TrimPrefix(fullMethod, "/"), "/", ".")
	message, ok := req.(proto.Message)
	if !ok {
		return nil
	}
	request, ok := auditMessageJSON(message)
	if !ok {
		return nil
	}
	call := &gateCall{query: url.Values{}, fields: func() map[string]any { return request }}
	rpc, known := idx.byName[name]
	switch {
	case gateStreamingREST[name] != nil:
		call.httpMethod, call.path, ok = gateStreamingREST[name](request)
	case known:
		call.rpc = rpc
		call.httpMethod, call.path, ok = gateRESTOf(rpc, request)
	default:
		ok = false
	}
	var decision gateDecision
	switch {
	case ok:
		decision, ok = gateResolve(call)
	case known:
		decision, ok = gateRPCDecision(call), true
	}
	if !ok {
		decision = gateDecision{methods: []string{name}, checks: [][]gateCheck{{{unsourced: true, resource: gateRequestResource(request)}}}}
	}
	principal := gcpContextPrincipal(ctx)
	failed, denied := gateAuthorize(principal, decision)
	if !denied {
		return nil
	}
	if failed.unsourced {
		return status.Error(codes.PermissionDenied, "The caller does not have permission")
	}
	st := status.New(codes.PermissionDenied, gateDeniedMessage(principal, decision, failed))
	if decision.api != "pubsub" && decision.api != "spanner" && decision.api != "firestore" {
		if detailed, err := st.WithDetails(gateErrorInfoProto(failed)); err == nil {
			st = detailed
		}
	}
	return st.Err()
}

// gateRequestResource is the resource a request without a binding names in
// one of the fields Google's RPCs name it by.
func gateRequestResource(request map[string]any) string {
	for _, field := range []string{"name", "resource", "parent"} {
		if v, _ := request[field].(string); v != "" {
			return v
		}
	}
	return ""
}
