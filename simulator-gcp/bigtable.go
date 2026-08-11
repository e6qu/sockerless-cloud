package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

type bigtableInstance struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	State       string            `json:"state,omitempty"`
	Type        string            `json:"type,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

type bigtableCluster struct {
	Name               string `json:"name"`
	Location           string `json:"location,omitempty"`
	State              string `json:"state,omitempty"`
	ServeNodes         int    `json:"serveNodes,omitempty"`
	DefaultStorageType string `json:"defaultStorageType,omitempty"`
}

type bigtableTable struct {
	Name               string                    `json:"name"`
	ClusterStates      map[string]map[string]any `json:"clusterStates,omitempty"`
	ColumnFamilies     map[string]map[string]any `json:"columnFamilies,omitempty"`
	Granularity        string                    `json:"granularity,omitempty"`
	DeletionProtection bool                      `json:"deletionProtection,omitempty"`
}

type bigtableMemoryConfig struct {
	StorageSizeGiB int `json:"storageSizeGib,omitempty"`
}

type bigtableMemoryLayer struct {
	Name         string                `json:"name"`
	MemoryConfig *bigtableMemoryConfig `json:"memoryConfig,omitempty"`
	State        string                `json:"state"`
	Etag         string                `json:"etag"`
}

// bigtableResource stores the JSON body of resource families whose schema is
// large and pass-through (app profiles, backups, authorized views, schema
// bundles, logical/materialized views). The map preserves every field the
// client sent so read-back is lossless; the sim sets server-managed fields
// (name, etag, state) on top.
type bigtableResource map[string]any

var (
	bigtableInstances    sim.Store[bigtableInstance]
	bigtableClusters     sim.Store[bigtableCluster]
	bigtableMemoryLayers sim.Store[bigtableMemoryLayer]
	bigtableTables       sim.Store[bigtableTable]
	bigtableAppProfiles  sim.Store[bigtableResource]
	bigtableBackups      sim.Store[bigtableResource]
	bigtableAuthViews    sim.Store[bigtableResource]
	bigtableSchemaBundle sim.Store[bigtableResource]
	bigtableLogicalView  sim.Store[bigtableResource]
	bigtableMatView      sim.Store[bigtableResource]
)

func registerBigtable(srv *sim.Server) {
	bigtableInstances = sim.MakeStore[bigtableInstance](srv.DB(), "bigtable_instances")
	bigtableClusters = sim.MakeStore[bigtableCluster](srv.DB(), "bigtable_clusters")
	bigtableMemoryLayers = sim.MakeStore[bigtableMemoryLayer](srv.DB(), "bigtable_memory_layers")
	bigtableTables = sim.MakeStore[bigtableTable](srv.DB(), "bigtable_tables")
	bigtableRows = sim.MakeStore[btStoredTableData](srv.DB(), "bigtable_table_rows")
	bigtableAppProfiles = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_app_profiles")
	bigtableBackups = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_backups")
	bigtableAuthViews = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_authorized_views")
	bigtableSchemaBundle = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_schema_bundles")
	bigtableLogicalView = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_logical_views")
	bigtableMatView = sim.MakeStore[bigtableResource](srv.DB(), "bigtable_materialized_views")

	// Project-scoped operations list. (bigtableadmin's locations.list shares the
	// identical GET /v2/projects/{project}/locations path already registered by
	// Cloud Functions; the single shared route serves both services.)
	srv.HandleFunc("GET /v2/operations/projects/{project}/operations", handleBigtableListOperations)

	// Instances.
	srv.HandleFunc("POST /v2/projects/{project}/instances", handleBigtableCreateInstance)
	srv.HandleFunc("GET /v2/projects/{project}/instances", handleBigtableListInstances)
	// {instance}:getIamPolicy / setIamPolicy / testIamPermissions ride the
	// colon on the instance segment; the wildcard captures "name:verb".
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instanceAction}", handleBigtableInstanceAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}", handleBigtableGetInstance)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}", handleBigtablePartialUpdateInstance)
	srv.HandleFunc("PUT /v2/projects/{project}/instances/{instance}", handleBigtableReplaceInstance)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}", handleBigtableDeleteInstance)

	// Clusters.
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/clusters", handleBigtableCreateCluster)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters", handleBigtableListClusters)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}", handleBigtableGetCluster)
	srv.HandleFunc("PUT /v2/projects/{project}/instances/{instance}/clusters/{cluster}", handleBigtableUpdateCluster)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}", handleBigtablePartialUpdateCluster)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/clusters/{cluster}", handleBigtableDeleteCluster)

	// Hot tablets + memory layers (cluster-scoped read surfaces).
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/hotTablets", handleBigtableListHotTablets)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer", handleBigtableGetMemoryLayer)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer", handleBigtableUpdateMemoryLayer)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayers", handleBigtableListMemoryLayers)

	// Backups (cluster-scoped). The collection-level "backups:copy" rides the
	// colon on the literal "backups" segment, so it is captured by the
	// {backupsColl} wildcard (the literal "backups" POST above wins for create).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups", handleBigtableCreateBackup)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups", handleBigtableListBackups)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/{backupsColl}", handleBigtableBackupCollectionAction)
	// Backup IAM verbs ride the colon on the item segment.
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backupAction}", handleBigtableBackupItemAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}", handleBigtableGetBackup)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}", handleBigtablePatchBackup)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}", handleBigtableDeleteBackup)

	// App profiles.
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/appProfiles", handleBigtableCreateAppProfile)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/appProfiles", handleBigtableListAppProfiles)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}", handleBigtableGetAppProfile)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}", handleBigtablePatchAppProfile)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}", handleBigtableDeleteAppProfile)

	// Tables. The collection-level "tables:restore" rides the colon on the
	// literal "tables" segment, captured by the {tablesColl} wildcard (the
	// literal "tables" POST above wins for create).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables", handleBigtableCreateTable)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables", handleBigtableListTables)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/{tablesColl}", handleBigtableTableCollectionAction)
	// {table}:verb (modifyColumnFamilies / dropRowRange / generateConsistencyToken /
	// checkConsistency / undelete / IAM) ride the colon on the table item segment.
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables/{tableAction}", handleBigtableTableAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables/{table}", handleBigtableGetTable)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/tables/{table}", handleBigtablePatchTable)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/tables/{table}", handleBigtableDeleteTable)

	// Authorized views (table-scoped).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews", handleBigtableCreateAuthView)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews", handleBigtableListAuthViews)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authViewAction}", handleBigtableAuthViewItemAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}", handleBigtableGetAuthView)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}", handleBigtablePatchAuthView)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}", handleBigtableDeleteAuthView)

	// Schema bundles (table-scoped).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles", handleBigtableCreateSchemaBundle)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles", handleBigtableListSchemaBundles)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundleAction}", handleBigtableSchemaBundleItemAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}", handleBigtableGetSchemaBundle)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}", handleBigtablePatchSchemaBundle)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}", handleBigtableDeleteSchemaBundle)

	// Logical views (instance-scoped).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/logicalViews", handleBigtableCreateLogicalView)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/logicalViews", handleBigtableListLogicalViews)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/logicalViews/{logicalViewAction}", handleBigtableLogicalViewItemAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}", handleBigtableGetLogicalView)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}", handleBigtablePatchLogicalView)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}", handleBigtableDeleteLogicalView)

	// Materialized views (instance-scoped).
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/materializedViews", handleBigtableCreateMatView)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/materializedViews", handleBigtableListMatViews)
	srv.HandleFunc("POST /v2/projects/{project}/instances/{instance}/materializedViews/{matViewAction}", handleBigtableMatViewItemAction)
	srv.HandleFunc("GET /v2/projects/{project}/instances/{instance}/materializedViews/{matView}", handleBigtableGetMatView)
	srv.HandleFunc("PATCH /v2/projects/{project}/instances/{instance}/materializedViews/{matView}", handleBigtablePatchMatView)
	srv.HandleFunc("DELETE /v2/projects/{project}/instances/{instance}/materializedViews/{matView}", handleBigtableDeleteMatView)
}

func bigtableInstanceName(project, instance string) string {
	return fmt.Sprintf("projects/%s/instances/%s", project, instance)
}

func bigtableClusterName(project, instance, cluster string) string {
	return fmt.Sprintf("%s/clusters/%s", bigtableInstanceName(project, instance), cluster)
}

func bigtableTableName(project, instance, table string) string {
	return fmt.Sprintf("%s/tables/%s", bigtableInstanceName(project, instance), table)
}

// bigtableSplitColonVerb separates a "{id}:{verb}" path segment. When no colon
// is present the whole segment is the id and verb is empty.
func bigtableSplitColonVerb(seg string) (id, verb string) {
	id, verb, _ = strings.Cut(seg, ":")
	return id, verb
}

func newBigtableAdminLRO(project string, resource any, typeName string) Operation {
	op := newLRO(project, "global", resource, typeName)
	return renameGCPOperation(op, "operations")
}

// ----- Operations -----

func handleBigtableListOperations(w http.ResponseWriter, r *http.Request) {
	prefix := "operations/"
	out := []Operation{}
	for _, op := range crOperations.List() {
		if strings.HasPrefix(op.Name, prefix) {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operations": out})
}

// ----- Instances -----

func handleBigtableCreateInstance(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req struct {
		InstanceID string                     `json:"instanceId"`
		Instance   bigtableInstance           `json:"instance"`
		Clusters   map[string]bigtableCluster `json:"clusters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.InstanceID == "" {
		sim.GCPError(w, http.StatusBadRequest, "instanceId is required", "INVALID_ARGUMENT")
		return
	}
	inst := req.Instance
	inst.Name = bigtableInstanceName(project, req.InstanceID)
	if inst.DisplayName == "" {
		inst.DisplayName = req.InstanceID
	}
	if inst.Type == "" {
		inst.Type = "PRODUCTION"
	}
	inst.State = "READY"
	bigtableInstances.Put(inst.Name, inst)
	for id, cluster := range req.Clusters {
		if cluster.Location == "" {
			cluster.Location = fmt.Sprintf("projects/%s/locations/us-central1-a", project)
		}
		if cluster.ServeNodes == 0 {
			cluster.ServeNodes = 1
		}
		if cluster.DefaultStorageType == "" {
			cluster.DefaultStorageType = "SSD"
		}
		cluster.Name = bigtableClusterName(project, req.InstanceID, id)
		cluster.State = "READY"
		bigtableClusters.Put(cluster.Name, cluster)
	}
	op := newBigtableAdminLRO(project, inst, "type.googleapis.com/google.bigtable.admin.v2.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListInstances(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("projects/%s/instances/", sim.PathParam(r, "project"))
	out := bigtableInstances.Filter(func(inst bigtableInstance) bool { return strings.HasPrefix(inst.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"instances": out})
}

// handleBigtableInstanceAction dispatches the instance-level IAM colon verbs.
func handleBigtableInstanceAction(w http.ResponseWriter, r *http.Request) {
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "instanceAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown instance action %q", sim.PathParam(r, "instanceAction"))
		return
	}
	resource := bigtableInstanceName(sim.PathParam(r, "project"), id)
	handleResourceIAM(w, r, gcpResourceIAMStore(), resource, verb)
}

