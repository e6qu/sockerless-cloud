terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.69.1"
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
  }
}

# The published Amazon Linux 2023 AMI, selected the way an operator selects
# it on AWS: by owner and name pattern, newest first.
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
  ami           = data.aws_ami.al2023.id
  instance_type = "t3.micro"
  subnet_id     = aws_subnet.main.id
  user_data     = "#!/bin/sh\necho terraform-user-data-ran\n"

  tags = {
    Name = "golden"
  }
}

# An AMI captured from the running instance, which a second instance boots.
resource "aws_ami_from_instance" "golden" {
  name               = "golden-from-instance"
  source_instance_id = aws_instance.golden.id
}

resource "aws_instance" "clone" {
  ami           = aws_ami_from_instance.golden.id
  instance_type = "t3.micro"
  subnet_id     = aws_subnet.main.id
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

output "clone_ami" {
  value = aws_instance.clone.ami
}

output "captured_ami" {
  value = aws_ami_from_instance.golden.id
}
