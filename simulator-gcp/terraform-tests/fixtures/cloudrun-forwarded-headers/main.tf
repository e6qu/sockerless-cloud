terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.7.0"
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
  description = "Workload image that answers every request with the headers it received"
  type        = string
}

resource "google_cloud_run_v2_service" "echo" {
  name                 = "tf-crv2-forwarded-headers"
  location             = "us-central1"
  deletion_protection  = false
  invoker_iam_disabled = true

  template {
    containers {
      image = var.image
      args  = ["echo-headers"]
    }
  }
}

output "uri" {
  value = google_cloud_run_v2_service.echo.uri
}
