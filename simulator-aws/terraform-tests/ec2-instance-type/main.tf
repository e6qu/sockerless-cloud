terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.68.0"
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

# Each filter selects over one published fact: vCPUs, memory in MiB, and
# architecture.
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
