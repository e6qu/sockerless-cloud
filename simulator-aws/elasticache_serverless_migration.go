package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
)

// A serverless cache migration opens with PrepareMigration from one node of
// a cluster-mode source and ends with CompleteMigration. Status follows the
// states the ElastiCache Developer Guide lists: ready-to-sync once prepared,
// initiating-sync while each source shard's full synchronisation loads,
// syncing while every write the source takes is applied to the cache,
// stopping, and cutting-over. A replication link that breaks marks the
// migration failed for good: StartMigration and CompleteMigration without
// Force then answer MigrationHealthFault.
type ECServerlessCacheMigration struct {
	SourceAddress        string
	SourcePort           int    `json:",omitempty"`
	SourceSecretArn      string `json:",omitempty"`
	SourceTlsMode        string `json:",omitempty"`
	SourceTlsCertificate string `json:",omitempty"`
	Status               string
	AutoCutover          bool `json:",omitempty"`
	Failed               bool `json:",omitempty"`
	// PrepareRefusal is why the caller's own read of the secret failed,
	// which the prepare step reports.
	PrepareRefusal string `json:",omitempty"`
}

const (
	ecMigrationPreparing      = "preparing"
	ecMigrationReadyToSync    = "ready-to-sync"
	ecMigrationInitiatingSync = "initiating-sync"
	ecMigrationSyncing        = "syncing"
	ecMigrationCuttingOver    = "cutting-over"
	ecMigrationStopping       = "stopping"
	// How long a cutover holds the source's writers while the sync catches
	// up; the guide gives no figure.
	ecCutoverPause = 10 * time.Second
)

// The guide spells the modes Manual cutover and Auto-cutover and names no
// wire value.
func ecCutoverMode(m ECServerlessCacheMigration) string {
	if m.AutoCutover {
		return "auto"
	}
	return "manual"
}

func renderECServerlessCacheMigration(c ECServerlessCache) string {
	if c.Migration == nil {
		return ""
	}
	return fmt.Sprintf("<Migration><Status>%s</Status><MigrationEndpoint><Address>%s</Address><Port>%d</Port></MigrationEndpoint><CutoverMode>%s</CutoverMode></Migration>",
		xmlEscape(c.Migration.Status), xmlEscape(c.EndpointAddress), c.EndpointPort, ecCutoverMode(*c.Migration))
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

func ecMigrationEndpoints(r *http.Request) []ecMigrationSource {
	var endpoints []ecMigrationSource
	for n := 1; ; n++ {
		prefix := fmt.Sprintf("CustomerNodeEndpointList.member.%d", n)
		address, port := r.FormValue(prefix+".Address"), r.FormValue(prefix+".Port")
		if address == "" && port == "" {
			return endpoints
		}
		endpoints = append(endpoints, ecMigrationSource{Address: address, Port: atoiOrZero(port)})
	}
}

// The prepare step's checks of the certificate value.
func ecCheckSourceCertificate(value string) string {
	if len(value) > 5000 {
		return "SourceTlsCertificate is longer than 5,000 characters."
	}
	rest := []byte(value)
	found := false
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			return "SourceTlsCertificate contains a private key."
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Sprintf("SourceTlsCertificate holds a %s block, not a certificate.", block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Sprintf("SourceTlsCertificate is not a valid X.509 certificate: %v", err)
		}
		now := time.Now()
		if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
			return "SourceTlsCertificate holds a certificate that is expired or not yet valid."
		}
		found = true
	}
	if !found || strings.TrimSpace(string(rest)) != "" {
		return "SourceTlsCertificate is not a PEM-encoded X.509 certificate."
	}
	return ""
}

