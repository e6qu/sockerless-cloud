terraform {
  backend "local" {}

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
    }
  }
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true

  # Force path-style addressing for S3 so the bucket name appears in
  # the URL path (https://<endpoint>/<bucket>/...) instead of the
  # subdomain (https://<bucket>.<endpoint>/...) which can't be
  # DNS-resolved against a localhost endpoint. Same option real users
  # set against MinIO / LocalStack / any IP-addressed S3 alternative.
  s3_use_path_style = true

  endpoints {
    ec2              = var.endpoint
    ecs              = var.endpoint
    sts              = var.endpoint
    ecr              = var.endpoint
    apigateway       = var.endpoint
    apigatewayv2     = var.endpoint
    servicediscovery = var.endpoint
    cloudfront       = var.endpoint
    acm              = var.endpoint
    acmpca           = var.endpoint
    route53          = var.endpoint
    wafv2            = var.endpoint
    amplify          = var.endpoint
    iam              = var.endpoint
    s3               = var.endpoint
    dynamodb         = var.endpoint
    efs              = var.endpoint
    events           = var.endpoint
    firehose         = var.endpoint
    kinesis          = var.endpoint
    kms              = var.endpoint
    lambda           = var.endpoint
    cloudwatchlogs   = var.endpoint
    cloudwatch       = var.endpoint
    elbv2            = var.endpoint
    autoscaling      = var.endpoint
    cloudtrail       = var.endpoint
    secretsmanager   = var.endpoint
    sqs              = var.endpoint
    ssm              = var.endpoint
    sfn              = var.endpoint
    codebuild        = var.endpoint
    glue             = var.endpoint
    batch            = var.endpoint
    appautoscaling   = var.endpoint
    scheduler        = var.endpoint
    budgets          = var.endpoint
    organizations    = var.endpoint
  }
}

data "aws_organizations_organization" "current" {}

resource "aws_kinesis_stream" "tf_stream" {
  name             = "tf-kinesis-stream"
  shard_count      = 1
  retention_period = 24

  stream_mode_details {
    stream_mode = "PROVISIONED"
  }

  tags = {
    env = "terraform"
  }
}

data "aws_caller_identity" "current" {}

resource "aws_vpc" "tf_ec2_vpc" {
  cidr_block = "10.251.0.0/16"
}

resource "aws_vpc_encryption_control" "tf_ec2_vpc" {
  vpc_id = aws_vpc.tf_ec2_vpc.id
  mode   = "monitor"

  tags = {
    env = "terraform"
  }
}

# data.aws_vpc by vpc-id filter — the fck-nat pattern; reads back the VPC's
# CIDR from cidr_block_associations. With the broken filter this
# returned the wrong VPC / an empty CIDR.
data "aws_vpc" "by_filter" {
  filter {
    name   = "vpc-id"
    values = [aws_vpc.tf_ec2_vpc.id]
  }
}

resource "aws_subnet" "tf_ec2_subnet" {
  vpc_id     = aws_vpc.tf_ec2_vpc.id
  cidr_block = "10.251.1.0/24"
}

resource "aws_subnet" "tf_elbv2_subnet" {
  vpc_id            = aws_vpc.tf_ec2_vpc.id
  cidr_block        = "10.251.2.0/24"
  availability_zone = "us-east-1b"
}

resource "aws_security_group" "tf_ec2_sg" {
  name        = "tf-ec2-sg"
  description = "terraform ec2 instance coverage"
  vpc_id      = aws_vpc.tf_ec2_vpc.id
}

resource "aws_efs_file_system" "tf_efs" {
  creation_token   = "tf-efs-file-system"
  performance_mode = "generalPurpose"
  throughput_mode  = "bursting"
}

resource "aws_efs_mount_target" "tf_efs_mount" {
  file_system_id  = aws_efs_file_system.tf_efs.id
  subnet_id       = aws_subnet.tf_ec2_subnet.id
  security_groups = [aws_security_group.tf_ec2_sg.id]
}

resource "aws_efs_access_point" "tf_efs_ap" {
  file_system_id = aws_efs_file_system.tf_efs.id

  posix_user {
    uid = 1000
    gid = 1000
  }

  root_directory {
    path = "/terraform"
    creation_info {
      owner_uid   = 1000
      owner_gid   = 1000
      permissions = "755"
    }
  }
}

resource "aws_eip" "tf_nat_eip" {
  domain = "vpc"
}

resource "aws_nat_gateway" "tf_nat" {
  allocation_id = aws_eip.tf_nat_eip.id
  subnet_id     = aws_subnet.tf_ec2_subnet.id
}

resource "aws_route_table" "tf_nat_rt" {
  vpc_id = aws_vpc.tf_ec2_vpc.id

  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.tf_nat.id
  }
}

resource "aws_lb" "tf_alb" {
  name               = "tf-alb"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.tf_ec2_sg.id]
  subnets            = [aws_subnet.tf_ec2_subnet.id, aws_subnet.tf_elbv2_subnet.id]

  tags = {
    env = "terraform"
  }
}

resource "aws_lb_target_group" "tf_alb_tg" {
  name        = "tf-alb-tg"
  port        = 80
  protocol    = "HTTP"
  vpc_id      = aws_vpc.tf_ec2_vpc.id
  target_type = "ip"

  health_check {
    path                = "/healthz"
    protocol            = "HTTP"
    interval            = 10
    timeout             = 5
    healthy_threshold   = 3
    unhealthy_threshold = 2
  }

  tags = {
    env = "terraform"
  }
}

resource "aws_lb_listener" "tf_alb_listener" {
  load_balancer_arn = aws_lb.tf_alb.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.tf_alb_tg.arn
  }
}

# Listener rule: host-header routing — the IAP-proxy ALB shape.
# Exercises CreateRule + DescribeRules (read-back) on apply, DeleteRule on
# destroy.
resource "aws_lb_listener_rule" "tf_alb_rule" {
  listener_arn = aws_lb_listener.tf_alb_listener.arn
  priority     = 100

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.tf_alb_tg.arn
  }

  condition {
    host_header {
      values = ["app.example.com"]
    }
  }
}

resource "aws_instance" "tf_vm" {
  ami           = "ami-tf1234"
  instance_type = "t3.micro"
  subnet_id     = aws_subnet.tf_ec2_subnet.id
  vpc_security_group_ids = [
    aws_security_group.tf_ec2_sg.id,
  ]

  tags = {
    Name = "tf-ec2-instance"
  }
}

resource "aws_ebs_volume" "tf_ebs" {
  availability_zone = "us-east-1a"
  size              = 1
  type              = "gp3"

  tags = {
    env = "terraform"
  }
}

resource "aws_volume_attachment" "tf_ebs_attachment" {
  device_name = "/dev/sdf"
  volume_id   = aws_ebs_volume.tf_ebs.id
  instance_id = aws_instance.tf_vm.id
}

resource "aws_ebs_snapshot" "tf_ebs_snapshot" {
  volume_id   = aws_ebs_volume.tf_ebs.id
  description = "terraform ebs snapshot coverage"

  tags = {
    env = "terraform"
  }

  depends_on = [aws_volume_attachment.tf_ebs_attachment]
}

resource "aws_cloudwatch_metric_alarm" "tf_alarm" {
  alarm_name          = "tf-alarm"
  alarm_description   = "terraform metric alarm coverage"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "Errors"
  namespace           = "Custom/TF"
  period              = 60
  statistic           = "Sum"
  threshold           = 0
  treat_missing_data  = "notBreaching"

  tags = {
    env = "terraform"
  }
}

resource "aws_cloudwatch_metric_alarm" "tf_alarm_p99" {
  alarm_name          = "tf-alarm-p99"
  alarm_description   = "terraform percentile alarm coverage"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "Latency"
  namespace           = "Custom/TF"
  period              = 300
  extended_statistic  = "p99"
  threshold           = 120000
  treat_missing_data  = "notBreaching"
}

