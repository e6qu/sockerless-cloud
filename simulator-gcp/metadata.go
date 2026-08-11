package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

var gcpMetadataInstancesByIP sync.Map // map[string]ComputeInstance

// registerComputeMetadata serves the GCE metadata server endpoints used
// by every GCP compute primitive that runs a workload (GCE, Cloud Run,
// Cloud Functions, App Engine). Real GCP exposes the service at
//
//	metadata.google.internal     (resolves to 169.254.169.254)
//	metadata                     (short alias)
//
// on port 80, requiring the `Metadata-Flavor: Google` request header on
// every read. Workloads in the sim reach it via the sim's main HTTP
// listener; cloud-product translators inject `GCE_METADATA_HOST` and
// `GCE_METADATA_IP` env vars on the workload host so the GCP Go/Python
// SDKs pick them up automatically.
//
// Coverage today: project ID, default-zone, instance ID + zone + name,
// the universe domain, service-account default access tokens (delegates to the
// existing IAM `iamcredentials.generateAccessToken` shape) and ID tokens
// (delegates to `iamcredentials.generateIdToken`), plus the directory listings
// and recursive reads a client walks to discover them (see the metadata tree
// below). Not yet covered: instance attributes, disks, network interfaces,
// startup-script — add as workloads need them per the sim-parity-per-commit
// rule.
func registerComputeMetadata(srv *sim.Server) {
	mustFlavor := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "Missing Metadata-Flavor:Google header.", http.StatusForbidden)
			return false
		}
		w.Header().Set("Metadata-Flavor", "Google")
		w.Header().Set("Server", "Metadata Server for VM")
		return true
	}
	writeText := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/text")
		_, _ = w.Write([]byte(s))
	}

	// /computeMetadata/v1/project/project-id
	srv.HandleFunc("GET /computeMetadata/v1/project/project-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataProjectID(r))
	})

	srv.HandleFunc("GET /computeMetadata/v1/project/numeric-project-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, strconv.FormatInt(metadataNumericProjectID, 10))
	})

	// /computeMetadata/v1/instance/{id|zone|name|hostname}
	srv.HandleFunc("GET /computeMetadata/v1/instance/id", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, strconv.FormatInt(metadataInstanceID, 10))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/zone", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataInstanceZone(r))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/name", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataInstanceName(r))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/hostname", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataInstanceHostname(r))
	})

	// /computeMetadata/v1/universe/universe-domain — the API host suffix a
	// client builds every Google endpoint from. Google auth libraries and the
	// `gcloud` CLI read it before choosing an endpoint.
	srv.HandleFunc("GET /computeMetadata/v1/universe/universe-domain", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataUniverseDomain)
	})

	// /computeMetadata/v1/instance/service-accounts/{sa}/{leaf}
	//
	// `default` is an alias for the project's default compute SA. The
	// sim accepts any SA name and stamps the requested audience into
	// the response (matches real GCE behaviour).
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/email", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, metadataServiceAccountEmail(r, sim.PathParam(r, "sa")))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/scopes", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		// Cloud-platform broad scope is what real GCE returns by default.
		writeText(w, strings.Join(metadataServiceAccountScopes, "\n"))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/aliases", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		writeText(w, strings.Join(metadataServiceAccountAliases, "\n"))
	})

	// /computeMetadata/v1/instance/service-accounts/{sa}/token
	// Bearer access token. Mints a token signed with the simulator's
	// access-token key (see signAccessToken) so the data-plane bearer
	// middleware verifies it exactly like an OAuth2 or federated token —
	// the metadata server is the workload's real path to a usable token.
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/token", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		sa := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		now := time.Now()
		expires := now.Add(time.Hour)
		// Real GCE returns JSON: {access_token,expires_in,token_type}.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"access_token": signAccessToken(sa, now, expires),
			"expires_in":   int(time.Until(expires).Seconds()),
			"token_type":   "Bearer",
		})
	})

	// /computeMetadata/v1/instance/service-accounts/{sa}/identity?audience=...
	// Identity token. A workload reads this to obtain the bearer it presents
	// when invoking a sibling Cloud Run / Cloud Functions service (the
	// google.golang.org/api/idtoken compute source fetches it verbatim). The
	// token is signed with the simulator's access-token key (see
	// signIdentityToken) so the invoked endpoint's data-plane bearer
	// middleware verifies it exactly like an OAuth2 or federated token — the
	// same consolidation the sibling `token` endpoint uses.
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/identity", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		audience := r.URL.Query().Get("audience")
		if audience == "" {
			http.Error(w, "non-empty audience parameter required", http.StatusBadRequest)
			return
		}
		sa := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		now := time.Now()
		expires := now.Add(time.Hour)
		token := signIdentityToken(sa, audience, now, expires)
		w.Header().Set("Content-Type", "application/text")
		_, _ = w.Write([]byte(token))
	})

	registerComputeMetadataDirectories(srv, mustFlavor)
}

