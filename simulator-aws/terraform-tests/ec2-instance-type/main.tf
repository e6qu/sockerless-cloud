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
  }
}

data "aws_ec2_instance_types" "graviton_2_vcpus_512_mib" {
  filter {
    name   = "vcpu-info.default-vcpus"
    values = ["2"]
  }
  filter {
    name   = "memory-info.size-in-mib"
    values = ["512"]
  }
  filter {
    name   = "processor-info.supported-architecture"
    values = ["arm64"]
  }
}

data "aws_ec2_instance_types" "m5_96_vcpus" {
  filter {
    name   = "instance-type"
    values = ["m5.*"]
  }
  filter {
    name   = "vcpu-info.default-vcpus"
    values = ["96"]
  }
}

output "graviton_2_vcpus_512_mib" {
  value = join(",", sort(data.aws_ec2_instance_types.graviton_2_vcpus_512_mib.instance_types))
}

output "m5_96_vcpus" {
  value = join(",", sort(data.aws_ec2_instance_types.m5_96_vcpus.instance_types))
}

# A Dedicated Host reports its family's cores and vCPUs as the AWS Price List
# states them.
resource "aws_ec2_host" "m5" {
  availability_zone = "us-east-1a"
  instance_type     = "m5.large"
}

data "aws_ec2_host" "m5" {
  host_id = aws_ec2_host.m5.id
}

output "host_cores_vcpus" {
  value = "${data.aws_ec2_host.m5.cores}/${data.aws_ec2_host.m5.total_vcpus}"
}