resource "aws_cloudwatch_dashboard" "tf_dashboard" {
  dashboard_name = "tf-dash"
  dashboard_body = jsonencode({
    widgets = [{ type = "text", x = 0, y = 0, width = 6, height = 2, properties = { markdown = "hello" } }]
  })
}

resource "aws_ebs_snapshot_copy" "tf_ebs_snapshot_copy" {
  source_snapshot_id = aws_ebs_snapshot.tf_ebs_snapshot.id
  source_region      = "us-east-1"
  description        = "terraform ebs snapshot copy coverage"

  tags = {
    env = "terraform"
  }
}

resource "aws_ebs_volume" "tf_ebs_restored" {
  availability_zone = "us-east-1a"
  snapshot_id       = aws_ebs_snapshot.tf_ebs_snapshot.id
  type              = "gp3"

  tags = {
    env = "terraform"
  }
}

resource "aws_launch_configuration" "tf_asg_lc" {
  name_prefix   = "tf-asg-lc-"
  image_id      = "ami-tf-asg"
  instance_type = "t3.micro"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_autoscaling_group" "tf_asg" {
  name                 = "tf-asg"
  launch_configuration = aws_launch_configuration.tf_asg_lc.name
  min_size             = 1
  max_size             = 2
  desired_capacity     = 1
  vpc_zone_identifier  = [aws_subnet.tf_ec2_subnet.id]

  tag {
    key                 = "env"
    value               = "terraform"
    propagate_at_launch = true
  }
}

resource "aws_ecs_cluster" "main" {
  name = "tf-test-cluster"

  # containerInsights setting + execute-command KMS config — read back via
  # DescribeClusters --include SETTINGS CONFIGURATIONS.
  setting {
    name  = "containerInsights"
    value = "enabled"
  }

  configuration {
    execute_command_configuration {
      kms_key_id = aws_kms_key.tf_kms.arn
      logging    = "DEFAULT"
    }
  }
}

# Cluster capacity providers — exercises PutClusterCapacityProviders; the
# provider reads them back via DescribeClusters.
resource "aws_ecs_cluster_capacity_providers" "main" {
  cluster_name       = aws_ecs_cluster.main.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
    base              = 1
  }
}

# Fargate task definition + long-lived service — exercises the ECS Service
# family (CreateService/DescribeServices/UpdateService/DeleteService). The
# provider waits for the service to reach ACTIVE with runningCount==desiredCount.
resource "aws_ecs_task_definition" "tf_runner" {
  family                   = "tf-runner"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([{
    name      = "app"
    image     = "sockerless-container-command:test"
    command   = ["hold"]
    essential = true
  }])
}

# A bridge-network-mode task definition — the mode Amazon ECS applies to a task
# that shares its container instance's networking rather than receiving its own
# elastic network interface. It is EC2-compatible only: FARGATE requires awsvpc.
resource "aws_ecs_task_definition" "tf_runner_bridge" {
  family       = "tf-runner-bridge"
  network_mode = "bridge"

  container_definitions = jsonencode([{
    name      = "app"
    image     = "public.ecr.aws/docker/library/alpine:latest"
    essential = true
  }])
}

resource "aws_ecs_service" "tf_runner" {
  name            = "tf-runner-svc"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.tf_runner.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = [aws_subnet.tf_ec2_subnet.id]
    security_groups = [aws_security_group.tf_ec2_sg.id]
  }

  service_registries {
    registry_arn = aws_service_discovery_service.tf_svc.arn
  }

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  alarms {
    alarm_names = [aws_cloudwatch_metric_alarm.tf_alarm.alarm_name]
    enable      = true
    rollback    = true
  }
}

# Application Auto Scaling — autoscale the ECS service that backs the runner.
# Exercises RegisterScalableTarget + PutScalingPolicy (AnyScaleFrontendService),
# distinct from EC2 Auto Scaling above.
resource "aws_appautoscaling_target" "ecs" {
  service_namespace  = "ecs"
  resource_id        = "service/${aws_ecs_cluster.main.name}/tf-runner-svc"
  scalable_dimension = "ecs:service:DesiredCount"
  min_capacity       = 1
  max_capacity       = 4
}

