package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// GCP operation-coverage gate — the Discovery-document analogue of the AWS
// service-conformance ratchet (simulator-aws/service_conformance_test.go).
//
// For each vendored Google Discovery document it counts how many of the
// service's REST methods the simulator actually SERVES. Coverage is measured
// by probing the running simulator: every documented method is rendered into a
// concrete request URI (path parameters filled from the document's own
// parameter patterns), sent through the same handler chain main() serves —
// bearer middleware included — and the response is classified by
// gcpClassifyProbe. A method counts only when a mounted handler answered it.
// The count is locked by gcpMethodFloor: a drop is a regression, an increase
// is a ratchet-up that must bump the floor. This makes GCP coverage a
// measured, gated number rather than something discovered later by a consumer.
//
// Probing is what makes the number honest. Matching a documented method
// against the registered route patterns by shape alone counts a method as
// covered whenever ANY pattern could spell its URI — a {+name} template landing
// under an unrelated mounted pattern, or a colon-verb fan-in handler that
// matches every custom method on a resource and then rejects the verb. Those
// are phantoms: no handler serves the method. Only the response says whether
// one does.
//
// The bulk of Compute Engine's ~2000-method surface (and other large
// data-plane/admin surfaces) is intentionally far from 100% — the floor records
// the honest implemented count, not an aspiration.

// gcpMethodFloor locks the served-method COUNT per Discovery document (keyed by
// file name without the .discovery.json.gz suffix). Serve a method (or grow the
// vendored doc) and the matching floor must move with it.
//
// A count moves for one of two reasons, and both must be spelled out where the
// count changes: the simulator started (or stopped) serving a method, or the
// vendored document itself added or withdrew one. Google withdraws methods — a
// count that drops purely because a method left the document is not a simulator
// regression, and the comment on that entry says which method left.
//
// A document's count is over method SPELLINGS: Discovery describes most methods
// twice, as an expanded flatPath and as a {+name} template, and both are real
// descriptions of the same URI. Both render to the same concrete URI and are
// probed the same way, so a served method contributes both of its spellings and
// an unserved one contributes neither.
// gcpDeclaredMethodTotals locks each vendored Discovery document's declared
// method-spelling count. The served floor above cannot see the failure mode
// this closes: a re-vendored document that ADDS methods leaves every served
// count unchanged, so the floors stay green while the new methods sit
// silently unserved — exactly how forty-three AWS operations drifted
// unnoticed between 2026-08-12 and 2026-08-23 before that simulator's model
// drift gate existed. A changed total fails here and forces the decision:
// serve the new methods, or record why not in the floor comment — then
// update both tables together.
var gcpDeclaredMethodTotals = map[string]int{
	"apigateway-v1":           60,
	"artifactregistry-v1":     147,
	"bigquery-v2":             95,
	"bigtableadmin-v2":        164,
	"cloudbilling-v1":         36,
	"cloudbuild-v1":           114,
	"cloudfunctions-v2":       42,
	"cloudkms-v1":             172,
	"cloudresourcemanager-v1": 76,
	"cloudresourcemanager-v2": 24,
	"cloudresourcemanager-v3": 126,
	"cloudrun-v1":             152,
	"cloudrun-v2":             119,
	"compute-v1":              2014,
	"dataflow-v1b3":           84,
	"dns-v1":                  80,
	"eventarc-v1":             132,
	"firestore-v1":            120,
	"iam-v1":                  266,
	"iamcredentials-v1":       14,
	"logging-v2":              508,
	"pubsub-v1":               92,
	"redis-v1":                94,
	"secretmanager-v1":        72,
	"serviceusage-v1":         20,
	"spanner-v1":              198,
	"sqladmin-v1":             150,
	"sqladmin-v1beta4":        150,
	"storage-v1":              89,
	"vpcaccess-v1":            16,
}

