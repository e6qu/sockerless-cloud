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
  cidr_block = "10.78.0.0/16"
}

resource "aws_subnet" "main" {
  vpc_id     = aws_vpc.main.id
  cidr_block = "10.78.1.0/24"
}

# The fck-nat floating ENI: a standalone interface with source/dest check
# disabled. Create + ModifyNetworkInterfaceAttribute (source_dest_check) +
# read on apply; Delete on destroy. (Attach/Detach are covered by the SDK
# test, which attaches to a running instance.)
resource "aws_network_interface" "nat" {
  subnet_id         = aws_subnet.main.id
  description       = "fck-nat floating ENI"
  source_dest_check = false
}

output "eni_id" {
  value = aws_network_interface.nat.id
}

output "source_dest_check" {
  value = aws_network_interface.nat.source_dest_check
}
