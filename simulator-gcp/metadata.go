package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

var gcpMetadataInstancesByIP workloadhost.MetadataIndex[ComputeInstance]

// gcpMetadataInstanceFor is the instance a request comes from, as the store
// holds it now: setMetadata and setTags change an instance while it runs.
func gcpMetadataInstanceFor(r *http.Request) (ComputeInstance, bool) {
	indexed, ok := gcpMetadataInstancesByIP.ForRequest(r)
	if !ok || gcpInstances == nil {
		return ComputeInstance{}, false
	}
	return gcpInstances.Get(indexed.SelfLink)
}

// gceNetworkMTU is the MTU of a VPC network created without one.
const gceNetworkMTU = 1460

// registerComputeMetadata serves the metadata server every workload Google
// Cloud runs reads: a Compute Engine instance, a Cloud Run container, a Cloud
// Build step. Google serves it at metadata.google.internal (169.254.169.254)
// on port 80 and requires `Metadata-Flavor: Google` on every read. A workload
// reaches it through its network's sidecar, which GCE_METADATA_HOST and
// GCE_METADATA_IP name, so the Google Cloud client libraries find it
// unchanged.
//
// The tree below is the single description the listings, the recursive reads
// and the leaf reads all resolve against, so a listing never advertises a key
// that does not answer; metadata_directory_test.go walks it.
func registerComputeMetadata(srv *sim.Server) {
	handle := func(pattern string, h http.HandlerFunc) {
		srv.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r, ok := gceMetadataAdmit(w, r); ok {
				gceMetadataServe(w, r, h)
			}
		})
	}
	handle("GET /computeMetadata", redirectToMetadataDirectory)
	handle("GET /computeMetadata/{$}", func(w http.ResponseWriter, r *http.Request) {
		writeMetadataDirectory(w, r, gceMetadataEntry{name: "computeMetadata", children: gceMetadataVersions})
	})
	handle("GET /computeMetadata/v1/{path...}", func(w http.ResponseWriter, r *http.Request) {
		serveMetadataPath(w, r, "v1/"+sim.PathParam(r, "path"))
	})
	registerGuestAttributeWrites(srv)
	registerMetadataResidencyProbe(srv)
}

// The metadata server is a filesystem-shaped namespace: a key ending in a
// slash is a directory whose GET lists its children, sub-directories with a
// trailing slash, and whose `?recursive=true` GET returns the sub-tree as JSON
// with each kebab-case segment spelled camelCase. A directory addressed
// without its slash answers `301 Moved Permanently` to the slash form. See
// querying-metadata in specs/cloud-api/gcp.
//
// A client finds its credentials through the listings: the `gcloud` CLI reads
// `instance/service-accounts/` before it uses any service account.

// gceMetadataEntry is one node of the metadata tree: a directory when it has
// children, a leaf otherwise.
type gceMetadataEntry struct {
	name string
	// jsonName is the key the node takes in recursive JSON output. Empty
	// means the camelCase spelling of name.
	jsonName string
	children func(r *http.Request) []gceMetadataEntry
	// list marks a directory of numbered entries, which recursive JSON
	// renders as an array.
	list  bool
	value func(r *http.Request) any
	// jsonDefault marks a leaf the server answers as JSON unless asked for
	// `alt=text`, as it answers `tags`.
	jsonDefault bool
	// serve answers a leaf the server mints per request — `token` and
	// `identity` — which recursive output omits.
	serve http.HandlerFunc
	// gate refuses a read that addresses the node or anything beneath it,
	// with the status and message the server answers; nil admits every read.
	gate func(r *http.Request) (int, string)
}

func (e gceMetadataEntry) isDir() bool { return e.children != nil }

// listingName is how the node appears in a directory listing.
func (e gceMetadataEntry) listingName() string {
	if e.isDir() {
		return e.name + "/"
	}
	return e.name
}

