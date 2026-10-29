package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
)

// The Discovery methods the IAM gate resolves a call to: every method of
// every embedded Discovery document, matched against a request's path the way
// the API's front end routes it.

type gateDiscoveryAPI struct {
	name string
	host string
	// mount is the path the simulator serves the API under when it is not
	// the API's own: Cloud Spanner's /spanner.
	mount string
}

type gateDiscoverySeg struct {
	literal string
	param   string
	greedy  bool
	prefix  string
	suffix  string
}

type gateDiscoveryTemplate struct {
	segs  []gateDiscoverySeg
	score int
	// relStart is the first segment of the resource's relative name: the
	// segment after the API version.
	relStart int
}

type gateDiscoveryMethod struct {
	api        *gateDiscoveryAPI
	id         string
	httpMethod string
	templates  []gateDiscoveryTemplate
}

// gateDiscoveryMatch is a call resolved to a Discovery method: the path
// parameters it binds, and the relative name of the resource it addresses,
// the path after the API version without a custom method's verb.
type gateDiscoveryMatch struct {
	method   *gateDiscoveryMethod
	params   map[string]string
	relative string
}

var gateDiscoveryMounts = map[string]string{"spanner": "/spanner"}

var gateDiscoveryMethods = sync.OnceValues(func() ([]*gateDiscoveryMethod, error) {
	files, err := fs.Glob(discoveryDocuments, "discovery/*.discovery.json.gz")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	type rawMethod struct {
		ID                      string `json:"id"`
		HTTPMethod              string `json:"httpMethod"`
		Path                    string `json:"path"`
		FlatPath                string `json:"flatPath"`
		UseMediaDownloadService bool   `json:"useMediaDownloadService"`
		MediaUpload             *struct {
			Protocols map[string]struct {
				Path string `json:"path"`
			} `json:"protocols"`
		} `json:"mediaUpload"`
	}
	type rawResource struct {
		Methods   map[string]rawMethod   `json:"methods"`
		Resources map[string]rawResource `json:"resources"`
	}
	var methods []*gateDiscoveryMethod
	for _, file := range files {
		body, err := readDiscoveryDocument(file)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Name      string                 `json:"name"`
			RootURL   string                 `json:"rootUrl"`
			BasePath  string                 `json:"basePath"`
			Methods   map[string]rawMethod   `json:"methods"`
			Resources map[string]rawResource `json:"resources"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if doc.Name == "discovery" {
			continue
		}
		root, err := url.Parse(doc.RootURL)
		if err != nil {
			return nil, fmt.Errorf("%s: rootUrl: %w", file, err)
		}
		api := &gateDiscoveryAPI{name: doc.Name, host: root.Host, mount: gateDiscoveryMounts[doc.Name]}
		add := func(m rawMethod) {
			if m.ID == "" || m.HTTPMethod == "" {
				return
			}
			dm := &gateDiscoveryMethod{api: api, id: m.ID, httpMethod: m.HTTPMethod}
			seen := map[string]bool{}
			addTemplate := func(path string) {
				if path == "" || seen[path] {
					return
				}
				seen[path] = true
				dm.templates = append(dm.templates, gateCompileTemplate(path, doc.Name))
			}
			base := api.mount + "/" + strings.Trim(doc.BasePath, "/")
			rels := []string{m.FlatPath, m.Path}
			// A path whose parameter carries no literal of its own matches any
			// request; flatPath spells the same method without it.
			if m.FlatPath != "" && m.Path != m.FlatPath && hasUnconstrainedParam(splitSpecSegs(m.Path)) {
				rels = []string{m.FlatPath}
			}
			for _, rel := range rels {
				if rel == "" {
					continue
				}
				addTemplate(strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(rel, "/"))
				if m.UseMediaDownloadService {
					addTemplate("/download" + strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(rel, "/"))
				}
			}
			if m.MediaUpload != nil {
				for _, p := range m.MediaUpload.Protocols {
					addTemplate(p.Path)
				}
			}
			methods = append(methods, dm)
		}
		var walk func(map[string]rawResource)
		walk = func(rs map[string]rawResource) {
			for _, r := range rs {
				for _, m := range r.Methods {
					add(m)
				}
				walk(r.Resources)
			}
		}
		for _, m := range doc.Methods {
			add(m)
		}
		walk(doc.Resources)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].id < methods[j].id })
	return methods, nil
})

// gateGreedyParams are the path parameters that may span segments: a Cloud
// Storage object name sent with its slashes unescaped.
var gateGreedyParams = map[string]bool{"object": true, "sourceObject": true, "destinationObject": true}

func gateCompileTemplate(path, api string) gateDiscoveryTemplate {
	t := gateDiscoveryTemplate{relStart: -1}
	for i, part := range strings.Split(strings.Trim(path, "/"), "/") {
		open, closing := strings.Index(part, "{"), strings.Index(part, "}")
		if open < 0 || closing < open {
			t.segs = append(t.segs, gateDiscoverySeg{literal: part})
			t.score += len(part)
			if t.relStart < 0 && auditVersionSegment(part) {
				t.relStart = i + 1
			}
			continue
		}
		name := part[open+1 : closing]
		seg := gateDiscoverySeg{prefix: part[:open], suffix: part[closing+1:]}
		seg.greedy = strings.HasPrefix(name, "+") || (api == "storage" && gateGreedyParams[name])
		seg.param = strings.TrimPrefix(name, "+")
		t.score += len(seg.prefix) + len(seg.suffix)
		t.segs = append(t.segs, seg)
	}
	if t.relStart < 0 {
		t.relStart = 0
	}
	return t
}

// match binds the template to the request's escaped path segments.
func (t gateDiscoveryTemplate) match(req []string) (map[string]string, bool) {
	values := map[string]string{}
	var walk func(r []string, s []gateDiscoverySeg) bool
	walk = func(r []string, s []gateDiscoverySeg) bool {
		if len(s) == 0 {
			return len(r) == 0
		}
		if len(r) == 0 {
			return false
		}
		seg := s[0]
		if seg.param == "" {
			return r[0] == seg.literal && walk(r[1:], s[1:])
		}
		if !seg.greedy {
			if !paramSegMatch(r[0], seg.prefix, seg.suffix) {
				return false
			}
			values[seg.param] = r[0][len(seg.prefix) : len(r[0])-len(seg.suffix)]
			return walk(r[1:], s[1:])
		}
		if !strings.HasPrefix(r[0], seg.prefix) {
			return false
		}
		for i := 1; i <= len(r); i++ {
			last := r[i-1]
			if seg.suffix != "" && !strings.HasSuffix(last, seg.suffix) {
				continue
			}
			value := strings.TrimSuffix(strings.TrimPrefix(strings.Join(r[:i], "/"), seg.prefix), seg.suffix)
			if value == "" || strings.Contains(strings.TrimSuffix(last, seg.suffix), ":") {
				continue
			}
			if walk(r[i:], s[1:]) {
				values[seg.param] = value
				return true
			}
		}
		return false
	}
	if !walk(req, t.segs) {
		return nil, false
	}
	for k, v := range values {
		if decoded, err := url.PathUnescape(v); err == nil {
			values[k] = decoded
		}
	}
	return values, true
}

// gateMatchDiscovery resolves a call to the Discovery methods that describe
// it most specifically: one per API, since two APIs can publish one path,
// which their hosts tell apart on Google Cloud.
func gateMatchDiscovery(httpMethod, escapedPath string) []gateDiscoveryMatch {
	methods, err := gateDiscoveryMethods()
	if err != nil {
		return nil
	}
	req := strings.Split(strings.Trim(escapedPath, "/"), "/")
	var best []gateDiscoveryMatch
	bestScore := -1
	for _, m := range methods {
		if m.httpMethod != httpMethod {
			continue
		}
		if m.api.mount != "" && !strings.HasPrefix(escapedPath, m.api.mount+"/") {
			continue
		}
		for _, t := range m.templates {
			if t.score < bestScore {
				continue
			}
			params, ok := t.match(req)
			if !ok {
				continue
			}
			match := gateDiscoveryMatch{method: m, params: params, relative: gateRelativeName(req, t)}
			switch {
			case t.score > bestScore:
				best, bestScore = []gateDiscoveryMatch{match}, t.score
			case !slices.ContainsFunc(best, func(b gateDiscoveryMatch) bool { return b.method.api.name == m.api.name }):
				best = append(best, match)
			}
		}
	}
	return best
}

// gateRelativeName is the relative name of the resource a path addresses:
// the segments after the API version, the last without a custom method's
// verb.
func gateRelativeName(req []string, t gateDiscoveryTemplate) string {
	if t.relStart >= len(req) {
		return ""
	}
	rel := make([]string, len(req)-t.relStart)
	copy(rel, req[t.relStart:])
	last := rel[len(rel)-1]
	if i := strings.LastIndex(last, ":"); i > 0 && auditCustomMethodSuffix(last) {
		rel[len(rel)-1] = last[:i]
	}
	joined, err := url.PathUnescape(strings.Join(rel, "/"))
	if err != nil {
		return strings.Join(rel, "/")
	}
	return joined
}

// gateChooseDiscovery picks the API that serves a call several publish: the
// one the request's host names, else the one whose claim takes it, else, of
// two, the one that does not claim it. Without any of these the call stays
// ambiguous, as the operations paths several services share are.
func gateChooseDiscovery(r *http.Request, candidates []gateDiscoveryMatch) (gateDiscoveryMatch, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	if host := gcpServiceFromHost(r); host != "" {
		for _, c := range candidates {
			if strings.TrimSuffix(c.method.api.host, ".googleapis.com") == host {
				return c, true
			}
		}
	}
	for _, c := range candidates {
		if claim := auditRESTClaims[c.method.api.host]; claim != nil && claim(r) {
			return c, true
		}
	}
	if len(candidates) == 2 {
		for i, c := range candidates {
			if auditRESTClaims[c.method.api.host] != nil {
				return candidates[1-i], true
			}
		}
	}
	return gateDiscoveryMatch{}, false
}
