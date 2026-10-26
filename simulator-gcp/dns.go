package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/miekg/dns"
)

// dnsChangeMu serializes Changes.create: Cloud DNS applies a zone's changes
// atomically and in sequence, so two concurrent creates must not read the same
// highest change ID or interleave record-set writes.
var dnsChangeMu sync.Mutex

// ManagedZone represents a Cloud DNS managed zone.
type ManagedZone struct {
	Name                    string            `json:"name"`
	DNSName                 string            `json:"dnsName"`
	Description             string            `json:"description,omitempty"`
	ID                      string            `json:"id,omitempty"`
	Visibility              string            `json:"visibility,omitempty"`
	PrivateVisibilityConfig map[string]any    `json:"privateVisibilityConfig,omitempty"`
	Labels                  map[string]string `json:"labels,omitempty"`
	NameServers             []string          `json:"nameServers,omitempty"`
	// Nested writable configs the simulator does not interpret, stored verbatim so
	// terraform-provider-google reads back what it wrote.
	DnssecConfig       json.RawMessage `json:"dnssecConfig,omitempty"`
	ForwardingConfig   json.RawMessage `json:"forwardingConfig,omitempty"`
	PeeringConfig      json.RawMessage `json:"peeringConfig,omitempty"`
	CloudLoggingConfig json.RawMessage `json:"cloudLoggingConfig,omitempty"`
}

// storedManagedZone is the persisted row backing a managed zone. It carries
// the wire-shape ManagedZone alone: a private zone is visible to the networks
// its privateVisibilityConfig names, which the resolver's catalog reads per
// querying network.
type storedManagedZone struct {
	ManagedZone
}

// pruneEmptyPrivateVisibilityConfig returns nil for a config with no networks
// and no GKE clusters, which Cloud DNS omits from a read.
func pruneEmptyPrivateVisibilityConfig(pvc map[string]any) map[string]any {
	if len(pvc) == 0 {
		return nil
	}
	hasItems := func(key string) bool {
		v, ok := pvc[key]
		if !ok {
			return false
		}
		s, ok := v.([]any)
		return ok && len(s) > 0
	}
	if hasItems("networks") || hasItems("gkeClusters") {
		return pvc
	}
	return nil
}

type ResourceRecordSet struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	TTL     int      `json:"ttl"`
	Rrdatas []string `json:"rrdatas"`
}

type storedResourceRecordSet struct {
	Project string `json:"project"`
	Zone    string `json:"zone"`
	Record  ResourceRecordSet
}

type DNSChange struct {
	Additions []ResourceRecordSet `json:"additions,omitempty"`
	Deletions []ResourceRecordSet `json:"deletions,omitempty"`
	StartTime string              `json:"startTime,omitempty"`
	ID        string              `json:"id,omitempty"`
	Status    string              `json:"status,omitempty"`
	IsServing bool                `json:"isServing,omitempty"`
	Kind      string              `json:"kind,omitempty"`
}

type storedDNSChange struct {
	Project string    `json:"project"`
	Zone    string    `json:"zone"`
	Change  DNSChange `json:"change"`
}

// DNSPolicy mirrors the Cloud DNS Policy resource: inbound and outbound
// forwarding and alternative name servers for VPC networks. Nested configs the
// simulator does not interpret are stored verbatim.
type DNSPolicy struct {
	ID                          string            `json:"id,omitempty"`
	Name                        string            `json:"name"`
	EnableInboundForwarding     bool              `json:"enableInboundForwarding,omitempty"`
	Description                 string            `json:"description,omitempty"`
	Networks                    []json.RawMessage `json:"networks,omitempty"`
	AlternativeNameServerConfig json.RawMessage   `json:"alternativeNameServerConfig,omitempty"`
	EnableLogging               bool              `json:"enableLogging,omitempty"`
	Dns64Config                 json.RawMessage   `json:"dns64Config,omitempty"`
	Kind                        string            `json:"kind,omitempty"`
}

type storedDNSPolicy struct {
	Project string    `json:"project"`
	Policy  DNSPolicy `json:"policy"`
}

// DNSResponsePolicy mirrors the Cloud DNS ResponsePolicy resource.
type DNSResponsePolicy struct {
	ID                 string            `json:"id,omitempty"`
	ResponsePolicyName string            `json:"responsePolicyName"`
	Description        string            `json:"description,omitempty"`
	Networks           []json.RawMessage `json:"networks,omitempty"`
	GKEClusters        []json.RawMessage `json:"gkeClusters,omitempty"`
	Labels             map[string]string `json:"labels,omitempty"`
	Kind               string            `json:"kind,omitempty"`
}

type storedDNSResponsePolicy struct {
	Project string            `json:"project"`
	Policy  DNSResponsePolicy `json:"policy"`
}

// DNSResponsePolicyRule mirrors the Cloud DNS ResponsePolicyRule resource.
type DNSResponsePolicyRule struct {
	RuleName  string          `json:"ruleName"`
	DNSName   string          `json:"dnsName,omitempty"`
	LocalData json.RawMessage `json:"localData,omitempty"`
	Behavior  string          `json:"behavior,omitempty"`
	Kind      string          `json:"kind,omitempty"`
}

type storedDNSResponsePolicyRule struct {
	Project        string                `json:"project"`
	ResponsePolicy string                `json:"responsePolicy"`
	Rule           DNSResponsePolicyRule `json:"rule"`
}

// DNSOperation mirrors the Cloud DNS Operation resource, the record Cloud DNS
// keeps of each managed-zone create, update, patch and delete, which
// managedZoneOperations.get and .list read back.
type DNSOperation struct {
	ID          string                   `json:"id,omitempty"`
	StartTime   string                   `json:"startTime,omitempty"`
	Status      string                   `json:"status,omitempty"`
	User        string                   `json:"user,omitempty"`
	Type        string                   `json:"type,omitempty"`
	ZoneContext *dnsOperationZoneContext `json:"zoneContext,omitempty"`
	Kind        string                   `json:"kind,omitempty"`
}

