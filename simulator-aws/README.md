# simulator-aws

`simulator-aws` is one binary that serves a slice of the AWS public API on one
port, `4566`. Every service shares that port, the way the AWS SDKs, the AWS
CLI and Terraform's `hashicorp/aws` provider reach AWS through one endpoint
override. The simulator verifies each request's SigV4 signature, evaluates
AWS Identity and Access Management (IAM) policies on it, and runs workloads as
real containers. Unit tests check every served operation and route against
the vendored Smithy models in [`specs/cloud-api/aws/`](../specs/cloud-api/aws/),
and `SOCKERLESS_SPEC_VALIDATE` checks live responses against them.

## Run it

```sh
# A release. Go pins it by its commit: the repository tags only vX.Y.Z at its root.
go install github.com/e6qu/sockerless-cloud/simulator-aws@<release-commit>
simulator-aws

# The release image, API-only.
docker run --rm -p 4566:4566 -e SIM_RUNTIME=process ghcr.io/e6qu/sockerless-simulator-aws:vX.Y.Z

# A checkout, with the console embedded (-tags console). Run from the repository root.
(cd ui && bun install && bunx turbo run build --filter=@sockerless/ui-simulator-aws)
make simulator-aws/build && ./simulator-aws/simulator-aws
```

`go install` and a plain `go build` embed no console; the release image does.

The simulator needs a Docker or Podman engine to run Amazon Elastic Compute
Cloud (Amazon EC2) instances, Amazon Elastic Container Service (Amazon ECS)
tasks, AWS Lambda functions, AWS Batch jobs, AWS CodeBuild builds, AWS Glue
jobs and AWS Amplify builds, and refuses to start without one.
`SIM_RUNTIME=process` starts it API-only, without an engine, and refuses every
operation that would run a workload. `GET /health` reports `runtime` and
`capabilities.workloadExecution`.

