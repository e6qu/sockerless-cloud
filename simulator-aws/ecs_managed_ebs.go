package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A restore recorded but not yet performed. Amazon ECS answers RunTask at once
// and hydrates the volume while the task moves PROVISIONING → PENDING →
// RUNNING, with the attachment ATTACHING; copying inline would hold RunTask
// open for the length of the copy.
type ecsPendingEBSRestore struct {
	AttachmentID string
	VolumeName   string
	// DockerDst is created as an engine volume and filled from DockerSrc for a
	// snapshot volume.
	DockerSrc string
	DockerDst string
	// A volume Amazon EC2 is still restoring from a snapshot; the task waits for
	// it.
	HydratingVolumeID string
}

func ecsPrepareManagedEBSVolumes(ctx context.Context, td ECSTaskDefinition, configs []ECSTaskVolumeConfiguration, taskArn, requestedSubnet string) (map[string]string, []ECSAttachment, []ecsPendingEBSRestore, *ecsRequestError) {
	taskID := taskArn[strings.LastIndex(taskArn, "/")+1:]
	allowed := ecsConfiguredAtLaunchVolumes(td)
	hosts := map[string]string{}
	var attachments []ECSAttachment
	var restores []ecsPendingEBSRestore
	if len(configs) == 0 {
		return hosts, attachments, restores, nil
	}

	az := awsAvailabilityZone()
	if requestedSubnet != "" {
		if subnet, ok := ec2Subnets.Get(requestedSubnet); ok && subnet.AvailabilityZone != "" {
			az = subnet.AvailabilityZone
		}
	}

	for _, cfg := range configs {
		if cfg.Name == "" {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", "volumeConfigurations.name is required", http.StatusBadRequest}
		}
		if !allowed[cfg.Name] {
			return nil, nil, nil, &ecsRequestError{"ClientException", fmt.Sprintf("Volume %s is not configuredAtLaunch in the task definition", cfg.Name), http.StatusBadRequest}
		}
		managed := cfg.ManagedEBSVolume
		if managed == nil {
			return nil, nil, nil, &ecsRequestError{"ClientException", fmt.Sprintf("Volume %s requires managedEBSVolume", cfg.Name), http.StatusBadRequest}
		}
		if managed.RoleArn == "" {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Volume %s requires managedEBSVolume.roleArn", cfg.Name), http.StatusBadRequest}
		}
		actor := awsActor{Service: "ecs.amazonaws.com", Source: taskArn, Role: managed.RoleArn}
		if _, err := awsAssumeServiceRole(actor); err != nil {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("The roleArn of volume %s could not be assumed. Verify the role exists and its trust policy allows ECS to assume it.", cfg.Name), http.StatusBadRequest}
		}

		size := managed.SizeInGiB
		if size == 0 {
			size = 8
		}
		snapshotID := managed.SnapshotId
		var snapshotDockerVolumeName string
		var snapshotHostPath string
		if snapshotID != "" {
			snap, ok := ec2Snapshots.Get(snapshotID)
			if !ok {
				return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Snapshot not found: %s", snapshotID), http.StatusBadRequest}
			}
			if snap.State != "completed" {
				return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Snapshot is not completed: %s", snapshotID), http.StatusBadRequest}
			}
			if size < snap.VolumeSize {
				size = snap.VolumeSize
			}
			snapshotDockerVolumeName = snap.DockerVolumeName
			snapshotHostPath = snap.HostPath
		}
		volumeType := managed.VolumeType
		if volumeType == "" {
			volumeType = "gp3"
		}
		fsType := managed.FilesystemType
		if fsType == "" {
			fsType = ebsDefaultFilesystemType
		}
		if !ebsValidFilesystemType(fsType) {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("Invalid filesystemType %q for volume %s: supported values are %s", managed.FilesystemType, cfg.Name, strings.Join(ebsFilesystemTypes, ", ")), http.StatusBadRequest}
		}
		deleteOnTermination := true
		if managed.TerminationPolicy != nil && managed.TerminationPolicy.DeleteOnTermination != nil {
			deleteOnTermination = *managed.TerminationPolicy.DeleteOnTermination
		}

		processMode := sim.DockerClient() == nil
		if snapshotDockerVolumeName != "" && processMode {
			return nil, nil, nil, &ecsRequestError{"InvalidParameterException", fmt.Sprintf("managed-EBS snapshot %s is Docker-volume-backed and cannot be restored under SIM_RUNTIME=process — start the simulator in container runtime to restore it", snapshotID), http.StatusBadRequest}
		}
		vol, requestErr := ecsCreateManagedEBSVolume(ctx, actor, cfg.Name, managed, size, snapshotID, volumeType, az)
		if requestErr != nil {
			return nil, nil, nil, requestErr
		}
		volumeID := vol.VolumeId
		ecsManagedVolumeOwners.Put(volumeID, ecsManagedVolumeOwner{TaskArn: taskArn, RoleArn: managed.RoleArn})
		// A Fargate task is no instance AttachVolume names; Amazon ECS records the
		// task's hold itself.
		vol.State = "in-use"
		vol.Attachments = []EC2VolumeAttachment{{
			VolumeId:            volumeID,
			InstanceId:          taskID,
			Device:              cfg.Name,
			State:               "attached",
			AttachTime:          time.Now().UTC().Format(time.RFC3339),
			DeleteOnTermination: deleteOnTermination,
		}}
		// A volume from an Amazon EC2 snapshot hydrates on the host; any other is an
		// engine named volume, reachable by sibling task containers; in process mode
		// the host path serves.
		if snapshotHostPath != "" {
			restores = append(restores, ecsPendingEBSRestore{
				AttachmentID:      "ebs-" + volumeID,
				VolumeName:        cfg.Name,
				HydratingVolumeID: volumeID,
			})
		}
		if snapshotHostPath == "" && !processMode {
			_ = os.RemoveAll(vol.HostPath)
			vol.HostPath = ""
			vol.DockerVolumeName = ebsECSDockerVolumeName(volumeID)
			// Filling from a snapshot happens on the task's transition.
			if snapshotDockerVolumeName != "" {
				restores = append(restores, ecsPendingEBSRestore{
					AttachmentID: "ebs-" + volumeID,
					VolumeName:   cfg.Name,
					DockerSrc:    snapshotDockerVolumeName,
					DockerDst:    vol.DockerVolumeName,
				})
			}
		}
		ec2Volumes.Put(volumeID, vol)
		if vol.DockerVolumeName != "" {
			hosts[cfg.Name] = vol.DockerVolumeName
		} else {
			hosts[cfg.Name] = vol.HostPath
		}
		attachments = append(attachments, ECSAttachment{
			Id:     "ebs-" + volumeID,
			Type:   "AmazonElasticBlockStorage",
			Status: "ATTACHING",
			Details: []ECSKeyValuePair{
				{Name: "volumeName", Value: cfg.Name},
				{Name: "volumeId", Value: volumeID},
				{Name: "deleteOnTermination", Value: strconv.FormatBool(deleteOnTermination)},
			},
		})
	}
	return hosts, attachments, restores, nil
}