type dnsOperationZoneContext struct {
	OldValue *ManagedZone `json:"oldValue,omitempty"`
	NewValue *ManagedZone `json:"newValue,omitempty"`
}

type storedDNSOperation struct {
	Project   string       `json:"project"`
	Zone      string       `json:"zone"`
	Seq       int          `json:"seq"`
	Operation DNSOperation `json:"operation"`
}

func registerCloudDNS(srv *sim.Server) {
	zones := sim.MakeStore[storedManagedZone](srv.DB(), "dns_zones")
	recordSets := sim.MakeStore[storedResourceRecordSet](srv.DB(), "dns_record_sets")
	changes := sim.MakeStore[storedDNSChange](srv.DB(), "dns_changes")
	policies := sim.MakeStore[storedDNSPolicy](srv.DB(), "dns_policies")
	responsePolicies := sim.MakeStore[storedDNSResponsePolicy](srv.DB(), "dns_response_policies")
	responsePolicyRules := sim.MakeStore[storedDNSResponsePolicyRule](srv.DB(), "dns_response_policy_rules")
	operations := sim.MakeStore[storedDNSOperation](srv.DB(), "dns_zone_operations")
	cloudDNSZones, cloudDNSRecordSets = zones, recordSets
	cloudDNSPolicies, cloudDNSResponsePolicies, cloudDNSResponsePolicyRules = policies, responsePolicies, responsePolicyRules

	// Cloud DNS outbound endpoints and the locations and long-running
	// operations they are created through: no vendored source describes what
	// an outbound endpoint forwards or how its operations progress.
	const outboundWhy = "no vendored source describes an outbound endpoint's forwarding behaviour or its operations' progression"
	dnsUnserved := func(what string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no %s: %s", what, outboundWhy)
		}
	}
	const loc = "/dns/v1/projects/{project}/locations"
	srv.HandleFunc("GET "+loc, dnsUnserved("locations.list"))
	srv.HandleFunc("GET "+loc+"/{location}", dnsUnserved("locations.get"))
	srv.HandleFunc("GET "+loc+"/{location}/outboundEndpoints", dnsUnserved("outboundEndpoints.list"))
	srv.HandleFunc("POST "+loc+"/{location}/outboundEndpoints", dnsUnserved("outboundEndpoints.create"))
	srv.HandleFunc("GET "+loc+"/{location}/outboundEndpoints/{endpoint}", dnsUnserved("outboundEndpoints.get"))
	srv.HandleFunc("PATCH "+loc+"/{location}/outboundEndpoints/{endpoint}", dnsUnserved("outboundEndpoints.patch"))
	srv.HandleFunc("DELETE "+loc+"/{location}/outboundEndpoints/{endpoint}", dnsUnserved("outboundEndpoints.delete"))
	srv.HandleFunc("GET "+loc+"/{location}/operations", dnsUnserved("operations.list"))
	srv.HandleFunc("GET "+loc+"/{location}/operations/{operation}", dnsUnserved("operations.get"))
	srv.HandleFunc("DELETE "+loc+"/{location}/operations/{operation}", dnsUnserved("operations.delete"))
	srv.HandleFunc("POST "+loc+"/{location}/operations/{opverb}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.PathValue("opverb"), ":cancel") {
			http.NotFound(w, r)
			return
		}
		dnsUnserved("operations.cancel")(w, r)
	})

	srv.HandleFunc("POST /dns/v1/projects/{project}/managedZones", func(w http.ResponseWriter, r *http.Request) {
		var zone ManagedZone
		if err := sim.ReadJSON(r, &zone); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		if zone.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		if zone.DNSName == "" {
			GCPError(w, http.StatusBadRequest, "dnsName is required", "INVALID_ARGUMENT")
			return
		}

		project := sim.PathParam(r, "project")
		key := project + "/" + zone.Name

		if _, exists := zones.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "managed zone %q already exists", zone.Name)
			return
		}

		if zone.ID == "" {
			// Cloud DNS zone IDs are numeric uint64s.
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			zone.ID = fmt.Sprintf("%d", binary.BigEndian.Uint64(b)>>1)
		}
		if zone.Visibility == "" {
			zone.Visibility = "public"
		}
		zone.NameServers = cloudDNSNameServers(zone)
		// terraform-provider-google sends an empty privateVisibilityConfig even for
		// public zones, and Cloud DNS drops it from the read; echoing it makes the
		// provider plan a phantom block on every refresh.
		zone.PrivateVisibilityConfig = pruneEmptyPrivateVisibilityConfig(zone.PrivateVisibilityConfig)

		stored := storedManagedZone{ManagedZone: zone}
		zones.Put(key, stored)
		for _, rs := range cloudDNSApexRecordSets(zone) {
			recordSets.Put(dnsRecordSetKey(project, zone.Name, rs.Name, rs.Type), storedResourceRecordSet{Project: project, Zone: zone.Name, Record: rs})
		}
		recordDNSZoneOperation(operations, project, zone.Name, "insert", nil, &stored.ManagedZone)
		sim.WriteJSON(w, http.StatusOK, stored.ManagedZone)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := project + "/"

		stored := zones.Filter(func(z storedManagedZone) bool {
			key := project + "/" + z.Name
			return strings.HasPrefix(key, prefix)
		})
		items := make([]ManagedZone, 0, len(stored))
		for _, z := range stored {
			items = append(items, z.ManagedZone)
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"managedZones": items,
		})
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		key := project + "/" + zoneName

		zone, ok := zones.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, zone.ManagedZone)
	})

	srv.HandleFunc("DELETE /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		key := project + "/" + zoneName

		zone, ok := zones.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		// Cloud DNS deletes only a zone left with its apex NS and SOA.
		apex := dns.Fqdn(zone.DNSName)
		for _, stored := range recordSets.ListPrefix(key + ":") {
			rs := stored.Item.Record
			if dns.CanonicalName(rs.Name) == dns.CanonicalName(apex) && (rs.Type == "NS" || rs.Type == "SOA") {
				continue
			}
			writeDNSReasonError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "containerNotEmpty",
				fmt.Sprintf("The resource named '%s' cannot be deleted because it is not empty", zoneName))
			return
		}
		zones.Delete(key)
		// The zone's IAM policy dies with the zone: a later zone created
		// under the same name starts with no bindings.
		gcpResourceIAMStore().Delete("dnsManagedZone/" + project + "/" + zoneName)
		oldZone := zone.ManagedZone
		recordDNSZoneOperation(operations, project, zoneName, "delete", &oldZone, nil)

		for _, stored := range recordSets.List() {
			if stored.Project == project && stored.Zone == zoneName {
				recordSets.Delete(dnsRecordSetKey(project, zoneName, stored.Record.Name, stored.Record.Type))
			}
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zoneKey := project + "/" + zoneName

		if _, ok := zones.Get(zoneKey); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}

		// rrsets.list filters on the optional name and type query parameters.
		nameFilter := r.URL.Query().Get("name")
		typeFilter := r.URL.Query().Get("type")
		var filtered []ResourceRecordSet
		for _, stored := range recordSets.List() {
			if stored.Project != project || stored.Zone != zoneName {
				continue
			}
			if nameFilter != "" && stored.Record.Name != nameFilter {
				continue
			}
			if typeFilter != "" && stored.Record.Type != typeFilter {
				continue
			}
			filtered = append(filtered, stored.Record)
		}
		if filtered == nil {
			filtered = []ResourceRecordSet{}
		}
		sort.Slice(filtered, func(i, j int) bool {
			if filtered[i].Name != filtered[j].Name {
				return filtered[i].Name < filtered[j].Name
			}
			return filtered[i].Type < filtered[j].Type
		})

		page, next, ok := paginateListCompute(w, r, filtered, gcpAsc(func(s ResourceRecordSet) string { return s.Name }), gcpAsc(func(s ResourceRecordSet) string { return s.Type }))
		if !ok {
			return
		}
		resp := map[string]any{
			"kind":   "dns#resourceRecordSetsListResponse",
			"rrsets": page,
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("POST /dns/v1/projects/{project}/managedZones/{zone}/rrsets", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zoneKey := project + "/" + zoneName

		if _, ok := zones.Get(zoneKey); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}

		var rs ResourceRecordSet
		if err := sim.ReadJSON(r, &rs); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		if rs.Name == "" || rs.Type == "" {
			GCPError(w, http.StatusBadRequest, "name and type are required", "INVALID_ARGUMENT")
			return
		}

		key := dnsRecordSetKey(project, zoneName, rs.Name, rs.Type)
		if _, exists := recordSets.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "record set %s/%s already exists", rs.Name, rs.Type)
			return
		}

		recordSets.Put(key, storedResourceRecordSet{Project: project, Zone: zoneName, Record: rs})
		sim.WriteJSON(w, http.StatusOK, rs)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		rrName := sim.PathParam(r, "name")
		rrType := sim.PathParam(r, "type")
		if _, ok := zones.Get(project + "/" + zoneName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		stored, ok := recordSets.Get(dnsRecordSetKey(project, zoneName, rrName, rrType))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "record set %s/%s not found", rrName, rrType)
			return
		}
		sim.WriteJSON(w, http.StatusOK, stored.Record)
	})

	srv.HandleFunc("DELETE /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		rrName := sim.PathParam(r, "name")
		rrType := sim.PathParam(r, "type")
		key := dnsRecordSetKey(project, zoneName, rrName, rrType)

		if !recordSets.Delete(key) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "record set %s/%s not found", rrName, rrType)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	srv.HandleFunc("PATCH /dns/v1/projects/{project}/managedZones/{zone}/rrsets/{name}/{type}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		rrName := sim.PathParam(r, "name")
		rrType := sim.PathParam(r, "type")
		zoneKey := project + "/" + zoneName
		if _, ok := zones.Get(zoneKey); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}

		var patch ResourceRecordSet
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		key := dnsRecordSetKey(project, zoneName, rrName, rrType)
		stored, ok := recordSets.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "record set %s/%s not found", rrName, rrType)
			return
		}
		updated := stored.Record
		if patch.Name != "" {
			updated.Name = patch.Name
		}
		if patch.Type != "" {
			updated.Type = patch.Type
		}
		if patch.TTL != 0 {
			updated.TTL = patch.TTL
		}
		if patch.Rrdatas != nil {
			updated.Rrdatas = patch.Rrdatas
		}
		recordSets.Delete(key)
		recordSets.Put(dnsRecordSetKey(project, zoneName, updated.Name, updated.Type),
			storedResourceRecordSet{Project: project, Zone: zoneName, Record: updated})
		sim.WriteJSON(w, http.StatusOK, updated)
	})

	srv.HandleFunc("POST /dns/v1/projects/{project}/managedZones/{zone}/changes", func(w http.ResponseWriter, r *http.Request) {
		dnsChangeMu.Lock()
		defer dnsChangeMu.Unlock()
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zoneKey := project + "/" + zoneName
		if _, ok := zones.Get(zoneKey); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}

		var change DNSChange
		if err := sim.ReadJSON(r, &change); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, deletion := range change.Deletions {
			key := dnsRecordSetKey(project, zoneName, deletion.Name, deletion.Type)
			stored, ok := recordSets.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "record set %s/%s not found", deletion.Name, deletion.Type)
				return
			}
			if !dnsRecordSetsEqual(stored.Record, deletion) {
				writeDNSChangeError(w, http.StatusPreconditionFailed, "FAILED_PRECONDITION",
					fmt.Sprintf("record set %s/%s does not match existing data", deletion.Name, deletion.Type))
				return
			}
		}
		for _, addition := range change.Additions {
			if addition.Name == "" || addition.Type == "" {
				GCPError(w, http.StatusBadRequest, "name and type are required", "INVALID_ARGUMENT")
				return
			}
			if _, exists := recordSets.Get(dnsRecordSetKey(project, zoneName, addition.Name, addition.Type)); exists &&
				!dnsChangeDeletesRecord(change, addition) {
				GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "record set %s/%s already exists", addition.Name, addition.Type)
				return
			}
		}

		for _, deletion := range change.Deletions {
			key := dnsRecordSetKey(project, zoneName, deletion.Name, deletion.Type)
			recordSets.Delete(key)
		}
		for _, addition := range change.Additions {
			recordSets.Put(dnsRecordSetKey(project, zoneName, addition.Name, addition.Type),
				storedResourceRecordSet{Project: project, Zone: zoneName, Record: addition})
		}

		change.ID = nextDNSChangeID(changes, project, zoneName)
		change.StartTime = nowTimestamp()
		change.Status = "done"
		change.IsServing = true
		change.Kind = "dns#change"
		changes.Put(dnsChangeKey(project, zoneName, change.ID),
			storedDNSChange{Project: project, Zone: zoneName, Change: change})
		sim.WriteJSON(w, http.StatusOK, change)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/changes/{change}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		id := sim.PathParam(r, "change")
		if _, ok := zones.Get(project + "/" + zoneName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		stored, ok := changes.Get(dnsChangeKey(project, zoneName, id))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "change %q not found", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, stored.Change)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/changes", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		if _, ok := zones.Get(project + "/" + zoneName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		var out []DNSChange
		for _, stored := range changes.List() {
			if stored.Project == project && stored.Zone == zoneName {
				out = append(out, stored.Change)
			}
		}
		// Cloud DNS changes.list sorts by change ID, ascending unless sortOrder says
		// descending.
		byID := gcpAscNumber(func(c DNSChange) string { return c.ID })
		if r.URL.Query().Get("sortOrder") == "descending" {
			byID = gcpDescNumber(func(c DNSChange) string { return c.ID })
		}
		page, next, ok := paginateListCompute(w, r, out, byID)
		if !ok {
			return
		}
		if page == nil {
			page = []DNSChange{}
		}
		resp := map[string]any{
			"kind":    "dns#changesListResponse",
			"changes": page,
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Patch merges the mutable fields of a managed zone.
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		dnsManagedZoneUpdate(w, r, zones, operations, false)
	})

	// Update replaces the mutable fields of a managed zone.
	srv.HandleFunc("PUT /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		dnsManagedZoneUpdate(w, r, zones, operations, true)
	})

	// Cloud DNS spells the AIP-141 triple as POSTs on the zone
	// (`managedZones/{zone}:getIamPolicy`), as `gcloud dns managed-zones
	// get-iam-policy` and google_dns_managed_zone_iam_* send. The verb resolves
	// before the zone, as Google's frontend resolves a method before the resource.
	srv.HandleFunc("POST /dns/v1/projects/{project}/managedZones/{zoneAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName, verb, found := gcpCustomMethod(sim.PathParam(r, "zoneAction"))
		if !found {
			gcpMethodNotFound(w)
			return
		}
		switch verb {
		case "getIamPolicy", "setIamPolicy", "testIamPermissions":
			if _, ok := zones.Get(project + "/" + zoneName); !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
				return
			}
			handleResourceIAM(w, r, gcpResourceIAMStore(), "dnsManagedZone/"+project+"/"+zoneName, verb)
		default:
			gcpMethodNotFound(w)
		}
	})

	// dnsKeys.list derives the keys from the zone's dnssecConfig.
	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zone, ok := zones.Get(project + "/" + zoneName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		keys := deriveDNSKeys(zone.ManagedZone)
		page, next, ok := paginateListCompute(w, r, keys, gcpAsc(func(k DNSKey) string { return k.Type }))
		if !ok {
			return
		}
		if page == nil {
			page = []DNSKey{}
		}
		resp := map[string]any{
			"kind":    "dns#dnsKeysListResponse",
			"dnsKeys": page,
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys/{dnsKeyId}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		keyID := sim.PathParam(r, "dnsKeyId")
		zone, ok := zones.Get(project + "/" + zoneName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		for _, k := range deriveDNSKeys(zone.ManagedZone) {
			if k.ID == keyID {
				sim.WriteJSON(w, http.StatusOK, k)
				return
			}
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "DNS key %q not found", keyID)
	})

	// managedZoneOperations.list reads the zone's mutation records.
	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/operations", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		if _, ok := zones.Get(project + "/" + zoneName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		var stored []storedDNSOperation
		for _, op := range operations.List() {
			if op.Project == project && op.Zone == zoneName {
				stored = append(stored, op)
			}
		}
		sort.Slice(stored, func(i, j int) bool { return stored[i].Seq < stored[j].Seq })
		out := make([]DNSOperation, 0, len(stored))
		for _, op := range stored {
			out = append(out, op.Operation)
		}
		page, next, ok := paginateListCompute(w, r, out, gcpAscNumber(func(op DNSOperation) string { return op.ID }))
		if !ok {
			return
		}
		if page == nil {
			page = []DNSOperation{}
		}
		resp := map[string]any{
			"kind":       "dns#managedZoneOperationsListResponse",
			"operations": page,
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		opID := sim.PathParam(r, "operation")
		if _, ok := zones.Get(project + "/" + zoneName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		stored, ok := operations.Get(dnsZoneOperationKey(project, zoneName, opID))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "operation %q not found", opID)
			return
		}
		sim.WriteJSON(w, http.StatusOK, stored.Operation)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		p, ok := crmResolveProject(sim.PathParam(r, "project"))
		if !ok || p.State != "ACTIVE" {
			crmProjectPermissionDenied(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, dnsProjectResource(p))
	})

	registerCloudDNSPolicies(srv, policies)
	registerCloudDNSResponsePolicies(srv, responsePolicies, responsePolicyRules)
}

