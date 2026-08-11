package main

import (
	"os"
	"path/filepath"
)

// simScopedDataDir resolves the on-disk root for a service slice's bulk data
// (EFS file trees, EBS volume backing images, Amplify build caches).
// Resolution order:
//
//  1. overrideEnv, when non-empty and set — the slice-specific explicit
//     override (e.g. SIM_EFS_DATA_DIR, SIM_EBS_DATA_DIR).
//  2. <SIM_DATA_DIR>/<subdir>, when SIM_DATA_DIR — the persistence
//     coordinate the shared server config reads — is set, so bulk data
//     survives a simulator restart alongside the SQLite control-plane state.
//  3. <os.TempDir()>/<tempFallback> when neither is set.
func simScopedDataDir(overrideEnv, subdir, tempFallback string) string {
	if overrideEnv != "" {
		if dir := os.Getenv(overrideEnv); dir != "" {
			return dir
		}
	}
	if dataDir := os.Getenv("SIM_DATA_DIR"); dataDir != "" {
		return filepath.Join(dataDir, subdir)
	}
	return filepath.Join(os.TempDir(), tempFallback)
}