// recursiveKey is the node's key in recursive JSON output.
func (e gceMetadataEntry) recursiveKey() string {
	if e.jsonName != "" {
		return e.jsonName
	}
	return kebabToCamel(e.name)
}

// kebabToCamel spells a kebab-case metadata key the way the metadata server
// spells it in recursive JSON: `numeric-project-id` becomes `numericProjectId`.
func kebabToCamel(s string) string {
	parts := strings.Split(s, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func leafOf(name string, value func(*http.Request) any) gceMetadataEntry {
	return gceMetadataEntry{name: name, value: value}
}

func constantLeaf(name string, value any) gceMetadataEntry {
	return gceMetadataEntry{name: name, value: func(*http.Request) any { return value }}
}

func gceMetadataRootChildren(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{{name: "computeMetadata", children: gceMetadataVersions}}
}

func gceMetadataVersions(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{{name: "v1", children: gceMetadataV1Children}}
}

func gceMetadataV1Children(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{
		{name: "instance", children: gceMetadataInstanceChildren},
		{name: "project", children: gceMetadataProjectChildren},
		{name: "universe", children: gceMetadataUniverseChildren},
	}
}

// gceMetadataUniverseChildren carries the universe domain, the API host
// suffix every Google auth library and the `gcloud` CLI build endpoints from.
// The simulator implements the public universe, googleapis.com.
func gceMetadataUniverseChildren(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{constantLeaf("universe-domain", metadataUniverseDomain)}
}

const metadataUniverseDomain = "googleapis.com"

// gceMetadataProjectChildren lists the workload's project. Its number comes
// from Cloud Resource Manager, and recursive JSON carries it as a number.
func gceMetadataProjectChildren(r *http.Request) []gceMetadataEntry {
	workload := gceMetadataWorkloadFor(r)
	var children []gceMetadataEntry
	children = append(children, gceMetadataEntry{name: "attributes", children: gceMetadataProjectAttributesChildren})
	if number, ok := workload.projectNumber(); ok {
		if n, err := strconv.ParseInt(number, 10, 64); err == nil {
			children = append(children, constantLeaf("numeric-project-id", n))
		}
	}
	return append(children, constantLeaf("project-id", workload.projectID()))
}

// gceProjectAttributes is the project's common instance metadata, which
// projects.setCommonInstanceMetadata writes and every instance reads.
func gceProjectAttributes(project string) map[string]string {
	attributes := map[string]string{}
	if gcpComputeProjects == nil {
		return attributes
	}
	held, ok := gcpComputeProjects.Get(project)
	if !ok {
		return attributes
	}
	metadata, _ := held["commonInstanceMetadata"].(map[string]any)
	items, _ := metadata["items"].([]any)
	for _, item := range items {
		entry, _ := item.(map[string]any)
		if key, _ := entry["key"].(string); key != "" {
			attributes[key], _ = entry["value"].(string)
		}
	}
	return attributes
}

func gceMetadataProjectAttributesChildren(r *http.Request) []gceMetadataEntry {
	attributes := gceProjectAttributes(gceMetadataWorkloadFor(r).projectID())
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	children := make([]gceMetadataEntry, 0, len(keys))
	for _, key := range keys {
		entry := constantLeaf(key, attributes[key])
		entry.jsonName = key
		children = append(children, entry)
	}
	return children
}

// gceMetadataAttribute is a custom metadata value the reading instance sees:
// its own, or else its project's.
func gceMetadataAttribute(r *http.Request, key string) (string, bool) {
	if inst, ok := gcpMetadataInstanceFor(r); ok {
		if value, ok := gcpInstanceMetadataValue(inst, key); ok {
			return value, true
		}
	}
	value, ok := gceProjectAttributes(gceMetadataWorkloadFor(r).projectID())[key]
	return value, ok
}

// gceMetadataAttributeTrue is whether a boolean custom metadata key the
// reading instance sees is set to TRUE.
func gceMetadataAttributeTrue(r *http.Request, key string) bool {
	value, ok := gceMetadataAttribute(r, key)
	return ok && strings.EqualFold(value, "true")
}

func gceKeyEntries(keys map[string]string) []gceMetadataEntry {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]gceMetadataEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, constantLeaf(name, keys[name]))
	}
	return entries
}

