package gcp_tf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerraformSpannerApplyPlanDestroy(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`
terraform {
  required_providers {
    google = {
      source = "hashicorp/google"
    }
  }
}

variable "endpoint" {
  type = string
}

variable "access_token" {
  type = string
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  access_token          = var.access_token
  user_project_override = false

  spanner_custom_endpoint = "${var.endpoint}/spanner/v1/"
}

resource "google_spanner_instance" "main" {
  name         = "tf-spanner-focused"
  config       = "regional-us-central1"
  display_name = "tf-spanner-focused"
  num_nodes    = 1

  labels = {
    env = "terraform"
  }
}

resource "google_spanner_database" "main" {
  instance            = google_spanner_instance.main.name
  name                = "application"
  deletion_protection = false

  ddl = [
    "CREATE TABLE Users (UserId STRING(36) NOT NULL, DisplayName STRING(MAX)) PRIMARY KEY (UserId)",
    "ALTER TABLE Users ADD COLUMN Email STRING(MAX)",
    "CREATE UNIQUE INDEX UsersByEmail ON Users(Email)",
  ]
}

output "database_id" {
  value = google_spanner_database.main.id
}
`), 0o644))

	out, err := runTimed(t, "terraform init Cloud Spanner", terraformCmdInDir(dir, "init"))
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy Cloud Spanner", terraformCmdInDir(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "%s", out)
	})

	out, err = runTimed(t, "terraform apply Cloud Spanner", terraformCmdInDir(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "%s", out)
	outputs := readOutputsInDir(t, dir)
	require.Equal(t, "tf-spanner-focused/application", outputs.must(t, "database_id"))

	out, err = runTimed(t, "terraform plan Cloud Spanner", terraformCmdInDir(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "Cloud Spanner plan showed drift after apply:\n%s", out)
}