The variables every simulator shares (`SIM_LISTEN_ADDR`, `SIM_AWS_PORT`,
`SIM_PERSIST`, `SIM_DATA_DIR`, `SIM_PLACEMENT`, `SIM_TLS_*`, `SIM_LOG_LEVEL`,
the console's OpenID Connect set) are in
[`docs/configuration.md`](../docs/configuration.md). These are AWS's own:

| Variable | Default | Effect |
|---|---|---|
| `SOCKERLESS_AWS_ACCOUNT_ID` | `123456789012` | The account every ARN and AWS Security Token Service (AWS STS) identity names |
| `SOCKERLESS_AWS_REGION` | `us-east-1` | The home Region: the Region resources report and the Availability Zones derive from |
| `SOCKERLESS_AWS_ORG_ID` | `o-sim0000000` | The AWS Organizations organization the account manages |
| `SIM_DNS_PORT` | a kernel-chosen port | The UDP and TCP port the Amazon Route 53 resolver binds. Workloads reach it through their network's sidecar, never at this port. |
| `SIM_DNS_UPSTREAM` | the host's `resolv.conf` | Resolvers, `host[:port]` comma-separated, for names no hosted zone holds |
| `SIM_EBS_DATA_DIR` | `<SIM_DATA_DIR>/ebs` | Where Amazon Elastic Block Store (Amazon EBS) snapshot payloads live |
| `SIM_EFS_DATA_DIR` | `<SIM_DATA_DIR>/efs` | Where Amazon Elastic File System (Amazon EFS) file contents live |
| `SOCKERLESS_SPEC_VALIDATE`, `SOCKERLESS_SPEC_DIR` | unset | Validate every success response against the Smithy models in `SOCKERLESS_SPEC_DIR` and write violations to the report file `SOCKERLESS_SPEC_VALIDATE` names |

In `SIM_PLACEMENT`, the AWS services are `ec2`, `ecs` (qualified by launch
type, such as `ecs:FARGATE`), `lambda` and `batch`.

## Connect a client

Every client takes three coordinates: the endpoint `http://localhost:4566`,
the access key `test` with secret `test`, and a Region. `test`/`test` is the
administrator the simulator starts with, as a new AWS account starts with its
root credential. Two settings follow from serving every service on one host:

- The simulator serves Amazon Simple Storage Service (Amazon S3)
  general-purpose buckets path-style. Set path-style in the Go SDK and
  Terraform; the AWS CLI and boto3 use it on their own once an endpoint is set.
- An operation with a modeled host prefix (AWS Cloud Map's
  `DiscoverInstances`, AWS Step Functions' `StartSyncExecution`, every
  Amazon S3 Control operation) puts a label in front of the endpoint's host.
  Give the client a host that the prefixed name still resolves for, such as
  `http://servicediscovery.localhost:4566`, or route it through the simulator
  as its HTTP proxy (`HTTP_PROXY=http://127.0.0.1:4566`).

### AWS SDK for Go v2

```go
cfg := aws.Config{
	Region:      "us-east-1",
	Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
}
sqsClient := sqs.NewFromConfig(cfg, func(o *sqs.Options) {
	o.BaseEndpoint = aws.String("http://localhost:4566")
})
s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
	o.BaseEndpoint = aws.String("http://localhost:4566")
	o.UsePathStyle = true
})
```

`sdkConfig` in [`sdk-tests/helpers_test.go`](sdk-tests/helpers_test.go) is
the configuration every client of the SDK suite starts from; `simEndpoint`
there gives the `<service>.localhost` endpoint host-prefixed operations need.

### AWS CLI

```sh
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1 AWS_PAGER=

aws sts get-caller-identity
aws sqs create-queue --queue-name demo
```

`AWS_ENDPOINT_URL` sends every service to the simulator;
`AWS_ENDPOINT_URL_<SERVICE>` overrides it for one. `awsCLI` in
[`cli-tests/helpers_test.go`](cli-tests/helpers_test.go) runs every command
of the CLI suite with this environment. Use a current AWS CLI v2. Older
releases lack recently launched operations, and AWS CLI v1 sends Amazon Simple
Queue Service (Amazon SQS) requests to AWS whatever `AWS_ENDPOINT_URL` says.

### Terraform

```hcl
provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3  = "http://localhost:4566"
    sqs = "http://localhost:4566"
    sts = "http://localhost:4566"
    # one line per service the configuration uses
  }
}
```

The `skip_*` flags keep the provider from probing the instance metadata
service and AWS STS before each run. Leave `skip_requesting_account_id` unset
where a resource is addressed by account ID, as Amazon S3 Control's are.
[`terraform-tests/main.tf`](terraform-tests/main.tf) carries the full block
the suite uses; `tfsim.Start` in
[`terraform-tests/internal/tfsim`](terraform-tests/internal/tfsim/tfsim.go)
passes it the endpoint as `TF_VAR_endpoint`. Without `SIM_PERSIST=true`, a
restart leaves Terraform state describing resources that no longer exist.

### boto3

```python
import boto3

session = boto3.Session(aws_access_key_id="test", aws_secret_access_key="test",
                        region_name="us-east-1")
sqs = session.client("sqs", endpoint_url="http://localhost:4566")
sqs.create_queue(QueueName="demo")
```

boto3 also reads `AWS_ENDPOINT_URL` and the credential variables above.
`TestLambdaExplicitDeploymentEnvironmentAndDownstreamSDK` in
[`sdk-tests/lambda_test.go`](sdk-tests/lambda_test.go) runs boto3 inside a
Lambda function and has it call Amazon SQS and AWS STS through
`AWS_ENDPOINT_URL`.

## Services

Each surface table lists the operations the simulator registers and the tests
that drive them. `buildSimulatorWithOptions` in [`main.go`](main.go) registers
every slice.

| Area | Service | Protocol | Surface |
|---|---|---|---|
| Compute | Amazon EC2, with Amazon EBS and Amazon VPC | EC2 Query | [ec2](../specs/SIM_SURFACE_TABLES/aws-ec2.md) |
| Compute | AWS Lambda | REST-JSON | [lambda](../specs/SIM_SURFACE_TABLES/aws-lambda.md) |
| Compute | AWS Batch | REST-JSON | [batch](../specs/SIM_SURFACE_TABLES/aws-batch.md) |
| Compute | Amazon EC2 Auto Scaling | AWS Query | [autoscaling](../specs/SIM_SURFACE_TABLES/aws-autoscaling.md) |
| Compute | Application Auto Scaling | AWS JSON 1.1 | [application-autoscaling](../specs/SIM_SURFACE_TABLES/aws-application-autoscaling.md) |
| Containers | Amazon ECS | AWS JSON 1.1 | [ecs](../specs/SIM_SURFACE_TABLES/aws-ecs.md) |
| Containers | Amazon Elastic Container Registry (Amazon ECR), with its OCI registry | AWS JSON 1.1 | [ecr](../specs/SIM_SURFACE_TABLES/aws-ecr.md) |
| Storage | Amazon S3, with S3 Express One Zone and S3 Object Lambda | REST-XML | [s3](../specs/SIM_SURFACE_TABLES/aws-s3.md), [subresources](../specs/SIM_SURFACE_TABLES/aws-s3-bucket-subresources.md), [multipart](../specs/SIM_SURFACE_TABLES/aws-s3-multipart.md), [Object Lambda](../specs/SIM_SURFACE_TABLES/aws-s3_object_lambda.md) |
| Storage | Amazon S3 Control | REST-XML | [jobs](../specs/SIM_SURFACE_TABLES/aws-s3control_jobs.md), [Storage Lens](../specs/SIM_SURFACE_TABLES/aws-s3control_storage_lens.md), [access grants](../specs/SIM_SURFACE_TABLES/aws-s3control_access_grants.md), [Multi-Region Access Points](../specs/SIM_SURFACE_TABLES/aws-s3control_mrap.md), [other](../specs/SIM_SURFACE_TABLES/aws-s3control_misc.md) |
| Storage | Amazon EFS | REST-JSON | [efs](../specs/SIM_SURFACE_TABLES/aws-efs.md) |
| Databases | Amazon Relational Database Service (Amazon RDS), with Amazon Aurora | AWS Query | [rds](../specs/SIM_SURFACE_TABLES/aws-rds.md) |
| Databases | Amazon DynamoDB | AWS JSON 1.0 | [dynamodb](../specs/SIM_SURFACE_TABLES/aws-dynamodb.md) |
| Databases | Amazon ElastiCache | AWS Query | [elasticache](../specs/SIM_SURFACE_TABLES/aws-elasticache.md) |
| Networking | Elastic Load Balancing (Application, Network and Gateway Load Balancers) | AWS Query | [elbv2](../specs/SIM_SURFACE_TABLES/aws-elbv2.md) |
| Networking | Amazon Route 53 | REST-XML | [route53](../specs/SIM_SURFACE_TABLES/aws-route53.md) |
| Networking | Amazon CloudFront | REST-XML | [cloudfront](../specs/SIM_SURFACE_TABLES/aws-cloudfront.md), [more](../specs/SIM_SURFACE_TABLES/aws-cloudfront_extras.md) |
| Networking | AWS Cloud Map | AWS JSON 1.1 | [cloudmap](../specs/SIM_SURFACE_TABLES/aws-cloudmap.md) |
| Networking | Amazon API Gateway: REST APIs, and HTTP and WebSocket APIs (V2) | REST-JSON | [apigateway](../specs/SIM_SURFACE_TABLES/aws-apigateway.md), [apigatewayv2](../specs/SIM_SURFACE_TABLES/aws-apigatewayv2.md) |
| Messaging | Amazon SQS | AWS JSON 1.0 | [sqs](../specs/SIM_SURFACE_TABLES/aws-sqs.md) |
| Messaging | Amazon Simple Notification Service (Amazon SNS) | AWS Query | [sns](../specs/SIM_SURFACE_TABLES/aws-sns.md) |
| Messaging | Amazon EventBridge | AWS JSON 1.1 | [eventbridge](../specs/SIM_SURFACE_TABLES/aws-eventbridge.md) |
| Messaging | Amazon EventBridge Scheduler | REST-JSON | [scheduler](../specs/SIM_SURFACE_TABLES/aws-scheduler.md) |
| Messaging | AWS Step Functions | AWS JSON 1.0 | [stepfunctions](../specs/SIM_SURFACE_TABLES/aws-stepfunctions.md) |
| Messaging | Amazon Kinesis Data Streams | AWS JSON 1.1 | [kinesis](../specs/SIM_SURFACE_TABLES/aws-kinesis.md) |
| Messaging | Amazon Data Firehose | AWS JSON 1.1 | [firehose](../specs/SIM_SURFACE_TABLES/aws-firehose.md) |
| Identity and security | IAM | AWS Query | [iam](../specs/SIM_SURFACE_TABLES/aws-iam.md) |
| Identity and security | AWS Security Token Service (AWS STS) | AWS Query | [sts](../specs/SIM_SURFACE_TABLES/aws-sts.md) |
| Identity and security | AWS Key Management Service (AWS KMS) | AWS JSON 1.1 | [kms](../specs/SIM_SURFACE_TABLES/aws-kms.md) |
| Identity and security | AWS Secrets Manager | AWS JSON 1.1 | [secretsmanager](../specs/SIM_SURFACE_TABLES/aws-secretsmanager.md) |
| Identity and security | AWS Certificate Manager (ACM), with its ACME endpoints | AWS JSON 1.1 | [acm](../specs/SIM_SURFACE_TABLES/aws-acm.md), [ACME](../specs/SIM_SURFACE_TABLES/aws-acm_acme.md) |
| Identity and security | AWS Private Certificate Authority | AWS JSON 1.1 | [acmpca](../specs/SIM_SURFACE_TABLES/aws-acmpca.md) |
| Identity and security | AWS WAF | AWS JSON 1.1 | [wafv2](../specs/SIM_SURFACE_TABLES/aws-wafv2.md) |
| Management | Amazon CloudWatch | Smithy RPC v2 CBOR, AWS JSON 1.0, AWS Query | [cloudwatch](../specs/SIM_SURFACE_TABLES/aws-cloudwatch.md) |
| Management | Amazon CloudWatch Logs | AWS JSON 1.1 | [cloudwatch](../specs/SIM_SURFACE_TABLES/aws-cloudwatch.md) |
| Management | AWS CloudTrail, with CloudTrail Lake | AWS JSON 1.1 | [cloudtrail](../specs/SIM_SURFACE_TABLES/aws-cloudtrail.md) |
| Management | AWS Systems Manager | AWS JSON 1.1 | [ssm](../specs/SIM_SURFACE_TABLES/aws-ssm_parameters.md) |
| Management | AWS Organizations | AWS JSON 1.1 | [organizations](../specs/SIM_SURFACE_TABLES/aws-organizations.md) |
| Management | AWS Budgets | AWS JSON 1.1 | [budgets](../specs/SIM_SURFACE_TABLES/aws-budgets.md) |
| Developer tools | AWS CodeBuild | AWS JSON 1.1 | [codebuild](../specs/SIM_SURFACE_TABLES/aws-codebuild.md) |
| Developer tools | AWS Amplify, with its hosting data plane | REST-JSON | [amplify](../specs/SIM_SURFACE_TABLES/aws-amplify.md) |
| Analytics | AWS Glue | AWS JSON 1.1 | [glue](../specs/SIM_SURFACE_TABLES/aws-glue.md) |

## How it runs your workloads

[`docs/architecture.md`](../docs/architecture.md) describes what the three
simulators share: placement, engine networks, sidecars and recovery.

### VPC networks

`CreateVpc` and `CreateSubnet` record API state. The first workload placed in
a subnet creates the VPC's engine network, `sockerless-sim-vpc-<vpc-id>`, over
the VPC's own CIDR, and the workload's private address is its address there.
EC2 instances, `awsvpc` tasks and VPC-attached Lambda functions land on it.
Every other workload lands on the platform network,
`sockerless-sim-aws-platform-<scope>`; none lands on the engine's default
bridge. Security groups filter what load balancers, published ports and NAT
gateways carry to a workload, but two containers on one network reach each
other on every port. Two VPCs with one CIDR can't coexist on one engine.

### The Route 53 Resolver

Each network carries one sidecar at the address the VPC reserves for the
Amazon Route 53 Resolver, the network's base plus two. Every workload's
resolver is that sidecar. It answers from the public hosted zones, the private
hosted zones associated with the workload's VPC (AWS Cloud Map namespaces
among them) and the load balancers' names, and recurses for everything else
through `SIM_DNS_UPSTREAM`.

