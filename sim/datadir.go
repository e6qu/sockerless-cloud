package sim

import (
	"os"
	"path/filepath"
)

// ScopedDataDir resolves the host directory a service slice keeps its bulk
// data in — file-share trees, bucket payloads, volume images, build caches:
//
//  1. the directory overrideEnv names, when overrideEnv is non-empty and set;
//  2. <SIM_DATA_DIR>/<subdir>, so the data survives a restart beside the
//     SQLite state the same directory holds;
//  3. <os.TempDir()>/<tempName>, for a process-lifetime run.
func ScopedDataDir(overrideEnv, subdir, tempName string) string {
	if overrideEnv != "" {
		if dir := os.Getenv(overrideEnv); dir != "" {
			return dir
		}
	}
	if dataDir := os.Getenv("SIM_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, subdir)
	}
	return filepath.Join(os.TempDir(), tempName)
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
