package main

import "testing"

func TestSSMRequestConditionKeysReadTheRequestMembers(t *testing.T) {
	cases := []struct {
		name, operation, body string
		want                  map[string][]string
	}{
		{"create document", "CreateDocument", `{"Name":"d","Content":"{}","DocumentType":"Automation"}`,
			map[string][]string{"ssm:DocumentType": {"Automation"}}},
		{"change request", "StartChangeRequestExecution", `{"DocumentName":"d","DocumentVersion":"3","AutoApprove":true,"Runbooks":[]}`,
			map[string][]string{"ssm:AutoApprove": {"true"}, "ssm:DocumentVersion": {"3"}}},
		{"automation", "StartAutomationExecution", `{"DocumentName":"d","DocumentVersion":"$LATEST"}`,
			map[string][]string{"ssm:DocumentVersion": {"$LATEST"}}},
		{"inventory", "PutInventory", `{"InstanceId":"i-1","Items":[
			{"TypeName":"Custom:RackInfo","SchemaVersion":"1.0","CaptureTime":"2026-01-01T00:00:00Z"},
			{"TypeName":"AWS:Application","SchemaVersion":"1.0","CaptureTime":"2026-01-01T00:00:00Z"}]}`,
			map[string][]string{"ssm:InventoryTypeName": {"Custom:RackInfo", "AWS:Application"}}},
		{"put parameter", "PutParameter", `{"Name":"p","Value":"v","Overwrite":false,"Policies":"[{\"Type\":\"Expiration\"}]"}`,
			map[string][]string{"ssm:Overwrite": {"false"}, "ssm:Policies": {`[{"Type":"Expiration"}]`}}},
		{"by path", "GetParametersByPath", `{"Path":"/app","Recursive":true}`,
			map[string][]string{"ssm:Recursive": {"true"}}},
		{"sync", "CreateResourceDataSync", `{"SyncName":"s","SyncType":"SyncFromSource"}`,
			map[string][]string{"ssm:SyncType": {"SyncFromSource"}}},
		{"list syncs", "ListResourceDataSync", `{"SyncType":"SyncToDestination"}`,
			map[string][]string{"ssm:SyncType": {"SyncToDestination"}}},
		{"absent members", "PutParameter", `{"Name":"p","Value":"v"}`, map[string][]string{}},
		{"undeclared action", "GetParameter", `{"Name":"p","Overwrite":true}`, map[string][]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("ssm", c.operation, c.body), c.want)
		})
	}
}
