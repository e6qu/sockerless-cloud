package gcp_tf_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformOrgFirewallPolicyAndCustomWriterSink applies an organization
// firewall policy, whose numeric name the provider reads from the insert
// operation's targetId, and a sink that routes to another project's log bucket
// under a custom writer identity.
func TestTerraformOrgFirewallPolicyAndCustomWriterSink(t *testing.T) {
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

variable "credentials" {
  type = string
}

provider "google" {
  project = "test-project"
  region  = "us-central1"

  credentials           = var.credentials
  user_project_override = false

  compute_custom_endpoint = "${var.endpoint}/compute/v1/"
  logging_custom_endpoint = "${var.endpoint}/v2/"
}

resource "google_compute_firewall_policy" "org" {
  parent      = "folders/515151"
  short_name  = "tf-org-policy"
  description = "organization firewall policy"
}

resource "google_logging_project_sink" "cross_project" {
  name                   = "tf-cross-project-sink"
  destination            = "logging.googleapis.com/projects/tf-sink-destination/locations/global/buckets/_Default"
  unique_writer_identity = true
  custom_writer_identity = "serviceAccount:tf-router@test-project.iam.gserviceaccount.com"
}

output "policy_name" {
  value = google_compute_firewall_policy.org.name
}

output "policy_parent" {
  value = google_compute_firewall_policy.org.parent
}

output "sink_writer_identity" {
  value = google_logging_project_sink.cross_project.writer_identity
}
`), 0o644))

	out, err := runTimed(t, "terraform init org firewall policy", terraformCmdInDir(dir, "init"))
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy org firewall policy",
			terraformCmdInDir(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "%s", out)
	})
	out, err = runTimed(t, "terraform apply org firewall policy",
		terraformCmdInDir(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "%s", out)

	outputs := readOutputsInDir(t, dir)
	name := outputs.must(t, "policy_name")
	require.NotEmpty(t, name)
	require.Empty(t, strings.Trim(name, "0123456789"), "an organization firewall policy's name is a numeric ID")
	require.Equal(t, "folders/515151", outputs.must(t, "policy_parent"))
	require.Equal(t, "serviceAccount:tf-router@test-project.iam.gserviceaccount.com", outputs.must(t, "sink_writer_identity"))

	out, err = runTimed(t, "terraform plan org firewall policy",
		terraformCmdInDir(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "org firewall policy re-plan is not empty:\n%s", out)
}
