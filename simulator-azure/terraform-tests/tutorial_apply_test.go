package azure_tf_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformTutorial runs the Azure steps of docs/tutorial.md: init, apply,
// a plan with nothing to change, the outputs, a call to the function app, and
// destroy.
func TestTerraformTutorial(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("tutorial"))
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	out, err = runTimed(t, "terraform plan", terraformCmd(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "terraform plan showed drift after apply:\n%s", out)
	outputs := readOutputs(t, dir)

	_, prefix, err := net.ParseCIDR(outputs.must(t, "nat_gateway_ip_prefix"))
	require.NoError(t, err, "the NAT gateway's public IP prefix is a CIDR")
	require.Equal(t, 4, len(prefix.IP.To4()))
	vmIP := net.ParseIP(outputs.must(t, "vm_private_ip"))
	require.NotNil(t, vmIP)
	_, subnet, err := net.ParseCIDR("10.97.1.0/24")
	require.NoError(t, err)
	require.True(t, subnet.Contains(vmIP), "the machine takes an address in its subnet")
	require.True(t, strings.HasPrefix(outputs.must(t, "container_app_fqdn"), "tutorial-web"), outputs.must(t, "container_app_fqdn"))
	require.True(t, strings.HasSuffix(outputs.must(t, "blob_url"), "/assets/hello.txt"), outputs.must(t, "blob_url"))
	require.Contains(t, outputs.must(t, "cosmos_container_id"), "/databaseAccounts/tutorial-orders/sqlDatabases/tutorial/containers/orders")
	require.Contains(t, outputs.must(t, "servicebus_queue_id"), "/namespaces/tutorial-orders-ns/queues/orders")
	require.NotEmpty(t, outputs.must(t, "log_analytics_workspace_id"))

	host := outputs.must(t, "function_hostname")
	require.Equal(t, "tutorial-worker.azurewebsites.net", host)
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/api/hello?code=%s", simPort, url.QueryEscape(outputs.must(t, "function_key"))), nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)
	require.Equal(t, "hello from tutorial-worker", string(body))
}