// The credentials the secret holds, read through Secrets Manager:
// {"username":…,"password":…} or {"password":…}. The message says why the
// secret does not serve.
// ecSourceCredentials reads the secret as ElastiCache does, with its own
// service credentials: the secret's resource policy and its key's policy must
// admit elasticache.amazonaws.com acting for the cache.
func ecSourceCredentials(arn, cacheARN string) (user, password, message string) {
	secret, ok := resolveSMSecret(arn)
	if !ok || secret.DeletedDate != 0 {
		return "", "", fmt.Sprintf("Access to the secret %s was denied.", arn)
	}
	if secret.KmsKeyId == "" || secret.KmsKeyId == "alias/aws/secretsmanager" {
		return "", "", fmt.Sprintf("The secret %s is encrypted with the AWS managed key for Secrets Manager, which cannot be granted to ElastiCache; encrypt it with a customer managed key.", arn)
	}
	service := iamServiceSource{Service: "elasticache.amazonaws.com", SourceArn: cacheARN, SourceAccount: awsAccountID()}
	if !iamAuthorizeServiceDelivery(secret.ARN, "secretsmanager:GetSecretValue", service) {
		return "", "", fmt.Sprintf("Access to the secret %s was denied.", arn)
	}
	keyID, ok := resolveKMSKey(secret.KmsKeyId)
	if !ok || !iamAuthorizeServiceDelivery(kmsKeyArn(keyID), "kms:Decrypt", service) {
		return "", "", fmt.Sprintf("Access to the secret %s was denied.", arn)
	}
	version, ok := secret.versionByIDOrStage("", "")
	if !ok || version.SecretString == "" {
		return "", "", fmt.Sprintf("The secret %s is empty.", arn)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(version.SecretString), &fields); err != nil {
		return "", "", fmt.Sprintf("The secret %s is not valid JSON.", arn)
	}
	password, _ = fields["password"].(string)
	if password == "" {
		return "", "", fmt.Sprintf("The secret %s has no password field.", arn)
	}
	user, _ = fields["username"].(string)
	if len(password) < 16 || len(password) > 128 {
		return "", "", "The source password must be 16-128 characters."
	}
	if _, named := fields["username"]; named {
		if len(user) < 1 || len(user) > 120 || strings.IndexFunc(user, unicode.IsSpace) >= 0 {
			return "", "", "The source user name must be 1-120 characters with no whitespace."
		}
		if strings.ContainsAny(password, `,"/@`) {
			return "", "", "The source password cannot contain the characters , \" / @."
		}
		return user, password, ""
	}
	for _, r := range password {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("!&#$^<>-", r) {
			return "", "", "The source password can contain only letters, digits, and the characters ! & # $ ^ < > -."
		}
	}
	return "", password, ""
}

// The source as a migration dials it, with the secret's credentials.
func ecServerlessMigrationSource(m ECServerlessCacheMigration, cacheARN string) (ecMigrationSource, string) {
	source := ecMigrationSource{
		Address: m.SourceAddress, Port: m.SourcePort,
		TLSMode: m.SourceTlsMode, TLSCertificate: m.SourceTlsCertificate,
	}
	if m.SourceSecretArn != "" {
		user, password, message := ecSourceCredentials(m.SourceSecretArn, cacheARN)
		if message != "" {
			return source, message
		}
		source.User, source.Password = user, password
	}
	return source, ""
}

// The source requirements the prepare step checks: a Valkey 7.2 through 8.1
// or Redis OSS 4.0 through 7.2 source, in cluster mode, at a version no
// higher than the target's.
func ecCheckMigrationSource(conn *cacheengine.Conn, target ECServerlessCache) string {
	server, err := conn.Do("INFO", "server")
	if err != nil {
		return fmt.Sprintf("The source does not answer INFO: %v", err)
	}
	fields := cacheengine.ParseInfo(fmt.Sprint(server))
	version := ecInfoVersion(fmt.Sprint(server))
	switch {
	case fields["valkey_version"] != "":
		if !ecVersionAtLeast(version, "7.2") || ecCompareVersions(ecMajorMinor(version), "8.1") > 0 {
			return fmt.Sprintf("The source runs Valkey %s; a migration reads Valkey 7.2 through 8.1.", version)
		}
	default:
		if !ecVersionAtLeast(version, "4.0") || ecCompareVersions(ecMajorMinor(version), "7.2") > 0 {
			return fmt.Sprintf("The source runs Redis OSS %s; a migration reads Redis OSS 4.0 through 7.2.", version)
		}
	}
	if ecCompareVersions(ecMajorMinor(version), ecMajorMinor(target.FullEngineVersion)) > 0 {
		return fmt.Sprintf("The source version %s is higher than the target's %s.", version, target.FullEngineVersion)
	}
	cluster, err := conn.Do("INFO", "cluster")
	if err != nil || cacheengine.ParseInfo(fmt.Sprint(cluster))["cluster_enabled"] != "1" {
		return "The source does not run in cluster mode; a cluster-mode-disabled source is rejected."
	}
	return ""
}

