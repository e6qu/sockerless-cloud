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

variable "probe" {
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
    ecs = var.endpoint
  }
}

resource "aws_vpc" "main" {
  cidr_block = "10.66.0.0/16"
}

resource "aws_subnet" "public" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.66.1.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_subnet" "private" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.66.2.0/24"
  availability_zone = "us-east-1a"
}

# No aws_route_table_association names this subnet: the main route table
# governs it.
resource "aws_subnet" "implicit" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.66.3.0/24"
  availability_zone = "us-east-1a"
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route" "public_default" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.main.id
}

resource "aws_route_table_association" "public" {
  route_table_id = aws_route_table.public.id
  subnet_id      = aws_subnet.public.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_nat_gateway" "main" {
  subnet_id     = aws_subnet.public.id
  allocation_id = aws_eip.nat.id
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route" "private_default" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.main.id
}

# Created after the NAT route, so the route's translation has to pick the
# subnet up when the association lands.
resource "aws_route_table_association" "private" {
  route_table_id = aws_route_table.private.id
  subnet_id      = aws_subnet.private.id
  depends_on     = [aws_route.private_default]
}

resource "aws_route" "main_default" {
  route_table_id         = aws_vpc.main.main_route_table_id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.main.id
}

resource "aws_ecs_cluster" "main" {
  name = "tf-nat-associations"
}

resource "aws_ecs_task_definition" "reporter" {
  for_each                 = toset(["explicit", "implicit"])
  family                   = "tf-nat-source-${each.key}"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = "256"
  memory                   = "512"
  container_definitions = jsonencode([{
    name        = "app"
    image       = "public.ecr.aws/docker/library/busybox:latest"
    essential   = true
    stopTimeout = 2
    entryPoint  = ["sh", "-c"]
    command     = ["wget -T 5 -q -O- ${var.probe}/${each.key}; trap 'exit 143' TERM; sleep 3600 & wait"]
  }])
}

resource "aws_ecs_service" "reporter" {
  for_each        = { explicit = aws_subnet.private.id, implicit = aws_subnet.implicit.id }
  name            = "tf-nat-source-${each.key}"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.reporter[each.key].arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets = [each.value]
  }

  depends_on = [aws_route_table_association.private, aws_route.main_default]
}

output "nat_public_ip" {
  value = aws_eip.nat.public_ip
}
