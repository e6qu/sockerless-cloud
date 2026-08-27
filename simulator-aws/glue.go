package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/google/uuid"
)

// AWS Glue — AWS JSON 1.1 protocol (X-Amz-Target: AWSGlue.<Op>).
// Python shell job runs execute the script stored at the job's S3 script
// location, in a container running the interpreter the job command names.

// glueJobScriptDir is where a Python shell job's script is mounted inside its
// job container, and the working directory the script runs from.
const glueJobScriptDir = "/tmp/glue-script"

type GlueDatabase struct {
	Name                          string            `json:"Name"`
	CatalogId                     string            `json:"CatalogId"`
	Parameters                    map[string]string `json:"Parameters"`
	CreateTime                    float64           `json:"CreateTime"`
	LocationUri                   string            `json:"LocationUri,omitempty"`
	Description                   string            `json:"Description,omitempty"`
	Tags                          map[string]string `json:"-"`
	CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
}

// glueDatabaseWire omits the persistence-only Tags member. AWS Glue accepts
// database tags during creation and exposes them through GetTags, but its
// public Database shape does not contain Tags.
type glueDatabaseWire struct {
	Name                          string            `json:"Name"`
	CatalogId                     string            `json:"CatalogId"`
	Parameters                    map[string]string `json:"Parameters"`
	CreateTime                    float64           `json:"CreateTime"`
	LocationUri                   string            `json:"LocationUri,omitempty"`
	Description                   string            `json:"Description,omitempty"`
	CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
}

func glueDatabaseResponse(db GlueDatabase) glueDatabaseWire {
	return glueDatabaseWire{
		Name:                          db.Name,
		CatalogId:                     db.CatalogId,
		Parameters:                    db.Parameters,
		CreateTime:                    db.CreateTime,
		LocationUri:                   db.LocationUri,
		Description:                   db.Description,
		CreateTableDefaultPermissions: db.CreateTableDefaultPermissions,
	}
}

type GlueTable struct {
	Name              string            `json:"Name"`
	DatabaseName      string            `json:"DatabaseName"`
	StorageDescriptor map[string]any    `json:"StorageDescriptor,omitempty"`
	PartitionKeys     []map[string]any  `json:"PartitionKeys,omitempty"`
	Parameters        map[string]string `json:"Parameters,omitempty"`
	CreateTime        float64           `json:"CreateTime"`
	UpdateTime        float64           `json:"UpdateTime"`
	TableType         string            `json:"TableType,omitempty"`
}

type GlueJob struct {
	Name             string            `json:"Name"`
	Description      string            `json:"Description,omitempty"`
	Role             string            `json:"Role"`
	Command          map[string]any    `json:"Command"`
	DefaultArguments map[string]string `json:"DefaultArguments,omitempty"`
	GlueVersion      string            `json:"GlueVersion,omitempty"`
	MaxCapacity      *float64          `json:"MaxCapacity,omitempty"`
	WorkerType       string            `json:"WorkerType,omitempty"`
	NumberOfWorkers  *int              `json:"NumberOfWorkers,omitempty"`
	CreatedOn        float64           `json:"CreatedOn"`
	LastModifiedOn   float64           `json:"LastModifiedOn"`
	Tags             map[string]string `json:"Tags,omitempty"`
}

type GlueJobRun struct {
	ID            string            `json:"Id"`
	JobName       string            `json:"JobName"`
	JobRunState   string            `json:"JobRunState"`
	StartedOn     float64           `json:"StartedOn"`
	CompletedOn   float64           `json:"CompletedOn"`
	ExecutionTime int               `json:"ExecutionTime"`
	Arguments     map[string]string `json:"Arguments,omitempty"`
	ErrorMessage  string            `json:"ErrorMessage,omitempty"`
}

// GluePartition models a Data Catalog partition belonging to a table.
type GluePartition struct {
	Values            []string          `json:"Values,omitempty"`
	DatabaseName      string            `json:"DatabaseName"`
	TableName         string            `json:"TableName"`
	StorageDescriptor map[string]any    `json:"StorageDescriptor,omitempty"`
	Parameters        map[string]string `json:"Parameters,omitempty"`
	CreationTime      float64           `json:"CreationTime"`
	LastAccessTime    *float64          `json:"LastAccessTime,omitempty"`
	LastAnalyzedTime  *float64          `json:"LastAnalyzedTime,omitempty"`
}

// GlueCrawler models a Data Catalog crawler keyed by name.
type GlueCrawler struct {
	Name         string            `json:"Name"`
	Role         string            `json:"Role"`
	Targets      map[string]any    `json:"Targets,omitempty"`
	DatabaseName string            `json:"DatabaseName,omitempty"`
	Description  string            `json:"Description,omitempty"`
	Classifiers  []string          `json:"Classifiers,omitempty"`
	TablePrefix  string            `json:"TablePrefix,omitempty"`
	Schedule     *GlueSchedule     `json:"Schedule,omitempty"`
	State        string            `json:"State"`
	CreationTime float64           `json:"CreationTime"`
	LastUpdated  float64           `json:"LastUpdated"`
	Version      int               `json:"Version"`
	Tags         map[string]string `json:"Tags,omitempty"`
}

// GlueSchedule mirrors the Glue Schedule structure returned in a Crawler.
type GlueSchedule struct {
	ScheduleExpression string `json:"ScheduleExpression,omitempty"`
	State              string `json:"State,omitempty"`
}

// GlueTrigger models a Glue workflow trigger keyed by name.
type GlueTrigger struct {
	ID           string            `json:"Id,omitempty"`
	Name         string            `json:"Name"`
	WorkflowName string            `json:"WorkflowName,omitempty"`
	Type         string            `json:"Type"`
	State        string            `json:"State"`
	Description  string            `json:"Description,omitempty"`
	Schedule     string            `json:"Schedule,omitempty"`
	Actions      []map[string]any  `json:"Actions,omitempty"`
	Predicate    map[string]any    `json:"Predicate,omitempty"`
	Tags         map[string]string `json:"Tags,omitempty"`
}

// GlueConnection models a Data Catalog connection keyed by name.
type GlueConnection struct {
	Name                           string            `json:"Name"`
	Description                    string            `json:"Description,omitempty"`
	ConnectionType                 string            `json:"ConnectionType,omitempty"`
	MatchCriteria                  []string          `json:"MatchCriteria,omitempty"`
	ConnectionProperties           map[string]string `json:"ConnectionProperties,omitempty"`
	PhysicalConnectionRequirements map[string]any    `json:"PhysicalConnectionRequirements,omitempty"`
	CreationTime                   float64           `json:"CreationTime"`
	LastUpdatedTime                float64           `json:"LastUpdatedTime"`
}

// GlueSecurityConfiguration models a security configuration keyed by name.
// EncryptionConfiguration is stored verbatim to preserve the (nested) wire shape.
type GlueSecurityConfiguration struct {
	Name                    string         `json:"Name"`
	CreatedTimeStamp        float64        `json:"CreatedTimeStamp"`
	EncryptionConfiguration map[string]any `json:"EncryptionConfiguration,omitempty"`
}

// GlueWorkflow models a workflow keyed by name.
type GlueWorkflow struct {
	Name                 string            `json:"Name"`
	Description          string            `json:"Description,omitempty"`
	DefaultRunProperties map[string]string `json:"DefaultRunProperties,omitempty"`
	CreatedOn            float64           `json:"CreatedOn"`
	LastModifiedOn       float64           `json:"LastModifiedOn"`
	MaxConcurrentRuns    *int              `json:"MaxConcurrentRuns,omitempty"`
	Tags                 map[string]string `json:"Tags,omitempty"`
}

// GlueWorkflowRun models a single execution of a workflow.
type GlueWorkflowRun struct {
	Name                  string            `json:"Name"`
	WorkflowRunId         string            `json:"WorkflowRunId"`
	WorkflowRunProperties map[string]string `json:"WorkflowRunProperties,omitempty"`
	StartedOn             float64           `json:"StartedOn"`
	CompletedOn           float64           `json:"CompletedOn"`
	Status                string            `json:"Status"`
}

// GlueClassifier models a classifier. Exactly one of the four sub-objects is set,
// matching the Classifier union shape; each sub-object is stored verbatim.
type GlueClassifier struct {
	GrokClassifier map[string]any `json:"GrokClassifier,omitempty"`
	XMLClassifier  map[string]any `json:"XMLClassifier,omitempty"`
	JsonClassifier map[string]any `json:"JsonClassifier,omitempty"`
	CsvClassifier  map[string]any `json:"CsvClassifier,omitempty"`
}

// GlueUserDefinedFunction models a Data Catalog user-defined function,
// keyed by database name + function name.
type GlueUserDefinedFunction struct {
	FunctionName string           `json:"FunctionName"`
	DatabaseName string           `json:"DatabaseName"`
	ClassName    string           `json:"ClassName,omitempty"`
	OwnerName    string           `json:"OwnerName,omitempty"`
	OwnerType    string           `json:"OwnerType,omitempty"`
	FunctionType string           `json:"FunctionType,omitempty"`
	CreateTime   float64          `json:"CreateTime"`
	ResourceUris []map[string]any `json:"ResourceUris,omitempty"`
	CatalogId    string           `json:"CatalogId,omitempty"`
}

// GlueRegistry models a schema registry keyed by name.
type GlueRegistry struct {
	RegistryName string            `json:"RegistryName"`
	RegistryArn  string            `json:"RegistryArn"`
	Description  string            `json:"Description,omitempty"`
	Status       string            `json:"Status"`
	CreatedTime  string            `json:"CreatedTime"`
	UpdatedTime  string            `json:"UpdatedTime"`
	Tags         map[string]string `json:"Tags,omitempty"`
}

// GlueSchema models a schema keyed by registry name + schema name.
type GlueSchema struct {
	RegistryName        string            `json:"RegistryName"`
	RegistryArn         string            `json:"RegistryArn"`
	SchemaName          string            `json:"SchemaName"`
	SchemaArn           string            `json:"SchemaArn"`
	Description         string            `json:"Description,omitempty"`
	DataFormat          string            `json:"DataFormat"`
	Compatibility       string            `json:"Compatibility,omitempty"`
	SchemaCheckpoint    int64             `json:"SchemaCheckpoint"`
	LatestSchemaVersion int64             `json:"LatestSchemaVersion"`
	NextSchemaVersion   int64             `json:"NextSchemaVersion"`
	SchemaStatus        string            `json:"SchemaStatus"`
	CreatedTime         string            `json:"CreatedTime"`
	UpdatedTime         string            `json:"UpdatedTime"`
	SchemaVersionId     string            `json:"SchemaVersionId,omitempty"`
	Tags                map[string]string `json:"Tags,omitempty"`
}

// GlueTableVersion captures one immutable snapshot of a table at a version.
// VersionId is the string form of a monotonically increasing integer.
type GlueTableVersion struct {
	DatabaseName string    `json:"DatabaseName"`
	TableName    string    `json:"TableName"`
	VersionId    string    `json:"VersionId"`
	Table        GlueTable `json:"Table"`
}

// GluePartitionIndex models a partition index attached to a table. Keys mirror
// the KeySchemaElementList descriptor shape (Name + Type).
type GluePartitionIndex struct {
	DatabaseName string           `json:"DatabaseName"`
	TableName    string           `json:"TableName"`
	IndexName    string           `json:"IndexName"`
	Keys         []map[string]any `json:"Keys"`
	IndexStatus  string           `json:"IndexStatus"`
}

// GlueColumnStatisticsRecord stores the verbatim ColumnStatistics object for one
// column, scoped to a table (PartitionValues empty) or a partition.
type GlueColumnStatisticsRecord struct {
	DatabaseName    string         `json:"DatabaseName"`
	TableName       string         `json:"TableName"`
	PartitionValues []string       `json:"PartitionValues,omitempty"`
	ColumnName      string         `json:"ColumnName"`
	Statistics      map[string]any `json:"Statistics"`
}

