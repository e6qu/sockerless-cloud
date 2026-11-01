package main

import (
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/cacheengine"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// Redis_ImportData and Redis_ExportData move RDB files between a cache's
// engine and Azure Blob Storage, through the Blob data plane, which
// authorizes each request as it authorizes any client's. Microsoft documents
// the contract in "Import and export data in Azure Cache for Redis"
// (specs/cloud-api/azure/redis-import-export-data.md.gz) and "Managed identity
// for storage accounts" (redis-managed-identity.md.gz):
//
//   - Import and export are available in the Premium tier only.
//   - Export writes the snapshot as an RDB page blob; Blob storage accounts and
//     Azure Data Lake Storage Gen2 accounts do not take an export.
//   - Import reads page or block blobs, deletes the cache's existing data and
//     inserts the keys of every file; a Redis 4.0 cache loads RDB version 8 and
//     below, a Redis 6.0 cache version 9 and below.
//   - With managed identity, the cache authenticates as its system-assigned
//     identity when enabled, otherwise its first user-assigned identity.
//
// Both operations are long-running with final-state-via location: the POST
// answers 202 with a Location the client polls, which answers 202 while the
// work runs, 200 once it succeeded, and the failure's error once it failed.

const (
	redisArchiveSAS             = "SAS"
	redisArchiveManagedIdentity = "ManagedIdentity"

	redisPageSize = 512
	// Put Page writes at most 4 MiB a request.
	redisPageWriteLimit = 4 << 20

	redisArchiveStorageVersion = "2021-12-02"
)

var (
	redisTransferLocksMu sync.Mutex
	redisTransferLocks   = map[string]*sync.Mutex{}
)

// redisTransferLock serializes the transfers of one cache.
func redisTransferLock(id string) *sync.Mutex {
	redisTransferLocksMu.Lock()
	defer redisTransferLocksMu.Unlock()
	lock, ok := redisTransferLocks[id]
	if !ok {
		lock = &sync.Mutex{}
		redisTransferLocks[id] = lock
	}
	return lock
}

// redisArchive is a blob or container a transfer addresses, and how the
// cache authenticates to it.
type redisArchive struct {
	scheme    string
	host      string
	account   string
	container string
	blob      string
	query     url.Values
	bearer    string
}

// redisSystemIdentityClientID is the client ID of the service principal a
// cache's system-assigned identity holds.
func redisSystemIdentityClientID(identity *SiteIdentity) string {
	if !identity.systemAssigned() || identity.PrincipalID == "" {
		return ""
	}
	sp, ok := entraServicePrincipalStore.Get(identity.PrincipalID)
	if !ok {
		return ""
	}
	return sp.AppID
}

// redisArchiveIdentity is the identity a cache reaches storage as: its
// system-assigned identity when enabled, otherwise its first user-assigned
// identity, by resource ID.
func redisArchiveIdentity(cache RedisCache) (appServiceIdentity, bool) {
	if cache.Identity.systemAssigned() {
		return selectManagedIdentity(cache.Identity, cache.ID, "", "", "")
	}
	if !cache.Identity.userAssigned() {
		return appServiceIdentity{}, false
	}
	ids := make([]string, 0, len(cache.Identity.UserAssignedIdentities))
	for rid := range cache.Identity.UserAssignedIdentities {
		ids = append(ids, rid)
	}
	sort.Strings(ids)
	for _, rid := range ids {
		if identity, ok := selectManagedIdentity(cache.Identity, cache.ID, "", "", rid); ok {
			return identity, true
		}
	}
	return appServiceIdentity{}, false
}

func redisArchiveFailure(format string, args ...any) *AsyncOperationError {
	return &AsyncOperationError{Code: "BadRequest", Message: fmt.Sprintf(format, args...)}
}

// redisResolveArchive resolves a blob or container URL to a storage account
// this simulator serves. The cache reaches storage at the URL's host as an
// Azure service does through DNS: a host outside the simulator's endpoint
// zones, at another port, or naming no storage account cannot be reached.
func redisResolveArchive(r *http.Request, raw string, wantBlob bool) (redisArchive, *AsyncOperationError) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return redisArchive{}, redisArchiveFailure("%q is not an absolute http or https URL.", raw)
	}
	hostname := strings.ToLower(u.Hostname())
	account, rest, _ := strings.Cut(hostname, ".")
	unreachable := func(reason string) (redisArchive, *AsyncOperationError) {
		return redisArchive{}, redisArchiveFailure("The storage endpoint %s cannot be reached: %s.", u.Host, reason)
	}
	if !strings.HasPrefix(rest, "blob.") || !redisSimulatorHost(r, account, hostname) {
		return unreachable("the host is not an Azure Blob Storage endpoint this cloud serves")
	}
	if redisPort(u.Scheme, u.Port()) != redisRequestPort(r) {
		return unreachable("nothing answers at port " + redisPort(u.Scheme, u.Port()))
	}
	if _, ok := azureStorageAccountByName(account); !ok {
		return unreachable(fmt.Sprintf("no storage account is named %q", account))
	}
	path := strings.Trim(u.Path, "/")
	container, blob, _ := strings.Cut(path, "/")
	switch {
	case container == "":
		return redisArchive{}, redisArchiveFailure("%q names no blob container.", raw)
	case wantBlob && blob == "":
		return redisArchive{}, redisArchiveFailure("%q names no blob.", raw)
	case !wantBlob && blob != "":
		return redisArchive{}, redisArchiveFailure("%q names a blob, not a blob container.", raw)
	}
	return redisArchive{
		scheme:    u.Scheme,
		host:      u.Host,
		account:   account,
		container: container,
		blob:      blob,
		query:     u.Query(),
	}, nil
}

