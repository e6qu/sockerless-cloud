package main

import (
	"github.com/e6qu/sockerless-cloud/sim"
)

// SandboxLambda matches AWS Lambda's container execution environment:
// read-only rootfs except /tmp; sandbox user (uid 1051); no new
// privileges; minimal capabilities.
//
// Source: AWS Lambda Operator Guide — "Lambda runtime environment"
// (https://docs.aws.amazon.com/lambda/latest/operatorguide/runtime-environment.html).
var SandboxLambda = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   true,
	User:             "1051:1051", // sbx_user1051
	CapDrop:          []string{"ALL"},
	NoNewPrivileges:  true,
	TmpfsSize:        "size=512m", // configurable in real Lambda; sim uses the default
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxFargate matches ECS Fargate's task isolation. Fargate
// permits more flexibility than Lambda (e.g. user from image) but
// still denies host net + docker.sock + privileged.
var SandboxFargate = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "NET_BIND_SERVICE", "SETFCAP", "SYS_CHROOT"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}
