# Using the GCP simulator with the Google Cloud Python client libraries

## Prerequisites

- The Google Cloud client libraries for the services you use, `google-auth` and
  `requests`
- Simulator running on `http://localhost:4567`

## Setup

The simulator verifies an OAuth 2.0 access token on every request. Mint one
from its token endpoint with the JWT-bearer grant and wrap it in a
`google.oauth2.credentials.Credentials`:

```python
import requests
from google.oauth2.credentials import Credentials

ENDPOINT = "http://localhost:4567"
PROJECT = "sockerless"  # the project every simulator starts with
LOCATION = "us-central1"

TOKEN = requests.post(
    f"{ENDPOINT}/token",
    data={"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer"},
).json()["access_token"]
CREDENTIALS = Credentials(TOKEN)
headers = {"Authorization": f"Bearer {TOKEN}", "Content-Type": "application/json"}
```

The REST APIs answer on the HTTP port; the gRPC services (Cloud Logging,
Bigtable, Firestore, Pub/Sub, Spanner, Cloud KMS, Secret Manager) also answer
on `:4569`. The examples below use `requests` with the same token where a
client library would need its endpoint and transport configured per service.

## Examples

### Cloud Storage (`google-cloud-storage`)

```sh
pip install google-cloud-storage
```

The client reads the `STORAGE_EMULATOR_HOST` coordinate:

```sh
export STORAGE_EMULATOR_HOST=localhost:4567
```

```python
from google.cloud import storage

client = storage.Client(project=PROJECT, credentials=CREDENTIALS)

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

### Cloud DNS

```python
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

### Compute Engine

```python
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

### IAM

```python
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

### Cloud Logging

```python
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

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR` across restarts.
- The [simulator README](../README.md#credentials) shows the service-account key
  and metadata-server credential paths, which Application Default Credentials
  resolve through.
