package main

// AWS DynamoDB uses the awsJson1_0 protocol. The AWS SDK Go v2's
// deserializer requires responses to carry `Content-Type:
// application/x-amz-json-1.0` (not `application/json`); without it the
// SDK silently fails to decode the body and the result struct is nil,
// which terraform-provider-aws then treats as ResourceNotFound (its
// waiter loops 21 times then errors "couldn't find resource").
//
// `sim.WriteJSON` (used elsewhere) sets `application/json`. The
// `writeDDBJSON` wrapper below sets the per-protocol header instead so
// each DynamoDB success response carries the right CT. Errors keep going
// through `sim.AWSErrorf` which already sets `application/x-amz-json-1.1`
// (real AWS uses 1.1 for errors across JSON-RPC services, regardless of
// the service's own payload protocol).

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// DynamoDB — Sockerless's runner workflows often use DynamoDB for
// Terraform state locking (`backend "s3" { dynamodb_table = "..." }`),
// runner-job tracking, and shared state across distributed CI tasks.
// Without this slice, terraform's state-lock acquire 404s and
// `aws dynamodb` workflow steps fail.
//
// Field set covers the JSON-protocol actions terraform + the SDK use:
// CreateTable / DescribeTable / DeleteTable / ListTables /
// PutItem / GetItem / UpdateItem / DeleteItem / Query / Scan +
// the conditional-write semantics terraform's state lock relies on
// (ConditionExpression with attribute_not_exists).

// DDBTable is a DynamoDB table. Real AWS stores items keyed by
// HashKey + RangeKey; the sim collapses to HashKey-only storage
// (the most common shape for Terraform state locks: `LockID` is the
// hash key, no range key) and falls through to the slow path for
// composite keys when a RangeKey is declared.
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

// DDBTableSettings holds table state that DynamoDB exposes through APIs other
// than DescribeTable. Keeping it in a separate durable record prevents
// simulator-only fields from appearing in the service response while allowing
// TTL, point-in-time recovery, and tags to survive SQLite serialization.
type DDBTableSettings struct {
	PITRStatus       string  `json:"pitrStatus,omitempty"` // ENABLED / DISABLED
	TTLStatus        string  `json:"ttlStatus,omitempty"`  // ENABLED / DISABLED
	TTLAttributeName string  `json:"ttlAttributeName,omitempty"`
	Tags             []SMTag `json:"tags,omitempty"`
}

// DDBSSEDescription mirrors the SDK SSEDescription returned by DescribeTable for
// an SSE-enabled table.
type DDBSSEDescription struct {
	Status          string `json:"Status"`
	SSEType         string `json:"SSEType"`
	KMSMasterKeyArn string `json:"KMSMasterKeyArn,omitempty"`
}

// DDBProvisionedThroughput mirrors the SDK shape. For PAY_PER_REQUEST
// tables real AWS still returns a zero-filled struct so terraform's
// reader doesn't NPE — the sim follows.
type DDBProvisionedThroughput struct {
	NumberOfDecreasesToday int64   `json:"NumberOfDecreasesToday"`
	ReadCapacityUnits      int64   `json:"ReadCapacityUnits"`
	WriteCapacityUnits     int64   `json:"WriteCapacityUnits"`
	LastIncreaseDateTime   float64 `json:"LastIncreaseDateTime,omitempty"`
	LastDecreaseDateTime   float64 `json:"LastDecreaseDateTime,omitempty"`
}

// DDBTableClassSummary mirrors the SDK shape — STANDARD (default) or
// STANDARD_INFREQUENT_ACCESS. Real AWS returns this on every Describe.
type DDBTableClassSummary struct {
	TableClass         string  `json:"TableClass"`
	LastUpdateDateTime float64 `json:"LastUpdateDateTime,omitempty"`
}

// DDBWarmThroughput mirrors `types.TableWarmThroughputDescription`. Real
// AWS DynamoDB returns this on every DescribeTable response, with
// Status=ACTIVE on a fresh on-demand table. terraform-provider-aws v6
// added `waitTableWarmThroughputActive` after `waitTableActive` in the
// Create flow — that wait function returns empty state and loops 21
// times if `output.WarmThroughput == nil`, so the field MUST be present
// on every response or terraform errors "waiting for update ... couldn't
// find resource".
type DDBWarmThroughput struct {
	ReadUnitsPerSecond  int64  `json:"ReadUnitsPerSecond"`
	Status              string `json:"Status"`
	WriteUnitsPerSecond int64  `json:"WriteUnitsPerSecond"`
}

// DDBAttributeDef matches the SDK's `AttributeDefinition` shape.
type DDBAttributeDef struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"` // S / N / B
}

// DDBKeySchemaEntry pairs an attribute with its role.
type DDBKeySchemaEntry struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"` // HASH / RANGE
}

// DDBProjection is a secondary index's attribute projection.
type DDBProjection struct {
	ProjectionType   string   `json:"ProjectionType"` // ALL / KEYS_ONLY / INCLUDE
	NonKeyAttributes []string `json:"NonKeyAttributes,omitempty"`
}

// DDBGlobalSecondaryIndex mirrors the GSI shape. The CreateTable request
// carries IndexName/KeySchema/Projection/ProvisionedThroughput; the
// Create/Describe responses additionally carry IndexStatus (ACTIVE),
// IndexArn, ItemCount, and IndexSizeBytes — terraform-provider-aws waits for
// IndexStatus==ACTIVE on every GSI before the table converges.
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

