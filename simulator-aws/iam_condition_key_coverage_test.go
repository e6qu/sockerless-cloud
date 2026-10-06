package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Per-action condition-key coverage. A condition on a key the request context
// lacks never matches, so a grant scoped by that condition denies the very
// request it was written to allow, and a deny scoped by it never fires.
// TestIAM_DeclaredConditionKeysAreResolvedOrClassified proves that the gate
// names every key some action declares; this measure proves, action by
// action, that the gate builds the key for a request of that action.
//
// For every operation the simulator serves, the probe renders the request a
// client sends from the vendored Smithy model, with every member filled,
// classifies it into the actions the gate authorizes it as — through the
// gate's own classifiers — and builds the gate's own condition context for
// each. Every (action, key) pair the vendored Service Reference declares for
// an action some served request is authorized as is satisfied when one of
// those contexts carries the key.
//
// An unsatisfied pair must be listed in iamConditionKeyGapsFile with the
// reason the gate cannot build it, and a listed pair that the gate now builds
// fails the test until its row is removed: the list is the exact set of
// open gaps, never a count of them.

const iamConditionKeyGapsFile = "testdata/iam_condition_key_gaps.tsv"

// iamConditionKeyGapReasons are the reasons a gap row may cite. A reason says
// either why AWS itself would leave the key out of a request of this kind, or
// what the simulator would have to model, read or seed before the key could be
// built — never merely that it is not built.
var iamConditionKeyGapReasons = map[string]string{
	"unmodelled": "The key is classified in iamUnmodelledConditionKeys, whose row says what the simulator " +
		"would have to model before any request could carry it.",
	"via-service": "AWS sets the key only when another AWS service makes the call on the caller's behalf. " +
		"Every request the gate authorizes is a direct client call, for which AWS leaves the key out too.",
	"resource-unseeded": "The key reports the state of the resource the request names (its tags, its " +
		"configuration). Creating one runs a container or boots a microVM — an Amazon ECS task, the Amazon EC2 " +
		"instance a Systems Manager request targets, an Amazon RDS snapshot's captured volume — which this " +
		"in-package measure does not, so the probe names a resource the simulator does not hold.",
	"create-names-no-resource": "Declared on an action that creates the resource. The request names no existing " +
		"resource whose tags the key could report.",
	"no-request-member": "The action's request has no member carrying this value, and AWS documents no value for " +
		"a request without one, so no request the simulator can receive settles the key.",
	"untag-carries-keys": "An untagging request names tag keys only, so there is no tag value to report as a " +
		"request tag. aws:TagKeys carries the keys.",
	"tag-on-create-authorization": "Set on the tagging action AWS additionally authorizes when a create carries " +
		"tags. The gate does not perform that additional authorization, so no request is authorized as the tagging " +
		"action with the create named (BUGS.md, tag-on-create authorization).",
	"bypass-governance-headers": "The gate authorizes s3:BypassGovernanceRetention only for a request carrying " +
		"x-amz-bypass-governance-retention — a delete or a retention change — and none of those carries the " +
		"object-write headers and tags the reference also declares on the action.",
	"access-grants-credential": "Present only on a request signed with credentials S3 Access Grants issued " +
		"(GetDataAccess). The probe signs as an IAM user, whose request rightly lacks it.",
	"access-point-addressed": "Present only on a request addressed through an access point. The probe addresses " +
		"the bucket.",
	"object-lambda-multi-region-access-point": "AWS documents the access-point keys for a data request made " +
		"through an access point, and documents no value for a control-plane request about an Object Lambda or " +
		"Multi-Region access point.",
	"undocumented-value": "AWS declares the key on the action but documents no value for it there: a listing " +
		"spans many configurations, a permission removal names only a statement id, and the transfer direction " +
		"of an invitation is not spelled anywhere in the vendored references.",
	"alexa-event-source": "Set on an invoke an Alexa Smart Home skill makes with the event source token " +
		"AddPermission names (lambda:EventSourceToken). Alexa is not an AWS API, so no request this simulator " +
		"receives is one.",
	"saml-session-tags": "Set on the sts:TagSession AWS STS authorizes, against the role's trust policy, when a " +
		"verified AssumeRoleWithSAML assertion carries PrincipalTag attributes. The probe's SAMLAssertion member is " +
		"no assertion a registered provider signed, so AWS STS refuses it before reading any attribute; " +
		"TestSTS_AssumeRoleWithSAMLSessionTags covers it with a signed one.",
	"job-operation-shape": "A batch job's Operation structure takes exactly one member. The probe fills every " +
		"member, which is an operation no client sends, and the gate rightly names none; a one-member request is " +
		"covered by TestS3ControlConditionKeysReadTheRequestedJob.",
	"access-request-unmodelled": "The just-in-time node access request a Session Manager session is opened " +
		"under. The simulator models no access requests.",
}

// iamConditionKeyPair is one key an action declares.
type iamConditionKeyPair struct {
	action, key string
}

// iamConditionKeyIsTemplated reports the literal prefix of a key whose last
// segment AWS writes as a placeholder (aws:RequestTag/${TagKey},
// kms:EncryptionContext:${EncryptionContextKey}, secretsmanager:ResourceTag/tag-key).
func iamConditionKeyIsTemplated(key string) (string, bool) {
	cut := strings.LastIndexAny(key, "/:")
	if cut <= 0 || cut == len(key)-1 {
		return "", false
	}
	tail := key[cut+1:]
	if strings.ContainsAny(tail, "${}<>") || tail == "tag-key" || tail == "key" {
		return key[:cut+1], true
	}
	return "", false
}

// iamConditionKeyPresent reports whether a context carries a key, matching
// IAM's case-insensitive key names and a templated key by its prefix.
func iamConditionKeyPresent(ctx map[string][]string, key string) bool {
	prefix, templated := iamConditionKeyIsTemplated(key)
	for name, values := range ctx {
		if len(values) == 0 {
			continue
		}
		if templated {
			if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
				return true
			}
			continue
		}
		if strings.EqualFold(name, key) {
			return true
		}
	}
	return false
}

// iamDeclaredActionKeys maps each service:Action to the keys it declares.
func iamDeclaredActionKeys(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for key, actions := range iamDeclaredConditionKeys(t) {
		for _, action := range actions {
			out[action] = append(out[action], key)
		}
	}
	return out
}

// iamKeyProbe is one served operation, rendered as the request a client sends.
type iamKeyProbe struct {
	label   string
	service string
	build   func() *http.Request
	// targets classifies a built request into what the gate authorizes it as.
	targets func(r *http.Request) []iamAuthorizationTarget
}

var iamRPCv2Route = regexp.MustCompile(`^POST /service/([^/]+)/operation/([^/]+)$`)