// redisSimulatorHost reports whether hostname is under one of the endpoint
// zones every host of which is this simulator, or is the Blob endpoint the
// simulator advertises for account.
func redisSimulatorHost(r *http.Request, account, hostname string) bool {
	if advertised, err := url.Parse(azureStorageEndpointURL(r, account, "blob")); err == nil &&
		strings.EqualFold(advertised.Hostname(), hostname) {
		return true
	}
	cfg, err := azureDNSConfigFromEnv()
	if err != nil {
		return false
	}
	for _, zone := range cfg.Zones {
		if strings.HasSuffix(hostname, "."+zone) {
			return true
		}
	}
	return false
}

func redisPort(scheme, port string) string {
	if port != "" {
		return port
	}
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func redisRequestPort(r *http.Request) string {
	_, suffix := azureRequestHostParts(r)
	return redisPort(azureRequestScheme(r), strings.TrimPrefix(suffix, ":"))
}

// redisAuthorizeArchives settles how the cache authenticates to each archive:
// the SAS token each URL carries, or a token for the cache's managed identity,
// whose storage account must be in storageSubscription when one is named.
func redisAuthorizeArchives(cache RedisCache, method, storageSubscription string, archives []redisArchive) *AsyncOperationError {
	if method != redisArchiveManagedIdentity {
		return nil
	}
	identity, ok := redisArchiveIdentity(cache)
	if !ok {
		return redisArchiveFailure("The cache %s has no managed identity to authenticate to storage with.", cache.Name)
	}
	now := time.Now()
	token, err := azureManagedIdentityToken(identity, "https://storage.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
	}
	for i := range archives {
		if storageSubscription != "" {
			account, _ := azureStorageAccountByName(archives[i].account)
			if !strings.HasPrefix(strings.ToLower(account.ID), strings.ToLower(armSubscriptionID(storageSubscription))+"/") {
				return redisArchiveFailure("The storage account %s is not in subscription %s.", archives[i].account, storageSubscription)
			}
		}
		archives[i].bearer = token
	}
	return nil
}

// call sends one Blob service request for blob in the archive's container.
func (a redisArchive) call(cacheID, method, blob string, query url.Values, header http.Header, body []byte) (int, []byte, error) {
	q := url.Values{}
	if a.bearer == "" {
		for key, values := range a.query {
			q[key] = values
		}
	}
	for key, values := range query {
		q[key] = values
	}
	target := (&url.URL{Scheme: a.scheme, Host: a.host, Path: "/" + a.container + "/" + blob, RawQuery: q.Encode()}).String()
	if header == nil {
		header = http.Header{}
	}
	header.Set("X-Ms-Version", redisArchiveStorageVersion)
	header.Set("X-Ms-Date", time.Now().UTC().Format(http.TimeFormat))
	if a.bearer != "" {
		header.Set("Authorization", "Bearer "+a.bearer)
	}
	return azureCallAs(context.Background(), "Microsoft.Cache", cacheID, method, target, header, body)
}

// redisStorageFailure reports the error the Blob service answered with.
func redisStorageFailure(action string, status int, body []byte) *AsyncOperationError {
	var answer struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal(body, &answer); err != nil || answer.Code == "" {
		return redisArchiveFailure("%s: Azure Blob Storage answered %d %s.", action, status, http.StatusText(status))
	}
	return &AsyncOperationError{Code: answer.Code, Message: fmt.Sprintf("%s: %s", action, answer.Message)}
}

type redisTransferRequest struct {
	Format              string   `json:"format,omitempty"`
	Files               []string `json:"files,omitempty"`
	Prefix              string   `json:"prefix,omitempty"`
	Container           string   `json:"container,omitempty"`
	AuthMethod          string   `json:"preferred-data-archive-auth-method,omitempty"`
	StorageSubscription string   `json:"storage-subscription-id,omitempty"`
}

// redisTransferCache reads a transfer request against a cache that takes one,
// writing the refusal otherwise.
func redisTransferCache(w http.ResponseWriter, r *http.Request) (RedisCache, redisTransferRequest, bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req redisTransferRequest
	if !redisCacheExists(w, sub, rg, name) {
		return RedisCache{}, req, false
	}
	cache, _ := redisCaches.Get(redisCacheID(sub, rg, name))
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return cache, req, false
	}
	sku, _ := cache.Properties["sku"].(map[string]any)
	if tier, _ := sku["name"].(string); tier != "Premium" {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"Import and export are supported in the Premium tier; the cache %s is %s.", cache.Name, tier)
		return cache, req, false
	}
	if state, _ := cache.Properties["provisioningState"].(string); state != "Succeeded" {
		AzureErrorf(w, "Conflict", http.StatusConflict,
			"The cache %s is %s; it takes an import or export once it has Succeeded.", cache.Name, state)
		return cache, req, false
	}
	if req.Format != "" && !strings.EqualFold(req.Format, "RDB") {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "The format %q is not RDB.", req.Format)
		return cache, req, false
	}
	switch {
	case req.AuthMethod == "", strings.EqualFold(req.AuthMethod, redisArchiveSAS):
		req.AuthMethod = redisArchiveSAS
	case strings.EqualFold(req.AuthMethod, redisArchiveManagedIdentity):
		req.AuthMethod = redisArchiveManagedIdentity
	default:
		AzureErrorf(w, "BadRequest", http.StatusBadRequest,
			"preferred-data-archive-auth-method must be SAS or ManagedIdentity, got %q.", req.AuthMethod)
		return cache, req, false
	}
	return cache, req, true
}