func ecMajorMinor(version string) string {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return version
	}
	return parts[0] + "." + parts[1]
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
	endpoints := ecMigrationEndpoints(r)
	if len(endpoints) != 1 {
		invalid(fmt.Sprintf("CustomerNodeEndpointList names %d endpoints; a serverless cache migration takes exactly one.", len(endpoints)))
		return
	}
	migration := ECServerlessCacheMigration{
		SourceAddress:        endpoints[0].Address,
		SourcePort:           endpoints[0].Port,
		SourceSecretArn:      r.FormValue("SourceAuthenticationConfiguration.SecretManagerConfig.SecretArn"),
		SourceTlsMode:        r.FormValue("SourceTlsConfiguration.SourceTlsMode"),
		SourceTlsCertificate: r.FormValue("SourceTlsConfiguration.SourceTlsCertificate"),
		Status:               ecMigrationReadyToSync,
	}
	if migration.SourceAddress == "" {
		invalid("The source endpoint needs an Address.")
		return
	}
	switch migration.SourceTlsMode {
	case "", "none":
		if migration.SourceTlsCertificate != "" {
			invalid("Provide SourceTlsCertificate only with SourceTlsMode verify-ca.")
			return
		}
	case "verify-ca":
		if migration.SourceTlsCertificate == "" {
			invalid("SourceTlsMode verify-ca requires SourceTlsCertificate.")
			return
		}
		if message := ecCheckSourceCertificate(migration.SourceTlsCertificate); message != "" {
			invalid(message)
			return
		}
	default:
		invalid(fmt.Sprintf("Invalid SourceTlsMode %q. Valid values: none | verify-ca.", migration.SourceTlsMode))
		return
	}
	if c.Engine == "memcached" {
		ecErrorXML(w, "InvalidParameterCombination", "A serverless cache migration moves a Valkey or Redis OSS dataset.", http.StatusBadRequest, requestID)
		return
	}
	if c.Engine != "valkey" {
		invalid("The target of a migration is an ElastiCache Serverless for Valkey cache.")
		return
	}
	if c.NetworkType != "" && c.NetworkType != "ipv4" {
		invalid(fmt.Sprintf("The target cache uses the %s network type; a migration target uses IPv4.", c.NetworkType))
		return
	}
	migration.Status = ecMigrationPreparing
	migration.PrepareRefusal = ecCallerSecretRefusal(r, migration.SourceSecretArn)
	ecServerlessCaches.Update(c.ServerlessCacheName, func(stored *ECServerlessCache) { stored.Migration = &migration })
	updated, _ := ecServerlessCaches.Get(c.ServerlessCacheName)
	name := c.ServerlessCacheName
	bg.Go(func() { ecPrepareServerlessMigration(name) })
	ecXMLResponse(w, "PrepareMigration", renderECServerlessCache(updated), requestID)
}

// ecCallerSecretRefusal is why the caller may not read the secret itself:
// ElastiCache first reads it with the caller's credentials.
func ecCallerSecretRefusal(r *http.Request, arn string) string {
	if arn == "" {
		return ""
	}
	secret, ok := resolveSMSecret(arn)
	if !ok {
		return ""
	}
	targets := [][2]string{{"secretsmanager:GetSecretValue", secret.ARN}}
	if keyID, ok := resolveKMSKey(secret.KmsKeyId); ok {
		targets = append(targets, [2]string{"kms:Decrypt", kmsKeyArn(keyID)})
	}
	for _, target := range targets {
		if _, allowed, registered := iamAuthorizeDenial(r, target[0], target[1], nil); registered && !allowed {
			return fmt.Sprintf("Access to the secret %s was denied.", arn)
		}
	}
	return ""
}

// ecPrepareServerlessMigration validates the source and the credentials as
// the prepare step does. A failure logs an event that names the reason and
// rolls the migration back, removing it; success leaves it ready-to-sync with
// the cache's endpoint serving from the source.
func ecPrepareServerlessMigration(name string) {
	c, ok := ecServerlessCaches.Get(name)
	if !ok || c.Migration == nil || c.Migration.Status != ecMigrationPreparing {
		return
	}
	migration := *c.Migration
	fail := func(message string) {
		removed := false
		ecServerlessCaches.Update(name, func(stored *ECServerlessCache) {
			if stored.Migration != nil && stored.Migration.Status == ecMigrationPreparing {
				stored.Migration, removed = nil, true
			}
		})
		if removed {
			ecRecordEvent("serverless-cache", name, "Migration preparation failed: "+message)
		}
	}
	if migration.PrepareRefusal != "" {
		fail(migration.PrepareRefusal)
		return
	}
	source, message := ecServerlessMigrationSource(migration, c.ARN)
	if message != "" {
		fail(message)
		return
	}
	conn, err := source.dial()
	if err != nil {
		fail(fmt.Sprintf("Unable to connect to the source %s: %v", source.endpoint(), err))
		return
	}
	message = ecCheckMigrationSource(conn, c)
	_ = conn.Close()
	if message != "" {
		fail(message)
		return
	}
	engine, running := ecLoadEngine(ecEngineKey(ecKindServerless, name))
	if running && engine.redis != nil {
		target, err := ecNewMigrationTarget(engine)
		if err == nil {
			var empty bool
			if empty, err = target.empty(); err == nil && !empty {
				fail("The target cache contains data; flush it before you prepare the migration.")
				return
			}
		}
		if err != nil {
			fail(fmt.Sprintf("The target cache does not answer: %v", err))
			return
		}
	}
	if !ecMoveMigration(name, []string{ecMigrationPreparing}, func(m *ECServerlessCacheMigration) {
		m.Status, m.PrepareRefusal = ecMigrationReadyToSync, ""
	}) {
		return
	}
	ecDivertToSource(name, migration)
	ecRecordEvent("serverless-cache", name, "Migration prepared")
}

