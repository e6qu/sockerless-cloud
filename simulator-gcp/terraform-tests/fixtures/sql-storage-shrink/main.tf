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

  sql_custom_endpoint = "${var.endpoint}/sql/v1beta4/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "credentials" {
  type = string
}

variable "disk_size" {
  description = "Provisioned storage in GB; a decrease shrinks it in place."
  type        = number
}

# A disk_size decrease with disk_autoresize off is an in-place storage shrink:
# the provider reads projects.instances.getDiskShrinkConfig and runs
# projects.instances.performDiskShrink.
resource "google_sql_database_instance" "shrink" {
  name             = "tf-sql-shrink"
  region           = "us-central1"
  database_version = "POSTGRES_15"

  settings {
    tier              = "db-custom-1-3840"
    availability_type = "REGIONAL"
    disk_size         = var.disk_size
    disk_autoresize   = false

    location_preference {
      zone           = "us-central1-a"
      secondary_zone = "us-central1-c"
    }
  }

  deletion_protection = false
}

output "disk_size" {
  value = google_sql_database_instance.shrink.settings[0].disk_size
}
