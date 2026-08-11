package aws_cli_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmHexSuffix returns a 17-hex-char suffix derived from the current
// nanosecond clock, for building a length-valid managed-instance ID
// ("mi-" + 17 hex == 20 chars, which the aws CLI enforces).
func ssmHexSuffix() string {
	return fmt.Sprintf("%017x", uint64(time.Now().UnixNano()))[:17]
}

// TestSSMCLI_Inventory pins the inventory report/read-back surface via
// the aws CLI: put-inventory captures typed rows, list-inventory-entries
// and get-inventory read them back, get-inventory-schema enumerates the
// type, and delete-inventory + describe-inventory-deletions model the
// async deletion job.
func TestSSMCLI_Inventory(t *testing.T) {
	instanceID := ssmInstanceID("0cliinv")
	typeName := "Custom:SockerlessCLI"
	capture := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	runCLI(t, awsCLI("ssm", "put-inventory",
		"--instance-id", instanceID,
		"--items", `[{"TypeName":"`+typeName+`","SchemaVersion":"1.0","CaptureTime":"`+capture+`","Content":[{"Name":"alpha","Version":"1.0"}]}]`,
		"--output", "json"))

	leOut := runCLI(t, awsCLI("ssm", "list-inventory-entries",
		"--instance-id", instanceID,
		"--type-name", typeName,
		"--output", "json"))
	var le struct {
		TypeName   string              `json:"TypeName"`
		InstanceId string              `json:"InstanceId"`
		Entries    []map[string]string `json:"Entries"`
	}
	parseJSON(t, leOut, &le)
	require.Equal(t, instanceID, le.InstanceId)
	require.Equal(t, typeName, le.TypeName)
	require.Len(t, le.Entries, 1)
	assert.Equal(t, "alpha", le.Entries[0]["Name"])

	giOut := runCLI(t, awsCLI("ssm", "get-inventory", "--output", "json"))
	var gi struct {
		Entities []struct {
			Id string `json:"Id"`
		} `json:"Entities"`
	}
	parseJSON(t, giOut, &gi)
	found := false
	for _, e := range gi.Entities {
		if e.Id == instanceID {
			found = true
		}
	}
	assert.True(t, found, "get-inventory must include the reporting node")

	gsOut := runCLI(t, awsCLI("ssm", "get-inventory-schema", "--output", "json"))
	var gs struct {
		Schemas []struct {
			TypeName string `json:"TypeName"`
		} `json:"Schemas"`
	}
	parseJSON(t, gsOut, &gs)
	foundType := false
	for _, s := range gs.Schemas {
		if s.TypeName == typeName {
			foundType = true
		}
	}
	assert.True(t, foundType, "get-inventory-schema must include the custom type")

	diOut := runCLI(t, awsCLI("ssm", "delete-inventory",
		"--type-name", typeName,
		"--output", "json"))
	var di struct {
		DeletionId string `json:"DeletionId"`
	}
	parseJSON(t, diOut, &di)
	require.NotEmpty(t, di.DeletionId)

	ddOut := runCLI(t, awsCLI("ssm", "describe-inventory-deletions",
		"--deletion-id", di.DeletionId,
		"--output", "json"))
	var dd struct {
		InventoryDeletions []struct {
			DeletionId string `json:"DeletionId"`
			LastStatus string `json:"LastStatus"`
		} `json:"InventoryDeletions"`
	}
	parseJSON(t, ddOut, &dd)
	require.Len(t, dd.InventoryDeletions, 1)
	assert.Equal(t, di.DeletionId, dd.InventoryDeletions[0].DeletionId)
	assert.Equal(t, "Complete", dd.InventoryDeletions[0].LastStatus)
}