var gcpMethodFloor = map[string]int{
	// Compute Engine: deliberately the furthest from full — 559 of the
	// document's 1,007 methods. The served slice is the one the consumers
	// exercise (instances, disks, networks/subnetworks and the real netns
	// fabric, firewalls, addresses, routes, NAT, load balancing, instance
	// groups/templates, project metadata, zones/regions/machine types); the
	// 448 unserved methods are the long tail of collections nothing here
	// consumes (commitments, interconnects, node groups, security policies,
	// TPUs and the rest). There is no per-method enumeration: lower this
	// floor by one and the gate prints the full unserved list on demand,
	// which is the work list whenever a slice widens.
	"compute-v1":              1118,
	"cloudresourcemanager-v3": 126,

	// Cloud Resource Manager v2: every documented method is served. v2's only
	// collection is folders — the wire gcloud's `resource-manager folders`
	// group speaks — over the same folder store v3 serves; its operations.get
	// is spelled at the v1 path, which the v1 routes already answer.
	"cloudresourcemanager-v2": 24,
	"bigtableadmin-v2":        164,

	// Cloud Run Admin v1: every documented method is served. The Knative
	// services family and its children, the jobs / executions / tasks family,
	// and the instances and workerpools collections all address the same
	// records the v2 collections own, projected into the Knative shape; the
	// operations poll/delete/wait and the IAM reads complete the surface.
	"cloudrun-v1": 152,

	"dataflow-v1b3": 84,

	// Cloud Run Admin v2: builds.submit hands the request to the Cloud Build
	// this simulator serves, and sourceUploads.upload names the Cloud Storage
	// location a client uploads to. Both ride the /v2 locations segment Cloud
	// Functions also claims, so they dispatch from its fan-in; that service's
	// count is unmoved at 42, which is what proves neither shadows the other.
	//
	// Fifteen spellings remain: the export family (exportImage,
	// exportImageMetadata, exportMetadata, exportProjectMetadata and the two
	// exportStatus spellings) reports Google's own image-export pipeline, which
	// this simulator does not run — there is no export whose status to report.
	"cloudrun-v2": 104,

	// BigQuery v2: the whole document is served. jobs.insert declares both a
	// JSON path and the /upload media path that carries a load job's bytes;
	// the same handler answers both, because the body is the same Job either way.
	"bigquery-v2": 95,

	// Cloud DNS: every documented method is served. The managed-zone IAM
	// triple (getIamPolicy, setIamPolicy, testIamPermissions) rides the same
	// per-resource policy store every other AIP-141 resource uses, behind
	// the managedZones colon-verb fan-in in dns.go.
	"dns-v1": 80,

	// Cloud KMS: the two Key Access Justifications reads
	// (showEffectiveKeyAccessJustificationsPolicyConfig and
	// ...EnrollmentConfig) report an organization-policy product the
	// simulator does not model; the projects colon-verb fan-in rejects them
	// as unknown verbs.
	"cloudkms-v1": 168,

	"eventarc-v1":       132,
	"cloudfunctions-v2": 42,
	"pubsub-v1":         92,
	"apigateway-v1":     60,
	"iamcredentials-v1": 14,
	"vpcaccess-v1":      16,
	// Cloud Logging: every documented method is served. locations.get shares
	// its URI shape with Cloud Run's locations/{location}:exportProjectMetadata,
	// because a wildcard segment matches one carrying a colon; the handler
	// splits on it, so a location is answered and a custom method is still
	// reported unknown. Cloud Run's count is unmoved by the mount, which is
	// what proves the split works.
	"logging-v2": 508,

	// Cloud Billing: every documented method is served. The account
	// collection, its sub-accounts, the organization-scoped spellings, the
	// project links (updateBillingInfo writing the store getBillingInfo
	// reads), the IAM triple, and the installation's own service catalog —
	// whose SKU lists are empty because this deployment publishes no price
	// sheet, which is that catalog's truth.
	"cloudbilling-v1": 36,

	// Cloud Resource Manager v1: every documented method is served — the
	// projects lifecycle and its getAncestry hierarchy read, the IAM triple,
	// the organizations and liens collections, the operations poll, and the
	// org-policy family on all three hierarchy nodes.
	"cloudresourcemanager-v1": 76,

	// Cloud Storage: the whole document is served.
	//
	// Read a count that moves by one with suspicion. objectAccessControls'
	// five reads and writes counted as served with no handler behind them —
	// `/o/{object}/acl` matched the `{object...}` catch-all serving
	// objects.get, whose JSON 404 the probe reads as an answer. Only .insert
	// looked missing, because POST had no catch-all to swallow it. Check the
	// siblings of any collection sitting under a multi-segment wildcard.
	"storage-v1": 89,

	// Artifact Registry: the whole document is served. Raised from 125 by the
	// prewarmed-artifact family and the plain /v1 spellings of the media
	// publish methods.
	//
	// A media method declares two paths — /upload/v1 for the bytes and /v1 —
	// and the service answers both, so registering only the upload one left
	// seven methods unserved. The prewarm family is real state: prewarmArtifact
	// records the artifact against a stream location with an expiry, check and
	// remove read and delete that record, list reports it (it previously
	// answered a hardcoded empty array), and exportArtifact writes the version
	// into the Cloud Storage bucket the request names.
	"artifactregistry-v1": 147,

	// Cloud Build: the whole document is served. Raised from 86 by the
	// regional build create, builds.retry and .approve, triggers.run and
	// .webhook, the three webhook receivers, and the Bitbucket Server
	// connected-repository pair.
	//
	// Eventarc owns projects/{p}/locations/{l}/triggers under the same /v1
	// prefix, so Cloud Build's trigger verbs are offered that fan-in before
	// Eventarc's IAM ones. Eventarc's count is unmoved, which is what proves
	// neither shadows the other.
	//
	// Declared fell from 130 to 114 at Discovery revision 20260814, when Google
	// withdrew the whole gitLabConfigs collection.
	"cloudbuild-v1": 114,

	// Memorystore for Redis: instance CRUD, upgrade, failover,
	// rescheduleMaintenance and operations.cancel are served, and ACL policy
	// revision get/list read immutable policy snapshots.
	//
	// export and import stay unserved, and not for want of routing: both move
	// an RDB snapshot of the instance's keyspace, and this slice models the
	// control plane only — no Redis runs behind an instance, so there are no
	// bytes to write out and nothing an import could load. Serving them would
	// fabricate an RDB.
	"redis-v1": 90,

	// Firestore: document CRUD, the transaction verbs, and the custom methods
	// on a document parent — listCollectionIds, runAggregationQuery and
	// partitionQuery, which share CreateDocument's URI shape and route through
	// its dispatcher — plus documents:write and the databases clone/restore
	// pair. Raised from 96.
	//
	// Twelve spellings remain, all of them the streaming surface: documents.listen
	// and documents.executePipeline, and the changeStreams collection (create,
	// get, list, delete) whose deliveries need that same plumbing. REST cannot
	// carry a bidirectional stream, and a change stream with no listener to
	// deliver to would be a record nothing reads.
	"firestore-v1": 108,

	// Identity and Access Management: every documented method is served —
	// service accounts, keys (including upload's caller-supplied public key
	// and the enable/disable bit the token endpoint honors), roles and the
	// grantable-roles query over the sim's own catalog, the iamPolicies lint
	// and auditable-services queries, workload/workforce identity pools with
	// their subjects' delete/undelete pair, and the IAM policy verbs.
	"iam-v1": 266,

	// Secret Manager: secrets and versions CRUD, addVersion, access, enable,
	// disable, destroy and the IAM verbs are served on both the global and the
	// regional (locations/{location}) surface. Typed Cloud SQL secrets support
	// enableManagedRotation and rotateSecret through the Cloud SQL Admin API,
	// preserving the real managed-version lifecycle.
	"secretmanager-v1": 72,

	// Service Usage: every documented method is served —
	// services.list/.get/.batchGet/.enable/.disable/.batchEnable and the
	// operations collection's get/delete/cancel.
	"serviceusage-v1": 20,

	// Cloud SQL Admin: every documented method is served on both the v1 and
	// the v1beta4 spelling — instances, databases, users, backups, SSL
	// certs, flags, tiers, the connect resource including the DNS-name
	// resolve, and instances.pointInTimeRestore. The point-in-time restore's
	// /v1 URI has the shape of Cloud Resource Manager's project
	// custom-method pattern; that dispatcher recognizes the verb as Cloud
	// SQL Admin's and forwards it, while the /sql/v1beta4 spelling is
	// mounted by the Cloud SQL module itself.
	"sqladmin-v1":      150,
	"sqladmin-v1beta4": 150,

	// Cloud Spanner: instances, instance configs, instance partitions,
	// databases, backups, backup schedules, database roles, the IAM triple on
	// every resource that exposes it, all five operation collections, and the
	// complete public session data plane except adapter/adaptMessage are served
	// over REST; the same data plane is served over gRPC. Five methods are
	// deliberately unserved because the simulator holds nothing for them to
	// report or act on, and a served answer would have to be invented:
	//   - databases.getScans — Key Visualizer scan data is a read/write heatmap
	//     the service derives from production traffic; the simulator measures no
	//     per-key traffic, so it has no scan to return.
	//   - databases.addSplitPoints — split points partition a database's key
	//     space across servers; the backing engine is a single SQLite database
	//     with no key-range splits to add them to.
	//   - databases.changequorum — the quorum type of a dual-region database;
	//     the simulator keeps one replica and models no quorum.
	//   - sessions.adapter / sessions.adaptMessage — the adapter tunnels raw
	//     PostgreSQL and Cassandra wire-protocol messages, which the simulator
	//     does not speak.
	// The "instances/{rest...}" mount that routes this surface has the shape of
	// every path in the document, so only its answer — a method-not-found for
	// each tail it does not route — distinguishes the two.
	"spanner-v1": 188,
}

