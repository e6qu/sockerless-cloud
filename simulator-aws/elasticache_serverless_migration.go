package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECServerlessCacheMigration is the migration PrepareMigration opens on a
// serverless cache, from the one source endpoint it names, until
// CompleteMigration ends it. Phase is where the migration stands:
// prepared until StartMigration starts the data sync, syncing while it runs,
// stopped once StopMigration stopped it.
type ECServerlessCacheMigration struct {
	SourceAddress        string
	SourcePort           int    `json:",omitempty"`
	SourceSecretArn      string `json:",omitempty"`
	SourceTlsMode        string `json:",omitempty"`
	SourceTlsCertificate string `json:",omitempty"`
	Phase                string
	AutoCutover          bool `json:",omitempty"`
}

const (
	ecMigrationPrepared = "prepared"
	ecMigrationSyncing  = "syncing"
	ecMigrationStopped  = "stopped"
)

func renderECServerlessCacheMigration(c ECServerlessCache) string {
	if c.Migration == nil {
		return ""
	}
	return fmt.Sprintf("<Migration><MigrationEndpoint><Address>%s</Address><Port>%d</Port></MigrationEndpoint></Migration>",
		xmlEscape(c.EndpointAddress), c.EndpointPort)
}

// ecServerlessCacheForMigration loads the serverless cache a migration
// request names, answering ServerlessCacheNotFoundFault when there is none.
func ecServerlessCacheForMigration(w http.ResponseWriter, r *http.Request) (ECServerlessCache, bool) {
	name := r.FormValue("ServerlessCacheName")
	c, ok := ecServerlessCaches.Get(name)
	if !ok {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", fmt.Sprintf("Serverless cache %s not found.", name), http.StatusNotFound, sim.RequestID(r.Context()))
	}
	return c, ok
}

// ecRequireMigration answers ServerlessCacheNotUnderMigrationFault for a
// serverless cache no migration is open on.
func ecRequireMigration(w http.ResponseWriter, r *http.Request, c ECServerlessCache) bool {
	if c.Migration != nil {
		return true
	}
	ecErrorXML(w, "ServerlessCacheNotUnderMigrationFault",
		fmt.Sprintf("Serverless cache %s is not under migration.", c.ServerlessCacheName), http.StatusBadRequest, sim.RequestID(r.Context()))
	return false
}

func handleECPrepareMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(message string) {
		ecErrorXML(w, "InvalidParameterValue", message, http.StatusBadRequest, requestID)
	}
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok {
		return
	}
	if c.Migration != nil {
		ecErrorXML(w, "ServerlessCacheAlreadyUnderMigrationFault",
			fmt.Sprintf("A migration is already in progress for serverless cache %s.", c.ServerlessCacheName), http.StatusBadRequest, requestID)
		return
	}
	if c.Status != "available" {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("Serverless cache %s is %s, not available.", c.ServerlessCacheName, c.Status), http.StatusBadRequest, requestID)
		return
	}
	var endpoints []ECServerlessCacheMigration
	for n := 1; ; n++ {
		prefix := fmt.Sprintf("CustomerNodeEndpointList.member.%d", n)
		address, port := r.FormValue(prefix+".Address"), r.FormValue(prefix+".Port")
		if address == "" && port == "" {
			break
		}
		endpoints = append(endpoints, ECServerlessCacheMigration{SourceAddress: address, SourcePort: atoiOrZero(port)})
	}
	if len(endpoints) != 1 {
		invalid(fmt.Sprintf("CustomerNodeEndpointList names %d endpoints; a serverless cache migration takes exactly one.", len(endpoints)))
		return
	}
	migration := endpoints[0]
	if migration.SourceAddress == "" {
		invalid("The source endpoint needs an Address.")
		return
	}
	migration.SourceSecretArn = r.FormValue("SourceAuthenticationConfiguration.SecretManagerConfig.SecretArn")
	migration.SourceTlsMode = r.FormValue("SourceTlsConfiguration.SourceTlsMode")
	migration.SourceTlsCertificate = r.FormValue("SourceTlsConfiguration.SourceTlsCertificate")
	switch migration.SourceTlsMode {
	case "", "none", "verify-ca":
	default:
		invalid(fmt.Sprintf("Invalid SourceTlsMode %q. Valid values: none | verify-ca.", migration.SourceTlsMode))
		return
	}
	migration.Phase = ecMigrationPrepared
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) { stored.Migration = &migration })
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	ecXMLResponse(w, "PrepareMigration", renderECServerlessCache(updated), requestID)
}

func handleECStopMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	if c.Migration.Phase != ecMigrationSyncing {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is not syncing data.", c.ServerlessCacheName), http.StatusBadRequest, requestID)
		return
	}
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) { stored.Migration.Phase = ecMigrationStopped })
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	ecXMLResponse(w, "StopMigration", renderECServerlessCache(updated), requestID)
}

// ecServerlessMigrationTarget reports whether a StartMigration or
// CompleteMigration request names a serverless cache, refusing one that names
// a replication group as well.
func ecServerlessMigrationTarget(w http.ResponseWriter, r *http.Request) (serverless, ok bool) {
	if r.FormValue("ServerlessCacheName") == "" {
		return false, true
	}
	if r.FormValue("ReplicationGroupId") != "" {
		ecErrorXML(w, "InvalidParameterCombination", "Specify either ServerlessCacheName or ReplicationGroupId, not both.",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return true, false
	}
	return true, true
}

// ecStartServerlessMigration starts the data sync of the migration
// PrepareMigration opened, from the endpoint it named. A sync StopMigration
// stopped restarts only with ClearTargetCacheData.
func ecStartServerlessMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	switch c.Migration.Phase {
	case ecMigrationSyncing:
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is already syncing data.", c.ServerlessCacheName), http.StatusBadRequest, requestID)
		return
	case ecMigrationStopped:
		if !strings.EqualFold(r.FormValue("ClearTargetCacheData"), "true") {
			ecErrorXML(w, "InvalidParameterValue",
				"After StopMigration, StartMigration requires ClearTargetCacheData to restart the data sync.", http.StatusBadRequest, requestID)
			return
		}
	}
	autoCutover := strings.EqualFold(r.FormValue("AutoCutover"), "true")
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) {
		stored.Migration.Phase = ecMigrationSyncing
		stored.Migration.AutoCutover = autoCutover
	})
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	ecXMLResponse(w, "StartMigration", renderECServerlessCache(updated), requestID)
}

// ecCompleteServerlessMigration ends the migration: a migration whose data
// sync runs cuts over, and Force ends one whose data is not in sync.
func ecCompleteServerlessMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	if c.Migration.Phase != ecMigrationSyncing && !strings.EqualFold(r.FormValue("Force"), "true") {
		ecErrorXML(w, "MigrationHealthFault",
			fmt.Sprintf("The migration of serverless cache %s is not syncing data; set Force to true to end it without its data in sync.", c.ServerlessCacheName),
			http.StatusBadRequest, requestID)
		return
	}
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) { stored.Migration = nil })
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	ecXMLResponse(w, "CompleteMigration", renderECServerlessCache(updated), requestID)
}
