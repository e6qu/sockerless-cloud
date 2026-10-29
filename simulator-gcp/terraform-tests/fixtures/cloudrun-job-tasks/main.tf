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

  cloud_run_v2_custom_endpoint = "${var.endpoint}/v2/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
  type = string
}

# run_execution_token starts the execution tf-crv2-tasks-job-run, and the
# apply waits for it to complete: three tasks, two at once, the second
# failing its first attempt and passing its retry.
resource "google_cloud_run_v2_job" "tasks" {
  name                = "tf-crv2-tasks-job"
  location            = "us-central1"
  deletion_protection = false
  run_execution_token = "run"

  template {
    task_count  = 3
    parallelism = 2

    template {
      max_retries = 1
      timeout     = "60s"

      containers {
        image   = "mirror.gcr.io/library/alpine:latest"
        command = ["sh", "-c", "[ \"$CLOUD_RUN_TASK_INDEX\" = 1 ] && [ \"$CLOUD_RUN_TASK_ATTEMPT\" = 0 ] && exit 6; echo task $CLOUD_RUN_TASK_INDEX of $CLOUD_RUN_TASK_COUNT"]
      }
    }
  }
}

output "job_name" {
  value = google_cloud_run_v2_job.tasks.id
}
