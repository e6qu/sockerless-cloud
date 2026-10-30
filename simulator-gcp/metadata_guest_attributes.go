package main

import (
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Guest attributes are the metadata a process inside a Compute Engine
// instance writes through its metadata server once `enable-guest-attributes`
// is TRUE on the instance or its project: values under a namespace, which
// instances.getGuestAttributes reads from outside. See
// compute-metadata-guest-attributes in specs/cloud-api/gcp.

// gcpGuestAttributes holds each instance's guest attributes by namespace and
// key, keyed by the instance's self link.
var gcpGuestAttributes sim.Store[map[string]map[string]string]

const (
	guestAttributeKeyLimit   = 128
	guestAttributeValueLimit = 256 << 10
)

// guestAttributeName is what a namespace or key may be spelled with: letters,
// numerals, underscores and hyphens.
var guestAttributeName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func gceGuestAttributesGate(r *http.Request) (int, string) {
	if !gceMetadataAttributeTrue(r, "enable-guest-attributes") {
		return http.StatusForbidden, "Guest attributes endpoint access is disabled."
	}
	return http.StatusOK, ""
}

func gceGuestAttributesOf(inst ComputeInstance) map[string]map[string]string {
	if gcpGuestAttributes == nil {
		return nil
	}
	held, _ := gcpGuestAttributes.Get(inst.SelfLink)
	return held
}

// gceMetadataGuestAttributesChildren lists the instance's namespaces, each a
// directory of its keys.
func gceMetadataGuestAttributesChildren(r *http.Request, inst ComputeInstance) []gceMetadataEntry {
	if status, _ := gceGuestAttributesGate(r); status != http.StatusOK {
		return []gceMetadataEntry{}
	}
	held := gceGuestAttributesOf(inst)
	namespaces := make([]string, 0, len(held))
	for namespace := range held {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	entries := make([]gceMetadataEntry, 0, len(namespaces))
	for _, namespace := range namespaces {
		keys := gceKeyEntries(held[namespace])
		for i := range keys {
			keys[i].jsonName = keys[i].name
		}
		entries = append(entries, gceMetadataEntry{name: namespace, jsonName: namespace, children: func(*http.Request) []gceMetadataEntry { return keys }})
	}
	return entries
}

// registerGuestAttributeWrites serves the writes a process inside an instance
// makes: PUT sets a key of a namespace to the request body, DELETE removes it.
func registerGuestAttributeWrites(srv *sim.Server) {
	gcpGuestAttributes = sim.Announcing(sim.MakeStore[map[string]map[string]string](srv.DB(), "compute_guest_attributes"), gceMetadataChanges)
	const path = "/computeMetadata/v1/instance/guest-attributes/{namespace}/{key}"
	write := func(method string, apply func(held map[string]map[string]string, namespace, key string, r *http.Request) (int, string)) {
		srv.HandleFunc(method+" "+path, func(w http.ResponseWriter, r *http.Request) {
			r, ok := gceMetadataAdmit(w, r)
			if !ok {
				return
			}
			inst, ok := gcpMetadataInstanceFor(r)
			if !ok {
				metadataNotFound(w)
				return
			}
			if status, message := gceGuestAttributesGate(r); status != http.StatusOK {
				http.Error(w, message, status)
				return
			}
			namespace, key := sim.PathParam(r, "namespace"), sim.PathParam(r, "key")
			if !guestAttributeName.MatchString(namespace) || !guestAttributeName.MatchString(key) {
				http.Error(w, "a guest attribute namespace and key use only letters, numerals, underscores and hyphens", http.StatusBadRequest)
				return
			}
			if len(key) > guestAttributeKeyLimit {
				http.Error(w, "a guest attribute key is at most 128 bytes", http.StatusBadRequest)
				return
			}
			held := gceGuestAttributesOf(inst)
			if held == nil {
				held = map[string]map[string]string{}
			}
			if status, message := apply(held, namespace, key, r); status != http.StatusOK {
				http.Error(w, message, status)
				return
			}
			gcpGuestAttributes.Put(inst.SelfLink, held)
		})
	}
	write(http.MethodPut, func(held map[string]map[string]string, namespace, key string, r *http.Request) (int, string) {
		value, err := io.ReadAll(io.LimitReader(r.Body, guestAttributeValueLimit+1))
		if err != nil {
			return http.StatusBadRequest, err.Error()
		}
		if len(value) > guestAttributeValueLimit {
			return http.StatusBadRequest, "a guest attribute value is at most 256 KiB"
		}
		if held[namespace] == nil {
			held[namespace] = map[string]string{}
		}
		held[namespace][key] = string(value)
		return http.StatusOK, ""
	})
	write(http.MethodDelete, func(held map[string]map[string]string, namespace, key string, _ *http.Request) (int, string) {
		if _, ok := held[namespace][key]; !ok {
			return http.StatusNotFound, "404 page not found"
		}
		delete(held[namespace], key)
		if len(held[namespace]) == 0 {
			delete(held, namespace)
		}
		return http.StatusOK, ""
	})
}

// gcpForgetGuestAttributes drops a deleted instance's guest attributes.
func gcpForgetGuestAttributes(selfLink string) {
	if gcpGuestAttributes != nil {
		gcpGuestAttributes.Delete(selfLink)
	}
}

// handleGetGuestAttributes answers instances.getGuestAttributes from the
// attributes the instance's processes wrote: every entry for an empty
// queryPath, a namespace's for `NAMESPACE/`, one key's for `NAMESPACE/KEY`,
// and the value of the `NAMESPACE/KEY` that variableKey names.
func handleGetGuestAttributes(w http.ResponseWriter, r *http.Request, inst ComputeInstance) {
	held := gceGuestAttributesOf(inst)
	out := map[string]any{"kind": "compute#guestAttributes"}
	if variableKey := r.URL.Query().Get("variableKey"); variableKey != "" {
		namespace, key, _ := strings.Cut(variableKey, "/")
		value, ok := held[namespace][key]
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s/guestAttributes/%s' was not found", computeSelfLink(inst.SelfLink), variableKey)
			return
		}
		out["variableKey"] = variableKey
		out["variableValue"] = value
		out["selfLink"] = computeSelfLink(inst.SelfLink) + "/guestAttributes/" + variableKey
		sim.WriteJSON(w, http.StatusOK, out)
		return
	}
	queryPath := r.URL.Query().Get("queryPath")
	namespace, key, _ := strings.Cut(queryPath, "/")
	namespaces := make([]string, 0, len(held))
	for ns := range held {
		if namespace == "" || ns == namespace {
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	items := []map[string]string{}
	for _, ns := range namespaces {
		keys := make([]string, 0, len(held[ns]))
		for k := range held[ns] {
			if key == "" || k == key {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			items = append(items, map[string]string{"namespace": ns, "key": k, "value": held[ns][k]})
		}
	}
	if queryPath != "" && len(items) == 0 {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s/guestAttributes/%s' was not found", computeSelfLink(inst.SelfLink), queryPath)
		return
	}
	out["queryPath"] = queryPath
	out["queryValue"] = map[string]any{"items": items}
	out["selfLink"] = computeSelfLink(inst.SelfLink) + "/guestAttributes/" + queryPath
	sim.WriteJSON(w, http.StatusOK, out)
}