// GlueResourcePolicy holds the Data Catalog resource policy. The Glue catalog
// has a single resource policy per account/region, so this store carries at most
// one row under glueResourcePolicyKey.
type GlueResourcePolicy struct {
	PolicyInJson string  `json:"PolicyInJson"`
	PolicyHash   string  `json:"PolicyHash"`
	CreateTime   float64 `json:"CreateTime"`
	UpdateTime   float64 `json:"UpdateTime"`
}

// GlueCatalogSettings holds the Data Catalog encryption settings (one row per
// catalog) plus the catalog import status.
type GlueCatalogSettings struct {
	DataCatalogEncryptionSettings map[string]any `json:"DataCatalogEncryptionSettings,omitempty"`
	ImportCompleted               bool           `json:"ImportCompleted"`
	ImportTime                    *float64       `json:"ImportTime,omitempty"`
	ImportedBy                    string         `json:"ImportedBy,omitempty"`
}

// GlueSchemaVersion models one registered version of a schema, keyed by its
// SchemaVersionId.
type GlueSchemaVersion struct {
	SchemaVersionId  string `json:"SchemaVersionId"`
	SchemaArn        string `json:"SchemaArn"`
	RegistryName     string `json:"RegistryName"`
	SchemaName       string `json:"SchemaName"`
	VersionNumber    int64  `json:"VersionNumber"`
	SchemaDefinition string `json:"SchemaDefinition"`
	DataFormat       string `json:"DataFormat"`
	Status           string `json:"Status"`
	CreatedTime      string `json:"CreatedTime"`
}

var (
	glueDatabases     sim.Store[GlueDatabase]
	glueTables        sim.Store[GlueTable]
	glueTableVersions sim.Store[GlueTableVersion]
	gluePartitions    sim.Store[GluePartition]
	gluePartIndexes   sim.Store[GluePartitionIndex]
	glueColumnStats   sim.Store[GlueColumnStatisticsRecord]
	glueJobs          sim.Store[GlueJob]
	glueJobRuns       sim.Store[GlueJobRun]
	glueCrawlers      sim.Store[GlueCrawler]
	glueTriggers      sim.Store[GlueTrigger]
	glueConnections   sim.Store[GlueConnection]
	glueSecConfigs    sim.Store[GlueSecurityConfiguration]
	glueWorkflows     sim.Store[GlueWorkflow]
	glueWfRuns        sim.Store[GlueWorkflowRun]
	glueClassifiers   sim.Store[GlueClassifier]
	glueUDFs          sim.Store[GlueUserDefinedFunction]
	glueRegistries    sim.Store[GlueRegistry]
	glueSchemas       sim.Store[GlueSchema]
	glueSchemaVers    sim.Store[GlueSchemaVersion]
	glueResourcePols  sim.Store[GlueResourcePolicy]
	glueCatalogSettgs sim.Store[GlueCatalogSettings]
	// glueMu guards the Glue catalog stores across operations. It is a
	// read-write lock because the read handlers — every Get and Search below —
	// exclude nothing but a writer, and a catalog read is most of what this
	// service serves. A section that writes, or reads and then writes based on
	// what it read, must keep taking Lock for the whole span; neither is
	// reentrant.
	glueMu sync.RWMutex
)

const glueResourcePolicyKey = "catalog"

// glueCatalogSettingsKey is the single store key for catalog-wide settings.
const glueCatalogSettingsKey = "catalog"

func registerGlue(r *sim.AWSRouter, srv *sim.Server) {
	glueDatabases = sim.MakeStore[GlueDatabase](srv.DB(), "glue_databases")
	glueTables = sim.MakeStore[GlueTable](srv.DB(), "glue_tables")
	glueTableVersions = sim.MakeStore[GlueTableVersion](srv.DB(), "glue_table_versions")
	gluePartitions = sim.MakeStore[GluePartition](srv.DB(), "glue_partitions")
	gluePartIndexes = sim.MakeStore[GluePartitionIndex](srv.DB(), "glue_partition_indexes")
	glueColumnStats = sim.MakeStore[GlueColumnStatisticsRecord](srv.DB(), "glue_column_statistics")
	glueJobs = sim.MakeStore[GlueJob](srv.DB(), "glue_jobs")
	glueJobRuns = sim.MakeStore[GlueJobRun](srv.DB(), "glue_job_runs")
	glueCrawlers = sim.MakeStore[GlueCrawler](srv.DB(), "glue_crawlers")
	glueTriggers = sim.MakeStore[GlueTrigger](srv.DB(), "glue_triggers")
	glueConnections = sim.MakeStore[GlueConnection](srv.DB(), "glue_connections")
	glueSecConfigs = sim.MakeStore[GlueSecurityConfiguration](srv.DB(), "glue_security_configs")
	glueWorkflows = sim.MakeStore[GlueWorkflow](srv.DB(), "glue_workflows")
	glueWfRuns = sim.MakeStore[GlueWorkflowRun](srv.DB(), "glue_workflow_runs")
	glueClassifiers = sim.MakeStore[GlueClassifier](srv.DB(), "glue_classifiers")
	glueUDFs = sim.MakeStore[GlueUserDefinedFunction](srv.DB(), "glue_user_defined_functions")
	glueRegistries = sim.MakeStore[GlueRegistry](srv.DB(), "glue_registries")
	glueSchemas = sim.MakeStore[GlueSchema](srv.DB(), "glue_schemas")
	glueSchemaVers = sim.MakeStore[GlueSchemaVersion](srv.DB(), "glue_schema_versions")
	glueResourcePols = sim.MakeStore[GlueResourcePolicy](srv.DB(), "glue_resource_policies")
	glueCatalogSettgs = sim.MakeStore[GlueCatalogSettings](srv.DB(), "glue_catalog_settings")
	registerGlueCatalogExport(r, srv)

	r.Register("AWSGlue.CreateDatabase", handleGlueCreateDatabase)
	r.Register("AWSGlue.GetDatabase", handleGlueGetDatabase)
	r.Register("AWSGlue.GetDatabases", handleGlueGetDatabases)
	r.Register("AWSGlue.UpdateDatabase", handleGlueUpdateDatabase)
	r.Register("AWSGlue.DeleteDatabase", handleGlueDeleteDatabase)
	r.Register("AWSGlue.CreateTable", handleGlueCreateTable)
	r.Register("AWSGlue.GetTable", handleGlueGetTable)
	r.Register("AWSGlue.GetTables", handleGlueGetTables)
	r.Register("AWSGlue.UpdateTable", handleGlueUpdateTable)
	r.Register("AWSGlue.DeleteTable", handleGlueDeleteTable)
	r.Register("AWSGlue.BatchDeleteTable", handleGlueBatchDeleteTable)
	r.Register("AWSGlue.CreatePartition", handleGlueCreatePartition)
	r.Register("AWSGlue.BatchCreatePartition", handleGlueBatchCreatePartition)
	r.Register("AWSGlue.GetPartition", handleGlueGetPartition)
	r.Register("AWSGlue.GetPartitions", handleGlueGetPartitions)
	r.Register("AWSGlue.BatchGetPartition", handleGlueBatchGetPartition)
	r.Register("AWSGlue.UpdatePartition", handleGlueUpdatePartition)
	r.Register("AWSGlue.DeletePartition", handleGlueDeletePartition)
	r.Register("AWSGlue.BatchDeletePartition", handleGlueBatchDeletePartition)
	r.Register("AWSGlue.CreateJob", handleGlueCreateJob)
	r.Register("AWSGlue.GetJob", handleGlueGetJob)
	r.Register("AWSGlue.GetJobs", handleGlueGetJobs)
	r.Register("AWSGlue.UpdateJob", handleGlueUpdateJob)
	r.Register("AWSGlue.DeleteJob", handleGlueDeleteJob)
	r.Register("AWSGlue.ListJobs", handleGlueListJobs)
	r.Register("AWSGlue.StartJobRun", handleGlueStartJobRun)
	r.Register("AWSGlue.GetJobRun", handleGlueGetJobRun)
	r.Register("AWSGlue.GetJobRuns", handleGlueGetJobRuns)
	r.Register("AWSGlue.BatchStopJobRun", handleGlueBatchStopJobRun)
	r.Register("AWSGlue.CreateCrawler", handleGlueCreateCrawler)
	r.Register("AWSGlue.GetCrawler", handleGlueGetCrawler)
	r.Register("AWSGlue.GetCrawlers", handleGlueGetCrawlers)
	r.Register("AWSGlue.UpdateCrawler", handleGlueUpdateCrawler)
	r.Register("AWSGlue.DeleteCrawler", handleGlueDeleteCrawler)
	r.Register("AWSGlue.StartCrawler", handleGlueStartCrawler)
	r.Register("AWSGlue.StopCrawler", handleGlueStopCrawler)
	r.Register("AWSGlue.ListCrawlers", handleGlueListCrawlers)
	r.Register("AWSGlue.CreateTrigger", handleGlueCreateTrigger)
	r.Register("AWSGlue.GetTrigger", handleGlueGetTrigger)
	r.Register("AWSGlue.GetTriggers", handleGlueGetTriggers)
	r.Register("AWSGlue.DeleteTrigger", handleGlueDeleteTrigger)
	r.Register("AWSGlue.StartTrigger", handleGlueStartTrigger)
	r.Register("AWSGlue.StopTrigger", handleGlueStopTrigger)
	r.Register("AWSGlue.CreateConnection", handleGlueCreateConnection)
	r.Register("AWSGlue.GetConnection", handleGlueGetConnection)
	r.Register("AWSGlue.GetConnections", handleGlueGetConnections)
	r.Register("AWSGlue.UpdateConnection", handleGlueUpdateConnection)
	r.Register("AWSGlue.DeleteConnection", handleGlueDeleteConnection)
	r.Register("AWSGlue.GetPartitionIndexes", handleGlueGetPartitionIndexes)
	r.Register("AWSGlue.TagResource", handleGlueTagResource)
	r.Register("AWSGlue.UntagResource", handleGlueUntagResource)
	r.Register("AWSGlue.GetTags", handleGlueGetTags)
	r.Register("AWSGlue.CreateSecurityConfiguration", handleGlueCreateSecurityConfiguration)
	r.Register("AWSGlue.GetSecurityConfiguration", handleGlueGetSecurityConfiguration)
	r.Register("AWSGlue.GetSecurityConfigurations", handleGlueGetSecurityConfigurations)
	r.Register("AWSGlue.DeleteSecurityConfiguration", handleGlueDeleteSecurityConfiguration)
	r.Register("AWSGlue.CreateWorkflow", handleGlueCreateWorkflow)
	r.Register("AWSGlue.GetWorkflow", handleGlueGetWorkflow)
	r.Register("AWSGlue.ListWorkflows", handleGlueListWorkflows)
	r.Register("AWSGlue.DeleteWorkflow", handleGlueDeleteWorkflow)
	r.Register("AWSGlue.StartWorkflowRun", handleGlueStartWorkflowRun)
	r.Register("AWSGlue.GetWorkflowRun", handleGlueGetWorkflowRun)
	r.Register("AWSGlue.CreateClassifier", handleGlueCreateClassifier)
	r.Register("AWSGlue.GetClassifier", handleGlueGetClassifier)
	r.Register("AWSGlue.GetClassifiers", handleGlueGetClassifiers)
	r.Register("AWSGlue.UpdateClassifier", handleGlueUpdateClassifier)
	r.Register("AWSGlue.DeleteClassifier", handleGlueDeleteClassifier)
	r.Register("AWSGlue.CreateUserDefinedFunction", handleGlueCreateUserDefinedFunction)
	r.Register("AWSGlue.GetUserDefinedFunction", handleGlueGetUserDefinedFunction)
	r.Register("AWSGlue.GetUserDefinedFunctions", handleGlueGetUserDefinedFunctions)
	r.Register("AWSGlue.DeleteUserDefinedFunction", handleGlueDeleteUserDefinedFunction)
	r.Register("AWSGlue.CreateRegistry", handleGlueCreateRegistry)
	r.Register("AWSGlue.GetRegistry", handleGlueGetRegistry)
	r.Register("AWSGlue.ListRegistries", handleGlueListRegistries)
	r.Register("AWSGlue.DeleteRegistry", handleGlueDeleteRegistry)
	r.Register("AWSGlue.CreateSchema", handleGlueCreateSchema)
	r.Register("AWSGlue.GetSchema", handleGlueGetSchema)
	r.Register("AWSGlue.DeleteSchema", handleGlueDeleteSchema)
	r.Register("AWSGlue.GetTableVersion", handleGlueGetTableVersion)
	r.Register("AWSGlue.GetTableVersions", handleGlueGetTableVersions)
	r.Register("AWSGlue.DeleteTableVersion", handleGlueDeleteTableVersion)
	r.Register("AWSGlue.BatchDeleteTableVersion", handleGlueBatchDeleteTableVersion)
	r.Register("AWSGlue.CreatePartitionIndex", handleGlueCreatePartitionIndex)
	r.Register("AWSGlue.DeletePartitionIndex", handleGlueDeletePartitionIndex)
	r.Register("AWSGlue.UpdateColumnStatisticsForTable", handleGlueUpdateColumnStatisticsForTable)
	r.Register("AWSGlue.GetColumnStatisticsForTable", handleGlueGetColumnStatisticsForTable)
	r.Register("AWSGlue.DeleteColumnStatisticsForTable", handleGlueDeleteColumnStatisticsForTable)
	r.Register("AWSGlue.UpdateColumnStatisticsForPartition", handleGlueUpdateColumnStatisticsForPartition)
	r.Register("AWSGlue.GetColumnStatisticsForPartition", handleGlueGetColumnStatisticsForPartition)
	r.Register("AWSGlue.DeleteColumnStatisticsForPartition", handleGlueDeleteColumnStatisticsForPartition)
	r.Register("AWSGlue.PutResourcePolicy", handleGluePutResourcePolicy)
	r.Register("AWSGlue.GetResourcePolicy", handleGlueGetResourcePolicy)
	r.Register("AWSGlue.DeleteResourcePolicy", handleGlueDeleteResourcePolicy)
	r.Register("AWSGlue.PutDataCatalogEncryptionSettings", handleGluePutDataCatalogEncryptionSettings)
	r.Register("AWSGlue.GetDataCatalogEncryptionSettings", handleGlueGetDataCatalogEncryptionSettings)
	r.Register("AWSGlue.GetCatalogImportStatus", handleGlueGetCatalogImportStatus)
	r.Register("AWSGlue.ImportCatalogToGlue", handleGlueImportCatalogToGlue)
	r.Register("AWSGlue.RegisterSchemaVersion", handleGlueRegisterSchemaVersion)
	r.Register("AWSGlue.GetSchemaVersion", handleGlueGetSchemaVersion)
	r.Register("AWSGlue.ListSchemaVersions", handleGlueListSchemaVersions)
	r.Register("AWSGlue.DeleteSchemaVersions", handleGlueDeleteSchemaVersions)
	r.Register("AWSGlue.GetSchemaByDefinition", handleGlueGetSchemaByDefinition)

	registerGlueMLDataQuality(r, srv)
	registerGlueSessionsBlueprints(r, srv)
	registerGlueCatalogOptimizer(r, srv)
	registerGlueMLTasksSchedules(r, srv)
	registerGlueEntityCatalog2(r, srv)
	registerGlueUnfilteredDQ(r, srv)
	registerGlueBusinessContext(r, srv)
}

