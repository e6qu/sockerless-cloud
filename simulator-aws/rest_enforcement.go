package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Call-time IAM enforcement for the REST services that register their routes
// on the server mux rather than at POST /: AWS Amplify, Amazon API Gateway,
// AWS Batch, Amazon EFS, Amazon EventBridge Scheduler, Amazon CloudFront and
// Amazon Route 53. The request's operation is the one whose Smithy http trait
// the route matches (awsRESTRoutes, generated from the vendored models), and
// the resources it names are read from the path's labels, its query and its
// document against the ARN formats the Service Reference publishes.

// awsRESTRoute is one operation's http trait: the method and URI template a
// request takes to reach it.
type awsRESTRoute struct {
	Method    string
	URI       string
	Operation string
}

// awsRESTGate describes how one REST service's routes are authorized: the
// models whose routes name its operations, the IAM service prefix its actions
// carry, and how it refuses.
type awsRESTGate struct {
	models  []string
	service string
	deny    func(http.ResponseWriter, *http.Request, string, string)
	// refuse answers a request the front end cannot authenticate with the
	// failure's code and message, in the service's protocol.
	refuse  func(http.ResponseWriter, *http.Request, string, string)
	restXML bool
}

// awsRESTGates maps a CloudTrail event source to the gate on its routes.
// Amazon API Gateway's two APIs share one event source and one IAM prefix.
var awsRESTGates = map[string]awsRESTGate{
	"amplify.amazonaws.com":           {models: []string{"amplify"}, service: "amplify", deny: amplifyWriteIAMDeny, refuse: restRefuseJSON("amplify")},
	"apigateway.amazonaws.com":        {models: []string{"apigateway", "apigatewayv2"}, service: "apigateway", deny: apigatewayWriteIAMDeny, refuse: apigatewayRefuse},
	"batch.amazonaws.com":             {models: []string{"batch"}, service: "batch", deny: restWriteJSONDeny("batch"), refuse: restRefuseJSON("batch")},
	"elasticfilesystem.amazonaws.com": {models: []string{"efs"}, service: "elasticfilesystem", deny: restWriteJSONDeny("efs"), refuse: restRefuseJSON("efs")},
	"scheduler.amazonaws.com":         {models: []string{"scheduler"}, service: "scheduler", deny: restWriteJSONDeny("scheduler"), refuse: restRefuseJSON("scheduler")},
	"cloudfront.amazonaws.com":        {models: []string{"cloudfront"}, service: "cloudfront", deny: cloudfrontWriteIAMDeny, refuse: cloudfrontRefuse, restXML: true},
	"route53.amazonaws.com":           {models: []string{"route53"}, service: "route53", deny: route53WriteIAMDeny, refuse: route53Refuse, restXML: true},
}

// awsRESTAuthenticate verifies the SigV4 signature on a request to a REST
// route of the service source names, in the header or the presigned-query
// form, and reports whether the request may go on to authorization. Every
// route these services serve is a management operation, so the front end
// refuses an unsigned request; the one unsigned caller it admits is an AWS
// service calling in-process as its own principal, which authorization then
// holds to the target's resource policy. body is the whole request body, whose
// digest the signature covers.
func awsRESTAuthenticate(w http.ResponseWriter, r *http.Request, source string, body []byte) bool {
	gate, gated := awsRESTGates[source]
	if !gated {
		return true
	}
	result, serr := sigv4Verify(r, body)
	if serr == nil && result == sigv4Verified && sigv4PresignedExpired(r.URL.Query(), time.Now()) {
		serr = &sigv4Error{sigErrRequestExpired, "Request has expired"}
	}
	if serr != nil {
		code, message := awsRESTAuthenticationError(gate.restXML, serr)
		gate.refuse(w, r, code, message)
		return false
	}
	if result == sigv4NoCredential && (!sim.InProcess(r) || iamDeliverySource(r) == nil) {
		gate.refuseUnsigned(w, r)
		return false
	}
	return true
}

