package main

// Amazon DynamoDB speaks awsJson1_0. The SDK decodes nothing from a success
// response whose Content-Type is not `application/x-amz-json-1.0`, and
// terraform-provider-aws then reads the nil result as ResourceNotFound, so
// writeDDBJSON sets it. Errors go through AWSErrorf with
// `application/x-amz-json-1.1`, as AWS uses for every JSON-RPC service.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

type DDBTable struct {
	TableName                 string                    `json:"TableName"`
	TableId                   string                    `json:"TableId"`
	TableArn                  string                    `json:"TableArn"`
	TableStatus               string                    `json:"TableStatus"`
	CreationDateTime          float64                   `json:"CreationDateTime"`
	AttributeDefinitions      []DDBAttributeDef         `json:"AttributeDefinitions"`
	KeySchema                 []DDBKeySchemaEntry       `json:"KeySchema"`
	GlobalSecondaryIndexes    []DDBGlobalSecondaryIndex `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes     []DDBLocalSecondaryIndex  `json:"LocalSecondaryIndexes,omitempty"`
	BillingModeSummary        *DDBBillingModeSummary    `json:"BillingModeSummary,omitempty"`
	ProvisionedThroughput     *DDBProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	ItemCount                 int64                     `json:"ItemCount"`
	TableSizeBytes            int64                     `json:"TableSizeBytes"`
	DeletionProtectionEnabled bool                      `json:"DeletionProtectionEnabled"`
	TableClassSummary         *DDBTableClassSummary     `json:"TableClassSummary,omitempty"`
	WarmThroughput            *DDBWarmThroughput        `json:"WarmThroughput,omitempty"`
	SSEDescription            *DDBSSEDescription        `json:"SSEDescription,omitempty"`
	// VectorIndexes are the table's vector indexes, which DescribeTable
	// reports and SearchVectors searches.
	VectorIndexes []DDBVectorIndexDescription `json:"VectorIndexes,omitempty"`
}

// Keep state DynamoDB exposes outside DescribeTable (TTL, point-in-time
// recovery, tags) in its own durable record so it never leaks into the
// DescribeTable response.
type DDBTableSettings struct {
	PITRStatus       string  `json:"pitrStatus,omitempty"`
	TTLStatus        string  `json:"ttlStatus,omitempty"`
	TTLAttributeName string  `json:"ttlAttributeName,omitempty"`
	Tags             []SMTag `json:"tags,omitempty"`
}

type DDBSSEDescription struct {
	Status          string `json:"Status"`
	SSEType         string `json:"SSEType"`
	KMSMasterKeyArn string `json:"KMSMasterKeyArn,omitempty"`
}

// DynamoDB returns a zero-filled struct for PAY_PER_REQUEST tables;
// terraform's reader dereferences it.
type DDBProvisionedThroughput struct {
	NumberOfDecreasesToday int64   `json:"NumberOfDecreasesToday"`
	ReadCapacityUnits      int64   `json:"ReadCapacityUnits"`
	WriteCapacityUnits     int64   `json:"WriteCapacityUnits"`
	LastIncreaseDateTime   float64 `json:"LastIncreaseDateTime,omitempty"`
	LastDecreaseDateTime   float64 `json:"LastDecreaseDateTime,omitempty"`
}

// DynamoDB returns this on every Describe.
type DDBTableClassSummary struct {
	TableClass         string  `json:"TableClass"`
	LastUpdateDateTime float64 `json:"LastUpdateDateTime,omitempty"`
}

// DynamoDB returns WarmThroughput on every DescribeTable, ACTIVE for a fresh
// on-demand table. terraform-provider-aws v6 waits on it after table creation
// and fails when it is nil.
type DDBWarmThroughput struct {
	ReadUnitsPerSecond  int64  `json:"ReadUnitsPerSecond"`
	Status              string `json:"Status"`
	WriteUnitsPerSecond int64  `json:"WriteUnitsPerSecond"`
}

type DDBAttributeDef struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"` // S / N / B
}

type DDBKeySchemaEntry struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"` // HASH / RANGE
}

type DDBProjection struct {
	ProjectionType   string   `json:"ProjectionType"` // ALL / KEYS_ONLY / INCLUDE
	NonKeyAttributes []string `json:"NonKeyAttributes,omitempty"`
}

// Responses add IndexStatus, IndexArn, ItemCount and IndexSizeBytes;
// terraform-provider-aws waits for every GSI to reach ACTIVE.
type DDBGlobalSecondaryIndex struct {
	IndexName             string                    `json:"IndexName"`
	KeySchema             []DDBKeySchemaEntry       `json:"KeySchema"`
	Projection            *DDBProjection            `json:"Projection,omitempty"`
	IndexStatus           string                    `json:"IndexStatus,omitempty"`
	IndexArn              string                    `json:"IndexArn,omitempty"`
	ItemCount             int64                     `json:"ItemCount"`
	IndexSizeBytes        int64                     `json:"IndexSizeBytes"`
	ProvisionedThroughput *DDBProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	WarmThroughput        *DDBWarmThroughput        `json:"WarmThroughput,omitempty"`
}

// A local secondary index is created with the table and has no IndexStatus.
type DDBLocalSecondaryIndex struct {
	IndexName      string              `json:"IndexName"`
	KeySchema      []DDBKeySchemaEntry `json:"KeySchema"`
	Projection     *DDBProjection      `json:"Projection,omitempty"`
	IndexArn       string              `json:"IndexArn,omitempty"`
	ItemCount      int64               `json:"ItemCount"`
	IndexSizeBytes int64               `json:"IndexSizeBytes"`
}

// Both billing modes are accepted.
type DDBBillingModeSummary struct {
	BillingMode                       string `json:"BillingMode"`
	LastUpdateToPayPerRequestDateTime int64  `json:"LastUpdateToPayPerRequestDateTime,omitempty"`
}

var (
	ddbTables sim.Store[DDBTable]
	// ddbTableSettingsMu serializes mutations so concurrent tag, TTL and PITR
	// updates do not overwrite one another.
	ddbTableSettings   sim.Store[DDBTableSettings]
	ddbTableSettingsMu sync.Mutex
	// Keyed `<table>/<itemKey>`, itemKey encoding the primary-key values (see
	// dynamodb_keys.go).
	ddbItems sim.Store[map[string]any]
)

// The SDK requires exactly `application/x-amz-json-1.0`.
func writeDDBJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// With ReturnValuesOnConditionCheckFailure=ALL_OLD, carry the existing item,
// which optimistic-locking libraries read.
func writeDDBConditionalCheckFailed(w http.ResponseWriter, returnValues string, old map[string]any, exists bool) {
	body := map[string]any{
		"__type":  "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException",
		"Message": "The conditional request failed",
	}
	if strings.EqualFold(returnValues, "ALL_OLD") && exists && len(old) > 0 {
		body["Item"] = old
	}
	writeDDBJSON(w, http.StatusBadRequest, body)
}

func ddbTableArn(name string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awsRegion(), awsAccountID(), name)
}

func ddbIndexArn(table, index string) string {
	return fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s/index/%s", awsRegion(), awsAccountID(), table, index)
}

// Tagging operations address a table by ResourceArn.
func ddbTableByArn(arn string) (string, DDBTable, bool) {
	if arn == "" {
		return "", DDBTable{}, false
	}
	const sep = ":table/"
	idx := strings.Index(arn, sep)
	if idx < 0 {
		return "", DDBTable{}, false
	}
	name := arn[idx+len(sep):]
	t, ok := ddbTables.Get(name)
	return name, t, ok
}

func registerDynamoDB(r *AWSRouter, srv *sim.Server, startBackgroundEvaluators bool) {
	// Item-level operations are CloudTrail data events, excluded from
	// LookupEvents; the table-level operations are management events.
	cloudTrailDeclareDataEvents("dynamodb.amazonaws.com",
		"GetItem", "PutItem", "UpdateItem", "DeleteItem", "Query", "Scan",
		"BatchGetItem", "BatchWriteItem", "TransactWriteItems", "TransactGetItems",
		"ExecuteStatement", "ExecuteTransaction", "BatchExecuteStatement")
	ddbTables = sim.MakeStore[DDBTable](srv.DB(), "ddb_tables")
	ddbTableSettings = sim.MakeStore[DDBTableSettings](srv.DB(), "ddb_table_settings")
	// Index queries, scans, the usage refresh and the TTL sweep read every item of
	// a table, so serve items from memory and write through to SQLite: a SQLite
	// read per item held the table lock long enough to stall writes.
	ddbItems = sim.MakeCachedStore[map[string]any](srv.DB(), "ddb_items")
	ddbItemNames = sim.MakeCachedStore[string](srv.DB(), "ddb_item_names")
	ddbKeyFormat = sim.MakeStore[string](srv.DB(), "ddb_key_format")
	if err := ddbMigrateItemKeys(); err != nil {
		log.Fatalf("dynamodb: %v", err)
	}
	ddbIndexEntries = sim.NewStateStore[string]()
	for _, table := range ddbTables.List() {
		ddbRebuildTableIndexes(table)
	}
	ddbResetUsage()
	if startBackgroundEvaluators {
		startDDBTTLSweeper(srv)
	}

	reg := func(target string, h http.HandlerFunc) {
		op := strings.TrimPrefix(target, "DynamoDB_20120810.")
		r.Register(target, ddbRequire(ddbRequiredMembers[op], h))
	}
	reg("DynamoDB_20120810.CreateTable", handleDDBCreateTable)
	reg("DynamoDB_20120810.DescribeTable", handleDDBDescribeTable)
	reg("DynamoDB_20120810.UpdateTable", handleDDBUpdateTable)
	reg("DynamoDB_20120810.SearchVectors", handleDDBSearchVectors)
	reg("DynamoDB_20120810.DeleteTable", handleDDBDeleteTable)
	reg("DynamoDB_20120810.ListTables", handleDDBListTables)
	reg("DynamoDB_20120810.PutItem", handleDDBPutItem)
	reg("DynamoDB_20120810.GetItem", handleDDBGetItem)
	reg("DynamoDB_20120810.UpdateItem", handleDDBUpdateItem)
	reg("DynamoDB_20120810.DeleteItem", handleDDBDeleteItem)
	reg("DynamoDB_20120810.Query", handleDDBQuery)
	reg("DynamoDB_20120810.Scan", handleDDBScan)
	reg("DynamoDB_20120810.BatchWriteItem", handleDDBBatchWriteItem)
	reg("DynamoDB_20120810.BatchGetItem", handleDDBBatchGetItem)
	reg("DynamoDB_20120810.TransactWriteItems", handleDDBTransactWriteItems)
	reg("DynamoDB_20120810.TransactGetItems", handleDDBTransactGetItems)
	reg("DynamoDB_20120810.DescribeContinuousBackups", handleDDBDescribeContinuousBackups)
	reg("DynamoDB_20120810.UpdateContinuousBackups", handleDDBUpdateContinuousBackups)
	reg("DynamoDB_20120810.DescribeTimeToLive", handleDDBDescribeTimeToLive)
	reg("DynamoDB_20120810.UpdateTimeToLive", handleDDBUpdateTimeToLive)
	reg("DynamoDB_20120810.ListTagsOfResource", handleDDBListTagsOfResource)
	reg("DynamoDB_20120810.TagResource", handleDDBTagResource)
	reg("DynamoDB_20120810.UntagResource", handleDDBUntagResource)
	reg("DynamoDB_20120810.DescribeLimits", handleDDBDescribeLimits)
	registerDDBPartiQL(r)
	registerDynamoDBExtended(r, srv)
}

