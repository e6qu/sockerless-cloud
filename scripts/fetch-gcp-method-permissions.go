//go:build ignore

// fetch-gcp-method-permissions compiles specs/cloud-api/gcp/method-permissions.json.gz:
// the IAM permissions each listed Discovery method's REST reference page states.
//
// Google states most methods' permissions only on the method's own reference
// page, as "Authorization requires the following IAM permission on the
// specified resource parent : run.authorizeddomains.list" beside a path
// parameter, or under an "IAM Permissions" heading. The role pages and audit
// pages leave them out, so the gate would otherwise admit only an Owner. One
// page per method is too many to vendor one by one, so this compiles the
// sentences into one table that records each page's URL and "Last updated"
// date; scripts/check-spec-freshness.sh samples those pages.
//
// The pages a service's reference index links to name the real URL of each
// method's page, which differs between services (Bigtable's admin API lives
// under reference/admin/rest, Cloud SQL's under mysql/admin-api/rest).
//
// A method keeps its row while its page states a permission. A sentence
// offering a choice of several permissions ("one or more of", "one of") is
// left out: the page does not say which the method checks.
//
// Usage, from the repository root:
//
//	go run scripts/fetch-gcp-method-permissions.go [method-id ...]
//
// With no arguments it refreshes the methods the table already holds; named
// methods are added to it.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	docsHost  = "https://docs.cloud.google.com"
	tablePath = "specs/cloud-api/gcp/method-permissions.json.gz"
)

// referenceIndex is the reference index page of each API whose methods the
// table covers; its navigation links to every method page.
var referenceIndex = map[string]string{
	"artifactregistry":     "artifact-registry/docs/reference/rest",
	"bigquery":             "bigquery/docs/reference/rest",
	"bigtableadmin":        "bigtable/docs/reference/admin/rest",
	"cloudbilling":         "billing/docs/reference/rest",
	"cloudbuild":           "build/docs/api/reference/rest",
	"cloudfunctions":       "functions/docs/reference/rest",
	"cloudkms":             "kms/docs/reference/rest",
	"cloudresourcemanager": "resource-manager/reference/rest",
	"cloudscheduler":       "scheduler/docs/reference/rest",
	"cloudtasks":           "tasks/docs/reference/rest",
	"compute":              "compute/docs/reference/rest/v1",
	"dataflow":             "dataflow/docs/reference/rest",
	"dns":                  "dns/docs/reference/rest",
	"eventarc":             "eventarc/docs/reference/rest",
	"firestore":            "firestore/docs/reference/rest",
	"iam":                  "iam/docs/reference/rest",
	"iamcredentials":       "iam/docs/reference/credentials/rest",
	"logging":              "logging/docs/reference/v2/rest",
	"redis":                "memorystore/docs/redis/reference/rest",
	"run":                  "run/docs/reference/rest",
	"serviceusage":         "service-usage/docs/reference/rest",
	"spanner":              "spanner/docs/reference/rest",
	"sql":                  "sql/docs/mysql/admin-api/rest",
	"storage":              "storage/docs/json_api/v1",
}

type requirement struct {
	Permission string `json:"permission"`
	On         string `json:"on,omitempty"`
	Optional   bool   `json:"optional,omitempty"`
}

type row struct {
	Page         string        `json:"page"`
	Updated      string        `json:"updated"`
	Requirements []requirement `json:"requirements"`
}

type table struct {
	Note    string          `json:"note"`
	Fetched string          `json:"fetched"`
	Methods map[string]*row `json:"methods"`
}

type method struct {
	api, version string
	pathParams   map[string]bool
}

var (
	tagRe     = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	spaceRe   = regexp.MustCompile(`\s+`)
	updatedRe = regexp.MustCompile(`Last updated (\d{4}-\d{2}-\d{2}) UTC`)
	perm      = `[a-z][a-zA-Z0-9]*\.[a-zA-Z0-9]+(?:\.[a-zA-Z0-9]+)*`
	// "Authorization requires the following IAM permission on the specified resource parent : run.authorizeddomains.list"
	onParamRe = regexp.MustCompile(`(?i)authorization requires the following IAM permissions? on the specified resource (\w+)\s*:\s*((?:` + perm + `\s*)+)`)
	// "IAM Permissions Requires the following IAM permission on the name resource: cloudkms.autokeyConfigs.get"
	headingRe = regexp.MustCompile(`IAM Permissions Requires the following IAM permissions? on the (\w+) resource\s*:\s*((?:` + perm + `\s*)+)`)
	// "IAM Permissions In addition to any permissions specified on the fields above, authorization requires one or more of the following IAM permissions: compute.backendBuckets.setSecurityPolicy"
	methodRe = regexp.MustCompile(`(?i)authorization requires one or more of the following IAM permissions\s*:\s*((?:` + perm + `\s*)+)`)
	permRe   = regexp.MustCompile(perm)
)

