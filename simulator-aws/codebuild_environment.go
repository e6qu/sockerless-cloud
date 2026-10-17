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
// instance metadata endpoint the SDKs take credentials from.
func cbEnvironmentServiceEnv(network awsWorkloadNetwork) (map[string]string, error) {
	env := workloadMetadataEnv(network.MetadataOrigin(), "")
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	env["AWS_ENDPOINT_URL"] = "http://" + addr
	return env, nil
}