// awsRESTAuthenticateBody is awsRESTAuthenticate for a handler that has not
// buffered the request body: it holds the body for the handler to read again.
func awsRESTAuthenticateBody(w http.ResponseWriter, r *http.Request, source string) bool {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			restWriteJSONError(w, "", "SerializationException", http.StatusBadRequest, "Failed to read the request body: "+err.Error())
			return false
		}
		_ = r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	return awsRESTAuthenticate(w, r, source, body)
}

func (gate awsRESTGate) refuseUnsigned(w http.ResponseWriter, r *http.Request) {
	if gate.restXML {
		gate.refuse(w, r, "MissingAuthenticationToken", "Request is missing Authentication Token")
		return
	}
	gate.refuse(w, r, "MissingAuthenticationTokenException", "Missing Authentication Token")
}

// awsRESTAuthenticationError is the code AWS's front end answers an
// authentication failure with: the JSON services' exception names, and the
// REST-XML services' codes, which they share with the Query protocol.
func awsRESTAuthenticationError(restXML bool, serr *sigv4Error) (string, string) {
	switch serr.kind {
	case sigErrInvalidClientToken:
		if restXML {
			return "InvalidClientTokenId", serr.message
		}
		return "UnrecognizedClientException", serr.message
	case sigErrExpiredToken:
		if restXML {
			return "ExpiredToken", serr.message
		}
		return "ExpiredTokenException", serr.message
	}
	if restXML {
		return "SignatureDoesNotMatch", serr.message
	}
	return "InvalidSignatureException", serr.message
}

// iamEnforceRESTRoute authorizes a request awsRESTAuthenticate admitted to a
// REST route of the service source names, and reports whether the handler
// should run: a signed request when its principal holds the operation's
// actions on the resources the request names, and an AWS service's unsigned
// in-process call when the target's resource policy admits the service.
func iamEnforceRESTRoute(w http.ResponseWriter, r *http.Request, source string) bool {
	gate, gated := awsRESTGates[source]
	if !gated {
		return true
	}
	operation, labels, ok := awsRESTOperationFor(gate.models, r)
	if !ok {
		return true
	}
	routed := iamWithRESTOperation(r, operation, labels)
	// The gate reads the body through the routed copy, which then holds the
	// body the handler reads from r.
	defer func() { r.Body = routed.Body }()
	if iamAccessKeyIDFromRequest(routed) == "" {
		src := iamDeliverySource(routed)
		if src == nil || !sim.InProcess(routed) {
			gate.refuseUnsigned(w, routed)
			return false
		}
		if denied, allowed := iamServicePrincipalAllowed(*src, iamRESTAuthorizationTargets(routed, gate.service, operation, labels)); !allowed {
			gate.deny(w, routed, src.Service, denied)
			return false
		}
		return true
	}
	for _, target := range iamRESTAuthorizationTargets(routed, gate.service, operation, labels) {
		if !iamEnforceREST(w, routed, target, gate.deny) {
			return false
		}
	}
	return iamEnforcePassRoleWith(w, routed, gate.service+":"+operation, gate.deny)
}

// awsRESTOperationKey carries the operation a REST request's route names, with
// the values of the route's labels: the request spells the operation nowhere,
// as an X-Amz-Target or an Action parameter would.
type awsRESTOperationKey struct{}

type awsRESTRequestRoute struct {
	operation string
	labels    map[string][]string
}

func iamWithRESTOperation(r *http.Request, operation string, labels map[string][]string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), awsRESTOperationKey{}, awsRESTRequestRoute{operation, labels}))
}

func iamRESTOperation(r *http.Request) string {
	route, _ := r.Context().Value(awsRESTOperationKey{}).(awsRESTRequestRoute)
	return route.operation
}

// iamRESTLabel is the value of the label the route a REST request took binds
// under name, as the operation's Smithy http trait spells it.
func iamRESTLabel(r *http.Request, name string) string {
	route, _ := r.Context().Value(awsRESTOperationKey{}).(awsRESTRequestRoute)
	if values := route.labels[name]; len(values) > 0 {
		return values[0]
	}
	return ""
}

