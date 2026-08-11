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
    ec2      = var.endpoint
    ecs      = var.endpoint
    elbv2    = var.endpoint
    logs     = var.endpoint
    dynamodb = var.endpoint
    ecr      = var.endpoint
    acm      = var.endpoint
    route53  = var.endpoint
  }
}

resource "aws_vpc" "main" {
  cidr_block                           = "10.91.0.0/16"
  instance_tenancy                     = "default"
  enable_network_address_usage_metrics = true
}

resource "aws_subnet" "a" {
  vpc_id                                      = aws_vpc.main.id
  cidr_block                                  = "10.91.1.0/24"
  availability_zone                           = "us-east-1a"
  map_public_ip_on_launch                     = true
  private_dns_hostname_type_on_launch         = "resource-name"
  enable_resource_name_dns_a_record_on_launch = true
}

resource "aws_subnet" "b" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.91.2.0/24"
  availability_zone = "us-east-1b"
}

resource "aws_security_group" "alb" {
  name   = "fidelity-alb"
  vpc_id = aws_vpc.main.id
}

resource "aws_security_group" "tasks" {
  name   = "fidelity-tasks"
  vpc_id = aws_vpc.main.id
}

# All-traffic egress: ip_protocol="-1" carries no ports. The provider reads
# from_port/to_port back as null; a sim returning 0 drifts "0 -> null".
resource "aws_vpc_security_group_egress_rule" "tasks_all" {
  security_group_id = aws_security_group.tasks.id
  ip_protocol       = "-1"
  cidr_ipv4         = "0.0.0.0/0"
}

# Standalone IPv6 egress rule: Authorize* with an IPv6 range must produce a
# SecurityGroupRule row (carrying cidr_ipv6), or this resource drifts/recreates
# every plan — it Reads via DescribeSecurityGroupRules by rule id.
resource "aws_vpc_security_group_egress_rule" "tasks_all_ipv6" {
  security_group_id = aws_security_group.tasks.id
  ip_protocol       = "-1"
  cidr_ipv6         = "::/0"
}

# Referencing rule: referenced_security_group_id must read back as the bare
# sg-id (no account prefix), or it drifts every plan.
resource "aws_vpc_security_group_ingress_rule" "tasks_from_alb" {
  security_group_id            = aws_security_group.tasks.id
  ip_protocol                  = "tcp"
  from_port                    = 3000
  to_port                      = 3000
  referenced_security_group_id = aws_security_group.alb.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

# connectivity_type is ForceNew; it must round-trip through DescribeNatGateways
# or the provider plans destroy+create every time.
resource "aws_nat_gateway" "this" {
  allocation_id     = aws_eip.nat.id
  subnet_id         = aws_subnet.a.id
  connectivity_type = "public"
}

resource "aws_cloudwatch_log_group" "this" {
  name = "/fidelity/app"
  tags = {
    Name = "fidelity"
    env  = "ci"
  }
}

resource "aws_dynamodb_table" "this" {
  name         = "fidelity-table"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"

  attribute {
    name = "PK"
    type = "S"
  }

  tags = {
    Name = "fidelity"
  }
}

resource "aws_ecr_repository" "this" {
  name = "fidelity-repo"
  tags = {
    Name = "fidelity"
  }
}

# Task-def tags are read by the provider via DescribeTaskDefinition
# --include TAGS (response top-level tags); a simple container def keeps the
# ForceNew containerDefinitions hash stable so only the tag path is exercised.
# runtime_platform + ephemeral_storage are top-level ForceNew knobs the provider
# reads back — each was dropped on register, so they drifted into a new revision
# every plan. Keeping the container def minimal isolates them from container-def
# normalization noise.
resource "aws_ecs_task_definition" "this" {
  family                   = "fidelity-control-plane"
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  cpu                      = "256"
  memory                   = "512"
  container_definitions = jsonencode([{
    name      = "app"
    image     = "nginx"
    essential = true
  }])
  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }
  ephemeral_storage {
    size_in_gib = 30
  }
  tags = {
    Name            = "fidelity"
    "edd:component" = "ecs-dev-desktop"
  }
}

resource "aws_acm_certificate" "this" {
  domain_name       = "fidelity.example.test"
  validation_method = "DNS"
}

# ALB created without minimum_load_balancer_capacity: DescribeCapacityReservation
# must omit the attribute (not report capacity_units=0, which drifts to null).
resource "aws_lb" "this" {
  name               = "fidelity-alb"
  internal           = true
  load_balancer_type = "application"
  subnets            = [aws_subnet.a.id, aws_subnet.b.id]
  security_groups    = [aws_security_group.alb.id]
}

