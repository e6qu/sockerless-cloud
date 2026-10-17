package main

import "slices"

// An Amazon ECS managed EBS volume is an engine named volume, created with the
// simulator's labels by the first container that binds it. The size the task
// asked for and the filesystem it named are validated as Amazon ECS validates
// them and not realized: the volume has the engine's capacity.

// ebsDefaultFilesystemType is the filesystem Amazon ECS formats a managed
// volume with when the task names none.
const ebsDefaultFilesystemType = "xfs"

var ebsFilesystemTypes = []string{"ext3", "ext4", "xfs"}

func ebsValidFilesystemType(fsType string) bool {
	return slices.Contains(ebsFilesystemTypes, fsType)
}