resource "aws_appautoscaling_policy" "ecs_cpu" {
  name               = "tf-runner-cpu-tt"
  policy_type        = "TargetTrackingScaling"
  service_namespace  = aws_appautoscaling_target.ecs.service_namespace
  resource_id        = aws_appautoscaling_target.ecs.resource_id
  scalable_dimension = aws_appautoscaling_target.ecs.scalable_dimension

  target_tracking_scaling_policy_configuration {
    target_value = 60
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}

# Exercise the pull-through-cache APIs added to the simulator in
# Terraform's aws_ecr_pull_through_cache_rule resource
# wraps the same CreatePullThroughCacheRule / DescribePullThroughCacheRules
# / DeletePullThroughCacheRule endpoints the SDK + CLI tests cover.
resource "aws_ecr_pull_through_cache_rule" "docker_hub" {
  ecr_repository_prefix = "tf-docker-hub"
  upstream_registry_url = "registry-1.docker.io"
}

# aws_ecr_repository — exercises the repository config the provider reads back
# on refresh (image_tag_mutability / image_scanning_configuration /
# encryption_configuration). Without the sim echoing these, the provider drifts.
resource "aws_ecr_repository" "tf_repo" {
  name                 = "tf-runner-repo"
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Exercise the Cloud Map namespace + service APIs that the sim fix
# depends on. The namespace and service are Cloud Map control-plane
# resources; the simulator creates a Docker user-defined network later,
# only when an ECS task registration needs private DNS at runtime.
resource "aws_service_discovery_private_dns_namespace" "tf_svc_net" {
  name        = "tf-svc-net.local"
  vpc         = "vpc-sim"
  description = "terraform private namespace"

  tags = {
    env     = "terraform"
    purpose = "service-discovery"
  }
}

resource "aws_service_discovery_service" "tf_svc" {
  name = "tf-svc"

  dns_config {
    namespace_id   = aws_service_discovery_private_dns_namespace.tf_svc_net.id
    routing_policy = "MULTIVALUE"

    dns_records {
      ttl  = 10
      type = "A"
    }
  }

  tags = {
    tier = "web"
  }
}

# A Cloud Map instance registered in the private-DNS service: the provider
# drives RegisterInstance on create, GetInstance on read and DeregisterInstance
# on destroy.
resource "aws_service_discovery_instance" "tf_svc_instance" {
  instance_id = "tf-instance"
  service_id  = aws_service_discovery_service.tf_svc.id

  attributes = {
    AWS_INSTANCE_IPV4 = "10.20.30.40"
  }
}

# An HTTP namespace — discovery through the DiscoverInstances API rather than
# DNS records — carrying a service whose health is caller-reported.
resource "aws_service_discovery_http_namespace" "tf_http_ns" {
  name        = "tf-http-ns"
  description = "terraform http namespace"

  tags = {
    env = "terraform"
  }
}

resource "aws_service_discovery_service" "tf_http_svc" {
  name         = "tf-http-svc"
  namespace_id = aws_service_discovery_http_namespace.tf_http_ns.id

  # AWS Cloud Map deprecated this member but always returns the fixed value 1.
  health_check_custom_config {
    failure_threshold = 1
  }
}

# The Cloud Map data sources look their resource up through the list APIs:
# ListNamespaces filtered by TYPE / NAME (DNS namespace), by HTTP_NAME (HTTP
# namespace) and ListServices filtered by NAMESPACE_ID.
data "aws_service_discovery_dns_namespace" "tf_svc_net_lookup" {
  name = aws_service_discovery_private_dns_namespace.tf_svc_net.name
  type = "DNS_PRIVATE"
}

data "aws_service_discovery_http_namespace" "tf_http_ns_lookup" {
  name = aws_service_discovery_http_namespace.tf_http_ns.name
}

data "aws_service_discovery_service" "tf_svc_lookup" {
  name         = aws_service_discovery_service.tf_svc.name
  namespace_id = aws_service_discovery_private_dns_namespace.tf_svc_net.id
}

# Exercise the CloudFront REST + XML wire on the simulator.
# Hits POST /2020-05-31/distribution + GET /2020-05-31/distribution/{id} +
# PUT /2020-05-31/distribution/{id}/config (Terraform sets Enabled=false
# automatically before destroy because the simulator enforces the real
# AWS "DistributionNotDisabled" precondition).
resource "aws_cloudfront_origin_access_control" "tf_oac" {
  name                              = "tf-oac"
  description                       = "tf-test"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

resource "aws_cloudfront_cache_policy" "tf_cp" {
  name        = "tf-cache-policy"
  comment     = "tf-test cache policy"
  default_ttl = 86400
  max_ttl     = 31536000
  min_ttl     = 1

  parameters_in_cache_key_and_forwarded_to_origin {
    enable_accept_encoding_gzip   = true
    enable_accept_encoding_brotli = true

    headers_config {
      header_behavior = "none"
    }
    cookies_config {
      cookie_behavior = "none"
    }
    query_strings_config {
      query_string_behavior = "none"
    }
  }
}

resource "aws_cloudfront_origin_request_policy" "tf_orp" {
  name    = "tf-origin-request-policy"
  comment = "tf-test origin request policy"

  headers_config {
    header_behavior = "none"
  }
  cookies_config {
    cookie_behavior = "none"
  }
  query_strings_config {
    query_string_behavior = "none"
  }
}

resource "aws_iam_service_linked_role" "tf_slr_cloudfront" {
  aws_service_name = "cloudfront.amazonaws.com"
  custom_suffix    = "tftest"
  description      = "tf-test CloudFront SLR"
}

resource "aws_iam_openid_connect_provider" "tf_oidc" {
  url             = "https://oidc.eks.us-east-1.amazonaws.com/id/TFTESTOIDC"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["9e99a48a9960b14926bb7f3b02e22da2b0ab7280"]
}

resource "aws_amplify_app" "tf_amplify" {
  name        = "tf-amplify"
  description = "tf-test Amplify app"
  platform    = "WEB"

  environment_variables = {
    ENV = "test"
  }

  enable_branch_auto_build = true
  enable_basic_auth        = false
}

resource "aws_amplify_branch" "tf_amplify_main" {
  app_id      = aws_amplify_app.tf_amplify.id
  branch_name = "main"
  framework   = "Next.js - SSR"
  stage       = "PRODUCTION"
}

resource "aws_amplify_webhook" "tf_amplify_hook" {
  app_id      = aws_amplify_app.tf_amplify.id
  branch_name = aws_amplify_branch.tf_amplify_main.branch_name
  description = "tf-test webhook"
}

resource "aws_amplify_backend_environment" "tf_amplify_be" {
  app_id           = aws_amplify_app.tf_amplify.id
  environment_name = "staging"
  stack_name       = "amplify-staging-stack"
}

# Domain verification is real: the association stays PENDING_VERIFICATION
# until its certificate-verification CNAME exists in a Route 53 hosted zone
# covering the domain. The association is created without waiting (the
# record can only be derived from its exported
# certificate_verification_dns_record), then the record flips it AVAILABLE
# on the next read — the standard real-world terraform shape for Amplify
# custom domains.
resource "aws_route53_zone" "tf_amplify_zone" {
  name = "tf-amplify.example.com"
}

resource "aws_amplify_domain_association" "tf_amplify_domain" {
  app_id                = aws_amplify_app.tf_amplify.id
  domain_name           = "tf-amplify.example.com"
  wait_for_verification = false

  sub_domain {
    branch_name = aws_amplify_branch.tf_amplify_main.branch_name
    prefix      = "www"
  }

  sub_domain {
    branch_name = aws_amplify_branch.tf_amplify_main.branch_name
    prefix      = ""
  }
}

resource "aws_route53_record" "tf_amplify_cert_verification" {
  zone_id = aws_route53_zone.tf_amplify_zone.zone_id
  name    = split(" ", aws_amplify_domain_association.tf_amplify_domain.certificate_verification_dns_record)[0]
  type    = split(" ", aws_amplify_domain_association.tf_amplify_domain.certificate_verification_dns_record)[1]
  ttl     = 300
  records = [split(" ", aws_amplify_domain_association.tf_amplify_domain.certificate_verification_dns_record)[2]]
}

resource "aws_wafv2_ip_set" "tf_ipset" {
  name               = "tf-ipset"
  description        = "tf-test IP allowlist"
  scope              = "CLOUDFRONT"
  ip_address_version = "IPV4"
  addresses          = ["203.0.113.0/24", "198.51.100.10/32"]
}

resource "aws_wafv2_web_acl" "tf_acl" {
  name        = "tf-acl"
  description = "tf-test WebACL"
  scope       = "CLOUDFRONT"

  default_action {
    allow {}
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "tf-acl-metric"
    sampled_requests_enabled   = true
  }

  rule {
    name     = "block-ipset"
    priority = 1

    action {
      block {}
    }

    statement {
      ip_set_reference_statement {
        arn = aws_wafv2_ip_set.tf_ipset.arn
      }
    }

    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "tf-acl-block"
      sampled_requests_enabled   = true
    }
  }
}

resource "aws_wafv2_web_acl_association" "tf_assoc" {
  resource_arn = aws_cloudfront_distribution.tf_dist.arn
  web_acl_arn  = aws_wafv2_web_acl.tf_acl.arn
}

resource "aws_wafv2_web_acl_association" "tf_amplify_assoc" {
  resource_arn = aws_amplify_app.tf_amplify.arn
  web_acl_arn  = aws_wafv2_web_acl.tf_acl.arn
}

resource "aws_sqs_queue" "tf_eventbridge_queue" {
  name = "tf-eventbridge-queue"
}

resource "aws_cloudwatch_event_bus" "tf_eventbridge_bus" {
  name = "tf-eventbridge-bus"

  tags = {
    env = "test"
  }
}

resource "aws_cloudwatch_event_permission" "tf_eventbridge_permission" {
  principal      = "123456789012"
  statement_id   = "tf-eventbridge-permission"
  action         = "events:PutEvents"
  event_bus_name = aws_cloudwatch_event_bus.tf_eventbridge_bus.name
}

resource "aws_cloudwatch_event_archive" "tf_eventbridge_archive" {
  name             = "tf-eventbridge-archive"
  event_source_arn = aws_cloudwatch_event_bus.tf_eventbridge_bus.arn
  description      = "tf-test EventBridge archive"
  event_pattern    = jsonencode({ source = ["sockerless.terraform.archive"] })
  retention_days   = 1
}

resource "aws_cloudwatch_event_rule" "tf_eventbridge_rule" {
  name          = "tf-eventbridge-rule"
  description   = "tf-test EventBridge rule"
  event_pattern = jsonencode({ source = ["sockerless.terraform"] })

  tags = {
    env = "test"
  }
}

resource "aws_cloudwatch_event_target" "tf_eventbridge_target" {
  rule      = aws_cloudwatch_event_rule.tf_eventbridge_rule.name
  target_id = "tf-eventbridge-queue"
  arn       = aws_sqs_queue.tf_eventbridge_queue.arn
}

resource "aws_cloudwatch_log_group" "tf_log_group" {
  name              = "/aws/sockerless/tf-log-group"
  retention_in_days = 7
  # KMS encryption at rest — read back via DescribeLogGroups.
  kms_key_id = aws_kms_key.tf_kms.arn
}

# Standalone managed policy — its destroy path calls ListPolicyVersions
#; without it `terraform destroy` previously failed.
resource "aws_iam_policy" "tf_nat_policy" {
  name = "tf-nat-policy"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["ec2:AssignPrivateIpAddresses", "ec2:DescribeNetworkInterfaces"]
      Resource = "*"
    }]
  })
  tags = {
    module = "fck-nat"
  }
}

