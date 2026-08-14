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
var gcpMethodFloor = map[string]int{
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

	"dataflow-v1b3":     84,
	"cloudrun-v2":       102,
	"bigquery-v2":       94,
	"dns-v1":            74,
	"cloudkms-v1":       168,
	"eventarc-v1":       132,
	"cloudfunctions-v2": 42,
	"pubsub-v1":         92,
	"apigateway-v1":     60,
	"iamcredentials-v1": 14,
	"vpcaccess-v1":      16,
	"logging-v2":        504,

	// Cloud Billing: projects.getBillingInfo — the read
	// terraform-provider-google issues on every google_project Read — and the
	// POST IAM verbs are served. The billing-account collection, its
	// sub-accounts, services, SKUs and projects.updateBillingInfo are mux
	// misses.
	"cloudbilling-v1": 6,

	// Cloud Resource Manager v1: every documented method is served — the
	// projects lifecycle and its getAncestry hierarchy read, the IAM triple,
	// the organizations and liens collections, the operations poll, and the
	// org-policy family on all three hierarchy nodes.
	"cloudresourcemanager-v1": 76,

	// Cloud Storage: objects.restore, objects.move, objects.bulkRestore and
	// objectAccessControls.insert are unmounted — the probe gets Go's own mux
	// miss on each. Every other method the document describes is served.
	"storage-v1": 79,

	// Artifact Registry: the Docker/Maven/npm/Python read surface, repository
	// and rule CRUD are served, as is operations.cancel. The package-format
	// publish methods that ride the plain (non-/upload) path —
	// aptArtifacts.create, genericArtifacts.create, goModules.create,
	// googetArtifacts.create, kfpArtifacts.create, yumArtifacts.create,
	// files.upload — and the prewarmed-artifact family (prewarmArtifact,
	// checkPrewarmedArtifact, removePrewarmedArtifact, exportArtifact) are
	// mux misses.
	"artifactregistry-v1": 125,

	// Cloud Build: the global builds/triggers/worker-pools surface is served,
	// as are both operations.cancel spellings and builds.cancel on the regional
	// resource path as well as the legacy global one — one build record, so one
	// cancel behind both. The rest of the regional (locations/{location}) builds
	// surface including .retry, the webhook receivers
	// (githubDotComWebhook.receive, webhook, regionalWebhook),
	// connectedRepositories.batchCreate, removeGitLabConnectedRepository and
	// removeBitbucketServerConnectedRepository are mux misses.
	"cloudbuild-v1": 98,

	// Memorystore for Redis: instance CRUD, upgrade, failover and
	// operations.cancel are served. ACL policy revision get/list are served
	// from immutable policy snapshots; the instances fan-in rejects export,
	// import and rescheduleMaintenance as unknown verbs.
	"redis-v1": 88,

	// Firestore: document CRUD, runQuery, batchGet, batchWrite and the
	// transaction verbs are served. documents.write, documents.listen,
	// documents.executePipeline, databases.clone, databases.restore and the
	// document-collection verbs listCollectionIds, partitionQuery and
	// runAggregationQuery are not — the last three share the URI shape of
	// CreateDocument, whose handler reports them as unrouted methods.
	"firestore-v1": 96,

	// Identity and Access Management: service accounts, keys, roles,
	// workload/workforce identity pools and the IAM policy verbs are served.
	// serviceAccounts.keys.upload/.enable/.disable,
	// workforcePools.subjects.undelete, iamPolicies.lintPolicy,
	// iamPolicies.queryAuditableServices and roles.queryGrantableRoles are
	// mux misses.
	"iam-v1": 252,

	// Secret Manager: secrets and versions CRUD, addVersion, access, enable,
	// disable, destroy and the IAM verbs are served on both the global and the
	// regional (locations/{location}) surface. Typed Cloud SQL secrets support
	// enableManagedRotation and rotateSecret through the Cloud SQL Admin API,
	// preserving the real managed-version lifecycle.
	"secretmanager-v1": 72,

	// Service Usage: services.list/.get/.enable/.disable/.batchEnable and the
	// operations collection's get/delete/cancel are served; services.batchGet
	// is a mux miss.
	"serviceusage-v1": 18,

	// Cloud SQL Admin v1: instances, databases, users, backups, SSL certs,
	// flags and tiers are served. connect.resolveConnectSettings is a mux
	// miss, and projects.instances.pointInTimeRestore has the URI shape of
	// Cloud Resource Manager's project custom-method pattern — that handler
	// serves no Cloud SQL method and answers it as unrouted. The v1beta4
	// surface is missing the same two.
	"sqladmin-v1":      146,
	"sqladmin-v1beta4": 146,

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
func (p *gcpCoverageProbe) docCoverage(d *discoveryDoc) int {
	served := 0
	for _, m := range d.Methods {
		if p.methodServed(d, m).served {
			served++
		}
	}
	return served
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
	for name, floor := range gcpMethodFloor {
		d, ok := byFile[name]
		if !ok {
			t.Errorf("%s: floor set but no vendored Discovery document found", name)
			continue
		}
		served := p.docCoverage(d)
		if served != floor {
			t.Errorf("%s: coverage %d/%d != floor %d — update gcpMethodFloor (a drop is a regression; more is a ratchet-up).",
				name, served, len(d.Methods), floor)
		}
	}
}

// TestServiceConformance_GCPCoverage reports per-document coverage
// (informational — never fails): the served fraction at a glance, then every
// method that is not served with the probe result that says so. The detail is
// the work list for closing a gap and the evidence behind each floor.
func TestServiceConformance_GCPCoverage(t *testing.T) {
	docs := loadDiscoveryDocs(t)
	p := newGCPCoverageProbe(t)
	type row struct {
		name        string
		served, tot int
		unserved    []string
	}
	var rows []row
	for _, d := range docs {
		r := row{name: strings.TrimSuffix(d.File, ".discovery.json.gz"), tot: len(d.Methods)}
		for _, m := range d.Methods {
			res := p.methodServed(d, m)
			if res.served {
				r.served++
				continue
			}
			r.unserved = append(r.unserved, m.HTTPMethod+" "+m.Path+"  ("+res.why+")")
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	ts, tt := 0, 0
	for _, r := range rows {
		ts += r.served
		tt += r.tot
		t.Logf("%-32s %d/%d", r.name, r.served, r.tot)
	}
	t.Logf("TOTAL: %d/%d GCP Discovery methods served", ts, tt)

	for _, r := range rows {
		if len(r.unserved) == 0 {
			continue
		}
		sort.Strings(r.unserved)
		t.Logf("%s — %d method spelling(s) not served:\n  %s", r.name, len(r.unserved), strings.Join(r.unserved, "\n  "))
	}
}
