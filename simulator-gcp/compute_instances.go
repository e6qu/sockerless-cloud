package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/machine"
)

func registerComputeInstances(srv *sim.Server, networks sim.Store[ComputeNetwork], subnetworks sim.Store[ComputeSubnetwork]) {
	instances := sim.Announcing(sim.MakeStore[ComputeInstance](srv.DB(), "compute_instances"), gceMetadataChanges)
	registerComputeInstanceVerbs(srv, instances)
	gcpInstances = instances
	recoverComputeInstances(instances)
	recoverComputeOperations()
	logger := srv.Logger()

	// The verbs registered beside these handlers share this instance key.
	instanceSelfLink := computeInstanceSelfLink

	normalizeInstance := func(ctx context.Context, project, zone string, inst *ComputeInstance) error {
		inst.Kind = "compute#instance"
		if inst.Id == "" {
			inst.Id = computeNumericID()
		}
		inst.SelfLink = instanceSelfLink(project, zone, inst.Name)
		inst.Zone = fmt.Sprintf("projects/%s/zones/%s", project, zone)
		if inst.CreationTimestamp == "" {
			inst.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		}
		if inst.MachineType == "" {
			inst.MachineType = fmt.Sprintf("projects/%s/zones/%s/machineTypes/e2-micro", project, zone)
		} else if !strings.Contains(inst.MachineType, "/") {
			inst.MachineType = fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, inst.MachineType)
		}
		if _, ok := gcpLookupMachineType(inst.MachineType); !ok {
			return computeFieldRefusal{reason: "invalid", message: fmt.Sprintf(
				"Invalid value for field 'resource.machineType': '%s'. Machine type with name '%s' does not exist in zone '%s'.",
				inst.MachineType, inst.MachineType[strings.LastIndex(inst.MachineType, "/")+1:], zone)}
		}
		inst.Status = ComputeInstanceRunning
		if inst.LabelFingerprint == "" {
			inst.LabelFingerprint = sim.NewUUID()[:8]
		}
		if inst.Tags == nil {
			inst.Tags = &ComputeInstanceTags{}
		}
		if inst.Tags.Fingerprint == "" {
			inst.Tags.Fingerprint = sim.NewUUID()[:8]
		}
		if inst.Metadata == nil {
			inst.Metadata = &ComputeInstanceMetadata{}
		}
		inst.Metadata.Kind = "compute#metadata"
		if inst.Metadata.Fingerprint == "" {
			inst.Metadata.Fingerprint = sim.NewUUID()[:8]
		}
		for i := range inst.Disks {
			if inst.Disks[i].Kind == "" {
				inst.Disks[i].Kind = "compute#attachedDisk"
			}
			if inst.Disks[i].Mode == "" {
				inst.Disks[i].Mode = "READ_WRITE"
			}
			if inst.Disks[i].Type == "" {
				inst.Disks[i].Type = "PERSISTENT"
			}
			if inst.Disks[i].Interface == "" {
				inst.Disks[i].Interface = "SCSI"
			}
			if inst.Disks[i].DeviceName == "" {
				inst.Disks[i].DeviceName = inst.Name
			}
			if inst.Disks[i].Source == "" {
				inst.Disks[i].Source = fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, inst.Disks[i].DeviceName)
			}
			inst.Disks[i].Index = int64(i)
		}
		if len(inst.Disks) == 0 {
			inst.Disks = []ComputeInstanceDisk{{
				Kind:       "compute#attachedDisk",
				Type:       "PERSISTENT",
				Mode:       "READ_WRITE",
				Source:     fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, inst.Name),
				DeviceName: inst.Name,
				Index:      0,
				Boot:       true,
				AutoDelete: true,
				Interface:  "SCSI",
			}}
		}
		if err := gcpRefuseUnbootableImage(*inst); err != nil {
			return err
		}
		for i := range inst.NetworkInterfaces {
			if inst.NetworkInterfaces[i].Kind == "" {
				inst.NetworkInterfaces[i].Kind = "compute#networkInterface"
			}
			if inst.NetworkInterfaces[i].Name == "" {
				inst.NetworkInterfaces[i].Name = fmt.Sprintf("nic%d", i)
			}
			if inst.NetworkInterfaces[i].Network == "" && inst.NetworkInterfaces[i].Subnetwork != "" {
				// An interface naming a subnetwork alone is on that subnetwork's
				// network; the default network is for one naming neither.
				subnetLink := normalizeComputeSubnetworkRef(project, regionFromZone(zone), inst.NetworkInterfaces[i].Subnetwork)
				if subnet, ok := subnetworks.Get(subnetLink); ok && subnet.Network != "" {
					inst.NetworkInterfaces[i].Network = normalizeComputeGlobalNetworkRef(project, subnet.Network)
				}
			}
			if inst.NetworkInterfaces[i].Network == "" {
				inst.NetworkInterfaces[i].Network = fmt.Sprintf("projects/%s/global/networks/default", project)
			} else {
				inst.NetworkInterfaces[i].Network = normalizeComputeGlobalNetworkRef(project, inst.NetworkInterfaces[i].Network)
			}
			if inst.NetworkInterfaces[i].Subnetwork == "" {
				subnetLink, err := ensureGCPAutoModeSubnetwork(ctx, networks, subnetworks, project, regionFromZone(zone), inst.NetworkInterfaces[i].Network)
				if err != nil {
					return err
				}
				inst.NetworkInterfaces[i].Subnetwork = subnetLink
			} else {
				inst.NetworkInterfaces[i].Subnetwork = normalizeComputeSubnetworkRef(project, regionFromZone(zone), inst.NetworkInterfaces[i].Subnetwork)
			}
			if inst.NetworkInterfaces[i].Subnetwork == "" {
				return fmt.Errorf("network interface %s requires a subnetwork to lease its address from", inst.NetworkInterfaces[i].Name)
			}
			if err := gcpLeaseInstanceAddress(inst, i); err != nil {
				return err
			}
			if inst.NetworkInterfaces[i].StackType == "" {
				inst.NetworkInterfaces[i].StackType = "IPV4_ONLY"
			}
			if inst.NetworkInterfaces[i].Fingerprint == "" {
				inst.NetworkInterfaces[i].Fingerprint = sim.NewUUID()[:8]
			}
			for j := range inst.NetworkInterfaces[i].AccessConfigs {
				if inst.NetworkInterfaces[i].AccessConfigs[j].Kind == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Kind = "compute#accessConfig"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].Name == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Name = "External NAT"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].Type == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].Type = "ONE_TO_ONE_NAT"
				}
				if inst.NetworkInterfaces[i].AccessConfigs[j].NetworkTier == "" {
					inst.NetworkInterfaces[i].AccessConfigs[j].NetworkTier = "PREMIUM"
				}
				// An access config that names no address gets an ephemeral one,
				// which the instance holds until it is deleted.
				if inst.NetworkInterfaces[i].AccessConfigs[j].NatIP == "" && inst.NetworkInterfaces[i].AccessConfigs[j].Type == "ONE_TO_ONE_NAT" {
					ip, err := ipam.ReserveGCPPublicIPv4(fmt.Sprintf("%s/networkInterfaces/%d/accessConfigs/%d", inst.SelfLink, i, j), nil)
					if err != nil {
						return fmt.Errorf("reserve an ephemeral external address: %w", err)
					}
					inst.NetworkInterfaces[i].AccessConfigs[j].NatIP = ip.String()
				}
			}
		}
		if len(inst.NetworkInterfaces) == 0 {
			return computeFieldRefusal{reason: "required", message: "Required field 'resource.networkInterfaces' not specified"}
		}
		gcpRecordInstanceDisks(project, zone, inst)
		return nil
	}
	gcpNormalizeInstance = normalizeInstance

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		var inst ComputeInstance
		if err := sim.ReadJSON(r, &inst); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, inst.Name) {
			return
		}
		if _, exists := instances.Get(instanceSelfLink(project, zone, inst.Name)); computeConflict(w, exists, "instance", inst.Name) {
			return
		}
		if err := normalizeInstance(r.Context(), project, zone, &inst); err != nil {
			computeWriteNormalizeRefusal(w, err)
			return
		}

		// Compute Engine records the instance PROVISIONING, answers a RUNNING zone
		// operation and boots the machine behind it, on a context of its own: the
		// request's context dies with the response, and a client that stops waiting
		// must not take the machine down.
		inst.Status = ComputeInstanceProvisioning
		instances.Put(inst.SelfLink, inst)

		op := newComputeOpRecord(project, "zones/"+zone, inst.SelfLink, "insert")
		recordComputeOp(op)

		booting := inst
		bg.Go(func() {
			ctx := context.WithoutCancel(r.Context())
			err := gcpBootVM(ctx, &booting)
			if errors.Is(err, machine.ErrHalted) {
				// A stop that arrived while the instance booted owns its
				// status now.
				computeOpFinish(op.Name, nil)
				return
			}
			if err != nil {
				logger.Error().
					Err(err).
					Str("project", project).
					Str("zone", zone).
					Str("instance", booting.Name).
					Msg("failed to boot Compute Engine instance")
				// An insert whose machine never came up leaves no instance
				// behind, and the verdict lives on the operation.
				if err := gcpDestroyVM(context.WithoutCancel(ctx), &booting); err != nil {
					logger.Error().Err(err).Str("instance", booting.SelfLink).
						Msg("failed to remove the machine of a Compute Engine instance that never came up")
				}
				instances.Delete(booting.SelfLink)
				gcpReleaseInstanceAddresses(booting)
				gcpDeleteInstanceDisks(booting)
				computeOpFinish(op.Name, err)
				return
			}
			booting.Status = ComputeInstanceRunning
			instances.Put(booting.SelfLink, booting)
			computeOpFinish(op.Name, nil)
		})

		sim.WriteJSON(w, http.StatusOK, computeOpJSON(op))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		inst, ok := instances.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		sim.WriteJSON(w, http.StatusOK, inst)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		prefix := fmt.Sprintf("projects/%s/zones/%s/instances/", project, zone)
		computeWriteScopedList(w, r, instances, "compute#instanceList", prefix, func(v ComputeInstance) string { return v.SelfLink }, func(v ComputeInstance) string { return v.Name })
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/instances", func(w http.ResponseWriter, r *http.Request) {
		computeWriteZonalAggregate(w, instances, sim.PathParam(r, "project"), "compute#instanceAggregatedList", "instances", func(v ComputeInstance) string { return v.SelfLink })
	})

	registerComputeInstancePower(srv)

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := instances.Update(selfLink, func(inst *ComputeInstance) {
			if !fingerprintMatches(inst.LabelFingerprint, req.LabelFingerprint) {
				conflict = true
				return
			}
			inst.Labels = req.Labels
			inst.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "labelFingerprint mismatch; the resource was modified concurrently")
			return
		}
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setLabels"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/zones/{zone}/instances/{name}/setTags", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		zone := sim.PathParam(r, "zone")
		name := sim.PathParam(r, "name")
		selfLink := instanceSelfLink(project, zone, name)
		var req ComputeInstanceTags
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		var conflict bool
		ok := instances.Update(selfLink, func(inst *ComputeInstance) {
			current := ""
			if inst.Tags != nil {
				current = inst.Tags.Fingerprint
			}
			if !fingerprintMatches(current, req.Fingerprint) {
				conflict = true
				return
			}
			inst.Tags = &req
			inst.Tags.Fingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found in zone %q", name, zone)
			return
		}
		if conflict {
			GCPErrorf(w, http.StatusPreconditionFailed, "conditionNotMet", "tags fingerprint mismatch; the resource was modified concurrently")
			return
		}
		gcpReapplyIngress()
		sim.WriteJSON(w, http.StatusOK, computeZoneOp(project, zone, selfLink, "setTags"))
	})
}
