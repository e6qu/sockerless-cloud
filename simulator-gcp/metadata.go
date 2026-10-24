package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

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
	handle := func(pattern string, h http.HandlerFunc) {
		srv.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r, ok := gceMetadataAdmit(w, r); ok {
				gceMetadataServe(w, r, h)
			}
		})
	}
	writeText := func(w http.ResponseWriter, s string) {
		w.Header().Set("Content-Type", "application/text")
		_, _ = w.Write([]byte(s))
	}

	// /computeMetadata/v1/project/project-id
	handle("GET /computeMetadata/v1/project/project-id", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, gceMetadataWorkloadFor(r).projectID())
	})

	handle("GET /computeMetadata/v1/project/numeric-project-id", func(w http.ResponseWriter, r *http.Request) {
		number, ok := gceMetadataWorkloadFor(r).projectNumber()
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, number)
	})

	// /computeMetadata/v1/instance/{id|zone|name|hostname}
	handle("GET /computeMetadata/v1/instance/id", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, fmt.Sprint(metadataInstanceIDFor(r)))
	})
	handle("GET /computeMetadata/v1/instance/region", func(w http.ResponseWriter, r *http.Request) {
		region, ok := metadataInstanceRegion(r)
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, region)
	})
	handle("GET /computeMetadata/v1/instance/attributes/{key}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := gcpMetadataInstanceFor(r)
		if !ok {
			metadataNotFound(w)
			return
		}
		value, ok := gcpInstanceMetadataValue(inst, sim.PathParam(r, "key"))
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, value)
	})
	handle("GET /computeMetadata/v1/project/attributes/{key}", func(w http.ResponseWriter, r *http.Request) {
		value, ok := gceProjectAttributes(gceMetadataWorkloadFor(r).projectID())[sim.PathParam(r, "key")]
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, value)
	})
	handle("GET /computeMetadata/v1/instance/network-interfaces/{nic}/{key}", func(w http.ResponseWriter, r *http.Request) {
		keys, ok := gceInterfaceKeys(r, sim.PathParam(r, "nic"))
		if !ok {
			metadataNotFound(w)
			return
		}
		value, ok := keys[sim.PathParam(r, "key")]
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, value)
	})
	handle("GET /computeMetadata/v1/instance/network-interfaces/{nic}/access-configs/{config}/{key}", func(w http.ResponseWriter, r *http.Request) {
		keys, ok := gceAccessConfigKeys(r, sim.PathParam(r, "nic"), sim.PathParam(r, "config"))
		if !ok {
			metadataNotFound(w)
			return
		}
		value, ok := keys[sim.PathParam(r, "key")]
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, value)
	})
	handle("GET /computeMetadata/v1/instance/zone", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, metadataInstanceZone(r))
	})
	handle("GET /computeMetadata/v1/instance/name", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, metadataInstanceName(r))
	})
	handle("GET /computeMetadata/v1/instance/hostname", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, metadataInstanceHostname(r))
	})

	// /computeMetadata/v1/universe/universe-domain — the API host suffix a
	// client builds every Google endpoint from. Google auth libraries and the
	// `gcloud` CLI read it before choosing an endpoint.
	handle("GET /computeMetadata/v1/universe/universe-domain", func(w http.ResponseWriter, r *http.Request) {
		writeText(w, metadataUniverseDomain)
	})

	// /computeMetadata/v1/instance/service-accounts/{sa}/{leaf}
	//
	// `default` is an alias for the account the workload runs as: the one its
	// Compute Engine instance or Cloud Run resource names, or else its
	// project's Compute Engine default service account. An account the
	// workload does not run as has no keys here.
	handle("GET /computeMetadata/v1/instance/service-accounts/{sa}/email", func(w http.ResponseWriter, r *http.Request) {
		email, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa"))
		if !ok {
			metadataNotFound(w)
			return
		}
		writeText(w, email)
	})
	handle("GET /computeMetadata/v1/instance/service-accounts/{sa}/scopes", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := metadataServiceAccountEmail(r, sim.PathParam(r, "sa")); !ok {
			metadataNotFound(w)
			return
		}
		// Cloud-platform broad scope is what real GCE returns by default.
		writeText(w, strings.Join(metadataServiceAccountScopes, "\n"))
	})
	handle("GET /computeMetadata/v1/instance/service-accounts/{sa}/aliases", func(w http.ResponseWriter, r *http.Request) {
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
	handle("GET /computeMetadata/v1/instance/service-accounts/{sa}/token", func(w http.ResponseWriter, r *http.Request) {
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
	handle("GET /computeMetadata/v1/instance/service-accounts/{sa}/identity", func(w http.ResponseWriter, r *http.Request) {
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

	registerComputeMetadataDirectories(srv, handle)
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
	children = append(children, gceMetadataEntry{name: "attributes", children: gceMetadataProjectAttributesChildren})
	return append(children, gceMetadataEntry{name: "project-id", value: func(*http.Request) any { return workload.projectID() }})
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
		value := attributes[key]
		children = append(children, gceMetadataEntry{name: key, jsonName: key, value: func(*http.Request) any { return value }})
	}
	return children
}

// gceInterfaceKeys are the keys of the reading instance's interface nic.
func gceInterfaceKeys(r *http.Request, nic string) (map[string]string, bool) {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok {
		return nil, false
	}
	index, err := strconv.Atoi(nic)
	if err != nil || index < 0 || index >= len(inst.NetworkInterfaces) {
		return nil, false
	}
	iface := inst.NetworkInterfaces[index]
	keys := map[string]string{
		"ip":  iface.NetworkIP,
		"mac": gceInterfaceMAC(iface.NetworkIP),
		"mtu": strconv.Itoa(gceNetworkMTU),
	}
	if number, ok := gceMetadataWorkloadFor(r).projectNumber(); ok {
		keys["network"] = "projects/" + number + "/networks/" + iface.Network[strings.LastIndex(iface.Network, "/")+1:]
	}
	if gcpSubnetworks != nil {
		if subnet, ok := gcpSubnetworks.Get(iface.Subnetwork); ok {
			keys["gateway"] = subnet.GatewayAddress
			if prefix, err := netip.ParsePrefix(subnet.IpCidrRange); err == nil {
				mask := net.CIDRMask(prefix.Bits(), 32)
				keys["subnetmask"] = net.IP(mask).String()
			}
		}
	}
	if booted, ok := gcpInstanceBootedAs(inst.SelfLink); ok && index == 0 {
		keys["dns-servers"] = strings.Join(booted.dns, "\n")
	}
	return keys, true
}

// gceAccessConfigKeys are the keys of one access config of the reading
// instance's interface nic: its external address and type.
func gceAccessConfigKeys(r *http.Request, nic, config string) (map[string]string, bool) {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok {
		return nil, false
	}
	index, err := strconv.Atoi(nic)
	if err != nil || index < 0 || index >= len(inst.NetworkInterfaces) {
		return nil, false
	}
	configs := inst.NetworkInterfaces[index].AccessConfigs
	at, err := strconv.Atoi(config)
	if err != nil || at < 0 || at >= len(configs) {
		return nil, false
	}
	return map[string]string{"external-ip": configs[at].NatIP, "type": configs[at].Type}, true
}

func gceKeyEntries(keys map[string]string) []gceMetadataEntry {
	names := make([]string, 0, len(keys))
	for name := range keys {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]gceMetadataEntry, 0, len(names))
	for _, name := range names {
		value := keys[name]
		entries = append(entries, gceMetadataEntry{name: name, value: func(*http.Request) any { return value }})
	}
	return entries
}

// gceMetadataInterfacesChildren lists the reading instance's interfaces by
// index.
func gceMetadataInterfacesChildren(r *http.Request) []gceMetadataEntry {
	inst, ok := gcpMetadataInstanceFor(r)
	if !ok {
		return []gceMetadataEntry{}
	}
	children := make([]gceMetadataEntry, 0, len(inst.NetworkInterfaces))
	for i := range inst.NetworkInterfaces {
		nic := strconv.Itoa(i)
		children = append(children, gceMetadataEntry{name: nic, jsonName: nic, children: func(r *http.Request) []gceMetadataEntry {
			return gceMetadataInterfaceChildren(r, nic)
		}})
	}
	return children
}

func gceMetadataInterfaceChildren(r *http.Request, nic string) []gceMetadataEntry {
	keys, ok := gceInterfaceKeys(r, nic)
	if !ok {
		return []gceMetadataEntry{}
	}
	entries := gceKeyEntries(keys)
	inst, _ := gcpMetadataInstanceFor(r)
	index, _ := strconv.Atoi(nic)
	if configs := inst.NetworkInterfaces[index].AccessConfigs; len(configs) > 0 {
		entries = append(entries, gceMetadataEntry{name: "access-configs", children: func(r *http.Request) []gceMetadataEntry {
			out := make([]gceMetadataEntry, 0, len(configs))
			for i := range configs {
				config := strconv.Itoa(i)
				out = append(out, gceMetadataEntry{name: config, jsonName: config, children: func(r *http.Request) []gceMetadataEntry {
					keys, _ := gceAccessConfigKeys(r, nic, config)
					return gceKeyEntries(keys)
				}})
			}
			return out
		}})
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	}
	return entries
}

func gceMetadataInstanceChildren(r *http.Request) []gceMetadataEntry {
	entries := []gceMetadataEntry{
		{name: "attributes", children: gceMetadataAttributesChildren},
		{name: "hostname", value: func(r *http.Request) any { return metadataInstanceHostname(r) }},
		{name: "id", value: func(r *http.Request) any { return metadataInstanceIDFor(r) }},
		{name: "name", value: func(r *http.Request) any { return metadataInstanceName(r) }},
		{name: "network-interfaces", children: gceMetadataInterfacesChildren},
	}
	if region, ok := metadataInstanceRegion(r); ok {
		entries = append(entries, gceMetadataEntry{name: "region", value: func(*http.Request) any { return region }})
	}
	return append(entries,
		gceMetadataEntry{name: "service-accounts", children: gceMetadataServiceAccountsChildren},
		gceMetadataEntry{name: "zone", value: func(r *http.Request) any { return metadataInstanceZone(r) }},
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
		value := item.Value
		children = append(children, gceMetadataEntry{name: item.Key, jsonName: item.Key, value: func(*http.Request) any { return value }})
	}
	return children
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
func registerComputeMetadataDirectories(srv *sim.Server, handle func(string, http.HandlerFunc)) {
	dir := func(pattern string, children func(*http.Request) []gceMetadataEntry) {
		handle("GET "+pattern+"{$}", func(w http.ResponseWriter, r *http.Request) {
			writeMetadataDirectory(w, r, children(r))
		})
		// The same node addressed without its trailing slash.
		handle("GET "+strings.TrimSuffix(pattern, "/"), func(w http.ResponseWriter, r *http.Request) {
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
	//
	// The wrapper runs ahead of the chain that attributes a request a sidecar
	// forwarded to its workload, so it attributes the probe itself.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return sim.SidecarTrust(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/" || r.Header.Get("Metadata-Flavor") != "Google" {
				next.ServeHTTP(w, r)
				return
			}
			// A caller that is no workload reaches no metadata server, only
			// the origin's own root.
			if _, ok := gceMetadataCaller(r); !ok {
				next.ServeHTTP(w, r)
				return
			}
			if r, ok := gceMetadataAdmit(w, r); ok {
				writeMetadataDirectory(w, r, gceMetadataRootChildren(r))
			}
		}))
	})

	dir("/computeMetadata/", gceMetadataVersions)
	dir("/computeMetadata/v1/", gceMetadataV1Children)
	dir("/computeMetadata/v1/instance/", gceMetadataInstanceChildren)
	dir("/computeMetadata/v1/instance/attributes/", gceMetadataAttributesChildren)
	dir("/computeMetadata/v1/project/", gceMetadataProjectChildren)
	dir("/computeMetadata/v1/project/attributes/", gceMetadataProjectAttributesChildren)
	dir("/computeMetadata/v1/instance/network-interfaces/", gceMetadataInterfacesChildren)
	dir("/computeMetadata/v1/instance/network-interfaces/{nic}/", func(r *http.Request) []gceMetadataEntry {
		return gceMetadataInterfaceChildren(r, sim.PathParam(r, "nic"))
	})
	dir("/computeMetadata/v1/instance/network-interfaces/{nic}/access-configs/", func(r *http.Request) []gceMetadataEntry {
		for _, entry := range gceMetadataInterfaceChildren(r, sim.PathParam(r, "nic")) {
			if entry.name == "access-configs" {
				return entry.children(r)
			}
		}
		return []gceMetadataEntry{}
	})
	dir("/computeMetadata/v1/instance/network-interfaces/{nic}/access-configs/{config}/", func(r *http.Request) []gceMetadataEntry {
		keys, _ := gceAccessConfigKeys(r, sim.PathParam(r, "nic"), sim.PathParam(r, "config"))
		return gceKeyEntries(keys)
	})
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

// metadataInstanceIDFor is the id of the reading instance: a Compute Engine
// instance's number, a Cloud Run instance's own identifier, or the number
// every other workload reports.
func metadataInstanceIDFor(r *http.Request) any {
	if w := gceMetadataWorkloadFor(r); w.cloudRunInstance != "" {
		return w.cloudRunInstance
	}
	if inst, ok := gcpMetadataInstanceFor(r); ok && inst.Id != "" {
		if id, err := strconv.ParseInt(inst.Id, 10, 64); err == nil {
			return id
		}
	}
	return metadataInstanceID
}

// metadataInstanceRegion is the fully-qualified region a Cloud Run instance
// runs in; a Compute Engine instance reports its zone instead.
func metadataInstanceRegion(r *http.Request) (string, bool) {
	w := gceMetadataWorkloadFor(r)
	if w.cloudRunInstance == "" || w.region == "" {
		return "", false
	}
	number, ok := w.projectNumber()
	if !ok {
		return "", false
	}
	return fmt.Sprintf("projects/%s/regions/%s", number, w.region), true
}

// metadataInstanceZone is the fully-qualified zone the reading workload runs in.
func metadataInstanceZone(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceFor(r); ok && inst.Zone != "" {
		if number, ok := gceMetadataWorkloadFor(r).projectNumber(); ok {
			return fmt.Sprintf("projects/%s/zones/%s", number, inst.Zone[strings.LastIndex(inst.Zone, "/")+1:])
		}
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
	if inst, ok := gcpMetadataInstanceFor(r); ok && inst.Name != "" {
		return inst.Name
	}
	return metadataDefaultName
}

// metadataInstanceHostname is the internal DNS name of the reading workload.
func metadataInstanceHostname(r *http.Request) string {
	if inst, ok := gcpMetadataInstanceFor(r); ok && inst.Name != "" {
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

// simListenAddr is the API listen address main() serves on, and
// simCallbackListenAddr the plain-HTTP one the workloads call back on; the
// two differ when the API listener terminates TLS.
var simListenAddr, simCallbackListenAddr string
