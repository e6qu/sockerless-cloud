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
)

// dnsChangeMu serializes the Changes.create critical section. Real Cloud DNS
// applies changes atomically and sequentially per zone; without this lock two
// concurrent POSTs could read the same highest change ID and overwrite each
// other's change record, and interleave record-set mutations mid-validation.
var dnsChangeMu sync.Mutex

// Cloud DNS types

// ManagedZone represents a Cloud DNS managed zone.
type ManagedZone struct {
	Name                    string            `json:"name"`
	DNSName                 string            `json:"dnsName"`
	Description             string            `json:"description,omitempty"`
	ID                      string            `json:"id,omitempty"`
	Visibility              string            `json:"visibility,omitempty"`
	PrivateVisibilityConfig map[string]any    `json:"privateVisibilityConfig,omitempty"`
	Labels                  map[string]string `json:"labels,omitempty"`
	// Nested writable configs the sim doesn't otherwise interpret are
	// stored verbatim so create→get round-trips byte-exact and the
	// terraform-provider-google read path doesn't perpetually drift.
	DnssecConfig       json.RawMessage `json:"dnssecConfig,omitempty"`
	ForwardingConfig   json.RawMessage `json:"forwardingConfig,omitempty"`
	PeeringConfig      json.RawMessage `json:"peeringConfig,omitempty"`
	CloudLoggingConfig json.RawMessage `json:"cloudLoggingConfig,omitempty"`
}

// storedManagedZone is the persisted row backing a managed zone: the
// wire-shape ManagedZone (what handlers emit — real Cloud DNS's
// ManagedZone has no dockerNetworkName member) plus sockerless wiring
// that must survive a simulator restart. The embedding flattens on
// json.Marshal, so sim.Store persistence keeps the same row shape the
// wiring has always been recovered from.
type storedManagedZone struct {
	ManagedZone
	// DockerNetworkName is the real Docker user-defined network backing
	// this private zone. Containers referenced by A records inside the
	// zone are connected to this network with the record's short name
	// as DNS alias, so cross-container DNS resolves via Docker's
	// embedded DNS. Empty for public zones. Store-only: never emitted
	// on the wire.
	DockerNetworkName string `json:"dockerNetworkName,omitempty"`
}

// pruneEmptyPrivateVisibilityConfig returns nil when the config carries no
// networks and no GKE clusters, matching real Cloud DNS (which omits an empty
// privateVisibilityConfig). A populated config is returned unchanged.
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

// DNSPolicy mirrors the Cloud DNS Policy resource (a DNS server policy:
// inbound/outbound forwarding + alternative name servers for one or more
// VPC networks). Nested writable configs the simulator doesn't otherwise
// interpret are stored verbatim as RawMessage so create→get round-trips
// byte-exact.
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

