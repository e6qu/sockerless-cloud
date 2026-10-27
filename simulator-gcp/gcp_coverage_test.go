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

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Google Cloud operation-coverage gate, the Discovery-document analogue of
// simulator-aws/service_conformance_test.go. It renders every documented method
// of each vendored Discovery document into a concrete request URI, sends it
// through the handler chain main() serves, bearer middleware included, and
// counts the method served only when a mounted handler answered it
// (gcpClassifyProbe). gcpMethodFloor locks the count: a drop is a regression,
// a rise must raise the floor. Matching routes by shape alone would count a
// method whose URI lands under an unrelated pattern or a fan-in that rejects
// its verb; only the response shows whether a handler serves it.

// gcpDeclaredMethodTotals locks each vendored Discovery document's declared
// method-spelling count. A re-vendor that adds methods leaves every served
// count unchanged, so without this the new methods would sit unserved behind
// green floors; a changed total forces serving them or recording why not.
var gcpDeclaredMethodTotals = map[string]int{
	"apigateway-v1":           60,
	"artifactregistry-v1":     149,
	"bigquery-v2":             96,
	"bigtableadmin-v2":        164,
	"cloudbilling-v1":         56,
	"cloudbuild-v1":           114,
	"cloudfunctions-v2":       42,
	"cloudkms-v1":             174,
	"cloudresourcemanager-v1": 76,
	"cloudresourcemanager-v2": 24,
	"cloudresourcemanager-v3": 184,
	"cloudscheduler-v1":       32,
	"cloudtasks-v2":           48,
	"cloudrun-v1":             152,
	"cloudrun-v2":             119,
	"compute-v1":              2036,
	"dataflow-v1b3":           114,
	"discovery-v1":            2,
	"dns-v1":                  102,
	"eventarc-v1":             132,
	"firestore-v1":            120,
	"iam-v1":                  274,
	"iamcredentials-v1":       14,
	"logging-v2":              508,
	"pubsub-v1":               92,
	"redis-v1":                94,
	"secretmanager-v1":        72,
	"serviceusage-v1":         20,
	"spanner-v1":              198,
	"sqladmin-v1":             170,
	"sqladmin-v1beta4":        170,
	"storage-v1":              90,
	"vpcaccess-v1":            16,
}

