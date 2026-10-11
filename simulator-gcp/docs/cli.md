# Using the GCP simulator with the gcloud CLI

## Prerequisites

- gcloud CLI installed (`gcloud version`)
- Simulator running on `http://localhost:4567`

## Setup

The gcloud CLI takes per-service endpoint overrides from
`CLOUDSDK_API_ENDPOINT_OVERRIDES_*` variables. The simulator verifies an OAuth 2.0
access token on every request; mint one from its token endpoint with the
JWT-bearer grant and hand it to gcloud, in a gcloud configuration of its own:

```sh
SIM=http://localhost:4567
export CLOUDSDK_CONFIG=/tmp/gcloud-sim-config
export CLOUDSDK_AUTH_ACCESS_TOKEN=$(curl -s -X POST $SIM/token \
  -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
export CLOUDSDK_CORE_PROJECT=sockerless
export CLOUDSDK_CORE_DISABLE_PROMPTS=1
```

`sockerless` is the project every simulator starts with; create others through
Cloud Resource Manager. The [simulator README](../README.md#credentials) shows
the service-account key and metadata-server credential paths.

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

Keep the trailing `/`: gcloud appends API paths to the override URL. For a
regional service gcloud prefixes the region onto the host
(`us-central1-localhost:4567`); reach it through `HTTP_PROXY=$SIM` or a resolver
entry.

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
gcloud logging read "resource.type=global" --project=sockerless --format=json
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
  us-central1-docker.pkg.dev/sockerless/docker-hub \
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

### Direct HTTP

Every API also answers plain HTTP with the same bearer token:

```sh
# Cloud Run Jobs — Create a job
curl -X POST http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs?jobId=my-job \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "template": {
      "template": {
        "containers": [{"image": "nginx:latest"}]
      }
    }
  }'

# Cloud Run Jobs — Get a job
curl http://localhost:4567/v2/projects/sockerless/locations/us-central1/jobs/my-job \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN"

# Cloud Functions — Create a function
curl -X POST "http://localhost:4567/v2/projects/sockerless/locations/us-central1/functions?functionId=my-func" \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "buildConfig": {"runtime": "docker"},
    "serviceConfig": {"environmentVariables": {"FOO": "bar"}}
  }'

# GCS — Create a bucket
curl -X POST http://localhost:4567/storage/v1/b \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-bucket"}'

# GCS — Upload an object
curl -X POST "http://localhost:4567/upload/storage/v1/b/my-bucket/o?name=hello.txt" \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: text/plain" \
  -d 'hello world'

# IAM — Create a service account
curl -X POST http://localhost:4567/v1/projects/sockerless/serviceAccounts \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"accountId": "my-sa", "serviceAccount": {"displayName": "My SA"}}'

# Compute — Create a network
curl -X POST http://localhost:4567/compute/v1/projects/sockerless/global/networks \
  -H "Authorization: Bearer $CLOUDSDK_AUTH_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name": "my-network", "autoCreateSubnetworks": false}'
```

## Coverage

[`specs/SIM_PARITY_MATRIX.md`](../../specs/SIM_PARITY_MATRIX.md) lists the
served services, and
[`specs/SIM_TEST_COVERAGE_MATRIX.md`](../../specs/SIM_TEST_COVERAGE_MATRIX.md)
names the gcloud test of each surface (`simulator-gcp/cli-tests/`).

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR` across restarts.
- Point `CLOUDSDK_CONFIG` at a directory of its own so the simulator's settings
  never reach your real gcloud configuration.
