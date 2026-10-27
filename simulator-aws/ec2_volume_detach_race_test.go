package main

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A volume restamp on an instance transition and a DetachVolume are two
// read-modify-writes of one row; a restamp deciding from a stale read would
// put the attachment back, and DeleteVolume would then be refused. Only slow
// transitions make the race observable.
func TestEC2DetachIsNotUndoneByAConcurrentInstanceTransition(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
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

// Termination reads attachments under the same lock, so a volume detached
// mid-walk is not written back attached. A delete-on-termination volume is
// still removed.
func TestEC2TerminationReleaseIsNotUndoneByAConcurrentDetach(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
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
			continue // The termination walk removed it.
		}
		if len(vol.Attachments) != 0 {
			t.Fatalf("attempt %d: the volume survived with %d attachment(s) to the terminated instance",
				attempt, len(vol.Attachments))
		}
	}
}
