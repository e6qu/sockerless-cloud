# Tutorial: one stack on three local clouds

This tutorial builds the same application stack on the AWS, Google Cloud and
Azure simulators with Terraform: a private network with a NAT gateway, a
virtual machine, a container service, object storage, a key-value or document
store, a queue, a function, and logs and monitoring. Each cloud uses its own
services and its own Terraform provider, unmodified. Only the provider's
endpoints point at a simulator.

| Piece | AWS | Google Cloud | Azure |
|---|---|---|---|
| Network | Amazon VPC | VPC network and subnetwork | Virtual Network |
| NAT | NAT gateway | Cloud NAT on a Cloud Router | NAT Gateway |
| Virtual machine | Amazon EC2 | Compute Engine | Linux virtual machine |
| Container service | Amazon ECS on AWS Fargate | Cloud Run | Azure Container Apps |
| Object storage | Amazon S3 | Cloud Storage | Blob Storage |
| Key-value or document store | Amazon DynamoDB | Firestore | Azure Cosmos DB for NoSQL |
| Queue | Amazon SQS | Pub/Sub | Azure Service Bus |
| Function | AWS Lambda | Cloud Run functions | Azure Functions |
| Logs and monitoring | Amazon CloudWatch Logs and a metric alarm | A Cloud Logging log-based metric | Log Analytics and Application Insights |

The tutorial is tested end to end. Each cloud's part is a Terraform test that
applies the same configuration this tutorial applies, checks the same results,
plans again expecting no changes, and destroys the stack:
`TestTutorialStack` in `simulator-aws/terraform-tests/tutorial`, and
`TestTerraformTutorial` in `simulator-gcp/terraform-tests` and
`simulator-azure/terraform-tests`. CI runs them as `tf (aws tutorial)`,
`tf (gcp tutorial)` and `tf (azure tutorial)`.

Do the steps in order. Each part starts and stops its own simulator, so you can
stop after any part.

## Step 1: Install the tools

You need:

- **Go**, the version `simulator-aws/go.mod` names, to install the simulators.
- **Docker Engine** (or Podman behind the Docker socket). The simulators run
  every virtual machine, container and function as a container on it.
- **Terraform** 1.16 or later.
- **curl** and **jq**, for the Google Cloud token and the Azure function call.
- **Caddy** 2, for the Azure part only.

Check them:

```sh
go version
docker info --format '{{.ServerVersion}}'
terraform version
caddy version
```

## Step 2: Get the tutorial's files

The Terraform configurations live in the repository beside their tests:

```sh
git clone https://github.com/e6qu/sockerless-cloud.git
cd sockerless-cloud
export TUTORIAL_ROOT="$PWD"
```

## Step 3: Install the three simulators

```sh
go install github.com/e6qu/sockerless-cloud/simulator-aws@latest
go install github.com/e6qu/sockerless-cloud/simulator-gcp@latest
go install github.com/e6qu/sockerless-cloud/simulator-azure@latest
```

Each command puts a binary in `$(go env GOPATH)/bin`. Make sure that directory
is on your `PATH`.

## Part 1: AWS

### Step 4: Start the AWS simulator

In a terminal of its own:

```sh
simulator-aws
```

It listens on port 4566 and prints a `Listening on` line once it's ready.

### Step 5: Apply the AWS stack

Back in your first terminal:

```sh
cd "$TUTORIAL_ROOT/simulator-aws/terraform-tests/tutorial"
terraform init
terraform apply -auto-approve
```

The configuration (`main.tf`) sets the AWS provider's `endpoints` to
`http://localhost:4566`. Nothing else in it is simulator-specific. Apply takes
a few minutes. The simulator does the work AWS would do:
- it boots the EC2 instance;
- it starts the Fargate task and waits for it to run;
- it builds the Lambda function's environment from the `python3.12` runtime
  image, and invokes the function once.

### Step 6: Read the results

```sh
terraform output
```

You should see:

- `instance_state = "running"`, and an `instance_private_ip` inside
  `10.240.2.0/24`, the private subnet;
- a `nat_gateway_public_ip`, the NAT gateway's Elastic IP;
- `s3_object_body = "Hello from S3"`, read back from the bucket;
- `lambda_result = "{\"received\":0,\"greeting\":\"hello\"}"`, which the
  handler in `main.tf` returned;
- `event_source_mapping_state = "Enabled"`: the queue now feeds the function;
- `queue_url`, `dynamodb_table_arn`, `ecs_service` and `alarm_arn`.

### Step 7: Confirm the stack is stable

```sh
terraform plan -detailed-exitcode
```

It reports `No changes.` and exits 0: what the simulator returns matches what
Terraform wrote.

### Step 8: Destroy the AWS stack

```sh
terraform destroy -auto-approve
```

Then stop `simulator-aws` with Ctrl-C.

## Part 2: Google Cloud

### Step 9: Start the Google Cloud simulator

In a terminal of its own:

```sh
simulator-gcp
```

It listens on port 4567.

### Step 10: Get an access token

The Google provider presents an OAuth 2.0 access token. The simulator's token
endpoint issues one:

```sh
export TF_VAR_access_token="$(curl -s -X POST \
  -d grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer \
  http://localhost:4567/token | jq -r .access_token)"
```

On an arm64 machine, choose the arm64 Debian image too:

```sh
export TF_VAR_debian_image_family=debian-12-arm64
```

### Step 11: Apply the Google Cloud stack

```sh
cd "$TUTORIAL_ROOT/simulator-gcp/terraform-tests/fixtures/tutorial"
terraform init
terraform apply -auto-approve
```