func registerCloudDNSPolicies(srv *sim.Server, policies sim.Store[storedDNSPolicy]) {
	srv.HandleFunc("POST /dns/v1/projects/{project}/policies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var p DNSPolicy
		key, ok := dnsReadNew(w, r, policies, project, &p, "name", "policy", func(p *DNSPolicy) string { return p.Name })
		if !ok {
			return
		}
		if p.ID == "" {
			p.ID = randomUint64String()
		}
		p.Kind = "dns#policy"
		policies.Put(key, storedDNSPolicy{Project: project, Policy: p})
		sim.WriteJSON(w, http.StatusOK, p)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/policies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var items []DNSPolicy
		for _, sp := range policies.List() {
			if sp.Project == project {
				items = append(items, sp.Policy)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		page, next, ok := paginateListCompute(w, r, items, gcpAsc(func(v DNSPolicy) string { return v.Name }))
		if !ok {
			return
		}
		if page == nil {
			page = []DNSPolicy{}
		}
		resp := map[string]any{
			"kind":     "dns#policiesListResponse",
			"policies": page,
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/policies/{policy}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "policy")
		sp, ok := policies.Get(project + "/" + name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "policy %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sp.Policy)
	})

	srv.HandleFunc("DELETE /dns/v1/projects/{project}/policies/{policy}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "policy")
		if !policies.Delete(project + "/" + name) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "policy %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	patchOrUpdatePolicy := func(w http.ResponseWriter, r *http.Request, replace bool) {
		dnsPatchOrUpdate(w, r, policies, "policy", "policy", replace,
			func(sp storedDNSPolicy) DNSPolicy { return sp.Policy }, dnsApplyPolicyUpdate,
			func(project string, p DNSPolicy) storedDNSPolicy { return storedDNSPolicy{Project: project, Policy: p} })
	}
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/policies/{policy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdatePolicy(w, r, false)
	})
	srv.HandleFunc("PUT /dns/v1/projects/{project}/policies/{policy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdatePolicy(w, r, true)
	})
}

