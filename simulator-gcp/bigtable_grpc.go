package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	btadmin "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	iampb "cloud.google.com/go/iam/apiv1/iampb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"github.com/e6qu/sockerless-cloud/sim"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
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

// A long-running operation is one resource whichever protocol minted it, so
// the gRPC door reads and writes crOperations, the store the REST operations
// handlers serve, converting at the boundary.

// grpcRecordOperation stores an operation in its REST rendering.
func grpcRecordOperation(op *longrunningpb.Operation) error {
	stored, err := grpcOperationToStored(op)
	if err != nil {
		return err
	}
	if crOperations == nil {
		return status.Error(codes.Internal, "the operations store is not wired")
	}
	crOperations.Put(op.GetName(), stored)
	return nil
}

// grpcOperationToStored renders a protobuf Operation in the JSON shape the REST
// doors store.
func grpcOperationToStored(op *longrunningpb.Operation) (Operation, error) {
	out := Operation{Name: op.GetName(), Done: op.GetDone()}
	if response := op.GetResponse(); response != nil {
		body, err := grpcAnyToJSON(response)
		if err != nil {
			return Operation{}, status.Errorf(codes.Internal, "could not render operation response: %v", err)
		}
		out.Response = body
	}
	if metadata := op.GetMetadata(); metadata != nil {
		body, err := grpcAnyToJSON(metadata)
		if err != nil {
			return Operation{}, status.Errorf(codes.Internal, "could not render operation metadata: %v", err)
		}
		out.Metadata = body
	}
	if opErr := op.GetError(); opErr != nil {
		out.Error = &OperationError{Code: int(opErr.GetCode()), Message: opErr.GetMessage()}
	}
	return out, nil
}

func grpcAnyToJSON(value *anypb.Any) (map[string]any, error) {
	raw, err := protojson.Marshal(value)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body, nil
}

func grpcAnyFromJSON(body any) (*anypb.Any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	value := &anypb.Any{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, value); err != nil {
		return nil, err
	}
	return value, nil
}

// grpcOperationFromStored reads a stored operation back into its protobuf
// shape. The stored response carries the @type the REST rendering wrote, which
// is what lets it become a protobuf Any again.
func grpcOperationFromStored(stored Operation) (*longrunningpb.Operation, error) {
	stored = gcpCurrentOperation(stored)
	op := &longrunningpb.Operation{Name: stored.Name, Done: stored.Done}
	if stored.Response != nil {
		response, err := grpcAnyFromJSON(stored.Response)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "could not read operation response: %v", err)
		}
		op.Result = &longrunningpb.Operation_Response{Response: response}
	}
	if stored.Metadata != nil {
		metadata, err := grpcAnyFromJSON(stored.Metadata)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "could not read operation metadata: %v", err)
		}
		op.Metadata = metadata
	}
	if stored.Error != nil {
		op.Result = &longrunningpb.Operation_Error{Error: &statuspb.Status{
			Code:    int32(stored.Error.Code),
			Message: stored.Error.Message,
		}}
	}
	return op, nil
}

