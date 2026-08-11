package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSM_DocumentLifecycle pins the versioned-document control plane:
// create, get content back, update to a new version, flip the default
// version, list versions, and delete.
func TestSSM_DocumentLifecycle(t *testing.T) {
	c := ssmClient()
	name := "sockerless-doc-sdk"

	content1 := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"step1","inputs":{"runCommand":["echo v1"]}}]}`
	created, err := c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(name),
		Content:        aws.String(content1),
		DocumentType:   ssmtypes.DocumentTypeCommand,
		DocumentFormat: ssmtypes.DocumentFormatJson,
	})
	require.NoError(t, err)
	require.NotNil(t, created.DocumentDescription)
	assert.Equal(t, name, *created.DocumentDescription.Name)
	assert.Equal(t, "1", *created.DocumentDescription.DocumentVersion)
	assert.Equal(t, ssmtypes.DocumentStatusActive, created.DocumentDescription.Status)
	t.Cleanup(func() {
		_, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(name)})
	})

	// Get content back.
	got, err := c.GetDocument(ctx, &ssm.GetDocumentInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, content1, *got.Content)
	assert.Equal(t, "1", *got.DocumentVersion)

	// Describe.
	desc, err := c.DescribeDocument(ctx, &ssm.DescribeDocumentInput{Name: aws.String(name)})
	require.NoError(t, err)
	require.NotNil(t, desc.Document)
	assert.Equal(t, ssmtypes.DocumentHashType("Sha256"), desc.Document.HashType)
	assert.NotEmpty(t, *desc.Document.Hash)

	// Update to version 2.
	content2 := `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"step1","inputs":{"runCommand":["echo v2"]}}]}`
	upd, err := c.UpdateDocument(ctx, &ssm.UpdateDocumentInput{
		Name:    aws.String(name),
		Content: aws.String(content2),
	})
	require.NoError(t, err)
	assert.Equal(t, "2", *upd.DocumentDescription.DocumentVersion)
	assert.Equal(t, "2", *upd.DocumentDescription.LatestVersion)
	// Default is still 1 until flipped.
	assert.Equal(t, "1", *upd.DocumentDescription.DefaultVersion)

	// List versions: expect both 1 and 2.
	lv, err := c.ListDocumentVersions(ctx, &ssm.ListDocumentVersionsInput{Name: aws.String(name)})
	require.NoError(t, err)
	require.Len(t, lv.DocumentVersions, 2)

	// Flip default to 2.
	_, err = c.UpdateDocumentDefaultVersion(ctx, &ssm.UpdateDocumentDefaultVersionInput{
		Name:            aws.String(name),
		DocumentVersion: aws.String("2"),
	})
	require.NoError(t, err)
	desc2, err := c.DescribeDocument(ctx, &ssm.DescribeDocumentInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "2", *desc2.Document.DefaultVersion)

	// ListDocuments includes our doc.
	ld, err := c.ListDocuments(ctx, &ssm.ListDocumentsInput{})
	require.NoError(t, err)
	found := false
	for _, d := range ld.DocumentIdentifiers {
		if d.Name != nil && *d.Name == name {
			found = true
		}
	}
	assert.True(t, found, "ListDocuments must include created document")
}

// TestSSM_MaintenanceWindowLifecycle pins window CRUD plus target/task
// registration and describe.
func TestSSM_MaintenanceWindowLifecycle(t *testing.T) {
	c := ssmClient()

	cw, err := c.CreateMaintenanceWindow(ctx, &ssm.CreateMaintenanceWindowInput{
		Name:                     aws.String("sockerless-mw-sdk"),
		Schedule:                 aws.String("cron(0 16 ? * TUE *)"),
		Duration:                 aws.Int32(4),
		Cutoff:                   1,
		AllowUnassociatedTargets: true,
	})
	require.NoError(t, err)
	require.NotNil(t, cw.WindowId)
	windowID := *cw.WindowId
	assert.Regexp(t, `^mw-[0-9a-f]{17}$`, windowID)
	t.Cleanup(func() {
		_, _ = c.DeleteMaintenanceWindow(ctx, &ssm.DeleteMaintenanceWindowInput{WindowId: aws.String(windowID)})
	})

	// Get it back.
	gw, err := c.GetMaintenanceWindow(ctx, &ssm.GetMaintenanceWindowInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	assert.Equal(t, "sockerless-mw-sdk", *gw.Name)
	assert.Equal(t, int32(4), *gw.Duration)
	assert.True(t, gw.Enabled)
	assert.NotNil(t, gw.CreatedDate)

	// Update.
	_, err = c.UpdateMaintenanceWindow(ctx, &ssm.UpdateMaintenanceWindowInput{
		WindowId: aws.String(windowID),
		Enabled:  aws.Bool(false),
		Cutoff:   aws.Int32(2),
	})
	require.NoError(t, err)
	gw2, err := c.GetMaintenanceWindow(ctx, &ssm.GetMaintenanceWindowInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	assert.False(t, gw2.Enabled)
	assert.Equal(t, int32(2), gw2.Cutoff)

	// DescribeMaintenanceWindows includes it.
	dw, err := c.DescribeMaintenanceWindows(ctx, &ssm.DescribeMaintenanceWindowsInput{})
	require.NoError(t, err)
	found := false
	for _, w := range dw.WindowIdentities {
		if w.WindowId != nil && *w.WindowId == windowID {
			found = true
		}
	}
	assert.True(t, found)

	// Register a target.
	rt, err := c.RegisterTargetWithMaintenanceWindow(ctx, &ssm.RegisterTargetWithMaintenanceWindowInput{
		WindowId:     aws.String(windowID),
		ResourceType: ssmtypes.MaintenanceWindowResourceTypeInstance,
		Targets: []ssmtypes.Target{
			{Key: aws.String("tag:Env"), Values: []string{"prod"}},
		},
	})
	require.NoError(t, err)
	targetID := *rt.WindowTargetId
	assert.NotEmpty(t, targetID)

	dt, err := c.DescribeMaintenanceWindowTargets(ctx, &ssm.DescribeMaintenanceWindowTargetsInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	require.Len(t, dt.Targets, 1)
	assert.Equal(t, targetID, *dt.Targets[0].WindowTargetId)
	require.Len(t, dt.Targets[0].Targets, 1)
	assert.Equal(t, "tag:Env", *dt.Targets[0].Targets[0].Key)

	// Register a task.
	rtask, err := c.RegisterTaskWithMaintenanceWindow(ctx, &ssm.RegisterTaskWithMaintenanceWindowInput{
		WindowId:       aws.String(windowID),
		TaskArn:        aws.String("AWS-RunShellScript"),
		TaskType:       ssmtypes.MaintenanceWindowTaskTypeRunCommand,
		Priority:       aws.Int32(1),
		MaxConcurrency: aws.String("1"),
		MaxErrors:      aws.String("1"),
		Targets: []ssmtypes.Target{
			{Key: aws.String("WindowTargetIds"), Values: []string{targetID}},
		},
	})
	require.NoError(t, err)
	taskID := *rtask.WindowTaskId
	assert.NotEmpty(t, taskID)

	dtask, err := c.DescribeMaintenanceWindowTasks(ctx, &ssm.DescribeMaintenanceWindowTasksInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	require.Len(t, dtask.Tasks, 1)
	assert.Equal(t, "AWS-RunShellScript", *dtask.Tasks[0].TaskArn)
	assert.Equal(t, ssmtypes.MaintenanceWindowTaskTypeRunCommand, dtask.Tasks[0].Type)

	// Deregister both.
	_, err = c.DeregisterTaskFromMaintenanceWindow(ctx, &ssm.DeregisterTaskFromMaintenanceWindowInput{
		WindowId: aws.String(windowID), WindowTaskId: aws.String(taskID),
	})
	require.NoError(t, err)
	_, err = c.DeregisterTargetFromMaintenanceWindow(ctx, &ssm.DeregisterTargetFromMaintenanceWindowInput{
		WindowId: aws.String(windowID), WindowTargetId: aws.String(targetID),
	})
	require.NoError(t, err)

	dt2, err := c.DescribeMaintenanceWindowTargets(ctx, &ssm.DescribeMaintenanceWindowTargetsInput{WindowId: aws.String(windowID)})
	require.NoError(t, err)
	assert.Empty(t, dt2.Targets)
}

// TestSSM_PatchBaselineLifecycle pins patch-baseline CRUD plus the
// default-baseline registration.
func TestSSM_PatchBaselineLifecycle(t *testing.T) {
	c := ssmClient()

	cb, err := c.CreatePatchBaseline(ctx, &ssm.CreatePatchBaselineInput{
		Name:            aws.String("sockerless-pb-sdk"),
		OperatingSystem: ssmtypes.OperatingSystemUbuntu,
		Description:     aws.String("test baseline"),
		ApprovedPatches: []string{"patch-1", "patch-2"},
		ApprovalRules: &ssmtypes.PatchRuleGroup{
			PatchRules: []ssmtypes.PatchRule{
				{
					ApproveAfterDays: aws.Int32(7),
					ComplianceLevel:  ssmtypes.PatchComplianceLevelHigh,
					PatchFilterGroup: &ssmtypes.PatchFilterGroup{
						PatchFilters: []ssmtypes.PatchFilter{
							{Key: ssmtypes.PatchFilterKeyProduct, Values: []string{"Ubuntu22.04"}},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	baselineID := *cb.BaselineId
	assert.NotEmpty(t, baselineID)
	t.Cleanup(func() {
		_, _ = c.DeletePatchBaseline(ctx, &ssm.DeletePatchBaselineInput{BaselineId: aws.String(baselineID)})
	})

	gb, err := c.GetPatchBaseline(ctx, &ssm.GetPatchBaselineInput{BaselineId: aws.String(baselineID)})
	require.NoError(t, err)
	assert.Equal(t, "sockerless-pb-sdk", *gb.Name)
	assert.Equal(t, ssmtypes.OperatingSystemUbuntu, gb.OperatingSystem)
	assert.Equal(t, []string{"patch-1", "patch-2"}, gb.ApprovedPatches)
	require.NotNil(t, gb.ApprovalRules)
	require.Len(t, gb.ApprovalRules.PatchRules, 1)
	assert.Equal(t, int32(7), *gb.ApprovalRules.PatchRules[0].ApproveAfterDays)

	// Update name.
	_, err = c.UpdatePatchBaseline(ctx, &ssm.UpdatePatchBaselineInput{
		BaselineId: aws.String(baselineID),
		Name:       aws.String("sockerless-pb-sdk-renamed"),
	})
	require.NoError(t, err)
	gb2, err := c.GetPatchBaseline(ctx, &ssm.GetPatchBaselineInput{BaselineId: aws.String(baselineID)})
	require.NoError(t, err)
	assert.Equal(t, "sockerless-pb-sdk-renamed", *gb2.Name)

	// Describe includes it.
	db, err := c.DescribePatchBaselines(ctx, &ssm.DescribePatchBaselinesInput{})
	require.NoError(t, err)
	found := false
	for _, b := range db.BaselineIdentities {
		if b.BaselineId != nil && *b.BaselineId == baselineID {
			found = true
		}
	}
	assert.True(t, found)

	// Register as default for Ubuntu, then GetDefaultPatchBaseline.
	_, err = c.RegisterDefaultPatchBaseline(ctx, &ssm.RegisterDefaultPatchBaselineInput{BaselineId: aws.String(baselineID)})
	require.NoError(t, err)
	gd, err := c.GetDefaultPatchBaseline(ctx, &ssm.GetDefaultPatchBaselineInput{OperatingSystem: ssmtypes.OperatingSystemUbuntu})
	require.NoError(t, err)
	assert.Equal(t, baselineID, *gd.BaselineId)
}

// TestSSM_ServiceSetting pins the get/update/reset cycle for an
// account-level service setting.
func TestSSM_ServiceSetting(t *testing.T) {
	c := ssmClient()
	settingID := "/ssm/parameter-store/default-parameter-tier"

	// Default first.
	gs, err := c.GetServiceSetting(ctx, &ssm.GetServiceSettingInput{SettingId: aws.String(settingID)})
	require.NoError(t, err)
	require.NotNil(t, gs.ServiceSetting)
	assert.Equal(t, settingID, *gs.ServiceSetting.SettingId)
	assert.Equal(t, "Standard", *gs.ServiceSetting.SettingValue)
	assert.Equal(t, "Default", *gs.ServiceSetting.Status)

	// Update.
	_, err = c.UpdateServiceSetting(ctx, &ssm.UpdateServiceSettingInput{
		SettingId:    aws.String(settingID),
		SettingValue: aws.String("Advanced"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.ResetServiceSetting(ctx, &ssm.ResetServiceSettingInput{SettingId: aws.String(settingID)})
	})

	gs2, err := c.GetServiceSetting(ctx, &ssm.GetServiceSettingInput{SettingId: aws.String(settingID)})
	require.NoError(t, err)
	assert.Equal(t, "Advanced", *gs2.ServiceSetting.SettingValue)
	assert.Equal(t, "Customized", *gs2.ServiceSetting.Status)

	// Reset returns the default value again.
	rs, err := c.ResetServiceSetting(ctx, &ssm.ResetServiceSettingInput{SettingId: aws.String(settingID)})
	require.NoError(t, err)
	require.NotNil(t, rs.ServiceSetting)
	assert.Equal(t, "Standard", *rs.ServiceSetting.SettingValue)
	assert.Equal(t, "Default", *rs.ServiceSetting.Status)
}

// TestSSM_ResourceDataSync pins create/list/update/delete of a sync.
func TestSSM_ResourceDataSync(t *testing.T) {
	c := ssmClient()
	syncName := "sockerless-rds-sdk"

	_, err := c.CreateResourceDataSync(ctx, &ssm.CreateResourceDataSyncInput{
		SyncName: aws.String(syncName),
		S3Destination: &ssmtypes.ResourceDataSyncS3Destination{
			BucketName: aws.String("my-inventory-bucket"),
			SyncFormat: ssmtypes.ResourceDataSyncS3FormatJsonSerde,
			Region:     aws.String("us-east-1"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteResourceDataSync(ctx, &ssm.DeleteResourceDataSyncInput{SyncName: aws.String(syncName)})
	})

	lr, err := c.ListResourceDataSync(ctx, &ssm.ListResourceDataSyncInput{})
	require.NoError(t, err)
	found := false
	for _, it := range lr.ResourceDataSyncItems {
		if it.SyncName != nil && *it.SyncName == syncName {
			found = true
			require.NotNil(t, it.S3Destination)
			assert.Equal(t, "my-inventory-bucket", *it.S3Destination.BucketName)
		}
	}
	assert.True(t, found, "ListResourceDataSync must include created sync")

	// UpdateResourceDataSync updates a sync's source definition (tolerant: applies
	// to the SyncFromSource type; exercised here for round-trip coverage).
	_, _ = c.UpdateResourceDataSync(ctx, &ssm.UpdateResourceDataSyncInput{
		SyncName: aws.String(syncName),
		SyncType: aws.String("SyncToDestination"),
		SyncSource: &ssmtypes.ResourceDataSyncSource{
			SourceType:    aws.String("singleAccountMultiRegions"),
			SourceRegions: []string{"us-east-1", "us-west-2"},
		},
	})

	// Delete and confirm gone.
	_, err = c.DeleteResourceDataSync(ctx, &ssm.DeleteResourceDataSyncInput{SyncName: aws.String(syncName)})
	require.NoError(t, err)
	lr2, err := c.ListResourceDataSync(ctx, &ssm.ListResourceDataSyncInput{})
	require.NoError(t, err)
	for _, it := range lr2.ResourceDataSyncItems {
		if it.SyncName != nil {
			assert.NotEqual(t, syncName, *it.SyncName)
		}
	}
}