The configuration sets each service's `*_custom_endpoint` to the simulator.
Apply does the work Google Cloud would do:
- it boots the Compute Engine instance;
- it starts the Cloud Run revision;
- it builds the Cloud Run function from the zip in the bucket.

### Step 12: Read the results

```sh
terraform output
```

You should see:

- `instance_status = "RUNNING"`, and an `instance_private_ip` inside
  `10.44.0.0/24`;
- a `nat_ip`, Cloud NAT's reserved address;
- `storage_object_content = "Hello from Cloud Storage"`;
- `function_state = "ACTIVE"`, and the function's `cloudfunctions.net` URL;
- `run_service_uri`, `firestore_document = "tutorial-orders/order-1"`,
  `subscription` and `log_metric`.

### Step 13: Confirm the stack is stable

```sh
terraform plan -detailed-exitcode
```

### Step 14: Destroy the Google Cloud stack

```sh
terraform destroy -auto-approve
```

Then stop `simulator-gcp` with Ctrl-C.

## Part 3: Azure

The azurerm provider reaches Azure only over HTTPS, and reaches each storage
account, Service Bus namespace and app's deployment site at a host name of its
own. A Caddy gateway terminates TLS for the simulator under
`*.azure.sockerless.localhost`.

### Step 15: Make the gateway's names resolve

On Linux with systemd-resolved, every `*.localhost` name already resolves to
127.0.0.1. Elsewhere, add the names this stack uses to `/etc/hosts`:

```text
127.0.0.1 azure.sockerless.localhost
127.0.0.1 tutorialassets.blob.azure.sockerless.localhost
127.0.0.1 tutorial-worker.scm.azure.sockerless.localhost
```

### Step 16: Start the Azure simulator

The simulator advertises its data planes at the gateway's names, so tell it
where the gateway listens. In a terminal of its own:

```sh
export SIM_AZURE_ARM_EXTERNAL_DATA_PLANE_URLS_JSON='{
  "storage": {
    "blob": "https://{account}.blob.azure.sockerless.localhost:8443/",
    "file": "https://{account}.file.azure.sockerless.localhost:8443/",
    "queue": "https://{account}.queue.azure.sockerless.localhost:8443/",
    "table": "https://{account}.table.azure.sockerless.localhost:8443/",
    "web": "https://{account}.web.azure.sockerless.localhost:8443/",
    "dfs": "https://{account}.dfs.azure.sockerless.localhost:8443/"
  },
  "keyVault": "https://{vault}.vault.azure.sockerless.localhost:8443/",
  "serviceBus": "https://{namespace}.servicebus.azure.sockerless.localhost:8443/",
  "eventGrid": "https://{topic}.eventgrid.azure.sockerless.localhost:8443/api/events",
  "acr": "https://{name}.azurecr.azure.sockerless.localhost:8443/",
  "appServiceScm": "https://{name}.scm.azure.sockerless.localhost:8443/"
}'
simulator-azure
```

It listens on port 4568.

### Step 17: Start the HTTPS gateway

In another terminal, from the repository root:

```sh
cd "$TUTORIAL_ROOT"
SOCKERLESS_AZURE_SIM_PORT=4568 caddy run --config make/https-gateway/Caddyfile --adapter caddyfile
```

Caddy issues the gateway's certificates from a local root it creates. Trust
that root for Terraform, in your first terminal:

```sh
export SSL_CERT_FILE="$HOME/.local/share/caddy/pki/authorities/local/root.crt"
```

On macOS Caddy keeps its data under
`~/Library/Application Support/Caddy`, so the root is at
`~/Library/Application Support/Caddy/pki/authorities/local/root.crt`.

### Step 18: Apply the Azure stack

```sh
cd "$TUTORIAL_ROOT/simulator-azure/terraform-tests/tutorial"
terraform init
terraform apply -auto-approve
```

The configuration sets the provider's `metadata_host` to the gateway. azurerm
reads the simulator's cloud metadata and token endpoints from there, as it
reads them from `management.azure.com` on Azure. Apply does the work Azure
would do:
- it boots the virtual machine;
- it starts the Container App's revision;
- it deploys the function app's zip package to its deployment site.

### Step 19: Read the results and call the function

```sh
terraform output
```

You should see:
- a `vm_private_ip` inside `10.97.1.0/24`, and the NAT gateway's
  `nat_gateway_ip_prefix`;
- `container_app_fqdn`, `blob_url`, `cosmos_container_id`,
  `servicebus_queue_id` and `log_analytics_workspace_id`;
- `function_hostname = "tutorial-worker.azurewebsites.net"`.

Call the function with its host key. The simulator serves every app's
`azurewebsites.net` host name on its own port, chosen by the `Host` header:

```sh
curl -H "Host: $(terraform output -raw function_hostname)" \
  "http://localhost:4568/api/hello?code=$(terraform output -raw function_key)"
```

It answers `hello from tutorial-worker`.

### Step 20: Confirm the stack is stable

```sh
terraform plan -detailed-exitcode
```

### Step 21: Destroy the Azure stack

```sh
terraform destroy -auto-approve
```

Then stop Caddy and `simulator-azure` with Ctrl-C.

## What the stacks leave out

- **Cloud Monitoring.** The Google Cloud simulator serves no Cloud Monitoring,
  so the Google Cloud stack counts errors with a log-based metric and sets no
  alert policy.
- **Azure Monitor metric alerts.** The Azure simulator serves no metric
  alerts, so the Azure stack's monitoring is Log Analytics and Application
  Insights.
- **A Firestore database resource.** The stack writes into the `(default)`
  database the simulator's project already has. On Google Cloud you create it
  first, with `google_firestore_database`.

To run the same configurations against the real clouds, drop the endpoint
settings, the fixed credentials and the token variable, and sign in the way
each provider documents.
