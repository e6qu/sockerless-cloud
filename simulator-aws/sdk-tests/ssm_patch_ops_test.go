package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmCreateBaseline creates a Linux patch baseline and returns its ID,
// registering a t.Cleanup to delete it.
func ssmCreateBaseline(t *testing.T, c *ssm.Client, name string) string {
	t.Helper()
	out, err := c.CreatePatchBaseline(ctx, &ssm.CreatePatchBaselineInput{
		Name:            aws.String(name),
		OperatingSystem: ssmtypes.OperatingSystemAmazonLinux2,
	})
	require.NoError(t, err)
	require.NotNil(t, out.BaselineId)
	t.Cleanup(func() {
		_, _ = c.DeletePatchBaseline(ctx, &ssm.DeletePatchBaselineInput{BaselineId: out.BaselineId})
	})
	return aws.ToString(out.BaselineId)
}

// TestSSM_PatchGroupRegistration exercises the patch-group ↔ baseline
// registration round trip: register, read it back via
// GetPatchBaselineForPatchGroup + DescribePatchGroups, then deregister.
func TestSSM_PatchGroupRegistration(t *testing.T) {
	c := ssmClient()
	blID := ssmCreateBaseline(t, c, "sdk-patchgroup-baseline")
	const group = "sdk-prod-group"

	reg, err := c.RegisterPatchBaselineForPatchGroup(ctx, &ssm.RegisterPatchBaselineForPatchGroupInput{
		BaselineId: aws.String(blID),
		PatchGroup: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, blID, aws.ToString(reg.BaselineId))
	assert.Equal(t, group, aws.ToString(reg.PatchGroup))

	got, err := c.GetPatchBaselineForPatchGroup(ctx, &ssm.GetPatchBaselineForPatchGroupInput{
		PatchGroup:      aws.String(group),
		OperatingSystem: ssmtypes.OperatingSystemAmazonLinux2,
	})
	require.NoError(t, err)
	assert.Equal(t, blID, aws.ToString(got.BaselineId))
	assert.Equal(t, group, aws.ToString(got.PatchGroup))
	assert.Equal(t, ssmtypes.OperatingSystemAmazonLinux2, got.OperatingSystem)

	groups, err := c.DescribePatchGroups(ctx, &ssm.DescribePatchGroupsInput{})
	require.NoError(t, err)
	foundGroup := false
	for _, m := range groups.Mappings {
		if aws.ToString(m.PatchGroup) == group {
			foundGroup = true
			require.NotNil(t, m.BaselineIdentity)
			assert.Equal(t, blID, aws.ToString(m.BaselineIdentity.BaselineId))
			assert.Equal(t, ssmtypes.OperatingSystemAmazonLinux2, m.BaselineIdentity.OperatingSystem)
		}
	}
	assert.True(t, foundGroup, "DescribePatchGroups must include the registered group")

	// Patch-group state is honest-empty (no scanned managed nodes).
	state, err := c.DescribePatchGroupState(ctx, &ssm.DescribePatchGroupStateInput{
		PatchGroup: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), state.Instances)
	assert.Equal(t, int32(0), state.InstancesWithMissingPatches)

	dereg, err := c.DeregisterPatchBaselineForPatchGroup(ctx, &ssm.DeregisterPatchBaselineForPatchGroupInput{
		BaselineId: aws.String(blID),
		PatchGroup: aws.String(group),
	})
	require.NoError(t, err)
	assert.Equal(t, group, aws.ToString(dereg.PatchGroup))

	// After deregistration the lookup must fail.
	_, err = c.GetPatchBaselineForPatchGroup(ctx, &ssm.GetPatchBaselineForPatchGroupInput{
		PatchGroup:      aws.String(group),
		OperatingSystem: ssmtypes.OperatingSystemAmazonLinux2,
	})
	require.Error(t, err, "lookup must fail after deregistration")
}

// TestSSM_AvailablePatches covers the patch-read ops: available patches,
// effective patches for a baseline, instance patch states/patches, patch
// properties, and the deployable snapshot.
func TestSSM_AvailablePatches(t *testing.T) {
	c := ssmClient()
	blID := ssmCreateBaseline(t, c, "sdk-patchread-baseline")

	avail, err := c.DescribeAvailablePatches(ctx, &ssm.DescribeAvailablePatchesInput{})
	require.NoError(t, err)
	assert.NotNil(t, avail.Patches)

	eff, err := c.DescribeEffectivePatchesForPatchBaseline(ctx, &ssm.DescribeEffectivePatchesForPatchBaselineInput{
		BaselineId: aws.String(blID),
	})
	require.NoError(t, err)
	assert.NotNil(t, eff.EffectivePatches)

	// Unknown baseline must 400.
	_, err = c.DescribeEffectivePatchesForPatchBaseline(ctx, &ssm.DescribeEffectivePatchesForPatchBaselineInput{
		BaselineId: aws.String("pb-doesnotexist00000"),
	})
	require.Error(t, err)

	props, err := c.DescribePatchProperties(ctx, &ssm.DescribePatchPropertiesInput{
		OperatingSystem: ssmtypes.OperatingSystemAmazonLinux2,
		Property:        ssmtypes.PatchPropertyPatchClassification,
	})
	require.NoError(t, err)
	require.NotEmpty(t, props.Properties, "AMAZON_LINUX_2 CLASSIFICATION must enumerate real values")
	foundSecurity := false
	for _, p := range props.Properties {
		if p["CLASSIFICATION"] == "Security" {
			foundSecurity = true
		}
	}
	assert.True(t, foundSecurity, "CLASSIFICATION values must include Security")
}

// TestSSM_InstancePatchReads covers the per-instance patch reads and the
// deployable snapshot. The sim has no managed nodes, so the state/patch
// reads are honest-empty.
func TestSSM_InstancePatchReads(t *testing.T) {
	c := ssmClient()

	patches, err := c.DescribeInstancePatches(ctx, &ssm.DescribeInstancePatchesInput{
		InstanceId: aws.String("i-0123456789abcdef0"),
	})
	require.NoError(t, err)
	assert.NotNil(t, patches.Patches)

	states, err := c.DescribeInstancePatchStates(ctx, &ssm.DescribeInstancePatchStatesInput{
		InstanceIds: []string{"i-0123456789abcdef0"},
	})
	require.NoError(t, err)
	assert.NotNil(t, states.InstancePatchStates)

	groupStates, err := c.DescribeInstancePatchStatesForPatchGroup(ctx, &ssm.DescribeInstancePatchStatesForPatchGroupInput{
		PatchGroup: aws.String("sdk-some-group"),
	})
	require.NoError(t, err)
	assert.NotNil(t, groupStates.InstancePatchStates)

	snap, err := c.GetDeployablePatchSnapshotForInstance(ctx, &ssm.GetDeployablePatchSnapshotForInstanceInput{
		InstanceId: aws.String("i-0123456789abcdef0"),
		SnapshotId: aws.String("11111111-2222-3333-4444-555555555555"),
	})
	require.NoError(t, err)
	assert.Equal(t, "11111111-2222-3333-4444-555555555555", aws.ToString(snap.SnapshotId))
	assert.Contains(t, aws.ToString(snap.SnapshotDownloadUrl), "11111111-2222-3333-4444-555555555555")
	assert.Contains(t, aws.ToString(snap.SnapshotDownloadUrl), "s3")
}

// TestSSM_OpsMetadata exercises the OpsMetadata CRUD round trip:
// create, get, update (add + delete keys), list, delete.
func TestSSM_OpsMetadata(t *testing.T) {
	c := ssmClient()
	resourceID := "arn:aws:resource-groups:us-east-1:123456789012:group/sdk-app"

	created, err := c.CreateOpsMetadata(ctx, &ssm.CreateOpsMetadataInput{
		ResourceId: aws.String(resourceID),
		Metadata: map[string]ssmtypes.MetadataValue{
			"team": {Value: aws.String("runner")},
			"env":  {Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.OpsMetadataArn)
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_, _ = c.DeleteOpsMetadata(ctx, &ssm.DeleteOpsMetadataInput{OpsMetadataArn: aws.String(arn)})
	})

	// Duplicate create must fail.
	_, err = c.CreateOpsMetadata(ctx, &ssm.CreateOpsMetadataInput{ResourceId: aws.String(resourceID)})
	require.Error(t, err, "duplicate OpsMetadata create must fail")

	got, err := c.GetOpsMetadata(ctx, &ssm.GetOpsMetadataInput{OpsMetadataArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, resourceID, aws.ToString(got.ResourceId))
	require.Contains(t, got.Metadata, "team")
	assert.Equal(t, "runner", aws.ToString(got.Metadata["team"].Value))

	_, err = c.UpdateOpsMetadata(ctx, &ssm.UpdateOpsMetadataInput{
		OpsMetadataArn: aws.String(arn),
		MetadataToUpdate: map[string]ssmtypes.MetadataValue{
			"region": {Value: aws.String("us-east-1")},
		},
		KeysToDelete: []string{"env"},
	})
	require.NoError(t, err)

	got2, err := c.GetOpsMetadata(ctx, &ssm.GetOpsMetadataInput{OpsMetadataArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Contains(t, got2.Metadata, "region")
	assert.NotContains(t, got2.Metadata, "env")

	listed, err := c.ListOpsMetadata(ctx, &ssm.ListOpsMetadataInput{})
	require.NoError(t, err)
	foundArn := false
	for _, m := range listed.OpsMetadataList {
		if aws.ToString(m.OpsMetadataArn) == arn {
			foundArn = true
			assert.Equal(t, resourceID, aws.ToString(m.ResourceId))
		}
	}
	assert.True(t, foundArn, "ListOpsMetadata must include the created object")
}

// TestSSM_OpsSummary verifies GetOpsSummary returns the real shape. The
// sim has no OpsData ingest pipeline, so the entity list is honest-empty.
func TestSSM_OpsSummary(t *testing.T) {
	c := ssmClient()
	out, err := c.GetOpsSummary(ctx, &ssm.GetOpsSummaryInput{
		ResultAttributes: []ssmtypes.OpsResultAttribute{
			{TypeName: aws.String("AWS:OpsItem")},
		},
	})
	require.NoError(t, err)
	assert.NotNil(t, out.Entities)
}

// TestSSM_ResourcePolicy exercises the resource-policy round trip with the
// PolicyId/PolicyHash optimistic-concurrency contract: put, get, update,
// delete.
func TestSSM_ResourcePolicy(t *testing.T) {
	c := ssmClient()
	resourceArn := "arn:aws:ssm:us-east-1:123456789012:opsitemgroup/default"
	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":["ssm:GetOpsItem"],"Resource":"*"}]}`

	put, err := c.PutResourcePolicy(ctx, &ssm.PutResourcePolicyInput{
		ResourceArn: aws.String(resourceArn),
		Policy:      aws.String(policy),
	})
	require.NoError(t, err)
	policyID := aws.ToString(put.PolicyId)
	policyHash := aws.ToString(put.PolicyHash)
	require.NotEmpty(t, policyID)
	require.NotEmpty(t, policyHash)

	got, err := c.GetResourcePolicies(ctx, &ssm.GetResourcePoliciesInput{
		ResourceArn: aws.String(resourceArn),
	})
	require.NoError(t, err)
	require.Len(t, got.Policies, 1)
	assert.Equal(t, policyID, aws.ToString(got.Policies[0].PolicyId))
	assert.Equal(t, policyHash, aws.ToString(got.Policies[0].PolicyHash))
	assert.Contains(t, aws.ToString(got.Policies[0].Policy), "ssm:GetOpsItem")

	// Update with stale hash must fail (optimistic concurrency).
	_, err = c.PutResourcePolicy(ctx, &ssm.PutResourcePolicyInput{
		ResourceArn: aws.String(resourceArn),
		Policy:      aws.String(policy),
		PolicyId:    aws.String(policyID),
		PolicyHash:  aws.String("staleHash"),
	})
	require.Error(t, err, "update with stale PolicyHash must fail")

	// Update with the correct hash succeeds.
	const updated = `{"Version":"2012-10-17","Statement":[]}`
	put2, err := c.PutResourcePolicy(ctx, &ssm.PutResourcePolicyInput{
		ResourceArn: aws.String(resourceArn),
		Policy:      aws.String(updated),
		PolicyId:    aws.String(policyID),
		PolicyHash:  aws.String(policyHash),
	})
	require.NoError(t, err)
	newHash := aws.ToString(put2.PolicyHash)
	assert.NotEqual(t, policyHash, newHash, "PolicyHash must change after update")

	_, err = c.DeleteResourcePolicy(ctx, &ssm.DeleteResourcePolicyInput{
		ResourceArn: aws.String(resourceArn),
		PolicyId:    aws.String(policyID),
		PolicyHash:  aws.String(newHash),
	})
	require.NoError(t, err)

	// Policy is gone.
	gone, err := c.GetResourcePolicies(ctx, &ssm.GetResourcePoliciesInput{
		ResourceArn: aws.String(resourceArn),
	})
	require.NoError(t, err)
	assert.Empty(t, gone.Policies)
}

// TestSSM_ParameterHistory verifies GetParameterHistory returns one entry
// per observed parameter version.
func TestSSM_ParameterHistory(t *testing.T) {
	c := ssmClient()
	name := "/sdk/history/param"
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)}) })

	for i, v := range []string{"v1", "v2", "v3"} {
		_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
			Name:      aws.String(name),
			Type:      ssmtypes.ParameterTypeString,
			Value:     aws.String(v),
			Overwrite: aws.Bool(i > 0),
		})
		require.NoError(t, err)
		// Read between writes so each version is reconciled into history.
		_, err = c.GetParameterHistory(ctx, &ssm.GetParameterHistoryInput{Name: aws.String(name)})
		require.NoError(t, err)
	}

	hist, err := c.GetParameterHistory(ctx, &ssm.GetParameterHistoryInput{Name: aws.String(name)})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(hist.Parameters), 3, "history must include all observed versions")
	versions := map[int64]string{}
	for _, p := range hist.Parameters {
		versions[p.Version] = aws.ToString(p.Value)
		assert.NotNil(t, p.Labels, "Labels must be non-nil")
	}
	assert.Equal(t, "v1", versions[1])
	assert.Equal(t, "v3", versions[3])
}

