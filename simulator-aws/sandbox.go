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
	TmpfsSize:        "size=512m", // Lambda's ephemeral storage is configurable; the simulator always uses the default 512 MB.
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}

// SandboxFargate matches Amazon ECS on AWS Fargate: the image's USER may win,
// but host networking, the Docker socket and privileged mode stay denied.
var SandboxFargate = sim.SandboxProfile{
	Privileged:       false,
	ReadonlyRootfs:   false,
	CapDrop:          []string{"ALL"},
	CapAdd:           []string{"SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "SETPCAP", "NET_BIND_SERVICE", "SETFCAP", "SYS_CHROOT"},
	NoNewPrivileges:  true,
	DenyDockerSocket: true,
	DenyHostNetwork:  true,
}
