package main

// DynamoDB extended control-plane operations: backups, point-in-time
// restore, global tables (legacy + current), resource-based policies,
// Kinesis streaming destinations, table exports/imports, and contributor
// insights. These are faithful CRUD over per-resource metadata/state — the
// sim stores the configuration the SDK/CLI/terraform write and reads back the
// real DynamoDB shapes + error codes (ResourceNotFoundException,
// ResourceInUseException, BackupNotFoundException, GlobalTableNotFoundException,
// GlobalTableAlreadyExistsException, ExportNotFoundException,
// ImportNotFoundException, PolicyNotFoundException).
//
// Timestamps serialize as Unix-epoch JSON numbers (the awsJson1_0 protocol's
// timestamp form for DynamoDB — the SDK deserializer calls
// smithytime.ParseEpochSeconds, which rejects an RFC3339 string).

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// --- Stores ------------------------------------------------------------------

// DDBBackup is a point-in-time snapshot of a table's schema + items, taken by
// CreateBackup. The item snapshot is stored verbatim so RestoreTableFromBackup
// can recreate the table exactly.
type DDBBackup struct {
	BackupArn          string                    `json:"BackupArn"`
	BackupName         string                    `json:"BackupName"`
	BackupStatus       string                    `json:"BackupStatus"`
	BackupType         string                    `json:"BackupType"`
	BackupCreationTime float64                   `json:"BackupCreationTime"`
	BackupSizeBytes    int64                     `json:"BackupSizeBytes"`
	TableName          string                    `json:"TableName"`
	TableId            string                    `json:"TableId"`
	TableArn           string                    `json:"TableArn"`
	TableCreationTime  float64                   `json:"TableCreationTime"`
	KeySchema          []DDBKeySchemaEntry       `json:"KeySchema"`
	AttributeDefs      []DDBAttributeDef         `json:"AttributeDefs"`
	GSIs               []DDBGlobalSecondaryIndex `json:"GSIs"`
	LSIs               []DDBLocalSecondaryIndex  `json:"LSIs"`
	BillingMode        string                    `json:"BillingMode"`
	Items              map[string]map[string]any `json:"Items"` // itemKey -> item
}

// DDBGlobalTable is a legacy (2017.11.29) global table: a named set of region
// replicas over a same-named table.
type DDBGlobalTable struct {
	GlobalTableName  string   `json:"GlobalTableName"`
	GlobalTableArn   string   `json:"GlobalTableArn"`
	CreationDateTime float64  `json:"CreationDateTime"`
	Replicas         []string `json:"Replicas"` // region names
	// Settings persisted by UpdateGlobalTableSettings.
	BillingMode             string           `json:"BillingMode"`
	ProvisionedWriteUnits   int64            `json:"ProvisionedWriteUnits"`
	ProvisionedReadUnits    int64            `json:"ProvisionedReadUnits"`
	ProvisionedReadByRegion map[string]int64 `json:"ProvisionedReadByRegion"`
}

// DDBStreamDestination is a Kinesis Data Streams destination associated with a
// table by EnableKinesisStreamingDestination.
type DDBStreamDestination struct {
	TableName                            string  `json:"TableName"`
	StreamArn                            string  `json:"StreamArn"`
	DestinationStatus                    string  `json:"DestinationStatus"`
	ApproximateCreationDateTimePrecision string  `json:"ApproximateCreationDateTimePrecision"`
	CreatedAt                            float64 `json:"CreatedAt"`
}

// DDBExport is a table export record created by ExportTableToPointInTime.
type DDBExport struct {
	ExportArn    string  `json:"ExportArn"`
	TableArn     string  `json:"TableArn"`
	TableId      string  `json:"TableId"`
	ExportStatus string  `json:"ExportStatus"`
	ExportFormat string  `json:"ExportFormat"`
	ExportType   string  `json:"ExportType"`
	ExportTime   float64 `json:"ExportTime"`
	StartTime    float64 `json:"StartTime"`
	EndTime      float64 `json:"EndTime"`
	S3Bucket     string  `json:"S3Bucket"`
	S3Prefix     string  `json:"S3Prefix"`
	ItemCount    int64   `json:"ItemCount"`
	ClientToken  string  `json:"ClientToken"`
}

// DDBImport is a table import record created by ImportTable.
type DDBImport struct {
	ImportArn          string              `json:"ImportArn"`
	ImportStatus       string              `json:"ImportStatus"`
	TableArn           string              `json:"TableArn"`
	TableId            string              `json:"TableId"`
	TableName          string              `json:"TableName"`
	InputFormat        string              `json:"InputFormat"`
	StartTime          float64             `json:"StartTime"`
	EndTime            float64             `json:"EndTime"`
	ImportedItemCount  int64               `json:"ImportedItemCount"`
	ProcessedItemCount int64               `json:"ProcessedItemCount"`
	S3Bucket           string              `json:"S3Bucket"`
	S3KeyPrefix        string              `json:"S3KeyPrefix"`
	ClientToken        string              `json:"ClientToken"`
	KeySchema          []DDBKeySchemaEntry `json:"KeySchema"`
	AttributeDefs      []DDBAttributeDef   `json:"AttributeDefs"`
	BillingMode        string              `json:"BillingMode"`
}

// DDBContributorInsight is the CloudWatch Contributor Insights enable/disable
// state for a table or one of its indexes.
type DDBContributorInsight struct {
	TableName string  `json:"TableName"`
	IndexName string  `json:"IndexName"`
	Status    string  `json:"Status"` // ENABLED / DISABLED
	Mode      string  `json:"Mode"`
	UpdatedAt float64 `json:"UpdatedAt"`
}

var (
	ddbBackups      sim.Store[DDBBackup]
	ddbGlobalTables sim.Store[DDBGlobalTable]
	ddbStreamDests  sim.Store[DDBStreamDestination]
	ddbExports      sim.Store[DDBExport]
	ddbImports      sim.Store[DDBImport]
	ddbContribInsts sim.Store[DDBContributorInsight]
	ddbResourcePols sim.Store[IAMResourcePolicy]
	ddbExtendedMu   sync.Mutex
)

