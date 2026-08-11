package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSM_Inventory pins the inventory report/read-back surface:
// PutInventory captures a typed item list for a node; GetInventory and
// ListInventoryEntries read it back; GetInventorySchema enumerates the
// known item types; DeleteInventory + DescribeInventoryDeletions model
// the async schema-deletion job. The reporting node also shows up as a
// managed instance (asserted in TestSSM_InstanceInformation).
func TestSSM_Inventory(t *testing.T) {
	c := ssmClient()
	instanceID := "i-0inventory0sdk001"
	typeName := "Custom:SockerlessApp"
	capture := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	_, err := c.PutInventory(ctx, &ssm.PutInventoryInput{
		InstanceId: aws.String(instanceID),
		Items: []ssmtypes.InventoryItem{
			{
				TypeName:      aws.String(typeName),
				SchemaVersion: aws.String("1.0"),
				CaptureTime:   aws.String(capture),
				Content: []map[string]string{
					{"Name": "alpha", "Version": "1.2.3"},
					{"Name": "beta", "Version": "4.5.6"},
				},
			},
		},
	})
	require.NoError(t, err)

	// ListInventoryEntries reads the typed rows back for (node, type).
	le, err := c.ListInventoryEntries(ctx, &ssm.ListInventoryEntriesInput{
		InstanceId: aws.String(instanceID),
		TypeName:   aws.String(typeName),
	})
	require.NoError(t, err)
	assert.Equal(t, instanceID, *le.InstanceId)
	assert.Equal(t, typeName, *le.TypeName)
	require.Len(t, le.Entries, 2)
	assert.Equal(t, "alpha", le.Entries[0]["Name"])

	// GetInventory groups the captured entries under the node entity.
	gi, err := c.GetInventory(ctx, &ssm.GetInventoryInput{})
	require.NoError(t, err)
	foundEntity := false
	for _, e := range gi.Entities {
		if e.Id != nil && *e.Id == instanceID {
			foundEntity = true
			item, ok := e.Data[typeName]
			require.True(t, ok, "entity data must carry the reported type")
			assert.Equal(t, typeName, *item.TypeName)
			require.Len(t, item.Content, 2)
		}
	}
	assert.True(t, foundEntity, "GetInventory must include the reporting node")

	// GetInventorySchema enumerates the type we reported plus the
	// AWS-managed core types.
	gs, err := c.GetInventorySchema(ctx, &ssm.GetInventorySchemaInput{})
	require.NoError(t, err)
	foundType := false
	for _, s := range gs.Schemas {
		if s.TypeName != nil && *s.TypeName == typeName {
			foundType = true
			assert.NotEmpty(t, s.Attributes)
		}
	}
	assert.True(t, foundType, "GetInventorySchema must include the custom type")

	// DeleteInventory removes the type's data and records a deletion job.
	di, err := c.DeleteInventory(ctx, &ssm.DeleteInventoryInput{
		TypeName: aws.String(typeName),
	})
	require.NoError(t, err)
	require.NotNil(t, di.DeletionId)
	deletionID := *di.DeletionId

	dd, err := c.DescribeInventoryDeletions(ctx, &ssm.DescribeInventoryDeletionsInput{
		DeletionId: aws.String(deletionID),
	})
	require.NoError(t, err)
	require.Len(t, dd.InventoryDeletions, 1)
	assert.Equal(t, deletionID, *dd.InventoryDeletions[0].DeletionId)
	assert.Equal(t, ssmtypes.InventoryDeletionStatusComplete, dd.InventoryDeletions[0].LastStatus)

	// Entries for the deleted type are gone.
	le2, err := c.ListInventoryEntries(ctx, &ssm.ListInventoryEntriesInput{
		InstanceId: aws.String(instanceID),
		TypeName:   aws.String(typeName),
	})
	require.NoError(t, err)
	assert.Empty(t, le2.Entries)
}

