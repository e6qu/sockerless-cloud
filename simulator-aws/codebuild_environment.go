package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A curated image `aws/codebuild/<name>:<tag>` runs as the image AWS publishes
// on Amazon ECR Public, `public.ecr.aws/codebuild/<name>:<tag>`.
func cbEnvironmentImage(image string) string {
	if rest, ok := strings.CutPrefix(image, "aws/codebuild/"); ok {
		return "public.ecr.aws/codebuild/" + rest
	}
	return image
}

// Point the AWS CLI and SDKs in the build at this simulator, since its
// services live in this process, and serve the service role's sessions from
// the container credentials endpoint, named for the build.
func cbEnvironmentServiceEnv(network awsWorkloadNetwork, holder, serviceRole, buildID string) (containerCredentials, error) {
	addr, err := workloadhost.CallbackAddr(simCallbackListenAddr)
	if err != nil {
		return containerCredentials{}, err
	}
	credentials := grantContainerCredentials(cbEnvironmentBackend, network, holder, serviceRole, "AWSCodeBuild-"+buildID[strings.LastIndex(buildID, ":")+1:])
	credentials.Env["AWS_ENDPOINT_URL"] = "http://" + addr
	return credentials, nil
}

var cbEnvironmentBackend placement.Backend = placement.ContainerBackend{}
