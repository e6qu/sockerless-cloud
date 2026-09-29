package workload

import (
	"context"
	"os"
	"os/exec"
	"time"
)

// DockerBuildInvocation returns the docker subcommand that builds an image
// into the engine's image store, where a later `docker push` finds it. With
// the buildx plugin present that is `buildx build --load`: the default
// docker-container buildx driver otherwise leaves the result in the build
// cache only. Without buildx it is plain `build`, which writes to the store
// natively and rejects the buildx-only --load flag. env is the docker CLI's
// environment.
func DockerBuildInvocation(ctx context.Context, env []string) []string {
	probe := exec.CommandContext(ctx, "docker", "buildx", "version")
	probe.Env = env
	if probe.Run() == nil {
		return []string{"buildx", "build", "--load"}
	}
	return []string{"build"}
}

// dockerCancelGrace is how long an interrupted docker command has to tell the
// engine to stop before it is killed outright.
const dockerCancelGrace = 10 * time.Second

// DockerCommand builds a docker CLI invocation that a cancelled ctx actually
// stops. The engine, not the CLI, runs a build — buildx tells buildkit to
// stop only when the CLI unwinds, which it does on an interrupt and not on a
// kill — so cancellation interrupts. A killed CLI can also leave a child
// holding the output pipe open and block Wait; WaitDelay bounds that unwind.
// env is the docker CLI's environment; nil inherits the simulator's.
func DockerCommand(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = dockerCancelGrace
	return cmd
}
