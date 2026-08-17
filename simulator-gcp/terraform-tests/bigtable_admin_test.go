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

output "instance_id" {
  value = google_bigtable_instance.main.id
}

output "instance_label_env" {
  value = google_bigtable_instance.main.labels.env
}

# The cluster is a separate resource in the Bigtable admin API — created by
# CreateInstance and read back by ListClusters — so its members are what the
# provider re-reads rather than what it wrote. A cluster the read lost its zone,
# node count or storage type on reports the loss here.
output "cluster_id" {
  value = google_bigtable_instance.main.cluster[0].cluster_id
}

output "cluster_zone" {
  value = google_bigtable_instance.main.cluster[0].zone
}

output "cluster_num_nodes" {
  value = google_bigtable_instance.main.cluster[0].num_nodes
}

output "cluster_storage_type" {
  value = google_bigtable_instance.main.cluster[0].storage_type
}

output "table_id" {
  value = google_bigtable_table.main.id
}

output "table_column_families" {
  value = [for cf in google_bigtable_table.main.column_family : cf.family]
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

	// What the provider read back out of the admin API after the create. An
	// apply that only checks its own exit status passes against a simulator
	// that accepted the instance and lost its labels, its cluster shape or the
	// table's column family.
	outputs := readOutputsInDir(t, dir)
	require.Equal(t, "projects/test-project/instances/tf-bt-focused", outputs.must(t, "instance_id"))
	require.Equal(t, "terraform", outputs.must(t, "instance_label_env"),
		"instance labels must survive CreateInstance + GetInstance")
	require.Equal(t, "tf-bt-focused-c1", outputs.must(t, "cluster_id"))
	require.Equal(t, "us-central1-a", outputs.must(t, "cluster_zone"))
	require.Equal(t, float64(1), outputs.mustNumber(t, "cluster_num_nodes"))
	require.Equal(t, "SSD", outputs.must(t, "cluster_storage_type"))
	require.Equal(t, "projects/test-project/instances/tf-bt-focused/tables/app_table",
		outputs.must(t, "table_id"))
	require.Equal(t, []any{"cf1"}, outputs.mustValue(t, "table_column_families"),
		"the table's column family must survive CreateTable + GetTable")

	// A second plan must be empty: a member the simulator drops or renames on
	// read-back surfaces here as perpetual drift even when no output covers it.
	out, err = runTimed(t, "terraform plan bigtable", terraformCmdInDir(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "Bigtable re-plan is not empty:\n%s", out)
}
