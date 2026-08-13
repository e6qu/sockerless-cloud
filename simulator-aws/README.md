# simulator-aws

Local reimplementation of the AWS slice that sockerless touches. Not a mock — workloads execute through real Docker, Amazon Elastic Container Service (ECS) / AWS Lambda tasks run with real exit semantics, Amazon Elastic Container Registry (ECR) stores real image manifests, and the broader CDN / DNS / cert / AWS WAF / AWS Amplify / AWS Identity and Access Management (IAM) surfaces respond on the real wire shapes that the AWS SDK v2 + AWS CLI + Terraform `aws` provider expect.

## Reference adaptor

The simulator exposes one HTTP endpoint (default `:4566`) that fronts all AWS services. Three external tools exercise that endpoint at AWS-API fidelity:

| Adaptor | Min version | What it proves |
|---|---|---|
| [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) (`github.com/aws/aws-sdk-go-v2/service/*`) | v1.30 | Wire-level SDK compatibility — request/response shapes, error envelopes, pagination, optimistic concurrency tokens. Covers 30+ services. |
| [`aws` CLI](https://docs.aws.amazon.com/cli/latest/reference/) | 2.17+ | Endpoint-override fidelity (`--endpoint-url`). CLI uses the same SDK but exercises a different argument-marshaling path. Some endpoints differ (e.g. Route 53 `/rrset/` with trailing slash). |
| [Terraform `aws` provider](https://registry.terraform.io/providers/hashicorp/aws/latest/docs) | v6.50.0 | Full plan → apply → destroy round-trip across 60+ resource types (`aws_ecs_*`, `aws_lambda_*`, `aws_cloudfront_*`, `aws_route53_*`, `aws_wafv2_*`, `aws_amplify_*`, `aws_acm_*`, `aws_iam_*`, `aws_ecr_*`, `aws_s3_*`). Stresses cross-resource references, Lambda invocation through the Runtime API, and stateful drift detection. |

Anything any of these three tools does against the real AWS endpoint, it must do against this simulator. Gaps from that contract are real bugs (see [BUGS.md](../BUGS.md)).

## Validation

| Test path | What runs | Last green |
|---|---|---|
| `sdk-tests/` — 30 packages (`ecs_test.go`, `ecr_test.go`, `cloudfront_test.go`, `route53_test.go`, `wafv2_test.go`, `amplify_test.go`, `acm_test.go`, `iam_slr_oidc_test.go`, …) | Real `aws-sdk-go-v2` clients against the sim. Per-op assertions on response shape + error codes. | 2026-05-15 (PR #159 P159.10) |
| `cli-tests/` — 30 packages (`ecs_test.go`, `iam_slr_oidc_test.go`, …) | Real `aws` CLI invoked via `os/exec`, parses CLI JSON output. | 2026-05-15 |
| `terraform-tests/` — `TestStackProductionShape` | Real Terraform `aws` v6.50.0 against the sim. Provisions CloudFront + ACM + WAFv2 + Route 53 ALIAS + Amplify + IAM SLR/OIDC + ECS + ECR + Cloud Map + Lambda resources together, asserts cross-resource outputs and Lambda Runtime API invocation output, then `terraform destroy`. | 2026-07-29 |
| `make simulator-aws/test` | Leaf-Makefile unit + integration suite per `docs/MAKEFILE_STANDARD.md`. | 2026-05-15 |

The SDK + Terraform tests are the load-bearing validation. CI runs all four on every PR (`.github/workflows/ci.yml`).

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
| `SIM_TLS_CERT`, `SIM_TLS_KEY` | unset | Enable HTTPS with the given cert/key. |
| `SIM_RUNTIME` | `docker` | Initializes Docker/Podman for workload execution. Set `process` only for explicit API-only runs that do not invoke ECS/Lambda workload execution. |
| `SIM_DATA_DIR` | unset | Persistence root: the SQLite control-plane store, plus the default location of every bulk-data root below (`<SIM_DATA_DIR>/efs`, `/ebs`, `/amplify-cache`), so file contents survive restarts alongside the metadata that describes them. |
| `SIM_EBS_DATA_DIR` | `<SIM_DATA_DIR>/ebs`, else `$TMPDIR/sockerless-sim-ebs` | Explicit override for the EC2/Firecracker EBS block-image root (volume backing files and snapshots). **Not used for ECS managed EBS volumes** — those use Docker named volumes (`sockerless-ebs-*`) so they are topology-independent. |
| `SIM_EFS_DATA_DIR` | `<SIM_DATA_DIR>/efs`, else `$TMPDIR/sockerless-sim-efs` | Explicit override for the EFS file-system content root. |
| `AWS_ENDPOINT_URL` | (client-side) | The AWS SDKs and AWS CLI's standard global endpoint setting. It routes every supported service to the simulator. |
| `AWS_ENDPOINT_URL_<SERVICE>` | (client-side) | The AWS SDKs' standard per-service setting (for example `AWS_ENDPOINT_URL_SQS`). It overrides the global coordinate for that service. |
| `AWS_DEFAULT_REGION` | `us-east-1` | The sim accepts any region; some validation (CloudFront → ACM us-east-1 pin) is region-aware. |

Docker or Podman is required for ECS and Lambda execution paths. For
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
private endpoint variable. In the Linux real-VPC tier, an explicitly supplied
outer-host simulator-listener authority is mapped onto the existing managed
task-local route because the isolated namespace intentionally has no route to
Docker's host gateway; other host authorities remain unreachable. The
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

**ECS managed EBS volumes** use Docker named volumes (`sockerless-ebs-<id>`) rather than bind-mounts on the sim process's filesystem. This means the sim can run in a container (with the Docker socket mounted) and task containers will see the correct volume data — no path-sharing between host and sim container is required.

**VPC and Subnet creation** (`CreateVpc`, `CreateSubnet`) always succeeds at the control-plane level (API state is stored). Real Linux network-namespace fabric is set up lazily when a data-plane resource attaches to the VPC/subnet and host networking capabilities (`ip`, `nft`, `sysctl`) are present. Without those capabilities the API calls still succeed and `awsvpc` tasks fall to the per-VPC Docker-network fabric described below.

### ECS task networking

The task definition's `networkMode` decides the fabric every container in the
task lands on, exactly as it does on real Amazon Elastic Container Service (ECS):

| `networkMode` | What the task gets | How the simulator realizes it |
|---|---|---|
| `awsvpc` | Its own elastic network interface in a subnet of the VPC. `networkConfiguration` is **required**; `RunTask` rejects the request without it. | On Linux with `CAP_NET_ADMIN` + `nsenter`, a pause container holds the task's network namespace and the ENI veth is plumbed into it from the VPC's namespace. Everywhere else, a per-VPC user-defined Docker network (`sockerless-sim-vpc-<vpc-id>`) whose IPAM subnet is a /24 slice of the reserved host-side pool `10.213.0.0/16` — never the VPC CIDR, so two live VPCs sharing a CIDR coexist — with the task's ENI address plumbed onto the container's interface as a secondary by an ephemeral `CAP_NET_ADMIN` setup container. Same-VPC tasks reach each other on their ENI addresses over the shared bridge; same-CIDR VPCs sit on different bridges and stay isolated. |
| `bridge` (the default when `networkMode` is unset) | An address on the container instance's default Docker bridge. No ENI. `networkConfiguration` is rejected. | The container runtime's default `bridge` network. |
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
network — Docker programs the rule per endpoint, so the per-VPC networks the
`awsvpc` Docker-network tier uses need the `raw` table too, and so does the
`awsvpc` namespace tier's pause container (it is created on the default network
before being moved into the task's namespace). Only `networkMode: host` and
`networkMode: none` create no bridge endpoint at all.

The simulator does not work around this — it reports the failure with the
missing module named, and the task stops with that reason. Fix it on the host:

- Run the container runtime on a kernel with the full netfilter set
  (`iptable_raw` / `CONFIG_IP_NF_RAW`, plus `nf_tables` for the nftables
  backend). The stock Firecracker CI guest kernel (`vmlinux-6.1.128`) omits
  both, so build a container-capable guest kernel before running the simulator
  inside such a microVM.
- Or start the Docker daemon with `DOCKER_INSECURE_NO_IPTABLES_RAW=1`, Docker's
  own opt-out from the `raw` rules. That drops the direct-access hardening
  host-wide; it is the daemon operator's decision, never the simulator's.

For Terraform:

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
| **SSM** | `AmazonSSM` | `ssm.go` |

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
| **Amazon Amplify** | `/apps/…` (REST + JSON, versionless); encrypted authenticated Git repository connections, real backend/frontend/test and monorepo builds with declared caches in a managed multi-language image from Amazon ECR Public, a host-addressed hosting data plane (`{branch}.{appId}.amplifyapp.com`, per-app `{hash}.cloudfront.net`, verified custom domains), server-side rendering per the deployment-manifest specification, and Route 53-backed domain verification | `amplify.go` + `amplify_domains.go` + `amplify_build.go` + `amplify_dataplane.go` + `amplify_compute.go` |

Amazon RDS DB instances backed by PostgreSQL, MySQL, or MariaDB expose their
native database wire protocol at the `Endpoint` returned by
`CreateDBInstance`. The engine starts on the first data-plane connection,
retains its files in an instance-owned volume, and accepts the configured
master password or a TLS-protected, 15-minute SigV4 IAM database authentication
token. `ModifyDBInstance` changes IAM authentication and rotates the actual
database account while running or across a stopped/start lifecycle without
replacing its data volume. Official AWS token generation plus stock PostgreSQL
and MySQL drivers exercise schema, insert, query, denial, TLS enforcement,
password rotation, and persistence operations against all three real engines.

AWS Step Functions Task states support optimized Amazon ECS `RunTask`
request/response, `.sync`, and callback-token integrations, plus the optimized
AWS CodeBuild build and build-batch operations. Synchronous workflows poll the
actual service resources, propagate terminal failures, and stop work they
started when the execution is aborted. CodeBuild clones private Git sources
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

The `terraform-tests/TestStackProductionShape` exercise provisions a CloudFront-fronted application with WAF + ACM + Route 53 + Amplify + IAM SLR in a single `terraform apply`. Captured 2026-05-15 (sim port `:NNNN` shown as `:46241` here):

```bash
# Boot the sim
$ AWS_ENDPOINT_URL=http://127.0.0.1:46241 ./simulator-aws &

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
  [arn=arn:aws:acm:us-east-1:000000000000:certificate/...]
aws_iam_service_linked_role.tf_slr_cloudfront: Creation complete after 0.02s
aws_iam_openid_connect_provider.tf_oidc: Creation complete after 0.02s
aws_amplify_app.tf_amplify: Creation complete after 0.01s

# Verify cross-resource references
$ terraform output -json | jq '. | with_entries(.value = .value.value)'
{
  "cloudfront_arn":             "arn:aws:cloudfront::000000000000:distribution/E8E549340297351",
  "cloudfront_domain_name":     "E8E549340297351.cloudfront.net",
  "wafv2_assoc_resource_arn":   "arn:aws:cloudfront::000000000000:distribution/E8E549340297351",
  "route53_alias_target_name":  "E8E549340297351.cloudfront.net",
  "acm_certificate_arn":        "arn:aws:acm:us-east-1:000000000000:certificate/...",
  "iam_slr_arn":                "arn:aws:iam::000000000000:role/aws-service-role/cloudfront.amazonaws.com/AWSServiceRoleForCloudFrontLogger_tftest"
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

Each test package's `TestMain` builds the simulator binary, finds a free port, boots the sim, waits for `/health`, runs the suite, then kills the sim. No external services needed.

## Known issues

None open for the services covered here. Selected closed items:

- **BUG-991** — `docker run --rm` against `backends/docker` used to fail with `No such container`. Fixed by removing the Store-direct shortcut in `handleContainerWait`.
- **BUG-992** — `docker images` used to return empty even when the upstream daemon had images. Fixed by delegating to `s.self.ImageList`.
- **issue #381** — ECS managed EBS volumes were stored on the sim process's own filesystem and bind-mounted by path, so task containers launched as Docker siblings couldn't see the data. `CreateVpc`/`CreateSubnet` also hard-failed without host nftables even when only control-plane API calls were needed. Fixed: ECS EBS volumes now use Docker named volumes; VPC/Subnet store state unconditionally and set up real networking fabric lazily when host caps are present and a data-plane resource attaches.

## What's out of scope

- **Edge propagation timing** — CloudFront distributions report `Status: Deployed` immediately; invalidations report `Completed` immediately. Real CloudFront cycles `InProgress → Deployed` over 5–15 minutes.
- **DNS resolution** — Route 53 stores records but does not serve them via UDP/53. The sim's purpose is API-shape parity, not actual DNS resolution. Use a separate dnsmasq sidecar if you need lookups.
- **WAF traffic inspection** — `GetSampledRequests` returns an empty list. The sim accepts WebACL rule definitions but doesn't actually filter traffic.
- **ACM cert auto-validation** — `RequestCertificate` with `ValidationMethod=DNS` stays `PENDING_VALIDATION` until you `ImportCertificate` to flip a cert to `ISSUED`. Real ACM polls Route 53 for the challenge CNAME.
- **Multi-region routing** — sim is single-region (defaults to `us-east-1`). Cross-region replication / failover is not modelled.
- **Cost / billing surfaces** — `cur`, `pricing`, `cost-explorer` are absent.
- **Real authentication** — sigv4 headers are accepted but not cryptographically verified.

See also: [API_SPEC.md](API_SPEC.md), [docs/POD_MATERIALIZATION.md](https://github.com/e6qu/sockerless/blob/main/docs/POD_MATERIALIZATION.md), [specs/CLOUD_RESOURCE_MAPPING.md](https://github.com/e6qu/sockerless/blob/main/specs/CLOUD_RESOURCE_MAPPING.md), [backends/ecs/README.md](https://github.com/e6qu/sockerless/blob/main/backends/ecs/README.md), [backends/lambda/README.md](https://github.com/e6qu/sockerless/blob/main/backends/lambda/README.md).
