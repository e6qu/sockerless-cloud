package vminstance

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

func seedOf(t *testing.T, seed []sim.SeedFile, path string) sim.SeedFile {
	t.Helper()
	for _, f := range seed {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no seed file %s among %d", path, len(seed))
	return sim.SeedFile{}
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// User data arrives as cloud-init's user-data processor reads it: gzip is
// inflated, a MIME multipart message is taken part by part with its scripts
// numbered in order and its cloud-config parts merged, and a part of a type
// cloud-init would not run is reported.
func TestCloudInitSplitsUserDataAsCloudInitDoes(t *testing.T) {
	multipartData := strings.Join([]string{
		"Content-Type: multipart/mixed; boundary=\"XX\"",
		"MIME-Version: 1.0",
		"",
		"--XX",
		"Content-Type: text/cloud-config",
		"",
		"#cloud-config",
		"runcmd: [first]",
		"final_message: first",
		"--XX",
		"Content-Type: text/x-shellscript",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho part\n")),
		"--XX",
		"Content-Type: text/cloud-config",
		"",
		"#cloud-config",
		"runcmd: [second]",
		"--XX",
		"Content-Type: text/x-include-url",
		"",
		"http://example.com/more",
		"--XX--",
		"",
	}, "\r\n")
	seed, err := cloudInitSeed(CloudInit{Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: "i-1"}, gzipped(t, []byte(multipartData)))
	if err != nil {
		t.Fatal(err)
	}
	dir := "/var/lib/cloud/instances/i-1/"
	if got := string(seedOf(t, seed, dir+"scripts/part-002").Data); got != "#!/bin/sh\necho part\n" {
		t.Fatalf("the shell script part = %q", got)
	}
	if got := string(seedOf(t, seed, dir+"scripts/runcmd").Data); got != "#!/bin/sh\nsecond\n" {
		t.Fatalf("a later cloud-config part's runcmd replaces the earlier one's: %q", got)
	}
	if got := string(seedOf(t, seed, cloudInitEnvPath).Data); !strings.Contains(got, "CI_FINAL_MESSAGE='first'") {
		t.Fatalf("keys only the earlier part names are kept: %q", got)
	}
	if got := string(seedOf(t, seed, dir+"warnings").Data); !strings.Contains(got, "text/x-include-url") {
		t.Fatalf("an include part is reported, not fetched: %q", got)
	}
}

// A cloud-config document reads the way cloud-init's YAML 1.1 loader reads
// it: an unquoted leading-zero mode is octal, yes and no are booleans, and a
// list command is quoted word by word.
func TestCloudInitReadsCloudConfigAsYAML11(t *testing.T) {
	doc := "#cloud-config\n" +
		"write_files:\n" +
		"  - {path: /etc/a, content: aGk=, encoding: b64, permissions: 0600}\n" +
		"  - {path: /etc/b, content: x, permissions: '0755', owner: 'bob:bob', defer: yes, append: on}\n" +
		"runcmd:\n" +
		"  - [sh, -c, \"echo 'quoted' $HOME\"]\n" +
		"package_update: yes\n" +
		"packages: [curl, [jq, '1.7']]\n" +
		"timezone: UTC\n" +
		"bootcmd:\n" +
		"  - echo \"a: b\" is a mapping\n"
	seed, err := cloudInitSeed(CloudInit{Platform: "gce", Datasource: "DataSourceGCELocal", InstanceID: "1234"}, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	dir := "/var/lib/cloud/instances/1234/"
	writeFiles := string(seedOf(t, seed, dir+"write-files").Data)
	if !strings.Contains(writeFiles, "ci_write_file '/etc/a' '"+dir+"payloads/1' 'root:root' 0600 0") {
		t.Fatalf("write_files = %q", writeFiles)
	}
	if got := string(seedOf(t, seed, dir+"payloads/1").Data); got != "hi" {
		t.Fatalf("the b64 content decodes to %q", got)
	}
	deferred := string(seedOf(t, seed, dir+"write-files-deferred").Data)
	if !strings.Contains(deferred, "'/etc/b' '"+dir+"payloads/2' 'bob:bob' 0755 1") {
		t.Fatalf("write_files deferred = %q", deferred)
	}
	if got := string(seedOf(t, seed, dir+"scripts/runcmd").Data); got != "#!/bin/sh\n'sh' '-c' 'echo '\\''quoted'\\'' $HOME'\n" {
		t.Fatalf("runcmd = %q", got)
	}
	if got := string(seedOf(t, seed, dir+"packages").Data); got != "ci_packages 1 0 'curl' 'jq=1.7'\n" {
		t.Fatalf("packages = %q", got)
	}
	warnings := string(seedOf(t, seed, dir+"warnings").Data)
	if !strings.Contains(warnings, "timezone") || !strings.Contains(warnings, "Invalid cloud-config provided: bootcmd:") {
		t.Fatalf("a module the simulator does not run and a malformed module are reported: %q", warnings)
	}
	for _, f := range seed {
		if f.Path == dir+"bootcmd" {
			t.Fatal("a malformed bootcmd is skipped, the other modules kept")
		}
	}
}

// The image's cloud-init runs each module in its stage and at its
// frequency: bootcmd at every boot, write_files before the users exist and
// deferred files after, the users with their groups, keys and sudo rules,
// the datasource's keys on the default user and behind disable_root on root,
// and runcmd once per instance; it leaves boot-finished and result.json and
// prints the final message.
func TestInstanceRunsItsCloudConfigInCloudInitsStages(t *testing.T) {
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(ctx, cloudInitImage, platform); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-cloud-init-" + sim.RandomHex(4)
	netID, cidr, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	const datasourceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDatasourceKeyForTheDefaultUser0000000000 datasource"
	const topKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITopLevelKeyForTheDefaultUser000000000000 top"
	const aliceKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAliceKey00000000000000000000000000000000 alice"
	cloudConfig := "#cloud-config\n" +
		"bootcmd:\n" +
		"  - echo \"bootcmd $INSTANCE_ID\" >> /var/order\n" +
		"write_files:\n" +
		"  - path: /etc/early.conf\n" +
		"    content: " + base64.StdEncoding.EncodeToString([]byte("early\n")) + "\n" +
		"    encoding: b64\n" +
		"    permissions: '0600'\n" +
		"  - path: /home/alice/late.txt\n" +
		"    content: late\n" +
		"    owner: alice:alice\n" +
		"    permissions: '0640'\n" +
		"    defer: true\n" +
		"groups:\n" +
		"  - ops: [alice]\n" +
		"users:\n" +
		"  - default\n" +
		"  - name: alice\n" +
		"    gecos: Alice\n" +
		"    shell: /bin/bash\n" +
		"    groups: adm\n" +
		"    sudo: ALL=(ALL) NOPASSWD:ALL\n" +
		"    ssh_authorized_keys: ['" + aliceKey + "']\n" +
		"ssh_authorized_keys: ['" + topKey + "']\n" +
		"runcmd:\n" +
		"  - echo runcmd >> /var/order\n" +
		"  - [sh, -c, 'echo \"alice is $(id -u alice)\" >> /var/order']\n" +
		"final_message: \"booted in $UPTIME s from $DATASOURCE\"\n"
	backend := placement.ContainerBackend{}
	sink := &lineSink{}
	spec := Spec{
		Name:     "sockerless-sim-test-cloud-init-" + sim.RandomHex(4),
		ID:       "i-cloudconfig",
		Hostname: "ip-10-0-0-8",
		Image:    Image{OCI: cloudInitImage, Architecture: platform},
		Network:  network,
		Address:  cidr.Masked().Addr().Next().Next().Next().Next().Next().String(),
		UserData: []byte(cloudConfig),
		CloudInit: &CloudInit{
			Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: "i-cloudconfig",
			DefaultUserKeys: []string{datasourceKey},
		},
		VCPUs:     1,
		MemoryMiB: 128,
		StopGrace: time.Second,
	}
	handle, err := Launch(ctx, backend, spec, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handle.Cancel()
		_ = backend.Remove(ctx, handle.ID())
	})
	waitReady(t, ctx, handle, sink)
	run := func(h placement.Handle, script string) string {
		t.Helper()
		res, err := h.Exec(ctx, []string{"sh", "-c", script})
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: %v %+v\nconsole:\n%s", script, err, res, sink.text())
		}
		return res.Stdout
	}
	if got := run(handle, "cat /var/order"); got != "bootcmd i-cloudconfig\nruncmd\nalice is 1001\n" {
		t.Fatalf("the modules ran out of order or with the wrong environment: %q\nconsole:\n%s", got, sink.text())
	}
	if got := run(handle, "stat -c '%a %U' /etc/early.conf; cat /etc/early.conf; stat -c '%a %U:%G' /home/alice/late.txt"); got != "600 root\nearly\n640 alice:alice\n" {
		t.Fatalf("write_files: %q", got)
	}
	// The groups module runs first, so ops and the default user's lxd take
	// the first free GIDs and alice's private group the next one, as useradd
	// picks it.
	if got := run(handle, "grep -E '^(ubuntu|alice):' /etc/passwd; grep -E '^(ops|lxd|alice):' /etc/group | cut -d: -f1,3"); got != "ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash\nalice:x:1001:1003:Alice:/home/alice:/bin/bash\nops:1001\nlxd:1002\nalice:1003\n" {
		t.Fatalf("users: %q", got)
	}
	groups := run(handle, "grep -E '^(ops|adm|sudo|lxd):' /etc/group")
	for _, want := range []string{"ops:x:", "adm:x:4:ubuntu,alice", "sudo:x:27:ubuntu", "lxd:x:"} {
		if !strings.Contains(groups, want) {
			t.Fatalf("groups lack %q:\n%s", want, groups)
		}
	}
	if !strings.Contains(groups, "ops:x:1002:alice") && !strings.Contains(groups, ":alice\n") {
		t.Fatalf("alice is not in ops:\n%s", groups)
	}
	if got := run(handle, "grep -E '^(ubuntu|alice):' /etc/shadow | cut -d: -f1,2"); got != "ubuntu:!\nalice:!\n" {
		t.Fatalf("the passwords are not locked: %q", got)
	}
	sudoers := run(handle, "cat /etc/sudoers.d/90-cloud-init-users; stat -c %a /etc/sudoers.d/90-cloud-init-users")
	if !strings.Contains(sudoers, "ubuntu ALL=(ALL) NOPASSWD:ALL") || !strings.Contains(sudoers, "alice ALL=(ALL) NOPASSWD:ALL") || !strings.HasSuffix(sudoers, "440\n") {
		t.Fatalf("sudoers: %q", sudoers)
	}
	if got := run(handle, "cat /home/ubuntu/.ssh/authorized_keys; stat -c '%a %U' /home/ubuntu/.ssh/authorized_keys"); got != datasourceKey+"\n"+topKey+"\n600 ubuntu\n" {
		t.Fatalf("the default user's keys: %q", got)
	}
	if got := run(handle, "cat /home/alice/.ssh/authorized_keys"); got != aliceKey+"\n" {
		t.Fatalf("alice's keys: %q", got)
	}
	if got := run(handle, "cat /root/.ssh/authorized_keys"); !strings.Contains(got, `Please login as the user \"ubuntu\" rather than the user \"root\".`) || !strings.Contains(got, datasourceKey) {
		t.Fatalf("root's keys: %q", got)
	}
	if got := run(handle, "cat /run/cloud-init/result.json"); got != "{\n \"v1\": {\n  \"datasource\": \"DataSourceEc2Local\",\n  \"errors\": []\n }\n}\n" {
		t.Fatalf("result.json: %q", got)
	}
	run(handle, "test -s "+BootFinishedPath)
	if !strings.Contains(sink.text(), "booted in ") || !strings.Contains(sink.text(), " s from DataSourceEc2Local") {
		t.Fatalf("the console lacks the final message:\n%s", sink.text())
	}

	handle.Cancel()
	<-handle.Exited()
	again, err := backend.Restart(ctx, handle.ID(), mustWorkload(t, spec), sink)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, again, sink)
	if got := run(again, "cat /var/order"); got != "bootcmd i-cloudconfig\nruncmd\nalice is 1001\nbootcmd i-cloudconfig\n" {
		t.Fatalf("a second boot runs bootcmd again and nothing that runs once per instance: %q", got)
	}
}

