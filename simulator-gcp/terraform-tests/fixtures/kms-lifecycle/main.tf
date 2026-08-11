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

  kms_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_kms_key_ring" "tf_ring" {
  name     = "tf-kms-ring"
  location = "global"
}

resource "google_kms_crypto_key" "tf_key" {
  name            = "tf-kms-key"
  key_ring        = google_kms_key_ring.tf_ring.id
  rotation_period = "604800s"
}

output "key_ring_id" {
  value = google_kms_key_ring.tf_ring.id
}

output "crypto_key_id" {
  value = google_kms_crypto_key.tf_key.id
}

output "crypto_key_purpose" {
  value = google_kms_crypto_key.tf_key.purpose
}
