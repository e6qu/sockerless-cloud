# simulator-gcp

`simulator-gcp` is one binary that serves a slice of Google Cloud on one port,
`4567`, where every REST API answers on its own paths. The services whose
client libraries speak gRPC (Cloud Logging, Bigtable, Firestore, Pub/Sub,
Spanner, Cloud Key Management Service and Secret Manager) also answer on a
gRPC port, `4569` by default, which `SIM_GCP_GRPC_PORT` moves. The simulator
verifies the OAuth 2.0 access token on every REST request and runs Compute
Engine instances, Cloud Run workloads, Cloud Build steps and database engines
as real containers. Unit tests check every registered route against the
Discovery documents vendored in [`specs/cloud-api/gcp/`](../specs/cloud-api/gcp/),
and `SOCKERLESS_SPEC_VALIDATE` checks live responses against them.

## Run it

```sh
# A release. Go pins it by its commit: the repository tags only vX.Y.Z at its root.
go install github.com/e6qu/sockerless-cloud/simulator-gcp@<release-commit>
simulator-gcp

# The release image, API-only.
docker run --rm -p 4567:4567 -p 4569:4569 -e SIM_RUNTIME=process ghcr.io/e6qu/sockerless-simulator-gcp:vX.Y.Z

# A checkout, with the console embedded (-tags console). Run from the repository root.
(cd ui && bun install && bunx turbo run build --filter=@sockerless/ui-simulator-gcp)
make simulator-gcp/build && ./simulator-gcp/simulator-gcp
```

`go install` and a plain `go build` embed no console; the release binaries and
image embed it.

The simulator needs a Docker or Podman engine to run Compute Engine
instances, Cloud Run services, jobs and worker pools, Cloud Run functions,
Cloud Build builds, Cloud SQL instances and Memorystore for Redis instances,
and refuses to start without one. `SIM_RUNTIME=process` starts it API-only,
without an engine, and refuses every operation that would run a workload.
`GET /health` reports `runtime` and `capabilities.workloadExecution`.