// ecDivertToSource has the cache's endpoint serve from the source, as the
// migration endpoint does until cutover; client credentials go to the source
// as the client sends them.
func ecDivertToSource(name string, m ECServerlessCacheMigration) {
	engine, ok := ecLoadEngine(ecEngineKey(ecKindServerless, name))
	if !ok || engine.redis == nil {
		return
	}
	source := ecMigrationSource{Address: m.SourceAddress, Port: m.SourcePort, TLSMode: m.SourceTlsMode, TLSCertificate: m.SourceTlsCertificate}
	engine.redis.Divert(&cacheengine.Diversion{
		Seed: source.endpoint(),
		Dial: func(address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			return source.at(host, atoiOrZero(port)).dialRaw()
		},
	})
}

// ecServeFromCache ends the diversion: the endpoint serves the cache.
func ecServeFromCache(name string) {
	if engine, ok := ecLoadEngine(ecEngineKey(ecKindServerless, name)); ok && engine.redis != nil {
		engine.redis.Divert(nil)
	}
}

// Moves the migration from one status to another, and reports whether it was
// in from.
func ecMoveMigration(name string, from []string, change func(*ECServerlessCacheMigration)) bool {
	moved := false
	ecServerlessCaches.Update(name, func(c *ECServerlessCache) {
		if c.Migration == nil {
			return
		}
		for _, status := range from {
			if c.Migration.Status == status {
				change(c.Migration)
				moved = true
				return
			}
		}
	})
	return moved
}

func ecEndServerlessMigration(name string) {
	ecServerlessCaches.Update(name, func(c *ECServerlessCache) { c.Migration = nil })
}

// The full synchronisation runs in the background; the migration reports
// syncing once every shard follows the source, and returns to ready-to-sync
// when the start fails: for good when the source cannot be read, for now when
// the cache holds data ClearTargetCacheData did not clear.
func ecStartServerlessMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	if c.Migration.Failed {
		ecErrorXML(w, "MigrationHealthFault",
			fmt.Sprintf("The migration of serverless cache %s reported an error; complete it with Force to end it.", c.ServerlessCacheName), http.StatusBadRequest, requestID)
		return
	}
	if c.Migration.Status != ecMigrationReadyToSync {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is %s, not ready-to-sync.", c.ServerlessCacheName, c.Migration.Status), http.StatusBadRequest, requestID)
		return
	}
	autoCutover := ecRequestedBool(r, "AutoCutover")
	clear := ecRequestedBool(r, "ClearTargetCacheData")
	name := c.ServerlessCacheName
	ecMoveMigration(name, []string{ecMigrationReadyToSync}, func(m *ECServerlessCacheMigration) {
		m.Status, m.AutoCutover = ecMigrationInitiatingSync, autoCutover
	})
	updated, _ := ecServerlessCaches.Get(name)
	migration := *updated.Migration
	bg.Go(func() { ecRunServerlessSync(name, migration, clear) })
	ecXMLResponse(w, "StartMigration", renderECServerlessCache(updated), requestID)
}

func ecRunServerlessSync(name string, migration ECServerlessCacheMigration, clear bool) {
	key := ecEngineKey(ecKindServerless, name)
	back := func(failed bool, err error) {
		log.Printf("Amazon ElastiCache serverless cache %s: the data sync did not start: %v", name, err)
		ecRecordEvent("serverless-cache", name, fmt.Sprintf("Migration data sync failed to start: %v", err))
		ecMoveMigration(name, []string{ecMigrationInitiatingSync}, func(m *ECServerlessCacheMigration) {
			m.Status, m.AutoCutover = ecMigrationReadyToSync, false
			m.Failed = m.Failed || failed
		})
	}
	engine, ok := ecLoadEngine(key)
	if !ok || engine.redis == nil {
		back(true, fmt.Errorf("no engine serves the cache"))
		return
	}
	c, _ := ecServerlessCaches.Get(name)
	source, message := ecServerlessMigrationSource(migration, c.ARN)
	if message != "" {
		back(true, fmt.Errorf("%s", message))
		return
	}
	engine.opMu.Lock()
	target, err := ecNewMigrationTarget(engine)
	if err == nil && clear {
		err = engine.redis.Flush()
	} else if err == nil {
		var empty bool
		if empty, err = target.empty(); err == nil && !empty {
			engine.opMu.Unlock()
			back(false, fmt.Errorf("the target is not empty"))
			return
		}
	}
	var run *ecMigrationRun
	if err == nil {
		run, err = ecStartMigrationRun(key, target, source, func(err error) {
			log.Printf("Amazon ElastiCache serverless cache %s: the data sync failed: %v", name, err)
			ecRecordEvent("serverless-cache", name, fmt.Sprintf("Migration data sync failed: %v", err))
			ecServerlessCaches.Update(name, func(c *ECServerlessCache) {
				if c.Migration != nil {
					c.Migration.Failed = true
				}
			})
		})
	}
	engine.opMu.Unlock()
	if err != nil {
		back(true, err)
		return
	}
	auto := false
	if !ecMoveMigration(name, []string{ecMigrationInitiatingSync}, func(m *ECServerlessCacheMigration) {
		m.Status = ecMigrationSyncing
		auto = m.AutoCutover
		if auto {
			m.Status = ecMigrationCuttingOver
		}
	}) {
		ecStopMigrationRun(key)
		return
	}
	ecRecordEvent("serverless-cache", name, "Migration data sync started")
	if auto {
		ecCutoverServerless(name, run)
	}
}

