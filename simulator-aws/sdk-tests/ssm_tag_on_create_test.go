package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSM_CreateCarriesTags creates one resource of every type a Systems
// Manager create tags, and reads each one's tags back through
// ListTagsForResource.
func TestSSM_CreateCarriesTags(t *testing.T) {
	c := ssmClient()
	tags := []ssmtypes.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}}
	assertTagged := func(resourceType ssmtypes.ResourceTypeForTagging, id string) {
		t.Helper()
		listed, err := c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
			ResourceType: resourceType, ResourceId: aws.String(id),
		})
		require.NoError(t, err)
		require.Len(t, listed.TagList, 1, "%s %s", resourceType, id)
		assert.Equal(t, "owner", aws.ToString(listed.TagList[0].Key))
		assert.Equal(t, "platform", aws.ToString(listed.TagList[0].Value))
	}

	parameter := uniqueName("/tag-on-create/parameter")
	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name: aws.String(parameter), Type: ssmtypes.ParameterTypeString, Value: aws.String("v1"), Tags: tags,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(parameter)}) })
	assertTagged(ssmtypes.ResourceTypeForTaggingParameter, parameter)
	_, err = c.PutParameter(ctx, &ssm.PutParameterInput{
		Name: aws.String(parameter), Type: ssmtypes.ParameterTypeString, Value: aws.String("v2"),
		Overwrite: aws.Bool(true), Tags: tags,
	})
	assertAWSAPIErrorCode(t, err, "ValidationException")

	document := uniqueName("tag-on-create-document")
	_, err = c.CreateDocument(ctx, &ssm.CreateDocumentInput{
		Name:           aws.String(document),
		Content:        aws.String(`{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"s","inputs":{"runCommand":["echo hi"]}}]}`),
		DocumentType:   ssmtypes.DocumentTypeCommand,
		DocumentFormat: ssmtypes.DocumentFormatJson,
		Tags:           tags,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteDocument(ctx, &ssm.DeleteDocumentInput{Name: aws.String(document)}) })
	assertTagged(ssmtypes.ResourceTypeForTaggingDocument, document)

	association, err := c.CreateAssociation(ctx, &ssm.CreateAssociationInput{
		Name: aws.String(document), Tags: tags,
		Targets: []ssmtypes.Target{{Key: aws.String("InstanceIds"), Values: []string{"i-0123456789abcdef0"}}},
	})
	require.NoError(t, err)
	associationID := aws.ToString(association.AssociationDescription.AssociationId)
	t.Cleanup(func() {
		_, _ = c.DeleteAssociation(ctx, &ssm.DeleteAssociationInput{AssociationId: aws.String(associationID)})
	})
	assertTagged(ssmtypes.ResourceTypeForTaggingAssociation, associationID)

	automation, err := c.StartAutomationExecution(ctx, &ssm.StartAutomationExecutionInput{
		DocumentName: aws.String(document), Tags: tags,
	})
	require.NoError(t, err)
	assertTagged(ssmtypes.ResourceTypeForTaggingAutomation, aws.ToString(automation.AutomationExecutionId))

	window, err := c.CreateMaintenanceWindow(ctx, &ssm.CreateMaintenanceWindowInput{
		Name: aws.String(uniqueName("tag-on-create-window")), Schedule: aws.String("cron(0 16 ? * TUE *)"),
		Duration: aws.Int32(4), Cutoff: 1, AllowUnassociatedTargets: true, Tags: tags,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteMaintenanceWindow(ctx, &ssm.DeleteMaintenanceWindowInput{WindowId: window.WindowId})
	})
	assertTagged(ssmtypes.ResourceTypeForTaggingMaintenanceWindow, aws.ToString(window.WindowId))

	baseline, err := c.CreatePatchBaseline(ctx, &ssm.CreatePatchBaselineInput{
		Name: aws.String(uniqueName("tag-on-create-baseline")), OperatingSystem: ssmtypes.OperatingSystemUbuntu,
		ApprovedPatches: []string{"patch-1"}, Tags: tags,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeletePatchBaseline(ctx, &ssm.DeletePatchBaselineInput{BaselineId: baseline.BaselineId})
	})
	assertTagged(ssmtypes.ResourceTypeForTaggingPatchBaseline, aws.ToString(baseline.BaselineId))

	opsItem, err := c.CreateOpsItem(ctx, &ssm.CreateOpsItemInput{
		Title: aws.String("Disk full on host"), Description: aws.String("The root volume is full."),
		Source: aws.String("EC2"), Tags: tags,
	})
	require.NoError(t, err)
	assertTagged(ssmtypes.ResourceTypeForTaggingOpsItem, aws.ToString(opsItem.OpsItemId))

	iamc := iamClient()
	roleName := uniqueName("SSMTagOnCreateRole")
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"ssm.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)}) })
	connector, err := c.CreateCloudConnector(ctx, &ssm.CreateCloudConnectorInput{
		DisplayName:        aws.String(uniqueName("tag-on-create-connector")),
		RoleArn:            role.Role.Arn,
		ConfigConnectorArn: aws.String("arn:aws:config:us-east-1:000000000000:connector/azure"),
		Configuration: ssmCloudConnectorAzureConfig("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"99999999-8888-7777-6666-555555555555"),
		Tags: tags,
	})
	require.NoError(t, err)
	connectorID := aws.ToString(connector.CloudConnectorId)
	t.Cleanup(func() {
		_, _ = c.DeleteCloudConnector(ctx, &ssm.DeleteCloudConnectorInput{CloudConnectorId: aws.String(connectorID)})
	})
	assertTagged(ssmtypes.ResourceTypeForTaggingCloudConnector, connectorID)
}