### EC2 instance metadata (IMDSv2)

The sidecar forwards metadata requests to the simulator with the caller's
address, so each instance reads only its own record. An instance finds the
service through `AWS_EC2_METADATA_SERVICE_ENDPOINT`, which names the sidecar;
`169.254.169.254` isn't routed ([BUGS.md](../BUGS.md)). `PUT /latest/api/token`
issues an IMDSv2 session token for 1 to 21,600 seconds and refuses a request a
proxy forwarded; `HttpTokens=required` refuses every read without a live
token. The service serves the identity document, placement, network
interfaces, block devices, the key pair's public key, tags when
`InstanceMetadataTags` allows them, and sessions of the instance profile's
role.

Only an EC2 instance reaches this service. A Lambda function and Amplify
compute take their role's session from environment variables; an ECS task, a
CodeBuild build, a Batch job and a Glue job run read theirs from
`AWS_CONTAINER_CREDENTIALS_FULL_URI`.

An instance is a container by default, with the AMI's image, its instance
type's vCPUs and memory, and its user data run under cloud-init.
`DescribeImages` lists Amazon Linux 2023, Ubuntu 24.04 LTS and Debian 12 for
`x86_64` and `arm64`.

### ECS task networking

The task definition's `networkMode` decides where the task's containers land:

| `networkMode` | What the task gets |
|---|---|
| `awsvpc` | An elastic network interface on the VPC's network. `RunTask` requires `networkConfiguration`. The task's other containers share the first one's network namespace. |
| `bridge` (the default) | An address per container on the platform network, and no interface. `RunTask` rejects `networkConfiguration`. |
| `host` | The engine host's network stack |
| `none` | No network |

A task reads its metadata at `ECS_CONTAINER_METADATA_URI_V4` and its task
role's credentials at `AWS_CONTAINER_CREDENTIALS_FULL_URI`, both on the
sidecar. `StopTask` sends `SIGTERM`, then `SIGKILL` after each container
definition's `stopTimeout`, 30 seconds when unset.

### Lambda execution environments

An execution environment is a container with its own Runtime API at
`AWS_LAMBDA_RUNTIME_API`. A ZIP function runs in AWS's managed-runtime base
image (`public.ecr.aws/lambda/<runtime>`) with its package at `/var/task` and
its layers at `/opt`; an image function runs its image with its
`ImageConfig`. The function's `MemorySize` limits the container's memory.

An environment stays initialized between invocations and serves one at a
time. Each function version keeps its own pool of environments, within the
function's reserved concurrency and the account's limit of 1,000. Provisioned
concurrency initializes environments before any invocation, and an update to
`$LATEST` drains the old environments.

