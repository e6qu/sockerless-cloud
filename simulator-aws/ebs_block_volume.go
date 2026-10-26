package main

import "slices"

// An Amazon ECS managed EBS volume is an engine named volume that the first
// container binding it creates. Amazon ECS's size and filesystem validation
// applies, but the volume has the engine's capacity.

// Amazon ECS formats a managed volume with this when the task names none.
const ebsDefaultFilesystemType = "xfs"

var ebsFilesystemTypes = []string{"ext3", "ext4", "xfs"}

func ebsValidFilesystemType(fsType string) bool {
	return slices.Contains(ebsFilesystemTypes, fsType)
}
