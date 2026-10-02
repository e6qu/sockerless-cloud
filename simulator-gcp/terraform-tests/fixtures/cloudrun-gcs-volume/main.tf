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

resource "google_storage_bucket" "mounted" {
  name          = "tf-crv2-gcs-volume"
  location      = "us-central1"
  force_destroy = true
}

resource "google_storage_bucket_object" "seed" {
  name         = "seed.txt"
  bucket       = google_storage_bucket.mounted.name
  content      = "seed\n"
  content_type = "text/plain"
}

resource "google_storage_bucket_object" "rename_me" {
  name    = "rename-me.txt"
  bucket  = google_storage_bucket.mounted.name
  content = "renamed\n"
}

resource "google_cloud_run_v2_job" "writer" {
  name                = "tf-crv2-gcs-volume-writer"
  location            = "us-central1"
  deletion_protection = false

  template {
    template {
      max_retries = 0
      timeout     = "60s"

      containers {
        image   = "alpine:latest"
        command = ["sh", "-c"]
        args = [<<-SCRIPT
          set -e
          cat /mnt/bucket/seed.txt > /mnt/results/copy.txt
          echo appended >> /mnt/bucket/seed.txt
          mkdir /mnt/bucket/made
          echo inside > /mnt/bucket/made/inside.txt
          mv /mnt/bucket/rename-me.txt /mnt/bucket/renamed.txt
        SCRIPT
        ]

        volume_mounts {
          name       = "bucket"
          mount_path = "/mnt/bucket"
        }
        volume_mounts {
          name       = "results"
          mount_path = "/mnt/results"
        }
      }

      volumes {
        name = "bucket"
        gcs {
          bucket    = google_storage_bucket.mounted.name
          read_only = false
        }
      }
      volumes {
        name = "results"
        gcs {
          bucket        = google_storage_bucket.mounted.name
          read_only     = false
          mount_options = ["only-dir=results"]
        }
      }
    }
  }

  depends_on = [google_storage_bucket_object.seed, google_storage_bucket_object.rename_me]
}

output "job_name" {
  value = google_cloud_run_v2_job.writer.id
}

output "bucket" {
  value = google_storage_bucket.mounted.name
}

output "mount_options" {
  value = google_cloud_run_v2_job.writer.template[0].template[0].volumes[1].gcs[0].mount_options
}