// registerCloudDNSResponsePolicies mounts the Cloud DNS ResponsePolicy resource
// and its nested ResponsePolicyRule subresource.
func registerCloudDNSResponsePolicies(srv *sim.Server, responsePolicies sim.Store[storedDNSResponsePolicy], rules sim.Store[storedDNSResponsePolicyRule]) {
	srv.HandleFunc("POST /dns/v1/projects/{project}/responsePolicies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var p DNSResponsePolicy
		key, ok := dnsReadNew(w, r, responsePolicies, project, &p, "responsePolicyName", "response policy", func(p *DNSResponsePolicy) string { return p.ResponsePolicyName })
		if !ok {
			return
		}
		if p.ID == "" {
			p.ID = randomUint64String()
		}
		p.Kind = "dns#responsePolicy"
		responsePolicies.Put(key, storedDNSResponsePolicy{Project: project, Policy: p})
		sim.WriteJSON(w, http.StatusOK, p)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/responsePolicies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var items []DNSResponsePolicy
		for _, sp := range responsePolicies.List() {
			if sp.Project == project {
				items = append(items, sp.Policy)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ResponsePolicyName < items[j].ResponsePolicyName })
		page, next, ok := paginateListCompute(w, r, items, gcpAsc(func(v DNSResponsePolicy) string { return v.ResponsePolicyName }))
		if !ok {
			return
		}
		if page == nil {
			page = []DNSResponsePolicy{}
		}
		resp := map[string]any{"responsePolicies": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "responsePolicy")
		sp, ok := responsePolicies.Get(project + "/" + name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "response policy %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sp.Policy)
	})

	srv.HandleFunc("DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "responsePolicy")
		if !responsePolicies.Delete(project + "/" + name) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "response policy %q not found", name)
			return
		}
		// Cascade-delete the policy's rules.
		for _, sr := range rules.List() {
			if sr.Project == project && sr.ResponsePolicy == name {
				rules.Delete(dnsResponsePolicyRuleKey(project, name, sr.Rule.RuleName))
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	patchOrUpdateRP := func(w http.ResponseWriter, r *http.Request, replace bool) {
		dnsPatchOrUpdate(w, r, responsePolicies, "responsePolicy", "response policy", replace,
			func(sp storedDNSResponsePolicy) DNSResponsePolicy { return sp.Policy }, dnsApplyResponsePolicyUpdate,
			func(project string, p DNSResponsePolicy) storedDNSResponsePolicy {
				return storedDNSResponsePolicy{Project: project, Policy: p}
			})
	}
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRP(w, r, false)
	})
	srv.HandleFunc("PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRP(w, r, true)
	})

	srv.HandleFunc("POST /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		rpName := sim.PathParam(r, "responsePolicy")
		if _, ok := responsePolicies.Get(project + "/" + rpName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "response policy %q not found", rpName)
			return
		}
		var rule DNSResponsePolicyRule
		if err := sim.ReadJSON(r, &rule); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if rule.RuleName == "" {
			GCPError(w, http.StatusBadRequest, "ruleName is required", "INVALID_ARGUMENT")
			return
		}
		key := dnsResponsePolicyRuleKey(project, rpName, rule.RuleName)
		if _, exists := rules.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "rule %q already exists", rule.RuleName)
			return
		}
		rule.Kind = "dns#responsePolicyRule"
		rules.Put(key, storedDNSResponsePolicyRule{Project: project, ResponsePolicy: rpName, Rule: rule})
		sim.WriteJSON(w, http.StatusOK, rule)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		rpName := sim.PathParam(r, "responsePolicy")
		if _, ok := responsePolicies.Get(project + "/" + rpName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "response policy %q not found", rpName)
			return
		}
		var items []DNSResponsePolicyRule
		for _, sr := range rules.List() {
			if sr.Project == project && sr.ResponsePolicy == rpName {
				items = append(items, sr.Rule)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].RuleName < items[j].RuleName })
		page, next, ok := paginateListCompute(w, r, items, gcpAsc(func(v DNSResponsePolicyRule) string { return v.RuleName }))
		if !ok {
			return
		}
		if page == nil {
			page = []DNSResponsePolicyRule{}
		}
		resp := map[string]any{"responsePolicyRules": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		rpName := sim.PathParam(r, "responsePolicy")
		ruleName := sim.PathParam(r, "rule")
		sr, ok := rules.Get(dnsResponsePolicyRuleKey(project, rpName, ruleName))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", ruleName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sr.Rule)
	})

	srv.HandleFunc("DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		rpName := sim.PathParam(r, "responsePolicy")
		ruleName := sim.PathParam(r, "rule")
		if !rules.Delete(dnsResponsePolicyRuleKey(project, rpName, ruleName)) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", ruleName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	patchOrUpdateRule := func(w http.ResponseWriter, r *http.Request, replace bool) {
		project := sim.PathParam(r, "project")
		rpName := sim.PathParam(r, "responsePolicy")
		ruleName := sim.PathParam(r, "rule")
		key := dnsResponsePolicyRuleKey(project, rpName, ruleName)
		sr, ok := rules.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", ruleName)
			return
		}
		var body DNSResponsePolicyRule
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		updated := dnsApplyRuleUpdate(sr.Rule, body, replace)
		rules.Put(key, storedDNSResponsePolicyRule{Project: project, ResponsePolicy: rpName, Rule: updated})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"responsePolicyRule": updated})
	}
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRule(w, r, false)
	})
	srv.HandleFunc("PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRule(w, r, true)
	})
}