resource "aws_lb_target_group" "this" {
  name             = "fidelity-tg"
  port             = 80
  protocol         = "HTTP"
  protocol_version = "HTTP1"
  vpc_id           = aws_vpc.main.id

  # Matcher was hardcoded to 200 in DescribeTargetGroups, so a non-default
  # health_check.matcher drifted every plan.
  health_check {
    path    = "/healthz"
    matcher = "200-299"
  }
}

# HTTPS listener with an ACM cert + ssl_policy: both must round-trip through
# DescribeListeners (cert ARN + SslPolicy) or the listener drifts every plan.
resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  certificate_arn   = aws_acm_certificate.this.arn
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}

# Second target group for the weighted forward rule below.
resource "aws_lb_target_group" "secondary" {
  name     = "fidelity-tg2"
  port     = 8080
  protocol = "HTTP"
  vpc_id   = aws_vpc.main.id
}

# TCP (NLB) target group with an explicit TCP health_check block. A TCP health
# check carries NO Matcher and NO HealthCheckPath (both are HTTP-only). If
# DescribeTargetGroups emits either for a TCP health check, the provider reads
# back an attribute it never set and plans a perpetual health_check.path /
# matcher drift (issues #685 and #688).
resource "aws_lb_target_group" "tcp" {
  name     = "fidelity-tg-tcp"
  port     = 443
  protocol = "TCP"
  vpc_id   = aws_vpc.main.id

  health_check {
    protocol            = "TCP"
    healthy_threshold   = 3
    unhealthy_threshold = 3
    interval            = 30
  }
}

# SNI certificate attached via aws_lb_listener_certificate: read back through
# DescribeListenerCertificates (IsDefault=false) or it drifts/recreates.
resource "aws_acm_certificate" "sni" {
  domain_name       = "sni.fidelity.example.test"
  validation_method = "DNS"
}

resource "aws_lb_listener_certificate" "sni" {
  listener_arn    = aws_lb_listener.https.arn
  certificate_arn = aws_acm_certificate.sni.arn
}

# Listener rule exercising authenticate-oidc (the IAP/Pomerium proxy shape) +
# weighted forward with stickiness. Each config block must round-trip through
# DescribeRules or the rule drifts every plan. client_secret is write-only (the
# provider keeps it in state; ELBv2 never returns it).
resource "aws_lb_listener_rule" "oidc" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 100

  action {
    type = "authenticate-oidc"
    authenticate_oidc {
      issuer                 = "https://idp.fidelity.example.test"
      authorization_endpoint = "https://idp.fidelity.example.test/authorize"
      token_endpoint         = "https://idp.fidelity.example.test/token"
      user_info_endpoint     = "https://idp.fidelity.example.test/userinfo"
      client_id              = "fidelity-client"
      client_secret          = "fidelity-secret"
      scope                  = "openid email"
      session_cookie_name    = "AWSELBAuthSessionCookie"
      session_timeout        = 3600
      authentication_request_extra_params = {
        prompt = "login"
      }
    }
  }

  action {
    type = "forward"
    forward {
      target_group {
        arn    = aws_lb_target_group.this.arn
        weight = 70
      }
      target_group {
        arn    = aws_lb_target_group.secondary.arn
        weight = 30
      }
      stickiness {
        enabled  = true
        duration = 600
      }
    }
  }

  condition {
    path_pattern {
      values = ["/api/*"]
    }
  }
}

# Network Load Balancer with a TCP listener, plus a Route53 alias record that
# targets it. DescribeLoadBalancers must return a STABLE, AWS-shaped hostname for
# dns_name (never the data-plane proxy host:port): an unstable or non-hostname
# dns_name drifts the NLB every plan, and an alias { name = <host:port> } is
# invalid for aws_route53_record (an alias target must be a hostname + zone id).
resource "aws_lb" "nlb" {
  name               = "fidelity-nlb"
  internal           = true
  load_balancer_type = "network"
  subnets            = [aws_subnet.a.id]
}

resource "aws_lb_target_group" "nlb_tcp" {
  name        = "fidelity-nlb-tg"
  port        = 2223
  protocol    = "TCP"
  target_type = "ip"
  vpc_id      = aws_vpc.main.id
}

resource "aws_lb_listener" "nlb_tcp" {
  load_balancer_arn = aws_lb.nlb.arn
  port              = 2223
  protocol          = "TCP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.nlb_tcp.arn
  }
}

