# Configuration

The variables every simulator reads, through `sim.ConfigFromEnv` and the
framework. They set where a simulator listens, keeps state and runs workloads;
none changes a cloud API's behaviour. Each simulator's README lists its own
variables: [AWS](../simulator-aws/README.md),
[Google Cloud](../simulator-gcp/README.md), [Azure](../simulator-azure/README.md).

## Listening

| Variable | Default | Effect |
|---|---|---|
| `SIM_AWS_PORT`, `SIM_GCP_PORT`, `SIM_AZURE_PORT` | unset | The port to listen on. Takes precedence over `SIM_LISTEN_ADDR`. |
| `SIM_LISTEN_ADDR` | `:4566`, `:4567`, `:4568` | The listen address |
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | unset | Serve HTTPS with this certificate and key |
| `SIM_CALLBACK_LISTEN_ADDR` | a free port on every interface | With TLS on, a plain-HTTP listener for the workloads the simulator places: their network sidecars forward metadata and identity requests here, without the operator's certificate authority. Without TLS the one listener serves everything. |
| `SIM_DIAGNOSTICS_ADDR` | `127.0.0.1:6060` | pprof and `/debug/inflight` (requests in flight, with slow-request reports); `off` disables it. It never shares the API listener. |

Google Cloud also serves gRPC on `SIM_GCP_GRPC_PORT`, by default the HTTP port plus two (4569). With `SIM_GCP_GRPC_TLS_CERT` and `SIM_GCP_GRPC_TLS_KEY` set, that port is a TLS front end serving gRPC and REST, as Google's hosts do; Google's client libraries send credentials only over TLS. Azure
Service Bus's AMQP listener is `SIM_SERVICEBUS_AMQP_LISTEN_ADDR`.

## State

| Variable | Default | Effect |
|---|---|---|
| `SIM_PERSIST` | `false` | `true` (or `1`) keeps resources, object contents and running workloads across restarts, in SQLite. [Architecture → State](architecture.md#state) |
| `SIM_DATA_DIR` | a temporary directory (`/tmp/sockerless-sim-<cloud>` with persistence) | The database (`simulator.db`) and the bulk-data directories. It is also the simulator's identity to the container sweep. |

## Workloads

| Variable | Default | Effect |
|---|---|---|
| `SIM_RUNTIME` | `docker` | `docker` needs a Docker or Podman engine and refuses to start without one. `process` is API-only: no engine, and every operation that runs a workload is refused. |
| `SIM_PLACEMENT` | unset | Per-service backend, `service[:qualifier]=container\|microvm`, comma-separated. Unnamed services run on containers. A microVM service starts a node agent, which needs a Linux engine with `/dev/kvm`. [Architecture → Workloads](architecture.md#workloads) |
| `SIM_PULL_POLICY` | `if-not-present` | `never` starts workloads only from images the engine already holds. Otherwise an image is pulled unless the engine holds it for the architecture the workload asks for. |
| `DOCKER_HOST` | the engine's default socket | The engine the simulator talks to, as the Docker client reads it |

## Logging and tracing

| Variable | Default | Effect |
|---|---|---|
| `SIM_LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | unset | Export OpenTelemetry traces, metrics and logs to this OTLP endpoint |
| `SOCKERLESS_PARENT_PID` | unset | Exit when this process exits. Every test harness sets it, so a simulator never outlives its suite. |

## Consoles

A `-tags console` build embeds its console under `/ui/`. A plain build serves
the bundle in `SIM_CONSOLE_DIR`, or no console.

The console's own sign-in is optional. Configure all of these together, or
none; partial configuration fails startup.

| Variable | Effect |
|---|---|
| `SIM_UI_OIDC_ISSUER` | The OpenID Connect issuer operators sign in with |
| `SIM_UI_OIDC_CLIENT_ID`, `SIM_UI_OIDC_CLIENT_SECRET` | The console's relying-party registration at that issuer |
| `SIM_UI_PUBLIC_URL` | The console's external origin, for example `https://aws.example.com` |
| `SIM_UI_SESSION_SECRET` | At least 32 random bytes that sign browser sessions |
| `SIM_UI_INSECURE_COOKIES` | `true` allows HTTP coordinates and non-Secure cookies on loopback only, for local development |
| `APPLICATION_RELEASE_REVISION` | The immutable release (12–64 lowercase hex characters, or an image digest); required with OpenID Connect |

The layer (`ui-auth`) uses the authorization code flow with PKCE, nonce and
state checks, and signed server-side sessions. It supports RP-Initiated
Logout, Front-Channel Logout and signed Back-Channel Logout, which needs the
`iat`, `exp` and single-use `jti` claims. It protects only `/ui/` and its own
`/auth/` routes: every cloud API route keeps the cloud's own authentication.
Register these URIs at the issuer, with `<origin>` as `SIM_UI_PUBLIC_URL`:

| Purpose | URI |
|---|---|
| Redirect | `<origin>/auth/oidc/callback` |
| Post-logout redirect | `<origin>/auth/shauth/logout/complete` |
| Front-channel logout | `<origin>/auth/oidc/frontchannel-logout` |
| Back-channel logout | `<origin>/auth/oidc/backchannel-logout` |

Inside the console, an operator's cloud credentials come from the cloud's own
federation primitive: `AssumeRoleWithWebIdentity` for AWS, Workforce Identity
Federation for Google Cloud, and Microsoft Entra ID for Azure.

## Monitoring

`SIM_MONITORING_TOKEN`, at least 32 non-whitespace characters, enables
`GET /monitoring/observation`. The route accepts only that exact bearer token,
sits outside the console's sign-in, and reports an `e6qu.monitoring/v2`
resource with the process's session and runtime evidence. Without the variable
the route doesn't exist.

## The local HTTPS gateway

Some clients only reach a cloud over HTTPS, or address each storage account,
namespace or site by host name. azurerm is one. For those, run Caddy with the
repository's configuration from the repository root:

```sh
caddy run --config make/https-gateway/Caddyfile --adapter caddyfile
```

It serves:
- `https://aws.sockerless.localhost:8443`;
- `https://gcp.sockerless.localhost:8443`;
- `https://azure.sockerless.localhost:8443`;
- Azure's host-addressed data-plane wildcards, such as `*.blob.azure.sockerless.localhost`.

The `SOCKERLESS_*` variables at the top of the Caddyfile move the gateway and
simulator ports. Caddy issues the certificates from its own local root,
`~/.local/share/caddy/pki/authorities/local/root.crt` on Linux. Point a client
at it with `SSL_CERT_FILE`. [The tutorial](tutorial.md#part-3-azure) runs
azurerm through this gateway.
