package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A server's engine keeps its data in the volume sockerless-azurepg-<rg>-<name>.
// An on-demand backup captures it into a backup volume, and a
// PointInTimeRestore create clones a backup volume into the new server's before
// its data plane installs. Deleting a backup deletes its volume; deleting the
// server deletes its backups too, as the service does.
//
// sim.SnapshotVolume runs one `cp -a --reflink=auto` with the engine frozen,
// for one crash-consistent point: copy-on-write and instant on btrfs, XFS with
// reflinks or OpenZFS with block cloning, a full copy elsewhere; the log line
// says which.
//
// On an API-only host servers run no engine, and their backups are metadata
// without a volume.

func azurePGBackupVolume(rg, name, backupName string) string {
	return "sockerless-azurepg-backup-" + strings.ToLower(rg) + "-" + strings.ToLower(name) + "-" + strings.ToLower(backupName)
}

// Long-term-retention backups live in their own namespace: their lifecycle is
// independent, and a name used on both must not collide.
func azurePGLtrBackupVolume(rg, name, backupName string) string {
	return "sockerless-azurepg-ltr-" + strings.ToLower(rg) + "-" + strings.ToLower(name) + "-" + strings.ToLower(backupName)
}

// A missing source volume — no engine, or one never started — captures
// nothing, which is that server's whole state.
func azurePGCaptureVolume(rg, name, backupVolume string) error {
	return sim.CaptureVolume(context.Background(), azurePGServerVolume(rg, name), backupVolume, "azurepg")
}

// A volume that never existed needs nothing.
func azurePGRemoveBackupVolume(name string) {
	sim.RemoveVolumeSettled(name, "azurepg")
}

type azurePGRestoreSource struct {
	sourceRG   string
	sourceName string
	// Empty when no backup precedes the requested point, so the clone takes the
	// source's live volume: a restore at the latest restorable time.
	backupVolume string
}

// The newest backup completed at or before the point in time, or the source's
// live volume when none qualifies.
func azurePGPickRestoreSource(sourceSub, sourceRG, sourceName string, pointInTime time.Time) azurePGRestoreSource {
	source := azurePGRestoreSource{sourceRG: sourceRG, sourceName: sourceName}
	prefix := pgServerID(sourceSub, sourceRG, sourceName) + "/backups/"
	var newest time.Time
	for _, b := range pgBackups.List() {
		if !strings.HasPrefix(b.ID, prefix) || b.Properties == nil {
			continue
		}
		completedRaw, _ := b.Properties["completedTime"].(string)
		if completedRaw == "" {
			// A capture in flight, or one whose operation failed, is no restorable point.
			continue
		}
		completed, err := time.Parse(time.RFC3339Nano, completedRaw)
		if err != nil || completed.After(pointInTime) {
			continue
		}
		if completed.After(newest) {
			newest = completed
			source.backupVolume = azurePGBackupVolume(sourceRG, sourceName, b.Name)
		}
	}
	return source
}

// A source without a volume restores what it captured: nothing.
func azurePGCloneForRestore(rg, name string, source azurePGRestoreSource) error {
	if sim.RequireContainerRuntime("restoring a flexible server") != nil {
		return nil
	}
	src := source.backupVolume
	if src == "" {
		src = azurePGServerVolume(source.sourceRG, source.sourceName)
	}
	if !sim.VolumeExists(src) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), src, azurePGServerVolume(rg, name)); err != nil {
		return fmt.Errorf("clone %s into flexible server %s/%s: %w", src, rg, name, err)
	}
	return nil
}
