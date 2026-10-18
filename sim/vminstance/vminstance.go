// Package vminstance runs a cloud's virtual machine instance — an Amazon EC2
// instance, a Compute Engine instance, an Azure virtual machine — on a
// placement backend. The instance's image is an OCI image from the cloud's
// catalog, its first process is an init the simulator seeds that runs the
// instance's user data once and then keeps the machine up, and its address,
// resolver and metadata are what the cloud's network gives it.
package vminstance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
)

// Image is one entry of a cloud's image catalog: the OCI image a machine
// image name resolves to.
type Image struct {
	// OCI is the pinned image reference, from a registry
	// scripts/base-images-for.sh admits.
	OCI string
	// Architecture is the image's platform, as "linux/amd64".
	Architecture string
}

// Spec describes one instance to run.
type Spec struct {
	// Name is the container's name; ID the instance's own identifier.
	Name, ID string
	Hostname string
	Image    Image
	// Network and Address are the engine network and the address the
	// instance's interface holds.
	Network, Address string
	// DNS is the resolver the instance asks: its network's sidecar.
	DNS []string
	// UserData is what the cloud hands the instance at launch; a script
	// starting with #! runs once as root, as cloud-init runs it.
	UserData []byte
	// UserDataPath is where the init finds it; empty takes cloud-init's.
	UserDataPath string
	// UserDataEveryBoot runs the script at every start, as Compute Engine
	// runs a startup-script; unset, it runs once per instance, as cloud-init
	// runs user data on Amazon EC2 and Azure.
	UserDataEveryBoot bool
	// Env reaches the init and everything it runs: the metadata endpoint
	// variables a cloud's SDKs honour.
	Env map[string]string
	// Seed files are placed beside the init before the instance starts.
	Seed      []sim.SeedFile
	VCPUs     int
	MemoryMiB int
	Labels    map[string]string
	StopGrace time.Duration
	// ExtraHosts and PublishPorts pass through to the container backend.
	ExtraHosts   []string
	PublishPorts []int
}

// InitPath is where the instance's init lives.
const InitPath = "/sbin/sockerless-instance-init"

// DefaultUserDataPath is where cloud-init keeps the instance's user data.
const DefaultUserDataPath = "/var/lib/cloud/instance/user-data.txt"

// OutputLogPath is where the user data's output lands, as cloud-init writes
// it.
const OutputLogPath = "/var/log/cloud-init-output.log"

// OnceMarkerPath is the semaphore cloud-init leaves after the user data ran,
// so a stopped and started instance does not run it again; a reimaged disk
// has no marker and runs it afresh.
const OnceMarkerPath = "/var/lib/cloud/instance/sem/config_scripts_user"

// Sandbox is the profile an instance's container runs under: the
// capabilities a Linux machine's root holds inside its own namespace, no
// host privilege, no engine socket, no host network.
var Sandbox = sim.SandboxProfile{
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETGID", "SETUID", "SETPCAP", "NET_BIND_SERVICE", "SYS_CHROOT", "SETFCAP", "NET_RAW", "AUDIT_WRITE", "MKNOD"},
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// ErrNoImage is returned for a spec that names no image.
var ErrNoImage = errors.New("the instance names no image")

// Init renders the instance's init: it runs the user data once per instance
// when it is a script, as cloud-init's scripts-user module does, writes its
// output to the console and to the log where cloud-init keeps it, and then
// stays up until the machine is stopped.
func Init(userDataPath string, everyBoot bool) string {
	if userDataPath == "" {
		userDataPath = DefaultUserDataPath
	}
	once := "/dev/null"
	if !everyBoot {
		once = OnceMarkerPath
	}
	return strings.Join([]string{
		"#!/bin/sh",
		"ud=" + shellQuote(userDataPath),
		"log=" + shellQuote(OutputLogPath),
		"mkdir -p \"$(dirname \"$log\")\"",
		"once=" + shellQuote(once),
		"if [ \"$once\" = /dev/null ] || [ ! -e \"$once\" ] && [ -s \"$ud\" ] && [ \"$(head -c 2 \"$ud\")\" = '#!' ]; then",
		"\t[ \"$once\" = /dev/null ] || { mkdir -p \"$(dirname \"$once\")\" && : >\"$once\"; }",
		"\tchmod 700 \"$ud\"",
		"\t{ \"$ud\" 2>&1; echo \"user data exited $?\"; } | tee -a \"$log\"",
		"fi",
		"mkdir -p /run && touch /run/sockerless-instance-ready",
		"trap 'exit 0' TERM INT",
		"while :; do sleep 2147483647 & wait $!; done",
		"",
	}, "\n")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Workload is the placement workload an instance runs as.
func Workload(spec Spec) (placement.Workload, error) {
	if spec.Image.OCI == "" || spec.Image.Architecture == "" {
		return placement.Workload{}, ErrNoImage
	}
	if spec.Network == "" || spec.Address == "" {
		return placement.Workload{}, fmt.Errorf("instance %s: a network and an address are required", spec.ID)
	}
	userDataPath := spec.UserDataPath
	if userDataPath == "" {
		userDataPath = DefaultUserDataPath
	}
	seed := []sim.SeedFile{
		{Path: InitPath, Mode: 0o755, Data: []byte(Init(userDataPath, spec.UserDataEveryBoot))},
		{Path: userDataPath, Mode: 0o600, Data: spec.UserData},
	}
	seed = append(seed, spec.Seed...)
	labels := map[string]string{"sockerless-sim-instance": spec.ID}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	grace := spec.StopGrace
	if grace == 0 {
		grace = 30 * time.Second
	}
	return placement.Workload{
		Image:             spec.Image.OCI,
		Architecture:      spec.Image.Architecture,
		Name:              spec.Name,
		Hostname:          spec.Hostname,
		Command:           []string{InitPath},
		Env:               spec.Env,
		Network:           spec.Network,
		IPv4Address:       spec.Address,
		DNS:               spec.DNS,
		ExtraHosts:        spec.ExtraHosts,
		PublishPorts:      spec.PublishPorts,
		Seed:              seed,
		Labels:            labels,
		Sandbox:           Sandbox,
		NanoCPU:           int64(spec.VCPUs) * 1_000_000_000,
		MemoryBytes:       int64(spec.MemoryMiB) << 20,
		CancelGracePeriod: grace,
		KeepOnExit:        true,
	}, nil
}

// Launch places the instance on the backend.
func Launch(ctx context.Context, backend placement.Backend, spec Spec, sink sim.LogSink) (placement.Handle, error) {
	w, err := Workload(spec)
	if err != nil {
		return nil, err
	}
	return backend.Place(ctx, w, sink)
}