// registerDynamoDBExtended mounts the extended control-plane handlers onto the
// existing DynamoDB JSON router and initializes the backing stores. Called from
// registerDynamoDB.
func registerDynamoDBExtended(r *sim.AWSRouter, srv *sim.Server) {
	ddbBackups = sim.MakeStore[DDBBackup](srv.DB(), "ddb_backups")
	ddbGlobalTables = sim.MakeStore[DDBGlobalTable](srv.DB(), "ddb_global_tables")
	ddbStreamDests = sim.MakeStore[DDBStreamDestination](srv.DB(), "ddb_stream_dests")
	ddbExports = sim.MakeStore[DDBExport](srv.DB(), "ddb_exports")
	ddbImports = sim.MakeStore[DDBImport](srv.DB(), "ddb_imports")
	ddbContribInsts = sim.MakeStore[DDBContributorInsight](srv.DB(), "ddb_contributor_insights")
	ddbResourcePols = sim.MakeStore[IAMResourcePolicy](srv.DB(), "ddb_resource_policies")

	reg := func(target string, h http.HandlerFunc) {
		op := strings.TrimPrefix(target, "DynamoDB_20120810.")
		r.Register(target, ddbRequire(ddbRequiredMembers[op], h))
	}

	// Backups.
	reg("DynamoDB_20120810.CreateBackup", handleDDBCreateBackup)
	reg("DynamoDB_20120810.DescribeBackup", handleDDBDescribeBackup)
	reg("DynamoDB_20120810.ListBackups", handleDDBListBackups)
	reg("DynamoDB_20120810.DeleteBackup", handleDDBDeleteBackup)
	reg("DynamoDB_20120810.RestoreTableFromBackup", handleDDBRestoreTableFromBackup)
	reg("DynamoDB_20120810.RestoreTableToPointInTime", handleDDBRestoreTableToPointInTime)

	// Global tables (legacy + current).
	reg("DynamoDB_20120810.CreateGlobalTable", handleDDBCreateGlobalTable)
	reg("DynamoDB_20120810.DescribeGlobalTable", handleDDBDescribeGlobalTable)
	reg("DynamoDB_20120810.ListGlobalTables", handleDDBListGlobalTables)
	reg("DynamoDB_20120810.UpdateGlobalTable", handleDDBUpdateGlobalTable)
	reg("DynamoDB_20120810.DescribeGlobalTableSettings", handleDDBDescribeGlobalTableSettings)
	reg("DynamoDB_20120810.UpdateGlobalTableSettings", handleDDBUpdateGlobalTableSettings)
	reg("DynamoDB_20120810.UpdateTableReplicaAutoScaling", handleDDBUpdateTableReplicaAutoScaling)
	reg("DynamoDB_20120810.DescribeTableReplicaAutoScaling", handleDDBDescribeTableReplicaAutoScaling)

	// Resource-based policy.
	reg("DynamoDB_20120810.PutResourcePolicy", handleDDBPutResourcePolicy)
	reg("DynamoDB_20120810.GetResourcePolicy", handleDDBGetResourcePolicy)
	reg("DynamoDB_20120810.DeleteResourcePolicy", handleDDBDeleteResourcePolicy)

	// Kinesis streaming destinations.
	reg("DynamoDB_20120810.EnableKinesisStreamingDestination", handleDDBEnableKinesisStreaming)
	reg("DynamoDB_20120810.DisableKinesisStreamingDestination", handleDDBDisableKinesisStreaming)
	reg("DynamoDB_20120810.DescribeKinesisStreamingDestination", handleDDBDescribeKinesisStreaming)
	reg("DynamoDB_20120810.UpdateKinesisStreamingDestination", handleDDBUpdateKinesisStreaming)

	// Exports / imports.
	reg("DynamoDB_20120810.ExportTableToPointInTime", handleDDBExportTableToPointInTime)
	reg("DynamoDB_20120810.DescribeExport", handleDDBDescribeExport)
	reg("DynamoDB_20120810.ListExports", handleDDBListExports)
	reg("DynamoDB_20120810.ImportTable", handleDDBImportTable)
	reg("DynamoDB_20120810.DescribeImport", handleDDBDescribeImport)
	reg("DynamoDB_20120810.ListImports", handleDDBListImports)

	// Contributor insights.
	reg("DynamoDB_20120810.UpdateContributorInsights", handleDDBUpdateContributorInsights)
	reg("DynamoDB_20120810.DescribeContributorInsights", handleDDBDescribeContributorInsights)
	reg("DynamoDB_20120810.ListContributorInsights", handleDDBListContributorInsights)

	// Endpoints discovery.
	reg("DynamoDB_20120810.DescribeEndpoints", handleDDBDescribeEndpoints)
}

func ddbBackupArn(table, id string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s/backup/%s", awsRegion(), awsAccountID(), table, id)
}

func ddbGlobalTableArn(name string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:global-table/%s", awsRegion(), awsAccountID(), name)
}

func ddbExportArn(tableArn, id string) string {
	return fmt.Sprintf("%s/export/%s", tableArn, id)
}

func ddbImportArn(tableArn, id string) string {
	return fmt.Sprintf("%s/import/%s", tableArn, id)
}

func ddbStreamArn(stream string) string {
	if stream == "" {
		return fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/ddb-default", awsRegion(), awsAccountID())
	}
	return stream
}

// ddbTableItemsSnapshot copies every stored item for a table into a fresh map
// keyed by the per-item store key. Reads under ddbItemsMu.
func ddbTableItemsSnapshot(tableName string) map[string]map[string]any {
	out := map[string]map[string]any{}
	prefix := tableName + "/"
	ddbItemsMu.RLock()
	defer ddbItemsMu.RUnlock()
	for _, k := range ddbItemNames.List() {
		if strings.HasPrefix(k, prefix) {
			if item, ok := ddbItems.Get(k); ok {
				out[k] = ddbCloneItem(item)
			}
		}
	}
	return out
}

// --- Backups -----------------------------------------------------------------

func handleDDBCreateBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName  string `json:"TableName"`
		BackupName string `json:"BackupName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "TableNotFoundException", http.StatusBadRequest,
			"Table not found: %s", req.TableName)
		return
	}
	now := float64(time.Now().Unix())
	id := generateUUID()
	items := ddbTableItemsSnapshot(req.TableName)
	size := int64(0)
	for _, it := range items {
		size += int64(ddbItemSizeBytes(it))
	}
	bk := DDBBackup{
		BackupArn:          ddbBackupArn(req.TableName, id),
		BackupName:         req.BackupName,
		BackupStatus:       "AVAILABLE",
		BackupType:         "USER",
		BackupCreationTime: now,
		BackupSizeBytes:    size,
		TableName:          req.TableName,
		TableId:            t.TableId,
		TableArn:           t.TableArn,
		TableCreationTime:  t.CreationDateTime,
		KeySchema:          t.KeySchema,
		AttributeDefs:      t.AttributeDefinitions,
		GSIs:               t.GlobalSecondaryIndexes,
		LSIs:               t.LocalSecondaryIndexes,
		BillingMode:        ddbBillingModeOf(t),
		Items:              items,
	}
	ddbBackups.Put(bk.BackupArn, bk)
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"BackupDetails": map[string]any{
			"BackupArn":              bk.BackupArn,
			"BackupName":             bk.BackupName,
			"BackupStatus":           bk.BackupStatus,
			"BackupType":             bk.BackupType,
			"BackupCreationDateTime": bk.BackupCreationTime,
			"BackupSizeBytes":        bk.BackupSizeBytes,
		},
	})
}

func ddbBillingModeOf(t DDBTable) string {
	if t.BillingModeSummary != nil && t.BillingModeSummary.BillingMode != "" {
		return t.BillingModeSummary.BillingMode
	}
	return "PROVISIONED"
}

// ddbBackupDescription assembles the BackupDescription shape DescribeBackup and
// DeleteBackup return.
func ddbBackupDescription(bk DDBBackup) map[string]any {
	keySchema := make([]map[string]any, 0, len(bk.KeySchema))
	for _, k := range bk.KeySchema {
		keySchema = append(keySchema, map[string]any{"AttributeName": k.AttributeName, "KeyType": k.KeyType})
	}
	return map[string]any{
		"BackupDetails": map[string]any{
			"BackupArn":              bk.BackupArn,
			"BackupName":             bk.BackupName,
			"BackupStatus":           bk.BackupStatus,
			"BackupType":             bk.BackupType,
			"BackupCreationDateTime": bk.BackupCreationTime,
			"BackupSizeBytes":        bk.BackupSizeBytes,
		},
		"SourceTableDetails": map[string]any{
			"TableName":             bk.TableName,
			"TableId":               bk.TableId,
			"TableArn":              bk.TableArn,
			"TableCreationDateTime": bk.TableCreationTime,
			"KeySchema":             keySchema,
			"ProvisionedThroughput": map[string]any{
				"ReadCapacityUnits":  0,
				"WriteCapacityUnits": 0,
			},
			"BillingMode": bk.BillingMode,
		},
	}
}

func handleDDBDescribeBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BackupArn string `json:"BackupArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bk, ok := ddbBackups.Get(req.BackupArn)
	if !ok {
		sim.AWSErrorf(w, "BackupNotFoundException", http.StatusBadRequest,
			"Backup not found: %s", req.BackupArn)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"BackupDescription": ddbBackupDescription(bk)})
}

func handleDDBDeleteBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BackupArn string `json:"BackupArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bk, ok := ddbBackups.Get(req.BackupArn)
	if !ok {
		sim.AWSErrorf(w, "BackupNotFoundException", http.StatusBadRequest,
			"Backup not found: %s", req.BackupArn)
		return
	}
	ddbBackups.Delete(req.BackupArn)
	bk.BackupStatus = "DELETED"
	writeDDBJSON(w, http.StatusOK, map[string]any{"BackupDescription": ddbBackupDescription(bk)})
}

func handleDDBListBackups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName               string `json:"TableName"`
		ExclusiveStartBackupArn string `json:"ExclusiveStartBackupArn"`
		Limit                   int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbBackups.List()
	filtered := all[:0:0]
	for _, b := range all {
		if req.TableName == "" || b.TableName == req.TableName {
			filtered = append(filtered, b)
		}
	}
	sortBy(filtered, func(b DDBBackup) string { return b.BackupArn })

	token := ""
	if req.ExclusiveStartBackupArn != "" {
		for i, b := range filtered {
			if b.BackupArn == req.ExclusiveStartBackupArn {
				token = strconv.Itoa(i + 1)
				break
			}
		}
	}
	page, next := awsPage(filtered, token, req.Limit, 100)
	summaries := make([]map[string]any, 0, len(page))
	for _, b := range page {
		summaries = append(summaries, map[string]any{
			"BackupArn":              b.BackupArn,
			"BackupName":             b.BackupName,
			"BackupStatus":           b.BackupStatus,
			"BackupType":             b.BackupType,
			"BackupCreationDateTime": b.BackupCreationTime,
			"BackupSizeBytes":        b.BackupSizeBytes,
			"TableArn":               b.TableArn,
			"TableId":                b.TableId,
			"TableName":              b.TableName,
		})
	}
	out := map[string]any{"BackupSummaries": summaries}
	if next != "" {
		idx, _ := strconv.Atoi(next)
		if idx > 0 && idx <= len(filtered) {
			out["LastEvaluatedBackupArn"] = filtered[idx-1].BackupArn
		}
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// ddbRestoreItems writes a backup/snapshot's items into the live item store
// under a new table name (re-keying from the source table prefix to target).
func ddbRestoreItems(items map[string]map[string]any, srcTable, dstTable string) {
	srcPrefix := srcTable + "/"
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	for k, item := range items {
		// Re-derive the store key under the target table so a same-shaped
		// recreate is queryable; the key suffix (hash[|range]) is reused.
		suffix := strings.TrimPrefix(k, srcPrefix)
		newKey := dstTable + "/" + suffix
		clone := ddbCloneItem(item)
		ddbItems.Put(newKey, clone)
		ddbItemNames.Put(newKey, newKey)
	}
	ddbBumpKeyGen()
}

func handleDDBRestoreTableFromBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BackupArn       string `json:"BackupArn"`
		TargetTableName string `json:"TargetTableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	bk, ok := ddbBackups.Get(req.BackupArn)
	if !ok {
		sim.AWSErrorf(w, "BackupNotFoundException", http.StatusBadRequest,
			"Backup not found: %s", req.BackupArn)
		return
	}
	if _, exists := ddbTables.Get(req.TargetTableName); exists {
		sim.AWSErrorf(w, "TableAlreadyExistsException", http.StatusBadRequest,
			"Table already exists: %s", req.TargetTableName)
		return
	}
	t := ddbRecreateTable(req.TargetTableName, bk.KeySchema, bk.AttributeDefs, bk.GSIs, bk.LSIs, bk.BillingMode)
	ddbTables.Put(req.TargetTableName, t)
	ddbRestoreItems(bk.Items, bk.TableName, req.TargetTableName)
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