func handleBigtableGetInstance(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"))
	inst, ok := bigtableInstances.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, inst)
}

func handleBigtablePartialUpdateInstance(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"))
	inst, ok := bigtableInstances.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	var req bigtableInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
		switch strings.TrimSpace(field) {
		case "displayName", "display_name":
			inst.DisplayName = req.DisplayName
		case "labels":
			inst.Labels = req.Labels
		case "type":
			inst.Type = req.Type
		}
	}
	bigtableInstances.Put(name, inst)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), inst, "type.googleapis.com/google.bigtable.admin.v2.Instance")
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleBigtableReplaceInstance is the synchronous PUT update — it returns the
// Instance directly (not an LRO), matching instances.update.
func handleBigtableReplaceInstance(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"))
	if _, ok := bigtableInstances.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	var req bigtableInstance
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	req.Name = name
	if req.Type == "" {
		req.Type = "PRODUCTION"
	}
	req.State = "READY"
	bigtableInstances.Put(name, req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleBigtableDeleteInstance(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"))
	if !bigtableInstances.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	for _, cluster := range bigtableClusters.List() {
		if strings.HasPrefix(cluster.Name, name+"/clusters/") {
			bigtableClusters.Delete(cluster.Name)
			bigtableMemoryLayers.Delete(cluster.Name + "/memoryLayer")
		}
	}
	for _, table := range bigtableTables.List() {
		if strings.HasPrefix(table.Name, name+"/tables/") {
			bigtableTables.Delete(table.Name)
			btDeleteTableData(table.Name)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ----- Clusters -----

func handleBigtableCreateCluster(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	clusterID := r.URL.Query().Get("clusterId")
	if clusterID == "" {
		sim.GCPError(w, http.StatusBadRequest, "clusterId is required", "INVALID_ARGUMENT")
		return
	}
	var cluster bigtableCluster
	if err := sim.ReadJSON(r, &cluster); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	cluster.Name = bigtableClusterName(project, instance, clusterID)
	cluster.State = "READY"
	if cluster.DefaultStorageType == "" {
		cluster.DefaultStorageType = "SSD"
	}
	bigtableClusters.Put(cluster.Name, cluster)
	op := newBigtableAdminLRO(project, cluster, "type.googleapis.com/google.bigtable.admin.v2.Cluster")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListClusters(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/clusters/"
	out := bigtableClusters.Filter(func(cluster bigtableCluster) bool { return strings.HasPrefix(cluster.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"clusters": out})
}

func handleBigtableGetCluster(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	cluster, ok := bigtableClusters.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, cluster)
}

func handleBigtableUpdateCluster(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	cluster, ok := bigtableClusters.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	var req bigtableCluster
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.ServeNodes != 0 {
		cluster.ServeNodes = req.ServeNodes
	}
	cluster.State = "READY"
	bigtableClusters.Put(name, cluster)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), cluster, "type.googleapis.com/google.bigtable.admin.v2.Cluster")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtablePartialUpdateCluster(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	cluster, ok := bigtableClusters.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	var req bigtableCluster
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
		switch strings.TrimSpace(field) {
		case "serveNodes", "serve_nodes":
			cluster.ServeNodes = req.ServeNodes
		case "defaultStorageType", "default_storage_type":
			cluster.DefaultStorageType = req.DefaultStorageType
		}
	}
	cluster.State = "READY"
	bigtableClusters.Put(name, cluster)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), cluster, "type.googleapis.com/google.bigtable.admin.v2.Cluster")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteCluster(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	if !bigtableClusters.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	bigtableMemoryLayers.Delete(name + "/memoryLayer")
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBigtableListHotTablets(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	if _, ok := bigtableClusters.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	// A healthy cluster with no measured hotspots returns an empty list.
	sim.WriteJSON(w, http.StatusOK, map[string]any{"hotTablets": []any{}})
}

func handleBigtableGetMemoryLayer(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	if _, ok := bigtableClusters.Get(name); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, bigtableMemoryLayerForCluster(name))
}

func bigtableMemoryLayerForCluster(clusterName string) bigtableMemoryLayer {
	name := clusterName + "/memoryLayer"
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

func handleBigtableUpdateMemoryLayer(w http.ResponseWriter, r *http.Request) {
	clusterName := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster"))
	if _, ok := bigtableClusters.Get(clusterName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", clusterName)
		return
	}
	var req bigtableMemoryLayer
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	name := clusterName + "/memoryLayer"
	if req.Name != "" && req.Name != name {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "memory layer name %q does not match request path %q", req.Name, name)
		return
	}
	layer := bigtableMemoryLayerForCluster(clusterName)
	if req.Etag != "" && req.Etag != layer.Etag {
		sim.GCPError(w, http.StatusConflict, "etag mismatch", "ABORTED")
		return
	}
	mask := strings.TrimSpace(r.URL.Query().Get("updateMask"))
	if mask != "" {
		for _, field := range strings.Split(mask, ",") {
			switch strings.TrimSpace(field) {
			case "memoryConfig", "memory_config":
			default:
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "field %q cannot be updated", field)
				return
			}
		}
	}
	if req.MemoryConfig == nil {
		layer.MemoryConfig = nil
		layer.State = "DISABLED"
	} else {
		// storageSizeGib is output-only; Bigtable owns the measured capacity.
		layer.MemoryConfig = &bigtableMemoryConfig{}
		layer.State = "READY"
	}
	layer.Etag = gcpPolicyETag()
	bigtableMemoryLayers.Put(name, layer)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), layer, "type.googleapis.com/google.bigtable.admin.v2.MemoryLayer")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListMemoryLayers(w http.ResponseWriter, r *http.Request) {
	project, instance, cluster := sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")
	prefix := bigtableInstanceName(project, instance) + "/clusters/"
	clusters := []bigtableCluster{}
	if cluster == "-" {
		clusters = bigtableClusters.Filter(func(item bigtableCluster) bool {
			return strings.HasPrefix(item.Name, prefix)
		})
	} else {
		name := bigtableClusterName(project, instance, cluster)
		item, ok := bigtableClusters.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", name)
			return
		}
		clusters = append(clusters, item)
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })
	layers := make([]bigtableMemoryLayer, 0, len(clusters))
	for _, item := range clusters {
		layers = append(layers, bigtableMemoryLayerForCluster(item.Name))
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

// ----- Backups -----

func handleBigtableCreateBackup(w http.ResponseWriter, r *http.Request) {
	project, instance, cluster := sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")
	clusterName := bigtableClusterName(project, instance, cluster)
	if _, ok := bigtableClusters.Get(clusterName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "cluster %q not found", clusterName)
		return
	}
	backupID := r.URL.Query().Get("backupId")
	if backupID == "" {
		sim.GCPError(w, http.StatusBadRequest, "backupId is required", "INVALID_ARGUMENT")
		return
	}
	var body bigtableResource
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if body == nil {
		body = bigtableResource{}
	}
	name := clusterName + "/backups/" + backupID
	body["name"] = name
	body["state"] = "READY"
	// Backup has no etag field in the Discovery schema — do not set one.
	bigtableBackups.Put(name, body)
	op := newBigtableAdminLRO(project, map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.Backup")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListBackups(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + "/backups/"
	out := bigtableFilterResources(bigtableBackups, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"backups": out})
}

