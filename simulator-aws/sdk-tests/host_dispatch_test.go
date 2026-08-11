package aws_sdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Workload-dispatch invariant.
//
// No sim handler may execute a workload via `os/exec`. Container/FaaS workloads
// run on a Docker host (StartContainerSync / StartHTTPContainer) honouring the
// workload's Architecture field. VM-level resources may only use the dedicated
// Firecracker/Linux-networking real-execution substrate described in
// specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.
//
// Allowlist: sim infrastructure files that legitimately spawn other
// processes that are NOT workloads (the docker CLI, etc.).
func TestNoOsExecOfWorkloads(t *testing.T) {
	allowList := map[string]string{
		// (no production aws/*.go files use os/exec — kept empty to
		// surface any reintroduction. See cloudbuild.go in the GCP sim
		// for the docker-CLI exception pattern.)
	}

	simDir, _ := filepath.Abs("..")
	entries, err := os.ReadDir(simDir)
	if err != nil {
		t.Fatalf("read sim dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		if reason, ok := allowList[e.Name()]; ok {
			t.Logf("allowlisted %s: %s", e.Name(), reason)
			continue
		}
		path := filepath.Join(simDir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)
		// "os/exec" import OR exec.Command call.
		if strings.Contains(text, `"os/exec"`) || strings.Contains(text, "exec.Command") {
			t.Errorf("%s imports os/exec or calls exec.Command — workloads must dispatch via Docker, or through the dedicated VM real-execution substrate. See specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.", e.Name())
		}
	}
}