// gcpProbePrincipal is the subject of the access token every probe presents.
// The simulator's data-plane middleware verifies the token it minted itself,
// exactly as it does for a real client, so a probe reaches the same handlers a
// real client reaches.
const gcpProbePrincipal = "coverage-probe@sockerless.iam.gserviceaccount.com"

// gcpProbeBody is the request body every write probe (POST/PUT/PATCH) carries:
// deliberately malformed JSON. The probe asks one question — "does a handler
// serve this method+URI?" — and a body no handler can decode answers it without
// letting any handler mutate simulator or host state: sim.ReadJSON fails, the
// handler returns INVALID_ARGUMENT, and the request never reaches the code that
// would create a real Compute network namespace, start a Firecracker VM, or
// shell out to a container build. A 400 is a served answer, so nothing is lost.
const gcpProbeBody = `{"sockerless-coverage-probe"`

// gcpProbeResult is one method's verdict plus the evidence behind it.
type gcpProbeResult struct {
	served bool
	// why names the arm of gcpClassifyProbe that decided, with the status
	// code and the response excerpt it decided on.
	why string
}

// gcpCoverageProbe drives the in-process simulator.
type gcpCoverageProbe struct {
	handler http.Handler
	token   string
	seen    map[string]gcpProbeResult
}

// newGCPCoverageProbe builds the simulator the way main() serves it — every
// registered service, plus the bearer middleware as the outermost wrap — and
// mints the access token the probes present.
func newGCPCoverageProbe(t *testing.T) *gcpCoverageProbe {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	srv.WrapHandler(bearerAuthMiddleware(srv))
	now := time.Now()
	return &gcpCoverageProbe{
		handler: srv,
		token:   signAccessToken(gcpProbePrincipal, now, now.Add(time.Hour)),
		seen:    map[string]gcpProbeResult{},
	}
}