// gcpMethodFloor locks the served count per Discovery document, keyed by file
// name without .discovery.json.gz. Discovery spells most methods twice, as a
// flatPath and a {+name} template; both render to one URI, so a served method
// counts both spellings and an unserved one neither.
var gcpMethodFloor = map[string]int{
	// Compute Engine: the unserved methods answer declared 501s in
	// compute_catalogs.go: regions.projectViews.get, regions.advice.capacity and
	// .capacityHistory, managedRulesets.get and .list, and
	// globalFrontendSettings.get and .patch, each Google's own replica, forecast,
	// rule catalogue or billing state the simulator does not hold. Lower the floor
	// by one and the gate prints the unserved list.
	"compute-v1": 2022,

	// Cloud Resource Manager v3: the folders' and organizations' boundaries
	// collection and boundaryConfig singleton answer a declared 501: no vendored
	// source states their Management Project provisioning or CREATING-to-ACTIVE
	// progression.
	"cloudresourcemanager-v3": 156,

	// Cloud Resource Manager v2: folders, over the store v3 serves; its
	// operations.get is the v1 route.
	"cloudresourcemanager-v2": 24,
	"bigtableadmin-v2":        164,

	// Cloud Run Admin v1: the Knative services, jobs, executions, tasks,
	// instances and workerpools all address the v2 records.
	"cloudrun-v1": 152,

	"dataflow-v1b3": 114,

	// Cloud Run Admin v2: builds.submit, sourceUploads and the export verbs ride
	// the /v2 locations segment Cloud Functions also claims and dispatch from its
	// fan-in; cloudfunctions-v2's count shows neither shadows the other.
	"cloudrun-v2": 119,

	// BigQuery v2: jobs.insert declares a JSON path, the /upload media path that
	// carries a load job's bytes, and the /resumable/upload path apitools uses.
	"bigquery-v2": 96,

	// Cloud DNS: the locations, operations and outboundEndpoints collections
	// answer a declared 501 in dns.go.
	"dns-v1": 80,

	"discovery-v1": 2,

	"cloudkms-v1": 174,

	"eventarc-v1":       132,
	"cloudfunctions-v2": 42,
	"pubsub-v1":         92,
	"apigateway-v1":     60,
	"iamcredentials-v1": 14,
	"vpcaccess-v1":      16,
	// Cloud Scheduler: getCmekConfig and updateCmekConfig answer a declared 501:
	// no job is encrypted with a customer-managed key.
	"cloudscheduler-v1": 28,
	// Cloud Tasks: getCmekConfig and updateCmekConfig answer a declared 501, as
	// does tasks.buffer, whose body binding (raw bytes in the guide's curl
	// example, BufferTaskRequest in the generated clients) no vendored source
	// settles.
	"cloudtasks-v2": 42,
	// Cloud Logging: locations.get shares its URI shape with Cloud Run's
	// locations/{location}:exportProjectMetadata; the handler splits on the colon,
	// and cloudrun-v2's count shows the split works.
	"logging-v2": 508,

	// Cloud Billing: the agent task surface (tasks, push notification configs,
	// message send and stream) and the payment card read answer a declared 501 in
	// cloudbilling.go: an LLM-backed agent and a real payment instrument hold that
	// state.
	"cloudbilling-v1": 36,

	"cloudresourcemanager-v1": 76,

	// Cloud Storage: a collection under a multi-segment wildcard can count as
	// served with no handler behind it, as objectAccessControls once did under
	// `{object...}`; gcp_phantom_coverage_test.go catches that. objects.insert
	// counts three spellings: JSON, /upload media and /resumable/upload.
	"storage-v1": 90,

	// Artifact Registry: a media method declares both /upload/v1 and /v1, and
	// files.upload and genericArtifacts.upload a /resumable/upload path too; each
	// spelling counts.
	"artifactregistry-v1": 149,

	// Cloud Build: Eventarc owns projects/{p}/locations/{l}/triggers under the
	// same /v1 prefix and offers Cloud Build's trigger verbs its fan-in first;
	// eventarc-v1's count shows neither shadows the other.
	"cloudbuild-v1": 114,

	"redis-v1": 94,

	"firestore-v1": 120,

	"iam-v1": 274,

	"secretmanager-v1": 72,

	"serviceusage-v1": 20,

	// Cloud SQL Admin, on both the v1 and the v1beta4 spelling: the
	// workloadCaptures collection answers a declared 501, since the data plane
	// relays the engine's wire protocol as bytes and never reads a query to
	// capture or replay. instances.pointInTimeRestore's /v1 URI has the shape of
	// Cloud Resource Manager's project custom methods; that dispatcher forwards
	// the verb.
	"sqladmin-v1":      160,
	"sqladmin-v1beta4": 160,

	// Cloud Spanner: the "instances/{rest...}" mount has the shape of every path in
	// the document, so only its method-not-found for a tail it does not route
	// tells served from unserved.
	"spanner-v1": 198,
}

// gcpProbePrincipal is the subject of the token every probe presents, which
// the data-plane middleware verifies as it does a real client's.
const gcpProbePrincipal = "coverage-probe@sockerless.iam.gserviceaccount.com"

// gcpProbeBody is the malformed JSON every write probe carries: a handler that
// serves the method answers INVALID_ARGUMENT, a served answer, before it can
// create a network, boot an instance or run a build.
const gcpProbeBody = `{"sockerless-coverage-probe"`

// gcpProbeResult is one method's verdict plus the evidence behind it.
type gcpProbeResult struct {
	served bool
	// why names the arm of gcpClassifyProbe that decided, with the status
	// code and the response excerpt it decided on.
	why string
	// status is what the simulator answered, which separates the one unserved
	// answer that is a declaration — a 501 naming what is missing — from a mux
	// miss, an unknown verb, and a 500.
	status int
}

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
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	srv.WrapHandler(bearerAuthMiddleware(srv))
	now := time.Now()
	return &gcpCoverageProbe{
		handler: srv,
		token:   signAccessToken(gcpProbePrincipal, now, now.Add(time.Hour)),
		seen:    map[string]gcpProbeResult{},
	}
}

