package azure_tf_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformManagedDiskAttachment attaches an azurerm_managed_disk to a
// running machine with azurerm_virtual_machine_data_disk_attachment. The
// Custom Script extension that depends on the attachment writes to the disk at
// its LUN's path, so an attachment that never reached the guest fails the
// apply; the destroy detaches the disk before it deletes it, which Azure
// requires.
func TestTerraformManagedDiskAttachment(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("manageddisk"))
	run := func(name string, args ...string) {
		t.Helper()
		out, err := runTimed(t, name, terraformCmd(dir, args...))
		require.NoError(t, err, "%s failed:\n%s", name, out)
	}
	run("terraform init", "init")
	run("terraform apply", "apply", "-auto-approve")
	run("terraform plan", "plan", "-detailed-exitcode")

	outputs := readOutputs(t, dir)
	diskID := outputs.must(t, "disk_id")
	require.Contains(t, diskID, "/resourceGroups/tf-azrm-disk-rg/providers/Microsoft.Compute/disks/tf-azrm-data-disk",
		"the managed disk id must carry the canonical ARM path; got %s", diskID)
	attachmentID := outputs.must(t, "attachment_id")
	require.True(t, strings.HasSuffix(attachmentID, "/virtualMachines/tf-azrm-disk-vm/dataDisks/tf-azrm-data-disk"),
		"the attachment id names the machine and the disk; got %s", attachmentID)

	run("terraform destroy", "destroy", "-auto-approve")
}
