package main

import (
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// s3ImplementedOps enumerates the S3 operations the sim implements. S3 is REST:
// its operation isn't a router registration but is composed at request time from
// method + path + the query subresource (s3BucketOperationName /
// s3ObjectOperationName). So we drive those functions over the method ×
// subresource matrix and collect the operation names they yield — the REST
// analogue of reading the awsJson/query routers.
func s3ImplementedOps() map[string]bool {
	ops := map[string]bool{"ListBuckets": true}
	bucketReq := func(method, rawquery string) string {
		r := httptest.NewRequest(method, "/bucket?"+rawquery, nil)
		return s3BucketOperationName(r, nil)
	}
	objReq := func(method, rawquery string, hdr map[string]string) string {
		r := httptest.NewRequest(method, "/bucket/key?"+rawquery, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return s3ObjectOperationName(r, nil)
	}
	add := func(op string) {
		if op != "" {
			ops[op] = true
		}
	}

	bucketSubresources := []string{
		"acl", "cors", "lifecycle", "policy", "versioning", "website", "logging",
		"requestPayment", "accelerate", "replication", "encryption", "tagging",
		"notification", "publicAccessBlock", "object-lock", "ownershipControls",
		"intelligent-tiering", "inventory", "analytics", "metrics",
		"uploads", "versions", "location", "policyStatus", "delete",
		"metadataConfiguration", "metadataTable", "metadataInventoryTable",
		"metadataJournalTable", "metadataAnnotationTable", "abac",
	}
	for _, m := range []string{"GET", "PUT", "DELETE", "HEAD", "POST"} {
		add(bucketReq(m, ""))
		for _, sr := range bucketSubresources {
			add(bucketReq(m, sr+"="))
			add(bucketReq(m, sr+"=&id=x"))
		}
	}
	// ListObjectsV2 is the same GET-bucket request with list-type=2 (a bare GET
	// resolves to the V1 ListObjects).
	add(bucketReq("GET", "list-type=2"))
	objQueries := []string{
		"", "tagging=", "uploads=", "uploadId=x", "uploadId=x&partNumber=1",
		"acl=", "retention=", "legal-hold=", "attributes=", "torrent=", "restore=", "select=",
		"renameObject=", "encryption=",
		// ?annotation resolves to the single-annotation ops when annotationName
		// names one, and to ListObjectAnnotations when it does not.
		"annotation=", "annotation=&annotationName=a",
	}
	for _, m := range []string{"GET", "PUT", "DELETE", "HEAD", "POST"} {
		for _, q := range objQueries {
			add(objReq(m, q, nil))
		}
	}
	add(objReq("PUT", "", map[string]string{"x-amz-copy-source": "/src/key"}))
	// UploadPartCopy is an UploadPart request (uploadId+partNumber) carrying an
	// x-amz-copy-source header.
	add(objReq("PUT", "uploadId=x&partNumber=1", map[string]string{"x-amz-copy-source": "/src/key"}))

	// The sim composes some op names with a "Bucket" infix that the real S3 API
	// omits (GetBucketPublicAccessBlock vs the API's GetPublicAccessBlock) and
	// names a couple of subresources more verbosely; expose the API-canonical
	// aliases so functional coverage isn't undercounted by a naming difference.
	for op := range ops {
		if alias := strings.Replace(op, "BucketPublicAccessBlock", "PublicAccessBlock", 1); alias != op {
			ops[alias] = true
		}
	}
	if ops["DeleteBucketLifecycleConfiguration"] {
		ops["DeleteBucketLifecycle"] = true
	}
	return ops
}

// Service API conformance: a spec-derived, mechanical measure of how completely a
// sim service implements its real AWS operation surface, so gaps are *measured*
// (and ratcheted) rather than discovered later by a consumer. This is the
// service-API analogue of the IAM policy-engine conformance gate
// (iam_conformance_test.go); the repeatable process is docs/SERVICE_CONFORMANCE.md.
//
// For a service it loads the vendored Smithy model (the authoritative operation
// list), computes which operations the sim's routers register, and reports
// coverage. TestServiceConformance_Ratchet locks each service's set of
// not-yet-implemented operations: the list IS the live non-conformity report,
// and an op that gets implemented (or a model that grows) must be reflected here.

// serviceConformanceCatalog maps a service's Smithy shape name to the set of its
// real operations the sim does NOT implement yet — the tracked non-conformities.
// The ratchet locks this; implement an op (or grow the model) and the entry must
// shrink/grow with it. Discovered + maintained via TestServiceConformance_Coverage.
//
// Scope: the awsJson (X-Amz-Target) + awsQuery (versioned Action) services, whose
// operations are introspectable from the routers. REST services compose their
// operation from method + path + query subresource at request time (e.g. S3's
// op-name is `Get/Put/Delete` + a subresource suffix, not a router registration),
// so they are measured by their own enumeration harness instead — S3 via
// s3ImplementedOps + TestServiceConformance_S3Ratchet.
var serviceConformanceCatalog = map[string][]string{
	// CloudWatch monitoring: all operations implemented.
	"GraniteServiceVersion20100801": {},
	// Organizations: all operations implemented.
	"AWSOrganizationsV20161128": {},
	// AWS Budgets: all operations implemented (budgets, notifications,
	// subscribers, and the budget-action family).
	"AWSBudgetServiceGateway": {},
	// SSM: all operations implemented (Parameter Store + documents + maintenance
	// windows + patch baselines/groups + service settings + resource data sync +
	// associations + automation + run command + ops-items + sessions + activations +
	// inventory + compliance + nodes + ops-metadata + resource policies).
	"AmazonSSM": {},
	// Step Functions / Secrets Manager / Application Auto Scaling: all
	// operations implemented (conformance-complete).
	"AWSStepFunctions": {},
	// AWS Certificate Manager: all certificate and RFC 8555 ACME control-plane
	// operations are implemented.
	"CertificateManager":      {},
	"ACMPrivateCA":            {},
	"Firehose_20150804":       {},
	"secretsmanager":          {},
	"AnyScaleFrontendService": {},
	// Kinesis: all operations implemented (SubscribeToShard streams over vnd.amazon.eventstream).
	"Kinesis_20131202": {},
	// KMS: all operations implemented (real Go-stdlib crypto for Sign/Verify/MAC/
	// data-key-pairs/ECDH; custom key stores; grants; multi-region keys).
	"TrentService": {},
	// ELBv2: all operations implemented (the mutual-TLS trust-store surface closed).
	"ElasticLoadBalancing_v10": {},
	"AmazonSQS":                {},
	// SNS / EventBridge / DynamoDB: all remaining operations are implemented.
	"AmazonSimpleNotificationService": {},
	"AWSEvents":                       {},
	// Amazon DynamoDB: all operations implemented, vector search included.
	"DynamoDB_20120810": {},
	// ECS: all real operations are implemented.
	"AmazonEC2ContainerServiceV20141113": {},
}

// legacyQueryRegistrars maps a service's Smithy service-shape name to the
// registration entry point that mounts that service's query-protocol actions.
//
// The AWSQueryRouter's unversioned bucket (version "") holds every action
// registered through Register(action, handler) rather than RegisterVersioned:
// services whose action names are globally unique across AWS (Amazon EC2, AWS
// Identity and Access Management, AWS Security Token Service, and Amazon
// CloudWatch's query surface) mount there. That bucket carries NO service
// identity of its own — the key is the empty string, not a service — so the
// only way to learn which service owns an action in it is to observe which
// registration entry point mounted it. Each entry point below is therefore run
// against a FRESH router and the actions it mounts in the unversioned bucket
// are recorded as that service's (see legacyQueryOwnership). An action nobody
// here claims stays unattributed and is credited to NO service, so a service
// can never be credited with an operation it does not serve.
var legacyQueryRegistrars = map[string]func(r *sim.AWSQueryRouter, srv *sim.Server){
	"AmazonEC2":                        registerEC2,
	"AWSIdentityManagementV20100508":   registerIAM,
	"AWSSecurityTokenServiceV20110615": registerSTS,
	// Amazon CloudWatch's query surface is split across several registrars;
	// together they are the service's unversioned-bucket registration.
	"GraniteServiceVersion20100801": func(r *sim.AWSQueryRouter, _ *sim.Server) {
		registerCloudWatchMetricsQuery(r)
		registerCloudWatchAlarmsQuery(r)
		registerCloudWatchAlarmOpsQuery(r)
		registerCloudWatchMetricStreamsQuery(r)
		registerCloudWatchAnomalyInsightQuery(r)
		registerCloudWatchMiscQuery(r)
		registerCloudWatchDashboardsQuery(r)
	},
}

// legacyQueryOwnership measures who owns each action in the query router's
// unversioned bucket. It returns action → owning service shape for every action
// exactly one registrar mounts; `unattributed` lists the bucket's actions no
// registrar in legacyQueryRegistrars claims, and `ambiguous` lists actions more
// than one registrar mounts (the last registration wins at runtime, so the sim
// cannot serve both — neither service is credited).
func legacyQueryOwnership(t *testing.T, srv *sim.Server, bucket []string) (owner map[string]string, unattributed, ambiguous []string) {
	t.Helper()
	owner = map[string]string{}
	claimedBy := map[string][]string{}
	for shape, register := range legacyQueryRegistrars {
		probe := sim.NewAWSQueryRouter()
		register(probe, srv)
		for _, action := range probe.VersionedActions()[""] {
			claimedBy[action] = append(claimedBy[action], shape)
		}
	}
	contested := map[string]bool{}
	for action, shapes := range claimedBy {
		if len(shapes) > 1 {
			sort.Strings(shapes)
			contested[action] = true
			ambiguous = append(ambiguous, action+" ("+strings.Join(shapes, ", ")+")")
			continue
		}
		owner[action] = shapes[0]
	}
	for _, action := range bucket {
		if _, ok := owner[action]; !ok && !contested[action] {
			unattributed = append(unattributed, action)
		}
	}
	sort.Strings(unattributed)
	sort.Strings(ambiguous)
	return owner, unattributed, ambiguous
}

// conformanceRouters is the measured registration surface the conformance gate
// reads: the awsJson targets, the query router's (version → actions) table, and
// the measured ownership of the query router's unversioned bucket.
type conformanceRouters struct {
	jsonTargets  []string
	versioned    map[string][]string
	legacyOwners map[string]string
}

// conformanceRegistrations builds the simulator and measures its registration
// surface, including honest ownership of the unversioned query bucket.
func conformanceRegistrations(t *testing.T) conformanceRouters {
	t.Helper()
	srv, jsonRouter, queryRouter := buildConformanceSimulator(t)
	versioned := queryRouter.VersionedActions()
	owners, _, _ := legacyQueryOwnership(t, srv, versioned[""])
	return conformanceRouters{
		jsonTargets:  jsonRouter.Targets(),
		versioned:    versioned,
		legacyOwners: owners,
	}
}

// TestServiceConformance_LegacyQueryAttribution keeps the unversioned query
// bucket honest: every action in it must be attributable to exactly one
// service. A new service that starts mounting actions there without being
// listed in legacyQueryRegistrars fails here rather than silently going
// uncredited (or, worse, being credited to everyone).
func TestServiceConformance_LegacyQueryAttribution(t *testing.T) {
	srv, _, queryRouter := buildConformanceSimulator(t)
	_, unattributed, ambiguous := legacyQueryOwnership(t, srv, queryRouter.VersionedActions()[""])
	if len(unattributed) > 0 {
		t.Errorf("%d unversioned query action(s) belong to no registrar in legacyQueryRegistrars — add the owning service's registration entry point: %v",
			len(unattributed), unattributed)
	}
	if len(ambiguous) > 0 {
		t.Errorf("%d unversioned query action(s) are mounted by more than one service — one handler shadows the other at runtime; give the collision-prone action a RegisterVersioned registration: %v",
			len(ambiguous), ambiguous)
	}
}

// TestServiceConformance_LegacyQueryShadowing guards the wire consequence of
// the unversioned bucket: AWSQueryRouter.ServeHTTP falls back to it when the
// request's Version has no handler for the action, so an operation a
// query-protocol service does NOT register for its own API version, but which
// another service owns in the unversioned bucket, is answered by that other
// service's handler. The caller then gets a well-formed response from the wrong
// service instead of the InvalidAction its unimplemented operation should
// produce — and the gap looks implemented. Every such operation must get the
// owning service's own RegisterVersioned registration.
func TestServiceConformance_LegacyQueryShadowing(t *testing.T) {
	models := loadSmithyModels(t)
	routers := conformanceRegistrations(t)
	for _, m := range models {
		if m.Version == "" || len(routers.versioned[m.Version]) == 0 {
			continue
		}
		own := map[string]bool{}
		for _, a := range routers.versioned[m.Version] {
			own[a] = true
		}
		var shadowed []string
		for op := range m.Ops {
			if own[op] {
				continue
			}
			if owner, ok := routers.legacyOwners[op]; ok && owner != m.ShapeName {
				shadowed = append(shadowed, op+" (would be served by "+owner+")")
			}
		}
		sort.Strings(shadowed)
		if len(shadowed) > 0 {
			t.Errorf("%s (Version=%s): %d operation(s) fall through to another service's unversioned handler — register them with RegisterVersioned(%q, …): %v",
				m.ShapeName, m.Version, len(shadowed), m.Version, shadowed)
		}
	}
}

// serviceRegisteredOps returns the set of operations the sim registers for the
// given Smithy model, across the awsJson (X-Amz-Target) and awsQuery routers.
func serviceRegisteredOps(m *smithyService, r conformanceRouters) map[string]bool {
	out := map[string]bool{}
	for _, target := range r.jsonTargets {
		i := strings.LastIndex(target, ".")
		if i < 0 {
			continue
		}
		prefix, op := target[:i], target[i+1:]
		// The target prefix is the service shape name, either bare (what the Go
		// SDK sends) or namespace-qualified (what botocore sends for services
		// whose legacy targetPrefix is the java-style name,
		// com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101). Both forms
		// identify exactly one service, which is what makes awsJson targets
		// self-attributing; a looser match would credit one service's target to
		// another whose shape name merely resembles it.
		// TestJSONTargetsExistInSmithyModels holds every registered target to
		// this same resolution.
		if prefix == m.ShapeName || (strings.Contains(prefix, ".") && prefix[strings.LastIndex(prefix, ".")+1:] == m.ShapeName) {
			out[op] = true
		}
	}
	// The versioned bucket is keyed by the service's own API version, so it is
	// self-attributing. The empty-string key is the unversioned bucket, not a
	// service's version — never read it here.
	if m.Version != "" {
		for _, a := range r.versioned[m.Version] {
			out[a] = true
		}
	}
	// The unversioned bucket is attributed by measured ownership: an action
	// counts for this service only if this service's registrar mounted it.
	for action, owner := range r.legacyOwners {
		if owner == m.ShapeName {
			out[action] = true
		}
	}
	return out
}

// serviceCoverage computes a model's operation coverage: the ops registered and
// the ops still missing (model ∖ registered), sorted.
func serviceCoverage(m *smithyService, r conformanceRouters) (registered, missing []string) {
	reg := serviceRegisteredOps(m, r)
	for op := range m.Ops {
		if reg[op] {
			registered = append(registered, op)
		} else {
			missing = append(missing, op)
		}
	}
	sort.Strings(registered)
	sort.Strings(missing)
	return registered, missing
}

// restConformanceSources maps a REST service's Smithy shape to its CloudTrail
// event source, so the gate reads restRegisteredOps (populated at registration
// by cloudTrailRecordedREST) for its operation coverage — the REST analogue of
// reading the awsJson/awsQuery routers.
var restConformanceSources = map[string]string{
	"Cloudfront2020_05_31":         "cloudfront.amazonaws.com", // Amazon CloudFront
	"ApiGatewayV2":                 "apigateway.amazonaws.com", // Amazon API Gateway v2
	"AWSGirApiService":             "lambda.amazonaws.com",     // AWS Lambda
	"AWSBatchV20160810":            "batch.amazonaws.com",      // AWS Batch
	"BackplaneControlService":      "apigateway.amazonaws.com", // Amazon API Gateway
	"Amplify":                      "amplify.amazonaws.com",
	"AWSChronosService":            "scheduler.amazonaws.com",         // EventBridge Scheduler
	"AWSDnsV20130401":              "route53.amazonaws.com",           // Amazon Route 53
	"MagnolioAPIService_v20150201": "elasticfilesystem.amazonaws.com", // Amazon EFS
}

// serviceImplementedCount returns how many of a model's operations the sim
// implements — from restRegisteredOps for a REST service, else from the routers.
func serviceImplementedCount(m *smithyService, r conformanceRouters) int {
	if src, ok := restConformanceSources[m.ShapeName]; ok {
		n := 0
		for op := range m.Ops {
			if restRegisteredOps[src][op] {
				n++
			}
		}
		return n
	}
	registered, _ := serviceCoverage(m, r)
	return len(registered)
}

// serviceCoverageFloor locks the implemented-operation COUNT for services tracked
// by coverage rather than an exact missing-list: the awsQuery/ec2Query giants
// (Amazon EC2, RDS, Glue, …) whose hundreds of unimplemented ops would bloat the
// catalog, and the REST services (Route 53, EFS) measured via restRegisteredOps.
// The count must EQUAL the floor — a drop is a regression; implementing more ops
// must bump the floor (the ratchet ratchets up). Every count here is the number
// of operations THIS service registers: an operation another service mounts in
// the query router's unversioned bucket is never counted for it (see
// legacyQueryRegistrars).
var serviceCoverageFloor = map[string]int{
	"AmazonEC2":                            801, // ec2Query
	"AWSBudgetServiceGateway":              26,  // AWS Budgets (awsJson1.1)
	"AWSSecurityTokenServiceV20110615":     11,  // STS (awsQuery, unversioned)
	"AWSIdentityManagementV20100508":       180, // IAM (awsQuery, unversioned)
	"AmazonEC2ContainerRegistry_V20150921": 58,  // ECR
	"AmazonElastiCacheV9":                  75,
	"AmazonRDSv19":                         164,
	"AutoScaling_2011_01_01":               66,
	"AWSGlue":                              299,
	"AWSWAF_20190729":                      59,
	"CloudTrail_20131101":                  60,
	"CodeBuild_20161006":                   59,
	"Logs_20140328":                        118, // CloudWatch Logs
	"Route53AutoNaming_v20170314":          30,  // Cloud Map / ServiceDiscovery
	"AWSDnsV20130401":                      71,  // Route 53 (REST)
	"MagnolioAPIService_v20150201":         31,  // EFS (REST)
	// restJson1 services measured via the REST registry (Part B).
	"AWSGirApiService":        85,  // AWS Lambda
	"AWSBatchV20160810":       45,  // AWS Batch
	"BackplaneControlService": 124, // Amazon API Gateway
	"Amplify":                 37,
	"AWSChronosService":       12,  // EventBridge Scheduler
	"ApiGatewayV2":            103, // Amazon API Gateway v2
	"Cloudfront2020_05_31":    167, // Amazon CloudFront (restXml)
	"DynamoDB_20120810":       58,  // Amazon DynamoDB (awsJson1.0, vector search included)
}

// TestServiceConformance_CoverageFloor locks the implemented-op count for the
// coverage-tracked services (see serviceCoverageFloor).
func TestServiceConformance_CoverageFloor(t *testing.T) {
	models := loadSmithyModels(t)
	routers := conformanceRegistrations(t)
	for shape, floor := range serviceCoverageFloor {
		m := serviceModel(t, models, shape)
		impl := serviceImplementedCount(m, routers)
		if impl != floor {
			t.Errorf("%s: coverage %d/%d != floor %d — update serviceCoverageFloor (a drop is a regression; more is a ratchet-up).", shape, impl, len(m.Ops), floor)
		}
	}
}

func serviceModel(t *testing.T, models []*smithyService, shapeName string) *smithyService {
	t.Helper()
	for _, m := range models {
		if m.ShapeName == shapeName {
			return m
		}
	}
	t.Fatalf("no vendored Smithy model with service shape %q (run scripts/fetch-aws-spec.sh)", shapeName)
	return nil
}

// TestServiceConformance_Coverage reports, for each catalogued service, how many
// of its real operations the sim implements — a measured number, logged.
func TestServiceConformance_Coverage(t *testing.T) {
	models := loadSmithyModels(t)
	routers := conformanceRegistrations(t)

	reported := map[string]bool{}
	for shape := range serviceConformanceCatalog {
		m := serviceModel(t, models, shape)
		registered, missing := serviceCoverage(m, routers)
		t.Logf("%s: %d/%d operations implemented; missing (%d): %v",
			shape, len(registered), len(m.Ops), len(missing), missing)
		reported[shape] = true
	}
	for shape := range serviceCoverageFloor {
		if reported[shape] {
			continue
		}
		m := serviceModel(t, models, shape)
		if _, ok := restConformanceSources[shape]; ok {
			var missing []string
			for op := range m.Ops {
				if !restRegisteredOps[restConformanceSources[shape]][op] {
					missing = append(missing, op)
				}
			}
			sort.Strings(missing)
			t.Logf("%s: %d/%d operations implemented; missing (%d): %v",
				shape, len(m.Ops)-len(missing), len(m.Ops), len(missing), missing)
			continue
		}
		registered, missing := serviceCoverage(m, routers)
		t.Logf("%s: %d/%d operations implemented; missing (%d): %v",
			shape, len(registered), len(m.Ops), len(missing), missing)
	}
}

// s3ConformanceMissing is S3's ratchet: the three modelled S3 operations the
// simulator does not serve. Every other operation in the vendored model is
// implemented, so this list is the whole of S3's gap and it is not a backlog.
//
// READ THIS BEFORE ADDING THEM. All three are **scoped out by decision**, not
// left undone by oversight, and the reason is the same for each: none of them
// is addressed to the regional `s3.<region>.amazonaws.com` surface the
// simulator hosts. The Smithy model routes each to a different endpoint family
// through endpoint rules, so "implement the operation" is really "host another
// endpoint family and build the feature behind it":
//
//   - WriteGetObjectResponse is the S3 Object Lambda callback. A client never
//     calls it; an Object Lambda *function* calls it, on a per-request
//     {RequestRoute}.s3-object-lambda.<region> host (the model marks it
//     UseObjectLambdaEndpoint). Serving it standalone would be a handler with
//     no caller — the operation only means anything once Object Lambda access
//     points exist and route GetObject through a Lambda that then calls back.
//
//   - CreateSession and ListDirectoryBuckets are S3 Express One Zone
//     operations, served from s3express-control.<region> and the zonal
//     bucket endpoints (the model marks them smithy.rules#staticContextParams).
//     They only mean anything once directory buckets exist as their own bucket
//     type — distinct naming, zonal endpoints, and session-token authentication
//     where the credentials CreateSession mints must actually verify on the
//     requests that follow. A CreateSession that returned credentials nothing
//     checks would be exactly the kind of fake this project forbids.
//
// Neither feature has a consumer here. Sockerless assembles the Docker and
// Podman API from cloud primitives; no backend, agent, runner, console, or test
// path uses S3 Express or Object Lambda, so building either would add surface
// for the coverage number alone — and both would add a whole endpoint family
// plus its feature semantics to keep faithful forever after.
//
// The condition to revisit is a real consumer, not a coverage sweep: a client
// that stores workload state in a directory bucket, or one that reads through
// an Object Lambda access point. If that
// happens, implement the *feature* — the endpoint family, the bucket type or
// the access point, and the authentication — and remove the entry here; do not
// mount a bare handler on the regional surface, which no real client would
// ever reach.
//
// The list is locked by TestServiceConformance_S3Ratchet.
var s3ConformanceMissing = []string{
	"WriteGetObjectResponse",
	"ListDirectoryBuckets",
	"CreateSession",
}

// TestServiceConformance_S3Ratchet locks S3's REST operation-coverage gap set,
// measured by driving the request→operation-name composition over the method ×
// subresource matrix (s3ImplementedOps).
func TestServiceConformance_S3Ratchet(t *testing.T) {
	models := loadSmithyModels(t)
	m := serviceModel(t, models, "AmazonS3")
	impl := s3ImplementedOps()
	want := map[string]bool{}
	for _, op := range s3ConformanceMissing {
		want[op] = true
	}
	var newlyMissing, nowImplemented []string
	for op := range m.Ops {
		if !impl[op] && !want[op] {
			newlyMissing = append(newlyMissing, op)
		}
	}
	for op := range want {
		if impl[op] {
			nowImplemented = append(nowImplemented, op)
		}
	}
	sort.Strings(newlyMissing)
	sort.Strings(nowImplemented)
	if len(newlyMissing) > 0 {
		t.Errorf("AmazonS3: %d op(s) missing that the ratchet doesn't list — classify in s3ConformanceMissing: %v", len(newlyMissing), newlyMissing)
	}
	if len(nowImplemented) > 0 {
		t.Errorf("AmazonS3: %d op(s) now implemented — remove from s3ConformanceMissing: %v", len(nowImplemented), nowImplemented)
	}
}

// TestServiceConformance_S3Coverage reports S3's REST operation coverage.
func TestServiceConformance_S3Coverage(t *testing.T) {
	models := loadSmithyModels(t)
	m := serviceModel(t, models, "AmazonS3")
	impl := s3ImplementedOps()
	var missing []string
	for op := range m.Ops {
		if !impl[op] {
			missing = append(missing, op)
		}
	}
	sort.Strings(missing)
	// "missing" reads as a backlog and invites someone to close it. These
	// three are scoped out by decision — each belongs to an endpoint family
	// this simulator does not host — and s3ConformanceMissing carries the
	// reasoning and the condition that would justify revisiting it.
	t.Logf("AmazonS3: %d/%d operations implemented; %d not served by decision (see s3ConformanceMissing): %v",
		len(m.Ops)-len(missing), len(m.Ops), len(missing), missing)
}

// TestServiceConformance_Ratchet locks each catalogued service's set of
// not-yet-implemented operations. The recorded list IS the live non-conformity
// report: implement one → remove it here and the test passes with fewer gaps;
// a model update that adds an op fails until it's classified. This is what makes
// "is service X complete?" a measured, enforced number instead of a claim.
func TestServiceConformance_Ratchet(t *testing.T) {
	models := loadSmithyModels(t)
	routers := conformanceRegistrations(t)

	for shape, want := range serviceConformanceCatalog {
		m := serviceModel(t, models, shape)
		_, missing := serviceCoverage(m, routers)
		wantSet := map[string]bool{}
		for _, op := range want {
			wantSet[op] = true
		}
		gotSet := map[string]bool{}
		for _, op := range missing {
			gotSet[op] = true
		}
		var newlyMissing, nowImplemented []string
		for op := range gotSet {
			if !wantSet[op] {
				newlyMissing = append(newlyMissing, op)
			}
		}
		for op := range wantSet {
			if !gotSet[op] {
				nowImplemented = append(nowImplemented, op)
			}
		}
		sort.Strings(newlyMissing)
		sort.Strings(nowImplemented)
		if len(newlyMissing) > 0 {
			t.Errorf("%s: %d operation(s) missing that the ratchet doesn't list — classify them in serviceConformanceCatalog: %v", shape, len(newlyMissing), newlyMissing)
		}
		if len(nowImplemented) > 0 {
			t.Errorf("%s: %d operation(s) are now implemented — remove them from serviceConformanceCatalog so the gap count drops: %v", shape, len(nowImplemented), nowImplemented)
		}
	}
}