func dnsRecordSetKey(project, zone, name, typ string) string {
	return fmt.Sprintf("%s/%s:%s:%s", project, zone, name, typ)
}

func writeDNSChangeError(w http.ResponseWriter, code int, status, message string) {
	writeDNSReasonError(w, code, status, status, message)
}

// writeDNSReasonError writes the Cloud DNS v1 error envelope, whose errors[]
// carries a reason gcloud and the client libraries match on.
func writeDNSReasonError(w http.ResponseWriter, code int, status, reason, message string) {
	sim.WriteJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
			"errors": []map[string]string{{
				"domain":  "global",
				"reason":  reason,
				"message": message,
			}},
		},
	})
}

func dnsChangeKey(project, zone, id string) string {
	return fmt.Sprintf("%s/%s:%s", project, zone, id)
}

func nextDNSChangeID(changes sim.Store[storedDNSChange], project, zone string) string {
	maxID := 0
	for _, stored := range changes.List() {
		if stored.Project != project || stored.Zone != zone {
			continue
		}
		id, err := strconv.Atoi(stored.Change.ID)
		if err == nil && id > maxID {
			maxID = id
		}
	}
	return strconv.Itoa(maxID + 1)
}

func dnsChangeDeletesRecord(change DNSChange, addition ResourceRecordSet) bool {
	for _, deletion := range change.Deletions {
		if deletion.Name == addition.Name && deletion.Type == addition.Type {
			return true
		}
	}
	return false
}

