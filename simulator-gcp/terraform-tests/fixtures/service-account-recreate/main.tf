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

  iam_beta_custom_endpoint = "${var.endpoint}/v1/"
  iam_custom_endpoint      = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_service_account" "recreated" {
  account_id   = "tf-recreated-sa"
  display_name = "tf recreated service account"
}

output "email" {
  value = google_service_account.recreated.email
}

output "unique_id" {
  value = google_service_account.recreated.unique_id
}