// With automatic cutover, a cutover that finds no fully synced window times
// out and leaves the migration to CompleteMigration.
func ecCutoverServerless(name string, run *ecMigrationRun) {
	key := ecEngineKey(ecKindServerless, name)
	ecRecordEvent("serverless-cache", name, "Migration cutover started")
	if err := run.cutover(ecCutoverPause, func() { ecServeFromCache(name) }); err != nil {
		log.Printf("Amazon ElastiCache serverless cache %s: the cutover did not complete: %v", name, err)
		ecRecordEvent("serverless-cache", name, fmt.Sprintf("Migration completion failed: %v", err))
		ecMoveMigration(name, []string{ecMigrationCuttingOver}, func(m *ECServerlessCacheMigration) {
			m.Status, m.AutoCutover = ecMigrationSyncing, false
		})
		return
	}
	ecMigrationRuns.Delete(key)
	ecEndServerlessMigration(name)
	ecRecordEvent("serverless-cache", name, "Migration completed")
}

func handleECStopMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	name := c.ServerlessCacheName
	if !ecMoveMigration(name, []string{ecMigrationSyncing}, func(m *ECServerlessCacheMigration) { m.Status = ecMigrationStopping }) {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is %s, not syncing.", name, c.Migration.Status), http.StatusBadRequest, requestID)
		return
	}
	updated, _ := ecServerlessCaches.Get(name)
	bg.Go(func() {
		ecStopMigrationRun(ecEngineKey(ecKindServerless, name))
		if ecMoveMigration(name, []string{ecMigrationStopping}, func(m *ECServerlessCacheMigration) {
			m.Status, m.AutoCutover = ecMigrationReadyToSync, false
		}) {
			ecRecordEvent("serverless-cache", name, "Migration data sync stopped")
		}
	})
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

// A healthy syncing migration with manual cutover cuts over; Force ends any
// migration not already cutting over at once, without the health check.
func ecCompleteServerlessMigration(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	c, ok := ecServerlessCacheForMigration(w, r)
	if !ok || !ecRequireMigration(w, r, c) {
		return
	}
	name, m := c.ServerlessCacheName, *c.Migration
	if m.Status == ecMigrationPreparing {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is preparing.", name), http.StatusBadRequest, requestID)
		return
	}
	if m.Status == ecMigrationCuttingOver {
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is already cutting over.", name), http.StatusBadRequest, requestID)
		return
	}
	if ecRequestedBool(r, "Force") {
		ecStopMigrationRun(ecEngineKey(ecKindServerless, name))
		ecServeFromCache(name)
		ecEndServerlessMigration(name)
		ecRecordEvent("serverless-cache", name, "Migration completed")
		updated, _ := ecServerlessCaches.Get(name)
		ecXMLResponse(w, "CompleteMigration", renderECServerlessCache(updated), requestID)
		return
	}
	switch {
	case m.Failed:
		ecErrorXML(w, "MigrationHealthFault",
			fmt.Sprintf("The migration of serverless cache %s is not healthy; set Force to true to end it.", name), http.StatusBadRequest, requestID)
		return
	case m.Status == ecMigrationReadyToSync || (m.Status == ecMigrationSyncing && m.AutoCutover):
		ecErrorXML(w, "InvalidParameterValue",
			fmt.Sprintf("The migration of serverless cache %s is %s with %s cutover; set Force to true to end it.", name, m.Status, ecCutoverMode(m)), http.StatusBadRequest, requestID)
		return
	case m.Status != ecMigrationSyncing:
		ecErrorXML(w, "InvalidServerlessCacheStateFault",
			fmt.Sprintf("The migration of serverless cache %s is %s, not syncing.", name, m.Status), http.StatusBadRequest, requestID)
		return
	}
	run, running := ecLoadMigrationRun(ecEngineKey(ecKindServerless, name))
	if !running || !ecMoveMigration(name, []string{ecMigrationSyncing}, func(m *ECServerlessCacheMigration) { m.Status = ecMigrationCuttingOver }) {
		ecErrorXML(w, "MigrationHealthFault",
			fmt.Sprintf("The migration of serverless cache %s is not syncing data.", name), http.StatusBadRequest, requestID)
		return
	}
	updated, _ := ecServerlessCaches.Get(name)
	bg.Go(func() { ecCutoverServerless(name, run) })
	ecXMLResponse(w, "CompleteMigration", renderECServerlessCache(updated), requestID)
}

