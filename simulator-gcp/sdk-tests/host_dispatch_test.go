package gcp_sdk_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Workload-dispatch invariant. No sim handler may execute a workload via
// `os/exec`. Container/FaaS workloads run in a Docker host honouring the
// workload's Architecture field. VM-level resources may only use the dedicated
// Firecracker/Linux-networking real-execution substrate described in
// specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.
func TestNoOsExecOfWorkloads(t *testing.T) {
	// cloudbuild.go uses os/exec to spawn the `docker` CLI for build
	// steps — that's sim tooling, not a workload.
	allowList := map[string]string{
		"cloudbuild.go": "shells out to `docker` CLI for Cloud Build step execution (sim tooling, not workload)",
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
		if _, ok := allowList[e.Name()]; ok {
			continue
		}
		path := filepath.Join(simDir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)
		if strings.Contains(text, `"os/exec"`) || strings.Contains(text, "exec.Command") {
			t.Errorf("%s imports os/exec or calls exec.Command — workloads must dispatch via Docker, or through the dedicated VM real-execution substrate. See specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.", e.Name())
		}
	}
}
