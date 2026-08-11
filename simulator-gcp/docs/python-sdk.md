# Using the GCP simulator with the Google Cloud Python client libraries

## Prerequisites

- Python 3.8+
- Google Cloud client libraries installed (see per-service examples below)
- Simulator running on `http://localhost:4567`

## Setup

Google Cloud Python libraries accept custom API endpoints and anonymous credentials:

```python
from google.auth.credentials import AnonymousCredentials

# Common setup for all clients
ENDPOINT = "http://localhost:4567"
PROJECT = "my-project"
LOCATION = "us-central1"
CREDENTIALS = AnonymousCredentials()
```

Most client libraries accept either `client_options` with `api_endpoint` or an environment variable for emulators.

## Examples

### Google Cloud Storage (`google-cloud-storage`)

```sh
pip install google-cloud-storage
```

GCS supports the `STORAGE_EMULATOR_HOST` environment variable:

```sh
export STORAGE_EMULATOR_HOST=localhost:4567
```

```python
from google.cloud import storage

# Client picks up STORAGE_EMULATOR_HOST automatically
client = storage.Client(
    project="my-project",
    credentials=AnonymousCredentials(),
)

# Create a bucket
bucket = client.create_bucket("my-bucket")

# Upload an object
blob = bucket.blob("hello.txt")
blob.upload_from_string("hello world")

# Download
print(blob.download_as_text())

# List objects
for blob in client.list_blobs("my-bucket"):
    print(blob.name)

# Delete
blob.delete()
bucket.delete()
```

### Cloud DNS (`google-cloud-dns`)

```sh
pip install google-api-python-client
```

```python
from googleapiclient import discovery
from google.auth.credentials import AnonymousCredentials

dns = discovery.build(
    "dns", "v1",
    credentials=AnonymousCredentials(),
    discoveryServiceUrl=f"{ENDPOINT}/dns/v1/$discovery/rest?version=v1",
    # Or use a static service URL:
)

# Since discovery may not work with the simulator, use requests directly:
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Create a managed zone
resp = requests.post(
    f"{ENDPOINT}/dns/v1/projects/{PROJECT}/managedZones",
    headers=headers,
    json={
        "name": "my-zone",
        "dnsName": "example.com.",
        "description": "Test zone",
        "visibility": "private",
    },
)
zone = resp.json()
print(zone["name"])

# Create a record set
resp = requests.post(
    f"{ENDPOINT}/dns/v1/projects/{PROJECT}/managedZones/my-zone/rrsets",
    headers=headers,
    json={
        "name": "www.example.com.",
        "type": "A",
        "ttl": 300,
        "rrdatas": ["10.0.0.1"],
    },
)

# List record sets
resp = requests.get(
    f"{ENDPOINT}/dns/v1/projects/{PROJECT}/managedZones/my-zone/rrsets",
    headers=headers,
)
for rrset in resp.json().get("rrsets", []):
    print(f"{rrset['name']} {rrset['type']} {rrset['rrdatas']}")

# Delete zone
requests.delete(
    f"{ENDPOINT}/dns/v1/projects/{PROJECT}/managedZones/my-zone",
    headers=headers,
)
```

### Compute Engine (`google-cloud-compute`)

```sh
pip install google-cloud-compute
```

```python
from google.cloud.compute_v1 import NetworksClient, SubnetworksClient
from google.api_core.client_options import ClientOptions

options = ClientOptions(api_endpoint=ENDPOINT)

networks = NetworksClient(credentials=CREDENTIALS, client_options=options)
subnets = SubnetworksClient(credentials=CREDENTIALS, client_options=options)

# Note: Compute client library may use gRPC by default.
# For REST-based simulator, use the requests approach below.
```

For direct HTTP (recommended for simulators):

```python
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Create a network
resp = requests.post(
    f"{ENDPOINT}/compute/v1/projects/{PROJECT}/global/networks",
    headers=headers,
    json={"name": "my-network", "autoCreateSubnetworks": False},
)
print(resp.json())

# Create a subnetwork
resp = requests.post(
    f"{ENDPOINT}/compute/v1/projects/{PROJECT}/regions/{LOCATION}/subnetworks",
    headers=headers,
    json={
        "name": "my-subnet",
        "network": f"projects/{PROJECT}/global/networks/my-network",
        "ipCidrRange": "10.0.0.0/24",
    },
)

# List networks
resp = requests.get(
    f"{ENDPOINT}/compute/v1/projects/{PROJECT}/global/networks",
    headers=headers,
)
for net in resp.json().get("items", []):
    print(net["name"])

# Cleanup
requests.delete(f"{ENDPOINT}/compute/v1/projects/{PROJECT}/regions/{LOCATION}/subnetworks/my-subnet", headers=headers)
requests.delete(f"{ENDPOINT}/compute/v1/projects/{PROJECT}/global/networks/my-network", headers=headers)
```

