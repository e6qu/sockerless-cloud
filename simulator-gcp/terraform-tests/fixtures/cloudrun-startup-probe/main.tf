terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.5.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  cloud_run_v2_custom_endpoint = "${var.endpoint}/v2/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "image" {
  description = "Workload image that answers every request with its method and path"
  type        = string
}

resource "google_cloud_run_v2_service" "probed" {
  name                = "tf-crv2-probed"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image = var.image
      args  = ["echo-request"]

      ports {
        container_port = 8080
      }

      startup_probe {
        period_seconds    = 1
        timeout_seconds   = 1
        failure_threshold = 10

        http_get {
          path = "/healthz"
          port = 8080
        }
      }
    }
  }
}

resource "google_cloud_run_v2_service" "never_ready" {
  name                = "tf-crv2-never-ready"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image = var.image
      args  = ["echo-request"]

      startup_probe {
        period_seconds    = 1
        timeout_seconds   = 1
        failure_threshold = 2

        tcp_socket {
          port = 9999
        }
      }
    }
  }
}

output "probed_uri" {
  value = google_cloud_run_v2_service.probed.uri
}

output "never_ready_uri" {
  value = google_cloud_run_v2_service.never_ready.uri
}