func handleDDBDescribeLimits(w http.ResponseWriter, r *http.Request) {
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"AccountMaxReadCapacityUnits":  80000,
		"AccountMaxWriteCapacityUnits": 80000,
		"TableMaxReadCapacityUnits":    40000,
		"TableMaxWriteCapacityUnits":   40000,
	})
}

// terraform-provider-aws polls DescribeContinuousBackups afterwards for
// convergence.
func handleDDBUpdateContinuousBackups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                        string `json:"TableName"`
		PointInTimeRecoverySpecification struct {
			PointInTimeRecoveryEnabled bool `json:"PointInTimeRecoveryEnabled"`
		} `json:"PointInTimeRecoverySpecification"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	status := "DISABLED"
	if req.PointInTimeRecoverySpecification.PointInTimeRecoveryEnabled {
		status = "ENABLED"
	}
	ddbTableSettingsMu.Lock()
	settings, _ := ddbTableSettings.Get(req.TableName)
	settings.PITRStatus = status
	ddbTableSettings.Put(req.TableName, settings)
	ddbTableSettingsMu.Unlock()
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"ContinuousBackupsDescription": map[string]any{
			"ContinuousBackupsStatus": "ENABLED",
			"PointInTimeRecoveryDescription": map[string]any{
				"PointInTimeRecoveryStatus": status,
			},
		},
	})
}

func handleDDBUpdateTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName               string `json:"TableName"`
		TimeToLiveSpecification struct {
			Enabled       bool   `json:"Enabled"`
			AttributeName string `json:"AttributeName"`
		} `json:"TimeToLiveSpecification"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	status := "DISABLED"
	if req.TimeToLiveSpecification.Enabled {
		status = "ENABLED"
	}
	ddbTableSettingsMu.Lock()
	settings, _ := ddbTableSettings.Get(req.TableName)
	settings.TTLStatus = status
	settings.TTLAttributeName = req.TimeToLiveSpecification.AttributeName
	ddbTableSettings.Put(req.TableName, settings)
	ddbTableSettingsMu.Unlock()
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TimeToLiveSpecification": map[string]any{
			"Enabled":       req.TimeToLiveSpecification.Enabled,
			"AttributeName": req.TimeToLiveSpecification.AttributeName,
		},
	})
}

// Re-tagging a key replaces its value.
func handleDDBTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string  `json:"ResourceArn"`
		Tags        []SMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: %s", req.ResourceArn)
		return
	}
	override := map[string]string{}
	for _, tag := range req.Tags {
		override[tag.Key] = tag.Value
	}
	ddbTableSettingsMu.Lock()
	settings, _ := ddbTableSettings.Get(name)
	merged := make([]SMTag, 0, len(settings.Tags)+len(req.Tags))
	for _, tag := range settings.Tags {
		if _, replaced := override[tag.Key]; !replaced {
			merged = append(merged, tag)
		}
	}
	merged = append(merged, req.Tags...)
	settings.Tags = merged
	ddbTableSettings.Put(name, settings)
	ddbTableSettingsMu.Unlock()
	writeDDBJSON(w, http.StatusOK, map[string]any{})
}

// Missing keys are ignored.
func handleDDBUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"ResourceArn"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: %s", req.ResourceArn)
		return
	}
	remove := map[string]bool{}
	for _, k := range req.TagKeys {
		remove[k] = true
	}
	ddbTableSettingsMu.Lock()
	settings, _ := ddbTableSettings.Get(name)
	filtered := make([]SMTag, 0, len(settings.Tags))
	for _, tag := range settings.Tags {
		if !remove[tag.Key] {
			filtered = append(filtered, tag)
		}
	}
	settings.Tags = filtered
	ddbTableSettings.Put(name, settings)
	ddbTableSettingsMu.Unlock()
	writeDDBJSON(w, http.StatusOK, map[string]any{})
}

// New tables default to DISABLED.
func handleDDBDescribeContinuousBackups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	settings, _ := ddbTableSettings.Get(req.TableName)
	pitr := settings.PITRStatus
	if pitr == "" {
		pitr = "DISABLED"
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"ContinuousBackupsDescription": map[string]any{
			"ContinuousBackupsStatus": "ENABLED",
			"PointInTimeRecoveryDescription": map[string]any{
				"PointInTimeRecoveryStatus": pitr,
			},
		},
	})
}

func handleDDBDescribeTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	settings, _ := ddbTableSettings.Get(req.TableName)
	status := settings.TTLStatus
	if status == "" {
		status = "DISABLED"
	}
	desc := map[string]any{"TimeToLiveStatus": status}
	if settings.TTLAttributeName != "" {
		desc["AttributeName"] = settings.TTLAttributeName
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"TimeToLiveDescription": desc,
	})
}

// DynamoDB keeps tags outside DescribeTable.
func handleDDBListTagsOfResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: %s", req.ResourceArn)
		return
	}
	settings, _ := ddbTableSettings.Get(name)
	tags := make([]map[string]any, 0, len(settings.Tags))
	for _, tag := range settings.Tags {
		tags = append(tags, map[string]any{"Key": tag.Key, "Value": tag.Value})
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"Tags": tags,
	})
}

func handleDDBCreateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName              string                    `json:"TableName"`
		AttributeDefinitions   []DDBAttributeDef         `json:"AttributeDefinitions"`
		KeySchema              []DDBKeySchemaEntry       `json:"KeySchema"`
		BillingMode            string                    `json:"BillingMode"`
		ProvisionedThroughput  *DDBProvisionedThroughput `json:"ProvisionedThroughput"`
		GlobalSecondaryIndexes []DDBGlobalSecondaryIndex `json:"GlobalSecondaryIndexes"`
		LocalSecondaryIndexes  []DDBLocalSecondaryIndex  `json:"LocalSecondaryIndexes"`
		VectorIndexes          []map[string]any          `json:"VectorIndexes"`
		SSESpecification       *struct {
			Enabled        bool   `json:"Enabled"`
			SSEType        string `json:"SSEType"`
			KMSMasterKeyId string `json:"KMSMasterKeyId"`
		} `json:"SSESpecification"`
		Tags []SMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TableName == "" {
		AWSError(w, "ValidationException", "TableName is required", http.StatusBadRequest)
		return
	}
	if _, exists := ddbTables.Get(req.TableName); exists {
		AWSErrorf(w, "ResourceInUseException", http.StatusBadRequest,
			"Table already exists: %s", req.TableName)
		return
	}
	if len(req.KeySchema) == 0 {
		AWSError(w, "ValidationException", "KeySchema is required", http.StatusBadRequest)
		return
	}
	// Every key attribute of the table and its indexes must be declared in
	// AttributeDefinitions.
	defined := map[string]bool{}
	for _, ad := range req.AttributeDefinitions {
		defined[ad.AttributeName] = true
	}
	keyAttrs := append([]DDBKeySchemaEntry(nil), req.KeySchema...)
	for _, g := range req.GlobalSecondaryIndexes {
		keyAttrs = append(keyAttrs, g.KeySchema...)
	}
	for _, l := range req.LocalSecondaryIndexes {
		keyAttrs = append(keyAttrs, l.KeySchema...)
	}
	for _, k := range keyAttrs {
		if !defined[k.AttributeName] {
			AWSErrorf(w, "ValidationException", http.StatusBadRequest,
				"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [%s]", k.AttributeName)
			return
		}
	}
	billingMode := req.BillingMode
	if billingMode == "" {
		billingMode = "PROVISIONED"
	}
	// A PROVISIONED table (the default) states both units; an on-demand table
	// states neither.
	hasUnits := req.ProvisionedThroughput != nil &&
		(req.ProvisionedThroughput.ReadCapacityUnits > 0 || req.ProvisionedThroughput.WriteCapacityUnits > 0)
	switch {
	case strings.EqualFold(billingMode, "PROVISIONED") &&
		(req.ProvisionedThroughput == nil || req.ProvisionedThroughput.ReadCapacityUnits < 1 || req.ProvisionedThroughput.WriteCapacityUnits < 1):
		AWSError(w, "ValidationException",
			"One or more parameter values were invalid: ReadCapacityUnits and WriteCapacityUnits must both be specified when BillingMode is PROVISIONED",
			http.StatusBadRequest)
		return
	case strings.EqualFold(billingMode, "PAY_PER_REQUEST") && hasUnits:
		AWSError(w, "ValidationException",
			"One or more parameter values were invalid: Neither ReadCapacityUnits nor WriteCapacityUnits can be specified when BillingMode is PAY_PER_REQUEST",
			http.StatusBadRequest)
		return
	}
	if strings.EqualFold(billingMode, "PROVISIONED") {
		for _, g := range req.GlobalSecondaryIndexes {
			if g.ProvisionedThroughput == nil || g.ProvisionedThroughput.ReadCapacityUnits < 1 || g.ProvisionedThroughput.WriteCapacityUnits < 1 {
				AWSErrorf(w, "ValidationException", http.StatusBadRequest,
					"One or more parameter values were invalid: ProvisionedThroughput must be specified for index: %s", g.IndexName)
				return
			}
		}
	}
	now := float64(time.Now().Unix())

	// Index builds complete at once; terraform-provider-aws waits for every GSI's
	// IndexStatus to reach ACTIVE.
	gsis := make([]DDBGlobalSecondaryIndex, 0, len(req.GlobalSecondaryIndexes))
	for _, g := range req.GlobalSecondaryIndexes {
		gsis = append(gsis, ddbActivateGSI(req.TableName, g))
	}
	lsis := make([]DDBLocalSecondaryIndex, 0, len(req.LocalSecondaryIndexes))
	for _, l := range req.LocalSecondaryIndexes {
		l.IndexArn = ddbIndexArn(req.TableName, l.IndexName)
		lsis = append(lsis, l)
	}

	table := DDBTable{
		TableName:            req.TableName,
		TableId:              sim.NewUUID(),
		TableArn:             ddbTableArn(req.TableName),
		TableStatus:          "ACTIVE",
		CreationDateTime:     now,
		AttributeDefinitions: req.AttributeDefinitions,
		KeySchema:            req.KeySchema,
		BillingModeSummary: &DDBBillingModeSummary{
			BillingMode: billingMode,
		},
		// DynamoDB returns a zero-filled ProvisionedThroughput for PAY_PER_REQUEST
		// tables.
		ProvisionedThroughput: ddbTableThroughput(billingMode, req.ProvisionedThroughput),
		TableClassSummary: &DDBTableClassSummary{
			TableClass: "STANDARD",
		},
		// terraform-provider-aws v6's waitTableWarmThroughputActive needs this
		// non-nil.
		WarmThroughput: &DDBWarmThroughput{
			ReadUnitsPerSecond:  12000,
			WriteUnitsPerSecond: 4000,
			Status:              "ACTIVE",
		},
	}
	if len(gsis) > 0 {
		table.GlobalSecondaryIndexes = gsis
	}
	if len(lsis) > 0 {
		table.LocalSecondaryIndexes = lsis
	}
	// SSEType defaults to KMS when omitted.
	if req.SSESpecification != nil && req.SSESpecification.Enabled {
		sseType := req.SSESpecification.SSEType
		if sseType == "" {
			sseType = "KMS"
		}
		table.SSEDescription = &DDBSSEDescription{
			Status:          "ENABLED",
			SSEType:         sseType,
			KMSMasterKeyArn: req.SSESpecification.KMSMasterKeyId,
		}
	}
	// Validate as UpdateTable validates an index it creates.
	for _, raw := range req.VectorIndexes {
		idx, err := ddbParseVectorIndex(req.TableName, raw)
		if err != nil {
			AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
			return
		}
		table.VectorIndexes = append(table.VectorIndexes, idx)
	}
	ddbTables.Put(req.TableName, table)
	// Store create-time tags; dropping them makes every plan re-add them.
	ddbTableSettings.Put(req.TableName, DDBTableSettings{Tags: req.Tags})
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": table})
}

func handleDDBDescribeTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"Table": ddbDescribedUsage(t, time.Now())})
}

// The simulator builds indexes instantly.
func ddbActivateGSI(tableName string, g DDBGlobalSecondaryIndex) DDBGlobalSecondaryIndex {
	g.IndexStatus = "ACTIVE"
	g.IndexArn = ddbIndexArn(tableName, g.IndexName)
	if g.ProvisionedThroughput == nil {
		g.ProvisionedThroughput = &DDBProvisionedThroughput{}
	}
	g.WarmThroughput = &DDBWarmThroughput{
		ReadUnitsPerSecond:  12000,
		WriteUnitsPerSecond: 4000,
		Status:              "ACTIVE",
	}
	return g
}

func ddbMergeAttributeDefs(existing, incoming []DDBAttributeDef) []DDBAttributeDef {
	seen := map[string]bool{}
	out := make([]DDBAttributeDef, 0, len(existing)+len(incoming))
	for _, a := range existing {
		if !seen[a.AttributeName] {
			seen[a.AttributeName] = true
			out = append(out, a)
		}
	}
	for _, a := range incoming {
		if !seen[a.AttributeName] {
			seen[a.AttributeName] = true
			out = append(out, a)
		}
	}
	return out
}

