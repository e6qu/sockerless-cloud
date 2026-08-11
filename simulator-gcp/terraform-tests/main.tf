terraform {
  required_providers {
    archive = {
      source = "hashicorp/archive"
    }
    google = {
      source = "hashicorp/google"
    }
    google-beta = {
      source = "hashicorp/google-beta"
    }
    random = {
      source = "hashicorp/random"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  compute_custom_endpoint           = "${var.endpoint}/compute/v1/"
  big_query_custom_endpoint         = "${var.endpoint}/bigquery/v2/"
  cloud_build_custom_endpoint       = "${var.endpoint}/v1/"
  cloudfunctions2_custom_endpoint   = "${var.endpoint}/v2/"
  dns_custom_endpoint               = "${var.endpoint}/dns/v1/"
  artifact_registry_custom_endpoint = "${var.endpoint}/v1/"
  cloud_run_v2_custom_endpoint      = "${var.endpoint}/v2/"
  eventarc_custom_endpoint          = "${var.endpoint}/v1/"
  firestore_custom_endpoint         = "${var.endpoint}/v1/"
  logging_custom_endpoint           = "${var.endpoint}/v2/"
  pubsub_custom_endpoint            = "${var.endpoint}/v1/"
  storage_custom_endpoint           = "${var.endpoint}/storage/v1/"
  secret_manager_custom_endpoint    = "${var.endpoint}/v1/"
  redis_custom_endpoint             = "${var.endpoint}/v1/"
  sql_custom_endpoint               = "${var.endpoint}/sql/v1beta4/"
  vpc_access_custom_endpoint        = "${var.endpoint}/v1/"
  spanner_custom_endpoint           = "${var.endpoint}/spanner/v1/"
  bigtable_custom_endpoint          = "${var.endpoint}/v2/"
  dataflow_custom_endpoint          = "${var.endpoint}/v1b3/"
  # iam_beta_custom_endpoint routes the `google_service_account` resource's
  # iambeta.NewClient → iam.googleapis.com surface; without it the resource
  # hits real iam.googleapis.com regardless of `iam_custom_endpoint`.
  iam_beta_custom_endpoint = "${var.endpoint}/v1/"

  # iam_custom_endpoint routes the handwritten IAM client that backs
  # `google_service_account_key` (projects.serviceAccounts.keys.create/.get).
  iam_custom_endpoint = "${var.endpoint}/v1/"

  # google_project speaks the Cloud Resource Manager v1 wire (create +
  # operation wait + read + delete) and reads Cloud Billing's
  # projects.getBillingInfo on every Read — the handwritten cloudbilling
  # client honors cloud_billing_custom_endpoint (NOT billing_custom_endpoint,
  # which routes the Billing Budgets product).
  resource_manager_custom_endpoint = "${var.endpoint}/v1/"
  cloud_billing_custom_endpoint    = "${var.endpoint}/v1/"

  # google_folder speaks the Cloud Resource Manager v3 folders collection
  # through its own generated client, which honors
  # resource_manager_v3_custom_endpoint — without it the resource reaches real
  # cloudresourcemanager.googleapis.com regardless of the v1 override above.
  # google_folder_organization_policy addresses the same folder over the v1
  # org-policy verbs, which resource_manager_custom_endpoint routes.
  resource_manager_v3_custom_endpoint = "${var.endpoint}/v3/"

}

provider "google-beta" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  api_gateway_custom_endpoint = "${var.endpoint}/v1/"
}

# ---------- Compute (network + disks) ----------

resource "google_compute_network" "main" {
  name                    = "tf-test-network"
  auto_create_subnetworks = false
}

# Compute disk CRUD. Exercises the same wire shape gcloud uses
# (zoneOp with operationType, full zone URL, kind=compute#operation)
# from a third consumer.
resource "google_compute_disk" "tf_disk" {
  name = "tf-test-disk"
  zone = "us-central1-a"
  size = 10
  type = "pd-balanced"
}

# Subnetwork inside the test network. Real runner pods get attached to
# subnets; the sim's POST /compute/v1/projects/{}/regions/{}/subnetworks
# is the wire surface terraform-provider-google calls.
resource "google_compute_subnetwork" "tf_subnet" {
  name          = "tf-test-subnet"
  region        = "us-central1"
  network       = google_compute_network.main.id
  ip_cidr_range = "10.42.0.0/16"
}

resource "google_vpc_access_connector" "tf_connector" {
  name          = "tf-vpc-connector"
  region        = "us-central1"
  network       = google_compute_network.main.name
  ip_cidr_range = "10.43.0.0/28"
}

# Global firewall rule attached to the network. Runner backends create
# matching allow-rules for SSH / health-check ranges; this exercises the
# /compute/v1/projects/{}/global/firewalls surface.
resource "google_compute_firewall" "tf_fw" {
  name    = "tf-test-fw-allow-ssh"
  network = google_compute_network.main.name

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = ["0.0.0.0/0"]
}

resource "google_compute_address" "tf_nat_ip" {
  name         = "tf-nat-ip"
  region       = "us-central1"
  address_type = "EXTERNAL"
  network_tier = "PREMIUM"
}

resource "google_compute_router" "tf_nat_router" {
  name    = "tf-nat-router"
  region  = "us-central1"
  network = google_compute_network.main.id
}

resource "google_compute_router_nat" "tf_nat" {
  name                               = "tf-router-nat"
  router                             = google_compute_router.tf_nat_router.name
  region                             = google_compute_router.tf_nat_router.region
  nat_ip_allocate_option             = "MANUAL_ONLY"
  nat_ips                            = [google_compute_address.tf_nat_ip.self_link]
  source_subnetwork_ip_ranges_to_nat = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

resource "google_compute_instance" "tf_vm" {
  name         = "tf-test-vm"
  machine_type = "e2-micro"
  zone         = "us-central1-a"

  boot_disk {
    initialize_params {
      image = "projects/debian-cloud/global/images/debian-12"
      size  = 10
    }
  }

  network_interface {
    subnetwork = google_compute_subnetwork.tf_subnet.id
  }

  labels = {
    env = "terraform"
  }
}

resource "google_compute_health_check" "tf_lb_hc" {
  name = "tf-lb-hc"

  timeout_sec        = 5
  check_interval_sec = 5

  http_health_check {
    port         = 80
    request_path = "/healthz"
  }
}

resource "google_compute_backend_service" "tf_lb_backend" {
  name        = "tf-lb-backend"
  protocol    = "HTTP"
  port_name   = "http"
  timeout_sec = 10

  health_checks = [google_compute_health_check.tf_lb_hc.id]
}

resource "google_compute_url_map" "tf_lb_url_map" {
  name            = "tf-lb-url-map"
  default_service = google_compute_backend_service.tf_lb_backend.id
}

resource "google_compute_target_http_proxy" "tf_lb_http_proxy" {
  name    = "tf-lb-http-proxy"
  url_map = google_compute_url_map.tf_lb_url_map.id
}

resource "google_compute_global_forwarding_rule" "tf_lb_rule" {
  name       = "tf-lb-rule"
  target     = google_compute_target_http_proxy.tf_lb_http_proxy.id
  port_range = "80"
}

# ---------- DNS (public + private zone) ----------

resource "google_dns_managed_zone" "main" {
  name     = "tf-test-zone"
  dns_name = "tf-test.example.com."
}

# Private managed zone auto-backs itself with a real Docker network
# inside the simulator (see simulators/gcp/dns.go handleCreateZone).
# Terraform round-trip covers zone Create + Get + Delete so the
# `Visibility=private` path triggers the simulator's network lifecycle.
resource "google_dns_managed_zone" "private_xjob" {
  name        = "tf-xjob-zone"
  dns_name    = "tf-xjob.local."
  visibility  = "private"
  description = "cross-job DNS coverage"
}

resource "google_dns_record_set" "main_a" {
  managed_zone = google_dns_managed_zone.main.name
  name         = "www.${google_dns_managed_zone.main.dns_name}"
  type         = "A"
  ttl          = 300
  rrdatas      = ["203.0.113.30"]
}

# ---------- Artifact Registry (Docker repo + cleanup policy) ----------

# Docker-format repository — backs both Cloud Run image pulls and the
# OCI distribution proxy at `/v2/{repo}/manifests/...`. Real production
# stacks reach for this in their pre-deploy step before `google_cloud_run_v2_service`.
resource "google_artifact_registry_repository" "tf_ar_docker" {
  location      = "us-central1"
  repository_id = "tf-ar-docker"
  description   = "tf-test Docker repository"
  format        = "DOCKER"
}

# Remote Docker Hub repository — same Artifact Registry shape the
# production Cloud Run/GCF modules use for Docker Hub image resolution.
resource "google_artifact_registry_repository" "tf_ar_docker_hub" {
  location      = "us-central1"
  repository_id = "docker-hub"
  description   = "tf-test Docker Hub remote repository"
  format        = "DOCKER"
  mode          = "REMOTE_REPOSITORY"

  remote_repository_config {
    description = "Proxies docker.io / Docker Hub"
    docker_repository {
      public_repository = "DOCKER_HUB"
    }
  }
}

# ---------- Cloud Run v2 (Service + Job) ----------

resource "google_cloud_run_v2_service" "tf_crv2_svc" {
  name                = "tf-crv2-svc"
  location            = "us-central1"
  deletion_protection = false

  template {
    containers {
      image = "us-central1-docker.pkg.dev/test-project/tf-ar-docker/test:latest"

      ports {
        container_port = 8080
      }

      env {
        name  = "TF_TEST"
        value = "true"
      }

      # Environment variable sourced from Secret Manager rather than a literal
      # (Container.env[].valueSource.secretKeyRef on the v2 wire).
      env {
        name = "TF_TEST_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.tf_secret.secret_id
            version = "latest"
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 3
        timeout_seconds       = 2
        period_seconds        = 10
        failure_threshold     = 4

        tcp_socket {
          port = 8080
        }
      }

      liveness_probe {
        initial_delay_seconds = 5
        timeout_seconds       = 1
        period_seconds        = 15
        failure_threshold     = 2

        http_get {
          path = "/healthz"
          port = 8080

          http_headers {
            name  = "X-Probe"
            value = "liveness"
          }
        }
      }

      readiness_probe {
        timeout_seconds   = 1
        period_seconds    = 5
        failure_threshold = 3

        grpc {
          port    = 8080
          service = "health.v1.Health"
        }
      }
    }

    scaling {
      min_instance_count = 0
      max_instance_count = 3
    }
  }

  depends_on = [google_artifact_registry_repository.tf_ar_docker]
}

resource "google_cloud_run_v2_worker_pool" "tf_crv2_worker_pool" {
  name                = "tf-crv2-worker-pool"
  location            = "us-central1"
  deletion_protection = false
  description         = "terraform-managed Cloud Run worker pool"
  launch_stage        = "GA"
  custom_audiences    = ["https://worker.tf-test.example.com"]

  labels = { managed_by = "terraform" }

  # Automatic scaling: scaling_mode/min_instance_count/max_instance_count are
  # members the pinned Cloud Run Discovery revision does not publish but the
  # provider sends and reads back (see specs/SIM_SURFACE_TABLES/gcp-cloudrun.md).
  scaling {
    scaling_mode       = "AUTOMATIC"
    min_instance_count = 1
    max_instance_count = 5
  }

  binary_authorization {
    use_default              = true
    breakglass_justification = "simulator coverage"
  }

  template {
    service_account = google_service_account.tf_sa.email

    containers {
      name        = "worker"
      image       = "us-central1-docker.pkg.dev/test-project/tf-ar-docker/worker:latest"
      command     = ["/worker"]
      args        = ["--mode", "pull"]
      working_dir = "/srv"

      env {
        name  = "TF_TEST"
        value = "true"
      }

      env {
        name = "TF_TEST_SECRET"
        value_source {
          secret_key_ref {
            secret  = google_secret_manager_secret.tf_secret.secret_id
            version = "1"
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 2
        timeout_seconds       = 3
        period_seconds        = 12
        failure_threshold     = 5

        http_get {
          path = "/startup"
          port = 9090

          http_headers {
            name  = "X-Probe"
            value = "startup"
          }
        }
      }

      liveness_probe {
        initial_delay_seconds = 4
        timeout_seconds       = 2
        period_seconds        = 20
        failure_threshold     = 6

        grpc {
          port    = 9090
          service = "worker.v1.Health"
        }
      }

      resources {
        limits = {
          cpu    = "1000m"
          memory = "512Mi"
        }
      }

      volume_mounts {
        name       = "scratch"
        mount_path = "/scratch"
      }
    }

    containers {
      name       = "sidecar"
      image      = "us-central1-docker.pkg.dev/test-project/tf-ar-docker/sidecar:latest"
      depends_on = ["worker"]
    }

    volumes {
      name = "scratch"
      empty_dir {
        medium     = "MEMORY"
        size_limit = "128Mi"
      }
    }

    vpc_access {
      connector = google_vpc_access_connector.tf_connector.id
      egress    = "ALL_TRAFFIC"
    }
  }

  depends_on = [google_artifact_registry_repository.tf_ar_docker]
}

resource "google_cloud_run_v2_worker_pool_iam_member" "tf_crv2_worker_pool_invoker" {
  location = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.location
  name     = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.tf_sa.email}"
}

resource "google_cloud_run_v2_job" "tf_crv2_job" {
  name                = "tf-crv2-job"
  location            = "us-central1"
  deletion_protection = false

  template {
    template {
      containers {
        image = "us-central1-docker.pkg.dev/test-project/tf-ar-docker/job:latest"

        env {
          name = "TF_TEST_SECRET"
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.tf_secret.secret_id
              version = "latest"
            }
          }
        }

        startup_probe {
          initial_delay_seconds = 6
          timeout_seconds       = 4
          period_seconds        = 30
          failure_threshold     = 2

          tcp_socket {
            port = 7070
          }
        }
      }
    }
  }

  depends_on = [google_artifact_registry_repository.tf_ar_docker]
}

# ---------- Cloud Functions v2 ----------

data "archive_file" "tf_function_source" {
  type        = "zip"
  output_path = "${path.module}/.terraform/function-source.zip"

  source {
    filename = "index.js"
    content  = <<-EOT
      exports.helloHttp = (req, res) => {
        res.status(200).send("ok");
      };
    EOT
  }
}

resource "google_storage_bucket_object" "tf_function_source" {
  name   = "function-source.zip"
  bucket = google_storage_bucket.tf_bucket.name
  source = data.archive_file.tf_function_source.output_path
}

resource "google_cloudfunctions2_function" "tf_gcfv2_function" {
  name        = "tf-gcfv2-function"
  location    = "us-central1"
  description = "Terraform simulator coverage for Cloud Functions v2"

  build_config {
    runtime     = "nodejs20"
    entry_point = "helloHttp"
    source {
      storage_source {
        bucket = google_storage_bucket.tf_bucket.name
        object = google_storage_bucket_object.tf_function_source.name
      }
    }
  }

  service_config {
    max_instance_count = 1
    available_memory   = "256M"
    timeout_seconds    = 60
  }

  depends_on = [google_storage_bucket_object.tf_function_source]
}

# ---------- Eventarc ----------

resource "google_eventarc_trigger" "tf_eventarc_trigger" {
  name     = "tf-eventarc-trigger"
  location = "us-central1"

  matching_criteria {
    attribute = "type"
    value     = "google.cloud.pubsub.topic.v1.messagePublished"
  }

  destination {
    cloud_run_service {
      service = google_cloud_run_v2_service.tf_crv2_svc.name
      region  = "us-central1"
    }
  }

  labels = {
    env = "test"
  }

  depends_on = [google_cloud_run_v2_service.tf_crv2_svc]
}

resource "google_eventarc_channel" "tf_eventarc_channel" {
  name     = "tf-eventarc-channel"
  location = "us-central1"

  labels = {
    env = "test"
  }
}

# ---------- Pub/Sub ----------

# message_retention_duration was dropped on create, so the provider read it
# back as null and drifted every plan.
resource "google_pubsub_topic" "tf_pubsub_topic" {
  name                       = "tf-pubsub-topic"
  message_retention_duration = "86400s"

  labels = {
    env = "terraform"
  }
}

# Dead-letter target for the subscription's dead_letter_policy.
resource "google_pubsub_topic" "tf_pubsub_dlq" {
  name = "tf-pubsub-dlq"
}

# dead_letter_policy + retry_policy were dropped on create → the subscription
# drifted every plan. Exercise both round-trips here.
resource "google_pubsub_subscription" "tf_pubsub_subscription" {
  name                 = "tf-pubsub-subscription"
  topic                = google_pubsub_topic.tf_pubsub_topic.id
  ack_deadline_seconds = 20

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.tf_pubsub_dlq.id
    max_delivery_attempts = 7
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }

  labels = {
    env = "terraform"
  }
}