// grpcStoredOperations returns the stored operations whose name begins with
// prefix, in name order.
func grpcStoredOperations(prefix string) []Operation {
	var matched []Operation
	for _, op := range crOperations.List() {
		if prefix == "" || strings.HasPrefix(op.Name, prefix) {
			matched = append(matched, op)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	return matched
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
	requested := time.Now()
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

	return bigtableDoneOperation(stored.Name, bigtableInstanceToPB(stored), &btadmin.CreateInstanceMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
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
	return &btadmin.ListInstancesResponse{Instances: gcpProtoList(bigtableInstances, prefix, func(v bigtableInstance) string { return v.Name }, bigtableInstanceToPB)}, nil
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
	requested := time.Now()
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
	return bigtableDoneOperation(name, bigtableClusterToPB(stored), &btadmin.CreateClusterMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
		Tables:          bigtableClusterTableProgress(req.GetParent()),
	})
}

// bigtableClusterTableProgress reports every table COMPLETED with all its bytes
// copied, since CreateCluster copies them before it returns.
func bigtableClusterTableProgress(instance string) map[string]*btadmin.CreateClusterMetadata_TableProgress {
	progress := map[string]*btadmin.CreateClusterMetadata_TableProgress{}
	for _, table := range bigtableTables.List() {
		if !strings.HasPrefix(table.Name, instance+"/tables/") {
			continue
		}
		size := btTableSizeBytes(table.Name)
		progress[table.Name] = &btadmin.CreateClusterMetadata_TableProgress{
			EstimatedSizeBytes:   size,
			EstimatedCopiedBytes: size,
			State:                btadmin.CreateClusterMetadata_TableProgress_COMPLETED,
		}
	}
	return progress
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
	return &btadmin.ListClustersResponse{Clusters: gcpProtoList(bigtableClusters, prefix, func(v bigtableCluster) string { return v.Name }, bigtableClusterToPB)}, nil
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
	return &btadmin.ListTablesResponse{Tables: gcpProtoList(bigtableTables, prefix, func(v bigtableTable) string { return v.Name }, bigtableTableToPB)}, nil
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
	stored, ok := crOperations.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	return grpcOperationFromStored(stored)
}

func (s *grpcOperationsService) WaitOperation(ctx context.Context, req *longrunningpb.WaitOperationRequest) (*longrunningpb.Operation, error) {
	stored, ok := gcpAwaitOperation(ctx, req.GetName(), req.GetTimeout().AsDuration())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	return grpcOperationFromStored(stored)
}

func (s *grpcOperationsService) DeleteOperation(_ context.Context, req *longrunningpb.DeleteOperationRequest) (*emptypb.Empty, error) {
	crOperations.Delete(req.GetName())
	return &emptypb.Empty{}, nil
}

// ListOperations returns the operations under a parent resource. Every
// operation name this service issues is the resource's own name followed by
// "/operations/<id>", so the parent is a prefix of its operations' names.
func (s *grpcOperationsService) ListOperations(_ context.Context, req *longrunningpb.ListOperationsRequest) (*longrunningpb.ListOperationsResponse, error) {
	filter, err := gcpParseFilterExpr(req.GetFilter())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	stored := grpcStoredOperations(req.GetName())
	matched := make([]*longrunningpb.Operation, 0, len(stored))
	for _, entry := range stored {
		if !filter.Eval(gcpOperationFilterDoc(entry)) {
			continue
		}
		op, err := grpcOperationFromStored(entry)
		if err != nil {
			return nil, err
		}
		matched = append(matched, op)
	}
	page, next, err := grpcKeyPage(matched, req.GetPageSize(), req.GetPageToken(),
		gcpAsc(func(op *longrunningpb.Operation) string { return op.GetName() }))
	if err != nil {
		return nil, err
	}
	return &longrunningpb.ListOperationsResponse{Operations: page, NextPageToken: next}, nil
}

// CancelOperation asks the service to stop an operation. A done operation is
// acknowledged and keeps its result, an unfinished one has its work stopped,
// and an unknown name is a NotFound.
func (s *grpcOperationsService) CancelOperation(_ context.Context, req *longrunningpb.CancelOperationRequest) (*emptypb.Empty, error) {
	op, ok := crOperations.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	gcpCancelOperationWork(op)
	return &emptypb.Empty{}, nil
}

// bigtableDoneOperation records a finished admin operation carrying its result
// and the metadata message the method declares in its operation_info.
func bigtableDoneOperation(resourceName string, resource, metadata proto.Message) (*longrunningpb.Operation, error) {
	resp, err := anypb.New(resource)
	if err != nil {
		return nil, err
	}
	meta, err := anypb.New(metadata)
	if err != nil {
		return nil, err
	}
	op := &longrunningpb.Operation{
		Name:     bigtableOperationName(resourceName),
		Metadata: meta,
		Done:     true,
		Result: &longrunningpb.Operation_Response{
			Response: resp,
		},
	}
	if err := grpcRecordOperation(op); err != nil {
		return nil, err
	}
	return op, nil
}

// bigtableOperationName names a Cloud Bigtable admin operation in the
// "operations/" collection the bigtableadmin document declares (get pattern
// "^operations/.*$", list parent "operations/projects/{project}"), as
// newBigtableAdminLRO does over REST.
func bigtableOperationName(resourceName string) string {
	project := "unknown"
	if parts := strings.Split(resourceName, "/"); len(parts) >= 2 && parts[0] == "projects" {
		project = parts[1]
	}
	return fmt.Sprintf("operations/projects/%s/operations/%d", project, time.Now().UnixNano())
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

func bigtableClusterParts(name string) (string, string, string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "instances" || parts[4] != "clusters" || parts[1] == "" || parts[3] == "" || parts[5] == "" {
		return "", "", "", status.Errorf(codes.InvalidArgument, "invalid cluster name %q", name)
	}
	return parts[1], parts[3], parts[5], nil
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

// App profiles, backups, authorized views, schema bundles and logical and
// materialized views live as the JSON body the REST door stores; the gRPC door
// converts through protojson, so both see the same fields under the same names.

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

// bigtableResourceFromProto renders a message with protojson field names, the
// names the resource's JSON body uses.
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

// bigtableResourceToProto reads a stored body into msg, dropping fields the
// message does not declare that the REST door accepted.
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

// bigtableDeleteResource refuses a stale etag with ABORTED, as Cloud Bigtable
// answers a blocked delete.
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
// parent, built by newMsg, and the token that continues the listing.
func bigtableListResources[T proto.Message](kind bigtableResourceKind, parent, collection string, pageSize int32, pageToken string, newMsg func() T) ([]T, string, error) {
	bodies := bigtableFilterResources(kind.store, parent+"/"+collection+"/")
	page, next, err := grpcKeyPage(bodies, pageSize, pageToken, gcpAsc(func(body bigtableResource) string {
		name, _ := body["name"].(string)
		return name
	}))
	if err != nil {
		return nil, "", err
	}
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

// bigtableApplyProtoMask merges the masked fields of patch into body. A masked
// field that patch leaves unset is cleared, and setting one oneof member clears
// its siblings so a replaced routing policy leaves no second spelling behind.
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

// bigtableJSONFieldName maps a field-mask path's leading segment onto the
// stored JSON key; the store holds nested values whole.
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

func bigtableClusterChild(parent, collection, id, idField string) (string, error) {
	if err := bigtableRequireCluster(parent); err != nil {
		return "", err
	}
	if id == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", idField)
	}
	return parent + "/" + collection + "/" + id, nil
}

func bigtableTableChild(parent, collection, id, idField string) (string, error) {
	if _, err := bigtableRequireTable(parent); err != nil {
		return "", err
	}
	if id == "" {
		return "", status.Errorf(codes.InvalidArgument, "%s is required", idField)
	}
	return parent + "/" + collection + "/" + id, nil
}

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
	return bigtableDoneOperation(profile.GetName(), updated, &btadmin.UpdateAppProfileMetadata{})
}

func (s *bigtableInstanceAdminGRPC) DeleteAppProfile(_ context.Context, req *btadmin.DeleteAppProfileRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableAppProfileKind(), req.GetName(), ""); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *bigtableInstanceAdminGRPC) CreateLogicalView(_ context.Context, req *btadmin.CreateLogicalViewRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
	name, err := bigtableInstanceChild(req.GetParent(), "logicalViews", req.GetLogicalViewId(), "logical_view_id")
	if err != nil {
		return nil, err
	}
	return bigtableCreateViewOperation(bigtableLogicalViewKind(), name, req.GetLogicalView(), req.GetLogicalView() != nil, "logical_view", &btadmin.LogicalView{}, requested,
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.CreateLogicalViewMetadata{OriginalRequest: proto.CloneOf(req), StartTime: start, EndTime: end}
		})
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
	return bigtableUpdateViewOperation(bigtableLogicalViewKind(), req.GetLogicalView(), req.GetUpdateMask(), "logical_view", &btadmin.LogicalView{}, time.Now(),
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.UpdateLogicalViewMetadata{OriginalRequest: proto.CloneOf(req), StartTime: start, EndTime: end}
		})
}

