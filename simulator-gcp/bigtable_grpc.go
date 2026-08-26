package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	btadmin "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// grpcLongRunningOperations holds every long-running operation the gRPC
// services return, whichever service returned it. The server mounts one
// google.longrunning.Operations service, so an operation name it hands a
// client has to resolve there — a Cloud KMS delete's operation as much as a
// Cloud Bigtable create's.
var grpcLongRunningOperations = struct {
	sync.Mutex
	items map[string]*longrunningpb.Operation
}{items: map[string]*longrunningpb.Operation{}}

// grpcRecordOperation files a completed operation so GetOperation,
// ListOperations and CancelOperation can find it by name.
func grpcRecordOperation(op *longrunningpb.Operation) {
	grpcLongRunningOperations.Lock()
	grpcLongRunningOperations.items[op.Name] = op
	grpcLongRunningOperations.Unlock()
}

type bigtableInstanceAdminGRPC struct {
	btadmin.UnimplementedBigtableInstanceAdminServer
}

type bigtableTableAdminGRPC struct {
	btadmin.UnimplementedBigtableTableAdminServer
}

type grpcOperationsService struct {
	longrunningpb.UnimplementedOperationsServer
}

func registerBigtableGRPC(gs *grpc.Server) {
	btadmin.RegisterBigtableInstanceAdminServer(gs, &bigtableInstanceAdminGRPC{})
	btadmin.RegisterBigtableTableAdminServer(gs, &bigtableTableAdminGRPC{})
	longrunningpb.RegisterOperationsServer(gs, &grpcOperationsService{})
}

func (s *bigtableInstanceAdminGRPC) CreateInstance(_ context.Context, req *btadmin.CreateInstanceRequest) (*longrunningpb.Operation, error) {
	project, err := bigtableProjectFromParent(req.GetParent())
	if err != nil {
		return nil, err
	}
	instanceID := req.GetInstanceId()
	if instanceID == "" {
		return nil, status.Error(codes.InvalidArgument, "instance_id is required")
	}
	name := bigtableInstanceName(project, instanceID)
	if _, ok := bigtableInstances.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "instance %q already exists", name)
	}

	inst := req.GetInstance()
	stored := bigtableInstance{
		Name:        name,
		DisplayName: instanceID,
		State:       "READY",
		Type:        "PRODUCTION",
	}
	if inst != nil {
		stored.DisplayName = inst.GetDisplayName()
		if stored.DisplayName == "" {
			stored.DisplayName = instanceID
		}
		stored.Type = bigtableInstanceTypeName(inst.GetType())
		stored.Labels = cloneStringMap(inst.GetLabels())
	}
	bigtableInstances.Put(name, stored)

	for clusterID, cluster := range req.GetClusters() {
		clusterName := bigtableClusterName(project, instanceID, clusterID)
		serveNodes := int(cluster.GetServeNodes())
		if serveNodes == 0 {
			serveNodes = 1
		}
		location := cluster.GetLocation()
		if location == "" {
			location = fmt.Sprintf("projects/%s/locations/us-central1-a", project)
		}
		storageType := bigtableStorageTypeName(cluster.GetDefaultStorageType())
		bigtableClusters.Put(clusterName, bigtableCluster{
			Name:               clusterName,
			Location:           location,
			State:              "READY",
			ServeNodes:         serveNodes,
			DefaultStorageType: storageType,
		})
	}

	return bigtableDoneOperation(stored.Name, bigtableInstanceToPB(stored))
}

func (s *bigtableInstanceAdminGRPC) GetInstance(_ context.Context, req *btadmin.GetInstanceRequest) (*btadmin.Instance, error) {
	inst, ok := bigtableInstances.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "instance %q not found", req.GetName())
	}
	return bigtableInstanceToPB(inst), nil
}

func (s *bigtableInstanceAdminGRPC) ListInstances(_ context.Context, req *btadmin.ListInstancesRequest) (*btadmin.ListInstancesResponse, error) {
	prefix := req.GetParent() + "/instances/"
	out := bigtableInstances.Filter(func(inst bigtableInstance) bool { return strings.HasPrefix(inst.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	resp := &btadmin.ListInstancesResponse{Instances: make([]*btadmin.Instance, 0, len(out))}
	for _, inst := range out {
		resp.Instances = append(resp.Instances, bigtableInstanceToPB(inst))
	}
	return resp, nil
}

func (s *bigtableInstanceAdminGRPC) DeleteInstance(_ context.Context, req *btadmin.DeleteInstanceRequest) (*emptypb.Empty, error) {
	name := req.GetName()
	if !bigtableInstances.Delete(name) {
		return nil, status.Errorf(codes.NotFound, "instance %q not found", name)
	}
	for _, cluster := range bigtableClusters.List() {
		if strings.HasPrefix(cluster.Name, name+"/clusters/") {
			bigtableClusters.Delete(cluster.Name)
		}
	}
	for _, table := range bigtableTables.List() {
		if strings.HasPrefix(table.Name, name+"/tables/") {
			bigtableTables.Delete(table.Name)
			btDeleteTableData(table.Name)
		}
	}
	return &emptypb.Empty{}, nil
}

func (s *bigtableInstanceAdminGRPC) CreateCluster(_ context.Context, req *btadmin.CreateClusterRequest) (*longrunningpb.Operation, error) {
	project, instance, err := bigtableInstanceParts(req.GetParent())
	if err != nil {
		return nil, err
	}
	if _, ok := bigtableInstances.Get(req.GetParent()); !ok {
		return nil, status.Errorf(codes.NotFound, "instance %q not found", req.GetParent())
	}
	clusterID := req.GetClusterId()
	if clusterID == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id is required")
	}
	name := bigtableClusterName(project, instance, clusterID)
	if _, ok := bigtableClusters.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "cluster %q already exists", name)
	}
	cluster := req.GetCluster()
	stored := bigtableCluster{
		Name:               name,
		Location:           cluster.GetLocation(),
		State:              "READY",
		ServeNodes:         int(cluster.GetServeNodes()),
		DefaultStorageType: bigtableStorageTypeName(cluster.GetDefaultStorageType()),
	}
	if stored.Location == "" {
		stored.Location = fmt.Sprintf("projects/%s/locations/us-central1-a", project)
	}
	if stored.ServeNodes == 0 {
		stored.ServeNodes = 1
	}
	bigtableClusters.Put(name, stored)
	return bigtableDoneOperation(name, bigtableClusterToPB(stored))
}

func (s *bigtableInstanceAdminGRPC) GetCluster(_ context.Context, req *btadmin.GetClusterRequest) (*btadmin.Cluster, error) {
	cluster, ok := bigtableClusters.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", req.GetName())
	}
	return bigtableClusterToPB(cluster), nil
}

