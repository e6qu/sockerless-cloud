# The tutorial's Google Cloud stack (docs/tutorial.md), the counterpart of the
# AWS one: a VPC network with Cloud NAT, a Compute Engine instance, a Cloud Run
# service, a Cloud Storage bucket, a Firestore document, a Pub/Sub topic and
# subscription, a Cloud Run function and a log-based metric in Cloud Logging.

terraform {
  required_providers {
    archive = {
      source  = "hashicorp/archive"
      version = "2.8.1"
    }
    google = {
      source  = "hashicorp/google"
      version = "8.8.1"
    }
  }
}

variable "endpoint" {
  description = "The Google Cloud simulator's address."
  type        = string
  default     = "http://localhost:4567"
}

variable "credentials" {
  description = "The service-account credential file the provider authenticates with, by path or by contents."
  type        = string
}

# The Debian 12 image family of the machine's architecture.
variable "debian_image_family" {
  type    = string
  default = "debian-12"
}

provider "google" {
  project = "sockerless"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  compute_custom_endpoint         = "${var.endpoint}/compute/v1/"
  cloud_run_v2_custom_endpoint    = "${var.endpoint}/v2/"
  cloudfunctions2_custom_endpoint = "${var.endpoint}/v2/"
  cloud_build_custom_endpoint     = "${var.endpoint}/v1/"
  firestore_custom_endpoint       = "${var.endpoint}/v1/"
  logging_custom_endpoint         = "${var.endpoint}/v2/"
  pubsub_custom_endpoint          = "${var.endpoint}/v1/"
  storage_custom_endpoint         = "${var.endpoint}/storage/v1/"
}

# Network: a custom-mode VPC network whose subnetwork reaches the internet
# through Cloud NAT on a Cloud Router.

resource "google_compute_network" "main" {
  name                    = "tutorial"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "private" {
  name          = "tutorial-private"
  region        = "us-central1"
  network       = google_compute_network.main.id
  ip_cidr_range = "10.44.0.0/24"
}

resource "google_compute_firewall" "app" {
  name    = "tutorial-allow-app"
  network = google_compute_network.main.name

  allow {
    protocol = "tcp"
    ports    = ["8080"]
  }

  source_ranges = [google_compute_subnetwork.private.ip_cidr_range]
}

resource "google_compute_address" "nat" {
  name   = "tutorial-nat"
  region = "us-central1"
}

resource "google_compute_router" "main" {
  name    = "tutorial"
  region  = "us-central1"
  network = google_compute_network.main.id
}

resource "google_compute_router_nat" "main" {
  name                               = "tutorial"
  router                             = google_compute_router.main.name
  region                             = google_compute_router.main.region
  nat_ip_allocate_option             = "MANUAL_ONLY"
  nat_ips                            = [google_compute_address.nat.self_link]
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

# Compute: a Compute Engine instance from the Debian 12 image family.

data "google_compute_image" "debian" {
  family  = var.debian_image_family
  project = "debian-cloud"
}

resource "google_compute_instance" "app" {
  name         = "tutorial"
  machine_type = "e2-micro"
  zone         = "us-central1-a"

  boot_disk {
    initialize_params {
      image = data.google_compute_image.debian.self_link
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.private.id
  }
}

# Containers: a Cloud Run service, the counterpart of an Amazon ECS service on
# AWS Fargate. The provider waits until its revision serves.

resource "google_cloud_run_v2_service" "web" {
  name                = "tutorial-web"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image   = "mirror.gcr.io/library/busybox:latest"
      command = ["httpd", "-f", "-p", "8080"]

      ports {
        container_port = 8080
      }
    }
  }
}

# Storage: a Cloud Storage bucket holding one object, and a Firestore document.

resource "google_storage_bucket" "assets" {
  name                        = "tutorial-assets"
  location                    = "us-central1"
  force_destroy               = true
  uniform_bucket_level_access = true
}

resource "google_storage_bucket_object" "hello" {
  name    = "hello.txt"
  bucket  = google_storage_bucket.assets.name
  content = "Hello from Cloud Storage"
}

data "google_storage_bucket_object_content" "hello" {
  name   = google_storage_bucket_object.hello.name
  bucket = google_storage_bucket_object.hello.bucket
}

resource "google_firestore_document" "first" {
  collection  = "tutorial-orders"
  document_id = "order-1"
  fields      = jsonencode({ status = { stringValue = "placed" } })
}

# Messaging and functions: a Pub/Sub topic with a pull subscription, and a
# Cloud Run function built from source in the bucket.

resource "google_pubsub_topic" "orders" {
  name = "tutorial-orders"
}

resource "google_pubsub_subscription" "orders" {
  name                 = "tutorial-orders"
  topic                = google_pubsub_topic.orders.id
  ack_deadline_seconds = 30
}

data "archive_file" "worker" {
  type        = "zip"
  output_path = "${path.module}/.terraform/worker.zip"

  source {
    filename = "index.js"
    content  = <<-JS
      exports.hello = (req, res) => {
        res.status(200).send("hello");
      };
    JS
  }
}

resource "google_storage_bucket_object" "worker_source" {
  name   = "worker-${data.archive_file.worker.output_md5}.zip"
  bucket = google_storage_bucket.assets.name
  source = data.archive_file.worker.output_path
}

resource "google_cloudfunctions2_function" "worker" {
  name     = "tutorial-worker"
  location = "us-central1"

  build_config {
    runtime     = "nodejs20"
    entry_point = "hello"
    source {
      storage_source {
        bucket = google_storage_bucket.assets.name
        object = google_storage_bucket_object.worker_source.name
      }
    }
  }

  service_config {
    max_instance_count = 1
    available_memory   = "256M"
    timeout_seconds    = 60
  }
}

# Monitoring: a log-based metric counting the function's errors.

resource "google_logging_metric" "worker_errors" {
  name   = "tutorial-worker-errors"
  filter = "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"tutorial-worker\" AND severity>=ERROR"

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

output "nat_ip" {
  value = google_compute_address.nat.address
}

output "instance_status" {
  value = google_compute_instance.app.current_status
}

output "instance_private_ip" {
  value = google_compute_instance.app.network_interface[0].network_ip
}

output "run_service_uri" {
  value = google_cloud_run_v2_service.web.uri
}

output "storage_object_content" {
  value = data.google_storage_bucket_object_content.hello.content
}

output "firestore_document" {
  value = google_firestore_document.first.path
}

output "subscription" {
  value = google_pubsub_subscription.orders.id
}

output "function_state" {
  value = google_cloudfunctions2_function.worker.state
}

output "function_url" {
  value = google_cloudfunctions2_function.worker.url
}

output "log_metric" {
  value = google_logging_metric.worker_errors.id
}