// redisStartTransfer answers the POST with the operation's Location, and runs
// work unless resolving the request already failed it.
func redisStartTransfer(w http.ResponseWriter, r *http.Request, cache RedisCache, failure *AsyncOperationError, work func(*cacheengine.Plane) *AsyncOperationError) {
	opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
		if failure != nil {
			return failure
		}
		plane, ok := redisLoadEngine(cache.ID)
		if !ok {
			return &AsyncOperationError{Code: "InternalServerError",
				Message: "The cache runs no Redis engine: this simulator has no container runtime."}
		}
		lock := redisTransferLock(cache.ID)
		lock.Lock()
		defer lock.Unlock()
		if err := plane.Ensure(); err != nil {
			return &AsyncOperationError{Code: "InternalServerError", Message: "The cache's engine is not running: " + err.Error()}
		}
		return work(plane)
	})
	w.Header().Set("Location", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"), "Microsoft.Cache",
		url.PathEscape(cache.Location), "operationResults", opID, r.URL.Query().Get("api-version")))
	setAzureAsyncOperationRetryAfter(w, opID)
	w.WriteHeader(http.StatusAccepted)
}

func handleRedisExportData(w http.ResponseWriter, r *http.Request) {
	cache, req, ok := redisTransferCache(w, r)
	if !ok {
		return
	}
	if req.Container == "" || req.Prefix == "" {
		AzureError(w, "BadRequest", "container and prefix are required to export", http.StatusBadRequest)
		return
	}
	target, failure := redisResolveArchive(r, req.Container, false)
	if failure == nil {
		account, _ := azureStorageAccountByName(target.account)
		switch {
		case strings.EqualFold(account.Kind, "BlobStorage"):
			failure = redisArchiveFailure("Blob storage accounts don't support export; %s is one.", target.account)
		case account.Properties.IsHnsEnabled != nil && *account.Properties.IsHnsEnabled:
			failure = redisArchiveFailure("Azure Cache for Redis doesn't export to Azure Data Lake Storage Gen2 storage accounts; %s is one.", target.account)
		}
	}
	if failure == nil {
		archives := []redisArchive{target}
		failure = redisAuthorizeArchives(cache, req.AuthMethod, req.StorageSubscription, archives)
		target = archives[0]
	}
	redisStartTransfer(w, r, cache, failure, func(plane *cacheengine.Plane) *AsyncOperationError {
		rdb, err := plane.Snapshot(plane.Primary())
		if err != nil {
			return &AsyncOperationError{Code: "InternalServerError", Message: "Snapshot the cache: " + err.Error()}
		}
		return redisWritePageBlob(cache.ID, target, req.Prefix, rdb)
	})
}