resource "aws_route53_zone" "internal" {
  name = "fidelity.internal"
}

# Alias to the NLB: requires a stable hostname (aws_lb.nlb.dns_name) and the
# NLB's canonical hosted zone id (aws_lb.nlb.zone_id). A host:port dns_name would
# make this resource invalid; an unstable dns_name would drift it every plan.
resource "aws_route53_record" "nlb_alias" {
  zone_id = aws_route53_zone.internal.zone_id
  name    = "nlb.fidelity.internal"
  type    = "A"

  alias {
    name                   = aws_lb.nlb.dns_name
    zone_id                = aws_lb.nlb.zone_id
    evaluate_target_health = true
  }
}

# Inline-egress SG with both IPv4 + IPv6: DescribeSecurityGroups must echo
# Ipv6Ranges or ipv6_cidr_blocks drifts every plan.
resource "aws_security_group" "dualstack" {
  name   = "fidelity-dualstack"
  vpc_id = aws_vpc.main.id

  egress {
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
}

# NAT-instance launch template + instance: launch_template is ForceNew and the
# provider reads it from the aws:ec2launchtemplate:* system tags; absent tags
# force destroy+create every plan.
resource "aws_launch_template" "nat" {
  name          = "fidelity-nat-lt"
  image_id      = "ami-12345678"
  instance_type = "t4g.nano"
}

resource "aws_instance" "nat" {
  subnet_id = aws_subnet.a.id
  # NAT-instance knobs the provider reads back — each was dropped/hardcoded and
  # drifted every plan: source_dest_check (set via ModifyInstanceAttribute),
  # monitoring, ebs_optimized, and metadata_options.
  source_dest_check = false
  monitoring        = false
  ebs_optimized     = false
  metadata_options {
    http_tokens                 = "required"
    http_endpoint               = "enabled"
    http_put_response_hop_limit = 2
  }
  launch_template {
    id      = aws_launch_template.nat.id
    version = "$Latest"
  }
}

# Route targeting a network interface: DescribeRouteTables must echo
# NetworkInterfaceId or aws_route drifts every plan.
resource "aws_network_interface" "nat" {
  subnet_id = aws_subnet.a.id
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id
}

resource "aws_route" "via_eni" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  network_interface_id   = aws_network_interface.nat.id
}

# The main route table AWS auto-creates per VPC — read via the association.main
# filter (exercises the main association + associationState round-trip).
data "aws_route_table" "main" {
  vpc_id = aws_vpc.main.id
  filter {
    name   = "association.main"
    values = ["true"]
  }
}

# Key pair: DescribeKeyPairs was always empty, so aws_key_pair drifted/recreated.
resource "aws_key_pair" "deployer" {
  key_name   = "fidelity-deployer"
  public_key = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQexample fidelity@test"
}

# Launch template with credit_specification + instance_market_options: both were
# dropped from the LT round-trip, so they drifted every plan.
resource "aws_launch_template" "spot" {
  name          = "fidelity-spot-lt"
  image_id      = "ami-12345678"
  instance_type = "t3.micro"
  credit_specification {
    cpu_credits = "unlimited"
  }
  instance_market_options {
    market_type = "spot"
    spot_options {
      max_price                      = "0.05"
      spot_instance_type             = "one-time"
      instance_interruption_behavior = "terminate"
    }
  }
}

# data.aws_ami lookup: DescribeImages ignored Filters, so the name/architecture
# filter couldn't actually select. The sim now resolves a deterministic image.
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-minimal"]
  }
  filter {
    name   = "architecture"
    values = ["x86_64"]
  }
}

# EBS volume with non-default performance + encryption: iops/throughput/kms/
# encrypted were dropped from DescribeVolumes, so each drifted every plan.
resource "aws_ebs_volume" "data" {
  availability_zone = "us-east-1a"
  size              = 20
  type              = "gp3"
  iops              = 3000
  throughput        = 125
  encrypted         = true
}

output "deployer_key_id" {
  value = aws_key_pair.deployer.key_pair_id
}

output "resolved_ami_id" {
  value = data.aws_ami.al2023.id
}

output "data_volume_id" {
  value = aws_ebs_volume.data.id
}

output "nat_gateway_id" {
  value = aws_nat_gateway.this.id
}

output "nat_instance_id" {
  value = aws_instance.nat.id
}

output "listener_certificate_arn" {
  value = aws_lb_listener.https.certificate_arn
}

output "nlb_dns_name" {
  value = aws_lb.nlb.dns_name
}

output "nlb_zone_id" {
  value = aws_lb.nlb.zone_id
}