func main() {
	tbl := table{Methods: map[string]*row{}}
	if raw, err := readGzip(tablePath); err == nil {
		check(json.Unmarshal(raw, &tbl))
	} else if !os.IsNotExist(err) {
		check(err)
	}
	ids := map[string]bool{}
	for id := range tbl.Methods {
		ids[id] = true
	}
	for _, id := range os.Args[1:] {
		ids[id] = true
	}
	methods := discoveryMethods()
	links := map[string][]string{}
	for id := range ids {
		api := strings.SplitN(id, ".", 2)[0]
		if _, ok := links[api]; ok {
			continue
		}
		index, ok := referenceIndex[api]
		if !ok {
			fmt.Fprintf(os.Stderr, "skip %s: no reference index for %s\n", id, api)
			continue
		}
		links[api] = indexLinks(index)
	}

	type result struct {
		id  string
		row *row
		err error
	}
	work := make(chan string)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				r, err := methodRow(id, methods[id], links[strings.SplitN(id, ".", 2)[0]])
				results <- result{id, r, err}
			}
		}()
	}
	go func() {
		for id := range ids {
			work <- id
		}
		close(work)
		wg.Wait()
		close(results)
	}()
	tbl.Methods = map[string]*row{}
	var stated, unstated int
	for r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", r.id, r.err)
			unstated++
			continue
		}
		if r.row == nil {
			unstated++
			continue
		}
		tbl.Methods[r.id] = r.row
		stated++
	}
	tbl.Fetched = time.Now().UTC().Format(time.RFC3339)
	tbl.Note = "Compiled by scripts/fetch-gcp-method-permissions.go from each method's REST reference page on " +
		docsHost + ": the IAM permissions the page states the method requires, keyed by Discovery method ID. " +
		"on is \"project\" for a permission checked on the project, or \"field:<name>\" for one checked on the " +
		"resource a request field names; such a field requirement is optional."
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	check(err)
	enc := json.NewEncoder(zw)
	enc.SetIndent("", " ")
	check(enc.Encode(tbl))
	check(zw.Close())
	check(os.WriteFile(tablePath, buf.Bytes(), 0o644))
	row := exec.Command("bash", "scripts/spec-sources-row.sh", "specs/cloud-api/gcp/SOURCES.md", "gcp",
		filepath.Base(tablePath), "docs.cloud.google.com", "(each method's REST reference page)", "CC-BY-4.0",
		"sampled"+tbl.Fetched[:10])
	row.Stdout, row.Stderr = os.Stdout, os.Stderr
	check(row.Run())
	fmt.Printf("%d methods state their permissions, %d do not -> %s\n", stated, unstated, tablePath)
}