// The infrastructure role that created the volume releases it.
type ecsManagedVolumeOwner struct {
	TaskArn string `json:"taskArn"`
	RoleArn string `json:"roleArn"`
}

var ecsManagedVolumeOwners sim.Store[ecsManagedVolumeOwner]

// The configuration maps 1:1 onto CreateVolume.
func ecsCreateManagedEBSVolume(ctx context.Context, actor awsActor, name string, managed *ECSTaskManagedEBSVolumeConfiguration, size int, snapshotID, volumeType, az string) (EC2Volume, *ecsRequestError) {
	form := url.Values{
		"AvailabilityZone": {az},
		"Size":             {strconv.Itoa(size)},
		"VolumeType":       {volumeType},
	}
	if snapshotID != "" {
		form.Set("SnapshotId", snapshotID)
	}
	if managed.Iops > 0 {
		form.Set("Iops", strconv.Itoa(managed.Iops))
	}
	if managed.Throughput > 0 {
		form.Set("Throughput", strconv.Itoa(managed.Throughput))
	}
	if managed.Encrypted {
		form.Set("Encrypted", "true")
	}
	if managed.KmsKeyId != "" {
		form.Set("KmsKeyId", managed.KmsKeyId)
	}
	if tags := ecsManagedEBSTags(managed.TagSpecifications); len(tags) > 0 {
		form.Set("TagSpecification.1.ResourceType", "volume")
		for i, tag := range tags {
			form.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Key", i+1), tag.Key)
			form.Set(fmt.Sprintf("TagSpecification.1.Tag.%d.Value", i+1), tag.Value)
		}
	}
	status, body, err := awsCallQuery(ctx, actor, "ec2", "CreateVolume", "2016-11-15", form)
	code, message := "", ""
	switch {
	case err != nil:
		code, message = "InternalFailure", err.Error()
		var refused *awsCallError
		if errors.As(err, &refused) {
			code, message = refused.Code, refused.Message
		}
	case status >= 400:
		code, message = awsXMLError(body)
	}
	if code != "" {
		return EC2Volume{}, &ecsRequestError{"ClientException", fmt.Sprintf("The role %s could not create the volume for %s: %s: %s", actor.Role, name, code, message), http.StatusBadRequest}
	}
	vol, ok := ec2Volumes.Get(awsXMLText(body, "volumeId"))
	if !ok {
		return EC2Volume{}, &ecsRequestError{"ServerException", fmt.Sprintf("The volume created for %s was not found", name), http.StatusInternalServerError}
	}
	return vol, nil
}

