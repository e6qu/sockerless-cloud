terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.70.0"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "second_zone" {
  type    = bool
  default = true
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
  cidr_block = "10.87.0.0/16"
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_subnet" "zone" {
  count             = 2
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.87.${count.index + 1}.0/24"
  availability_zone = ["us-east-1a", "us-east-1b"][count.index]
}

resource "aws_eip" "nat" {
  count  = 3
  domain = "vpc"
}

# Automatic mode: the gateway covers the zones the VPC's subnets use.
resource "aws_nat_gateway" "auto" {
  vpc_id            = aws_vpc.main.id
  availability_mode = "regional"

  depends_on = [aws_internet_gateway.main, aws_subnet.zone]
}

# Manual mode: second_zone = false moves to a second address in us-east-1a
# and drops us-east-1b, which the provider applies through
# AssociateNatGatewayAddress and DisassociateNatGatewayAddress.
resource "aws_nat_gateway" "manual" {
  vpc_id            = aws_vpc.main.id
  availability_mode = "regional"

  availability_zone_address {
    availability_zone = "us-east-1a"
    allocation_ids    = var.second_zone ? [aws_eip.nat[0].id] : [aws_eip.nat[0].id, aws_eip.nat[2].id]
  }

  dynamic "availability_zone_address" {
    for_each = var.second_zone ? [1] : []
    content {
      availability_zone = "us-east-1b"
      allocation_ids    = [aws_eip.nat[1].id]
    }
  }

  depends_on = [aws_internet_gateway.main]
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.manual.id
  }
}

output "auto_id" {
  value = aws_nat_gateway.auto.id
}

output "auto_mode" {
  value = aws_nat_gateway.auto.availability_mode
}

output "auto_provision_zones" {
  value = aws_nat_gateway.auto.auto_provision_zones
}

output "auto_scaling_ips" {
  value = aws_nat_gateway.auto.auto_scaling_ips
}

output "auto_route_table_id" {
  value = aws_nat_gateway.auto.route_table_id
}

output "auto_zones" {
  value = join(",", sort([for a in aws_nat_gateway.auto.regional_nat_gateway_address : a.availability_zone]))
}

output "manual_id" {
  value = aws_nat_gateway.manual.id
}

output "manual_auto_provision_zones" {
  value = aws_nat_gateway.manual.auto_provision_zones
}

output "manual_addresses" {
  value = join(",", sort([for a in aws_nat_gateway.manual.regional_nat_gateway_address : "${a.availability_zone}=${a.allocation_id}"]))
}

output "eips" {
  value = join(",", aws_eip.nat[*].id)
}

output "route_nat_gateway_id" {
  value = one(aws_route_table.private.route[*].nat_gateway_id)
}
