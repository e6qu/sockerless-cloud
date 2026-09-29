package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// imageOnSimulatorRegistry reports whether an image reference names this
// simulator's own registry: a host whose port is the simulator's, at which
// a coordinate that relocates Amazon ECR reaches it. A reference that names
// `<account>.dkr.ecr.<region>.amazonaws.com` is Amazon ECR by shape and is
// reached where it points, as a pull-through-cache reference is resolved
// through its rule.
func imageOnSimulatorRegistry(image string) (bool, error) {
	port, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		return false, err
	}
	return sim.ImageOnPort(image, port), nil
}

// ecrWorkloadRegistryAuth is the credential an AWS workload host presents when
// it pulls image from this simulator's registry: an authorization token this
// registry issues, as the `AWS` user's Basic credential — what an ECR pull
// presents after `aws ecr get-login-password | docker login --username AWS`.
// Empty for any other registry, which the host reaches anonymously.
func ecrWorkloadRegistryAuth(image string) (string, error) {
	onSimulator, err := imageOnSimulatorRegistry(image)
	if err != nil {
		return "", fmt.Errorf("resolve the registry of image %s: %w", image, err)
	}
	if !onSimulator {
		return "", nil
	}
	password, _, err := ecrIssueAuthorizationPassword()
	if err != nil {
		return "", fmt.Errorf("issue an Amazon ECR authorization token to pull %s: %w", image, err)
	}
	return sim.RegistryCredential(ecrDockerLoginUsername, password), nil
}