func (s *bigtableInstanceAdminGRPC) DeleteLogicalView(_ context.Context, req *btadmin.DeleteLogicalViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableLogicalViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *bigtableInstanceAdminGRPC) CreateMaterializedView(_ context.Context, req *btadmin.CreateMaterializedViewRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
	name, err := bigtableInstanceChild(req.GetParent(), "materializedViews", req.GetMaterializedViewId(), "materialized_view_id")
	if err != nil {
		return nil, err
	}
	return bigtableCreateViewOperation(bigtableMatViewKind(), name, req.GetMaterializedView(), req.GetMaterializedView() != nil, "materialized_view", &btadmin.MaterializedView{}, requested,
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.CreateMaterializedViewMetadata{OriginalRequest: proto.CloneOf(req), StartTime: start, EndTime: end}
		})
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
	return bigtableUpdateViewOperation(bigtableMatViewKind(), req.GetMaterializedView(), req.GetUpdateMask(), "materialized_view", &btadmin.MaterializedView{}, time.Now(),
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.UpdateMaterializedViewMetadata{OriginalRequest: proto.CloneOf(req), StartTime: start, EndTime: end}
		})
}

func (s *bigtableInstanceAdminGRPC) DeleteMaterializedView(_ context.Context, req *btadmin.DeleteMaterializedViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableMatViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

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
	requested := time.Now()
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
	return bigtableDoneOperation(stored.Name, bigtableInstanceToPB(stored), &btadmin.UpdateInstanceMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
}

// UpdateCluster replaces a cluster. Serve nodes are the mutable part of a
// cluster; its location and storage type are fixed at creation.
func (s *bigtableInstanceAdminGRPC) UpdateCluster(_ context.Context, req *btadmin.Cluster) (*longrunningpb.Operation, error) {
	requested := time.Now()
	stored, ok := bigtableClusters.Get(req.GetName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "cluster %q not found", req.GetName())
	}
	if req.GetServeNodes() != 0 {
		stored.ServeNodes = int(req.GetServeNodes())
	}
	stored.State = "READY"
	bigtableClusters.Put(stored.Name, stored)
	return bigtableDoneOperation(stored.Name, bigtableClusterToPB(stored), &btadmin.UpdateClusterMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
}

func (s *bigtableInstanceAdminGRPC) PartialUpdateCluster(_ context.Context, req *btadmin.PartialUpdateClusterRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	return bigtableDoneOperation(stored.Name, bigtableClusterToPB(stored), &btadmin.PartialUpdateClusterMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
}

// ListHotTablets reports none: nothing in the simulator concentrates load on a
// tablet.
func (s *bigtableInstanceAdminGRPC) ListHotTablets(_ context.Context, req *btadmin.ListHotTabletsRequest) (*btadmin.ListHotTabletsResponse, error) {
	if err := bigtableRequireCluster(req.GetParent()); err != nil {
		return nil, err
	}
	return &btadmin.ListHotTabletsResponse{HotTablets: []*btadmin.HotTablet{}}, nil
}

// bigtableUnsupportedMaskPath answers InvalidArgument for a path the message
// does not declare, and Unimplemented for one the store does not keep, rather
// than silently dropping the field.
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

func (s *bigtableTableAdminGRPC) UpdateTable(_ context.Context, req *btadmin.UpdateTableRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	return bigtableDoneOperation(table.Name, bigtableTableToPB(table), &btadmin.UpdateTableMetadata{
		Name:      table.Name,
		StartTime: timestamppb.New(requested),
		EndTime:   timestamppb.Now(),
	})
}

// UndeleteTable reports NotFound for a deleted table, since DeleteTable drops
// it and its rows outright, and returns a table still present as it stands.
func (s *bigtableTableAdminGRPC) UndeleteTable(_ context.Context, req *btadmin.UndeleteTableRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
	table, err := bigtableRequireTable(req.GetName())
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(table.Name, bigtableTableToPB(table), &btadmin.UndeleteTableMetadata{
		Name:      table.Name,
		StartTime: timestamppb.New(requested),
		EndTime:   timestamppb.Now(),
	})
}

// DropRowRange writes through the row store the data plane reads, so a dropped
// range is gone from ReadRows too.
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
	return &btadmin.GenerateConsistencyTokenResponse{ConsistencyToken: sim.NewUUID()}, nil
}

// CheckConsistency reports every token satisfied: the simulator serves a table
// from one copy, so a mutation is visible everywhere once it returns.
func (s *bigtableTableAdminGRPC) CheckConsistency(_ context.Context, req *btadmin.CheckConsistencyRequest) (*btadmin.CheckConsistencyResponse, error) {
	if _, err := bigtableRequireTable(req.GetName()); err != nil {
		return nil, err
	}
	if req.GetConsistencyToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "consistency_token is required")
	}
	return &btadmin.CheckConsistencyResponse{Consistent: true}, nil
}

