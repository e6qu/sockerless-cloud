package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Lambda's REST control plane requires a SigV4 signature on every request and
// evaluates the caller's IAM policy for the lambda: action. The Lambda Runtime
// API (/2018-06-01/runtime/...) lives on a per-invocation sidecar mux the
// function container polls, unsigned by design, and never passes through here.

// lambdaEnforced wraps a Lambda REST handler for one operation with SigV4
// authentication and IAM authorization. An unregistered credential is allowed,
// as iamEnforceREST allows it. resource resolves the target function ARN; nil
// evaluates the operation against "*".
func lambdaEnforced(op string, resource func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	return lambdaEnforcedDynamic(func(*http.Request) string { return op }, resource, h)
}

// lambdaEnforcedDynamic resolves the operation per request for the routes a
// query selector splits: GET /2018-10-31/layers (ListLayers or
// GetLayerVersionByArn) and GET .../provisioned-concurrency (Get or List).
func lambdaEnforcedDynamic(opFn func(*http.Request) string, resource func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Hash the buffered body for the canonical request: unlike Amazon S3 clients,
		// the Lambda SDK sends no x-amz-content-sha256 header, so its signer hashed the
		// payload itself.
		var body []byte
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		// Lambda's control plane has no anonymous surface, so an absent credential fails like an invalid one.
		res, serr := sigv4Verify(r, body)
		if serr != nil {
			lambdaWriteSigv4Error(w, serr)
			return
		}
		resARN := "*"
		if resource != nil {
			if v := resource(r); v != "" {
				resARN = v
			}
		}
		targets := lambdaAuthorizationTargets(r, opFn(r), resARN)
		if res == sigv4NoCredential {
			// An AWS service calling as its own principal, as Amazon
			// EventBridge invokes a rule's target, is admitted by the
			// function's resource policy; any other unsigned call is refused.
			src := iamDeliverySource(r)
			if src == nil || !sim.InProcess(r) {
				lambdaWriteMissingAuth(w)
				return
			}
			if denied, ok := iamServicePrincipalAllowed(*src, targets); !ok {
				lambdaWriteIAMDeny(w, r, src.Service, denied)
				return
			}
		} else {
			for _, target := range targets {
				if !iamEnforceREST(w, r, target, lambdaWriteIAMDeny) {
					return
				}
			}
		}
		// A FunctionName is a name, a full ARN or a partial ARN, any of them
		// with a qualifier; the handlers look the function up by name.
		if name := r.PathValue("name"); strings.Contains(name, ":function:") {
			r.SetPathValue("name", name[strings.Index(name, ":function:")+len(":function:"):])
		}
		h(w, r)
	}
}

// lambdaAuthorizationTargets is what AWS Lambda authorizes an operation as: its
// action on the resource, and lambda:TagResource as well when a create carries
// tags.
func lambdaAuthorizationTargets(r *http.Request, op, resource string) []iamAuthorizationTarget {
	return append([]iamAuthorizationTarget{{action: lambdaIAMAction(op), resource: resource}},
		iamTagOnCreateTargets(r, "lambda", op, []string{resource})...)
}

// lambdaIAMAction maps a Lambda REST operation to its IAM action: "lambda:" plus
// the operation, except the invoke family, which all require
// lambda:InvokeFunction.
func lambdaIAMAction(op string) string {
	switch op {
	case "Invoke", "InvokeAsync", "InvokeWithResponseStream":
		return "lambda:InvokeFunction"
	default:
		return "lambda:" + op
	}
}

// lambdaFunctionResourceARN resolves the target function ARN from {name}, or
// {arn} on the tags routes, and returns "" when the route names none, which
// evaluates against "*".
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

// lambdaCreatedFunctionARN is the function a CreateFunction names in its body,
// by name, partial ARN or full ARN.
func lambdaCreatedFunctionARN(r *http.Request) string {
	name := iamJSONBodyField(r, "FunctionName")
	switch {
	case name == "", strings.HasPrefix(name, "arn:"):
		return name
	case strings.Contains(name, ":function:"):
		return lambdaArn(name[strings.Index(name, ":function:")+len(":function:"):])
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

// lambdaWriteAuthError writes Lambda's restJson1 error: the x-amzn-Errortype
// header the SDKs and CloudTrail read, and the __type/message body.
func lambdaWriteAuthError(w http.ResponseWriter, code, message string, status int) {
	w.Header().Set("X-Amzn-Errortype", code)
	AWSError(w, code, message, status)
}

// lambdaWriteSigv4Error maps a SigV4 failure through the awsJson error codes,
// since Lambda's REST API is JSON-protocol too.
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
