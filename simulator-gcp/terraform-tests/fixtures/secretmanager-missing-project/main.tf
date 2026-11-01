terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.9.0"
    }
  }
}

provider "google" {
  project = "tf-secret-no-such-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  secret_manager_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
  type = string
}

resource "google_secret_manager_secret" "orphan" {
  secret_id       = "tf-orphan-secret"
  deletion_policy = "DELETE"

  replication {
    auto {}
  }
}
