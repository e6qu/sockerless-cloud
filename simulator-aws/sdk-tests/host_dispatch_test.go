package aws_sdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Workload-dispatch invariant.
//
// No sim handler may execute a workload as a process on the host. Container and
// FaaS workloads run on a Docker host (StartContainerSync / StartHTTPContainer)
// honouring the workload's Architecture field; VM-level resources may only use
// the dedicated Firecracker/Linux-networking real-execution substrate described
// in specs/SIMULATOR_REAL_EXECUTION.md.
//
// The gate looks for both ways a handler can reach the host: `os/exec` directly,
// and a shared `sim.StartProcess` wrapper around it. Watching only for
// `os/exec` would miss a handler that dispatched through a wrapper, so a gate
// that checked only the import could not fail whatever the handlers did.
//
// It walks the whole module, not just the top-level directory, because the
// container reaper lives in shared/ and is named below.
func TestNoHostProcessDispatchOfWorkloads(t *testing.T) {
	// Each entry is a file allowed to reach the host, with the reason. Nothing
	// runs a workload as a host process: there is no process substrate, and
	// SIM_RUNTIME=process means API-only — serving the API surface without a
	// container engine, never executing a workload outside one.
	allowList := map[string]string{
		"shared/container_reaper.go": "reaps the sim's own containers through the docker CLI; not a workload",
	}

	simDir, err := filepath.Abs("..")
	require.NoError(t, err)

	scanned := 0
	var offenders []string
	require.NoError(t, filepath.WalkDir(simDir, func(path string, entry os.DirEntry, walkErr error) error {
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
		relative, err := filepath.Rel(simDir, path)
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
	}))

	// A walk that reached nothing would report clean without having looked.
	require.Greaterf(t, scanned, 200, "the gate read only %d files under %s", scanned, simDir)

	require.Emptyf(t, offenders,
		"these files dispatch a process on the host — a workload must run on the Docker host, or through the dedicated VM real-execution substrate (specs/SIMULATOR_REAL_EXECUTION.md): %s",
		strings.Join(offenders, ", "))

	// Every allowlist entry must still name a file that reaches the host, so a
	// dispatch removed from one of them drops off this list instead of
	// silently licensing whatever the file becomes next.
	for relative := range allowList {
		body, err := os.ReadFile(filepath.Join(simDir, relative))
		require.NoErrorf(t, err, "allowlisted %s no longer exists — remove it from the list", relative)
		text := string(body)
		require.Truef(t,
			strings.Contains(text, `"os/exec"`) || strings.Contains(text, "exec.Command") ||
				strings.Contains(text, "sim.StartProcess(") || strings.Contains(text, "sim.ProcessConfig{"),
			"allowlisted %s no longer dispatches a host process — remove it from the list", relative)
	}
}
