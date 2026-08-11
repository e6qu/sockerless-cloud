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

  # google_service_account is routed through iambeta.NewClient; the
  # google_service_account_key resource is routed through the handwritten IAM
  # client, which honours iam_custom_endpoint.
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

resource "google_service_account" "key_owner" {
  account_id   = "tf-key-owner-sa"
  display_name = "tf service-account key owner"
}

# The credential an operator provisions for a non-interactive client. The
# provider creates it through projects.serviceAccounts.keys.create and reads it
# back through keys.get; private_key carries the base64 PKCS#8 credential file
# IAM returns once, at creation.
resource "google_service_account_key" "key" {
  service_account_id = google_service_account.key_owner.name
  key_algorithm      = "KEY_ALG_RSA_2048"
}

output "service_account_email" {
  value = google_service_account.key_owner.email
}

output "key_name" {
  value = google_service_account_key.key.name
}

output "key_algorithm" {
  value = google_service_account_key.key.key_algorithm
}

output "key_private_key" {
  value     = google_service_account_key.key.private_key
  sensitive = true
}
