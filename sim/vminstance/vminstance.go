// Package vminstance runs a cloud's virtual machine instance — an Amazon EC2
// instance, a Compute Engine instance, an Azure virtual machine — on a
// placement backend. The instance's image is an OCI image from the cloud's
// catalog, its first process is an init the simulator seeds that runs the
// image's cloud-init over the instance's user data and then keeps the machine
// up, and its address, resolver and metadata are what the cloud's network
// gives it.
package vminstance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/netrules"
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
	// instance's interface holds; MAC the hardware address the cloud gave
	// that interface.
	Network, Address, MAC string
	// DNS is the resolver the instance asks: its network's sidecar.
	DNS []string
	// UserData is what the cloud hands the instance's cloud-init at launch:
	// a cloud-config document, a script, or a MIME multipart message of them.
	UserData []byte
	// CloudInit is the image's cloud-init as the cloud's datasource feeds it;
	// nil for an image that runs none, whose user data nothing reads.
	CloudInit *CloudInit
	// Startup runs at every boot after cloud-init, as Compute Engine's guest
	// environment runs a startup-script; its output goes to the console.
	Startup *Startup
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
	// Ingress is what the instance may receive: the cloud's security groups,
	// firewall rules or network security groups, compiled.
	Ingress [][]netrules.PacketRule
	// Gateway is where the instance's default route goes, in the terms of
	// sim.ContainerConfig.Gateway: the cloud's routing for its subnet.
	Gateway string
}

// Startup is a script the instance runs at every boot.
type Startup struct {
	// Path is where the script is placed; Script what it holds.
	Path   string
	Script []byte
}

// InitPath is where the instance's init lives.
const InitPath = "/sbin/sockerless-instance-init"

// OutputLogPath is where cloud-init's output lands, besides the console.
const OutputLogPath = "/var/log/cloud-init-output.log"

// OnceMarkerPath is the semaphore cloud-init leaves after the user data's
// scripts ran, so a stopped and started instance does not run them again; a
// new instance has its own state directory and runs them afresh.
const OnceMarkerPath = "/var/lib/cloud/instance/sem/config_scripts_user"

// BootFinishedPath is the file cloud-init writes when it has finished a boot.
const BootFinishedPath = "/var/lib/cloud/instance/boot-finished"

// ReadyPath is the marker the init raises once the user data and the startup
// script have run: the machine is up.
const ReadyPath = "/run/sockerless-instance-ready"

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

// Init renders the instance's init: it runs cloud-init's stages when the
// image carries them, with their output on the console and in cloud-init's
// log, then the startup script at its path, if any, by its interpreter line,
// with its output on the console, and then stays up until the machine is
// stopped. The init waits
// for each stage's process and not for its output's end: a daemon a script
// leaves behind keeps the output open, and the tee relays it for as long as
// the daemon runs.
func Init(startupPath string) string {
	lines := []string{
		"#!/bin/sh",
		"mkdir -p /run",
		"date +%s.%N >/run/sockerless-boot-epoch",
		"if [ -f " + CloudInitScriptPath + " ]; then",
		"	log=" + shellQuote(OutputLogPath),
		"	mkdir -p \"$(dirname \"$log\")\"",
		"	out=/run/sockerless-cloud-init.out",
		"	rm -f \"$out\" && mkfifo \"$out\"",
		"	tee -a \"$log\" <\"$out\" &",
		"	sh " + CloudInitScriptPath + " >\"$out\" 2>&1",
		"fi",
	}
	if startupPath != "" {
		lines = append(lines,
			"if [ -f "+shellQuote(startupPath)+" ]; then",
			"	"+shellQuote(startupPath)+" 2>&1",
			"fi",
		)
	}
	lines = append(lines,
		"touch "+ReadyPath,
		// SIGRTMIN+4 is systemd's poweroff signal and SIGUSR2 busybox init's,
		// so a guest that powers itself off ends the init as it ends a real one.
		"trap 'exit 0' TERM INT USR2 38",
		"while :; do sleep 2147483647 & wait $!; done",
		"",
	)
	return strings.Join(lines, "\n")
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
	startupPath := ""
	if spec.Startup != nil {
		startupPath = spec.Startup.Path
	}
	seed := []sim.SeedFile{{Path: InitPath, Mode: 0o755, Data: []byte(Init(startupPath))}}
	if spec.CloudInit != nil {
		cloudInit, err := cloudInitSeed(*spec.CloudInit, spec.UserData)
		if err != nil {
			return placement.Workload{}, fmt.Errorf("instance %s: %w", spec.ID, err)
		}
		seed = append(seed, cloudInit...)
	}
	if spec.Startup != nil {
		seed = append(seed, sim.SeedFile{Path: spec.Startup.Path, Mode: 0o755, Data: spec.Startup.Script})
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
		MACAddress:        spec.MAC,
		DNS:               spec.DNS,
		ExtraHosts:        spec.ExtraHosts,
		PublishPorts:      spec.PublishPorts,
		Ingress:           spec.Ingress,
		Gateway:           spec.Gateway,
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