# ---------- Cloud Build ----------

resource "google_cloudbuild_trigger" "tf_cloudbuild_trigger" {
  name     = "tf-cloudbuild-trigger"
  location = "us-central1"
  filename = "cloudbuild.yaml"

  trigger_template {
    branch_name = "main"
    repo_name   = "tf-repo"
  }

  substitutions = {
    _ENV = "terraform"
  }
}

# ---------- API Gateway ----------

resource "google_api_gateway_api" "tf_apigw_api" {
  provider = google-beta

  api_id       = "tf-apigw-api"
  display_name = "Terraform API Gateway API"

  labels = {
    env = "terraform"
  }
}

resource "google_api_gateway_api_config" "tf_apigw_config" {
  provider = google-beta

  api           = google_api_gateway_api.tf_apigw_api.api_id
  api_config_id = "tf-apigw-config"
  display_name  = "Terraform API Gateway config"

  openapi_documents {
    document {
      path = "openapi.yaml"
      contents = base64encode(<<-EOT
        swagger: "2.0"
        info:
          title: Terraform API Gateway simulator coverage
          version: "1.0.0"
        schemes:
          - https
        produces:
          - application/json
        paths:
          /health:
            get:
              operationId: health
              responses:
                "200":
                  description: ok
      EOT
      )
    }
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "google_api_gateway_gateway" "tf_apigw_gateway" {
  provider = google-beta

  gateway_id   = "tf-apigw-gateway"
  api_config   = google_api_gateway_api_config.tf_apigw_config.id
  display_name = "Terraform API Gateway gateway"
  region       = "us-central1"

  labels = {
    env = "terraform"
  }
}

# ---------- Cloud Storage ----------

resource "google_storage_bucket" "tf_bucket" {
  name          = "tf-test-bucket-${random_id.bucket_suffix.hex}"
  location      = "us-central1"
  force_destroy = true

  uniform_bucket_level_access = true
}

resource "random_id" "bucket_suffix" {
  byte_length = 4
}

# GCS object inside the bucket — runner workflows stage build artifacts
# / function source archives here. Exercises POST /upload/storage/v1/b/{bucket}/o.
resource "google_storage_bucket_object" "tf_artifact" {
  name    = "tf-test-artifact.txt"
  bucket  = google_storage_bucket.tf_bucket.name
  content = "tf-test-payload"
}

# ---------- Cloud Logging ----------

resource "google_logging_project_sink" "tf_log_sink" {
  name                   = "tf-log-sink"
  destination            = "storage.googleapis.com/${google_storage_bucket.tf_bucket.name}"
  filter                 = "resource.type=\"global\""
  unique_writer_identity = true
}

resource "google_logging_metric" "tf_log_metric" {
  name   = "tf-log-metric"
  filter = "resource.type=\"global\" AND severity>=ERROR"

  metric_descriptor {
    metric_kind = "DELTA"
    value_type  = "INT64"
  }
}

# ---------- Data SaaS (BigQuery + Firestore) ----------

resource "google_bigquery_dataset" "tf_bq_dataset" {
  dataset_id = "tf_test_dataset"
  location   = "US"

  labels = {
    env = "terraform"
  }
}

resource "google_bigquery_table" "tf_bq_table" {
  dataset_id          = google_bigquery_dataset.tf_bq_dataset.dataset_id
  table_id            = "events"
  deletion_protection = false

  labels = {
    env = "terraform"
  }

  schema = jsonencode([
    {
      name = "id"
      type = "STRING"
      mode = "REQUIRED"
    },
    {
      name = "kind"
      type = "STRING"
      mode = "NULLABLE"
    }
  ])
}

resource "google_firestore_document" "tf_firestore_doc" {
  project     = "test-project"
  collection  = "tf-users"
  document_id = "alice"
  fields      = "{\"team\":{\"stringValue\":\"platform\"},\"role\":{\"stringValue\":\"admin\"}}"
}

resource "google_redis_instance" "tf_redis" {
  name           = "tf-redis"
  tier           = "BASIC"
  memory_size_gb = 1
  region         = "us-central1"
  redis_version  = "REDIS_7_0"

  labels = {
    env = "terraform"
  }
}

resource "google_sql_database_instance" "tf_sql" {
  name             = "tf-sql"
  region           = "us-central1"
  database_version = "POSTGRES_15"

  settings {
    tier = "db-f1-micro"
  }

  deletion_protection = false
}

resource "google_sql_database" "tf_sql_db" {
  name     = "appdb"
  instance = google_sql_database_instance.tf_sql.name
}

resource "google_sql_user" "tf_sql_user" {
  name     = "appuser"
  instance = google_sql_database_instance.tf_sql.name
  password = "Str0ng-password-12345!"
}

# ---------- Spanner ----------

resource "google_spanner_instance" "tf_spanner" {
  name         = "tf-spanner"
  config       = "regional-us-central1"
  display_name = "tf-spanner"
  num_nodes    = 1

  labels = {
    env = "terraform"
  }
}

resource "google_spanner_database" "tf_spanner_db" {
  instance            = google_spanner_instance.tf_spanner.name
  name                = "appdb"
  deletion_protection = false

  ddl = [
    "CREATE TABLE Users (UserId STRING(36) NOT NULL, DisplayName STRING(MAX)) PRIMARY KEY (UserId)"
  ]
}

# ---------- Cloud Bigtable ----------

resource "google_bigtable_instance" "tf_bigtable" {
  name                = "tf-bt1"
  display_name        = "tf-bt1"
  deletion_protection = false
  deletion_policy     = "DELETE"

  labels = {
    env = "terraform"
  }

  cluster {
    cluster_id   = "tf-bt1-c1"
    zone         = "us-central1-a"
    num_nodes    = 1
    storage_type = "SSD"
  }
}

resource "google_bigtable_table" "tf_bigtable_table" {
  name                = "app_table"
  instance_name       = google_bigtable_instance.tf_bigtable.name
  deletion_protection = "UNPROTECTED"
  deletion_policy     = "DELETE"

  column_family {
    family = "cf1"
  }
}

# ---------- Secret Manager ----------

resource "google_secret_manager_secret" "tf_secret" {
  secret_id       = "tf-test-secret"
  deletion_policy = "DELETE"

  labels = {
    env = var.secret_label_env
  }

  replication {
    auto {}
  }
}

resource "google_secret_manager_secret_version" "tf_secret_v1" {
  secret          = google_secret_manager_secret.tf_secret.id
  secret_data     = "tf-test-secret-payload"
  deletion_policy = "DELETE"
}

# ---------- IAM (service account) ----------

# Service accounts are the runner identity primitive. The sim exposes
# POST /v1/projects/{}/serviceAccounts; terraform-provider-google routes
# google_service_account through iambeta.NewClient which is configured
# via iam_beta_custom_endpoint (above).
resource "google_service_account" "tf_sa" {
  account_id   = "tf-test-runner-sa"
  display_name = "tf-test runner service account"
}

# A service-account key is the credential a non-interactive client — the gcloud
# CLI's `auth activate-service-account`, a backend reading
# GOOGLE_APPLICATION_CREDENTIALS — authenticates with. The provider creates it
# through IAM's projects.serviceAccounts.keys.create and reads it back through
# keys.get, so private_key carries the base64 PKCS#8 credential file the API
# returns once at creation, and public_key carries the registered public half.
resource "google_service_account_key" "tf_sa_key" {
  service_account_id = google_service_account.tf_sa.name
  key_algorithm      = "KEY_ALG_RSA_2048"
}

# Project lifecycle via the Cloud Resource Manager v1 wire: create returns an
# operation the provider waits on (GET /v1/operations/{op}), Read is the v1
# GetProject plus Cloud Billing's getBillingInfo, and destroy performs the
# real soft-delete (deletion_policy = "DELETE").
resource "google_project" "tf_project" {
  name            = "TF Test Project"
  project_id      = "tf-created-project"
  org_id          = "123456789012"
  deletion_policy = "DELETE"

  labels = {
    env = "tf-test"
  }
}


# Resource hierarchy and Organization Policy over the Cloud Resource Manager
# wire. google_folder speaks the folders collection; the three
# *_organization_policy resources and google_resource_manager_lien speak v1 —
# setOrgPolicy / getOrgPolicy / clearOrgPolicy on the project, folder and
# organization, and the liens collection.
resource "google_folder" "tf_folder" {
  display_name        = "TF Test Folder"
  parent              = "organizations/123456789012"
  deletion_protection = false
}

resource "google_project_organization_policy" "tf_project_org_policy" {
  project    = google_project.tf_project.project_id
  constraint = "constraints/iam.disableServiceAccountKeyCreation"

  boolean_policy {
    enforced = true
  }
}

resource "google_folder_organization_policy" "tf_folder_org_policy" {
  folder     = google_folder.tf_folder.name
  constraint = "constraints/gcp.resourceLocations"

  list_policy {
    allow {
      values = ["in:us-locations"]
    }
  }
}

resource "google_organization_policy" "tf_org_policy" {
  org_id     = "123456789012"
  constraint = "constraints/iam.disableServiceAccountCreation"

  boolean_policy {
    enforced = false
  }
}

resource "google_resource_manager_lien" "tf_lien" {
  parent       = "projects/${google_project.tf_project.number}"
  restrictions = ["resourcemanager.projects.delete"]
  origin       = "terraform-tests"
  reason       = "Terraform lien coverage"
}

# ---------- Outputs (cross-resource invariants) ----------

output "compute_disk_self_link" {
  value = google_compute_disk.tf_disk.self_link
}

output "dns_zone_name_servers" {
  value = google_dns_managed_zone.main.name_servers
}

output "dns_record_set_id" {
  value = google_dns_record_set.main_a.id
}

output "ar_repo_id" {
  value = google_artifact_registry_repository.tf_ar_docker.id
}

output "ar_remote_repo_id" {
  value = google_artifact_registry_repository.tf_ar_docker_hub.id
}

output "cloud_run_v2_service_uri" {
  value = google_cloud_run_v2_service.tf_crv2_svc.uri
}

output "cloud_run_v2_job_id" {
  value = google_cloud_run_v2_job.tf_crv2_job.id
}

output "cloud_run_v2_worker_pool_id" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.id
}

output "cloud_run_v2_worker_pool_latest_ready_revision" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.latest_ready_revision
}