resource "aws_api_gateway_rest_api" "tf_rest_api" {
  name        = "tf-rest-api"
  description = "terraform API Gateway REST API coverage"
}

resource "aws_api_gateway_resource" "tf_rest_resource" {
  rest_api_id = aws_api_gateway_rest_api.tf_rest_api.id
  parent_id   = aws_api_gateway_rest_api.tf_rest_api.root_resource_id
  path_part   = "tf"
}

resource "aws_api_gateway_method" "tf_rest_method" {
  rest_api_id   = aws_api_gateway_rest_api.tf_rest_api.id
  resource_id   = aws_api_gateway_resource.tf_rest_resource.id
  http_method   = "GET"
  authorization = "NONE"
}

resource "aws_api_gateway_integration" "tf_rest_integration" {
  rest_api_id          = aws_api_gateway_rest_api.tf_rest_api.id
  resource_id          = aws_api_gateway_resource.tf_rest_resource.id
  http_method          = aws_api_gateway_method.tf_rest_method.http_method
  type                 = "MOCK"
  passthrough_behavior = "WHEN_NO_MATCH"

  request_templates = {
    "application/json" = "{\"statusCode\":200}"
  }
}

resource "aws_api_gateway_method_response" "tf_rest_method_response" {
  rest_api_id = aws_api_gateway_rest_api.tf_rest_api.id
  resource_id = aws_api_gateway_resource.tf_rest_resource.id
  http_method = aws_api_gateway_method.tf_rest_method.http_method
  status_code = "200"

  response_models = {
    "application/json" = "Empty"
  }
}

resource "aws_api_gateway_integration_response" "tf_rest_integration_response" {
  rest_api_id = aws_api_gateway_rest_api.tf_rest_api.id
  resource_id = aws_api_gateway_resource.tf_rest_resource.id
  http_method = aws_api_gateway_method.tf_rest_method.http_method
  status_code = aws_api_gateway_method_response.tf_rest_method_response.status_code

  response_templates = {
    "application/json" = "{\"ok\":true}"
  }
}

resource "aws_api_gateway_deployment" "tf_rest_deployment" {
  rest_api_id = aws_api_gateway_rest_api.tf_rest_api.id
  description = "terraform API Gateway REST deployment"

  depends_on = [
    aws_api_gateway_integration.tf_rest_integration,
    aws_api_gateway_integration_response.tf_rest_integration_response,
  ]
}

resource "aws_api_gateway_stage" "tf_rest_stage" {
  rest_api_id   = aws_api_gateway_rest_api.tf_rest_api.id
  deployment_id = aws_api_gateway_deployment.tf_rest_deployment.id
  stage_name    = "tf"
}

resource "aws_apigatewayv2_api" "tf_http_api" {
  name          = "tf-http-api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_integration" "tf_http_integration" {
  api_id                 = aws_apigatewayv2_api.tf_http_api.id
  integration_type       = "HTTP_PROXY"
  integration_uri        = "https://example.com"
  integration_method     = "GET"
  payload_format_version = "1.0"
}

resource "aws_apigatewayv2_route" "tf_http_route" {
  api_id    = aws_apigatewayv2_api.tf_http_api.id
  route_key = "GET /tf"
  target    = "integrations/${aws_apigatewayv2_integration.tf_http_integration.id}"
}

resource "aws_apigatewayv2_deployment" "tf_http_deployment" {
  api_id      = aws_apigatewayv2_api.tf_http_api.id
  description = "terraform API Gateway v2 deployment"

  depends_on = [aws_apigatewayv2_route.tf_http_route]
}

resource "aws_apigatewayv2_stage" "tf_http_stage" {
  api_id        = aws_apigatewayv2_api.tf_http_api.id
  name          = "tf"
  deployment_id = aws_apigatewayv2_deployment.tf_http_deployment.id
  auto_deploy   = false

  tags = {
    consumer = "terraform"
  }
}

resource "aws_lambda_function" "tf_lambda" {
  function_name                  = "tf-lambda-image"
  role                           = "arn:aws:iam::123456789012:role/tf-lambda"
  package_type                   = "Image"
  image_uri                      = "123456789012.dkr.ecr.us-east-1.amazonaws.com/sockerless-lambda-runtime-handler:test"
  memory_size                    = 128
  timeout                        = 3
  publish                        = true
  reserved_concurrent_executions = 5

  vpc_config {
    subnet_ids         = [aws_subnet.tf_ec2_subnet.id]
    security_group_ids = [aws_security_group.tf_ec2_sg.id]
  }

  tags = {
    env = "terraform"
  }
}

resource "aws_lambda_alias" "tf_lambda_live" {
  name             = "live"
  function_name    = aws_lambda_function.tf_lambda.function_name
  function_version = aws_lambda_function.tf_lambda.version
}

resource "aws_lambda_permission" "tf_lambda_events" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.tf_lambda.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.tf_eventbridge_rule.arn
}

resource "aws_lambda_function_url" "tf_lambda_url" {
  function_name      = aws_lambda_function.tf_lambda.function_name
  authorization_type = "NONE"
}

# EventBridge Scheduler — cron/rate-driven invocation of the runner Lambda.
# Exercises the REST-JSON Scheduler surface (CreateSchedule / GetSchedule),
# distinct from the EventBridge rule above.
resource "aws_scheduler_schedule" "tf_schedule" {
  name       = "tf-runner-schedule"
  group_name = "default"

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression = "rate(1 hour)"

  target {
    arn      = aws_lambda_function.tf_lambda.arn
    role_arn = "arn:aws:iam::123456789012:role/tf-scheduler"
  }
}

data "aws_lambda_invocation" "tf_lambda_echo" {
  function_name = aws_lambda_function.tf_lambda.function_name

  input = jsonencode({
    source = "terraform"
  })
}

resource "aws_route53_zone" "tf_zone" {
  name    = "tf-route53.local"
  comment = "tf-test zone"
  tags = {
    Environment = "terraform"
  }
}

# A-record + ALIAS record. ALIAS targets the CloudFront distribution
# created below by reference; this exercises the cross-resource flow
# that real production stacks use (Route 53 ALIAS → CloudFront).
resource "aws_route53_record" "tf_a" {
  zone_id = aws_route53_zone.tf_zone.zone_id
  name    = "api.tf-route53.local"
  type    = "A"
  ttl     = 300
  records = ["203.0.113.42"]
}