func gceMetadataInstanceChildren(r *http.Request) []gceMetadataEntry {
	workload := gceMetadataWorkloadFor(r)
	entries := []gceMetadataEntry{{name: "attributes", children: gceMetadataAttributesChildren}}
	if inst, ok := gcpMetadataInstanceFor(r); ok {
		entries = append(entries, gceMetadataComputeInstanceKeys(r, inst)...)
	} else if workload.cloudRunInstance != "" {
		entries = append(entries, gceMetadataCloudRunKeys(r, workload)...)
	}
	entries = append(entries,
		gceMetadataEntry{name: "network-interfaces", list: true, children: gceMetadataInterfacesChildren},
		gceMetadataEntry{name: "service-accounts", children: gceMetadataServiceAccountsChildren},
	)
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries
}

// gceMetadataCloudRunKeys are the instance keys Cloud Run's container contract
// states: the instance's own identifier, its region, and the `<region>-1` zone
// it reports because Cloud Run names no zone. It states no name or hostname.
func gceMetadataCloudRunKeys(r *http.Request, workload gceMetadataWorkload) []gceMetadataEntry {
	entries := []gceMetadataEntry{constantLeaf("id", workload.cloudRunInstance)}
	number, ok := workload.projectNumber()
	if !ok || workload.region == "" {
		return entries
	}
	return append(entries,
		constantLeaf("region", fmt.Sprintf("projects/%s/regions/%s", number, workload.region)),
		constantLeaf("zone", fmt.Sprintf("projects/%s/zones/%s-1", number, workload.region)),
	)
}

// gceMetadataAttributesChildren lists the reading instance's custom metadata:
// the keys instances.insert and setMetadata wrote, startup-script among them,
// which the guest agent fetches from here. A workload that is no instance
// has none.
func gceMetadataAttributesChildren(r *http.Request) []gceMetadataEntry {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok || inst.Metadata == nil {
		return []gceMetadataEntry{}
	}
	children := make([]gceMetadataEntry, 0, len(inst.Metadata.Items))
	for _, item := range inst.Metadata.Items {
		entry := constantLeaf(item.Key, item.Value)
		entry.jsonName = item.Key
		children = append(children, entry)
	}
	return children
}

// gceMetadataServiceAccountsChildren lists the workload's identity twice, once
// under the `default` alias and once under its own email, as Compute Engine
// does.
func gceMetadataServiceAccountsChildren(r *http.Request) []gceMetadataEntry {
	email, ok := metadataServiceAccountEmail(r, "default")
	if !ok {
		return []gceMetadataEntry{}
	}
	account := func(name string) gceMetadataEntry {
		return gceMetadataEntry{name: name, jsonName: name, children: func(r *http.Request) []gceMetadataEntry {
			return gceMetadataServiceAccountChildren(r, name)
		}}
	}
	return []gceMetadataEntry{account("default"), account(email)}
}

func gceMetadataServiceAccountChildren(r *http.Request, account string) []gceMetadataEntry {
	return []gceMetadataEntry{
		constantLeaf("aliases", []string{"default"}),
		leafOf("email", func(r *http.Request) any {
			email, _ := metadataServiceAccountEmail(r, account)
			return email
		}),
		{name: "identity", serve: func(w http.ResponseWriter, r *http.Request) { serveMetadataIdentity(w, r, account) }},
		leafOf("scopes", func(r *http.Request) any { return metadataServiceAccountScopes(r) }),
		{name: "token", serve: func(w http.ResponseWriter, r *http.Request) { serveMetadataToken(w, r, account) }},
	}
}

