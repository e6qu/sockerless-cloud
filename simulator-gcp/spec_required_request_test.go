package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// A Discovery document expresses required-ness only for requests, and it does
// so per method: a schema property carries annotations.required listing the
// method ids the property is required *for*. Route.destRange is required for
// compute.routes.insert and optional everywhere else.
//
// Nothing checked that the simulator refuses a request that omits one. That is
// the same defect two Azure API Management contracts had — a resource stored
// that the service would have refused — and it is invisible to the response
// validators, which can only judge fields that are present.

// requiredRequestProperty is one property a method requires of its request.
type requiredRequestProperty struct {
	File       string
	MethodID   string
	HTTPMethod string
	Path       string
	Host       string
	Schema     string
	Property   string
}

// TestRequestsMissingARequiredPropertyAreRefused drives every method that
// declares a required request property with a body that omits it, and requires
// a refusal. A request the service would reject must not be accepted here:
// accepting it stores a resource the caller could never have created.
func TestRequestsMissingARequiredPropertyAreRefused(t *testing.T) {
	required := loadRequiredRequestProperties(t)
	if len(required) == 0 {
		t.Fatal("no required request properties found in the vendored Discovery documents")
	}

	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}

	var accepted []string
	judged := 0
	for _, req := range required {
		if _, skip := requiredRequestExemptions[req.MethodID+"."+req.Property]; skip {
			continue
		}
		uri, ok := renderRequiredRequestURI(req)
		if !ok {
			continue
		}
		// The body omits the property under test and carries nothing else, so
		// what the method rejects it for is the omission.
		httpReq := httptest.NewRequest(req.HTTPMethod, "http://"+req.Host+uri, strings.NewReader("{}"))
		httpReq.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httpReq)
		body := rec.Body.String()
		if rec.Code >= 200 && rec.Code < 300 {
			accepted = append(accepted, fmt.Sprintf("%s: %s omits %s.%s (%s %s → %d %s)",
				req.File, req.MethodID, req.Schema, req.Property, req.HTTPMethod, uri,
				rec.Code, strings.TrimSpace(body)))
			continue
		}
		// A refusal has to say what it refused. A bare status with no error
		// document is not something a client can act on, and it would let an
		// empty response stand in for a rejection.
		if !strings.Contains(body, `"error"`) {
			accepted = append(accepted, fmt.Sprintf(
				"%s: %s refused a request omitting %s.%s with %d and no error document (%s %s)",
				req.File, req.MethodID, req.Schema, req.Property, rec.Code, req.HTTPMethod, uri))
			continue
		}
		// A method that answers 404 refused the parent, not the omission, so
		// the probe never reached the validation it is here to check. Counting
		// those as passes is how this gate would go quiet: the count below is
		// what says how much of the corpus it actually judged.
		if rec.Code != http.StatusNotFound {
			judged++
		}
	}
	// The judged count may only grow. A method that starts answering 404 to a
	// probe it used to validate has stopped being covered, and the count is
	// the only thing that notices.
	if judged < requiredRequestJudgedFloor {
		t.Errorf("the probe judged %d of %d required properties, below the floor of %d — "+
			"a method that used to reject the omission now refuses the parent instead, "+
			"so it is no longer covered", judged, len(required), requiredRequestJudgedFloor)
	}
	if judged > requiredRequestJudgedFloor {
		t.Errorf("the probe judged %d required properties, above the floor of %d — "+
			"raise requiredRequestJudgedFloor to hold the gain", judged, requiredRequestJudgedFloor)
	}
	sort.Strings(accepted)
	if len(accepted) > 0 {
		t.Fatalf("%d method(s) accepted a request missing a property the document requires of it — "+
			"reject it the way the service does, or exempt it with its reason:\n  %s",
			len(accepted), strings.Join(accepted, "\n  "))
	}
}

