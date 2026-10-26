package gcp_sdk_test

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/scratchimage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// debianFamily matches the engine's architecture, which an instance booted on
// a container backend runs.
func debianFamily() string {
	if scratchimage.NativePlatform() == "linux/arm64" {
		return "debian-12-arm64"
	}
	return "debian-12"
}

// The public image catalog answers the way Google's image projects do: an
// image by its versioned name, a family by the image it resolves to, a
// listing of the project, and a name the project does not hold is not found.
func TestCompute_PublicImageCatalog(t *testing.T) {
	t.Parallel()
	svc := computeService(t)

	family, err := svc.Images.GetFromFamily("debian-cloud", "debian-12").Do()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(family.Name, "debian-12-bookworm-v"), "family resolves to a versioned image, got %s", family.Name)
	assert.Equal(t, "debian-12", family.Family)
	assert.Equal(t, "X86_64", family.Architecture)
	assert.Equal(t, "READY", family.Status)
	assert.Contains(t, family.SelfLink, "/projects/debian-cloud/global/images/"+family.Name)

	got, err := svc.Images.Get("debian-cloud", family.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, family.Id, got.Id, "the family's image and the image itself are the same resource")

	arm, err := svc.Images.GetFromFamily("ubuntu-os-cloud", "ubuntu-2404-lts-arm64").Do()
	require.NoError(t, err)
	assert.Equal(t, "ARM64", arm.Architecture)
	assert.True(t, strings.HasPrefix(arm.Name, "ubuntu-2404-noble-arm64-v"), "got %s", arm.Name)

	list, err := svc.Images.List("ubuntu-os-cloud").Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(i *compute.Image) string { return i.Name }, arm.Name)
	assertComputeListHasName(t, list.Items, func(i *compute.Image) string { return i.Name }, strings.Replace(arm.Name, "arm64", "amd64", 1))

	// A family is not an image name: Google answers the plain name with the same
	// not-found as any absent resource.
	_, err = svc.Images.Get("debian-cloud", "debian-12").Do()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 404, gerr.Code)
	_, err = svc.Images.GetFromFamily("debian-cloud", "debian-99").Do()
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 404, gerr.Code)
}

