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

variable "grant_invoker" {
  description = "Whether the invoker service account holds roles/run.invoker on the service"
  type        = bool
  default     = true
}

resource "google_cloud_run_v2_service" "private" {
  name                = "tf-crv2-invoker-iam"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image = var.image
      args  = ["echo-request"]
    }
  }
}

resource "google_service_account" "invoker" {
  account_id   = "tf-run-invoker"
  display_name = "Cloud Run invoker"
}

resource "google_service_account" "bystander" {
  account_id   = "tf-run-bystander"
  display_name = "Holds no role on the service"
}

resource "google_cloud_run_v2_service_iam_member" "invoker" {
  count    = var.grant_invoker ? 1 : 0
  project  = google_cloud_run_v2_service.private.project
  location = google_cloud_run_v2_service.private.location
  name     = google_cloud_run_v2_service.private.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.invoker.email}"
}

# The ID tokens each service account presents at the service URL, minted
# through the IAM Service Account Credentials generateIdToken method.
data "google_service_account_id_token" "invoker" {
  target_service_account = google_service_account.invoker.email
  target_audience        = google_cloud_run_v2_service.private.uri
  include_email          = true
}

data "google_service_account_id_token" "bystander" {
  target_service_account = google_service_account.bystander.email
  target_audience        = google_cloud_run_v2_service.private.uri
  include_email          = true
}

output "uri" {
  value = google_cloud_run_v2_service.private.uri
}

output "invoker_member" {
  value = "serviceAccount:${google_service_account.invoker.email}"
}

output "invoker_id_token" {
  value     = data.google_service_account_id_token.invoker.id_token
  sensitive = true
}

output "bystander_id_token" {
  value     = data.google_service_account_id_token.bystander.id_token
  sensitive = true
}

output "service_name" {
  value = google_cloud_run_v2_service.private.name
}