resource "aws_route53_record" "tf_alias" {
  zone_id = aws_route53_zone.tf_zone.zone_id
  name    = "cdn.tf-route53.local"
  type    = "A"

  alias {
    name                   = aws_cloudfront_distribution.tf_dist.domain_name
    zone_id                = aws_cloudfront_distribution.tf_dist.hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_acm_certificate" "tf_cert" {
  domain_name               = "tf-cert.example.com"
  subject_alternative_names = ["www.tf-cert.example.com"]
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_cloudfront_function" "tf_fn" {
  name    = "tf-fn"
  runtime = "cloudfront-js-2.0"
  comment = "tf-test function"
  publish = true
  code    = <<-EOF
    function handler(event) {
      return event.request;
    }
  EOF
}

resource "aws_cloudfront_public_key" "tf_pk" {
  name        = "tf-pk"
  comment     = "tf-test public key"
  encoded_key = <<-EOF
    -----BEGIN PUBLIC KEY-----
    dGVzdC1rZXktYnl0ZXMtZm9yLXNpbXVsYXRvcg==
    -----END PUBLIC KEY-----
  EOF
}

resource "aws_cloudfront_key_group" "tf_kg" {
  name    = "tf-kg"
  comment = "tf-test key group"
  items   = [aws_cloudfront_public_key.tf_pk.id]
}

resource "aws_cloudfront_response_headers_policy" "tf_rhp" {
  name    = "tf-response-headers-policy"
  comment = "tf-test response headers policy"

  security_headers_config {
    content_type_options {
      override = true
    }
    frame_options {
      override     = true
      frame_option = "DENY"
    }
  }
}

resource "aws_cloudfront_distribution" "tf_dist" {
  enabled         = false # let terraform destroy without an explicit disable step
  is_ipv6_enabled = true
  comment         = "tf-test cloudfront"
  price_class     = "PriceClass_100"

  origin {
    domain_name              = "tf-origin.example.com"
    origin_id                = "tf-origin"
    origin_access_control_id = aws_cloudfront_origin_access_control.tf_oac.id

    custom_origin_config {
      http_port                = 80
      https_port               = 443
      origin_protocol_policy   = "https-only"
      origin_ssl_protocols     = ["TLSv1.2"]
      origin_read_timeout      = 30
      origin_keepalive_timeout = 5
    }
  }

  default_cache_behavior {
    target_origin_id       = "tf-origin"
    viewer_protocol_policy = "redirect-to-https"
    allowed_methods        = ["GET", "HEAD"]
    cached_methods         = ["GET", "HEAD"]

    forwarded_values {
      query_string = false
      cookies {
        forward = "none"
      }
    }

    min_ttl     = 0
    default_ttl = 0
    max_ttl     = 0
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    cloudfront_default_certificate = true
  }
}

# ---------- Runner foundation (S3 / DynamoDB / KMS / SecretsManager / SSM) ----------

# S3 bucket — runner backends stash workflow artifacts + terraform state.
resource "aws_s3_bucket" "tf_bucket" {
  bucket        = "tf-test-runner-bucket"
  force_destroy = true

  tags = {
    env = "test"
  }
}

# Amazon Data Firehose assumes this role and writes delivered records to the
# production-shaped S3 destination. Keeping the trust and permissions in the
# Terraform graph proves that the service consumes IAM and S3 cloud primitives
# instead of accepting an inert role ARN.
resource "aws_iam_role" "tf_firehose_role" {
  name = "tf-firehose-delivery-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Service = "firehose.amazonaws.com"
      }
      Action = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy" "tf_firehose_policy" {
  name = "tf-firehose-s3-delivery"
  role = aws_iam_role.tf_firehose_role.name

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Action = [
        "s3:GetBucketLocation",
        "s3:ListBucket",
        "s3:PutObject",
      ]
      Resource = [
        aws_s3_bucket.tf_bucket.arn,
        "${aws_s3_bucket.tf_bucket.arn}/*",
      ]
    }]
  })
}

resource "aws_kinesis_firehose_delivery_stream" "tf_firehose" {
  name        = "tf-firehose-stream"
  destination = "extended_s3"
  depends_on  = [aws_iam_role_policy.tf_firehose_policy]

  extended_s3_configuration {
    role_arn           = aws_iam_role.tf_firehose_role.arn
    bucket_arn         = aws_s3_bucket.tf_bucket.arn
    prefix             = "firehose/"
    buffering_interval = 0
    buffering_size     = 1
    compression_format = "UNCOMPRESSED"
  }

  tags = {
    env = "terraform"
  }
}

# A ROOT AWS Private Certificate Authority is activated through the same
# create → CSR → issue → import sequence required by real AWS. The separate
# certificate resources make every step visible to the official provider.
resource "aws_acmpca_certificate_authority" "tf_root_ca" {
  type                            = "ROOT"
  permanent_deletion_time_in_days = 7

  certificate_authority_configuration {
    key_algorithm     = "RSA_2048"
    signing_algorithm = "SHA256WITHRSA"

    subject {
      common_name  = "Terraform Root CA"
      organization = "Sockerless"
    }
  }

  tags = {
    env = "terraform"
  }
}

resource "aws_acmpca_certificate" "tf_root_ca" {
  certificate_authority_arn   = aws_acmpca_certificate_authority.tf_root_ca.arn
  certificate_signing_request = aws_acmpca_certificate_authority.tf_root_ca.certificate_signing_request
  signing_algorithm           = "SHA256WITHRSA"
  template_arn                = "arn:aws:acm-pca:::template/RootCACertificate/V1"

  validity {
    type  = "YEARS"
    value = 10
  }
}

resource "aws_acmpca_certificate_authority_certificate" "tf_root_ca" {
  certificate_authority_arn = aws_acmpca_certificate_authority.tf_root_ca.arn
  certificate               = aws_acmpca_certificate.tf_root_ca.certificate
  certificate_chain         = aws_acmpca_certificate.tf_root_ca.certificate_chain
}

resource "aws_cloudtrail" "tf_trail" {
  name                          = "tf-trail"
  s3_bucket_name                = aws_s3_bucket.tf_bucket.id
  s3_key_prefix                 = "trail-logs"
  enable_logging                = false
  include_global_service_events = false

  tags = {
    env = "terraform"
  }
}

resource "aws_s3_bucket" "tf_bucket_replication_dest" {
  bucket        = "tf-test-replication-dest"
  force_destroy = true
}

resource "aws_s3_bucket" "tf_bucket_acl_target" {
  bucket        = "tf-test-acl-bucket"
  force_destroy = true
}

resource "aws_s3_bucket" "tf_bucket_object_lock" {
  bucket              = "tf-test-object-lock-bucket"
  force_destroy       = true
  object_lock_enabled = true
}

# S3 bucket-subresource fan-out. Every resource here calls a distinct
# `PUT /{bucket}?<subresource>` against the sim — without the bucket-
# subresource dispatcher in s3_bucket_subresources.go, each would
# route to CreateBucket and 409. tf-provider-aws follows each Create
# with a paired Read against the matching GET subresource; PUT→GET
# round-trip is what the provider asserts on apply, so any break in
# the dispatcher surfaces here as `plan diff after apply`.

resource "aws_s3_bucket_versioning" "tf_bucket_versioning" {
  bucket = aws_s3_bucket.tf_bucket.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "tf_bucket_lifecycle" {
  bucket = aws_s3_bucket.tf_bucket.id
  rule {
    id     = "expire-30d"
    status = "Enabled"
    filter {
      prefix = ""
    }
    expiration {
      days = 30
    }
  }
}

resource "aws_s3_bucket_cors_configuration" "tf_bucket_cors" {
  bucket = aws_s3_bucket.tf_bucket.id
  cors_rule {
    allowed_methods = ["GET", "PUT"]
    allowed_origins = ["https://app.example.com"]
    allowed_headers = ["*"]
    max_age_seconds = 3000
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "tf_bucket_sse" {
  bucket = aws_s3_bucket.tf_bucket.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_website_configuration" "tf_bucket_website" {
  bucket = aws_s3_bucket.tf_bucket.id
  index_document {
    suffix = "index.html"
  }
}

resource "aws_s3_bucket_policy" "tf_bucket_policy" {
  bucket = aws_s3_bucket.tf_bucket.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = "*"
      Action    = "s3:GetObject"
      Resource  = "${aws_s3_bucket.tf_bucket.arn}/*"
    }]
  })
}

resource "aws_s3_bucket_replication_configuration" "tf_bucket_replication" {
  bucket = aws_s3_bucket.tf_bucket.id
  role   = "arn:aws:iam::000000000001:role/s3-replication"

  rule {
    id     = "replicate-logs"
    status = "Enabled"
    filter {
      prefix = "logs/"
    }
    destination {
      bucket = aws_s3_bucket.tf_bucket_replication_dest.arn
    }
  }
}

resource "aws_s3_bucket_logging" "tf_bucket_logging" {
  bucket        = aws_s3_bucket.tf_bucket.id
  target_bucket = aws_s3_bucket.tf_bucket.id
  target_prefix = "logs/"
}

resource "aws_s3_bucket_ownership_controls" "tf_bucket_acl_ownership" {
  bucket = aws_s3_bucket.tf_bucket_acl_target.id
  rule {
    object_ownership = "ObjectWriter"
  }
}

