package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The account every case addresses, and the ARNs the S3 control plane
// authorizes against for it.
const (
	s3ControlTestAccount    = "123456789012"
	s3ControlTestRegion     = "us-east-1"
	s3ControlAccessPoint    = "arn:aws:s3:us-east-1:123456789012:accesspoint/points"
	s3ExpressAccessPoint    = "arn:aws:s3express:us-east-1:123456789012:accesspoint/points"
	s3ControlObjectLambda   = "arn:aws:s3-object-lambda:us-east-1:123456789012:accesspoint/olap"
	s3ControlInstance       = "arn:aws:s3:us-east-1:123456789012:access-grants/default"
	s3ControlLocation       = "arn:aws:s3:us-east-1:123456789012:access-grants/default/location/loc-1"
	s3ControlAnyLocation    = "arn:aws:s3:us-east-1:123456789012:access-grants/default/location/*"
	s3ControlGrant          = "arn:aws:s3:us-east-1:123456789012:access-grants/default/grant/g-1"
	s3ControlAnyGrant       = "arn:aws:s3:us-east-1:123456789012:access-grants/default/grant/*"
	s3ControlJob            = "arn:aws:s3:us-east-1:123456789012:job/j-1"
	s3ControlLens           = "arn:aws:s3:us-east-1:123456789012:storage-lens/sl-1"
	s3ControlLensGroup      = "arn:aws:s3:us-east-1:123456789012:storage-lens-group/segment"
	s3ControlMultiRegion    = "arn:aws:s3::123456789012:accesspoint/reports.9012.mrap"
	s3ControlAsyncRequest   = "arn:aws:s3:us-west-2:123456789012:async-request/mrap/create/e3a1"
	s3ControlTaggedBucket   = "arn:aws:s3:::reports"
	s3ControlMultiRegionXML = `<CreateMultiRegionAccessPointRequest><ClientToken>t</ClientToken>` +
		`<Details><Name>reports</Name></Details></CreateMultiRegionAccessPointRequest>`
)