// The metadata server is a filesystem-shaped namespace: every node whose key
// ends in a trailing slash is a directory whose GET returns a newline-separated
// listing of its children (sub-directories carry the trailing slash, leaves do
// not), and `?recursive=true` returns the whole sub-tree as JSON with each
// kebab-case path segment spelled camelCase. Real Google answers a directory
// path written without its trailing slash with `301 Moved Permanently` to the
// slash form. See
// https://cloud.google.com/compute/docs/metadata/querying-metadata.
//
// A client reaches its credentials through those listings, not only through the
// leaves: the `gcloud` CLI enumerates the instance's identities by reading
// `/computeMetadata/v1/instance/service-accounts/` before it will use any
// service account, so a metadata server that serves only leaves cannot
// authenticate the vendor CLI.
//
// The tree below is the single description the listing, the recursive JSON and
// the leaf handlers all read, so a listing can never advertise a key no handler
// serves. metadata_directory_test.go walks it and GETs every leaf it names.

// gceMetadataEntry is one node of the metadata tree. A node with children is a
// directory; a node without is a leaf. A leaf whose value is nil is one real
// Google computes per request rather than storing — `token` and `identity`,
// which the metadata server omits from recursive output.
type gceMetadataEntry struct {
	name string
	// jsonName is the key the node takes in recursive JSON output. Empty
	// means the camelCase spelling of name.
	jsonName string
	children func(r *http.Request) []gceMetadataEntry
	value    func(r *http.Request) any
}

func (e gceMetadataEntry) isDir() bool { return e.children != nil }

// listingName is how the node appears in a directory listing: real Google
// marks a directory with a trailing slash and a leaf with none.
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

// gceMetadataUniverseChildren carries the universe domain — the API host suffix
// a client builds every Google endpoint from. `googleapis.com` is the public
// Google Cloud universe, which is the one the simulator implements; a Trusted
// Partner Cloud instance reports its own domain here. Every Google auth library
// and the `gcloud` CLI read it before choosing an endpoint.
func gceMetadataUniverseChildren(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{
		{name: "universe-domain", value: func(*http.Request) any { return metadataUniverseDomain }},
	}
}

func gceMetadataProjectChildren(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{
		{name: "numeric-project-id", value: func(*http.Request) any { return metadataNumericProjectID }},
		{name: "project-id", value: func(r *http.Request) any { return metadataProjectID(r) }},
	}
}

func gceMetadataInstanceChildren(*http.Request) []gceMetadataEntry {
	return []gceMetadataEntry{
		{name: "hostname", value: func(r *http.Request) any { return metadataInstanceHostname(r) }},
		{name: "id", value: func(*http.Request) any { return metadataInstanceID }},
		{name: "name", value: func(r *http.Request) any { return metadataInstanceName(r) }},
		{name: "service-accounts", children: gceMetadataServiceAccountsChildren},
		{name: "zone", value: func(r *http.Request) any { return metadataInstanceZone(r) }},
	}
}

// gceMetadataServiceAccountsChildren lists the instance's identities. Real GCE
// lists each account twice — once under the `default` alias and once under its
// own email — and both are directories.
func gceMetadataServiceAccountsChildren(r *http.Request) []gceMetadataEntry {
	email := metadataServiceAccountEmail(r, "default")
	return []gceMetadataEntry{
		{name: "default", jsonName: "default", children: gceMetadataServiceAccountChildren},
		{name: email, jsonName: email, children: gceMetadataServiceAccountChildren},
	}
}

// gceMetadataServiceAccountChildren lists one identity's keys. `token` and
// `identity` are minted per request and carry no value function: real Google
// serves them as leaves but omits them from recursive output.
func gceMetadataServiceAccountChildren(r *http.Request) []gceMetadataEntry {
	account := sim.PathParam(r, "sa")
	if account == "" {
		account = "default"
	}
	return []gceMetadataEntry{
		{name: "aliases", value: func(*http.Request) any { return metadataServiceAccountAliases }},
		{name: "email", value: func(r *http.Request) any { return metadataServiceAccountEmail(r, account) }},
		{name: "identity"},
		{name: "scopes", value: func(*http.Request) any { return metadataServiceAccountScopes }},
		{name: "token"},
	}
}