// Runs on the task's transition path, so a large restore delays only this
// task's RUNNING, not the API response.
func ecsRunPendingEBSRestores(ctx context.Context, taskID string, restores []ecsPendingEBSRestore) error {
	for _, restore := range restores {
		switch {
		case restore.DockerSrc != "":
			if err := ebsCopyDockerVolumes(ctx, restore.DockerSrc, restore.DockerDst); err != nil {
				return fmt.Errorf("volume %s: %w", restore.VolumeName, err)
			}
		case restore.HydratingVolumeID != "":
			if err := ec2AwaitVolumeHydrated(ctx, restore.HydratingVolumeID); err != nil {
				return fmt.Errorf("volume %s: %w", restore.VolumeName, err)
			}
			if vol, ok := ec2Volumes.Get(restore.HydratingVolumeID); ok && vol.State == "error" {
				return fmt.Errorf("volume %s: Amazon EC2 could not restore %s from its snapshot", restore.VolumeName, vol.VolumeId)
			}
		}
		ecsTasks.Update(taskID, func(t *ECSTask) {
			for i := range t.Attachments {
				if t.Attachments[i].Id == restore.AttachmentID {
					t.Attachments[i].Status = "ATTACHED"
				}
			}
		})
	}
	return nil
}

// Returns the volumes whose termination policy deletes them, for the caller to
// pass to ecsDeleteManagedVolumes. Callers run inside an ecsTasks.Update
// callback, which holds the task store's write lock, so the Amazon EC2 calls
// happen after it is released.
func ecsCleanupTaskManagedEBS(task *ECSTask) (doomed []string) {
	for i := range task.Attachments {
		att := &task.Attachments[i]
		if att.Type != "AmazonElasticBlockStorage" {
			continue
		}
		volumeID := ecsTaskDetail(att.Details, "volumeId")
		if volumeID == "" {
			continue
		}
		ec2Volumes.Update(volumeID, func(vol *EC2Volume) {
			vol.State = "available"
			vol.Attachments = nil
		})
		if deleteOnTermination, _ := strconv.ParseBool(ecsTaskDetail(att.Details, "deleteOnTermination")); deleteOnTermination {
			doomed = append(doomed, volumeID)
		}
		att.Status = "DETACHED"
	}
	return doomed
}

// Call after the releasing ecsTasks.Update returns. A volume the role is
// refused stays, as in AWS, and the refusal is logged.
func ecsDeleteManagedVolumes(volumeIDs []string) {
	for _, volumeID := range volumeIDs {
		owner, _ := ecsManagedVolumeOwners.Get(volumeID)
		actor := awsActor{Service: "ecs.amazonaws.com", Source: owner.TaskArn, Role: owner.RoleArn}
		status, body, err := awsCallQuery(context.Background(), actor, "ec2", "DeleteVolume", "2016-11-15", url.Values{"VolumeId": {volumeID}})
		switch {
		case err != nil:
			log.Printf("Amazon ECS: the volume %s of %s was not deleted: %v", volumeID, owner.TaskArn, err)
			continue
		case status >= 400:
			code, message := awsXMLError(body)
			log.Printf("Amazon ECS: the volume %s of %s was not deleted: %s: %s", volumeID, owner.TaskArn, code, message)
			continue
		}
		ecsManagedVolumeOwners.Delete(volumeID)
	}
}
