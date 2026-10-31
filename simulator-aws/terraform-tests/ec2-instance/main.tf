terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
    }
  }
}

variable "endpoint" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    ec2 = var.endpoint
    iam = var.endpoint
    ssm = var.endpoint
  }
}

# The instance profile lets SSM Agent register the instance as a managed node,
# as AmazonSSMManagedInstanceCore does on AWS.
resource "aws_iam_role" "ssm" {
  name = "tf-ec2-instance-ssm"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "ssm" {
  name = "ssm-managed-node"
  role = aws_iam_role.ssm.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["ssm:UpdateInstanceInformation", "ssmmessages:*", "ec2messages:*"]
      Resource = "*"
    }]
  })
}

resource "aws_iam_instance_profile" "ssm" {
  name = "tf-ec2-instance-ssm"
  role = aws_iam_role.ssm.name
}

data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-kernel-6.1-x86_64"]
  }

  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

resource "aws_vpc" "main" {
  cidr_block = "10.218.0.0/16"
}

resource "aws_subnet" "main" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.218.1.0/24"
}

resource "aws_instance" "golden" {
  ami                  = data.aws_ami.al2023.id
  instance_type        = "t3.micro"
  subnet_id            = aws_subnet.main.id
  iam_instance_profile = aws_iam_instance_profile.ssm.name
  user_data            = "#!/bin/sh\necho terraform-user-data-ran\n"

  depends_on = [aws_iam_role_policy.ssm]

  tags = {
    Name = "golden"
  }
}

resource "aws_ssm_association" "golden" {
  name = "AWS-RunShellScript"

  targets {
    key    = "InstanceIds"
    values = [aws_instance.golden.id]
  }

  parameters = {
    commands = "echo association-ran"
  }
}

output "association_id" {
  value = aws_ssm_association.golden.association_id
}

resource "aws_ami_from_instance" "golden" {
  name               = "golden-from-instance"
  source_instance_id = aws_instance.golden.id
}

resource "aws_instance" "clone" {
  ami           = aws_ami_from_instance.golden.id
  instance_type = "t3.micro"
  subnet_id     = aws_subnet.main.id

  instance_initiated_shutdown_behavior = "terminate"
}

output "ami_name" {
  value = data.aws_ami.al2023.name
}

output "ami_owner_alias" {
  value = data.aws_ami.al2023.image_owner_alias
}

# aws_instance exports no architecture; the image the instance booted from
# carries it.
output "golden_architecture" {
  value = data.aws_ami.al2023.architecture
}

output "golden_state" {
  value = aws_instance.golden.instance_state
}

output "captured_architecture" {
  value = aws_ami_from_instance.golden.architecture
}

output "clone_state" {
  value = aws_instance.clone.instance_state
}

output "clone_shutdown_behavior" {
  value = aws_instance.clone.instance_initiated_shutdown_behavior
}

output "golden_shutdown_behavior" {
  value = aws_instance.golden.instance_initiated_shutdown_behavior
}

output "clone_ami" {
  value = aws_instance.clone.ami
}

output "captured_ami" {
  value = aws_ami_from_instance.golden.id
}
