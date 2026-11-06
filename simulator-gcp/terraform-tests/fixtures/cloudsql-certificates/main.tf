terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "8.10.0"
    }
  }
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  sql_custom_endpoint = "${var.endpoint}/sql/v1beta4/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
  type = string
}

resource "google_sql_database_instance" "ssl" {
  name             = "tf-sql-ssl"
  region           = "us-central1"
  database_version = "POSTGRES_15"
  root_password    = "Str0ng-password-12345!"

  settings {
    tier = "db-f1-micro"
  }

  deletion_protection = false
}

resource "google_sql_ssl_cert" "client" {
  common_name = "tf-client"
  instance    = google_sql_database_instance.ssl.name
}

data "google_sql_ca_certs" "ssl" {
  instance = google_sql_database_instance.ssl.name

  depends_on = [google_sql_ssl_cert.client]
}

output "address" {
  value = google_sql_database_instance.ssl.public_ip_address
}

output "instance_server_ca" {
  value     = google_sql_database_instance.ssl.server_ca_cert[0].cert
  sensitive = true
}

output "instance_server_ca_fingerprint" {
  value     = google_sql_database_instance.ssl.server_ca_cert[0].sha1_fingerprint
  sensitive = true
}

output "active_ca_version" {
  value     = data.google_sql_ca_certs.ssl.active_version
  sensitive = true
}

output "client_cert" {
  value     = google_sql_ssl_cert.client.cert
  sensitive = true
}

output "client_private_key" {
  value     = google_sql_ssl_cert.client.private_key
  sensitive = true
}

output "client_server_ca" {
  value     = google_sql_ssl_cert.client.server_ca_cert
  sensitive = true
}

output "client_fingerprint" {
  value     = google_sql_ssl_cert.client.sha1_fingerprint
  sensitive = true
}
