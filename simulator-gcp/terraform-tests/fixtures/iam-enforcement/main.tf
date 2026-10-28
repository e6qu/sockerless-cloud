terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.9.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  iam_beta_custom_endpoint = "${var.endpoint}/v1/"
  iam_custom_endpoint      = "${var.endpoint}/v1/"
  storage_custom_endpoint  = "${var.endpoint}/storage/v1/"
}

# The service account's own credential, once its key exists.
provider "google" {
  alias   = "reader"
  project = "test-project"
  region  = "us-central1"

  credentials           = var.reader_credentials == "" ? var.credentials : var.reader_credentials
  user_project_override = false

  storage_custom_endpoint = "${var.endpoint}/storage/v1/"
}

variable "endpoint" {
  type = string
}

variable "credentials" {
  type = string
}

variable "reader_credentials" {
  type    = string
  default = ""
}

variable "read_object" {
  type    = bool
  default = false
}

variable "write_object" {
  type    = bool
  default = false
}

variable "grant_admin" {
  type    = bool
  default = false
}

resource "google_service_account" "reader" {
  account_id   = "tf-enforce-reader"
  display_name = "IAM enforcement reader"
}

resource "google_service_account_key" "reader" {
  service_account_id = google_service_account.reader.name
}

resource "google_storage_bucket" "guarded" {
  name                        = "tf-iam-enforce-bucket"
  location                    = "US"
  uniform_bucket_level_access = true
  force_destroy               = true
}

resource "google_storage_bucket_object" "seed" {
  bucket  = google_storage_bucket.guarded.name
  name    = "seed.txt"
  content = "seeded"
}

resource "google_storage_bucket_iam_member" "viewer" {
  bucket = google_storage_bucket.guarded.name
  role   = "roles/storage.objectViewer"
  member = google_service_account.reader.member
}

resource "google_storage_bucket_iam_member" "admin" {
  count  = var.grant_admin ? 1 : 0
  bucket = google_storage_bucket.guarded.name
  role   = "roles/storage.objectAdmin"
  member = google_service_account.reader.member
}

data "google_storage_bucket_object_content" "read" {
  count    = var.read_object ? 1 : 0
  provider = google.reader
  bucket   = google_storage_bucket.guarded.name
  name     = google_storage_bucket_object.seed.name
}

resource "google_storage_bucket_object" "written" {
  count    = var.write_object ? 1 : 0
  provider = google.reader
  bucket   = google_storage_bucket.guarded.name
  name     = "written.txt"
  content  = "by the reader"

  depends_on = [google_storage_bucket_iam_member.admin]
}

output "reader_email" {
  value = google_service_account.reader.email
}

output "reader_key" {
  value     = google_service_account_key.reader.private_key
  sensitive = true
}

output "read_content" {
  value = var.read_object ? data.google_storage_bucket_object_content.read[0].content : ""
}
