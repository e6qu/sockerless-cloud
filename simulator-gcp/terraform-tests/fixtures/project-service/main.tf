terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.0"
    }
  }
}

provider "google" {
  project = "tf-service-usage"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  service_usage_custom_endpoint       = "${var.endpoint}/v1/"
  resource_manager_custom_endpoint    = "${var.endpoint}/v1/"
  resource_manager_v3_custom_endpoint = "${var.endpoint}/v3/"
  cloud_billing_custom_endpoint       = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_project" "usage" {
  project_id      = "tf-service-usage"
  name            = "tf-service-usage"
  org_id          = "123456789012"
  deletion_policy = "DELETE"
}

resource "google_project_service" "pubsub" {
  project            = google_project.usage.project_id
  service            = "pubsub.googleapis.com"
  disable_on_destroy = true
}

output "id" {
  value = google_project_service.pubsub.id
}