func (s *bigtableInstanceAdminGRPC) ListClusters(_ context.Context, req *btadmin.ListClustersRequest) (*btadmin.ListClustersResponse, error) {
	prefix := req.GetParent() + "/clusters/"
	out := bigtableClusters.Filter(func(cluster bigtableCluster) bool { return strings.HasPrefix(cluster.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	resp := &btadmin.ListClustersResponse{Clusters: make([]*btadmin.Cluster, 0, len(out))}
	for _, cluster := range out {
		resp.Clusters = append(resp.Clusters, bigtableClusterToPB(cluster))
	}
	return resp, nil
}

func (s *bigtableInstanceAdminGRPC) DeleteCluster(_ context.Context, req *btadmin.DeleteClusterRequest) (*emptypb.Empty, error) {
	if !bigtableClusters.Delete(req.GetName()) {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", req.GetName())
	}
	return &emptypb.Empty{}, nil
}

func (s *bigtableTableAdminGRPC) CreateTable(_ context.Context, req *btadmin.CreateTableRequest) (*btadmin.Table, error) {
	project, instance, err := bigtableInstanceParts(req.GetParent())
	if err != nil {
		return nil, err
	}
	if _, ok := bigtableInstances.Get(req.GetParent()); !ok {
		return nil, status.Errorf(codes.NotFound, "instance %q not found", req.GetParent())
	}
	tableID := req.GetTableId()
	if tableID == "" {
		return nil, status.Error(codes.InvalidArgument, "table_id is required")
	}
	name := bigtableTableName(project, instance, tableID)
	if _, ok := bigtableTables.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "table %q already exists", name)
	}
	table := bigtableTableFromPB(name, req.GetTable())
	bigtableTables.Put(name, table)
	return bigtableTableToPB(table), nil
}

func (s *bigtableTableAdminGRPC) ListTables(_ context.Context, req *btadmin.ListTablesRequest) (*btadmin.ListTablesResponse, error) {
	prefix := req.GetParent() + "/tables/"
	out := bigtableTables.Filter(func(table bigtableTable) bool { return strings.HasPrefix(table.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	resp := &btadmin.ListTablesResponse{Tables: make([]*btadmin.Table, 0, len(out))}
	for _, table := range out {
		resp.Tables = append(resp.Tables, bigtableTableToPB(table))
	}
	return resp, nil
}

func (s *bigtableTableAdminGRPC) GetTable(_ context.Context, req *btadmin.GetTableRequest) (*btadmin.Table, error) {
	table, ok := bigtableTables.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "table %q not found", req.GetName())
	}
	return bigtableTableToPB(table), nil
}

func (s *bigtableTableAdminGRPC) DeleteTable(_ context.Context, req *btadmin.DeleteTableRequest) (*emptypb.Empty, error) {
	if !bigtableTables.Delete(req.GetName()) {
		return nil, status.Errorf(codes.NotFound, "table %q not found", req.GetName())
	}
	btDeleteTableData(req.GetName())
	return &emptypb.Empty{}, nil
}

func (s *bigtableTableAdminGRPC) ModifyColumnFamilies(_ context.Context, req *btadmin.ModifyColumnFamiliesRequest) (*btadmin.Table, error) {
	table, ok := bigtableTables.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "table %q not found", req.GetName())
	}
	if table.ColumnFamilies == nil {
		table.ColumnFamilies = map[string]map[string]any{}
	}
	for _, mod := range req.GetModifications() {
		id := mod.GetId()
		if id == "" {
			return nil, status.Error(codes.InvalidArgument, "column family id is required")
		}
		switch {
		case mod.GetCreate() != nil:
			if _, exists := table.ColumnFamilies[id]; exists {
				return nil, status.Errorf(codes.AlreadyExists, "column family %q already exists", id)
			}
			table.ColumnFamilies[id] = map[string]any{}
		case mod.GetUpdate() != nil:
			if _, exists := table.ColumnFamilies[id]; !exists {
				return nil, status.Errorf(codes.NotFound, "column family %q not found", id)
			}
			table.ColumnFamilies[id] = map[string]any{}
		case mod.GetDrop():
			if _, exists := table.ColumnFamilies[id]; !exists {
				return nil, status.Errorf(codes.NotFound, "column family %q not found", id)
			}
			delete(table.ColumnFamilies, id)
		default:
			return nil, status.Error(codes.InvalidArgument, "column family modification is required")
		}
	}
	bigtableTables.Put(table.Name, table)
	return bigtableTableToPB(table), nil
}

func (s *grpcOperationsService) GetOperation(_ context.Context, req *longrunningpb.GetOperationRequest) (*longrunningpb.Operation, error) {
	grpcLongRunningOperations.Lock()
	defer grpcLongRunningOperations.Unlock()
	op, ok := grpcLongRunningOperations.items[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	return op, nil
}

func (s *grpcOperationsService) WaitOperation(ctx context.Context, req *longrunningpb.WaitOperationRequest) (*longrunningpb.Operation, error) {
	return s.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: req.GetName()})
}

func (s *grpcOperationsService) DeleteOperation(_ context.Context, req *longrunningpb.DeleteOperationRequest) (*emptypb.Empty, error) {
	grpcLongRunningOperations.Lock()
	defer grpcLongRunningOperations.Unlock()
	delete(grpcLongRunningOperations.items, req.GetName())
	return &emptypb.Empty{}, nil
}

// ListOperations returns the operations under a parent resource. Every
// operation name this service issues is the resource's own name followed by
// "/operations/<id>", so the parent is a prefix of its operations' names.
func (s *grpcOperationsService) ListOperations(_ context.Context, req *longrunningpb.ListOperationsRequest) (*longrunningpb.ListOperationsResponse, error) {
	if req.GetFilter() != "" {
		return nil, status.Error(codes.Unimplemented, "the operations list filter is not supported")
	}
	prefix := req.GetName()
	grpcLongRunningOperations.Lock()
	matched := make([]*longrunningpb.Operation, 0, len(grpcLongRunningOperations.items))
	for name, op := range grpcLongRunningOperations.items {
		if prefix == "" || strings.HasPrefix(name, prefix) {
			matched = append(matched, op)
		}
	}
	grpcLongRunningOperations.Unlock()
	sort.Slice(matched, func(i, j int) bool { return matched[i].GetName() < matched[j].GetName() })
	page, next := bigtableGRPCPage(matched, req.GetPageSize(), req.GetPageToken())
	return &longrunningpb.ListOperationsResponse{Operations: page, NextPageToken: next}, nil
}

// CancelOperation asks the service to stop an operation. Every Cloud Bigtable
// admin operation this service issues is already complete when the caller
// receives it, so there is never work left to interrupt: a known operation is
// acknowledged and keeps its result, and an unknown name is a NotFound.
func (s *grpcOperationsService) CancelOperation(_ context.Context, req *longrunningpb.CancelOperationRequest) (*emptypb.Empty, error) {
	grpcLongRunningOperations.Lock()
	defer grpcLongRunningOperations.Unlock()
	if _, ok := grpcLongRunningOperations.items[req.GetName()]; !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	return &emptypb.Empty{}, nil
}

func bigtableDoneOperation(resourceName string, resource proto.Message) (*longrunningpb.Operation, error) {
	resp, err := anypb.New(resource)
	if err != nil {
		return nil, err
	}
	op := &longrunningpb.Operation{
		Name: fmt.Sprintf("%s/operations/%d", resourceName, time.Now().UnixNano()),
		Done: true,
		Result: &longrunningpb.Operation_Response{
			Response: resp,
		},
	}
	grpcRecordOperation(op)
	return op, nil
}

func bigtableProjectFromParent(parent string) (string, error) {
	parts := strings.Split(parent, "/")
	if len(parts) != 2 || parts[0] != "projects" || parts[1] == "" {
		return "", status.Errorf(codes.InvalidArgument, "invalid parent %q", parent)
	}
	return parts[1], nil
}

func bigtableInstanceParts(parent string) (string, string, error) {
	parts := strings.Split(parent, "/")
	if len(parts) != 4 || parts[0] != "projects" || parts[2] != "instances" || parts[1] == "" || parts[3] == "" {
		return "", "", status.Errorf(codes.InvalidArgument, "invalid instance name %q", parent)
	}
	return parts[1], parts[3], nil
}

func bigtableInstanceToPB(inst bigtableInstance) *btadmin.Instance {
	return &btadmin.Instance{
		Name:        inst.Name,
		DisplayName: inst.DisplayName,
		State:       btadmin.Instance_READY,
		Type:        bigtableInstanceTypePB(inst.Type),
		Labels:      cloneStringMap(inst.Labels),
	}
}

func bigtableClusterToPB(cluster bigtableCluster) *btadmin.Cluster {
	return &btadmin.Cluster{
		Name:               cluster.Name,
		Location:           cluster.Location,
		State:              btadmin.Cluster_READY,
		ServeNodes:         int32(cluster.ServeNodes),
		DefaultStorageType: bigtableStorageTypePB(cluster.DefaultStorageType),
	}
}

