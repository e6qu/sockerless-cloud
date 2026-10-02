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
  eventarc_custom_endpoint     = "${var.endpoint}/v1/"
  iam_beta_custom_endpoint     = "${var.endpoint}/v1/"
  storage_custom_endpoint      = "${var.endpoint}/storage/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

variable "image" {
  description = "Workload image that writes each CloudEvent it receives to stdout"
  type        = string
}

resource "google_cloud_run_v2_service" "receiver" {
  name                = "tf-audit-receiver"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image = var.image
      args  = ["log-cloudevent"]
    }
  }
}

resource "google_service_account" "trigger" {
  account_id   = "tf-audit-trigger"
  display_name = "Eventarc audit-log trigger"
}

resource "google_cloud_run_v2_service_iam_member" "trigger_invoker" {
  project  = google_cloud_run_v2_service.receiver.project
  location = google_cloud_run_v2_service.receiver.location
  name     = google_cloud_run_v2_service.receiver.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.trigger.email}"
}

resource "google_eventarc_trigger" "bucket_created" {
  name            = "tf-audit-bucket-created"
  location        = "us-central1"
  service_account = google_service_account.trigger.email

  matching_criteria {
    attribute = "type"
    value     = "google.cloud.audit.log.v1.written"
  }
  matching_criteria {
    attribute = "serviceName"
    value     = "storage.googleapis.com"
  }
  matching_criteria {
    attribute = "methodName"
    value     = "storage.buckets.create"
  }
  matching_criteria {
    attribute = "resourceName"
    value     = "/projects/_/buckets/tf-audit-*"
    operator  = "match-path-pattern"
  }

  destination {
    cloud_run_service {
      service = google_cloud_run_v2_service.receiver.name
      region  = "us-central1"
    }
  }

  depends_on = [google_cloud_run_v2_service_iam_member.trigger_invoker]
}

# Created once the trigger exists, so its creation is the call the trigger
# routes.
resource "google_storage_bucket" "audited" {
  name          = "tf-audit-bucket"
  location      = "US-CENTRAL1"
  force_destroy = true

  depends_on = [google_eventarc_trigger.bucket_created]
}

output "service_name" {
  value = google_cloud_run_v2_service.receiver.name
}

output "bucket" {
  value = google_storage_bucket.audited.name
}

output "transport_topic" {
  value = google_eventarc_trigger.bucket_created.transport[0].pubsub[0].topic
}