func dnsRecordSetsEqual(a, b ResourceRecordSet) bool {
	return a.Name == b.Name &&
		a.Type == b.Type &&
		a.TTL == b.TTL &&
		reflect.DeepEqual(a.Rrdatas, b.Rrdatas)
}

// randomUint64String returns a server-defined numeric id, matching the
// uint64/int64-format ids Cloud DNS assigns to Policy/ResponsePolicy.
func randomUint64String() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d", binary.BigEndian.Uint64(b)>>1)
}

func dnsResponsePolicyRuleKey(project, responsePolicy, rule string) string {
	return fmt.Sprintf("%s/%s:%s", project, responsePolicy, rule)
}

func dnsZoneOperationKey(project, zone, id string) string {
	return fmt.Sprintf("%s/%s:%s", project, zone, id)
}

// recordDNSZoneOperation records an Operation for a managed-zone mutation, as
// Cloud DNS surfaces each one through managedZoneOperations.
func recordDNSZoneOperation(ops sim.Store[storedDNSOperation], project, zone, opType string, oldVal, newVal *ManagedZone) {
	seq := 0
	for _, op := range ops.List() {
		if op.Project == project && op.Zone == zone && op.Seq >= seq {
			seq = op.Seq + 1
		}
	}
	id := strconv.Itoa(seq)
	op := DNSOperation{
		ID:        id,
		StartTime: nowTimestamp(),
		Status:    "done",
		User:      "cloud-dns-system",
		Type:      opType,
		ZoneContext: &dnsOperationZoneContext{
			OldValue: oldVal,
			NewValue: newVal,
		},
		Kind: "dns#operation",
	}
	ops.Put(dnsZoneOperationKey(project, zone, id), storedDNSOperation{
		Project:   project,
		Zone:      zone,
		Seq:       seq,
		Operation: op,
	})
}

// dnsManagedZoneUpdate applies a PATCH (merge) or PUT (replace) to a managed
// zone and records an "update" operation. The id, dnsName and visibility are
// immutable; description, labels and the nested configs change.
func dnsManagedZoneUpdate(w http.ResponseWriter, r *http.Request, zones sim.Store[storedManagedZone], ops sim.Store[storedDNSOperation], replace bool) {
	project := sim.PathParam(r, "project")
	zoneName := sim.PathParam(r, "zone")
	key := project + "/" + zoneName
	stored, ok := zones.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
		return
	}
	var body ManagedZone
	if err := sim.ReadJSON(r, &body); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	oldVal := stored.ManagedZone
	updated := stored.ManagedZone
	if replace {
		updated.Description = body.Description
		updated.Labels = body.Labels
		updated.DnssecConfig = body.DnssecConfig
		updated.ForwardingConfig = body.ForwardingConfig
		updated.PeeringConfig = body.PeeringConfig
		updated.CloudLoggingConfig = body.CloudLoggingConfig
	} else {
		if body.Description != "" {
			updated.Description = body.Description
		}
		if body.Labels != nil {
			updated.Labels = body.Labels
		}
		if body.DnssecConfig != nil {
			updated.DnssecConfig = body.DnssecConfig
		}
		if body.ForwardingConfig != nil {
			updated.ForwardingConfig = body.ForwardingConfig
		}
		if body.PeeringConfig != nil {
			updated.PeeringConfig = body.PeeringConfig
		}
		if body.CloudLoggingConfig != nil {
			updated.CloudLoggingConfig = body.CloudLoggingConfig
		}
	}
	stored.ManagedZone = updated
	zones.Put(key, stored)
	recordDNSZoneOperation(ops, project, zoneName, "update", &oldVal, &updated)
	// managedZones.patch and update answer the Operation they recorded.
	op, _ := ops.Get(dnsZoneOperationKey(project, zoneName, strconv.Itoa(latestDNSZoneOpSeq(ops, project, zoneName))))
	sim.WriteJSON(w, http.StatusOK, op.Operation)
}

