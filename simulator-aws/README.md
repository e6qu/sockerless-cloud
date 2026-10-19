# simulator-aws

Local reimplementation of a slice of AWS. Amazon Elastic Container Service (ECS) tasks and AWS Lambda functions run as real containers with real exit semantics, Amazon Elastic Container Registry (ECR) stores real images, and every other service answers in the wire shapes the AWS SDKs, the AWS CLI and the Terraform `aws` provider expect, behind real SigV4 verification and IAM authorization.

## Reference adaptor

The simulator exposes one HTTP endpoint (default `:4566`) that fronts all AWS services. Three external tools exercise that endpoint at AWS-API fidelity:

| Adaptor | Min version | What it proves |
|---|---|---|
| [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) (`github.com/aws/aws-sdk-go-v2/service/*`) | the release `sdk-tests/go.mod` pins | Wire-level SDK compatibility — request/response shapes, error envelopes, pagination, optimistic concurrency tokens. |
| [`aws` CLI](https://docs.aws.amazon.com/cli/latest/reference/) | the current v2 release (`cli-tests` installs it when absent) | Endpoint-override fidelity (`--endpoint-url`). CLI uses the same SDK but exercises a different argument-marshaling path. Some endpoints differ (e.g. Route 53 `/rrset/` with trailing slash). |
| [Terraform `aws` provider](https://registry.terraform.io/providers/hashicorp/aws/latest/docs) | the version `terraform-tests` pins | Full plan → apply → destroy round-trip across resource types such as (`aws_ecs_*`, `aws_lambda_*`, `aws_cloudfront_*`, `aws_route53_*`, `aws_wafv2_*`, `aws_amplify_*`, `aws_acm_*`, `aws_iam_*`, `aws_ecr_*`, `aws_s3_*`). Stresses cross-resource references, Lambda invocation through the Runtime API, and stateful drift detection. |

Anything any of these three tools does against the real AWS endpoint, it must do against this simulator. Gaps from that contract are real bugs (see [BUGS.md](../BUGS.md)).

## Validation

| Test path | What runs |
|---|---|
| `sdk-tests/` — one file per service family (`ecs_test.go`, `ecr_test.go`, `cloudfront_test.go`, `route53_test.go`, `wafv2_test.go`, `amplify_test.go`, …) | Real `aws-sdk-go-v2` clients against the simulator. Per-operation assertions on response shape and error codes. |
| `cli-tests/` | The real `aws` CLI invoked via `os/exec`, parsing its JSON output. |
| `terraform-tests/` — `TestStackProductionShape` and per-resource packages | The real Terraform `aws` provider. Provisions CloudFront, ACM, WAFv2, Route 53 ALIAS, Amplify, IAM service-linked roles and OIDC providers, ECS, ECR, Cloud Map and Lambda resources together, asserts cross-resource outputs and Lambda Runtime API invocation output, then runs `terraform destroy`. |
| `make simulator-aws/test` | The module's unit tests, per [`docs/MAKEFILE_STANDARD.md`](../docs/MAKEFILE_STANDARD.md). |

CI runs all four on every pull request (`.github/workflows/ci.yml`). `test`/`test` is the access key of the administrator the simulator starts with, as an AWS account starts with a root credential; STS reports account `123456789012` unless `SOCKERLESS_AWS_ACCOUNT_ID` names another.

## Wiring the adaptor

```bash
# 1. Build + start the sim (default :4566).
cd simulator-aws
go build -tags noui -o simulator-aws .
SIM_LISTEN_ADDR=:4566 ./simulator-aws
```

```bash
# 2. Point any AWS client at it.
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1

aws ecs list-clusters
aws cloudfront list-distributions
aws iam create-service-linked-role --aws-service-name cloudfront.amazonaws.com
```

| Variable | Default | What it does |
|---|---|---|
| `SIM_LISTEN_ADDR` | `:4566` | Listen address (`host:port`). |
| `SIM_AWS_PORT` | unset | Port to listen on, ahead of `SIM_LISTEN_ADDR`. |
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | unset | Enable HTTPS with the given cert/key. |
| `SIM_RUNTIME` | `docker` | Initializes Docker/Podman for workload execution. Set `process` only for explicit API-only runs that do not invoke ECS/Lambda workload execution. |
| `SIM_PERSIST` | `false` | `true` keeps state across restarts and leaves running workloads for the next process to adopt. |
| `SIM_PLACEMENT` | unset | The per-service placement table, `service[:qualifier]=container|microvm` comma-separated (`ec2=microvm,ecs:FARGATE=microvm,lambda=container,batch=container`). Every service unnamed runs on containers. Naming a microVM service starts a node agent on the platform network, which needs a Linux engine with `/dev/kvm`. |
| `SIM_DATA_DIR` | unset | Persistence root: the SQLite control-plane store, plus the default location of every bulk-data root below (`<SIM_DATA_DIR>/efs`, `/ebs`, `/amplify-cache`), so file contents survive restarts alongside the metadata that describes them. |
| `SIM_EBS_DATA_DIR` | `<SIM_DATA_DIR>/ebs`, else `$TMPDIR/sockerless-sim-ebs` | Explicit override for the Amazon EC2 EBS snapshot root. Amazon ECS managed EBS volumes live in engine volumes (see below). |
| `SIM_EFS_DATA_DIR` | `<SIM_DATA_DIR>/efs`, else `$TMPDIR/sockerless-sim-efs` | Explicit override for the EFS file-system content root. |
| `AWS_ENDPOINT_URL` | (client-side) | The AWS SDKs and AWS CLI's standard global endpoint setting. It routes every supported service to the simulator. |
| `AWS_ENDPOINT_URL_<SERVICE>` | (client-side) | The AWS SDKs' standard per-service setting (for example `AWS_ENDPOINT_URL_SQS`). It overrides the global coordinate for that service. |
| `AWS_DEFAULT_REGION` | `us-east-1` | The sim accepts any region; some validation (CloudFront → ACM us-east-1 pin) is region-aware. |

The ECS and Lambda execution paths require Docker or Podman. For
control-plane or data-plane API checks that do not start workloads,
`SIM_RUNTIME=process` starts the AWS simulator without initializing Docker/Podman.
The `/health` response reports `runtime` and
`capabilities.workloadExecution`; clients must require that capability before
submitting work that needs a running container.

The same endpoint convention applies inside workloads. Pass
`AWS_ENDPOINT_URL` (or `AWS_ENDPOINT_URL_<SERVICE>`) and ordinary AWS
credentials through the real workload configuration surface—Amazon ECS
container overrides, AWS CodeBuild environment overrides, or AWS Lambda
function environment variables. The simulator does not inject or broker a
private endpoint variable. The
official-client suite proves this by
having an AWS Step Functions-launched AWS CodeBuild process invoke the vendor
AWS CLI against Amazon SQS and by having explicitly deployed AWS Lambda code
invoke the bundled AWS SDK against Amazon SQS.
It also launches the official HashiCorp Terraform image as an Amazon ECS task
from AWS Step Functions; Terraform uses only the standard `AWS_ENDPOINT_URL`
coordinate, applies an Amazon SQS resource to the simulator, and an independent
AWS SDK client reads the resulting queue and tags.

AWS Lambda follows the real deployment contract: `CreateFunction` supplies a
ZIP archive or image plus handler, role, and environment configuration.
Repository files and database settings are not discovered from the simulator
host. An end-to-end SDK test deploys code and environment explicitly, invokes
the managed runtime, and observes its authenticated downstream Amazon SQS
write.

**Amazon ECS managed EBS volumes** are engine volumes (`sockerless-ebs-<id>`): each task's volume is its own, snapshots copy it, and nothing is shared by path between the host and the simulator's container. The requested size and filesystem type are validated and recorded, not realized on the container backend (`BUGS.md`).

**VPC and subnet creation** (`CreateVpc`, `CreateSubnet`) records API state. The simulator creates the VPC's engine network when the first workload attaches to a subnet of it, and removes it with the VPC.

### Networks, the Route 53 Resolver and instance metadata

Every workload runs on an engine network, never on the engine's default
bridge: a VPC's network (`sockerless-sim-vpc-<vpc-id>`, over the VPC's own
CIDR) for an Amazon EC2 instance, an `awsvpc` task or a VPC-attached AWS
Lambda function, and the simulator's platform network
(`sockerless-sim-aws-platform-<scope>`) for everything else — `bridge`-mode
tasks, Lambda functions outside a VPC, AWS Batch jobs, AWS CodeBuild
environments, AWS Glue jobs, Amplify builds and compute.

Each network carries one sidecar container at the address the cloud reserves:
the VPC's base plus two, where the Amazon Route 53 Resolver answers. The
sidecar serves DNS and HTTP there. Every workload's resolver is the sidecar,
so a name resolves through Route 53: the public hosted zones, the private
hosted zones associated with the workload's VPC (AWS Cloud Map namespaces
among them), the load balancers' names, and everything else through the
simulator's upstream resolvers. The sidecar forwards the instance metadata
service, the Amazon ECS task metadata endpoint and the container credentials
endpoint to the simulator with the workload's address, so each workload reads
its own identity; the SDKs find them through
`AWS_EC2_METADATA_SERVICE_ENDPOINT`, `ECS_CONTAINER_METADATA_URI_V4` and
`AWS_CONTAINER_CREDENTIALS_FULL_URI`, which name the sidecar. The link-local
addresses themselves (`169.254.169.254`, `169.254.170.2`) are not routed
(`BUGS.md`).