// serveMetadataPath answers a GET of /computeMetadata/<path>: a directory's
// listing or recursive contents, a leaf's value, or not found.
func serveMetadataPath(w http.ResponseWriter, r *http.Request, path string) {
	isDirPath := strings.HasSuffix(path, "/")
	entry, status, message := resolveMetadataPath(r, strings.Split(strings.TrimSuffix(path, "/"), "/"))
	if status != http.StatusOK {
		http.Error(w, message, status)
		return
	}
	waiting := strings.EqualFold(r.URL.Query().Get("wait_for_change"), "true")
	switch {
	case entry.isDir() && !isDirPath:
		redirectToMetadataDirectory(w, r)
	case entry.isDir():
		if waiting && !strings.EqualFold(r.URL.Query().Get("recursive"), "true") {
			http.Error(w, "wait_for_change is not supported on a directory listing", http.StatusBadRequest)
			return
		}
		writeMetadataDirectory(w, r, entry)
	case isDirPath:
		metadataNotFound(w)
	case entry.serve != nil:
		if waiting {
			http.Error(w, "wait_for_change is not supported for "+entry.name, http.StatusBadRequest)
			return
		}
		entry.serve(w, r)
	default:
		writeMetadataValue(w, r, entry.value(r), entry.jsonDefault)
	}
}

// resolveMetadataPath walks the tree from /computeMetadata/ along segments,
// and answers the status a read of the node gets short of its value: 200 for
// a node it may read, the gate's refusal, or not found.
func resolveMetadataPath(r *http.Request, segments []string) (gceMetadataEntry, int, string) {
	node := gceMetadataEntry{name: "computeMetadata", children: gceMetadataVersions}
	for _, segment := range segments {
		if !node.isDir() {
			return gceMetadataEntry{}, http.StatusNotFound, "404 page not found"
		}
		found := false
		for _, child := range node.children(r) {
			if child.name == segment {
				node, found = child, true
				break
			}
		}
		if !found {
			return gceMetadataEntry{}, http.StatusNotFound, "404 page not found"
		}
		if node.gate != nil {
			if status, message := node.gate(r); status != http.StatusOK {
				return gceMetadataEntry{}, status, message
			}
		}
	}
	return node, http.StatusOK, ""
}

// metadataFormat is the `alt` a read asks for: "json", "text", or empty for
// the key's own default.
func metadataFormat(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch alt := r.URL.Query().Get("alt"); alt {
	case "", "json", "text":
		return alt, true
	default:
		http.Error(w, "alt must be json or text", http.StatusBadRequest)
		return "", false
	}
}

// writeMetadataValue answers a leaf: as text, a list one value per line, or —
// for `alt=json` or a key that defaults to it — as JSON.
func writeMetadataValue(w http.ResponseWriter, r *http.Request, value any, jsonDefault bool) {
	alt, ok := metadataFormat(w, r)
	if !ok {
		return
	}
	if alt == "json" || (alt == "" && jsonDefault) {
		writeMetadataJSON(w, value)
		return
	}
	writeMetadataText(w, metadataText(value))
}

func writeMetadataText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "application/text")
	_, _ = w.Write([]byte(s))
}

func writeMetadataJSON(w http.ResponseWriter, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func metadataText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	default:
		return fmt.Sprint(v)
	}
}

// writeMetadataDirectory answers a directory GET: the newline-separated
// listing, or with `recursive=true` the sub-tree, as JSON by default and as
// `path value` lines for `alt=text`.
func writeMetadataDirectory(w http.ResponseWriter, r *http.Request, dir gceMetadataEntry) {
	alt, ok := metadataFormat(w, r)
	if !ok {
		return
	}
	children := dir.children(r)
	if strings.EqualFold(r.URL.Query().Get("recursive"), "true") {
		if alt == "text" {
			var b strings.Builder
			writeMetadataRecursiveText(&b, r, "", children)
			writeMetadataText(w, b.String())
			return
		}
		writeMetadataJSON(w, metadataRecursiveDir(r, dir))
		return
	}
	names := make([]string, 0, len(children))
	for _, child := range children {
		names = append(names, child.listingName())
	}
	if alt == "json" {
		writeMetadataJSON(w, names)
		return
	}
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteString("\n")
	}
	writeMetadataText(w, b.String())
}