const (
	// gcpMuxMissBody is what ServeMux writes when no pattern matches. The
	// simulator's catch-all subtrees (the Cloud Storage XML data plane, the
	// Compute load-balancer data plane, the generic IAM verb route) call
	// http.NotFound for a request they do not own, so they mean the same.
	gcpMuxMissBody = "404 page not found"
	// gcpMuxMethodMissBody is what Go's ServeMux writes when patterns exist
	// for the path but none for this HTTP method.
	gcpMuxMethodMissBody = "Method Not Allowed"
)

var (
	// gcpUnknownMethodError matches errors that report the method, not the
	// resource, as unknown: Google's "Method not found." and the colon-verb
	// fan-ins' "unknown … action/verb". Either means no handler serves the method.
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
		return gcpProbeResult{served: false, status: status, why: "mux miss: no pattern matches the URI"}
	case status == http.StatusMethodNotAllowed && strings.HasPrefix(body, gcpMuxMethodMissBody):
		return gcpProbeResult{served: false, status: status, why: "mux miss: no pattern for this HTTP method"}
	case gcpUnknownMethodError.MatchString(body):
		return gcpProbeResult{served: false, status: status, why: "handler reports the method/verb as unknown: " + excerpt}
	case status >= http.StatusInternalServerError:
		return gcpProbeResult{served: false, status: status, why: "handler failed with " + http.StatusText(status) + ": " + excerpt}
	}
	return gcpProbeResult{served: true, status: status, why: "handler answered " + http.StatusText(status) + ": " + excerpt}
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
		// The project the probe creates: a handler that resolves the project
		// before doing its work reaches its own work.
		return []string{gcpTestProjectID}
	case "location":
		return []string{"us-central1", "global"}
	case "region":
		return []string{"us-central1"}
	case "zone":
		return []string{"us-central1-a"}
	case "billingaccount":
		return []string{"012345-678901-ABCDEF"}
	case "association":
		// Compute Engine's host methods take the association as a path, not a
		// name: the document spells it "reservations/reservation_name", or that
		// followed by a block and a sub-block. Discovery declares it without
		// reserved expansion, so a generated client percent-encodes the
		// separators and sends the whole path as one segment — which is the
		// coordinate the simulator must be reachable at, and so the one the
		// probe sends. A single bare token is a coordinate no client sends.
		return []string{
			"reservations%2Fsim-reservation",
			"reservations%2Fsim-reservation%2FreservationBlocks%2Fsim-block",
			"reservations%2Fsim-reservation%2FreservationBlocks%2Fsim-block%2FreservationSubBlocks%2Fsim-sub-block",
		}
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

// gcpPatternNamesSegments reports whether a path-parameter pattern names at
// least one literal collection segment, which makes it a shape other
// parameters of the same collection can be rendered from.
func gcpPatternNamesSegments(pattern string) bool {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	segs := gcpSplitPatternSegments(pattern)
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if _, ok := gcpPatternLiterals(seg); ok {
			return true
		}
	}
	return false
}

// gcpPatternIsShapeless reports whether a pattern permits a multi-segment value
// while describing none: a single character class that allows "/"
// ("[a-z](?:[-a-zA-Z0-9/]{0,255}[a-zA-Z0-9])?"). A client fills such a
// parameter with a resource path, but the pattern gives no way to render one,
// so the shape has to come from elsewhere. A pattern that splits into segments
// already describes its shape, whether or not it names literals.
func gcpPatternIsShapeless(pattern string) bool {
	pattern = strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	if len(gcpSplitPatternSegments(pattern)) != 1 {
		return false
	}
	for _, class := range regexp.MustCompile(`\[[^]]*]`).FindAllString(pattern, -1) {
		if strings.Contains(class, "/") && !strings.HasPrefix(class, "[^") {
			return true
		}
	}
	return false
}

