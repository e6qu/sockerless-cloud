terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.1"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  kms_custom_endpoint = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
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

# A conditional role binding on the key: the provider reads the policy back at
# version 3 and matches the binding by role, member and condition, so a
# simulator that dropped or rewrote the condition shows up as drift here.
resource "google_kms_crypto_key_iam_member" "tf_key_viewer" {
  crypto_key_id = google_kms_crypto_key.tf_key.id
  role          = "roles/cloudkms.viewer"
  member        = "serviceAccount:tf-kms-cond@test-project.iam.gserviceaccount.com"

  condition {
    title      = "keys only"
    expression = "resource.type == \"cloudkms.googleapis.com/CryptoKey\""
  }
}

output "key_ring_id" {
  value = google_kms_key_ring.tf_ring.id
}

output "crypto_key_iam_condition_title" {
  value = google_kms_crypto_key_iam_member.tf_key_viewer.condition[0].title
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