[`docs/configuration.md`](../docs/configuration.md) lists the variables every
simulator shares (`SIM_LISTEN_ADDR`, `SIM_GCP_PORT`, `SIM_TLS_CERT`,
`SIM_TLS_KEY`, `SIM_PERSIST`, `SIM_DATA_DIR`, `SIM_RUNTIME`, `SIM_PLACEMENT`,
`SIM_LOG_LEVEL`, the console's sign-in). These are Google Cloud's own:

| Variable | Default | Effect |
|---|---|---|
| `SIM_GCP_GRPC_PORT` | the HTTP port plus two | The gRPC port |
| `SIM_DNS_PORT` | a kernel-chosen port | The UDP and TCP port where Cloud DNS answers public managed zones for the host. Workloads reach the resolver through their network's sidecar. |
| `SIM_DNS_UPSTREAM` | the host's `resolv.conf` | Resolvers, `host[:port]` comma-separated, for names no zone holds |
| `SIM_GCS_DATA_DIR` | `<SIM_DATA_DIR>/gcs` | Where Cloud Storage object contents live |
| `SIM_GCP_CPU_QUOTA_PER_REGION`, `SIM_GCP_CPU_QUOTA_WINDOW` | `0` (no quota), `1m` | A Cloud Run CPU budget per project and region over a sliding window; a deploy that would exceed it fails with Cloud Run's quota error |
| `SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT`, `_FEDERATION_ENDPOINT`, `_FEDERATION_AUDIENCE` | the console's origin | The API endpoint the console reads and the Security Token Service it federates operators through |
| `SOCKERLESS_SPEC_VALIDATE`, `SOCKERLESS_SPEC_DIR` | unset | Validate every response against the Discovery documents in `SOCKERLESS_SPEC_DIR`, writing violations to the file `SOCKERLESS_SPEC_VALIDATE` names |

With `SIM_DATA_DIR` set, Spanner databases are files under
`<SIM_DATA_DIR>/spanner`. In `SIM_PLACEMENT`, the Google Cloud services are
`compute`, `run`, `cloudbuild` and `redis`.

## Connect a client

Every client signs in as the seeded bootstrap service account, with a key
whose private half the repository publishes:

| Coordinate | Value |
|---|---|
| Service account | `sockerless-bootstrap@sockerless.iam.gserviceaccount.com` |
| Project | `sockerless` |
| Key ID | `49c4c9ee1dffb89ca13534183ea5e484e46abaaa` |
| Token endpoint | `<endpoint>/token` |

Write the key as a service-account credential file, the JSON IAM's
`keys.create` returns. Its `token_uri` names the simulator's token endpoint;
`-token-uri` sets another port:

```sh
go run github.com/e6qu/sockerless-cloud/testutil/cmd/gcp-bootstrap-key@latest -o bootstrap-key.json
# In a checkout: go run ./testutil/cmd/gcp-bootstrap-key -o bootstrap-key.json
```

A client signs a JWT with the key and exchanges it at `token_uri`, as it does
at `oauth2.googleapis.com`, and presents the access token it gets back. The
token endpoint refuses a JWT-bearer grant without an assertion. The bootstrap
account owns every project, `sockerless` among them; create other projects
through Cloud Resource Manager, and other accounts and keys through IAM.
[`docs/iam.md`](../docs/iam.md#google-cloud) covers identity in depth.

### gcloud

```sh
export CLOUDSDK_CONFIG=$PWD/gcloud-sim   # keep the simulator out of your own configuration
gcloud auth activate-service-account --key-file=bootstrap-key.json
gcloud config set project sockerless
gcloud config set auth/token_host http://localhost:4567/token
gcloud config set api_endpoint_overrides/storage http://localhost:4567/
gcloud config set api_endpoint_overrides/iam http://localhost:4567/

gcloud storage buckets create gs://demo
gcloud iam service-accounts list
```

Set one `api_endpoint_overrides/<api>` per API, with the trailing `/`; the
`CLOUDSDK_API_ENDPOINT_OVERRIDES_<API>` variables do the same. A few take a
path: `bigquery` is `http://localhost:4567/bigquery/v2/` and `spanner` is
`http://localhost:4567/spanner/`. `bq` asks gcloud for its token through
`auth/token_host`, so set that too.

Two kinds of request need the simulator as gcloud's HTTP proxy
(`export HTTP_PROXY=http://localhost:4567`). gcloud puts the region in front
of a regional API's host (`us-central1-localhost:4567` for Cloud Run), which
the proxy resolves. And Eventarc, Cloud Build, Serverless VPC Access and Cloud
Scheduler share paths with other APIs, so Google tells them apart by host:
override them with their real hosts, such as
`http://eventarc.googleapis.com/`, and the proxy delivers each request with
that `Host`.

`gcloudCLI` in [`cli-tests/helpers_test.go`](cli-tests/helpers_test.go) runs
every command of the CLI suite with this configuration, after
`activateBootstrapServiceAccount` activates the key.

### Cloud Client Libraries for Go

Application Default Credentials read the credential file that
`GOOGLE_APPLICATION_CREDENTIALS` names. A REST client takes the endpoint:

```go
// GOOGLE_APPLICATION_CREDENTIALS=$PWD/bootstrap-key.json
jobs, err := run.NewJobsRESTClient(ctx, option.WithEndpoint("http://localhost:4567"))
gcs, err := storage.NewClient(ctx, option.WithEndpoint("http://localhost:4567/storage/v1/"))
zones, err := dns.NewService(ctx, option.WithEndpoint("http://localhost:4567/"))
```

Use the `New…RESTClient` constructor where a library has both: Cloud Run,
Eventarc, Cloud Scheduler, Cloud Tasks, Memorystore for Redis and Cloud
Resource Manager serve REST only. The Pub/Sub, Firestore, Spanner, Bigtable
and Cloud Logging clients, and the gRPC constructors of Cloud KMS and Secret
Manager, dial the gRPC port in plaintext:

```go
client, err := pubsub.NewClient(ctx, "sockerless",
	option.WithEndpoint("localhost:4569"),
	option.WithoutAuthentication(),
	option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
```

Spanner and Bigtable also read `SPANNER_EMULATOR_HOST` and
`BIGTABLE_EMULATOR_HOST=localhost:4569`. The SDK suite mints its tokens from
the same file through `simTokenSource` in
[`sdk-tests/helpers_test.go`](sdk-tests/helpers_test.go) and passes them with
`option.WithTokenSource`; `newPSGRPCClient` in
[`sdk-tests/pubsub_grpc_test.go`](sdk-tests/pubsub_grpc_test.go) builds the
gRPC client above.

### Terraform

The `hashicorp/google` provider reads the credential file and takes one
`*_custom_endpoint` per API, with the API's path and a trailing `/`:

```hcl
provider "google" {
  project     = "sockerless"
  region      = "us-central1"
  credentials = var.credentials # TF_VAR_credentials=$PWD/bootstrap-key.json

  user_project_override = false

  compute_custom_endpoint         = "http://localhost:4567/compute/v1/"
  cloud_run_v2_custom_endpoint    = "http://localhost:4567/v2/"
  cloudfunctions2_custom_endpoint = "http://localhost:4567/v2/"
  pubsub_custom_endpoint          = "http://localhost:4567/v1/"
  storage_custom_endpoint         = "http://localhost:4567/storage/v1/"
  # one line per API the configuration uses
}
```

`google_service_account` reaches IAM through `iam_beta_custom_endpoint`, and
Cloud SQL takes `http://localhost:4567/sql/v1beta4/`. The Bigtable resources
dial gRPC through `BIGTABLE_EMULATOR_HOST=localhost:4569`.
[`terraform-tests/main.tf`](terraform-tests/main.tf) carries the full block,
[`fixtures/tutorial/main.tf`](terraform-tests/fixtures/tutorial/main.tf) a
small one, and `terraformCmd` in
[`terraform-tests/helpers_test.go`](terraform-tests/helpers_test.go) runs
every command with `TF_VAR_endpoint`, `TF_VAR_credentials` and
`BIGTABLE_EMULATOR_HOST`. [Part 2 of the tutorial](../docs/tutorial.md#part-2-google-cloud)
walks through it by hand. Without `SIM_PERSIST=true`, a restart leaves
Terraform state describing resources that no longer exist.

Over HTTPS, start the Caddy gateway in
[`make/https-gateway/Caddyfile`](../make/https-gateway/Caddyfile) from the
repository root. It terminates TLS for `gcp.sockerless.localhost:8443` and
forwards to the simulator at `SOCKERLESS_GCP_SIM_PORT` (default 4567). Trust
Caddy's local root through `SSL_CERT_FILE`, write the credential file with
`-token-uri https://gcp.sockerless.localhost:8443/token`, and set
`TF_VAR_endpoint=https://gcp.sockerless.localhost:8443` for a configuration
that builds its endpoints from `var.endpoint`, as `main.tf` does.

### Cloud Client Libraries for Python

Application Default Credentials read the same file, and each client takes the
endpoint as `client_options`. No suite runs the Python libraries:

```python
# GOOGLE_APPLICATION_CREDENTIALS=$PWD/bootstrap-key.json
from google.api_core.client_options import ClientOptions
from google.cloud import storage

gcs = storage.Client(project="sockerless",
                     client_options=ClientOptions(api_endpoint="http://localhost:4567"))
bucket = gcs.create_bucket("demo")
bucket.blob("hello.txt").upload_from_string("hello world")
```

The clients generated from protocol buffers, such as `google-cloud-run`, sign
their own JWT instead of exchanging it, which the simulator refuses
([Limits](#limits)).

## Services

[`specs/SIM_SURFACE_TABLES/`](../specs/SIM_SURFACE_TABLES/README.md) lists the
operations of each surface whose routes register under a literal path; the
handler files hold the rest. `buildSimulator` in [`main.go`](main.go)
registers every REST slice, and `registerAllGRPCServices` every gRPC service.

| Area | Service | API host | Transport | Surface table |
|---|---|---|---|---|
| Compute | Compute Engine, with VPC networks, Cloud Load Balancing, Cloud Router and Cloud NAT | `compute.googleapis.com` | REST | [compute](../specs/SIM_SURFACE_TABLES/gcp-compute.md), [instance verbs](../specs/SIM_SURFACE_TABLES/gcp-compute_instance_verbs.md), [images](../specs/SIM_SURFACE_TABLES/gcp-compute_images_catalog.md), [load balancing](../specs/SIM_SURFACE_TABLES/gcp-compute_loadbalancing.md), and the other `gcp-compute_*` tables |
| Compute | Compute Engine metadata server | `metadata.google.internal` | REST | none |
| Compute | Cloud Run: services, jobs, worker pools and instances (Admin API v1 and v2) | `run.googleapis.com`, `*.run.app` | REST | [cloudrun](../specs/SIM_SURFACE_TABLES/gcp-cloudrun.md) |
| Compute | Cloud Run functions | `cloudfunctions.googleapis.com`, `*.cloudfunctions.net` | REST | [cloudfunctions](../specs/SIM_SURFACE_TABLES/gcp-cloudfunctions.md) |
| Developer tools | Cloud Build | `cloudbuild.googleapis.com` | REST | [cloudbuild](../specs/SIM_SURFACE_TABLES/gcp-cloudbuild.md), [regional](../specs/SIM_SURFACE_TABLES/gcp-cloudbuild_regional.md) |
| Developer tools | Artifact Registry, with its Docker registry | `artifactregistry.googleapis.com`, `<location>-docker.pkg.dev` | REST | [artifactregistry](../specs/SIM_SURFACE_TABLES/gcp-artifactregistry.md) |
| Storage | Cloud Storage | `storage.googleapis.com` | REST | [gcs](../specs/SIM_SURFACE_TABLES/gcp-gcs.md), [batch](../specs/SIM_SURFACE_TABLES/gcp-gcs_batch.md), [ACLs](../specs/SIM_SURFACE_TABLES/gcp-gcs_object_acls.md), [IAM](../specs/SIM_SURFACE_TABLES/gcp-gcs_object_iam.md), [contexts](../specs/SIM_SURFACE_TABLES/gcp-gcs_object_contexts.md), [restore](../specs/SIM_SURFACE_TABLES/gcp-gcs_object_restore.md) |
| Databases | Firestore | `firestore.googleapis.com` | REST, gRPC | [firestore](../specs/SIM_SURFACE_TABLES/gcp-firestore.md), [document verbs](../specs/SIM_SURFACE_TABLES/gcp-firestore_document_verbs.md), [change streams](../specs/SIM_SURFACE_TABLES/gcp-firestore_change_streams.md) |
| Databases | Spanner | `spanner.googleapis.com` (REST under `/spanner/v1/`) | REST, gRPC | [spanner](../specs/SIM_SURFACE_TABLES/gcp-spanner.md) |
| Databases | Bigtable: Bigtable Admin API and Bigtable Data API | `bigtableadmin.googleapis.com`, `bigtable.googleapis.com` | REST and gRPC; data gRPC only | [bigtable](../specs/SIM_SURFACE_TABLES/gcp-bigtable.md) |
| Databases | Cloud SQL | `sqladmin.googleapis.com` | REST | [sqladmin](../specs/SIM_SURFACE_TABLES/gcp-sqladmin.md), [blue-green](../specs/SIM_SURFACE_TABLES/gcp-sqladmin_blue_green.md) |
| Databases | Memorystore for Redis and Memorystore for Redis Cluster | `redis.googleapis.com` | REST | [memorystore_redis](../specs/SIM_SURFACE_TABLES/gcp-memorystore_redis.md) |
| Analytics | BigQuery | `bigquery.googleapis.com` | REST | [bigquery](../specs/SIM_SURFACE_TABLES/gcp-bigquery.md) |
| Analytics | Dataflow | `dataflow.googleapis.com` | REST | [dataflow](../specs/SIM_SURFACE_TABLES/gcp-dataflow.md) |
| Messaging | Pub/Sub | `pubsub.googleapis.com` | REST, gRPC | [pubsub](../specs/SIM_SURFACE_TABLES/gcp-pubsub.md) |
| Messaging | Eventarc | `eventarc.googleapis.com` | REST | [eventarc](../specs/SIM_SURFACE_TABLES/gcp-eventarc.md) |
| Messaging | Cloud Scheduler | `cloudscheduler.googleapis.com` | REST | [cloudscheduler](../specs/SIM_SURFACE_TABLES/gcp-cloudscheduler.md) |
| Messaging | Cloud Tasks | `cloudtasks.googleapis.com` | REST | [cloudtasks](../specs/SIM_SURFACE_TABLES/gcp-cloudtasks.md) |
| Networking | Cloud DNS | `dns.googleapis.com` | REST | [dns](../specs/SIM_SURFACE_TABLES/gcp-dns.md) |
| Networking | Serverless VPC Access | `vpcaccess.googleapis.com` | REST | [vpcaccess](../specs/SIM_SURFACE_TABLES/gcp-vpcaccess.md) |
| Networking | API Gateway | `apigateway.googleapis.com` | REST | [apigateway](../specs/SIM_SURFACE_TABLES/gcp-apigateway.md) |
| Identity and security | Identity and Access Management (IAM), with Workload Identity Federation and Workforce Identity Federation | `iam.googleapis.com` | REST | [iam](../specs/SIM_SURFACE_TABLES/gcp-iam.md) |
| Identity and security | IAM Service Account Credentials API | `iamcredentials.googleapis.com` | REST | none |
| Identity and security | OAuth 2.0 token endpoint and Security Token Service | `oauth2.googleapis.com`, `sts.googleapis.com` | REST | [sts](../specs/SIM_SURFACE_TABLES/gcp-sts.md) |
| Identity and security | Cloud Key Management Service (Cloud KMS) | `cloudkms.googleapis.com` | REST, gRPC | [cloudkms](../specs/SIM_SURFACE_TABLES/gcp-cloudkms.md) |
| Identity and security | Secret Manager | `secretmanager.googleapis.com` | REST, gRPC | [secretmanager](../specs/SIM_SURFACE_TABLES/gcp-secretmanager.md) |
| Management | Cloud Resource Manager v1, v2 and v3 | `cloudresourcemanager.googleapis.com` | REST | [cloudresourcemanager](../specs/SIM_SURFACE_TABLES/gcp-cloudresourcemanager.md), [folders v2](../specs/SIM_SURFACE_TABLES/gcp-cloudresourcemanager_folders_v2.md), [capability configs](../specs/SIM_SURFACE_TABLES/gcp-cloudresourcemanager_capability_configs.md) |
| Management | Cloud Billing | `cloudbilling.googleapis.com` | REST | [cloudbilling](../specs/SIM_SURFACE_TABLES/gcp-cloudbilling.md) |
| Management | Service Usage | `serviceusage.googleapis.com` | REST | none |
| Management | Cloud Logging, with Cloud Audit Logs | `logging.googleapis.com` | REST, gRPC | [logging](../specs/SIM_SURFACE_TABLES/gcp-logging.md) |
| Management | Long-running operations | each API's `operations` | REST | [operations_cancel](../specs/SIM_SURFACE_TABLES/gcp-operations_cancel.md) |
| Management | API Discovery Service | `www.googleapis.com/discovery/v1` | REST | [discovery_service](../specs/SIM_SURFACE_TABLES/gcp-discovery_service.md) |

A long-running operation finishes when its work does: Cloud Run's `RunJob` and
Cloud Build's `CreateBuild` return their operation at once and complete it
when the execution or build ends, and `operations.wait` blocks until then.

## How it runs your workloads

[`docs/architecture.md`](../docs/architecture.md) covers what every simulator
shares: placement on containers or Firecracker microVMs, network sidecars,
persistence and recovery. These parts are Google Cloud's own.

### VPC networks

Each subnetwork is an engine network over its own primary range, with the
subnetwork's gateway. A workload on it takes an address the subnetwork
leases: a Compute Engine instance the `networkIP` its interface reports, a
Cloud Run instance or task with Direct VPC egress
(`vpcAccess.networkInterfaces`) or a Serverless VPC Access connector an
address of that subnetwork or connector range. Every other workload, such as a
Cloud Run instance without VPC egress or a Cloud Build step, sits on the
platform network, `sockerless-sim-gcp-platform-<scope>`. Firewall rules decide
whether load balancer proxies and health checkers (`130.211.0.0/22`,
`35.191.0.0/16`) reach an instance backend; two containers on one network
reach each other on every port.

Each network carries a sidecar at the address Google reserves for the
subnetwork's resolver and metadata server, the second-to-last address of its
primary range. Every workload's resolver is that sidecar. A query through it
sees the private, forwarding and peering zones visible to the subnetwork's VPC
network and the response policies bound to it; a query from the platform
network sees public zones only.

### The metadata server

The sidecar forwards metadata requests to the simulator with the caller's
address, so each workload reads its own instance, project and service account.
Every workload receives `GCE_METADATA_HOST`, `GCE_METADATA_IP` and
`GCE_METADATA_ROOT` naming the sidecar, which the Google client libraries
honour, and hosts entries that resolve `metadata.google.internal` and
`metadata` to it. [`docs/instance-metadata.md`](../docs/instance-metadata.md)
lists what the server answers.

### Cloud Run services and Cloud Run functions

Each Cloud Run service is served at its run.app URL,
`https://<service>-<hash>-<region>.a.run.app`, and each function at
`https://<region>-<project>.cloudfunctions.net/<function>`, which forwards to
the Cloud Run service behind it with the function's name taken off the path.
The simulator answers both on its own port: a `lbplane.HostRouter` claims each
request by its `Host` and hands every other request to the APIs.

```sh
curl -H "Host: my-fn-<hash>-us-central1.a.run.app" -H "Authorization: Bearer $ID_TOKEN" \
  http://localhost:4567/hello
```

The front end starts an instance when none is running, and passes the
request and the container's answer through. A request carries a Google-signed
ID token for one of the service's URLs whose principal holds
`run.routes.invoke`, unless the service's policy grants `roles/run.invoker` to
`allUsers`; an access token answers 401. The container receives `PORT`,
`K_SERVICE`, `K_REVISION` and `K_CONFIGURATION`, and what it writes reaches
Cloud Logging under `resource.type="cloud_run_revision"`.

### Cloud Run jobs

`RunJob` creates an execution of `taskCount` tasks (1 by default), at most
`parallelism` at once, each a container with `CLOUD_RUN_JOB`,
`CLOUD_RUN_EXECUTION`, `CLOUD_RUN_TASK_INDEX`, `CLOUD_RUN_TASK_ATTEMPT` and
`CLOUD_RUN_TASK_COUNT`. A task attempt runs until its container exits or
reaches the task template's `timeout` (ten minutes by default), and a failed
task retries up to `maxRetries` times (3 by default). Cancelling an execution
sends SIGTERM, then SIGKILL ten seconds later, as Cloud Run does. Output
reaches Cloud Logging under `resource.type="cloud_run_job"`.

### Compute Engine guests

`instances.insert` boots the image the boot disk names: the public catalog's
`debian-cloud` (`debian-12`, `debian-12-arm64`) and `ubuntu-os-cloud`
(`ubuntu-2404-lts-amd64`, `ubuntu-2404-lts-arm64`) families, each a
digest-pinned image on `mirror.gcr.io`, an image `images.insert` created, or
one captured from a stopped instance's boot disk. The machine takes its vCPUs
and memory from its machine type, and its hostname from its name. On Ubuntu,
cloud-init runs over the `user-data` metadata item; every guest runs the
`startup-script` metadata at each boot and writes its output to the serial
console, which `instances.getSerialPortOutput` reads. Stop, suspend, start,
resume, reset and delete move the machine, and a stopped instance keeps its
disk. An instance is a container by default; `SIM_PLACEMENT=compute=microvm`
boots it as a Firecracker guest, which needs a Linux engine with `/dev/kvm`.

## Test it

| Target | What it runs |
|---|---|
| `make simulator-gcp/unit-test` | The module's own tests: route conformance against the Discovery documents, the gRPC and REST coverage ratchets, and the parsers' fuzz seed corpora. No engine is needed. |
| `make simulator-gcp/sdk-test` | [`sdk-tests/`](sdk-tests/): the Cloud Client Libraries for Go over REST and gRPC |
| `make simulator-gcp/cli-test` | [`cli-tests/`](cli-tests/): gcloud, `bq` and `cbt`. `TestMain` installs a pinned gcloud when none is on `PATH`, its `alpha` and `beta` components, and `cbt`. |
| `make simulator-gcp/terraform-test` | [`terraform-tests/`](terraform-tests/): `hashicorp/google` through `terraform apply`, state assertions and `terraform destroy`, for `main.tf` and each configuration under `fixtures/` |

Each suite builds the simulator into `.build/<suite>/`, starts it on free HTTP
and gRPC ports, waits for its `Listening on` banner, creates the project
`test-project` and stops the simulator at the end. The suites need a
container engine, and the Terraform suite needs `terraform`.
`TERRAFORM_TEST_ARGS` selects tests (`TERRAFORM_TEST_ARGS='-run TestTerraformTutorial'`)
and `TERRAFORM_TEST_TIMEOUT` bounds the run (300 seconds by default);
`SDK_TEST_ARGS` and `CLI_TEST_ARGS` do the same for their suites. On macOS
the Terraform target runs in the Linux test image.
`make simulator-gcp/terraform-https-test` runs the Terraform suite through the
HTTPS gateway, which needs `caddy` (or `CADDY=/path/to/caddy`).

## Limits

- The gRPC port serves plaintext and verifies no credential: a call without a
  valid token acts as the owner.
- The REST APIs accept only access tokens the token endpoint minted. A client
  that signs its own JWT for an API audience, as the Cloud Client Libraries
  for Python generated from protocol buffers do, gets 401.
- `Bigtable.OpenMaterializedView`, `Firestore.ExecutePipeline` and
  `Spanner.FetchCacheUpdate` answer `UNIMPLEMENTED`.
- Cloud DNS private, forwarding and peering zones, response policies and a DNS
  policy's alternative name servers answer only through a network's sidecar.
- A workload reaches the metadata server at its sidecar's address, not at
  `169.254.169.254`.
- `gcloud auth login` and `gcloud auth application-default login` are browser
  flows against Google's consent screen and do not apply.
- Cloud Billing publishes no price catalog: `services.skus.list` answers an
  empty list.

[`BUGS.md`](../BUGS.md) lists every open gap.
