package main

import (
	"errors"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// Each public AMI names an OCI image on mirror.gcr.io pinned by digest;
// scripts/check-latest-deps.sh keeps the pins current.
const (
	ec2AmazonLinux2023OCI = "mirror.gcr.io/library/amazonlinux:2023@sha256:c04a1012acc483977f61f7f91c28624a39a621e56a59781319a75dec528be9b6"
	ec2Ubuntu2404OCI      = "mirror.gcr.io/library/ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852"
	ec2Debian12OCI        = "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880"
)

const (
	ec2AmazonImageOwnerID    = "137112412989"
	ec2CanonicalImageOwnerID = "099720109477"
	ec2DebianImageOwnerID    = "136693071363"
)

// Amazon Linux 2023, Ubuntu 24.04 LTS and Debian 12, each for x86_64 and
// arm64.
var ec2ImageCatalog = sync.OnceValues(func() ([]EC2Image, map[string]EC2Image) {
	public := func(id, name, description, owner, alias, arch, rootDevice, snapshot, oci, created string) EC2Image {
		return EC2Image{
			ImageId:            id,
			Name:               name,
			Description:        description,
			State:              "available",
			OwnerId:            owner,
			OwnerAlias:         alias,
			Public:             true,
			Architecture:       arch,
			ImageType:          "machine",
			RootDeviceType:     "ebs",
			RootDeviceName:     rootDevice,
			VirtualizationType: "hvm",
			Hypervisor:         "xen",
			CreationDate:       created,
			SnapshotId:         snapshot,
			VolumeSize:         8,
			BlockDeviceName:    rootDevice,
			ImageLocation:      oci,
			PlatformDetails:    "Linux/UNIX",
			UsageOperation:     "RunInstances",
			BootMode:           "uefi-preferred",
			EnaSupport:         true,
		}
	}
	list := []EC2Image{
		public("ami-0c101f26f147fa7fd", "al2023-ami-2023.6.20250123.4-kernel-6.1-x86_64",
			"Amazon Linux 2023 AMI 2023.6.20250123.4 x86_64 HVM kernel-6.1",
			ec2AmazonImageOwnerID, "amazon", "x86_64", "/dev/xvda", "snap-0f3ac6ec6d4f1a2c7", ec2AmazonLinux2023OCI, "2025-01-23T22:06:49.000Z"),
		public("ami-0a0c8eebcdd6dcbd0", "al2023-ami-2023.6.20250123.4-kernel-6.1-arm64",
			"Amazon Linux 2023 AMI 2023.6.20250123.4 arm64 HVM kernel-6.1",
			ec2AmazonImageOwnerID, "amazon", "arm64", "/dev/xvda", "snap-07b2d9e0b4c8f6a15", ec2AmazonLinux2023OCI, "2025-01-23T22:06:52.000Z"),
		public("ami-04b4f1a9cf54c11d0", "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20250115",
			"Canonical, Ubuntu, 24.04, amd64 noble image build on 2025-01-15",
			ec2CanonicalImageOwnerID, "", "x86_64", "/dev/sda1", "snap-0d5e9f8c2b1a7e634", ec2Ubuntu2404OCI, "2025-01-15T17:56:40.000Z"),
		public("ami-0a7a4e87acd4fe60f", "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-20250115",
			"Canonical, Ubuntu, 24.04, arm64 noble image build on 2025-01-15",
			ec2CanonicalImageOwnerID, "", "arm64", "/dev/sda1", "snap-0b17c2e4d9a5f3081", ec2Ubuntu2404OCI, "2025-01-15T17:57:12.000Z"),
		public("ami-0779caf41f9ba54f0", "debian-12-amd64-20250113-1998",
			"Debian 12 (20250113-1998)",
			ec2DebianImageOwnerID, "", "x86_64", "/dev/xvda", "snap-03e8d1c7f2a9b4056", ec2Debian12OCI, "2025-01-14T10:42:15.000Z"),
		public("ami-02a1f5b3b8ad1ff6b", "debian-12-arm64-20250113-1998",
			"Debian 12 (20250113-1998)",
			ec2DebianImageOwnerID, "", "arm64", "/dev/xvda", "snap-0c4a7b9e1d2f8e3a0", ec2Debian12OCI, "2025-01-14T10:42:31.000Z"),
	}
	byID := make(map[string]EC2Image, len(list))
	for i, img := range list {
		// Amazon Linux 2023 AMIs are registered with ImdsSupport v2.0.
		if img.OwnerAlias == "amazon" {
			list[i].ImdsSupport = "v2.0"
		}
		byID[img.ImageId] = list[i]
	}
	return list, byID
})

// A published AMI, or one the account registered, created or imported.
func ec2LookupImage(imageID string) (EC2Image, bool) {
	_, catalog := ec2ImageCatalog()
	if img, ok := catalog[imageID]; ok {
		return img, true
	}
	return ec2Images.Get(imageID)
}

// Published AMIs first.
func ec2AllImages() []EC2Image {
	list, _ := ec2ImageCatalog()
	return append(append([]EC2Image(nil), list...), ec2Images.List()...)
}

var errEC2ImageNotBootable = errors.New("the image names no root filesystem to boot")

// An AMI registered from a block device mapping alone has none.
func ec2ImageRootFS(img EC2Image) (vminstance.Image, error) {
	if img.ImageLocation == "" {
		return vminstance.Image{}, errEC2ImageNotBootable
	}
	platform, err := ec2PlatformForArchitecture(img.Architecture)
	if err != nil {
		return vminstance.Image{}, err
	}
	return vminstance.Image{OCI: img.ImageLocation, Architecture: platform}, nil
}

func ec2PlatformForArchitecture(arch string) (string, error) {
	switch arch {
	case "x86_64", "":
		return "linux/amd64", nil
	case "arm64":
		return "linux/arm64", nil
	}
	return "", errors.New("the architecture " + arch + " has no container platform")
}

// Owners: self, amazon, aws-marketplace or an account ID.
func ec2ImageOwnedBy(img EC2Image, owners []string) bool {
	if len(owners) == 0 {
		return true
	}
	for _, owner := range owners {
		switch owner {
		case "self":
			if img.OwnerId == awsAccountID() {
				return true
			}
		case "amazon", "aws-marketplace":
			if img.OwnerAlias == owner {
				return true
			}
		default:
			if img.OwnerId == owner {
				return true
			}
		}
	}
	return false
}