func bigtableTableToPB(table bigtableTable) *btadmin.Table {
	out := &btadmin.Table{
		Name:               table.Name,
		Granularity:        btadmin.Table_MILLIS,
		DeletionProtection: table.DeletionProtection,
		ColumnFamilies:     map[string]*btadmin.ColumnFamily{},
		ClusterStates:      map[string]*btadmin.Table_ClusterState{},
	}
	for id := range table.ColumnFamilies {
		out.ColumnFamilies[id] = &btadmin.ColumnFamily{}
	}
	for name := range table.ClusterStates {
		clusterID := name[strings.LastIndex(name, "/")+1:]
		out.ClusterStates[clusterID] = &btadmin.Table_ClusterState{
			ReplicationState: btadmin.Table_ClusterState_READY,
		}
	}
	return out
}

func bigtableTableFromPB(name string, table *btadmin.Table) bigtableTable {
	stored := bigtableTable{
		Name:               name,
		Granularity:        "MILLIS",
		DeletionProtection: table.GetDeletionProtection(),
		ColumnFamilies:     map[string]map[string]any{},
		ClusterStates:      map[string]map[string]any{},
	}
	for id := range table.GetColumnFamilies() {
		stored.ColumnFamilies[id] = map[string]any{}
	}
	instancePrefix := name[:strings.LastIndex(name, "/tables/")]
	for _, cluster := range bigtableClusters.List() {
		if strings.HasPrefix(cluster.Name, instancePrefix+"/clusters/") {
			stored.ClusterStates[cluster.Name] = map[string]any{"replicationState": "READY"}
		}
	}
	return stored
}

func bigtableInstanceTypeName(t btadmin.Instance_Type) string {
	switch t {
	case btadmin.Instance_DEVELOPMENT:
		return "DEVELOPMENT"
	default:
		return "PRODUCTION"
	}
}

func bigtableInstanceTypePB(t string) btadmin.Instance_Type {
	if t == "DEVELOPMENT" {
		return btadmin.Instance_DEVELOPMENT
	}
	return btadmin.Instance_PRODUCTION
}

func bigtableStorageTypeName(t btadmin.StorageType) string {
	switch t {
	case btadmin.StorageType_HDD:
		return "HDD"
	default:
		return "SSD"
	}
}

func bigtableStorageTypePB(t string) btadmin.StorageType {
	if t == "HDD" {
		return btadmin.StorageType_HDD
	}
	return btadmin.StorageType_SSD
}

// ---------------------------------------------------------------------------
// Pass-through resource families
//
// App profiles, backups, authorized views, schema bundles and the logical and
// materialized views are held as the JSON body their resource carries, keyed
// by resource name. This door reads and writes those same stores, converting
// through protojson so both spellings of the API observe the same fields under
// the same names.
// ---------------------------------------------------------------------------

// bigtableResourceKind names one pass-through family: the store holding it,
// the label its errors use, and whether the resource carries a server-managed
// etag.
type bigtableResourceKind struct {
	label string
	store sim.Store[bigtableResource]
	etag  bool
}

func bigtableAppProfileKind() bigtableResourceKind {
	return bigtableResourceKind{label: "app profile", store: bigtableAppProfiles, etag: true}
}

func bigtableLogicalViewKind() bigtableResourceKind {
	return bigtableResourceKind{label: "logical view", store: bigtableLogicalView, etag: true}
}

func bigtableMatViewKind() bigtableResourceKind {
	return bigtableResourceKind{label: "materialized view", store: bigtableMatView, etag: true}
}

func bigtableAuthViewKind() bigtableResourceKind {
	return bigtableResourceKind{label: "authorized view", store: bigtableAuthViews, etag: true}
}

func bigtableSchemaBundleKind() bigtableResourceKind {
	return bigtableResourceKind{label: "schema bundle", store: bigtableSchemaBundle, etag: true}
}

// bigtableBackupKind has no etag: the Backup resource declares none.
func bigtableBackupKind() bigtableResourceKind {
	return bigtableResourceKind{label: "backup", store: bigtableBackups, etag: false}
}

// bigtableResourceFromProto renders a resource message into the stored JSON
// shape — protojson field names, which are the names the resource's JSON body
// uses.
func bigtableResourceFromProto(msg proto.Message) (bigtableResource, error) {
	raw, err := protojson.Marshal(msg)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%T cannot be encoded: %v", msg, err)
	}
	body := bigtableResource{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%T cannot be encoded: %v", msg, err)
	}
	return body, nil
}

// bigtableResourceToProto reads a stored resource body into msg. Fields the
// body carries that the message does not declare are dropped, so a body
// written through a spelling of the API that accepts more than the proto does
// still reads back as the resource.
func bigtableResourceToProto(body bigtableResource, msg proto.Message) error {
	raw, err := json.Marshal(map[string]any(body))
	if err != nil {
		return status.Errorf(codes.Internal, "stored resource cannot be encoded: %v", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, msg); err != nil {
		name, _ := body["name"].(string)
		return status.Errorf(codes.Internal, "stored resource %q is not a valid %T: %v", name, msg, err)
	}
	return nil
}

// bigtableStoredProto reads a stored resource body into msg and returns it.
func bigtableStoredProto[T proto.Message](body bigtableResource, msg T) (T, error) {
	if err := bigtableResourceToProto(body, msg); err != nil {
		var zero T
		return zero, err
	}
	return msg, nil
}

// bigtableCreateResource stores a new resource of the family under name,
// stamping the server-managed name and etag, and returns the stored body.
func bigtableCreateResource(kind bigtableResourceKind, name string, msg proto.Message) (bigtableResource, error) {
	if _, exists := kind.store.Get(name); exists {
		return nil, status.Errorf(codes.AlreadyExists, "%s %q already exists", kind.label, name)
	}
	body, err := bigtableResourceFromProto(msg)
	if err != nil {
		return nil, err
	}
	body["name"] = name
	if kind.etag {
		body["etag"] = gcpPolicyETag()
	}
	kind.store.Put(name, body)
	return body, nil
}

// bigtableReadResource loads a stored resource of the family into msg.
func bigtableReadResource[T proto.Message](kind bigtableResourceKind, name string, msg T) (T, error) {
	body, ok := kind.store.Get(name)
	if !ok {
		var zero T
		return zero, status.Errorf(codes.NotFound, "%s %q not found", kind.label, name)
	}
	return bigtableStoredProto(body, msg)
}

// bigtableUpdateResource merges the masked fields of msg into the stored
// resource and returns the stored body. A resource carrying an etag gets a
// fresh one, the way any write to it does.
func bigtableUpdateResource(kind bigtableResourceKind, name string, msg proto.Message, mask *fieldmaskpb.FieldMask) (bigtableResource, error) {
	body, ok := kind.store.Get(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "%s %q not found", kind.label, name)
	}
	patch, err := bigtableResourceFromProto(msg)
	if err != nil {
		return nil, err
	}
	bigtableApplyProtoMask(body, patch, mask, msg.ProtoReflect().Descriptor())
	body["name"] = name
	if kind.etag {
		body["etag"] = gcpPolicyETag()
	}
	kind.store.Put(name, body)
	return body, nil
}

// bigtableDeleteResource removes a stored resource. A caller that supplies an
// etag is deleting the version it read: a stale one is refused with ABORTED,
// which is the status Cloud Bigtable answers a blocked delete with.
func bigtableDeleteResource(kind bigtableResourceKind, name, etag string) error {
	body, ok := kind.store.Get(name)
	if !ok {
		return status.Errorf(codes.NotFound, "%s %q not found", kind.label, name)
	}
	if etag != "" {
		if stored, _ := body["etag"].(string); stored != etag {
			return status.Errorf(codes.Aborted, "%s %q has changed since it was read", kind.label, name)
		}
	}
	kind.store.Delete(name)
	return nil
}

