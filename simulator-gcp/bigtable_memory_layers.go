package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	btadmin "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/e6qu/sockerless-cloud/sim"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// A cluster's memory layer is a singleton under it, disabled until an update
// sets its memory configuration. The REST and gRPC surfaces share the logic
// below and differ only in how they carry a request and an error.

const bigtableMemoryLayerSuffix = "/memoryLayer"

func bigtableMemoryLayerForCluster(clusterName string) bigtableMemoryLayer {
	name := clusterName + bigtableMemoryLayerSuffix
	if layer, ok := bigtableMemoryLayers.Get(name); ok {
		return layer
	}
	layer := bigtableMemoryLayer{
		Name:  name,
		State: "DISABLED",
		Etag:  gcpPolicyETag(),
	}
	bigtableMemoryLayers.Put(name, layer)
	return layer
}

// bigtableMemoryLayerCluster returns the cluster a memory layer name belongs
// to, which must exist.
func bigtableMemoryLayerCluster(name string) (string, error) {
	clusterName, ok := strings.CutSuffix(name, bigtableMemoryLayerSuffix)
	if !ok {
		return "", status.Errorf(codes.InvalidArgument, "memory layer name %q does not end in %q", name, bigtableMemoryLayerSuffix)
	}
	if err := bigtableRequireCluster(clusterName); err != nil {
		return "", err
	}
	return clusterName, nil
}

// bigtableUpdateMemoryLayer enables the memory layer when enable is set and
// disables it otherwise. A non-empty etag must be the layer's current one.
func bigtableUpdateMemoryLayer(name, etag string, maskPaths []string, enable bool) (bigtableMemoryLayer, error) {
	clusterName, err := bigtableMemoryLayerCluster(name)
	if err != nil {
		return bigtableMemoryLayer{}, err
	}
	for _, path := range maskPaths {
		switch strings.TrimSpace(path) {
		case "memoryConfig", "memory_config":
		default:
			return bigtableMemoryLayer{}, status.Errorf(codes.InvalidArgument, "field %q cannot be updated", path)
		}
	}
	layer := bigtableMemoryLayerForCluster(clusterName)
	if etag != "" && etag != layer.Etag {
		return bigtableMemoryLayer{}, status.Error(codes.Aborted, "etag mismatch")
	}
	if enable {
		// storageSizeGib is output-only; Bigtable owns the measured capacity.
		layer.MemoryConfig = &bigtableMemoryConfig{}
		layer.State = "READY"
	} else {
		layer.MemoryConfig = nil
		layer.State = "DISABLED"
	}
	layer.Etag = gcpPolicyETag()
	bigtableMemoryLayers.Put(layer.Name, layer)
	return layer, nil
}

// bigtableListMemoryLayers returns the memory layer of one cluster, or of every
// cluster in the instance when cluster is "-".
func bigtableListMemoryLayers(project, instance, cluster string) ([]bigtableMemoryLayer, error) {
	var clusters []bigtableCluster
	if cluster == "-" {
		instanceName := bigtableInstanceName(project, instance)
		if err := bigtableRequireInstance(instanceName); err != nil {
			return nil, err
		}
		prefix := instanceName + "/clusters/"
		clusters = bigtableClusters.Filter(func(item bigtableCluster) bool {
			return strings.HasPrefix(item.Name, prefix)
		})
	} else {
		name := bigtableClusterName(project, instance, cluster)
		item, ok := bigtableClusters.Get(name)
		if !ok {
			return nil, status.Errorf(codes.NotFound, "cluster %q not found", name)
		}
		clusters = append(clusters, item)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })
	layers := make([]bigtableMemoryLayer, 0, len(clusters))
	for _, item := range clusters {
		layers = append(layers, bigtableMemoryLayerForCluster(item.Name))
	}
	return layers, nil
}

func handleBigtableGetMemoryLayer(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + bigtableMemoryLayerSuffix
	clusterName, err := bigtableMemoryLayerCluster(name)
	if err != nil {
		GCPStatusError(w, err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, bigtableMemoryLayerForCluster(clusterName))
}

func handleBigtableUpdateMemoryLayer(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + bigtableMemoryLayerSuffix
	var req bigtableMemoryLayer
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.Name != "" && req.Name != name {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "memory layer name %q does not match request path %q", req.Name, name)
		return
	}
	var mask []string
	if raw := strings.TrimSpace(r.URL.Query().Get("updateMask")); raw != "" {
		mask = strings.Split(raw, ",")
	}
	layer, err := bigtableUpdateMemoryLayer(name, req.Etag, mask, req.MemoryConfig != nil)
	if err != nil {
		GCPStatusError(w, err)
		return
	}
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), layer, "type.googleapis.com/google.bigtable.admin.v2.MemoryLayer")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListMemoryLayers(w http.ResponseWriter, r *http.Request) {
	layers, err := bigtableListMemoryLayers(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	if err != nil {
		GCPStatusError(w, err)
		return
	}
	page, next, ok := paginateList(w, r, layers)
	if !ok {
		return
	}
	resp := map[string]any{"memoryLayers": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func bigtableMemoryLayerToPB(layer bigtableMemoryLayer) *btadmin.MemoryLayer {
	out := &btadmin.MemoryLayer{
		Name:  layer.Name,
		Etag:  layer.Etag,
		State: btadmin.MemoryLayer_State(btadmin.MemoryLayer_State_value[layer.State]),
	}
	if layer.MemoryConfig != nil {
		out.MemoryConfig = &btadmin.MemoryLayer_MemoryConfig{StorageSizeGib: int32(layer.MemoryConfig.StorageSizeGiB)} // #nosec G115 -- a size the simulator never sets past zero
	}
	return out
}

func (s *bigtableInstanceAdminGRPC) GetMemoryLayer(_ context.Context, req *btadmin.GetMemoryLayerRequest) (*btadmin.MemoryLayer, error) {
	clusterName, err := bigtableMemoryLayerCluster(req.GetName())
	if err != nil {
		return nil, err
	}
	return bigtableMemoryLayerToPB(bigtableMemoryLayerForCluster(clusterName)), nil
}

func (s *bigtableInstanceAdminGRPC) UpdateMemoryLayer(_ context.Context, req *btadmin.UpdateMemoryLayerRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
	update := req.GetMemoryLayer()
	if update.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "memory_layer.name is required")
	}
	layer, err := bigtableUpdateMemoryLayer(update.GetName(), update.GetEtag(), req.GetUpdateMask().GetPaths(), update.GetMemoryConfig() != nil)
	if err != nil {
		return nil, err
	}
	metadata := &btadmin.UpdateMemoryLayerMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	}
	return bigtableDoneOperationWithMetadata(layer.Name, bigtableMemoryLayerToPB(layer), metadata)
}

func (s *bigtableInstanceAdminGRPC) ListMemoryLayers(_ context.Context, req *btadmin.ListMemoryLayersRequest) (*btadmin.ListMemoryLayersResponse, error) {
	project, instance, cluster, err := bigtableClusterParts(req.GetParent())
	if err != nil {
		return nil, err
	}
	layers, err := bigtableListMemoryLayers(project, instance, cluster)
	if err != nil {
		return nil, err
	}
	page, next, err := bigtableGRPCPage(layers, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	resp := &btadmin.ListMemoryLayersResponse{MemoryLayers: make([]*btadmin.MemoryLayer, 0, len(page)), NextPageToken: next}
	for _, layer := range page {
		resp.MemoryLayers = append(resp.MemoryLayers, bigtableMemoryLayerToPB(layer))
	}
	return resp, nil
}