// iamRESTAuthorizationTargets is what a REST request is authorized as. Amazon
// API Gateway authorizes most operations as the HTTP method on the request's
// own path, which is the resource the Service Reference publishes for every
// one of its types, and its portal and routing-rule operations as actions of
// their own; every other service authorizes the operation's actions on the
// resources the request names.
func iamRESTAuthorizationTargets(r *http.Request, service, operation string, labels map[string][]string) []iamAuthorizationTarget {
	if service == "apigateway" {
		return iamAPIGatewayAuthorizationTargets(r, operation, labels)
	}
	return iamOperationTargets(r, service, operation, iamRESTResourceARNs(r, service, operation, labels))
}

var iamAPIGatewayMethodActions = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

// iamAPIGatewayAdditionalActions are the actions the Service Reference's
// operation table authorizes a portal operation as beside its own.
var iamAPIGatewayAdditionalActions = map[string][]string{
	"CreatePortal":        {"GetPortalProduct", "POST"},
	"CreatePortalProduct": {"POST"},
	"UpdatePortal":        {"GetPortalProduct"},
	"PreviewPortal":       {"GetPortalProduct"},
	"PublishPortal":       {"GetPortalProduct"},
}

// iamAPIGatewayAuthorizationTargets authorizes an operation the Service
// Reference maps to an HTTP-method action as that method on the request's
// path, and one it maps to an action of the operation's own name -- a portal,
// portal product, product page or routing rule -- as that action on the
// resources the request names.
func iamAPIGatewayAuthorizationTargets(r *http.Request, operation string, labels map[string][]string) []iamAuthorizationTarget {
	method := iamAuthorizationTarget{
		action:   "apigateway:" + r.Method,
		resource: "arn:aws:apigateway:" + iamRESTRegion(r) + "::" + r.URL.Path,
	}
	action := iamRESTAction("apigateway", operation)
	if _, declared := iamActionResourceTypes["apigateway:"+action]; !declared || iamAPIGatewayMethodActions[action] {
		return []iamAuthorizationTarget{method}
	}
	targets := iamOperationTargets(r, "apigateway", operation, iamRESTResourceARNs(r, "apigateway", operation, labels))
	for _, additional := range iamAPIGatewayAdditionalActions[operation] {
		if additional == "POST" {
			targets = append(targets, method)
			continue
		}
		for _, product := range iamAPIGatewayPortalProducts(r, operation, labels) {
			targets = append(targets, iamAuthorizationTarget{action: "apigateway:" + additional, resource: product})
		}
	}
	return targets
}

// iamAPIGatewayPortalProducts are the portal products a portal operation reads:
// those a create or an update includes, and those the portal being published
// or previewed includes.
func iamAPIGatewayPortalProducts(r *http.Request, operation string, labels map[string][]string) []string {
	if operation == "CreatePortal" || operation == "UpdatePortal" {
		var request struct {
			IncludedPortalProductArns []string `json:"includedPortalProductArns"`
		}
		if !iamDecodeJSONRequest(iamRequestBody(r), &request) {
			return nil
		}
		return request.IncludedPortalProductArns
	}
	ids := labels["PortalId"]
	if len(ids) == 0 {
		return nil
	}
	portal, ok := apigwv2Portals.Get(ids[0])
	if !ok {
		return nil
	}
	return portal.IncludedPortalProductArns
}

func iamRESTRegion(r *http.Request) string {
	if region := iamRequestedRegion(r); region != "" {
		return region
	}
	return awsRegion()
}

// iamRESTResourceARNs derives the ARNs a REST request names: an ARN the
// document carries outright, else the published format of each resource type
// the action declares, filled from the path's labels, the query and the
// document, else the type wildcard a create is evaluated against.
func iamRESTResourceARNs(r *http.Request, service, operation string, labels map[string][]string) []string {
	region, account := iamRESTRegion(r), awsAccountID()
	types := iamActionResourceTypes[service+":"+iamRESTAction(service, operation)]
	if len(types) == 0 {
		return []string{"*"}
	}
	if arn := iamRequestARNField(r); arn != "" {
		return []string{arn}
	}
	fields := map[string][]string{}
	for name, values := range iamJSONRequestFields(r) {
		fields[name] = values
	}
	for name, values := range r.URL.Query() {
		fields[strings.ToLower(name)] = values
	}
	for name, values := range labels {
		fields[strings.ToLower(name)] = values
	}
	if service == "scheduler" && len(fields["groupname"]) == 0 {
		// A schedule outside a named group is in the default group, which is
		// part of its ARN.
		fields["groupname"] = []string{"default"}
	}
	lookup := func(field string) []string { return fields[strings.ToLower(field)] }
	if arns := iamTableDrivenARNs(service, types, region, account, iamRESTLabelAliases[service], lookup); len(arns) > 0 {
		return arns
	}
	if arns := iamARNsNamingADeclaredType(r, service, types, region, account); len(arns) > 0 {
		return arns
	}
	if arns := iamCreateWildcardARNs(service, operation, types, region, account); len(arns) > 0 {
		return arns
	}
	return []string{"*"}
}