// terraform-provider-aws manages GSIs after creation through
// GlobalSecondaryIndexUpdates and polls until each is ACTIVE.
func handleDDBUpdateTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                 string                    `json:"TableName"`
		AttributeDefinitions      []DDBAttributeDef         `json:"AttributeDefinitions"`
		BillingMode               string                    `json:"BillingMode"`
		DeletionProtectionEnabled *bool                     `json:"DeletionProtectionEnabled"`
		ProvisionedThroughput     *DDBProvisionedThroughput `json:"ProvisionedThroughput"`

		VectorIndexUpdates []map[string]any `json:"VectorIndexUpdates"`

		GlobalSecondaryIndexUpdates []struct {
			Create *DDBGlobalSecondaryIndex `json:"Create"`
			Update *struct {
				IndexName             string                    `json:"IndexName"`
				ProvisionedThroughput *DDBProvisionedThroughput `json:"ProvisionedThroughput"`
			} `json:"Update"`
			Delete *struct {
				IndexName string `json:"IndexName"`
			} `json:"Delete"`
		} `json:"GlobalSecondaryIndexUpdates"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if len(req.VectorIndexUpdates) > 0 {
		if err := ddbApplyVectorIndexUpdates(&t, req.VectorIndexUpdates); err != nil {
			AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
			return
		}
	}
	if len(req.AttributeDefinitions) > 0 {
		t.AttributeDefinitions = ddbMergeAttributeDefs(t.AttributeDefinitions, req.AttributeDefinitions)
	}
	if req.BillingMode != "" {
		if t.BillingModeSummary == nil {
			t.BillingModeSummary = &DDBBillingModeSummary{}
		}
		t.BillingModeSummary.BillingMode = req.BillingMode
	}
	if req.ProvisionedThroughput != nil {
		t.ProvisionedThroughput = req.ProvisionedThroughput
	}
	defer ddbForgetBuckets(req.TableName)
	defer ddbForgetUsage(req.TableName)
	if req.DeletionProtectionEnabled != nil {
		t.DeletionProtectionEnabled = *req.DeletionProtectionEnabled
	}
	for _, upd := range req.GlobalSecondaryIndexUpdates {
		switch {
		case upd.Create != nil:
			t.GlobalSecondaryIndexes = append(t.GlobalSecondaryIndexes, ddbActivateGSI(req.TableName, *upd.Create))
		case upd.Delete != nil:
			kept := t.GlobalSecondaryIndexes[:0:0]
			for _, g := range t.GlobalSecondaryIndexes {
				if g.IndexName != upd.Delete.IndexName {
					kept = append(kept, g)
				}
			}
			t.GlobalSecondaryIndexes = kept
		case upd.Update != nil && upd.Update.ProvisionedThroughput != nil:
			for i := range t.GlobalSecondaryIndexes {
				if t.GlobalSecondaryIndexes[i].IndexName == upd.Update.IndexName {
					t.GlobalSecondaryIndexes[i].ProvisionedThroughput = upd.Update.ProvisionedThroughput
				}
			}
		}
	}
	ddbTables.Put(req.TableName, t)
	if len(req.GlobalSecondaryIndexUpdates) > 0 {
		func() {
			defer ddbItemLocks.Lock(true, req.TableName)()
			ddbRebuildTableIndexes(t)
		}()
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

func handleDDBDeleteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	ddbTables.Delete(req.TableName)
	ddbForgetBuckets(req.TableName)
	ddbForgetUsage(req.TableName)
	ddbTableSettings.Delete(req.TableName)
	// DeleteTable deletes the items too. The trailing "/" keeps "foo" from
	// matching "foobar".
	for _, k := range ddbTableSortedKeys(req.TableName + "/") {
		ddbItems.Delete(k)
		ddbItemNames.Delete(k)
	}
	ddbDropTableIndexes(req.TableName)
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

func handleDDBListTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExclusiveStartTableName string `json:"ExclusiveStartTableName"`
		Limit                   int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbTables.List()
	sortBy(all, func(t DDBTable) string { return t.TableName })

	page, lastEvaluated := ddbKeyPage(all, func(t DDBTable) string { return t.TableName }, req.ExclusiveStartTableName, req.Limit, 100)
	names := make([]string, 0, len(page))
	for _, t := range page {
		names = append(names, t.TableName)
	}
	out := map[string]any{"TableNames": names}
	if lastEvaluated != "" {
		out["LastEvaluatedTableName"] = lastEvaluated
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// Resume at the first key greater than exclusiveStart whether or not it still
// exists; lastEvaluated names the page's last key when more remain. A limit
// out of (0, max] takes max.
func ddbKeyPage[T any](sorted []T, key func(T) string, exclusiveStart string, limit, max int) (page []T, lastEvaluated string) {
	start := 0
	if exclusiveStart != "" {
		start = sort.Search(len(sorted), func(i int) bool { return key(sorted[i]) > exclusiveStart })
	}
	rest := sorted[start:]
	if limit <= 0 || limit > max {
		limit = max
	}
	if len(rest) <= limit {
		return rest, ""
	}
	page = rest[:limit]
	return page, key(page[len(page)-1])
}

// Measure binary after base64 decoding, numbers by the documented
// significant-digit formula, and documents with their per-container and
// per-element overhead.
func ddbAttrValueSize(v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 1
	}
	if s, ok := m["S"].(string); ok {
		return len(s)
	}
	if n, ok := m["N"].(string); ok {
		return ddbNumberSize(n)
	}
	if b, ok := m["B"].(string); ok {
		return ddbBinarySize(b)
	}
	if mm, ok := m["M"].(map[string]any); ok {
		sz := 3
		for k, sub := range mm {
			sz += len(k) + ddbAttrValueSize(sub) + 1
		}
		return sz
	}
	if ll, ok := m["L"].([]any); ok {
		sz := 3
		for _, sub := range ll {
			sz += ddbAttrValueSize(sub) + 1
		}
		return sz
	}
	if set, ok := m["SS"].([]any); ok {
		sz := 0
		for _, e := range set {
			if s, ok := e.(string); ok {
				sz += len(s)
			}
		}
		return sz
	}
	if set, ok := m["NS"].([]any); ok {
		sz := 0
		for _, e := range set {
			if s, ok := e.(string); ok {
				sz += ddbNumberSize(s)
			}
		}
		return sz
	}
	if set, ok := m["BS"].([]any); ok {
		sz := 0
		for _, e := range set {
			if s, ok := e.(string); ok {
				sz += ddbBinarySize(s)
			}
		}
		return sz
	}
	return 1
}

func ddbBinarySize(encoded string) int {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0
	}
	return len(decoded)
}

func ddbNumberSize(number string) int {
	significand := strings.TrimSpace(number)
	significand = strings.TrimPrefix(significand, "+")
	significand = strings.TrimPrefix(significand, "-")
	if exponent := strings.IndexAny(significand, "eE"); exponent >= 0 {
		significand = significand[:exponent]
	}
	significand = strings.ReplaceAll(significand, ".", "")
	significand = strings.TrimLeft(significand, "0")
	significand = strings.TrimRight(significand, "0")
	digits := len(significand)
	if digits == 0 {
		digits = 1
	}
	size := (digits+1)/2 + 1
	if size > 21 {
		return 21
	}
	return size
}

func ddbItemSizeBytes(item map[string]any) int {
	sz := 0
	for name, v := range item {
		sz += len(name) + ddbAttrValueSize(v)
	}
	return sz
}

const ddbMaxItemSizeBytes = 400 * 1024

func ddbValidateItemSize(item map[string]any) error {
	if size := ddbItemSizeBytes(item); size > ddbMaxItemSizeBytes {
		return fmt.Errorf("item size has exceeded the maximum allowed size")
	}
	return nil
}

// Reads use 4 KB blocks, halved for eventually consistent; writes 1 KB blocks.
func ddbReadUnits(item map[string]any, strong bool) float64 {
	blocks := (ddbItemSizeBytes(item) + 4095) / 4096
	if blocks < 1 {
		blocks = 1
	}
	if strong {
		return float64(blocks)
	}
	return float64(blocks) / 2
}

func ddbWriteUnits(item map[string]any) float64 {
	blocks := (ddbItemSizeBytes(item) + 1023) / 1024
	if blocks < 1 {
		blocks = 1
	}
	return float64(blocks)
}

// nil when ReturnConsumedCapacity is unset or NONE.
func ddbConsumedCapacity(returnLevel, tableName string, units float64) map[string]any {
	if returnLevel == "" || strings.EqualFold(returnLevel, "NONE") {
		return nil
	}
	return map[string]any{
		"TableName":     tableName,
		"CapacityUnits": units,
		"Table":         map[string]any{"CapacityUnits": units},
	}
}

// Equal numbers share a store key; an invalid number returns the trimmed
// input.
func ddbCanonicalNumber(s string) string {
	if r, ok := new(big.Rat).SetString(strings.TrimSpace(s)); ok {
		return r.RatString()
	}
	return strings.TrimSpace(s)
}

// A top-level attribute is level 1, so its children may descend 31 more.
const ddbMaxItemDepth = 32

// The walk stops at the limit, so a pathological item cannot overflow the
// stack.
func ddbItemTooDeep(item map[string]any) bool {
	for _, av := range item {
		if ddbAttrExceedsDepth(av, ddbMaxItemDepth-1) {
			return true
		}
	}
	return false
}

func ddbAttrExceedsDepth(v any, remaining int) bool {
	if remaining < 0 {
		return true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if mm, ok := m["M"].(map[string]any); ok {
		for _, sub := range mm {
			if ddbAttrExceedsDepth(sub, remaining-1) {
				return true
			}
		}
	}
	if ll, ok := m["L"].([]any); ok {
		for _, sub := range ll {
			if ddbAttrExceedsDepth(sub, remaining-1) {
				return true
			}
		}
	}
	return false
}

func handleDDBPutItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                           string                      `json:"TableName"`
		Item                                map[string]any              `json:"Item"`
		ConditionExpression                 string                      `json:"ConditionExpression"`
		ExpressionAttributeNames            map[string]string           `json:"ExpressionAttributeNames,omitempty"`
		ExpressionAttributeValues           map[string]any              `json:"ExpressionAttributeValues,omitempty"`
		ReturnValues                        string                      `json:"ReturnValues,omitempty"`
		ReturnValuesOnConditionCheckFailure string                      `json:"ReturnValuesOnConditionCheckFailure,omitempty"`
		Expected                            map[string]ddbExpectedEntry `json:"Expected,omitempty"`
		ConditionalOperator                 string                      `json:"ConditionalOperator,omitempty"`
		ReturnConsumedCapacity              string                      `json:"ReturnConsumedCapacity,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if message := ddbItemKeyError(t, req.Item); message != "" {
		AWSError(w, "ValidationException", message, http.StatusBadRequest)
		return
	}
	if ddbItemTooDeep(req.Item) {
		AWSError(w, "ValidationException",
			"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
		return
	}
	if err := ddbValidateItemSize(req.Item); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	if index, ok := ddbTakeWrite(t, req.Item, ddbWriteUnits(req.Item)); !ok {
		ddbWriteThrottled(w, index)
		return
	}
	defer ddbItemLocks.Lock(true, req.TableName)()
	itemKey := ddbItemKey(t, req.Item)
	old, exists := ddbItems.Get(itemKey)

	// Evaluate the condition atomically with the write, e.g. terraform's
	// "attribute_not_exists(LockID)".
	if condOK, err := ddbEvalCondition(old, exists, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, old, exists)
		return
	}
	if okExp, err := ddbCheckExpected(old, exists, req.Expected, req.ConditionalOperator); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, old, exists)
		return
	}
	ddbPutItem(t, itemKey, req.Item)
	resp := map[string]any{}
	if req.ReturnValues == "ALL_OLD" && exists {
		resp["Attributes"] = old
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(req.Item)); cc != nil {
		resp["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

// An empty projection returns the item unchanged.
func ddbProjectItem(item map[string]any, projection string, exprNames map[string]string) map[string]any {
	if projection == "" || item == nil {
		return item
	}
	out := map[string]any{}
	for _, raw := range strings.Split(projection, ",") {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		// Graft the value at its path so `a.b` returns only the sub-attribute and
		// sibling paths merge under their prefix.
		if v, ok := ddbResolvePath(item, path, exprNames); ok {
			_ = ddbSetByPath(out, path, exprNames, v)
		}
	}
	return out
}

func handleDDBGetItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                string            `json:"TableName"`
		Key                      map[string]any    `json:"Key"`
		ProjectionExpression     string            `json:"ProjectionExpression"`
		ExpressionAttributeNames map[string]string `json:"ExpressionAttributeNames"`
		ConsistentRead           bool              `json:"ConsistentRead"`
		ReturnConsumedCapacity   string            `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if message := ddbKeyParamError(t, req.Key); message != "" {
		AWSError(w, "ValidationException", message, http.StatusBadRequest)
		return
	}
	itemKey := ddbItemKey(t, req.Key)
	item, found := ddbItemSnapshot(itemKey)
	if !ddbTake(t, "", ddbReadUnits(item, req.ConsistentRead), false) {
		ddbWriteThrottled(w, "")
		return
	}
	out := map[string]any{}
	if found {
		out["Item"] = ddbProjectItem(item, req.ProjectionExpression, req.ExpressionAttributeNames)
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbReadUnits(item, req.ConsistentRead)); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

func handleDDBUpdateItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string         `json:"TableName"`
		Key       map[string]any `json:"Key"`
		// The legacy AttributeUpdates parameter is accepted too.
		UpdateExpression          string            `json:"UpdateExpression"`
		ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues map[string]any    `json:"ExpressionAttributeValues"`
		AttributeUpdates          map[string]struct {
			Action string         `json:"Action"`
			Value  map[string]any `json:"Value"`
		} `json:"AttributeUpdates"`
		ConditionExpression                 string                      `json:"ConditionExpression"`
		ReturnValues                        string                      `json:"ReturnValues"`
		ReturnValuesOnConditionCheckFailure string                      `json:"ReturnValuesOnConditionCheckFailure"`
		Expected                            map[string]ddbExpectedEntry `json:"Expected,omitempty"`
		ConditionalOperator                 string                      `json:"ConditionalOperator,omitempty"`
		ReturnConsumedCapacity              string                      `json:"ReturnConsumedCapacity,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if message := ddbKeyParamError(t, req.Key); message != "" {
		AWSError(w, "ValidationException", message, http.StatusBadRequest)
		return
	}
	defer ddbItemLocks.Lock(true, req.TableName)()
	itemKey := ddbItemKey(t, req.Key)
	item, existed := ddbItems.Get(itemKey)
	if condOK, err := ddbEvalCondition(item, existed, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, item, existed)
		return
	}
	if okExp, err := ddbCheckExpected(item, existed, req.Expected, req.ConditionalOperator); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, item, existed)
		return
	}
	oldItem := ddbCloneItem(item)
	// Update a copy and publish it: a published item never changes, so readers
	// may copy it outside the lock.
	item = ddbCloneItem(item)
	if item == nil {
		item = map[string]any{}
		for k, v := range req.Key {
			item[k] = v
		}
	}
	if req.UpdateExpression != "" {
		if err := ddbApplyUpdateExpression(item, req.UpdateExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
			AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
			return
		}
	}
	for attr, upd := range req.AttributeUpdates {
		switch upd.Action {
		case "DELETE":
			// No value removes the attribute; a set value removes those elements.
			if upd.Value != nil {
				if cur, ok := item[attr]; ok {
					item[attr] = ddbDeleteSetElems(cur, upd.Value)
				}
			} else {
				delete(item, attr)
			}
		case "ADD":
			item[attr] = ddbAddValues(item[attr], upd.Value)
		default:
			item[attr] = upd.Value
		}
	}
	if err := ddbValidateItemSize(item); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	if message := ddbIndexKeyError(t, item); message != "" {
		AWSError(w, "ValidationException", message, http.StatusBadRequest)
		return
	}
	if index, ok := ddbTakeWrite(t, item, math.Max(ddbWriteUnits(oldItem), ddbWriteUnits(item))); !ok {
		ddbWriteThrottled(w, index)
		return
	}
	ddbPutItem(t, itemKey, item)

	resp := map[string]any{}
	switch strings.ToUpper(req.ReturnValues) {
	case "ALL_NEW":
		resp["Attributes"] = item
	case "ALL_OLD":
		if len(oldItem) > 0 {
			resp["Attributes"] = oldItem
		}
	case "UPDATED_NEW":
		changed := map[string]any{}
		for attr, newVal := range item {
			if oldVal, ok := oldItem[attr]; !ok || !ddbAttrEqual(oldVal, newVal) {
				changed[attr] = newVal
			}
		}
		if len(changed) > 0 {
			resp["Attributes"] = changed
		}
	case "UPDATED_OLD":
		changed := map[string]any{}
		for attr, newVal := range item {
			oldVal, ok := oldItem[attr]
			if !ok {
				continue
			}
			if !ddbAttrEqual(oldVal, newVal) {
				changed[attr] = oldVal
			}
		}
		if len(changed) > 0 {
			resp["Attributes"] = changed
		}
	default:
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(item)); cc != nil {
		resp["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

// Returns nil for a nil or empty item.
func ddbCloneItem(item map[string]any) map[string]any {
	if len(item) == 0 {
		return nil
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil
	}
	var clone map[string]any
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil
	}
	return clone
}

func ddbItemSnapshot(itemKey string) (map[string]any, bool) {
	release := ddbLockItemKeys(false, itemKey)
	item, ok := ddbItems.Get(itemKey)
	release()
	if !ok {
		return nil, false
	}
	// Copy outside the lock: a published item never changes in place, and the
	// copy is the expensive half of a read.
	return ddbCloneItem(item), true
}

// A Scan copies items out of a table stripe this many at a time: one
// acquisition per item made concurrent scans contend item by item, and
// snapshotting every key would copy items a Limit never reaches.
const ddbSnapshotBatch = 256

// One acquisition per table; keys without an item are absent.
func ddbItemSnapshots(itemKeys []string) map[string]map[string]any {
	found := make(map[string]map[string]any, len(itemKeys))
	release := ddbLockItemKeys(false, itemKeys...)
	for _, itemKey := range itemKeys {
		if item, ok := ddbItems.Get(itemKey); ok {
			found[itemKey] = item
		}
	}
	release()
	// Copy outside the lock; a published item never changes.
	out := make(map[string]map[string]any, len(found))
	for itemKey, item := range found {
		out[itemKey] = ddbCloneItem(item)
	}
	return out
}

// A caller that stops early has copied only the batch it stopped in.
type ddbBatchedSnapshots struct {
	keys   []string
	batch  map[string]map[string]any
	loaded int
}

func newDDBBatchedSnapshots(keys []string) *ddbBatchedSnapshots {
	return &ddbBatchedSnapshots{keys: keys}
}

func (s *ddbBatchedSnapshots) at(i int, itemKey string) (map[string]any, bool) {
	if s.batch == nil || i >= s.loaded {
		end := i + ddbSnapshotBatch
		if end > len(s.keys) {
			end = len(s.keys)
		}
		s.batch = ddbItemSnapshots(s.keys[i:end])
		s.loaded = end
	}
	item, ok := s.batch[itemKey]
	return item, ok
}

func ddbAttrEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

func handleDDBDeleteItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                           string                      `json:"TableName"`
		Key                                 map[string]any              `json:"Key"`
		ConditionExpression                 string                      `json:"ConditionExpression"`
		ExpressionAttributeNames            map[string]string           `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues           map[string]any              `json:"ExpressionAttributeValues"`
		ReturnValues                        string                      `json:"ReturnValues"`
		ReturnValuesOnConditionCheckFailure string                      `json:"ReturnValuesOnConditionCheckFailure"`
		Expected                            map[string]ddbExpectedEntry `json:"Expected,omitempty"`
		ConditionalOperator                 string                      `json:"ConditionalOperator,omitempty"`
		ReturnConsumedCapacity              string                      `json:"ReturnConsumedCapacity,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if message := ddbKeyParamError(t, req.Key); message != "" {
		AWSError(w, "ValidationException", message, http.StatusBadRequest)
		return
	}
	defer ddbItemLocks.Lock(true, req.TableName)()
	itemKey := ddbItemKey(t, req.Key)
	oldItem, existed := ddbItems.Get(itemKey)
	if index, ok := ddbTakeWrite(t, oldItem, ddbWriteUnits(oldItem)); !ok {
		ddbWriteThrottled(w, index)
		return
	}
	if condOK, err := ddbEvalCondition(oldItem, existed, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, oldItem, existed)
		return
	}
	if okExp, err := ddbCheckExpected(oldItem, existed, req.Expected, req.ConditionalOperator); err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, oldItem, existed)
		return
	}
	ddbDeleteItem(t, itemKey)
	out := map[string]any{}
	if strings.EqualFold(req.ReturnValues, "ALL_OLD") && existed {
		out["Attributes"] = oldItem
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(oldItem)); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// handleDDBQuery returns the items matching the KeyConditionExpression.
func handleDDBQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                 string            `json:"TableName"`
		IndexName                 string            `json:"IndexName"`
		KeyConditionExpression    string            `json:"KeyConditionExpression"`
		FilterExpression          string            `json:"FilterExpression"`
		ProjectionExpression      string            `json:"ProjectionExpression"`
		ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues map[string]any    `json:"ExpressionAttributeValues"`
		Limit                     int               `json:"Limit"`
		ExclusiveStartKey         map[string]any    `json:"ExclusiveStartKey"`
		ScanIndexForward          *bool             `json:"ScanIndexForward"`
		Select                    string            `json:"Select"`
		ConsistentRead            bool              `json:"ConsistentRead"`
		ReturnConsumedCapacity    string            `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	// The matcher is generic over item attributes, so an index query only needs
	// the index to exist.
	if req.IndexName != "" && !ddbHasIndex(t, req.IndexName) {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"The table does not have the specified index: %s", req.IndexName)
		return
	}
	if req.ConsistentRead && ddbIsGSI(t, req.IndexName) {
		AWSError(w, "ValidationException",
			"Consistent reads are not supported on global secondary indexes", http.StatusBadRequest)
		return
	}
	// A malformed expression or an undefined #name / :value is a
	// ValidationException even when no item is examined.
	keyExpr, err := ddbCompileExpr("KeyConditionExpression", req.KeyConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	filterExpr, err := ddbCompileExpr("FilterExpression", req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}

	prefix := req.TableName + "/"
	// ScanIndexForward=false walks the sort key descending; resume, Limit and
	// LastEvaluatedKey follow that direction. A Query reads one partition as a
	// contiguous key range; when the key condition does not fix the table's
	// partition, the whole table is the candidate set.
	forward := req.ScanIndexForward == nil || *req.ScanIndexForward
	index, onIndex := ddbIndexByName(t, req.IndexName)
	onGSI := onIndex && ddbIsGSI(t, req.IndexName)
	var remaining []string
	if onIndex {
		// An index query reads its partition in index sort-key order and resumes by
		// the index's key.
		remaining = ddbIndexCandidateKeys(t, index, keyExpr, req.ExclusiveStartKey, forward)
	} else {
		var candidates []string
		if partition, ok := ddbQueryPartitionPrefix(t, keyExpr); ok {
			candidates = ddbKeysInPartition(ddbTableSortedKeys(partition), partition)
		} else {
			candidates = ddbTableSortedKeys(prefix)
		}
		remaining = ddbQueryCandidateKeys(candidates, t, req.ExclusiveStartKey, prefix, forward)
	}

	// Limit caps the items examined (ScannedCount); FilterExpression applies to
	// those, so Count <= ScannedCount.
	var items []map[string]any
	scanned := 0
	var lastScanned map[string]any
	exhausted := true
	// Hold one critical section for the whole query and clone only the matches:
	// a lock and a clone per candidate made concurrent queries contend item by
	// item. The evaluator never writes to what it reads.
	var matched []map[string]any
	func() {
		defer ddbItemLocks.Lock(false, req.TableName)()
		var lastScannedStored map[string]any
		for _, k := range remaining {
			stored, ok2 := ddbItems.Get(k)
			if !ok2 {
				continue
			}
			if !keyExpr.match(stored, true) {
				continue
			}
			scanned++
			lastScannedStored = stored
			// A global secondary index holds only what it projects; a local one reads the
			// table.
			view := stored
			if onGSI {
				view = ddbProjectToIndex(t, index, stored)
			}
			if filterExpr.match(view, true) {
				matched = append(matched, stored)
			}
			// Reaching Limit ends the page with a LastEvaluatedKey even when nothing is
			// left; the next page is then empty.
			if req.Limit > 0 && scanned >= req.Limit {
				exhausted = false
				break
			}
		}
		// The resume cursor leaves the critical section as a copy too.
		if lastScannedStored != nil {
			lastScanned = ddbCloneItem(lastScannedStored)
		}
	}()
	// An index query returns what the index projects, unless a local index is
	// asked for table attributes by Select or ProjectionExpression.
	projectToIndex := onGSI || (onIndex && !strings.EqualFold(req.Select, "ALL_ATTRIBUTES") && req.ProjectionExpression == "")
	for _, stored := range matched {
		item := ddbCloneItem(stored)
		if projectToIndex {
			item = ddbProjectToIndex(t, index, item)
		}
		items = append(items, item)
	}
	if items == nil {
		items = []map[string]any{}
	}

	out := map[string]any{"Count": len(items), "ScannedCount": scanned}
	// Stopping on Limit emits the last key-matched item's key as the cursor.
	if !exhausted && lastScanned != nil {
		if onIndex {
			out["LastEvaluatedKey"] = ddbIndexLastEvaluatedKey(t, index, lastScanned)
		} else {
			out["LastEvaluatedKey"] = ddbExtractKey(t, lastScanned)
		}
	}
	// Select=COUNT omits Items.
	if !strings.EqualFold(req.Select, "COUNT") {
		out["Items"] = ddbProjectItems(items, req.ProjectionExpression, req.ExpressionAttributeNames)
	}
	queryUnits := 0.0
	for _, it := range items {
		queryUnits += ddbReadUnits(it, req.ConsistentRead)
	}
	if !ddbTake(t, ddbReadIndex(t, req.IndexName), queryUnits, false) {
		ddbWriteThrottled(w, ddbReadIndex(t, req.IndexName))
		return
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, queryUnits); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// keys must be sorted ascending.
func ddbQueryCandidateKeys(keys []string, t DDBTable, exclusiveStart map[string]any, prefix string, forward bool) []string {
	startKey := ddbItemKey(t, exclusiveStart)
	if forward {
		return keys[ddbResumeIndex(keys, startKey, prefix, t.TableName+"/"):]
	}
	end := len(keys)
	if startKey != prefix && startKey != t.TableName+"/" {
		end = sort.Search(len(keys), func(i int) bool { return keys[i] >= startKey })
	}
	out := make([]string, end)
	for i := 0; i < end; i++ {
		out[i] = keys[end-1-i]
	}
	return out
}

func ddbProjectItems(items []map[string]any, projection string, exprNames map[string]string) []map[string]any {
	if projection == "" {
		return items
	}
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i] = ddbProjectItem(it, projection, exprNames)
	}
	return out
}

// Global secondary indexes, unlike local ones and the table, reject
// consistent reads.
func ddbIsGSI(t DDBTable, name string) bool {
	if name == "" {
		return false
	}
	for _, g := range t.GlobalSecondaryIndexes {
		if g.IndexName == name {
			return true
		}
	}
	return false
}

func ddbHasIndex(t DDBTable, name string) bool {
	for _, g := range t.GlobalSecondaryIndexes {
		if g.IndexName == name {
			return true
		}
	}
	for _, l := range t.LocalSecondaryIndexes {
		if l.IndexName == name {
			return true
		}
	}
	return false
}

func handleDDBScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                 string            `json:"TableName"`
		IndexName                 string            `json:"IndexName"`
		FilterExpression          string            `json:"FilterExpression"`
		ProjectionExpression      string            `json:"ProjectionExpression"`
		ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues map[string]any    `json:"ExpressionAttributeValues"`
		Limit                     int               `json:"Limit"`
		ExclusiveStartKey         map[string]any    `json:"ExclusiveStartKey"`
		Select                    string            `json:"Select"`
		Segment                   *int              `json:"Segment"`
		TotalSegments             *int              `json:"TotalSegments"`
		ConsistentRead            bool              `json:"ConsistentRead"`
		ReturnConsumedCapacity    string            `json:"ReturnConsumedCapacity"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if req.IndexName != "" && !ddbHasIndex(t, req.IndexName) {
		AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"The table does not have the specified index: %s", req.IndexName)
		return
	}
	if req.ConsistentRead && ddbIsGSI(t, req.IndexName) {
		AWSError(w, "ValidationException",
			"Consistent reads are not supported on global secondary indexes", http.StatusBadRequest)
		return
	}
	// Segment and TotalSegments come together, with 0 <= Segment < TotalSegments.
	if (req.Segment != nil) != (req.TotalSegments != nil) {
		AWSError(w, "ValidationException",
			"The Segment and TotalSegments parameters must be provided together", http.StatusBadRequest)
		return
	}
	if req.TotalSegments != nil {
		seg := 0
		if req.Segment != nil {
			seg = *req.Segment
		}
		if *req.TotalSegments < 1 || seg < 0 || seg >= *req.TotalSegments {
			AWSError(w, "ValidationException",
				"The Segment parameter is zero-based and must be less than the TotalSegments parameter", http.StatusBadRequest)
			return
		}
	}
	// A malformed FilterExpression is a ValidationException.
	filterExpr, err := ddbCompileExpr("FilterExpression", req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	prefix := req.TableName + "/"
	// An index scan sees only the items the index holds.
	index, onIndex := ddbIndexByName(t, req.IndexName)
	onGSI := onIndex && ddbIsGSI(t, req.IndexName)
	var remaining []string
	if onIndex {
		remaining = ddbIndexCandidateKeys(t, index, nil, req.ExclusiveStartKey, true)
	} else {
		keys := ddbTableSortedKeys(prefix)
		remaining = keys[ddbResumeIndex(keys, ddbItemKey(t, req.ExclusiveStartKey), prefix, prefix):]
	}
	// Each segment owns the partitions that hash to it, so segments stay disjoint
	// and complete however the table changes between pages.
	if req.TotalSegments != nil {
		segment := 0
		if req.Segment != nil {
			segment = *req.Segment
		}
		part := remaining[:0:0]
		for _, k := range remaining {
			if ddbScanSegment(k, *req.TotalSegments) == segment {
				part = append(part, k)
			}
		}
		remaining = part
	}

	// Limit caps the items examined, so a filtered Scan can return fewer than
	// Limit and still carry a LastEvaluatedKey.
	var items []map[string]any
	scanned := 0
	var lastScanned map[string]any
	exhausted := true
	snapshots := newDDBBatchedSnapshots(remaining)
	for i, k := range remaining {
		it, ok2 := snapshots.at(i, k)
		if !ok2 {
			continue
		}
		scanned++
		lastScanned = it
		if onGSI {
			it = ddbProjectToIndex(t, index, it)
		}
		if filterExpr.match(it, true) {
			if onIndex && !onGSI && !strings.EqualFold(req.Select, "ALL_ATTRIBUTES") && req.ProjectionExpression == "" {
				it = ddbProjectToIndex(t, index, it)
			}
			items = append(items, it)
		}
		if req.Limit > 0 && scanned >= req.Limit {
			exhausted = false
			break
		}
	}
	if items == nil {
		items = []map[string]any{}
	}

	out := map[string]any{"Count": len(items), "ScannedCount": scanned}
	if !exhausted && lastScanned != nil {
		if onIndex {
			out["LastEvaluatedKey"] = ddbIndexLastEvaluatedKey(t, index, lastScanned)
		} else {
			out["LastEvaluatedKey"] = ddbExtractKey(t, lastScanned)
		}
	}
	if !strings.EqualFold(req.Select, "COUNT") {
		out["Items"] = ddbProjectItems(items, req.ProjectionExpression, req.ExpressionAttributeNames)
	}
	scanUnits := 0.0
	for _, it := range items {
		scanUnits += ddbReadUnits(it, req.ConsistentRead)
	}
	if !ddbTake(t, ddbReadIndex(t, req.IndexName), scanUnits, false) {
		ddbWriteThrottled(w, ddbReadIndex(t, req.IndexName))
		return
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, scanUnits); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// exists says whether the item is present. An empty expression holds; a
// malformed one or an undefined reference is an error.
func ddbEvalCondition(item map[string]any, exists bool, expr string, names map[string]string, values map[string]any) (bool, error) {
	c, err := ddbCompileExpr("ConditionExpression", expr, names, values)
	if err != nil {
		return false, err
	}
	return c.match(item, exists), nil
}

type ddbExpectedEntry struct {
	Value              map[string]any   `json:"Value"`
	Exists             *bool            `json:"Exists"`
	ComparisonOperator string           `json:"ComparisonOperator"`
	AttributeValueList []map[string]any `json:"AttributeValueList"`
}

// Translate the legacy Expected map into a ConditionExpression so it evaluates
// through the same engine.
func ddbExpectedToCondition(expected map[string]ddbExpectedEntry, condOp string) (string, map[string]string, map[string]any, error) {
	names := map[string]string{}
	values := map[string]any{}
	var clauses []string
	i := 0
	for attr, spec := range expected {
		n := fmt.Sprintf("#e%d", i)
		names[n] = attr
		addVal := func(v map[string]any) string {
			p := fmt.Sprintf(":e%d_%d", i, len(values))
			values[p] = v
			return p
		}
		op := strings.ToUpper(spec.ComparisonOperator)
		// Legacy shorthand: Exists or Value without an operator.
		if op == "" {
			switch {
			case spec.Exists != nil && !*spec.Exists:
				clauses = append(clauses, "attribute_not_exists("+n+")")
			case spec.Value != nil:
				clauses = append(clauses, n+" = "+addVal(spec.Value))
			case spec.Exists != nil && *spec.Exists:
				clauses = append(clauses, "attribute_exists("+n+")")
			default:
				return "", nil, nil, fmt.Errorf("invalid Expected entry for %q", attr)
			}
			i++
			continue
		}
		args := spec.AttributeValueList
		if len(args) == 0 && spec.Value != nil {
			args = []map[string]any{spec.Value}
		}
		var clause string
		switch op {
		case "EQ":
			clause = n + " = " + addVal(args[0])
		case "NE":
			clause = n + " <> " + addVal(args[0])
		case "LE":
			clause = n + " <= " + addVal(args[0])
		case "LT":
			clause = n + " < " + addVal(args[0])
		case "GE":
			clause = n + " >= " + addVal(args[0])
		case "GT":
			clause = n + " > " + addVal(args[0])
		case "NOT_NULL":
			clause = "attribute_exists(" + n + ")"
		case "NULL":
			clause = "attribute_not_exists(" + n + ")"
		case "CONTAINS":
			clause = "contains(" + n + ", " + addVal(args[0]) + ")"
		case "NOT_CONTAINS":
			clause = "NOT contains(" + n + ", " + addVal(args[0]) + ")"
		case "BEGINS_WITH":
			clause = "begins_with(" + n + ", " + addVal(args[0]) + ")"
		case "BETWEEN":
			if len(args) < 2 {
				return "", nil, nil, fmt.Errorf("BETWEEN needs 2 values for %q", attr)
			}
			clause = n + " BETWEEN " + addVal(args[0]) + " AND " + addVal(args[1])
		case "IN":
			ps := make([]string, len(args))
			for j, a := range args {
				ps[j] = addVal(a)
			}
			clause = n + " IN (" + strings.Join(ps, ", ") + ")"
		default:
			return "", nil, nil, fmt.Errorf("unsupported ComparisonOperator %q", spec.ComparisonOperator)
		}
		if (op != "NOT_NULL" && op != "NULL") && len(args) == 0 {
			return "", nil, nil, fmt.Errorf("ComparisonOperator %q needs a value for %q", op, attr)
		}
		clauses = append(clauses, clause)
		i++
	}
	joiner := " AND "
	if strings.EqualFold(condOp, "OR") {
		joiner = " OR "
	}
	return strings.Join(clauses, joiner), names, values, nil
}

// false only when a non-empty Expected condition fails.
func ddbCheckExpected(item map[string]any, exists bool, expected map[string]ddbExpectedEntry, condOp string) (bool, error) {
	if len(expected) == 0 {
		return true, nil
	}
	expr, names, values, err := ddbExpectedToCondition(expected, condOp)
	if err != nil {
		return false, err
	}
	return ddbEvalExpr(item, exists, expr, names, values)
}

func ddbScalarString(v any) string {
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"S", "N", "B"} {
			if s, ok := m[k]; ok {
				return fmt.Sprint(s)
			}
		}
	}
	return fmt.Sprint(v)
}

