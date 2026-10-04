package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

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
	"federated-session-claims": "The claims of the web-identity or SAML session an sts:AssumeRole caller chains " +
		"from. The simulator's temporary credentials do not retain the identity provider's claims, so a chained " +
		"AssumeRole carries none.",
	"resource-unseeded": "The key reports the state of the resource the request names (its tags, its " +
		"configuration). The probe names one the simulator does not hold — the measure seeds none of this type, or " +
		"names it in a form the request cannot resolve — so the gate rightly leaves the key out. Seeding the " +
		"resource in iamSeedConditionKeyFixtures is what measures the key here.",
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
	"access-grants-unseeded": "The Access Grants instance, location or grant the request names. The measure " +
		"seeds none, and the tags an Access Grants create carries live in the grant's own record, which the gate " +
		"does not read (BUGS.md, Access Grants tags).",
	"object-lambda-multi-region-access-point": "AWS documents the access-point keys for a data request made " +
		"through an access point, and documents no value for a control-plane request about an Object Lambda or " +
		"Multi-Region access point.",
	"undocumented-value": "AWS declares the key on the action but documents no value for it there: a listing " +
		"spans many configurations, a permission removal names only a statement id, and the transfer direction " +
		"of an invitation is not spelled anywhere in the vendored references.",
	"source-resource-not-read": "The value is a property of the source the request copies — the DB instance a " +
		"snapshot is taken of, the snapshot or cluster a restore reads, the database a blue/green deployment " +
		"clones — which the gate does not look up.",
	"daemon-task-definition-size": "A daemon's CPU and memory are its daemon task definition's, which the gate " +
		"does not look up; it reads the size of an ordinary task definition only.",
	"invoke-path-unserved": "Set on an invoke that arrives through a function URL (lambda:InvokedViaFunctionUrl) " +
		"or an Alexa event source (lambda:EventSourceToken). The gate authorizes the Invoke API, which carries " +
		"neither.",
	"job-operation-shape": "A batch job's Operation structure takes exactly one member. The probe fills every " +
		"member, which is an operation no client sends, and the gate rightly names none; a one-member request is " +
		"covered by TestS3ControlConditionKeysReadTheRequestedJob.",
	"delete-objects-versions": "A DeleteObjects body names the version of each entry. The gate reads " +
		"s3:versionid from the query string only, so a version deleted inside a batch carries none.",
	"get-access-point-resource": "GetAccessPoint's route authorizes against \"*\", so the gate has no access " +
		"point ARN to read tags for.",
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
	label string
	build func() *http.Request
	// actions classifies a built request into the actions the gate authorizes
	// it as.
	actions func(r *http.Request) []string
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
	// The awsJson and awsQuery classifier is the gate's own: the action
	// iamEnforce reads off the request, and every target iamAuthorizationTargets
	// adds for it.
	gateActions := func(r *http.Request) []string {
		action, ok := iamActionForRequest(r)
		if !ok {
			return nil
		}
		var out []string
		for _, target := range iamAuthorizationTargets(r, action) {
			out = append(out, target.action)
		}
		return out
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
		f := fill(m, operation)
		probes = append(probes, iamKeyProbe{
			label:   "awsJson " + target,
			build:   func() *http.Request { return f.jsonRequest(target, contentType) },
			actions: gateActions,
		})
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
				f := fill(m, operation)
				ec2 := idx.protocols["ec2Query"]
				probes = append(probes, iamKeyProbe{
					label:   "query " + m.service + " " + operation,
					build:   func() *http.Request { return f.queryRequest(ec2) },
					actions: gateActions,
				})
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
			label: "rpcv2Cbor " + m.service + " " + operation,
			build: f.cborRequest,
			// Amazon CloudWatch is the one service served over RPC v2 CBOR;
			// see cloudWatchCBORAuthorized.
			actions: func(r *http.Request) []string {
				var out []string
				for _, target := range cloudWatchCBORTargets(r, operation) {
					out = append(out, target.action)
				}
				return out
			},
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
			var actions func(*http.Request) []string
			switch {
			case m.service == "s3" && idx.serviceShort == "AmazonS3":
				// The operation name the data plane's own routes give: the
				// bucket routes name theirs with s3BucketOperationName, the
				// object routes with s3ObjectOperationName; see registerS3.
				actions = func(r *http.Request) []string {
					operation := s3ObjectOperationName(r, nil)
					switch {
					case r.Pattern == "GET /{$}":
						operation = "ListBuckets"
					case !strings.Contains(r.Pattern, "{key...}"):
						operation = s3BucketOperationName(r, nil)
					}
					var out []string
					for _, target := range s3AuthorizationTargets(r, operation, []string{s3RequestResourceARN(r)}) {
						out = append(out, target.action)
					}
					return out
				}
			case s3ControlOperations[pattern].operation != "":
				route := s3ControlOperations[pattern]
				actions = func(r *http.Request) []string {
					var out []string
					for _, target := range s3ControlAuthorizationTargets(r, route.operation, route.resource) {
						out = append(out, target.action)
					}
					return out
				}
			case m.service == "lambda":
				actions = func(r *http.Request) []string {
					var out []string
					for _, target := range lambdaAuthorizationTargets(r, operation, "*") {
						out = append(out, target.action)
					}
					return out
				}
			default:
				continue
			}
			probes = append(probes, iamKeyProbe{
				label:   "rest " + m.service + " " + operation,
				build:   build,
				actions: actions,
			})
		}
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
// probe's request is authorized as, and for the iam:PassRole check the gate
// adds when the request passes a role.
func iamKeyProbeContexts(t *testing.T, probe iamKeyProbe) map[string][]map[string][]string {
	t.Helper()
	out := map[string][]map[string][]string{}
	for _, action := range probe.actions(probe.build()) {
		// The gate authorizes no permissionless call, so it builds no context
		// for one; see iamPermissionlessAction.
		if iamPermissionlessAction(action) {
			continue
		}
		r := probe.build()
		ctx := iamProbeContextFor(t, r, iamProbeAccessKeyID, action)
		out[action] = append(out[action], ctx)
		principals, passes := iamPassRoleOperations[action]
		if !passes || len(iamPassedRoleARNs(probe.build())) == 0 {
			continue
		}
		pass := iamProbeContextFor(t, probe.build(), iamProbeAccessKeyID, "iam:PassRole")
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

	measured := map[string]bool{}
	satisfied := map[iamConditionKeyPair]bool{}
	probes := iamServedKeyProbes(t, srv, jsonRouter, queryRouter, referenced, fixtures)
	for _, probe := range probes {
		for action, contexts := range iamKeyProbeContexts(t, probe) {
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
	return fixtures
}
