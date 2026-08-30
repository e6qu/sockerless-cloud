package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A machine's disk outlives the guest process that runs it.
//
// Firecracker builds its root filesystem inside a working directory it removes
// when the guest stops, so a stopped machine used to have no disk at all. Azure
// is the other way round: the managed disk is a resource of its own, which is
// why a deallocated machine restarts with its data and why the normal capture
// sequence is deallocate, generalize, capture. Tying the disk to the guest's
// lifetime made that sequence impossible to perform — generalizing first
// destroyed the disk the capture needed, and capturing first was refused for
// want of generalization, so the operation was unreachable by any order of
// calls.
//
// The disk is therefore copied to a path of its own before the guest goes away,
// and read from there by anything that needs the disk of a machine that is not
// running.

// azureVMDiskDir holds the preserved disks, one file per machine.
func azureVMDiskDir() string {
	return filepath.Join(os.TempDir(), "sockerless-azure-vm-disks")
}

// azureVMDiskPath names the preserved disk of a machine. The resource id is
// hashed because it holds slashes and is longer than a file name allows.
func azureVMDiskPath(vmID string) string {
	sum := sha256.Sum256([]byte(vmID))
	return filepath.Join(azureVMDiskDir(), hex.EncodeToString(sum[:8])+".ext4")
}

// azurePreserveVMDisk copies a running machine's root filesystem to the disk
// path that survives it. It runs before the guest is stopped, which is the last
// moment the file exists.
func azurePreserveVMDisk(vmID, workDir string) error {
	source := filepath.Join(workDir, "rootfs.ext4")
	if _, err := os.Stat(source); err != nil {
		// A machine whose guest never finished booting has no disk to keep,
		// and that is not a failure of the stop.
		return nil
	}
	if err := os.MkdirAll(azureVMDiskDir(), 0o755); err != nil {
		return fmt.Errorf("make the disk directory for %q: %w", vmID, err)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("read the disk of %q to preserve it: %w", vmID, err)
	}
	defer in.Close()

	// Write beside the destination and rename, so a stop interrupted midway
	// leaves the previous disk rather than a truncated one.
	temporary := azureVMDiskPath(vmID) + ".partial"
	out, err := os.Create(temporary)
	if err != nil {
		return fmt.Errorf("create the preserved disk of %q: %w", vmID, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(temporary)
		return fmt.Errorf("copy the disk of %q: %w", vmID, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("finish the preserved disk of %q: %w", vmID, err)
	}
	if err := os.Rename(temporary, azureVMDiskPath(vmID)); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("publish the preserved disk of %q: %w", vmID, err)
	}
	return nil
}

// azureReadVMDisk returns the bytes of a machine's disk: the live one when the
// guest is running, the preserved copy when it is not.
func azureReadVMDisk(vmID, liveWorkDir string) ([]byte, error) {
	if liveWorkDir != "" {
		if disk, err := os.ReadFile(filepath.Join(liveWorkDir, "rootfs.ext4")); err == nil {
			return disk, nil
		}
	}
	disk, err := os.ReadFile(azureVMDiskPath(vmID))
	if err != nil {
		return nil, fmt.Errorf(
			"virtual machine %q has no disk to read: it is not running and none was preserved when it stopped: %w",
			vmID, err)
	}
	return disk, nil
}

// azureDiscardVMDisk removes a preserved disk, for a machine that is deleted
// rather than stopped.
func azureDiscardVMDisk(vmID string) {
	os.Remove(azureVMDiskPath(vmID))
}