func glueEpochNow() float64 {
	return float64(time.Now().UTC().Unix())
}

func glueWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func glueWriteError(w http.ResponseWriter, code string, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": msg,
	})
}

func handleGlueCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string            `json:"CatalogId"`
		Tags          map[string]string `json:"Tags"`
		DatabaseInput struct {
			Name                          string            `json:"Name"`
			Parameters                    map[string]string `json:"Parameters"`
			LocationUri                   string            `json:"LocationUri"`
			Description                   string            `json:"Description"`
			CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
		} `json:"DatabaseInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseInput.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Database already exists: "+req.DatabaseInput.Name)
		return
	}
	if req.CatalogId == "" {
		req.CatalogId = awsAccountID()
	}
	if req.DatabaseInput.Parameters == nil {
		req.DatabaseInput.Parameters = map[string]string{}
	}
	if req.Tags == nil {
		req.Tags = map[string]string{}
	}
	if req.DatabaseInput.CreateTableDefaultPermissions == nil {
		req.DatabaseInput.CreateTableDefaultPermissions = []map[string]any{}
	}
	db := GlueDatabase{
		Name:                          req.DatabaseInput.Name,
		CatalogId:                     req.CatalogId,
		Parameters:                    req.DatabaseInput.Parameters,
		LocationUri:                   req.DatabaseInput.LocationUri,
		Description:                   req.DatabaseInput.Description,
		CreateTime:                    glueEpochNow(),
		Tags:                          req.Tags,
		CreateTableDefaultPermissions: req.DatabaseInput.CreateTableDefaultPermissions,
	}
	glueDatabases.Put(req.DatabaseInput.Name, db)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	db, ok := glueDatabases.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Database": glueDatabaseResponse(db)})
}

func handleGlueGetDatabases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	stored := glueDatabases.List()
	all := make([]glueDatabaseWire, 0, len(stored))
	for _, database := range stored {
		all = append(all, glueDatabaseResponse(database))
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	resp := map[string]any{"DatabaseList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	glueDatabases.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string `json:"CatalogId"`
		Name          string `json:"Name"`
		DatabaseInput struct {
			Name                          string            `json:"Name"`
			Parameters                    map[string]string `json:"Parameters"`
			LocationUri                   string            `json:"LocationUri"`
			Description                   string            `json:"Description"`
			CreateTableDefaultPermissions []map[string]any  `json:"CreateTableDefaultPermissions"`
		} `json:"DatabaseInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" || req.DatabaseInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name and DatabaseInput.Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	existing, ok := glueDatabases.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.Name)
		return
	}
	parameters := req.DatabaseInput.Parameters
	if parameters == nil {
		parameters = map[string]string{}
	}
	defaultPermissions := req.DatabaseInput.CreateTableDefaultPermissions
	if defaultPermissions == nil {
		defaultPermissions = []map[string]any{}
	}
	updated := GlueDatabase{
		Name:                          req.DatabaseInput.Name,
		CatalogId:                     firstNonEmpty(req.CatalogId, existing.CatalogId),
		Parameters:                    parameters,
		LocationUri:                   req.DatabaseInput.LocationUri,
		Description:                   req.DatabaseInput.Description,
		CreateTime:                    existing.CreateTime,
		Tags:                          existing.Tags,
		CreateTableDefaultPermissions: defaultPermissions,
	}
	// A rename moves the row; otherwise overwrite in place.
	if req.DatabaseInput.Name != req.Name {
		if _, clash := glueDatabases.Get(req.DatabaseInput.Name); clash {
			glueWriteError(w, "AlreadyExistsException", "Database already exists: "+req.DatabaseInput.Name)
			return
		}
		glueDatabases.Delete(req.Name)
	}
	glueDatabases.Put(req.DatabaseInput.Name, updated)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func glueTableKey(database, table string) string {
	return database + "/" + table
}

// glueTableVersionKey is the store key for one (database, table, versionId)
// snapshot.
func glueTableVersionKey(database, table, versionID string) string {
	return database + "/" + table + "/" + versionID
}

// glueLatestTableVersion returns the highest VersionId recorded for a table as
// an integer (0 if none). Caller holds glueMu.
func glueLatestTableVersion(database, table string) int {
	latest := 0
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == database && v.TableName == table {
			if n, err := strconv.Atoi(v.VersionId); err == nil && n > latest {
				latest = n
			}
		}
	}
	return latest
}

// glueRecordTableVersion snapshots a table under the given version id. Caller
// holds glueMu.
func glueRecordTableVersion(database, table, versionID string, t GlueTable) {
	glueTableVersions.Put(glueTableVersionKey(database, table, versionID), GlueTableVersion{
		DatabaseName: database,
		TableName:    table,
		VersionId:    versionID,
		Table:        t,
	})
}

func handleGlueCreateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableInput   struct {
			Name              string            `json:"Name"`
			StorageDescriptor map[string]any    `json:"StorageDescriptor"`
			PartitionKeys     []map[string]any  `json:"PartitionKeys"`
			Parameters        map[string]string `json:"Parameters"`
			TableType         string            `json:"TableType"`
		} `json:"TableInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.DatabaseName)
		return
	}
	key := glueTableKey(req.DatabaseName, req.TableInput.Name)
	if _, ok := glueTables.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Table already exists: "+req.TableInput.Name)
		return
	}
	now := glueEpochNow()
	t := GlueTable{
		Name:              req.TableInput.Name,
		DatabaseName:      req.DatabaseName,
		StorageDescriptor: req.TableInput.StorageDescriptor,
		PartitionKeys:     req.TableInput.PartitionKeys,
		Parameters:        req.TableInput.Parameters,
		TableType:         req.TableInput.TableType,
		CreateTime:        now,
		UpdateTime:        now,
	}
	glueTables.Put(key, t)
	glueRecordTableVersion(req.DatabaseName, req.TableInput.Name, "1", t)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Name         string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	t, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.Name))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Table": t})
}

func handleGlueGetTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueTables.List()
	var filtered []GlueTable
	for _, t := range all {
		if t.DatabaseName == req.DatabaseName {
			filtered = append(filtered, t)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(filtered, req.NextToken, maxR, 100)
	resp := map[string]any{"TableList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Name         string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableKey(req.DatabaseName, req.Name)
	if _, ok := glueTables.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.Name)
		return
	}
	glueTables.Delete(key)
	glueDeleteTableVersions(req.DatabaseName, req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueDeleteTableVersions removes all recorded versions for a table. Caller
// holds glueMu.
func glueDeleteTableVersions(database, table string) {
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == database && v.TableName == table {
			glueTableVersions.Delete(glueTableVersionKey(database, table, v.VersionId))
		}
	}
}

func handleGlueUpdateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableInput   struct {
			Name              string            `json:"Name"`
			StorageDescriptor map[string]any    `json:"StorageDescriptor"`
			PartitionKeys     []map[string]any  `json:"PartitionKeys"`
			Parameters        map[string]string `json:"Parameters"`
			TableType         string            `json:"TableType"`
		} `json:"TableInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableInput.Name are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableKey(req.DatabaseName, req.TableInput.Name)
	existing, ok := glueTables.Get(key)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableInput.Name)
		return
	}
	updated := GlueTable{
		Name:              req.TableInput.Name,
		DatabaseName:      req.DatabaseName,
		StorageDescriptor: req.TableInput.StorageDescriptor,
		PartitionKeys:     req.TableInput.PartitionKeys,
		Parameters:        req.TableInput.Parameters,
		TableType:         req.TableInput.TableType,
		CreateTime:        existing.CreateTime,
		UpdateTime:        glueEpochNow(),
	}
	glueTables.Put(key, updated)
	nextVer := strconv.Itoa(glueLatestTableVersion(req.DatabaseName, req.TableInput.Name) + 1)
	glueRecordTableVersion(req.DatabaseName, req.TableInput.Name, nextVer, updated)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeleteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string   `json:"DatabaseName"`
		TablesToDelete []string `json:"TablesToDelete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, name := range req.TablesToDelete {
		key := glueTableKey(req.DatabaseName, name)
		if _, ok := glueTables.Get(key); !ok {
			errs = append(errs, map[string]any{
				"TableName": name,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Table not found: " + name,
				},
			})
			continue
		}
		glueTables.Delete(key)
		glueDeleteTableVersions(req.DatabaseName, name)
		// Cascade-delete the table's partitions.
		for _, p := range gluePartitions.List() {
			if p.DatabaseName == req.DatabaseName && p.TableName == name {
				gluePartitions.Delete(gluePartitionKey(req.DatabaseName, name, p.Values))
			}
		}
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// gluePartitionKey is the store key for a partition; partition values are
// ordered and joined under a key that also scopes them to (database, table).
func gluePartitionKey(database, table string, values []string) string {
	return database + "/" + table + "/" + strings.Join(values, "\x1f")
}

func glueValuesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// glueRequirePartitionTable validates the (database, table) parent exists.
// Caller holds glueMu. Returns false and writes the error if missing.
func glueRequirePartitionTable(w http.ResponseWriter, database, table string) bool {
	if _, ok := glueDatabases.Get(database); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+database)
		return false
	}
	if _, ok := glueTables.Get(glueTableKey(database, table)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+table)
		return false
	}
	return true
}

type gluePartitionInput struct {
	Values            []string          `json:"Values"`
	StorageDescriptor map[string]any    `json:"StorageDescriptor"`
	Parameters        map[string]string `json:"Parameters"`
	LastAccessTime    *float64          `json:"LastAccessTime"`
	LastAnalyzedTime  *float64          `json:"LastAnalyzedTime"`
}

func gluePartitionFromInput(database, table string, in gluePartitionInput, creationTime float64) GluePartition {
	return GluePartition{
		Values:            in.Values,
		DatabaseName:      database,
		TableName:         table,
		StorageDescriptor: in.StorageDescriptor,
		Parameters:        in.Parameters,
		CreationTime:      creationTime,
		LastAccessTime:    in.LastAccessTime,
		LastAnalyzedTime:  in.LastAnalyzedTime,
	}
}

func handleGlueCreatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string             `json:"DatabaseName"`
		TableName      string             `json:"TableName"`
		PartitionInput gluePartitionInput `json:"PartitionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}
	if len(req.PartitionInput.Values) == 0 {
		glueWriteError(w, "InvalidInputException", "PartitionInput.Values is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	key := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionInput.Values)
	if _, ok := gluePartitions.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Partition already exists")
		return
	}
	gluePartitions.Put(key, gluePartitionFromInput(req.DatabaseName, req.TableName, req.PartitionInput, glueEpochNow()))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchCreatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string               `json:"DatabaseName"`
		TableName          string               `json:"TableName"`
		PartitionInputList []gluePartitionInput `json:"PartitionInputList"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	now := glueEpochNow()
	var errs []map[string]any
	for _, in := range req.PartitionInputList {
		key := gluePartitionKey(req.DatabaseName, req.TableName, in.Values)
		if _, ok := gluePartitions.Get(key); ok {
			errs = append(errs, map[string]any{
				"PartitionValues": in.Values,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "AlreadyExistsException",
					"ErrorMessage": "Partition already exists",
				},
			})
			continue
		}
		gluePartitions.Put(key, gluePartitionFromInput(req.DatabaseName, req.TableName, in, now))
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	p, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Partition": p})
}

func handleGlueGetPartitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var filtered []GluePartition
	for _, p := range gluePartitions.List() {
		if p.DatabaseName == req.DatabaseName && p.TableName == req.TableName {
			filtered = append(filtered, p)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(filtered, req.NextToken, maxR, 100)
	resp := map[string]any{"Partitions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchGetPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string `json:"DatabaseName"`
		TableName       string `json:"TableName"`
		PartitionsToGet []struct {
			Values []string `json:"Values"`
		} `json:"PartitionsToGet"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	var found []GluePartition
	var unprocessed []map[string]any
	for _, pk := range req.PartitionsToGet {
		p, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, pk.Values))
		if ok {
			found = append(found, p)
		} else {
			unprocessed = append(unprocessed, map[string]any{"Values": pk.Values})
		}
	}
	resp := map[string]any{}
	if len(found) > 0 {
		resp["Partitions"] = found
	}
	if len(unprocessed) > 0 {
		resp["UnprocessedKeys"] = unprocessed
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdatePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string             `json:"DatabaseName"`
		TableName          string             `json:"TableName"`
		PartitionValueList []string           `json:"PartitionValueList"`
		PartitionInput     gluePartitionInput `json:"PartitionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and TableName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	oldKey := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValueList)
	existing, ok := gluePartitions.Get(oldKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	updated := gluePartitionFromInput(req.DatabaseName, req.TableName, req.PartitionInput, existing.CreationTime)
	// UpdatePartition may move the partition to new values.
	if !glueValuesEqual(req.PartitionInput.Values, req.PartitionValueList) {
		newKey := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionInput.Values)
		if _, clash := gluePartitions.Get(newKey); clash {
			glueWriteError(w, "AlreadyExistsException", "Partition already exists")
			return
		}
		gluePartitions.Delete(oldKey)
		gluePartitions.Put(newKey, updated)
	} else {
		gluePartitions.Put(oldKey, updated)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeletePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)
	if _, ok := gluePartitions.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	gluePartitions.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeletePartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName       string `json:"DatabaseName"`
		TableName          string `json:"TableName"`
		PartitionsToDelete []struct {
			Values []string `json:"Values"`
		} `json:"PartitionsToDelete"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, pk := range req.PartitionsToDelete {
		key := gluePartitionKey(req.DatabaseName, req.TableName, pk.Values)
		if _, ok := gluePartitions.Get(key); !ok {
			errs = append(errs, map[string]any{
				"PartitionValues": pk.Values,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Partition not found",
				},
			})
			continue
		}
		gluePartitions.Delete(key)
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCreateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string            `json:"Name"`
		Description      string            `json:"Description"`
		Role             string            `json:"Role"`
		Command          map[string]any    `json:"Command"`
		DefaultArguments map[string]string `json:"DefaultArguments"`
		GlueVersion      string            `json:"GlueVersion"`
		MaxCapacity      *float64          `json:"MaxCapacity"`
		WorkerType       string            `json:"WorkerType"`
		NumberOfWorkers  *int              `json:"NumberOfWorkers"`
		Tags             map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueJobs.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Job already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	job := GlueJob{
		Name:             req.Name,
		Description:      req.Description,
		Role:             req.Role,
		Command:          req.Command,
		DefaultArguments: req.DefaultArguments,
		GlueVersion:      req.GlueVersion,
		MaxCapacity:      req.MaxCapacity,
		WorkerType:       req.WorkerType,
		NumberOfWorkers:  req.NumberOfWorkers,
		CreatedOn:        now,
		LastModifiedOn:   now,
		Tags:             req.Tags,
	}
	glueJobs.Put(req.Name, job)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Job": glueJobWire{job}})
}

// glueJobWire wraps GlueJob for response emission. The real Job shape
// has no Tags member — tags ride GetTags. The struct keeps its Tags
// JSON tag so tag state survives Store persistence; the wrapper strips
// it from GetJob/GetJobs responses.
type glueJobWire struct {
	GlueJob
}

func (j glueJobWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(j.GlueJob)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueJobs.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 25)
	jobs := make([]glueJobWire, 0, len(page))
	for _, job := range page {
		jobs = append(jobs, glueJobWire{job})
	}
	resp := map[string]any{"Jobs": jobs}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueJobs.Delete(req.JobName)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobName": req.JobName})
}

func handleGlueStartJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string            `json:"JobName"`
		Arguments map[string]string `json:"Arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	script, err := gluePythonScript(job)
	if err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	runID := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	now := glueEpochNow()
	run := GlueJobRun{
		ID:          runID,
		JobName:     req.JobName,
		JobRunState: "RUNNING",
		StartedOn:   now,
		Arguments:   req.Arguments,
	}
	glueJobRuns.Put(req.JobName+"/"+runID, run)
	go glueRunPythonJob(req.JobName, runID, job, script, req.Arguments)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobRunId": runID})
}

func gluePythonScript(job GlueJob) ([]byte, error) {
	commandName := glueString(job.Command["Name"])
	if commandName == "" {
		commandName = glueString(job.Command["name"])
	}
	if commandName != "pythonshell" {
		return nil, fmt.Errorf("only pythonshell jobs execute in the simulator")
	}
	location := glueString(job.Command["ScriptLocation"])
	if location == "" {
		location = glueString(job.Command["scriptLocation"])
	}
	if location == "" {
		return nil, fmt.Errorf("Command.ScriptLocation is required")
	}
	bucket, key, ok := glueS3Location(location)
	if !ok {
		return nil, fmt.Errorf("Command.ScriptLocation must be an s3:// URL")
	}
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		return nil, fmt.Errorf("script object not found: %s", location)
	}
	return obj.Data, nil
}

// gluePythonShellImage is the interpreter a Python shell job runs on, chosen by
// the job command's PythonVersion. AWS: "The Python version being used to run a
// Python shell job. Allowed values are 2 or 3." Glue runs Python 3 shell jobs
// on Python 3.9; Python 2 shell jobs ran only on Glue versions AWS has since
// retired, so a job asking for one is refused rather than quietly run on a
// different interpreter.
func gluePythonShellImage(job GlueJob) (string, error) {
	version := glueString(job.Command["PythonVersion"])
	if version == "" {
		version = glueString(job.Command["pythonVersion"])
	}
	switch version {
	case "", "3", "3.9":
		return "public.ecr.aws/docker/library/python:3.9", nil
	case "2":
		return "", fmt.Errorf("Command.PythonVersion 2 is not supported for Python shell jobs")
	default:
		return "", fmt.Errorf("Command.PythonVersion %q is not a Python shell job version", version)
	}
}

func glueRunPythonJob(jobName, runID string, job GlueJob, script []byte, args map[string]string) {
	image, err := gluePythonShellImage(job)
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	workDir, err := os.MkdirTemp("", "sockerless-glue-*")
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	defer os.RemoveAll(workDir)

	scriptPath := filepath.Join(workDir, "script.py")
	if err := os.WriteFile(scriptPath, script, 0700); err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}

	platform, err := localImagePlatform(context.Background(), image)
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	// The job runs in its interpreter's container, taking the script and the
	// job's arguments the way Glue hands them to a Python shell job.
	handle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Command:      []string{"python3"},
		Args:         append([]string{glueJobScriptDir + "/script.py"}, glueArgs(args)...),
		WorkingDir:   glueJobScriptDir,
		Binds:        []string{filepath.Clean(workDir) + ":" + glueJobScriptDir + ":z"},
		Env: map[string]string{
			"AWS_DEFAULT_REGION": awsRegion(),
			"AWS_REGION":         awsRegion(),
		},
		ExtraHosts: hostMetadataExtraHosts(),
		Labels:     map[string]string{"sockerless-glue-job-run": runID},
		Sandbox:    sim.SandboxFargate,
	}, sim.NoopSink{})
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, fmt.Sprintf("start job environment %s: %v", image, err))
		return
	}
	result := handle.Wait()
	status := "SUCCEEDED"
	reason := ""
	if result.Error != nil {
		status = "FAILED"
		reason = result.Error.Error()
	} else if result.ExitCode != 0 {
		status = "FAILED"
		reason = fmt.Sprintf("Python shell script exited with status %d", result.ExitCode)
	}
	executionTime := int(result.StoppedAt.Sub(result.StartedAt).Seconds())
	if executionTime < 1 {
		executionTime = 1
	}
	glueCompleteRun(jobName, runID, status, executionTime, reason)
}

func glueCompleteRun(jobName, runID, status string, executionTime int, reason string) {
	glueMu.Lock()
	defer glueMu.Unlock()

	key := jobName + "/" + runID
	run, ok := glueJobRuns.Get(key)
	if !ok {
		return
	}
	run.JobRunState = status
	run.CompletedOn = glueEpochNow()
	run.ExecutionTime = executionTime
	run.ErrorMessage = reason
	glueJobRuns.Put(key, run)
}

func handleGlueGetJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
		RunId   string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueJobRuns.Get(req.JobName + "/" + req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job run not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobRun": run})
}

func handleGlueGetJobRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName    string `json:"JobName"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	if _, ok := glueJobs.Get(req.JobName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Job with name: "+req.JobName+" not found")
		return
	}

	all := glueJobRuns.List()
	var runs []GlueJobRun
	for _, run := range all {
		if run.JobName == req.JobName {
			runs = append(runs, run)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(runs, req.NextToken, maxR, 25)
	resp := map[string]any{"JobRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string `json:"JobName"`
		JobUpdate struct {
			Description      string            `json:"Description"`
			Role             string            `json:"Role"`
			Command          map[string]any    `json:"Command"`
			DefaultArguments map[string]string `json:"DefaultArguments"`
			GlueVersion      string            `json:"GlueVersion"`
			MaxCapacity      *float64          `json:"MaxCapacity"`
			WorkerType       string            `json:"WorkerType"`
			NumberOfWorkers  *int              `json:"NumberOfWorkers"`
		} `json:"JobUpdate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	// JobUpdate replaces the configuration; tags are preserved (they ride GetTags).
	upd := req.JobUpdate
	job.Description = upd.Description
	job.Role = upd.Role
	job.Command = upd.Command
	job.DefaultArguments = upd.DefaultArguments
	job.GlueVersion = upd.GlueVersion
	job.MaxCapacity = upd.MaxCapacity
	job.WorkerType = upd.WorkerType
	job.NumberOfWorkers = upd.NumberOfWorkers
	job.LastModifiedOn = glueEpochNow()
	glueJobs.Put(req.JobName, job)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobName": req.JobName})
}

func handleGlueListJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueJobs.List()
	names := make([]string, 0, len(all))
	for _, job := range all {
		names = append(names, job.Name)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(names, req.NextToken, maxR, 25)
	resp := map[string]any{"JobNames": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchStopJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string   `json:"JobName"`
		JobRunIds []string `json:"JobRunIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	successes := make([]map[string]any, 0, len(req.JobRunIds))
	errs := make([]map[string]any, 0)
	for _, id := range req.JobRunIds {
		key := req.JobName + "/" + id
		run, ok := glueJobRuns.Get(key)
		if !ok {
			errs = append(errs, map[string]any{
				"JobName":  req.JobName,
				"JobRunId": id,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Job run not found: " + id,
				},
			})
			continue
		}
		if run.JobRunState == "RUNNING" || run.JobRunState == "STARTING" {
			run.JobRunState = "STOPPING"
			glueJobRuns.Put(key, run)
		}
		successes = append(successes, map[string]any{
			"JobName":  req.JobName,
			"JobRunId": id,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SuccessfulSubmissions": successes,
		"Errors":                errs,
	})
}

func handleGlueCreateCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string            `json:"Name"`
		Role         string            `json:"Role"`
		Targets      map[string]any    `json:"Targets"`
		DatabaseName string            `json:"DatabaseName"`
		Description  string            `json:"Description"`
		Classifiers  []string          `json:"Classifiers"`
		TablePrefix  string            `json:"TablePrefix"`
		Schedule     string            `json:"Schedule"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueCrawlers.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Crawler already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	crawler := GlueCrawler{
		Name:         req.Name,
		Role:         req.Role,
		Targets:      req.Targets,
		DatabaseName: req.DatabaseName,
		Description:  req.Description,
		Classifiers:  req.Classifiers,
		TablePrefix:  req.TablePrefix,
		State:        "READY",
		CreationTime: now,
		LastUpdated:  now,
		Version:      1,
		Tags:         req.Tags,
	}
	if req.Schedule != "" {
		crawler.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
	}
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueCrawlerWire strips the persistence-only Tags member from the wire shape;
// the real Crawler structure has no Tags field (tags ride GetTags).
type glueCrawlerWire struct {
	GlueCrawler
}

func (c glueCrawlerWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(c.GlueCrawler)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Crawler": glueCrawlerWire{crawler}})
}

func handleGlueGetCrawlers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueCrawlers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 25)
	crawlers := make([]glueCrawlerWire, 0, len(page))
	for _, c := range page {
		crawlers = append(crawlers, glueCrawlerWire{c})
	}
	resp := map[string]any{"Crawlers": crawlers}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string         `json:"Name"`
		Role         string         `json:"Role"`
		Targets      map[string]any `json:"Targets"`
		DatabaseName string         `json:"DatabaseName"`
		Description  string         `json:"Description"`
		Classifiers  []string       `json:"Classifiers"`
		TablePrefix  string         `json:"TablePrefix"`
		Schedule     string         `json:"Schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if req.Role != "" {
		crawler.Role = req.Role
	}
	if req.Targets != nil {
		crawler.Targets = req.Targets
	}
	if req.DatabaseName != "" {
		crawler.DatabaseName = req.DatabaseName
	}
	crawler.Description = req.Description
	if req.Classifiers != nil {
		crawler.Classifiers = req.Classifiers
	}
	if req.TablePrefix != "" {
		crawler.TablePrefix = req.TablePrefix
	}
	if req.Schedule != "" {
		crawler.Schedule = &GlueSchedule{ScheduleExpression: req.Schedule, State: "SCHEDULED"}
	}
	crawler.LastUpdated = glueEpochNow()
	crawler.Version++
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State == "RUNNING" {
		glueWriteError(w, "CrawlerRunningException", "Cannot delete crawler while running: "+req.Name)
		return
	}
	glueCrawlers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStartCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State == "RUNNING" {
		glueWriteError(w, "CrawlerRunningException", "Crawler is already running: "+req.Name)
		return
	}
	crawler.State = "RUNNING"
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueStopCrawler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	crawler, ok := glueCrawlers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Crawler not found: "+req.Name)
		return
	}
	if crawler.State != "RUNNING" {
		glueWriteError(w, "CrawlerNotRunningException", "Crawler is not running: "+req.Name)
		return
	}
	crawler.State = "STOPPING"
	crawler.LastUpdated = glueEpochNow()
	glueCrawlers.Put(req.Name, crawler)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueListCrawlers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueCrawlers.List()
	names := make([]string, 0, len(all))
	for _, c := range all {
		names = append(names, c.Name)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(names, req.NextToken, maxR, 25)
	resp := map[string]any{"CrawlerNames": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueCreateTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string            `json:"Name"`
		WorkflowName    string            `json:"WorkflowName"`
		Type            string            `json:"Type"`
		Schedule        string            `json:"Schedule"`
		Predicate       map[string]any    `json:"Predicate"`
		Actions         []map[string]any  `json:"Actions"`
		Description     string            `json:"Description"`
		StartOnCreation bool              `json:"StartOnCreation"`
		Tags            map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}
	if req.Type == "" {
		glueWriteError(w, "InvalidInputException", "Type is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueTriggers.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Trigger already exists: "+req.Name)
		return
	}
	state := "CREATED"
	if req.Type == "ON_DEMAND" {
		state = "CREATED"
	} else if req.StartOnCreation {
		state = "ACTIVATED"
	}
	trigger := GlueTrigger{
		ID:           strings.ReplaceAll(uuid.New().String(), "-", ""),
		Name:         req.Name,
		WorkflowName: req.WorkflowName,
		Type:         req.Type,
		State:        state,
		Description:  req.Description,
		Schedule:     req.Schedule,
		Actions:      req.Actions,
		Predicate:    req.Predicate,
		Tags:         req.Tags,
	}
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

// glueTriggerWire strips the persistence-only Tags member from the wire shape;
// the real Trigger structure has no Tags field (tags ride GetTags).
type glueTriggerWire struct {
	GlueTrigger
}

func (t glueTriggerWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(t.GlueTrigger)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Trigger": glueTriggerWire{trigger}})
}

func handleGlueGetTriggers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken        string `json:"NextToken"`
		DependentJobName string `json:"DependentJobName"`
		MaxResults       *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueTriggers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 25)
	triggers := make([]glueTriggerWire, 0, len(page))
	for _, t := range page {
		triggers = append(triggers, glueTriggerWire{t})
	}
	resp := map[string]any{"Triggers": triggers}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueTriggers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStartTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	trigger.State = "ACTIVATED"
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStopTrigger(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	trigger, ok := glueTriggers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Trigger not found: "+req.Name)
		return
	}
	trigger.State = "DEACTIVATED"
	glueTriggers.Put(req.Name, trigger)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueCreateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionInput struct {
			Name                           string            `json:"Name"`
			Description                    string            `json:"Description"`
			ConnectionType                 string            `json:"ConnectionType"`
			MatchCriteria                  []string          `json:"MatchCriteria"`
			ConnectionProperties           map[string]string `json:"ConnectionProperties"`
			PhysicalConnectionRequirements map[string]any    `json:"PhysicalConnectionRequirements"`
		} `json:"ConnectionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.ConnectionInput.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueConnections.Get(req.ConnectionInput.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Connection already exists: "+req.ConnectionInput.Name)
		return
	}
	now := glueEpochNow()
	in := req.ConnectionInput
	conn := GlueConnection{
		Name:                           in.Name,
		Description:                    in.Description,
		ConnectionType:                 in.ConnectionType,
		MatchCriteria:                  in.MatchCriteria,
		ConnectionProperties:           in.ConnectionProperties,
		PhysicalConnectionRequirements: in.PhysicalConnectionRequirements,
		CreationTime:                   now,
		LastUpdatedTime:                now,
	}
	glueConnections.Put(in.Name, conn)
	glueWriteJSON(w, http.StatusOK, map[string]any{"CreateConnectionStatus": "READY"})
}

func handleGlueGetConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	conn, ok := glueConnections.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Connection": conn})
}

func handleGlueGetConnections(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueConnections.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 25)
	resp := map[string]any{"ConnectionList": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"Name"`
		ConnectionInput struct {
			Name                           string            `json:"Name"`
			Description                    string            `json:"Description"`
			ConnectionType                 string            `json:"ConnectionType"`
			MatchCriteria                  []string          `json:"MatchCriteria"`
			ConnectionProperties           map[string]string `json:"ConnectionProperties"`
			PhysicalConnectionRequirements map[string]any    `json:"PhysicalConnectionRequirements"`
		} `json:"ConnectionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	conn, ok := glueConnections.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Connection not found: "+req.Name)
		return
	}
	in := req.ConnectionInput
	conn.Description = in.Description
	conn.ConnectionType = in.ConnectionType
	conn.MatchCriteria = in.MatchCriteria
	conn.ConnectionProperties = in.ConnectionProperties
	conn.PhysicalConnectionRequirements = in.PhysicalConnectionRequirements
	conn.LastUpdatedTime = glueEpochNow()
	glueConnections.Put(req.Name, conn)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConnectionName string `json:"ConnectionName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueConnections.Delete(req.ConnectionName)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// gluePartitionIndexKey is the store key for one (database, table, indexName)
// partition index.
func gluePartitionIndexKey(database, table, index string) string {
	return database + "/" + table + "/" + index
}

func handleGlueCreatePartitionIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName   string `json:"DatabaseName"`
		TableName      string `json:"TableName"`
		PartitionIndex struct {
			Keys      []string `json:"Keys"`
			IndexName string   `json:"IndexName"`
		} `json:"PartitionIndex"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.TableName == "" || req.PartitionIndex.IndexName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName, TableName and PartitionIndex.IndexName are required")
		return
	}
	if len(req.PartitionIndex.Keys) == 0 {
		glueWriteError(w, "InvalidInputException", "PartitionIndex.Keys is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	tbl, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	idxKey := gluePartitionIndexKey(req.DatabaseName, req.TableName, req.PartitionIndex.IndexName)
	if _, exists := gluePartIndexes.Get(idxKey); exists {
		glueWriteError(w, "AlreadyExistsException", "Partition index already exists: "+req.PartitionIndex.IndexName)
		return
	}
	// Resolve each key name to a KeySchemaElement (Name + Type) from the table's
	// partition keys, matching the real PartitionIndexDescriptor read-back shape.
	keyType := func(name string) string {
		for _, pk := range tbl.PartitionKeys {
			if glueString(pk["Name"]) == name {
				return glueString(pk["Type"])
			}
		}
		return "string"
	}
	keys := make([]map[string]any, 0, len(req.PartitionIndex.Keys))
	for _, name := range req.PartitionIndex.Keys {
		keys = append(keys, map[string]any{"Name": name, "Type": keyType(name)})
	}
	gluePartIndexes.Put(idxKey, GluePartitionIndex{
		DatabaseName: req.DatabaseName,
		TableName:    req.TableName,
		IndexName:    req.PartitionIndex.IndexName,
		Keys:         keys,
		IndexStatus:  "ACTIVE",
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetPartitionIndexes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	descriptors := []map[string]any{}
	for _, idx := range gluePartIndexes.List() {
		if idx.DatabaseName == req.DatabaseName && idx.TableName == req.TableName {
			descriptors = append(descriptors, map[string]any{
				"IndexName":   idx.IndexName,
				"Keys":        idx.Keys,
				"IndexStatus": idx.IndexStatus,
			})
		}
	}
	sort.Slice(descriptors, func(i, j int) bool {
		return glueString(descriptors[i]["IndexName"]) < glueString(descriptors[j]["IndexName"])
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"PartitionIndexDescriptorList": descriptors})
}

func handleGlueDeletePartitionIndex(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		IndexName    string `json:"IndexName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	idxKey := gluePartitionIndexKey(req.DatabaseName, req.TableName, req.IndexName)
	if _, ok := gluePartIndexes.Get(idxKey); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition index not found: "+req.IndexName)
		return
	}
	gluePartIndexes.Delete(idxKey)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueResourceFromARN splits the last colon-segment of a Glue ARN into resource type and name.
