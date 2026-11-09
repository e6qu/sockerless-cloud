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

variable "database_version" {
  description = "The instance's database version; a later one upgrades it in place."
  type        = string
}

# A database_version change to a later major version is Cloud SQL's in-place
# major version upgrade: the provider patches the instance, and the
# MAJOR_VERSION_UPGRADE operation moves the data onto the new release.
resource "google_sql_database_instance" "upgrade" {
  name             = "tf-sql-version"
  region           = "us-central1"
  database_version = var.database_version

  settings {
    tier = "db-f1-micro"
  }

  deletion_protection = false
}

resource "google_sql_database" "upgrade" {
  name     = "appdb"
  instance = google_sql_database_instance.upgrade.name
}

output "database_version" {
  value = google_sql_database_instance.upgrade.database_version
}
