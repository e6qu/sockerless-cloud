terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.9.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  cloud_quotas_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
  type = string
}

variable "preferred_value" {
  type    = string
  default = "1000000"
}

# Cloud Run's CPU quota in a region no other test deploys to, raised, so it
# bounds nothing the suite runs.
resource "google_cloud_quotas_quota_preference" "run_cpu" {
  parent        = "projects/test-project"
  name          = "tf-run-cpu-asia-northeast1"
  service       = "run.googleapis.com"
  quota_id      = "CpuAllocPerProjectRegion"
  dimensions    = { region = "asia-northeast1" }
  contact_email = "quota-owner@example.com"
  justification = "room for the region's Cloud Run instances"

  quota_config {
    preferred_value = var.preferred_value
  }
}

output "name" {
  value = google_cloud_quotas_quota_preference.run_cpu.name
}

output "granted_value" {
  value = google_cloud_quotas_quota_preference.run_cpu.quota_config[0].granted_value
}

output "reconciling" {
  value = google_cloud_quotas_quota_preference.run_cpu.reconciling
}