// metadataRecursiveValue renders a sub-tree the way `?recursive=true` does:
// an object keyed by each child's camelCase spelling, a numbered directory as
// an array, and the leaves minted per request omitted.
func metadataRecursiveValue(r *http.Request, children []gceMetadataEntry) map[string]any {
	out := make(map[string]any, len(children))
	for _, child := range children {
		switch {
		case child.isDir():
			out[child.recursiveKey()] = metadataRecursiveDir(r, child)
		case child.value != nil:
			out[child.recursiveKey()] = child.value(r)
		}
	}
	return out
}

func metadataRecursiveDir(r *http.Request, dir gceMetadataEntry) any {
	children := dir.children(r)
	if !dir.list {
		return metadataRecursiveValue(r, children)
	}
	items := make([]any, 0, len(children))
	for _, child := range children {
		items = append(items, metadataRecursiveDir(r, child))
	}
	return items
}

// writeMetadataRecursiveText writes a sub-tree as `alt=text` does: one line
// per value, its path from the directory read and the value, a list's values
// each on a line of their own.
func writeMetadataRecursiveText(b *strings.Builder, r *http.Request, prefix string, children []gceMetadataEntry) {
	for _, child := range children {
		path := prefix + child.name
		switch {
		case child.isDir():
			writeMetadataRecursiveText(b, r, path+"/", child.children(r))
		case child.value != nil:
			values, isList := child.value(r).([]string)
			if !isList {
				values = []string{metadataText(child.value(r))}
			}
			for _, value := range values {
				b.WriteString(path + " " + value + "\n")
			}
		}
	}
}

// redirectToMetadataDirectory answers a directory addressed without its
// trailing slash with `301 Moved Permanently`, keeping the query so a
// redirect-following client's `recursive=true` survives.
func redirectToMetadataDirectory(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Path + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// registerMetadataResidencyProbe answers the metadata server's own root, which
// every Google auth library GETs before it resolves Application Default
// Credentials and accepts only with `Metadata-Flavor: Google` on the answer.
//
// The simulator serves every Google host on one origin, so `/` belongs to the
// console too. The required request header names the metadata server, as the
// `Host` header names a service in endpoint_hosts.go, so the probe is
// answered ahead of the mux and every other request to `/` reaches the
// origin's root. The wrapper runs ahead of the chain that attributes a
// sidecar-forwarded request to its workload, so it attributes the probe
// itself.
func registerMetadataResidencyProbe(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return sim.SidecarTrust(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/" || r.Header.Get("Metadata-Flavor") != "Google" {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := gceMetadataCaller(r); !ok {
				next.ServeHTTP(w, r)
				return
			}
			if r, ok := gceMetadataAdmit(w, r); ok {
				writeMetadataDirectory(w, r, gceMetadataEntry{children: gceMetadataRootChildren})
			}
		}))
	})
}

// metadataServiceAccountEmail resolves the account a request addresses, by the
// `default` alias or by its email, to the email of the account the workload
// runs as, and false when the workload runs as no account or as another one.
func metadataServiceAccountEmail(r *http.Request, account string) (string, bool) {
	email, ok := gceMetadataWorkloadFor(r).defaultServiceAccount()
	if !ok || (account != "default" && account != "" && account != email) {
		return "", false
	}
	return email, true
}

// metadataNotFound answers a key the workload's metadata does not hold the way
// the metadata server does.
func metadataNotFound(w http.ResponseWriter) {
	http.Error(w, "404 page not found", http.StatusNotFound)
}

// simListenAddr is the API listen address main() serves on, and
// simCallbackListenAddr the plain-HTTP one the workloads call back on; the
// two differ when the API listener terminates TLS.
var simListenAddr, simCallbackListenAddr string
