package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// SandboxCloudRun matches Cloud Run's container execution environment, which
// Cloud Run functions share: gVisor denies privileged mode, CAP_SYS_ADMIN and
// raw sockets, and the user is non-root unless the image's USER says
// otherwise.
var SandboxCloudRun = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"NET_BIND_SERVICE", "SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "SETFCAP"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxCloudBuild matches a Cloud Build step: an ordinary container on the
// build's worker, running as its image's user with the engine's default
// capabilities, and never on the worker's host network.
var SandboxCloudBuild = sim.SandboxProfile{
	DenyHostNetwork: true,
}
