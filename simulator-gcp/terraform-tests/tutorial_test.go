package gcp_tf_test

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformTutorial runs the Google Cloud steps of docs/tutorial.md: init,
// apply, read the outputs, plan again with nothing to change, destroy.
func TestTerraformTutorial(t *testing.T) {
	t.Setenv("TF_VAR_debian_image_family", debianImageFamily())
	outputs := applyFixture(t, "tutorial")

	require.NotNil(t, net.ParseIP(outputs.must(t, "nat_ip")), "Cloud NAT holds its reserved address")
	require.Equal(t, "RUNNING", outputs.must(t, "instance_status"))
	privateIP := net.ParseIP(outputs.must(t, "instance_private_ip"))
	require.NotNil(t, privateIP)
	_, subnetwork, err := net.ParseCIDR("10.44.0.0/24")
	require.NoError(t, err)
	require.True(t, subnetwork.Contains(privateIP), "the instance takes an address in its subnetwork")
	require.True(t, strings.HasPrefix(outputs.must(t, "run_service_uri"), "https://tutorial-web-"), outputs.must(t, "run_service_uri"))
	require.Equal(t, "Hello from Cloud Storage", outputs.must(t, "storage_object_content"))
	require.Equal(t, "tutorial-orders/order-1", outputs.must(t, "firestore_document"))
	require.Equal(t, "projects/sockerless/subscriptions/tutorial-orders", outputs.must(t, "subscription"))
	require.Equal(t, "ACTIVE", outputs.must(t, "function_state"))
	require.Equal(t, "https://us-central1-sockerless.cloudfunctions.net/tutorial-worker", outputs.must(t, "function_url"))
	require.Equal(t, "tutorial-worker-errors", outputs.must(t, "log_metric"))
	requireNoDrift(t, "tutorial")
}
