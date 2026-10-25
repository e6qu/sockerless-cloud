terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.8.1"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  cloud_tasks_custom_endpoint = "${var.endpoint}/v2/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_cloud_tasks_queue" "orders" {
  name     = "tf-tasks-orders"
  location = "us-central1"

  rate_limits {
    max_dispatches_per_second = 5
    max_concurrent_dispatches = 3
  }

  retry_config {
    max_attempts       = 6
    max_retry_duration = "120s"
    min_backoff        = "1s"
    max_backoff        = "30s"
    max_doublings      = 2
  }

  stackdriver_logging_config {
    sampling_ratio = 0.5
  }
}

resource "google_cloud_tasks_queue" "routed" {
  name          = "tf-tasks-routed"
  location      = "us-central1"
  desired_state = "PAUSED"

  http_target {
    http_method = "PUT"

    uri_override {
      scheme = "HTTPS"
      host   = "worker.example.com"
      path_override {
        path = "/work"
      }
    }

    header_overrides {
      header {
        key   = "X-Source"
        value = "terraform"
      }
    }

    oidc_token {
      service_account_email = "invoker@test-project.iam.gserviceaccount.com"
      audience              = "https://worker.example.com"
    }
  }
}

output "orders_id" {
  value = google_cloud_tasks_queue.orders.id
}

output "orders_state" {
  value = google_cloud_tasks_queue.orders.state
}

output "orders_burst" {
  value = google_cloud_tasks_queue.orders.rate_limits[0].max_burst_size
}

output "routed_state" {
  value = google_cloud_tasks_queue.routed.state
}
