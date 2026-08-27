# Using the AWS simulator with the AWS CLI

## Prerequisites

- AWS CLI v2 installed (`aws --version`) — use a current version; recently-launched operations (e.g. the ECS Express Mode subcommands) are absent from older CLIs. CI installs the latest v2 for this reason.
- Simulator running on `http://localhost:4566`

## Setup

Export the following environment variables to point the AWS CLI at the simulator:

```sh
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
export AWS_PAGER=
```

`AWS_ENDPOINT_URL` redirects all AWS CLI requests to the simulator. The simulator accepts any access key and secret. `AWS_PAGER=` disables the pager for easier scripting.

For S3 commands, the simulator routes S3 under a `/s3` prefix:

```sh
export AWS_ENDPOINT_URL=http://localhost:4566/s3
```

If you need both S3 and non-S3 commands in the same session, use the `--endpoint-url` flag for S3 commands instead:

```sh
aws s3 ls --endpoint-url http://localhost:4566/s3
```

## Examples

### STS

```sh
aws sts get-caller-identity --output json
```

### ECS

```sh
# Create a cluster
aws ecs create-cluster --cluster-name my-cluster --output json

# Describe clusters
aws ecs describe-clusters --clusters my-cluster --output json

# Register a task definition
aws ecs register-task-definition \
  --family my-task \
  --container-definitions '[{"name":"main","image":"nginx","essential":true}]' \
  --output json

# Run a task
aws ecs run-task --cluster my-cluster --task-definition my-task --output json
```

### ECR

```sh
# Create a repository
aws ecr create-repository --repository-name my-repo --output json

# Get authorization token
aws ecr get-authorization-token --output json

# Describe repositories
aws ecr describe-repositories --output json
```

### Lambda

```sh
# Create a function
aws lambda create-function \
  --function-name my-func \
  --runtime nodejs18.x \
  --role arn:aws:iam::123456789012:role/test-role \
  --handler index.handler \
  --zip-file fileb://function.zip \
  --output json

# Get function details
aws lambda get-function --function-name my-func --output json

# List functions
aws lambda list-functions --output json

# Invoke
aws lambda invoke --function-name my-func /dev/stdout

# Delete
aws lambda delete-function --function-name my-func
```

### S3

```sh
# Create a bucket
aws s3 mb s3://my-bucket --endpoint-url http://localhost:4566/s3

# Upload a file
aws s3 cp myfile.txt s3://my-bucket/myfile.txt --endpoint-url http://localhost:4566/s3

# List objects
aws s3 ls s3://my-bucket/ --endpoint-url http://localhost:4566/s3

# Download a file
aws s3 cp s3://my-bucket/myfile.txt downloaded.txt --endpoint-url http://localhost:4566/s3

# Remove
aws s3 rm s3://my-bucket/myfile.txt --endpoint-url http://localhost:4566/s3
aws s3 rb s3://my-bucket --endpoint-url http://localhost:4566/s3
```

### CloudWatch Logs

```sh
# Create a log group
aws logs create-log-group --log-group-name /my/logs --output json

# Create a log stream
aws logs create-log-stream --log-group-name /my/logs --log-stream-name stream1

# Put log events
aws logs put-log-events \
  --log-group-name /my/logs \
  --log-stream-name stream1 \
  --log-events '[{"timestamp":1700000000000,"message":"hello world"}]' \
  --output json

# Get log events
aws logs get-log-events \
  --log-group-name /my/logs \
  --log-stream-name stream1 \
  --output json

# Delete
aws logs delete-log-group --log-group-name /my/logs
```

### Cloud Map (Service Discovery)

```sh
# Create a namespace
aws servicediscovery create-private-dns-namespace \
  --name my-namespace.local \
  --vpc vpc-12345 \
  --output json

# List namespaces
aws servicediscovery list-namespaces --output json

# Create a service
aws servicediscovery create-service \
  --name my-service \
  --namespace-id ns-abc123 \
  --dns-config 'NamespaceId=ns-abc123,DnsRecords=[{Type=A,TTL=60}]' \
  --output json
```

### EFS

```sh
# Create a file system
aws efs create-file-system --creation-token my-efs --output json

# Describe file systems
aws efs describe-file-systems --output json

# Create a mount target
aws efs create-mount-target \
  --file-system-id fs-abc123 \
  --subnet-id subnet-abc123 \
  --output json

# Delete
aws efs delete-file-system --file-system-id fs-abc123
```

### IAM

```sh
# Create a role
aws iam create-role \
  --role-name my-role \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[]}' \
  --output json

# Get role
aws iam get-role --role-name my-role --output json

# Attach a managed policy
aws iam attach-role-policy \
  --role-name my-role \
  --policy-arn arn:aws:iam::aws:policy/AmazonECSTaskExecutionRolePolicy

# Delete role
aws iam delete-role --role-name my-role
```

### EC2

```sh
# Create a VPC
aws ec2 create-vpc --cidr-block 10.0.0.0/16 --output json

# Create a subnet
aws ec2 create-subnet --vpc-id vpc-abc123 --cidr-block 10.0.1.0/24 --output json

# Create a security group
aws ec2 create-security-group \
  --group-name my-sg \
  --description "Test SG" \
  --vpc-id vpc-abc123 \
  --output json

# Describe VPCs
aws ec2 describe-vpcs --output json
```

## Supported services

| Service | CLI Subcommand | Tested |
|---------|---------------|--------|
| ECS | `aws ecs` | Yes |
| ECR | `aws ecr` | Yes (via SDK tests) |
| Lambda | `aws lambda` | Yes |
| S3 | `aws s3` / `aws s3api` | Yes |
| CloudWatch Logs | `aws logs` | Yes |
| Cloud Map | `aws servicediscovery` | Yes |
| EFS | `aws efs` | Yes |
| IAM | `aws iam` | Yes (via SDK tests) |
| EC2 | `aws ec2` | Yes (via SDK tests) |
| STS | `aws sts` | Yes |

## Automated bash tests

A self-contained bash test script exercises all services above in both `--output text` and `--output json` modes:

```sh
cd simulator-aws/bash-tests
./test_aws_cli.sh
```

The script builds the simulator, starts it on a random port, runs 61 tests, and prints a pass/fail summary.

## Notes

- The simulator accepts authentication without validating it. Any access key and secret works.
- All state is in-memory and resets when the simulator restarts.
- The simulator returns the local-test account ID `123456789012` for STS calls.