// DDBLocalSecondaryIndex mirrors the LSI shape. LSIs are created with the
// table and have no independent status (no IndexStatus field).
type DDBLocalSecondaryIndex struct {
	IndexName      string              `json:"IndexName"`
	KeySchema      []DDBKeySchemaEntry `json:"KeySchema"`
	Projection     *DDBProjection      `json:"Projection,omitempty"`
	IndexArn       string              `json:"IndexArn,omitempty"`
	ItemCount      int64               `json:"ItemCount"`
	IndexSizeBytes int64               `json:"IndexSizeBytes"`
}

// DDBBillingModeSummary mirrors the SDK shape — `PAY_PER_REQUEST` or
// `PROVISIONED`. The sim accepts both; tests don't exercise actual
// throughput throttling.
type DDBBillingModeSummary struct {
	BillingMode                       string `json:"BillingMode"`
	LastUpdateToPayPerRequestDateTime int64  `json:"LastUpdateToPayPerRequestDateTime,omitempty"`
}

var (
	ddbTables sim.Store[DDBTable]
	// ddbTableSettings holds control-plane state returned outside
	// DescribeTable. Mutations use ddbTableSettingsMu so simultaneous tag,
	// TTL, and PITR updates cannot overwrite one another.
	ddbTableSettings   sim.Store[DDBTableSettings]
	ddbTableSettingsMu sync.Mutex
	// ddbItems holds per-table item maps. Keyed by `<table>/<itemKey>`,
	// where itemKey is a deterministic encoding of the primary-key
	// attribute values (HASH#<value> or HASH#<v>|RANGE#<v>).
	ddbItems   sim.Store[map[string]any]
	ddbItemsMu sync.Mutex
)

// writeDDBJSON writes a DynamoDB success response with the awsJson1_0
// content-type. The AWS SDK Go v2 DynamoDB deserializer requires the
// exact `application/x-amz-json-1.0` value — `application/json` causes
// silent decode failure where output.Table comes back nil, which
// terraform-provider-aws then treats as ResourceNotFound.
func writeDDBJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeDDBConditionalCheckFailed emits ConditionalCheckFailedException, including
// the existing item when ReturnValuesOnConditionCheckFailure=ALL_OLD — the `Item`
// member optimistic-locking libraries read to surface the conflicting record.
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

// ddbTableByArn locates a stored table by its full ARN. Tag CRUD takes
// ResourceArn (not TableName) and real DynamoDB accepts both forms; the
// sim's name-keyed store has to be scanned for an ARN match.
func ddbTableByArn(arn string) (string, DDBTable, bool) {
	if arn == "" {
		return "", DDBTable{}, false
	}
	// ARN shape: arn:aws:dynamodb:<region>:<account>:table/<name>
	const sep = ":table/"
	idx := strings.Index(arn, sep)
	if idx < 0 {
		return "", DDBTable{}, false
	}
	name := arn[idx+len(sep):]
	t, ok := ddbTables.Get(name)
	return name, t, ok
}

func registerDynamoDB(r *sim.AWSRouter, srv *sim.Server) {
	// Item-level ops are CloudTrail DATA events (excluded from LookupEvents); the
	// table-level ops registered below are management events.
	cloudTrailDeclareDataEvents("dynamodb.amazonaws.com",
		"GetItem", "PutItem", "UpdateItem", "DeleteItem", "Query", "Scan",
		"BatchGetItem", "BatchWriteItem", "TransactWriteItems", "TransactGetItems",
		"ExecuteStatement", "ExecuteTransaction", "BatchExecuteStatement")
	ddbTables = sim.MakeStore[DDBTable](srv.DB(), "ddb_tables")
	ddbTableSettings = sim.MakeStore[DDBTableSettings](srv.DB(), "ddb_table_settings")
	ddbItems = sim.MakeStore[map[string]any](srv.DB(), "ddb_items")
	ddbItemNames = sim.MakeStore[string](srv.DB(), "ddb_item_names")

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

// handleDDBDescribeLimits returns the account/table capacity maximums.
func handleDDBDescribeLimits(w http.ResponseWriter, r *http.Request) {
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"AccountMaxReadCapacityUnits":  80000,
		"AccountMaxWriteCapacityUnits": 80000,
		"TableMaxReadCapacityUnits":    40000,
		"TableMaxWriteCapacityUnits":   40000,
	})
}

// handleDDBUpdateContinuousBackups enables/disables PITR. Persists to the
// table's out-of-band settings so DescribeContinuousBackups reads back the
// updated state. Real DynamoDB returns the new ContinuousBackupsDescription;
// terraform-provider-aws polls DescribeContinuousBackups after this to
// confirm convergence.
func handleDDBUpdateContinuousBackups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName                        string `json:"TableName"`
		PointInTimeRecoverySpecification struct {
			PointInTimeRecoveryEnabled bool `json:"PointInTimeRecoveryEnabled"`
		} `json:"PointInTimeRecoverySpecification"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBUpdateTimeToLive enables/disables TTL on a table attribute.
// Persists to the table's out-of-band settings so DescribeTimeToLive reads
// back the updated state.
func handleDDBUpdateTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName               string `json:"TableName"`
		TimeToLiveSpecification struct {
			Enabled       bool   `json:"Enabled"`
			AttributeName string `json:"AttributeName"`
		} `json:"TimeToLiveSpecification"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBTagResource attaches tags + persists upsert. Real DynamoDB
// returns empty body but stores the tags so ListTagsOfResource reads
// them back (same upsert semantics as real AWS: re-tag with same Key
// replaces Value).
func handleDDBTagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string  `json:"ResourceArn"`
		Tags        []SMTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		sim.AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBUntagResource removes tag keys from the persisted set.