func handleBigtableGetBackup(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + "/backups/" + sim.PathParam(r, "backup")
	body, ok := bigtableBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchBackup(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + "/backups/" + sim.PathParam(r, "backup")
	body, ok := bigtableBackups.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	var patch bigtableResource
	if err := sim.ReadJSON(r, &patch); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	bigtableBackups.Put(name, body)
	// backups.patch returns the Backup directly (not an LRO).
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtableDeleteBackup(w http.ResponseWriter, r *http.Request) {
	name := bigtableClusterName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")) + "/backups/" + sim.PathParam(r, "backup")
	if !bigtableBackups.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "backup %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleBigtableBackupCollectionAction dispatches "backups:copy", which rides
// the colon on the literal "backups" collection segment.
func handleBigtableBackupCollectionAction(w http.ResponseWriter, r *http.Request) {
	project, instance, cluster := sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")
	coll, verb := bigtableSplitColonVerb(sim.PathParam(r, "backupsColl"))
	if coll != "backups" || verb != "copy" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown collection action %q", sim.PathParam(r, "backupsColl"))
		return
	}
	clusterName := bigtableClusterName(project, instance, cluster)
	var req struct {
		BackupID     string `json:"backupId"`
		SourceBackup string `json:"sourceBackup"`
		ExpireTime   string `json:"expireTime"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.BackupID == "" {
		sim.GCPError(w, http.StatusBadRequest, "backupId is required", "INVALID_ARGUMENT")
		return
	}
	name := clusterName + "/backups/" + req.BackupID
	body := bigtableResource{
		"name":         name,
		"sourceBackup": req.SourceBackup,
		"expireTime":   req.ExpireTime,
		"state":        "READY",
	}
	bigtableBackups.Put(name, body)
	op := newBigtableAdminLRO(project, map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.Backup")
	sim.WriteJSON(w, http.StatusOK, op)
}

// handleBigtableBackupItemAction dispatches the IAM colon verbs that ride the
// backups item segment: "{backup}:getIamPolicy|setIamPolicy|testIamPermissions".
func handleBigtableBackupItemAction(w http.ResponseWriter, r *http.Request) {
	project, instance, cluster := sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "cluster")
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "backupAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown backup action %q", sim.PathParam(r, "backupAction"))
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), bigtableClusterName(project, instance, cluster)+"/backups/"+id, verb)
}

// ----- App profiles -----

func handleBigtableCreateAppProfile(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	appProfileID := r.URL.Query().Get("appProfileId")
	if appProfileID == "" {
		sim.GCPError(w, http.StatusBadRequest, "appProfileId is required", "INVALID_ARGUMENT")
		return
	}
	var body bigtableResource
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if body == nil {
		body = bigtableResource{}
	}
	name := bigtableInstanceName(project, instance) + "/appProfiles/" + appProfileID
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableAppProfiles.Put(name, body)
	// appProfiles.create returns the AppProfile directly (not an LRO).
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtableListAppProfiles(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/appProfiles/"
	out := bigtableFilterResources(bigtableAppProfiles, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"appProfiles": out})
}

func handleBigtableGetAppProfile(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/appProfiles/" + sim.PathParam(r, "appProfile")
	body, ok := bigtableAppProfiles.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "app profile %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchAppProfile(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/appProfiles/" + sim.PathParam(r, "appProfile")
	body, ok := bigtableAppProfiles.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "app profile %q not found", name)
		return
	}
	var patch bigtableResource
	if err := sim.ReadJSON(r, &patch); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableAppProfiles.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.AppProfile")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteAppProfile(w http.ResponseWriter, r *http.Request) {
	name := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/appProfiles/" + sim.PathParam(r, "appProfile")
	if !bigtableAppProfiles.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "app profile %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ----- Tables -----

func handleBigtableCreateTable(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	var req struct {
		TableID string        `json:"tableId"`
		Table   bigtableTable `json:"table"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.TableID == "" {
		sim.GCPError(w, http.StatusBadRequest, "tableId is required", "INVALID_ARGUMENT")
		return
	}
	table := req.Table
	table.Name = bigtableTableName(project, instance, req.TableID)
	if table.Granularity == "" {
		table.Granularity = "MILLIS"
	}
	if table.ColumnFamilies == nil {
		table.ColumnFamilies = map[string]map[string]any{}
	}
	if table.ClusterStates == nil {
		table.ClusterStates = map[string]map[string]any{}
	}
	for _, cluster := range bigtableClusters.List() {
		if strings.HasPrefix(cluster.Name, bigtableInstanceName(project, instance)+"/clusters/") {
			table.ClusterStates[cluster.Name] = map[string]any{"replicationState": "READY"}
		}
	}
	bigtableTables.Put(table.Name, table)
	sim.WriteJSON(w, http.StatusOK, table)
}

func handleBigtableListTables(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/tables/"
	out := bigtableTables.Filter(func(table bigtableTable) bool { return strings.HasPrefix(table.Name, prefix) })
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"tables": out})
}

// handleBigtableTableCollectionAction dispatches "tables:restore", which rides
// the colon on the literal "tables" collection segment.
func handleBigtableTableCollectionAction(w http.ResponseWriter, r *http.Request) {
	coll, verb := bigtableSplitColonVerb(sim.PathParam(r, "tablesColl"))
	if coll != "tables" || verb != "restore" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown collection action %q", sim.PathParam(r, "tablesColl"))
		return
	}
	handleBigtableRestoreTable(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "instance"))
}