// redisWritePageBlob writes rdb as the page blob name, zero-padded to the
// 512-byte pages a page blob is made of.
func redisWritePageBlob(cacheID string, target redisArchive, name string, rdb []byte) *AsyncOperationError {
	size := (len(rdb) + redisPageSize - 1) / redisPageSize * redisPageSize
	padded := make([]byte, size)
	copy(padded, rdb)
	status, answer, err := target.call(cacheID, http.MethodPut, name, nil, http.Header{
		"X-Ms-Blob-Type":           {"PageBlob"},
		"X-Ms-Blob-Content-Length": {strconv.Itoa(size)},
		"X-Ms-Blob-Content-Type":   {"application/octet-stream"},
	}, nil)
	if err != nil {
		return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
	}
	if status != http.StatusCreated {
		return redisStorageFailure("Create the page blob "+name, status, answer)
	}
	for offset := 0; offset < size; offset += redisPageWriteLimit {
		end := min(offset+redisPageWriteLimit, size)
		status, answer, err := target.call(cacheID, http.MethodPut, name, url.Values{"comp": {"page"}}, http.Header{
			"X-Ms-Page-Write": {"update"},
			"X-Ms-Range":      {fmt.Sprintf("bytes=%d-%d", offset, end-1)},
		}, padded[offset:end])
		if err != nil {
			return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
		}
		if status != http.StatusCreated {
			return redisStorageFailure("Write the pages of "+name, status, answer)
		}
	}
	return nil
}

func handleRedisImportData(w http.ResponseWriter, r *http.Request) {
	cache, req, ok := redisTransferCache(w, r)
	if !ok {
		return
	}
	if len(req.Files) == 0 {
		AzureError(w, "BadRequest", "at least one file is required to import", http.StatusBadRequest)
		return
	}
	var failure *AsyncOperationError
	sources := make([]redisArchive, 0, len(req.Files))
	for _, file := range req.Files {
		source, unresolved := redisResolveArchive(r, file, true)
		if unresolved != nil {
			failure = unresolved
			break
		}
		sources = append(sources, source)
	}
	if failure == nil {
		failure = redisAuthorizeArchives(cache, req.AuthMethod, req.StorageSubscription, sources)
	}
	version, _ := cache.Properties["redisVersion"].(string)
	redisStartTransfer(w, r, cache, failure, func(plane *cacheengine.Plane) *AsyncOperationError {
		files := make([][]byte, 0, len(sources))
		for _, source := range sources {
			status, data, err := source.call(cache.ID, http.MethodGet, source.blob, nil, nil, nil)
			if err != nil {
				return &AsyncOperationError{Code: "InternalServerError", Message: err.Error()}
			}
			if status != http.StatusOK {
				return redisStorageFailure("Read "+source.blob, status, data)
			}
			if failure := redisCheckRDB(source.blob, data, version); failure != nil {
				return failure
			}
			files = append(files, data)
		}
		if err := plane.Restart(files[0], ""); err != nil {
			return &AsyncOperationError{Code: "InternalServerError", Message: "Load " + sources[0].blob + ": " + err.Error()}
		}
		for i, data := range files[1:] {
			if err := redisMergeRDB(cache.ID, plane, data); err != nil {
				return &AsyncOperationError{Code: "InternalServerError", Message: "Load " + sources[i+1].blob + ": " + err.Error()}
			}
		}
		return nil
	})
}