// renderRequiredRequestURI fills the method's path parameters with values the
// document's own patterns admit. A collection-level insert has only ancestry in
// its path, so the values name a project and a location rather than the
// resource under test.
func renderRequiredRequestURI(req requiredRequestProperty) (string, bool) {
	uri := req.Path
	for {
		open := strings.Index(uri, "{")
		if open < 0 {
			break
		}
		close := strings.Index(uri[open:], "}")
		if close < 0 {
			return "", false
		}
		name := strings.Trim(uri[open+1:open+close], "+")
		uri = uri[:open] + requiredRequestPathValue(name) + uri[open+close+1:]
	}
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return uri, true
}

// requiredRequestPathValue is a value the named path parameter admits. Project
// and location names are the common ancestry; anything else takes a generic
// name, which every Google Cloud resource-name pattern accepts.
func requiredRequestPathValue(name string) string {
	switch name {
	case "project", "projectId", "projectsId":
		return "test-project"
	case "zone", "zonesId":
		return "us-central1-a"
	case "region", "regionsId", "location", "locationsId":
		return "us-central1"
	}
	return "probe-parent"
}

// loadRequiredRequestProperties reads every property the vendored Discovery
// documents mark required for a method, paired with that method's own route.
func loadRequiredRequestProperties(t *testing.T) []requiredRequestProperty {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "gcp", "*.discovery.json.gz"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vendored Discovery documents found (glob err: %v)", err)
	}

	type rawMethod struct {
		ID         string `json:"id"`
		HTTPMethod string `json:"httpMethod"`
		Path       string `json:"path"`
		FlatPath   string `json:"flatPath"`
	}
	type rawResource struct {
		Methods   map[string]rawMethod   `json:"methods"`
		Resources map[string]rawResource `json:"resources"`
	}
	type rawProperty struct {
		Annotations struct {
			Required []string `json:"required"`
		} `json:"annotations"`
	}
	type rawSchema struct {
		Properties map[string]rawProperty `json:"properties"`
	}

	var out []requiredRequestProperty
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gunzip %s: %v", path, err)
		}
		var doc struct {
			BasePath  string                 `json:"basePath"`
			RootURL   string                 `json:"rootUrl"`
			Schemas   map[string]rawSchema   `json:"schemas"`
			Methods   map[string]rawMethod   `json:"methods"`
			Resources map[string]rawResource `json:"resources"`
		}
		err = json.NewDecoder(gz).Decode(&doc)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}

		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(doc.RootURL, "https://"), "http://"), "/")
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}

		byID := map[string]rawMethod{}
		var walk func(map[string]rawMethod, map[string]rawResource)
		walk = func(methods map[string]rawMethod, resources map[string]rawResource) {
			for _, m := range methods {
				if m.ID != "" {
					byID[m.ID] = m
				}
			}
			for _, r := range resources {
				walk(r.Methods, r.Resources)
			}
		}
		walk(doc.Methods, doc.Resources)

		for schemaID, schema := range doc.Schemas {
			for propertyName, property := range schema.Properties {
				for _, methodID := range property.Annotations.Required {
					method, ok := byID[methodID]
					if !ok {
						continue
					}
					methodPath := method.FlatPath
					if methodPath == "" {
						methodPath = method.Path
					}
					out = append(out, requiredRequestProperty{
						File: filepath.Base(path), MethodID: methodID,
						HTTPMethod: strings.ToUpper(method.HTTPMethod),
						Path:       strings.TrimSuffix(doc.BasePath, "/") + "/" + strings.TrimPrefix(methodPath, "/"),
						Host:       host, Schema: schemaID, Property: propertyName,
					})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MethodID != out[j].MethodID {
			return out[i].MethodID < out[j].MethodID
		}
		return out[i].Schema+out[i].Property < out[j].Schema+out[j].Property
	})
	return out
}

// requiredRequestJudgedFloor is how many of the corpus's required properties
// the probe reaches validation for. The rest answer 404 because the probe
// addresses a parent resource nothing created — the omission is never reached,
// so those are unjudged rather than passing. Raise this when a method starts
// being judged; a fall means one stopped.
const requiredRequestJudgedFloor = 53

// requiredRequestExemptions names a method-and-property pair the probe cannot
// judge, with the reason. Keyed "<method id>.<property>".
var requiredRequestExemptions = map[string]string{}