// handleBigtableTableAction dispatches the colon verbs riding the table item
// segment (modifyColumnFamilies / dropRowRange / generateConsistencyToken /
// checkConsistency / undelete / IAM).
func handleBigtableTableAction(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "tableAction"))
	if verb == "" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown table action %q", sim.PathParam(r, "tableAction"))
		return
	}
	name := bigtableTableName(project, instance, id)
	switch verb {
	case "getIamPolicy", "setIamPolicy", "testIamPermissions":
		handleResourceIAM(w, r, gcpResourceIAMStore(), name, verb)
		return
	}
	table, ok := bigtableTables.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", name)
		return
	}
	switch verb {
	case "modifyColumnFamilies":
		handleBigtableModifyColumnFamilies(w, r, name, table)
	case "dropRowRange":
		// Drops the requested rows; the sim doesn't model row data, so it
		// acknowledges with an empty body (the documented response type).
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	case "generateConsistencyToken":
		sim.WriteJSON(w, http.StatusOK, map[string]any{"consistencyToken": generateUUID()})
	case "checkConsistency":
		sim.WriteJSON(w, http.StatusOK, map[string]any{"consistent": true})
	case "undelete":
		op := newBigtableAdminLRO(project, table, "type.googleapis.com/google.bigtable.admin.v2.Table")
		sim.WriteJSON(w, http.StatusOK, op)
	default:
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown table action %q", sim.PathParam(r, "tableAction"))
	}
}

