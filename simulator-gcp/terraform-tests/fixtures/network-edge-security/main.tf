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

  compute_custom_endpoint = "${var.endpoint}/compute/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_compute_region_security_policy" "edge" {
  name        = "tf-edge-policy"
  region      = "us-central1"
  description = "network edge security policy"
  type        = "CLOUD_ARMOR_NETWORK"
}

resource "google_compute_network_edge_security_service" "edge" {
  name            = "tf-edge-service"
  region          = "us-central1"
  description     = "network edge security service"
  security_policy = google_compute_region_security_policy.edge.self_link
}

data "google_compute_service_attachments" "regional" {
  region = "us-central1"
}

output "edge_service_id" {
  value = google_compute_network_edge_security_service.edge.id
}

output "edge_service_policy" {
  value = google_compute_network_edge_security_service.edge.security_policy
}

output "service_attachment_count" {
  value = length(data.google_compute_service_attachments.regional.service_attachments)
}