// TestSSMCLI_Compliance pins the compliance surface: put-compliance-items
// records items, list-compliance-items reads them, and the two summary
// ops roll up counts.
func TestSSMCLI_Compliance(t *testing.T) {
	resourceID := ssmInstanceID("0clicmp")
	complianceType := "Custom:SockerlessCLICheck"
	execTime := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	runCLI(t, awsCLI("ssm", "put-compliance-items",
		"--resource-id", resourceID,
		"--resource-type", "ManagedInstance",
		"--compliance-type", complianceType,
		"--execution-summary", `{"ExecutionTime":"`+execTime+`","ExecutionId":"exec-cli","ExecutionType":"Command"}`,
		"--items", `[{"Id":"rule-a","Title":"Rule A","Severity":"HIGH","Status":"COMPLIANT"},{"Id":"rule-b","Title":"Rule B","Severity":"CRITICAL","Status":"NON_COMPLIANT"}]`,
		"--output", "json"))

	liOut := runCLI(t, awsCLI("ssm", "list-compliance-items",
		"--resource-ids", resourceID,
		"--output", "json"))
	var li struct {
		ComplianceItems []struct {
			Id     string `json:"Id"`
			Status string `json:"Status"`
		} `json:"ComplianceItems"`
	}
	parseJSON(t, liOut, &li)
	require.Len(t, li.ComplianceItems, 2)

	lsOut := runCLI(t, awsCLI("ssm", "list-compliance-summaries", "--output", "json"))
	var ls struct {
		ComplianceSummaryItems []struct {
			ComplianceType   string `json:"ComplianceType"`
			CompliantSummary struct {
				CompliantCount int `json:"CompliantCount"`
			} `json:"CompliantSummary"`
			NonCompliantSummary struct {
				NonCompliantCount int `json:"NonCompliantCount"`
			} `json:"NonCompliantSummary"`
		} `json:"ComplianceSummaryItems"`
	}
	parseJSON(t, lsOut, &ls)
	foundSummary := false
	for _, s := range ls.ComplianceSummaryItems {
		if s.ComplianceType == complianceType {
			foundSummary = true
			assert.Equal(t, 1, s.CompliantSummary.CompliantCount)
			assert.Equal(t, 1, s.NonCompliantSummary.NonCompliantCount)
		}
	}
	assert.True(t, foundSummary)

	lrOut := runCLI(t, awsCLI("ssm", "list-resource-compliance-summaries", "--output", "json"))
	var lr struct {
		ResourceComplianceSummaryItems []struct {
			ResourceId      string `json:"ResourceId"`
			Status          string `json:"Status"`
			OverallSeverity string `json:"OverallSeverity"`
		} `json:"ResourceComplianceSummaryItems"`
	}
	parseJSON(t, lrOut, &lr)
	foundRes := false
	for _, s := range lr.ResourceComplianceSummaryItems {
		if s.ResourceId == resourceID {
			foundRes = true
			assert.Equal(t, "NON_COMPLIANT", s.Status)
			assert.Equal(t, "CRITICAL", s.OverallSeverity)
		}
	}
	assert.True(t, foundRes)
}

// TestSSMCLI_NodesAndInstances pins node enumeration (list-nodes /
// list-nodes-summary) and managed-instance information
// (describe-instance-information / describe-instance-properties) plus
// update-managed-instance-role and deregister-managed-instance over the
// same set a node joins by reporting inventory.
func TestSSMCLI_NodesAndInstances(t *testing.T) {
	// The CLI validates the managed-instance ID as "mi-" plus 17 hex
	// chars (total length 20), so build a deterministic 17-hex suffix
	// from the timestamp.
	instanceID := "mi-" + ssmHexSuffix()
	capture := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	runCLI(t, awsCLI("ssm", "put-inventory",
		"--instance-id", instanceID,
		"--items", `[{"TypeName":"AWS:InstanceInformation","SchemaVersion":"1.0","CaptureTime":"`+capture+`","Content":[{"PlatformName":"Ubuntu"}]}]`,
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "deregister-managed-instance", "--instance-id", instanceID).Run()
	})

	lnOut := runCLI(t, awsCLI("ssm", "list-nodes", "--output", "json"))
	var ln struct {
		Nodes []struct {
			Id string `json:"Id"`
		} `json:"Nodes"`
	}
	parseJSON(t, lnOut, &ln)
	foundNode := false
	for _, n := range ln.Nodes {
		if n.Id == instanceID {
			foundNode = true
		}
	}
	assert.True(t, foundNode, "list-nodes must include the reporting node")

	lnsOut := runCLI(t, awsCLI("ssm", "list-nodes-summary",
		"--aggregators", `[{"AggregatorType":"Count","TypeName":"Instance","AttributeName":"ResourceType"}]`,
		"--output", "json"))
	var lns struct {
		Summary []map[string]string `json:"Summary"`
	}
	parseJSON(t, lnsOut, &lns)
	assert.NotEmpty(t, lns.Summary)

	diiOut := runCLI(t, awsCLI("ssm", "describe-instance-information", "--output", "json"))
	var dii struct {
		InstanceInformationList []struct {
			InstanceId string `json:"InstanceId"`
			PingStatus string `json:"PingStatus"`
		} `json:"InstanceInformationList"`
	}
	parseJSON(t, diiOut, &dii)
	foundInfo := false
	for _, ii := range dii.InstanceInformationList {
		if ii.InstanceId == instanceID {
			foundInfo = true
			assert.Equal(t, "Online", ii.PingStatus)
		}
	}
	assert.True(t, foundInfo, "describe-instance-information must include the node")

	dipOut := runCLI(t, awsCLI("ssm", "describe-instance-properties", "--output", "json"))
	var dip struct {
		InstanceProperties []struct {
			InstanceId string `json:"InstanceId"`
		} `json:"InstanceProperties"`
	}
	parseJSON(t, dipOut, &dip)
	foundProp := false
	for _, p := range dip.InstanceProperties {
		if p.InstanceId == instanceID {
			foundProp = true
		}
	}
	assert.True(t, foundProp, "describe-instance-properties must include the node")

	runCLI(t, awsCLI("ssm", "update-managed-instance-role",
		"--instance-id", instanceID,
		"--iam-role", "SockerlessCLIRole",
		"--output", "json"))
	dii2Out := runCLI(t, awsCLI("ssm", "describe-instance-information", "--output", "json"))
	var dii2 struct {
		InstanceInformationList []struct {
			InstanceId string `json:"InstanceId"`
			IamRole    string `json:"IamRole"`
		} `json:"InstanceInformationList"`
	}
	parseJSON(t, dii2Out, &dii2)
	for _, ii := range dii2.InstanceInformationList {
		if ii.InstanceId == instanceID {
			assert.Equal(t, "SockerlessCLIRole", ii.IamRole)
		}
	}

	runCLI(t, awsCLI("ssm", "deregister-managed-instance", "--instance-id", instanceID, "--output", "json"))
}

