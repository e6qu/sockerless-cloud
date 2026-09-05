package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// SandboxACA matches Azure Container Apps' workload restrictions.
// ACA runs on AKS pod security baseline: no privileged, no host net,
// no docker.sock. Default user is non-root unless overridden by the
// container image. Capabilities default to a small subset (NET_BIND
// + IDENTITY for managed-identity sidecar communication).
var SandboxACA = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"NET_BIND_SERVICE", "SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "SETFCAP"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxAZF matches Azure Functions' workload restrictions. AZF
// Linux containers run on App Service Linux underneath, which
// applies similar AKS-style pod restrictions. Treat as ACA-equivalent
// for now; refine if Azure documents a stricter cap list.
var SandboxAZF = SandboxACA