// A replication group's online migration: without cluster mode its primary
// becomes a replica of the source with REPLICAOF, as the guide describes;
// Redis Cluster refuses REPLICAOF, so with cluster mode each source shard's
// writes reach the target through a replication link.
type ecReplGroupMigration struct {
	Address string `json:"address"`
	Port    int    `json:"port,omitempty"`
	Cluster bool   `json:"cluster,omitempty"`
}

var (
	ecReplGroupMigrations sim.Store[ecReplGroupMigration]
	ecSourceRelays        sync.Map // replication group id -> *ecSourceRelay
)

func ecOpenMigrationStores(srv *sim.Server) {
	ecReplGroupMigrations = sim.MakeStore[ecReplGroupMigration](srv.DB(), "elasticache_replication_group_migrations")
}

// Answers the error itself and reports false: the group must exist, be
// available, run without encryption in transit and name one source endpoint
// that answers.
func ecCheckReplGroupMigration(w http.ResponseWriter, r *http.Request) (ECReplicationGroup, ecMigrationSource, bool) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	g, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %s not found.", id), http.StatusNotFound, requestID)
		return g, ecMigrationSource{}, false
	}
	if _, migrating := ecReplGroupMigrations.Get(id); migrating {
		ecErrorXML(w, "ReplicationGroupAlreadyUnderMigrationFault", fmt.Sprintf("Replication group %s is already under migration.", id), http.StatusBadRequest, requestID)
		return g, ecMigrationSource{}, false
	}
	if g.Status != "available" {
		ecInvalidReplGroupState(w, r, g)
		return g, ecMigrationSource{}, false
	}
	endpoints := ecMigrationEndpoints(r)
	if len(endpoints) != 1 || endpoints[0].Address == "" {
		ecErrorXML(w, "InvalidParameterValue", "CustomerNodeEndpointList must name exactly one endpoint with an Address.", http.StatusBadRequest, requestID)
		return g, ecMigrationSource{}, false
	}
	if g.TransitEncryptionEnabled {
		ecErrorXML(w, "InvalidParameterCombination", "A migration target does not have encryption in transit enabled.", http.StatusBadRequest, requestID)
		return g, ecMigrationSource{}, false
	}
	if !ecRunsEngines() {
		ecErrorXML(w, "InvalidParameterCombination", "No engine can replicate the source: this simulator runs no Amazon ElastiCache data plane.", http.StatusBadRequest, requestID)
		return g, ecMigrationSource{}, false
	}
	conn, err := endpoints[0].dial()
	if err != nil {
		ecErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Unable to connect to the source %s: %v", endpoints[0].endpoint(), err), http.StatusBadRequest, requestID)
		return g, ecMigrationSource{}, false
	}
	_ = conn.Close()
	return g, endpoints[0], true
}

// Validates the setup without migrating any data.
func handleECTestMigration(w http.ResponseWriter, r *http.Request) {
	g, _, ok := ecCheckReplGroupMigration(w, r)
	if !ok {
		return
	}
	ecXMLResponse(w, "TestMigration", renderECReplGroup(g), sim.RequestID(r.Context()))
}

// The group reports migrating; when its primaries cannot follow the source
// it is available again.
func handleECStartMigration(w http.ResponseWriter, r *http.Request) {
	if serverless, ok := ecServerlessMigrationTarget(w, r); !ok || serverless {
		if ok {
			ecStartServerlessMigration(w, r)
		}
		return
	}
	g, source, ok := ecCheckReplGroupMigration(w, r)
	if !ok {
		return
	}
	id := g.ReplicationGroupId
	ecReplGroupMigrations.Put(id, ecReplGroupMigration{Address: source.Address, Port: source.Port, Cluster: g.ClusterEnabled})
	ecReplGroups.Update(id, func(stored *ECReplicationGroup) { stored.Status = "migrating" })
	updated, _ := ecReplGroups.Get(id)
	ecFollowSource(id, g.Incarnation)
	ecXMLResponse(w, "StartMigration", renderECReplGroup(updated), sim.RequestID(r.Context()))
}

