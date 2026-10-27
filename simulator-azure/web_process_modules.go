package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim/procimage"
)

// webProcModule is one loaded module: the file, and the address the lowest of
// its mappings begins at.
type webProcModule struct {
	Path        string
	BaseAddress uint64
	Size        uint64
}

// The second result is false when the process is not the one the engine's
// table named.
func webProcessModules(ctx context.Context, containerID string, proc webProcess) ([]webProcModule, bool, error) {
	if !webProcIsProcess(proc) {
		return nil, false, nil
	}
	maps, err := webReadProcess(ctx, containerID, proc.PID, "maps")
	if err != nil {
		return nil, true, err
	}
	mappings, err := procimage.ParseMappings(bytes.NewReader(maps))
	if err != nil {
		return nil, true, err
	}
	var modules []webProcModule
	for _, m := range procimage.Modules(mappings) {
		modules = append(modules, webProcModule{Path: m.Path, BaseAddress: m.BaseAddress, Size: m.Size})
	}
	return modules, true, nil
}

// webProcIsProcess compares the command line the kernel records with the one
// the engine reported, so a reused PID, or one naming an unrelated process on
// a host that is not the engine's, never reports another process's modules.
func webProcIsProcess(proc webProcess) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", proc.PID))
	if err != nil {
		return false
	}
	// /proc/<pid>/cmdline separates the arguments with NUL and ends with one.
	kernel := strings.Join(strings.FieldsFunc(string(raw), func(r rune) bool { return r == 0 }), " ")
	return strings.TrimSpace(kernel) == strings.TrimSpace(proc.CommandLine)
}

func webProcModuleAddress(base uint64) string {
	return fmt.Sprintf("0x%x", base)
}
