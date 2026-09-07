package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
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
func cbEnvironmentServiceEnv() (map[string]string, error) {
	env, err := hostMetadataEnv("")
	if err != nil {
		return nil, err
	}
	host, err := workloadCallbackHost()
	if err != nil {
		return nil, err
	}
	port, err := simHostMetadataPort()
	if err != nil {
		return nil, err
	}
	env["AWS_ENDPOINT_URL"] = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	return env, nil
}

// cbEnvironmentEngine is what a privileged build environment gets so its
// docker steps run: the socket of the engine this simulator runs on, bound
// where the `docker` client looks (the daemon real CodeBuild starts inside
// the environment listens there), or DOCKER_HOST when the engine is reached
// over TCP. A build that names privilegedMode and finds no engine is a
// failed build, as it would be without the mode.
func cbEnvironmentEngine(privileged bool) (binds []string, env map[string]string, err error) {
	if !privileged {
		return nil, nil, nil
	}
	if sock := sim.EngineSocketPath(); sock != "" {
		return []string{sock + ":/var/run/docker.sock"}, nil, nil
	}
	if host := sim.EngineHost(); strings.HasPrefix(host, "tcp://") {
		return nil, map[string]string{"DOCKER_HOST": host}, nil
	}
	return nil, nil, fmt.Errorf("privileged build environment: no container engine socket to hand the build (DOCKER_HOST=%q)", sim.EngineHost())
}