// iamRESTAction is the action an operation's resource types are declared
// under, which for a renamed operation is the action the reference maps it to.
func iamRESTAction(service, operation string) string {
	if mapped, ok := iamOperationActions[service+":"+operation]; ok {
		return mapped
	}
	return operation
}

// iamRESTLabelAliases names the path label a service spells an ARN format
// variable as when the two differ: Amazon CloudFront's distribution ARN carries
// ${DistributionId} where its routes carry {Id}, and Amazon Route 53's health
// check ARN carries ${Id} where its routes carry {HealthCheckId}. Amazon
// EventBridge Scheduler names a schedule's group in the query and the
// schedule itself in the path.
var iamRESTLabelAliases = map[string]map[string][]string{
	"cloudfront": {
		"distribution.DistributionId":           {"Id"},
		"streaming-distribution.DistributionId": {"Id"},
	},
	"route53": {
		"healthcheck.Id":        {"HealthCheckId"},
		"cidrcollection.Id":     {"CidrCollectionId"},
		"trafficpolicy.Id":      {"Id"},
		"delegationset.Id":      {"Id", "DelegationSetId"},
		"hostedzone.Id":         {"Id", "HostedZoneId"},
		"queryloggingconfig.Id": {"Id"},
	},
	"scheduler": {
		"schedule.ScheduleName":    {"Name"},
		"schedule.GroupName":       {"groupName"},
		"schedule-group.GroupName": {"Name"},
	},
	"batch": {
		"job-queue.JobQueueName":                     {"jobQueue"},
		"job-definition.JobDefinitionName":           {"jobDefinition"},
		"compute-environment.ComputeEnvironmentName": {"computeEnvironment"},
		"scheduling-policy.SchedulingPolicyName":     {"name"},
	},
}

// awsRESTOperationFor names the operation whose route a request took, with the
// values of the route's labels. Among the routes a path fits, the one with the
// most literal segments wins, and a route whose URI names query literals fits
// only a request that carries them.
func awsRESTOperationFor(models []string, r *http.Request) (string, map[string][]string, bool) {
	segments := awsRESTSegments(r.URL.Path)
	query := r.URL.Query()
	bestScore := -1
	var best awsRESTRoute
	var bestLabels map[string][]string
	for _, model := range models {
		for _, route := range awsRESTRoutes[model] {
			if route.Method != r.Method {
				continue
			}
			template, literals, _ := strings.Cut(route.URI, "?")
			labels, literalSegments, ok := awsRESTMatchPath(awsRESTSegments(template), segments)
			if !ok || !awsRESTQueryLiteralsMatch(literals, query) {
				continue
			}
			score := literalSegments*2 + len(literals)
			if score > bestScore {
				bestScore, best, bestLabels = score, route, labels
			}
		}
	}
	if bestScore < 0 {
		return "", nil, false
	}
	return best.Operation, bestLabels, true
}

func awsRESTSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// awsRESTMatchPath fits a request's path segments to a template's: a {label}
// takes one segment, a {label+} the rest, and anything else must be equal. It
// returns the labels' values and how many segments matched literally.
func awsRESTMatchPath(template, segments []string) (map[string][]string, int, bool) {
	labels := map[string][]string{}
	literal := 0
	for i, part := range template {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "+}") {
			if i >= len(segments) {
				return nil, 0, false
			}
			rest := strings.Join(segments[i:], "/")
			if unescaped, err := url.PathUnescape(rest); err == nil {
				rest = unescaped
			}
			labels[part[1:len(part)-2]] = []string{rest}
			return labels, literal, true
		}
		if i >= len(segments) {
			return nil, 0, false
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			value := segments[i]
			if unescaped, err := url.PathUnescape(value); err == nil {
				value = unescaped
			}
			labels[part[1:len(part)-1]] = []string{value}
			continue
		}
		if part != segments[i] {
			return nil, 0, false
		}
		literal++
	}
	if len(segments) != len(template) {
		return nil, 0, false
	}
	return labels, literal, true
}