func latestDNSZoneOpSeq(ops sim.Store[storedDNSOperation], project, zone string) int {
	seq := 0
	for _, op := range ops.List() {
		if op.Project == project && op.Zone == zone && op.Seq > seq {
			seq = op.Seq
		}
	}
	return seq
}

func dnsApplyPolicyUpdate(cur, body DNSPolicy, replace bool) DNSPolicy {
	out := cur
	if replace {
		out.Name = cur.Name
		out.ID = cur.ID
		out.Description = body.Description
		out.EnableInboundForwarding = body.EnableInboundForwarding
		out.EnableLogging = body.EnableLogging
		out.Networks = body.Networks
		out.AlternativeNameServerConfig = body.AlternativeNameServerConfig
		out.Dns64Config = body.Dns64Config
	} else {
		if body.Description != "" {
			out.Description = body.Description
		}
		out.EnableInboundForwarding = body.EnableInboundForwarding
		out.EnableLogging = body.EnableLogging
		if body.Networks != nil {
			out.Networks = body.Networks
		}
		if body.AlternativeNameServerConfig != nil {
			out.AlternativeNameServerConfig = body.AlternativeNameServerConfig
		}
		if body.Dns64Config != nil {
			out.Dns64Config = body.Dns64Config
		}
	}
	out.Kind = "dns#policy"
	return out
}

func dnsApplyResponsePolicyUpdate(cur, body DNSResponsePolicy, replace bool) DNSResponsePolicy {
	out := cur
	if replace {
		out.ResponsePolicyName = cur.ResponsePolicyName
		out.ID = cur.ID
		out.Description = body.Description
		out.Networks = body.Networks
		out.GKEClusters = body.GKEClusters
		out.Labels = body.Labels
	} else {
		if body.Description != "" {
			out.Description = body.Description
		}
		if body.Networks != nil {
			out.Networks = body.Networks
		}
		if body.GKEClusters != nil {
			out.GKEClusters = body.GKEClusters
		}
		if body.Labels != nil {
			out.Labels = body.Labels
		}
	}
	out.Kind = "dns#responsePolicy"
	return out
}

func dnsApplyRuleUpdate(cur, body DNSResponsePolicyRule, replace bool) DNSResponsePolicyRule {
	out := cur
	if replace {
		out.RuleName = cur.RuleName
		out.DNSName = body.DNSName
		out.LocalData = body.LocalData
		out.Behavior = body.Behavior
	} else {
		if body.DNSName != "" {
			out.DNSName = body.DNSName
		}
		if body.LocalData != nil {
			out.LocalData = body.LocalData
		}
		if body.Behavior != "" {
			out.Behavior = body.Behavior
		}
	}
	out.Kind = "dns#responsePolicyRule"
	return out
}

// dnsProjectResource returns the Cloud DNS Project resource: the project's ID
// and the number Cloud Resource Manager holds, with the documented Cloud DNS
// default per-project quotas.
func dnsProjectResource(p CRMProject) map[string]any {
	return map[string]any{
		"kind":   "dns#project",
		"id":     p.ProjectId,
		"number": strings.TrimPrefix(p.Name, "projects/"),
		"quota": map[string]any{
			"kind":                                 "dns#quota",
			"managedZones":                         10000,
			"rrsetsPerManagedZone":                 10000,
			"rrsetAdditionsPerChange":              1000,
			"rrsetDeletionsPerChange":              1000,
			"totalRrdataSizePerChange":             100000,
			"resourceRecordsPerRrset":              100,
			"dnsKeysPerManagedZone":                4,
			"networksPerManagedZone":               100,
			"managedZonesPerNetwork":               10000,
			"policies":                             100,
			"networksPerPolicy":                    100,
			"targetNameServersPerPolicy":           100,
			"targetNameServersPerManagedZone":      100,
			"peeringZonesPerTargetNetwork":         1000,
			"responsePolicies":                     100,
			"networksPerResponsePolicy":            100,
			"nameserversPerDelegation":             50,
			"gkeClustersPerManagedZone":            100,
			"managedZonesPerGkeCluster":            1000,
			"gkeClustersPerPolicy":                 100,
			"responsePolicyRulesPerResponsePolicy": 1000,
			"gkeClustersPerResponsePolicy":         100,
			"itemsPerRoutingPolicy":                500,
		},
	}
}

// DNSKey mirrors the Cloud DNS DnsKey resource (a DNSSEC key pair). All
// cryptographic fields are output-only.
type DNSKey struct {
	ID           string         `json:"id,omitempty"`
	Algorithm    string         `json:"algorithm,omitempty"`
	KeyLength    int            `json:"keyLength,omitempty"`
	PublicKey    string         `json:"publicKey,omitempty"`
	CreationTime string         `json:"creationTime,omitempty"`
	IsActive     bool           `json:"isActive,omitempty"`
	Type         string         `json:"type,omitempty"`
	KeyTag       int            `json:"keyTag,omitempty"`
	Digests      []DNSKeyDigest `json:"digests,omitempty"`
	Description  string         `json:"description,omitempty"`
	Kind         string         `json:"kind,omitempty"`
}

type DNSKeyDigest struct {
	Type   string `json:"type,omitempty"`
	Digest string `json:"digest,omitempty"`
}

// deriveDNSKeys returns a zone's DNSSEC keys: none when DNSSEC is off, as
// Cloud DNS exposes none, and otherwise one key-signing and one zone-signing
// ECDSA P-256 key derived from the zone ID, with the RFC 4034 Appendix B key
// tag and a SHA-256 DS digest.
func deriveDNSKeys(zone ManagedZone) []DNSKey {
	state := dnssecState(zone.DnssecConfig)
	if state != "on" && state != "transfer" {
		return []DNSKey{}
	}
	return []DNSKey{
		deriveDNSKey(zone, "keySigning"),
		deriveDNSKey(zone, "zoneSigning"),
	}
}

func dnssecState(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "off"
	}
	var cfg struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "off"
	}
	if cfg.State == "" {
		return "off"
	}
	return cfg.State
}

