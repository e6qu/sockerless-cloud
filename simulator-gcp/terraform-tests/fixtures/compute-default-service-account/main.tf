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

  compute_custom_endpoint  = "${var.endpoint}/compute/v1/"
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

data "google_compute_default_service_account" "default" {}

output "email" {
  value = data.google_compute_default_service_account.default.email
}

output "name" {
  value = data.google_compute_default_service_account.default.name
}

output "display_name" {
  value = data.google_compute_default_service_account.default.display_name
}

output "unique_id" {
  value = data.google_compute_default_service_account.default.unique_id
}

output "member" {
  value = data.google_compute_default_service_account.default.member
}