const (
	// gcpMuxMissBody is what Go's own ServeMux writes when no registered
	// pattern matches the request: plain text, not a Google error envelope.
	// The simulator's own catch-all subtrees — the Cloud Storage XML data
	// plane, the Compute load-balancer data plane, the generic IAM verb
	// route — call http.NotFound for a request they do not own, so they
	// produce the identical body and mean the identical thing.
	gcpMuxMissBody = "404 page not found"
	// gcpMuxMethodMissBody is what Go's ServeMux writes when patterns exist
	// for the path but none for this HTTP method.
	gcpMuxMethodMissBody = "Method Not Allowed"
)

var (
	// gcpUnknownMethodError matches the structured errors that report the
	// METHOD as unknown rather than the resource as absent: real Google's API
	// frontend answers an undefined method with "Method not found.", and the
	// simulator's colon-verb fan-in handlers — one handler mounted on
	// "{resource}" that dispatches every AIP-136 custom method on it — answer
	// an unrecognised verb with "unknown … action/verb". Both mean no handler
	// serves the method.
	gcpUnknownMethodError = regexp.MustCompile(`(?i)method not found|(unknown|unsupported|unrecognized|unrecognised) [^"]*\b(action|verb|method)\b`)
)

// gcpClassifyProbe decides whether a probe response proves a handler serves the
// method. The whole point of the metric is the distinction between "no handler
// is mounted here" and "a handler ran and told me the resource is absent", so
// each arm is explicit:
//
//   - Go's ServeMux miss ("404 page not found") — nothing is mounted on the
//     URI at all. NOT served.
//   - Go's ServeMux method mismatch ("Method Not Allowed") — something is
//     mounted on the path, but not for this HTTP method, which is what the
//     Discovery method is. NOT served.
//   - A structured error naming the METHOD as unknown — real Google's
//     "Method not found.", or a colon-verb fan-in handler rejecting a custom
//     method it does not implement. The fan-in pattern matches every verb on
//     the resource, so its own rejection is the only evidence that the verb
//     is unserved. NOT served.
//   - A 5xx — the handler was reached and broke, which the recovered-panic
//     middleware also surfaces this way. A method that cannot answer is not a
//     method the simulator serves. NOT served.
//   - Anything else — served. That deliberately includes a structured
//     404 NOT_FOUND for a resource that does not exist (a mounted handler ran,
//     looked the resource up and answered), 400 INVALID_ARGUMENT (the handler
//     read and rejected the probe body), 401/403, 409, 200. The probe creates
//     no resources, so "the resource does not exist" is the expected answer
//     from a served read/write method and must not be confused with an
//     unmounted one.
func gcpClassifyProbe(status int, body string) gcpProbeResult {
	body = strings.TrimSpace(body)
	excerpt := body
	if len(excerpt) > 160 {
		excerpt = excerpt[:160]
	}
	switch {
	case status == http.StatusNotFound && strings.HasPrefix(body, gcpMuxMissBody):
		return gcpProbeResult{served: false, why: "mux miss: no pattern matches the URI"}
	case status == http.StatusMethodNotAllowed && strings.HasPrefix(body, gcpMuxMethodMissBody):
		return gcpProbeResult{served: false, why: "mux miss: no pattern for this HTTP method"}
	case gcpUnknownMethodError.MatchString(body):
		return gcpProbeResult{served: false, why: "handler reports the method/verb as unknown: " + excerpt}
	case status >= http.StatusInternalServerError:
		return gcpProbeResult{served: false, why: "handler failed with " + http.StatusText(status) + ": " + excerpt}
	}
	return gcpProbeResult{served: true, why: "handler answered " + http.StatusText(status) + ": " + excerpt}
}