// e.g. arn:aws:glue:us-east-1::database/my-db → ("database", "my-db")
//
//	arn:aws:glue:us-east-1:123:job/my-job  → ("job", "my-job")
func glueResourceFromARN(arn string) (resType, name string) {
	idx := strings.LastIndex(arn, ":")
	resource := arn[idx+1:]
	slash := strings.Index(resource, "/")
	if slash < 0 {
		return "job", resource
	}
	return resource[:slash], resource[slash+1:]
}

func glueString(v any) string {
	s, _ := v.(string)
	return s
}

func glueS3Location(location string) (bucket, key string, ok bool) {
	if !strings.HasPrefix(location, "s3://") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(location, "s3://"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func glueArgs(args map[string]string) []string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(args)*2)
	for _, k := range keys {
		out = append(out, k, args[k])
	}
	return out
}

func handleGlueTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string            `json:"ResourceArn"`
		TagsToAdd   map[string]string `json:"TagsToAdd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	resType, name := glueResourceFromARN(req.ResourceArn)
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		if db.Tags == nil {
			db.Tags = make(map[string]string)
		}
		for k, v := range req.TagsToAdd {
			db.Tags[k] = v
		}
		glueDatabases.Put(name, db)
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		if job.Tags == nil {
			job.Tags = make(map[string]string)
		}
		for k, v := range req.TagsToAdd {
			job.Tags[k] = v
		}
		glueJobs.Put(name, job)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn  string   `json:"ResourceArn"`
		TagsToRemove []string `json:"TagsToRemove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	resType, name := glueResourceFromARN(req.ResourceArn)
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		for _, k := range req.TagsToRemove {
			delete(db.Tags, k)
		}
		glueDatabases.Put(name, db)
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		for _, k := range req.TagsToRemove {
			delete(job.Tags, k)
		}
		glueJobs.Put(name, job)
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	resType, name := glueResourceFromARN(req.ResourceArn)
	var tags map[string]string
	switch resType {
	case "database":
		db, ok := glueDatabases.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = db.Tags
	case "crawler":
		cr, ok := glueCrawlers.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = cr.Tags
	case "trigger":
		tr, ok := glueTriggers.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = tr.Tags
	default:
		job, ok := glueJobs.Get(name)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Resource not found: "+req.ResourceArn)
			return
		}
		tags = job.Tags
	}
	if tags == nil {
		tags = map[string]string{}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Tags": tags})
}

// glueRFC3339 formats a timestamp the way Glue schema-registry shapes return
// CreatedTime/UpdatedTime (an ISO-8601/RFC3339 string, not an epoch number).
func glueRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// glueGlueArn builds an arn:aws:glue ARN for the given resource path.
func glueGlueArn(resource string) string {
	return "arn:aws:glue:us-east-1:123456789012:" + resource
}

func handleGlueCreateSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                    string         `json:"Name"`
		EncryptionConfiguration map[string]any `json:"EncryptionConfiguration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueSecConfigs.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Security configuration already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	glueSecConfigs.Put(req.Name, GlueSecurityConfiguration{
		Name:                    req.Name,
		CreatedTimeStamp:        now,
		EncryptionConfiguration: req.EncryptionConfiguration,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"Name":             req.Name,
		"CreatedTimestamp": now,
	})
}

func handleGlueGetSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	sc, ok := glueSecConfigs.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Security configuration not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"SecurityConfiguration": sc})
}

func handleGlueGetSecurityConfigurations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueSecConfigs.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	resp := map[string]any{"SecurityConfigurations": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteSecurityConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueSecConfigs.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Security configuration not found: "+req.Name)
		return
	}
	glueSecConfigs.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name                 string            `json:"Name"`
		Description          string            `json:"Description"`
		DefaultRunProperties map[string]string `json:"DefaultRunProperties"`
		Tags                 map[string]string `json:"Tags"`
		MaxConcurrentRuns    *int              `json:"MaxConcurrentRuns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueWorkflows.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Workflow already exists: "+req.Name)
		return
	}
	now := glueEpochNow()
	glueWorkflows.Put(req.Name, GlueWorkflow{
		Name:                 req.Name,
		Description:          req.Description,
		DefaultRunProperties: req.DefaultRunProperties,
		MaxConcurrentRuns:    req.MaxConcurrentRuns,
		Tags:                 req.Tags,
		CreatedOn:            now,
		LastModifiedOn:       now,
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	wf, ok := glueWorkflows.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Workflow": wf})
}

func handleGlueListWorkflows(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueWorkflows.List()
	names := make([]string, 0, len(all))
	for _, wf := range all {
		names = append(names, wf.Name)
	}
	sort.Strings(names)
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(names, req.NextToken, maxR, 25)
	resp := map[string]any{"Workflows": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteWorkflow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueWorkflows.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	glueWorkflows.Delete(req.Name)
	// DeleteWorkflow returns the deleted workflow's name.
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueStartWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string            `json:"Name"`
		RunProperties map[string]string `json:"RunProperties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueWorkflows.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow not found: "+req.Name)
		return
	}
	runID := "wr_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := glueEpochNow()
	// The simulator settles the run synchronously (no native async backend).
	glueWfRuns.Put(req.Name+"\x00"+runID, GlueWorkflowRun{
		Name:                  req.Name,
		WorkflowRunId:         runID,
		WorkflowRunProperties: req.RunProperties,
		StartedOn:             now,
		CompletedOn:           now,
		Status:                "COMPLETED",
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{"RunId": runID})
}

func handleGlueGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"Name"`
		RunId string `json:"RunId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueWfRuns.Get(req.Name + "\x00" + req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Workflow run not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Run": run})
}

// glueClassifierName extracts the classifier name from the single set sub-object.
func glueClassifierName(c GlueClassifier) string {
	for _, m := range []map[string]any{c.GrokClassifier, c.XMLClassifier, c.JsonClassifier, c.CsvClassifier} {
		if m != nil {
			if n, ok := m["Name"].(string); ok {
				return n
			}
		}
	}
	return ""
}

// glueClassifierFromCreate maps a CreateClassifier/UpdateClassifier request body
// (which carries the trimmed Create*ClassifierRequest sub-objects) into the
// stored Classifier shape, stamping server-managed timestamp/version fields.
func glueClassifierFromCreate(req map[string]json.RawMessage, prior *GlueClassifier) (GlueClassifier, string, bool) {
	var out GlueClassifier
	now := glueEpochNow()
	set := func(raw json.RawMessage, priorObj map[string]any) (map[string]any, bool) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, false
		}
		if priorObj != nil {
			m["CreationTime"] = priorObj["CreationTime"]
			ver := 1.0
			if v, ok := priorObj["Version"].(float64); ok {
				ver = v + 1
			}
			m["Version"] = ver
		} else {
			m["CreationTime"] = now
			m["Version"] = float64(1)
		}
		m["LastUpdated"] = now
		return m, true
	}
	for key, raw := range req {
		if raw == nil {
			continue
		}
		var priorObj map[string]any
		switch key {
		case "GrokClassifier":
			if prior != nil {
				priorObj = prior.GrokClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.GrokClassifier = m
		case "XMLClassifier":
			if prior != nil {
				priorObj = prior.XMLClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.XMLClassifier = m
		case "JsonClassifier":
			if prior != nil {
				priorObj = prior.JsonClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.JsonClassifier = m
		case "CsvClassifier":
			if prior != nil {
				priorObj = prior.CsvClassifier
			}
			m, ok := set(raw, priorObj)
			if !ok {
				return out, "", false
			}
			out.CsvClassifier = m
		}
	}
	return out, glueClassifierName(out), true
}

func handleGlueCreateClassifier(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cls, name, ok := glueClassifierFromCreate(req, nil)
	if !ok || name == "" {
		glueWriteError(w, "InvalidInputException", "classifier Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, exists := glueClassifiers.Get(name); exists {
		glueWriteError(w, "AlreadyExistsException", "Classifier already exists: "+name)
		return
	}
	glueClassifiers.Put(name, cls)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetClassifier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	cls, ok := glueClassifiers.Get(req.Name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+req.Name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Classifier": cls})
}

func handleGlueGetClassifiers(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueClassifiers.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(all, req.NextToken, maxR, 100)
	resp := map[string]any{"Classifiers": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateClassifier(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	// The classifier name comes from the single set sub-object's Name field.
	var name string
	for _, raw := range req {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err == nil {
			if n, ok := m["Name"].(string); ok {
				name = n
				break
			}
		}
	}
	if name == "" {
		glueWriteError(w, "InvalidInputException", "classifier Name is required")
		return
	}
	prior, ok := glueClassifiers.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+name)
		return
	}
	cls, _, ok := glueClassifierFromCreate(req, &prior)
	if !ok {
		glueWriteError(w, "InvalidInputException", "invalid classifier input")
		return
	}
	glueClassifiers.Put(name, cls)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueDeleteClassifier(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"Name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	if _, ok := glueClassifiers.Get(req.Name); !ok {
		glueWriteError(w, "EntityNotFoundException", "Classifier not found: "+req.Name)
		return
	}
	glueClassifiers.Delete(req.Name)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

// glueUDFKey keys a UDF by its database + function name.
func glueUDFKey(db, fn string) string { return db + "\x00" + fn }

func handleGlueCreateUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogId     string `json:"CatalogId"`
		DatabaseName  string `json:"DatabaseName"`
		FunctionInput struct {
			FunctionName string           `json:"FunctionName"`
			ClassName    string           `json:"ClassName"`
			OwnerName    string           `json:"OwnerName"`
			OwnerType    string           `json:"OwnerType"`
			FunctionType string           `json:"FunctionType"`
			ResourceUris []map[string]any `json:"ResourceUris"`
		} `json:"FunctionInput"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.DatabaseName == "" || req.FunctionInput.FunctionName == "" {
		glueWriteError(w, "InvalidInputException", "DatabaseName and FunctionInput.FunctionName are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueDatabases.Get(req.DatabaseName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Database not found: "+req.DatabaseName)
		return
	}
	key := glueUDFKey(req.DatabaseName, req.FunctionInput.FunctionName)
	if _, ok := glueUDFs.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Function already exists: "+req.FunctionInput.FunctionName)
		return
	}
	glueUDFs.Put(key, GlueUserDefinedFunction{
		FunctionName: req.FunctionInput.FunctionName,
		DatabaseName: req.DatabaseName,
		ClassName:    req.FunctionInput.ClassName,
		OwnerName:    req.FunctionInput.OwnerName,
		OwnerType:    req.FunctionInput.OwnerType,
		FunctionType: req.FunctionInput.FunctionType,
		ResourceUris: req.FunctionInput.ResourceUris,
		CatalogId:    req.CatalogId,
		CreateTime:   glueEpochNow(),
	})
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		FunctionName string `json:"FunctionName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	udf, ok := glueUDFs.Get(glueUDFKey(req.DatabaseName, req.FunctionName))
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Function not found: "+req.FunctionName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"UserDefinedFunction": udf})
}

func handleGlueGetUserDefinedFunctions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		Pattern      string `json:"Pattern"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueUDFs.List()
	filtered := make([]GlueUserDefinedFunction, 0, len(all))
	for _, udf := range all {
		if req.DatabaseName != "" && udf.DatabaseName != req.DatabaseName {
			continue
		}
		filtered = append(filtered, udf)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(filtered, req.NextToken, maxR, 100)
	resp := map[string]any{"UserDefinedFunctions": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteUserDefinedFunction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		FunctionName string `json:"FunctionName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	key := glueUDFKey(req.DatabaseName, req.FunctionName)
	if _, ok := glueUDFs.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Function not found: "+req.FunctionName)
		return
	}
	glueUDFs.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueCreateRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryName string            `json:"RegistryName"`
		Description  string            `json:"Description"`
		Tags         map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.RegistryName == "" {
		glueWriteError(w, "InvalidInputException", "RegistryName is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueRegistries.Get(req.RegistryName); ok {
		glueWriteError(w, "AlreadyExistsException", "Registry already exists: "+req.RegistryName)
		return
	}
	arn := glueGlueArn("registry/" + req.RegistryName)
	now := glueRFC3339()
	glueRegistries.Put(req.RegistryName, GlueRegistry{
		RegistryName: req.RegistryName,
		RegistryArn:  arn,
		Description:  req.Description,
		Status:       "AVAILABLE",
		CreatedTime:  now,
		UpdatedTime:  now,
		Tags:         req.Tags,
	})
	resp := map[string]any{
		"RegistryArn":  arn,
		"RegistryName": req.RegistryName,
	}
	if req.Description != "" {
		resp["Description"] = req.Description
	}
	if len(req.Tags) > 0 {
		resp["Tags"] = req.Tags
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueRegistryName resolves a RegistryId wrapper to the registry name (by name or ARN).
func glueRegistryName(name, arn string) string {
	if name != "" {
		return name
	}
	if i := strings.LastIndex(arn, "registry/"); i >= 0 {
		return arn[i+len("registry/"):]
	}
	return ""
}

func handleGlueGetRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	name := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	reg, ok := glueRegistries.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+name)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName": reg.RegistryName,
		"RegistryArn":  reg.RegistryArn,
		"Description":  reg.Description,
		"Status":       reg.Status,
		"CreatedTime":  reg.CreatedTime,
		"UpdatedTime":  reg.UpdatedTime,
	})
}

func handleGlueListRegistries(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	all := glueRegistries.List()
	items := make([]map[string]any, 0, len(all))
	for _, reg := range all {
		items = append(items, map[string]any{
			"RegistryName": reg.RegistryName,
			"RegistryArn":  reg.RegistryArn,
			"Description":  reg.Description,
			"Status":       reg.Status,
			"CreatedTime":  reg.CreatedTime,
			"UpdatedTime":  reg.UpdatedTime,
		})
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(items, req.NextToken, maxR, 100)
	resp := map[string]any{"Registries": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteRegistry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	name := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	reg, ok := glueRegistries.Get(name)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+name)
		return
	}
	// Delete the registry and any schemas it contains.
	for _, sc := range glueSchemas.List() {
		if sc.RegistryName == name {
			glueSchemas.Delete(glueSchemaKey(name, sc.SchemaName))
		}
	}
	glueRegistries.Delete(name)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName": reg.RegistryName,
		"RegistryArn":  reg.RegistryArn,
		"Status":       "DELETING",
	})
}

func glueSchemaKey(registry, schema string) string { return registry + "\x00" + schema }

func handleGlueCreateSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistryId struct {
			RegistryName string `json:"RegistryName"`
			RegistryArn  string `json:"RegistryArn"`
		} `json:"RegistryId"`
		SchemaName       string            `json:"SchemaName"`
		DataFormat       string            `json:"DataFormat"`
		Compatibility    string            `json:"Compatibility"`
		Description      string            `json:"Description"`
		SchemaDefinition string            `json:"SchemaDefinition"`
		Tags             map[string]string `json:"Tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.SchemaName == "" || req.DataFormat == "" {
		glueWriteError(w, "InvalidInputException", "SchemaName and DataFormat are required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	registryName := glueRegistryName(req.RegistryId.RegistryName, req.RegistryId.RegistryArn)
	if registryName == "" {
		registryName = "default-registry"
	}
	registryArn := glueGlueArn("registry/" + registryName)
	if reg, ok := glueRegistries.Get(registryName); ok {
		registryArn = reg.RegistryArn
	} else if registryName != "default-registry" {
		glueWriteError(w, "EntityNotFoundException", "Registry not found: "+registryName)
		return
	}

	key := glueSchemaKey(registryName, req.SchemaName)
	if _, ok := glueSchemas.Get(key); ok {
		glueWriteError(w, "AlreadyExistsException", "Schema already exists: "+req.SchemaName)
		return
	}
	compat := req.Compatibility
	if compat == "" {
		compat = "BACKWARD"
	}
	schemaArn := glueGlueArn("schema/" + registryName + "/" + req.SchemaName)
	now := glueRFC3339()
	sc := GlueSchema{
		RegistryName:        registryName,
		RegistryArn:         registryArn,
		SchemaName:          req.SchemaName,
		SchemaArn:           schemaArn,
		Description:         req.Description,
		DataFormat:          req.DataFormat,
		Compatibility:       compat,
		SchemaCheckpoint:    1,
		LatestSchemaVersion: 1,
		NextSchemaVersion:   2,
		SchemaStatus:        "AVAILABLE",
		CreatedTime:         now,
		UpdatedTime:         now,
		SchemaVersionId:     uuid.NewString(),
		Tags:                req.Tags,
	}
	glueSchemas.Put(key, sc)
	glueSchemaVers.Put(sc.SchemaVersionId, GlueSchemaVersion{
		SchemaVersionId:  sc.SchemaVersionId,
		SchemaArn:        schemaArn,
		RegistryName:     registryName,
		SchemaName:       req.SchemaName,
		VersionNumber:    1,
		SchemaDefinition: req.SchemaDefinition,
		DataFormat:       req.DataFormat,
		Status:           "AVAILABLE",
		CreatedTime:      now,
	})
	resp := map[string]any{
		"RegistryName":        registryName,
		"RegistryArn":         registryArn,
		"SchemaName":          req.SchemaName,
		"SchemaArn":           schemaArn,
		"DataFormat":          req.DataFormat,
		"Compatibility":       compat,
		"SchemaCheckpoint":    sc.SchemaCheckpoint,
		"LatestSchemaVersion": sc.LatestSchemaVersion,
		"NextSchemaVersion":   sc.NextSchemaVersion,
		"SchemaStatus":        sc.SchemaStatus,
		"SchemaVersionId":     sc.SchemaVersionId,
		"SchemaVersionStatus": "AVAILABLE",
	}
	if req.Description != "" {
		resp["Description"] = req.Description
	}
	if len(req.Tags) > 0 {
		resp["Tags"] = req.Tags
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueResolveSchema locates a schema from a SchemaId wrapper (by ARN, or by
// registry name + schema name).
func glueResolveSchema(schemaArn, schemaName, registryName string) (GlueSchema, bool) {
	if schemaArn != "" {
		for _, sc := range glueSchemas.List() {
			if sc.SchemaArn == schemaArn {
				return sc, true
			}
		}
		return GlueSchema{}, false
	}
	if registryName == "" {
		registryName = "default-registry"
	}
	return glueSchemas.Get(glueSchemaKey(registryName, schemaName))
}

func handleGlueGetSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"RegistryName":        sc.RegistryName,
		"RegistryArn":         sc.RegistryArn,
		"SchemaName":          sc.SchemaName,
		"SchemaArn":           sc.SchemaArn,
		"Description":         sc.Description,
		"DataFormat":          sc.DataFormat,
		"Compatibility":       sc.Compatibility,
		"SchemaCheckpoint":    sc.SchemaCheckpoint,
		"LatestSchemaVersion": sc.LatestSchemaVersion,
		"NextSchemaVersion":   sc.NextSchemaVersion,
		"SchemaStatus":        sc.SchemaStatus,
		"CreatedTime":         sc.CreatedTime,
		"UpdatedTime":         sc.UpdatedTime,
	})
}

func handleGlueDeleteSchema(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	glueMu.Lock()
	defer glueMu.Unlock()
	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	glueSchemas.Delete(glueSchemaKey(sc.RegistryName, sc.SchemaName))
	for _, v := range glueSchemaVers.List() {
		if v.SchemaArn == sc.SchemaArn {
			glueSchemaVers.Delete(v.SchemaVersionId)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaArn":  sc.SchemaArn,
		"SchemaName": sc.SchemaName,
		"Status":     "DELETING",
	})
}

// glueResolveTableVersion returns the version id to read for a request: an
// explicit VersionId if given, otherwise the latest recorded version.
// Returns ("", false) if the table has no recorded versions.
func glueResolveTableVersion(database, table, versionID string) (string, bool) {
	if versionID != "" {
		if _, ok := glueTableVersions.Get(glueTableVersionKey(database, table, versionID)); ok {
			return versionID, true
		}
		return "", false
	}
	latest := glueLatestTableVersion(database, table)
	if latest == 0 {
		return "", false
	}
	return strconv.Itoa(latest), true
}

func handleGlueGetTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		VersionId    string `json:"VersionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	verID, ok := glueResolveTableVersion(req.DatabaseName, req.TableName, req.VersionId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Table version not found")
		return
	}
	tv, _ := glueTableVersions.Get(glueTableVersionKey(req.DatabaseName, req.TableName, verID))
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"TableVersion": map[string]any{
			"Table":     tv.Table,
			"VersionId": tv.VersionId,
		},
	})
}

func handleGlueGetTableVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		NextToken    string `json:"NextToken"`
		MaxResults   *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := glueTables.Get(glueTableKey(req.DatabaseName, req.TableName)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table not found: "+req.TableName)
		return
	}
	var versions []GlueTableVersion
	for _, v := range glueTableVersions.List() {
		if v.DatabaseName == req.DatabaseName && v.TableName == req.TableName {
			versions = append(versions, v)
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, _ := strconv.Atoi(versions[i].VersionId)
		b, _ := strconv.Atoi(versions[j].VersionId)
		return a < b
	})
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(versions, req.NextToken, maxR, 100)
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		out = append(out, map[string]any{
			"Table":     v.Table,
			"VersionId": v.VersionId,
		})
	}
	resp := map[string]any{"TableVersions": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		VersionId    string `json:"VersionId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	key := glueTableVersionKey(req.DatabaseName, req.TableName, req.VersionId)
	if _, ok := glueTableVersions.Get(key); !ok {
		glueWriteError(w, "EntityNotFoundException", "Table version not found: "+req.VersionId)
		return
	}
	glueTableVersions.Delete(key)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueBatchDeleteTableVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string   `json:"DatabaseName"`
		TableName    string   `json:"TableName"`
		VersionIds   []string `json:"VersionIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var errs []map[string]any
	for _, vid := range req.VersionIds {
		key := glueTableVersionKey(req.DatabaseName, req.TableName, vid)
		if _, ok := glueTableVersions.Get(key); !ok {
			errs = append(errs, map[string]any{
				"TableName": req.TableName,
				"VersionId": vid,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Table version not found: " + vid,
				},
			})
			continue
		}
		glueTableVersions.Delete(key)
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["Errors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueColumnStatKey is the store key for one column's statistics, scoped to a
// table (empty values) or a partition.
func glueColumnStatKey(database, table string, values []string, column string) string {
	return database + "\x00" + table + "\x00" + strings.Join(values, "\x1f") + "\x00" + column
}

func handleGlueUpdateColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName         string           `json:"DatabaseName"`
		TableName            string           `json:"TableName"`
		ColumnStatisticsList []map[string]any `json:"ColumnStatisticsList"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	for _, cs := range req.ColumnStatisticsList {
		col := glueString(cs["ColumnName"])
		if col == "" {
			continue
		}
		glueColumnStats.Put(glueColumnStatKey(req.DatabaseName, req.TableName, nil, col), GlueColumnStatisticsRecord{
			DatabaseName: req.DatabaseName,
			TableName:    req.TableName,
			ColumnName:   col,
			Statistics:   cs,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string   `json:"DatabaseName"`
		TableName    string   `json:"TableName"`
		ColumnNames  []string `json:"ColumnNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	list := []map[string]any{}
	for _, col := range req.ColumnNames {
		rec, ok := glueColumnStats.Get(glueColumnStatKey(req.DatabaseName, req.TableName, nil, col))
		if ok {
			list = append(list, rec.Statistics)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsList": list})
}

func handleGlueDeleteColumnStatisticsForTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName string `json:"DatabaseName"`
		TableName    string `json:"TableName"`
		ColumnName   string `json:"ColumnName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if !glueRequirePartitionTable(w, req.DatabaseName, req.TableName) {
		return
	}
	glueColumnStats.Delete(glueColumnStatKey(req.DatabaseName, req.TableName, nil, req.ColumnName))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueUpdateColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName         string           `json:"DatabaseName"`
		TableName            string           `json:"TableName"`
		PartitionValues      []string         `json:"PartitionValues"`
		ColumnStatisticsList []map[string]any `json:"ColumnStatisticsList"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	for _, cs := range req.ColumnStatisticsList {
		col := glueString(cs["ColumnName"])
		if col == "" {
			continue
		}
		glueColumnStats.Put(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, col), GlueColumnStatisticsRecord{
			DatabaseName:    req.DatabaseName,
			TableName:       req.TableName,
			PartitionValues: req.PartitionValues,
			ColumnName:      col,
			Statistics:      cs,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
		ColumnNames     []string `json:"ColumnNames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.RLock()
	defer glueMu.RUnlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	list := []map[string]any{}
	for _, col := range req.ColumnNames {
		rec, ok := glueColumnStats.Get(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, col))
		if ok {
			list = append(list, rec.Statistics)
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ColumnStatisticsList": list})
}

func handleGlueDeleteColumnStatisticsForPartition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DatabaseName    string   `json:"DatabaseName"`
		TableName       string   `json:"TableName"`
		PartitionValues []string `json:"PartitionValues"`
		ColumnName      string   `json:"ColumnName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := gluePartitions.Get(gluePartitionKey(req.DatabaseName, req.TableName, req.PartitionValues)); !ok {
		glueWriteError(w, "EntityNotFoundException", "Partition not found")
		return
	}
	glueColumnStats.Delete(glueColumnStatKey(req.DatabaseName, req.TableName, req.PartitionValues, req.ColumnName))
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGluePutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyInJson string `json:"PolicyInJson"`
		ResourceArn  string `json:"ResourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.PolicyInJson == "" {
		glueWriteError(w, "InvalidInputException", "PolicyInJson is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	now := glueEpochNow()
	create := now
	if existing, ok := glueResourcePols.Get(glueResourcePolicyKey); ok {
		create = existing.CreateTime
	}
	hash := fmt.Sprintf("%x", uuid.NewSHA1(uuid.Nil, []byte(req.PolicyInJson)))
	glueResourcePols.Put(glueResourcePolicyKey, GlueResourcePolicy{
		PolicyInJson: req.PolicyInJson,
		PolicyHash:   hash,
		CreateTime:   create,
		UpdateTime:   now,
	})
	arn := req.ResourceArn
	if arn == "" {
		arn = glueGlueArn("catalog")
	}
	iamPutResourcePolicy(arn, req.PolicyInJson)
	glueWriteJSON(w, http.StatusOK, map[string]any{"PolicyHash": hash})
}

func handleGlueGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	rp, ok := glueResourcePols.Get(glueResourcePolicyKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "No resource policy exists")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"PolicyInJson": rp.PolicyInJson,
		"PolicyHash":   rp.PolicyHash,
		"CreateTime":   rp.CreateTime,
		"UpdateTime":   rp.UpdateTime,
	})
}

func handleGlueDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueResourcePols.Get(glueResourcePolicyKey); !ok {
		glueWriteError(w, "EntityNotFoundException", "No resource policy exists")
		return
	}
	glueResourcePols.Delete(glueResourcePolicyKey)
	arn := req.ResourceArn
	if arn == "" {
		arn = glueGlueArn("catalog")
	}
	iamDeleteResourcePolicy(arn)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGluePutDataCatalogEncryptionSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataCatalogEncryptionSettings map[string]any `json:"DataCatalogEncryptionSettings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	settings.DataCatalogEncryptionSettings = req.DataCatalogEncryptionSettings
	glueCatalogSettgs.Put(glueCatalogSettingsKey, settings)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetDataCatalogEncryptionSettings(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	dcs := settings.DataCatalogEncryptionSettings
	if dcs == nil {
		// AWS returns an empty (default-disabled) settings object when none set.
		dcs = map[string]any{
			"EncryptionAtRest":             map[string]any{"CatalogEncryptionMode": "DISABLED"},
			"ConnectionPasswordEncryption": map[string]any{"ReturnConnectionPasswordEncrypted": false},
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"DataCatalogEncryptionSettings": dcs})
}

func handleGlueImportCatalogToGlue(w http.ResponseWriter, r *http.Request) {
	glueMu.Lock()
	defer glueMu.Unlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	now := glueEpochNow()
	settings.ImportCompleted = true
	settings.ImportTime = &now
	settings.ImportedBy = "arn:aws:iam::123456789012:root"
	glueCatalogSettgs.Put(glueCatalogSettingsKey, settings)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetCatalogImportStatus(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	status := map[string]any{"ImportCompleted": settings.ImportCompleted}
	if settings.ImportTime != nil {
		status["ImportTime"] = *settings.ImportTime
	}
	if settings.ImportedBy != "" {
		status["ImportedBy"] = settings.ImportedBy
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ImportStatus": status})
}

// glueSchemaVersionsFor returns all versions belonging to a schema ARN, sorted
// ascending by VersionNumber.
func glueSchemaVersionsFor(schemaArn string) []GlueSchemaVersion {
	var out []GlueSchemaVersion
	for _, v := range glueSchemaVers.List() {
		if v.SchemaArn == schemaArn {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VersionNumber < out[j].VersionNumber })
	return out
}

func handleGlueRegisterSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaDefinition string `json:"SchemaDefinition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.SchemaDefinition == "" {
		glueWriteError(w, "InvalidInputException", "SchemaDefinition is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	// If an existing version carries the identical definition, return it
	// idempotently (matching the real registry's de-dup behavior).
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.SchemaDefinition == req.SchemaDefinition {
			glueWriteJSON(w, http.StatusOK, map[string]any{
				"SchemaVersionId": v.SchemaVersionId,
				"VersionNumber":   v.VersionNumber,
				"Status":          v.Status,
			})
			return
		}
	}
	newVer := sc.LatestSchemaVersion + 1
	versionID := uuid.NewString()
	glueSchemaVers.Put(versionID, GlueSchemaVersion{
		SchemaVersionId:  versionID,
		SchemaArn:        sc.SchemaArn,
		RegistryName:     sc.RegistryName,
		SchemaName:       sc.SchemaName,
		VersionNumber:    newVer,
		SchemaDefinition: req.SchemaDefinition,
		DataFormat:       sc.DataFormat,
		Status:           "AVAILABLE",
		CreatedTime:      glueRFC3339(),
	})
	sc.LatestSchemaVersion = newVer
	sc.NextSchemaVersion = newVer + 1
	sc.UpdatedTime = glueRFC3339()
	glueSchemas.Put(glueSchemaKey(sc.RegistryName, sc.SchemaName), sc)
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaVersionId": versionID,
		"VersionNumber":   newVer,
		"Status":          "AVAILABLE",
	})
}

