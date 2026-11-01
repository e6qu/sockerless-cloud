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
    elasticache = var.endpoint
  }
}

resource "aws_elasticache_cluster" "tf_cache" {
  cluster_id      = "tf-cache"
  engine          = "redis"
  engine_version  = "7.1"
  node_type       = "cache.t3.micro"
  num_cache_nodes = 1
  port            = 6379

  tags = {
    env = "terraform"
  }
}

resource "aws_elasticache_replication_group" "tf_group" {
  replication_group_id       = "tf-group"
  description                = "replicated by terraform"
  engine                     = "valkey"
  node_type                  = "cache.t3.micro"
  num_cache_clusters         = 2
  automatic_failover_enabled = true
  port                       = 6385
}

resource "aws_elasticache_user" "tf_default" {
  user_id       = "tf-rbac-default"
  user_name     = "default"
  engine        = "redis"
  access_string = "off ~* +@all"
  passwords     = ["tf-rbac-default-password"]
}

resource "aws_elasticache_user" "tf_app" {
  user_id       = "tf-rbac-app"
  user_name     = "tf-app"
  engine        = "redis"
  access_string = "on ~app:* +@all"

  authentication_mode {
    type      = "password"
    passwords = ["tf-rbac-app-password-001"]
  }
}

resource "aws_elasticache_user_group" "tf_users" {
  user_group_id = "tf-rbac-users"
  engine        = "redis"
  user_ids      = [aws_elasticache_user.tf_default.user_id, aws_elasticache_user.tf_app.user_id]
}

resource "aws_elasticache_replication_group" "tf_rbac" {
  replication_group_id       = "tf-rbac"
  description                = "user groups in the engine"
  engine                     = "redis"
  node_type                  = "cache.t3.micro"
  num_cache_clusters         = 1
  port                       = 6386
  transit_encryption_enabled = true
  user_group_ids             = [aws_elasticache_user_group.tf_users.user_group_id]
}

resource "aws_elasticache_serverless_cache" "tf_serverless" {
  name                 = "tf-serverless"
  engine               = "valkey"
  major_engine_version = "8"
}

output "elasticache_cluster_arn" {
  value = aws_elasticache_cluster.tf_cache.arn
}
output "elasticache_cluster_engine" {
  value = aws_elasticache_cluster.tf_cache.engine
}
output "elasticache_cluster_port" {
  value = tostring(aws_elasticache_cluster.tf_cache.port)
}
output "elasticache_cluster_tags_env" {
  value = aws_elasticache_cluster.tf_cache.tags["env"]
}
output "elasticache_cluster_node_address" {
  value = aws_elasticache_cluster.tf_cache.cache_nodes[0].address
}
output "elasticache_cluster_node_port" {
  value = tostring(aws_elasticache_cluster.tf_cache.cache_nodes[0].port)
}
output "replication_group_primary_address" {
  value = aws_elasticache_replication_group.tf_group.primary_endpoint_address
}
output "replication_group_reader_address" {
  value = aws_elasticache_replication_group.tf_group.reader_endpoint_address
}
output "replication_group_port" {
  value = tostring(aws_elasticache_replication_group.tf_group.port)
}
output "replication_group_members" {
  value = join(",", sort(aws_elasticache_replication_group.tf_group.member_clusters))
}
output "serverless_endpoint_address" {
  value = aws_elasticache_serverless_cache.tf_serverless.endpoint[0].address
}
output "serverless_endpoint_port" {
  value = tostring(aws_elasticache_serverless_cache.tf_serverless.endpoint[0].port)
}
output "rbac_primary_address" {
  value = aws_elasticache_replication_group.tf_rbac.primary_endpoint_address
}
output "rbac_user_group_ids" {
  value = join(",", aws_elasticache_replication_group.tf_rbac.user_group_ids)
}