// probe sends one request through the simulator and classifies the response.
// Results are memoized: the two Discovery spellings of a method render to the
// same concrete URI, as do the alternative renderings of many methods.
func (p *gcpCoverageProbe) probe(method, host, path string) gcpProbeResult {
	key := method + " " + host + " " + path
	if res, ok := p.seen[key]; ok {
		return res
	}
	var body io.Reader
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		body = strings.NewReader(gcpProbeBody)
	}
	req := httptest.NewRequest(method, path, body)
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	res := gcpClassifyProbe(rec.Code, rec.Body.String())
	p.seen[key] = res
	return res
}

// gcpParamSamples returns the plausible values a path parameter is filled with,
// most-likely first. A simulator pattern segment is usually a wildcard that any
// token matches, but some handlers are mounted on a literal where Discovery
// describes a parameter (API Gateway serves its single-region surface as
// ".../locations/global/apis/{api}"), so a parameter whose value carries
// meaning offers the alternatives a real client would use. Values follow the
// service's own naming rules — zones and regions must look like zones and
// regions for the handlers that parse them.
func gcpParamSamples(name string) []string {
	if m := gcpHierarchyPlaceholder.FindStringSubmatch(name); m != nil {
		// Discovery spells an unconstrained multi-segment parameter as
		// "{v2Id}/{v2Id1}" in the flatPath — the resource-hierarchy prefix a
		// client puts in front of the collection ("projects/my-project",
		// "folders/123"). The first segment is the collection, the second the
		// id.
		switch m[1] {
		case "":
			return []string{"projects"}
		case "1":
			return gcpParamSamples("project")
		default:
			return []string{"sim-id"}
		}
	}
	hint := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(name, "Id"), "s"))
	switch hint {
	case "project", "projectid", "projectnumber":
		// The project Cloud Resource Manager seeds and the SDK/CLI/terraform
		// harnesses are configured with: a handler that resolves the project
		// before doing its work reaches its own work.
		return []string{"test-project"}
	case "location":
		return []string{"us-central1", "global"}
	case "region":
		return []string{"us-central1"}
	case "zone":
		return []string{"us-central1-a"}
	case "billingaccount":
		return []string{"012345-678901-ABCDEF"}
	}
	return []string{"sim-" + gcpSampleToken(name)}
}

