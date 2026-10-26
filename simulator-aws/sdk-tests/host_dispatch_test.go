package aws_sdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNoHostProcessDispatchOfWorkloads holds that no handler runs a workload as
// a host process: container and function workloads run on a Docker host
// (StartContainerSync) honouring their Architecture, and VM-level resources
// through the placement backend in docs/architecture.md. It looks for both
// ways to reach the host, `os/exec` and the `sim.StartProcess` wrapper around
// it, across the simulator module and the `sim/` framework, which holds the
// container reaper; paths are repository-relative so both share one allow
// list.
func TestNoHostProcessDispatchOfWorkloads(t *testing.T) {
	t.Parallel()
	// Each entry is a file allowed to reach the host, with the reason. No workload
	// runs as a host process: SIM_RUNTIME=process means API-only, serving the API
	// without a container engine.
	allowList := map[string]string{
		"sim/container_reaper.go":                    "reaps the sim's own containers through the docker CLI; not a workload",
		"sim/workload/build.go":                      "drives the docker CLI's image build and push for a build step; the build runs on the Docker host",
		"sim/workloadhost/workloadhost.go":           "reads the Podman machine's default route to find the host address; runs no workload",
		"sim/process_unix.go":                        "detaches the container reaper and checks whether a simulator process is alive; runs no workload",
		"sim/process_windows.go":                     "detaches the container reaper and checks whether a simulator process is alive; runs no workload",
		"sim/nodeagent/cmd/nodeagent/firecracker.go": "the node agent program, which runs inside the agent container: starts Firecracker for a guest there, not on the simulator's host",
		"sim/nodeagent/cmd/nodeagent/guest.go":       "the node agent program: builds a guest's disks inside the agent container",
		"sim/nodeagent/cmd/nodeagent/network.go":     "the node agent program: configures taps and filters inside the agent container's own namespace",
		"sim/nodeagent/cmd/nodeagent/export.go":      "the node agent program: reads a guest's root filesystem out of its image inside the agent container",
		"sim/nodeagent/cmd/guestinit/main.go":        "the guest's init, which runs inside a microVM and starts the workload there",
	}

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	roots := []string{filepath.Join(repoRoot, "simulator-aws"), filepath.Join(repoRoot, "sim")}

	scanned := 0
	var offenders []string
	walk := func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "dist", "testdata", "sdk-tests", "cli-tests", "terraform-tests":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		text := string(body)
		dispatches := strings.Contains(text, `"os/exec"`) ||
			strings.Contains(text, "exec.Command") ||
			strings.Contains(text, "sim.StartProcess(") ||
			strings.Contains(text, "sim.ProcessConfig{")
		if !dispatches {
			return nil
		}
		if reason, ok := allowList[relative]; ok {
			t.Logf("allowlisted %s: %s", relative, reason)
			return nil
		}
		offenders = append(offenders, relative)
		return nil
	}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, walk))
	}

	require.Greaterf(t, scanned, 200, "the gate read only %d files under %s", scanned, strings.Join(roots, ", "))

	require.Emptyf(t, offenders,
		"these files dispatch a process on the host — a workload must run on the Docker host, or through the placement backend (docs/architecture.md): %s",
		strings.Join(offenders, ", "))

	// Each allow-list entry must still reach the host, so a removed dispatch drops
	// off the list instead of licensing whatever the file becomes.
	for relative := range allowList {
		body, err := os.ReadFile(filepath.Join(repoRoot, relative))
		require.NoErrorf(t, err, "allowlisted %s no longer exists — remove it from the list", relative)
		text := string(body)
		require.Truef(t,
			strings.Contains(text, `"os/exec"`) || strings.Contains(text, "exec.Command") ||
				strings.Contains(text, "sim.StartProcess(") || strings.Contains(text, "sim.ProcessConfig{"),
			"allowlisted %s no longer dispatches a host process — remove it from the list", relative)
	}
}