// Starts replication from the recorded source; the target's data goes, as
// replication replaces it with the source's.
func ecFollowSource(id, incarnation string) {
	migration, _ := ecReplGroupMigrations.Get(id)
	source := ecMigrationSource{Address: migration.Address, Port: migration.Port}
	key := ecEngineKey(ecKindReplicationGroup, id)
	engine, _ := ecLoadEngine(key)
	ecRunInBackground(engine, func(e *ecEngine) error {
		if !migration.Cluster {
			relay, err := ecOpenSourceRelay(source)
			if err != nil {
				return err
			}
			if err := ecPrimaryMasterAuth(e, false); err != nil {
				relay.Close()
				return err
			}
			if _, err := e.redis.Command(e.redis.Primary(), "REPLICAOF", relay.host, fmt.Sprint(relay.port)); err != nil {
				relay.Close()
				return err
			}
			if previous, loaded := ecSourceRelays.Swap(id, relay); loaded {
				if previous, ok := previous.(*ecSourceRelay); ok {
					previous.Close()
				}
			}
			return nil
		}
		// The group follows the source as replicas would: it takes no
		// writes until the migration completes.
		e.redis.RefuseWrites(true)
		target, err := ecNewMigrationTarget(e)
		if err != nil {
			return err
		}
		control, err := source.dial()
		if err != nil {
			return err
		}
		layout, err := ecSourceSlotLayout(control)
		_ = control.Close()
		if err != nil {
			return err
		}
		if len(layout) != len(target.primaries) {
			return fmt.Errorf("the source runs %d shards and the target %d", len(layout), len(target.primaries))
		}
		if err := e.redis.Flush(); err != nil {
			return err
		}
		if err := e.redis.AssignSlots(ecPairSlotLayout(target, layout)); err != nil {
			return fmt.Errorf("reshard to the source's slot layout: %w", err)
		}
		if target, err = ecNewMigrationTarget(e); err != nil {
			return err
		}
		_, err = ecStartMigrationRun(key, target, source, func(err error) {
			log.Printf("Amazon ElastiCache replication group %s: the migration's replication failed: %v", id, err)
			ecRecordEvent("replication-group", id, fmt.Sprintf("Migration replication failed: %v", err))
		})
		return err
	}, func(err error) {
		if err == nil {
			ecRecordEvent("replication-group", id, "Migration started")
			return
		}
		log.Printf("Amazon ElastiCache replication group %s: the migration did not start: %v", id, err)
		ecRecordEvent("replication-group", id, fmt.Sprintf("Migration failed to start: %v", err))
		if engine != nil && engine.redis != nil {
			engine.redis.RefuseWrites(false)
		}
		ecReplGroupMigrations.Delete(id)
		ecRefreshReplGroup(id, incarnation, engine)
	})
}

// ecPairSlotLayout gives each target primary, in the order of its lowest
// slot, the slots of the source primary in the same place of the source's
// order.
func ecPairSlotLayout(target *ecMigrationTarget, layout [][]int) map[int][]int {
	lowest := map[int]int{}
	for slot := cacheengine.ClusterSlots - 1; slot >= 0; slot-- {
		lowest[target.owners[slot]] = slot
	}
	primaries := slices.Clone(target.primaries)
	slices.SortFunc(primaries, func(a, b int) int { return lowest[a] - lowest[b] })
	want := map[int][]int{}
	for i, node := range primaries {
		want[node] = layout[i]
	}
	return want
}

// ecPrimaryMasterAuth sets the credentials the primary replicates with. The
// migration guide requires a source without AUTH, so the primary follows it
// with none, and takes the plane's admin user back once it leads again.
func ecPrimaryMasterAuth(e *ecEngine, own bool) error {
	user, password := "", ""
	if own && e.redis.Password() != "" {
		user, password = ecAdminUser, e.redis.Password()
	}
	_, err := e.redis.Command(e.redis.Primary(), "CONFIG", "SET", "masteruser", user, "masterauth", password)
	return err
}

func ecStopFollowingSource(id string) {
	if value, loaded := ecSourceRelays.LoadAndDelete(id); loaded {
		if relay, ok := value.(*ecSourceRelay); ok {
			relay.Close()
		}
	}
	ecStopMigrationRun(ecEngineKey(ecKindReplicationGroup, id))
}