func handleDDBRestoreTableToPointInTime(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceTableName string `json:"SourceTableName"`
		SourceTableArn  string `json:"SourceTableArn"`
		TargetTableName string `json:"TargetTableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	srcName := req.SourceTableName
	if srcName == "" && req.SourceTableArn != "" {
		if n, _, ok := ddbTableByArn(req.SourceTableArn); ok {
			srcName = n
		}
	}
	src, ok := ddbTables.Get(srcName)
	if !ok {
		sim.AWSErrorf(w, "TableNotFoundException", http.StatusBadRequest,
			"Source table not found: %s", srcName)
		return
	}
	// PITR must be enabled on the source (terraform/real AWS reject otherwise).
	settings, _ := ddbTableSettings.Get(srcName)
	if settings.PITRStatus != "ENABLED" {
		sim.AWSErrorf(w, "InvalidRestoreTimeException", http.StatusBadRequest,
			"Point in time recovery is not enabled for table: %s", srcName)
		return
	}
	if _, exists := ddbTables.Get(req.TargetTableName); exists {
		sim.AWSErrorf(w, "TableAlreadyExistsException", http.StatusBadRequest,
			"Table already exists: %s", req.TargetTableName)
		return
	}
	t := ddbRecreateTable(req.TargetTableName, src.KeySchema, src.AttributeDefinitions,
		src.GlobalSecondaryIndexes, src.LocalSecondaryIndexes, ddbBillingModeOf(src))
	ddbTables.Put(req.TargetTableName, t)
	items := ddbTableItemsSnapshot(srcName)
	ddbRestoreItems(items, srcName, req.TargetTableName)
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

// ddbRecreateTable builds a fresh ACTIVE table from a backup/source schema.
func ddbRecreateTable(name string, keySchema []DDBKeySchemaEntry, attrs []DDBAttributeDef,
	gsis []DDBGlobalSecondaryIndex, lsis []DDBLocalSecondaryIndex, billingMode string) DDBTable {
	if billingMode == "" {
		billingMode = "PROVISIONED"
	}
	now := float64(time.Now().Unix())
	activeGSIs := make([]DDBGlobalSecondaryIndex, 0, len(gsis))
	for _, g := range gsis {
		activeGSIs = append(activeGSIs, ddbActivateGSI(name, g))
	}
	activeLSIs := make([]DDBLocalSecondaryIndex, 0, len(lsis))
	for _, l := range lsis {
		l.IndexArn = ddbIndexArn(name, l.IndexName)
		activeLSIs = append(activeLSIs, l)
	}
	t := DDBTable{
		TableName:            name,
		TableId:              generateUUID(),
		TableArn:             ddbTableArn(name),
		TableStatus:          "ACTIVE",
		CreationDateTime:     now,
		AttributeDefinitions: attrs,
		KeySchema:            keySchema,
		BillingModeSummary:   &DDBBillingModeSummary{BillingMode: billingMode},
		ProvisionedThroughput: &DDBProvisionedThroughput{
			NumberOfDecreasesToday: 0,
			ReadCapacityUnits:      0,
			WriteCapacityUnits:     0,
		},
		TableClassSummary: &DDBTableClassSummary{TableClass: "STANDARD"},
		WarmThroughput: &DDBWarmThroughput{
			ReadUnitsPerSecond:  12000,
			WriteUnitsPerSecond: 4000,
			Status:              "ACTIVE",
		},
	}
	if len(activeGSIs) > 0 {
		t.GlobalSecondaryIndexes = activeGSIs
	}
	if len(activeLSIs) > 0 {
		t.LocalSecondaryIndexes = activeLSIs
	}
	return t
}

// --- Global tables -----------------------------------------------------------

func ddbGlobalTableDescription(gt DDBGlobalTable) map[string]any {
	replicas := make([]map[string]any, 0, len(gt.Replicas))
	for _, region := range gt.Replicas {
		replicas = append(replicas, map[string]any{
			"RegionName":    region,
			"ReplicaStatus": "ACTIVE",
		})
	}
	return map[string]any{
		"GlobalTableName":   gt.GlobalTableName,
		"GlobalTableArn":    gt.GlobalTableArn,
		"GlobalTableStatus": "ACTIVE",
		"CreationDateTime":  gt.CreationDateTime,
		"ReplicationGroup":  replicas,
	}
}

func handleDDBCreateGlobalTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalTableName  string `json:"GlobalTableName"`
		ReplicationGroup []struct {
			RegionName string `json:"RegionName"`
		} `json:"ReplicationGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, exists := ddbGlobalTables.Get(req.GlobalTableName); exists {
		sim.AWSErrorf(w, "GlobalTableAlreadyExistsException", http.StatusBadRequest,
			"Global table already exists: %s", req.GlobalTableName)
		return
	}
	// The same-named table must exist in the current region (real AWS requires
	// the source table before a legacy global table can be formed).
	if _, ok := ddbTables.Get(req.GlobalTableName); !ok {
		sim.AWSErrorf(w, "TableNotFoundException", http.StatusBadRequest,
			"Table not found: %s", req.GlobalTableName)
		return
	}
	regions := make([]string, 0, len(req.ReplicationGroup))
	for _, rg := range req.ReplicationGroup {
		regions = append(regions, rg.RegionName)
	}
	gt := DDBGlobalTable{
		GlobalTableName:  req.GlobalTableName,
		GlobalTableArn:   ddbGlobalTableArn(req.GlobalTableName),
		CreationDateTime: float64(time.Now().Unix()),
		Replicas:         regions,
		BillingMode:      "PROVISIONED",
	}
	ddbGlobalTables.Put(req.GlobalTableName, gt)
	writeDDBJSON(w, http.StatusOK, map[string]any{"GlobalTableDescription": ddbGlobalTableDescription(gt)})
}

func handleDDBDescribeGlobalTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalTableName string `json:"GlobalTableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	gt, ok := ddbGlobalTables.Get(req.GlobalTableName)
	if !ok {
		sim.AWSErrorf(w, "GlobalTableNotFoundException", http.StatusBadRequest,
			"Global table not found: %s", req.GlobalTableName)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"GlobalTableDescription": ddbGlobalTableDescription(gt)})
}

func handleDDBListGlobalTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExclusiveStartGlobalTableName string `json:"ExclusiveStartGlobalTableName"`
		RegionName                    string `json:"RegionName"`
		Limit                         int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbGlobalTables.List()
	filtered := all[:0:0]
	for _, gt := range all {
		if req.RegionName != "" {
			hasRegion := false
			for _, region := range gt.Replicas {
				if region == req.RegionName {
					hasRegion = true
					break
				}
			}
			if !hasRegion {
				continue
			}
		}
		filtered = append(filtered, gt)
	}
	sortBy(filtered, func(gt DDBGlobalTable) string { return gt.GlobalTableName })

	token := ""
	if req.ExclusiveStartGlobalTableName != "" {
		for i, gt := range filtered {
			if gt.GlobalTableName == req.ExclusiveStartGlobalTableName {
				token = strconv.Itoa(i + 1)
				break
			}
		}
	}
	page, next := awsPage(filtered, token, req.Limit, 100)
	out := make([]map[string]any, 0, len(page))
	for _, gt := range page {
		replicas := make([]map[string]any, 0, len(gt.Replicas))
		for _, region := range gt.Replicas {
			replicas = append(replicas, map[string]any{"RegionName": region})
		}
		out = append(out, map[string]any{
			"GlobalTableName":  gt.GlobalTableName,
			"ReplicationGroup": replicas,
		})
	}
	resp := map[string]any{"GlobalTables": out}
	if next != "" {
		idx, _ := strconv.Atoi(next)
		if idx > 0 && idx <= len(filtered) {
			resp["LastEvaluatedGlobalTableName"] = filtered[idx-1].GlobalTableName
		}
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

func handleDDBUpdateGlobalTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalTableName string `json:"GlobalTableName"`
		ReplicaUpdates  []struct {
			Create *struct {
				RegionName string `json:"RegionName"`
			} `json:"Create"`
			Delete *struct {
				RegionName string `json:"RegionName"`
			} `json:"Delete"`
		} `json:"ReplicaUpdates"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	gt, ok := ddbGlobalTables.Get(req.GlobalTableName)
	if !ok {
		sim.AWSErrorf(w, "GlobalTableNotFoundException", http.StatusBadRequest,
			"Global table not found: %s", req.GlobalTableName)
		return
	}
	for _, upd := range req.ReplicaUpdates {
		switch {
		case upd.Create != nil:
			exists := false
			for _, region := range gt.Replicas {
				if region == upd.Create.RegionName {
					exists = true
					break
				}
			}
			if exists {
				sim.AWSErrorf(w, "ReplicaAlreadyExistsException", http.StatusBadRequest,
					"Replica already exists: %s", upd.Create.RegionName)
				return
			}
			gt.Replicas = append(gt.Replicas, upd.Create.RegionName)
		case upd.Delete != nil:
			kept := gt.Replicas[:0:0]
			for _, region := range gt.Replicas {
				if region != upd.Delete.RegionName {
					kept = append(kept, region)
				}
			}
			gt.Replicas = kept
		}
	}
	ddbGlobalTables.Put(req.GlobalTableName, gt)
	writeDDBJSON(w, http.StatusOK, map[string]any{"GlobalTableDescription": ddbGlobalTableDescription(gt)})
}

func ddbReplicaSettings(gt DDBGlobalTable) []map[string]any {
	out := make([]map[string]any, 0, len(gt.Replicas))
	for _, region := range gt.Replicas {
		setting := map[string]any{
			"RegionName":    region,
			"ReplicaStatus": "ACTIVE",
			"ReplicaBillingModeSummary": map[string]any{
				"BillingMode": ddbGTBillingMode(gt),
			},
		}
		if gt.ProvisionedWriteUnits > 0 {
			setting["ReplicaProvisionedWriteCapacityUnits"] = gt.ProvisionedWriteUnits
		}
		readUnits := gt.ProvisionedReadUnits
		if gt.ProvisionedReadByRegion != nil {
			if v, ok := gt.ProvisionedReadByRegion[region]; ok {
				readUnits = v
			}
		}
		if readUnits > 0 {
			setting["ReplicaProvisionedReadCapacityUnits"] = readUnits
		}
		out = append(out, setting)
	}
	return out
}

func ddbGTBillingMode(gt DDBGlobalTable) string {
	if gt.BillingMode != "" {
		return gt.BillingMode
	}
	return "PROVISIONED"
}

func handleDDBDescribeGlobalTableSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalTableName string `json:"GlobalTableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	gt, ok := ddbGlobalTables.Get(req.GlobalTableName)
	if !ok {
		sim.AWSErrorf(w, "GlobalTableNotFoundException", http.StatusBadRequest,
			"Global table not found: %s", req.GlobalTableName)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"GlobalTableName": gt.GlobalTableName,
		"ReplicaSettings": ddbReplicaSettings(gt),
	})
}

func handleDDBUpdateGlobalTableSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GlobalTableName                          string `json:"GlobalTableName"`
		GlobalTableBillingMode                   string `json:"GlobalTableBillingMode"`
		GlobalTableProvisionedWriteCapacityUnits *int64 `json:"GlobalTableProvisionedWriteCapacityUnits"`
		ReplicaSettingsUpdate                    []struct {
			RegionName                          string `json:"RegionName"`
			ReplicaProvisionedReadCapacityUnits *int64 `json:"ReplicaProvisionedReadCapacityUnits"`
		} `json:"ReplicaSettingsUpdate"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ddbExtendedMu.Lock()
	defer ddbExtendedMu.Unlock()
	gt, ok := ddbGlobalTables.Get(req.GlobalTableName)
	if !ok {
		sim.AWSErrorf(w, "GlobalTableNotFoundException", http.StatusBadRequest,
			"Global table not found: %s", req.GlobalTableName)
		return
	}
	if req.GlobalTableBillingMode != "" {
		gt.BillingMode = req.GlobalTableBillingMode
	}
	if req.GlobalTableProvisionedWriteCapacityUnits != nil {
		gt.ProvisionedWriteUnits = *req.GlobalTableProvisionedWriteCapacityUnits
	}
	for _, ru := range req.ReplicaSettingsUpdate {
		if ru.ReplicaProvisionedReadCapacityUnits != nil {
			if gt.ProvisionedReadByRegion == nil {
				gt.ProvisionedReadByRegion = map[string]int64{}
			}
			gt.ProvisionedReadByRegion[ru.RegionName] = *ru.ReplicaProvisionedReadCapacityUnits
		}
	}
	ddbGlobalTables.Put(req.GlobalTableName, gt)
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"GlobalTableName": gt.GlobalTableName,
		"ReplicaSettings": ddbReplicaSettings(gt),
	})
}

// ddbTableAutoScalingDescription assembles the TableAutoScalingDescription a
// table reports for auto-scaling settings (one replica row for the current
// region).
func ddbTableAutoScalingDescription(t DDBTable) map[string]any {
	return map[string]any{
		"TableName":   t.TableName,
		"TableStatus": "ACTIVE",
		"Replicas": []map[string]any{
			{
				"RegionName":    awsRegion(),
				"ReplicaStatus": "ACTIVE",
			},
		},
	}
}

