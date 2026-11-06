terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.10.0"
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

resource "google_kms_key_ring" "pqc" {
  name     = "tf-kms-pqc-ring"
  location = "global"
}

# A key-encapsulation key: the provider re-reads the version template, so an
# algorithm the create dropped or rewrote shows up as drift.
resource "google_kms_crypto_key" "ml_kem" {
  name     = "tf-ml-kem-768"
  key_ring = google_kms_key_ring.pqc.id
  purpose  = "KEY_ENCAPSULATION"

  version_template {
    algorithm        = "ML_KEM_768"
    protection_level = "SOFTWARE"
  }
}

resource "google_kms_crypto_key" "ed25519" {
  name     = "tf-ed25519"
  key_ring = google_kms_key_ring.pqc.id
  purpose  = "ASYMMETRIC_SIGN"

  version_template {
    algorithm = "EC_SIGN_ED25519"
  }
}

data "google_kms_crypto_key_version" "ed25519" {
  crypto_key = google_kms_crypto_key.ed25519.id
}

# Autokey provisions the key the handle names; the test enables Autokey on the
# project before the apply.
resource "google_kms_key_handle" "bucket" {
  name                   = "tf-bucket-handle"
  location               = "us-central1"
  resource_type_selector = "storage.googleapis.com/Bucket"
}

output "ml_kem_algorithm" {
  value = google_kms_crypto_key.ml_kem.version_template[0].algorithm
}

output "ml_kem_purpose" {
  value = google_kms_crypto_key.ml_kem.purpose
}

output "ed25519_public_key_pem" {
  value = data.google_kms_crypto_key_version.ed25519.public_key[0].pem
}

output "ed25519_public_key_algorithm" {
  value = data.google_kms_crypto_key_version.ed25519.public_key[0].algorithm
}

output "key_handle_kms_key" {
  value = google_kms_key_handle.bucket.kms_key
}
