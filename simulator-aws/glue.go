package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
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
	LogGroupName  string            `json:"LogGroupName,omitempty"`
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

func registerGlue(r *AWSRouter, srv *sim.Server) {
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
	registerGlueSystemLogs(r)
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
	awsWriteError(w, awsErrorEnvelope{Protocol: awsJSON11, Model: "glue"}, http.StatusBadRequest, code, msg, "")
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

// glueRFC3339 formats a timestamp the way Glue schema-registry shapes return
// CreatedTime/UpdatedTime (an ISO-8601/RFC3339 string, not an epoch number).
func glueRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func glueGlueArn(resource string) string {
	return "arn:aws:glue:" + awsRegion() + ":" + awsAccountID() + ":" + resource
}

// glueListNames answers a List call with the names of store's resources, a
// NextToken page at a time.
func glueListNames[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], name func(T) string, field string, defaultMax int) {
	gluePage(w, r, store.List, name, func(it T) any { return name(it) }, field, defaultMax)
}

// gluePage answers a List or Get call with the wire form of the items list
// returns, a NextToken page at a time in the order of key.
func gluePage[T any](w http.ResponseWriter, r *http.Request, list func() []T, key func(T) string, wire func(T) any, field string, defaultMax int) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, list(), key, req.NextToken, derefIntDefault(req.MaxResults, 0), defaultMax)
	if !pageOK {
		return
	}
	out := make([]any, 0, len(page))
	for _, item := range page {
		out = append(out, wire(item))
	}
	resp := map[string]any{field: out}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}