// iamServedKeyProbes renders every operation the simulator serves for a
// service the vendored Service Reference describes.
func iamServedKeyProbes(t *testing.T, srv *sim.Server,
	jsonRouter *AWSRouter, queryRouter *AWSQueryRouter, referenced map[string]bool,
	fixtures map[string]map[string]string,
) []iamKeyProbe {
	t.Helper()
	spec, err := loadSmithySpecSet("../specs/cloud-api/aws")
	if err != nil {
		t.Fatalf("load the vendored Smithy models: %v", err)
	}
	models := map[*smithyModelIndex]*iamKeyProbeModel{}
	model := func(idx *smithyModelIndex) *iamKeyProbeModel {
		if m, ok := models[idx]; ok {
			return m
		}
		m := newIAMKeyProbeModel(idx)
		models[idx] = m
		return m
	}
	fill := func(m *iamKeyProbeModel, operation string) *iamKeyProbeFill {
		return &iamKeyProbeFill{
			model: m, operation: operation,
			arn:      iamProbeARN(m.service, operation, "arn:aws:"+m.service+":us-east-1:"+iamProbeAccount+":probe"),
			fixtures: fixtures[m.service],
		}
	}
	// A member a request fills with one of several kinds of resource — the
	// Source of a blue/green deployment is a DB instance or a DB cluster —
	// carries its fixtures separated by newlines, and the operation is probed
	// once with each.
	fills := func(m *iamKeyProbeModel, operation string) []*iamKeyProbeFill {
		for key, value := range fixtures[m.service] {
			if !strings.HasPrefix(key, operation+":") || !strings.Contains(value, "\n") {
				continue
			}
			var out []*iamKeyProbeFill
			for _, alternative := range strings.Split(value, "\n") {
				f := fill(m, operation)
				f.fixtures = maps.Clone(f.fixtures)
				f.fixtures[key] = alternative
				out = append(out, f)
			}
			return out
		}
		return []*iamKeyProbeFill{fill(m, operation)}
	}
	// The awsJson and awsQuery classifier is the gate's own: the action
	// iamEnforce reads off the request, and every target iamAuthorizationTargets
	// adds for it.
	gateTargets := func(r *http.Request) []iamAuthorizationTarget {
		action, ok := iamActionForRequest(r)
		if !ok {
			return nil
		}
		return iamAuthorizationTargets(r, action)
	}

	var probes []iamKeyProbe
	for _, target := range jsonRouter.Targets() {
		prefix, operation, ok := strings.Cut(target, ".")
		if !ok {
			continue
		}
		idx := spec.byShort[prefix]
		if idx == nil {
			continue
		}
		m := model(idx)
		if !referenced[m.service] || m.inputs[operation] == "" {
			continue
		}
		contentType := "application/x-amz-json-1.1"
		if idx.protocols["awsJson1_0"] {
			contentType = "application/x-amz-json-1.0"
		}
		for _, f := range fills(m, operation) {
			probes = append(probes, iamKeyProbe{
				label:   "awsJson " + target,
				service: m.service,
				build:   func() *http.Request { return f.jsonRequest(target, contentType) },
				targets: gateTargets,
			})
		}
	}
	for version, actions := range queryRouter.VersionedActions() {
		candidates := spec.queryByVersion[version]
		if version == "" {
			candidates = spec.queryModels
		}
		for _, operation := range actions {
			for _, idx := range candidates {
				m := model(idx)
				if !referenced[m.service] || m.inputs[operation] == "" {
					continue
				}
				ec2 := idx.protocols["ec2Query"]
				for _, f := range fills(m, operation) {
					probes = append(probes, iamKeyProbe{
						label:   "query " + m.service + " " + operation,
						service: m.service,
						build:   func() *http.Request { return f.queryRequest(ec2) },
						targets: gateTargets,
					})
				}
			}
		}
	}

	byShort := map[string]*smithyModelIndex{}
	for _, idx := range spec.byShort {
		byShort[idx.serviceShort] = idx
	}
	s3ControlOperations := map[string]s3ControlRoute{}
	for _, route := range s3ControlGatedRoutes() {
		s3ControlOperations[route.pattern] = route
	}
	patterns := map[string]bool{}
	for _, pattern := range srv.RoutePatterns() {
		patterns[pattern] = true
		match := iamRPCv2Route.FindStringSubmatch(pattern)
		if match == nil {
			continue
		}
		idx := byShort[match[1]]
		if idx == nil {
			continue
		}
		m := model(idx)
		operation := match[2]
		if m.service != "cloudwatch" || !referenced[m.service] || m.inputs[operation] == "" {
			continue
		}
		f := fill(m, operation)
		probes = append(probes, iamKeyProbe{
			label:   "rpcv2Cbor " + m.service + " " + operation,
			service: m.service,
			build:   f.cborRequest,
			// Amazon CloudWatch is the one service served over RPC v2 CBOR;
			// see cloudWatchCBORAuthorized.
			targets: func(r *http.Request) []iamAuthorizationTarget { return cloudWatchCBORTargets(r, operation) },
		})
	}

	// A REST operation is served when the mux routes its request to a pattern
	// spelling the operation's own path; the data plane's wildcard routes
	// catch paths no service registered, and those are not this operation.
	for _, idx := range spec.byShort {
		useXML := idx.protocols["restXml"]
		if !useXML && !idx.protocols["restJson1"] {
			continue
		}
		m := model(idx)
		if !referenced[m.service] {
			continue
		}
		for operation, def := range idx.ops {
			if def.httpMethod == "" || m.inputs[operation] == "" {
				continue
			}
			f := fill(m, operation)
			build := func() *http.Request {
				r := f.restRequest(def, useXML)
				iamKeyProbeRoute(srv.Mux(), r)
				return r
			}
			probe := build()
			pattern := probe.Pattern
			if pattern == "" || !iamKeyProbeRouteServes(pattern, def) {
				continue
			}
			var targets func(*http.Request) []iamAuthorizationTarget
			switch {
			case m.service == "s3" && idx.serviceShort == "AmazonS3":
				// The operation name the data plane's own routes give: the
				// bucket routes name theirs with s3BucketOperationName, the
				// object routes with s3ObjectOperationName; see registerS3.
				targets = func(r *http.Request) []iamAuthorizationTarget {
					operation := s3ObjectOperationName(r, nil)
					switch {
					case r.Pattern == "GET /{$}":
						operation = "ListBuckets"
					case !strings.Contains(r.Pattern, "{key...}"):
						operation = s3BucketOperationName(r, nil)
					}
					return s3AuthorizationTargets(r, operation, []string{s3RequestResourceARN(r)})
				}
			case s3ControlOperations[pattern].operation != "":
				route := s3ControlOperations[pattern]
				targets = func(r *http.Request) []iamAuthorizationTarget {
					return s3ControlAuthorizationTargets(r, route.operation, route.resource)
				}
			case m.service == "lambda":
				targets = func(r *http.Request) []iamAuthorizationTarget {
					return lambdaAuthorizationTargets(r, operation, "*")
				}
			default:
				continue
			}
			probes = append(probes, iamKeyProbe{
				label:   "rest " + m.service + " " + operation,
				service: m.service,
				build:   build,
				targets: targets,
			})
		}
	}
	// A function URL is served at its own host, not as an API operation; the
	// seeded function's URL is probed as a client calls it.
	if config, ok := lambdaURLConfigs.Get("probe"); ok {
		probes = append(probes, iamKeyProbe{
			label:   "function URL " + config.FunctionUrl,
			service: "lambda",
			build: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, config.FunctionUrl, nil)
				iamKeyProbeEnvelope(r, "lambda")
				return r
			},
			targets: lambdaFunctionURLTargets,
		})
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].label < probes[j].label })
	return probes
}

// iamKeyProbeRoute matches a request against the simulator's mux and binds
// the path values the matched pattern names, without running the handler.
func iamKeyProbeRoute(mux *http.ServeMux, r *http.Request) {
	_, pattern := mux.Handler(r)
	if pattern == "" {
		return
	}
	one := http.NewServeMux()
	one.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	one.ServeHTTP(httptest.NewRecorder(), r)
}

// iamKeyProbeRouteServes reports whether a mux pattern spells the operation's
// own path.
func iamKeyProbeRouteServes(pattern string, def smithyOpDef) bool {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		return false
	}
	want := def.httpMethod
	if want == http.MethodHead {
		want = http.MethodGet
	}
	if method != want {
		return false
	}
	got, spec := normalizeAWSPath(path), normalizeAWSPath(def.httpURI)
	return got == spec || strings.ReplaceAll(got, "{+}", "{}") == spec ||
		got == strings.ReplaceAll(spec, "{+}", "{}")
}