resource "aws_s3_bucket_acl" "tf_bucket_acl" {
  depends_on = [aws_s3_bucket_ownership_controls.tf_bucket_acl_ownership]
  bucket     = aws_s3_bucket.tf_bucket_acl_target.id
  acl        = "private"
}

resource "aws_s3_bucket_request_payment_configuration" "tf_bucket_request_payment" {
  bucket = aws_s3_bucket.tf_bucket.id
  payer  = "Requester"
}

resource "aws_s3_bucket_accelerate_configuration" "tf_bucket_accelerate" {
  bucket = aws_s3_bucket.tf_bucket.id
  status = "Enabled"
}

resource "aws_s3_bucket_public_access_block" "tf_bucket_pab" {
  bucket                  = aws_s3_bucket.tf_bucket.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "tf_bucket_ownership" {
  bucket = aws_s3_bucket.tf_bucket.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_notification" "tf_bucket_notification" {
  bucket = aws_s3_bucket.tf_bucket.id
  queue {
    id        = "queue-created"
    queue_arn = aws_sqs_queue.tf_eventbridge_queue.arn
    events    = ["s3:ObjectCreated:Put"]
  }
}

resource "aws_s3_bucket_object_lock_configuration" "tf_bucket_object_lock" {
  bucket              = aws_s3_bucket.tf_bucket_object_lock.id
  object_lock_enabled = "Enabled"
  rule {
    default_retention {
      mode = "GOVERNANCE"
      days = 1
    }
  }
}

resource "aws_s3_bucket_intelligent_tiering_configuration" "tf_bucket_intelligent_tiering" {
  bucket = aws_s3_bucket.tf_bucket.id
  name   = "archive-tier"
  status = "Enabled"
  tiering {
    access_tier = "ARCHIVE_ACCESS"
    days        = 90
  }
}

resource "aws_s3_bucket_inventory" "tf_bucket_inventory" {
  bucket                   = aws_s3_bucket.tf_bucket.id
  name                     = "inventory-current"
  included_object_versions = "Current"
  destination {
    bucket {
      bucket_arn = aws_s3_bucket.tf_bucket.arn
      format     = "CSV"
    }
  }
  schedule {
    frequency = "Daily"
  }
}

resource "aws_s3_bucket_analytics_configuration" "tf_bucket_analytics" {
  bucket = aws_s3_bucket.tf_bucket.id
  name   = "analytics-all"
  storage_class_analysis {
    data_export {
      output_schema_version = "V_1"
      destination {
        s3_bucket_destination {
          bucket_arn = aws_s3_bucket.tf_bucket.arn
          format     = "CSV"
        }
      }
    }
  }
}

resource "aws_s3_bucket_metric" "tf_bucket_metric" {
  bucket = aws_s3_bucket.tf_bucket.id
  name   = "metrics-prefix"
  filter {
    prefix = "logs/"
  }
}

# DynamoDB table — runner state-locking + per-pod registries.
resource "aws_dynamodb_table" "tf_table" {
  name         = "tf-test-table"
  hash_key     = "LockID"
  billing_mode = "PAY_PER_REQUEST"

  attribute {
    name = "LockID"
    type = "S"
  }
  attribute {
    name = "GSI1PK"
    type = "S"
  }

  # The provider waits for the GSI's IndexStatus to reach ACTIVE; broken GSI
  # modeling hangs apply for ~21 retries then fails.
  global_secondary_index {
    name = "GSI1"
    key_schema {
      attribute_name = "GSI1PK"
      key_type       = "HASH"
    }
    projection_type = "ALL"
  }

  # These settings use separate DynamoDB APIs and provider waiters. They must
  # persist independently of DescribeTable so apply and refresh converge.
  ttl {
    attribute_name = "ExpiresAt"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  tags = {
    env        = "terraform"
    managed-by = "sockerless"
  }
}

# KMS key — encrypts SecretsManager secrets and S3 objects.
# enable_key_rotation drives EnableKeyRotation + GetKeyRotationStatus, which
# nearly every real-world KMS key sets.
resource "aws_kms_key" "tf_kms" {
  description             = "tf-test runner KMS key"
  deletion_window_in_days = 7
  enable_key_rotation     = true
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "EnableIamUserPermissions"
      Effect   = "Allow"
      Action   = "kms:*"
      Resource = "*"
      Principal = {
        AWS = "arn:aws:iam::123456789012:root"
      }
    }]
  })

  # Tags drive TagResource + ListResourceTags; the provider polls
  # ListResourceTags until they propagate, so broken KMS tagging hangs apply.
  tags = {
    env        = "terraform"
    managed-by = "sockerless"
  }
}

resource "aws_kms_alias" "tf_kms_alias" {
  name          = "alias/tf-test-runner"
  target_key_id = aws_kms_key.tf_kms.key_id
}

# KMS grant — how AWS services delegate CMK use for encryption at rest
# CreateGrant on apply (read-back via ListGrants), RevokeGrant on
# destroy.
resource "aws_kms_grant" "tf_kms_grant" {
  name              = "tf-runner-grant"
  key_id            = aws_kms_key.tf_kms.key_id
  grantee_principal = "arn:aws:iam::000000000000:role/tf-kms-grantee"
  operations        = ["Decrypt", "GenerateDataKey"]
}

# Secrets Manager secret + version — runner credentials.
resource "aws_secretsmanager_secret" "tf_secret" {
  name                    = "tf-test-runner-secret"
  recovery_window_in_days = 0

  replica {
    region = "us-west-2"
  }
}

resource "aws_secretsmanager_secret_version" "tf_secret_v1" {
  secret_id     = aws_secretsmanager_secret.tf_secret.id
  secret_string = "tf-test-runner-secret-payload"
}

# SSM Parameter — runtime config injection (ECS task-def envFrom).
resource "aws_ssm_parameter" "tf_param" {
  name  = "/tf-test/runner/config"
  type  = "String"
  value = "tf-test-config-value"
}

# 10 end-to-end stack outputs — verify the production-shape
# cross-resource links converge after apply. apply_test.go asserts that
# WAF.resource_arn == CloudFront.arn, Route 53 ALIAS target == CloudFront
# domain_name, and the ACM cert ARN region is us-east-1 (CloudFront pin).
output "cloudfront_arn" {
  value = aws_cloudfront_distribution.tf_dist.arn
}
output "cloudfront_domain_name" {
  value = aws_cloudfront_distribution.tf_dist.domain_name
}
output "cloudfront_hosted_zone_id" {
  value = aws_cloudfront_distribution.tf_dist.hosted_zone_id
}
output "acm_certificate_arn" {
  value = aws_acm_certificate.tf_cert.arn
}
output "wafv2_assoc_resource_arn" {
  value = aws_wafv2_web_acl_association.tf_assoc.resource_arn
}
output "wafv2_amplify_assoc_resource_arn" {
  value = aws_wafv2_web_acl_association.tf_amplify_assoc.resource_arn
}
output "wafv2_assoc_webacl_arn" {
  value = aws_wafv2_web_acl_association.tf_assoc.web_acl_arn
}
output "route53_alias_target_name" {
  value = aws_route53_record.tf_alias.alias[0].name
}
output "route53_alias_target_zone_id" {
  value = aws_route53_record.tf_alias.alias[0].zone_id
}
output "apigateway_rest_api_id" {
  value = aws_api_gateway_rest_api.tf_rest_api.id
}
output "apigateway_rest_resource_path" {
  value = aws_api_gateway_resource.tf_rest_resource.path
}
output "apigateway_rest_stage_name" {
  value = aws_api_gateway_stage.tf_rest_stage.stage_name
}
output "apigatewayv2_api_id" {
  value = aws_apigatewayv2_api.tf_http_api.id
}
output "apigatewayv2_route_key" {
  value = aws_apigatewayv2_route.tf_http_route.route_key
}
output "apigatewayv2_stage_name" {
  value = aws_apigatewayv2_stage.tf_http_stage.name
}
output "appautoscaling_target_resource_id" {
  value = aws_appautoscaling_target.ecs.resource_id
}
output "appautoscaling_policy_arn" {
  value = aws_appautoscaling_policy.ecs_cpu.arn
}
output "scheduler_schedule_arn" {
  value = aws_scheduler_schedule.tf_schedule.arn
}
output "ecs_service_name" {
  value = aws_ecs_service.tf_runner.name
}

