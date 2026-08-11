# simulator

Shared framework for building local cloud service reimplementations. Provides HTTP server infrastructure, request routing, in-memory state management, authentication passthrough, and provider-specific error formatting.

All three cloud simulators (AWS, GCP, Azure) import this library as `sim`. The simulators built on this framework are not mocks or stubs — they reimplement actual cloud service semantics, with execution behavior driven by the same configuration (timeouts, replica counts, etc.) that the real services honor.

## Components

| File | Purpose |
|------|---------|
| `server.go` | HTTP server with graceful shutdown, TLS support, health endpoint |
| `config.go` | Configuration from environment variables |
| `router.go` | AWS (`X-Amz-Target`), GCP (REST path), and Azure (ARM path) request routers |
| `state.go` | Generic thread-safe `StateStore[T]` for in-memory resources |
| `errors.go` | Provider-specific error formatters (AWS JSON, GCP JSON, Azure ARM, AWS XML) |
| `middleware.go` | Request ID generation, identity extraction, request logging |

## StateStore

Generic, thread-safe key-value store for simulated resources:

```go
store := sim.NewStateStore[*VPC]()
store.Put("vpc-123", &VPC{ID: "vpc-123"})
vpc, ok := store.Get("vpc-123")
store.Delete("vpc-123")
store.List()   // returns all values
store.Count()  // returns count
```

## Routers

### AWSRouter
Routes by `X-Amz-Target` header (AWS JSON protocol):
```go
router := sim.NewAWSRouter()
router.Register("AmazonEC2ContainerServiceV20141113.RunTask", handleRunTask)
```

### Path-based routing
GCP and Azure simulators register handlers on the server mux directly.

## Error formatting

```go
sim.AWSError(w, "ResourceNotFoundException", "Cluster not found", 400)
sim.GCPError(w, 404, "Zone not found", "NOT_FOUND")
sim.AzureError(w, 404, "ResourceNotFound", "Resource group not found")
```

## Configuration

Loaded from environment variables via `sim.ConfigFromEnv(provider)`:

| Variable | Default | Description |
|----------|---------|-------------|
| `SIM_LISTEN_ADDR` | `:8443` | Listen address |
| `SIM_TLS_CERT` | — | TLS certificate file path |
| `SIM_TLS_KEY` | — | TLS private key file path |
| `SIM_LOG_LEVEL` | `info` | Log level: trace, debug, info, warn, error |
| `SIM_UI_OIDC_ISSUER` | — | Shauth OpenID Connect issuer; enables dashboard authentication with the remaining UI values |
| `SIM_UI_OIDC_CLIENT_ID` | — | Dashboard relying-party client ID |
| `SIM_UI_OIDC_CLIENT_SECRET` | — | Dashboard relying-party client secret |
| `SIM_UI_PUBLIC_URL` | — | Exact public dashboard origin |
| `SIM_UI_SESSION_SECRET` | — | Independent random session-signing secret of at least 32 bytes |
| `SIM_UI_INSECURE_COOKIES` | `false` | Allows HTTP only for explicit loopback integration tests |
| `APPLICATION_RELEASE_REVISION` | — | Immutable 12–64 character lowercase hexadecimal revision or `sha256:` digest; required with dashboard authentication |

Authenticated dashboards expose `GET /auth/validation` with Shauth's exact
username, email, role, and immutable release fields. Anonymous and bearer-only
requests receive an exact `303` to the dashboard's public
`/auth/signed-out` page; validator material is accepted only by Shauth.

## Usage

```go
cfg := sim.ConfigFromEnv("aws")
srv, err := sim.NewServer(cfg)
if err != nil {
    log.Fatalf("simulator startup: %v", err)
}

// Register service handlers
srv.Handle("/ecs/", ecsHandler)

srv.Run() // blocks until SIGTERM/SIGINT
```

## Testing

This library is tested transitively through the simulator and SDK/CLI/Terraform test suites.
