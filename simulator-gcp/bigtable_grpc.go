package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	btadmin "cloud.google.com/go/bigtable/admin/apiv2/adminpb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
)

var bigtableGRPCOperations = struct {
	sync.Mutex
	items map[string]*longrunningpb.Operation
}{items: map[string]*longrunningpb.Operation{}}

type bigtableInstanceAdminGRPC struct {
	btadmin.UnimplementedBigtableInstanceAdminServer
}

type bigtableTableAdminGRPC struct {
	btadmin.UnimplementedBigtableTableAdminServer
}

type bigtableOperationsGRPC struct {
	longrunningpb.UnimplementedOperationsServer
}

func registerBigtableGRPC(gs *grpc.Server) {
	btadmin.RegisterBigtableInstanceAdminServer(gs, &bigtableInstanceAdminGRPC{})
	btadmin.RegisterBigtableTableAdminServer(gs, &bigtableTableAdminGRPC{})
	longrunningpb.RegisterOperationsServer(gs, &bigtableOperationsGRPC{})
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

func (s *bigtableOperationsGRPC) GetOperation(_ context.Context, req *longrunningpb.GetOperationRequest) (*longrunningpb.Operation, error) {
	bigtableGRPCOperations.Lock()
	defer bigtableGRPCOperations.Unlock()
	op, ok := bigtableGRPCOperations.items[req.GetName()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "operation %q not found", req.GetName())
	}
	return op, nil
}

func (s *bigtableOperationsGRPC) WaitOperation(ctx context.Context, req *longrunningpb.WaitOperationRequest) (*longrunningpb.Operation, error) {
	return s.GetOperation(ctx, &longrunningpb.GetOperationRequest{Name: req.GetName()})
}

func (s *bigtableOperationsGRPC) DeleteOperation(_ context.Context, req *longrunningpb.DeleteOperationRequest) (*emptypb.Empty, error) {
	bigtableGRPCOperations.Lock()
	defer bigtableGRPCOperations.Unlock()
	delete(bigtableGRPCOperations.items, req.GetName())
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
	bigtableGRPCOperations.Lock()
	bigtableGRPCOperations.items[op.Name] = op
	bigtableGRPCOperations.Unlock()
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
