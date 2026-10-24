terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  cloud_scheduler_custom_endpoint = "${var.endpoint}/v1/"
  pubsub_custom_endpoint          = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_pubsub_topic" "ticks" {
  name = "tf-scheduler-ticks"
}

resource "google_cloud_scheduler_job" "publish" {
  name        = "tf-scheduler-publish"
  description = "publishes a tick every quarter hour"
  schedule    = "*/15 * * * *"
  time_zone   = "Europe/London"
  region      = "us-central1"

  retry_config {
    retry_count          = 3
    min_backoff_duration = "10s"
    max_backoff_duration = "300s"
    max_doublings        = 2
  }

  pubsub_target {
    topic_name = google_pubsub_topic.ticks.id
    data       = base64encode("tick")
    attributes = {
      origin = "terraform"
    }
  }
}

resource "google_cloud_scheduler_job" "call" {
  name             = "tf-scheduler-call"
  schedule         = "0 6 * * *"
  region           = "us-central1"
  attempt_deadline = "60s"
  paused           = true

  http_target {
    uri         = "https://hooks.example.com/daily"
    http_method = "POST"
    body        = base64encode("{\"run\":\"daily\"}")
    headers = {
      "Content-Type" = "application/json"
    }
  }
}

output "publish_id" {
  value = google_cloud_scheduler_job.publish.id
}

output "publish_state" {
  value = google_cloud_scheduler_job.publish.state
}

output "call_state" {
  value = google_cloud_scheduler_job.call.state
}

output "call_deadline" {
  value = google_cloud_scheduler_job.call.attempt_deadline
}
