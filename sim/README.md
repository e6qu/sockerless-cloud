# sim

The framework the three cloud simulators are built on: one HTTP server with
graceful shutdown, health and console routes; a container-engine layer that
runs every workload as a real container; durable state with a generation
counter; the OCI Distribution data plane every registry slice mounts; request
logging, tracing and diagnostics; and the parsing primitives that keep
untrusted input bounds-safe.

Each simulator imports it as `github.com/e6qu/sockerless-cloud/sim` and pins it
by pseudo-version like the other support modules (see the module layout in
[`AGENTS.md`](../AGENTS.md)). The framework is cloud-neutral: a cloud's own
error shape, protocol router, sandbox profile and console coordinates live in
that cloud's module and reach the framework through the hooks below.

## Files

| File | Provides |
|------|----------|
| `server.go` | `Server`: mux registration that records every route pattern for the spec-conformance gates, `WrapHandler` for host-addressed data planes, `RegisterUI` with `ConsoleOptions`, background workers drained before SQLite closes, graceful shutdown, TLS |
| `program.go` | `Main` and `Program`: the startup every simulator binary runs — the detached reaper invocation, the parent-process watch, configuration, observability, the cloud's build, the listeners it serves beside the HTTP port, and the HTTP server until a signal, ending the process at the first step that fails |
| `config.go` | `Config` read from `SIM_*` environment variables, including the `RewriteRequest` hook a cloud uses to map virtual-hosted or case-folded addressing onto the paths the router works in |
| `middleware.go` | request id, structured request logging (5xx bodies captured), identity extraction from the caller's credential, `Flush` and `Hijack` passthrough for streaming and WebSocket handlers |
| `diagnostics.go`, `diagnostics_stores.go`, `runtime_mode.go` | in-flight request registry with slow-request reporting; per-table sizes at `/debug/stores`; `SIM_RUNTIME` resolution |
| `state.go`, `state_sqlite.go`, `state_cached.go`, `state_prune.go`, `migrate.go`, `db.go`, `db_checkpoint.go`, `index.go`, `store_scan.go` | `Store[T]` in memory, on SQLite, or cached in memory over SQLite (the persistence envelope keeps `json:"-"` fields); `PrefixStore[T]` and `ListPrefix` key-range reads; `Upsert`; `Prune` for retention sweeps; `Migrate` runs a named one-time startup conversion per database and `LegacyRows` reads a table in the row shape an earlier build wrote; write generations and `GenerationIndex` for keyed lookups; WAL checkpoints; the tracked-store set cross-cutting passes address |
| `keyed_locks.go` | per-key locks that make a conditional write's check and store one step |
| `kvstore/` | key-value and document store primitives: `Buckets` of `TokenBucket`s for provisioned throughput (`Take` refuses what the balance cannot cover; `Admit` and `Charge` let a request whose cost is known only afterwards run into debt), `RWLocks` (per-key read-write locks, multi-key calls taken in sorted order, entries reclaimed when released), `StartSweeper` for time-to-live deletion on `Server.StartBackground`, and `ChangeLog[T]` — every create, replace and delete per stream, read after a sequence number, pruned by retention |
| `container.go`, `container_reaper.go`, `container_memory.go`, `sandbox.go`, `volume_snapshot.go`, `enginebuild.go` | the container engine layer: start, adopt, stop and remove workloads, and read back the loopback ports the engine published for them (`ContainerHandle.PublishedPort`, `PublishedHostPort`); the detached reaper and startup sweep; memory-peak observation; `SandboxProfile` enforcement; crash-consistent volume snapshots (`SnapshotVolume`, `CaptureVolume`, `HoldThawed`); `EnsureEngineBuild`, which builds an embedded source tree into an image on the engine once per engine and source version |
| `cloudnet.go`, `sidecar_source.go`, `sidecar_trust.go`, `call.go` | cloud networks as engine networks over their own ranges (`EnsureCloudNetwork`), the platform network and its leased lower half (`EnsurePlatformNetwork`, `LeasePlatformAddress`); the sidecar's embedded source and the trust middleware that attributes a sidecar's request to its workload; `Server.Call` and `Caller`, an in-process request through the whole chain as a named service principal |
| `datadir.go` | `ScopedDataDir` resolves a slice's bulk-data root (override variable, `<SIM_DATA_DIR>/<subdir>`, temp); `EnsureWritableDir` makes a bind-mounted directory writable past the umask |
| `workload/` | launching and reaching workloads: `Place` and `PlaceMember` through a placement backend, `StartGroup` for a group started in `dependsOn` order behind startup checks, `LocalImagePlatform`, `PostBootstrap`, `DockerBuildInvocation` and the cancellable `DockerCommand` for image builds |
| `archive/` | `ExtractZip`, `ExtractTar` (gzip detected) and `ReadZip`: every write goes through an `os.Root`, entry names must stay local, symbolic links are recreated but never written through out of the root, and a size bound caps expansion |
| `oci.go`, `registry_credential.go`, `docker_config.go` | the Docker Registry HTTP API v2 data plane, keyed per registry scope, with the hooks a cloud's registry differs on; the registry credential a workload host pulls with; the Docker configuration and credential helper a build service's steps run with |
| `payloads.go`, `blobstore/` | `Server.Payloads` opens a `blobstore.Payloads` store: object contents in files of their own, written once (`WriteFrom` computes size, MD5 and CRC32C in the same pass), streamed `Concat`, staged `WriteAt` for resumable uploads, `Materialize` for mount mirrors, `Adopt`/`Sweep` at startup, and `OpenCurrent` that follows an overwrite a reader raced; plus RFC 9110 conditional-request evaluation (`EvaluateHTTP`, `ETagListMatches`), per-service byte-range grammars with `ServeRange`, and delimited listing roll-up with combined item/prefix paging (`RollUp`, `PageAfter`) |
| `sparse/` | sparse files: `DataExtents` (SEEK_DATA/SEEK_HOLE), `PunchHole`, `Clear`, extent-preserving `Copy`/`CopyFile`/`CopyTree`, and extent-set arithmetic (`Merge`, `Subtract`, `Clip`, `Diff`) |
| `ociauth.go` | registry authentication primitives: the error envelope with `Docker-Distribution-Api-Version`, Bearer and Basic challenges, `Authorization` and Basic-credential parsing, token-scope parsing, and the image-on-this-port check |
| `simjwt/` | JWT signing and verification for a simulator acting as its own identity provider: a persisted RSA or P-384 key (`LoadOrCreate` reads the PKCS#1 / SEC 1 PEM the clouds already persist), `Sign`, `Verify`, the JWKS and the OpenID Connect discovery document |
| `oidcfed/` | verification of tokens an external OpenID Connect issuer signed: one cached verifier per issuer on a bounded HTTP client and a background context, `UnverifiedIssuer`, `NormalizeIssuer`, `AudienceIntersects` |
| `router.go`, `errors.go` | `ReadJSON`, `PathParam`, `WriteJSON` |
| `parsecore.go`, `safeparse.go` | bounds-safe scanner and frame reader, byte-length-preserving ASCII folding |
| `listq/` | list-operation plumbing: offset page windows over a cloud's own token spelling (`Tokens`, `Window`, `OffsetPage`, `TokenPage`, `ErrBadToken`), `KeyPage`, whose token names the last key a page returned so a create or delete between calls skips and repeats nothing, the filter tree (`Node`: `True`, `And`, `Or`, `Not`, `Cmp`) each cloud's grammar parses into, JSON field lookup and scalar rendering (`Lookup`, `Field`, `ScalarString`), numeric-else-lexical ordering (`CompareNumeric`, `CompareOrdered`), and `ParseOrderBy` / `ApplyList` / `ToDoc` for filter-then-sort over any resource type; each cloud maps `ErrBadToken` and parse errors to its own error shape |
| `msgq/` | the leased message queue behind Amazon SQS, Cloud Pub/Sub subscriptions, Azure Service Bus entities and Azure Queue Storage: `Queue[P]` is plain data kept in the owner's `Store` row and changed inside its `Update` — `Enqueue` with delay, time to live, deduplication key and ordering group; `Receive` leasing in order, blocking an unavailable group, dead-lettering past `Policy.MaxDeliveries` and expiring past retention; `Extend`, `Settle`, `Abandon` (with `Policy.Backoff`), `Release`, `DeadLetter`, `Exhausted`, `Peek`, `Remove`, `Purge`, `Count`; `Dedup` for a topic that deduplicates before fan-out. Each cloud spells its own receipts |
| `streamlog/` | the partitioned append-only record log behind Amazon Kinesis Data Streams shards and Azure Event Hubs partitions: one row per record plus a partition head, so `Append`, `Read`, `Trim` (retention), `SeekTime`, `Last` and `Drop` never rewrite a partition, and `Import` fills an empty partition from another layout keeping sequence numbers and times; `HashRanges` (MD5 hash-key ranges) and `Modulo` implement `Partitioner` |
| `otel.go`, `parent.go`, `process.go`, `specvalidate.go` | OpenTelemetry export, exit-with-parent, log sinks and workload results, runtime wire-shape validation |
| `ids.go` | `NewUUID` (random version 4) and `RandomHex` from crypto/rand |
| `workloadhost/` | the coordinates a workload container dials back on: `CallbackHost`/`CallbackAddr` (container address, Linux bridge gateway, or the desktop runtime's host alias), `ListenPort`, `ExtraHosts` and `AliasHosts` for Docker and Podman, `OuterHostEntries`, `MergeEnv`, and `MetadataIndex[T]` keying a metadata server's instance records by source address |
| `delivery/` | at-least-once push delivery: `Dispatcher[T]` persists each pending delivery with its next attempt time, retries it under a `Policy` (`MaxAttempts`, `MaxAge`, `Backoff`, `MinWait` from the failed answer), resumes after a restart (`Resume`), and hands a delivery that ends without success to the cloud's dead-letter path (`Handler.Finish` with the `Reason`); `SubmitAttempted` makes the first attempt on the caller's goroutine; `Post` sends a webhook and classifies the answer through a cloud's `Classifier`; `Exponential` and `Steps` backoff shapes. A push that drains a leased queue, such as a Cloud Pub/Sub push subscription, keeps its attempts, backoff and dead-lettering in the `msgq` queue it shares with pull and uses `Post` alone |
| `eventbus/` | event fan-out: `Bus[E, S, P]` publishes an event to a source's `Subscriptions`, filters it through the cloud's `Admits` dialect, cuts the admitted events into deliveries by `Batch` `Limits` (count and bytes), turns each into a payload with `Transform` (the input transformation), and either hands it to a `Sink` that owns its own retries — a Pub/Sub subscription's queue — or delivers it through a `delivery` dispatcher under the subscription's retry policy with `Settled` and `DeadLetter` hooks; `Archive` sees every published event, `Replay` re-delivers without archiving, `DeliverTo` addresses subscriptions directly, `Submit` sends a payload no event caused. Amazon EventBridge, Amazon SNS, Azure Event Grid, Pub/Sub and Eventarc fan out through it |
| `cron/` | cron schedules: `Parse` in the `Vixie` five-field dialect (0–7 day-of-week, names, the either-day rule when both day fields are restricted) or the `AWS` six-field dialect (year, `?`, `L`, `W`, `#`), bound to a time zone; `Schedule.Next` skips wall-clock times a daylight-saving jump removes; `Ticker` fires `Entry` schedules from `Server.StartBackground`, persisting each one's next occurrence (`Record`), collapsing occurrences missed while stopped into the latest, dropping those older than `MaxLate`, and forgetting schedules that no longer exist |
| `bg/` | counted background work a test drains with `bg.Await` before it rebuilds the stores: `Go`, `JoinedGo`, `Handoff` (work handed to `Server.StartBackground`), `AfterFunc` and `WatchThen` |
| `dbengine/` | the managed relational database data plane: `Instance` runs a real PostgreSQL, MySQL or MariaDB engine (`Postgres16`, `MySQL80`, `MariaDB114`) on a named volume behind an endpoint the simulator owns — start on first client, adopt after restart, readiness classified the way `pg_isready` does, `Exec`, `Stop` that returns once the volume is free; `ListenLoopback` keeps the advertised port or fails; `SelfSignedCertificate`; SQL and shell quoting |
| `logstore/` | log entries by scope (a log stream, a log, a table): one row per entry keyed by scope and arrival sequence plus a scope head, so `Append`, `Entries`, `Expire` (retention by the entry's own time, leaving gaps reads step over), `Cap`, `Edit` (rewrite or delete entries in place), `Drop` and `DropPrefix` never rewrite a scope; `Open` lists a scope before its first write and `Import` fills an empty scope from another layout |
| `simkms/` | key material: AES-256-GCM `Seal`/`Open` (nonce followed by the sealed bytes, which every cloud's ciphertext framing ends with) and `SealWithNonce`/`OpenWithNonce` for raw-encryption APIs that return the IV; `NewKey`; `EnsureKey`, which creates a service-owned key on first use inside the store's `Upsert` over any row shape; RFC 5649 `WrapWithPadding`/`UnwrapWithPadding`. Each cloud keeps its ciphertext framing, key versions and key API |
| `dnsserver/` | the DNS listener each simulator answers its zones on: one port on UDP and TCP, a `Catalog` hook that returns the zones and names a query can see — given the address it came from and the address it arrived at, which names a workload network — and everything that is DNS rather than cloud: longest-zone matching, RFC 4592 wildcards, CNAME chains across zones, delegations with glue, NXDOMAIN versus NODATA with the zone's SOA, EDNS0 and truncation on UDP, forwarding zones, and recursion through upstreams for names outside every zone |
| `placement/` | the one interface through which a service places a workload — `Place`, `PlaceMember`, `Adopt`, `Find`, `Console` and `Capture`, and a `Handle` with wait, stop, exec, address, published port, ingress and gateway control — with the container backend and the microVM backend, chosen per service by `SIM_PLACEMENT` |
| `nodeagent/` | the node agent's client, its program (`cmd/nodeagent`) and the guest's first process (`guestinit`): Firecracker guests from OCI-built root filesystems, members on disk slots, tap filtering, file shares and extra interfaces |
| `machine/` | the virtual machine lifecycle on a placement backend — boot to ready, watch, stop keeping the disk, start, discard, adopt, capture, console — with each cloud's states as hooks |
| `vminstance/` | VM instances from a cloud's image catalog with the seeded init that runs the image's cloud-init over the user data |
| `replicas/`, `revisions/` | a replica set kept at its count with restarts, back-off, adoption and a pick for front ends; revision identity by template content and the choice of a revision by traffic weight |
| `sidecar/` | the network sidecar program: the resolver and metadata forwarder at the cloud's reserved address, the NAT egress proxy, the container-credentials forwarder, file shares and process reads |
| `fileshare/` | an NFSv4.0 server over host directories, the bind mounts of guests |
| `lbplane/` | the load-balancer and front-end data plane: listeners, TLS, forwarding with `Upstream` timeouts and header builders, health probes and admission hooks |
| `ipam/`, `netrules/` | address ledgers and the parsers of security group, firewall and network security group rules |
| `procimage/` | a process's mapping table and an ELF core read from `/proc` |
| `dbengine/pgwire/`, `dbengine/mysqlwire/` | the front half of each wire protocol: startup and SSLRequest upgrade, cleartext-password exchange and `FATAL 28P01` refusal; the MySQL connection phase with `mysql_clear_password` on the front and a native or `caching_sha2_password` login to the engine |

## What stays with the cloud

- **Error shapes** — `AWSError`, `S3ErrorXML`, `GCPError`, `AzureError` are
  defined in the simulator that speaks that protocol.
- **Protocol routers** — the AWS JSON (`X-Amz-Target`) and AWS Query
  (`Action`/`Version`) routers live in `simulator-aws`; Google Cloud and Azure
  register on the server mux directly.
- **Sandbox profiles** — `SandboxLambda`, `SandboxFargate`, `SandboxCloudRun`,
  `SandboxACA` and their aliases are each cloud's documented workload
  restrictions; the framework enforces whatever profile it is handed.
- **Registry behaviour** — `OCIRegistry` hooks: `Authorize`, `AdmitRepository`,
  `Scope`, `BaseResponse`, `RefuseChunkedUpload`, `OnManifestPut`,
  `HydrateManifest`.
- **Console coordinates** — `ConsoleOptions.Coordinates` fills
  `GET /ui/config.json`; `BrowserFederationCoordinates` serves the consoles
  that federate from the browser, and Azure's server-side Entra broker
  registers through `ConsoleOptions.AuthRoutes`.
- **Database credentials** — `dbengine.Instance` hooks: `Environment`,
  `Ready`, `Authenticate`, `RefusePlaintext`, `BackendLogin`; each cloud keeps
  its credential store and the row its sealing key lives in, and seals through
  `simkms`.
- **DNS visibility** — which zones a query sees (Route 53 private zones by
  VPC, Azure Private DNS by virtual network link, Cloud DNS private zones by
  network) and each cloud's record shapes reach `dnsserver` through its
  `Catalog`.
- **Path rewriting** — Amazon S3's zonal virtual-hosted addressing and Azure
  Resource Manager's case folding are `Config.RewriteRequest` hooks.

## Configuration

`ConfigFromEnv(provider, defaultPort)` reads:

| Variable | Default | Description |
|----------|---------|-------------|
| `SIM_<PROVIDER>_PORT` | — | Port to listen on, ahead of `SIM_LISTEN_ADDR` (`SIM_AWS_PORT`, `SIM_GCP_PORT`, `SIM_AZURE_PORT`) |
| `SIM_LISTEN_ADDR` | `:<defaultPort>` | Listen address |
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | — | TLS certificate and key |
| `SIM_LOG_LEVEL` | `info` | trace, debug, info, warn, error |
| `SIM_RUNTIME` | `docker` | `process` starts API-only, with no container engine |
| `SIM_PERSIST`, `SIM_DATA_DIR` | `false`, temp | SQLite persistence and its directory |
| `SIM_PLACEMENT` | — | Per-service placement table, `service[:qualifier]=container\|microvm` |
| `SIM_CALLBACK_LISTEN_ADDR` | free port | Plain-HTTP listener for workload callbacks when the API listener terminates TLS |
| `SIM_UI_OIDC_ISSUER`, `SIM_UI_OIDC_CLIENT_ID`, `SIM_UI_OIDC_CLIENT_SECRET`, `SIM_UI_PUBLIC_URL`, `SIM_UI_SESSION_SECRET` | — | Console OpenID Connect layer; all or none |
| `SIM_UI_INSECURE_COOKIES` | `false` | HTTP cookies for loopback integration tests only |
| `APPLICATION_RELEASE_REVISION` | — | Immutable release revision the console and monitoring report |
| `SIM_MONITORING_TOKEN` | — | Bearer for `GET /monitoring/observation` |
| `SOCKERLESS_PARENT_PID` | — | The process the simulator must not outlive (set by every test harness) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Enables OpenTelemetry trace, metric and log export |

## Usage

```go
func main() {
    sim.Main(sim.Program{
        Provider:    "aws",
        DefaultPort: "4566",
        Build: func(cfg sim.Config) (*sim.Server, error) {
            srv, err := sim.NewServer(cfg)
            if err != nil {
                return nil, err
            }
            srv.HandleFunc("GET /2015-03-31/functions", listFunctions)
            return srv, nil
        },
    })
}
```

`Configure` checks or records the resolved configuration before the build, and
`Start` starts what serves beside the HTTP port (a gRPC or DNS listener),
returning the stop that runs once the HTTP server has shut down.

## Testing

`make -C sim test` runs the module's own tests, which start real containers
for the reaper, the startup sweep, the memory observer and the cloud
networks; `make node-agent-test` boots a Firecracker guest through the node
agent on a host with `/dev/kvm`; `make -C sim race-test` runs them under the race detector. The
framework is also exercised by every simulator's SDK, CLI and Terraform suite.
