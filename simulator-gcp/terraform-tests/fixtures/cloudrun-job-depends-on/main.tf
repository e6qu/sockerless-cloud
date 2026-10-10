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
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

# The main container makes one connection attempt to the server, which opens
# its port three seconds after it starts, so the job succeeds only when the
# main container starts after the server passed its startup probe.
resource "google_cloud_run_v2_job" "ordered" {
  name                = "tf-crv2-job-depends-on"
  location            = "us-central1"
  deletion_protection = false

  template {
    template {
      max_retries = 0
      timeout     = "60s"

      containers {
        name       = "main"
        image      = "public.ecr.aws/docker/library/alpine:latest"
        command    = ["nc", "-z", "127.0.0.1", "9090"]
        depends_on = ["server"]
      }

      containers {
        name    = "server"
        image   = "public.ecr.aws/docker/library/alpine:latest"
        command = ["sh", "-c", "sleep 3; exec nc -lk -p 9090 -e true"]

        startup_probe {
          period_seconds    = 1
          timeout_seconds   = 1
          failure_threshold = 30

          tcp_socket {
            port = 9090
          }
        }
      }
    }
  }
}

output "job_name" {
  value = google_cloud_run_v2_job.ordered.id
}

output "depends_on" {
  value = google_cloud_run_v2_job.ordered.template[0].template[0].containers[0].depends_on
}