// DNSOperation mirrors the Cloud DNS Operation resource: an audit-log
// record of a successful mutation on a ManagedZone. Real Cloud DNS records
// one per managed-zone create/update/patch/delete; managedZoneOperations.get
// and .list read them back.
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

	// Create managed zone
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
			// DNS API expects a numeric uint64 ID
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			zone.ID = fmt.Sprintf("%d", binary.BigEndian.Uint64(b)>>1)
		}
		if zone.Visibility == "" {
			zone.Visibility = "public"
		}
		// terraform-provider-google always sends privateVisibilityConfig with an
		// empty networks list (even for public zones). Real Cloud DNS drops an
		// empty privateVisibilityConfig from the read-back; echoing it makes the
		// provider's flatten materialize a phantom block on every refresh. Strip
		// it unless it actually carries networks or GKE clusters.
		zone.PrivateVisibilityConfig = pruneEmptyPrivateVisibilityConfig(zone.PrivateVisibilityConfig)

		// Back every private zone with a real Docker network.
		// Containers registered in the zone via A records (sockerless's
		// service-register step) get connected to this network with
		// their record short-name as DNS alias, so cross-container DNS
		// works via Docker's embedded resolver. Public zones get no
		// Docker network.
		stored := storedManagedZone{ManagedZone: zone}
		if zone.Visibility == "private" {
			netName := "sim-" + zone.ID
			if _, err := sim.EnsureDockerNetwork(netName); err == nil {
				stored.DockerNetworkName = netName
			}
		}

		zones.Put(key, stored)
		recordDNSZoneOperation(operations, project, zone.Name, "insert", nil, &stored.ManagedZone)
		sim.WriteJSON(w, http.StatusOK, stored.ManagedZone)
	})

	// List managed zones
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

	// Get managed zone
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

	// Delete managed zone
	srv.HandleFunc("DELETE /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		key := project + "/" + zoneName

		zone, ok := zones.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		zones.Delete(key)
		// The zone's IAM policy dies with the zone: a later zone created
		// under the same name starts with no bindings.
		gcpResourceIAMStore().Delete("dnsManagedZone/" + project + "/" + zoneName)
		oldZone := zone.ManagedZone
		recordDNSZoneOperation(operations, project, zoneName, "delete", &oldZone, nil)

		// Delete associated record sets for this zone.
		for _, stored := range recordSets.List() {
			if stored.Project == project && stored.Zone == zoneName {
				recordSets.Delete(dnsRecordSetKey(project, zoneName, stored.Record.Name, stored.Record.Type))
			}
		}

		// Drop the Docker network backing the private zone.
		if zone.DockerNetworkName != "" {
			_ = sim.RemoveDockerNetwork(zone.DockerNetworkName)
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// List record sets
	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/rrsets", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zoneKey := project + "/" + zoneName

		if _, ok := zones.Get(zoneKey); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}

		// Real Cloud DNS rrsets.list filters on the optional name +
		// type query params (the Go SDK's .Name()/.Type() builders);
		// the Cloud Run service-discovery path relies on this.
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

		page, next, ok := paginateListCompute(w, r, filtered)
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

	// Create record set
	srv.HandleFunc("POST /dns/v1/projects/{project}/managedZones/{zone}/rrsets", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zoneKey := project + "/" + zoneName

		zone, ok := zones.Get(zoneKey)
		if !ok {
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

		// For A records on a private zone, connect the container
		// identified by Rrdatas[0] (its bridge-network IP) to the
		// zone's Docker network, with the record's short name as
		// DNS alias. Cross-container DNS resolves via Docker's
		// embedded resolver from that point on.
		if zone.DockerNetworkName != "" && rs.Type == "A" && len(rs.Rrdatas) > 0 {
			if containerName := sim.FindContainerByIP(rs.Rrdatas[0]); containerName != "" {
				alias := shortHostnameFromDNS(rs.Name, zone.DNSName)
				_ = sim.ConnectContainerToNetwork(containerName, zone.DockerNetworkName, []string{alias})
			}
		}

		sim.WriteJSON(w, http.StatusOK, rs)
	})

	// Get record set
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
		zoneKey := project + "/" + zoneName
		key := dnsRecordSetKey(project, zoneName, rrName, rrType)

		stored, rsOk := recordSets.Get(key)
		if !recordSets.Delete(key) {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "record set %s/%s not found", rrName, rrType)
			return
		}

		// Disconnect the container that was connected when the
		// record was created. Best-effort — container shutdown
		// already cleans up Docker-side network memberships.
		if rsOk && stored.Record.Type == "A" && len(stored.Record.Rrdatas) > 0 {
			if zone, ok := zones.Get(zoneKey); ok && zone.DockerNetworkName != "" {
				if containerName := sim.FindContainerByIP(stored.Record.Rrdatas[0]); containerName != "" {
					_ = sim.DisconnectContainerFromNetwork(containerName, zone.DockerNetworkName)
				}
			}
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
		zone, ok := zones.Get(zoneKey)
		if !ok {
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
			disconnectDNSRecordFromZone(zone, deletion)
		}
		for _, addition := range change.Additions {
			recordSets.Put(dnsRecordSetKey(project, zoneName, addition.Name, addition.Type),
				storedResourceRecordSet{Project: project, Zone: zoneName, Record: addition})
			connectDNSRecordToZone(zone, addition)
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
		// Honor sortOrder (ascending default | descending) — real Cloud DNS
		// changes.list sorts by change id.
		desc := r.URL.Query().Get("sortOrder") == "descending"
		sort.Slice(out, func(i, j int) bool {
			left, _ := strconv.Atoi(out[i].ID)
			right, _ := strconv.Atoi(out[j].ID)
			if desc {
				return left > right
			}
			return left < right
		})
		page, next, ok := paginateListCompute(w, r, out)
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

	// Patch managed zone (merge-update of mutable fields).
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		dnsManagedZoneUpdate(w, r, zones, operations, false)
	})

	// Update managed zone (full replace of mutable fields).
	srv.HandleFunc("PUT /dns/v1/projects/{project}/managedZones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		dnsManagedZoneUpdate(w, r, zones, operations, true)
	})

	// Managed-zone IAM — the AIP-141 triple, which Cloud DNS spells as POSTs
	// on the zone (`managedZones/{zone}:getIamPolicy` and friends): the wire
	// `gcloud dns managed-zones get-iam-policy` and terraform's
	// google_dns_managed_zone_iam_* resources speak. Go's mux captures the
	// "{zone}:{verb}" segment whole; the verb resolves before the zone, the
	// way Google's frontend resolves a method before the resource. The policy
	// rides the same per-resource store every other AIP-141 resource uses, so
	// etag / member-validation / optimistic-concurrency behavior matches.
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

	// List DNSSEC keys for a zone — derived from the zone's dnssecConfig.
	srv.HandleFunc("GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zoneName := sim.PathParam(r, "zone")
		zone, ok := zones.Get(project + "/" + zoneName)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "managed zone %q not found", zoneName)
			return
		}
		keys := deriveDNSKeys(zone.ManagedZone)
		page, next, ok := paginateListCompute(w, r, keys)
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

	// Get a single DNSSEC key by id.
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

	// List managed-zone operations (audit log of zone mutations).
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
		page, next, ok := paginateListCompute(w, r, out)
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

	// Get a single managed-zone operation by id.
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

	// Get project DNS info (quota/number).
	srv.HandleFunc("GET /dns/v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		sim.WriteJSON(w, http.StatusOK, dnsProjectResource(project))
	})

	registerCloudDNSPolicies(srv, policies)
	registerCloudDNSResponsePolicies(srv, responsePolicies, responsePolicyRules)
}

