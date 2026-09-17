package main

import (
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Call-time IAM enforcement for the Amazon S3 control plane, the way
// s3Enforced gates the data plane. Each /v20180820 route serves one API
// operation; AWS authorizes that operation as the action its Service Reference
// entry names, against the resource the request names — an access point, an
// Access Grants instance, location or grant, a batch job, a Multi-Region
// Access Point, a Storage Lens configuration or group. The evaluation is
// iamAuthorize's, reached through iamEnforceREST, so a control-plane call is
// judged by exactly the policies a data-plane call is, and an unregistered
// credential passes here as it does everywhere else in the gate.
//
// An operation whose action declares no resource type authorizes "*". That is
// what AWS publishes about the action rather than a default this file chose,
// and TestS3ControlRoutesAuthorizeWhatTheReferenceDeclares holds the route
// table and the vendored reference together.
//
// Not every control-plane operation is authorized as an s3 action. AWS
// publishes the directory-bucket surface as s3express, the Outposts surface as
// s3-outposts and the transformation callback as s3-object-lambda, each in its
// own Service Reference; all three are vendored here now, and a route whose
// operation carries a namespace prefix ("s3express:PutAccessPointScope") is
// authorized as that namespace's action against that namespace's ARN format.
//
// One route still carries no gate, and BUGS.md holds it: the control plane's
// DeleteBucketLifecycleConfiguration. No vendored document declares an action
// for it -- the s3 reference lists the operation with an empty authorized-action
// set, and s3-outposts declares only Get/PutLifecycleConfiguration, whose
// resource type is an Outposts bucket whose ARN carries an OutpostId this
// simulator models nothing of. Authorizing it as the s3 lifecycle action would
// deny the grant real AWS honours.

// s3ControlRoute is one gated route: the pattern it mounts on, the operation
// it serves, the resource AWS authorizes that operation against, and the
// handler behind the gate. A nil resource is an operation that names none.
//
// The operation may carry the namespace that declares its action, as
// "s3express:PutAccessPointScope"; a bare name is an s3 operation, which most
// of them are.
type s3ControlRoute struct {
	pattern   string
	operation string
	resource  func(*http.Request) string
	handler   http.HandlerFunc
}

// s3ControlRegister mounts routes with the IAM gate in front of each handler.
func s3ControlRegister(srv *sim.Server, routes []s3ControlRoute) {
	for _, route := range routes {
		srv.HandleFunc(route.pattern, s3ControlEnforced(route.operation, route.resource, route.handler))
	}
}

// s3ControlGatedRoutes is every route the gate covers, which the conformance
// test crosses against the vendored reference.
func s3ControlGatedRoutes() []s3ControlRoute {
	var all []s3ControlRoute
	for _, table := range [][]s3ControlRoute{
		s3AccessPointRoutes, s3ObjectLambdaAccessPointRoutes,
		s3ControlAccessGrantsRoutes, s3ControlJobRoutes,
		s3ControlMultiRegionRoutes, s3ControlStorageLensRoutes,
		s3ControlTaggingRoutes, s3ControlMiscRoutes,
	} {
		all = append(all, table...)
	}
	return all
}

// s3ControlRouteService splits a route's operation into the namespace whose
// Service Reference declares its action and the operation's own name.
func s3ControlRouteService(operation string) (string, string) {
	if service, name, ok := strings.Cut(operation, ":"); ok {
		return service, name
	}
	return "s3", operation
}

func s3ControlEnforced(operation string, resource func(*http.Request) string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, target := range s3ControlAuthorizationTargets(r, operation, resource) {
			if !iamEnforceREST(w, r, target.action, target.resource, s3ControlWriteIAMDeny) {
				return
			}
		}
		// Registering an Access Grants location hands S3 a role, which AWS
		// authorizes a second time against the role itself.
		service, name := s3ControlRouteService(operation)
		if !iamEnforcePassRoleWith(w, r, service+":"+name, s3ControlWriteIAMDeny) {
			return
		}
		h(w, r)
	}
}

// s3ControlAuthorizationTargets is what a request to one S3 Control route is
// authorized as: the operation's own action on the resource the request names,
// plus the further action the operation performs where the reference declares
// one.
func s3ControlAuthorizationTargets(r *http.Request, operation string, resource func(*http.Request) string) []iamAuthorizationTarget {
	arn := "*"
	if resource != nil {
		if named := resource(r); named != "" {
			arn = named
		}
	}
	service, name := s3ControlRouteService(operation)
	targets := iamOperationTargets(r, service, name, []string{arn})
	return append(targets, s3ControlTaggingTargets(r, name)...)
}

