package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

var gcpMetadataInstancesByIP workloadhost.MetadataIndex[ComputeInstance]

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
		writeText(w, gceMetadataWorkloadFor(r).projectID())
	})

	srv.HandleFunc("GET /computeMetadata/v1/project/numeric-project-id", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		number, ok := gceMetadataWorkloadFor(r).projectNumber()
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, number)
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
	// `default` is an alias for the account the workload runs as: the one its
	// Compute Engine instance or Cloud Run resource names, or else its
	// project's Compute Engine default service account. An account the
	// workload does not run as has no keys here.
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/email", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		email, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, email)
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/scopes", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		if _, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa")); !ok {
			metadataNotFound(w)
			return
		}
		// Cloud-platform broad scope is what real GCE returns by default.
		writeText(w, strings.Join(metadataServiceAccountScopes, "\n"))
	})
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/aliases", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		if _, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa")); !ok {
			metadataNotFound(w)
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
		sa, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		if !ok {
			metadataNotFound(w)
			return
		}
		now := time.Now()
		expires := now.Add(time.Hour)
		// Real GCE returns JSON: {access_token,expires_in,token_type}.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"access_token": signAccessToken(sa, now, expires),
			"expires_in":   int(time.Until(expires).Seconds()),
			"token_type":   "Bearer",
		})
	})

	// A workload reads its identity token here to invoke a sibling Cloud Run
	// service; the google.golang.org/api/idtoken compute source fetches it
	// verbatim.
	srv.HandleFunc("GET /computeMetadata/v1/instance/service-accounts/{sa}/identity", func(w http.ResponseWriter, r *http.Request) {
		if !mustFlavor(w, r) {
			return
		}
		audience := r.URL.Query().Get("audience")
		if audience == "" {
			http.Error(w, "non-empty audience parameter required", http.StatusBadRequest)
			return
		}
		sa, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		if !ok {
			metadataNotFound(w)
			return
		}
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

// gceMetadataProjectChildren lists the workload's project. Its number comes
// from Cloud Resource Manager, and recursive JSON carries it as a number.
func gceMetadataProjectChildren(r *http.Request) []gceMetadataEntry {
	workload := gceMetadataWorkloadFor(r)
	var children []gceMetadataEntry
	if number, ok := workload.projectNumber(); ok {
		n, err := strconv.ParseInt(number, 10, 64)
		if err == nil {
			children = append(children, gceMetadataEntry{name: "numeric-project-id", value: func(*http.Request) any { return n }})
		}
	}
	return append(children, gceMetadataEntry{name: "project-id", value: func(*http.Request) any { return workload.projectID() }})
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
	email, ok := metadataServiceAccountEmail(r, "default")
	if !ok {
		return []gceMetadataEntry{}
	}
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
		{name: "email", value: func(r *http.Request) any {
			email, _ := metadataServiceAccountEmail(r, account)
			return email
		}},
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
// instance id as a number in recursive JSON, so it is held as an integer and
// formatted only for the text leaf.
const (
	metadataInstanceID  int64 = 1000000000001
	metadataDefaultName       = "sim-instance-1"
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

// metadataInstanceZone is the fully-qualified zone the reading workload runs in.
func metadataInstanceZone(r *http.Request) string {
	if inst, ok := gcpMetadataInstancesByIP.ForRequest(r); ok && inst.Zone != "" {
		return inst.Zone
	}
	number, ok := gceMetadataWorkloadFor(r).projectNumber()
	if !ok {
		number = gceMetadataWorkloadFor(r).projectID()
	}
	return fmt.Sprintf("projects/%s/zones/%s", number, defaultMetadataZone(r))
}

// metadataInstanceName is the reading workload's instance name.
func metadataInstanceName(r *http.Request) string {
	if inst, ok := gcpMetadataInstancesByIP.ForRequest(r); ok && inst.Name != "" {
		return inst.Name
	}
	return metadataDefaultName
}

// metadataInstanceHostname is the internal DNS name of the reading workload.
func metadataInstanceHostname(r *http.Request) string {
	if inst, ok := gcpMetadataInstancesByIP.ForRequest(r); ok && inst.Name != "" {
		zone := defaultMetadataZone(r)
		if inst.Zone != "" {
			zone = inst.Zone[strings.LastIndex(inst.Zone, "/")+1:]
		}
		return fmt.Sprintf("%s.%s.c.%s.internal", inst.Name, zone, gceMetadataWorkloadFor(r).projectID())
	}
	return fmt.Sprintf("%s.%s.c.%s.internal", metadataDefaultName, defaultMetadataZone(r), gceMetadataWorkloadFor(r).projectID())
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

func defaultMetadataZone(r *http.Request) string {
	if v := r.URL.Query().Get("zone"); v != "" {
		return v
	}
	return "us-central1-a"
}

// simListenAddr is the listen address main() serves on.
var simListenAddr string

// hostMetadataExtraHosts resolves the outer host's aliases and the metadata
// server's names, metadata.google.internal and metadata, for a workload that
// dials them directly instead of reading GCE_METADATA_HOST.
func hostMetadataExtraHosts() ([]string, error) {
	aliases, err := workloadhost.AliasHosts("metadata.google.internal", "metadata")
	if err != nil {
		return nil, err
	}
	return append(workloadhost.ExtraHosts(), aliases...), nil
}

// hostMetadataEnv returns env vars to inject on every GCP workload host
// so the GCP SDKs route metadata-server reads to the sim. Apply on every
// Cloud Run / Cloud Run Jobs / Cloud Functions / GCE-style workload host.
func hostMetadataEnv() (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"GCE_METADATA_HOST": addr,
		"GCE_METADATA_IP":   addr,
		"GCE_METADATA_ROOT": addr,
	}, nil
}