func handleDDBUpdateTableReplicaAutoScaling(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableAutoScalingDescription": ddbTableAutoScalingDescription(t),
	})
}

func handleDDBDescribeTableReplicaAutoScaling(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableAutoScalingDescription": ddbTableAutoScalingDescription(t),
	})
}

// --- Resource-based policy ---------------------------------------------------

func handleDDBPutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		Policy      string `json:"Policy"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ddbResourceExistsForPolicy(req.ResourceArn) {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: %s", req.ResourceArn)
		return
	}
	rev := generateUUID()
	ddbResourcePols.Put(req.ResourceArn, IAMResourcePolicy{ARN: req.ResourceArn, Policy: req.Policy})
	// Mirror into the central IAM resource-policy store so the enforcement gate
	// sees it, exactly as SQS/SNS do.
	iamPutResourcePolicy(req.ResourceArn, req.Policy)
	writeDDBJSON(w, http.StatusOK, map[string]any{"RevisionId": rev})
}

func handleDDBGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	rp, ok := ddbResourcePols.Get(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "PolicyNotFoundException", http.StatusBadRequest,
			"No resource policy found for resource: %s", req.ResourceArn)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"Policy":     rp.Policy,
		"RevisionId": generateUUID(),
	})
}

func handleDDBDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbResourcePols.Get(req.ResourceArn); !ok {
		sim.AWSErrorf(w, "PolicyNotFoundException", http.StatusBadRequest,
			"No resource policy found for resource: %s", req.ResourceArn)
		return
	}
	ddbResourcePols.Delete(req.ResourceArn)
	iamDeleteResourcePolicy(req.ResourceArn)
	writeDDBJSON(w, http.StatusOK, map[string]any{"RevisionId": generateUUID()})
}

// ddbResourceExistsForPolicy reports whether a table or stream ARN names a real
// resource a policy may attach to. Accepts table ARNs (including index/stream
// sub-ARNs) for an existing table.
func ddbResourceExistsForPolicy(arn string) bool {
	if _, _, ok := ddbTableByArn(arn); ok {
		return true
	}
	// table-stream sub-ARN: arn:aws:dynamodb:...:table/<name>/stream/<ts>
	const sep = ":table/"
	if idx := strings.Index(arn, sep); idx >= 0 {
		rest := arn[idx+len(sep):]
		name := rest
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			name = rest[:slash]
		}
		if _, ok := ddbTables.Get(name); ok {
			return true
		}
	}
	return false
}

// --- Kinesis streaming destinations ------------------------------------------

func ddbStreamDestKey(table, stream string) string {
	return table + "\x00" + stream
}

func handleDDBEnableKinesisStreaming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                           string `json:"TableName"`
		StreamArn                           string `json:"StreamArn"`
		EnableKinesisStreamingConfiguration *struct {
			ApproximateCreationDateTimePrecision string `json:"ApproximateCreationDateTimePrecision"`
		} `json:"EnableKinesisStreamingConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	precision := "MICROSECOND"
	if req.EnableKinesisStreamingConfiguration != nil && req.EnableKinesisStreamingConfiguration.ApproximateCreationDateTimePrecision != "" {
		precision = req.EnableKinesisStreamingConfiguration.ApproximateCreationDateTimePrecision
	}
	dest := DDBStreamDestination{
		TableName:                            req.TableName,
		StreamArn:                            ddbStreamArn(req.StreamArn),
		DestinationStatus:                    "ACTIVE",
		ApproximateCreationDateTimePrecision: precision,
		CreatedAt:                            float64(time.Now().Unix()),
	}
	ddbStreamDests.Put(ddbStreamDestKey(req.TableName, dest.StreamArn), dest)
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableName":         req.TableName,
		"StreamArn":         dest.StreamArn,
		"DestinationStatus": "ENABLING",
		"EnableKinesisStreamingConfiguration": map[string]any{
			"ApproximateCreationDateTimePrecision": precision,
		},
	})
}

func handleDDBDisableKinesisStreaming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
		StreamArn string `json:"StreamArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	key := ddbStreamDestKey(req.TableName, ddbStreamArn(req.StreamArn))
	if _, ok := ddbStreamDests.Get(key); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Kinesis streaming destination not found for table %s", req.TableName)
		return
	}
	ddbStreamDests.Delete(key)
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableName":         req.TableName,
		"StreamArn":         ddbStreamArn(req.StreamArn),
		"DestinationStatus": "DISABLING",
	})
}

func handleDDBDescribeKinesisStreaming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	dests := make([]map[string]any, 0)
	for _, d := range ddbStreamDests.List() {
		if d.TableName == req.TableName {
			dests = append(dests, map[string]any{
				"StreamArn":                            d.StreamArn,
				"DestinationStatus":                    d.DestinationStatus,
				"ApproximateCreationDateTimePrecision": d.ApproximateCreationDateTimePrecision,
			})
		}
	}
	sort.Slice(dests, func(i, j int) bool {
		return fmt.Sprint(dests[i]["StreamArn"]) < fmt.Sprint(dests[j]["StreamArn"])
	})
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableName":                     req.TableName,
		"KinesisDataStreamDestinations": dests,
	})
}

func handleDDBUpdateKinesisStreaming(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                           string `json:"TableName"`
		StreamArn                           string `json:"StreamArn"`
		UpdateKinesisStreamingConfiguration *struct {
			ApproximateCreationDateTimePrecision string `json:"ApproximateCreationDateTimePrecision"`
		} `json:"UpdateKinesisStreamingConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	key := ddbStreamDestKey(req.TableName, ddbStreamArn(req.StreamArn))
	dest, ok := ddbStreamDests.Get(key)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Kinesis streaming destination not found for table %s", req.TableName)
		return
	}
	precision := dest.ApproximateCreationDateTimePrecision
	if req.UpdateKinesisStreamingConfiguration != nil && req.UpdateKinesisStreamingConfiguration.ApproximateCreationDateTimePrecision != "" {
		precision = req.UpdateKinesisStreamingConfiguration.ApproximateCreationDateTimePrecision
		dest.ApproximateCreationDateTimePrecision = precision
		ddbStreamDests.Put(key, dest)
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TableName":         req.TableName,
		"StreamArn":         dest.StreamArn,
		"DestinationStatus": "UPDATING",
		"UpdateKinesisStreamingConfiguration": map[string]any{
			"ApproximateCreationDateTimePrecision": precision,
		},
	})
}

// --- Exports / imports -------------------------------------------------------