// iamKeyProbeContexts builds the gate's condition context for every action a
// probe's request is authorized as, signed by signer, and for the
// iam:PassRole check the gate adds when the request passes a role.
func iamKeyProbeContexts(t *testing.T, probe iamKeyProbe, signer string) map[string][]map[string][]string {
	t.Helper()
	build := func() *http.Request {
		r := probe.build()
		r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"),
			"Credential="+iamProbeAccessKeyID+"/", "Credential="+signer+"/", 1))
		return r
	}
	out := map[string][]map[string][]string{}
	for _, target := range probe.targets(build()) {
		action := target.action
		// The gate authorizes no permissionless call, so it builds no context
		// for one; see iamPermissionlessAction.
		if iamPermissionlessAction(action) {
			continue
		}
		r := build()
		ctx := iamProbeContextFor(t, r, signer, action)
		for key, values := range target.context {
			ctx[key] = values
		}
		out[action] = append(out[action], ctx)
		principals, passes := iamPassRoleOperations[action]
		if !passes || len(iamPassedRoleARNs(build())) == 0 {
			continue
		}
		pass := iamProbeContextFor(t, build(), signer, "iam:PassRole")
		if len(principals) > 0 {
			pass["iam:PassedToService"] = principals
		}
		out["iam:PassRole"] = append(out["iam:PassRole"], pass)
	}
	return out
}

// iamConditionKeyGap is one row of the gaps file.
type iamConditionKeyGap struct {
	pair   iamConditionKeyPair
	reason string
	line   int
}

func loadIAMConditionKeyGaps(t *testing.T) []iamConditionKeyGap {
	t.Helper()
	f, err := os.Open(iamConditionKeyGapsFile)
	if err != nil {
		t.Fatalf("open %s: %v", iamConditionKeyGapsFile, err)
	}
	defer f.Close()
	var gaps []iamConditionKeyGap
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := scanner.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "\t")
		if len(fields) != 3 {
			t.Fatalf("%s:%d: want action<TAB>key<TAB>reason, got %q", iamConditionKeyGapsFile, line, text)
		}
		gaps = append(gaps, iamConditionKeyGap{
			pair:   iamConditionKeyPair{action: fields[0], key: fields[1]},
			reason: fields[2], line: line,
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read %s: %v", iamConditionKeyGapsFile, err)
	}
	return gaps
}

