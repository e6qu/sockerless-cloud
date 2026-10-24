//go:build kvm

package nodeagent_test

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
	"github.com/e6qu/sockerless-cloud/sim/nodeagent"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

const memberImage = "mirror.gcr.io/library/busybox:latest"

const guestImage = "mirror.gcr.io/library/golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c"

// The agent's proof, on a host with /dev/kvm: the agent starts as a container
// on an engine network, a guest built from an OCI image boots on that network
// with the hosts entries it was given, builds and runs the repository's
// arithmetic program as its first process, answers a command over the agent,
// boots again on its kept disk with its console continuing, has that disk
// captured as an image, and leaves no guest, tap or disk behind.
func TestNodeAgentBootsAGuestFromAnOCIImage(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), guestImage, platform); err != nil {
		t.Fatal(err)
	}
	if err := sim.PullImage(t.Context(), memberImage, platform); err != nil {
		t.Fatal(err)
	}
	networkName := fmt.Sprintf("sockerless-sim-kvm-%d", time.Now().UnixNano())
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: networkName, CIDR: netip.MustParsePrefix("10.79.0.0/24")}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sim.RemoveDockerNetworkContext(context.Background(), networkName) }()

	var agentLog []string
	var mu sync.Mutex
	agent, err := nodeagent.Start(t.Context(), nodeagent.Options{
		Name:    networkName + "-agent",
		Network: networkName,
		Address: netip.MustParseAddr("10.79.0.3"),
		Labels:  map[string]string{"sockerless-sim-kvm-proof": networkName},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		agentLog = append(agentLog, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Stop()

	root, _ := filepath.Abs("../..")
	source, err := os.ReadFile(filepath.Join(root, "testdata", "eval-arithmetic", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	backend := placement.NewMicroVMBackend(agent, "")
	var guestLog []string
	printed := make(chan struct{})
	var printedOnce sync.Once
	workload := placement.Workload{
		Image:        guestImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-arithmetic",
		Hostname:     "arithmetic",
		Network:      networkName,
		IPv4Address:  "10.79.0.10",
		ExtraHosts:   []string{"metadata.example.internal:10.79.0.3", "host.docker.internal:host-gateway"},
		Command:      []string{"/bin/sh", "-c"},
		Args:         []string{"cd /work && go mod init arithmetic >/dev/null; go build -o eval . && ./eval '3 + 4 * 2' && hostname && sleep 300"},
		Env:          map[string]string{"GOFLAGS": "-mod=mod", "GOTOOLCHAIN": "local"},
		Seed:         []sim.SeedFile{{Path: "/work/main.go", Data: source}},
		MemoryBytes:  1024 << 20,
		NanoCPU:      2_000_000_000,
		KeepOnExit:   true,
		Labels:       map[string]string{"sockerless-sim-kvm-guest": "arithmetic"},
	}
	handle, err := backend.Place(t.Context(), workload, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		guestLog = append(guestLog, line.Text)
		mu.Unlock()
		if strings.TrimSpace(line.Text) == "11" {
			printedOnce.Do(func() { close(printed) })
		}
	}))
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("place: %v\nagent log:\n%s", err, strings.Join(agentLog, "\n"))
	}
	// The guest builds and runs the program before it sleeps; the exec and
	// the stop follow its result rather than race it.
	select {
	case <-printed:
	case <-time.After(5 * time.Minute):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the guest did not print the arithmetic result; log:\n%s", strings.Join(guestLog, "\n"))
	}

	execCtx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := handle.Exec(execCtx, []string{"cat", "/etc/hostname"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.TrimSpace(out.Stdout) != "arithmetic" || out.ExitCode != 0 {
		t.Fatalf("exec answered %+v", out)
	}
	hosts, err := handle.Exec(execCtx, []string{"cat", "/etc/hosts"})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, want := range []string{"10.79.0.3\tmetadata.example.internal", "10.79.0.1\thost.docker.internal", "10.79.0.10\tarithmetic"} {
		if !strings.Contains(hosts.Stdout, want) {
			t.Fatalf("the guest's hosts file lacks %q:\n%s", want, hosts.Stdout)
		}
	}
	// A member joins the running guest from an image of its own: it serves
	// on the guest's localhost, which the first process reaches, from a root
	// the first process cannot see, and its exit leaves the guest running.
	var memberLog []string
	memberUp := make(chan struct{})
	var memberUpOnce sync.Once
	member, err := backend.PlaceMember(t.Context(), handle.ID(), placement.Workload{
		Image:   memberImage,
		Name:    "sockerless-sim-kvm-httpd",
		Command: []string{"sh", "-c"},
		Args:    []string{"echo member up; exec httpd -f -p 8080 -h /www"},
		Seed:    []sim.SeedFile{{Path: "/www/hello", Data: []byte("from the member\n")}},
		Labels:  map[string]string{"sockerless-sim-kvm-member": "httpd"},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		memberLog = append(memberLog, line.Text)
		mu.Unlock()
		if strings.TrimSpace(line.Text) == "member up" {
			memberUpOnce.Do(func() { close(memberUp) })
		}
	}))
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("place the member: %v\nagent log:\n%s", err, strings.Join(agentLog, "\n"))
	}
	select {
	case <-memberUp:
	case <-time.After(time.Minute):
		t.Fatal("the member did not report itself up")
	}
	if member.ID() != handle.ID()+"/sockerless-sim-kvm-httpd" && member.ID() != handle.ID()+"/kvm-httpd" {
		t.Fatalf("the member's ID is %q", member.ID())
	}
	if addr, err := member.Address(execCtx); err != nil || addr != "10.79.0.10" {
		t.Fatalf("the member's address is %q (%v), want the guest's", addr, err)
	}
	// "member up" precedes exec httpd, which signals nothing once it binds, so
	// the first read retries the port until it answers.
	served, err := handle.Exec(execCtx, []string{"sh", "-c",
		"for try in $(seq 100); do wget -qO- http://127.0.0.1:8080/hello && exit 0; sleep 0.1; done; exit 1"})
	if err != nil || served.ExitCode != 0 || strings.TrimSpace(served.Stdout) != "from the member" {
		t.Fatalf("the first process read %+v (%v) from the member on localhost", served, err)
	}
	if out, err := member.Exec(execCtx, []string{"cat", "/www/hello"}); err != nil || strings.TrimSpace(out.Stdout) != "from the member" {
		t.Fatalf("exec in the member answered %+v (%v)", out, err)
	}
	if out, err := handle.Exec(execCtx, []string{"cat", "/www/hello"}); err != nil || out.ExitCode == 0 {
		t.Fatalf("the member's file is visible from the first process's root: %+v (%v)", out, err)
	}
	if out, err := member.Exec(execCtx, []string{"cat", "/etc/hosts"}); err != nil || !strings.Contains(out.Stdout, "10.79.0.3\tmetadata.example.internal") {
		t.Fatalf("the member does not see the guest's hosts file: %+v (%v)", out, err)
	}
	members, err := backend.Find(t.Context(), map[string]string{"sockerless-sim-kvm-member": "httpd"})
	if err != nil || len(members) != 1 || members[0].ID != member.ID() || !members[0].Running {
		t.Fatalf("Find lists the member as %+v (%v)", members, err)
	}
	member.Cancel()
	if result := member.Wait(); result.ExitCode != 143 {
		t.Fatalf("the stopped member exited %d, want 143 from SIGTERM", result.ExitCode)
	}
	select {
	case <-handle.Exited():
		t.Fatal("the first process ended with the member")
	default:
	}
	if gone, err := handle.Exec(execCtx, []string{"wget", "-qO-", "-T", "2", "http://127.0.0.1:8080/hello"}); err != nil || gone.ExitCode == 0 {
		t.Fatalf("the member still serves after its stop: %+v (%v)", gone, err)
	}

	handle.Cancel()
	result := handle.Wait()
	mu.Lock()
	all := strings.Join(guestLog, "\n")
	mu.Unlock()
	if !strings.Contains(all, "11") {
		t.Fatalf("the guest did not print the arithmetic result; exit %d, log:\n%s", result.ExitCode, all)
	}
	if strings.Contains(all, "member up") {
		t.Fatalf("the member's output reached the first process's sink:\n%s", all)
	}

	// The kept guest boots again on its disk: the program it built is there
	// to run, the console carries both boots, and the disk captures as an
	// image the engine holds.
	id := handle.ID()
	again, err := backend.Restart(t.Context(), id, workload, sim.NoopSink{})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	for i := 0; i < 600; i++ {
		console, err := backend.Console(t.Context(), id)
		if err != nil {
			t.Fatalf("console: %v", err)
		}
		if strings.Count(console, "\n11\n")+boolToInt(strings.HasPrefix(console, "11\n")) >= 2 {
			break
		}
		if i == 599 {
			t.Fatalf("the second boot did not print the arithmetic result; console:\n%s", console)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// A later simulator process finds the agent by its labels and the guest
	// by the labels its workload carried, and takes both over: the adopted
	// handle reaches the running guest.
	adoptedAgent, found, err := nodeagent.Adopt(t.Context(), networkName, map[string]string{"sockerless-sim-kvm-proof": networkName}, sim.NoopSink{})
	if err != nil || !found {
		t.Fatalf("adopt the agent: found=%v err=%v", found, err)
	}
	adoptedBackend := placement.NewMicroVMBackend(adoptedAgent, "")
	existing, err := adoptedBackend.Find(t.Context(), map[string]string{"sockerless-sim-kvm-guest": "arithmetic"})
	if err != nil || len(existing) != 1 || existing[0].ID != id || !existing[0].Running {
		t.Fatalf("the adopted agent lists %+v (%v), want the running guest %s", existing, err, id)
	}
	if _, err := adoptedBackend.Find(t.Context(), map[string]string{"sockerless-sim-kvm-guest": "other"}); err != nil {
		t.Fatalf("find: %v", err)
	} else if others, _ := adoptedBackend.Find(t.Context(), map[string]string{"sockerless-sim-kvm-guest": "other"}); len(others) != 0 {
		t.Fatalf("a label the guest does not carry matched %+v", others)
	}
	taken, err := adoptedBackend.Adopt(t.Context(), id, workload, sim.NoopSink{})
	if err != nil {
		t.Fatalf("adopt the guest: %v", err)
	}
	if out, err := taken.Exec(execCtx, []string{"cat", "/etc/hostname"}); err != nil || strings.TrimSpace(out.Stdout) != "arithmetic" {
		t.Fatalf("the adopted guest answered %+v (%v)", out, err)
	}

	reference := "sockerless-sim-kvm-capture:" + sim.RandomHex(4)
	if err := backend.Capture(t.Context(), id, reference); err != nil {
		t.Fatalf("capture: %v", err)
	}
	defer func() {
		_, _ = sim.DockerClient().ImageRemove(context.Background(), reference, client.ImageRemoveOptions{Force: true})
	}()
	var capturedLog []string
	captured, err := placement.ContainerBackend{}.Place(t.Context(), placement.Workload{
		Image:        reference,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-captured-" + sim.RandomHex(4),
		Command:      []string{"/work/eval", "5 * 5"},
		Timeout:      time.Minute,
		Sandbox:      sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		capturedLog = append(capturedLog, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatalf("run the captured image: %v", err)
	}
	if result := captured.Wait(); result.ExitCode != 0 {
		t.Fatalf("the captured image's program exited %d", result.ExitCode)
	}
	// The program explains itself on standard error and answers on standard
	// output, and the two streams arrive in no fixed order.
	mu.Lock()
	answered := slices.Contains(capturedLog, "25")
	answer := strings.Join(capturedLog, "\n")
	mu.Unlock()
	if !answered {
		t.Fatalf("the captured image's program answered %q", answer)
	}
	again.Cancel()
	again.Wait()
	taken.Wait()
	if err := backend.Remove(t.Context(), id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	guests, err := agent.Client().List(t.Context())
	if err != nil || len(guests) != 0 {
		t.Fatalf("after the guest was removed the agent still lists %d guests (%v)", len(guests), err)
	}
	links, err := placement.ExecInContainer(t.Context(), agent.ContainerID(), []string{"ip", "-o", "link"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links.Stdout, "tap") {
		t.Fatalf("a tap outlived its guest:\n%s", links.Stdout)
	}

	// A first process that ends at once is a started guest whose exit the
	// handle reports, as a container that exits at once is a started
	// container.
	quick, err := backend.Place(t.Context(), placement.Workload{
		Image:        memberImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-quick",
		Hostname:     "quick",
		Network:      networkName,
		IPv4Address:  "10.79.0.13",
		Command:      []string{"sh", "-c"},
		Args:         []string{"echo quick; exit 7"},
		MemoryBytes:  256 << 20,
		NanoCPU:      1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place a guest whose process exits at once: %v", err)
	}
	if result := quick.Wait(); result.ExitCode != 7 {
		t.Fatalf("the quick guest exited %d (%v), want 7", result.ExitCode, result.Error)
	}

	// A bind of a host directory reaches a guest as a file share the
	// simulator serves over NFSv4 at the network's gateway: the guest reads
	// what the host wrote, the host reads what the guest wrote, and a
	// read-only bind refuses the guest's write.
	shared := t.TempDir()
	if err := os.WriteFile(filepath.Join(shared, "from-host"), []byte("host wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sharing, err := backend.Place(t.Context(), placement.Workload{
		Image:        memberImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-share",
		Hostname:     "share",
		Network:      networkName,
		IPv4Address:  "10.79.0.11",
		Command:      []string{"sleep"},
		Args:         []string{"300"},
		Binds:        []string{shared + ":/mnt/shared", shared + ":/mnt/readonly:ro"},
		MemoryBytes:  256 << 20,
		NanoCPU:      1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place with binds: %v", err)
	}
	shareCtx, cancelShare := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelShare()
	if out, err := sharing.Exec(shareCtx, []string{"cat", "/mnt/shared/from-host"}); err != nil || strings.TrimSpace(out.Stdout) != "host wrote this" {
		t.Fatalf("the guest read %+v (%v) from the share", out, err)
	}
	if out, err := sharing.Exec(shareCtx, []string{"sh", "-c", "echo guest wrote this > /mnt/shared/from-guest && mkdir /mnt/shared/dir && mv /mnt/shared/from-guest /mnt/shared/dir/moved && ls /mnt/shared/dir"}); err != nil || out.ExitCode != 0 || strings.TrimSpace(out.Stdout) != "moved" {
		t.Fatalf("the guest's writes to the share: %+v (%v)", out, err)
	}
	if data, err := os.ReadFile(filepath.Join(shared, "dir", "moved")); err != nil || string(data) != "guest wrote this\n" {
		t.Fatalf("the host reads %q (%v) of what the guest wrote", data, err)
	}
	if out, err := sharing.Exec(shareCtx, []string{"sh", "-c", "echo no > /mnt/readonly/refused"}); err != nil || out.ExitCode == 0 {
		t.Fatalf("a write to the read-only share: %+v (%v)", out, err)
	}
	if out, err := sharing.Exec(shareCtx, []string{"cat", "/mnt/readonly/dir/moved"}); err != nil || strings.TrimSpace(out.Stdout) != "guest wrote this" {
		t.Fatalf("the read-only share reads %+v (%v)", out, err)
	}
	if err := backend.Remove(t.Context(), sharing.ID()); err != nil {
		t.Fatalf("remove the sharing guest: %v", err)
	}

	// A bind of an engine volume reaches a guest as a share a container
	// serves from the agent's network namespace, at the agent's address:
	// the guest reads what a container wrote to the volume, a container
	// reads what the guest wrote, and the share container goes with the
	// guest, so the volume can be removed once the guest is gone. A bind of
	// one host file reaches the guest as that file, read-only where the
	// bind is.
	volume := networkName + "-volume"
	defer func() { _ = sim.RemoveVolume(volume) }()
	volumeShell(t, platform, volume, "echo container wrote this > /v/from-container")
	oneFile := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(oneFile, []byte("{\"one\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	volumed, err := backend.Place(t.Context(), placement.Workload{
		Image:        memberImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-volume",
		Hostname:     "volume",
		Network:      networkName,
		IPv4Address:  "10.79.0.12",
		Command:      []string{"sleep"},
		Args:         []string{"300"},
		Binds:        []string{volume + ":/mnt/vol", volume + ":/mnt/vol-ro:ro", oneFile + ":/etc/app/settings.json", oneFile + ":/etc/app/settings-ro.json:ro"},
		MemoryBytes:  256 << 20,
		NanoCPU:      1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place with a volume and a file: %v", err)
	}
	volumeCtx, cancelVolume := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancelVolume()
	if out, err := volumed.Exec(volumeCtx, []string{"cat", "/mnt/vol/from-container"}); err != nil || strings.TrimSpace(out.Stdout) != "container wrote this" {
		t.Fatalf("the guest read %+v (%v) from the volume", out, err)
	}
	if out, err := volumed.Exec(volumeCtx, []string{"sh", "-c", "echo guest wrote this > /mnt/vol/from-guest && cat /mnt/vol-ro/from-guest"}); err != nil || out.ExitCode != 0 || strings.TrimSpace(out.Stdout) != "guest wrote this" {
		t.Fatalf("the guest's write to the volume: %+v (%v)", out, err)
	}
	if out, err := volumed.Exec(volumeCtx, []string{"sh", "-c", "echo no > /mnt/vol-ro/refused"}); err != nil || out.ExitCode == 0 {
		t.Fatalf("a write to the read-only volume: %+v (%v)", out, err)
	}
	if out, err := volumed.Exec(volumeCtx, []string{"sh", "-c", `cat /etc/app/settings.json && echo '{"two":2}' > /etc/app/settings.json && ls /etc/app`}); err != nil || out.ExitCode != 0 || strings.TrimSpace(out.Stdout) != "{\"one\":1}\nsettings-ro.json\nsettings.json" {
		t.Fatalf("the guest's read and write of the one file: %+v (%v)", out, err)
	}
	if data, err := os.ReadFile(oneFile); err != nil || string(data) != "{\"two\":2}\n" {
		t.Fatalf("the host reads %q (%v) of the file the guest wrote", data, err)
	}
	if out, err := volumed.Exec(volumeCtx, []string{"sh", "-c", "echo no > /etc/app/settings-ro.json"}); err != nil || out.ExitCode == 0 {
		t.Fatalf("a write to the read-only file: %+v (%v)", out, err)
	}
	if err := sim.RemoveVolume(volume); err == nil {
		t.Fatal("the volume was removed while the guest's share container holds it")
	}
	if err := backend.Remove(t.Context(), volumed.ID()); err != nil {
		t.Fatalf("remove the volume guest: %v", err)
	}
	<-volumed.Exited()
	if out := volumeShell(t, platform, volume, "cat /v/from-guest"); out != "guest wrote this" {
		t.Fatalf("a container reads %q of what the guest wrote to the volume", out)
	}
	if err := sim.RemoveVolume(volume); err != nil {
		t.Fatalf("remove the volume once the guest is gone: %v", err)
	}

	// What a guest may receive is filtered at its tap, in the stages its
	// cloud's groups compile to: a server guest admitting port 80 from the
	// network answers a neighbour on 80 and drops its connection to 81; the
	// rules replaced while it runs admit 81 at once; a second stage that
	// admits nothing from the neighbour drops both; and no stages admit
	// everything.
	web, err := backend.Place(t.Context(), placement.Workload{
		Image:        memberImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-web",
		Hostname:     "web",
		Network:      networkName,
		IPv4Address:  "10.79.0.13",
		Command:      []string{"sh", "-c"},
		Args:         []string{"mkdir -p /www && echo ok > /www/index.html && httpd -p 80 -h /www && httpd -p 81 -h /www && sleep 300"},
		Ingress:      [][]netrules.PacketRule{{{Protocol: "tcp", SourceCIDR: "10.79.0.0/24", FromPort: 80, ToPort: 80}}},
		MemoryBytes:  256 << 20,
		NanoCPU:      1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place the filtered guest: %v", err)
	}
	defer func() { _ = backend.Remove(context.Background(), web.ID()) }()
	probe, err := backend.Place(t.Context(), placement.Workload{
		Image:        memberImage,
		Architecture: platform,
		Name:         "sockerless-sim-kvm-probe",
		Hostname:     "probe",
		Network:      networkName,
		IPv4Address:  "10.79.0.14",
		Command:      []string{"sleep"},
		Args:         []string{"300"},
		Gateway:      sim.NoGateway,
		MemoryBytes:  256 << 20,
		NanoCPU:      1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place the probing guest: %v", err)
	}
	defer func() { _ = backend.Remove(context.Background(), probe.ID()) }()
	filterCtx, cancelFilter := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancelFilter()
	reaches := func(port int) bool {
		t.Helper()
		out, err := probe.Exec(filterCtx, []string{"wget", "-T", "3", "-q", "-O", "-", fmt.Sprintf("http://10.79.0.13:%d/", port)})
		if err != nil {
			t.Fatalf("probe port %d: %v", port, err)
		}
		return out.ExitCode == 0 && strings.TrimSpace(out.Stdout) == "ok"
	}
	for attempt := 0; !reaches(80); attempt++ {
		if attempt == 20 {
			t.Fatal("the admitted port 80 never answered")
		}
		time.Sleep(500 * time.Millisecond)
	}
	if reaches(81) {
		t.Fatal("port 81, which no rule admits, answered")
	}
	if err := web.SetIngress(filterCtx, [][]netrules.PacketRule{{
		{Protocol: "tcp", SourceCIDR: "10.79.0.0/24", FromPort: 80, ToPort: 81},
	}}); err != nil {
		t.Fatalf("replace the rules: %v", err)
	}
	if !reaches(81) {
		t.Fatal("port 81 does not answer after a rule admitted it")
	}
	if err := web.SetIngress(filterCtx, [][]netrules.PacketRule{
		{{Protocol: "tcp", SourceCIDR: "10.79.0.0/24", FromPort: 80, ToPort: 81}},
		{{Protocol: "-1", SourceCIDR: "10.79.0.14/32", Action: "drop"}, {Protocol: "-1", SourceCIDR: "10.79.0.0/24"}},
	}); err != nil {
		t.Fatalf("replace the rules with two stages: %v", err)
	}
	if reaches(80) {
		t.Fatal("a second stage that drops the neighbour let port 80 answer")
	}
	if err := web.SetIngress(filterCtx, nil); err != nil {
		t.Fatalf("clear the rules: %v", err)
	}
	if !reaches(81) {
		t.Fatal("a guest with no stages does not receive everything")
	}
	if err := probe.SetIngress(filterCtx, [][]netrules.PacketRule{{}}); err != nil {
		t.Fatalf("give the probe an empty stage: %v", err)
	}
	if !reaches(80) {
		t.Fatal("an empty stage on the prober kept its own connection's replies out")
	}

	// A guest's default route goes where its cloud's routing says: placed
	// with none it reaches its neighbours and nothing beyond, routed through
	// a neighbour while it runs it leaves through the neighbour, and routed
	// back through the network's gateway it leaves through the engine.
	defaultRoute := func() string {
		t.Helper()
		// BusyBox's ip prints every route whatever selector follows "show".
		out, err := probe.Exec(filterCtx, []string{"ip", "route"})
		if err != nil {
			t.Fatalf("read the probe's default route: %v", err)
		}
		for _, line := range strings.Split(out.Stdout, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "default ") {
				return strings.TrimSpace(line) + " "
			}
		}
		return ""
	}
	if route := defaultRoute(); route != "" {
		t.Fatalf("a guest placed with no gateway has a default route: %q", route)
	}
	if err := probe.SetGateway(filterCtx, "10.79.0.13"); err != nil {
		t.Fatalf("route the probe through its neighbour: %v", err)
	}
	if route := defaultRoute(); !strings.Contains(route, "via 10.79.0.13 ") {
		t.Fatalf("the probe's default route is %q, want one through 10.79.0.13", route)
	}
	if err := probe.SetGateway(filterCtx, ""); err != nil {
		t.Fatalf("route the probe through the gateway: %v", err)
	}
	if route := defaultRoute(); !strings.Contains(route, "via 10.79.0.1 ") {
		t.Fatalf("the probe's default route is %q, want one through the network's gateway", route)
	}
	if err := probe.SetGateway(filterCtx, sim.NoGateway); err != nil {
		t.Fatalf("take the probe's route away: %v", err)
	}
	if route := defaultRoute(); route != "" {
		t.Fatalf("a guest routed nowhere keeps a default route: %q", route)
	}

	// A cloud deletes a network once nothing of its own is on it, and the
	// engine removes no network a container is still on: the agent leaves
	// the network at the cloud's word, its guests' bridge with it, and a
	// network of the same range joined again at the same address gets a
	// bridge of its own, so the guest on it reaches the agent.
	otherName := networkName + "-other"
	for round := 1; round <= 2; round++ {
		if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: otherName, CIDR: netip.MustParsePrefix("10.79.1.0/24")}); err != nil {
			t.Fatal(err)
		}
		if err := backend.Join(t.Context(), otherName, netip.MustParseAddr("10.79.1.3")); err != nil {
			t.Fatalf("round %d: join %s: %v", round, otherName, err)
		}
		other, err := backend.Place(t.Context(), placement.Workload{
			Image:        memberImage,
			Architecture: platform,
			Name:         fmt.Sprintf("sockerless-sim-kvm-other-%d", round),
			Hostname:     "other",
			Network:      otherName,
			IPv4Address:  "10.79.1.10",
			Command:      []string{"sleep"},
			Args:         []string{"300"},
			MemoryBytes:  256 << 20,
			NanoCPU:      1_000_000_000,
		}, sim.NoopSink{})
		if err != nil {
			t.Fatalf("round %d: place on %s: %v", round, otherName, err)
		}
		pingCtx, cancelPing := context.WithTimeout(t.Context(), time.Minute)
		out, err := other.Exec(pingCtx, []string{"ping", "-c", "1", "-W", "3", "10.79.1.3"})
		cancelPing()
		if err != nil || out.ExitCode != 0 {
			t.Fatalf("round %d: the guest on %s does not reach the agent: %+v (%v)", round, otherName, out, err)
		}
		if err := backend.Remove(t.Context(), other.ID()); err != nil {
			t.Fatalf("round %d: remove: %v", round, err)
		}
		if err := backend.Leave(t.Context(), otherName); err != nil {
			t.Fatalf("round %d: leave %s: %v", round, otherName, err)
		}
		if addr := sim.ContainerIPv4On(t.Context(), agent.ContainerID(), otherName); addr.IsValid() {
			t.Fatalf("round %d: the agent still holds %s on %s after leaving it", round, addr, otherName)
		}
		removeCtx, cancelRemove := context.WithTimeout(t.Context(), 10*time.Second)
		err = sim.RemoveDockerNetworkContext(removeCtx, otherName)
		cancelRemove()
		if err != nil {
			t.Fatalf("round %d: the engine did not remove %s once the agent left it: %v", round, otherName, err)
		}
	}

	// A guest on two networks: its first interface on the agent's network
	// and a second on the other, each at its address, so it reaches the
	// agent on both while its route out stays on the first.
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: otherName, CIDR: netip.MustParsePrefix("10.79.1.0/24")}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Join(t.Context(), otherName, netip.MustParseAddr("10.79.1.3")); err != nil {
		t.Fatalf("join %s: %v", otherName, err)
	}
	twoNets, err := backend.Place(t.Context(), placement.Workload{
		Image:         memberImage,
		Architecture:  platform,
		Name:          "sockerless-sim-kvm-two-networks",
		Hostname:      "two",
		Network:       networkName,
		IPv4Address:   "10.79.0.12",
		ExtraNetworks: []sim.NetworkAttachment{{Network: otherName, IPv4Address: "10.79.1.12"}},
		Command:       []string{"sleep"},
		Args:          []string{"300"},
		MemoryBytes:   256 << 20,
		NanoCPU:       1_000_000_000,
	}, sim.NoopSink{})
	if err != nil {
		t.Fatalf("place on two networks: %v", err)
	}
	twoCtx, cancelTwo := context.WithTimeout(t.Context(), time.Minute)
	defer cancelTwo()
	for _, agentAddress := range []string{"10.79.0.3", "10.79.1.3"} {
		if out, err := twoNets.Exec(twoCtx, []string{"ping", "-c", "1", "-W", "3", agentAddress}); err != nil || out.ExitCode != 0 {
			t.Fatalf("the guest on two networks does not reach the agent at %s: %+v (%v)", agentAddress, out, err)
		}
	}
	if out, err := twoNets.Exec(twoCtx, []string{"ip", "-o", "-4", "addr", "show", "eth1"}); err != nil || !strings.Contains(out.Stdout, "10.79.1.12/24") {
		t.Fatalf("the guest's second interface: %+v (%v)", out, err)
	}
	if out, err := twoNets.Exec(twoCtx, []string{"ip", "route", "show", "default"}); err != nil || !strings.Contains(out.Stdout, "dev eth0") {
		t.Fatalf("the guest's default route: %+v (%v)", out, err)
	}
	if err := backend.Remove(t.Context(), twoNets.ID()); err != nil {
		t.Fatalf("remove the guest on two networks: %v", err)
	}
	if err := backend.Leave(t.Context(), otherName); err != nil {
		t.Fatalf("leave %s: %v", otherName, err)
	}
	removeCtx, cancelRemove := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelRemove()
	if err := sim.RemoveDockerNetworkContext(removeCtx, otherName); err != nil {
		t.Fatalf("the engine did not remove %s once the agent left it: %v", otherName, err)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// volumeShell runs a script in a container with the volume at /v and
// returns what it printed.
func volumeShell(t *testing.T, platform, volume, script string) string {
	t.Helper()
	var out []string
	var mu sync.Mutex
	handle, err := sim.StartContainerSyncContext(t.Context(), sim.ContainerConfig{
		Image:        memberImage,
		Architecture: platform,
		Command:      []string{"sh", "-c", script},
		Binds:        []string{volume + ":/v"},
		Name:         fmt.Sprintf("sockerless-sim-kvm-volume-shell-%d", time.Now().UnixNano()),
		Sandbox:      sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true, DenyDockerSocket: true, DenyHostNetwork: true},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		out = append(out, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result := handle.Wait(); result.ExitCode != 0 {
		t.Fatalf("%q in a container with the volume exited %d: %s", script, result.ExitCode, strings.Join(out, "\n"))
	}
	mu.Lock()
	defer mu.Unlock()
	return strings.TrimSpace(strings.Join(out, "\n"))
}