### IAM (`google-cloud-iam`)

```python
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Create a service account
resp = requests.post(
    f"{ENDPOINT}/v1/projects/{PROJECT}/serviceAccounts",
    headers=headers,
    json={
        "accountId": "my-sa",
        "serviceAccount": {"displayName": "My Service Account"},
    },
)
sa = resp.json()
print(sa["email"])

# Get
resp = requests.get(
    f"{ENDPOINT}/v1/projects/{PROJECT}/serviceAccounts/{sa['email']}",
    headers=headers,
)

# List
resp = requests.get(
    f"{ENDPOINT}/v1/projects/{PROJECT}/serviceAccounts",
    headers=headers,
)

# Delete
requests.delete(
    f"{ENDPOINT}/v1/projects/{PROJECT}/serviceAccounts/{sa['email']}",
    headers=headers,
)
```

### Cloud Run Jobs

```python
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Create a job
resp = requests.post(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/jobs?jobId=my-job",
    headers=headers,
    json={
        "template": {
            "template": {
                "containers": [{"image": "nginx:latest"}],
            },
        },
    },
)
print(resp.json())

# Get the job
resp = requests.get(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/jobs/my-job",
    headers=headers,
)
print(resp.json()["name"])

# Run the job (create execution)
resp = requests.post(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/jobs/my-job:run",
    headers=headers,
    json={},
)

# Delete
requests.delete(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/jobs/my-job",
    headers=headers,
)
```

### Cloud Functions v2

```python
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Create a function
resp = requests.post(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/functions?functionId=my-func",
    headers=headers,
    json={
        "buildConfig": {"runtime": "docker"},
        "serviceConfig": {
            "environmentVariables": {"FOO": "bar"},
        },
    },
)
func = resp.json()

# Get
resp = requests.get(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/functions/my-func",
    headers=headers,
)

# Delete
requests.delete(
    f"{ENDPOINT}/v2/projects/{PROJECT}/locations/{LOCATION}/functions/my-func",
    headers=headers,
)
```

### Cloud Logging (`google-cloud-logging`)

```python
import requests

headers = {"Authorization": "Bearer local-test-token", "Content-Type": "application/json"}

# Write log entries
requests.post(
    f"{ENDPOINT}/v2/entries:write",
    headers=headers,
    json={
        "logName": f"projects/{PROJECT}/logs/my-log",
        "resource": {"type": "global"},
        "entries": [
            {"textPayload": "hello from python"},
            {"textPayload": "another log line"},
        ],
    },
)

# List log entries
resp = requests.post(
    f"{ENDPOINT}/v2/entries:list",
    headers=headers,
    json={
        "resourceNames": [f"projects/{PROJECT}"],
        "pageSize": 100,
    },
)
for entry in resp.json().get("entries", []):
    print(entry.get("textPayload", ""))
```

## Approach summary

| Service | Recommended Client | Notes |
|---------|-------------------|-------|
| GCS | `google-cloud-storage` | Use `STORAGE_EMULATOR_HOST` env var |
| DNS | `requests` (direct HTTP) | Discovery-based clients may not connect |
| Compute | `requests` (direct HTTP) | gRPC clients won't work with HTTP simulator |
| IAM | `requests` (direct HTTP) | |
| Cloud Run Jobs | `requests` (direct HTTP) | |
| Cloud Functions | `requests` (direct HTTP) | |
| Cloud Logging | `requests` (direct HTTP) | |
| Artifact Registry | `requests` or Docker CLI | OCI Distribution API at `/v2/` |

For services beyond GCS, the simplest approach is using `requests` with direct HTTP calls. The simulator speaks REST/JSON, and most Google Cloud Python libraries default to gRPC transport which is not supported.

## Notes

- Authentication is not validated. Any Bearer token or `AnonymousCredentials` will work.
- All state is in-memory and resets when the simulator restarts.
- The simulator is HTTP-only (REST/JSON). gRPC-based client libraries need either a REST transport option or direct HTTP calls.