func (s *bigtableTableAdminGRPC) CreateBackup(_ context.Context, req *btadmin.CreateBackupRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	// The backup captures the table's rows inside this call, so its start and
	// end times bound this request.
	stored := &btadmin.Backup{}
	proto.Merge(stored, backup)
	stored.State = btadmin.Backup_READY
	stored.StartTime = timestamppb.New(requested)
	stored.SizeBytes = btTableSizeBytes(backup.GetSourceTable())
	stored.EndTime = timestamppb.Now()
	body, err := bigtableCreateResource(bigtableBackupKind(), name, stored)
	if err != nil {
		return nil, err
	}
	btCaptureTable(name, backup.GetSourceTable())
	created, err := bigtableStoredProto(body, &btadmin.Backup{})
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created, &btadmin.CreateBackupMetadata{
		Name:        name,
		SourceTable: backup.GetSourceTable(),
		StartTime:   stored.GetStartTime(),
		EndTime:     stored.GetEndTime(),
	})
}

// bigtableBackupInfo describes a backup as the operations that read from it
// report it.
func bigtableBackupInfo(backup *btadmin.Backup) *btadmin.BackupInfo {
	return &btadmin.BackupInfo{
		Backup:       backup.GetName(),
		StartTime:    backup.GetStartTime(),
		EndTime:      backup.GetEndTime(),
		SourceTable:  backup.GetSourceTable(),
		SourceBackup: backup.GetSourceBackup(),
	}
}

