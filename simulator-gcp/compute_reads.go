package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Compute Engine reads that answer from what the project holds, and their
// writes: a region's zones, a family's image, a storage pool's disks, usable
// subnetworks and a snapshot's key.

func registerComputeReads(srv *sim.Server) {
	// A zone's name is its region's plus a letter, as the simulator's zone
	// catalogue builds them, so a region's zones derive from the catalogue.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/zones",
		func(w http.ResponseWriter, r *http.Request) {
			project, region := sim.PathParam(r, "project"), sim.PathParam(r, "region")
			items := []any{}
			for _, suffix := range []string{"a", "b", "c"} {
				zone := region + "-" + suffix
				items = append(items, map[string]any{
					"kind":     "compute#zone",
					"id":       computeNumericID(),
					"name":     zone,
					"status":   "UP",
					"region":   computeSelfLink(fmt.Sprintf("projects/%s/regions/%s", project, region)),
					"selfLink": computeSelfLink(fmt.Sprintf("projects/%s/zones/%s", project, zone)),
				})
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#zoneList", "items": items,
			})
		})

	// The family view answers what images.getFromFamily would.
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/imageFamilyViews/{family}",
		func(w http.ResponseWriter, r *http.Request) {
			project, family := sim.PathParam(r, "project"), sim.PathParam(r, "family")
			image, ok := gcpImageFromFamily(project, family)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s' was not found", gcpImageLink(project, "family/"+family))
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"image": image})
		})

	// A pool's disks are the disks naming it, derived rather than kept as a second
	// list.
	srv.HandleFunc("GET /compute/v1/projects/{project}/zones/{zone}/storagePools/{storagePool}/listDisks",
		func(w http.ResponseWriter, r *http.Request) {
			project, zone, pool := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "storagePool")
			poolLink := fmt.Sprintf("projects/%s/zones/%s/storagePools/%s", project, zone, pool)
			held := []ComputeDisk{}
			for _, disk := range gcpComputeZoneDisks.List() {
				if disk.StoragePool == "" {
					continue
				}
				if !strings.HasSuffix(disk.StoragePool, poolLink) && disk.StoragePool != pool {
					continue
				}
				held = append(held, disk)
			}
			sort.Slice(held, func(i, j int) bool { return held[i].Name < held[j].Name })
			// The document declares a StoragePoolDisk here, which names the disk and its
			// use of the pool but none of the disk's other members.
			items := make([]any, 0, len(held))
			for _, disk := range held {
				item := map[string]any{
					"disk":              disk.SelfLink,
					"name":              disk.Name,
					"status":            disk.Status,
					"type":              disk.Type,
					"sizeGb":            disk.SizeGb,
					"creationTimestamp": disk.CreationTimestamp,
				}
				if len(disk.Users) > 0 {
					item["attachedInstances"] = disk.Users
				}
				if len(disk.ResourcePolicies) > 0 {
					item["resourcePolicies"] = disk.ResourcePolicies
				}
				items = append(items, item)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#storagePoolListDisks", "items": items,
			})
		})

	// Every subnetwork the project holds is usable: there is no second permission
	// model to narrow it.
	srv.HandleFunc("GET /compute/v1/projects/{project}/aggregated/subnetworks/listUsable",
		func(w http.ResponseWriter, r *http.Request) {
			prefix := "projects/" + sim.PathParam(r, "project") + "/regions/"
			items := []any{}
			for _, subnet := range gcpComputeSubnetworks.List() {
				if !strings.HasPrefix(subnet.SelfLink, prefix) {
					continue
				}
				items = append(items, map[string]any{
					"subnetwork":  computeSelfLink(subnet.SelfLink),
					"network":     computeSelfLink(subnet.Network),
					"ipCidrRange": subnet.IpCidrRange,
				})
			}
			sort.Slice(items, func(i, j int) bool {
				a, _ := items[i].(map[string]any)["subnetwork"].(string)
				b, _ := items[j].(map[string]any)["subnetwork"].(string)
				return a < b
			})
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#usableSubnetworksAggregatedList", "items": items,
			})
		})

	// Re-encrypting a snapshot records the new key's name; the material never
	// comes back.
	updateKmsKey := func(store sim.Store[map[string]any], scope computeScopeKind) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var req struct {
				KmsKeyName string `json:"kmsKeyName"`
			}
			if err := sim.ReadJSON(r, &req); err != nil || req.KmsKeyName == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"updateKmsKey needs the key the snapshot is to be encrypted under")
				return
			}
			key := fmt.Sprintf("projects/%s/%s/snapshots/%s", project, computeScopeSegment(scope, r), name)
			if !store.Update(key, func(m *map[string]any) {
				encryption, _ := (*m)["snapshotEncryptionKey"].(map[string]any)
				if encryption == nil {
					encryption = map[string]any{}
				}
				encryption["kmsKeyName"] = req.KmsKeyName
				(*m)["snapshotEncryptionKey"] = encryption
			}) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "snapshot %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK,
				newComputeOpWithType(project, computeScopeSegment(scope, r), key, "updateKmsKey"))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/snapshots/{name}/updateKmsKey",
		updateKmsKey(gcpComputeSnapshots, cScopeGlobal))
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/snapshots/{name}/updateKmsKey",
		updateKmsKey(gcpComputeRegionSnapshots, cScopeRegion))

	srv.HandleFunc("POST /compute/v1/projects/{project}/global/images/{name}/deprecate",
		func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "name")
			var status map[string]any
			if err := sim.ReadJSON(r, &status); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if state, _ := status["state"].(string); state == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a deprecation needs the state the image is being moved to")
				return
			}
			key := computeGlobalLink(project, "images", name)
			if !gcpComputeImages.Update(key, func(m *map[string]any) { (*m)["deprecated"] = status }) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "image %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "deprecate"))
		})

	// A disk only grows: Compute Engine refuses a shrink, which would lose data.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/disks/{name}/resize",
		func(w http.ResponseWriter, r *http.Request) {
			project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
			var req struct {
				SizeGb int64 `json:"sizeGb,string"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			key := fmt.Sprintf("projects/%s/regions/%s/disks/%s", project, region, name)
			held, ok := gcpComputeRegionDisks.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "disk %q not found", name)
				return
			}
			if req.SizeGb <= int64(computeStoredInt(held["sizeGb"])) {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"a disk can only grow: %d is not larger than the current size", req.SizeGb)
				return
			}
			held["sizeGb"] = fmt.Sprintf("%d", req.SizeGb)
			gcpComputeRegionDisks.Put(key, held)
			sim.WriteJSON(w, http.StatusOK,
				newComputeOpWithType(project, "regions/"+region, key, "resize"))
		})
}
