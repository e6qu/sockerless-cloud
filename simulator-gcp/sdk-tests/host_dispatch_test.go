package gcp_sdk_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
// `os/exec`. Workloads run in the container engine honouring the workload's
// Architecture field, as specs/SIMULATOR_EXECUTION.md describes.
//
// The scan walks every non-test Go file of the simulator module and of the
// framework module `sim/` it is built on, so the framework cannot host what
// the handlers forbid; paths are repository-relative. The three
// test-suite modules (sdk-tests, cli-tests, terraform-tests) are their own
// modules of test drivers — they shell out to `gcloud`, `terraform` and the
// simulator binary by design — and are outside the invariant.
func TestNoOsExecOfWorkloads(t *testing.T) {
	t.Parallel()
	allowed := []execCallSite{
		{
			file: filepath.Join("sim", "workload", "build.go"),
			call: `exec.CommandContext(ctx, "docker", "buildx", "version")`,
			why:  "probes the docker CLI's buildx driver; runs no workload",
		},
		{
			file: filepath.Join("simulator-gcp", "cloudbuild.go"),
			call: `exec.LookPath("docker")`,
			why:  "reports whether the docker CLI Cloud Build steps need is installed",
		},
		{
			file: filepath.Join("sim", "workload", "build.go"),
			call: `exec.CommandContext(ctx, "docker", args...)`,
			why: "the one docker invocation every Cloud Build step goes through — the build itself, " +
				"the push of its image, and the drop of the local tag afterwards — dispatched to the " +
				"Docker host by the docker CLI, and interruptible so a cancelled build stops the engine's work",
		},
		{
			file: filepath.Join("sim", "workloadhost", "workloadhost.go"),
			call: `exec.CommandContext(ctx, "podman", "machine", "ssh", "--", "ip", "-4", "route", "show", "default")`,
			why:  "reads the Podman machine's default route to find the address workloads reach the host at; runs no workload",
		},
		{
			file: filepath.Join("sim", "container_reaper.go"),
			call: "exec.Command(executable, containerReaperArgument, provider, runID, strconv.Itoa(os.Getpid()))",
			why:  "re-executes the simulator's own binary in reaper mode to reap orphaned containers; the child is the simulator, not a workload",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "guestinit", "main.go"),
			call: `cmd := &exec.Cmd{Path: path, Args: append(append([]string{command[0]}, command[1:]...), args...), Env: env}`,
			why:  "the guest's init, which runs inside a microVM and starts the workload, its members and exec requests there, each resolved in its own root; never on the simulator's host",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "firecracker.go"),
			call: `exec.Command(g.agent.cfg.firecracker, "--api-sock", api)`,
			why:  "the node agent program, which runs inside the agent container, starting Firecracker for a guest there",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "guest.go"),
			call: `exec.CommandContext(ctx, "du", "-sk", tree)`,
			why:  "the node agent sizing a guest's root filesystem inside the agent container",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "guest.go"),
			call: `exec.CommandContext(ctx, "mkfs.ext4", "-q", "-F", "-d", tree, image)`,
			why:  "the node agent building a guest's root filesystem inside the agent container",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "network.go"),
			call: `exec.CommandContext(ctx, "nft", "-f", "-")`,
			why:  "the node agent filtering a guest's tap inside the agent container's own namespace",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "export.go"),
			call: `exec.CommandContext(ctx, "debugfs", "-R", "ls -p /", image)`,
			why:  "the node agent listing a guest's root filesystem inside the agent container",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "export.go"),
			call: `exec.CommandContext(ctx, "debugfs", "-f", commands.Name(), image)`,
			why:  "the node agent reading a guest's root filesystem out of its image inside the agent container",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "export.go"),
			call: `exec.CommandContext(ctx, "tar", "--numeric-owner", "-C", dir, "-cf", "-", ".")`,
			why:  "the node agent archiving a guest's root filesystem inside the agent container",
		},
		{
			file: filepath.Join("sim", "nodeagent", "cmd", "nodeagent", "network.go"),
			call: `exec.CommandContext(ctx, args[0], args[1:]...)`,
			why:  "the node agent configuring its bridge and taps inside the agent container's own namespace",
		},
		{
			file: filepath.Join("sim", "process_unix.go"),
			call: `func DetachCommand(command *exec.Cmd)`,
			why:  "detaches the container reaper the simulator re-executes; runs no workload",
		},
		{
			file: filepath.Join("sim", "process_windows.go"),
			call: `func DetachCommand(command *exec.Cmd)`,
			why:  "detaches the container reaper the simulator re-executes; runs no workload",
		},
	}

	// Directories that are not part of the simulator module.
	skipDirs := map[string]bool{
		"sdk-tests":       true,
		"cli-tests":       true,
		"terraform-tests": true,
		"dist":            true,
		"docs":            true,
	}

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	roots := []string{filepath.Join(repoRoot, "simulator-gcp"), filepath.Join(repoRoot, "sim")}

	var scanned int
	matched := map[execCallSite]bool{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			return relErr
		}
		if d.IsDir() {
			if !slices.Contains(roots, path) && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
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
			t.Errorf("%s imports os/exec but has no sanctioned call site — workloads must dispatch through the container engine. See specs/SIMULATOR_EXECUTION.md.", rel)
			return nil
		}

		for lineNo, line := range strings.Split(text, "\n") {
			// Cut every sanctioned expression out of the line before deciding:
			// a line that held one sanctioned call and one new one would
			// otherwise pass on the strength of the sanctioned half. A
			// sanctioned expression may also be a declaration that only types
			// a value, which is how a file reaches os/exec without a call.
			residue := line
			for _, site := range fileSites {
				if strings.Contains(residue, site.call) {
					matched[site] = true
					residue = strings.Replace(residue, site.call, "", 1)
				}
			}
			if containsAny(residue, execCallForms) {
				t.Errorf("%s:%d reaches os/exec at an unsanctioned call site:\n\t%s\nWorkloads must dispatch through the container engine. See specs/SIMULATOR_EXECUTION.md.",
					rel, lineNo+1, strings.TrimSpace(line))
			}
		}
		return nil
	}
	for _, root := range roots {
		if walkErr := filepath.WalkDir(root, walk); walkErr != nil {
			t.Fatalf("walk %s: %v", root, walkErr)
		}
	}

	// A sanctioned call site that no longer exists, in a file that still
	// imports os/exec or in one that no longer does, would silently widen the
	// allowance for whatever is added to the file next.
	for _, site := range allowed {
		if !matched[site] {
			t.Errorf("%s no longer contains the sanctioned call site %q (%s) — drop the stale allowance", site.file, site.call, site.why)
		}
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