func handleGlueGetSchemaVersion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaVersionId     string `json:"SchemaVersionId"`
		SchemaVersionNumber struct {
			LatestVersion bool  `json:"LatestVersion"`
			VersionNumber int64 `json:"VersionNumber"`
		} `json:"SchemaVersionNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	var ver GlueSchemaVersion
	found := false
	if req.SchemaVersionId != "" {
		ver, found = glueSchemaVers.Get(req.SchemaVersionId)
	} else {
		sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
		if !ok {
			glueWriteError(w, "EntityNotFoundException", "Schema not found")
			return
		}
		versions := glueSchemaVersionsFor(sc.SchemaArn)
		if len(versions) == 0 {
			glueWriteError(w, "EntityNotFoundException", "Schema version not found")
			return
		}
		if req.SchemaVersionNumber.VersionNumber > 0 && !req.SchemaVersionNumber.LatestVersion {
			for _, v := range versions {
				if v.VersionNumber == req.SchemaVersionNumber.VersionNumber {
					ver, found = v, true
					break
				}
			}
		} else {
			ver, found = versions[len(versions)-1], true
		}
	}
	if !found {
		glueWriteError(w, "EntityNotFoundException", "Schema version not found")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SchemaVersionId":  ver.SchemaVersionId,
		"SchemaDefinition": ver.SchemaDefinition,
		"DataFormat":       ver.DataFormat,
		"SchemaArn":        ver.SchemaArn,
		"VersionNumber":    ver.VersionNumber,
		"Status":           ver.Status,
		"CreatedTime":      ver.CreatedTime,
	})
}

func handleGlueListSchemaVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	versions := glueSchemaVersionsFor(sc.SchemaArn)
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok := awsPage(versions, req.NextToken, maxR, 25)
	out := make([]map[string]any, 0, len(page))
	for _, v := range page {
		out = append(out, map[string]any{
			"SchemaArn":       v.SchemaArn,
			"SchemaVersionId": v.SchemaVersionId,
			"VersionNumber":   v.VersionNumber,
			"Status":          v.Status,
			"CreatedTime":     v.CreatedTime,
		})
	}
	resp := map[string]any{"Schemas": out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueGetSchemaByDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		SchemaDefinition string `json:"SchemaDefinition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.SchemaDefinition == req.SchemaDefinition {
			glueWriteJSON(w, http.StatusOK, map[string]any{
				"SchemaVersionId": v.SchemaVersionId,
				"SchemaArn":       v.SchemaArn,
				"DataFormat":      v.DataFormat,
				"Status":          v.Status,
				"CreatedTime":     v.CreatedTime,
			})
			return
		}
	}
	glueWriteError(w, "EntityNotFoundException", "No schema version found for the given definition")
}

func handleGlueDeleteSchemaVersions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SchemaId struct {
			SchemaArn    string `json:"SchemaArn"`
			SchemaName   string `json:"SchemaName"`
			RegistryName string `json:"RegistryName"`
		} `json:"SchemaId"`
		Versions string `json:"Versions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	sc, ok := glueResolveSchema(req.SchemaId.SchemaArn, req.SchemaId.SchemaName, req.SchemaId.RegistryName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Schema not found")
		return
	}
	lo, hi, ok := glueParseVersionRange(req.Versions)
	if !ok {
		glueWriteError(w, "InvalidInputException", "Invalid Versions range: "+req.Versions)
		return
	}
	var errs []map[string]any
	for _, v := range glueSchemaVersionsFor(sc.SchemaArn) {
		if v.VersionNumber >= lo && v.VersionNumber <= hi {
			glueSchemaVers.Delete(v.SchemaVersionId)
		}
	}
	resp := map[string]any{}
	if len(errs) > 0 {
		resp["SchemaVersionErrors"] = errs
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

// glueParseVersionRange parses a single version ("5") or an inclusive range
// ("5-8") into (lo, hi).
func glueParseVersionRange(s string) (lo, hi int64, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	if i := strings.Index(s, "-"); i >= 0 {
		a, err1 := strconv.ParseInt(strings.TrimSpace(s[:i]), 10, 64)
		b, err2 := strconv.ParseInt(strings.TrimSpace(s[i+1:]), 10, 64)
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		if a > b {
			a, b = b, a
		}
		return a, b, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return n, n, true
}
