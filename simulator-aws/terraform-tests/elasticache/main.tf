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
