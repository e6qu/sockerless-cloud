package gcp_tf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerraformBigtableAdminApplyDestroy(t *testing.T) {
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

  bigtable_custom_endpoint = "${var.endpoint}/v2/"
}

resource "google_bigtable_instance" "main" {
  name                = "tf-bt-focused"
  display_name        = "tf-bt-focused"
  deletion_protection = false
  deletion_policy     = "DELETE"

  labels = {
    env = "terraform"
  }

  cluster {
    cluster_id   = "tf-bt-focused-c1"
    zone         = "us-central1-a"
    num_nodes    = 1
    storage_type = "SSD"
  }
}

resource "google_bigtable_table" "main" {
  name                = "app_table"
  instance_name       = google_bigtable_instance.main.name
  deletion_protection = "UNPROTECTED"
  deletion_policy     = "DELETE"

  column_family {
    family = "cf1"
  }
}
`), 0o644))

	out, err := runTimed(t, "terraform init bigtable", terraformCmdInDir(dir, "init"))
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy bigtable", terraformCmdInDir(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "%s", out)
	})
	out, err = runTimed(t, "terraform apply bigtable", terraformCmdInDir(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "%s", out)
}
