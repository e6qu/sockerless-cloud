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
  pubsub_custom_endpoint       = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "instances" {
  type    = number
  default = 2
}

resource "google_storage_bucket" "mounted" {
  name          = "tf-crv2-worker-pool-volume"
  location      = "us-central1"
  force_destroy = true
}

# Cloud Storage publishes the bucket's object changes as the project's service
# agent, so the topic grants that agent publish before the notification names
# it, and the test receives each object the instances write.
data "google_storage_project_service_account" "gcs_agent" {}

resource "google_pubsub_topic" "objects" {
  name = "tf-crv2-worker-pool-objects"
}

resource "google_pubsub_subscription" "objects" {
  name  = "tf-crv2-worker-pool-objects"
  topic = google_pubsub_topic.objects.id
}

resource "google_pubsub_topic_iam_member" "gcs_agent_publisher" {
  topic  = google_pubsub_topic.objects.id
  role   = "roles/pubsub.publisher"
  member = data.google_storage_project_service_account.gcs_agent.member
}

resource "google_storage_notification" "objects" {
  bucket         = google_storage_bucket.mounted.name
  payload_format = "JSON_API_V1"
  topic          = google_pubsub_topic.objects.id
  event_types    = ["OBJECT_FINALIZE"]
  depends_on     = [google_pubsub_topic_iam_member.gcs_agent_publisher, google_pubsub_subscription.objects]
}

resource "google_cloud_run_v2_worker_pool" "writer" {
  name                = "tf-crv2-worker-pool-writer"
  location            = "us-central1"
  deletion_protection = false
  launch_stage        = "GA"

  scaling {
    manual_instance_count = var.instances
  }

  template {
    containers {
      image   = "alpine:latest"
      command = ["sh", "-c"]
      args = [<<-SCRIPT
        trap 'echo stopped > /mnt/bucket/stopped-$HOSTNAME; exit 0' TERM
        echo started > /mnt/bucket/started-$HOSTNAME
        sleep 2147483647 &
        wait $!
      SCRIPT
      ]

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

  depends_on = [google_storage_notification.objects]
}

output "bucket" {
  value = google_storage_bucket.mounted.name
}

output "subscription" {
  value = google_pubsub_subscription.objects.id
}