// amazonLinuxImage carries no shadow-utils, which the accounts module does
// without.
const amazonLinuxImage = "mirror.gcr.io/library/amazonlinux:2023@sha256:c04a1012acc483977f61f7f91c28624a39a621e56a59781319a75dec528be9b6"

// On Amazon Linux 2023 the default user is ec2-user, in the wheel group,
// holding the key pair's key, though the image has no useradd.
func TestInstanceCreatesAmazonLinuxsDefaultUserWithoutShadowUtils(t *testing.T) {
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(ctx, amazonLinuxImage, platform); err != nil {
		t.Fatal(err)
	}
	network := "sockerless-sim-test-cloud-init-" + sim.RandomHex(4)
	netID, cidr, err := sim.EnsurePlatformNetwork(ctx, network, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = sim.DockerClient().NetworkRemove(ctx, netID, client.NetworkRemoveOptions{}) })
	const keyPair = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDKeyPairMaterial000000000000000000000 my-key"
	backend := placement.ContainerBackend{}
	sink := &lineSink{}
	handle, err := Launch(ctx, backend, Spec{
		Name:     "sockerless-sim-test-cloud-init-" + sim.RandomHex(4),
		ID:       "i-amazonlinux",
		Hostname: "ip-10-0-0-9",
		Image:    Image{OCI: amazonLinuxImage, Architecture: platform},
		Network:  network,
		Address:  cidr.Masked().Addr().Next().Next().Next().Next().Next().Next().String(),
		CloudInit: &CloudInit{
			Platform: "ec2", Datasource: "DataSourceEc2Local", InstanceID: "i-amazonlinux",
			DefaultUserKeys: []string{keyPair},
		},
		VCPUs:     1,
		MemoryMiB: 128,
		StopGrace: time.Second,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handle.Cancel()
		_ = backend.Remove(ctx, handle.ID())
	})
	waitReady(t, ctx, handle, sink)
	res, err := handle.Exec(ctx, []string{"sh", "-c", "grep '^ec2-user:' /etc/passwd | cut -d: -f1,3,5,6,7; grep '^wheel:' /etc/group | cut -d: -f4; cat /home/ec2-user/.ssh/authorized_keys; cat /run/cloud-init/result.json"})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	want := "ec2-user:1000:EC2 Default User:/home/ec2-user:/bin/bash\nec2-user\n" + keyPair + "\n" +
		"{\n \"v1\": {\n  \"datasource\": \"DataSourceEc2Local\",\n  \"errors\": []\n }\n}\n"
	if res.Stdout != want {
		t.Fatalf("Amazon Linux's default user:\n%s\nconsole:\n%s", res.Stdout, sink.text())
	}
	if !strings.Contains(sink.text(), "Cloud-init v. 22.2.2 finished at ") {
		t.Fatalf("the console lacks Amazon Linux's cloud-init final message:\n%s", sink.text())
	}
}
