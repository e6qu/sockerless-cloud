package simulator

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// RuntimeMode is how a simulator process was started, and decides whether it
// holds a container engine client. It is resolved once, at startup, from the
// SIM_RUNTIME environment variable, and carried on the server: re-reading the
// variable later would let what the process reports about itself drift from
// what it actually has.
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
// simulator implements.
//
// An unrecognised value is refused rather than read as "not API-only". The
// variable is the only thing standing between a process that runs workloads and
// one that cannot, so a value the simulator does not understand is a
// misconfiguration whose consequences show up much later — as workloads that
// fail one by one against a process that looks healthy — and it costs nothing
// to say so at startup instead.
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
