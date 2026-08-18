package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// An instance reaching "running" or "stopped" restamps every volume attached to
// it. That walk and a DetachVolume for the same volume are two read-modify-write
// sequences over one row, and if the walk decides from a value it read before
// the detach, its write puts the attachment back — leaving a volume the API
// reported as detached still in-use, so the DeleteVolume that follows is
// refused. It needs a slow instance transition to be observable, which is why a
// host running real machines sees it and a fast one does not.
func TestEC2DetachIsNotUndoneByAConcurrentInstanceTransition(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")

	const instanceID = "i-0race"
	for attempt := 0; attempt < 200; attempt++ {
		volumeID := "vol-0race"
		ec2Volumes.Put(volumeID, EC2Volume{
			VolumeId: volumeID,
			State:    "in-use",
			Attachments: []EC2VolumeAttachment{
				{VolumeId: volumeID, InstanceId: instanceID, State: "attached", Device: "/dev/sdf"},
			},
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/", strings.NewReader("Action=DetachVolume&VolumeId="+volumeID))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handleDetachVolume(httptest.NewRecorder(), r)
		}()
		go func() {
			defer wg.Done()
			ec2UpdateVolumeAttachmentsForInstance(instanceID, "attached", "in-use")
		}()
		wg.Wait()

		vol, ok := ec2Volumes.Get(volumeID)
		if !ok {
			t.Fatalf("attempt %d: the volume disappeared", attempt)
		}
		if len(vol.Attachments) != 0 {
			t.Fatalf("attempt %d: the volume still carries %d attachment(s) after a detach that succeeded — "+
				"the instance walk wrote back a value it read before the detach, and DeleteVolume now answers VolumeInUse",
				attempt, len(vol.Attachments))
		}
		if vol.State != "available" {
			t.Fatalf("attempt %d: volume state = %q after detach, want available", attempt, vol.State)
		}
	}
}

// Terminating an instance releases the volumes it held, and reads which
// attachments each volume has for the same reason: a volume detached while the
// walk is in flight must not be written back holding the attachment it just
// lost. A volume marked delete-on-termination is still removed.
func TestEC2TerminationReleaseIsNotUndoneByAConcurrentDetach(t *testing.T) {
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ec2Volumes = sim.MakeStore[EC2Volume](nil, "ec2_volumes")

	const instanceID = "i-0term"
	for attempt := 0; attempt < 200; attempt++ {
		volumeID := "vol-0term"
		ec2Volumes.Put(volumeID, EC2Volume{
			VolumeId: volumeID,
			State:    "in-use",
			Attachments: []EC2VolumeAttachment{
				{VolumeId: volumeID, InstanceId: instanceID, State: "attached", Device: "/dev/sdf"},
			},
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/", strings.NewReader("Action=DetachVolume&VolumeId="+volumeID))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handleDetachVolume(httptest.NewRecorder(), r)
		}()
		go func() {
			defer wg.Done()
			ec2DeleteOnTerminationVolumes(instanceID)
		}()
		wg.Wait()

		vol, ok := ec2Volumes.Get(volumeID)
		if !ok {
			continue // the termination walk removed it, which is its job
		}
		if len(vol.Attachments) != 0 {
			t.Fatalf("attempt %d: the volume survived with %d attachment(s) to the terminated instance",
				attempt, len(vol.Attachments))
		}
	}
}