// TestIAMConditionKeyCoveragePerAction measures, per (action, declared key)
// pair, whether the gate builds the key for a request of that action, and
// holds the unsatisfied pairs to the exact list in iamConditionKeyGapsFile.
func TestIAMConditionKeyCoveragePerAction(t *testing.T) {
	srv, jsonRouter, queryRouter := buildConformanceSimulator(t)
	iamProbeSeedUser(t)
	declared := iamDeclaredActionKeys(t)
	referenced := map[string]bool{}
	for action := range declared {
		service, _, _ := strings.Cut(action, ":")
		referenced[service] = true
	}
	fixtures := iamSeedConditionKeyFixtures(t, srv)
	federated := iamSeedFederatedSessions(t)

	measured := map[string]bool{}
	satisfied := map[iamConditionKeyPair]bool{}
	probes := iamServedKeyProbes(t, srv, jsonRouter, queryRouter, referenced, fixtures)
	for _, probe := range probes {
		contexts := iamKeyProbeContexts(t, probe, iamProbeAccessKeyID)
		// A web-identity or SAML session is a caller AWS STS alone tells
		// apart, by the claims it carries into the AssumeRole it chains to.
		if probe.service == "sts" {
			for _, signer := range federated {
				for action, more := range iamKeyProbeContexts(t, probe, signer) {
					contexts[action] = append(contexts[action], more...)
				}
			}
		}
		for action, contexts := range contexts {
			measured[action] = true
			for _, key := range declared[action] {
				pair := iamConditionKeyPair{action, key}
				for _, ctx := range contexts {
					if iamConditionKeyPresent(ctx, key) {
						satisfied[pair] = true
						break
					}
				}
			}
		}
	}

	var pairs, unserved int
	unsatisfied := map[iamConditionKeyPair]bool{}
	byService := map[string][3]int{}
	for action, keys := range declared {
		service, _, _ := strings.Cut(action, ":")
		for _, key := range keys {
			counts := byService[service]
			if !measured[action] {
				unserved++
				counts[2]++
				byService[service] = counts
				continue
			}
			pairs++
			counts[0]++
			pair := iamConditionKeyPair{action, key}
			if !satisfied[pair] {
				unsatisfied[pair] = true
				counts[1]++
			}
			byService[service] = counts
		}
	}

	var report strings.Builder
	fmt.Fprintf(&report, "%d served operations probed; %d (action, key) pairs measured, %d unsatisfied; "+
		"%d pairs on actions no served request is authorized as\n",
		len(probes), pairs, len(unsatisfied), unserved)
	services := make([]string, 0, len(byService))
	for service := range byService {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		fmt.Fprintf(&report, "  %-24s %4d pairs, %4d unsatisfied, %4d on actions no served request is authorized as\n",
			service, byService[service][0], byService[service][1], byService[service][2])
	}
	t.Log(report.String())

	listed := map[iamConditionKeyPair]bool{}
	for _, gap := range loadIAMConditionKeyGaps(t) {
		if listed[gap.pair] {
			t.Errorf("%s:%d: %s %s is listed twice", iamConditionKeyGapsFile, gap.line, gap.pair.action, gap.pair.key)
		}
		listed[gap.pair] = true
		if _, classified := iamClassifiedUnmodelled(gap.pair.key); (gap.reason == "unmodelled") != classified {
			t.Errorf("%s:%d: %s %s: a row cites unmodelled exactly when iamUnmodelledConditionKeys classifies the key",
				iamConditionKeyGapsFile, gap.line, gap.pair.action, gap.pair.key)
		}
		if _, ok := iamConditionKeyGapReasons[gap.reason]; !ok {
			t.Errorf("%s:%d: %s %s cites unknown reason %q", iamConditionKeyGapsFile, gap.line,
				gap.pair.action, gap.pair.key, gap.reason)
		}
		switch {
		case !measured[gap.pair.action] || !iamConditionKeyDeclares(declared, gap.pair):
			t.Errorf("%s:%d: %s %s is not a measured pair — no served request is authorized as the action, "+
				"or the action does not declare the key; drop the row", iamConditionKeyGapsFile, gap.line,
				gap.pair.action, gap.pair.key)
		case !unsatisfied[gap.pair]:
			t.Errorf("%s:%d: the gate now builds %s for %s — drop the row", iamConditionKeyGapsFile, gap.line,
				gap.pair.key, gap.pair.action)
		}
	}
	var missing []string
	for pair := range unsatisfied {
		if !listed[pair] {
			missing = append(missing, pair.action+"\t"+pair.key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d (action, key) pairs the gate does not build are not listed in %s — build each key, "+
			"or add the row with its reason:\n%s", len(missing), iamConditionKeyGapsFile, strings.Join(missing, "\n"))
	}
}

func iamConditionKeyDeclares(declared map[string][]string, pair iamConditionKeyPair) bool {
	for _, key := range declared[pair.action] {
		if key == pair.key {
			return true
		}
	}
	return false
}

// iamSeedConditionKeyFixtures creates, through each service's own API, the
// resources whose state some condition keys report — a resource's tags, a
// role's permissions boundary, a table's partition key, an access point's
// network origin — and returns, per service, the request-member values that
// name them. Most are named "probe", the name every probe sends; the members
// listed here are the ones whose value the service assigns.
func iamSeedConditionKeyFixtures(t *testing.T, srv *sim.Server) map[string]map[string]string {
	t.Helper()
	call := func(method, path, contentType, body string, header map[string]string) string {
		t.Helper()
		r := httptest.NewRequest(method, "https://sim.local"+path, strings.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		for key, value := range header {
			r.Header.Set(key, value)
		}
		signSeedControlPlane(r)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code >= 300 {
			t.Fatalf("seed %s %s %s: %d %s", method, path, header["X-Amz-Target"], rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	jsonCall := func(target, contentType, body string) string {
		t.Helper()
		return call(http.MethodPost, "/", contentType, body, map[string]string{"X-Amz-Target": target})
	}
	query := func(form url.Values) string {
		t.Helper()
		return call(http.MethodPost, "/", "application/x-www-form-urlencoded", form.Encode(), nil)
	}
	field := func(document, pattern string) string {
		t.Helper()
		match := regexp.MustCompile(pattern).FindStringSubmatch(document)
		if match == nil {
			t.Fatalf("seed: %s not found in %s", pattern, document)
		}
		return match[1]
	}
	const json11, json10 = "application/x-amz-json-1.1", "application/x-amz-json-1.0"
	fixtures := map[string]map[string]string{}

	// Amazon EC2: the subnet a Lambda function's VPC configuration names.
	vpc := field(query(url.Values{"Action": {"CreateVpc"}, "Version": {"2016-11-15"}, "CidrBlock": {"10.0.0.0/16"}}),
		`<vpcId>([^<]+)</vpcId>`)
	subnet := field(query(url.Values{"Action": {"CreateSubnet"}, "Version": {"2016-11-15"},
		"VpcId": {vpc}, "CidrBlock": {"10.0.1.0/24"}}), `<subnetId>([^<]+)</subnetId>`)

	// Amazon S3: a tagged bucket, a tagged object in it, and a tagged access
	// point over it.
	call(http.MethodPut, "/probe", "", "", nil)
	call(http.MethodPut, "/probe?tagging", "application/xml",
		`<Tagging><TagSet><Tag><Key>owner</Key><Value>platform</Value></Tag></TagSet></Tagging>`, nil)
	call(http.MethodPut, "/probe/probe", "text/plain", "probe", map[string]string{"x-amz-tagging": "owner=platform"})
	call(http.MethodPut, "/v20180820/accesspoint/probe", "application/xml",
		`<CreateAccessPointRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Bucket>probe</Bucket>`+
			`<Tags><Tag><Key>owner</Key><Value>platform</Value></Tag></Tags></CreateAccessPointRequest>`,
		map[string]string{"x-amz-account-id": iamProbeAccount})

	// Amazon SQS and Amazon SNS: a tagged queue and topic.
	jsonCall("AmazonSQS.CreateQueue", json10, `{"QueueName":"probe","tags":{"owner":"platform"}}`)
	query(url.Values{"Action": {"CreateTopic"}, "Version": {"2010-03-31"}, "Name": {"probe"},
		"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"platform"}})

	// Amazon DynamoDB: a tagged table keyed by the attribute every probe
	// names.
	jsonCall("DynamoDB_20120810.CreateTable", json10, `{"TableName":"probe","BillingMode":"PAY_PER_REQUEST",`+
		`"AttributeDefinitions":[{"AttributeName":"probe","AttributeType":"S"}],`+
		`"KeySchema":[{"AttributeName":"probe","KeyType":"HASH"}],"Tags":[{"Key":"owner","Value":"platform"}]}`)

	// AWS KMS: a tagged key behind the alias every probe names it by.
	key := field(jsonCall("TrentService.CreateKey", json11, `{"Tags":[{"TagKey":"owner","TagValue":"platform"}]}`),
		`"KeyId":"([^"]+)"`)
	jsonCall("TrentService.CreateAlias", json11, `{"AliasName":"alias/probe","TargetKeyId":"`+key+`"}`)
	// A use of the key, which is what its idle days count from, and a grant
	// whose constraints a retirement reports.
	jsonCall("TrentService.Encrypt", json11, `{"KeyId":"`+key+`","Plaintext":"cHJvYmU="}`)
	grant := field(jsonCall("TrentService.CreateGrant", json11, `{"KeyId":"`+key+`","GranteePrincipal":`+
		`"arn:aws:iam::`+iamProbeAccount+`:role/probe","Operations":["Decrypt"],`+
		`"Constraints":{"EncryptionContextSubset":{"owner":"platform"}}}`), `"GrantId":"([^"]+)"`)
	fixtures["kms"] = map[string]string{
		"keyid": "alias/probe", "sourcekeyid": "alias/probe", "destinationkeyid": "alias/probe", "grantid": grant,
	}

	// Amazon ECR: a tagged repository.
	jsonCall("AmazonEC2ContainerRegistry_V20150921.CreateRepository", json11,
		`{"repositoryName":"probe","tags":[{"Key":"owner","Value":"platform"}]}`)

	// Amazon ECS: a tagged cluster, a sized task definition, and a service
	// running it at a desired count of zero.
	const ecs = "AmazonEC2ContainerServiceV20141113."
	jsonCall(ecs+"CreateCluster", json11, `{"clusterName":"probe","tags":[{"key":"owner","value":"platform"}]}`)
	jsonCall(ecs+"RegisterTaskDefinition", json11, `{"family":"probe","cpu":"256","memory":"512",`+
		`"containerDefinitions":[{"name":"probe","image":"alpine"}],"tags":[{"key":"owner","value":"platform"}]}`)
	jsonCall(ecs+"CreateService", json11, `{"cluster":"probe","serviceName":"probe","taskDefinition":"probe",`+
		`"desiredCount":0,"tags":[{"key":"owner","value":"platform"}]}`)
	// A tagged capacity provider, a container instance registered with the
	// cluster, and a task set in the service, each named by the id the
	// service assigns.
	jsonCall(ecs+"CreateCapacityProvider", json11, `{"name":"probe","autoScalingGroupProvider":{`+
		`"autoScalingGroupArn":"arn:aws:autoscaling:us-east-1:`+iamProbeAccount+`:autoScalingGroup:`+
		`11111111-2222-3333-4444-555555555555:autoScalingGroupName/probe"},"tags":[{"key":"owner","value":"platform"}]}`)
	containerInstance := field(jsonCall(ecs+"RegisterContainerInstance", json11,
		`{"cluster":"probe","tags":[{"key":"owner","value":"platform"}]}`), `"containerInstanceArn":"[^"]*/([^"/]+)"`)
	taskSet := field(jsonCall(ecs+"CreateTaskSet", json11, `{"cluster":"probe","service":"probe",`+
		`"taskDefinition":"probe:1","tags":[{"key":"owner","value":"platform"}]}`), `"id":"([^"]+)"`)
	fixtures["ecs"] = map[string]string{
		"containerinstance": containerInstance, "containerinstances": containerInstance, "targetid": containerInstance,
		"taskset": taskSet, "tasksets": taskSet, "taskdefinitions": "probe:1",
	}

	// AWS Identity and Access Management: a boundary policy, and a tagged
	// user and role that carry it.
	query(url.Values{"Action": {"CreatePolicy"}, "Version": {"2010-05-08"}, "PolicyName": {"probe"},
		"PolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`}})
	boundary := "arn:aws:iam::" + iamProbeAccount + ":policy/probe"
	query(url.Values{"Action": {"CreateUser"}, "Version": {"2010-05-08"}, "UserName": {"probe"},
		"PermissionsBoundary": {boundary}, "Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"platform"}})
	query(url.Values{"Action": {"CreateRole"}, "Version": {"2010-05-08"}, "RoleName": {"probe"},
		"AssumeRolePolicyDocument": {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`},
		"PermissionsBoundary": {boundary}, "Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"platform"}})

	// Amazon S3 Access Grants: a tagged instance, a tagged location behind the
	// seeded role, and a tagged grant in it.
	const s3Control = `xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"`
	const s3ControlTags = `<Tags><Tag><Key>owner</Key><Value>platform</Value></Tag></Tags>`
	accountHeader := map[string]string{"x-amz-account-id": iamProbeAccount}
	call(http.MethodPost, "/v20180820/accessgrantsinstance", "application/xml",
		`<CreateAccessGrantsInstanceRequest `+s3Control+`>`+s3ControlTags+`</CreateAccessGrantsInstanceRequest>`,
		accountHeader)
	location := field(call(http.MethodPost, "/v20180820/accessgrantsinstance/location", "application/xml",
		`<CreateAccessGrantsLocationRequest `+s3Control+`><LocationScope>s3://probe/*</LocationScope>`+
			`<IAMRoleArn>arn:aws:iam::`+iamProbeAccount+`:role/probe</IAMRoleArn>`+s3ControlTags+
			`</CreateAccessGrantsLocationRequest>`, accountHeader),
		`<AccessGrantsLocationId>([^<]+)</AccessGrantsLocationId>`)
	accessGrant := field(call(http.MethodPost, "/v20180820/accessgrantsinstance/grant", "application/xml",
		`<CreateAccessGrantRequest `+s3Control+`><AccessGrantsLocationId>`+location+`</AccessGrantsLocationId>`+
			`<Grantee><GranteeType>IAM</GranteeType><GranteeIdentifier>arn:aws:iam::`+iamProbeAccount+
			`:role/probe</GranteeIdentifier></Grantee><Permission>READ</Permission>`+s3ControlTags+
			`</CreateAccessGrantRequest>`, accountHeader),
		`<AccessGrantId>([^<]+)</AccessGrantId>`)
	fixtures["s3"] = map[string]string{"accessgrantslocationid": location, "accessgrantid": accessGrant}

	// AWS Lambda: a tagged function in the seeded subnet, its URL, a
	// permission, and an event-source mapping from the seeded queue.
	var archive bytes.Buffer
	zipWriter := zip.NewWriter(&archive)
	entry, err := zipWriter.Create("bootstrap")
	if err != nil {
		t.Fatalf("seed: create the Lambda ZIP entry: %v", err)
	}
	if _, err := entry.Write([]byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("seed: write the Lambda ZIP entry: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("seed: close the Lambda ZIP: %v", err)
	}
	call(http.MethodPost, "/2015-03-31/functions", "application/json", fmt.Sprintf(
		`{"FunctionName":"probe","Role":"arn:aws:iam::%s:role/probe","Code":{"ZipFile":%q},`+
			`"Handler":"bootstrap","Runtime":"provided.al2023","PackageType":"Zip","Tags":{"owner":"platform"}}`,
		iamProbeAccount, base64.StdEncoding.EncodeToString(archive.Bytes())), nil)
	call(http.MethodPost, "/2021-10-31/functions/probe/url", "application/json", `{"AuthType":"AWS_IAM"}`, nil)
	call(http.MethodPost, "/2015-03-31/functions/probe/policy", "application/json",
		`{"StatementId":"probe","Action":"lambda:InvokeFunction","Principal":"s3.amazonaws.com"}`, nil)
	mapping := field(call(http.MethodPost, "/2015-03-31/event-source-mappings", "application/json",
		`{"FunctionName":"probe","EventSourceArn":"arn:aws:sqs:us-east-1:`+iamProbeAccount+`:probe"}`, nil),
		`"UUID":"([^"]+)"`)
	fixtures["lambda"] = map[string]string{"uuid": mapping, "subnetids": subnet}

	// AWS Secrets Manager: a tagged secret encrypted under the seeded key and
	// rotated by the seeded function.
	jsonCall("secretsmanager.CreateSecret", json11, `{"Name":"probe","SecretString":"probe","KmsKeyId":"alias/probe",`+
		`"Tags":[{"Key":"owner","Value":"platform"}]}`)
	jsonCall("secretsmanager.RotateSecret", json11, `{"SecretId":"probe","RotateImmediately":false,`+
		`"RotationLambdaARN":"arn:aws:lambda:us-east-1:`+iamProbeAccount+`:function:probe",`+
		`"RotationRules":{"AutomaticallyAfterDays":30}}`)
	fixtures["secretsmanager"] = map[string]string{"kmskeyid": "alias/probe"}

	// AWS Systems Manager: a tagged Command document.
	jsonCall("AmazonSSM.CreateDocument", json11, `{"Name":"probe","DocumentType":"Command",`+
		`"Content":"{\"schemaVersion\":\"2.2\",\"mainSteps\":[{\"action\":\"aws:runShellScript\",`+
		`\"name\":\"probe\",\"inputs\":{\"runCommand\":[\"true\"]}}]}",`+
		`"Tags":[{"Key":"owner","Value":"platform"}]}`)
	// A tagged parameter, association running the document, Automation
	// execution, and cloud connector, each named by the id the service
	// assigns.
	const ssmTags = `"Tags":[{"Key":"owner","Value":"platform"}]`
	jsonCall("AmazonSSM.PutParameter", json11, `{"Name":"probe","Type":"String","Value":"probe",`+ssmTags+`}`)
	association := field(jsonCall("AmazonSSM.CreateAssociation", json11, `{"Name":"probe",`+ssmTags+`}`),
		`"AssociationId":"([^"]+)"`)
	jsonCall("AmazonSSM.CreateDocument", json11, `{"Name":"probe-automation","DocumentType":"Automation",`+
		`"Content":"{\"schemaVersion\":\"0.3\",\"mainSteps\":[{\"action\":\"aws:sleep\",`+
		`\"name\":\"probe\",\"inputs\":{\"Duration\":\"PT0S\"}}]}"}`)
	automation := field(jsonCall("AmazonSSM.StartAutomationExecution", json11,
		`{"DocumentName":"probe-automation",`+ssmTags+`}`), `"AutomationExecutionId":"([^"]+)"`)
	cloudConnector := field(jsonCall("AmazonSSM.CreateCloudConnector", json11, `{"DisplayName":"probe",`+
		`"RoleArn":"arn:aws:iam::`+iamProbeAccount+`:role/probe",`+
		`"ConfigConnectorArn":"arn:aws:config:us-east-1:`+iamProbeAccount+`:connector/probe",`+
		`"Configuration":{"AzureConfiguration":{"TenantId":"probe","ApplicationId":"probe"}},`+ssmTags+`}`),
		`"CloudConnectorId":"([^"]+)"`)
	fixtures["ssm"] = map[string]string{
		"associationid": association, "associationids": association,
		"automationexecutionid": automation, "cloudconnectorid": cloudConnector,
		"AddTagsToResource:resourceid": association, "ListTagsForResource:resourceid": association,
		"RemoveTagsFromResource:resourceid": association,
	}

	// AWS WAF: a tagged IP set, regex pattern set, rule group and web ACL in
	// the CLOUDFRONT scope, which is the scope every probe names.
	const waf = "AWSWAF_20190729."
	wafVisibility := `"VisibilityConfig":{"SampledRequestsEnabled":false,"CloudWatchMetricsEnabled":false,` +
		`"MetricName":"probe"}`
	wafTags := `"Tags":[{"Key":"owner","Value":"platform"}]`
	wafCreate := func(operation, body string) (string, string) {
		t.Helper()
		summary := jsonCall(waf+operation, json11, `{"Name":"probe","Scope":"CLOUDFRONT",`+body+`,`+wafTags+`}`)
		return field(summary, `"Id":"([^"]+)"`), field(summary, `"ARN":"([^"]+)"`)
	}
	ipSet, ipSetARN := wafCreate("CreateIPSet", `"IPAddressVersion":"IPV4","Addresses":["10.0.0.0/8"]`)
	regexSet, _ := wafCreate("CreateRegexPatternSet", `"RegularExpressionList":[{"RegexString":"probe"}]`)
	ruleGroup, ruleGroupARN := wafCreate("CreateRuleGroup", `"Capacity":10,`+wafVisibility)
	webACL, webACLARN := wafCreate("CreateWebACL", `"DefaultAction":{"Allow":{}},`+wafVisibility)
	fixtures["wafv2"] = map[string]string{
		"GetIPSet:id": ipSet, "UpdateIPSet:id": ipSet,
		"GetRegexPatternSet:id": regexSet, "UpdateRegexPatternSet:id": regexSet,
		"GetRuleGroup:id": ruleGroup, "UpdateRuleGroup:id": ruleGroup, "GetRuleGroup:arn": ruleGroupARN,
		"GetWebACL:id": webACL, "UpdateWebACL:id": webACL, "GetWebACL:arn": webACLARN,
		"GetRateBasedStatementManagedKeys:webaclid": webACL, "GetLoggingConfiguration:resourcearn": webACLARN,
		"ListTagsForResource:resourcearn": ipSetARN, "TagResource:resourcearn": ipSetARN,
		"UntagResource:resourcearn": ipSetARN,
	}

	// AWS Cloud Map: an HTTP namespace and a service in it, named by the ids
	// the service assigns.
	const cloudMap = "Route53AutoNaming_v20170314."
	operation := field(jsonCall(cloudMap+"CreateHttpNamespace", json11, `{"Name":"probe"}`),
		`"OperationId":"([^"]+)"`)
	namespace := field(jsonCall(cloudMap+"GetOperation", json11, `{"OperationId":"`+operation+`"}`),
		`"NAMESPACE":"([^"]+)"`)
	cloudMapService := field(jsonCall(cloudMap+"CreateService", json11, `{"Name":"probe","NamespaceId":"`+
		namespace+`"}`), `"Id":"([^"]+)"`)
	fixtures["servicediscovery"] = map[string]string{
		"namespaceid": namespace, "serviceid": cloudMapService,
		"DeleteService:id": cloudMapService, "UpdateService:id": cloudMapService,
	}

	// Amazon EventBridge: a tagged rule on a bus named like it.
	jsonCall("AWSEvents.CreateEventBus", json11, `{"Name":"probe"}`)
	jsonCall("AWSEvents.PutRule", json11, `{"Name":"probe","EventBusName":"probe","EventPattern":"{\"source\":[\"probe\"]}",`+
		`"Tags":[{"Key":"owner","Value":"platform"}]}`)

	// The event pattern a rule is put with, matching on every field
	// EventBridge declares a key for.
	fixtures["events"] = map[string]string{"eventpattern": `{"source":["aws.ec2"],"detail-type":["probe"],` +
		`"detail":{"service":["ec2"],"eventTypeCode":["probe"],"userIdentity":{"principalId":["probe"]}}}`,
		"resourcearn": "arn:aws:events:us-east-1:" + iamProbeAccount + ":rule/probe/probe"}

	// A Spot price and a Contributor Insights rule body, which their keys are
	// read out of.
	fixtures["autoscaling"] = map[string]string{"spotprice": "0.05"}
	fixtures["cloudwatch"] = map[string]string{"ruledefinition": `{"Schema":{"Name":"CloudWatchLogRule","Version":1},` +
		`"LogGroupNames":["probe"],"LogFormat":"JSON","Contribution":{"Keys":["$.ip"]},"AggregateOn":"Count"}`}

	// AWS Step Functions: a state machine with a published version, which a
	// request qualifies the machine's ARN by.
	version := field(jsonCall("AWSStepFunctions.CreateStateMachine", json10, `{"name":"probe","publish":true,`+
		`"roleArn":"arn:aws:iam::`+iamProbeAccount+`:role/probe",`+
		`"definition":"{\"StartAt\":\"Done\",\"States\":{\"Done\":{\"Type\":\"Succeed\"}}}"}`),
		`"stateMachineVersionArn":"([^"]+)"`)
	alias := field(jsonCall("AWSStepFunctions.CreateStateMachineAlias", json10, `{"name":"live",`+
		`"routingConfiguration":[{"stateMachineVersionArn":"`+version+`","weight":100}]}`),
		`"stateMachineAliasArn":"([^"]+)"`)
	fixtures["states"] = map[string]string{
		"statemachinearn": version, "statemachineversionarn": version, "statemachinealiasarn": alias,
	}

	// Elastic Load Balancing: a tagged load balancer, a listener on it with a
	// rule, a target group, and a trust store reading its CA bundle from Amazon
	// S3, each named by the ARN the service assigns.
	const elb = "2015-12-01"
	elbTags := url.Values{"Tags.member.1.Key": {"owner"}, "Tags.member.1.Value": {"platform"}}
	elbCall := func(action string, form url.Values) string {
		t.Helper()
		form.Set("Action", action)
		form.Set("Version", elb)
		for key, values := range elbTags {
			form[key] = values
		}
		return query(form)
	}
	loadBalancer := field(elbCall("CreateLoadBalancer", url.Values{"Name": {"probe"}, "Subnets.member.1": {subnet}}),
		`<LoadBalancerArn>([^<]+)</LoadBalancerArn>`)
	targetGroup := field(elbCall("CreateTargetGroup", url.Values{"Name": {"probe"}, "Protocol": {"HTTP"},
		"Port": {"80"}, "VpcId": {vpc}}), `<TargetGroupArn>([^<]+)</TargetGroupArn>`)
	listenerPort := iamProbeFreePort(t)
	listener := field(elbCall("CreateListener", url.Values{"LoadBalancerArn": {loadBalancer}, "Protocol": {"HTTP"},
		"Port": {listenerPort}, "DefaultActions.member.1.Type": {"forward"},
		"DefaultActions.member.1.TargetGroupArn": {targetGroup}}), `<ListenerArn>([^<]+)</ListenerArn>`)
	rule := field(elbCall("CreateRule", url.Values{"ListenerArn": {listener}, "Priority": {"1"},
		"Conditions.member.1.Field": {"path-pattern"}, "Conditions.member.1.Values.member.1": {"/probe"},
		"Actions.member.1.Type": {"forward"}, "Actions.member.1.TargetGroupArn": {targetGroup}}),
		`<RuleArn>([^<]+)</RuleArn>`)
	call(http.MethodPut, "/probe/probe-ca.pem", "application/x-pem-file", iamProbeCABundle(t), nil)
	trustStore := field(elbCall("CreateTrustStore", url.Values{"Name": {"probe"},
		"CaCertificatesBundleS3Bucket": {"probe"}, "CaCertificatesBundleS3Key": {"probe-ca.pem"}}),
		`<TrustStoreArn>([^<]+)</TrustStoreArn>`)
	fixtures["elasticloadbalancing"] = map[string]string{
		"loadbalancerarn": loadBalancer, "loadbalancerarns": loadBalancer, "resourcearns": loadBalancer,
		"targetgrouparn": targetGroup, "targetgrouparns": targetGroup,
		"listenerarn": listener, "listenerarns": listener, "rulearn": rule, "rulearns": rule,
		"truststorearn": trustStore, "truststorearns": trustStore, "resourcearn": trustStore,
	}

	// Amazon ElastiCache: one tagged resource of every type its tagging
	// operations accept, named "probe" except for the global replication
	// group, whose id the service prefixes.
	const elastiCache = "2015-02-02"
	ecCall := func(action string, form url.Values) {
		t.Helper()
		form.Set("Action", action)
		form.Set("Version", elastiCache)
		form.Set("Tags.Tag.1.Key", "owner")
		form.Set("Tags.Tag.1.Value", "platform")
		query(form)
	}
	ecCall("CreateCacheParameterGroup", url.Values{"CacheParameterGroupName": {"probe"},
		"CacheParameterGroupFamily": {"redis7"}, "Description": {"probe"}})
	ecCall("CreateCacheSubnetGroup", url.Values{"CacheSubnetGroupName": {"probe"},
		"CacheSubnetGroupDescription": {"probe"}, "SubnetIds.SubnetIdentifier.1": {subnet}})
	ecCall("CreateCacheCluster", url.Values{"CacheClusterId": {"probe"}, "Engine": {"redis"},
		"CacheNodeType": {"cache.t3.micro"}, "NumCacheNodes": {"1"}})
	ecCall("CreateReplicationGroup", url.Values{"ReplicationGroupId": {"probe"},
		"ReplicationGroupDescription": {"probe"}, "Engine": {"redis"}, "CacheNodeType": {"cache.t3.micro"}})
	ecCall("CreateSnapshot", url.Values{"SnapshotName": {"probe"}, "CacheClusterId": {"probe"}})
	ecCall("CreateUser", url.Values{"UserId": {"probe"}, "UserName": {"probe"}, "Engine": {"redis"},
		"AccessString": {"on ~* +@all"}, "NoPasswordRequired": {"true"}})
	ecCall("CreateUserGroup", url.Values{"UserGroupId": {"probe"}, "Engine": {"redis"},
		"UserIds.member.1": {"probe"}})
	ecCall("CreateCacheSecurityGroup", url.Values{"CacheSecurityGroupName": {"probe"}, "Description": {"probe"}})
	ecCall("PurchaseReservedCacheNodesOffering", url.Values{"ReservedCacheNodeId": {"probe"},
		"ReservedCacheNodesOfferingId": {ecReservedCacheNodesOfferings[0].Id}})
	globalGroup := field(query(url.Values{"Action": {"CreateGlobalReplicationGroup"}, "Version": {elastiCache},
		"GlobalReplicationGroupIdSuffix": {"probe"}, "PrimaryReplicationGroupId": {"probe"},
		"Tags.Tag.1.Key": {"owner"}, "Tags.Tag.1.Value": {"platform"}}),
		`<GlobalReplicationGroupId>([^<]+)</GlobalReplicationGroupId>`)
	fixtures["elasticache"] = map[string]string{"globalreplicationgroupid": globalGroup}

	// Amazon ECS: a sized daemon task definition, whose size a daemon runs at.
	daemonDefinition := field(jsonCall(ecs+"RegisterDaemonTaskDefinition", json11, `{"family":"probe",`+
		`"cpu":"256","memory":"512","containerDefinitions":[{"name":"probe","image":"alpine"}]}`),
		`"daemonTaskDefinitionArn":"([^"]+)"`)
	fixtures["ecs"]["daemontaskdefinitionarn"] = daemonDefinition

	// Amazon RDS: tagged parameter groups, a tagged DB cluster and DB instance
	// of engines whose data plane runs no container, a blue/green deployment
	// of the cluster and a zero-ETL integration from it. A blue/green
	// deployment clones either kind of database, so it is probed with each.
	const rdsVersion = "2014-10-31"
	rdsCall := func(action string, form url.Values) string {
		t.Helper()
		form.Set("Action", action)
		form.Set("Version", rdsVersion)
		form.Set("Tags.Tag.1.Key", "owner")
		form.Set("Tags.Tag.1.Value", "platform")
		return query(form)
	}
	rdsCall("CreateDBParameterGroup", url.Values{"DBParameterGroupName": {"probe"},
		"DBParameterGroupFamily": {"mysql8.0"}, "Description": {"probe"}})
	rdsCall("CreateDBClusterParameterGroup", url.Values{"DBClusterParameterGroupName": {"probe"},
		"DBParameterGroupFamily": {"mysql8.0"}, "Description": {"probe"}})
	cluster := field(rdsCall("CreateDBCluster", url.Values{"DBClusterIdentifier": {"probe"}, "Engine": {"mysql"},
		"DatabaseName": {"probe"}, "StorageEncrypted": {"true"}, "AllocatedStorage": {"100"},
		"MasterUsername": {"probe"}, "DBClusterInstanceClass": {"db.m6gd.large"}}),
		`<DBClusterArn>([^<]+)</DBClusterArn>`)
	instance := field(rdsCall("CreateDBInstance", url.Values{"DBInstanceIdentifier": {"probe"},
		"Engine": {"sqlserver-ex"}, "DBInstanceClass": {"db.t3.micro"}, "AllocatedStorage": {"20"},
		"MasterUsername": {"probe"}, "MasterUserPassword": {"probe-password"}}),
		`<DBInstanceArn>([^<]+)</DBInstanceArn>`)
	deployment := field(rdsCall("CreateBlueGreenDeployment", url.Values{"BlueGreenDeploymentName": {"probe"},
		"Source": {cluster}}), `<BlueGreenDeploymentIdentifier>([^<]+)</BlueGreenDeploymentIdentifier>`)
	integration := field(rdsCall("CreateIntegration", url.Values{"IntegrationName": {"probe"}, "SourceArn": {cluster},
		"TargetArn": {"arn:aws:redshift-serverless:us-east-1:" + iamProbeAccount + ":namespace/probe"}}),
		`<IntegrationArn>([^<]+)</IntegrationArn>`)
	fixtures["rds"] = map[string]string{
		"CreateBlueGreenDeployment:source": cluster + "\n" + instance,
		"bluegreendeploymentidentifier":    deployment, "integrationidentifier": integration,
	}

	// AWS Certificate Manager: a certificate ACM issues for a name.
	certificate := field(jsonCall("CertificateManager.RequestCertificate", json11,
		`{"DomainName":"probe.example.com","ValidationMethod":"DNS"}`), `"CertificateArn":"([^"]+)"`)
	fixtures["acm"] = map[string]string{"certificatearn": certificate}

	// Amazon EC2 Auto Scaling: a launch configuration and a launch template of
	// an image. An instance refresh names the template by the id Amazon EC2
	// assigns and the version it moves to.
	query(url.Values{"Action": {"CreateLaunchConfiguration"}, "Version": {"2011-01-01"},
		"LaunchConfigurationName": {"probe"}, "ImageId": {"ami-0123456789abcdef0"}, "InstanceType": {"t3.micro"}})
	launchTemplate := field(query(url.Values{"Action": {"CreateLaunchTemplate"}, "Version": {"2016-11-15"},
		"LaunchTemplateName": {"probe"}, "LaunchTemplateData.ImageId": {"ami-0123456789abcdef0"}}),
		`<launchTemplateId>([^<]+)</launchTemplateId>`)
	fixtures["autoscaling"]["StartInstanceRefresh:launchtemplateid"] = launchTemplate
	fixtures["autoscaling"]["StartInstanceRefresh:version"] = "$Latest"

	// AWS Glue: a connection into the seeded subnet and a security group, and
	// a tagged zero-ETL integration.
	securityGroup := field(query(url.Values{"Action": {"CreateSecurityGroup"}, "Version": {"2016-11-15"},
		"GroupName": {"probe"}, "GroupDescription": {"probe"}, "VpcId": {vpc}}), `<groupId>([^<]+)</groupId>`)
	jsonCall("AWSGlue.CreateConnection", json11, `{"ConnectionInput":{"Name":"probe","ConnectionType":"NETWORK",`+
		`"ConnectionProperties":{},"PhysicalConnectionRequirements":{"SubnetId":"`+subnet+`",`+
		`"SecurityGroupIdList":["`+securityGroup+`"],"AvailabilityZone":"us-east-1a"}}}`)
	jsonCall("AWSGlue.CreateIntegration", json11, `{"IntegrationName":"probe","SourceArn":"`+cluster+`",`+
		`"TargetArn":"arn:aws:glue:us-east-1:`+iamProbeAccount+`:catalog","Tags":[{"key":"owner","value":"platform"}]}`)
	fixtures["glue"] = map[string]string{"integrationidentifier": "probe"}

	// AWS Identity and Access Management: a service-specific credential of the
	// seeded user.
	credential := field(query(url.Values{"Action": {"CreateServiceSpecificCredential"}, "Version": {"2010-05-08"},
		"UserName": {"probe"}, "ServiceName": {"codecommit.amazonaws.com"}}),
		`<ServiceSpecificCredentialId>([^<]+)</ServiceSpecificCredentialId>`)
	fixtures["iam"] = map[string]string{"servicespecificcredentialid": credential}

	// AWS Organizations: a service control policy in the account's
	// organization, and an outbound responsibility transfer.
	const organizations = "AWSOrganizationsV20161128."
	policy := field(jsonCall(organizations+"CreatePolicy", json11, `{"Name":"probe","Description":"probe",`+
		`"Type":"SERVICE_CONTROL_POLICY","Content":"{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":`+
		`\"Allow\",\"Action\":\"*\",\"Resource\":\"*\"}]}"}`), `"Id":"(p-[^"]+)"`)
	jsonCall(organizations+"InviteOrganizationToTransferResponsibility", json11, `{"Type":"BILLING",`+
		`"Target":{"Id":"210987654321","Type":"ACCOUNT"},"SourceName":"probe"}`)
	transfer := field(jsonCall(organizations+"ListOutboundResponsibilityTransfers", json11, `{"Type":"BILLING"}`),
		`"Id":"(rt-[^"]+)"`)
	fixtures["organizations"] = map[string]string{
		"policyid": policy, "ListTagsForResource:resourceid": policy, "UntagResource:resourceid": policy,
		"DescribeResponsibilityTransfer:id": transfer, "UpdateResponsibilityTransfer:id": transfer,
		"TerminateResponsibilityTransfer:id": transfer,
	}

	// Amazon S3 Batch Operations: a job over a manifest in the seeded bucket,
	// held for confirmation so it never runs.
	manifestETag := func() string {
		r := httptest.NewRequest(http.MethodPut, "https://sim.local/probe/manifest.csv", strings.NewReader("probe,probe\n"))
		r.Header.Set("Content-Type", "text/csv")
		signSeedControlPlane(r)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code >= 300 {
			t.Fatalf("seed: put the manifest: %d %s", rec.Code, rec.Body.String())
		}
		return strings.Trim(rec.Header().Get("ETag"), `"`)
	}()
	job := field(call(http.MethodPost, "/v20180820/jobs", "application/xml",
		`<CreateJobRequest `+s3Control+`><ConfirmationRequired>true</ConfirmationRequired>`+
			`<Operation><S3PutObjectTagging><TagSet><member><Key>owner</Key><Value>platform</Value></member>`+
			`</TagSet></S3PutObjectTagging></Operation><Report><Enabled>false</Enabled></Report>`+
			`<ClientRequestToken>probe</ClientRequestToken><Manifest><Spec><Format>S3BatchOperations_CSV_20180820</Format>`+
			`<Fields><member>Bucket</member><member>Key</member></Fields></Spec><Location>`+
			`<ObjectArn>arn:aws:s3:::probe/manifest.csv</ObjectArn><ETag>`+manifestETag+`</ETag></Location></Manifest>`+
			`<Priority>10</Priority><RoleArn>arn:aws:iam::`+iamProbeAccount+`:role/probe</RoleArn></CreateJobRequest>`,
		accountHeader), `<JobId>([^<]+)</JobId>`)
	fixtures["s3"]["jobid"] = job

	// Amazon CloudWatch: the default dataset, tagged.
	jsonCall("GraniteServiceVersion20100801.TagResource", json10, `{"ResourceARN":"arn:aws:cloudwatch:us-east-1:`+
		iamProbeAccount+`:dataset/default","Tags":[{"Key":"owner","Value":"platform"}]}`)
	fixtures["cloudwatch"]["datasetidentifier"] = "default"
	return fixtures
}

// iamSeedFederatedSessions opens an assumed-role session of the seeded role as
// each identity provider whose claims AWS STS carries into a chained
// AssumeRole, and returns the sessions' access keys. Each session's claims are
// derived from the provider's token or assertion by the code
// AssumeRoleWithWebIdentity and AssumeRoleWithSAML run.
func iamSeedFederatedSessions(t *testing.T) []string {
	t.Helper()
	sessions := map[string]map[string][]string{}
	for i, provider := range []struct {
		url    string
		claims map[string]any
	}{
		{"https://accounts.google.com", map[string]any{"aud": "probe", "sub": "probe"}},
		{"https://cognito-identity.amazonaws.com", map[string]any{"aud": "probe", "sub": "probe",
			"amr": []any{"authenticated"}}},
		{"https://graph.facebook.com", map[string]any{"app_id": "probe", "id": "probe"}},
		{"https://www.amazon.com", map[string]any{"app_id": "probe", "user_id": "probe"}},
	} {
		identity := webIdentity{Subject: "probe", Claims: provider.claims, Provider: IAMOIDCProvider{
			URL: provider.url, Arn: "arn:aws:iam::" + iamProbeAccount + ":oidc-provider/" + strings.TrimPrefix(provider.url, "https://")}}
		sessions[fmt.Sprintf("ASIAFEDERATEDPROBE%d", i)] = stsChainedClaims(identity.conditionContext("probe"))
	}
	assertion := samlAssertion{Issuer: "https://idp.probe.example/saml", Subject: "probe", SubjectType: "persistent",
		ProviderName: "probe", ProviderArn: "arn:aws:iam::" + iamProbeAccount + ":saml-provider/probe"}
	sessions["ASIAFEDERATEDPROBESAML"] = stsChainedClaims(assertion.conditionContext(iamProbeAccount))

	var keys []string
	for key, claims := range sessions {
		if len(claims) == 0 {
			t.Fatalf("seed: the session %s carries no chained claims", key)
		}
		iamTempCreds.Put(key, IAMTempCred{AccessKeyID: key, RoleName: "probe",
			PrincipalArn: "arn:aws:sts::" + iamProbeAccount + ":assumed-role/probe/probe",
			Expiration:   "2999-01-01T00:00:00Z", FederatedClaims: claims})
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// iamProbeFreePort is a TCP port nothing on the host listens on, for a seeded
// listener whose data plane binds one.
func iamProbeFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("seed: find a free port: %v", err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// iamProbeCABundle is a PEM bundle of one self-signed CA certificate.
func iamProbeCABundle(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "probe CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