Only an EC2 instance reaches the instance metadata service, and it reads its
own record there: identity, placement, key pair, tags when its metadata
options allow them, and sessions of its instance profile's role. Every other
workload holds its role the way its service hands it over: an AWS Lambda
function and Amplify compute take their execution or compute role's session
from the environment, and an Amazon ECS task, an AWS CodeBuild build, an AWS
Batch job and an AWS Glue job run read theirs from the container credentials
endpoint. Each session is the role's, named for the workload, and IAM
evaluates the role's policies against what the workload calls.

Security groups are enforced on the flows the simulator mediates: a load
balancer reaches an instance or IP target only through what the target's
security groups admit from the load balancer (its security groups and
subnets, or the client's address where a Network Load Balancer preserves
it), and a blocked target's health checks time out. Traffic between two
workloads on one network is not filtered (`BUGS.md`).

### Amazon EC2 instances

An instance is a machine on the placement backend (a container by default):
the AMI's image, its private address on the VPC's network, the hostname
`ip-10-0-0-7`, the VPC's resolver, and the vCPUs and memory of its instance
type. `DescribeImages` lists the AMIs the simulator publishes — Amazon Linux
2023, Ubuntu 24.04 LTS and Debian 12 for `x86_64` and `arm64`, with the
names, owners and descriptions AWS reports — beside the account's own.
`RunInstances` refuses an AMI that does not exist and an instance type of
another architecture.

