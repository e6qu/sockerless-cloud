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

  eventarc_custom_endpoint = "${var.endpoint}/v1/"
  pubsub_custom_endpoint   = "${var.endpoint}/v1/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_pubsub_topic" "destination" {
  name = "tf-pipeline-destination"
}

resource "google_eventarc_pipeline" "pipeline" {
  location    = "us-central1"
  pipeline_id = "tf-pipeline"

  destinations {
    topic = google_pubsub_topic.destination.id
  }
}

resource "google_eventarc_pipeline_iam_binding" "viewers" {
  location    = google_eventarc_pipeline.pipeline.location
  pipeline_id = google_eventarc_pipeline.pipeline.pipeline_id
  role        = "roles/viewer"
  members     = ["user:viewer@example.com"]
}

resource "google_eventarc_pipeline_iam_member" "editor" {
  location    = google_eventarc_pipeline.pipeline.location
  pipeline_id = google_eventarc_pipeline.pipeline.pipeline_id
  role        = "roles/editor"
  member      = "user:editor@example.com"

  depends_on = [google_eventarc_pipeline_iam_binding.viewers]
}

data "google_eventarc_pipeline_iam_policy" "pipeline" {
  location    = google_eventarc_pipeline.pipeline.location
  pipeline_id = google_eventarc_pipeline.pipeline.pipeline_id

  depends_on = [google_eventarc_pipeline_iam_member.editor]
}

resource "google_pubsub_topic" "second_destination" {
  name = "tf-pipeline-second-destination"
}

resource "google_eventarc_pipeline" "governed" {
  location    = "us-central1"
  pipeline_id = "tf-pipeline-governed"

  destinations {
    topic = google_pubsub_topic.second_destination.id
  }
}

data "google_iam_policy" "governed" {
  binding {
    role    = "roles/viewer"
    members = ["user:auditor@example.com"]
  }
}

resource "google_eventarc_pipeline_iam_policy" "governed" {
  location    = google_eventarc_pipeline.governed.location
  pipeline_id = google_eventarc_pipeline.governed.pipeline_id
  policy_data = data.google_iam_policy.governed.policy_data
}

output "pipeline_policy" {
  value = data.google_eventarc_pipeline_iam_policy.pipeline.policy_data
}

output "governed_policy_etag" {
  value = google_eventarc_pipeline_iam_policy.governed.etag
}
