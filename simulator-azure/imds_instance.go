package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The request rules of "Azure Instance Metadata Service"
// (specs/cloud-api/azure/imds-instance-metadata-service.md.gz): versioning,
// the json and text formats, and route parameters that walk the document.

// azureIMDSVersions lists the page's supported API versions, newest first.
var azureIMDSVersions = []string{
	"2025-04-07", "2024-07-17", "2024-03-15", "2023-11-15", "2023-07-01",
	"2021-12-13", "2021-11-15", "2021-11-01", "2021-10-01", "2021-08-01",
	"2021-05-01", "2021-03-01", "2021-02-01", "2021-01-01", "2020-12-01",
	"2020-10-01", "2020-09-01", "2020-07-15", "2020-06-01", "2019-11-01",
	"2019-08-15", "2019-08-01", "2019-06-04", "2019-06-01", "2019-04-30",
	"2019-03-11", "2019-02-01", "2018-10-01", "2018-04-02", "2018-02-01",
	"2017-12-01", "2017-10-01", "2017-08-01", "2017-04-02", "2017-03-01",
}

// azureIMDSVersionsSince is the versions a category serves: those from the
// version that introduced it, newest first.
func azureIMDSVersionsSince(introduced string) []string {
	var out []string
	for _, version := range azureIMDSVersions {
		if version >= introduced {
			out = append(out, version)
		}
	}
	return out
}

var (
	azureIMDSInstanceVersions     = azureIMDSVersionsSince("2017-04-02")
	azureIMDSAttestedVersions     = azureIMDSVersionsSince("2018-10-01")
	azureIMDSLoadBalancerVersions = azureIMDSVersionsSince("2020-10-01")
)

func azureIMDSBadRequest(w http.ResponseWriter, message string) {
	sim.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": message})
}

// azureIMDSQuery refuses a request with a repeated query parameter or without
// one of the versions the endpoint serves, and returns the version.
func azureIMDSQuery(w http.ResponseWriter, r *http.Request, versions []string) (string, bool) {
	query := r.URL.Query()
	for name, values := range query {
		if len(values) > 1 {
			azureIMDSBadRequest(w, "Bad request. Query parameter '"+name+"' is specified more than once.")
			return "", false
		}
	}
	version := query.Get("api-version")
	for _, served := range versions {
		if version == served {
			return version, true
		}
	}
	newest := versions
	if len(newest) > 3 {
		newest = newest[:3]
	}
	sim.WriteJSON(w, http.StatusBadRequest, map[string]any{
		"error":           "Bad request. api-version is invalid or was not specified in the request. For more information refer to aka.ms/azureimds",
		"newest-versions": newest,
	})
	return "", false
}

// azureIMDSFormat reads the format parameter, json by default.
func azureIMDSFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch format := r.URL.Query().Get("format"); format {
	case "", "json":
		return "json", true
	case "text":
		return "text", true
	default:
		azureIMDSBadRequest(w, "Bad request. format '"+format+"' is not supported; use json or text.")
		return "", false
	}
}

// azureIMDSServeFiltered answers document filtered by the route parameters
// after the category root, in the requested format. A leaf answers only as
// text, as the page's note on leaf nodes requires.
func azureIMDSServeFiltered(w http.ResponseWriter, route, format string, document any) {
	node, err := azureIMDSDocument(document)
	if err != nil {
		AzureError(w, "InternalServerError", err.Error(), http.StatusInternalServerError)
		return
	}
	for _, key := range strings.Split(route, "/") {
		if key == "" {
			continue
		}
		switch parent := node.(type) {
		case map[string]any:
			child, ok := parent[key]
			if !ok {
				sim.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
				return
			}
			node = child
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(parent) {
				sim.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
				return
			}
			node = parent[index]
		default:
			sim.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
			return
		}
	}
	_, isObject := node.(map[string]any)
	_, isArray := node.([]any)
	leaf := !isObject && !isArray
	if format == "json" {
		if leaf {
			azureIMDSBadRequest(w, "Bad request. The requested element is a leaf node; specify format=text to read it.")
			return
		}
		sim.WriteJSON(w, http.StatusOK, node)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(azureIMDSText(node)))
}

