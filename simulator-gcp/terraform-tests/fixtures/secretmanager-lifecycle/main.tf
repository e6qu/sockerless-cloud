terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  secret_manager_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "secret_label_env" {
  description = "Secret Manager label value used to exercise UpdateSecret."
  type        = string
}

resource "google_secret_manager_secret" "tf_secret" {
  secret_id       = "tf-update-secret"
  deletion_policy = "DELETE"

  labels = {
    env = var.secret_label_env
  }

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "tf_secret_v1" {
  secret          = google_secret_manager_secret.tf_secret.id
  secret_data     = "tf-test-secret-payload"
  deletion_policy = "DELETE"
}

output "secret_label_env" {
  value = google_secret_manager_secret.tf_secret.labels.env
}

output "secret_id" {
  value = google_secret_manager_secret.tf_secret.id
}