func ddbCompare(a, b any, op string) bool {
	if op == "=" {
		return ddbAttrValuesEqual(a, b)
	}
	if op == "<>" {
		return !ddbAttrValuesEqual(a, b)
	}
	// Numeric when both sides are N, lexicographic otherwise.
	as, bs := ddbScalarString(a), ddbScalarString(b)
	// Compare exactly: DynamoDB numbers carry 38 digits, beyond float64.
	if ar, aok := new(big.Rat).SetString(as); aok {
		if br, bok := new(big.Rat).SetString(bs); bok {
			c := ar.Cmp(br)
			switch op {
			case "<":
				return c < 0
			case "<=":
				return c <= 0
			case ">":
				return c > 0
			case ">=":
				return c >= 0
			}
		}
	}
	switch op {
	case "<":
		return as < bs
	case "<=":
		return as <= bs
	case ">":
		return as > bs
	case ">=":
		return as >= bs
	}
	return false
}

func handleDDBBatchWriteItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestItems map[string][]struct {
			PutRequest *struct {
				Item map[string]any `json:"Item"`
			} `json:"PutRequest"`
			DeleteRequest *struct {
				Key map[string]any `json:"Key"`
			} `json:"DeleteRequest"`
		} `json:"RequestItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Take every table's stripe in one ordered call so concurrent batches cannot
	// deadlock.
	batchTables := make([]string, 0, len(req.RequestItems))
	for table := range req.RequestItems {
		batchTables = append(batchTables, table)
	}
	defer ddbItemLocks.Lock(true, batchTables...)()
	// Validate the whole batch before applying: 1..25 requests, existing tables,
	// put items within depth.
	total := 0
	for tableName, ops := range req.RequestItems {
		total += len(ops)
		batchTable, ok := ddbTables.Get(tableName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", tableName)
			return
		}
		for _, op := range ops {
			var keyProblem string
			switch {
			case op.PutRequest != nil:
				keyProblem = ddbItemKeyError(batchTable, op.PutRequest.Item)
			case op.DeleteRequest != nil:
				keyProblem = ddbKeyParamError(batchTable, op.DeleteRequest.Key)
			}
			if keyProblem != "" {
				AWSError(w, "ValidationException", keyProblem, http.StatusBadRequest)
				return
			}
			if op.PutRequest != nil && ddbItemTooDeep(op.PutRequest.Item) {
				AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if op.PutRequest != nil {
				if err := ddbValidateItemSize(op.PutRequest.Item); err != nil {
					AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
		}
	}
	if total == 0 || total > 25 {
		AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'requestItems' failed to satisfy constraint: Member must have length less than or equal to 25 and at least 1",
			http.StatusBadRequest)
		return
	}
	// A throttled entry comes back in UnprocessedItems; only a batch that could
	// process nothing is refused.
	unprocessed := map[string]any{}
	processed := 0
	for tableName, ops := range req.RequestItems {
		t, _ := ddbTables.Get(tableName)
		var left []any
		for _, op := range ops {
			switch {
			case op.PutRequest != nil:
				if _, ok := ddbTakeWrite(t, op.PutRequest.Item, ddbWriteUnits(op.PutRequest.Item)); !ok {
					left = append(left, map[string]any{"PutRequest": map[string]any{"Item": op.PutRequest.Item}})
					continue
				}
				key := ddbItemKey(t, op.PutRequest.Item)
				ddbPutItem(t, key, op.PutRequest.Item)
				processed++
			case op.DeleteRequest != nil:
				key := ddbItemKey(t, op.DeleteRequest.Key)
				old, _ := ddbItems.Get(key)
				if _, ok := ddbTakeWrite(t, old, ddbWriteUnits(old)); !ok {
					left = append(left, map[string]any{"DeleteRequest": map[string]any{"Key": op.DeleteRequest.Key}})
					continue
				}
				ddbDeleteItem(t, key)
				processed++
			}
		}
		if len(left) > 0 {
			unprocessed[tableName] = left
		}
	}
	if processed == 0 && len(unprocessed) > 0 {
		ddbWriteThrottled(w, "")
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"UnprocessedItems": unprocessed})
}

func handleDDBBatchGetItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestItems map[string]struct {
			Keys []map[string]any `json:"Keys"`
		} `json:"RequestItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Resolve every key first so the batch takes one acquisition per table, not
	// one per key.
	wanted := map[string][]string{}
	var allKeys []string
	for tableName, spec := range req.RequestItems {
		t, ok := ddbTables.Get(tableName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", tableName)
			return
		}
		keys := make([]string, 0, len(spec.Keys))
		for _, key := range spec.Keys {
			if message := ddbKeyParamError(t, key); message != "" {
				AWSError(w, "ValidationException", message, http.StatusBadRequest)
				return
			}
			keys = append(keys, ddbItemKey(t, key))
		}
		wanted[tableName] = keys
		allKeys = append(allKeys, keys...)
	}
	found := ddbItemSnapshots(allKeys)
	responses := map[string][]map[string]any{}
	unprocessed := map[string]any{}
	for tableName, keys := range wanted {
		items := []map[string]any{}
		units := 0.0
		for _, itemKey := range keys {
			if it, ok := found[itemKey]; ok {
				items = append(items, it)
				units += ddbReadUnits(it, false)
			} else {
				units += ddbReadUnits(nil, false)
			}
		}
		t, _ := ddbTables.Get(tableName)
		if !ddbTake(t, "", units, false) {
			unprocessed[tableName] = map[string]any{"Keys": req.RequestItems[tableName].Keys}
			continue
		}
		responses[tableName] = items
	}
	if len(responses) == 0 && len(unprocessed) > 0 {
		ddbWriteThrottled(w, "")
		return
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"Responses":       responses,
		"UnprocessedKeys": unprocessed,
	})
}