// TestSSM_Compliance pins the compliance report/read-back surface:
// PutComplianceItems records items per (resource, compliance-type),
// ListComplianceItems reads them, and the two summary ops roll up
// compliant/non-compliant counts.
func TestSSM_Compliance(t *testing.T) {
	c := ssmClient()
	resourceID := "i-0compliance0sdk01"
	complianceType := "Custom:SockerlessCheck"
	execTime := time.Now()

	_, err := c.PutComplianceItems(ctx, &ssm.PutComplianceItemsInput{
		ResourceId:     aws.String(resourceID),
		ResourceType:   aws.String("ManagedInstance"),
		ComplianceType: aws.String(complianceType),
		ExecutionSummary: &ssmtypes.ComplianceExecutionSummary{
			ExecutionTime: aws.Time(execTime),
			ExecutionId:   aws.String("exec-001"),
			ExecutionType: aws.String("Command"),
		},
		Items: []ssmtypes.ComplianceItemEntry{
			{
				Id:       aws.String("rule-1"),
				Title:    aws.String("Rule one"),
				Severity: ssmtypes.ComplianceSeverityHigh,
				Status:   ssmtypes.ComplianceStatusCompliant,
			},
			{
				Id:       aws.String("rule-2"),
				Title:    aws.String("Rule two"),
				Severity: ssmtypes.ComplianceSeverityCritical,
				Status:   ssmtypes.ComplianceStatusNonCompliant,
			},
		},
	})
	require.NoError(t, err)

	// ListComplianceItems returns both recorded items for the resource.
	li, err := c.ListComplianceItems(ctx, &ssm.ListComplianceItemsInput{
		ResourceIds: []string{resourceID},
	})
	require.NoError(t, err)
	require.Len(t, li.ComplianceItems, 2)
	byID := map[string]ssmtypes.ComplianceItem{}
	for _, it := range li.ComplianceItems {
		byID[*it.Id] = it
	}
	require.Contains(t, byID, "rule-2")
	assert.Equal(t, ssmtypes.ComplianceStatusNonCompliant, byID["rule-2"].Status)
	assert.Equal(t, ssmtypes.ComplianceSeverityCritical, byID["rule-2"].Severity)
	require.NotNil(t, byID["rule-2"].ExecutionSummary)
	assert.Equal(t, "exec-001", *byID["rule-2"].ExecutionSummary.ExecutionId)

	// ListComplianceSummaries rolls up by compliance type.
	ls, err := c.ListComplianceSummaries(ctx, &ssm.ListComplianceSummariesInput{})
	require.NoError(t, err)
	foundSummary := false
	for _, s := range ls.ComplianceSummaryItems {
		if s.ComplianceType != nil && *s.ComplianceType == complianceType {
			foundSummary = true
			require.NotNil(t, s.CompliantSummary)
			require.NotNil(t, s.NonCompliantSummary)
			assert.Equal(t, int32(1), s.CompliantSummary.CompliantCount)
			assert.Equal(t, int32(1), s.NonCompliantSummary.NonCompliantCount)
			require.NotNil(t, s.NonCompliantSummary.SeveritySummary)
			assert.Equal(t, int32(1), s.NonCompliantSummary.SeveritySummary.CriticalCount)
		}
	}
	assert.True(t, foundSummary, "ListComplianceSummaries must include the type")

	// ListResourceComplianceSummaries rolls up per resource.
	lr, err := c.ListResourceComplianceSummaries(ctx, &ssm.ListResourceComplianceSummariesInput{})
	require.NoError(t, err)
	foundRes := false
	for _, s := range lr.ResourceComplianceSummaryItems {
		if s.ResourceId != nil && *s.ResourceId == resourceID && *s.ComplianceType == complianceType {
			foundRes = true
			assert.Equal(t, ssmtypes.ComplianceStatusNonCompliant, s.Status)
			assert.Equal(t, ssmtypes.ComplianceSeverityCritical, s.OverallSeverity)
		}
	}
	assert.True(t, foundRes, "ListResourceComplianceSummaries must include the resource")
}

