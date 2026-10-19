package main

import (
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// cbEnvironmentImage returns the image a build environment runs: a curated
// CodeBuild image, named `aws/codebuild/<name>:<tag>` in a project, is the
// image AWS publishes for it on the Amazon ECR Public Gallery,
// `public.ecr.aws/codebuild/<name>:<tag>`; any other image runs as named.
func cbEnvironmentImage(image string) string {
	if rest, ok := strings.CutPrefix(image, "aws/codebuild/"); ok {
		return "public.ecr.aws/codebuild/" + rest
	}
	return image
}

// cbEnvironmentServiceEnv is the environment every build environment gets so
// the AWS CLI and SDKs a buildspec runs reach the services of this simulator:
// the service endpoint at the address the environment can reach the
// simulator on (real CodeBuild leaves AWS_ENDPOINT_URL unset and resolves
// the regional hosts; here the services live in this process), and the
// container credentials endpoint serving sessions of the project's service
// role to holder, in sessions named for the build.
func cbEnvironmentServiceEnv(network awsWorkloadNetwork, holder, serviceRole, buildID string) (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	env := grantContainerCredentials(network.MetadataOrigin(), holder, serviceRole, "AWSCodeBuild-"+buildID[strings.LastIndex(buildID, ":")+1:])
	env["AWS_ENDPOINT_URL"] = "http://" + addr
	return env, nil
}
