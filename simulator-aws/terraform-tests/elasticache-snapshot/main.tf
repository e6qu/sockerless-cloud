terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.72.0"
    }
  }
}

variable "endpoint" {
  type = string
}

# 1 runs the source cluster, 2 destroys it and so takes its final snapshot,
# and 3 restores the snapshot into a cluster, a replication group and a
# serverless cache.
variable "stage" {
  type = number
}

variable "snapshot_arn" {
  type    = string
  default = ""
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

resource "aws_elasticache_cluster" "source" {
  count                     = var.stage == 1 ? 1 : 0
  cluster_id                = "tf-ec-snapshot-source"
  engine                    = "redis"
  node_type                 = "cache.t3.micro"
  num_cache_nodes           = 1
  final_snapshot_identifier = "tf-ec-final"
}

resource "aws_elasticache_cluster" "restored" {
  count           = var.stage == 3 ? 1 : 0
  cluster_id      = "tf-ec-restored"
  engine          = "redis"
  node_type       = "cache.t3.micro"
  num_cache_nodes = 1
  snapshot_name   = "tf-ec-final"
}

resource "aws_elasticache_replication_group" "restored" {
  count                = var.stage == 3 ? 1 : 0
  replication_group_id = "tf-ec-restored-group"
  description          = "restored by terraform"
  engine               = "redis"
  node_type            = "cache.t3.micro"
  num_cache_clusters   = 1
  port                 = 6379
  snapshot_name        = "tf-ec-final"
}

resource "aws_elasticache_serverless_cache" "restored" {
  count                    = var.stage == 3 ? 1 : 0
  name                     = "tf-ec-restored-serverless"
  engine                   = "valkey"
  snapshot_arns_to_restore = [var.snapshot_arn]
}

output "source_arn" {
  value = try(aws_elasticache_cluster.source[0].arn, "")
}
output "source_address" {
  value = try(aws_elasticache_cluster.source[0].cache_nodes[0].address, "")
}
output "source_port" {
  value = try(tostring(aws_elasticache_cluster.source[0].cache_nodes[0].port), "")
}
output "restored_address" {
  value = try(aws_elasticache_cluster.restored[0].cache_nodes[0].address, "")
}
output "restored_port" {
  value = try(tostring(aws_elasticache_cluster.restored[0].cache_nodes[0].port), "")
}
output "group_primary_address" {
  value = try(aws_elasticache_replication_group.restored[0].primary_endpoint_address, "")
}
output "group_port" {
  value = try(tostring(aws_elasticache_replication_group.restored[0].port), "")
}
output "serverless_address" {
  value = try(aws_elasticache_serverless_cache.restored[0].endpoint[0].address, "")
}
output "serverless_port" {
  value = try(tostring(aws_elasticache_serverless_cache.restored[0].endpoint[0].port), "")
}
