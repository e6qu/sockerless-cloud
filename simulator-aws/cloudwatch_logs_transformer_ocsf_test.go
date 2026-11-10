package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/csv"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cwoSamples reads the raw and OCSF samples of each dossier in the vendored
// ocsf/examples snapshot.
func cwoSamples(t *testing.T, version string) map[string][2]string {
	t.Helper()
	f, err := os.Open("../specs/cloud-api/aws/ocsf-examples-aws-v" + version + ".tar.gz")
	require.NoError(t, err)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	require.NoError(t, err)
	out := map[string][2]string{}
	tr := tar.NewReader(zr)
	prefix := "mappings/markdown/AWS/v" + version + "/"
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		name := strings.TrimPrefix(h.Name, prefix)
		i := strings.Index(strings.ToLower(name), "/samples/")
		if i < 0 {
			continue
		}
		dir, pair := name[:i], out[name[:i]]
		switch {
		case strings.HasSuffix(name, ".raw"):
			pair[0] = string(b)
		case strings.HasSuffix(name, ".ocsf"):
			pair[1] = string(b)
		}
		out[dir] = pair
	}
}

var cwoTrailingCommaRE = regexp.MustCompile(`,(\s*[}\]])`)

// cwoLeaves flattens a JSON document to its leaf values by path, arrays of
// scalars as one sorted, comma-joined leaf.
func cwoLeaves(t *testing.T, doc string) map[string]string {
	t.Helper()
	v, err := cwtDecode(cwoTrailingCommaRE.ReplaceAllString(doc, "$1"))
	require.NoError(t, err, "%s", doc)
	out := map[string]string{}
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch x := v.(type) {
		case *cwtObj:
			for _, k := range x.keys {
				walk(x.m[k], cwoJoin(path, k))
			}
		case []any:
			var scalars []string
			for _, e := range x {
				if s, ok := cwtScalarString(e); ok {
					scalars = append(scalars, s)
				} else {
					walk(e, path+"[]")
				}
			}
			if len(scalars) > 0 {
				sort.Strings(scalars)
				out[path] = strings.Join(scalars, ",")
			}
		case nil:
			out[path] = "null"
		default:
			s, _ := cwtScalarString(x)
			out[path] = s
		}
	}
	walk(v, "")
	return out
}

// cwoObservableKeys lists a document's observables as name=value|type_id.
func cwoObservableKeys(t *testing.T, doc string) []string {
	t.Helper()
	v, err := cwtDecode(cwoTrailingCommaRE.ReplaceAllString(doc, "$1"))
	require.NoError(t, err)
	obs, _ := v.(*cwtObj).Get("observables")
	list, _ := obs.([]any)
	var out []string
	for _, o := range list {
		obj := o.(*cwtObj)
		name, _ := obj.Get("name")
		value, _ := obj.Get("value")
		id, _ := obj.Get("type_id")
		ids, _ := cwtScalarString(id)
		out = append(out, name.(string)+"="+value.(string)+"|"+ids)
	}
	return out
}

func cwoRecordFromCSV(t *testing.T, raw string) *cwtObj {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	require.NoError(t, err)
	require.Len(t, rows, 2)
	rec := newCWTObj()
	for i, k := range rows[0] {
		rec.Put(k, rows[1][i])
	}
	return rec
}

// Every value a dossier's OCSF sample shows, the conversion of its raw sample
// reproduces, except where the sample contradicts its own dossier or the
// schema it names; each exception carries why.
func TestParseToOCSFMatchesDossierSamples(t *testing.T) {
	sources := map[string]string{
		"CloudTrail/API Activity": "CloudTrail", "CloudTrail/Account Change": "CloudTrail",
		"CloudTrail/Authentication": "CloudTrail", "Route53": "Route53Resolver", "VPC Flowlogs": "VPCFlow",
		"EKS Audit Logs": "EKSAudit", "WAF": "AWSWAF",
	}
	for _, version := range []string{"1.1.0", "1.5.0"} {
		for dir, pair := range cwoSamples(t, version) {
			t.Run(version+"/"+dir, func(t *testing.T) {
				m := &cwoMapper{schema: cwoSchemas[version], version: version, source: sources[dir]}
				var rec *cwtObj
				if m.source == "VPCFlow" {
					rec = cwoRecordFromCSV(t, pair[0])
				} else {
					v, err := cwtDecode(pair[0])
					require.NoError(t, err)
					rec = v.(*cwtObj)
				}
				out, err := m.convert(rec)
				require.NoError(t, err)
				got, want := cwoLeaves(t, cwtEncode(out)), cwoLeaves(t, pair[1])
				skip := cwoSampleExceptions[version+"/"+dir]
				ignored := func(path string) bool {
					_, ok := skip[path]
					return ok || strings.HasPrefix(path, "observables[]") || strings.HasSuffix(path, "_dt")
				}
				for path, w := range want {
					if ignored(path) {
						continue
					}
					assert.Equal(t, w, got[path], "%s", path)
				}
				gotObs := map[string]bool{}
				for _, o := range cwoObservableKeys(t, cwtEncode(out)) {
					gotObs[o] = true
				}
				for _, o := range cwoObservableKeys(t, pair[1]) {
					name, rest, _ := strings.Cut(o, "=")
					value, _, _ := strings.Cut(rest, "|")
					if _, ok := skip["observables"]; ok || !strings.Contains(name, "[]") && got[name] != value {
						continue
					}
					assert.True(t, gotObs[o], "observable %s=%s", name, value)
				}
				for path := range got {
					if ignored(path) {
						continue
					}
					_, inSample := want[path]
					assert.True(t, inSample, "%s=%q is not in the sample", path, got[path])
				}
			})
		}
	}
}