// TestSSMCLI_DocumentPermission pins the account-share list plus the
// review-metadata ops and Change-Calendar state over the document store.
func TestSSMCLI_DocumentPermission(t *testing.T) {
	name := "cli-perm-doc-" + ssmStamp()
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`

	runCLI(t, awsCLI("ssm", "create-document",
		"--name", name,
		"--content", content,
		"--document-type", "Command",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-document", "--name", name).Run()
	})

	runCLI(t, awsCLI("ssm", "modify-document-permission",
		"--name", name,
		"--permission-type", "Share",
		"--account-ids-to-add", "111111111111", "222222222222",
		"--output", "json"))

	dpOut := runCLI(t, awsCLI("ssm", "describe-document-permission",
		"--name", name,
		"--permission-type", "Share",
		"--output", "json"))
	var dp struct {
		AccountIds             []string `json:"AccountIds"`
		AccountSharingInfoList []struct {
			AccountId string `json:"AccountId"`
		} `json:"AccountSharingInfoList"`
	}
	parseJSON(t, dpOut, &dp)
	require.Len(t, dp.AccountIds, 2)
	assert.Contains(t, dp.AccountIds, "111111111111")
	require.Len(t, dp.AccountSharingInfoList, 2)

	// Remove one account.
	runCLI(t, awsCLI("ssm", "modify-document-permission",
		"--name", name,
		"--permission-type", "Share",
		"--account-ids-to-remove", "111111111111",
		"--output", "json"))
	dp2Out := runCLI(t, awsCLI("ssm", "describe-document-permission",
		"--name", name,
		"--permission-type", "Share",
		"--output", "json"))
	var dp2 struct {
		AccountIds []string `json:"AccountIds"`
	}
	parseJSON(t, dp2Out, &dp2)
	require.Len(t, dp2.AccountIds, 1)
	assert.Equal(t, "222222222222", dp2.AccountIds[0])

	// Update metadata (send for review) then read history.
	runCLI(t, awsCLI("ssm", "update-document-metadata",
		"--name", name,
		"--document-version", "1",
		"--document-reviews", `{"Action":"SendForReview"}`,
		"--output", "json"))
	lhOut := runCLI(t, awsCLI("ssm", "list-document-metadata-history",
		"--name", name,
		"--metadata", "DocumentReviews",
		"--output", "json"))
	var lh struct {
		Name     string `json:"Name"`
		Metadata struct {
			ReviewerResponse []struct {
				ReviewStatus string `json:"ReviewStatus"`
			} `json:"ReviewerResponse"`
		} `json:"Metadata"`
	}
	parseJSON(t, lhOut, &lh)
	require.Equal(t, name, lh.Name)
	require.Len(t, lh.Metadata.ReviewerResponse, 1)
	assert.Equal(t, "PENDING", lh.Metadata.ReviewerResponse[0].ReviewStatus)
}

// TestSSMCLI_CalendarState pins get-calendar-state reading a
// Change-Calendar document's default state.
func TestSSMCLI_CalendarState(t *testing.T) {
	name := "cli-cal-" + ssmStamp()
	content := "BEGIN:VCALENDAR\nPRODID:-//AWS//Change Calendar 1.0//EN\nVERSION:2.0\nX-CALENDAR-TYPE:DEFAULT-OPEN\nEND:VCALENDAR\n"
	runCLI(t, awsCLI("ssm", "create-document",
		"--name", name,
		"--content", content,
		"--document-type", "ChangeCalendar",
		"--document-format", "TEXT",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-document", "--name", name).Run()
	})

	gsOut := runCLI(t, awsCLI("ssm", "get-calendar-state",
		"--calendar-names", name,
		"--output", "json"))
	var gs struct {
		State  string `json:"State"`
		AtTime string `json:"AtTime"`
	}
	parseJSON(t, gsOut, &gs)
	assert.Equal(t, "OPEN", gs.State)
	assert.NotEmpty(t, gs.AtTime)
}

// Note: the just-in-time node-access ops start-access-request and
// get-access-token are not present in aws CLI 2.26.6 (added in a later
// release), so they are exercised SDK-only (TestSSM_AccessRequest); the
// SDK path covers the simulator-contract hook for both.