func ddbExportDescription(e DDBExport) map[string]any {
	d := map[string]any{
		"ExportArn":      e.ExportArn,
		"ExportStatus":   e.ExportStatus,
		"ExportFormat":   e.ExportFormat,
		"ExportType":     e.ExportType,
		"TableArn":       e.TableArn,
		"TableId":        e.TableId,
		"StartTime":      e.StartTime,
		"EndTime":        e.EndTime,
		"S3Bucket":       e.S3Bucket,
		"ItemCount":      e.ItemCount,
		"ExportManifest": fmt.Sprintf("%s/manifest-summary.json", e.S3Prefix),
	}
	if e.ExportTime > 0 {
		d["ExportTime"] = e.ExportTime
	}
	if e.S3Prefix != "" {
		d["S3Prefix"] = e.S3Prefix
	}
	if e.ClientToken != "" {
		d["ClientToken"] = e.ClientToken
	}
	return d
}

func handleDDBExportTableToPointInTime(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableArn     string  `json:"TableArn"`
		S3Bucket     string  `json:"S3Bucket"`
		S3Prefix     string  `json:"S3Prefix"`
		ExportFormat string  `json:"ExportFormat"`
		ExportType   string  `json:"ExportType"`
		ExportTime   float64 `json:"ExportTime"`
		ClientToken  string  `json:"ClientToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, t, ok := ddbTableByArn(req.TableArn)
	if !ok {
		sim.AWSErrorf(w, "TableNotFoundException", http.StatusBadRequest,
			"Table not found for ARN: %s", req.TableArn)
		return
	}
	format := req.ExportFormat
	if format == "" {
		format = "DYNAMODB_JSON"
	}
	etype := req.ExportType
	if etype == "" {
		etype = "FULL_EXPORT"
	}
	now := float64(time.Now().Unix())
	items := ddbTableItemsSnapshot(name)
	e := DDBExport{
		ExportArn:    ddbExportArn(t.TableArn, generateUUID()),
		TableArn:     t.TableArn,
		TableId:      t.TableId,
		ExportStatus: "COMPLETED",
		ExportFormat: format,
		ExportType:   etype,
		ExportTime:   req.ExportTime,
		StartTime:    now,
		EndTime:      now,
		S3Bucket:     req.S3Bucket,
		S3Prefix:     req.S3Prefix,
		ItemCount:    int64(len(items)),
		ClientToken:  req.ClientToken,
	}
	ddbExports.Put(e.ExportArn, e)
	writeDDBJSON(w, http.StatusOK, map[string]any{"ExportDescription": ddbExportDescription(e)})
}

func handleDDBDescribeExport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExportArn string `json:"ExportArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	e, ok := ddbExports.Get(req.ExportArn)
	if !ok {
		sim.AWSErrorf(w, "ExportNotFoundException", http.StatusBadRequest,
			"Export not found: %s", req.ExportArn)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"ExportDescription": ddbExportDescription(e)})
}

func handleDDBListExports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableArn   string `json:"TableArn"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbExports.List()
	filtered := all[:0:0]
	for _, e := range all {
		if req.TableArn == "" || e.TableArn == req.TableArn {
			filtered = append(filtered, e)
		}
	}
	sortBy(filtered, func(e DDBExport) string { return e.ExportArn })
	page, next := awsPageExplicit(filtered, req.NextToken, req.MaxResults)
	summaries := make([]map[string]any, 0, len(page))
	for _, e := range page {
		summaries = append(summaries, map[string]any{
			"ExportArn":    e.ExportArn,
			"ExportStatus": e.ExportStatus,
			"ExportType":   e.ExportType,
		})
	}
	out := map[string]any{"ExportSummaries": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	writeDDBJSON(w, http.StatusOK, out)
}

func ddbImportDescription(im DDBImport) map[string]any {
	keySchema := make([]map[string]any, 0, len(im.KeySchema))
	for _, k := range im.KeySchema {
		keySchema = append(keySchema, map[string]any{"AttributeName": k.AttributeName, "KeyType": k.KeyType})
	}
	attrDefs := make([]map[string]any, 0, len(im.AttributeDefs))
	for _, a := range im.AttributeDefs {
		attrDefs = append(attrDefs, map[string]any{"AttributeName": a.AttributeName, "AttributeType": a.AttributeType})
	}
	return map[string]any{
		"ImportArn":          im.ImportArn,
		"ImportStatus":       im.ImportStatus,
		"TableArn":           im.TableArn,
		"TableId":            im.TableId,
		"InputFormat":        im.InputFormat,
		"StartTime":          im.StartTime,
		"EndTime":            im.EndTime,
		"ImportedItemCount":  im.ImportedItemCount,
		"ProcessedItemCount": im.ProcessedItemCount,
		"ErrorCount":         0,
		"S3BucketSource": map[string]any{
			"S3Bucket":    im.S3Bucket,
			"S3KeyPrefix": im.S3KeyPrefix,
		},
		"TableCreationParameters": map[string]any{
			"TableName":            im.TableName,
			"KeySchema":            keySchema,
			"AttributeDefinitions": attrDefs,
			"BillingMode":          im.BillingMode,
		},
		"ClientToken": im.ClientToken,
	}
}

func handleDDBImportTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InputFormat    string `json:"InputFormat"`
		ClientToken    string `json:"ClientToken"`
		S3BucketSource *struct {
			S3Bucket    string `json:"S3Bucket"`
			S3KeyPrefix string `json:"S3KeyPrefix"`
		} `json:"S3BucketSource"`
		TableCreationParameters *struct {
			TableName            string              `json:"TableName"`
			KeySchema            []DDBKeySchemaEntry `json:"KeySchema"`
			AttributeDefinitions []DDBAttributeDef   `json:"AttributeDefinitions"`
			BillingMode          string              `json:"BillingMode"`
		} `json:"TableCreationParameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TableCreationParameters == nil || req.TableCreationParameters.TableName == "" {
		sim.AWSError(w, "ValidationException", "TableCreationParameters.TableName is required", http.StatusBadRequest)
		return
	}
	tcp := req.TableCreationParameters
	if _, exists := ddbTables.Get(tcp.TableName); exists {
		sim.AWSErrorf(w, "ImportConflictException", http.StatusBadRequest,
			"Table already exists: %s", tcp.TableName)
		return
	}
	billing := tcp.BillingMode
	if billing == "" {
		billing = "PAY_PER_REQUEST"
	}
	// ImportTable creates the destination table as part of the import. Model the
	// import as immediately COMPLETED (the sim's synchronous model) and the
	// table as ACTIVE so a subsequent DescribeTable/PutItem works.
	t := ddbRecreateTable(tcp.TableName, tcp.KeySchema, tcp.AttributeDefinitions, nil, nil, billing)
	ddbTables.Put(tcp.TableName, t)

	s3bucket, s3prefix := "", ""
	if req.S3BucketSource != nil {
		s3bucket = req.S3BucketSource.S3Bucket
		s3prefix = req.S3BucketSource.S3KeyPrefix
	}
	now := float64(time.Now().Unix())
	im := DDBImport{
		ImportArn:          ddbImportArn(t.TableArn, generateUUID()),
		ImportStatus:       "COMPLETED",
		TableArn:           t.TableArn,
		TableId:            t.TableId,
		TableName:          tcp.TableName,
		InputFormat:        req.InputFormat,
		StartTime:          now,
		EndTime:            now,
		ImportedItemCount:  0,
		ProcessedItemCount: 0,
		S3Bucket:           s3bucket,
		S3KeyPrefix:        s3prefix,
		ClientToken:        req.ClientToken,
		KeySchema:          tcp.KeySchema,
		AttributeDefs:      tcp.AttributeDefinitions,
		BillingMode:        billing,
	}
	ddbImports.Put(im.ImportArn, im)
	writeDDBJSON(w, http.StatusOK, map[string]any{"ImportTableDescription": ddbImportDescription(im)})
}