output "cloud_run_v2_worker_pool_iam_member_id" {
  value = google_cloud_run_v2_worker_pool_iam_member.tf_crv2_worker_pool_invoker.id
}

# Automatic-scaling members the provider sends on the v2 wire; a simulator that
# drops them re-plans the worker pool on every run.
output "cloud_run_v2_worker_pool_scaling_mode" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.scaling[0].scaling_mode
}

output "cloud_run_v2_worker_pool_min_instance_count" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.scaling[0].min_instance_count
}

output "cloud_run_v2_worker_pool_max_instance_count" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.scaling[0].max_instance_count
}

# Container probes and secret-sourced environment variables, read back from
# state after apply: each must be the value the configuration set.
output "cloud_run_v2_service_startup_probe" {
  value = google_cloud_run_v2_service.tf_crv2_svc.template[0].containers[0].startup_probe[0]
}

output "cloud_run_v2_service_liveness_probe" {
  value = google_cloud_run_v2_service.tf_crv2_svc.template[0].containers[0].liveness_probe[0]
}

output "cloud_run_v2_service_readiness_probe" {
  value = google_cloud_run_v2_service.tf_crv2_svc.template[0].containers[0].readiness_probe[0]
}

output "cloud_run_v2_service_secret_env" {
  value = one([for e in google_cloud_run_v2_service.tf_crv2_svc.template[0].containers[0].env : e if e.name == "TF_TEST_SECRET"])
}