- **User data** that starts with `#!` runs once as root, with its output on
  the console (`GetConsoleOutput`) and in `/var/log/cloud-init-output.log`;
  `#cloud-config` user data is not interpreted (`BUGS.md`).
- **Lifecycle**: `pending` while the machine boots and `running` once its
  init is ready; `StopInstances` keeps the root disk (`stopping`, `stopped`)
  and `StartInstances` boots it again; `TerminateInstances` discards it
  (`shutting-down`, `terminated`); `RebootInstances` restarts the init in
  place; an init that exits on its own stops the instance.
- **AWS Systems Manager Run Command** (`SendCommand` with
  `AWS-RunShellScript` or a command document with `aws:runShellScript`
  steps) runs on the instance's machine; `GetCommandInvocation` carries the
  output. An instance whose machine is not up is not a managed node.
- **Images**: `CreateImage` captures the instance's root disk into the new
  AMI; `RegisterImage` takes an OCI image reference in `ImageLocation`, and
  `ImportImage` one in the disk container's `Url`, which the instances of
  those AMIs boot. An AMI registered from a block device mapping alone has
  no root filesystem to boot (`BUGS.md`).

### ECS task networking

The task definition's `networkMode` decides the fabric every container in the
task lands on, exactly as it does on real Amazon Elastic Container Service (ECS):