// registerComputeMetadataDirectories mounts every directory node of the tree,
// plus the trailing-slash redirect real Google answers a directory path with.
// Each pattern is anchored with {$} so it claims only the directory itself and
// never shadows the leaf handlers mounted beneath it.
func registerComputeMetadataDirectories(srv *sim.Server, mustFlavor func(http.ResponseWriter, *http.Request) bool) {
	dir := func(pattern string, children func(*http.Request) []gceMetadataEntry) {
		srv.HandleFunc("GET "+pattern+"{$}", func(w http.ResponseWriter, r *http.Request) {
			if !mustFlavor(w, r) {
				return
			}
			writeMetadataDirectory(w, r, children(r))
		})
		// The same node addressed without its trailing slash.
		srv.HandleFunc("GET "+strings.TrimSuffix(pattern, "/"), func(w http.ResponseWriter, r *http.Request) {
			if !mustFlavor(w, r) {
				return
			}
			redirectToMetadataDirectory(w, r)
		})
	}

	// The metadata server's own root, which every Google auth library GETs as
	// its residency probe before it will resolve Application Default
	// Credentials: it accepts the responder as a metadata server only when the
	// answer carries `Metadata-Flavor: Google` back. `gcloud auth
	// application-default` cannot mint a token without it.
	//
	// The simulator collapses every Google host onto one origin, so `/` is
	// addressed by more than one of them — the console owns it too. The
	// required request header is what names the metadata server, the same way
	// the `Host` header names a service in endpoint_hosts.go, so the probe is
	// answered ahead of the mux and every other request to `/` falls through
	// to the origin's own root untouched.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/" || r.Header.Get("Metadata-Flavor") != "Google" {
				next.ServeHTTP(w, r)
				return
			}
			if !mustFlavor(w, r) {
				return
			}
			writeMetadataDirectory(w, r, gceMetadataRootChildren(r))
		})
	})

	dir("/computeMetadata/", gceMetadataVersions)
	dir("/computeMetadata/v1/", gceMetadataV1Children)
	dir("/computeMetadata/v1/instance/", gceMetadataInstanceChildren)
	dir("/computeMetadata/v1/project/", gceMetadataProjectChildren)
	dir("/computeMetadata/v1/universe/", gceMetadataUniverseChildren)
	dir("/computeMetadata/v1/instance/service-accounts/", gceMetadataServiceAccountsChildren)
	dir("/computeMetadata/v1/instance/service-accounts/{sa}/", gceMetadataServiceAccountChildren)
}

// writeMetadataDirectory answers a directory GET: the newline-separated listing
// real Google returns, or — when the caller asked for `recursive=true` — the
// sub-tree as JSON.
func writeMetadataDirectory(w http.ResponseWriter, r *http.Request, children []gceMetadataEntry) {
	if strings.EqualFold(r.URL.Query().Get("recursive"), "true") {
		sim.WriteJSON(w, http.StatusOK, metadataRecursiveValue(r, children))
		return
	}
	var b strings.Builder
	for _, child := range children {
		b.WriteString(child.listingName())
		b.WriteString("\n")
	}
	w.Header().Set("Content-Type", "application/text")
	_, _ = w.Write([]byte(b.String()))
}

// metadataRecursiveValue renders a sub-tree the way `?recursive=true` does:
// a JSON object keyed by the camelCase spelling of each child, directories
// nested, and the per-request leaves (`token`, `identity`) omitted because real
// Google mints rather than stores them.
func metadataRecursiveValue(r *http.Request, children []gceMetadataEntry) map[string]any {
	out := make(map[string]any, len(children))
	for _, child := range children {
		switch {
		case child.isDir():
			out[child.recursiveKey()] = metadataRecursiveValue(r, child.children(r))
		case child.value != nil:
			out[child.recursiveKey()] = child.value(r)
		}
	}
	return out
}