// s3ControlRouteRequest builds the request one route serves, addressed with the
// path parameters a caller would fill its pattern with.
func s3ControlRouteRequest(route s3ControlRoute, params map[string]string, body string) *http.Request {
	method, pattern, _ := strings.Cut(route.pattern, " ")
	path := pattern
	for name, value := range params {
		path = strings.ReplaceAll(path, "{"+name+"...}", value)
		path = strings.ReplaceAll(path, "{"+name+"}", value)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("x-amz-account-id", s3ControlTestAccount)
	for name, value := range params {
		r.SetPathValue(name, value)
	}
	return r
}

type s3ControlDerivationCase struct {
	pattern string
	params  map[string]string
	body    string
	want    []string
}

// Every gated S3 Control route, the request a caller makes to it, and what AWS
// authorizes that request as.
var s3ControlDerivationCases = []s3ControlDerivationCase{
	// Standard access points.
	{pattern: "PUT /v20180820/accesspoint/{name}", params: map[string]string{"name": "points"},
		want: []string{"s3:CreateAccessPoint " + s3ControlAccessPoint}},
	{pattern: "GET /v20180820/accesspoint/{name}", params: map[string]string{"name": "points"},
		want: []string{"s3:GetAccessPoint *"}},
	{pattern: "DELETE /v20180820/accesspoint/{name}", params: map[string]string{"name": "points"},
		want: []string{"s3:DeleteAccessPoint " + s3ControlAccessPoint}},
	{pattern: "GET /v20180820/accesspoint", want: []string{"s3:ListAccessPoints *"}},
	{pattern: "PUT /v20180820/accesspoint/{name}/policy", params: map[string]string{"name": "points"},
		want: []string{"s3:PutAccessPointPolicy " + s3ControlAccessPoint}},
	{pattern: "GET /v20180820/accesspoint/{name}/policy", params: map[string]string{"name": "points"},
		want: []string{"s3:GetAccessPointPolicy " + s3ControlAccessPoint}},
	{pattern: "DELETE /v20180820/accesspoint/{name}/policy", params: map[string]string{"name": "points"},
		want: []string{"s3:DeleteAccessPointPolicy " + s3ControlAccessPoint}},
	{pattern: "GET /v20180820/accesspoint/{name}/policyStatus", params: map[string]string{"name": "points"},
		want: []string{"s3:GetAccessPointPolicyStatus " + s3ControlAccessPoint}},

	// Object Lambda access points.
	{pattern: "PUT /v20180820/accesspointforobjectlambda/{name}", params: map[string]string{"name": "olap"},
		want: []string{"s3:CreateAccessPointForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "GET /v20180820/accesspointforobjectlambda/{name}", params: map[string]string{"name": "olap"},
		want: []string{"s3:GetAccessPointForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "DELETE /v20180820/accesspointforobjectlambda/{name}", params: map[string]string{"name": "olap"},
		want: []string{"s3:DeleteAccessPointForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "GET /v20180820/accesspointforobjectlambda",
		want: []string{"s3:ListAccessPointsForObjectLambda *"}},
	{pattern: "GET /v20180820/accesspointforobjectlambda/{name}/configuration", params: map[string]string{"name": "olap"},
		want: []string{"s3:GetAccessPointConfigurationForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "PUT /v20180820/accesspointforobjectlambda/{name}/configuration", params: map[string]string{"name": "olap"},
		want: []string{"s3:PutAccessPointConfigurationForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "PUT /v20180820/accesspointforobjectlambda/{name}/policy", params: map[string]string{"name": "olap"},
		want: []string{"s3:PutAccessPointPolicyForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "GET /v20180820/accesspointforobjectlambda/{name}/policy", params: map[string]string{"name": "olap"},
		want: []string{"s3:GetAccessPointPolicyForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "DELETE /v20180820/accesspointforobjectlambda/{name}/policy", params: map[string]string{"name": "olap"},
		want: []string{"s3:DeleteAccessPointPolicyForObjectLambda " + s3ControlObjectLambda}},
	{pattern: "GET /v20180820/accesspointforobjectlambda/{name}/policyStatus", params: map[string]string{"name": "olap"},
		want: []string{"s3:GetAccessPointPolicyStatusForObjectLambda " + s3ControlObjectLambda}},

	// Access Grants.
	{pattern: "POST /v20180820/accessgrantsinstance",
		want: []string{"s3:CreateAccessGrantsInstance " + s3ControlInstance}},
	{pattern: "GET /v20180820/accessgrantsinstance",
		want: []string{"s3:GetAccessGrantsInstance " + s3ControlInstance}},
	{pattern: "DELETE /v20180820/accessgrantsinstance",
		want: []string{"s3:DeleteAccessGrantsInstance " + s3ControlInstance}},
	{pattern: "GET /v20180820/accessgrantsinstances",
		want: []string{"s3:ListAccessGrantsInstances *"}},
	{pattern: "GET /v20180820/accessgrantsinstance/prefix",
		want: []string{"s3:GetAccessGrantsInstanceForPrefix " + s3ControlInstance}},
	{pattern: "POST /v20180820/accessgrantsinstance/identitycenter",
		want: []string{"s3:AssociateAccessGrantsIdentityCenter " + s3ControlInstance}},
	{pattern: "DELETE /v20180820/accessgrantsinstance/identitycenter",
		want: []string{"s3:DissociateAccessGrantsIdentityCenter " + s3ControlInstance}},
	{pattern: "PUT /v20180820/accessgrantsinstance/resourcepolicy",
		want: []string{"s3:PutAccessGrantsInstanceResourcePolicy " + s3ControlInstance}},
	{pattern: "GET /v20180820/accessgrantsinstance/resourcepolicy",
		want: []string{"s3:GetAccessGrantsInstanceResourcePolicy " + s3ControlInstance}},
	{pattern: "DELETE /v20180820/accessgrantsinstance/resourcepolicy",
		want: []string{"s3:DeleteAccessGrantsInstanceResourcePolicy " + s3ControlInstance}},
	{pattern: "POST /v20180820/accessgrantsinstance/location",
		want: []string{"s3:CreateAccessGrantsLocation " + s3ControlAnyLocation}},
	{pattern: "GET /v20180820/accessgrantsinstance/location/{locationId}", params: map[string]string{"locationId": "loc-1"},
		want: []string{"s3:GetAccessGrantsLocation " + s3ControlLocation}},
	{pattern: "PUT /v20180820/accessgrantsinstance/location/{locationId}", params: map[string]string{"locationId": "loc-1"},
		want: []string{"s3:UpdateAccessGrantsLocation " + s3ControlLocation}},
	{pattern: "DELETE /v20180820/accessgrantsinstance/location/{locationId}", params: map[string]string{"locationId": "loc-1"},
		want: []string{"s3:DeleteAccessGrantsLocation " + s3ControlLocation}},
	{pattern: "GET /v20180820/accessgrantsinstance/locations",
		want: []string{"s3:ListAccessGrantsLocations " + s3ControlInstance}},
	{pattern: "POST /v20180820/accessgrantsinstance/grant",
		want: []string{"s3:CreateAccessGrant " + s3ControlAnyGrant}},
	{pattern: "GET /v20180820/accessgrantsinstance/grant/{grantId}", params: map[string]string{"grantId": "g-1"},
		want: []string{"s3:GetAccessGrant " + s3ControlGrant}},
	{pattern: "DELETE /v20180820/accessgrantsinstance/grant/{grantId}", params: map[string]string{"grantId": "g-1"},
		want: []string{"s3:DeleteAccessGrant " + s3ControlGrant}},
	{pattern: "GET /v20180820/accessgrantsinstance/grants",
		want: []string{"s3:ListAccessGrants " + s3ControlInstance}},
	{pattern: "GET /v20180820/accessgrantsinstance/caller/grants",
		want: []string{"s3:ListCallerAccessGrants " + s3ControlInstance}},
	{pattern: "GET /v20180820/accessgrantsinstance/dataaccess",
		want: []string{"s3:GetDataAccess " + s3ControlInstance}},

	// Batch Operations.
	{pattern: "POST /v20180820/jobs", want: []string{"s3:CreateJob *"}},
	{pattern: "GET /v20180820/jobs", want: []string{"s3:ListJobs *"}},
	{pattern: "GET /v20180820/jobs/{jobId}", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:DescribeJob " + s3ControlJob}},
	{pattern: "POST /v20180820/jobs/{jobId}/priority", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:UpdateJobPriority " + s3ControlJob}},
	{pattern: "POST /v20180820/jobs/{jobId}/status", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:UpdateJobStatus " + s3ControlJob}},
	{pattern: "PUT /v20180820/jobs/{jobId}/tagging", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:PutJobTagging " + s3ControlJob}},
	{pattern: "GET /v20180820/jobs/{jobId}/tagging", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:GetJobTagging " + s3ControlJob}},
	{pattern: "DELETE /v20180820/jobs/{jobId}/tagging", params: map[string]string{"jobId": "j-1"},
		want: []string{"s3:DeleteJobTagging " + s3ControlJob}},

	// Multi-Region Access Points.
	{pattern: "POST /v20180820/async-requests/mrap/create", body: s3ControlMultiRegionXML,
		want: []string{"s3:CreateMultiRegionAccessPoint " + s3ControlMultiRegion}},
	{pattern: "POST /v20180820/async-requests/mrap/delete",
		body: `<DeleteMultiRegionAccessPointRequest><Details><Name>reports</Name></Details></DeleteMultiRegionAccessPointRequest>`,
		want: []string{"s3:DeleteMultiRegionAccessPoint " + s3ControlMultiRegion}},
	{pattern: "POST /v20180820/async-requests/mrap/put-policy",
		body: `<PutMultiRegionAccessPointPolicyRequest><Details><Name>reports</Name><Policy>{}</Policy></Details></PutMultiRegionAccessPointPolicyRequest>`,
		want: []string{"s3:PutMultiRegionAccessPointPolicy " + s3ControlMultiRegion}},
	{pattern: "GET /v20180820/async-requests/mrap/{token...}", params: map[string]string{"token": s3ControlAsyncRequest},
		want: []string{"s3:DescribeMultiRegionAccessPointOperation " + s3ControlAsyncRequest}},
	{pattern: "GET /v20180820/mrap/instances", want: []string{"s3:ListMultiRegionAccessPoints *"}},
	{pattern: "GET /v20180820/mrap/instances/{name}", params: map[string]string{"name": "reports"},
		want: []string{"s3:GetMultiRegionAccessPoint " + s3ControlMultiRegion}},
	{pattern: "GET /v20180820/mrap/instances/{name}/policy", params: map[string]string{"name": "reports"},
		want: []string{"s3:GetMultiRegionAccessPointPolicy " + s3ControlMultiRegion}},
	{pattern: "GET /v20180820/mrap/instances/{name}/policystatus", params: map[string]string{"name": "reports"},
		want: []string{"s3:GetMultiRegionAccessPointPolicyStatus " + s3ControlMultiRegion}},
	{pattern: "GET /v20180820/mrap/instances/{name}/routes", params: map[string]string{"name": "reports"},
		want: []string{"s3:GetMultiRegionAccessPointRoutes " + s3ControlMultiRegion}},
	{pattern: "PATCH /v20180820/mrap/instances/{name}/routes", params: map[string]string{"name": "reports"},
		want: []string{"s3:SubmitMultiRegionAccessPointRoutes " + s3ControlMultiRegion}},

	// Storage Lens.
	{pattern: "PUT /v20180820/storagelens/{configId}", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:PutStorageLensConfiguration *"}},
	{pattern: "GET /v20180820/storagelens/{configId}", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:GetStorageLensConfiguration " + s3ControlLens}},
	{pattern: "DELETE /v20180820/storagelens/{configId}", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:DeleteStorageLensConfiguration " + s3ControlLens}},
	{pattern: "GET /v20180820/storagelens", want: []string{"s3:ListStorageLensConfigurations *"}},
	{pattern: "PUT /v20180820/storagelens/{configId}/tagging", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:PutStorageLensConfigurationTagging " + s3ControlLens}},
	{pattern: "GET /v20180820/storagelens/{configId}/tagging", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:GetStorageLensConfigurationTagging " + s3ControlLens}},
	{pattern: "DELETE /v20180820/storagelens/{configId}/tagging", params: map[string]string{"configId": "sl-1"},
		want: []string{"s3:DeleteStorageLensConfigurationTagging " + s3ControlLens}},
	{pattern: "POST /v20180820/storagelensgroup",
		body: `<CreateStorageLensGroupRequest><StorageLensGroup><Name>segment</Name></StorageLensGroup></CreateStorageLensGroupRequest>`,
		want: []string{"s3:CreateStorageLensGroup *"}},
	// A group created with tags is also tagged, which the reference declares
	// as a second action on the group itself.
	{pattern: "POST /v20180820/storagelensgroup",
		body: `<CreateStorageLensGroupRequest><StorageLensGroup><Name>segment</Name></StorageLensGroup>` +
			`<Tags><Tag><Key>team</Key><Value>storage</Value></Tag></Tags></CreateStorageLensGroupRequest>`,
		want: []string{"s3:CreateStorageLensGroup *", "s3:TagResource " + s3ControlLensGroup}},
	{pattern: "GET /v20180820/storagelensgroup/{name}", params: map[string]string{"name": "segment"},
		want: []string{"s3:GetStorageLensGroup " + s3ControlLensGroup}},
	{pattern: "PUT /v20180820/storagelensgroup/{name}", params: map[string]string{"name": "segment"},
		want: []string{"s3:UpdateStorageLensGroup " + s3ControlLensGroup}},
	{pattern: "DELETE /v20180820/storagelensgroup/{name}", params: map[string]string{"name": "segment"},
		want: []string{"s3:DeleteStorageLensGroup " + s3ControlLensGroup}},
	{pattern: "GET /v20180820/storagelensgroup", want: []string{"s3:ListStorageLensGroups *"}},

	// The routes AWS authorizes out of another namespace: a directory
	// bucket's access-point scope and listing are s3express actions, and the
	// Outposts bucket listing is an s3-outposts one. A scope names the access
	// point in s3express's own ARN format; neither listing declares a resource
	// type, so both authorize "*".
	{pattern: "PUT /v20180820/accesspoint/{name}/scope", params: map[string]string{"name": "points"},
		want: []string{"s3express:PutAccessPointScope " + s3ExpressAccessPoint}},
	{pattern: "GET /v20180820/accesspoint/{name}/scope", params: map[string]string{"name": "points"},
		want: []string{"s3express:GetAccessPointScope " + s3ExpressAccessPoint}},
	{pattern: "DELETE /v20180820/accesspoint/{name}/scope", params: map[string]string{"name": "points"},
		want: []string{"s3express:DeleteAccessPointScope " + s3ExpressAccessPoint}},
	{pattern: "GET /v20180820/accesspointfordirectory",
		want: []string{"s3express:ListAccessPointsForDirectoryBuckets *"}},
	{pattern: "GET /v20180820/bucket", want: []string{"s3-outposts:ListRegionalBuckets *"}},

	// The shared tagging trio, authorized against the ARN it names.
	{pattern: "POST /v20180820/tags/{resourceArn...}", params: map[string]string{"resourceArn": s3ControlTaggedBucket},
		want: []string{"s3:TagResource " + s3ControlTaggedBucket}},
	{pattern: "DELETE /v20180820/tags/{resourceArn...}", params: map[string]string{"resourceArn": s3ControlLensGroup},
		want: []string{"s3:UntagResource " + s3ControlLensGroup}},
	{pattern: "GET /v20180820/tags/{resourceArn...}", params: map[string]string{"resourceArn": s3ControlAccessPoint},
		want: []string{"s3:ListTagsForResource " + s3ControlAccessPoint}},
}

func TestS3ControlRoutesDeriveTheirActionAndResource(t *testing.T) {
	// The expectations name a region, so the test fixes the one the simulator
	// builds its ARNs for.
	t.Setenv("SOCKERLESS_AWS_REGION", s3ControlTestRegion)

	routes := map[string]s3ControlRoute{}
	for _, route := range s3ControlGatedRoutes() {
		routes[route.pattern] = route
	}
	covered := map[string]bool{}
	for _, tc := range s3ControlDerivationCases {
		route, ok := routes[tc.pattern]
		if !ok {
			t.Errorf("%s: no gated route serves this pattern", tc.pattern)
			continue
		}
		covered[tc.pattern] = true
		t.Run(tc.pattern+" "+strings.Join(tc.want, ","), func(t *testing.T) {
			r := s3ControlRouteRequest(route, tc.params, tc.body)
			got := targetStrings(s3ControlAuthorizationTargets(r, route.operation, route.resource))
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("targets = %v, want %v", got, want)
			}
			// The gate reads a request's document without consuming it.
			if rest, _ := io.ReadAll(r.Body); string(rest) != tc.body {
				t.Fatalf("the handler would read %q, want the whole body", rest)
			}
		})
	}
	for pattern := range routes {
		if !covered[pattern] {
			t.Errorf("%s is gated but no case says what it authorizes", pattern)
		}
	}
}

// s3ServiceReference is the vendored Amazon S3 Service Reference, the
// authority on which action an operation is authorized as and which resource
// types that action supports.
func s3ServiceReference(t *testing.T, service string) (actions map[string][]string, operations map[string][]string) {
	t.Helper()
	f, err := os.Open("../specs/cloud-api/aws/service-reference/" + service + ".servicereference.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var ref struct {
		Actions []struct {
			Name      string
			Resources []struct{ Name string }
		}
		Operations []struct {
			Name              string
			AuthorizedActions []struct{ Name, Service string }
		}
	}
	if err := json.NewDecoder(zr).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	actions = map[string][]string{}
	for _, action := range ref.Actions {
		types := []string{}
		for _, resource := range action.Resources {
			types = append(types, resource.Name)
		}
		actions[action.Name] = types
	}
	operations = map[string][]string{}
	for _, op := range ref.Operations {
		for _, action := range op.AuthorizedActions {
			if action.Service == service {
				operations[op.Name] = append(operations[op.Name], action.Name)
			}
		}
	}
	return actions, operations
}

// A gated route authorizes a resource exactly when AWS declares a resource
// type for its action, and "*" when AWS declares none. Refreshing the vendored
// reference moves either answer, and this is what says so.
func TestS3ControlRoutesAuthorizeWhatTheReferenceDeclares(t *testing.T) {
	// A route is crossed against the reference of the namespace it names, so
	// an s3express action is looked for where AWS publishes it rather than
	// among the s3 ones.
	references := map[string][2]map[string][]string{}
	reference := func(service string) (map[string][]string, map[string][]string) {
		if cached, ok := references[service]; ok {
			return cached[0], cached[1]
		}
		actions, operations := s3ServiceReference(t, service)
		references[service] = [2]map[string][]string{actions, operations}
		return actions, operations
	}
	for _, route := range s3ControlGatedRoutes() {
		service, action := s3ControlRouteService(route.operation)
		actions, operations := reference(service)
		if _, isAction := actions[action]; !isAction {
			mapped := operations[action]
			if len(mapped) != 1 {
				t.Errorf("%s: the reference authorizes %s as %v, which names no single action",
					route.pattern, route.operation, mapped)
				continue
			}
			action = mapped[0]
		}
		types, declared := actions[action]
		if !declared {
			t.Errorf("%s: the reference declares no %s:%s action", route.pattern, service, action)
			continue
		}
		switch {
		case len(types) == 0 && route.resource != nil:
			t.Errorf("%s authorizes a resource, but the reference declares none for %s:%s",
				route.pattern, service, action)
		case len(types) > 0 && route.resource == nil:
			t.Errorf("%s authorizes \"*\", but the reference declares %v for %s:%s",
				route.pattern, types, service, action)
		}
	}
}

// s3ControlUngatedRoutes are the control-plane routes that carry no gate, and
// why each one cannot. A route earns a place here only when no vendored
// document declares the action its operation is authorized as, or declares a
// resource this simulator cannot name -- not merely because the action lives
// outside the s3 namespace, which a route now states for itself.
var s3ControlUngatedRoutes = map[string]string{
	"DELETE /v20180820/bucket/{bucket}/lifecycleconfiguration": "no vendored document declares an action for the control plane's " +
		"DeleteBucketLifecycleConfiguration: the s3 reference lists the operation with an empty authorized-action set, and " +
		"s3-outposts declares only Get/PutLifecycleConfiguration, whose resource type is an Outposts bucket whose ARN carries " +
		"an OutpostId this simulator models nothing of",
}

// Every S3 control-plane route the simulator serves is either gated or listed
// as one that cannot be, so a route added later cannot slip through
// unauthorized without saying why.
func TestEveryS3ControlRouteIsGatedOrDeclaredUngated(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)
	gated := map[string]bool{}
	for _, route := range s3ControlGatedRoutes() {
		gated[route.pattern] = true
	}
	for _, pattern := range srv.RoutePatterns() {
		_, path, _ := strings.Cut(pattern, " ")
		if !strings.HasPrefix(path, "/v20180820/") {
			continue
		}
		if gated[pattern] {
			continue
		}
		if _, listed := s3ControlUngatedRoutes[pattern]; !listed {
			t.Errorf("%s passes through no IAM enforcement and is not listed as ungated", pattern)
		}
	}
	for pattern := range s3ControlUngatedRoutes {
		if gated[pattern] {
			t.Errorf("%s is listed as ungated but is gated", pattern)
		}
	}
}