// Evaluate every condition first under the item lock; any failure aborts the
// whole transaction with TransactionCanceledException.
func handleDDBTransactWriteItems(w http.ResponseWriter, r *http.Request) {
	// Exactly one of Put, Update, Delete and ConditionCheck is set per item.
	type txWrite struct {
		TableName                 string            `json:"TableName"`
		Item                      map[string]any    `json:"Item"`
		Key                       map[string]any    `json:"Key"`
		UpdateExpression          string            `json:"UpdateExpression"`
		ConditionExpression       string            `json:"ConditionExpression"`
		ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames"`
		ExpressionAttributeValues map[string]any    `json:"ExpressionAttributeValues"`
	}
	var req struct {
		TransactItems []struct {
			Put            *txWrite `json:"Put"`
			Update         *txWrite `json:"Update"`
			Delete         *txWrite `json:"Delete"`
			ConditionCheck *txWrite `json:"ConditionCheck"`
		} `json:"TransactItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// A transaction holds 1..100 actions.
	if n := len(req.TransactItems); n == 0 || n > 100 {
		AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to 100 and at least 1",
			http.StatusBadRequest)
		return
	}
	// Take the stripes in one ordered call: the transaction is atomic against its
	// tables, and transactions naming them in different orders cannot deadlock.
	transactTables := make([]string, 0, len(req.TransactItems))
	for _, ti := range req.TransactItems {
		for _, op := range []*txWrite{ti.Put, ti.Update, ti.Delete, ti.ConditionCheck} {
			if op != nil {
				transactTables = append(transactTables, op.TableName)
			}
		}
	}
	defer ddbItemLocks.Lock(true, transactTables...)()
	// A transaction spends twice a single write's capacity per item, checked
	// before anything is written.
	for _, ti := range req.TransactItems {
		for _, op := range []*txWrite{ti.Put, ti.Update, ti.Delete, ti.ConditionCheck} {
			if op == nil {
				continue
			}
			t, ok := ddbTables.Get(op.TableName)
			if !ok {
				continue // Reported below, where the service reports it.
			}
			item := op.Item
			if item == nil {
				item, _ = ddbItems.Get(ddbItemKey(t, op.Key))
			}
			if index, ok := ddbTakeWrite(t, item, 2*ddbWriteUnits(item)); !ok {
				ddbWriteThrottled(w, index)
				return
			}
		}
	}

	// Evaluate every item's condition so CancellationReasons carries one Code per
	// item in order, "None" for items that did not cancel.
	reasons := make([]map[string]any, len(req.TransactItems))
	cancelled := false
	for i, ti := range req.TransactItems {
		var op *txWrite
		opCount := 0
		for _, o := range []*txWrite{ti.Put, ti.Update, ti.Delete, ti.ConditionCheck} {
			if o != nil {
				opCount++
				op = o
			}
		}
		if opCount != 1 {
			AWSError(w, "ValidationException",
				"TransactItems can only contain one of Put, Update, Delete, or ConditionCheck", http.StatusBadRequest)
			return
		}
		keyItem := op.Key
		if ti.Put != nil {
			keyItem = op.Item
		}
		t, ok := ddbTables.Get(op.TableName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", op.TableName)
			return
		}
		keyProblem := ddbKeyParamError(t, keyItem)
		if ti.Put != nil {
			keyProblem = ddbItemKeyError(t, keyItem)
		}
		if keyProblem != "" {
			AWSError(w, "ValidationException", keyProblem, http.StatusBadRequest)
			return
		}
		current, exists := ddbItems.Get(ddbItemKey(t, keyItem))
		condOK, err := ddbEvalCondition(current, exists, op.ConditionExpression, op.ExpressionAttributeNames, op.ExpressionAttributeValues)
		if err != nil {
			AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
			return
		}
		if !condOK {
			reasons[i] = map[string]any{"Code": "ConditionalCheckFailed", "Message": "The conditional request failed"}
			cancelled = true
		} else {
			reasons[i] = map[string]any{"Code": "None"}
		}
		if ti.Put != nil {
			if ddbItemTooDeep(op.Item) {
				AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if err := ddbValidateItemSize(op.Item); err != nil {
				AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
				return
			}
		}
		if ti.Update != nil {
			updated := ddbCloneItem(current)
			if updated == nil {
				updated = map[string]any{}
				for key, value := range op.Key {
					updated[key] = value
				}
			}
			if op.UpdateExpression != "" {
				if err := ddbApplyUpdateExpression(updated, op.UpdateExpression, op.ExpressionAttributeNames, op.ExpressionAttributeValues); err != nil {
					AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
			if ddbItemTooDeep(updated) {
				AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if err := ddbValidateItemSize(updated); err != nil {
				AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	if cancelled {
		writeDDBTransactionCancelled(w, reasons)
		return
	}

	// Prepare every write before applying any: an update that cannot apply, or
	// leaves an index key of the wrong type, refuses the whole transaction.
	// ConditionCheck is read-only.
	type plannedWrite struct {
		table  DDBTable
		key    string
		item   map[string]any
		delete bool
	}
	var planned []plannedWrite
	for _, ti := range req.TransactItems {
		switch {
		case ti.Put != nil:
			t, _ := ddbTables.Get(ti.Put.TableName)
			planned = append(planned, plannedWrite{table: t, key: ddbItemKey(t, ti.Put.Item), item: ti.Put.Item})
		case ti.Update != nil:
			t, _ := ddbTables.Get(ti.Update.TableName)
			key := ddbItemKey(t, ti.Update.Key)
			stored, _ := ddbItems.Get(key)
			// Copy before mutating, as UpdateItem does.
			item := ddbCloneItem(stored)
			if item == nil {
				item = map[string]any{}
				for k, v := range ti.Update.Key {
					item[k] = v
				}
			}
			if ti.Update.UpdateExpression != "" {
				if err := ddbApplyUpdateExpression(item, ti.Update.UpdateExpression, ti.Update.ExpressionAttributeNames, ti.Update.ExpressionAttributeValues); err != nil {
					AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
			if message := ddbIndexKeyError(t, item); message != "" {
				AWSError(w, "ValidationException", message, http.StatusBadRequest)
				return
			}
			planned = append(planned, plannedWrite{table: t, key: key, item: item})
		case ti.Delete != nil:
			t, _ := ddbTables.Get(ti.Delete.TableName)
			planned = append(planned, plannedWrite{table: t, key: ddbItemKey(t, ti.Delete.Key), delete: true})
		}
	}
	for _, write := range planned {
		if write.delete {
			ddbDeleteItem(write.table, write.key)
		} else {
			ddbPutItem(write.table, write.key, write.item)
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{})
}

// The SDK maps the per-item CancellationReasons to conditional failures; the
// __type carries the service prefix.
func writeDDBTransactionCancelled(w http.ResponseWriter, reasons []map[string]any) {
	codes := make([]string, len(reasons))
	for i, rsn := range reasons {
		codes[i], _ = rsn["Code"].(string)
	}
	writeDDBJSON(w, http.StatusBadRequest, map[string]any{
		"__type":              "com.amazonaws.dynamodb.v20120810#TransactionCanceledException",
		"Message":             fmt.Sprintf("Transaction cancelled, please refer cancellation reasons for specific reasons [%s]", strings.Join(codes, ", ")),
		"CancellationReasons": reasons,
	})
}

func handleDDBTransactGetItems(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TransactItems []struct {
			Get *struct {
				TableName string         `json:"TableName"`
				Key       map[string]any `json:"Key"`
			} `json:"Get"`
		} `json:"TransactItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// TransactGetItems is one atomic read, so read every item under a single
	// acquisition: per-item acquisitions let a writer commit between two items.
	itemKeys := make([]string, len(req.TransactItems))
	for i, ti := range req.TransactItems {
		if ti.Get == nil {
			continue
		}
		t, ok := ddbTables.Get(ti.Get.TableName)
		if !ok {
			AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", ti.Get.TableName)
			return
		}
		if message := ddbKeyParamError(t, ti.Get.Key); message != "" {
			AWSError(w, "ValidationException", message, http.StatusBadRequest)
			return
		}
		itemKeys[i] = ddbItemKey(t, ti.Get.Key)
	}
	found := ddbItemSnapshots(itemKeys)
	for i, ti := range req.TransactItems {
		if ti.Get == nil {
			continue
		}
		t, _ := ddbTables.Get(ti.Get.TableName)
		if !ddbTake(t, "", 2*ddbReadUnits(found[itemKeys[i]], true), false) {
			ddbWriteThrottled(w, "")
			return
		}
	}
	responses := make([]map[string]any, 0, len(req.TransactItems))
	for i, ti := range req.TransactItems {
		if ti.Get == nil {
			responses = append(responses, map[string]any{})
			continue
		}
		if it, ok := found[itemKeys[i]]; ok {
			responses = append(responses, map[string]any{"Item": it})
		} else {
			responses = append(responses, map[string]any{})
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"Responses": responses})
}

func ddbExtractKey(t DDBTable, item map[string]any) map[string]any {
	key := map[string]any{}
	for _, k := range t.KeySchema {
		if v, ok := item[k.AttributeName]; ok {
			key[k.AttributeName] = v
		}
	}
	return key
}

// A table's prefix is "<table>/", a partition's "<table>/<hash>".
func ddbTableSortedKeys(prefix string) []string {
	rows := ddbItemNames.ListPrefix(prefix)
	keys := make([]string, len(rows))
	for i, row := range rows {
		keys[i] = row.ID
	}
	return keys
}

// keys must be sorted. Resume at the first key strictly greater than
// startKey.
func ddbResumeIndex(keys []string, startKey, prefix, tablePrefix string) int {
	if startKey == prefix || startKey == tablePrefix {
		return 0
	}
	// Resume after the cursor's position whether or not its item still exists, so
	// a deletion between pages does not restart the listing.
	return sort.Search(len(keys), func(i int) bool { return keys[i] > startKey })
}

func ddbResolveAttrName(name string, aliases map[string]string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "#") {
		if v := aliases[name]; v != "" {
			return v
		}
	}
	return name
}

