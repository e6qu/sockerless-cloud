package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestRDSLifecycleActionsRequireTheirSourceState pins Amazon RDS's
// InvalidDBInstanceState answer for a lifecycle action on an instance that is
// not in the state the action runs from — starting an instance that is
// already available used to try to bind its endpoint a second time.
func TestRDSLifecycleActionsRequireTheirSourceState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		status  string
		handler http.HandlerFunc
	}{
		{name: "StartDBInstance on an available instance", status: "available", handler: handleRDSStartInstance},
		{name: "StopDBInstance on a stopped instance", status: "stopped", handler: handleRDSStopInstance},
		{name: "RebootDBInstance on a stopped instance", status: "stopped", handler: handleRDSReboot},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			id := "lifecycle-state-db"
			rdsInstances.Put(id, RDSInstance{DBInstanceIdentifier: id, Engine: "postgres", DBInstanceStatus: testCase.status})
			t.Cleanup(func() { rdsInstances.Delete(id) })
			form := url.Values{"DBInstanceIdentifier": {id}}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			testCase.handler(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "<Code>InvalidDBInstanceState</Code>") {
				t.Fatalf("answer = %d %s, want 400 InvalidDBInstanceState", recorder.Code, recorder.Body.String())
			}
			if stored, _ := rdsInstances.Get(id); stored.DBInstanceStatus != testCase.status {
				t.Fatalf("status moved to %q on a refused action", stored.DBInstanceStatus)
			}
		})
	}
}