output "cloud_run_v2_job_startup_probe" {
  value = google_cloud_run_v2_job.tf_crv2_job.template[0].template[0].containers[0].startup_probe[0]
}

output "cloud_run_v2_job_secret_env" {
  value = one([for e in google_cloud_run_v2_job.tf_crv2_job.template[0].template[0].containers[0].env : e if e.name == "TF_TEST_SECRET"])
}

output "cloud_run_v2_worker_pool_liveness_probe" {
  value = google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.template[0].containers[0].liveness_probe[0]
}

output "cloud_run_v2_worker_pool_secret_env" {
  value = one([for e in google_cloud_run_v2_worker_pool.tf_crv2_worker_pool.template[0].containers[0].env : e if e.name == "TF_TEST_SECRET"])
}

output "cloudfunctions2_function_id" {
  value = google_cloudfunctions2_function.tf_gcfv2_function.id
}

output "eventarc_trigger_id" {
  value = google_eventarc_trigger.tf_eventarc_trigger.id
}

output "pubsub_topic_id" {
  value = google_pubsub_topic.tf_pubsub_topic.id
}

output "pubsub_subscription_id" {
  value = google_pubsub_subscription.tf_pubsub_subscription.id
}

output "cloudbuild_trigger_id" {
  value = google_cloudbuild_trigger.tf_cloudbuild_trigger.id
}