// redirectToMetadataDirectory answers a directory addressed without its
// trailing slash with the `301 Moved Permanently` real Google answers, keeping
// any query string so a redirect-following client's `recursive=true` survives.
func redirectToMetadataDirectory(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Path + "/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// The instance identity the metadata server reports. Real GCE reports the
// numeric project number and instance id as numbers in recursive JSON, so both
// are held as integers and formatted only for the text leaves.
const (
	metadataNumericProjectID int64 = 1000000000001
	metadataInstanceID       int64 = 1000000000001
	metadataDefaultName            = "sim-instance-1"
	// metadataUniverseDomain is the public Google Cloud universe — the API
	// host suffix every Google endpoint is built from.
	metadataUniverseDomain = "googleapis.com"
)

// metadataServiceAccountAliases and metadataServiceAccountScopes are what the
// matching leaves return, as the arrays recursive JSON carries. The text leaves
// join them with newlines, which is how the metadata server serves a
// multi-valued key.
var (
	metadataServiceAccountAliases = []string{"default"}
	metadataServiceAccountScopes  = []string{"https://www.googleapis.com/auth/cloud-platform"}
)

// metadataProjectID is the project the reading workload belongs to.
func metadataProjectID(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceForRequest(r); ok {
		return gcpMetadataProject(inst.SelfLink, defaultMetadataProject(r))
	}
	return defaultMetadataProject(r)
}

// metadataInstanceZone is the fully-qualified zone the reading workload runs in.
func metadataInstanceZone(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceForRequest(r); ok && inst.Zone != "" {
		return inst.Zone
	}
	return fmt.Sprintf("projects/%s/zones/%s", defaultMetadataProject(r), defaultMetadataZone(r))
}

// metadataInstanceName is the reading workload's instance name.
func metadataInstanceName(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceForRequest(r); ok && inst.Name != "" {
		return inst.Name
	}
	return metadataDefaultName
}

// metadataInstanceHostname is the internal DNS name of the reading workload.
func metadataInstanceHostname(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceForRequest(r); ok && inst.Name != "" {
		zone := defaultMetadataZone(r)
		if inst.Zone != "" {
			zone = inst.Zone[strings.LastIndex(inst.Zone, "/")+1:]
		}
		return fmt.Sprintf("%s.%s.c.%s.internal", inst.Name, zone, gcpMetadataProject(inst.SelfLink, defaultMetadataProject(r)))
	}
	return fmt.Sprintf("%s.%s.c.%s.internal", metadataDefaultName, defaultMetadataZone(r), defaultMetadataProject(r))
}

// metadataServiceAccountEmail resolves the account a request addressed to the
// email of a real identity. `default` is real GCE's alias for the instance's
// primary service account.
func metadataServiceAccountEmail(r *http.Request, account string) string {
	if account == "default" || account == "" {
		return fmt.Sprintf("default@%s.iam.gserviceaccount.com", defaultMetadataProject(r))
	}
	return account
}

func gcpMetadataInstanceForRequest(r *http.Request) (ComputeInstance, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	v, ok := gcpMetadataInstancesByIP.Load(host)
	if !ok {
		return ComputeInstance{}, false
	}
	inst, ok := v.(ComputeInstance)
	return inst, ok
}

func gcpMetadataProject(selfLink, defaultProject string) string {
	if project := gcpProjectFromSelfLink(selfLink); project != "" {
		return project
	}
	return defaultProject
}

func defaultMetadataProject(r *http.Request) string {
	if v := r.URL.Query().Get("project"); v != "" {
		return v
	}
	return "sim-project"
}

func defaultMetadataZone(r *http.Request) string {
	if v := r.URL.Query().Get("zone"); v != "" {
		return v
	}
	return "us-central1-a"
}

// simListenAddr is captured by main() so host translators can wire it
// into workload-host env. Workloads in Docker reach the sim host via
// host.docker.internal.
var simListenAddr string

// hostMetadataAddr returns the address workloads use to reach the sim's
// metadata service. Cloud-product translators inject this as
// GCE_METADATA_HOST on the workload host so the GCP SDKs route metadata
// reads here instead of attempting metadata.google.internal:80.
func hostMetadataAddr() string {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	return workloadCallbackHost() + ":" + port
}

func hostMetadataPort() (int, error) {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid simulator metadata listen port %q", port)
	}
	return n, nil
}

// hostMetadataExtraHosts returns ExtraHosts entries needed for the
// workload to resolve host.docker.internal AND metadata.google.internal
// to the sim's host gateway. Workloads that read GCE_METADATA_HOST will
// use the explicit address; workloads that hard-code metadata.google.internal
// will resolve it to host.docker.internal via /etc/hosts.
func hostMetadataExtraHosts() []string {
	if host := workloadCallbackHost(); host != "host.docker.internal" {
		return []string{
			"metadata.google.internal:" + host,
			"metadata:" + host,
		}
	}
	info := strings.ToLower(sim.RuntimeInfo())
	if strings.Contains(info, "podman") {
		// Podman exposes host.docker.internal natively.
		return []string{"metadata.google.internal:host.docker.internal"}
	}
	return []string{
		"host.docker.internal:host-gateway",
		"metadata.google.internal:host-gateway",
		"metadata:host-gateway",
	}
}

func workloadCallbackHost() string {
	if runningInsideContainer() {
		if host := firstNonLoopbackIPv4(); host != "" {
			return host
		}
	}
	return "host.docker.internal"
}

func runningInsideContainer() bool {
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("container") != ""
}

func firstNonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	return ""
}

// hostMetadataEnv returns env vars to inject on every GCP workload host
// so the GCP SDKs route metadata-server reads to the sim. Apply on every
// Cloud Run / Cloud Run Jobs / Cloud Functions / GCE-style workload host.
func hostMetadataEnv() map[string]string {
	addr := hostMetadataAddr()
	return map[string]string{
		"GCE_METADATA_HOST": addr,
		"GCE_METADATA_IP":   addr,
		"GCE_METADATA_ROOT": addr,
	}
}

// mergeEnv returns a new map with all keys from `base` and `extra`,
// where `extra` wins on conflict. Both inputs may be nil.
func mergeEnv(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