| `networkMode` | What the task gets | How the simulator realizes it |
|---|---|---|
| `awsvpc` | Its own elastic network interface in a subnet of the VPC. `networkConfiguration` is **mandatory**; `RunTask` rejects the request without it. | A user-defined engine network per VPC (`sockerless-sim-vpc-<vpc-id>`) over the VPC's own CIDR, created when the first task attaches, with the VPC's resolver sidecar; the task's first container takes the ENI address from the engine and the task's other containers share its network namespace. Same-VPC tasks reach each other on their ENI addresses over the shared bridge. The engine admits one network per address range, so two live VPCs sharing a CIDR do not coexist on one engine (`BUGS.md`). |
| `bridge` (the default when `networkMode` is unset) | An address on the container instance's network. No ENI. `networkConfiguration` is rejected. | The simulator's platform network (`sockerless-sim-aws-platform-<scope>`) with its resolver sidecar. |
| `host` | The container instance's own network stack. No ENI. | Docker `host` network mode. |
| `none` | No connectivity. No ENI. | Docker `none` network mode. |

An `awsvpc` task is therefore never placed on the default bridge, and only an
`awsvpc` task carries an `ElasticNetworkInterface` attachment and per-container
`networkInterfaces`.

### Guest-kernel requirements for `SIM_RUNTIME=docker`

Workload execution is the container runtime's job, so the kernel the **runtime**
runs on must be able to program everything that runtime needs. Docker 28 and
later adds a `raw`-table `PREROUTING … -j DROP` rule (direct access filtering)
for every endpoint it creates on a bridge-driver network, so a kernel built
without `CONFIG_IP_NF_RAW` cannot start those containers at all:

