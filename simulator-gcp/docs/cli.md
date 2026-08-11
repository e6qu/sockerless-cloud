# Using the GCP simulator with the gcloud CLI

## Prerequisites

- gcloud CLI installed (`gcloud version`)
- Simulator running on `http://localhost:4567`

## Setup

The gcloud CLI supports per-service endpoint overrides via `CLOUDSDK_API_ENDPOINT_OVERRIDES_*` environment variables. Set up an isolated gcloud config and a local-test auth token:

```sh
export CLOUDSDK_CONFIG=/tmp/gcloud-sim-config
export CLOUDSDK_AUTH_ACCESS_TOKEN=local-test-gcp-token
export CLOUDSDK_CORE_PROJECT=my-project
export CLOUDSDK_CORE_DISABLE_PROMPTS=1
```

Then override the endpoints for the services you need:

```sh
export CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDFUNCTIONS=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_SERVICEUSAGE=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_VPCACCESS=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_ARTIFACTREGISTRY=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_STORAGE=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_REDIS=http://localhost:4567/
export CLOUDSDK_API_ENDPOINT_OVERRIDES_SQL=http://localhost:4567/
```

Note the trailing `/` — gcloud appends API paths directly to the override URL.

## Examples

### Cloud DNS

```sh
# Create a managed zone
gcloud dns managed-zones create my-zone \
  --dns-name=example.com. \
  --description="Test zone" \
  --visibility=private \
  --format=json

# Describe a zone
gcloud dns managed-zones describe my-zone --format=json

# List zones
gcloud dns managed-zones list --format=json

# Delete a zone
gcloud dns managed-zones delete my-zone
```

Cloud DNS record-set mutation also works through the public transaction and update flows:

```sh
gcloud dns record-sets transaction start --zone=my-zone
gcloud dns record-sets transaction add 203.0.113.10 \
  --name=www.example.com. \
  --ttl=300 \
  --type=A \
  --zone=my-zone
gcloud dns record-sets transaction execute --zone=my-zone

gcloud dns record-sets update www.example.com. \
  --zone=my-zone \
  --type=A \
  --ttl=60 \
  --rrdatas=203.0.113.11
```

### Memorystore Redis

```sh
gcloud redis instances create my-redis \
  --region=us-central1 \
  --tier=basic \
  --size=1 \
  --redis-version=redis_6_x \
  --format=json

gcloud redis instances describe my-redis --region=us-central1 --format=json
gcloud redis instances delete my-redis --region=us-central1
```

### Cloud SQL

```sh
gcloud sql instances create my-sql \
  --database-version=POSTGRES_15 \
  --tier=db-custom-1-3840 \
  --region=us-central1 \
  --format=json

gcloud sql databases create appdb --instance=my-sql --format=json
gcloud sql users create appuser --instance=my-sql --password=local-password --format=json
gcloud sql databases list --instance=my-sql --format=json
gcloud sql users list --instance=my-sql --format=json
```

### Cloud Logging

```sh
# List log entries (via gcloud or direct HTTP)
gcloud logging read "resource.type=global" --project=my-project --format=json
```

### Service Usage

```sh
# Enable a service
gcloud services enable compute.googleapis.com

# List enabled services
gcloud services list --enabled --format=json
```

### VPC Access

```sh
# Create a connector
gcloud compute networks vpc-access connectors create my-connector \
  --region=us-central1 \
  --network=default \
  --range=10.8.0.0/28

# List connectors
gcloud compute networks vpc-access connectors list \
  --region=us-central1 \
  --format=json
```

### Artifact Registry

```sh
# Create a Docker Hub remote repository
gcloud artifacts repositories create docker-hub \
  --location=us-central1 \
  --repository-format=docker \
  --mode=remote-repository \
  --remote-docker-repo=docker-hub \
  --disable-remote-validation \
  --format=json

# List cached Docker images through the Artifact Registry API surface
gcloud artifacts docker images list \
  us-central1-docker.pkg.dev/my-project/docker-hub \
  --include-tags \
  --format=json
```

### Cloud Storage

