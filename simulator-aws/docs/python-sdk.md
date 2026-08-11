# Using the AWS simulator with boto3

## Prerequisites

- Python 3.8+
- `boto3` installed (`pip install boto3`)
- Simulator running on `http://localhost:4566`

## Setup

Create a boto3 client or resource with the `endpoint_url` parameter pointing at the simulator:

```python
import boto3

session = boto3.Session(
    aws_access_key_id="test",
    aws_secret_access_key="test",
    region_name="us-east-1",
)

# Per-service clients
ecs = session.client("ecs", endpoint_url="http://localhost:4566")
ecr = session.client("ecr", endpoint_url="http://localhost:4566")
ec2 = session.client("ec2", endpoint_url="http://localhost:4566")
iam = session.client("iam", endpoint_url="http://localhost:4566")
sts = session.client("sts", endpoint_url="http://localhost:4566")
logs = session.client("logs", endpoint_url="http://localhost:4566")
efs = session.client("efs", endpoint_url="http://localhost:4566")
lam = session.client("lambda", endpoint_url="http://localhost:4566")
sd = session.client("servicediscovery", endpoint_url="http://localhost:4566")
s3 = session.client("s3", endpoint_url="http://localhost:4566/s3")
```

Any access key and secret will be accepted. S3 requires the `/s3` path prefix.

Alternatively, set environment variables and let boto3 pick them up:

```sh
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1
```

```python
import boto3

# boto3 reads AWS_ENDPOINT_URL automatically (boto3 >= 1.34)
ecs = boto3.client("ecs")
```

> Note: `AWS_ENDPOINT_URL` is supported in boto3 1.34+. For older versions, pass `endpoint_url` explicitly.

## Examples

### STS

```python
sts = session.client("sts", endpoint_url="http://localhost:4566")

identity = sts.get_caller_identity()
print(identity["Account"])  # "123456789012"
```

### ECS

```python
ecs = session.client("ecs", endpoint_url="http://localhost:4566")

# Create a cluster
ecs.create_cluster(clusterName="my-cluster")

# Register a task definition
ecs.register_task_definition(
    family="my-task",
    containerDefinitions=[{
        "name": "main",
        "image": "nginx:latest",
        "essential": True,
    }],
)

# Run a task
ecs.run_task(cluster="my-cluster", taskDefinition="my-task")

# Describe clusters
clusters = ecs.describe_clusters(clusters=["my-cluster"])
print(clusters["clusters"][0]["clusterName"])

# Cleanup
ecs.delete_cluster(cluster="my-cluster")
```

### ECR

```python
ecr = session.client("ecr", endpoint_url="http://localhost:4566")

# Create a repository
ecr.create_repository(repositoryName="my-repo")

# Get auth token
auth = ecr.get_authorization_token()
print(auth["authorizationData"][0]["authorizationToken"])

# Describe repositories
repos = ecr.describe_repositories()
print(repos["repositories"][0]["repositoryName"])

# Cleanup
ecr.delete_repository(repositoryName="my-repo")
```

### Lambda

```python
import json

lam = session.client("lambda", endpoint_url="http://localhost:4566")

# Create a function (from a zip file)
with open("function.zip", "rb") as f:
    lam.create_function(
        FunctionName="my-func",
        Runtime="python3.12",
        Role="arn:aws:iam::123456789012:role/test-role",
        Handler="lambda_function.handler",
        Code={"ZipFile": f.read()},
    )

# Get function
func = lam.get_function(FunctionName="my-func")
print(func["Configuration"]["FunctionName"])

# Invoke
resp = lam.invoke(FunctionName="my-func")
print(resp["StatusCode"])

# Cleanup
lam.delete_function(FunctionName="my-func")
```

### S3

```python
s3 = session.client("s3", endpoint_url="http://localhost:4566/s3")

# Create a bucket
s3.create_bucket(Bucket="my-bucket")

# Upload an object
s3.put_object(Bucket="my-bucket", Key="hello.txt", Body=b"hello world")

# Download an object
resp = s3.get_object(Bucket="my-bucket", Key="hello.txt")
print(resp["Body"].read().decode())

# List objects
objects = s3.list_objects_v2(Bucket="my-bucket")
for obj in objects.get("Contents", []):
    print(obj["Key"])

# Delete
s3.delete_object(Bucket="my-bucket", Key="hello.txt")
s3.delete_bucket(Bucket="my-bucket")
```