```
failed to create endpoint … on network bridge:
Unable to enable DIRECT ACCESS FILTERING - DROP rule:
(iptables failed: iptables --wait -t raw -A PREROUTING …:
can't initialize iptables table `raw': Table does not exist)
```

This covers the default `bridge` network **and** every user-defined bridge
network — Docker programs the rule per endpoint, so the per-VPC networks
`awsvpc` tasks use need the `raw` table too. Only `networkMode: host` and
`networkMode: none` create no bridge endpoint at all.

The simulator does not work around this — it reports the failure with the
missing module named, and the task stops with that reason. Fix it on the host:

- Run the container runtime on a kernel with the full netfilter set
  (`iptable_raw` / `CONFIG_IP_NF_RAW`, plus `nf_tables` for the nftables
  backend).
- Or start the Docker daemon with `DOCKER_INSECURE_NO_IPTABLES_RAW=1`, Docker's
  own opt-out from the `raw` rules. That drops the direct-access hardening
  host-wide; it is the daemon operator's decision, never the simulator's.

### Terraform

See [`docs/terraform.md`](docs/terraform.md) for the full provider configuration:

```hcl
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  endpoints {
    ecs              = "http://localhost:4566"
    ecr              = "http://localhost:4566"
    cloudfront       = "http://localhost:4566"
    acm              = "http://localhost:4566"
    route53          = "http://localhost:4566"
    wafv2            = "http://localhost:4566"
    amplify          = "http://localhost:4566"
    iam              = "http://localhost:4566"
    # …any service you exercise.
  }
}
```

## Services

### AWS-JSON 1.1 (POST / + X-Amz-Target)

| Service | Target Prefix | Source file |
|---|---|---|
| **ECS** | `AmazonEC2ContainerServiceV20141113` | `ecs.go` |
| **ECR** | `AmazonEC2ContainerRegistry_V20150921` | `ecr.go` |
| **CloudWatch Logs** | `Logs_20140328` | `cloudwatch.go` |
| **Cloud Map** | `Route53AutoNaming_v20170314` | `cloudmap.go` |
| **ACM** | `CertificateManager` | `acm.go` |
| **WAFv2** | `AWSWAF_20190729` | `wafv2.go` |
| **KMS** | `TrentService` | `kms.go` |
| **Secrets Manager** | `secretsmanager` | `secretsmanager.go` |
| **DynamoDB** | `DynamoDB_20120810` | `dynamodb.go` |
| **SSM** | `AmazonSSM` | `ssm_*.go` |

### AWS Query Protocol (POST / + Action=)

| Service | Source file |
|---|---|
| **EC2** | `ec2.go` |
| **IAM** (roles, policies, instance profiles, service-linked roles, OIDC providers) | `iam.go` + `iam_slr_oidc.go` |
| **STS** | `sts.go` |

### REST APIs (path routing)

| Service | Base Path | Source file |
|---|---|---|
| **EFS** | `/2015-02-01/…` | `efs.go` |
| **Lambda** | `/2015-03-31/functions/…` | `lambda.go` |
| **S3** | `/{bucket}/{key}` | `s3.go` |
| **CloudFront** | `/2020-05-31/…` (REST + XML) | `cloudfront.go` + `cloudfront_policies.go` + `cloudfront_functions.go` + `cloudfront_keys.go` |
| **Route 53** | `/2013-04-01/…` (REST + XML) | `route53.go` |
| **Amazon Amplify** | `/apps/…` (REST + JSON, versionless); encrypted authenticated Git repository connections, real backend/frontend/test and monorepo builds with declared caches in a managed multi-language image from mirror.gcr.io, a host-addressed hosting data plane (`{branch}.{appId}.amplifyapp.com`, per-app `{hash}.cloudfront.net`, verified custom domains), server-side rendering per the deployment-manifest specification, and Route 53-backed domain verification | `amplify.go` + `amplify_domains.go` + `amplify_build.go` + `amplify_dataplane.go` + `amplify_compute.go` |

Amazon RDS DB instances backed by PostgreSQL, MySQL, or MariaDB expose their
native database wire protocol at the `Endpoint` returned by
`CreateDBInstance`. The engine starts on the first data-plane connection,
retains its files in an instance-owned volume, and accepts the configured
master password, the passwords of the users the engine holds, and a
TLS-protected, 15-minute SigV4 IAM database authentication token for a
PostgreSQL role granted `rds_iam` or a MySQL or MariaDB user identified with
`AWSAuthenticationPlugin`, a server plugin the simulator ships for both
engines. `ModifyDBInstance` changes IAM authentication and rotates the actual
database account while running or across a stopped/start lifecycle without
replacing its data volume. Official AWS token generation plus stock PostgreSQL
and MySQL drivers exercise schema, insert, query, denial, TLS enforcement,
password rotation, and persistence operations against all three real engines.

AWS Step Functions Task states support optimized Amazon ECS `RunTask`
request/response, `.sync`, and callback-token integrations, plus the optimized
AWS CodeBuild build and build-batch operations. Synchronous workflows poll the
actual service resources, propagate terminal failures, and stop work they
started when the caller aborts the execution. CodeBuild clones private Git sources
with encrypted imported or AWS Secrets Manager credentials and executes the
checked-in build specification inside the project's exact configured image;
stopping a build or build batch cancels that container.

Amazon Amplify release and retry jobs expose the hosted build ZIP through the
`BUILD` step's `artifactsUrl`; `ListArtifacts` and `GetArtifactUrl` expose only
the actual end-to-end test files declared by the build specification. Test
artifact bundles and configuration URLs live on that same `BUILD` step and are
deleted with the job. AWS WAF associations update the Amplify app's
`wafConfiguration`, protect the hosting data plane with the WebACL default
action and IP-set rules, and feed actual matching requests into
`GetSampledRequests`.

Full per-verb wire shape: see [API_SPEC.md](API_SPEC.md).

## Sample — end-to-end production-shape stack

The `terraform-tests` `TestStackProductionShape` test provisions a CloudFront-fronted application with AWS WAF, ACM, Route 53, Amplify and an IAM service-linked role in a single `terraform apply` (simulator port shown as `:46241`):

```bash
# Boot the sim
$ SIM_LISTEN_ADDR=:46241 ./simulator-aws &

# Configure AWS SDK + Terraform
$ export AWS_ENDPOINT_URL=http://127.0.0.1:46241
$ export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1