// An instance boots its image on the subnetwork it names, runs the
// startup-script its metadata carries with the script's output on the serial
// console, reaches the metadata server and the private zones of its network
// through the subnetwork's resolver, keeps its disk across a stop and a
// start, and a disk image captured from it boots with that disk's contents.
func TestCompute_InstanceBootsRunsItsStartupScriptAndKeepsItsDisk(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone, region = "boot-project", "us-central1-a", "us-central1"
	subnet := createInstanceSubnetwork(t, svc, project, region, "boot-net", "10.86.0.0/24")

	// A private zone visible to the instance's network, holding a record only
	// a querier on that network can see.
	dnsSvc, err := dns.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = dnsSvc.ManagedZones.Create(project, &dns.ManagedZone{
		Name: "boot-zone", DnsName: "boot.internal.", Visibility: "private",
		PrivateVisibilityConfig: &dns.ManagedZonePrivateVisibilityConfig{
			Networks: []*dns.ManagedZonePrivateVisibilityConfigNetwork{{NetworkUrl: "projects/" + project + "/global/networks/boot-net"}},
		},
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { deleteDNSZone(t, dnsSvc, project, "boot-zone") })
	_, err = dnsSvc.ResourceRecordSets.Create(project, "boot-zone", &dns.ResourceRecordSet{
		Name: "db.boot.internal.", Type: "A", Ttl: 60, Rrdatas: []string{"10.86.0.50"},
	}).Do()
	require.NoError(t, err)

	// The script counts its boots on the disk, reads the metadata server by
	// the name every Compute Engine guest resolves it under, and resolves the
	// private zone through the subnetwork's resolver.
	const startupScript = `#!/bin/bash
n=0
[ -f /var/lib/sockerless-boots ] && n=$(cat /var/lib/sockerless-boots)
n=$((n+1))
echo "$n" >/var/lib/sockerless-boots
echo "boot=$n"
md() {
	exec 3<>/dev/tcp/metadata.google.internal/80
	printf 'GET %s HTTP/1.0\r\nHost: metadata.google.internal\r\nMetadata-Flavor: Google\r\n\r\n' "$1" >&3
	sed -e '1,/^\r$/d' <&3
	exec 3<&-
}
echo "name=$(md /computeMetadata/v1/instance/name)"
echo "id=$(md /computeMetadata/v1/instance/id)"
echo "zone=$(md /computeMetadata/v1/instance/zone)"
echo "hostname=$(md /computeMetadata/v1/instance/hostname)"
echo "project-id=$(md /computeMetadata/v1/project/project-id)"
echo "marker=$(md /computeMetadata/v1/instance/attributes/marker)"
echo "project-marker=$(md /computeMetadata/v1/project/attributes/project-marker)"
echo "ip=$(md /computeMetadata/v1/instance/network-interfaces/0/ip)"
echo "mac=$(md /computeMetadata/v1/instance/network-interfaces/0/mac)"
echo "eth0=$(cat /sys/class/net/eth0/address)"
echo "gateway=$(md /computeMetadata/v1/instance/network-interfaces/0/gateway)"
echo "subnetmask=$(md /computeMetadata/v1/instance/network-interfaces/0/subnetmask)"
echo "network=$(md /computeMetadata/v1/instance/network-interfaces/0/network)"
echo "external-ip=$(md /computeMetadata/v1/instance/network-interfaces/0/access-configs/0/external-ip)"
echo "db=$(getent hosts db.boot.internal | awk '{print $1}')"
`
	// Every instance of the project reads the project's common metadata.
	number := requireProject(t, project)
	_, err = svc.Projects.SetCommonInstanceMetadata(project, &compute.Metadata{Items: []*compute.MetadataItems{
		{Key: "project-marker", Value: googleapi.String("from-the-project")},
	}}).Do()
	require.NoError(t, err)

	const name = "boot-vm"
	instance := func(name, sourceImage string) *compute.Instance {
		return &compute.Instance{
			Name:        name,
			MachineType: "e2-micro",
			Disks: []*compute.AttachedDisk{{
				Boot: true, AutoDelete: true,
				InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: sourceImage},
			}},
			NetworkInterfaces: []*compute.NetworkInterface{{
				Subnetwork:    subnet,
				AccessConfigs: []*compute.AccessConfig{{Name: "External NAT", Type: "ONE_TO_ONE_NAT"}},
			}},
			Metadata: &compute.Metadata{Items: []*compute.MetadataItems{
				{Key: "startup-script", Value: googleapi.String(startupScript)},
				{Key: "marker", Value: googleapi.String("from-metadata")},
			}},
		}
	}
	op, err := svc.Instances.Insert(project, zone, instance(name, "projects/debian-cloud/global/images/family/"+debianFamily())).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, name).Do() })
	done := awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the insert operation failed: %+v", done.Error)

	got, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	assert.Equal(t, "RUNNING", got.Status)
	assert.True(t, strings.HasPrefix(got.NetworkInterfaces[0].NetworkIP, "10.86.0."), "the address is leased from the subnetwork, got %s", got.NetworkInterfaces[0].NetworkIP)

	console := awaitSerialPortOutput(t, svc, project, zone, name, "startup-script exit status 0")
	assert.Contains(t, console, "startup-script: boot=1")
	assert.Contains(t, console, "startup-script: name="+name, "the metadata server answers the instance's own name through the subnetwork's sidecar")
	assert.Contains(t, console, "startup-script: marker=from-metadata", "the instance reads its custom metadata")
	assert.Contains(t, console, "startup-script: id="+strconv.FormatUint(got.Id, 10), "the instance reads its own id")
	assert.Contains(t, console, "startup-script: zone=projects/"+number+"/zones/"+zone)
	assert.Contains(t, console, "startup-script: hostname="+name+"."+zone+".c."+project+".internal")
	assert.Contains(t, console, "startup-script: project-id="+project)
	assert.Contains(t, console, "startup-script: db=10.86.0.50", "the private zone of the network resolves through the subnetwork's resolver")
	assert.Contains(t, console, "startup-script: project-marker=from-the-project", "the instance reads its project's common metadata")
	iface := got.NetworkInterfaces[0]
	address, err := netip.ParseAddr(iface.NetworkIP)
	require.NoError(t, err)
	octets := address.As4()
	mac := fmt.Sprintf("42:01:%02x:%02x:%02x:%02x", octets[0], octets[1], octets[2], octets[3])
	assert.Contains(t, console, "startup-script: ip="+iface.NetworkIP)
	assert.Contains(t, console, "startup-script: mac="+mac, "Compute Engine's MAC is 42:01 and the interface's address")
	assert.Contains(t, console, "startup-script: eth0="+mac, "the guest's interface carries the MAC the metadata server reports")
	assert.Contains(t, console, "startup-script: gateway=10.86.0.1")
	assert.Contains(t, console, "startup-script: subnetmask=255.255.255.0")
	assert.Contains(t, console, "startup-script: network=projects/")
	assert.Contains(t, console, "/networks/boot-net")
	require.Len(t, iface.AccessConfigs, 1)
	assert.NotEmpty(t, iface.AccessConfigs[0].NatIP, "an access config naming no address gets an ephemeral one")
	assert.Contains(t, console, "startup-script: external-ip="+iface.AccessConfigs[0].NatIP)

	// A stop keeps the disk: the next boot counts on, and runs the
	// startup-script the metadata holds when it boots.
	stopOp, err := svc.Instances.Stop(project, zone, name).Do()
	require.NoError(t, err)
	stopDone := awaitZoneOperation(t, svc, project, zone, stopOp.Name)
	require.Nil(t, stopDone.Error, "the stop operation failed: %+v", stopDone.Error)
	stopped, err := svc.Instances.Get(project, zone, name).Do()
	require.NoError(t, err)
	setOp, err := svc.Instances.SetMetadata(project, zone, name, &compute.Metadata{
		Fingerprint: stopped.Metadata.Fingerprint,
		Items: []*compute.MetadataItems{
			{Key: "startup-script", Value: googleapi.String(startupScript + "echo revised=yes\n")},
			{Key: "marker", Value: googleapi.String("from-metadata")},
		},
	}).Do()
	require.NoError(t, err)
	setDone := awaitZoneOperation(t, svc, project, zone, setOp.Name)
	require.Nil(t, setDone.Error, "the setMetadata operation failed: %+v", setDone.Error)
	startOp, err := svc.Instances.Start(project, zone, name).Do()
	require.NoError(t, err)
	startDone := awaitZoneOperation(t, svc, project, zone, startOp.Name)
	require.Nil(t, startDone.Error, "the start operation failed: %+v", startDone.Error)
	console = awaitSerialPortOutput(t, svc, project, zone, name, "startup-script: boot=2")
	assert.Contains(t, console, "startup-script: boot=1", "the serial console keeps what the machine wrote before the stop")
	console = awaitSerialPortOutput(t, svc, project, zone, name, "startup-script: revised=yes")
	assert.Equal(t, 1, strings.Count(console, "startup-script: revised=yes"), "the revised startup-script ran on the boot after it was set")

	// A disk image is captured from the stopped instance's boot disk, and an
	// instance created from it continues the count that disk held.
	stopOp, err = svc.Instances.Stop(project, zone, name).Do()
	require.NoError(t, err)
	stopDone = awaitZoneOperation(t, svc, project, zone, stopOp.Name)
	require.Nil(t, stopDone.Error, "the stop operation failed: %+v", stopDone.Error)
	_, err = svc.Images.Insert(project, &compute.Image{
		Name:       "boot-capture",
		SourceDisk: fmt.Sprintf("zones/%s/disks/%s", zone, name),
		Family:     "boot-captures",
	}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Images.Delete(project, "boot-capture").Do() })
	captured, err := svc.Images.Get(project, "boot-capture").Do()
	require.NoError(t, err)
	assert.Equal(t, "READY", captured.Status)
	assert.Contains(t, captured.SourceDisk, "/zones/"+zone+"/disks/"+name)
	fromFamily, err := svc.Images.GetFromFamily(project, "boot-captures").Do()
	require.NoError(t, err)
	assert.Equal(t, "boot-capture", fromFamily.Name)

	const clone = "boot-vm-clone"
	op, err = svc.Instances.Insert(project, zone, instance(clone, "projects/"+project+"/global/images/boot-capture")).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, clone).Do() })
	done = awaitZoneOperation(t, svc, project, zone, op.Name)
	require.Nil(t, done.Error, "the clone's insert operation failed: %+v", done.Error)
	console = awaitSerialPortOutput(t, svc, project, zone, clone, "startup-script exit status 0")
	assert.Contains(t, console, "startup-script: boot=3", "the captured disk carries the two boots of its source")
	assert.Contains(t, console, "startup-script: name="+clone)

	// An image the catalog does not hold cannot start an instance.
	_, err = svc.Instances.Insert(project, zone, instance("boot-nowhere", "projects/debian-cloud/global/images/family/debian-99")).Do()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 400, gerr.Code)
	assert.Contains(t, gerr.Message, "The referenced image resource cannot be found")

	// Neither can a machine type no zone offers.
	unsized := instance("boot-unsized", "projects/debian-cloud/global/images/family/"+debianFamily())
	unsized.MachineType = "x9-enormous-64"
	_, err = svc.Instances.Insert(project, zone, unsized).Do()
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 400, gerr.Code)
	assert.Contains(t, gerr.Message, "Machine type with name 'x9-enormous-64' does not exist")
}

