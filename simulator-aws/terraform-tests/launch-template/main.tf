terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
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

resource "aws_vpc" "main" {
  cidr_block = "10.79.0.0/16"
}

resource "aws_subnet" "main" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.79.1.0/24"
}

resource "aws_security_group" "nat" {
  name   = "fck-nat-sg"
  vpc_id = aws_vpc.main.id
}

# The fck-nat launch template: the ASG launch config for nat_mode = "instance".
# Create + read-back (DescribeLaunchTemplates + DescribeLaunchTemplateVersions)
# on apply; Delete on destroy. The launch-template data must round-trip so the
# provider sees no perpetual diff.
resource "aws_launch_template" "nat" {
  name          = "fck-nat-lt"
  image_id      = "ami-12345678"
  instance_type = "t4g.nano"

  network_interfaces {
    device_index                = 0
    associate_public_ip_address = true
    delete_on_termination       = true
    security_groups             = [aws_security_group.nat.id]
  }

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name = "fck-nat"
    }
  }

  metadata_options {
    http_tokens                 = "required"
    http_put_response_hop_limit = 2
    http_endpoint               = "enabled"
  }

  tags = {
    module = "fck-nat"
  }
}

output "launch_template_id" {
  value = aws_launch_template.nat.id
}

output "image_id" {
  value = aws_launch_template.nat.image_id
}

output "latest_version" {
  value = aws_launch_template.nat.latest_version
}
