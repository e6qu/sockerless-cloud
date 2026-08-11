package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// Call-time credential enforcement for Lambda's REST control plane
// (registerLambda + registerLambdaExtras2/3), mirroring s3Enforced. Real AWS
// Lambda requires a valid SigV4 signature on every control-plane request
// (CreateFunction, ListFunctions, Invoke, …) and evaluates the caller's IAM
// policy for the corresponding lambda: action before dispatching. This does
// NOT apply to the Lambda Runtime API (/2018-06-01/runtime/...): those routes
// are served by a separate, per-invocation runtimeAPISidecar mux
// (lambda_runtime.go) that the function CONTAINER polls as its execution
// bootstrap — they are never registered on srv/registerLambda and carry no
// SigV4 signature by design, so they are untouched by this file.

// lambdaEnforced wraps a Lambda REST handler for a single, statically-known
// operation name with SigV4 authentication + IAM authorization, mirroring
// s3Enforced's contract: enforcement is a no-op for unregistered/test
// credentials (the permissive default from iamEnforceREST), so existing
// SDK/CLI/Terraform Lambda tests using the bootstrap "test"/"test" credential
// keep working — only a caller with a registered restricted key is blocked.
// resource resolves the request's target function ARN when the route names
// one; nil means the operation is account/global-scoped and IAM evaluates it
// against "*".
func lambdaEnforced(op string, resource func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	return lambdaEnforcedDynamic(func(*http.Request) string { return op }, resource, h)
}

// lambdaEnforcedDynamic is lambdaEnforced for the two routes whose operation
// name depends on a query selector rather than the path alone (GET
// /2018-10-31/layers serves ListLayers or GetLayerVersionByArn; GET
// .../provisioned-concurrency serves GetProvisionedConcurrencyConfig or
// ListProvisionedConcurrencyConfigs), so the correct lambda: action is
// resolved per request instead of once at registration time.
func lambdaEnforcedDynamic(opFn func(*http.Request) string, resource func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Buffer the body so its real SHA-256 can be recomputed for the
		// canonical request. Unlike S3 (whose clients always declare the
		// payload hash in x-amz-content-sha256, so the handler never needs to
		// read the body), the Lambda SDK's REST-JSON protocol does not set
		// that header — the signer hashes the actual payload in-memory — so
		// the gate must hash the real body itself or every signed
		// CreateFunction/UpdateFunctionCode/etc. call would fail to verify.
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		// Authenticate the SigV4 signature first. Unlike S3 (which permits
		// anonymous access for public bucket/object policies), Lambda's
		// control plane has no anonymous surface: every operation requires a
		// signed request, so an absent credential is rejected the same as an
		// invalid one.
		res, serr := sigv4Verify(r, body)
		if serr != nil {
			lambdaWriteSigv4Error(w, serr)
			return
		}
		if res == sigv4NoCredential {
			lambdaWriteMissingAuth(w)
			return
		}
		action := lambdaIAMAction(opFn(r))
		resARN := "*"
		if resource != nil {
			if v := resource(r); v != "" {
				resARN = v
			}
		}
		if !iamEnforceREST(w, r, action, resARN, lambdaWriteIAMDeny) {
			return
		}
		h(w, r)
	}
}

// lambdaIAMAction maps a Lambda REST API operation name to its documented IAM
// action. Every operation's action is "lambda:" + the operation name except
// the invoke family, which the AWS documentation lists as all requiring
// lambda:InvokeFunction regardless of which invoke variant (synchronous,
// legacy async, or response-streaming) the caller used.
func lambdaIAMAction(op string) string {
	switch op {
	case "Invoke", "InvokeAsync", "InvokeWithResponseStream":
		return "lambda:InvokeFunction"
	default:
		return "lambda:" + op
	}
}

// lambdaFunctionResourceARN resolves the target function ARN from a REST
// route's {name} or {arn} path parameter (the tags routes key on {arn...}
// where every other function-scoped route keys on {name}), for IAM
// resource-scoped evaluation. Returns "" when the route names no function —
// lambdaEnforced then evaluates against "*", the conservative default that
// never over-denies an account/global-scoped operation.
func lambdaFunctionResourceARN(r *http.Request) string {
	if arn := sim.PathParam(r, "arn"); arn != "" {
		return arn
	}
	name := sim.PathParam(r, "name")
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "arn:") {
		return name
	}
	return lambdaArn(name)
}

// lambdaProvisionedConcurrencyOpName resolves GET
// /2019-09-30/functions/{name}/provisioned-concurrency to
// GetProvisionedConcurrencyConfig or, when the SDK's ?List=ALL selector is
// present, ListProvisionedConcurrencyConfigs — the two ops the shared handler
// (lambda_extras2.go) dispatches between.
func lambdaProvisionedConcurrencyOpName(r *http.Request) string {
	if r.URL.Query().Get("List") == "ALL" {
		return "ListProvisionedConcurrencyConfigs"
	}
	return "GetProvisionedConcurrencyConfig"
}

// lambdaLayersOpName resolves GET /2018-10-31/layers to ListLayers or, when
// the SDK's ?find=LayerVersion selector is present, GetLayerVersionByArn —
// the two ops the shared handler (lambda_extras.go) dispatches between.
func lambdaLayersOpName(r *http.Request) string {
	return lambdaLayersEventName(r, nil)
}

// lambdaWriteAuthError renders an authentication or authorization failure in
// Lambda's restJson1 error shape: the x-amzn-Errortype header the AWS SDKs
// read to classify the exception (also what CloudTrail's error-code
// extraction reads, so denied calls are recorded with the real error code),
// plus the __type/message JSON body every other Lambda error in this package
// already returns via sim.AWSError.
func lambdaWriteAuthError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("X-Amzn-Errortype", code)
	sim.AWSError(w, code, message, status)
}

// lambdaWriteSigv4Error renders a SigV4 authentication failure. It reuses the
// same JSON error-code mapping the control-plane (POST /) gate uses for
// awsJson services, since Lambda's REST API is JSON-protocol like they are —
// only the transport (path routing vs. X-Amz-Target) differs.
func lambdaWriteSigv4Error(w http.ResponseWriter, serr *sigv4Error) {
	jsonCode, _ := sigv4ErrorCodes(serr.kind)
	lambdaWriteAuthError(w, jsonCode, serr.message, http.StatusForbidden)
}

func lambdaWriteMissingAuth(w http.ResponseWriter) {
	lambdaWriteAuthError(w, "MissingAuthenticationTokenException",
		"Missing Authentication Token", http.StatusForbidden)
}

func lambdaWriteIAMDeny(w http.ResponseWriter, r *http.Request, principalArn, action string) {
	lambdaWriteAuthError(w, "AccessDeniedException",
		"User: "+principalArn+" is not authorized to perform: "+action+
			" because no identity-based policy allows the "+action+" action",
		http.StatusForbidden)
}