// TestSSM_Nodes pins the node-enumeration surface (ListNodes /
// ListNodesSummary) over the same managed-instance set a node joins when
// it reports inventory.
func TestSSM_Nodes(t *testing.T) {
	c := ssmClient()
	instanceID := "i-0nodes0sdk0000001"

	_, err := c.PutInventory(ctx, &ssm.PutInventoryInput{
		InstanceId: aws.String(instanceID),
		Items: []ssmtypes.InventoryItem{
			{
				TypeName:      aws.String("AWS:InstanceInformation"),
				SchemaVersion: aws.String("1.0"),
				CaptureTime:   aws.String(time.Now().UTC().Format("2006-01-02T15:04:05Z")),
				Content:       []map[string]string{{"PlatformName": "Ubuntu"}},
			},
		},
	})
	require.NoError(t, err)

	ln, err := c.ListNodes(ctx, &ssm.ListNodesInput{})
	require.NoError(t, err)
	found := false
	for _, n := range ln.Nodes {
		if n.Id != nil && *n.Id == instanceID {
			found = true
			inst, ok := n.NodeType.(*ssmtypes.NodeTypeMemberInstance)
			require.True(t, ok, "node type must be the Instance member")
			assert.Equal(t, "amazon-ssm-agent", *inst.Value.AgentType)
		}
	}
	assert.True(t, found, "ListNodes must include the reporting node")

	lns, err := c.ListNodesSummary(ctx, &ssm.ListNodesSummaryInput{
		Aggregators: []ssmtypes.NodeAggregator{
			{
				AggregatorType: ssmtypes.NodeAggregatorTypeCount,
				TypeName:       ssmtypes.NodeTypeNameInstance,
				AttributeName:  ssmtypes.NodeAttributeNameResourceType,
			},
		},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, lns.Summary, "ListNodesSummary must aggregate at least one node")
}

// TestSSM_InstanceInformation pins managed-instance enumeration
// (DescribeInstanceInformation / DescribeInstanceProperties) plus
// UpdateManagedInstanceRole and DeregisterManagedInstance.
func TestSSM_InstanceInformation(t *testing.T) {
	c := ssmClient()
	instanceID := "mi-0instinfo0sdk0001"

	_, err := c.PutInventory(ctx, &ssm.PutInventoryInput{
		InstanceId: aws.String(instanceID),
		Items: []ssmtypes.InventoryItem{
			{
				TypeName:      aws.String("AWS:InstanceInformation"),
				SchemaVersion: aws.String("1.0"),
				CaptureTime:   aws.String(time.Now().UTC().Format("2006-01-02T15:04:05Z")),
				Content:       []map[string]string{{"PlatformName": "Ubuntu"}},
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeregisterManagedInstance(ctx, &ssm.DeregisterManagedInstanceInput{
			InstanceId: aws.String(instanceID),
		})
	})

	dii, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
	require.NoError(t, err)
	var info *ssmtypes.InstanceInformation
	for i := range dii.InstanceInformationList {
		if *dii.InstanceInformationList[i].InstanceId == instanceID {
			info = &dii.InstanceInformationList[i]
		}
	}
	require.NotNil(t, info, "DescribeInstanceInformation must include the node")
	assert.Equal(t, ssmtypes.PingStatusOnline, info.PingStatus)
	assert.Equal(t, ssmtypes.ResourceTypeManagedInstance, info.ResourceType)
	require.NotNil(t, info.IsLatestVersion)

	dip, err := c.DescribeInstanceProperties(ctx, &ssm.DescribeInstancePropertiesInput{})
	require.NoError(t, err)
	foundProp := false
	for _, p := range dip.InstanceProperties {
		if p.InstanceId != nil && *p.InstanceId == instanceID {
			foundProp = true
			assert.Equal(t, "3.3.0.0", *p.AgentVersion)
		}
	}
	assert.True(t, foundProp, "DescribeInstanceProperties must include the node")

	// UpdateManagedInstanceRole then re-read.
	_, err = c.UpdateManagedInstanceRole(ctx, &ssm.UpdateManagedInstanceRoleInput{
		InstanceId: aws.String(instanceID),
		IamRole:    aws.String("SockerlessManagedRole"),
	})
	require.NoError(t, err)
	dii2, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
	require.NoError(t, err)
	for _, ii := range dii2.InstanceInformationList {
		if *ii.InstanceId == instanceID {
			assert.Equal(t, "SockerlessManagedRole", *ii.IamRole)
		}
	}

	// Deregister and confirm gone.
	_, err = c.DeregisterManagedInstance(ctx, &ssm.DeregisterManagedInstanceInput{
		InstanceId: aws.String(instanceID),
	})
	require.NoError(t, err)
	dii3, err := c.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{})
	require.NoError(t, err)
	for _, ii := range dii3.InstanceInformationList {
		assert.NotEqual(t, instanceID, *ii.InstanceId)
	}
}

// TestSSM_DocumentPermission pins the account-share list plus the review
// metadata ops layered over an existing document.
func TestSSM_DocumentPermission(t *testing.T) {
	c := ssmClient()
	name := "sockerless-perm-doc-sdk"
	content := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`

	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:         aws.String(name),
		Content:      aws.String(content),
		DocumentType: ssmtypes.DocumentTypeCommand,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(name)})
	})

	// Initially no shares.
	dp0, err := c.DescribeDocumentPermission(ctx, &ssm.DescribeDocumentPermissionInput{
		Name:           aws.String(name),
		PermissionType: ssmtypes.DocumentPermissionTypeShare,
	})
	require.NoError(t, err)
	assert.Empty(t, dp0.AccountIds)

	// Share with two accounts.
	_, err = c.ModifyDocumentPermission(ctx, &ssm.ModifyDocumentPermissionInput{
		Name:            aws.String(name),
		PermissionType:  ssmtypes.DocumentPermissionTypeShare,
		AccountIdsToAdd: []string{"111111111111", "222222222222"},
	})
	require.NoError(t, err)

	dp1, err := c.DescribeDocumentPermission(ctx, &ssm.DescribeDocumentPermissionInput{
		Name:           aws.String(name),
		PermissionType: ssmtypes.DocumentPermissionTypeShare,
	})
	require.NoError(t, err)
	require.Len(t, dp1.AccountIds, 2)
	assert.Contains(t, dp1.AccountIds, "111111111111")
	require.Len(t, dp1.AccountSharingInfoList, 2)

	// Remove one.
	_, err = c.ModifyDocumentPermission(ctx, &ssm.ModifyDocumentPermissionInput{
		Name:               aws.String(name),
		PermissionType:     ssmtypes.DocumentPermissionTypeShare,
		AccountIdsToRemove: []string{"111111111111"},
	})
	require.NoError(t, err)
	dp2, err := c.DescribeDocumentPermission(ctx, &ssm.DescribeDocumentPermissionInput{
		Name:           aws.String(name),
		PermissionType: ssmtypes.DocumentPermissionTypeShare,
	})
	require.NoError(t, err)
	require.Len(t, dp2.AccountIds, 1)
	assert.Equal(t, "222222222222", dp2.AccountIds[0])

	// Update metadata (send review request) then read history back.
	_, err = c.UpdateDocumentMetadata(ctx, &ssm.UpdateDocumentMetadataInput{
		Name:            aws.String(name),
		DocumentVersion: aws.String("1"),
		DocumentReviews: &ssmtypes.DocumentReviews{
			Action: ssmtypes.DocumentReviewActionSendForReview,
		},
	})
	require.NoError(t, err)
	lh, err := c.ListDocumentMetadataHistory(ctx, &ssm.ListDocumentMetadataHistoryInput{
		Name:     aws.String(name),
		Metadata: ssmtypes.DocumentMetadataEnumDocumentReviews,
	})
	require.NoError(t, err)
	assert.Equal(t, name, *lh.Name)
	require.NotNil(t, lh.Metadata)
	require.Len(t, lh.Metadata.ReviewerResponse, 1)
	assert.Equal(t, ssmtypes.ReviewStatusPending, lh.Metadata.ReviewerResponse[0].ReviewStatus)
}

// TestSSM_CalendarState pins GetCalendarState reading a Change-Calendar
// document's default state.
func TestSSM_CalendarState(t *testing.T) {
	c := ssmClient()

	// An open-by-default calendar.
	openName := "sockerless-cal-open-sdk"
	openContent := "BEGIN:VCALENDAR\nPRODID:-//AWS//Change Calendar 1.0//EN\nVERSION:2.0\nX-CALENDAR-TYPE:DEFAULT-OPEN\nEND:VCALENDAR\n"
	_, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(openName),
		Content:        aws.String(openContent),
		DocumentType:   ssmtypes.DocumentTypeChangeCalendar,
		DocumentFormat: ssmtypes.DocumentFormatText,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(openName)}) })

	gs, err := c.GetCalendarState(ctx, &ssm.GetCalendarStateInput{
		CalendarNames: []string{openName},
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.CalendarStateOpen, gs.State)
	assert.NotEmpty(t, *gs.AtTime)

	// A closed-by-default calendar.
	closedName := "sockerless-cal-closed-sdk"
	closedContent := "BEGIN:VCALENDAR\nPRODID:-//AWS//Change Calendar 1.0//EN\nVERSION:2.0\nX-CALENDAR-TYPE:DEFAULT-CLOSED\nEND:VCALENDAR\n"
	_, err = c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(closedName),
		Content:        aws.String(closedContent),
		DocumentType:   ssmtypes.DocumentTypeChangeCalendar,
		DocumentFormat: ssmtypes.DocumentFormatText,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(closedName)}) })

	gs2, err := c.GetCalendarState(ctx, &ssm.GetCalendarStateInput{
		CalendarNames: []string{closedName},
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.CalendarStateClosed, gs2.State)
}

// TestSSM_AccessRequest pins the just-in-time node-access flow:
// StartAccessRequest issues a request id, GetAccessToken returns the
// short-lived credentials for an approved request.
func TestSSM_AccessRequest(t *testing.T) {
	c := ssmClient()

	sr, err := c.StartAccessRequest(ctx, &ssm.StartAccessRequestInput{
		Reason: aws.String("sockerless node access test"),
		Targets: []ssmtypes.Target{
			{Key: aws.String("InstanceIds"), Values: []string{"i-0access0sdk000001"}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, sr.AccessRequestId)
	assert.True(t, strings.HasPrefix(*sr.AccessRequestId, "oi-"))

	gt, err := c.GetAccessToken(ctx, &ssm.GetAccessTokenInput{
		AccessRequestId: sr.AccessRequestId,
	})
	require.NoError(t, err)
	assert.Equal(t, ssmtypes.AccessRequestStatusApproved, gt.AccessRequestStatus)
	require.NotNil(t, gt.Credentials)
	assert.NotEmpty(t, *gt.Credentials.AccessKeyId)
	assert.NotEmpty(t, *gt.Credentials.SecretAccessKey)
	assert.NotEmpty(t, *gt.Credentials.SessionToken)
}
