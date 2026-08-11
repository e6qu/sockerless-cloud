package gcp_tf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The HashiCorp Google provider applies a packet mirroring policy together with
// the collector chain the resource defines: an INTERNAL forwarding rule in
// front of a regional backend service whose backend is an instance group. That
// chain is what the simulator resolves to decide where mirrored traffic is
// delivered, so applying it through the provider is what proves the resource is
// wired the way a real operator writes it.
//
// It stands apart from the monolithic apply because it needs no booted guest —
// the collector group is empty here, and the delivery itself is proven against
// real traffic in realexec. Folding it into that apply would tie a
// control-plane assertion to a Firecracker boot it does not need.
func TestTerraformComputePacketMirroringApplyDestroy(t *testing.T) {
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

  compute_custom_endpoint = "${var.endpoint}/compute/v1/"
}

resource "google_compute_network" "main" {
  name                    = "tf-pm-net"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "main" {
  name          = "tf-pm-subnet"
  region        = "us-central1"
  network       = google_compute_network.main.id
  ip_cidr_range = "10.42.0.0/16"
}

resource "google_compute_instance_group" "collectors" {
  name    = "tf-pm-collectors"
  zone    = "us-central1-a"
  network = google_compute_network.main.id
}

resource "google_compute_region_health_check" "collector" {
  name               = "tf-pm-hc"
  region             = "us-central1"
  timeout_sec        = 5
  check_interval_sec = 5

  tcp_health_check {
    port = 80
  }
}

resource "google_compute_region_backend_service" "collector" {
  name                  = "tf-pm-backend"
  region                = "us-central1"
  load_balancing_scheme = "INTERNAL"
  protocol              = "TCP"
  health_checks         = [google_compute_region_health_check.collector.id]

  backend {
    group = google_compute_instance_group.collectors.id
  }
}

resource "google_compute_forwarding_rule" "collector" {
  name                   = "tf-pm-ilb"
  region                 = "us-central1"
  load_balancing_scheme  = "INTERNAL"
  ip_protocol            = "TCP"
  ports                  = ["80"]
  network                = google_compute_network.main.id
  subnetwork             = google_compute_subnetwork.main.id
  backend_service        = google_compute_region_backend_service.collector.id
  is_mirroring_collector = true
}

resource "google_compute_packet_mirroring" "main" {
  name        = "tf-pm"
  region      = "us-central1"
  description = "mirror the subnetwork to the collector"
  priority    = 1000

  network {
    url = google_compute_network.main.id
  }

  collector_ilb {
    url = google_compute_forwarding_rule.collector.id
  }

  mirrored_resources {
    subnetworks {
      url = google_compute_subnetwork.main.id
    }
    tags = ["mirrored"]
  }

  filter {
    ip_protocols = ["tcp"]
    cidr_ranges  = ["10.42.0.0/16"]
    direction    = "BOTH"
  }
}
`), 0o644))

	out, err := runTimed(t, "terraform init packet mirroring", terraformCmdInDir(dir, "init"))
	require.NoError(t, err, "%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy packet mirroring",
			terraformCmdInDir(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "%s", out)
	})
	out, err = runTimed(t, "terraform apply packet mirroring",
		terraformCmdInDir(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "%s", out)

	// A second plan must be empty: a member the simulator drops or renames on
	// read-back shows up here as perpetual drift, which is how a policy that
	// looks applied silently stops matching what the operator wrote.
	out, err = runTimed(t, "terraform plan packet mirroring",
		terraformCmdInDir(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "packet mirroring re-plan is not empty:\n%s", out)
}