output "api_gateway_gateway_id" {
  value = google_api_gateway_gateway.tf_apigw_gateway.id
}

output "storage_bucket_url" {
  value = google_storage_bucket.tf_bucket.url
}

output "logging_project_sink_id" {
  value = google_logging_project_sink.tf_log_sink.id
}

output "logging_metric_id" {
  value = google_logging_metric.tf_log_metric.id
}

output "secret_version_id" {
  value = google_secret_manager_secret_version.tf_secret_v1.id
}

output "secret_label_env" {
  value = google_secret_manager_secret.tf_secret.labels.env
}

output "subnet_id" {
  value = google_compute_subnetwork.tf_subnet.id
}

output "vpc_access_connector_id" {
  value = google_vpc_access_connector.tf_connector.id
}

output "firewall_id" {
  value = google_compute_firewall.tf_fw.id
}

output "nat_address" {
  value = google_compute_address.tf_nat_ip.address
}

output "router_nat_name" {
  value = google_compute_router_nat.tf_nat.name
}

output "lb_health_check_id" {
  value = google_compute_health_check.tf_lb_hc.id
}

output "lb_backend_service_id" {
  value = google_compute_backend_service.tf_lb_backend.id
}

output "lb_url_map_id" {
  value = google_compute_url_map.tf_lb_url_map.id
}