// Real DynamoDB returns empty body + silently ignores missing keys.
func handleDDBUntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string   `json:"ResourceArn"`
		TagKeys     []string `json:"TagKeys"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		sim.AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBDescribeContinuousBackups returns the PITR status for a
// table from its persisted out-of-band settings. New tables default to
// DISABLED. terraform-provider-aws polls this after UpdateContinuousBackups
// for convergence.
func handleDDBDescribeContinuousBackups(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBDescribeTimeToLive returns TTL config from the table's persisted
// out-of-band settings. terraform-provider-aws
// polls this after UpdateTimeToLive until status matches.
func handleDDBDescribeTimeToLive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TableName string `json:"TableName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	_, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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

// handleDDBListTagsOfResource returns the persisted out-of-band tag list for a
// table ARN, matching how real DynamoDB keeps tags outside DescribeTable.
func handleDDBListTagsOfResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.ResourceArn == "" {
		sim.AWSError(w, "ValidationException", "ResourceArn is required", http.StatusBadRequest)
		return
	}
	name, _, ok := ddbTableByArn(req.ResourceArn)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.TableName == "" {
		sim.AWSError(w, "ValidationException", "TableName is required", http.StatusBadRequest)
		return
	}
	if _, exists := ddbTables.Get(req.TableName); exists {
		sim.AWSErrorf(w, "ResourceInUseException", http.StatusBadRequest,
			"Table already exists: %s", req.TableName)
		return
	}
	if len(req.KeySchema) == 0 {
		sim.AWSError(w, "ValidationException", "KeySchema is required", http.StatusBadRequest)
		return
	}
	// Every key attribute (table + GSI + LSI) must be declared in
	// AttributeDefinitions — real DynamoDB rejects otherwise.
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
			sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
				"One or more parameter values were invalid: Some index key attributes are not defined in AttributeDefinitions. Keys: [%s]", k.AttributeName)
			return
		}
	}
	billingMode := req.BillingMode
	if billingMode == "" {
		billingMode = "PROVISIONED"
	}
	now := float64(time.Now().Unix())

	// Model secondary indexes as immediately ACTIVE. terraform-provider-aws
	// waits for every GSI's IndexStatus to reach ACTIVE before the table
	// converges; pre-fix the indexes were dropped entirely (returned null).
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
		TableId:              generateUUID(),
		TableArn:             ddbTableArn(req.TableName),
		TableStatus:          "ACTIVE",
		CreationDateTime:     now,
		AttributeDefinitions: req.AttributeDefinitions,
		KeySchema:            req.KeySchema,
		BillingModeSummary: &DDBBillingModeSummary{
			BillingMode: billingMode,
		},
		// Real AWS returns a zero-filled ProvisionedThroughput even for
		// PAY_PER_REQUEST tables so terraform's reader doesn't NPE.
		ProvisionedThroughput: &DDBProvisionedThroughput{
			NumberOfDecreasesToday: 0,
			ReadCapacityUnits:      0,
			WriteCapacityUnits:     0,
		},
		TableClassSummary: &DDBTableClassSummary{
			TableClass: "STANDARD",
		},
		// Real DynamoDB returns WarmThroughput on every Describe with
		// Status=ACTIVE for on-demand tables; terraform-provider-aws v6's
		// waitTableWarmThroughputActive depends on this field being
		// present + non-nil.
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
	// Server-side encryption: once enabled, real DynamoDB reports the full
	// descriptor (Status ENABLED) on every Describe. SSEType defaults to KMS
	// (a customer/AWS-managed key) when omitted.
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
	// Vector indexes declared with the table, validated the same way
	// UpdateTable validates one it is asked to create.
	for _, raw := range req.VectorIndexes {
		idx, err := ddbParseVectorIndex(req.TableName, raw)
		if err != nil {
			sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
			return
		}
		table.VectorIndexes = append(table.VectorIndexes, idx)
	}
	ddbTables.Put(req.TableName, table)
	// Tags set at create time round-trip through ListTagsOfResource — real
	// DynamoDB accepts Tags on CreateTable; dropping them makes every plan
	// re-add them.
	ddbTableSettings.Put(req.TableName, DDBTableSettings{Tags: req.Tags})
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": table})
}

func handleDDBDescribeTable(w http.ResponseWriter, r *http.Request) {
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
	writeDDBJSON(w, http.StatusOK, map[string]any{"Table": t})
}

// ddbActivateGSI fills the stored/response fields of a GSI so it reports as
// immediately ACTIVE (the sim models index builds as instantaneous).
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

