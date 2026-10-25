package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/sidecar"
)

// A process dump is the memory image of one of the site's processes, written
// from the process itself, as an ELF core a debugger opens. The site's workload
// runs as a container, and the simulator's own host does not let it read
// another identity's /proc, so a platform helper joins the container's pid
// namespace with permission to trace and reads the image there
// (sidecar.ReadProcess). The core carries no register set: reading one stops
// the process, and a dump of a running site must not.

// webReadProcess reads a process's mapping table ("maps") or core ("core")
// through the platform helper; hostPID is the id the engine's table reports.
func webReadProcess(ctx context.Context, containerID string, hostPID int, what string) ([]byte, error) {
	pid, err := sidecar.NamespacePID(hostPID)
	if err != nil {
		return nil, err
	}
	return sidecar.ReadProcess(ctx, containerID, pid, what)
}

// webGetProcessDump answers WebApps_GetProcessDump and its three siblings.
func webGetProcessDump(w http.ResponseWriter, r *http.Request) {
	proc, instance, ok := webResolveProcess(w, r)
	if !ok {
		return
	}
	if !webProcIsProcess(proc) {
		AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"%s is not implemented by the simulator: a process dump is the memory "+
				"image of the process, read from its own /proc/<pid>/mem, and this host "+
				"does not share a kernel with the container engine, so the site's "+
				"processes are not in its /proc.", webProcessDumpOperation(r))
		return
	}
	// The body is the image itself, so nothing may be written before it is
	// known to be producible — an error after the first byte would be a
	// truncated core the caller cannot tell from a complete one.
	core, err := webReadProcess(r.Context(), instance.ID(), proc.PID, "core")
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"%s: read the process's memory image: %v", webProcessDumpOperation(r), err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", fmt.Sprintf("dump-%d.core", proc.PID)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(core)
}

// webProcessDumpOperation names the operation the request addressed, which
// differs by scope and by whether the site is a slot.
func webProcessDumpOperation(r *http.Request) string {
	name := "WebApps_GetProcessDump"
	if webInstanceSegment(r) != "" {
		name = "WebApps_GetInstanceProcessDump"
	}
	if strings.Contains(r.URL.Path, "/slots/") {
		name += "Slot"
	}
	return name
}