func handleBigtableModifyColumnFamilies(w http.ResponseWriter, r *http.Request, name string, table bigtableTable) {
	var req struct {
		Modifications []struct {
			ID     string         `json:"id"`
			Create map[string]any `json:"create"`
			Update map[string]any `json:"update"`
			Drop   bool           `json:"drop"`
		} `json:"modifications"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if table.ColumnFamilies == nil {
		table.ColumnFamilies = map[string]map[string]any{}
	}
	for _, mod := range req.Modifications {
		switch {
		case mod.Drop:
			delete(table.ColumnFamilies, mod.ID)
		case mod.Create != nil:
			table.ColumnFamilies[mod.ID] = mod.Create
		case mod.Update != nil:
			table.ColumnFamilies[mod.ID] = mod.Update
		}
	}
	bigtableTables.Put(name, table)
	sim.WriteJSON(w, http.StatusOK, table)
}

func handleBigtableRestoreTable(w http.ResponseWriter, r *http.Request, project, instance string) {
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	var req struct {
		TableID string `json:"tableId"`
		Backup  string `json:"backup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if req.TableID == "" {
		sim.GCPError(w, http.StatusBadRequest, "tableId is required", "INVALID_ARGUMENT")
		return
	}
	table := bigtableTable{
		Name:           bigtableTableName(project, instance, req.TableID),
		Granularity:    "MILLIS",
		ColumnFamilies: map[string]map[string]any{},
		ClusterStates:  map[string]map[string]any{},
	}
	bigtableTables.Put(table.Name, table)
	op := newBigtableAdminLRO(project, table, "type.googleapis.com/google.bigtable.admin.v2.Table")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableGetTable(w http.ResponseWriter, r *http.Request) {
	name := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table"))
	table, ok := bigtableTables.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, table)
}

