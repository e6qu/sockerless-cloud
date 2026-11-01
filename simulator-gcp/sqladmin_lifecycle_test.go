package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A restart settles the operations it caught unfinished, and an instance
// takes one operation at a time.
func TestSQLRecoverOperationsSettlesInterruptedOperations(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	orphan := newSQLOperationRunning("p", "RESTART", "orders")
	sqlRecoverOperations()
	op, _ := sqlOperations.Get(sqlOperationKey("p", orphan.Name))
	if op.Status != "DONE" || op.Error == nil || op.EndTime == "" {
		t.Fatalf("recovered operation %+v", op)
	}

	do := func(path, body string) int {
		req := httptest.NewRequest(http.MethodPost, "http://sqladmin.googleapis.com"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do("/v1/projects/p/instances", `{"name":"orders","databaseVersion":"SQLSERVER_2022_STANDARD"}`); code != http.StatusOK {
		t.Fatalf("insert: %d", code)
	}
	blocker := newSQLOperationRunning("p", "BACKUP_VOLUME", "orders")
	if code := do("/v1/projects/p/instances/orders/restart", ""); code != http.StatusConflict {
		t.Fatalf("a restart while %s runs answered %d, want 409", blocker.OperationType, code)
	}
	sqlSettleOperation("p", blocker.Name, nil)
	if code := do("/v1/projects/p/instances/orders/restart", ""); code != http.StatusOK {
		t.Fatalf("a restart once the instance is idle answered %d", code)
	}
}