```sh
gcloud storage buckets create gs://my-bucket --location=us --format=json
printf 'hello world' > hello.txt
gcloud storage cp hello.txt gs://my-bucket/hello.txt
gcloud storage objects list gs://my-bucket --format=json
gcloud storage cat gs://my-bucket/hello.txt
gcloud storage rm gs://my-bucket/hello.txt
gcloud storage buckets delete gs://my-bucket
```

### Direct HTTP (for services without CLI endpoint overrides)

Some services work better with direct HTTP calls since gcloud doesn't support endpoint overrides for all APIs:

```sh
# Cloud Run Jobs — Create a job
curl -X POST http://localhost:4567/v2/projects/my-project/locations/us-central1/jobs?jobId=my-job \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: application/json" \
  -d '{
    "template": {
      "template": {
        "containers": [{"image": "nginx:latest"}]
      }
    }
  }'

# Cloud Run Jobs — Get a job
curl http://localhost:4567/v2/projects/my-project/locations/us-central1/jobs/my-job \
  -H "Authorization: Bearer local-test-gcp-token"

# Cloud Functions — Create a function
curl -X POST "http://localhost:4567/v2/projects/my-project/locations/us-central1/functions?functionId=my-func" \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: application/json" \
  -d '{
    "buildConfig": {"runtime": "docker"},
    "serviceConfig": {"environmentVariables": {"FOO": "bar"}}
  }'

# GCS — Create a bucket
curl -X POST http://localhost:4567/storage/v1/b \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-bucket"}'

# GCS — Upload an object
curl -X POST "http://localhost:4567/upload/storage/v1/b/my-bucket/o?name=hello.txt" \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: text/plain" \
  -d 'hello world'

# IAM — Create a service account
curl -X POST http://localhost:4567/v1/projects/my-project/serviceAccounts \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: application/json" \
  -d '{"accountId": "my-sa", "serviceAccount": {"displayName": "My SA"}}'

# Compute — Create a network
curl -X POST http://localhost:4567/compute/v1/projects/my-project/global/networks \
  -H "Authorization: Bearer local-test-gcp-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-network", "autoCreateSubnetworks": false}'
```

## Supported services

| Service | gcloud Subcommand | Endpoint Override | Notes |
|---------|------------------|-------------------|-------|
| Cloud DNS | `gcloud dns` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS` | Full CLI support |
| Cloud Logging | `gcloud logging` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING` | Full CLI support |
| Service Usage | `gcloud services` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_SERVICEUSAGE` | Full CLI support |
| VPC Access | `gcloud compute networks vpc-access` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_VPCACCESS` | Full CLI support |
| Cloud Functions | `gcloud functions` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDFUNCTIONS` | Deploy may require direct HTTP |
| Memorystore Redis | `gcloud redis instances` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_REDIS` | Instance lifecycle |
| Cloud SQL | `gcloud sql` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_SQL` | Instance, database, and user lifecycle |
| Cloud Storage | `gcloud storage` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_STORAGE` | Bucket and object lifecycle |
| Cloud Run Jobs | — | — | Use direct HTTP |
| GCS | — | — | Use direct HTTP or `STORAGE_EMULATOR_HOST` |
| Artifact Registry | `gcloud artifacts` | `CLOUDSDK_API_ENDPOINT_OVERRIDES_ARTIFACTREGISTRY` | Repository CRUD and Docker image listing |
| Compute | — | — | Use direct HTTP |
| IAM | — | — | Use direct HTTP |

## Automated bash tests

A self-contained bash test script exercises Cloud DNS, Service Usage, Cloud Logging, Cloud Run Jobs, and GCS in both text and JSON output modes:

```sh
cd simulator-gcp/bash-tests
./test_gcp_cli.sh
```

The script builds the simulator, starts it on a random port, runs 33 tests, and prints a pass/fail summary.

## Notes

- Authentication is accepted but not validated. Any Bearer token will work.
- All state is in-memory and resets when the simulator restarts.
- `CLOUDSDK_CONFIG` should point to an isolated directory to avoid interfering with your real gcloud configuration.
