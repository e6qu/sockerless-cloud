terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.0.0"
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

# rotation_period is re-read from the API's rotationPeriod on every Read, so
# this output is what CreateCryptoKey stored and GetCryptoKey answered rather
# than the configured literal. A key created without its rotation schedule reads
# back empty here, and re-plans forever.
output "crypto_key_rotation_period" {
  value = google_kms_crypto_key.tf_key.rotation_period
}
