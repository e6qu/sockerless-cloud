package gcp_sdk_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execCallSite names one `os/exec` use the workload-dispatch invariant permits,
// scoped to the exact expression rather than to the whole file: a file may hold
// one sanctioned call and still be the place a workload exec is added.
type execCallSite struct {
	file string // path relative to the simulator module root
	call string // the exact expression allowed, matched as a substring of the line
	why  string
}

// execCallForms are the ways a Go file reaches os/exec. A file that imports the
// package is scanned for every one of them; a file that does not import it is
// skipped, so an unrelated local variable named `exec` (Cloud Run executions
// are spelled that way) is never mistaken for a process launch.
var execCallForms = []string{
	"exec.Command(",
	"exec.CommandContext(",
	"exec.LookPath(",
	"exec.Cmd{",
}

// Workload-dispatch invariant. No sim handler may execute a workload via
// `os/exec`. Container/FaaS workloads run in a Docker host honouring the
// workload's Architecture field. VM-level resources may only use the dedicated
// Firecracker/Linux-networking real-execution substrate described in
// specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.
//
// The scan walks every non-test Go file of the simulator module, `shared/`
// included, so a subpackage cannot host what the top level forbids. The three
// test-suite modules (sdk-tests, cli-tests, terraform-tests) are their own
// modules of test drivers — they shell out to `gcloud`, `terraform` and the
// simulator binary by design — and are outside the invariant.
func TestNoOsExecOfWorkloads(t *testing.T) {
	allowed := []execCallSite{
		{
			file: "cloudbuild.go",
			call: `exec.CommandContext(ctx, "docker", "buildx", "version")`,
			why:  "probes the docker CLI's buildx driver; runs no workload",
		},
		{
			file: "cloudbuild.go",
			call: `exec.LookPath("docker")`,
			why:  "reports whether the docker CLI Cloud Build steps need is installed",
		},
		{
			file: "cloudbuild.go",
			call: `exec.CommandContext(ctx, "docker", "push", target)`,
			why:  "pushes a built Cloud Build image to the registry; sim tooling, not a workload",
		},
		{
			file: "cloudbuild.go",
			call: `exec.CommandContext(ctx, "docker", "rmi", "-f", target)`,
			why:  "drops the local tag after a Cloud Build push; sim tooling, not a workload",
		},
		{
			file: "cloudbuild.go",
			call: `exec.CommandContext(ctx, "docker", args...)`,
			why:  "runs a Cloud Build step through the docker CLI, which dispatches it to the Docker host",
		},
		{
			file: filepath.Join("shared", "container_reaper.go"),
			call: "exec.Command(executable, containerReaperArgument, provider, runID, strconv.Itoa(os.Getpid()))",
			why:  "re-executes the simulator's own binary in reaper mode to reap orphaned containers; the child is the simulator, not a workload",
		},
	}

	// Directories that are not part of the simulator module.
	skipDirs := map[string]bool{
		"sdk-tests":       true,
		"cli-tests":       true,
		"terraform-tests": true,
		"bash-tests":      true,
		"dist":            true,
		"docs":            true,
	}

	simDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve sim dir: %v", err)
	}

	var scanned int
	walkErr := filepath.WalkDir(simDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(simDir, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if path != simDir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(body)
		if !strings.Contains(text, `"os/exec"`) {
			return nil
		}
		scanned++

		var fileSites []execCallSite
		for _, site := range allowed {
			if site.file == rel {
				fileSites = append(fileSites, site)
			}
		}
		if len(fileSites) == 0 {
			t.Errorf("%s imports os/exec but has no sanctioned call site — workloads must dispatch via Docker, or through the dedicated VM real-execution substrate. See specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.", rel)
			return nil
		}

		matched := map[string]bool{}
		for lineNo, line := range strings.Split(text, "\n") {
			if !containsAny(line, execCallForms) {
				continue
			}
			// Cut every sanctioned expression out of the line before deciding:
			// a line that held one sanctioned call and one new one would
			// otherwise pass on the strength of the sanctioned half.
			residue := line
			for _, site := range fileSites {
				if strings.Contains(residue, site.call) {
					matched[site.call] = true
					residue = strings.Replace(residue, site.call, "", 1)
				}
			}
			if containsAny(residue, execCallForms) {
				t.Errorf("%s:%d reaches os/exec at an unsanctioned call site:\n\t%s\nWorkloads must dispatch via Docker, or through the dedicated VM real-execution substrate. See specs/SIMULATOR_REAL_EXECUTION.md and feedback_sim_host_model.md.",
					rel, lineNo+1, strings.TrimSpace(line))
			}
		}
		// A sanctioned call site that no longer exists would silently widen the
		// allowance for whatever is added to the file next.
		for _, site := range fileSites {
			if !matched[site.call] {
				t.Errorf("%s no longer contains the sanctioned call site %q (%s) — drop the stale allowance", rel, site.call, site.why)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk sim module: %v", walkErr)
	}

	// A scan that reached nothing proves nothing: the allowlist names files that
	// do import os/exec today, so the walk must have found them.
	if scanned == 0 {
		t.Fatal("the os/exec scan examined no file — the walk did not reach the simulator sources")
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
