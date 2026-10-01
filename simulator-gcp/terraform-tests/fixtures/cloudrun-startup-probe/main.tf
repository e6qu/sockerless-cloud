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

  cloud_run_v2_custom_endpoint    = "${var.endpoint}/v2/"
  iam_beta_custom_endpoint        = "${var.endpoint}/v1/"
  iam_credentials_custom_endpoint = "${var.endpoint}/v1/"
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

resource "google_service_account" "invoker" {
  account_id   = "tf-probe-invoker"
  display_name = "Invokes the probed services"
}

resource "google_cloud_run_v2_service_iam_member" "invoker" {
  for_each = {
    probed      = google_cloud_run_v2_service.probed.name
    never_ready = google_cloud_run_v2_service.never_ready.name
  }
  location = "us-central1"
  name     = each.value
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.invoker.email}"
}

data "google_service_account_id_token" "probed" {
  target_service_account = google_service_account.invoker.email
  target_audience        = google_cloud_run_v2_service.probed.uri
}

data "google_service_account_id_token" "never_ready" {
  target_service_account = google_service_account.invoker.email
  target_audience        = google_cloud_run_v2_service.never_ready.uri
}

output "probed_id_token" {
  value     = data.google_service_account_id_token.probed.id_token
  sensitive = true
}

output "never_ready_id_token" {
  value     = data.google_service_account_id_token.never_ready.id_token
  sensitive = true
}

output "probed_uri" {
  value = google_cloud_run_v2_service.probed.uri
}

output "never_ready_uri" {
  value = google_cloud_run_v2_service.never_ready.uri
}