// handleDDBUpdateTable applies GSI create/update/delete, throughput, billing
// mode, and deletion-protection changes. terraform-provider-aws manages the GSI
// lifecycle after table creation via UpdateTable's GlobalSecondaryIndexUpdates,
// then polls DescribeTable until each new GSI's IndexStatus is ACTIVE.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if len(req.VectorIndexUpdates) > 0 {
		if err := ddbApplyVectorIndexUpdates(&t, req.VectorIndexUpdates); err != nil {
			sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
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
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

func handleDDBDeleteTable(w http.ResponseWriter, r *http.Request) {
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
	ddbTables.Delete(req.TableName)
	ddbTableSettings.Delete(req.TableName)
	// Real DeleteTable deletes the table AND all of its items — purge the
	// item stores so the rows don't survive into a same-named recreate.
	// Keys are "<table>/<hash>[|<rng>]"; the trailing "/" prevents a prefix
	// collision with a differently-named table (e.g. "foo" vs "foobar").
	prefix := req.TableName + "/"
	for _, k := range ddbItemNames.List() {
		if strings.HasPrefix(k, prefix) {
			ddbItems.Delete(k)
			ddbItemNames.Delete(k)
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"TableDescription": t})
}

func handleDDBListTables(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExclusiveStartTableName string `json:"ExclusiveStartTableName"`
		Limit                   int    `json:"Limit"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidParameterValue", "Invalid request body", http.StatusBadRequest)
		return
	}
	all := ddbTables.List()
	sortBy(all, func(t DDBTable) string { return t.TableName })

	// ExclusiveStartTableName is a name-based cursor; convert to offset token.
	token := ""
	if req.ExclusiveStartTableName != "" {
		for i, t := range all {
			if t.TableName == req.ExclusiveStartTableName {
				token = strconv.Itoa(i + 1)
				break
			}
		}
	}
	page, next := awsPage(all, token, req.Limit, 100)
	names := make([]string, 0, len(page))
	for _, t := range page {
		names = append(names, t.TableName)
	}
	out := map[string]any{"TableNames": names}
	if next != "" {
		// Convert token back to a table name for LastEvaluatedTableName.
		idx, _ := strconv.Atoi(next)
		if idx > 0 && idx <= len(all) {
			out["LastEvaluatedTableName"] = all[idx-1].TableName
		}
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// ddbItemKey encodes the primary-key attribute values into a stable
// store key. Composite keys join HASH and RANGE with `|`.
func ddbItemKey(table DDBTable, item map[string]any) string {
	var hash, rng string
	for _, k := range table.KeySchema {
		val := ddbExtractAttrValue(item[k.AttributeName])
		switch k.KeyType {
		case "HASH":
			hash = val
		case "RANGE":
			rng = val
		}
	}
	if rng != "" {
		return table.TableName + "/" + hash + "|" + rng
	}
	return table.TableName + "/" + hash
}

// ddbExtractAttrValue encodes a key AttributeValue (`{"S"|"N"|"B": ...}`) into
// the store-key component. The type tag is prefixed so an S value never collides
// with an equal-looking N/B value, and N values are canonicalized so DynamoDB's
// numeric equality holds: "01", "1", and "1.0" map to the same key. big.Rat is
// exact (DynamoDB numbers carry up to 38 digits — float64 would corrupt them).
func ddbExtractAttrValue(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m["S"]; ok {
		return "S#" + fmt.Sprintf("%v", s)
	}
	if n, ok := m["N"]; ok {
		return "N#" + ddbCanonicalNumber(fmt.Sprintf("%v", n))
	}
	if b, ok := m["B"]; ok {
		return "B#" + fmt.Sprintf("%v", b)
	}
	return ""
}

// ddbAttrValueSize returns the stored byte size DynamoDB assigns an attribute
// value. Binary values are measured after base64 decoding, numbers use the
// documented significant-digit formula, and document values include their
// per-container and per-element overhead.
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
	return 1 // BOOL / NULL
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

// ddbReadUnits / ddbWriteUnits convert an item size to consumed capacity units
// (read: 4KB blocks, halved for eventually-consistent; write: 1KB blocks).
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

// ddbConsumedCapacity builds the ConsumedCapacity response block, or nil when
// ReturnConsumedCapacity was unset/NONE.
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

// ddbCanonicalNumber returns an exact canonical form of a DynamoDB number string
// so equal numbers share a store key. Falls back to the trimmed input when it
// isn't a valid number.
func ddbCanonicalNumber(s string) string {
	if r, ok := new(big.Rat).SetString(strings.TrimSpace(s)); ok {
		return r.RatString()
	}
	return strings.TrimSpace(s)
}

// ddbMaxItemDepth is DynamoDB's attribute nesting limit (32 levels). A top-level
// attribute is level 1, so its M/L children may descend 31 further levels.
const ddbMaxItemDepth = 32

// ddbItemTooDeep reports whether any attribute of an item nests beyond the
// 32-level limit (real DynamoDB returns ValidationException). The walk is
// bounded to the limit, so a pathologically deep item can't overflow the stack
// here either.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if ddbItemTooDeep(req.Item) {
		sim.AWSError(w, "ValidationException",
			"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
		return
	}
	if err := ddbValidateItemSize(req.Item); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	itemKey := ddbItemKey(t, req.Item)
	old, exists := ddbItems.Get(itemKey)

	// Atomically evaluate the ConditionExpression (e.g. terraform's state-lock
	// "attribute_not_exists(LockID)") before writing.
	if condOK, err := ddbEvalCondition(old, exists, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, old, exists)
		return
	}
	if okExp, err := ddbCheckExpected(old, exists, req.Expected, req.ConditionalOperator); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, old, exists)
		return
	}
	ddbItems.Put(itemKey, req.Item)
	ddbItemNames.Put(itemKey, itemKey)
	ddbBumpKeyGen()
	resp := map[string]any{}
	if req.ReturnValues == "ALL_OLD" && exists {
		resp["Attributes"] = old
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(req.Item)); cc != nil {
		resp["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

// ddbProjectItem restricts an item to the top-level attributes named in a
// ProjectionExpression (resolving #alias names via ExpressionAttributeNames).
// An empty projection returns the item unchanged. Nested document paths select
// by their top-level attribute — sufficient for the common projection case;
// previously ProjectionExpression was ignored entirely and the whole item came
// back.
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
		// Project just the value at this (possibly nested) path, grafting it into
		// out at the same path so `a.b` returns only the nested sub-attribute and
		// sibling paths (`a.b, a.c`) merge under their shared prefix.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	itemKey := ddbItemKey(t, req.Key)
	item, found := ddbItemSnapshot(itemKey)
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
		// Modern clients drive UpdateItem with an UpdateExpression; the legacy
		// AttributeUpdates parameter is also accepted.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	itemKey := ddbItemKey(t, req.Key)
	item, existed := ddbItems.Get(itemKey)
	if condOK, err := ddbEvalCondition(item, existed, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, item, existed)
		return
	}
	if okExp, err := ddbCheckExpected(item, existed, req.Expected, req.ConditionalOperator); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, item, existed)
		return
	}
	oldItem := ddbCloneItem(item)
	if item == nil {
		item = map[string]any{}
		// Copy primary-key attrs from Key into the new item.
		for k, v := range req.Key {
			item[k] = v
		}
	}
	if req.UpdateExpression != "" {
		if err := ddbApplyUpdateExpression(item, req.UpdateExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
			sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
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
		default: // PUT (default)
			item[attr] = upd.Value
		}
	}
	if err := ddbValidateItemSize(item); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	ddbItems.Put(itemKey, item)
	ddbItemNames.Put(itemKey, itemKey)
	ddbBumpKeyGen()

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
				// Newly-added attr has no old value to emit.
				continue
			}
			if !ddbAttrEqual(oldVal, newVal) {
				changed[attr] = oldVal
			}
		}
		if len(changed) > 0 {
			resp["Attributes"] = changed
		}
	default: // NONE (default) and "" emit no Attributes field.
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(item)); cc != nil {
		resp["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, resp)
}

// ddbCloneItem deep-copies a stored item via JSON round-trip so the
// pre-update snapshot is independent of in-place mutations. Returns nil
// when the source item is nil/empty.
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
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	item, ok := ddbItems.Get(itemKey)
	if !ok {
		return nil, false
	}
	return ddbCloneItem(item), true
}

// ddbAttrEqual compares two attribute values structurally via JSON
// canonicalization so attrs present-and-different are detected.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	itemKey := ddbItemKey(t, req.Key)
	oldItem, existed := ddbItems.Get(itemKey)
	if condOK, err := ddbEvalCondition(oldItem, existed, req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !condOK {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, oldItem, existed)
		return
	}
	if okExp, err := ddbCheckExpected(oldItem, existed, req.Expected, req.ConditionalOperator); err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	} else if !okExp {
		writeDDBConditionalCheckFailed(w, req.ReturnValuesOnConditionCheckFailure, oldItem, existed)
		return
	}
	ddbItems.Delete(itemKey)
	ddbItemNames.Delete(itemKey)
	ddbBumpKeyGen()
	out := map[string]any{}
	if strings.EqualFold(req.ReturnValues, "ALL_OLD") && existed {
		out["Attributes"] = oldItem
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, ddbWriteUnits(oldItem)); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// handleDDBQuery returns items whose primary-key attributes match the
// request's KeyConditionExpression. The implemented expression subset is
// the DynamoDB equality form used by SDK/CLI/Terraform clients:
// `<hash> = :value` plus optional `AND <range> = :value`.
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
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	// IndexName selects a GSI/LSI; the KeyConditionExpression then matches that
	// index's key attributes. The matcher is generic over item attributes, so a
	// GSI query needs no special handling beyond rejecting an unknown index.
	if req.IndexName != "" && !ddbHasIndex(t, req.IndexName) {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"The table does not have the specified index: %s", req.IndexName)
		return
	}
	if req.ConsistentRead && ddbIsGSI(t, req.IndexName) {
		sim.AWSError(w, "ValidationException",
			"Consistent reads are not supported on global secondary indexes", http.StatusBadRequest)
		return
	}
	// Compile the key + filter expressions once, up front: a malformed
	// expression (or an undefined #name/:value) is a ValidationException
	// regardless of whether any item is examined — never a silent empty result.
	keyExpr, err := ddbCompileExpr("KeyConditionExpression", req.KeyConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	filterExpr, err := ddbCompileExpr("FilterExpression", req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}

	prefix := req.TableName + "/"
	// ScanIndexForward (default true) walks the sort key ascending; false walks
	// it descending — the basis of every "latest N" access pattern. The
	// candidate key set is built in the scan direction so ExclusiveStartKey
	// resume + Limit + LastEvaluatedKey all work in that direction.
	remaining := ddbQueryCandidateKeys(ddbTableSortedKeys(prefix), t, req.ExclusiveStartKey, prefix,
		req.ScanIndexForward == nil || *req.ScanIndexForward)

	// Query reads only the items matching the KeyConditionExpression; Limit caps
	// how many such items are *examined* (ScannedCount), and the optional
	// FilterExpression is applied to those, so Count <= ScannedCount.
	var items []map[string]any
	scanned := 0
	var lastScanned map[string]any
	exhausted := true
	for i, k := range remaining {
		it, ok2 := ddbItemSnapshot(k)
		if !ok2 {
			continue
		}
		if !keyExpr.match(it, true) {
			continue
		}
		scanned++
		lastScanned = it
		if filterExpr.match(it, true) {
			items = append(items, it)
		}
		if req.Limit > 0 && scanned >= req.Limit {
			if i+1 < len(remaining) {
				exhausted = false
			}
			break
		}
	}
	if items == nil {
		items = []map[string]any{}
	}

	out := map[string]any{"Count": len(items), "ScannedCount": scanned}
	// Emit LastEvaluatedKey when we stopped on Limit before exhausting the keys —
	// from the last *scanned* (key-matched) item's full key, the resume cursor.
	if !exhausted && lastScanned != nil {
		out["LastEvaluatedKey"] = ddbExtractKey(t, lastScanned)
	}
	// Select=COUNT returns only Count/ScannedCount and omits Items.
	if !strings.EqualFold(req.Select, "COUNT") {
		out["Items"] = ddbProjectItems(items, req.ProjectionExpression, req.ExpressionAttributeNames)
	}
	queryUnits := 0.0
	for _, it := range items {
		queryUnits += ddbReadUnits(it, req.ConsistentRead)
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, queryUnits); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// ddbQueryCandidateKeys returns the table's item keys to examine for a Query,
// resumed past ExclusiveStartKey and ordered for ScanIndexForward (ascending
// when forward, descending otherwise). keys must be the ascending sorted set.
func ddbQueryCandidateKeys(keys []string, t DDBTable, exclusiveStart map[string]any, prefix string, forward bool) []string {
	startKey := ddbItemKey(t, exclusiveStart)
	if forward {
		return keys[ddbResumeIndex(keys, startKey, prefix, t.TableName+"/"):]
	}
	// Descending: examine keys strictly less than the resume key, reversed.
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

// ddbProjectItems projects every item by a ProjectionExpression (no-op when empty).
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

// ddbIsGSI reports whether name is one of the table's global secondary indexes
// (which, unlike LSIs and the base table, don't support consistent reads).
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

// ddbHasIndex reports whether the table has a GSI or LSI with the given name.
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
		sim.AWSErrorf(w, "InvalidParameterValue", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	t, ok := ddbTables.Get(req.TableName)
	if !ok {
		sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
			"Requested resource not found: Table: %s not found", req.TableName)
		return
	}
	if req.IndexName != "" && !ddbHasIndex(t, req.IndexName) {
		sim.AWSErrorf(w, "ValidationException", http.StatusBadRequest,
			"The table does not have the specified index: %s", req.IndexName)
		return
	}
	if req.ConsistentRead && ddbIsGSI(t, req.IndexName) {
		sim.AWSError(w, "ValidationException",
			"Consistent reads are not supported on global secondary indexes", http.StatusBadRequest)
		return
	}
	// Segment/TotalSegments must be provided together and 0 <= Segment < TotalSegments.
	if (req.Segment != nil) != (req.TotalSegments != nil) {
		sim.AWSError(w, "ValidationException",
			"The Segment and TotalSegments parameters must be provided together", http.StatusBadRequest)
		return
	}
	if req.TotalSegments != nil {
		seg := 0
		if req.Segment != nil {
			seg = *req.Segment
		}
		if *req.TotalSegments < 1 || seg < 0 || seg >= *req.TotalSegments {
			sim.AWSError(w, "ValidationException",
				"The Segment parameter is zero-based and must be less than the TotalSegments parameter", http.StatusBadRequest)
			return
		}
	}
	// Compile the filter once: a malformed FilterExpression (or an undefined
	// #name/:value) is a ValidationException, not a silently emptied result.
	filterExpr, err := ddbCompileExpr("FilterExpression", req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
		return
	}
	prefix := req.TableName + "/"
	keys := ddbTableSortedKeys(prefix)
	// Parallel scan: a TotalSegments=N scan issues N calls, each owning a disjoint
	// subset of the table's keys — partition by position so the union is the whole
	// table with no overlap (real DynamoDB hashes; deterministic positional split
	// gives the same disjoint-coverage contract).
	if req.TotalSegments != nil {
		seg := 0
		if req.Segment != nil {
			seg = *req.Segment
		}
		part := keys[:0:0]
		for i, k := range keys {
			if i%*req.TotalSegments == seg {
				part = append(part, k)
			}
		}
		keys = part
	}

	startKey := ddbItemKey(t, req.ExclusiveStartKey)
	startIdx := ddbResumeIndex(keys, startKey, prefix, t.TableName+"/")

	// Limit caps the number of items *examined* (ScannedCount), not the number
	// returned; the FilterExpression is applied to the examined items, so a
	// filtered Scan can return fewer than Limit and still carry a
	// LastEvaluatedKey to resume from.
	remaining := keys[startIdx:]
	var items []map[string]any
	scanned := 0
	var lastScanned map[string]any
	exhausted := true
	for i, k := range remaining {
		it, ok2 := ddbItemSnapshot(k)
		if !ok2 {
			continue
		}
		scanned++
		lastScanned = it
		if filterExpr.match(it, true) {
			items = append(items, it)
		}
		if req.Limit > 0 && scanned >= req.Limit {
			if i+1 < len(remaining) {
				exhausted = false
			}
			break
		}
	}
	if items == nil {
		items = []map[string]any{}
	}

	out := map[string]any{"Count": len(items), "ScannedCount": scanned}
	if !exhausted && lastScanned != nil {
		out["LastEvaluatedKey"] = ddbExtractKey(t, lastScanned)
	}
	if !strings.EqualFold(req.Select, "COUNT") {
		out["Items"] = ddbProjectItems(items, req.ProjectionExpression, req.ExpressionAttributeNames)
	}
	scanUnits := 0.0
	for _, it := range items {
		scanUnits += ddbReadUnits(it, req.ConsistentRead)
	}
	if cc := ddbConsumedCapacity(req.ReturnConsumedCapacity, req.TableName, scanUnits); cc != nil {
		out["ConsumedCapacity"] = cc
	}
	writeDDBJSON(w, http.StatusOK, out)
}

// ddbEvalCondition reports whether a DynamoDB ConditionExpression holds for an
// item. `exists` is whether the item is currently present. Supports the common
// ConditionExpression via the full expression grammar in dynamodb_expr.go
// (functions, comparators, BETWEEN/IN, AND/OR/NOT, parentheses, nested paths).
// An empty expression always holds. A malformed expression, or one referencing
// an undefined #name / :value, returns an error (surfaced as a
// ValidationException) rather than a silent non-match.
func ddbEvalCondition(item map[string]any, exists bool, expr string, names map[string]string, values map[string]any) (bool, error) {
	c, err := ddbCompileExpr("ConditionExpression", expr, names, values)
	if err != nil {
		return false, err
	}
	return c.match(item, exists), nil
}

// ddbExpectedEntry is one entry of the legacy Expected map.
type ddbExpectedEntry struct {
	Value              map[string]any   `json:"Value"`
	Exists             *bool            `json:"Exists"`
	ComparisonOperator string           `json:"ComparisonOperator"`
	AttributeValueList []map[string]any `json:"AttributeValueList"`
}

// ddbExpectedToCondition translates the legacy Expected map (+ ConditionalOperator)
// into an equivalent ConditionExpression with generated #name/:val placeholders,
// so it evaluates through the same engine as a modern ConditionExpression.
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
		// Legacy shorthand: Exists / Value without an explicit operator.
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

// ddbCheckExpected evaluates the legacy Expected condition (no-op when empty).
// Returns false only when a non-empty Expected condition fails.
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
	// Numeric comparison when both sides are N; lexicographic otherwise.
	as, bs := ddbScalarString(a), ddbScalarString(b)
	// Exact rational comparison when both sides are numbers (DynamoDB carries 38
	// digits — float64 would mis-order large/precise numbers); lexicographic else.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()
	// Validate the whole batch first (real DynamoDB rejects before applying):
	// 1..25 total requests, every table exists, every put item within depth.
	total := 0
	for tableName, ops := range req.RequestItems {
		total += len(ops)
		if _, ok := ddbTables.Get(tableName); !ok {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", tableName)
			return
		}
		for _, op := range ops {
			if op.PutRequest != nil && ddbItemTooDeep(op.PutRequest.Item) {
				sim.AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if op.PutRequest != nil {
				if err := ddbValidateItemSize(op.PutRequest.Item); err != nil {
					sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
		}
	}
	if total == 0 || total > 25 {
		sim.AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'requestItems' failed to satisfy constraint: Member must have length less than or equal to 25 and at least 1",
			http.StatusBadRequest)
		return
	}
	for tableName, ops := range req.RequestItems {
		t, _ := ddbTables.Get(tableName)
		for _, op := range ops {
			switch {
			case op.PutRequest != nil:
				key := ddbItemKey(t, op.PutRequest.Item)
				ddbItems.Put(key, op.PutRequest.Item)
				ddbItemNames.Put(key, key)
				ddbBumpKeyGen()
			case op.DeleteRequest != nil:
				key := ddbItemKey(t, op.DeleteRequest.Key)
				ddbItems.Delete(key)
				ddbItemNames.Delete(key)
				ddbBumpKeyGen()
			}
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"UnprocessedItems": map[string]any{}})
}

func handleDDBBatchGetItem(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestItems map[string]struct {
			Keys []map[string]any `json:"Keys"`
		} `json:"RequestItems"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	responses := map[string][]map[string]any{}
	for tableName, spec := range req.RequestItems {
		t, ok := ddbTables.Get(tableName)
		if !ok {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", tableName)
			return
		}
		items := []map[string]any{}
		for _, key := range spec.Keys {
			if it, ok := ddbItemSnapshot(ddbItemKey(t, key)); ok {
				items = append(items, it)
			}
		}
		responses[tableName] = items
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{
		"Responses":       responses,
		"UnprocessedKeys": map[string]any{},
	})
}

// handleDDBTransactWriteItems applies Put/Delete/Update/ConditionCheck actions
// atomically: all ConditionExpressions are evaluated first under the item lock;
// if any fails the whole transaction aborts with TransactionCanceledException.
func handleDDBTransactWriteItems(w http.ResponseWriter, r *http.Request) {
	// One operation shape covers Put/Update/Delete/ConditionCheck — exactly one
	// is set per item. Put carries Item; the others carry Key; Update also
	// carries UpdateExpression.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	// Real DynamoDB allows 1..100 actions per transaction.
	if n := len(req.TransactItems); n == 0 || n > 100 {
		sim.AWSError(w, "ValidationException",
			"1 validation error detected: Value at 'transactItems' failed to satisfy constraint: Member must have length less than or equal to 100 and at least 1",
			http.StatusBadRequest)
		return
	}
	ddbItemsMu.Lock()
	defer ddbItemsMu.Unlock()

	// Validate exactly-one-op + the table exists, and evaluate EVERY item's
	// condition so CancellationReasons reflects all items (real DynamoDB returns
	// one {Code} per item, in order; "None" for items that didn't cause the
	// cancellation).
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
			sim.AWSError(w, "ValidationException",
				"TransactItems can only contain one of Put, Update, Delete, or ConditionCheck", http.StatusBadRequest)
			return
		}
		keyItem := op.Key
		if ti.Put != nil {
			keyItem = op.Item
		}
		t, ok := ddbTables.Get(op.TableName)
		if !ok {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", op.TableName)
			return
		}
		current, exists := ddbItems.Get(ddbItemKey(t, keyItem))
		condOK, err := ddbEvalCondition(current, exists, op.ConditionExpression, op.ExpressionAttributeNames, op.ExpressionAttributeValues)
		if err != nil {
			sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
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
				sim.AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if err := ddbValidateItemSize(op.Item); err != nil {
				sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
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
					sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
			if ddbItemTooDeep(updated) {
				sim.AWSError(w, "ValidationException",
					"Item nesting exceeds the 32-level maximum", http.StatusBadRequest)
				return
			}
			if err := ddbValidateItemSize(updated); err != nil {
				sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	if cancelled {
		writeDDBTransactionCancelled(w, reasons)
		return
	}

	// Apply mutations (every condition passed). ConditionCheck is read-only.
	for _, ti := range req.TransactItems {
		switch {
		case ti.Put != nil:
			t, _ := ddbTables.Get(ti.Put.TableName)
			key := ddbItemKey(t, ti.Put.Item)
			ddbItems.Put(key, ti.Put.Item)
			ddbItemNames.Put(key, key)
			ddbBumpKeyGen()
		case ti.Update != nil:
			t, _ := ddbTables.Get(ti.Update.TableName)
			key := ddbItemKey(t, ti.Update.Key)
			item, _ := ddbItems.Get(key)
			if item == nil {
				item = map[string]any{}
				for k, v := range ti.Update.Key {
					item[k] = v
				}
			}
			if ti.Update.UpdateExpression != "" {
				if err := ddbApplyUpdateExpression(item, ti.Update.UpdateExpression, ti.Update.ExpressionAttributeNames, ti.Update.ExpressionAttributeValues); err != nil {
					sim.AWSError(w, "ValidationException", err.Error(), http.StatusBadRequest)
					return
				}
			}
			ddbItems.Put(key, item)
			ddbItemNames.Put(key, key)
			ddbBumpKeyGen()
		case ti.Delete != nil:
			t, _ := ddbTables.Get(ti.Delete.TableName)
			key := ddbItemKey(t, ti.Delete.Key)
			ddbItems.Delete(key)
			ddbItemNames.Delete(key)
			ddbBumpKeyGen()
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{})
}

// writeDDBTransactionCancelled emits TransactionCanceledException with a per-item
// CancellationReasons array (one entry per TransactItem, in request order) — the
// shape the AWS SDK / ElectroDB read to map a conditional failure to a domain
// conflict. The __type carries the service prefix DynamoDB emits.
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
		sim.AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	responses := make([]map[string]any, 0, len(req.TransactItems))
	for _, ti := range req.TransactItems {
		if ti.Get == nil {
			responses = append(responses, map[string]any{})
			continue
		}
		t, ok := ddbTables.Get(ti.Get.TableName)
		if !ok {
			sim.AWSErrorf(w, "ResourceNotFoundException", http.StatusBadRequest,
				"Requested resource not found: Table: %s not found", ti.Get.TableName)
			return
		}
		if it, ok := ddbItemSnapshot(ddbItemKey(t, ti.Get.Key)); ok {
			responses = append(responses, map[string]any{"Item": it})
		} else {
			responses = append(responses, map[string]any{})
		}
	}
	writeDDBJSON(w, http.StatusOK, map[string]any{"Responses": responses})
}

// ddbExtractKey builds a DynamoDB key AttributeValue map from an item's primary key attributes.
func ddbExtractKey(t DDBTable, item map[string]any) map[string]any {
	key := map[string]any{}
	for _, k := range t.KeySchema {
		if v, ok := item[k.AttributeName]; ok {
			key[k.AttributeName] = v
		}
	}
	return key
}

// ddbKeyIndex caches per-table sorted item-key slices so a paginated Query/Scan
// resumes from its ExclusiveStartKey cursor without re-walking and re-sorting the
// entire cross-table key set on every page. It is rebuilt from ddbItemNames (the
// source of truth, which survives SQLite-backed restarts) only when a write has
// bumped the generation since the last build — within a page sequence no write
// occurs, so pages 2..N reuse the cached slice.
var ddbKeyIndex struct {
	mu      sync.Mutex
	gen     uint64 // last generation the cache was built at
	byTable map[string][]string
}

// ddbKeyGen is bumped (atomically) on every item-name Put/Delete so the index
// cache knows to rebuild. Mutations already hold ddbItemsMu, but the read path
// (Query/Scan) does not, so a dedicated counter keeps the cache coherent.
var ddbKeyGen atomic.Uint64

// ddbBumpKeyGen invalidates the cached key index. Call after any ddbItemNames
// Put/Delete.
func ddbBumpKeyGen() { ddbKeyGen.Add(1) }

// ddbTableSortedKeys returns the sorted item keys for one table (prefix
// "<table>/"), using the cached index and rebuilding it only when the key set
// has changed since the last build.
func ddbTableSortedKeys(prefix string) []string {
	gen := ddbKeyGen.Load()
	ddbKeyIndex.mu.Lock()
	defer ddbKeyIndex.mu.Unlock()
	if ddbKeyIndex.byTable == nil || ddbKeyIndex.gen != gen {
		byTable := make(map[string][]string)
		for _, name := range ddbItemNames.List() {
			if i := strings.IndexByte(name, '/'); i >= 0 {
				tp := name[:i+1]
				byTable[tp] = append(byTable[tp], name)
			}
		}
		for tp := range byTable {
			sort.Strings(byTable[tp])
		}
		ddbKeyIndex.byTable = byTable
		ddbKeyIndex.gen = gen
	}
	keys := ddbKeyIndex.byTable[prefix]
	// Return a copy so callers can't mutate the cached slice.
	out := make([]string, len(keys))
	copy(out, keys)
	return out
}

// ddbResumeIndex returns the index into a sorted key slice at which to resume
// after an ExclusiveStartKey, via binary search rather than a linear scan. keys
// must be sorted. The resume point is the first key strictly greater than
// startKey, matching the original "advance past the matching key" semantics.
func ddbResumeIndex(keys []string, startKey, prefix, tablePrefix string) int {
	if startKey == prefix || startKey == tablePrefix {
		return 0
	}
	// sort.Search finds the first index with keys[i] >= startKey. The original
	// linear scan advanced one past an exact match and left the cursor at 0 when
	// the key was absent (e.g. it was deleted between pages); reproduce both:
	// exact match → i+1, no match → 0.
	i := sort.Search(len(keys), func(i int) bool { return keys[i] >= startKey })
	if i < len(keys) && keys[i] == startKey {
		return i + 1
	}
	return 0
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
	// Numbers compare by value, not text: {N:"5"} == {N:"5.0"} == {N:"1e1"}.
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

// ddbItemNames mirrors the keys of ddbItems for iteration. Maintained
// alongside Put/Delete in handleDDBPutItem etc.
var ddbItemNames sim.Store[string]