func handleBigtablePatchTable(w http.ResponseWriter, r *http.Request) {
	name := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table"))
	table, ok := bigtableTables.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", name)
		return
	}
	var req bigtableTable
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	for _, field := range strings.Split(r.URL.Query().Get("updateMask"), ",") {
		switch strings.TrimSpace(field) {
		case "deletionProtection", "deletion_protection":
			table.DeletionProtection = req.DeletionProtection
		}
	}
	bigtableTables.Put(name, table)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), table, "type.googleapis.com/google.bigtable.admin.v2.Table")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteTable(w http.ResponseWriter, r *http.Request) {
	name := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table"))
	if !bigtableTables.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", name)
		return
	}
	btDeleteTableData(name)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// ----- Authorized views (table-scoped) -----

func bigtableAuthViewName(r *http.Request, view string) string {
	return bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table")) + "/authorizedViews/" + view
}

func handleBigtableCreateAuthView(w http.ResponseWriter, r *http.Request) {
	tableName := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table"))
	if _, ok := bigtableTables.Get(tableName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", tableName)
		return
	}
	viewID := r.URL.Query().Get("authorizedViewId")
	if viewID == "" {
		sim.GCPError(w, http.StatusBadRequest, "authorizedViewId is required", "INVALID_ARGUMENT")
		return
	}
	body := bigtableReadResourceBody(w, r)
	if body == nil {
		return
	}
	name := tableName + "/authorizedViews/" + viewID
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableAuthViews.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.AuthorizedView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListAuthViews(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table")) + "/authorizedViews/"
	out := bigtableFilterResources(bigtableAuthViews, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"authorizedViews": out})
}