func handleDDBDescribeImport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ImportArn string `json:"ImportArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	im, ok := ddbImports.Get(req.ImportArn)
	if !ok {
		sim.AWSErrorf(w, "ImportNotFoundException", http.StatusBadRequest,
			"Import not found: %s", req.ImportArn)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"ImportTableDescription": ddbImportDescription(im)})
}

func handleDDBListImports(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableArn  string `json:"TableArn"`
		PageSize  int    `json:"PageSize"`
		NextToken string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbImports.List()
	filtered := all[:0:0]
	for _, im := range all {
		if req.TableArn == "" || im.TableArn == req.TableArn {
			filtered = append(filtered, im)
		}
	}
	sortBy(filtered, func(im DDBImport) string { return im.ImportArn })
	page, next := awsPageExplicit(filtered, req.NextToken, req.PageSize)
	summaries := make([]map[string]any, 0, len(page))
	for _, im := range page {
		summaries = append(summaries, map[string]any{
			"ImportArn":    im.ImportArn,
			"ImportStatus": im.ImportStatus,
			"TableArn":     im.TableArn,
			"InputFormat":  im.InputFormat,
			"StartTime":    im.StartTime,
			"EndTime":      im.EndTime,
			"S3BucketSource": map[string]any{
				"S3Bucket":    im.S3Bucket,
				"S3KeyPrefix": im.S3KeyPrefix,
			},
		})
	}
	out := map[string]any{"ImportSummaryList": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// --- Contributor insights ----------------------------------------------------

func ddbContribKey(table, index string) string {
	return table + "\x00" + index
}

func handleDDBUpdateContributorInsights(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                 string `json:"TableName"`
		IndexName                 string `json:"IndexName"`
		ContributorInsightsAction string `json:"ContributorInsightsAction"`
		ContributorInsightsMode   string `json:"ContributorInsightsMode"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	status := "DISABLED"
	if strings.EqualFold(req.ContributorInsightsAction, "ENABLE") {
		status = "ENABLED"
	}
	ci := DDBContributorInsight{
		TableName: req.TableName,
		IndexName: req.IndexName,
		Status:    status,
		Mode:      req.ContributorInsightsMode,
		UpdatedAt: float64(time.Now().Unix()),
	}
	ddbContribInsts.Put(ddbContribKey(req.TableName, req.IndexName), ci)
	resp := map[string]any{
		"TableName":                 req.TableName,
		"ContributorInsightsStatus": ddbContribTransitionStatus(req.ContributorInsightsAction),
	}
	if req.IndexName != "" {
		resp["IndexName"] = req.IndexName
	}
	if req.ContributorInsightsMode != "" {
		resp["ContributorInsightsMode"] = req.ContributorInsightsMode
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

func ddbContribTransitionStatus(action string) string {
	if strings.EqualFold(action, "ENABLE") {
		return "ENABLING"
	}
	return "DISABLING"
}

func handleDDBDescribeContributorInsights(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
		IndexName string `json:"IndexName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := ddbTables.Get(req.TableName); !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	ci, ok := ddbContribInsts.Get(ddbContribKey(req.TableName, req.IndexName))
	status := "DISABLED"
	updated := float64(0)
	mode := ""
	if ok {
		status = ci.Status
		updated = ci.UpdatedAt
		mode = ci.Mode
	}
	resp := map[string]any{
		"TableName":                   req.TableName,
		"ContributorInsightsStatus":   status,
		"ContributorInsightsRuleList": []string{},
	}
	if req.IndexName != "" {
		resp["IndexName"] = req.IndexName
	}
	if updated > 0 {
		resp["LastUpdateDateTime"] = updated
	}
	if mode != "" {
		resp["ContributorInsightsMode"] = mode
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

func handleDDBListContributorInsights(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName  string `json:"TableName"`
		MaxResults int    `json:"MaxResults"`
		NextToken  string `json:"NextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbContribInsts.List()
	filtered := all[:0:0]
	for _, ci := range all {
		if req.TableName == "" || ci.TableName == req.TableName {
			filtered = append(filtered, ci)
		}
	}
	sortBy(filtered, func(ci DDBContributorInsight) string { return ci.TableName + "\x00" + ci.IndexName })
	page, next := awsPageExplicit(filtered, req.NextToken, req.MaxResults)
	summaries := make([]map[string]any, 0, len(page))
	for _, ci := range page {
		s := map[string]any{
			"TableName":                 ci.TableName,
			"ContributorInsightsStatus": ci.Status,
		}
		if ci.IndexName != "" {
			s["IndexName"] = ci.IndexName
		}
		if ci.Mode != "" {
			s["ContributorInsightsMode"] = ci.Mode
		}
		summaries = append(summaries, s)
	}
	out := map[string]any{"ContributorInsightsSummaries": summaries}
	if next != "" {
		out["NextToken"] = next
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// --- Endpoints discovery -----------------------------------------------------

func handleDDBDescribeEndpoints(w http.ResponseWriter, r *http.Request) {
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"Endpoints": []map[string]any{
			{
				"Address":              fmt.Sprintf("dynamodb.%s.amazonaws.com", awsRegion()),
				"CachePeriodInMinutes": 1440,
			},
		},
	})
}
