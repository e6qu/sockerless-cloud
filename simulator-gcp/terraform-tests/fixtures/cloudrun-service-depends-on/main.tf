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
  description = "Workload image whose after-sidecar mode serves only when the sidecar listened as it started"
  type        = string
}

# The ingress container checks once, as it starts, that the sidecar listens on
# 9090 and exits if not; the sidecar opens the port three seconds after it
# starts. The service answers only when its instance starts the ingress after
# the sidecar passed its startup probe.
resource "google_cloud_run_v2_service" "ordered" {
  name                = "tf-crv2-service-depends-on"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      name       = "ingress"
      image      = var.image
      args       = ["after-sidecar", "tf-sidecar-started-first"]
      depends_on = ["sidecar"]

      ports {
        container_port = 8080
      }
    }

    containers {
      name    = "sidecar"
      image   = "alpine:latest"
      command = ["sh", "-c", "sleep 3; exec nc -lk -p 9090 -e true"]

      startup_probe {
        period_seconds    = 1
        timeout_seconds   = 1
        failure_threshold = 30

        tcp_socket {
          port = 9090
        }
      }
    }
  }
}

resource "google_service_account" "invoker" {
  account_id   = "tf-depends-on-invoker"
  display_name = "Invokes the ordered service"
}

resource "google_cloud_run_v2_service_iam_member" "invoker" {
  location = "us-central1"
  name     = google_cloud_run_v2_service.ordered.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.invoker.email}"
}

data "google_service_account_id_token" "ordered" {
  target_service_account = google_service_account.invoker.email
  target_audience        = google_cloud_run_v2_service.ordered.uri
}

output "id_token" {
  value     = data.google_service_account_id_token.ordered.id_token
  sensitive = true
}

output "uri" {
  value = google_cloud_run_v2_service.ordered.uri
}

output "depends_on" {
  value = google_cloud_run_v2_service.ordered.template[0].containers[0].depends_on
}