func handleBigtableGetAuthView(w http.ResponseWriter, r *http.Request) {
	name := bigtableAuthViewName(r, sim.PathParam(r, "authView"))
	body, ok := bigtableAuthViews.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authorized view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchAuthView(w http.ResponseWriter, r *http.Request) {
	name := bigtableAuthViewName(r, sim.PathParam(r, "authView"))
	body, ok := bigtableAuthViews.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authorized view %q not found", name)
		return
	}
	patch := bigtableReadResourceBody(w, r)
	if patch == nil {
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableAuthViews.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.AuthorizedView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteAuthView(w http.ResponseWriter, r *http.Request) {
	name := bigtableAuthViewName(r, sim.PathParam(r, "authView"))
	if !bigtableAuthViews.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "authorized view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBigtableAuthViewItemAction(w http.ResponseWriter, r *http.Request) {
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "authViewAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown authorized view action %q", sim.PathParam(r, "authViewAction"))
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), bigtableAuthViewName(r, id), verb)
}

// ----- Schema bundles (table-scoped) -----

func bigtableSchemaBundleName(r *http.Request, bundle string) string {
	return bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table")) + "/schemaBundles/" + bundle
}

func handleBigtableCreateSchemaBundle(w http.ResponseWriter, r *http.Request) {
	tableName := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table"))
	if _, ok := bigtableTables.Get(tableName); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "table %q not found", tableName)
		return
	}
	bundleID := r.URL.Query().Get("schemaBundleId")
	if bundleID == "" {
		sim.GCPError(w, http.StatusBadRequest, "schemaBundleId is required", "INVALID_ARGUMENT")
		return
	}
	body := bigtableReadResourceBody(w, r)
	if body == nil {
		return
	}
	name := tableName + "/schemaBundles/" + bundleID
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableSchemaBundle.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.SchemaBundle")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListSchemaBundles(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableTableName(sim.PathParam(r, "project"), sim.PathParam(r, "instance"), sim.PathParam(r, "table")) + "/schemaBundles/"
	out := bigtableFilterResources(bigtableSchemaBundle, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"schemaBundles": out})
}

func handleBigtableGetSchemaBundle(w http.ResponseWriter, r *http.Request) {
	name := bigtableSchemaBundleName(r, sim.PathParam(r, "schemaBundle"))
	body, ok := bigtableSchemaBundle.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "schema bundle %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchSchemaBundle(w http.ResponseWriter, r *http.Request) {
	name := bigtableSchemaBundleName(r, sim.PathParam(r, "schemaBundle"))
	body, ok := bigtableSchemaBundle.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "schema bundle %q not found", name)
		return
	}
	patch := bigtableReadResourceBody(w, r)
	if patch == nil {
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableSchemaBundle.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.SchemaBundle")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteSchemaBundle(w http.ResponseWriter, r *http.Request) {
	name := bigtableSchemaBundleName(r, sim.PathParam(r, "schemaBundle"))
	if !bigtableSchemaBundle.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "schema bundle %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBigtableSchemaBundleItemAction(w http.ResponseWriter, r *http.Request) {
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "schemaBundleAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown schema bundle action %q", sim.PathParam(r, "schemaBundleAction"))
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), bigtableSchemaBundleName(r, id), verb)
}

// ----- Logical views (instance-scoped) -----

func bigtableLogicalViewName(r *http.Request, view string) string {
	return bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/logicalViews/" + view
}

func handleBigtableCreateLogicalView(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	viewID := r.URL.Query().Get("logicalViewId")
	if viewID == "" {
		sim.GCPError(w, http.StatusBadRequest, "logicalViewId is required", "INVALID_ARGUMENT")
		return
	}
	body := bigtableReadResourceBody(w, r)
	if body == nil {
		return
	}
	name := bigtableLogicalViewName(r, viewID)
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableLogicalView.Put(name, body)
	op := newBigtableAdminLRO(project, map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.LogicalView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListLogicalViews(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/logicalViews/"
	out := bigtableFilterResources(bigtableLogicalView, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"logicalViews": out})
}

func handleBigtableGetLogicalView(w http.ResponseWriter, r *http.Request) {
	name := bigtableLogicalViewName(r, sim.PathParam(r, "logicalView"))
	body, ok := bigtableLogicalView.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logical view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchLogicalView(w http.ResponseWriter, r *http.Request) {
	name := bigtableLogicalViewName(r, sim.PathParam(r, "logicalView"))
	body, ok := bigtableLogicalView.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logical view %q not found", name)
		return
	}
	patch := bigtableReadResourceBody(w, r)
	if patch == nil {
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableLogicalView.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.LogicalView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteLogicalView(w http.ResponseWriter, r *http.Request) {
	name := bigtableLogicalViewName(r, sim.PathParam(r, "logicalView"))
	if !bigtableLogicalView.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "logical view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBigtableLogicalViewItemAction(w http.ResponseWriter, r *http.Request) {
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "logicalViewAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown logical view action %q", sim.PathParam(r, "logicalViewAction"))
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), bigtableLogicalViewName(r, id), verb)
}