// TestSSM_ParameterLabel exercises label attach/detach over a parameter
// version, including label uniqueness (relabel moves a label off the old
// version) and Unlabel.
func TestSSM_ParameterLabel(t *testing.T) {
	c := ssmClient()
	name := "/sdk/label/param"
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(name)}) })

	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name: aws.String(name), Type: ssmtypes.ParameterTypeString, Value: aws.String("a"),
	})
	require.NoError(t, err)
	// Reconcile v1 into history.
	_, err = c.GetParameterHistory(ctx, &ssm.GetParameterHistoryInput{Name: aws.String(name)})
	require.NoError(t, err)

	lbl, err := c.LabelParameterVersion(ctx, &ssm.LabelParameterVersionInput{
		Name:   aws.String(name),
		Labels: []string{"prod", "1bad"},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), lbl.ParameterVersion)
	assert.Contains(t, lbl.InvalidLabels, "1bad", "labels starting with a digit are invalid")

	hist, err := c.GetParameterHistory(ctx, &ssm.GetParameterHistoryInput{Name: aws.String(name)})
	require.NoError(t, err)
	var v1Labels []string
	for _, p := range hist.Parameters {
		if p.Version == 1 {
			v1Labels = p.Labels
		}
	}
	assert.Contains(t, v1Labels, "prod")
	assert.NotContains(t, v1Labels, "1bad")

	// Unlabel removes the label; an unattached label is reported invalid.
	un, err := c.UnlabelParameterVersion(ctx, &ssm.UnlabelParameterVersionInput{
		Name:             aws.String(name),
		ParameterVersion: aws.Int64(1),
		Labels:           []string{"prod", "never-attached"},
	})
	require.NoError(t, err)
	assert.Contains(t, un.RemovedLabels, "prod")
	assert.Contains(t, un.InvalidLabels, "never-attached")
}