// s3ControlTaggingTargets is the tagging AWS authorizes alongside a create
// that carries tags: creating a Storage Lens group with them is also a tagging
// of the group, which is why the reference lists s3:TagResource against the
// operation.
func s3ControlTaggingTargets(r *http.Request, operation string) []iamAuthorizationTarget {
	if operation != "CreateStorageLensGroup" {
		return nil
	}
	body := s3ControlGateBody(r)
	group, _ := body.Child("StorageLensGroup")
	name := group.ChildText("Name")
	if name == "" || len(s3ControlTagsFrom(body, "Tags", "Tag")) == 0 {
		return nil
	}
	return []iamAuthorizationTarget{{
		action:   "s3:TagResource",
		resource: s3StorageLensGroupARN(s3ControlAccountID(r), name),
	}}
}

// s3ControlWriteIAMDeny refuses the call in the envelope the rest of the
// control plane answers in.
func s3ControlWriteIAMDeny(w http.ResponseWriter, _ *http.Request, principalArn, action string) {
	s3ControlError(w, "AccessDenied",
		"User: "+principalArn+" is not authorized to perform: "+action+
			" because no identity-based policy allows the "+action+" action",
		http.StatusForbidden)
}

// s3ControlGateBody reads the request's document for the gate, leaving the
// body in place for the handler. A request that names its resource in the
// document rather than in the path — a Multi-Region Access Point, a Storage
// Lens group — is authorized against what the document names.
func s3ControlGateBody(r *http.Request) s3ControlXMLNode {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return s3ControlXMLNode{}
	}
	var node s3ControlXMLNode
	if xml.Unmarshal(body, &node) != nil {
		return s3ControlXMLNode{}
	}
	return node
}

func s3ControlAccessPointResource(r *http.Request) string {
	return s3AccessPointARN(s3ControlAccountID(r), sim.PathParam(r, "name"))
}

func s3ControlObjectLambdaResource(r *http.Request) string {
	return s3ObjectLambdaARN(s3ControlAccountID(r), sim.PathParam(r, "name"))
}

func s3ControlAccessGrantsInstanceResource(r *http.Request) string {
	return s3AccessGrantsInstanceARN(s3ControlAccountID(r))
}

func s3ControlAccessGrantsLocationResource(r *http.Request) string {
	return s3AccessGrantsLocationARN(s3ControlAccountID(r), sim.PathParam(r, "locationId"))
}

// s3ControlNewAccessGrantsLocationResource is what a registration is
// authorized against: S3 assigns the location's identifier, so the request
// carries none and AWS evaluates the call against the type — the same answer
// iamCreateWildcardARNs derives for every other create whose identifier the
// service mints. A literal "*" would be a different answer, refusing a policy
// scoped to the account's locations that real AWS honours.
func s3ControlNewAccessGrantsLocationResource(r *http.Request) string {
	return s3AccessGrantsLocationARN(s3ControlAccountID(r), "*")
}

func s3ControlAccessGrantResource(r *http.Request) string {
	return s3AccessGrantARN(s3ControlAccountID(r), sim.PathParam(r, "grantId"))
}

// s3ControlNewAccessGrantResource is the type wildcard a grant's creation
// authorizes against, for the reason s3ControlNewAccessGrantsLocationResource
// gives.
func s3ControlNewAccessGrantResource(r *http.Request) string {
	return s3AccessGrantARN(s3ControlAccountID(r), "*")
}

func s3ControlJobResource(r *http.Request) string {
	return s3BatchJobARN(s3ControlAccountID(r), sim.PathParam(r, "jobId"))
}

func s3ControlStorageLensResource(r *http.Request) string {
	return s3StorageLensARN(s3ControlAccountID(r), sim.PathParam(r, "configId"))
}

func s3ControlStorageLensGroupResource(r *http.Request) string {
	return s3StorageLensGroupARN(s3ControlAccountID(r), sim.PathParam(r, "name"))
}

func s3ControlMultiRegionResource(r *http.Request) string {
	return s3MultiRegionAccessPointARN(s3ControlAccountID(r), sim.PathParam(r, "name"))
}

// s3ControlMultiRegionRequestResource reads the endpoint an asynchronous
// request acts on out of its document, where the three asynchronous
// operations name it.
func s3ControlMultiRegionRequestResource(r *http.Request) string {
	details, ok := s3ControlGateBody(r).Child("Details")
	if !ok {
		return ""
	}
	return s3MultiRegionAccessPointARN(s3ControlAccountID(r), details.ChildText("Name"))
}

// s3ControlAsyncRequestResource is the request token itself: S3 hands the
// caller an ARN of the async-request type, and the poll carries it as the path.
func s3ControlAsyncRequestResource(r *http.Request) string {
	return sim.PathParam(r, "token")
}

// s3ControlTaggedResource is the ARN the shared tagging trio names outright.
func s3ControlTaggedResource(r *http.Request) string {
	return sim.PathParam(r, "resourceArn")
}