// ----- Materialized views (instance-scoped) -----

func bigtableMatViewName(r *http.Request, view string) string {
	return bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/materializedViews/" + view
}

func handleBigtableCreateMatView(w http.ResponseWriter, r *http.Request) {
	project, instance := sim.PathParam(r, "project"), sim.PathParam(r, "instance")
	if _, ok := bigtableInstances.Get(bigtableInstanceName(project, instance)); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", instance)
		return
	}
	viewID := r.URL.Query().Get("materializedViewId")
	if viewID == "" {
		sim.GCPError(w, http.StatusBadRequest, "materializedViewId is required", "INVALID_ARGUMENT")
		return
	}
	body := bigtableReadResourceBody(w, r)
	if body == nil {
		return
	}
	name := bigtableMatViewName(r, viewID)
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableMatView.Put(name, body)
	op := newBigtableAdminLRO(project, map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.MaterializedView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableListMatViews(w http.ResponseWriter, r *http.Request) {
	prefix := bigtableInstanceName(sim.PathParam(r, "project"), sim.PathParam(r, "instance")) + "/materializedViews/"
	out := bigtableFilterResources(bigtableMatView, prefix)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"materializedViews": out})
}

func handleBigtableGetMatView(w http.ResponseWriter, r *http.Request) {
	name := bigtableMatViewName(r, sim.PathParam(r, "matView"))
	body, ok := bigtableMatView.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "materialized view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handleBigtablePatchMatView(w http.ResponseWriter, r *http.Request) {
	name := bigtableMatViewName(r, sim.PathParam(r, "matView"))
	body, ok := bigtableMatView.Get(name)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "materialized view %q not found", name)
		return
	}
	patch := bigtableReadResourceBody(w, r)
	if patch == nil {
		return
	}
	bigtableApplyUpdateMask(body, patch, r.URL.Query().Get("updateMask"))
	body["name"] = name
	body["etag"] = gcpPolicyETag()
	bigtableMatView.Put(name, body)
	op := newBigtableAdminLRO(sim.PathParam(r, "project"), map[string]any(body), "type.googleapis.com/google.bigtable.admin.v2.MaterializedView")
	sim.WriteJSON(w, http.StatusOK, op)
}

func handleBigtableDeleteMatView(w http.ResponseWriter, r *http.Request) {
	name := bigtableMatViewName(r, sim.PathParam(r, "matView"))
	if !bigtableMatView.Delete(name) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "materialized view %q not found", name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBigtableMatViewItemAction(w http.ResponseWriter, r *http.Request) {
	id, verb := bigtableSplitColonVerb(sim.PathParam(r, "matViewAction"))
	if verb != "getIamPolicy" && verb != "setIamPolicy" && verb != "testIamPermissions" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown materialized view action %q", sim.PathParam(r, "matViewAction"))
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), bigtableMatViewName(r, id), verb)
}

// ----- shared resource helpers -----

// bigtableReadResourceBody decodes a pass-through resource body, writing an
// INVALID_ARGUMENT error and returning nil on failure.
func bigtableReadResourceBody(w http.ResponseWriter, r *http.Request) bigtableResource {
	var body bigtableResource
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return nil
	}
	if body == nil {
		body = bigtableResource{}
	}
	return body
}

// bigtableFilterResources returns the stored resources whose name has the given
// prefix, sorted by name.
func bigtableFilterResources(store sim.Store[bigtableResource], prefix string) []bigtableResource {
	out := store.Filter(func(res bigtableResource) bool {
		name, _ := res["name"].(string)
		return strings.HasPrefix(name, prefix)
	})
	sort.Slice(out, func(i, j int) bool {
		a, _ := out[i]["name"].(string)
		b, _ := out[j]["name"].(string)
		return a < b
	})
	return out
}

// bigtableApplyUpdateMask merges the masked fields of patch into body. An empty
// mask applies every field present in patch (server-managed fields excepted).
func bigtableApplyUpdateMask(body, patch bigtableResource, mask string) {
	mask = strings.TrimSpace(mask)
	if mask == "" || mask == "*" {
		for k, v := range patch {
			if k == "name" || k == "etag" {
				continue
			}
			body[k] = v
		}
		return
	}
	for _, field := range strings.Split(mask, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		// Only the top-level field name is honored; nested paths (a.b) map to
		// their root field in this pass-through store.
		root := field
		if i := strings.IndexByte(field, '.'); i >= 0 {
			root = field[:i]
		}
		if v, ok := patch[root]; ok {
			body[root] = v
		}
	}
}
