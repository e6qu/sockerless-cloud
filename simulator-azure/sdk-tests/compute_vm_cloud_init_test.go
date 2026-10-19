package azure_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute"
	"github.com/stretchr/testify/assert"
)

// A machine's cloud-init runs the #cloud-config its customData carries,
// creating the osProfile's adminUsername as the default user with the
// linuxConfiguration's public keys and running each module in its stage.
func TestCompute_VirtualMachineRunsItsCloudConfig(t *testing.T) {
	t.Parallel()
	const rg = "vm-cloud-init-rg"
	const vmName = "vm-cloud-init"
	const adminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPAkqz56K/EWsdIxGWKGQ5SxtXw8WhqPshWaZNevDsZv azureuser@test"
	nicID := vmOperationsFixture(t, rg, vmName, "10.253.0.0/16")
	cloudConfig := "#cloud-config\n" +
		"write_files:\n" +
		"  - path: /etc/app/settings.conf\n" +
		"    content: written by cloud-init\n" +
		"groups: [operators]\n" +
		"users:\n" +
		"  - default\n" +
		"  - {name: svc, system: true, shell: /usr/sbin/nologin}\n" +
		"runcmd:\n" +
		"  - [sh, -c, 'id -nG azureuser > /var/tmp/default-groups']\n"
	createOperationsVM(t, rg, vmName, nicID, func(vm *armcompute.VirtualMachine) {
		vm.Properties.OSProfile.CustomData = to.Ptr(base64.StdEncoding.EncodeToString([]byte(cloudConfig)))
		vm.Properties.OSProfile.LinuxConfiguration = &armcompute.LinuxConfiguration{
			SSH: &armcompute.SSHConfiguration{PublicKeys: []*armcompute.SSHPublicKey{{
				Path:    to.Ptr("/home/azureuser/.ssh/authorized_keys"),
				KeyData: to.Ptr(adminKey),
			}}},
		}
	})

	assert.Equal(t, "Enable succeeded: \n[stdout]\nwritten by cloud-init\n\n[stderr]\n",
		runInGuest(t, rg, vmName, "settings", "cat /etc/app/settings.conf; echo"))
	assert.Contains(t, runInGuest(t, rg, vmName, "admin-key", "cat /home/azureuser/.ssh/authorized_keys"), adminKey,
		"the adminUsername holds the osProfile's key")
	groups := runInGuest(t, rg, vmName, "default-groups", "cat /var/tmp/default-groups")
	_, listed, _ := strings.Cut(groups, "[stdout]\n")
	listed, _, _ = strings.Cut(listed, "\n")
	assert.ElementsMatch(t, []string{"azureuser", "adm", "audio", "cdrom", "dialout", "dip", "floppy", "netdev", "plugdev", "sudo", "video"},
		strings.Fields(listed), "the adminUsername is Debian's default user, in its default groups")
	svc := runInGuest(t, rg, vmName, "svc", "getent passwd svc | cut -d: -f3,7; getent group operators | cut -d: -f1")
	assert.Contains(t, svc, "/usr/sbin/nologin")
	assert.Contains(t, svc, "operators")
	assert.Contains(t, runInGuest(t, rg, vmName, "result", "cat /run/cloud-init/result.json"), `"datasource": "DataSourceAzure [seed=/dev/sr0]"`)
}