func deriveDNSKey(zone ManagedZone, keyType string) DNSKey {
	// Derive the private scalar from a SHA-256 keystream over the zone ID and key
	// type, so list and get return the same key and key tag. ecdsa.GenerateKey is
	// not deterministic: it reads a random number of bytes through MaybeReadByte.
	// Reducing 320 bits into [1, n-1] leaves negligible bias.
	curve := elliptic.P256()
	scalarBuf := make([]byte, 40)
	_, _ = io.ReadFull(newDNSKeyKeystream("dnssec:"+zone.ID+":"+keyType), scalarBuf)
	d := new(big.Int).SetBytes(scalarBuf)
	d.Mod(d, new(big.Int).Sub(curve.Params().N, big.NewInt(1)))
	d.Add(d, big.NewInt(1))
	priv := new(ecdsa.PrivateKey)
	priv.Curve = curve
	priv.D = d
	priv.X, priv.Y = curve.ScalarBaseMult(d.Bytes())
	// RFC 6605 §4: the DNSKEY public key for ECDSAP256SHA256 is the raw
	// 64-byte X||Y point.
	pub := make([]byte, 64)
	priv.X.FillBytes(pub[:32])
	priv.Y.FillBytes(pub[32:])

	flags := uint16(256) // ZONE flag
	if keyType == "keySigning" {
		flags |= 1 // Secure Entry Point
	}
	const algorithm = 13 // ECDSAP256SHA256
	rdata := dnskeyRDATA(flags, algorithm, pub)
	keyTag := dnskeyKeyTag(rdata)

	ownerName := dnssecOwnerWire(zone.DNSName)
	dsInput := append(append([]byte{}, ownerName...), rdata...)
	dsHash := sha256.Sum256(dsInput)

	return DNSKey{
		ID:           fmt.Sprintf("%d", keyTag),
		Algorithm:    "ecdsap256sha256",
		KeyLength:    256,
		PublicKey:    base64.StdEncoding.EncodeToString(pub),
		CreationTime: nowTimestamp(),
		IsActive:     true,
		Type:         keyType,
		KeyTag:       int(keyTag),
		Digests: []DNSKeyDigest{{
			Type:   "sha256",
			Digest: strings.ToUpper(hex.EncodeToString(dsHash[:])),
		}},
		Kind: "dns#dnsKey",
	}
}

// dnsKeyKeystream is an unbounded deterministic byte source: SHA-256(seed ||
// counter) blocks, concatenated.
type dnsKeyKeystream struct {
	seed    []byte
	counter uint64
	buf     []byte
}

func newDNSKeyKeystream(seed string) *dnsKeyKeystream {
	return &dnsKeyKeystream{seed: []byte(seed)}
}

func (k *dnsKeyKeystream) Read(p []byte) (int, error) {
	for len(k.buf) < len(p) {
		var ctr [8]byte
		binary.BigEndian.PutUint64(ctr[:], k.counter)
		k.counter++
		block := sha256.Sum256(append(append([]byte{}, k.seed...), ctr[:]...))
		k.buf = append(k.buf, block[:]...)
	}
	n := copy(p, k.buf)
	k.buf = k.buf[n:]
	return n, nil
}

// dnskeyRDATA assembles the DNSKEY RR RDATA: flags(2) | protocol(1) |
// algorithm(1) | public key.
func dnskeyRDATA(flags uint16, algorithm byte, pub []byte) []byte {
	out := make([]byte, 0, 4+len(pub))
	out = append(out, byte(flags>>8), byte(flags))
	out = append(out, 3) // protocol is always 3
	out = append(out, algorithm)
	out = append(out, pub...)
	return out
}

// dnskeyKeyTag computes the DNSKEY key tag per RFC 4034 Appendix B for
// algorithms other than 1.
func dnskeyKeyTag(rdata []byte) uint16 {
	var ac uint32
	for i, b := range rdata {
		if i&1 == 0 {
			ac += uint32(b) << 8
		} else {
			ac += uint32(b)
		}
	}
	ac += (ac >> 16) & 0xFFFF
	return uint16(ac & 0xFFFF)
}

// dnssecOwnerWire encodes a DNS owner name (the zone's dnsName) into the
// length-prefixed wire format used for the DS digest input.
func dnssecOwnerWire(dnsName string) []byte {
	name := strings.TrimSuffix(dnsName, ".")
	var out []byte
	if name != "" {
		for _, label := range strings.Split(name, ".") {
			out = append(out, byte(len(label)))
			out = append(out, []byte(label)...)
		}
	}
	out = append(out, 0) // root label
	return out
}

// dnsReadNew reads a create request's body into p and answers the refusals
// every create of a project's named resource shares: an unreadable body, a
// missing name, and a name already in use. key is the resource's key in store.
func dnsReadNew[P, S any](w http.ResponseWriter, r *http.Request, store sim.Store[S], project string, p *P, field, noun string, name func(*P) string) (string, bool) {
	if err := sim.ReadJSON(r, p); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return "", false
	}
	if name(p) == "" {
		GCPError(w, http.StatusBadRequest, field+" is required", "INVALID_ARGUMENT")
		return "", false
	}
	key := project + "/" + name(p)
	if _, exists := store.Get(key); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "%s %q already exists", noun, name(p))
		return "", false
	}
	return key, true
}

// dnsPatchOrUpdate answers the patch, or with replace the update, of the
// project's resource of store the path parameter param names, which the
// response carries under param.
func dnsPatchOrUpdate[S, P any](w http.ResponseWriter, r *http.Request, store sim.Store[S], param, noun string, replace bool,
	policy func(S) P, apply func(current, body P, replace bool) P, wrap func(project string, p P) S) {
	project := sim.PathParam(r, "project")
	name := sim.PathParam(r, param)
	key := project + "/" + name
	stored, ok := store.Get(key)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", noun, name)
		return
	}
	var body P
	if err := sim.ReadJSON(r, &body); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	updated := apply(policy(stored), body, replace)
	store.Put(key, wrap(project, updated))
	sim.WriteJSON(w, http.StatusOK, map[string]any{param: updated})
}
