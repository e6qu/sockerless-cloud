package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The modules a process has loaded, read from the process itself.
//
// A site's processes come from the container engine's process table, which
// reports them in the engine host's PID namespace — so where the simulator and
// the engine share a kernel, `/proc/<pid>/maps` is the process's own mapping
// table and its file-backed entries are the modules it has loaded. That is the
// only source for this: a module's load address is a fact about a running
// process, and nothing else knows it.
//
// Where the simulator and the engine do not share a kernel — the engine in a
// virtual machine, which is every macOS host — /proc holds no such process and
// the read declares that rather than answering.

// webProcModule is one loaded module: the file, and the address the lowest of
// its mappings begins at.
type webProcModule struct {
	Path        string
	BaseAddress uint64
	Size        uint64
}

// webProcessModules reads the modules a process has loaded. The second result
// is false when this host cannot see the process, which is not the same as a
// process with no modules.
func webProcessModules(proc webProcess) ([]webProcModule, bool) {
	if !webProcIsProcess(proc) {
		return nil, false
	}
	f, err := os.Open(fmt.Sprintf("/proc/%d/maps", proc.PID))
	if err != nil {
		return nil, false
	}
	defer f.Close()

	// One module spans several mappings — the text, the read-only data and the
	// writable data are separate lines for the same file — so the entries are
	// folded by path: the module begins where its lowest mapping begins and
	// ends where its highest one ends.
	type extent struct{ low, high uint64 }
	byPath := map[string]extent{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// addr-addr perms offset dev inode [path]
		if len(fields) < 6 {
			continue
		}
		path := strings.Join(fields[5:], " ")
		// An anonymous mapping, the stack, the heap and the kernel's own
		// mappings are not modules; a module is a file the process mapped.
		if !strings.HasPrefix(path, "/") {
			continue
		}
		lowText, highText, ok := strings.Cut(fields[0], "-")
		if !ok {
			continue
		}
		low, lowErr := strconv.ParseUint(lowText, 16, 64)
		high, highErr := strconv.ParseUint(highText, 16, 64)
		if lowErr != nil || highErr != nil {
			continue
		}
		current, seen := byPath[path]
		if !seen {
			byPath[path] = extent{low: low, high: high}
			continue
		}
		if low < current.low {
			current.low = low
		}
		if high > current.high {
			current.high = high
		}
		byPath[path] = current
	}
	if scanner.Err() != nil {
		return nil, false
	}

	modules := make([]webProcModule, 0, len(byPath))
	for path, span := range byPath {
		modules = append(modules, webProcModule{
			Path: path, BaseAddress: span.low, Size: span.high - span.low,
		})
	}
	// Lowest address first, which is the order the mapping table itself is in.
	sort.Slice(modules, func(i, j int) bool { return modules[i].BaseAddress < modules[j].BaseAddress })
	return modules, true
}

// webProcIsProcess reports whether /proc/<pid> is the process the engine's
// table named, by comparing the command line the kernel records with the one
// the engine reported. Without this a PID that has been reused, or that names
// an unrelated process on a host that is not the engine's, would be read as if
// it were the site's — reporting one process's modules as another's.
func webProcIsProcess(proc webProcess) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", proc.PID))
	if err != nil {
		return false
	}
	// /proc/<pid>/cmdline separates the arguments with NUL and ends with one.
	kernel := strings.Join(strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 }), " ")
	return strings.TrimSpace(kernel) == strings.TrimSpace(proc.CommandLine)
}

// webProcModuleAddress renders a module's base address the way the process's
// own mapping table spells it.
func webProcModuleAddress(base uint64) string {
	return fmt.Sprintf("0x%x", base)
}
