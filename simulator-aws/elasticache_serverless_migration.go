package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// Opened by PrepareMigration from one source endpoint, ended by
// CompleteMigration. Phase is prepared until StartMigration syncs, syncing
// from then, stopped after StopMigration.
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

// Answers ServerlessCacheNotFoundFault when there is none.
func ecServerlessCacheForMigration(w http.ResponseWriter, r *http.Request) (ECServerlessCache, bool) {
	name := r.FormValue("ServerlessCacheName")
	c, ok := ecServerlessCaches.Get(name)
	if !ok {
		ecErrorXML(w, "ServerlessCacheNotFoundFault", fmt.Sprintf("Serverless cache %s not found.", name), http.StatusNotFound, sim.RequestID(r.Context()))
	}
	return c, ok
}

// Answers ServerlessCacheNotUnderMigrationFault without an open migration.
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
	if c.Engine == "memcached" {
		ecErrorXML(w, "InvalidParameterCombination", "A serverless cache migration moves a Valkey or Redis OSS dataset.", http.StatusBadRequest, requestID)
		return
	}
	if migration.SourceSecretArn != "" {
		invalid("The simulator reads no Secrets Manager secret for a migration source; name a source that requires no authentication.")
		return
	}
	source, err := ecDialMigrationSource(migration)
	if err != nil {
		invalid(fmt.Sprintf("The source endpoint %s:%d does not answer: %v", migration.SourceAddress, migration.SourcePort, err))
		return
	}
	_ = source.Close()
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

// Refuses a request naming a replication group as well.
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

// A sync StopMigration stopped restarts only with ClearTargetCacheData.
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
	clear := strings.EqualFold(r.FormValue("ClearTargetCacheData"), "true")
	if err := ecSyncMigration(c, *c.Migration, clear); err != nil {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The data sync of serverless cache %s failed: %v", c.ServerlessCacheName, err), http.StatusBadRequest, requestID)
		return
	}
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) {
		if autoCutover {
			stored.Migration = nil
			return
		}
		stored.Migration.Phase = ecMigrationSyncing
		stored.Migration.AutoCutover = autoCutover
	})
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	ecXMLResponse(w, "StartMigration", renderECServerlessCache(updated), requestID)
}

// A syncing migration cuts over; Force ends one that is not in sync.
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

// TLS trusting the named certificate in verify-ca mode; the source must answer
// PING.
func ecDialMigrationSource(m ECServerlessCacheMigration) (*cacheengine.Conn, error) {
	port := m.SourcePort
	if port == 0 {
		port = ecRedisPort
	}
	address := net.JoinHostPort(m.SourceAddress, strconv.Itoa(port))
	var conn *cacheengine.Conn
	var err error
	if m.SourceTlsMode == "verify-ca" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(m.SourceTlsCertificate)) {
			return nil, fmt.Errorf("SourceTlsCertificate is not a PEM certificate")
		}
		conn, err = cacheengine.DialTLS(address, &tls.Config{RootCAs: roots, ServerName: m.SourceAddress, MinVersion: tls.VersionTLS12}, "", "", 10*time.Second)
	} else {
		conn, err = cacheengine.Dial(address, "", 10*time.Second)
	}
	if err != nil {
		return nil, err
	}
	if reply, err := conn.Do("PING"); err != nil || reply != "PONG" {
		_ = conn.Close()
		return nil, fmt.Errorf("PING answered %v, %v", reply, err)
	}
	return conn, nil
}

// Empties the cache first when clear is set.
func ecSyncMigration(c ECServerlessCache, m ECServerlessCacheMigration, clear bool) error {
	engine, ok := ecLoadEngine(ecEngineKey(ecKindServerless, c.ServerlessCacheName))
	if !ok || engine.redis == nil {
		return fmt.Errorf("no engine serves the cache")
	}
	source, err := ecDialMigrationSource(m)
	if err != nil {
		return fmt.Errorf("connect to the source: %w", err)
	}
	defer source.Close()
	engine.opMu.Lock()
	defer engine.opMu.Unlock()
	if clear {
		if err := engine.redis.Flush(); err != nil {
			return err
		}
	}
	_, err = engine.redis.CopyFrom(source)
	return err
}
