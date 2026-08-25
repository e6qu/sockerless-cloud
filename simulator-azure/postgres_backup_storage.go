package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Flexible-server backups carry the server's data.
//
// A server's engine keeps its data directory in the named volume
// sockerless-azurepg-<rg>-<name>. An on-demand backup captures that volume
// into a backup volume, and a create with createMode=PointInTimeRestore
// clones a backup volume into the new server's — the same operation in the
// other direction, run before the new server's data plane installs. Deleting
// a backup deletes its volume; deleting the server deletes its backups and
// their volumes, as the service does.
//
// The capture is sim.SnapshotVolume's single `cp -a --reflink=auto`: on a
// container engine whose volume store sits on btrfs, XFS with reflinks, or
// OpenZFS with block cloning, the capture clones blocks copy-on-write and is
// effectively instant however large the database; on any other filesystem the
// same command is a real full copy. The ARM surface is identical either way —
// the filesystem underneath is the deployment's choice, and the log line
// below is how an operator confirms which they got.
//
// On an API-only host servers are modeled without a running engine, and
// their backups are exactly as modeled as they are: metadata, no volume, the
// same fidelity tier as everything else on that host.

func azurePGBackupVolume(rg, name, backupName string) string {
	return "sockerless-azurepg-backup-" + strings.ToLower(rg) + "-" + strings.ToLower(name) + "-" + strings.ToLower(backupName)
}

// azurePGCaptureVolume captures a server's volume into a backup volume and
// logs whether the filesystem gave the copy-on-write path. A source volume
// that does not exist — the modeled tier, or an engine that never started —
// captures nothing, which is that server's whole state.
func azurePGCaptureVolume(rg, name, backupVolume string) error {
	if sim.RequireContainerRuntime("capturing a flexible-server backup") != nil {
		return nil
	}
	if !sim.VolumeExists(azurePGServerVolume(rg, name)) {
		return nil
	}
	filesystem, err := sim.SnapshotVolume(context.Background(), azurePGServerVolume(rg, name), backupVolume)
	if err != nil {
		return err
	}
	if sim.VolumeSnapshotIsInstant(filesystem) {
		fmt.Fprintf(os.Stderr, "[sim-azurepg] backup %s captured copy-on-write on %s\n", backupVolume, filesystem)
	} else {
		fmt.Fprintf(os.Stderr, "[sim-azurepg] backup %s captured by full copy on %s (put the engine's volume store on btrfs, XFS with reflinks, or OpenZFS block cloning for instant backups)\n", backupVolume, filesystem)
	}
	return nil
}

// azurePGRemoveBackupVolume removes a backup's volume when the backup is
// deleted. A volume that never existed — the modeled tier — needs nothing.
func azurePGRemoveBackupVolume(name string) {
	azurePGRemoveVolumeSettled(name)
}

// azurePGRestoreSource is the volume a PointInTimeRestore create clones the
// new server's data from.
type azurePGRestoreSource struct {
	sourceRG   string
	sourceName string
	// backupVolume is the captured backup to restore; empty means no backup
	// preceded the requested point in time, so the clone takes the source
	// server's live volume — a restore at the latest restorable time.
	backupVolume string
}

// azurePGPickRestoreSource resolves which volume a PointInTimeRestore clones:
// the newest backup of the source server whose completedTime is at or before
// the requested point in time, or the source's live volume when no backup
// qualifies.
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
			// A capture still in flight, or one whose LRO failed, is not a
			// restorable point.
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

// azurePGCloneForRestore seeds a restored server's volume from its restore
// source, before the new server's data plane installs. A source without a
// volume — the modeled tier — restores the state it captured: nothing.
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