// awaitSerialPortOutput reads the serial console by offset, as gcloud tails
// it, until it carries marker: the console has no wait of its own.
func awaitSerialPortOutput(t *testing.T, svc *compute.Service, project, zone, name, marker string) string {
	t.Helper()
	var contents string
	require.Eventually(t, func() bool {
		out, err := svc.Instances.GetSerialPortOutput(project, zone, name).Do()
		if err != nil {
			return false
		}
		contents = out.Contents
		return strings.Contains(contents, marker)
	}, 30*time.Second, 500*time.Millisecond, "the serial console never carried %q; last contents:\n%s", marker, contents)
	return contents
}

// The machine type catalog reports each type's shape and refuses a name no
// zone offers.
func TestCompute_MachineTypeCatalog(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone = "test-project", "us-central1-a"

	micro, err := svc.MachineTypes.Get(project, zone, "e2-micro").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(2), micro.GuestCpus)
	assert.Equal(t, int64(1024), micro.MemoryMb)
	assert.True(t, micro.IsSharedCpu)
	standard, err := svc.MachineTypes.Get(project, zone, "n2-standard-4").Do()
	require.NoError(t, err)
	assert.Equal(t, int64(4), standard.GuestCpus)
	assert.Equal(t, int64(16384), standard.MemoryMb)
	assert.False(t, standard.IsSharedCpu)

	list, err := svc.MachineTypes.List(project, zone).Do()
	require.NoError(t, err)
	assertComputeListHasName(t, list.Items, func(m *compute.MachineType) string { return m.Name }, "t2a-standard-2")

	_, err = svc.MachineTypes.Get(project, zone, "x9-enormous-64").Do()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 404, gerr.Code)
}

