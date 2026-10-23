terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  cloud_build_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_cloudbuild_worker_pool" "pool" {
  name         = "tf-worker-pool"
  location     = "us-central1"
  display_name = "Terraform worker pool"

  worker_config {
    disk_size_gb   = 100
    machine_type   = "e2-standard-4"
    no_external_ip = false
  }
}

data "google_cloudbuild_worker_pool" "pool" {
  name     = google_cloudbuild_worker_pool.pool.name
  location = google_cloudbuild_worker_pool.pool.location
}

output "data_name" {
  value = data.google_cloudbuild_worker_pool.pool.name
}

output "data_display_name" {
  value = data.google_cloudbuild_worker_pool.pool.display_name
}

output "data_machine_type" {
  value = data.google_cloudbuild_worker_pool.pool.worker_config[0].machine_type
}

output "data_state" {
  value = data.google_cloudbuild_worker_pool.pool.state
}
