# simulator-gcp

Local reimplementation of a slice of Google Cloud. Cloud Run job executions honour the task template's `timeout`, Cloud Run services and functions serve requests from their own containers and log what those containers write, Cloud Logging entries are queryable with the standard filter syntax, and Artifact Registry stores real OCI images.

## Reference adaptor

The simulator exposes one HTTP endpoint (default `:4567`) that fronts all GCP services. Three external tools exercise that endpoint at GCP-API fidelity:

| Adaptor | Min version | What it proves |
|---|---|---|
| [GCP Go SDK](https://pkg.go.dev/cloud.google.com/go) (`cloud.google.com/go/{run,functions,storage,logging,...}`) | the releases `sdk-tests/go.mod` pins | Wire-level SDK compatibility — request/response shapes, error envelopes, pagination, long-running operation polling. |
| [`gcloud` CLI](https://cloud.google.com/sdk/docs/install) | the host's gcloud, or 500.0.0, which `cli-tests` installs when absent | Endpoint-override fidelity (`CLOUDSDK_API_ENDPOINT_OVERRIDES_*`), through gcloud's own request building. |
| [Terraform `google` provider](https://registry.terraform.io/providers/hashicorp/google/latest/docs) | the version `terraform-tests` pins | Full plan → apply → destroy round-trip across `google_cloud_run_v2_job`, `google_cloudfunctions2_function`, `google_storage_bucket`, `google_artifact_registry_repository`, `google_dns_managed_zone`, `google_compute_network`, `google_service_account`, etc. |

Anything any of these three tools does against the real GCP endpoint, it must do against this simulator. Gaps from that contract are real bugs (see [BUGS.md](../BUGS.md)).


## Validation

| Test path | What runs |
|---|---|
| `sdk-tests/` | Real `cloud.google.com/go/*` clients against the simulator. Per-operation assertions on response shape and error codes, including Artifact Registry remote-repository and OCI paths. |
| `cli-tests/` | The real `gcloud` CLI invoked via `os/exec`, parsing its JSON output, including Artifact Registry endpoint overrides. `TestMain` installs gcloud and `cbt` when they are absent. |
| `terraform-tests/` | The real Terraform `google` provider: `terraform apply`, assert resource state, `terraform destroy`. |
| `make simulator-gcp/test` | The module's unit tests, per [`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md). |

CI runs all four on every PR (`.github/workflows/ci.yml`).

## Wiring the adaptor

```bash
# 1. Build + start the sim (default :4567).
cd simulator-gcp
go build -tags noui -o simulator-gcp .
./simulator-gcp
```

With `SIM_PERSIST=true`, `SIM_DATA_DIR` is the persistence root: the SQLite
control-plane store plus the bulk-data roots that default beneath it —
`<SIM_DATA_DIR>/gcs` (object bytes; `SIM_GCS_DATA_DIR` overrides) and
`<SIM_DATA_DIR>/spanner` (file-backed database engines) — so payload bytes
survive restarts alongside the metadata that describes them.

```bash
# 2. Point gcloud at it with a token the simulator minted (see Credentials).
export CLOUDSDK_AUTH_ACCESS_TOKEN=$(curl -s -X POST http://localhost:4567/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
export CLOUDSDK_CORE_PROJECT=sockerless
export CLOUDSDK_API_ENDPOINT_OVERRIDES_STORAGE=http://localhost:4567/

gcloud storage buckets list
```

`sockerless` is the project every simulator starts with. The Go storage client
reads `STORAGE_EMULATOR_HOST=localhost:4567`.

### Credentials

The data plane verifies an OAuth2 access token on every request, so a client
needs a real credential. Both of Google's non-interactive credential paths work
against the simulator, differing from real Google only in the coordinates.

**Service-account key** — the `gcloud auth activate-service-account` path. Mint
a key through the real IAM API and point the credential's token exchange at the
simulator; the token endpoint verifies the assertion's signature against the
public half IAM registered, so only a genuinely minted key authenticates.

Creating that first account is itself an authenticated call, so it is made with
a token straight from the token endpoint — the simulator's stand-in for the
administrator who bootstraps a real project. The administrator creates the
project first, since the simulator holds no project but its own `sockerless`:

```bash
SIM=http://localhost:4567
TOKEN=$(curl -s -X POST $SIM/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"projectId":"test-project","parent":"organizations/123456789012"}' \
  $SIM/v3/projects

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"accountId":"runner"}' \
  $SIM/v1/projects/test-project/serviceAccounts

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}' \
  $SIM/v1/projects/test-project/serviceAccounts/runner@test-project.iam.gserviceaccount.com/keys \
  | python3 -c 'import sys,json,base64; sys.stdout.write(base64.b64decode(json.load(sys.stdin)["privateKeyData"]).decode())' \
  > key.json
```

From there gcloud takes over, with `auth/token_host` as the only coordinate the
credential needs:

```bash
gcloud config set auth/token_host $SIM/token
gcloud config set api_endpoint_overrides/iam $SIM/
gcloud auth activate-service-account --key-file=key.json
gcloud iam service-accounts list
```

Once activated, `gcloud iam service-accounts keys create` mints every subsequent
key.

**Metadata server** — the on-instance path, and the one Application Default
Credentials resolve through. Point the metadata-server coordinate at the
simulator and gcloud discovers its identity and mints tokens on its own, with no
key file and no interactive login:

```bash
export GCE_METADATA_HOST=localhost:4567
export GCE_METADATA_IP=localhost:4567
export GCE_METADATA_ROOT=localhost:4567

gcloud auth list                                    # the metadata identity
gcloud auth application-default print-access-token  # ADC through the same path
```

`gcloud auth login` (interactive) and `gcloud auth application-default login`
are browser flows against Google's own consent screen and do not apply. For a
federated operator identity, `gcloud auth login --cred-file` with an
`external_account` credential works too — the simulator serves the Security
Token Service token-exchange and introspection endpoints the file names.

**Regional services.** Given an endpoint override, gcloud prefixes the region
onto the host for regional services — `us-central1-localhost:4567`, mirroring
the real `us-central1-run.googleapis.com`. The simulator answers that host form;
the client just has to be able to reach it, either by resolving the prefixed
name (a hosts-file entry, or `*.localhost` on Linux) or by routing through
gcloud's proxy setting:

```bash
export CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN=http://localhost:4567/
export HTTP_PROXY=http://localhost:4567
gcloud run services list --region=us-central1
```

Cloud Run and Cloud Functions calls require Docker or Podman to execute
workloads. For API-only checks that do not invoke workload execution,
`SIM_RUNTIME=process` starts the GCP simulator without initializing
Docker/Podman.
The `/health` response reports `runtime` and
`capabilities.workloadExecution`; clients must require that capability before
submitting work that needs a running container.

For Terraform:

```hcl
provider "google" {
  project = "sockerless"
  region  = "us-central1"

  endpoints = {
    cloud_run_v2     = "http://localhost:4567/"
    cloudfunctions2  = "http://localhost:4567/"
    artifact_registry = "http://localhost:4567/"
    storage          = "http://localhost:4567/"
    # …any service you exercise.
  }
}
```

## Services

The simulator serves the Discovery documents vendored under
[`specs/cloud-api/gcp`](../specs/cloud-api/gcp) — 31 APIs over REST, and the
gRPC services on the HTTP port plus two (`:4569`, `SIM_GCP_GRPC_PORT`).
[`specs/SIM_PARITY_MATRIX.md`](../specs/SIM_PARITY_MATRIX.md) lists them, and
[`specs/SIM_SURFACE_TABLES/`](../specs/SIM_SURFACE_TABLES/README.md) holds the
per-method tables. A long-running operation finishes when its work does: Cloud
Run's `RunJob` and Cloud Build's `CreateBuild` return their operation at once and
complete it when the execution or build ends, and `operations.wait` blocks until
then.

## Building

```bash
cd simulator-gcp && go build -tags noui -o simulator-gcp .
```

## Sample

End-to-end via `gcloud` + `curl`:

```bash
$ SIM_LISTEN_ADDR=:4567 ./simulator-gcp &
$ export CLOUDSDK_API_ENDPOINT_OVERRIDES_RUN=http://localhost:4567/

# Create a job
$ curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs?jobId=hello-job' \
    -H 'Content-Type: application/json' \
    -d '{"template":{"template":{"timeout":"10s","containers":[{"image":"alpine","command":["echo","hello"]}]}}}'
{"done": true, "response": {"name":"projects/sockerless/locations/us-central1/jobs/hello-job",...}}

# Run it
$ curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs/hello-job:run' -d '{}'

# Check via gcloud
$ gcloud run jobs executions list --job hello-job --region us-central1
NAME             STATUS     COMPLETION_TIME
hello-job-...    Succeeded  2026-...
```

[Extended examples](#extended-examples) below cover Cloud Run jobs, Cloud Run functions, Cloud Logging, Cloud Storage and Artifact Registry; [API_SPEC.md](API_SPEC.md) describes the wire shapes.

## Project structure

One Go file per service family at the module root (`cloudrunjobs.go`,
`cloudfunctions.go`, `gcs.go`, `artifactregistry.go`, `compute*.go`,
`spanner*.go`, …), each registering its routes in `buildSimulator` in
`main.go`, with the gRPC services mounted by `registerAllGRPCServices`. The
framework — server, container-engine layer, durable state, OCI data plane — is
the [`sim`](../sim/README.md) module; the Google-specific pieces of it (the
error envelope and the sandbox profiles) live here. `sdk-tests/`, `cli-tests/`
and `terraform-tests/` are separate Go modules driving the Google Cloud client
libraries, gcloud and terraform-provider-google against a running simulator.

## Testing

```bash
# SDK tests (cloud.google.com/go clients against the running sim)
cd sdk-tests && go test -v ./...

# CLI tests (gcloud CLI shell-outs)
cd cli-tests && go test -v ./...

# Terraform tests (real terraform apply against the sim)
cd terraform-tests && go test -v ./...
```

Each suite's `TestMain` builds the simulator, starts it on a free port, waits for its `Listening on` banner (`testutil/simready`), runs the tests and stops it. The suites need a container engine; the CLI suite installs its CLI when absent.

## Execution model

Every Cloud Run job execution, service instance, worker pool instance and Cloud Run function is a real container on the engine the simulator started against, under the Cloud Run sandbox profile, with its output streamed into Cloud Logging as it is produced. A job task runs until its container exits, is cancelled, or reaches the task template's `timeout`, which the simulator enforces the way Cloud Run does; a cancelled execution's container gets SIGTERM and, ten seconds later as on Cloud Run, SIGKILL. Services and functions are invoked over HTTP at their run.app (and cloudfunctions.net) URLs, which reach the container's own listener.

## Known issues

Open simulator bugs live in [BUGS.md](../BUGS.md).

## What's out of scope

- **Three gRPC methods**: the gRPC door (default `:4569`) serves 213 of 216 declared methods; `Bigtable.OpenMaterializedView`, `Firestore.ExecutePipeline` and `Spanner.FetchCacheUpdate` each need state the simulator does not hold and answer `Unimplemented`.
- **Private zones in container workloads**: Cloud DNS answers public managed zones over UDP and TCP on `SIM_DNS_PORT` (a kernel-chosen port when unset) and recurses for other names through `SIM_DNS_UPSTREAM` or the host's resolvers. A Compute Engine machine asks it at the metadata server's address and sees the private, forwarding and peering zones visible to its network, the response policies bound to it, and its DNS policy's alternative name servers; container workloads resolve private zones only through the container engine's embedded DNS.
- **Google's interactive consent screen**: `gcloud auth login` and `gcloud auth
  application-default login` are browser flows against accounts.google.com. The
  non-interactive credential paths — service-account keys, the metadata server,
  and workforce federation — all work; see [Credentials](#credentials).
- **Published price catalogs**: Cloud Billing's `services.skus.list` answers an
  empty listing, since this installation publishes no price sheet.

## Extended examples

Every request carries the token from [Credentials](#credentials) as
`-H "Authorization: Bearer $TOKEN"`.

### Cloud Run Jobs

Create a job, run it (creating an execution), and check status.

**Create a job:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs?jobId=hello-job' \
  -H 'Content-Type: application/json' \
  -d '{
    "template": {
      "template": {
        "timeout": "10s",
        "containers": [{"image": "alpine", "command": ["echo", "hello world"]}]
      }
    }
  }'
```

The response is a long-running operation with `"done": true` and the job in the `response` field.

**Run the job:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs/hello-job:run' \
  -H 'Content-Type: application/json' -d '{}'
```

The response is a long-running operation that stays `"done": false` while the
execution runs, with the Execution in `metadata`. Cloud Run completes it when
the execution finishes: the Execution in `response` when every task succeeded,
an `error` otherwise. `operations.wait` blocks until then:

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/<operation-name>:wait' \
  -H 'Content-Type: application/json' -d '{"timeout":"120s"}'
```

**Check execution status:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:4567/v2/<exec-name>
# Running:  {"runningCount": 1, "succeededCount": 0, "failedCount": 0}
# Done:     {"runningCount": 0, "succeededCount": 1, "completionTime": "2026-..."}
```

**Query execution logs via Cloud Logging:**

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/entries:list' \
  -H 'Content-Type: application/json' \
  -d '{"resourceNames":["projects/sockerless"],"filter":"resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"hello-job\""}'
```

**Go over plain HTTP** (the Cloud Run v2 Go client defaults to gRPC, which the simulator does not serve for Cloud Run; `token` is an access token from the token endpoint):

```go
import (
    "encoding/json"
    "net/http"
    "strings"
)

job := map[string]any{
    "template": map[string]any{
        "template": map[string]any{
            "timeout": "5s",
            "containers": []map[string]any{
                {"image": "alpine", "command": []string{"echo", "hello"}},
            },
        },
    },
}
body, _ := json.Marshal(job)
req, _ := http.NewRequest("POST",
    "http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs?jobId=sdk-job",
    strings.NewReader(string(body)))
req.Header.Set("Content-Type", "application/json")
req.Header.Set("Authorization", "Bearer "+token)
http.DefaultClient.Do(req)

runReq, _ := http.NewRequest("POST",
    "http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs/sdk-job:run",
    strings.NewReader("{}"))
runReq.Header.Set("Content-Type", "application/json")
runReq.Header.Set("Authorization", "Bearer "+token)
http.DefaultClient.Do(runReq)
```

### Cloud Functions

Create a function, deploy a container to the Cloud Run service that serves it,
and request it with a Google-signed ID token for one of its URLs (`$ID_TOKEN`;
an access token answers 401) whose principal holds `run.routes.invoke`, unless
the service's policy grants `roles/run.invoker` to `allUsers`. CreateFunction creates that service and reports its run.app URL
as `serviceConfig.uri`; the function's `url` is
`https://<region>-<project>.cloudfunctions.net/<function>`. The simulator serves
both hosts on its own endpoint and dispatches on the `Host` header, the address
a resolver pointed at the simulator hands a client. The cloudfunctions.net front
end takes the function's name off the front of the path, and both pass the
container's answer through.

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/functions?functionId=my-fn' \
  -H 'Content-Type: application/json' \
  -d '{"buildConfig":{"runtime":"go121","entryPoint":"Handler"}}'
# serviceConfig.service: projects/sockerless/locations/us-central1/services/my-fn
# serviceConfig.uri:     https://my-fn-<hash>-us-central1.a.run.app
# url:                   https://us-central1-sockerless.cloudfunctions.net/my-fn

curl -s -H "Authorization: Bearer $TOKEN" -X PATCH 'http://localhost:4567/v2/projects/sockerless/locations/us-central1/services/my-fn' \
  -H 'Content-Type: application/json' \
  -d '{"template":{"containers":[{"image":"my-function-image"}]}}'

curl -s -X POST 'http://localhost:4567/my-fn/hello' \
  -H 'Host: us-central1-sockerless.cloudfunctions.net' \
  -H "Authorization: Bearer $ID_TOKEN" -d '{}'
# The container receives POST /hello; its stdout reaches Cloud Logging under
# resource.type="cloud_run_revision" AND resource.labels.service_name="my-fn".
```

### Cloud Logging

Write and list log entries over REST; the gRPC `LoggingServiceV2` answers on `:4569`.

```bash
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/entries:write' \
  -H 'Content-Type: application/json' \
  -d '{"logName":"projects/sockerless/logs/my-app",
       "resource":{"type":"global"},
       "entries":[{"textPayload":"Server started"}]}'

curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v2/entries:list' \
  -H 'Content-Type: application/json' \
  -d '{"resourceNames":["projects/sockerless"],
       "filter":"logName=\"projects/sockerless/logs/my-app\""}'
```

Filters follow the Logging query language: AND, OR, NOT and parentheses, comparisons on any field, `=~`/`!~` regular expressions, severity by level, timestamps as times, and bare values as global restrictions.

### GCS (Cloud Storage)

```bash
export STORAGE_EMULATOR_HOST=localhost:4567

curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/storage/v1/b?project=sockerless' -d '{"name":"my-bucket"}'
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/upload/storage/v1/b/my-bucket/o?name=hello.txt' \
  -H 'Content-Type: text/plain' -d 'hello world'
curl -s -H "Authorization: Bearer $TOKEN" http://localhost:4567/download/storage/v1/b/my-bucket/o/hello.txt
# => hello world

curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  'http://localhost:4567/storage/v1/b/my-bucket/o/hello.txt/rewriteTo/b/my-bucket/o/copy.txt' \
  -H 'Content-Type: application/json' \
  -d '{"contentType":"text/plain","cacheControl":"no-cache","metadata":{"copied":"true"}}'
```

The Go client reads `STORAGE_EMULATOR_HOST`; give it an HTTP client that carries the token:

```go
os.Setenv("STORAGE_EMULATOR_HOST", "localhost:4567")
client, _ := storage.NewClient(ctx, option.WithHTTPClient(oauth2.NewClient(ctx,
    oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}))))
client.Bucket("sdk-bucket").Create(ctx, "sockerless", nil)
client.Bucket("sdk-bucket").Object("copy.txt").
    CopierFrom(client.Bucket("sdk-bucket").Object("hello.txt")).
    Run(ctx)
```

Copy/rewrite persists the destination object resource metadata that Cloud Storage exposes publicly: custom metadata, cache control, content disposition, content encoding, content language, storage class, and custom time. Destination fields override copied source fields; absent fields inherit from the source object.

### Artifact Registry

```bash
# Create a Docker-format repository
curl -s -H "Authorization: Bearer $TOKEN" -X POST 'http://localhost:4567/v1/projects/sockerless/locations/us-central1/repositories?repositoryId=my-repo' \
  -H 'Content-Type: application/json' \
  -d '{"format":"DOCKER"}'
```

The simulator also supports OCI Distribution endpoints under `/v2/` for pushing and pulling container images per the [OCI Distribution spec](https://github.com/opencontainers/distribution-spec).