output "ecs_service_registry_arn" {
  value = one(aws_ecs_service.tf_runner.service_registries).registry_arn
}
output "ecs_cluster_capacity_providers" {
  value = join(",", sort(aws_ecs_cluster_capacity_providers.main.capacity_providers))
}
output "ecs_task_definition_bridge_network_mode" {
  value = aws_ecs_task_definition.tf_runner_bridge.network_mode
}
output "lambda_function_arn" {
  value = aws_lambda_function.tf_lambda.arn
}
output "lambda_function_version" {
  value = aws_lambda_function.tf_lambda.version
}
output "lambda_alias_arn" {
  value = aws_lambda_alias.tf_lambda_live.arn
}
output "lambda_function_url" {
  value = aws_lambda_function_url.tf_lambda_url.function_url
}
output "lambda_invocation_result" {
  value = data.aws_lambda_invocation.tf_lambda_echo.result
}
output "amplify_app_arn" {
  value = aws_amplify_app.tf_amplify.arn
}
output "elbv2_lb_arn" {
  value = aws_lb.tf_alb.arn
}
output "elbv2_lb_dns_name" {
  value = aws_lb.tf_alb.dns_name
}
output "elbv2_target_group_arn" {
  value = aws_lb_target_group.tf_alb_tg.arn
}
output "elbv2_listener_arn" {
  value = aws_lb_listener.tf_alb_listener.arn
}
output "elbv2_listener_rule_arn" {
  value = aws_lb_listener_rule.tf_alb_rule.arn
}
output "kms_grant_id" {
  value = aws_kms_grant.tf_kms_grant.grant_id
}
output "ecr_repository_url" {
  value = aws_ecr_repository.tf_repo.repository_url
}
output "ecr_repository_tag_mutability" {
  value = aws_ecr_repository.tf_repo.image_tag_mutability
}
output "data_vpc_cidr" {
  value = data.aws_vpc.by_filter.cidr_block
}
output "data_vpc_id" {
  value = data.aws_vpc.by_filter.id
}
output "iam_nat_policy_arn" {
  value = aws_iam_policy.tf_nat_policy.arn
}
output "log_group_kms_key_id" {
  value = aws_cloudwatch_log_group.tf_log_group.kms_key_id
}
output "ec2_nat_gateway_id" {
  value = aws_nat_gateway.tf_nat.id
}
output "ec2_nat_eip_public_ip" {
  value = aws_eip.tf_nat_eip.public_ip
}
output "ec2_nat_route_table_id" {
  value = aws_route_table.tf_nat_rt.id
}
output "ec2_ebs_volume_id" {
  value = aws_ebs_volume.tf_ebs.id
}
output "ec2_ebs_snapshot_id" {
  value = aws_ebs_snapshot.tf_ebs_snapshot.id
}
output "ec2_ebs_snapshot_copy_id" {
  value = aws_ebs_snapshot_copy.tf_ebs_snapshot_copy.id
}
output "cloudwatch_dashboard_arn" {
  value = aws_cloudwatch_dashboard.tf_dashboard.dashboard_arn
}
output "cloudwatch_alarm_arn" {
  value = aws_cloudwatch_metric_alarm.tf_alarm.arn
}
output "ec2_ebs_restored_volume_id" {
  value = aws_ebs_volume.tf_ebs_restored.id
}
output "autoscaling_group_name" {
  value = aws_autoscaling_group.tf_asg.name
}
output "cloudtrail_arn" {
  value = aws_cloudtrail.tf_trail.arn
}
output "firehose_delivery_stream_arn" {
  value = aws_kinesis_firehose_delivery_stream.tf_firehose.arn
}
output "acmpca_certificate_authority_arn" {
  value = aws_acmpca_certificate_authority.tf_root_ca.arn
}
output "acmpca_certificate" {
  value     = aws_acmpca_certificate_authority_certificate.tf_root_ca.certificate
  sensitive = true
}
output "efs_file_system_arn" {
  value = aws_efs_file_system.tf_efs.arn
}
output "efs_mount_target_id" {
  value = aws_efs_mount_target.tf_efs_mount.id
}
output "efs_access_point_arn" {
  value = aws_efs_access_point.tf_efs_ap.arn
}
output "cloudwatch_log_group_arn" {
  value = aws_cloudwatch_log_group.tf_log_group.arn
}
output "iam_slr_arn" {
  value = aws_iam_service_linked_role.tf_slr_cloudfront.arn
}
output "s3_bucket_arn" {
  value = aws_s3_bucket.tf_bucket.arn
}
output "s3_bucket_tags_env" {
  value = aws_s3_bucket.tf_bucket.tags["env"]
}
output "dynamodb_table_arn" {
  value = aws_dynamodb_table.tf_table.arn
}
output "kms_key_arn" {
  value = aws_kms_key.tf_kms.arn
}
output "kms_key_rotation_enabled" {
  # tostring so the test's string-typed output reader (outputs.must) can read it;
  # enable_key_rotation is a bool attribute.
  value = tostring(aws_kms_key.tf_kms.enable_key_rotation)
}
output "kms_key_tag_env" {
  # Reads back through ListResourceTags; empty/missing means KMS tagging is broken.
  value = aws_kms_key.tf_kms.tags["env"]
}
output "kms_alias_arn" {
  value = aws_kms_alias.tf_kms_alias.arn
}
output "secretsmanager_secret_arn" {
  value = aws_secretsmanager_secret.tf_secret.arn
}

output "secretsmanager_secret_replica_region" {
  value = one(aws_secretsmanager_secret.tf_secret.replica[*].region)
}
output "ssm_parameter_arn" {
  value = aws_ssm_parameter.tf_param.arn
}
output "s3_bucket_versioning_status" {
  value = aws_s3_bucket_versioning.tf_bucket_versioning.versioning_configuration[0].status
}
output "s3_bucket_lifecycle_id" {
  value = aws_s3_bucket_lifecycle_configuration.tf_bucket_lifecycle.rule[0].id
}
output "s3_bucket_cors_origin" {
  value = one([for rule in aws_s3_bucket_cors_configuration.tf_bucket_cors.cors_rule : tolist(rule.allowed_origins)[0]])
}
output "s3_bucket_policy_bucket" {
  value = aws_s3_bucket_policy.tf_bucket_policy.bucket
}
output "s3_bucket_sse_algorithm" {
  value = one([for rule in aws_s3_bucket_server_side_encryption_configuration.tf_bucket_sse.rule : rule.apply_server_side_encryption_by_default[0].sse_algorithm])
}
output "s3_bucket_replication_rule_id" {
  value = aws_s3_bucket_replication_configuration.tf_bucket_replication.rule[0].id
}
output "s3_bucket_logging_target_prefix" {
  value = aws_s3_bucket_logging.tf_bucket_logging.target_prefix
}
output "s3_bucket_acl_value" {
  value = aws_s3_bucket_acl.tf_bucket_acl.acl
}
output "s3_bucket_request_payment_payer" {
  value = aws_s3_bucket_request_payment_configuration.tf_bucket_request_payment.payer
}
output "s3_bucket_accelerate_status" {
  value = aws_s3_bucket_accelerate_configuration.tf_bucket_accelerate.status
}
output "s3_bucket_website_index" {
  value = aws_s3_bucket_website_configuration.tf_bucket_website.index_document[0].suffix
}
output "s3_bucket_pab_block_public_acls" {
  value = aws_s3_bucket_public_access_block.tf_bucket_pab.block_public_acls
}
output "s3_bucket_ownership" {
  value = aws_s3_bucket_ownership_controls.tf_bucket_ownership.rule[0].object_ownership
}
output "s3_bucket_notification_queue_id" {
  value = aws_s3_bucket_notification.tf_bucket_notification.queue[0].id
}
output "s3_bucket_object_lock_mode" {
  value = aws_s3_bucket_object_lock_configuration.tf_bucket_object_lock.rule[0].default_retention[0].mode
}
output "s3_bucket_intelligent_tiering_name" {
  value = aws_s3_bucket_intelligent_tiering_configuration.tf_bucket_intelligent_tiering.name
}
output "s3_bucket_inventory_name" {
  value = aws_s3_bucket_inventory.tf_bucket_inventory.name
}
output "s3_bucket_analytics_name" {
  value = aws_s3_bucket_analytics_configuration.tf_bucket_analytics.name
}
output "s3_bucket_metric_name" {
  value = aws_s3_bucket_metric.tf_bucket_metric.name
}

