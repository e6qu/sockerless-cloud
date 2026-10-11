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

`AWS_ENDPOINT_URL` sends every AWS CLI request, Amazon S3 included, to the
simulator. The simulator verifies each SigV4 signature: `test`/`test` is the
access key of the administrator every simulator starts with, as a new AWS
account starts with a root credential. `AWS_PAGER=` turns off the pager.

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
aws s3 mb s3://my-bucket

# Upload a file
aws s3 cp myfile.txt s3://my-bucket/myfile.txt

# List objects
aws s3 ls s3://my-bucket/

# Download a file
aws s3 cp s3://my-bucket/myfile.txt downloaded.txt

# Remove
aws s3 rm s3://my-bucket/myfile.txt
aws s3 rb s3://my-bucket
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

## Coverage

[`specs/SIM_PARITY_MATRIX.md`](../../specs/SIM_PARITY_MATRIX.md) lists every
service the simulator serves, and
[`specs/SIM_TEST_COVERAGE_MATRIX.md`](../../specs/SIM_TEST_COVERAGE_MATRIX.md)
names the CLI test that drives each surface (`simulator-aws/cli-tests/`).

## Notes

- State lives in memory unless the simulator runs with `SIM_PERSIST=true`, which
  keeps it under `SIM_DATA_DIR` across restarts.
- STS reports account `123456789012` unless `SOCKERLESS_AWS_ACCOUNT_ID` names
  another.
