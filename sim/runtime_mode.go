package sim

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RuntimeMode is how a simulator process was started, and decides whether it
// holds a container engine client. Startup resolves it once from the
// SIM_RUNTIME environment variable and carries it on the server: re-reading
// the variable later would let what the process reports about itself drift
// from what it actually has.
type RuntimeMode string

const (
	// RuntimeModeContainer is the default: workloads run as containers, so the
	// process must hold a container engine client and refuses to start without
	// one.
	RuntimeModeContainer RuntimeMode = "docker"

	// RuntimeModeAPIOnly serves the API surface without a container engine, for
	// runs that exercise control planes and never execute a workload. It is the
	// one mode that legitimately holds no engine client, which is why it has to
	// be asked for by name rather than fallen into.
	RuntimeModeAPIOnly RuntimeMode = "process"
)

// runtimeModeVariable is the environment variable that selects the mode.
const runtimeModeVariable = "SIM_RUNTIME"

// runtimeModes is every mode the simulator understands.
var runtimeModes = map[RuntimeMode]bool{
	RuntimeModeContainer: true,
	RuntimeModeAPIOnly:   true,
}

// ExecutesWorkloads reports whether the mode runs containers, and therefore
// whether startup must obtain a container engine client before serving.
func (m RuntimeMode) ExecutesWorkloads() bool { return m != RuntimeModeAPIOnly }

// ResolveRuntimeMode reads the runtime mode from the environment. An empty
// variable is the container default; anything else must name a mode the
// simulator implements. It refuses an unrecognised value rather than reading
// it as "not API-only": the variable alone decides whether the process can run
// workloads, and a misread shows up much later as workloads failing one by
// one against a process that looks healthy.
func ResolveRuntimeMode() (RuntimeMode, error) {
	value := strings.TrimSpace(os.Getenv(runtimeModeVariable))
	if value == "" {
		return RuntimeModeContainer, nil
	}
	mode := RuntimeMode(value)
	if !runtimeModes[mode] {
		known := make([]string, 0, len(runtimeModes))
		for name := range runtimeModes {
			known = append(known, string(name))
		}
		sort.Strings(known)
		return "", fmt.Errorf("%s=%q is not a runtime mode this simulator implements (known modes: %s)",
			runtimeModeVariable, value, strings.Join(known, ", "))
	}
	return mode, nil
}