// The sample values that contradict their dossier, the schema, or another
// sample of the same dossier, and why the conversion differs. Every
// datetime attribute (*_dt) differs too: the samples print them in three
// formats and in the zone of the machine that generated them.
var cwoSampleExceptions = map[string]map[string]string{
	"1.1.0/VPC Flowlogs": {
		"dst_endpoint.interface_uid": "the raw sample's interface_id has two fewer digits than the OCSF sample's",
	},
	"1.1.0/WAF": {
		"observables":                   "the sample names raw fields and types firewall_rule.uid and src_endpoint.uid as IP addresses",
		"http_request.referrer":         "the sample writes null for a header the request does not carry",
		"metadata.labels":               "the sample writes null for labels the record does not carry",
		"metadata.product.name":         "the dossier names the product Amazon WAF",
		"metadata.product.feature.name": "the dossier sets the feature name to WAF",
		"http_status":                   "the 1.1.0 dossier maps responseCodeSent to status_code",
		"status_code":                   "the 1.1.0 dossier maps responseCodeSent to status_code",
	},
	"1.5.0/WAF": {
		"http_request.referrer":         "the sample writes null for a header the request does not carry",
		"metadata.labels":               "the sample writes null for labels the record does not carry",
		"http_status":                   "the sample writes null for responseCodeSent's null",
		"metadata.product.name":         "the dossier names the product Amazon WAF",
		"metadata.product.feature.name": "the dossier sets the feature name to WAF",
		"action":                        "the raw sample's action is ALLOW, which the sample maps to Denied",
		"action_id":                     "the raw sample's action is ALLOW, which the sample maps to Denied",
		"disposition":                   "the raw sample's action is ALLOW, which the sample maps to Blocked",
		"disposition_id":                "the raw sample's action is ALLOW, which the sample maps to Blocked",
	},
	"1.1.0/CloudTrail/Authentication": {
		"api.request.uid": "the raw sample carries no requestID, and the OCSF sample shows a quoted empty string",
	},
	"1.1.0/EKS Audit Logs": {
		"start_time": "the 1.1.0 dossier maps requestReceivedTimestamp to start_time; the sample shows only its datetime",
	},
	"1.5.0/EKS Audit Logs": {
		"metadata.profiles":         "the dossier's profiles are cloud and datetime",
		"action":                    "the EKS dossier maps no action; the sample carries the security_control profile's",
		"action_id":                 "the EKS dossier maps no action; the sample carries the security_control profile's",
		"unmapped.eks_cluster_name": "no raw field carries a cluster name",
		"policy.name":               "the dossier maps annotations.authorization.k8s.io/reason, empty in the raw sample, which the sample omits",
		"unmapped.annotations.authorization.k8s.io/decision": "the sample drops the unmapped annotation",
	},
}

// Every attribute a dossier maps exists in the schema of its version (a
// *_dt attribute as the datetime profile's companion of a timestamp), and
// every dossier's class constants agree with the schema's class.
func TestParseToOCSFDossiersMatchTheSchema(t *testing.T) {
	for v, schema := range cwoSchemas {
		assert.Equal(t, v, schema.version)
	}
	for _, d := range cwoDossiers {
		class := cwoClassOfDossier[d.dir]
		cls, ok := cwoSchemas[d.version].classes[class]
		require.True(t, ok, "%s %s: class %s", d.version, d.dir, class)
		consts := map[string]string{}
		for _, c := range d.constants {
			consts[c[0]] = c[1]
		}
		assert.Equal(t, strconv.Itoa(cls.uid), consts["class_uid"], "%s %s", d.version, d.dir)
		assert.Equal(t, cls.caption, consts["class_name"], "%s %s", d.version, d.dir)
		assert.Equal(t, strconv.Itoa(cls.categoryUID), consts["category_uid"], "%s %s", d.version, d.dir)
		assert.Equal(t, cls.categoryCaption, consts["category_name"], "%s %s", d.version, d.dir)
		r := &cwoRun{m: &cwoMapper{schema: cwoSchemas[d.version], version: d.version}, class: class}
		for _, rows := range [][][2]string{d.direct, d.conditional} {
			for _, row := range rows {
				for _, path := range strings.Split(row[0], "/") {
					if base, ok := strings.CutSuffix(path, "_dt"); ok {
						_, _, typ, _ := r.attrAt(base)
						assert.Equal(t, "timestamp_t", typ, "%s %s: the datetime profile adds %s beside a timestamp", d.version, d.dir, path)
						continue
					}
					_, _, typ, _ := r.attrAt(path)
					assert.NotEmpty(t, typ, "%s %s: %s is not an attribute of %s", d.version, d.dir, path, class)
				}
			}
		}
	}
}