# Apply the full stack
$ terraform apply -auto-approve
aws_cloudfront_distribution.tf_dist: Creation complete after 0.2s
  [id=E8E549340297351, domain_name=E8E549340297351.cloudfront.net]
aws_wafv2_web_acl_association.tf_assoc: Creation complete after 0.03s
aws_route53_record.tf_alias: Creation complete after 0.1s
aws_acm_certificate.tf_cert: Creation complete after 0.03s
  [arn=arn:aws:acm:us-east-1:123456789012:certificate/...]
aws_iam_service_linked_role.tf_slr_cloudfront: Creation complete after 0.02s
aws_iam_openid_connect_provider.tf_oidc: Creation complete after 0.02s
aws_amplify_app.tf_amplify: Creation complete after 0.01s

# Verify cross-resource references
$ terraform output -json | jq '. | with_entries(.value = .value.value)'
{
  "cloudfront_arn":             "arn:aws:cloudfront::123456789012:distribution/E8E549340297351",
  "cloudfront_domain_name":     "E8E549340297351.cloudfront.net",
  "wafv2_assoc_resource_arn":   "arn:aws:cloudfront::123456789012:distribution/E8E549340297351",
  "route53_alias_target_name":  "E8E549340297351.cloudfront.net",
  "acm_certificate_arn":        "arn:aws:acm:us-east-1:123456789012:certificate/...",
  "iam_slr_arn":                "arn:aws:iam::123456789012:role/aws-service-role/cloudfront.amazonaws.com/AWSServiceRoleForCloudFrontLogger_tftest"
}
```

The Go test asserts `wafv2_assoc_resource_arn == cloudfront_arn`, `route53_alias_target_name == cloudfront_domain_name`, and `acm_certificate_arn` starts with `arn:aws:acm:us-east-1:` — the three load-bearing cross-resource invariants of a CloudFront-fronted production stack.

```bash
$ terraform destroy -auto-approve
# Destroys all 30+ resources in dependency order.
```

## Building

```bash
cd simulator-aws
go build -tags noui -o simulator-aws .
```

## Testing

```bash
# SDK tests (AWS SDK v2 against the running sim — sim is built + booted per TestMain)
cd sdk-tests && go test -v ./...

# CLI tests (aws CLI shell-outs)
cd cli-tests && go test -v ./...

# Terraform tests (real terraform apply → assert outputs → destroy)
cd terraform-tests && go test -v ./...
```

Each suite's `TestMain` builds the simulator, starts it on a free port, waits for its `Listening on` banner (`testutil/simready`), runs the tests and stops it. The suites need a container engine; the CLI suite installs its CLI when absent.

## Known issues

Open simulator bugs live in [BUGS.md](../BUGS.md).

## What's out of scope

- **Edge propagation** — a CloudFront distribution or invalidation takes effect when its call returns, so it reports `Deployed` or `Completed` at once; real CloudFront spends minutes `InProgress`.
- **Multi-region routing** — sim is single-region (defaults to `us-east-1`). Cross-region replication / failover is not modelled.
- **Cost / billing surfaces** — `cur`, `pricing` and `cost-explorer` are absent; AWS Budgets is served.
- **Outbound delivery to carriers and push services** — Amazon SNS SMS and mobile push need a telecommunications carrier or Apple's and Google's hosts, which no AWS API provisions; those publishes fail naming the missing dependency.

A stopped Amazon ECS task's containers get SIGTERM and then SIGKILL after each container definition's `stopTimeout` (30 seconds when unset). Route 53 serves the zones it holds over UDP and TCP on `SIM_DNS_PORT` (a kernel-chosen port when unset; workload namespaces are redirected to it), a private hosted zone only to the VPCs it is associated with, and recurses for other names through `SIM_DNS_UPSTREAM` or the host's resolvers, a DNS-validated ACM certificate is issued once its validation CNAME is present in Route 53, WAFv2 web ACLs are evaluated against the traffic of the resources they are associated with, and every request is SigV4-verified against the principal's stored secret.

See also: [API_SPEC.md](API_SPEC.md).