// bigtableListResources returns one page of the family's resources under
// parent, newest message per entry built by newMsg, plus the token that
// continues the listing.
func bigtableListResources[T proto.Message](kind bigtableResourceKind, parent, collection string, pageSize int32, pageToken string, newMsg func() T) ([]T, string, error) {
	bodies := bigtableFilterResources(kind.store, parent+"/"+collection+"/")
	page, next := bigtableGRPCPage(bodies, pageSize, pageToken)
	out := make([]T, 0, len(page))
	for _, body := range page {
		msg, err := bigtableStoredProto(body, newMsg())
		if err != nil {
			return nil, "", err
		}
		out = append(out, msg)
	}
	return out, next, nil
}

// bigtableApplyProtoMask merges the masked fields of patch into body. A field
// the mask names but the message leaves unset is cleared, which is what a
// field mask asks for. Setting one member of a oneof clears its siblings, so a
// routing policy replaced by an update does not leave both spellings behind.
func bigtableApplyProtoMask(body, patch bigtableResource, mask *fieldmaskpb.FieldMask, md protoreflect.MessageDescriptor) {
	applied := map[string]any{}
	paths := mask.GetPaths()
	wildcard := len(paths) == 1 && paths[0] == "*"
	if len(paths) == 0 || wildcard {
		for key, value := range patch {
			if key == "name" || key == "etag" {
				continue
			}
			applied[key] = value
		}
	} else {
		for _, path := range paths {
			key := bigtableJSONFieldName(md, path)
			if key == "name" || key == "etag" {
				continue
			}
			if value, ok := patch[key]; ok {
				applied[key] = value
			} else {
				delete(body, key)
			}
		}
	}
	for i := 0; i < md.Oneofs().Len(); i++ {
		oneof := md.Oneofs().Get(i)
		if oneof.IsSynthetic() {
			continue
		}
		members := oneof.Fields()
		replaced := false
		for j := 0; j < members.Len(); j++ {
			if _, ok := applied[members.Get(j).JSONName()]; ok {
				replaced = true
				break
			}
		}
		if !replaced {
			continue
		}
		for j := 0; j < members.Len(); j++ {
			key := members.Get(j).JSONName()
			if _, ok := applied[key]; !ok {
				delete(body, key)
			}
		}
	}
	for key, value := range applied {
		body[key] = value
	}
}

// bigtableJSONFieldName maps a field-mask path onto the JSON key the stored
// resource uses. Only the leading segment is resolved: a nested path addresses
// a field inside a value this store holds whole.
func bigtableJSONFieldName(md protoreflect.MessageDescriptor, path string) string {
	root := path
	if i := strings.IndexByte(path, '.'); i >= 0 {
		root = path[:i]
	}
	if fd := md.Fields().ByName(protoreflect.Name(root)); fd != nil {
		return fd.JSONName()
	}
	if fd := md.Fields().ByJSONName(root); fd != nil {
		return fd.JSONName()
	}
	return root
}

// bigtableGRPCPage slices a sorted list onto the requested page and returns the
// token that continues it.
func bigtableGRPCPage[T any](items []T, pageSize int32, pageToken string) ([]T, string) {
	start, end := psPaging(len(items), pageSize, pageToken)
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next
}

// ---------------------------------------------------------------------------
// Parent resolution
// ---------------------------------------------------------------------------

func bigtableRequireInstance(name string) error {
	if _, ok := bigtableInstances.Get(name); !ok {
		return status.Errorf(codes.NotFound, "instance %q not found", name)
	}
	return nil
}

func bigtableRequireCluster(name string) error {
	if _, ok := bigtableClusters.Get(name); !ok {
		return status.Errorf(codes.NotFound, "cluster %q not found", name)
	}
	return nil
}

func bigtableRequireTable(name string) (bigtableTable, error) {
	table, ok := bigtableTables.Get(name)
	if !ok {
		return bigtableTable{}, status.Errorf(codes.NotFound, "table %q not found", name)
	}
	return table, nil
}

// bigtableInstanceChild builds an instance-scoped child's resource name, after
// checking that the instance exists and the caller supplied an id.
func bigtableInstanceChild(parent, collection, id, idField string) (string, error) {
	if err := bigtableRequireInstance(parent); err != nil {
		return "", err
	}
	if id == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", idField)
	}
	return parent + "/" + collection + "/" + id, nil
}

// bigtableClusterChild builds a cluster-scoped child's resource name.
func bigtableClusterChild(parent, collection, id, idField string) (string, error) {
	if err := bigtableRequireCluster(parent); err != nil {
		return "", err
	}
	if id == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", idField)
	}
	return parent + "/" + collection + "/" + id, nil
}

// bigtableTableChild builds a table-scoped child's resource name.
func bigtableTableChild(parent, collection, id, idField string) (string, error) {
	if _, err := bigtableRequireTable(parent); err != nil {
		return "", err
	}
	if id == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", idField)
	}
	return parent + "/" + collection + "/" + id, nil
}

// ---------------------------------------------------------------------------
// Instance admin: app profiles
// ---------------------------------------------------------------------------

func (s *bigtableInstanceAdminGRPC) CreateAppProfile(_ context.Context, req *btadmin.CreateAppProfileRequest) (*btadmin.AppProfile, error) {
	name, err := bigtableInstanceChild(req.GetParent(), "appProfiles", req.GetAppProfileId(), "app_profile_id")
	if err != nil {
		return nil, err
	}
	if req.GetAppProfile() == nil {
		return nil, status.Error(codes.InvalidArgument, "app_profile is required")
	}
	body, err := bigtableCreateResource(bigtableAppProfileKind(), name, req.GetAppProfile())
	if err != nil {
		return nil, err
	}
	return bigtableStoredProto(body, &btadmin.AppProfile{})
}

func (s *bigtableInstanceAdminGRPC) GetAppProfile(_ context.Context, req *btadmin.GetAppProfileRequest) (*btadmin.AppProfile, error) {
	return bigtableReadResource(bigtableAppProfileKind(), req.GetName(), &btadmin.AppProfile{})
}

