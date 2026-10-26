package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// SandboxACA matches Azure Container Apps' pod security baseline: not
// privileged, no host network, no docker.sock, non-root unless the image says
// otherwise, and a small capability set.
var SandboxACA = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"NET_BIND_SERVICE", "SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "SETFCAP"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// Azure Functions Linux containers run on App Service Linux; the simulator
// applies the Container Apps profile.
var SandboxAZF = SandboxACA
