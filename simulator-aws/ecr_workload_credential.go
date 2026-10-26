package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A host on the simulator's port relocates Amazon ECR. A reference naming
// `<account>.dkr.ecr.<region>.amazonaws.com` is Amazon ECR by shape and is
// reached where it points, as a pull-through-cache rule resolves it.
func imageOnSimulatorRegistry(image string) (bool, error) {
	port, err := workloadhost.ListenPort(simListenAddr)
	if err != nil {
		return false, err
	}
	return sim.ImageOnPort(image, port), nil
}

// A token this registry issues, as the `AWS` user's Basic credential, the way
// `aws ecr get-login-password | docker login --username AWS` presents it.
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