func (s *bigtableInstanceAdminGRPC) ListAppProfiles(_ context.Context, req *btadmin.ListAppProfilesRequest) (*btadmin.ListAppProfilesResponse, error) {
	if err := bigtableRequireInstance(req.GetParent()); err != nil {
		return nil, err
	}
	profiles, next, err := bigtableListResources(bigtableAppProfileKind(), req.GetParent(), "appProfiles",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.AppProfile { return &btadmin.AppProfile{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListAppProfilesResponse{AppProfiles: profiles, NextPageToken: next}, nil
}

func (s *bigtableInstanceAdminGRPC) UpdateAppProfile(_ context.Context, req *btadmin.UpdateAppProfileRequest) (*longrunningpb.Operation, error) {
	profile := req.GetAppProfile()
	if profile.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_profile.name is required")
	}
	body, err := bigtableUpdateResource(bigtableAppProfileKind(), profile.GetName(), profile, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, &btadmin.AppProfile{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(profile.GetName(), updated)
}

func (s *bigtableInstanceAdminGRPC) DeleteAppProfile(_ context.Context, req *btadmin.DeleteAppProfileRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableAppProfileKind(), req.GetName(), ""); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Instance admin: logical views
// ---------------------------------------------------------------------------

func (s *bigtableInstanceAdminGRPC) CreateLogicalView(_ context.Context, req *btadmin.CreateLogicalViewRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableInstanceChild(req.GetParent(), "logicalViews", req.GetLogicalViewId(), "logical_view_id")
	if err != nil {
		return nil, err
	}
	if req.GetLogicalView() == nil {
		return nil, status.Error(codes.InvalidArgument, "logical_view is required")
	}
	body, err := bigtableCreateResource(bigtableLogicalViewKind(), name, req.GetLogicalView())
	if err != nil {
		return nil, err
	}
	created, err := bigtableStoredProto(body, &btadmin.LogicalView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

func (s *bigtableInstanceAdminGRPC) GetLogicalView(_ context.Context, req *btadmin.GetLogicalViewRequest) (*btadmin.LogicalView, error) {
	return bigtableReadResource(bigtableLogicalViewKind(), req.GetName(), &btadmin.LogicalView{})
}

func (s *bigtableInstanceAdminGRPC) ListLogicalViews(_ context.Context, req *btadmin.ListLogicalViewsRequest) (*btadmin.ListLogicalViewsResponse, error) {
	if err := bigtableRequireInstance(req.GetParent()); err != nil {
		return nil, err
	}
	views, next, err := bigtableListResources(bigtableLogicalViewKind(), req.GetParent(), "logicalViews",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.LogicalView { return &btadmin.LogicalView{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListLogicalViewsResponse{LogicalViews: views, NextPageToken: next}, nil
}

func (s *bigtableInstanceAdminGRPC) UpdateLogicalView(_ context.Context, req *btadmin.UpdateLogicalViewRequest) (*longrunningpb.Operation, error) {
	view := req.GetLogicalView()
	if view.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "logical_view.name is required")
	}
	body, err := bigtableUpdateResource(bigtableLogicalViewKind(), view.GetName(), view, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, &btadmin.LogicalView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(view.GetName(), updated)
}

func (s *bigtableInstanceAdminGRPC) DeleteLogicalView(_ context.Context, req *btadmin.DeleteLogicalViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableLogicalViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Instance admin: materialized views
// ---------------------------------------------------------------------------

func (s *bigtableInstanceAdminGRPC) CreateMaterializedView(_ context.Context, req *btadmin.CreateMaterializedViewRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableInstanceChild(req.GetParent(), "materializedViews", req.GetMaterializedViewId(), "materialized_view_id")
	if err != nil {
		return nil, err
	}
	if req.GetMaterializedView() == nil {
		return nil, status.Error(codes.InvalidArgument, "materialized_view is required")
	}
	body, err := bigtableCreateResource(bigtableMatViewKind(), name, req.GetMaterializedView())
	if err != nil {
		return nil, err
	}
	created, err := bigtableStoredProto(body, &btadmin.MaterializedView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

func (s *bigtableInstanceAdminGRPC) GetMaterializedView(_ context.Context, req *btadmin.GetMaterializedViewRequest) (*btadmin.MaterializedView, error) {
	return bigtableReadResource(bigtableMatViewKind(), req.GetName(), &btadmin.MaterializedView{})
}

func (s *bigtableInstanceAdminGRPC) ListMaterializedViews(_ context.Context, req *btadmin.ListMaterializedViewsRequest) (*btadmin.ListMaterializedViewsResponse, error) {
	if err := bigtableRequireInstance(req.GetParent()); err != nil {
		return nil, err
	}
	views, next, err := bigtableListResources(bigtableMatViewKind(), req.GetParent(), "materializedViews",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.MaterializedView { return &btadmin.MaterializedView{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListMaterializedViewsResponse{MaterializedViews: views, NextPageToken: next}, nil
}

func (s *bigtableInstanceAdminGRPC) UpdateMaterializedView(_ context.Context, req *btadmin.UpdateMaterializedViewRequest) (*longrunningpb.Operation, error) {
	view := req.GetMaterializedView()
	if view.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "materialized_view.name is required")
	}
	body, err := bigtableUpdateResource(bigtableMatViewKind(), view.GetName(), view, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, &btadmin.MaterializedView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(view.GetName(), updated)
}

func (s *bigtableInstanceAdminGRPC) DeleteMaterializedView(_ context.Context, req *btadmin.DeleteMaterializedViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableMatViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Instance admin: instance and cluster updates, hot tablets
// ---------------------------------------------------------------------------

// UpdateInstance replaces an instance. Cloud Bigtable answers it
// synchronously, with the instance as stored.
func (s *bigtableInstanceAdminGRPC) UpdateInstance(_ context.Context, req *btadmin.Instance) (*btadmin.Instance, error) {
	if err := bigtableRequireInstance(req.GetName()); err != nil {
		return nil, err
	}
	stored := bigtableInstance{
		Name:        req.GetName(),
		DisplayName: req.GetDisplayName(),
		State:       "READY",
		Type:        bigtableInstanceTypeName(req.GetType()),
		Labels:      cloneStringMap(req.GetLabels()),
	}
	bigtableInstances.Put(stored.Name, stored)
	return bigtableInstanceToPB(stored), nil
}

func (s *bigtableInstanceAdminGRPC) PartialUpdateInstance(_ context.Context, req *btadmin.PartialUpdateInstanceRequest) (*longrunningpb.Operation, error) {
	instance := req.GetInstance()
	if instance.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "instance.name is required")
	}
	stored, ok := bigtableInstances.Get(instance.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "instance %q not found", instance.GetName())
	}
	for _, path := range req.GetUpdateMask().GetPaths() {
		switch path {
		case "display_name", "displayName":
			stored.DisplayName = instance.GetDisplayName()
		case "labels":
			stored.Labels = cloneStringMap(instance.GetLabels())
		case "type":
			stored.Type = bigtableInstanceTypeName(instance.GetType())
		default:
			return nil, bigtableUnsupportedMaskPath(instance.ProtoReflect().Descriptor(), path)
		}
	}
	bigtableInstances.Put(stored.Name, stored)
	return bigtableDoneOperation(stored.Name, bigtableInstanceToPB(stored))
}

// UpdateCluster replaces a cluster. Serve nodes are the mutable part of a
// cluster; its location and storage type are fixed at creation.
func (s *bigtableInstanceAdminGRPC) UpdateCluster(_ context.Context, req *btadmin.Cluster) (*longrunningpb.Operation, error) {
	stored, ok := bigtableClusters.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", req.GetName())
	}
	if req.GetServeNodes() != 0 {
		stored.ServeNodes = int(req.GetServeNodes())
	}
	stored.State = "READY"
	bigtableClusters.Put(stored.Name, stored)
	return bigtableDoneOperation(stored.Name, bigtableClusterToPB(stored))
}

func (s *bigtableInstanceAdminGRPC) PartialUpdateCluster(_ context.Context, req *btadmin.PartialUpdateClusterRequest) (*longrunningpb.Operation, error) {
	cluster := req.GetCluster()
	if cluster.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster.name is required")
	}
	stored, ok := bigtableClusters.Get(cluster.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", cluster.GetName())
	}
	for _, path := range req.GetUpdateMask().GetPaths() {
		switch path {
		case "serve_nodes", "serveNodes":
			stored.ServeNodes = int(cluster.GetServeNodes())
		case "default_storage_type", "defaultStorageType":
			stored.DefaultStorageType = bigtableStorageTypeName(cluster.GetDefaultStorageType())
		default:
			return nil, bigtableUnsupportedMaskPath(cluster.ProtoReflect().Descriptor(), path)
		}
	}
	stored.State = "READY"
	bigtableClusters.Put(stored.Name, stored)
	return bigtableDoneOperation(stored.Name, bigtableClusterToPB(stored))
}

// ListHotTablets reports the tablets a cluster measured as hot over the
// requested window. Nothing in this simulator concentrates load onto a tablet,
// so a healthy cluster reports none.
func (s *bigtableInstanceAdminGRPC) ListHotTablets(_ context.Context, req *btadmin.ListHotTabletsRequest) (*btadmin.ListHotTabletsResponse, error) {
	if err := bigtableRequireCluster(req.GetParent()); err != nil {
		return nil, err
	}
	return &btadmin.ListHotTabletsResponse{HotTablets: []*btadmin.HotTablet{}}, nil
}

// bigtableUnsupportedMaskPath reports the status for a field-mask path the
// stored resource cannot carry. A path that names no field of the message at
// all is a malformed request; one the message declares but this store does not
// keep is an update this door does not perform, and says so rather than
// silently dropping the field.
func bigtableUnsupportedMaskPath(md protoreflect.MessageDescriptor, path string) error {
	root := path
	if i := strings.IndexByte(path, '.'); i >= 0 {
		root = path[:i]
	}
	if md.Fields().ByName(protoreflect.Name(root)) == nil && md.Fields().ByJSONName(root) == nil {
		return status.Errorf(codes.InvalidArgument, "%s has no field %q", md.Name(), path)
	}
	return status.Errorf(codes.Unimplemented, "updating field %q is not supported", path)
}

// ---------------------------------------------------------------------------
// Table admin: table lifecycle beyond create/read/delete
// ---------------------------------------------------------------------------

func (s *bigtableTableAdminGRPC) UpdateTable(_ context.Context, req *btadmin.UpdateTableRequest) (*longrunningpb.Operation, error) {
	update := req.GetTable()
	if update.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "table.name is required")
	}
	table, err := bigtableRequireTable(update.GetName())
	if err != nil {
		return nil, err
	}
	if len(req.GetUpdateMask().GetPaths()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "update_mask is required")
	}
	for _, path := range req.GetUpdateMask().GetPaths() {
		switch path {
		case "deletion_protection", "deletionProtection":
			table.DeletionProtection = update.GetDeletionProtection()
		default:
			return nil, bigtableUnsupportedMaskPath(update.ProtoReflect().Descriptor(), path)
		}
	}
	bigtableTables.Put(table.Name, table)
	return bigtableDoneOperation(table.Name, bigtableTableToPB(table))
}

// UndeleteTable restores a table that DeleteTable removed. DeleteTable drops
// the table and its rows outright here, so a deleted name has nothing left to
// bring back and reports NotFound; a table still present is returned as it
// stands.
func (s *bigtableTableAdminGRPC) UndeleteTable(_ context.Context, req *btadmin.UndeleteTableRequest) (*longrunningpb.Operation, error) {
	table, err := bigtableRequireTable(req.GetName())
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(table.Name, bigtableTableToPB(table))
}

// DropRowRange permanently deletes rows from a table — every row, or those
// under a key prefix. It writes through the same row store the data plane
// reads, so a dropped range is gone from ReadRows as well.
func (s *bigtableTableAdminGRPC) DropRowRange(_ context.Context, req *btadmin.DropRowRangeRequest) (*emptypb.Empty, error) {
	if _, err := bigtableRequireTable(req.GetName()); err != nil {
		return nil, err
	}
	td := bigtableTableData(req.GetName())
	td.mu.Lock()
	defer td.mu.Unlock()
	switch target := req.GetTarget().(type) {
	case *btadmin.DropRowRangeRequest_DeleteAllDataFromTable:
		if !target.DeleteAllDataFromTable {
			return nil, status.Error(codes.InvalidArgument, "delete_all_data_from_table must be true when set")
		}
		td.rows = map[string]map[string]map[string][]btCell{}
	case *btadmin.DropRowRangeRequest_RowKeyPrefix:
		prefix := string(target.RowKeyPrefix)
		if prefix == "" {
			return nil, status.Error(codes.InvalidArgument, "row_key_prefix must not be empty")
		}
		for key := range td.rows {
			if strings.HasPrefix(key, prefix) {
				delete(td.rows, key)
			}
		}
	default:
		return nil, status.Error(codes.InvalidArgument, "one of row_key_prefix or delete_all_data_from_table is required")
	}
	btPersistTableData(req.GetName(), td)
	return &emptypb.Empty{}, nil
}

// GenerateConsistencyToken issues the token a caller then presents to
// CheckConsistency.
func (s *bigtableTableAdminGRPC) GenerateConsistencyToken(_ context.Context, req *btadmin.GenerateConsistencyTokenRequest) (*btadmin.GenerateConsistencyTokenResponse, error) {
	if _, err := bigtableRequireTable(req.GetName()); err != nil {
		return nil, err
	}
	return &btadmin.GenerateConsistencyTokenResponse{ConsistencyToken: generateUUID()}, nil
}

// CheckConsistency reports whether replication has caught up to the point the
// token was issued. This simulator serves a table from one copy, so every
// mutation is visible everywhere the moment it returns and any token a caller
// holds is already satisfied.
func (s *bigtableTableAdminGRPC) CheckConsistency(_ context.Context, req *btadmin.CheckConsistencyRequest) (*btadmin.CheckConsistencyResponse, error) {
	if _, err := bigtableRequireTable(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetConsistencyToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "consistency_token is required")
	}
	return &btadmin.CheckConsistencyResponse{Consistent: true}, nil
}

// ---------------------------------------------------------------------------
// Table admin: backups
// ---------------------------------------------------------------------------

func (s *bigtableTableAdminGRPC) CreateBackup(_ context.Context, req *btadmin.CreateBackupRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableClusterChild(req.GetParent(), "backups", req.GetBackupId(), "backup_id")
	if err != nil {
		return nil, err
	}
	backup := req.GetBackup()
	if backup == nil {
		return nil, status.Error(codes.InvalidArgument, "backup is required")
	}
	if backup.GetSourceTable() == "" {
		return nil, status.Error(codes.InvalidArgument, "backup.source_table is required")
	}
	if _, err := bigtableRequireTable(backup.GetSourceTable()); err != nil {
		return nil, err
	}
	stored := &btadmin.Backup{}
	proto.Merge(stored, backup)
	stored.State = btadmin.Backup_READY
	body, err := bigtableCreateResource(bigtableBackupKind(), name, stored)
	if err != nil {
		return nil, err
	}
	btCaptureTable(name, backup.GetSourceTable())
	created, err := bigtableStoredProto(body, &btadmin.Backup{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

func (s *bigtableTableAdminGRPC) GetBackup(_ context.Context, req *btadmin.GetBackupRequest) (*btadmin.Backup, error) {
	return bigtableReadResource(bigtableBackupKind(), req.GetName(), &btadmin.Backup{})
}

func (s *bigtableTableAdminGRPC) ListBackups(_ context.Context, req *btadmin.ListBackupsRequest) (*btadmin.ListBackupsResponse, error) {
	if err := bigtableRequireCluster(req.GetParent()); err != nil {
		return nil, err
	}
	if req.GetFilter() != "" {
		return nil, status.Error(codes.Unimplemented, "the backups list filter is not supported")
	}
	if req.GetOrderBy() != "" {
		return nil, status.Error(codes.Unimplemented, "the backups list order_by is not supported")
	}
	backups, next, err := bigtableListResources(bigtableBackupKind(), req.GetParent(), "backups",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.Backup { return &btadmin.Backup{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListBackupsResponse{Backups: backups, NextPageToken: next}, nil
}

// UpdateBackup answers synchronously, with the backup as stored.
func (s *bigtableTableAdminGRPC) UpdateBackup(_ context.Context, req *btadmin.UpdateBackupRequest) (*btadmin.Backup, error) {
	backup := req.GetBackup()
	if backup.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "backup.name is required")
	}
	body, err := bigtableUpdateResource(bigtableBackupKind(), backup.GetName(), backup, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	return bigtableStoredProto(body, &btadmin.Backup{})
}

func (s *bigtableTableAdminGRPC) DeleteBackup(_ context.Context, req *btadmin.DeleteBackupRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableBackupKind(), req.GetName(), ""); err != nil {
		return nil, err
	}
	btDeleteCapture(req.GetName())
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Table admin: snapshots
// ---------------------------------------------------------------------------
//
// Snapshots are the older sibling of backups: a copy of a table taken into the
// cluster that serves it, and a table created back out of that copy. The
// bigtableadmin Discovery document declares no snapshots collection, so this
// family exists on the gRPC door alone — which is why these read and write the
// same capture store the backups do rather than a REST handler's state.

func bigtableSnapshotKind() bigtableResourceKind {
	return bigtableResourceKind{label: "snapshot", store: bigtableSnapshots, etag: false}
}

// SnapshotTable copies a table into a snapshot in the named cluster.
func (s *bigtableTableAdminGRPC) SnapshotTable(_ context.Context, req *btadmin.SnapshotTableRequest) (*longrunningpb.Operation, error) {
	table, err := bigtableRequireTable(req.GetName())
	if err != nil {
		return nil, err
	}
	name, err := bigtableClusterChild(req.GetCluster(), "snapshots", req.GetSnapshotId(), "snapshot_id")
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	snapshot := &btadmin.Snapshot{
		Name:        name,
		SourceTable: bigtableTableToPB(table),
		CreateTime:  timestamppb.New(now),
		State:       btadmin.Snapshot_READY,
		Description: req.GetDescription(),
	}
	// A snapshot's ttl is how long it lives; the delete time it implies is what
	// a client reads back.
	if ttl := req.GetTtl(); ttl != nil {
		snapshot.DeleteTime = timestamppb.New(now.Add(ttl.AsDuration()))
	}
	body, err := bigtableCreateResource(bigtableSnapshotKind(), name, snapshot)
	if err != nil {
		return nil, err
	}
	if !btCaptureTable(name, req.GetName()) {
		return nil, status.Errorf(codes.NotFound, "table %q not found", req.GetName())
	}
	created, err := bigtableStoredProto(body, &btadmin.Snapshot{})
	if err != nil {
		return nil, err
	}
	created.DataSizeBytes = int64(btCaptureRowCount(name))
	return bigtableDoneOperation(name, created)
}

func (s *bigtableTableAdminGRPC) GetSnapshot(_ context.Context, req *btadmin.GetSnapshotRequest) (*btadmin.Snapshot, error) {
	return bigtableReadResource(bigtableSnapshotKind(), req.GetName(), &btadmin.Snapshot{})
}

func (s *bigtableTableAdminGRPC) ListSnapshots(_ context.Context, req *btadmin.ListSnapshotsRequest) (*btadmin.ListSnapshotsResponse, error) {
	if err := bigtableRequireCluster(req.GetParent()); err != nil {
		return nil, err
	}
	snapshots, next, err := bigtableListResources(bigtableSnapshotKind(), req.GetParent(), "snapshots",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.Snapshot { return &btadmin.Snapshot{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListSnapshotsResponse{Snapshots: snapshots, NextPageToken: next}, nil
}

func (s *bigtableTableAdminGRPC) DeleteSnapshot(_ context.Context, req *btadmin.DeleteSnapshotRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableSnapshotKind(), req.GetName(), ""); err != nil {
		return nil, err
	}
	btDeleteCapture(req.GetName())
	return &emptypb.Empty{}, nil
}

// CreateTableFromSnapshot creates a table holding the rows its snapshot
// captured.
func (s *bigtableTableAdminGRPC) CreateTableFromSnapshot(_ context.Context, req *btadmin.CreateTableFromSnapshotRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableInstanceChild(req.GetParent(), "tables", req.GetTableId(), "table_id")
	if err != nil {
		return nil, err
	}
	if req.GetSourceSnapshot() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_snapshot is required")
	}
	if _, ok := bigtableSnapshots.Get(req.GetSourceSnapshot()); !ok {
		return nil, status.Errorf(codes.NotFound, "snapshot %q not found", req.GetSourceSnapshot())
	}
	if _, ok := bigtableTables.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "table %q already exists", name)
	}
	table, ok := btRestoreCapture(req.GetSourceSnapshot(), name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "snapshot %q not found", req.GetSourceSnapshot())
	}
	return bigtableDoneOperation(name, bigtableTableToPB(table))
}

// CopyBackup copies a backup into a cluster, carrying the source's table and
// size across and taking a new expiry from the request.
func (s *bigtableTableAdminGRPC) CopyBackup(_ context.Context, req *btadmin.CopyBackupRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableClusterChild(req.GetParent(), "backups", req.GetBackupId(), "backup_id")
	if err != nil {
		return nil, err
	}
	if req.GetSourceBackup() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_backup is required")
	}
	source, err := bigtableReadResource(bigtableBackupKind(), req.GetSourceBackup(), &btadmin.Backup{})
	if err != nil {
		return nil, err
	}
	body, err := bigtableCreateResource(bigtableBackupKind(), name, &btadmin.Backup{
		SourceTable:  source.GetSourceTable(),
		SourceBackup: req.GetSourceBackup(),
		ExpireTime:   req.GetExpireTime(),
		SizeBytes:    source.GetSizeBytes(),
		State:        btadmin.Backup_READY,
	})
	if err != nil {
		return nil, err
	}
	btCopyCapture(name, req.GetSourceBackup())
	created, err := bigtableStoredProto(body, &btadmin.Backup{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

// RestoreTable creates a table from a backup. A backup records the metadata of
// the table it was taken from — this store keeps no copy of the rows — so the
// restored table arrives with the instance's cluster states and no data.
func (s *bigtableTableAdminGRPC) RestoreTable(_ context.Context, req *btadmin.RestoreTableRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableInstanceChild(req.GetParent(), "tables", req.GetTableId(), "table_id")
	if err != nil {
		return nil, err
	}
	if req.GetBackup() == "" {
		return nil, status.Error(codes.InvalidArgument, "backup is required")
	}
	if _, ok := bigtableBackups.Get(req.GetBackup()); !ok {
		return nil, status.Errorf(codes.NotFound, "backup %q not found", req.GetBackup())
	}
	if _, ok := bigtableTables.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "table %q already exists", name)
	}
	table, ok := btRestoreCapture(req.GetBackup(), name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "backup %q not found", req.GetBackup())
	}
	return bigtableDoneOperation(name, bigtableTableToPB(table))
}

// ---------------------------------------------------------------------------
// Table admin: authorized views
// ---------------------------------------------------------------------------

func (s *bigtableTableAdminGRPC) CreateAuthorizedView(_ context.Context, req *btadmin.CreateAuthorizedViewRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableTableChild(req.GetParent(), "authorizedViews", req.GetAuthorizedViewId(), "authorized_view_id")
	if err != nil {
		return nil, err
	}
	if req.GetAuthorizedView() == nil {
		return nil, status.Error(codes.InvalidArgument, "authorized_view is required")
	}
	body, err := bigtableCreateResource(bigtableAuthViewKind(), name, req.GetAuthorizedView())
	if err != nil {
		return nil, err
	}
	created, err := bigtableStoredProto(body, &btadmin.AuthorizedView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

func (s *bigtableTableAdminGRPC) GetAuthorizedView(_ context.Context, req *btadmin.GetAuthorizedViewRequest) (*btadmin.AuthorizedView, error) {
	view, err := bigtableReadResource(bigtableAuthViewKind(), req.GetName(), &btadmin.AuthorizedView{})
	if err != nil {
		return nil, err
	}
	return bigtableAuthorizedViewForResponseView(view, req.GetView()), nil
}

func (s *bigtableTableAdminGRPC) ListAuthorizedViews(_ context.Context, req *btadmin.ListAuthorizedViewsRequest) (*btadmin.ListAuthorizedViewsResponse, error) {
	if _, err := bigtableRequireTable(req.GetParent()); err != nil {
		return nil, err
	}
	views, next, err := bigtableListResources(bigtableAuthViewKind(), req.GetParent(), "authorizedViews",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.AuthorizedView { return &btadmin.AuthorizedView{} })
	if err != nil {
		return nil, err
	}
	for i, view := range views {
		views[i] = bigtableAuthorizedViewForResponseView(view, req.GetView())
	}
	return &btadmin.ListAuthorizedViewsResponse{AuthorizedViews: views, NextPageToken: next}, nil
}

// bigtableAuthorizedViewForResponseView trims an authorized view to the detail
// the request asked for. NAME_ONLY carries the name alone; every other view
// carries the whole resource.
func bigtableAuthorizedViewForResponseView(view *btadmin.AuthorizedView, responseView btadmin.AuthorizedView_ResponseView) *btadmin.AuthorizedView {
	if responseView == btadmin.AuthorizedView_NAME_ONLY {
		return &btadmin.AuthorizedView{Name: view.GetName()}
	}
	return view
}

func (s *bigtableTableAdminGRPC) UpdateAuthorizedView(_ context.Context, req *btadmin.UpdateAuthorizedViewRequest) (*longrunningpb.Operation, error) {
	view := req.GetAuthorizedView()
	if view.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "authorized_view.name is required")
	}
	body, err := bigtableUpdateResource(bigtableAuthViewKind(), view.GetName(), view, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, &btadmin.AuthorizedView{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(view.GetName(), updated)
}

func (s *bigtableTableAdminGRPC) DeleteAuthorizedView(_ context.Context, req *btadmin.DeleteAuthorizedViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableAuthViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Table admin: schema bundles
// ---------------------------------------------------------------------------

func (s *bigtableTableAdminGRPC) CreateSchemaBundle(_ context.Context, req *btadmin.CreateSchemaBundleRequest) (*longrunningpb.Operation, error) {
	name, err := bigtableTableChild(req.GetParent(), "schemaBundles", req.GetSchemaBundleId(), "schema_bundle_id")
	if err != nil {
		return nil, err
	}
	if req.GetSchemaBundle() == nil {
		return nil, status.Error(codes.InvalidArgument, "schema_bundle is required")
	}
	body, err := bigtableCreateResource(bigtableSchemaBundleKind(), name, req.GetSchemaBundle())
	if err != nil {
		return nil, err
	}
	created, err := bigtableStoredProto(body, &btadmin.SchemaBundle{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created)
}

func (s *bigtableTableAdminGRPC) GetSchemaBundle(_ context.Context, req *btadmin.GetSchemaBundleRequest) (*btadmin.SchemaBundle, error) {
	return bigtableReadResource(bigtableSchemaBundleKind(), req.GetName(), &btadmin.SchemaBundle{})
}

func (s *bigtableTableAdminGRPC) ListSchemaBundles(_ context.Context, req *btadmin.ListSchemaBundlesRequest) (*btadmin.ListSchemaBundlesResponse, error) {
	if _, err := bigtableRequireTable(req.GetParent()); err != nil {
		return nil, err
	}
	bundles, next, err := bigtableListResources(bigtableSchemaBundleKind(), req.GetParent(), "schemaBundles",
		req.GetPageSize(), req.GetPageToken(), func() *btadmin.SchemaBundle { return &btadmin.SchemaBundle{} })
	if err != nil {
		return nil, err
	}
	return &btadmin.ListSchemaBundlesResponse{SchemaBundles: bundles, NextPageToken: next}, nil
}

func (s *bigtableTableAdminGRPC) UpdateSchemaBundle(_ context.Context, req *btadmin.UpdateSchemaBundleRequest) (*longrunningpb.Operation, error) {
	bundle := req.GetSchemaBundle()
	if bundle.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "schema_bundle.name is required")
	}
	body, err := bigtableUpdateResource(bigtableSchemaBundleKind(), bundle.GetName(), bundle, req.GetUpdateMask())
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, &btadmin.SchemaBundle{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(bundle.GetName(), updated)
}

func (s *bigtableTableAdminGRPC) DeleteSchemaBundle(_ context.Context, req *btadmin.DeleteSchemaBundleRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableSchemaBundleKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// IAM
//
// Both admin services expose the AIP-141 IAM triple over the per-resource
// policy store every GCP resource in this simulator shares, and convert with
// the same helpers the Secret Manager door uses — one policy per resource
// name, whichever service the caller reached it through.
// ---------------------------------------------------------------------------

// bigtableIAMResourceExists reports whether the named IAM resource exists,
// reading the store that holds that kind of resource. The collection segment
// nearest the leaf names the kind, so the more deeply nested collections are
// matched before the ones whose names are prefixes of theirs.
func bigtableIAMResourceExists(resource string) bool {
	var ok bool
	switch {
	case strings.Contains(resource, "/backups/"):
		_, ok = bigtableBackups.Get(resource)
	case strings.Contains(resource, "/authorizedViews/"):
		_, ok = bigtableAuthViews.Get(resource)
	case strings.Contains(resource, "/schemaBundles/"):
		_, ok = bigtableSchemaBundle.Get(resource)
	case strings.Contains(resource, "/appProfiles/"):
		_, ok = bigtableAppProfiles.Get(resource)
	case strings.Contains(resource, "/logicalViews/"):
		_, ok = bigtableLogicalView.Get(resource)
	case strings.Contains(resource, "/materializedViews/"):
		_, ok = bigtableMatView.Get(resource)
	case strings.Contains(resource, "/tables/"):
		_, ok = bigtableTables.Get(resource)
	case strings.Contains(resource, "/clusters/"):
		_, ok = bigtableClusters.Get(resource)
	default:
		_, ok = bigtableInstances.Get(resource)
	}
	return ok
}

func bigtableGetIamPolicy(req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	resource := req.GetResource()
	if !bigtableIAMResourceExists(resource) {
		return nil, status.Errorf(codes.NotFound, "resource %q not found", resource)
	}
	store := gcpResourceIAMStore()
	policy, ok := store.Get(resource)
	if !ok {
		// The synthesized default is persisted so its etag is stable across
		// reads: a SetIamPolicy validates against the etag a read returned.
		policy = IAMPolicy{Bindings: []IAMBinding{}, Etag: gcpPolicyETag(), Version: 1}
		store.Put(resource, policy)
	}
	return smPolicyToProto(policy), nil
}

func bigtableSetIamPolicy(req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	resource := req.GetResource()
	if !bigtableIAMResourceExists(resource) {
		return nil, status.Errorf(codes.NotFound, "resource %q not found", resource)
	}
	policy := req.GetPolicy()
	if policy == nil {
		return nil, status.Error(codes.InvalidArgument, "policy is required")
	}
	store := gcpResourceIAMStore()
	current, present := store.Get(resource)
	if reqEtag := string(policy.GetEtag()); reqEtag != "" && (!present || reqEtag != current.Etag) {
		return nil, status.Error(codes.Aborted, "There were concurrent policy changes. Please retry the whole read-modify-write with exponential backoff.")
	}
	stored := IAMPolicy{Bindings: smIamBindingsFromProto(policy.GetBindings()), Etag: gcpPolicyETag(), Version: 1}
	if v := policy.GetVersion(); v != 0 {
		stored.Version = int(v)
	}
	store.Put(resource, stored)
	return smPolicyToProto(stored), nil
}

// bigtableTestIamPermissions echoes the requested permissions. The simulator
// models no authorization: every caller reaches it as a project administrator,
// so the whole requested set is the truthful answer. A resource that does not
// exist grants nothing, which real GCP reports as an empty set rather than an
// error.
func bigtableTestIamPermissions(req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	if !bigtableIAMResourceExists(req.GetResource()) {
		return &iampb.TestIamPermissionsResponse{}, nil
	}
	return &iampb.TestIamPermissionsResponse{Permissions: append([]string(nil), req.GetPermissions()...)}, nil
}

func (s *bigtableInstanceAdminGRPC) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableGetIamPolicy(req)
}

func (s *bigtableInstanceAdminGRPC) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableSetIamPolicy(req)
}

func (s *bigtableInstanceAdminGRPC) TestIamPermissions(_ context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	return bigtableTestIamPermissions(req)
}

func (s *bigtableTableAdminGRPC) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableGetIamPolicy(req)
}

func (s *bigtableTableAdminGRPC) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableSetIamPolicy(req)
}

func (s *bigtableTableAdminGRPC) TestIamPermissions(_ context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	return bigtableTestIamPermissions(req)
}