func ddbAttrValuesEqual(a, b any) bool {
	av, aok := a.(map[string]any)
	bv, bok := b.(map[string]any)
	if !aok || !bok {
		return fmt.Sprint(a) == fmt.Sprint(b)
	}
	// Numbers compare by value: {N:"5"} == {N:"5.0"} == {N:"1e1"}.
	if an, ok := av["N"]; ok {
		bn, ok2 := bv["N"]
		return ok2 && ddbCanonicalNumber(fmt.Sprint(an)) == ddbCanonicalNumber(fmt.Sprint(bn))
	}
	if _, ok := bv["N"]; ok {
		return false
	}
	for _, key := range []string{"S", "B", "BOOL", "NULL"} {
		if fmt.Sprint(av[key]) != fmt.Sprint(bv[key]) {
			return false
		}
		if _, ok := av[key]; ok {
			return true
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

// ddbItemNames mirrors ddbItems' keys for ordered iteration.
var ddbItemNames sim.Store[string]

// For an on-demand table, the zero-filled struct the service returns, so
// terraform's state read has a shape to compare.
func ddbTableThroughput(billingMode string, requested *DDBProvisionedThroughput) *DDBProvisionedThroughput {
	if strings.EqualFold(billingMode, "PROVISIONED") && requested != nil {
		return &DDBProvisionedThroughput{
			ReadCapacityUnits:  requested.ReadCapacityUnits,
			WriteCapacityUnits: requested.WriteCapacityUnits,
		}
	}
	return &DDBProvisionedThroughput{}
}
