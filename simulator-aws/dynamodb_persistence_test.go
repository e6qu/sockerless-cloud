package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

func callDynamoDBHandler(t *testing.T, handler http.HandlerFunc, body string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	handler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("DynamoDB handler returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestDynamoDBTableSettingsPersistAcrossStoreReopen(t *testing.T) {
	stateDir := t.TempDir()
	db, err := sim.OpenDB(stateDir)
	if err != nil {
		t.Fatalf("open simulator state: %v", err)
	}
	tables, err := sim.NewSQLiteStore[DDBTable](db, "ddb_tables")
	if err != nil {
		t.Fatalf("open DynamoDB table store: %v", err)
	}
	settings, err := sim.NewSQLiteStore[DDBTableSettings](db, "ddb_table_settings")
	if err != nil {
		t.Fatalf("open DynamoDB table settings store: %v", err)
	}

	const tableName = "jobs"
	tableARN := ddbTableArn(tableName)
	tables.Put(tableName, DDBTable{TableName: tableName, TableArn: tableARN})
	ddbTables = tables
	ddbTableSettings = settings
	callDynamoDBHandler(t, handleDDBUpdateContinuousBackups,
		`{"TableName":"jobs","PointInTimeRecoverySpecification":{"PointInTimeRecoveryEnabled":true}}`)
	callDynamoDBHandler(t, handleDDBUpdateTimeToLive,
		`{"TableName":"jobs","TimeToLiveSpecification":{"Enabled":true,"AttributeName":"ExpiresAt"}}`)
	callDynamoDBHandler(t, handleDDBTagResource,
		fmt.Sprintf(`{"ResourceArn":%q,"Tags":[{"Key":"env","Value":"dev"}]}`, tableARN))
	if err := db.Close(); err != nil {
		t.Fatalf("close simulator state: %v", err)
	}

	db, err = sim.OpenDB(stateDir)
	if err != nil {
		t.Fatalf("reopen simulator state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tables, err = sim.NewSQLiteStore[DDBTable](db, "ddb_tables")
	if err != nil {
		t.Fatalf("reopen DynamoDB table store: %v", err)
	}
	settings, err = sim.NewSQLiteStore[DDBTableSettings](db, "ddb_table_settings")
	if err != nil {
		t.Fatalf("reopen DynamoDB table settings store: %v", err)
	}

	ddbTables = tables
	ddbTableSettings = settings
	if _, ok := tables.Get(tableName); !ok {
		t.Fatal("persisted DynamoDB table disappeared after reopening the state store")
	}
	got, ok := settings.Get(tableName)
	if !ok {
		t.Fatal("persisted DynamoDB table settings disappeared after reopening the state store")
	}
	if got.PITRStatus != "ENABLED" {
		t.Fatalf("persisted PITR status = %q, want ENABLED", got.PITRStatus)
	}
	if got.TTLStatus != "ENABLED" || got.TTLAttributeName != "ExpiresAt" {
		t.Fatalf("persisted TTL settings = %#v, want ENABLED on ExpiresAt", got)
	}
	if len(got.Tags) != 1 || got.Tags[0].Key != "env" || got.Tags[0].Value != "dev" {
		t.Fatalf("persisted DynamoDB tags = %#v, want env=dev", got.Tags)
	}

	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	ddbItems = sim.MakeStore[map[string]any](nil, "ddb_items")
	ddbItemNames = sim.MakeStore[string](nil, "ddb_item_names")
	callDynamoDBHandler(t, handleDDBDeleteTable, `{"TableName":"jobs"}`)
	if _, ok := settings.Get(tableName); ok {
		t.Fatal("DynamoDB table settings survived deletion of their owning table")
	}
}
