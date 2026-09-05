# Sockerless Cloud — simulators

Local reimplementations of cloud-provider APIs. Not mocks — jobs run, functions execute, timeouts fire, logs land in the cloud-native sink, all driven by the same configuration knobs (replica timeouts, task timeouts, etc.) that the real cloud services honor. Code that works against the simulators works against the real cloud and vice versa.

They are general-purpose: anything that speaks a cloud's API can be pointed at one and tested against it — the cloud's own SDKs, its official CLI, its Terraform provider, and the libraries built on top of them (boto for AWS, for instance). Each cloud's surface is validated against that cloud's published specification in the format the cloud publishes it: Smithy models for AWS, Discovery documents for Google Cloud, OpenAPI (Swagger) for Azure.

This file is the **end-to-end showcase + navigation hub**. The canonical per-cloud documentation lives in the three sub-directories — read those for full per-service detail.

| Cloud | README | Published specification it is validated against |
|---|---|---|
| AWS | [`simulator-aws/README.md`](simulator-aws/README.md) | Smithy models ([`specs/cloud-api/aws`](specs/cloud-api/aws)) |
| GCP | [`simulator-gcp/README.md`](simulator-gcp/README.md) | Discovery documents ([`specs/cloud-api/gcp`](specs/cloud-api/gcp)) |
| Azure | [`simulator-azure/README.md`](simulator-azure/README.md) | OpenAPI/Swagger ([`specs/cloud-api/azure`](specs/cloud-api/azure)) |

## Reference adaptors

Every simulator answers the same three external tools per cloud — the SDK, the official CLI, and the Terraform provider. **Anything any of these does against the real cloud's endpoint, it must do against the simulator on the same wire.** The per-sim READMEs list the exact versions + spec links.