// azureIMDSDocument turns a response into the generic tree route parameters
// walk, keyed the way the JSON response spells it.
func azureIMDSDocument(document any) (any, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, err
	}
	return node, nil
}

// azureIMDSText renders a leaf as its value and an object or array as its
// keys or indexes, one per line, with "/" after each one that has children.
func azureIMDSText(node any) string {
	switch value := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key, child := range value {
			keys = append(keys, key+azureIMDSTextSuffix(child))
		}
		sort.Strings(keys)
		return strings.Join(keys, "\n")
	case []any:
		lines := make([]string, len(value))
		for i, child := range value {
			lines[i] = strconv.Itoa(i) + azureIMDSTextSuffix(child)
		}
		return strings.Join(lines, "\n")
	case string:
		return value
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(value)
		return string(raw)
	}
}

func azureIMDSTextSuffix(child any) string {
	switch child.(type) {
	case map[string]any, []any:
		return "/"
	}
	return ""
}

// azureIMDSComputeIntroduced is the version that introduced each compute
// member the simulator serves, from the page's schema breakdown; a request
// for an older version does not get it.
var azureIMDSComputeIntroduced = map[string]string{
	"azEnvironment":        "2018-10-01",
	"location":             "2017-04-02",
	"name":                 "2017-04-02",
	"offer":                "2017-04-02",
	"osProfile":            "2020-07-15",
	"osType":               "2017-04-02",
	"placementGroupId":     "2017-08-01",
	"platformFaultDomain":  "2017-04-02",
	"platformUpdateDomain": "2017-04-02",
	"provider":             "2018-10-01",
	"publicKeys":           "2018-04-02",
	"publisher":            "2017-04-02",
	"resourceGroupName":    "2017-08-01",
	"resourceId":           "2019-03-11",
	"sku":                  "2017-04-02",
	"subscriptionId":       "2017-08-01",
	"tags":                 "2017-08-01",
	"tagsList":             "2019-06-04",
	"userData":             "2021-01-01",
	"version":              "2017-04-02",
	"vmId":                 "2017-04-02",
	"vmScaleSetName":       "2017-12-01",
	"vmSize":               "2017-04-02",
	"zone":                 "2017-12-01",
}

// azureIMDSComputeAt drops the members a version predates.
func azureIMDSComputeAt(compute map[string]any, version string) map[string]any {
	out := make(map[string]any, len(compute))
	for key, value := range compute {
		if introduced, ok := azureIMDSComputeIntroduced[key]; ok && version < introduced {
			continue
		}
		out[key] = value
	}
	if profile, ok := out["osProfile"].(map[string]any); ok && version < "2020-10-01" {
		trimmed := make(map[string]any, len(profile))
		for key, value := range profile {
			if key != "disablePasswordAuthentication" {
				trimmed[key] = value
			}
		}
		out["osProfile"] = trimmed
	}
	return out
}

// handleAzureIMDSInstance serves /metadata/instance and every route under it.
func handleAzureIMDSInstance(w http.ResponseWriter, r *http.Request) {
	if !mustMetadataHeader(w, r) {
		return
	}
	version, ok := azureIMDSQuery(w, r, azureIMDSInstanceVersions)
	if !ok {
		return
	}
	format, ok := azureIMDSFormat(w, r)
	if !ok {
		return
	}
	vm, ok := azureIMDSRequest(w, r)
	if !ok {
		return
	}
	azureIMDSServeFiltered(w, r.PathValue("route"), format, map[string]any{
		"compute": azureIMDSComputeAt(azureIMDSCompute(vm), version),
		"network": azureIMDSNetwork(vm),
	})
}