// registerCloudDNSPolicies mounts the Cloud DNS Policy resource (a DNS server
// policy applied to one or more VPC networks).
func registerCloudDNSPolicies(srv *sim.Server, policies sim.Store[storedDNSPolicy]) {
	srv.HandleFunc("POST /dns/v1/projects/{project}/policies", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var p DNSPolicy
		if err := sim.ReadJSON(r, &p); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if p.Name == "" {
			GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
			return
		}
		key := project + "/" + p.Name
		if _, exists := policies.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "policy %q already exists", p.Name)
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
		page, next, ok := paginateListCompute(w, r, items)
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
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "policy")
		key := project + "/" + name
		sp, ok := policies.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "policy %q not found", name)
			return
		}
		var body DNSPolicy
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		updated := dnsApplyPolicyUpdate(sp.Policy, body, replace)
		policies.Put(key, storedDNSPolicy{Project: project, Policy: updated})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"policy": updated})
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
		if err := sim.ReadJSON(r, &p); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if p.ResponsePolicyName == "" {
			GCPError(w, http.StatusBadRequest, "responsePolicyName is required", "INVALID_ARGUMENT")
			return
		}
		key := project + "/" + p.ResponsePolicyName
		if _, exists := responsePolicies.Get(key); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "response policy %q already exists", p.ResponsePolicyName)
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
		page, next, ok := paginateListCompute(w, r, items)
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
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "responsePolicy")
		key := project + "/" + name
		sp, ok := responsePolicies.Get(key)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "response policy %q not found", name)
			return
		}
		var body DNSResponsePolicy
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		updated := dnsApplyResponsePolicyUpdate(sp.Policy, body, replace)
		responsePolicies.Put(key, storedDNSResponsePolicy{Project: project, Policy: updated})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"responsePolicy": updated})
	}
	srv.HandleFunc("PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRP(w, r, false)
	})
	srv.HandleFunc("PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}", func(w http.ResponseWriter, r *http.Request) {
		patchOrUpdateRP(w, r, true)
	})

	// Response-policy rules.
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
		page, next, ok := paginateListCompute(w, r, items)
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
	sim.WriteJSON(w, code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"status":  status,
			"errors": []map[string]string{{
				"domain":  "global",
				"reason":  status,
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

func connectDNSRecordToZone(zone storedManagedZone, rs ResourceRecordSet) {
	if zone.DockerNetworkName == "" || rs.Type != "A" || len(rs.Rrdatas) == 0 {
		return
	}
	if containerName := sim.FindContainerByIP(rs.Rrdatas[0]); containerName != "" {
		alias := shortHostnameFromDNS(rs.Name, zone.DNSName)
		_ = sim.ConnectContainerToNetwork(containerName, zone.DockerNetworkName, []string{alias})
	}
}

func disconnectDNSRecordFromZone(zone storedManagedZone, rs ResourceRecordSet) {
	if zone.DockerNetworkName == "" || rs.Type != "A" || len(rs.Rrdatas) == 0 {
		return
	}
	if containerName := sim.FindContainerByIP(rs.Rrdatas[0]); containerName != "" {
		_ = sim.DisconnectContainerFromNetwork(containerName, zone.DockerNetworkName)
	}
}

// shortHostnameFromDNS strips the zone's DNS suffix from a record name
// so we can use the short hostname as a Docker DNS alias. Cloud DNS
// names are always FQDNs with a trailing dot, e.g. "alpha.test.local."
// for a zone whose DNSName is "test.local." → "alpha". Docker's
// embedded DNS resolves short names via aliases, so this is what we
// want containers inside the network to use as `getent hosts alpha`.
func shortHostnameFromDNS(recordName, zoneDNS string) string {
	name := strings.TrimSuffix(recordName, ".")
	suffix := strings.TrimSuffix(zoneDNS, ".")
	if suffix != "" && strings.HasSuffix(name, "."+suffix) {
		name = strings.TrimSuffix(name, "."+suffix)
	}
	return name
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

// recordDNSZoneOperation appends an audit-log Operation for a managed-zone
// mutation, matching real Cloud DNS (which surfaces every zone insert/update/
// delete through managedZoneOperations).
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

// dnsManagedZoneUpdate applies a PATCH (merge) or PUT (replace of mutable
// fields) to a managed zone and records an "update" operation. The id,
// dnsName, and visibility of a zone are immutable; only description, labels,
// and the nested writable configs change.
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
	// Cloud DNS managedZones.patch/update return a Operation (long-running),
	// but the Go SDK ManagedZonesUpdateCall/PatchCall decode it into an
	// Operation only when DNSSEC mutation is involved; the common terraform
	// path issues these and reads the zone back via Get. Returning the
	// updated zone keeps create→patch→get round-trips coherent. Real Cloud
	// DNS returns the Operation; the Go SDK's ManagedZones.Patch/Update
	// response type is *Operation, so emit that shape.
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

// dnsProjectResource returns the Cloud DNS Project resource (quota + numeric
// id). The quota values mirror the documented Cloud DNS default per-project
// limits.
func dnsProjectResource(project string) map[string]any {
	return map[string]any{
		"kind":   "dns#project",
		"id":     project,
		"number": dnsProjectNumber(project),
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

// dnsProjectNumber derives a stable numeric project number from the project
// id, the way Cloud DNS surfaces a server-assigned project number.
func dnsProjectNumber(project string) string {
	h := sha256.Sum256([]byte("dns-project-number:" + project))
	n := binary.BigEndian.Uint64(h[:8]) >> 1
	return strconv.FormatUint(n, 10)
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

// deriveDNSKeys returns the DNSSEC key set for a zone, derived from its
// dnssecConfig. When DNSSEC is off (or unset), Cloud DNS exposes no DnsKeys,
// so the list is honestly empty. When on, Cloud DNS generates one key-signing
// key (KSK) and one zone-signing key (ZSK); we derive real ECDSA P-256 key
// pairs deterministically from the zone id, compute the RFC 4034 Appendix B
// key tag, and emit a real SHA-256 DS digest.
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
	// Deterministic ECDSA P-256 key from the zone id + key type, so dnsKeys.list
	// and dnsKeys.get re-derive the SAME key (and thus the same key tag get-by-id
	// must match). ecdsa.GenerateKey is deliberately NOT deterministic — it
	// consumes a random number of bytes from the reader via the crypto-internal
	// MaybeReadByte masking — so derive the private scalar directly from a
	// SHA-256 keystream instead. 320 bits reduced into [1, n-1] leaves negligible
	// modular bias, ample for a simulator's stable DNSSEC key.
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

// dnsKeyKeystream is an unbounded deterministic byte source: blocks of
// SHA-256(seed || counter) concatenated. It feeds ecdsa.GenerateKey so key
// derivation is reproducible regardless of how many bytes rejection sampling
// consumes.
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
