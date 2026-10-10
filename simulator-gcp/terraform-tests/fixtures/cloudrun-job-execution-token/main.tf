terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.6.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  cloud_run_v2_custom_endpoint = "${var.endpoint}/v2/"
  storage_custom_endpoint      = "${var.endpoint}/storage/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "token" {
  type = string
}

resource "google_storage_bucket" "mounted" {
  name          = "tf-crv2-job-token-volume"
  location      = "us-central1"
  force_destroy = true
}

# run_execution_token starts the execution tf-crv2-token-job-<token>, and the
# apply waits for it to complete, so its write is in the bucket when the apply
# returns.
resource "google_cloud_run_v2_job" "token" {
  name                = "tf-crv2-token-job"
  location            = "us-central1"
  deletion_protection = false
  run_execution_token = var.token

  template {
    template {
      max_retries = 0
      timeout     = "60s"

      containers {
        image   = "public.ecr.aws/docker/library/alpine:latest"
        command = ["sh", "-c", "echo ran > /mnt/bucket/ran-${var.token}"]

        volume_mounts {
          name       = "bucket"
          mount_path = "/mnt/bucket"
        }
      }

      volumes {
        name = "bucket"
        gcs {
          bucket    = google_storage_bucket.mounted.name
          read_only = false
        }
      }
    }
  }
}

output "job_name" {
  value = google_cloud_run_v2_job.token.id
}

output "bucket" {
  value = google_storage_bucket.mounted.name
}
