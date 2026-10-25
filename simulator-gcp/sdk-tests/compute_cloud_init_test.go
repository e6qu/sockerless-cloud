package gcp_sdk_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

func ubuntuFamily() string {
	if scratchimage.NativePlatform() == "linux/arm64" {
		return "ubuntu-2404-lts-arm64"
	}
	return "ubuntu-2404-lts-amd64"
}

// An Ubuntu instance's cloud-init runs the user-data metadata item, giving
// the default user ubuntu the ssh-keys entries named for it, and the guest
// environment runs startup-script after it; a Debian instance carries no
// cloud-init and runs the startup script alone.
func TestCompute_UbuntuInstanceRunsItsCloudConfig(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone, region = "cloud-init-project", "us-central1-a", "us-central1"
	subnet := createInstanceSubnetwork(t, svc, project, region, "cloud-init-net", "10.89.0.0/24")
	const ubuntuKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPAkqz56K/EWsdIxGWKGQ5SxtXw8WhqPshWaZNevDsZv ubuntu@test"
	const otherKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherUsersKey000000000000000000000000000 alice@test"
	userData := "#cloud-config\n" +
		"write_files:\n" +
		"  - path: /etc/app.conf\n" +
		"    content: from cloud-init\n" +
		"runcmd:\n" +
		"  - 'echo \"runcmd: $(cat /etc/app.conf)\"'\n" +
		"  - 'echo \"keys: $(cat /home/ubuntu/.ssh/authorized_keys)\"'\n"
	launch := func(name, imageProject, family string) {
		t.Helper()
		op, err := svc.Instances.Insert(project, zone, &compute.Instance{
			Name:        name,
			MachineType: "e2-micro",
			Disks: []*compute.AttachedDisk{{
				Boot: true, AutoDelete: true,
				InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: "projects/" + imageProject + "/global/images/family/" + family},
			}},
			NetworkInterfaces: []*compute.NetworkInterface{{Subnetwork: subnet}},
			Metadata: &compute.Metadata{Items: []*compute.MetadataItems{
				{Key: "user-data", Value: googleapi.String(userData)},
				{Key: "ssh-keys", Value: googleapi.String("ubuntu:" + ubuntuKey + "\nalice:" + otherKey)},
				{Key: "startup-script", Value: googleapi.String("#!/bin/sh\necho \"startup sees: $(cat /etc/app.conf 2>/dev/null || echo nothing)\"\n")},
			}},
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, name).Do() })
		done := awaitZoneOperation(t, svc, project, zone, op.Name)
		require.Nil(t, done.Error, "the insert of %s failed: %+v", name, done.Error)
	}
	launch("cloud-init-ubuntu", "ubuntu-os-cloud", ubuntuFamily())
	launch("cloud-init-debian", "debian-cloud", debianFamily())

	ubuntu := awaitSerialPortOutput(t, svc, project, zone, "cloud-init-ubuntu", "startup-script exit status 0")
	assert.Contains(t, ubuntu, "runcmd: from cloud-init")
	assert.Contains(t, ubuntu, "keys: "+ubuntuKey, "the default user holds only the keys named for it")
	assert.NotContains(t, ubuntu, "alice@test")
	assert.Contains(t, ubuntu, "Cloud-init v. 24.1.3-0ubuntu3 finished at ")
	assert.Contains(t, ubuntu, "Datasource DataSourceGCELocal.")
	assert.Contains(t, ubuntu, "startup-script: startup sees: from cloud-init", "the startup script runs after cloud-init")

	debian := awaitSerialPortOutput(t, svc, project, zone, "cloud-init-debian", "startup-script exit status 0")
	assert.NotContains(t, debian, "Cloud-init v.", "Debian's Compute Engine image carries no cloud-init")
	assert.Contains(t, debian, "startup-script: startup sees: nothing")
}