The function runs as a session of its execution role, which Lambda passes in
the environment. The simulator also sets `AWS_ENDPOINT_URL` to an address the
container reaches it on, unless the function's own environment sets it.
CodeBuild builds get the same variable.

### Guest-kernel requirements

Docker 28 and later adds a `raw`-table `PREROUTING` drop rule for every
container it attaches to a bridge network, so a kernel without
`CONFIG_IP_NF_RAW` starts no workload except `host`- and `none`-mode ECS
tasks. The workload fails with the missing module named. Boot the engine host
on a kernel with the full netfilter set (`iptable_raw`, and `nf_tables` for the
nftables backend), or start the Docker daemon with
`DOCKER_INSECURE_NO_IPTABLES_RAW=1`, which turns off Docker's direct-access
filtering host-wide.

## Test it

| Target | What it runs |
|---|---|
| `make simulator-aws/unit-test` | The module's own tests. These include the conformance tests that check every `X-Amz-Target`, `Action` and REST route against the Smithy models. No engine is needed. |
| `make simulator-aws/sdk-test` | [`sdk-tests/`](sdk-tests/): the AWS SDK for Go v2, one file per service family |
| `make simulator-aws/cli-test` | [`cli-tests/`](cli-tests/): the AWS CLI, which `TestMain` installs when `aws` is not on `PATH` |
| `make simulator-aws/terraform-test` | [`terraform-tests/`](terraform-tests/): `hashicorp/aws` through `terraform apply`, output assertions and `terraform destroy`, one package per stack |

The SDK, CLI and Terraform targets build the simulator, and each suite starts
it on a free port, waits for its `Listening on` banner and stops it at the
end. They need a container engine, and the Terraform suite needs `terraform`.
`make simulator-aws/terraform-https-test` runs the Terraform suite through the
local HTTPS gateway, which needs `caddy`.

## Limits

- One home Region. Amazon RDS DB instances live in the Region that signs the
  creating request; every other service keeps resources in
  `SOCKERLESS_AWS_REGION`.
- No edge propagation. A CloudFront distribution or invalidation reports
  `Deployed` or `Completed` when its call returns.
- No cost or pricing APIs: AWS Cost and Usage Reports, AWS Price List and
  AWS Cost Explorer are absent. AWS Budgets is served.
- Amazon SNS SMS and mobile push fail and name the missing dependency: a
  carrier or a push service that no AWS API provisions.

[BUGS.md](../BUGS.md) lists open bugs.
