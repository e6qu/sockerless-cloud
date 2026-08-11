package azure_cli_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The `az` counterpart of the SDK request-validation tests: Azure answers a
// virtual-machine or subnet write the client got wrong with 4xx, before its
// resource provider realizes anything. A 503 OperationNotAllowed would tell az
// (and every SDK retry policy) that the service is temporarily unwell and the
// request is worth repeating, which it never is.
//
// Neither test calls requireNetworkHost — the rejection happens ahead of the
// host capabilities, so both run everywhere.

// runCLIExpectFailure runs an az command that must fail, returning its combined
// output so the caller can assert on the error Azure reported. runCLI fails the
// test on a non-zero exit, which is the opposite of what these tests need.
func runCLIExpectFailure(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	const perCmdTimeout = 60 * time.Second
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "PYTHONWARNINGS=ignore")
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Start(); err != nil {
		t.Fatalf("CLI command failed to start: %v\nCommand: %s", err, strings.Join(cmd.Args, " "))
	}
	timer := time.AfterFunc(perCmdTimeout, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	if err := cmd.Wait(); err == nil {
		t.Fatalf("CLI command unexpectedly succeeded\nCommand: %s\nOutput: %s",
			strings.Join(cmd.Args, " "), combined.String())
	}
	return combined.String()
}

func TestComputeVirtualMachineWithoutNetworkProfileRejectedCLI(t *testing.T) {
	vmURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/cli-vm-no-nic?api-version=2024-07-01",
		baseURL, subscriptionID, resourceGroup)
	body := `{"location":"eastus","properties":{"hardwareProfile":{"vmSize":"Standard_B1s"}}}`

	out := runCLIExpectFailure(t, azRest("PUT", vmURL, body))
	if !strings.Contains(out, "InvalidParameter") {
		t.Fatalf("expected InvalidParameter for a virtual machine with no network interface, got %s", out)
	}
	if !strings.Contains(out, "networkProfile.networkInterfaces") {
		t.Fatalf("expected the error to name the offending parameter, got %s", out)
	}
	if strings.Contains(out, "OperationNotAllowed") {
		t.Fatalf("a permanently invalid request must not be reported as a retryable service problem, got %s", out)
	}
}

func TestNetworkSubnetInAbsentVirtualNetworkNotFoundCLI(t *testing.T) {
	subnetURL := armURL("Microsoft.Network",
		"virtualNetworks/cli-vnet-never-created/subnets/cli-orphan-subnet", "2024-05-01")

	out := runCLIExpectFailure(t, azRest("PUT", subnetURL, `{"properties":{"addressPrefix":"10.98.1.0/24"}}`))
	if !strings.Contains(out, "ResourceNotFound") {
		t.Fatalf("expected ResourceNotFound for a subnet whose virtual network is absent, got %s", out)
	}
	if !strings.Contains(out, "cli-vnet-never-created") {
		t.Fatalf("expected the error to name the absent virtual network, got %s", out)
	}
	if strings.Contains(out, "OperationNotAllowed") {
		t.Fatalf("an absent parent must not be reported as a host problem, got %s", out)
	}
}