// gcpHierarchyPlaceholder matches Discovery's generic path-parameter names —
// "{v1Id}", "{v2Id}", "{v2Id1}" — the flatPath expansion of a parameter the
// document constrains to no particular collection.
var gcpHierarchyPlaceholder = regexp.MustCompile(`^v[0-9]+(?:beta[0-9]*)?Id([0-9]*)$`)

var gcpNonTokenChars = regexp.MustCompile(`[^a-z0-9-]+`)

// gcpSampleToken turns a parameter name into a URI-safe sample token.
func gcpSampleToken(name string) string {
	t := gcpNonTokenChars.ReplaceAllString(strings.ToLower(name), "-")
	t = strings.Trim(t, "-")
	if t == "" {
		return "id"
	}
	return t
}

var gcpLiteralPatternSegment = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// gcpSplitPatternSegments splits a Discovery parameter pattern into its URI
// segments, splitting only on "/" outside a character class or group so that
// "[^/]+" — which contains a slash — stays one segment.
func gcpSplitPatternSegments(pattern string) []string {
	var (
		segs  []string
		cur   strings.Builder
		depth int
	)
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '\\':
			cur.WriteByte(c)
			if i+1 < len(pattern) {
				i++
				cur.WriteByte(pattern[i])
			}
		case '[', '(':
			depth++
			cur.WriteByte(c)
		case ']', ')':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
		case '/':
			if depth == 0 {
				segs = append(segs, cur.String())
				cur.Reset()
			} else {
				cur.WriteByte(c)
			}
		default:
			cur.WriteByte(c)
		}
	}
	return append(segs, cur.String())
}

// gcpPatternLiterals returns the literal collection names a pattern segment
// admits — "secrets" for a plain literal, both branches for an alternation of
// literals such as "(?:projects|folders|organizations)" — and reports false for
// a segment that stands for a value ("[^/]+", ".*", a character-class form).
func gcpPatternLiterals(seg string) ([]string, bool) {
	inner := seg
	inner = strings.TrimPrefix(inner, "(?:")
	inner = strings.TrimPrefix(inner, "(")
	inner = strings.TrimSuffix(inner, ")")
	branches := strings.Split(inner, "|")
	for _, b := range branches {
		if !gcpLiteralPatternSegment.MatchString(b) {
			return nil, false
		}
	}
	return branches, true
}

// gcpPatternSamples renders the candidate values of a reserved-expansion path
// parameter ({+name}) from the Discovery pattern that describes it: literal
// segments are kept, and each value segment is filled from the collection name
// that precedes it ("^projects/[^/]+/locations/[^/]+/secrets/[^/]+$" renders
// "projects/sim-project/locations/us-central1/secrets/sim-secret"). Index 0 is
// the primary rendering; later indices vary the segments that offer
// alternatives.
func gcpPatternSamples(pattern string) []string {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	segs := gcpSplitPatternSegments(pattern)
	perSeg := make([][]string, 0, len(segs))
	prevLiteral := ""
	for _, seg := range segs {
		if lits, ok := gcpPatternLiterals(seg); ok {
			perSeg = append(perSeg, lits)
			prevLiteral = lits[0]
			continue
		}
		if prevLiteral == "" {
			// An unconstrained leading segment ("^[^/]+/[^/]+/logs/[^/]+$")
			// is the resource-hierarchy prefix: the collection, then its id.
			// Filling it with the collection also gives the next segment its
			// hint.
			prevLiteral = "projects"
			perSeg = append(perSeg, []string{prevLiteral})
			continue
		}
		perSeg = append(perSeg, gcpParamSamples(prevLiteral))
	}
	return gcpJoinSamples(perSeg)
}