output "lb_target_http_proxy_id" {
  value = google_compute_target_http_proxy.tf_lb_http_proxy.id
}

output "lb_forwarding_rule_ip" {
  value = google_compute_global_forwarding_rule.tf_lb_rule.ip_address
}

output "gcs_object_self_link" {
  value = google_storage_bucket_object.tf_artifact.self_link
}

output "service_account_email" {
  value = google_service_account.tf_sa.email
}

output "service_account_name" {
  value = google_service_account.tf_sa.name
}

output "service_account_key_name" {
  value = google_service_account_key.tf_sa_key.name
}

# The base64 PKCS#8 credential file IAM returns once, at creation. The apply
# test decodes it and checks it is the shape a Google client can authenticate
# with.
output "service_account_key_private_key" {
  value     = google_service_account_key.tf_sa_key.private_key
  sensitive = true
}

output "service_account_key_algorithm" {
  value = google_service_account_key.tf_sa_key.key_algorithm
}

output "bigquery_table_id" {
  value = google_bigquery_table.tf_bq_table.id
}

output "bigquery_table_label_env" {
  value = google_bigquery_table.tf_bq_table.labels["env"]
}

output "firestore_document_name" {
  value = google_firestore_document.tf_firestore_doc.id
}

output "redis_instance_id" {
  value = google_redis_instance.tf_redis.id
}