// redisCheckRDB refuses a file that is not an RDB file, or whose RDB version
// is newer than the cache's Redis version loads.
func redisCheckRDB(name string, data []byte, redisVersion string) *AsyncOperationError {
	if !cacheengine.IsRDB(data) || len(data) < 9 {
		return redisArchiveFailure("%s is not an RDB file.", name)
	}
	format, err := strconv.Atoi(string(data[5:9]))
	if err != nil {
		return redisArchiveFailure("%s is not an RDB file.", name)
	}
	newest := 9
	if redisVersion == "4.0" {
		newest = 8
	}
	if format > newest {
		return redisArchiveFailure("%s is RDB version %d; a Redis %s cache imports RDB version %d and below.",
			name, format, redisVersion, newest)
	}
	return nil
}

// redisMergeRDB inserts the keys of an RDB file into the cache: a standalone
// redis-server beside the engine loads the file, and the engine copies its
// keys, each with its remaining time to live.
func redisMergeRDB(id string, plane *cacheengine.Plane, data []byte) error {
	record, ok := redisEngineRecords.Get(id)
	if !ok {
		return fmt.Errorf("the cache has no engine record")
	}
	backend, err := azurePlacementBackend(placementRedisCache)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	network, resolvers, err := redisEngineNetwork(ctx)
	if err != nil {
		return err
	}
	const directory = "/import"
	ready := make(chan struct{})
	var once sync.Once
	handle, err := backend.Place(ctx, placement.Workload{
		CancelGracePeriod: 5 * time.Second,
		Image:             record.Image,
		Architecture:      "linux/amd64",
		Command: []string{"redis-server", "--port", strconv.Itoa(cacheengine.Port), "--bind", "0.0.0.0",
			"--protected-mode", "no", "--dir", directory, "--dbfilename", "source.rdb", "--save", "", "--appendonly", "no"},
		PublishPorts: []int{cacheengine.Port},
		Labels:       map[string]string{"sockerless-azure-redis-import": redisScopedName(record.Owner)},
		Network:      network,
		DNS:          resolvers,
		Seed:         []sim.SeedFile{{Path: directory + "/source.rdb", Data: data}},
		Sandbox:      SandboxACA,
	}, sim.FuncSink(func(line sim.LogLine) {
		if strings.Contains(strings.ToLower(line.Text), "ready to accept connections") {
			once.Do(func() { close(ready) })
		}
	}))
	if err != nil {
		return fmt.Errorf("place the file's loader: %w", err)
	}
	defer func() {
		handle.Cancel()
		_ = handle.Wait()
	}()
	select {
	case <-ready:
	case <-handle.Exited():
		return fmt.Errorf("the RDB file did not load: redis-server exited with status %d", handle.Wait().ExitCode)
	case <-ctx.Done():
		return fmt.Errorf("the RDB file did not load: %w", ctx.Err())
	}
	port, err := handle.PublishedPort(ctx, cacheengine.Port)
	if err != nil {
		return err
	}
	source, err := cacheengine.Dial(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "", 5*time.Second)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = plane.CopyFrom(source)
	return err
}

// handleCacheOperationResult serves the Location of a Microsoft.Cache
// operation whose final state comes via Location: 202 while it runs, 200 once
// it succeeded, and its error once it failed. A failure answers 400, since a
// client reads 5xx on a poll as transient and polls on.
func handleCacheOperationResult(w http.ResponseWriter, r *http.Request) {
	opID := sim.PathParam(r, "opId")
	op, ok := azureAsyncOps.Get(opID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found.", opID)
		return
	}
	switch op.Status {
	case "InProgress":
		w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
		w.WriteHeader(http.StatusAccepted)
	case "Succeeded":
		w.WriteHeader(http.StatusOK)
	default:
		failure := op.Error
		if failure == nil {
			failure = &AsyncOperationError{Code: "InternalServerError", Message: "The operation failed."}
		}
		sim.WriteJSON(w, http.StatusBadRequest, map[string]any{"error": failure})
	}
}