| Cloud | SDK | CLI | Terraform provider |
|---|---|---|---|
| AWS | [`aws-sdk-go-v2`](https://github.com/aws/aws-sdk-go-v2) | [`aws`](https://docs.aws.amazon.com/cli/latest/reference/) | [`hashicorp/aws`](https://registry.terraform.io/providers/hashicorp/aws/latest/docs) |
| GCP | [`cloud.google.com/go`](https://pkg.go.dev/cloud.google.com/go) | [`gcloud`](https://cloud.google.com/sdk/docs/install) | [`hashicorp/google`](https://registry.terraform.io/providers/hashicorp/google/latest/docs) |
| Azure | [`azure-sdk-for-go`](https://pkg.go.dev/github.com/Azure/azure-sdk-for-go/sdk) | [`az`](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) | [`hashicorp/azurerm`](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs) |

Discipline patterns for wire fidelity:

- Capturing wire shape from the SDK serializer source code — [`.claude/skills/sim-handler-checklist`](.claude/skills/sim-handler-checklist/SKILL.md).
- Cross-resource invariants under one `terraform apply` — [`.claude/skills/cross-resource-stack-test`](.claude/skills/cross-resource-stack-test/SKILL.md).
- The broader wire-fidelity discipline — [`.claude/skills/adaptor-fidelity-check`](.claude/skills/adaptor-fidelity-check/SKILL.md).

## Three governing principles

1. **The simulator is a cloud slice.** `simulator-aws/` implements whatever slice of AWS sockerless depends on — ECS + ECR + Lambda + CloudWatch + Cloud Map + EC2 + STS + IAM + S3 + EFS + KMS + SSM + Secrets Manager + DynamoDB + CloudFront + ACM + Route 53 + WAFv2 + Amplify — at cloud-API fidelity. Not a per-product simulator; a cloud slice.
2. **One binary per cloud.** Adding a new service slice means a new `registerX(srv)` + handler file inside `simulator-aws/`, `simulator-gcp/`, or `simulator-azure/`. Never a new binary per product.
3. **Cloud-API fidelity.** Match the real cloud's error shapes, response headers, async operation semantics, path templates, HTTP status codes, and wire encodings exactly. When the cloud's contract doesn't cover something, neither does the simulator.

Full statement and rationale in [AGENTS.md → Simulator architecture — cloud-slice principle](AGENTS.md#simulator-architecture--cloud-slice-principle). Enforced per-commit by the `simulator-testing-contract` pre-commit hook (every handler change must touch sdk-tests + cli-tests + terraform-tests).

## Default ports

| Simulator | HTTP | gRPC (where used) |
|---|---|---|
| AWS | `:4566` | n/a |
| GCP | `:4567` | `:4569` (Cloud Logging, Bigtable, Firestore, Pub/Sub, Spanner, Cloud KMS, Secret Manager; `SIM_GCP_GRPC_PORT`) |
| Azure | `:4568` | n/a |

## Quick start — all three at once

```sh
docker compose up
```

This starts all three simulators on their default ports with health checks. Each one logs to stdout in the same compose process.

Equivalent without compose:

```sh
cd simulator-aws && go run . &
cd simulator-gcp && go run . &
cd simulator-azure && go run . &
```

Environment knobs (per sim — full list in each sub-README):

| Variable | Default | Description |
|---|---|---|
| `SIM_LISTEN_ADDR` | `:8443` (overridden per provider) | Listen address |
| `SIM_AWS_PORT` / `SIM_GCP_PORT` / `SIM_AZURE_PORT` | `4566` / `4567` / `4568` | Provider-specific port override |
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | unset | Enable HTTPS (required by some Terraform providers — see [`simulator-azure/README.md § Special handling`](simulator-azure/README.md)) |
| `SIM_RUNTIME` | `docker` | Workload runtime mode. The default initializes Docker/Podman for execution. Set `process` only for explicit API-only runs that do not invoke workload-execution APIs. |
| `SIM_SERVICEBUS_AMQP_LISTEN_ADDR` | unset | Azure-only raw Service Bus AMQP/TLS listener; requires `SIM_SERVICEBUS_AMQP_TLS_CERT` / `SIM_SERVICEBUS_AMQP_TLS_KEY` or the shared TLS cert/key |
| `SIM_LOG_LEVEL` | `info` | Log level (`trace`, `debug`, `info`, `warn`, `error`) |
| `SIM_UI_OIDC_ISSUER` | unset | OpenID Connect issuer for the embedded operator UI. Configure all UI OIDC values together. |
| `SIM_UI_OIDC_CLIENT_ID` | unset | OpenID Connect relying-party client ID for this simulator dashboard. |
| `SIM_UI_OIDC_CLIENT_SECRET` | unset | OpenID Connect relying-party client secret, supplied through the deployment secret store. |
| `SIM_UI_PUBLIC_URL` | unset | Externally visible origin for callback and logout redirects, such as `https://aws.dev.e6qu.dev`. |
| `SIM_UI_SESSION_SECRET` | unset | Independent random value of at least 32 bytes used to sign local browser sessions. |
| `SIM_UI_INSECURE_COOKIES` | `false` | Explicit loopback-development opt-in for HTTP issuer/public coordinates and non-Secure cookies; non-loopback HTTP coordinates remain invalid. |
| `APPLICATION_RELEASE_REVISION` | unset | Immutable 12–64 character lowercase hexadecimal revision or `sha256:` image digest; required whenever UI OpenID Connect is enabled. |
| `SIM_MONITORING_TOKEN` | unset | Independent deployment bearer credential of at least 32 non-whitespace characters for `GET /monitoring/observation`; without it the route is not registered. |

The optional first-party UI authentication layer uses authorization code +
PKCE, nonce and state validation, signed server-tracked sessions, RP-Initiated
Logout with an ID-token hint, OIDC Front-Channel Logout correlated by trusted
issuer and `sid`, and signed OIDC Back-Channel Logout correlated by `sid` or
`sub` with required `iat`, future `exp`, and single-use `jti` claims. It
protects only `/ui/` and the UI identity/logout endpoints;
all AWS, Google Cloud, and Microsoft Azure API routes retain their native
authentication and protocol behavior. Partial configuration fails startup.

When configured, the monitoring route sits outside browser OpenID Connect
protection and accepts only its exact bearer credential. It publishes an
`e6qu.monitoring/v2` application resource with real session and process
evidence; it does not change any simulated cloud API route or invent cloud
resource cost.

Each simulator dashboard registers these relying-party coordinates, replacing
`<origin>` with that dashboard's `SIM_UI_PUBLIC_URL`:

- redirect URI: `<origin>/auth/oidc/callback`
- post-logout redirect URI: `<origin>/auth/shauth/logout/complete`

The fixed completion bridge returns to Shauth's `/oauth/logout/complete` endpoint;
Shauth then redirects to the registered application-local
`<origin>/auth/signed-out` page. The bridge ignores all query parameters and
never reflects a caller-controlled destination.
- front-channel logout URI: `<origin>/auth/oidc/frontchannel-logout`
- back-channel logout URI: `<origin>/auth/oidc/backchannel-logout`

The development registrations are therefore:

| Dashboard | Redirect URI | Post-logout redirect URI | Front-channel logout URI | Back-channel logout URI |
|---|---|---|---|---|
| AWS | `https://aws.dev.e6qu.dev/auth/oidc/callback` | `https://aws.dev.e6qu.dev/auth/signed-out` | `https://aws.dev.e6qu.dev/auth/oidc/frontchannel-logout` | `https://aws.dev.e6qu.dev/auth/oidc/backchannel-logout` |
| Google Cloud | `https://gcp.dev.e6qu.dev/auth/oidc/callback` | `https://gcp.dev.e6qu.dev/auth/signed-out` | `https://gcp.dev.e6qu.dev/auth/oidc/frontchannel-logout` | `https://gcp.dev.e6qu.dev/auth/oidc/backchannel-logout` |
| Microsoft Azure | `https://azure.dev.e6qu.dev/auth/oidc/callback` | `https://azure.dev.e6qu.dev/auth/signed-out` | `https://azure.dev.e6qu.dev/auth/oidc/frontchannel-logout` | `https://azure.dev.e6qu.dev/auth/oidc/backchannel-logout` |

The browser starts at `/ui/`, redirects through `/auth/oidc/login` when no
local session is active, obtains its identity from `/auth/session`, and submits
logout to `/auth/logout`. The simulators need no authentication proxy, and support none, for
the simulator UI.

Shauth validates each deployed dashboard at `<origin>/auth/validation`.
Anonymous and bearer-only requests receive an exact `303` to the dashboard's
own `/auth/signed-out` page. An authenticated request exposes the verified
username, email, role, and immutable release through the standard
`validation-username`, `validation-email`, `validation-role`, and
`validation-release` fields and signs out through the same global OpenID
Connect flow as the dashboard.

Continuous integration ran the compiled AWS, Google Cloud, and Microsoft
Azure dashboards together with Sockerless Admin against real Shauth, Ory
Hydra, and PostgreSQL. One browser matrix verified direct and app-catalog entry,
shared sign-on, identity, logout from every relying party, global revocation,
exact app-local signed-out destinations, and signed back-channel acceptance at
every dashboard. Shauth's passwordless validator ran both catalog and direct
entry for each dashboard, verified exact identity and release fields,
reauthenticated after relying-party logout, and proved provider logout against
a second relying-party witness without exposing validator credentials to any
Sockerless process.

Simulator calls that execute workloads require Docker or Podman. If the
operator intentionally needs only non-execution API surfaces, `SIM_RUNTIME=process`
starts the simulator without initializing Docker/Podman; execution endpoints still
require a real workload runtime when used.

## Optional local HTTPS gateway

The simulators keep their direct HTTP and `SIM_TLS_CERT` / `SIM_TLS_KEY`
entry points. For clients that expect cloud-like HTTPS URLs, run the
local Caddy gateway from the repository root:

```sh
make stack-https-up
make stack-https-status
```

Default endpoints are `https://aws.sockerless.localhost:8443`,
`https://gcp.sockerless.localhost:8443`, and
`https://azure.sockerless.localhost:8443`, plus Azure host-addressed
data-plane wildcards. Details and CA trust setup live in each simulator's README, under the HTTPS
gateway section.

## End-to-end showcase

The canonical multi-cloud workflow combines simulators across all three clouds in one CI run:

```sh
# 1. Start all three sims
cd simulators && docker compose up -d

# 2. Drive each one with its real reference adaptor
export AWS_ENDPOINT_URL=http://localhost:4566 AWS_REGION=us-east-1 \
       AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
aws ecs list-clusters
aws cloudfront list-distributions

export CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN=http://localhost:4567/
gcloud run jobs list --region us-central1

az rest --method GET --url "http://localhost:4568/subscriptions/.../resourceGroups?api-version=2021-04-01"

# 3. Apply Terraform that touches all three clouds at once
cd simulator-aws/terraform-tests   && go test -run TestStackProductionShape
cd ../../simulator-gcp/terraform-tests        && go test ./...
cd ../../simulator-azure/terraform-tests      && go test ./...

```

Per-sim captured-output samples live in each sub-README. For the most exercised production-shape integration test, see [`simulator-aws/terraform-tests/TestStackProductionShape`](simulator-aws/terraform-tests/apply_test.go) — it provisions CloudFront + ACM + WAFv2 + Route 53 ALIAS + Amplify + IAM SLR/OIDC + ECS + Cloud Map in one `terraform apply` and asserts the cross-resource references resolve correctly.

## Validation

Every simulator ships four test surfaces:

- `sdk-tests/` — official cloud SDK against the running sim.
- `cli-tests/` — official cloud CLI against the running sim.
- `terraform-tests/` — real Terraform provider with `endpoints {}` overrides against the running sim.
- `bash-tests/` — standalone bash scripts validating CLI behaviour in text + JSON modes.

```sh
# Per-cloud run-all
cd simulator-aws/sdk-tests       && GOWORK=off go test -v ./...
cd simulator-aws/cli-tests       && GOWORK=off go test -v ./...
cd simulator-aws/terraform-tests && GOWORK=off go test -v ./...
cd simulator-aws/bash-tests      && ./test_aws_cli.sh
```

Top-level Makefile entry points:

```sh
make docker-test                  # Docker-based SDK/CLI/Terraform tests for all clouds
make simulator-aws/docker-test   # Docker-based tests for one cloud
make test-integration             # Simulator-backend integration tests (every Go app + test category)
```

`make docker-test` builds the shared `Dockerfile.test` image, mounts the repository root plus the host Docker socket, and runs each simulator's existing `test-all` target. The Docker path is a real-client validation harness, not a reduced smoke test.

CI runs all of them on every PR — the `sim (<cloud> sdk …)`, `sim (<cloud> cli …)` and `tf (<cloud> …)` jobs in `.github/workflows/ci.yml`, sharded per cloud, with `race (simulator-<cloud>)` and `race (sim)` running the module unit tests under the race detector.

### Spec-based validation

On top of the real-client suites, a spec validator checks every simulator against the
official machine-readable API specs vendored under
[`specs/cloud-api/`](specs/cloud-api/README.md) (AWS Smithy models, GCP
Discovery documents, Azure Swagger):

- **Static surface conformance** (`spec_conformance_test.go`, runs with
  `make unit-test`): every registered operation/route must exist in the
  vendored spec — the simulator cannot invent paths or wire keys under a
  cloud's namespace. Real-but-unspecified surfaces (IMDS, OCI registry
  data planes, LRO polling URLs) live in justified in-test allowlists.
- **Runtime wire-shape validation** (armed via
  `SOCKERLESS_SPEC_VALIDATE=<report.jsonl>` +
  `SOCKERLESS_SPEC_DIR=specs/cloud-api/<cloud>`): it checks responses
  member-by-member against the spec's output shapes while the SDK/CLI
  suites run; `scripts/check-spec-violations.sh` gates the report against
  `simulator-<cloud>/spec-violation-allowlist.txt`. The allowlist only
  shrinks: the bug burn-down is complete — AWS and Azure ship no
  allowlist at all, and GCP's carries only two permanent, documented
  modeling exemptions (Firestore REST server-streaming responses, which
  are JSON arrays of stream elements on the real wire too). Any new
  violation fails CI until the simulator matches.

## Shared framework

The three simulators are built on one framework module, [`sim/`](sim/README.md)
(`github.com/e6qu/sockerless-cloud/sim`), pinned by each simulator like the
other support modules (`realexec/`, `ui-auth/`): the HTTP server and its
console and monitoring routes, the container-engine layer every workload runs
on, durable state on SQLite, the OCI Distribution data plane the registries
mount, request logging and tracing, and the bounds-safe parsing primitives.
The framework is cloud-neutral; each cloud's error shape, protocol router,
sandbox profile and console coordinates live in its own module and reach the
framework through hooks.

## Design philosophy

Simulators are **real implementations**, not fakes. They don't approximate cloud behavior with synthetic timers or hardcoded responses — they reimplement the actual service semantics:

- **Cloud-native configuration drives the execution lifecycle.** Azure ACA jobs respect `replicaTimeout`. GCP Cloud Run jobs respect the task-template `timeout`. AWS ECS tasks run until the process exits or a caller invokes `StopTask`, because ECS has no native execution timeout.
- **Log injection** writes entries to the same tables and log groups that the real services would, queryable through the same APIs (KQL for Azure, Cloud Logging filters for GCP, CloudWatch for AWS).
- **Agent integration** spawns real subprocesses — the same `sockerless-agent` binary used in production — enabling full exec/attach through simulated cloud resources.
- **SDK + Terraform compatibility** rides on the real official clients, not custom HTTP calls.

The simulators run locally on a single machine today. The architecture allows distributing them across machines later, behind the same API surface.

## Workload execution — host model

Every execution-service (ECS, Lambda, Cloud Run, Cloud Functions, Cloud Run Jobs, ACA, App Service / AZF) runs the workload on a **Docker host** shaped per cloud-product. Workloads never run as `os/exec` host processes of the simulator binary itself — `simulator-<cloud>/sdk-tests/host_dispatch_test.go` enforces that distinction. The workload's `Architecture` field (default `linux/arm64`) flows through `ContainerConfig.Architecture` to Docker's image-pull + container-create `Platform` option.

The host model is stated in full in each simulator's README, beside the execution services it applies to.

Each workload streams stdout/stderr in real time into the cloud-native log sink:

| Service | Log sink | API for retrieval |
|---|---|---|
| ECS | CloudWatch Logs (awslogs) | `GetLogEvents` / `FilterLogEvents` |
| Cloud Run Jobs | Cloud Logging | `entries.list` (REST) / `ListLogEntries` (gRPC) |
| Container Apps | Log Analytics | KQL via `QueryWorkspace` |
| Lambda | CloudWatch Logs | `GetLogEvents` |
| Cloud Functions | Cloud Logging | `entries.list` / `ListLogEntries` |
| Azure Functions | Log Analytics (AppTraces) | KQL via `QueryWorkspace` |

FaaS simulators (Lambda, Cloud Functions, Azure Functions) also execute real processes when `SimCommand` is set, returning the result synchronously.

## ECS ExecuteCommand

The ECS simulator supports `ExecuteCommand` with WebSocket-based session bridging:

1. Spawn a new process with the given command.
2. Register a WebSocket handler at `/ecs-exec/{sessionId}`.
3. Return a session with the WebSocket URL.
4. Bridge stdin/stdout/stderr over the WebSocket connection.

## Request routing per cloud

| Cloud | Protocol | Routing |
|---|---|---|
| AWS (ECS, ECR, CloudWatch, Cloud Map, WAFv2, ACM, KMS, SSM, Secrets, DynamoDB, Kinesis, EventBridge) | AWS-JSON | `X-Amz-Target` header dispatch |
| AWS (EC2, IAM, STS) | AWS Query | `Action` form parameter dispatch |
| AWS (Lambda, S3, EFS, CloudFront, Route 53, Amplify) | REST | Path-based mux (CloudFront / Route 53 use XML bodies, others JSON) |
| GCP (all services including BigQuery and Firestore) | REST + gRPC | Path-based mux (HTTP), proto service (gRPC on port+1 for Cloud Logging) |
| Azure (ARM services including Cosmos DB) | ARM REST | Path-based mux with `api-version` validation |
| Azure (data planes including Storage, Key Vault, Service Bus/Event Hubs, Event Grid, Cosmos DB) | REST / AMQP | Host/path-based mux and optional raw AMQP/TLS listeners |

## Known issues

Open simulator bugs live in [`BUGS.md`](BUGS.md); the tooling quirks that are not simulator defects are recorded in [`DO_NEXT.md`](DO_NEXT.md).

## What's out of scope

- **Cloud-side production deployments.** Simulators are for local development and CI. A production deployment talks to the real cloud endpoints; the point of the simulators is that the same client code reaches both.
- **Multi-region / cross-region replication.** Each sim is single-region; multi-region routing belongs to real cloud infra.
- **Published price sheets and vendor catalogs.** Google Cloud Billing's SKU catalog and the like are somebody else's published data; a partial copy would be fabrication, so those surfaces answer with what this installation publishes, which is nothing. Quota enforcement exists where a cloud enforces it (`SIM_GCP_CPU_QUOTA_PER_REGION` wires Cloud Run's regional CPU budget).
- **Identity providers outside the simulator.** Every credential is verified — SigV4 signatures against the principal's stored secret, Google Cloud and Microsoft Entra bearers against the simulator's own signing keys — but the identities are the ones the simulator's IAM, Google Cloud IAM and Microsoft Entra slices minted. A real external identity provider is a coordinate the deployment supplies, not something the simulator stands in for.
- **Outbound delivery to carriers and push services.** Amazon SNS SMS and mobile push need a telecommunications carrier or Apple's and Google's hosts, which no AWS API provisions; those deliveries fail naming the missing dependency (BUG-2712).

## Per-cloud guides

| Cloud | CLI | Terraform | Python SDK |
|---|---|---|---|
| AWS | [AWS CLI](simulator-aws/docs/cli.md) | [`hashicorp/aws`](simulator-aws/docs/terraform.md) | [boto3](simulator-aws/docs/python-sdk.md) |
| GCP | [gcloud CLI](simulator-gcp/docs/cli.md) | [`hashicorp/google`](simulator-gcp/docs/terraform.md) | [`google-cloud-*`](simulator-gcp/docs/python-sdk.md) |
| Azure | [az CLI](simulator-azure/docs/cli.md) | [`hashicorp/azurerm`](simulator-azure/docs/terraform.md) | [`azure-mgmt-*`](simulator-azure/docs/python-sdk.md) |

See also: [`specs/SIM_PARITY_MATRIX.md`](specs/SIM_PARITY_MATRIX.md) for the per-service inventory, and [`specs/SIM_TEST_COVERAGE_MATRIX.md`](specs/SIM_TEST_COVERAGE_MATRIX.md) for which client exercises each surface.

## Copyright and licence

Copyright 2026 [Adrian Mârza](https://www.linkedin.com/in/adrian-m%C3%A2rza-52606512a).

Copyright in this project is retained by Adrian Mârza, and by each contributor
to the extent of their own contributions.

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU Affero General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option) any
later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
PARTICULAR PURPOSE. See the GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License along
with this program. If not, see <https://www.gnu.org/licenses/>.

- Licence text as distributed here: [`LICENSE`](LICENSE)
- Upstream licence text: <https://www.gnu.org/licenses/agpl-3.0.html>
- SPDX identifier: [`AGPL-3.0-or-later`](https://spdx.org/licenses/AGPL-3.0-or-later.html)

### Vendored material

The simulators are validated against verbatim snapshots of each cloud's own
published, machine-readable specification. Those snapshots are third-party
material under their own licences, not under this project's:

| Vendored | Format | Upstream | Licence |
|---|---|---|---|
| [`specs/cloud-api/aws/`](specs/cloud-api/aws/SOURCES.md) | Smithy 2.0 models | `aws/aws-sdk-go-v2` | [Apache-2.0](https://spdx.org/licenses/Apache-2.0.html) |
| [`specs/cloud-api/gcp/`](specs/cloud-api/gcp/SOURCES.md) | API Discovery documents | per-service Google endpoints | [Apache-2.0](https://spdx.org/licenses/Apache-2.0.html) |
| [`specs/cloud-api/azure/`](specs/cloud-api/azure/SOURCES.md) | OpenAPI (Swagger 2.0) | `Azure/azure-rest-api-specs` | [MIT](https://spdx.org/licenses/MIT.html) |
| [`specs/cloud-api/aws/service-reference/`](specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md) | AWS Service Reference | `servicereference.us-east-1.amazonaws.com` | AWS Service Reference (public service authorization data) |

Every snapshot is traceable to its origin. Each row of a `SOURCES.md` records
the local file, the upstream repository or host, the exact upstream path, the
licence, the revision it is pinned at, and the time it was fetched — so any
vendored byte can be traced back to the published document it came from and
checked against it. [`scripts/check-spec-freshness.sh`](scripts/check-spec-freshness.sh)
compares each pin against upstream, and
[`specs/cloud-api/README.md`](specs/cloud-api/README.md) is the index.

Third-party dependencies of the test suites and console UIs carry their own
licences, recorded in the `go.mod`/`go.sum` and `package.json` files of the
modules that require them.
