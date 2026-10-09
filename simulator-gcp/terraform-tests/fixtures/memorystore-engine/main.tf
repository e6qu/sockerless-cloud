terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.6.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  redis_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "replica_count" {
  description = "Read replicas the instance runs."
  type        = number
}

variable "shard_count" {
  description = "Shards the cluster runs."
  type        = number
}

resource "google_redis_instance" "encrypted" {
  name               = "tf-redis-engine"
  tier               = "STANDARD_HA"
  memory_size_gb     = 1
  region             = "us-central1"
  redis_version      = "REDIS_7_2"
  read_replicas_mode = "READ_REPLICAS_ENABLED"
  replica_count      = var.replica_count
  auth_enabled       = true

  transit_encryption_mode = "SERVER_AUTHENTICATION"

  persistence_config {
    persistence_mode    = "RDB"
    rdb_snapshot_period = "TWELVE_HOURS"
  }
}

resource "google_redis_cluster" "sharded" {
  name                        = "tf-redis-cluster"
  region                      = "us-central1"
  shard_count                 = var.shard_count
  replica_count               = 1
  deletion_protection_enabled = false
  transit_encryption_mode     = "TRANSIT_ENCRYPTION_MODE_SERVER_AUTHENTICATION"

  psc_configs {
    network = "projects/test-project/global/networks/default"
  }

  persistence_config {
    mode = "AOF"
    aof_config {
      append_fsync = "EVERYSEC"
    }
  }
}

output "instance_host" {
  value = google_redis_instance.encrypted.host
}

output "instance_port" {
  value = google_redis_instance.encrypted.port
}

output "instance_replica_count" {
  value = google_redis_instance.encrypted.replica_count
}

output "instance_auth_string" {
  value     = google_redis_instance.encrypted.auth_string
  sensitive = true
}

output "instance_server_ca" {
  value = google_redis_instance.encrypted.server_ca_certs[0].cert
}

output "instance_persistence_mode" {
  value = google_redis_instance.encrypted.persistence_config[0].persistence_mode
}

output "cluster_shard_count" {
  value = google_redis_cluster.sharded.shard_count
}

output "cluster_discovery_address" {
  value = google_redis_cluster.sharded.discovery_endpoints[0].address
}

output "cluster_discovery_port" {
  value = google_redis_cluster.sharded.discovery_endpoints[0].port
}

output "cluster_server_ca" {
  value = google_redis_cluster.sharded.managed_server_ca[0].ca_certs[0].certificates[0]
}

output "cluster_persistence_mode" {
  value = google_redis_cluster.sharded.persistence_config[0].mode
}