# ── Step Functions ──────────────────────────────────────────────────────────

resource "aws_sfn_state_machine" "tf_sfn_sm" {
  name     = "tf-sfn-state-machine"
  role_arn = "arn:aws:iam::123456789012:role/sfn-role"
  publish  = true

  definition = jsonencode({
    Comment = "Terraform test"
    StartAt = "Pass"
    States = {
      Pass = {
        Type = "Pass"
        End  = true
      }
    }
  })

  tags = {
    env = "terraform"
  }
}

resource "aws_sfn_alias" "tf_sfn_alias" {
  name        = "PROD"
  description = "Terraform-managed AWS Step Functions alias"

  routing_configuration {
    state_machine_version_arn = aws_sfn_state_machine.tf_sfn_sm.state_machine_version_arn
    weight                    = 100
  }
}

output "sfn_state_machine_arn" {
  value = aws_sfn_state_machine.tf_sfn_sm.arn
}

output "sfn_alias_arn" {
  value = aws_sfn_alias.tf_sfn_alias.arn
}

# ── CodeBuild ───────────────────────────────────────────────────────────────

resource "aws_codebuild_project" "tf_cb_project" {
  name         = "tf-codebuild-project"
  service_role = "arn:aws:iam::123456789012:role/cb-role"

  source {
    type      = "NO_SOURCE"
    buildspec = "version: 0.2\nphases:\n  build:\n    commands:\n      - echo done\n"
  }

  artifacts {
    type = "NO_ARTIFACTS"
  }

  environment {
    type         = "LINUX_CONTAINER"
    image        = "aws/codebuild/standard:7.0"
    compute_type = "BUILD_GENERAL1_SMALL"
  }

  tags = {
    env = "terraform"
  }
}

output "codebuild_project_arn" {
  value = aws_codebuild_project.tf_cb_project.arn
}

# ── Glue ────────────────────────────────────────────────────────────────────

resource "aws_glue_catalog_database" "tf_glue_db" {
  name       = "tf-glue-database"
  catalog_id = data.aws_caller_identity.current.account_id
  tags = {
    owner = "terraform"
  }
}

resource "aws_glue_catalog_table" "tf_glue_table" {
  name          = "tf-glue-table"
  catalog_id    = data.aws_caller_identity.current.account_id
  database_name = aws_glue_catalog_database.tf_glue_db.name

  storage_descriptor {
    location      = "s3://tf-bucket/prefix/"
    input_format  = "org.apache.hadoop.mapred.TextInputFormat"
    output_format = "org.apache.hadoop.hive.ql.io.HiveIgnoreKeyTextOutputFormat"
  }
}

resource "aws_glue_job" "tf_glue_job" {
  name     = "tf-glue-job"
  role_arn = "arn:aws:iam::123456789012:role/glue-role"

  command {
    script_location = "s3://tf-bucket/script.py"
  }

  glue_version      = "4.0"
  worker_type       = "G.1X"
  number_of_workers = 2

  tags = {
    env = "terraform"
  }
}

output "glue_database_id" {
  value = aws_glue_catalog_database.tf_glue_db.id
}

output "glue_job_name" {
  value = aws_glue_job.tf_glue_job.name
}

# ── Batch ───────────────────────────────────────────────────────────────────

resource "aws_batch_compute_environment" "tf_batch_ce" {
  name  = "tf-batch-compute-env"
  type  = "UNMANAGED"
  state = "ENABLED"

  tags = {
    env = "terraform"
  }
}

resource "aws_batch_job_queue" "tf_batch_jq" {
  name     = "tf-batch-job-queue"
  state    = "ENABLED"
  priority = 10

  compute_environment_order {
    order               = 1
    compute_environment = aws_batch_compute_environment.tf_batch_ce.arn
  }

  tags = {
    env = "terraform"
  }
}

resource "aws_batch_job_definition" "tf_batch_jd" {
  name = "tf-batch-job-definition"
  type = "container"

  container_properties = jsonencode({
    image  = "public.ecr.aws/docker/library/alpine:3"
    vcpus  = 1
    memory = 512
  })

  tags = {
    env = "terraform"
  }
}

output "batch_compute_env_arn" {
  value = aws_batch_compute_environment.tf_batch_ce.arn
}

output "batch_job_queue_arn" {
  value = aws_batch_job_queue.tf_batch_jq.arn
}

output "batch_job_definition_arn" {
  value = aws_batch_job_definition.tf_batch_jd.arn
}

resource "aws_budgets_budget" "tf_monthly" {
  account_id   = "123456789012"
  name         = "tf-monthly"
  budget_type  = "COST"
  limit_amount = "100"
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  cost_types {
    include_credit             = false
    include_discount           = false
    include_other_subscription = false
    include_recurring          = false
    include_refund             = false
    include_subscription       = true
    include_support            = false
    include_tax                = false
    include_upfront            = false
    use_amortized              = false
    use_blended                = false
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = ["alerts@example.com"]
  }

  tags = {
    env = "terraform"
  }
}

output "budgets_budget_name" {
  value = aws_budgets_budget.tf_monthly.name
}

output "budgets_budget_limit_amount" {
  value = aws_budgets_budget.tf_monthly.limit_amount
}

output "budgets_budget_tag_env" {
  value = aws_budgets_budget.tf_monthly.tags["env"]
}

output "organizations_id" {
  value = data.aws_organizations_organization.current.id
}

output "service_discovery_namespace_arn" {
  value = aws_service_discovery_private_dns_namespace.tf_svc_net.arn
}

output "service_discovery_namespace_tag_env" {
  value = aws_service_discovery_private_dns_namespace.tf_svc_net.tags["env"]
}

output "service_discovery_service_arn" {
  value = aws_service_discovery_service.tf_svc.arn
}

output "service_discovery_service_tag_tier" {
  value = aws_service_discovery_service.tf_svc.tags["tier"]
}

output "service_discovery_instance_id" {
  value = aws_service_discovery_instance.tf_svc_instance.instance_id
}

output "service_discovery_http_namespace_arn" {
  value = aws_service_discovery_http_namespace.tf_http_ns.arn
}

output "service_discovery_http_service_custom_health_configured" {
  value = tostring(length(aws_service_discovery_service.tf_http_svc.health_check_custom_config) == 1)
}

output "service_discovery_namespace_lookup_id" {
  value = data.aws_service_discovery_dns_namespace.tf_svc_net_lookup.id
}

output "service_discovery_http_namespace_lookup_id" {
  value = data.aws_service_discovery_http_namespace.tf_http_ns_lookup.id
}

output "service_discovery_service_lookup_id" {
  value = data.aws_service_discovery_service.tf_svc_lookup.id
}

output "service_discovery_namespace_id" {
  value = aws_service_discovery_private_dns_namespace.tf_svc_net.id
}

output "service_discovery_http_namespace_id" {
  value = aws_service_discovery_http_namespace.tf_http_ns.id
}

output "service_discovery_service_id" {
  value = aws_service_discovery_service.tf_svc.id
}
