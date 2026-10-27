package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud SQL backups carry the instance's data. The engine keeps its data in
// the volume sockerless-cloudsql-<project>-<instance>; a backup run captures
// it into a backup volume, and instances.restoreBackup clones that back over
// the instance's, with the engine stopped for the swap. Deleting a backup
// deletes its volume, and deleting the instance its runs and their volumes.
//
// The capture is sim.SnapshotVolume's `cp -a --reflink=auto`: copy-on-write
// and near instant on btrfs, reflink XFS or block-cloning OpenZFS, a full copy
// elsewhere; the log line reports which. On an API-only host instances run no
// engine, so their backups are metadata with no volume.

func sqlBackupRunVolume(project, instance string, id int64) string {
	return "sockerless-cloudsql-backup-" + project + "-" + instance + "-" + strconv.FormatInt(id, 10)
}

func sqlBackupVolume(project, backupID string) string {
	return "sockerless-cloudsql-backup-" + project + "-" + backupID
}

// sqlCaptureVolume captures an instance's volume into a backup volume and logs
// whether the copy was copy-on-write. A missing source volume captures
// nothing.
func sqlCaptureVolume(project, instance, backupVolume string) error {
	return sim.CaptureVolume(context.Background(), sqlInstanceVolume(project, instance), backupVolume, "cloudsql")
}

// sqlRestoreVolume stops the instance's engine and replaces its data volume
// with a clone of the backup volume; the engine boots on it at the next
// connection. A backup without a volume restores nothing.
func sqlRestoreVolume(project, instance, backupVolume string) error {
	if sim.RequireContainerRuntime("restoring a Cloud SQL backup") != nil {
		return nil
	}
	sqlStopEngine(project, instance)
	sim.RemoveVolumeSettled(sqlInstanceVolume(project, instance), "cloudsql")
	if !sim.VolumeExists(backupVolume) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), backupVolume, sqlInstanceVolume(project, instance)); err != nil {
		return fmt.Errorf("clone backup volume %s into instance %s: %w", backupVolume, instance, err)
	}
	return nil
}

// sqlCloneVolume seeds a cloned instance's volume from its source's, so the
// clone's first engine start finds the source's data.
func sqlCloneVolume(project, source, destination string) error {
	if sim.RequireContainerRuntime("cloning a Cloud SQL instance") != nil {
		return nil
	}
	if !sim.VolumeExists(sqlInstanceVolume(project, source)) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), sqlInstanceVolume(project, source), sqlInstanceVolume(project, destination)); err != nil {
		return fmt.Errorf("clone instance volume %s into %s: %w", source, destination, err)
	}
	return nil
}

// sqlRemoveBackupVolume removes a deleted backup's volume, if it has one.
func sqlRemoveBackupVolume(name string) {
	if sim.RequireContainerRuntime("deleting a Cloud SQL backup volume") != nil {
		return
	}
	if !sim.VolumeExists(name) {
		return
	}
	sim.RemoveVolumeSettled(name, "cloudsql")
}
