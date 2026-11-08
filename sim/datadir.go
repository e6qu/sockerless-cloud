package sim

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// ScopedDataDir resolves the host directory a service slice keeps its bulk
// data in — file-share trees, bucket payloads, volume images, build caches:
//
//  1. the directory overrideEnv names, when overrideEnv is non-empty and set;
//  2. <SIM_DATA_DIR>/<subdir>, so the data survives a restart beside the
//     SQLite state the same directory holds;
//  3. <os.TempDir()>/<tempName>-<pid>, for a process-lifetime run, so two
//     simulators on one host never share it; the server's shutdown removes
//     it.
func ScopedDataDir(overrideEnv, subdir, tempName string) string {
	if overrideEnv != "" {
		if dir := os.Getenv(overrideEnv); dir != "" {
			return dir
		}
	}
	if dataDir := os.Getenv("SIM_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, subdir)
	}
	dir := filepath.Join(os.TempDir(), tempName+"-"+strconv.Itoa(os.Getpid()))
	processDataDirs.Lock()
	processDataDirs.held[dir] = true
	processDataDirs.Unlock()
	return dir
}

// processDataDirs holds the per-process directories and counts the servers
// serving from them; the last server to stop removes them.
var processDataDirs = struct {
	sync.Mutex
	held    map[string]bool
	servers int
}{held: map[string]bool{}}

func releaseProcessDataDirs(remove bool) {
	processDataDirs.Lock()
	defer processDataDirs.Unlock()
	processDataDirs.servers--
	if !remove || processDataDirs.servers > 0 {
		return
	}
	for dir := range processDataDirs.held {
		_ = os.RemoveAll(dir)
		delete(processDataDirs.held, dir)
	}
}

// EnsureWritableDir creates dir and its parents and makes dir mode 0777, so a
// workload bind-mounting it writes to it under any uid. MkdirAll alone lands
// at 0755 under the usual umask; the chmod is not masked.
func EnsureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	return os.Chmod(dir, 0o777)
}
