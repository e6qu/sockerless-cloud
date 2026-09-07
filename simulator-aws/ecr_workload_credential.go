package main

import (
	"fmt"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// imageOnSimulatorRegistry reports whether an image reference names this
// simulator's own registry: a host whose port is the simulator's, at which
// a coordinate that relocates Amazon ECR reaches it. A reference that names
// `<account>.dkr.ecr.<region>.amazonaws.com` is Amazon ECR by shape and is
// reached where it points, as a pull-through-cache reference is resolved
// through its rule.
func imageOnSimulatorRegistry(image string) bool {
	host, _, found := strings.Cut(image, "/")
	if !found {
		return false
	}
	i := strings.LastIndex(host, ":")
	if i < 0 {
		return false
	}
	port, err := simHostMetadataPort()
	if err != nil {
		return false
	}
	return host[i+1:] == fmt.Sprint(port)
}

// ecrWorkloadRegistryAuth is the credential an AWS workload host presents when
// it pulls image from this simulator's registry: an authorization token this
// registry issues, as the `AWS` user's Basic credential — what an ECR pull
// presents after `aws ecr get-login-password | docker login --username AWS`.
// Empty for any other registry, which the host reaches anonymously.
func ecrWorkloadRegistryAuth(image string) string {
	if !imageOnSimulatorRegistry(image) {
		return ""
	}
	password, _, err := ecrIssueAuthorizationPassword()
	if err != nil {
		return ""
	}
	return sim.RegistryCredential(ecrDockerLoginUsername, password)
}