// Without Force, the primaries must be in sync with the source. Each then
// stops replicating and takes writes again; the group is available.
func handleECCompleteMigration(w http.ResponseWriter, r *http.Request) {
	if serverless, ok := ecServerlessMigrationTarget(w, r); !ok || serverless {
		if ok {
			ecCompleteServerlessMigration(w, r)
		}
		return
	}
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("ReplicationGroupId"))
	g, ok := ecReplGroups.Get(id)
	if !ok {
		ecErrorXML(w, "ReplicationGroupNotFoundFault", fmt.Sprintf("Replication group %s not found.", id), http.StatusNotFound, requestID)
		return
	}
	migration, migrating := ecReplGroupMigrations.Get(id)
	if !migrating || g.Status != "migrating" {
		ecErrorXML(w, "ReplicationGroupNotUnderMigrationFault", fmt.Sprintf("Replication group %s is not under migration.", id), http.StatusBadRequest, requestID)
		return
	}
	key := ecEngineKey(ecKindReplicationGroup, id)
	engine, _ := ecLoadEngine(key)
	force := ecRequestedBool(r, "Force")
	if !force && engine != nil {
		if message := ecReplGroupMigrationHealth(engine, migration); message != "" {
			ecErrorXML(w, "MigrationHealthFault", message, http.StatusBadRequest, requestID)
			return
		}
	}
	ecRunInBackground(engine, func(e *ecEngine) error {
		if !migration.Cluster {
			if _, err := e.redis.Command(e.redis.Primary(), "REPLICAOF", "NO", "ONE"); err != nil {
				return err
			}
			ecStopFollowingSource(id)
			return ecPrimaryMasterAuth(e, true)
		}
		run, running := ecLoadMigrationRun(key)
		if !force && running {
			if err := run.cutover(ecCutoverPause, func() { e.redis.RefuseWrites(false) }); err != nil {
				return err
			}
		}
		ecStopFollowingSource(id)
		e.redis.RefuseWrites(false)
		return nil
	}, func(err error) {
		if err != nil {
			log.Printf("Amazon ElastiCache replication group %s: the migration did not complete: %v", id, err)
			ecRecordEvent("replication-group", id, fmt.Sprintf("Migration completion failed: %v", err))
			return
		}
		ecReplGroupMigrations.Delete(id)
		ecRecordEvent("replication-group", id, "Migration completed")
		ecRefreshReplGroup(id, g.Incarnation, engine)
	})
	ecXMLResponse(w, "CompleteMigration", renderECReplGroup(g), requestID)
}

// Why the group's primaries are not in sync with the source, or nothing.
func ecReplGroupMigrationHealth(engine *ecEngine, migration ecReplGroupMigration) string {
	if migration.Cluster {
		run, running := ecLoadMigrationRun(engine.key)
		if !running {
			return "The replication group is not replicating from the source; set Force to true to end the migration."
		}
		run.mu.RLock()
		failure := run.failure
		run.mu.RUnlock()
		if failure != nil {
			return fmt.Sprintf("Replication from the source failed: %v; set Force to true to end the migration.", failure)
		}
		return ""
	}
	reply, err := engine.redis.Command(engine.redis.Primary(), "INFO", "replication")
	if err != nil {
		return fmt.Sprintf("The primary does not answer: %v", err)
	}
	info := cacheengine.ParseInfo(fmt.Sprint(reply))
	if info["master_link_status"] != "up" || info["master_sync_in_progress"] != "0" {
		return "The data is not in sync: the primary's link to the source is not up; set Force to true to end the migration."
	}
	return ""
}

// A restarted simulator holds no replication link: a replication group
// follows its source again, and a serverless migration that was syncing
// reports the broken link for good.
func ecRecoverMigrations() {
	for _, g := range ecReplGroups.List() {
		if _, migrating := ecReplGroupMigrations.Get(g.ReplicationGroupId); migrating && g.Status == "migrating" && ecRunsEngines() {
			ecFollowSource(g.ReplicationGroupId, g.Incarnation)
		}
	}
	for _, c := range ecServerlessCaches.List() {
		if c.Migration == nil {
			continue
		}
		name := c.ServerlessCacheName
		if c.Migration.Status != ecMigrationPreparing && c.Migration.Status != ecMigrationCuttingOver {
			ecDivertToSource(name, *c.Migration)
		}
		switch c.Migration.Status {
		case ecMigrationPreparing:
			bg.Go(func() { ecPrepareServerlessMigration(name) })
		case ecMigrationCuttingOver:
			ecEndServerlessMigration(name)
		case ecMigrationSyncing:
			// The link the old process held is gone, and the offset it reached
			// went with it, so the sync starts over: the target is cleared and
			// takes a full synchronisation of the source as it now stands.
			if !ecRunsEngines() {
				ecServerlessCaches.Update(name, func(stored *ECServerlessCache) { stored.Migration.Failed = true })
				continue
			}
			ecMoveMigration(name, []string{ecMigrationSyncing}, func(m *ECServerlessCacheMigration) { m.Status = ecMigrationInitiatingSync })
			migration := *c.Migration
			bg.Go(func() { ecRunServerlessSync(name, migration, true) })
		case ecMigrationReadyToSync:
		case ecMigrationStopping:
			ecServerlessCaches.Update(name, func(stored *ECServerlessCache) {
				stored.Migration.Status, stored.Migration.AutoCutover = ecMigrationReadyToSync, false
			})
		default:
			ecServerlessCaches.Update(name, func(stored *ECServerlessCache) {
				stored.Migration.Status, stored.Migration.AutoCutover = ecMigrationReadyToSync, false
				stored.Migration.Failed = true
			})
		}
	}
}
