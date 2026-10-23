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

  resource_manager_custom_endpoint    = "${var.endpoint}/v1/"
  resource_manager_v3_custom_endpoint = "${var.endpoint}/v3/"
}

variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  type = string
}

resource "google_folder" "capability" {
  display_name        = "tf-capability-folder"
  parent              = "organizations/123456789012"
  deletion_protection = false
}

resource "google_resource_manager_capability_config" "folder" {
  parent               = google_folder.capability.name
  capability_config_id = "tf-capcfg"
  display_name         = "Terraform capabilities"
  types                = ["APP_MANAGEMENT"]
}

output "name" {
  value = google_resource_manager_capability_config.folder.id
}

output "types" {
  value = google_resource_manager_capability_config.folder.types
}

output "management_project" {
  value = google_resource_manager_capability_config.folder.management_project
}