// awsRESTQueryLiteralsMatch reports whether a request carries every query
// literal a URI template names, such as "find=LayerVersion" or "tags".
func awsRESTQueryLiteralsMatch(literals string, query url.Values) bool {
	if literals == "" {
		return true
	}
	for _, literal := range strings.Split(literals, "&") {
		key, value, hasValue := strings.Cut(literal, "=")
		values, present := query[key]
		if !present {
			return false
		}
		if hasValue && !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func iamRESTDenyMessage(principalArn, action string) string {
	return "User: " + principalArn + " is not authorized to perform: " + action +
		" because no identity-based policy allows the " + action + " action"
}

// restWriteJSONDeny refuses in the AccessDeniedException every REST-JSON
// service answers an unauthorized caller with, under the message member the
// model declares.
func restWriteJSONDeny(model string) func(http.ResponseWriter, *http.Request, string, string) {
	return func(w http.ResponseWriter, _ *http.Request, principalArn, action string) {
		restWriteJSONError(w, model, "AccessDeniedException", http.StatusForbidden, iamRESTDenyMessage(principalArn, action))
	}
}

func restWriteJSONError(w http.ResponseWriter, model, code string, status int, message string) {
	awsWriteError(w, awsErrorEnvelope{Protocol: awsRestJSON1, Model: model}, status, code, message, "")
}

// AWS Amplify declares UnauthorizedException, 401, for an operation that
// fails for a lack of access.
func amplifyWriteIAMDeny(w http.ResponseWriter, _ *http.Request, principalArn, action string) {
	amplifyWriteError(w, http.StatusUnauthorized, "UnauthorizedException", iamRESTDenyMessage(principalArn, action))
}

// Amazon API Gateway's REST API declares UnauthorizedException, 401, for a
// caller with insufficient permissions; its HTTP API declares
// AccessDeniedException, 403.
func apigatewayWriteIAMDeny(w http.ResponseWriter, r *http.Request, principalArn, action string) {
	message := iamRESTDenyMessage(principalArn, action)
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		restWriteJSONError(w, "apigatewayv2", "AccessDeniedException", http.StatusForbidden, message)
		return
	}
	restWriteJSONError(w, "api-gateway", "UnauthorizedException", http.StatusUnauthorized, message)
}

func cloudfrontWriteIAMDeny(w http.ResponseWriter, _ *http.Request, principalArn, action string) {
	cfWriteError(w, http.StatusForbidden, "AccessDenied", iamRESTDenyMessage(principalArn, action))
}

func route53WriteIAMDeny(w http.ResponseWriter, _ *http.Request, principalArn, action string) {
	r53WriteError(w, http.StatusForbidden, "AccessDenied", iamRESTDenyMessage(principalArn, action))
}

func restRefuseJSON(model string) func(http.ResponseWriter, *http.Request, string, string) {
	return func(w http.ResponseWriter, _ *http.Request, code, message string) {
		restWriteJSONError(w, model, code, http.StatusForbidden, message)
	}
}

func apigatewayRefuse(w http.ResponseWriter, r *http.Request, code, message string) {
	model := "api-gateway"
	if strings.HasPrefix(r.URL.Path, "/v2/") {
		model = "apigatewayv2"
	}
	restWriteJSONError(w, model, code, http.StatusForbidden, message)
}

func cloudfrontRefuse(w http.ResponseWriter, _ *http.Request, code, message string) {
	cfWriteError(w, http.StatusForbidden, code, message)
}

func route53Refuse(w http.ResponseWriter, _ *http.Request, code, message string) {
	r53WriteError(w, http.StatusForbidden, code, message)
}
