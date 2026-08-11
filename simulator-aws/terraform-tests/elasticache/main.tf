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