// An instance receives what the network's firewall rules targeting it
// allow and nothing else: a neighbour in the subnetwork reaches the port a
// rule allows and not another, and a rule inserted while the instance runs
// allows its port to an instance booted after it.
func TestCompute_FirewallRulesFilterWhatAnInstanceReceives(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, zone, region = "firewall-project", "us-central1-a", "us-central1"
	subnet := createInstanceSubnetwork(t, svc, project, region, "fw-net", "10.87.0.0/24")
	firewall := func(name string, port string) {
		_, err := svc.Firewalls.Insert(project, &compute.Firewall{
			Name: name, Network: "projects/" + project + "/global/networks/fw-net", Direction: "INGRESS",
			Allowed:      []*compute.FirewallAllowed{{IPProtocol: "tcp", Ports: []string{port}}},
			SourceRanges: []string{"10.87.0.0/24"}, TargetTags: []string{"web"},
		}).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Firewalls.Delete(project, name).Do() })
	}
	firewall("fw-allow-80", "80")

	// The server answers on two ports; the client reports which it reaches. The
	// listeners leave the script's output behind, since the script runner reads
	// that output to its end before it reports the script's exit.
	const serverScript = `#!/bin/bash
serve() { perl -MIO::Socket::INET -e '$s=IO::Socket::INET->new(LocalPort=>$ARGV[0],Listen=>5,Reuse=>1) or die; while($c=$s->accept){print $c "HTTP/1.0 200 OK\r\n\r\nok\n"; close $c}' "$1" >/dev/null 2>&1 & }
serve 80
serve 81
echo "serving"
`
	clientScript := func(server string, ports ...int) string {
		script := "#!/bin/bash\n"
		for _, port := range ports {
			script += fmt.Sprintf("if timeout 5 bash -c 'exec 3<>/dev/tcp/%s/%d'; then echo \"port%d=open\"; else echo \"port%d=closed\"; fi\n", server, port, port, port)
		}
		return script
	}
	instance := func(name, script string, tags []string) *compute.Instance {
		inst := &compute.Instance{
			Name:        name,
			MachineType: "e2-micro",
			Disks: []*compute.AttachedDisk{{
				Boot: true, AutoDelete: true,
				InitializeParams: &compute.AttachedDiskInitializeParams{SourceImage: "projects/debian-cloud/global/images/family/" + debianFamily()},
			}},
			NetworkInterfaces: []*compute.NetworkInterface{{
				Subnetwork:    subnet,
				AccessConfigs: []*compute.AccessConfig{{Name: "External NAT", Type: "ONE_TO_ONE_NAT"}},
			}},
			Metadata: &compute.Metadata{Items: []*compute.MetadataItems{{Key: "startup-script", Value: googleapi.String(script)}}},
		}
		if len(tags) > 0 {
			inst.Tags = &compute.Tags{Items: tags}
		}
		return inst
	}
	boot := func(inst *compute.Instance) *compute.Instance {
		op, err := svc.Instances.Insert(project, zone, inst).Do()
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = svc.Instances.Delete(project, zone, inst.Name).Do() })
		done := awaitZoneOperation(t, svc, project, zone, op.Name)
		require.Nil(t, done.Error, "the insert of %s failed: %+v", inst.Name, done.Error)
		got, err := svc.Instances.Get(project, zone, inst.Name).Do()
		require.NoError(t, err)
		return got
	}
	server := boot(instance("fw-web", serverScript, []string{"web"}))
	awaitSerialPortOutput(t, svc, project, zone, "fw-web", "startup-script: serving")
	serverIP := server.NetworkInterfaces[0].NetworkIP

	boot(instance("fw-probe", clientScript(serverIP, 80, 81), nil))
	console := awaitSerialPortOutput(t, svc, project, zone, "fw-probe", "startup-script exit status 0")
	assert.Contains(t, console, "startup-script: port80=open", "the allowed port does not answer the neighbour")
	assert.Contains(t, console, "startup-script: port81=closed", "a port no rule allows answered the neighbour")

	firewall("fw-allow-81", "81")
	boot(instance("fw-probe-2", clientScript(serverIP, 81), nil))
	console = awaitSerialPortOutput(t, svc, project, zone, "fw-probe-2", "startup-script exit status 0")
	assert.Contains(t, console, "startup-script: port81=open", "the rule inserted while the server runs does not allow its port")
}