output "redis_instance_host" {
  value = google_redis_instance.tf_redis.host
}

output "sql_instance_connection_name" {
  value = google_sql_database_instance.tf_sql.connection_name
}

output "sql_database_id" {
  value = google_sql_database.tf_sql_db.id
}

output "sql_user_name" {
  value = google_sql_user.tf_sql_user.name
}

output "spanner_instance_id" {
  value = google_spanner_instance.tf_spanner.id
}

output "spanner_database_id" {
  value = google_spanner_database.tf_spanner_db.id
}

output "bigtable_instance_id" {
  value = google_bigtable_instance.tf_bigtable.id
}

output "bigtable_table_id" {
  value = google_bigtable_table.tf_bigtable_table.id
}

output "project_id" {
  value = google_project.tf_project.project_id
}

output "project_number" {
  value = google_project.tf_project.number
}

output "folder_name" {
  value = google_folder.tf_folder.name
}

output "folder_parent" {
  value = google_folder.tf_folder.parent
}

output "project_org_policy_constraint" {
  value = google_project_organization_policy.tf_project_org_policy.constraint
}

output "project_org_policy_enforced" {
  value = google_project_organization_policy.tf_project_org_policy.boolean_policy[0].enforced
}

output "folder_org_policy_allowed_values" {
  value = google_folder_organization_policy.tf_folder_org_policy.list_policy[0].allow[0].values
}

output "organization_org_policy_constraint" {
  value = google_organization_policy.tf_org_policy.constraint
}

output "resource_manager_lien_name" {
  value = google_resource_manager_lien.tf_lien.name
}