### CloudWatch Logs

```python
import time

logs = session.client("logs", endpoint_url="http://localhost:4566")

# Create log group and stream
logs.create_log_group(logGroupName="/my/logs")
logs.create_log_stream(logGroupName="/my/logs", logStreamName="stream1")

# Put log events
logs.put_log_events(
    logGroupName="/my/logs",
    logStreamName="stream1",
    logEvents=[{
        "timestamp": int(time.time() * 1000),
        "message": "hello from boto3",
    }],
)

# Get log events
events = logs.get_log_events(
    logGroupName="/my/logs",
    logStreamName="stream1",
)
for event in events["events"]:
    print(event["message"])

# Cleanup
logs.delete_log_group(logGroupName="/my/logs")
```

### EC2

```python
ec2 = session.client("ec2", endpoint_url="http://localhost:4566")

# Create a VPC
vpc = ec2.create_vpc(CidrBlock="10.0.0.0/16")
vpc_id = vpc["Vpc"]["VpcId"]

# Create a subnet
subnet = ec2.create_subnet(VpcId=vpc_id, CidrBlock="10.0.1.0/24")
subnet_id = subnet["Subnet"]["SubnetId"]

# Create a security group
sg = ec2.create_security_group(
    GroupName="my-sg",
    Description="Test security group",
    VpcId=vpc_id,
)
sg_id = sg["GroupId"]

# Add an ingress rule
ec2.authorize_security_group_ingress(
    GroupId=sg_id,
    IpPermissions=[{
        "IpProtocol": "tcp",
        "FromPort": 80,
        "ToPort": 80,
        "IpRanges": [{"CidrIp": "0.0.0.0/0"}],
    }],
)

# Describe
vpcs = ec2.describe_vpcs()
print(f"VPCs: {len(vpcs['Vpcs'])}")

# Cleanup
ec2.delete_security_group(GroupId=sg_id)
ec2.delete_subnet(SubnetId=subnet_id)
ec2.delete_vpc(VpcId=vpc_id)
```

### IAM

```python
import json

iam = session.client("iam", endpoint_url="http://localhost:4566")

# Create a role
iam.create_role(
    RoleName="my-role",
    AssumeRolePolicyDocument=json.dumps({
        "Version": "2012-10-17",
        "Statement": [{
            "Effect": "Allow",
            "Principal": {"Service": "ecs-tasks.amazonaws.com"},
            "Action": "sts:AssumeRole",
        }],
    }),
)

# Get role
role = iam.get_role(RoleName="my-role")
print(role["Role"]["RoleName"])

# Put inline policy
iam.put_role_policy(
    RoleName="my-role",
    PolicyName="my-policy",
    PolicyDocument=json.dumps({
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Action": "*", "Resource": "*"}],
    }),
)

# Cleanup
iam.delete_role_policy(RoleName="my-role", PolicyName="my-policy")
iam.delete_role(RoleName="my-role")
```

### EFS

```python
efs = session.client("efs", endpoint_url="http://localhost:4566")

# Create a file system
fs = efs.create_file_system(CreationToken="my-efs")
fs_id = fs["FileSystemId"]

# Describe
filesystems = efs.describe_file_systems()
print(f"File systems: {len(filesystems['FileSystems'])}")

# Cleanup
efs.delete_file_system(FileSystemId=fs_id)
```

### Cloud Map (Service Discovery)

```python
sd = session.client("servicediscovery", endpoint_url="http://localhost:4566")

# Create a namespace
resp = sd.create_private_dns_namespace(
    Name="my-namespace.local",
    Vpc="vpc-12345",
)
op_id = resp["OperationId"]

# Get namespace (need to get ID from operation or list)
namespaces = sd.list_namespaces()
ns_id = namespaces["Namespaces"][0]["Id"]

# Create a service
svc = sd.create_service(
    Name="my-service",
    NamespaceId=ns_id,
    DnsConfig={
        "NamespaceId": ns_id,
        "DnsRecords": [{"Type": "A", "TTL": 60}],
    },
)
svc_id = svc["Service"]["Id"]

# Cleanup
sd.delete_service(Id=svc_id)
sd.delete_namespace(Id=ns_id)
```

## Notes

- Authentication is accepted but not validated. Any credentials will work.
- All state is in-memory and resets when the simulator restarts.
- S3 requires the `/s3` path prefix in the endpoint URL.
- The simulator returns the local-test account ID `123456789012` for STS calls.