// bigtableCompleteProgress is the progress of an operation that finished
// inside the request that started it.
func bigtableCompleteProgress(requested time.Time) *btadmin.OperationProgress {
	return &btadmin.OperationProgress{
		ProgressPercent: 100,
		StartTime:       timestamppb.New(requested),
		EndTime:         timestamppb.Now(),
	}
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

// The bigtableadmin Discovery document declares no snapshots collection, so
// snapshots exist only on the gRPC door and share the backups' capture store.

func bigtableSnapshotKind() bigtableResourceKind {
	return bigtableResourceKind{label: "snapshot", store: bigtableSnapshots, etag: false}
}

func (s *bigtableTableAdminGRPC) SnapshotTable(_ context.Context, req *btadmin.SnapshotTableRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
		Name:          name,
		SourceTable:   bigtableTableToPB(table),
		CreateTime:    timestamppb.New(now),
		State:         btadmin.Snapshot_READY,
		Description:   req.GetDescription(),
		DataSizeBytes: btTableSizeBytes(table.Name),
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
	return bigtableDoneOperation(name, created, &btadmin.SnapshotTableMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
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
	requested := time.Now()
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
	return bigtableDoneOperation(name, bigtableTableToPB(table), &btadmin.CreateTableFromSnapshotMetadata{
		OriginalRequest: proto.CloneOf(req),
		RequestTime:     timestamppb.New(requested),
		FinishTime:      timestamppb.Now(),
	})
}

// CopyBackup copies a backup into a cluster, carrying the source's table and
// size across and taking a new expiry from the request.
func (s *bigtableTableAdminGRPC) CopyBackup(_ context.Context, req *btadmin.CopyBackupRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	// A copy holds the source's data as of the source's own capture, so it
	// carries the source's start and end times.
	body, err := bigtableCreateResource(bigtableBackupKind(), name, &btadmin.Backup{
		SourceTable:  source.GetSourceTable(),
		SourceBackup: req.GetSourceBackup(),
		ExpireTime:   req.GetExpireTime(),
		StartTime:    source.GetStartTime(),
		EndTime:      source.GetEndTime(),
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
	return bigtableDoneOperation(name, created, &btadmin.CopyBackupMetadata{
		Name:             name,
		SourceBackupInfo: bigtableBackupInfo(source),
		Progress:         bigtableCompleteProgress(requested),
	})
}

// RestoreTable creates a table holding the rows its backup captured.
func (s *bigtableTableAdminGRPC) RestoreTable(_ context.Context, req *btadmin.RestoreTableRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	backup, err := bigtableReadResource(bigtableBackupKind(), req.GetBackup(), &btadmin.Backup{})
	if err != nil {
		return nil, err
	}
	if _, ok := bigtableTables.Get(name); ok {
		return nil, status.Errorf(codes.AlreadyExists, "table %q already exists", name)
	}
	table, ok := btRestoreCapture(req.GetBackup(), name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "backup %q not found", req.GetBackup())
	}
	// No optimization follows the restore, so optimize_table_operation_name
	// stays empty.
	return bigtableDoneOperation(name, bigtableTableToPB(table), &btadmin.RestoreTableMetadata{
		Name:       name,
		SourceType: btadmin.RestoreSourceType_BACKUP,
		SourceInfo: &btadmin.RestoreTableMetadata_BackupInfo{BackupInfo: bigtableBackupInfo(backup)},
		Progress:   bigtableCompleteProgress(requested),
	})
}

func (s *bigtableTableAdminGRPC) CreateAuthorizedView(_ context.Context, req *btadmin.CreateAuthorizedViewRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
	name, err := bigtableTableChild(req.GetParent(), "authorizedViews", req.GetAuthorizedViewId(), "authorized_view_id")
	if err != nil {
		return nil, err
	}
	return bigtableCreateViewOperation(bigtableAuthViewKind(), name, req.GetAuthorizedView(), req.GetAuthorizedView() != nil, "authorized_view", &btadmin.AuthorizedView{}, requested,
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.CreateAuthorizedViewMetadata{OriginalRequest: proto.CloneOf(req), RequestTime: start, FinishTime: end}
		})
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
	return bigtableUpdateViewOperation(bigtableAuthViewKind(), req.GetAuthorizedView(), req.GetUpdateMask(), "authorized_view", &btadmin.AuthorizedView{}, time.Now(),
		func(start, end *timestamppb.Timestamp) proto.Message {
			return &btadmin.UpdateAuthorizedViewMetadata{OriginalRequest: proto.CloneOf(req), RequestTime: start, FinishTime: end}
		})
}

func (s *bigtableTableAdminGRPC) DeleteAuthorizedView(_ context.Context, req *btadmin.DeleteAuthorizedViewRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableAuthViewKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *bigtableTableAdminGRPC) CreateSchemaBundle(_ context.Context, req *btadmin.CreateSchemaBundleRequest) (*longrunningpb.Operation, error) {
	requested := time.Now()
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
	return bigtableDoneOperation(name, created, &btadmin.CreateSchemaBundleMetadata{
		Name:      name,
		StartTime: timestamppb.New(requested),
		EndTime:   timestamppb.Now(),
	})
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
	requested := time.Now()
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
	return bigtableDoneOperation(bundle.GetName(), updated, &btadmin.UpdateSchemaBundleMetadata{
		Name:      bundle.GetName(),
		StartTime: timestamppb.New(requested),
		EndTime:   timestamppb.Now(),
	})
}

func (s *bigtableTableAdminGRPC) DeleteSchemaBundle(_ context.Context, req *btadmin.DeleteSchemaBundleRequest) (*emptypb.Empty, error) {
	if err := bigtableDeleteResource(bigtableSchemaBundleKind(), req.GetName(), req.GetEtag()); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

// Both admin services serve the AIP-141 IAM triple from the per-resource
// policy store every Google Cloud resource here shares, one policy per
// resource name whichever service reaches it.

// bigtableIAMResourceExists reads the store for the resource's kind, which the
// collection segment nearest the leaf names; deeper collections match before
// the ones whose names prefix theirs.
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
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
	store := gcpResourceIAMStore()
	policy, ok := store.Get(resource)
	if !ok {
		// Persist the default so its etag stays stable: SetIamPolicy validates
		// against the etag a read returned.
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
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
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

// bigtableTestIamPermissions answers from the resource's policy. A resource
// that does not exist grants nothing: Google Cloud answers an empty set, not
// an error.
func bigtableTestIamPermissions(ctx context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	if !bigtableIAMResourceExists(req.GetResource()) {
		return &iampb.TestIamPermissionsResponse{}, nil
	}
	policy, _ := gcpResourceIAMStore().Get(req.GetResource())
	return &iampb.TestIamPermissionsResponse{
		Permissions: gcpAnswerTestIamPermissionsForContext(ctx, policy, req.GetPermissions(), gcpIAMResourceNamed(req.GetResource()))}, nil
}

func (s *bigtableInstanceAdminGRPC) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableGetIamPolicy(req)
}

func (s *bigtableInstanceAdminGRPC) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableSetIamPolicy(req)
}

func (s *bigtableInstanceAdminGRPC) TestIamPermissions(ctx context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	return bigtableTestIamPermissions(ctx, req)
}

func (s *bigtableTableAdminGRPC) GetIamPolicy(_ context.Context, req *iampb.GetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableGetIamPolicy(req)
}

func (s *bigtableTableAdminGRPC) SetIamPolicy(_ context.Context, req *iampb.SetIamPolicyRequest) (*iampb.Policy, error) {
	return bigtableSetIamPolicy(req)
}

func (s *bigtableTableAdminGRPC) TestIamPermissions(ctx context.Context, req *iampb.TestIamPermissionsRequest) (*iampb.TestIamPermissionsResponse, error) {
	return bigtableTestIamPermissions(ctx, req)
}

// bigtableCreateViewOperation stores view under name and answers the settled
// operation carrying it, with metadata built from when the request arrived and
// finished. present is false when the request carries no view in field.
func bigtableCreateViewOperation[V proto.Message](kind bigtableResourceKind, name string, view proto.Message, present bool, field string,
	stored V, requested time.Time, metadata func(start, end *timestamppb.Timestamp) proto.Message) (*longrunningpb.Operation, error) {
	if !present {
		return nil, status.Error(codes.InvalidArgument, field+" is required")
	}
	body, err := bigtableCreateResource(kind, name, view)
	if err != nil {
		return nil, err
	}
	created, err := bigtableStoredProto(body, stored)
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(name, created, metadata(timestamppb.New(requested), timestamppb.Now()))
}

// bigtableUpdateViewOperation applies a masked update to a view and answers the
// settled operation carrying it, with metadata built from when the request
// arrived and finished.
func bigtableUpdateViewOperation[V interface {
	proto.Message
	GetName() string
}, S proto.Message](kind bigtableResourceKind, view V, mask *fieldmaskpb.FieldMask, field string, stored S, requested time.Time,
	metadata func(start, end *timestamppb.Timestamp) proto.Message) (*longrunningpb.Operation, error) {
	if view.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, field+".name is required")
	}
	body, err := bigtableUpdateResource(kind, view.GetName(), view, mask)
	if err != nil {
		return nil, err
	}
	updated, err := bigtableStoredProto(body, stored)
	if err != nil {
		return nil, err
	}
	return bigtableDoneOperation(view.GetName(), updated, metadata(timestamppb.New(requested), timestamppb.Now()))
}