// methodRow fetches a method's reference page and reads its permission
// sentences; nil means the page states none this table can use.
func methodRow(id string, m method, links []string) (*row, error) {
	if m.api == "" {
		return nil, fmt.Errorf("no Discovery method")
	}
	page := methodPage(id, m, links)
	if page == "" {
		return nil, fmt.Errorf("no reference page in the %s index", m.api)
	}
	text, err := fetchText(docsHost + page)
	if err != nil {
		return nil, err
	}
	var reqs []requirement
	seen := map[string]bool{}
	add := func(p, on string, optional bool) {
		key := p + "|" + on
		if seen[key] {
			return
		}
		seen[key] = true
		reqs = append(reqs, requirement{Permission: p, On: on, Optional: optional})
	}
	for _, re := range []*regexp.Regexp{onParamRe, headingRe} {
		for _, sm := range re.FindAllStringSubmatch(text, -1) {
			on, optional := target(sm[1], m)
			for _, p := range permRe.FindAllString(sm[2], -1) {
				add(p, on, optional)
			}
		}
	}
	// Compute Engine's "In addition to any permissions specified on the
	// fields above" list names what a request may need beyond its fields'
	// permissions, the disks a per-instance config names among them. It
	// states the method's own permission only on a page whose fields state
	// none.
	if len(reqs) == 0 {
		for _, sm := range methodRe.FindAllStringSubmatch(text, -1) {
			if ps := permRe.FindAllString(sm[1], -1); len(ps) == 1 {
				add(ps[0], "", false)
			}
		}
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	// iam.permissions.none is how a reference page says the method checks
	// no permission; the row then admits every authenticated caller.
	var checked []requirement
	for _, r := range reqs {
		if r.Permission != "iam.permissions.none" {
			checked = append(checked, r)
		}
	}
	reqs = checked
	if reqs == nil {
		reqs = []requirement{}
	}
	updated := ""
	if um := updatedRe.FindStringSubmatch(text); um != nil {
		updated = um[1]
	}
	return &row{Page: page, Updated: updated, Requirements: reqs}, nil
}

// target maps the resource a sentence names onto what the gate checks: the
// resource the method addresses for one of its path parameters, the project
// for its project parameter, and otherwise the resource a request field names,
// when the request names one.
func target(param string, m method) (string, bool) {
	switch {
	case param == "project" && m.api == "compute":
		return "project", false
	case m.pathParams[param]:
		return "", false
	}
	return "field:" + param, true
}

// methodPage picks the method's page among the index's links: the one ending
// in its resource path and method name, in the method's API version when the
// index spans several.
func methodPage(id string, m method, links []string) string {
	parts := strings.Split(id, ".")
	suffix := strings.ToLower("/" + strings.Join(parts[1:len(parts)-1], ".") + "/" + parts[len(parts)-1])
	var matches []string
	for _, l := range links {
		if strings.HasSuffix(strings.ToLower(l), suffix) {
			matches = append(matches, l)
		}
	}
	for _, l := range matches {
		if strings.Contains(l, "/"+m.version+"/") {
			return l
		}
	}
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func indexLinks(index string) []string {
	raw, err := fetch(docsHost + "/" + index)
	check(err)
	re := regexp.MustCompile(`href="(/` + regexp.QuoteMeta(index) + `/[^"#?]+)"`)
	set := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(raw, -1) {
		set[m[1]] = true
	}
	var out []string
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

func fetchText(url string) (string, error) {
	raw, err := fetch(url)
	if err != nil {
		return "", err
	}
	return spaceRe.ReplaceAllString(html.UnescapeString(tagRe.ReplaceAllString(raw, " ")), " "), nil
}

func fetch(url string) (string, error) {
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*3) * time.Second)
		}
		resp, err := http.Get(url)
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("%s: 404", url)
		}
		if err != nil || resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("%s: %d %v", url, resp.StatusCode, err)
			continue
		}
		return string(body), nil
	}
	return "", last
}

// discoveryMethods reads every vendored Discovery document's methods: their
// API, version and path parameters.
func discoveryMethods() map[string]method {
	files, err := filepath.Glob("simulator-gcp/discovery/*.discovery.json.gz")
	check(err)
	type param struct {
		Location string `json:"location"`
	}
	type rmethod struct {
		ID         string           `json:"id"`
		Parameters map[string]param `json:"parameters"`
	}
	type resource struct {
		Methods   map[string]rmethod  `json:"methods"`
		Resources map[string]resource `json:"resources"`
	}
	out := map[string]method{}
	for _, f := range files {
		raw, err := readGzip(f)
		check(err)
		var doc struct {
			Name      string              `json:"name"`
			Version   string              `json:"version"`
			Methods   map[string]rmethod  `json:"methods"`
			Resources map[string]resource `json:"resources"`
		}
		check(json.Unmarshal(raw, &doc))
		add := func(ms map[string]rmethod) {
			for _, rm := range ms {
				params := map[string]bool{}
				for name, p := range rm.Parameters {
					if p.Location == "path" {
						params[name] = true
					}
				}
				prev, ok := out[rm.ID]
				if ok && prev.version > doc.Version {
					continue
				}
				out[rm.ID] = method{api: strings.SplitN(rm.ID, ".", 2)[0], version: doc.Version, pathParams: params}
			}
		}
		add(doc.Methods)
		var walk func(map[string]resource)
		walk = func(rs map[string]resource) {
			for _, r := range rs {
				add(r.Methods)
				walk(r.Resources)
			}
		}
		walk(doc.Resources)
	}
	return out
}

func readGzip(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
