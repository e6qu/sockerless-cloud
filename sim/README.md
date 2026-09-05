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
| `config.go` | `Config` read from `SIM_*` environment variables, including the `RewriteRequest` hook a cloud uses to map virtual-hosted or case-folded addressing onto the paths the router works in |
| `middleware.go` | request id, structured request logging (5xx bodies captured), identity extraction from the caller's credential, `Flush` and `Hijack` passthrough for streaming and WebSocket handlers |
| `diagnostics.go`, `runtime_mode.go` | in-flight request registry with slow-request reporting; `SIM_RUNTIME` resolution |
| `state.go`, `state_sqlite.go`, `db.go`, `index.go`, `store_scan.go` | `Store[T]` in memory or on SQLite (persistence envelope keeps `json:"-"` fields), `Upsert`, write generations, `GenerationIndex` for keyed lookups, and the tracked-store set cross-cutting passes address |
| `container.go`, `container_reaper.go`, `container_memory.go`, `sandbox.go`, `volume_snapshot.go` | the container engine layer: start, adopt, stop and remove workloads; the detached reaper and startup sweep; memory-peak observation; `SandboxProfile` enforcement; VPC bridge networks with secondary addresses; copy-on-write volume snapshots |
| `oci.go` | the Docker Registry HTTP API v2 data plane, keyed per registry scope, with the hooks a cloud's registry differs on |
| `router.go`, `errors.go` | `ReadJSON`, `PathParam`, `WriteJSON` |
| `parsecore.go`, `safeparse.go`, `httpupgrade.go` | bounds-safe scanner and frame reader, byte-length-preserving ASCII folding, WebSocket upgrade |
| `otel.go`, `parent.go`, `process.go`, `specvalidate.go` | OpenTelemetry export, exit-with-parent, log sinks and workload results, runtime wire-shape validation |

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
- **Path rewriting** — Amazon S3's zonal virtual-hosted addressing and Azure
  Resource Manager's case folding are `Config.RewriteRequest` hooks.

## Configuration

`ConfigFromEnv(provider)` reads:

| Variable | Default | Description |
|----------|---------|-------------|
| `SIM_LISTEN_ADDR` | `:8443` | Listen address (each simulator's `main` substitutes its own default port) |
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | — | TLS certificate and key |
| `SIM_LOG_LEVEL` | `info` | trace, debug, info, warn, error |
| `SIM_RUNTIME` | `docker` | `process` starts API-only, with no container engine |
| `SIM_PERSIST`, `SIM_DATA_DIR` | `false`, temp | SQLite persistence and its directory |
| `SIM_UI_OIDC_ISSUER`, `SIM_UI_OIDC_CLIENT_ID`, `SIM_UI_OIDC_CLIENT_SECRET`, `SIM_UI_PUBLIC_URL`, `SIM_UI_SESSION_SECRET` | — | Console OpenID Connect layer; all or none |
| `SIM_UI_INSECURE_COOKIES` | `false` | HTTP cookies for loopback integration tests only |
| `APPLICATION_RELEASE_REVISION` | — | Immutable release revision the console and monitoring report |
| `SIM_MONITORING_TOKEN` | — | Bearer for `GET /monitoring/observation` |
| `SOCKERLESS_PARENT_PID` | — | The process the simulator must not outlive (set by every test harness) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | — | Enables OpenTelemetry trace, metric and log export |

## Usage

```go
cfg := sim.ConfigFromEnv("aws")
srv, err := sim.NewServer(cfg)
if err != nil {
    log.Fatalf("simulator startup: %v", err)
}
srv.HandleFunc("GET /2015-03-31/functions", listFunctions)
if err := srv.ListenAndServe(); err != nil {
    log.Fatal(err)
}
```

## Testing

`make -C sim test` runs the module's own tests, which start real containers
for the reaper, the startup sweep, the memory observer and the VPC network
allocator; `make -C sim race-test` runs them under the race detector. The
framework is also exercised by every simulator's SDK, CLI and Terraform suite.
