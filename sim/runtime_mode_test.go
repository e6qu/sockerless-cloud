package sim

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A simulator that executes workloads must obtain its container engine at
// startup and refuse to serve without one. The alternative — the behaviour
// these tests exist to keep out — is a process that answers its health check
// while every workload it is given fails at first use, deep in a background
// goroutine, reporting nothing more than an uninitialised client.

// enginelessChildVariable makes the test binary re-enter itself as the process
// under test. The engine client is built behind a sync.Once, so a startup that
// fails to reach an engine poisons that Once for the rest of the binary: the
// refusal can only be observed honestly in a process of its own.
const enginelessChildVariable = "SIM_TEST_ENGINELESS_CHILD"

// unreachableEngineSocket is a container engine endpoint nothing serves.
const unreachableEngineSocket = "unix:///nonexistent/sockerless-sim-engine.sock"

// TestNewServerRefusesToStartWithoutAContainerEngine points the engine socket
// at nothing and requires startup to fail, naming the engine and what the
// operator can do about it.
func TestNewServerRefusesToStartWithoutAContainerEngine(t *testing.T) {
	if os.Getenv(enginelessChildVariable) == "1" {
		_, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
		if err == nil {
			t.Fatal("STARTED-WITHOUT-AN-ENGINE: NewServer returned a server with no container engine client")
		}
		t.Logf("STARTUP-REFUSED: %v", err)
		return
	}

	output, err := runEnginelessChild(t, RuntimeModeContainer)
	if err != nil {
		t.Fatalf("child run failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "STARTUP-REFUSED") {
		t.Fatalf("startup did not refuse to serve without a container engine:\n%s", output)
	}
	for _, want := range []string{
		"container engine not available",
		"SIM_RUNTIME=process",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("startup refusal does not mention %q:\n%s", want, output)
		}
	}
	t.Logf("child startup refusal:\n%s", output)
}

// TestAPIOnlyModeStartsWithoutAContainerEngine is the other half: the one mode
// that legitimately holds no engine still starts against the same unreachable
// socket, and says in its own report that it cannot execute workloads. Without
// this, "refuse to start without an engine" would break every control-plane
// run that never executes one.
func TestAPIOnlyModeStartsWithoutAContainerEngine(t *testing.T) {
	if os.Getenv(enginelessChildVariable) == "1" {
		srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
		if err != nil {
			t.Fatalf("API-only startup failed without a container engine: %v", err)
		}
		if srv.runtimeMode != RuntimeModeAPIOnly {
			t.Fatalf("runtime mode = %q, want %q", srv.runtimeMode, RuntimeModeAPIOnly)
		}
		if DockerClient() != nil {
			t.Fatal("API-only startup built a container engine client")
		}
		// What a workload sees in that process. Startup guarantees only
		// API-only mode gets this far, so the refusal has to say which of two
		// very different situations the caller is in — a process that never had
		// an engine, rather than an engine that went away.
		refusal := RequireContainerRuntime("starting a container")
		if refusal == nil {
			t.Fatal("RequireContainerRuntime accepted a process with no container engine client")
		}
		for _, want := range []string{"starting a container", "API-only", string(RuntimeModeAPIOnly)} {
			if !strings.Contains(refusal.Error(), want) {
				t.Fatalf("refusal does not mention %q: %v", want, refusal)
			}
		}
		if _, err := StartContainerSync(ContainerConfig{Image: "alpine"}, nil); err == nil ||
			!strings.Contains(err.Error(), "API-only") {
			t.Fatalf("StartContainerSync error = %v, want one naming the API-only mode", err)
		}
		t.Logf("API-ONLY-STARTED, workloads refused with: %v", refusal)
		return
	}

	output, err := runEnginelessChild(t, RuntimeModeAPIOnly)
	if err != nil {
		t.Fatalf("child run failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "API-ONLY-STARTED") {
		t.Fatalf("API-only mode did not start without a container engine:\n%s", output)
	}
}

// runEnginelessChild re-runs this one test in a child process whose container
// engine endpoint is unreachable, and returns everything the child printed.
func runEnginelessChild(t *testing.T, mode RuntimeMode) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(),
		enginelessChildVariable+"=1",
		"DOCKER_HOST="+unreachableEngineSocket,
		runtimeModeVariable+"="+string(mode),
	)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// TestResolveRuntimeModeRefusesAModeItDoesNotImplement covers the variable that
// selects the mode. It is the only thing standing between a process that runs
// workloads and one that cannot, so a value the simulator does not implement is
// a misconfiguration to report at startup rather than to read as "not
// API-only".
func TestResolveRuntimeModeRefusesAModeItDoesNotImplement(t *testing.T) {
	for _, mode := range []struct {
		value string
		want  RuntimeMode
	}{
		{value: "", want: RuntimeModeContainer},
		{value: "docker", want: RuntimeModeContainer},
		{value: "process", want: RuntimeModeAPIOnly},
		{value: "  process  ", want: RuntimeModeAPIOnly},
	} {
		t.Setenv(runtimeModeVariable, mode.value)
		got, err := ResolveRuntimeMode()
		if err != nil {
			t.Fatalf("%s=%q: %v", runtimeModeVariable, mode.value, err)
		}
		if got != mode.want {
			t.Fatalf("%s=%q resolved to %q, want %q", runtimeModeVariable, mode.value, got, mode.want)
		}
	}

	for _, value := range []string{"Process", "podman", "none", "proces"} {
		t.Setenv(runtimeModeVariable, value)
		got, err := ResolveRuntimeMode()
		if err == nil {
			t.Fatalf("%s=%q resolved to %q instead of being refused", runtimeModeVariable, value, got)
		}
		if !strings.Contains(err.Error(), "known modes") {
			t.Fatalf("%s=%q refusal does not name the modes it knows: %v", runtimeModeVariable, value, err)
		}
	}
}

// TestNewServerRefusesAModeItDoesNotImplement carries that refusal through
// startup, so an unimplemented mode never reaches a served request.
func TestNewServerRefusesAModeItDoesNotImplement(t *testing.T) {
	t.Setenv(runtimeModeVariable, "api-only")
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "disabled"})
	if err == nil {
		t.Fatalf("NewServer started with an unimplemented runtime mode: %#v", srv.runtimeMode)
	}
	if !strings.Contains(err.Error(), runtimeModeVariable) {
		t.Fatalf("startup refusal does not name %s: %v", runtimeModeVariable, err)
	}
}
