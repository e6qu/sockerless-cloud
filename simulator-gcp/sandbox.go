package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// SandboxCloudRun matches Cloud Run / Cloud Run Functions Gen2's
// container execution environment. Cloud Run gVisor sandbox denies
// privileged, bans CAP_SYS_ADMIN, disallows raw sockets. User defaults
// to non-root unless the image's USER directive overrides. gVisor's
// syscall filter is stricter than local Docker enforces; these
// settings match the cap deny list Cloud Run applies on top of gVisor.
var SandboxCloudRun = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"NET_BIND_SERVICE", "SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "SETFCAP"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxGCFGen2 mirrors SandboxCloudRun — Cloud Functions Gen2 runs
// on Cloud Run Services underneath. Alias for clarity at call sites.
var SandboxGCFGen2 = SandboxCloudRun