// gcpPatternSamples renders the candidate values of a reserved-expansion path
// parameter ({+name}) from its Discovery pattern: literal segments stay, and
// each value segment is filled from the collection name before it
// ("^projects/[^/]+/locations/[^/]+/secrets/[^/]+$" renders
// "projects/sim-project/locations/us-central1/secrets/sim-secret"). Index 0 is
// the primary rendering; later ones vary the segments that offer alternatives.
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

func (p *gcpCoverageProbe) docCoverage(d *discoveryDoc) (served int, unserved []string) {
	served, unserved, _ = p.docCoverageDetail(d)
	return served, unserved
}

// docCoverageDetail also reports the unserved spellings that did not declare
// themselves with a 501 — a mux miss, an unknown verb, or a 500.
func (p *gcpCoverageProbe) docCoverageDetail(d *discoveryDoc) (served int, unserved, silent []string) {
	for _, m := range d.Methods {
		res := p.methodServed(d, m)
		if res.served {
			served++
			continue
		}
		unserved = append(unserved, m.HTTPMethod+" "+m.Path+"  ("+res.why+")")
		if res.status != http.StatusNotImplemented {
			silent = append(silent, m.HTTPMethod+" "+m.Path+"  ("+res.why+")")
		}
	}
	sort.Strings(unserved)
	sort.Strings(silent)
	return served, unserved, silent
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
// served count exactly.
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

// Every method spelling the simulator does not serve says so with a 501
// naming what is missing. The floor counts unserved spellings without caring
// why, so a gap that stopped declaring itself (a route gone to the mux's 404,
// a verb the dispatcher no longer knows) would keep the count while a client
// mistook the routing 404 for an absent resource.
func TestServiceConformance_GCPUnservedMethodsDeclareThemselves(t *testing.T) {
	p := newGCPCoverageProbe(t)
	var silent []string
	for _, d := range loadDiscoveryDocs(t) {
		_, _, quiet := p.docCoverageDetail(d)
		for _, m := range quiet {
			silent = append(silent, strings.TrimSuffix(d.File, ".discovery.json.gz")+": "+m)
		}
	}
	if len(silent) > 0 {
		sort.Strings(silent)
		t.Errorf("%d unserved method spelling(s) answered something other than a declared 501:\n  %s",
			len(silent), strings.Join(silent, "\n  "))
	}
}

// The reserved-expansion shape rule, pinned by the three patterns Google
// writes: one that names its segments, one that constrains a single segment,
// and one that permits a resource path while describing none of it.
func TestServiceConformance_GCPShapelessPatternsAreOnlyTheSlashPermittingOnes(t *testing.T) {
	shapeless := []string{
		// compute reservationSubBlocks {+parentResource}
		`[a-z](?:[-a-zA-Z0-9/]{0,255}[a-zA-Z0-9])?`,
	}
	shaped := []string{
		// compute reservationSubBlocks {+parentName}
		`^reservations/[^/]+/reservationBlocks/[^/]+$`,
		// bigquery {+tableId}: one segment, no slash
		`^[^/]+$`,
		// logging {+parent}: two segments, neither named
		`^[^/]+/[^/]+$`,
		"",
	}
	for _, pattern := range shapeless {
		if !gcpPatternIsShapeless(pattern) {
			t.Errorf("pattern %q describes no shape, but was read as if it did", pattern)
		}
	}
	for _, pattern := range shaped {
		if gcpPatternIsShapeless(pattern) {
			t.Errorf("pattern %q describes its own shape, and must not borrow another", pattern)
		}
	}

	// Only the one that names literal segments can stand in for another.
	if !gcpPatternNamesSegments(`^reservations/[^/]+/reservationBlocks/[^/]+$`) {
		t.Error("a pattern naming its collections is the shape others render from")
	}
	if gcpPatternNamesSegments(`^[^/]+/[^/]+$`) {
		t.Error("a pattern naming no collection cannot stand in for another")
	}
}