// gcpJoinSamples joins per-segment candidate lists into whole-value candidates:
// candidate i takes each segment's i-th value, or its last one when it offers
// fewer. Duplicates collapse.
func gcpJoinSamples(perSeg [][]string) []string {
	width := 1
	for _, vals := range perSeg {
		if len(vals) > width {
			width = len(vals)
		}
	}
	var out []string
	for i := 0; i < width; i++ {
		parts := make([]string, 0, len(perSeg))
		for _, vals := range perSeg {
			if i < len(vals) {
				parts = append(parts, vals[i])
			} else {
				parts = append(parts, vals[len(vals)-1])
			}
		}
		out = appendUnique(out, strings.Join(parts, "/"))
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

var gcpTemplateLabel = regexp.MustCompile(`\{[^}]+\}`)

// gcpRenderMethodURIs renders a documented method's path template into the
// concrete request URIs a client would send. Plain labels ({projectsId}) take
// one segment; reserved-expansion labels ({+name}, {name...}) take the shape
// their Discovery pattern describes.
func gcpRenderMethodURIs(m discoveryMethod) []string {
	labels := gcpTemplateLabel.FindAllString(m.Path, -1)
	perLabel := make([][]string, 0, len(labels))
	for _, label := range labels {
		inner := label[1 : len(label)-1]
		greedy := strings.HasPrefix(inner, "+") || strings.HasSuffix(inner, "...")
		name := strings.TrimSuffix(strings.TrimPrefix(inner, "+"), "...")
		if greedy {
			if pattern := m.PathParams[name]; pattern != "" {
				perLabel = append(perLabel, gcpPatternSamples(pattern))
				continue
			}
		}
		perLabel = append(perLabel, gcpParamSamples(name))
	}

	width := 1
	for _, vals := range perLabel {
		if len(vals) > width {
			width = len(vals)
		}
	}
	var out []string
	for i := 0; i < width; i++ {
		n := 0
		uri := gcpTemplateLabel.ReplaceAllStringFunc(m.Path, func(string) string {
			vals := perLabel[n]
			n++
			if i < len(vals) {
				return vals[i]
			}
			return vals[len(vals)-1]
		})
		out = appendUnique(out, uri)
	}
	return out
}

// gcpDocMountPrefix returns the simulator mount prefix a Discovery document's
// methods are served under. Real Google separates services by hostname; the
// single-port simulator mounts the one service whose paths would otherwise
// collide under a prefix, and that prefix is part of the URI a client
// configured with the simulator's coordinate sends.
func gcpDocMountPrefix(file string) string {
	for prefix, mounted := range gcpMountPrefixes {
		if mounted == file {
			return prefix
		}
	}
	return ""
}

// methodServed probes one documented method and reports whether the
// simulator serves it. A method counts as served when any of its plausible
// renderings is served.
func (p *gcpCoverageProbe) methodServed(d *discoveryDoc, m discoveryMethod) gcpProbeResult {
	prefix := gcpDocMountPrefix(d.File)
	var last gcpProbeResult
	for _, uri := range gcpRenderMethodURIs(m) {
		res := p.probe(m.HTTPMethod, d.Host, prefix+uri)
		if res.served {
			return res
		}
		last = res
	}
	return last
}

// docCoverage returns the served count for one Discovery document.
func (p *gcpCoverageProbe) docCoverage(d *discoveryDoc) (served int, unserved []string) {
	for _, m := range d.Methods {
		res := p.methodServed(d, m)
		if res.served {
			served++
			continue
		}
		unserved = append(unserved, m.HTTPMethod+" "+m.Path+"  ("+res.why+")")
	}
	sort.Strings(unserved)
	return served, unserved
}

// TestServiceConformance_GCPCoverageProbeIsSound guards the probe itself: a
// broken probe would classify the whole surface one way and turn the ratchet
// into a rubber stamp. It asserts both verdicts on known cases — a URI no
// service publishes must come back a mux miss, a method the simulator serves
// must come back served, and the bearer the probe presents must be accepted so
// that "401" never becomes the reason every method looks served.
func TestServiceConformance_GCPCoverageProbeIsSound(t *testing.T) {
	p := newGCPCoverageProbe(t)

	if res := p.probe(http.MethodGet, "run.googleapis.com", "/v2/projects/test-project/locations/us-central1/jobs"); !res.served {
		t.Errorf("Cloud Run v2 ListJobs probed as unserved (%s) — the probe cannot see mounted handlers", res.why)
	}
	if res := p.probe(http.MethodGet, "run.googleapis.com", "/v1/no-such-google-surface/test-project"); res.served {
		t.Errorf("an unmounted URI probed as served (%s) — the probe cannot see mux misses", res.why)
	}

	// The bearer gate must accept the probe's token: a rejected token answers
	// 401, which is a served answer, and every method would count as covered.
	req := httptest.NewRequest(http.MethodGet, "/v2/projects/test-project/locations/us-central1/jobs", nil)
	req.Host = "run.googleapis.com"
	req.Header.Set("Authorization", "Bearer "+p.token)
	rec := httptest.NewRecorder()
	p.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("simulator rejected the probe's own access token: %s", rec.Body.String())
	}
}

// TestServiceConformance_GCPCoverageFloor locks each Discovery document's
// served-method count: an exact-equality ratchet (a drop is a regression;
// more requires bumping the floor).
func TestServiceConformance_GCPCoverageFloor(t *testing.T) {
	docs := loadDiscoveryDocs(t)
	p := newGCPCoverageProbe(t)
	byFile := map[string]*discoveryDoc{}
	for _, d := range docs {
		byFile[strings.TrimSuffix(d.File, ".discovery.json.gz")] = d
	}
	// Every vendored document is locked: a document with no floor is a service
	// whose coverage could drop unnoticed.
	for name := range byFile {
		if _, ok := gcpMethodFloor[name]; !ok {
			t.Errorf("%s: vendored Discovery document has no gcpMethodFloor entry — add one at its measured coverage", name)
		}
	}
	names := make([]string, 0, len(gcpMethodFloor))
	for name := range gcpMethodFloor {
		names = append(names, name)
	}
	sort.Strings(names)

	totalServed, totalSpellings := 0, 0
	for _, name := range names {
		floor := gcpMethodFloor[name]
		d, ok := byFile[name]
		if !ok {
			t.Errorf("%s: floor set but no vendored Discovery document found", name)
			continue
		}
		served, unserved := p.docCoverage(d)
		totalServed += served
		totalSpellings += len(d.Methods)
		t.Logf("%-32s %d/%d method spellings served", name, served, len(d.Methods))
		if declared, locked := gcpDeclaredMethodTotals[name]; !locked {
			t.Errorf("%s: vendored Discovery document has no gcpDeclaredMethodTotals entry — add one at its declared count (%d)", name, len(d.Methods))
		} else if len(d.Methods) != declared {
			t.Errorf("%s: the vendored document declares %d method spellings, the lock says %d — a re-vendor changed the surface. Serve the new methods or record why not, then update gcpDeclaredMethodTotals (and gcpMethodFloor if coverage moved).",
				name, len(d.Methods), declared)
		}
		if served == floor {
			continue
		}
		// The unserved list is the evidence behind the number that moved: it is
		// the work list for closing a gap, and it names what stopped being
		// served when a floor drops.
		t.Errorf("%s: coverage %d/%d != floor %d — update gcpMethodFloor (a drop is a regression; more is a ratchet-up).\n  %d spelling(s) not served:\n    %s",
			name, served, len(d.Methods), floor, len(unserved), strings.Join(unserved, "\n    "))
	}

	// Discovery describes most methods twice — an expanded flatPath and a
	// {+name} template — and both spellings render to the same URI, so this
	// total counts spellings, not distinct methods. It is roughly twice the
	// method count for the documents that declare both.
	t.Logf("TOTAL: %d/%d GCP Discovery method spellings served", totalServed, totalSpellings)
}
