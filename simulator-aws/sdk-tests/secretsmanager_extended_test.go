package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	types "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// smUniqueName returns a per-test unique secret name so the extended-op tests
// don't collide with one another or with the lifecycle suite.
func smUniqueName(prefix string) string {
	return prefix + "-" + strings.ReplaceAll(time.Now().Format("150405.000000000"), ".", "-")
}

// smCreate creates a secret with a single value and registers a force-delete
// cleanup. Returns the ARN.
func smCreate(t *testing.T, c *secretsmanager.Client, name, value string) string {
	t.Helper()
	out, err := c.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(value),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
			SecretId:                   aws.String(name),
			ForceDeleteWithoutRecovery: aws.Bool(true),
		})
	})
	return *out.ARN
}

func TestSecretsManager_ResourcePolicyLifecycle(t *testing.T) {
	c := smClient()
	name := smUniqueName("rp")
	smCreate(t, c, name, "v1")

	const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`

	putOut, err := c.PutResourcePolicy(ctx, &secretsmanager.PutResourcePolicyInput{
		SecretId:       aws.String(name),
		ResourcePolicy: aws.String(policy),
	})
	require.NoError(t, err)
	assert.Equal(t, name, *putOut.Name)
	require.Contains(t, *putOut.ARN, ":secret:"+name+"-")

	getOut, err := c.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, getOut.ResourcePolicy)
	assert.JSONEq(t, policy, *getOut.ResourcePolicy)

	delOut, err := c.DeleteResourcePolicy(ctx, &secretsmanager.DeleteResourcePolicyInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, name, *delOut.Name)

	getAfter, err := c.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Nil(t, getAfter.ResourcePolicy, "ResourcePolicy must be nil after delete")
}

func TestSecretsManager_ValidateResourcePolicy(t *testing.T) {
	c := smClient()
	name := smUniqueName("vrp")
	smCreate(t, c, name, "v1")

	good, err := c.ValidateResourcePolicy(ctx, &secretsmanager.ValidateResourcePolicyInput{
		SecretId:       aws.String(name),
		ResourcePolicy: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)
	assert.True(t, good.PolicyValidationPassed)
	assert.Empty(t, good.ValidationErrors)

	bad, err := c.ValidateResourcePolicy(ctx, &secretsmanager.ValidateResourcePolicyInput{
		SecretId:       aws.String(name),
		ResourcePolicy: aws.String("not json{"),
	})
	require.NoError(t, err)
	assert.False(t, bad.PolicyValidationPassed)
	require.NotEmpty(t, bad.ValidationErrors)
	assert.NotEmpty(t, *bad.ValidationErrors[0].CheckName)
}

func TestSecretsManager_DeleteRestore(t *testing.T) {
	c := smClient()
	name := smUniqueName("restore")
	smCreate(t, c, name, "v1")

	// Soft-delete with a recovery window: the value can no longer be read.
	del, err := c.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:             aws.String(name),
		RecoveryWindowInDays: aws.Int64(7),
	})
	require.NoError(t, err)
	require.NotNil(t, del.DeletionDate)

	_, err = c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	require.Error(t, err, "GetSecretValue on a secret scheduled for deletion must fail")

	desc, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, desc.DeletedDate, "DescribeSecret must report DeletedDate while scheduled")

	// Restore clears the scheduled deletion.
	rest, err := c.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, name, *rest.Name)

	got, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err, "GetSecretValue must succeed after RestoreSecret")
	assert.Equal(t, "v1", *got.SecretString)
}

func TestSecretsManager_RotateAndCancel(t *testing.T) {
	c := smClient()
	name := smUniqueName("rotate")
	smCreate(t, c, name, "v1")

	before, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(before.RotationEnabled))

	getBefore, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
	require.NoError(t, err)

	rot, err := c.RotateSecret(ctx, &secretsmanager.RotateSecretInput{
		SecretId: aws.String(name),
		RotationRules: &types.RotationRulesType{
			AutomaticallyAfterDays: aws.Int64(30),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, rot.VersionId)
	assert.NotEqual(t, *getBefore.VersionId, *rot.VersionId, "RotateSecret must mint a new version")

	afterRotate, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(afterRotate.RotationEnabled))
	require.NotNil(t, afterRotate.RotationRules)
	require.NotNil(t, afterRotate.RotationRules.AutomaticallyAfterDays)
	assert.Equal(t, int64(30), *afterRotate.RotationRules.AutomaticallyAfterDays)

	cancel, err := c.CancelRotateSecret(ctx, &secretsmanager.CancelRotateSecretInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, cancel.VersionId)

	afterCancel, err := c.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(name)})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(afterCancel.RotationEnabled))
}

func TestSecretsManager_BatchGetSecretValue(t *testing.T) {
	c := smClient()
	n1 := smUniqueName("batch1")
	n2 := smUniqueName("batch2")
	smCreate(t, c, n1, "secret-one")
	smCreate(t, c, n2, "secret-two")

	out, err := c.BatchGetSecretValue(ctx, &secretsmanager.BatchGetSecretValueInput{
		SecretIdList: []string{n1, n2, "no-such-secret-xyz"},
	})
	require.NoError(t, err)

	values := map[string]string{}
	for _, sv := range out.SecretValues {
		if sv.Name != nil && sv.SecretString != nil {
			values[*sv.Name] = *sv.SecretString
		}
	}
	assert.Equal(t, "secret-one", values[n1])
	assert.Equal(t, "secret-two", values[n2])

	// The missing secret is reported in Errors, not a top-level failure.
	require.NotEmpty(t, out.Errors)
	foundErr := false
	for _, e := range out.Errors {
		if e.SecretId != nil && *e.SecretId == "no-such-secret-xyz" {
			foundErr = true
		}
	}
	assert.True(t, foundErr, "missing secret must surface in Errors")
}

func TestSecretsManager_UpdateSecretVersionStage(t *testing.T) {
	c := smClient()
	name := smUniqueName("stage")
	smCreate(t, c, name, "v1")

	v1, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
	require.NoError(t, err)

	put, err := c.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("v2"),
	})
	require.NoError(t, err)
	// v2 is now AWSCURRENT, v1 is AWSPREVIOUS. Move AWSCURRENT back to v1.
	upd, err := c.UpdateSecretVersionStage(ctx, &secretsmanager.UpdateSecretVersionStageInput{
		SecretId:            aws.String(name),
		VersionStage:        aws.String("AWSCURRENT"),
		RemoveFromVersionId: put.VersionId,
		MoveToVersionId:     v1.VersionId,
	})
	require.NoError(t, err)
	assert.Equal(t, name, *upd.Name)

	current, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId:     aws.String(name),
		VersionStage: aws.String("AWSCURRENT"),
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", *current.SecretString, "AWSCURRENT must now point at the original version")
}

func TestSecretsManager_Replication(t *testing.T) {
	c := smClient()
	regionalClient := func(region string) *secretsmanager.Client {
		config := sdkConfig()
		config.Region = region
		return secretsmanager.NewFromConfig(config, func(options *secretsmanager.Options) {
			options.BaseEndpoint = aws.String(baseURL)
		})
	}
	west := regionalClient("us-west-2")
	europe := regionalClient("eu-west-1")
	name := smUniqueName("replica")
	smCreate(t, c, name, "v1")
	smCreate(t, west, name, "destination-conflict")
	t.Cleanup(func() {
		for _, client := range []*secretsmanager.Client{c, west, europe} {
			_, _ = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
				SecretId:                   aws.String(name),
				ForceDeleteWithoutRecovery: aws.Bool(true),
			})
		}
	})

	_, err := c.ReplicateSecretToRegions(ctx, &secretsmanager.ReplicateSecretToRegionsInput{
		SecretId: aws.String(name),
		AddReplicaRegions: []types.ReplicaRegionType{
			{Region: aws.String("us-west-2")},
			{Region: aws.String("eu-west-1")},
		},
	})
	require.Error(t, err, "an existing destination secret requires ForceOverwriteReplicaSecret")

	rep, err := c.ReplicateSecretToRegions(ctx, &secretsmanager.ReplicateSecretToRegionsInput{
		SecretId: aws.String(name),
		AddReplicaRegions: []types.ReplicaRegionType{
			{Region: aws.String("us-west-2")},
			{Region: aws.String("eu-west-1")},
		},
		ForceOverwriteReplicaSecret: true,
	})
	require.NoError(t, err)
	require.Len(t, rep.ReplicationStatus, 2)
	regions := map[string]bool{}
	for _, rs := range rep.ReplicationStatus {
		regions[*rs.Region] = true
		assert.Equal(t, types.StatusTypeInSync, rs.Status)
	}
	assert.True(t, regions["us-west-2"] && regions["eu-west-1"])

	for region, client := range map[string]*secretsmanager.Client{
		"us-west-2": west,
		"eu-west-1": europe,
	} {
		replica, replicaErr := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
			SecretId: aws.String(name),
		})
		require.NoError(t, replicaErr)
		assert.Equal(t, "v1", aws.ToString(replica.SecretString))
		assert.Contains(t, aws.ToString(replica.ARN), ":secretsmanager:"+region+":")
	}

	_, err = c.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("v2"),
	})
	require.NoError(t, err)
	replicatedValue, err := europe.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", aws.ToString(replicatedValue.SecretString),
		"primary versions propagate to the regional replica")

	removed, err := c.RemoveRegionsFromReplication(ctx, &secretsmanager.RemoveRegionsFromReplicationInput{
		SecretId:             aws.String(name),
		RemoveReplicaRegions: []string{"us-west-2"},
	})
	require.NoError(t, err)
	require.Len(t, removed.ReplicationStatus, 1)
	assert.Equal(t, "eu-west-1", *removed.ReplicationStatus[0].Region)
	_, err = west.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(name)})
	require.Error(t, err, "removing a replica Region deletes its regional secret")

	// StopReplicationToReplica runs in the replica Region and promotes that
	// durable copy to an independent secret.
	stop, err := europe.StopReplicationToReplica(ctx, &secretsmanager.StopReplicationToReplicaInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.NotNil(t, stop.ARN)
	require.Contains(t, *stop.ARN, ":secret:"+name+"-")
	require.Contains(t, *stop.ARN, ":secretsmanager:eu-west-1:")

	_, err = europe.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(name),
		SecretString: aws.String("independent"),
	})
	require.NoError(t, err)
	primaryValue, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", aws.ToString(primaryValue.SecretString),
		"a promoted replica no longer receives or sends primary updates")
}

func TestSecretsManager_CreateWithReplication(t *testing.T) {
	primary := smClient()
	config := sdkConfig()
	config.Region = "us-west-2"
	replica := secretsmanager.NewFromConfig(config, func(options *secretsmanager.Options) {
		options.BaseEndpoint = aws.String(baseURL)
	})
	name := smUniqueName("create-replica")
	_, err := primary.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String("created-with-replica"),
		AddReplicaRegions: []types.ReplicaRegionType{{
			Region: aws.String("us-west-2"),
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, client := range []*secretsmanager.Client{primary, replica} {
			_, _ = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
				SecretId:                   aws.String(name),
				ForceDeleteWithoutRecovery: aws.Bool(true),
			})
		}
	})
	described, err := primary.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, described.ReplicationStatus, 1)
	assert.Equal(t, "us-west-2", aws.ToString(described.ReplicationStatus[0].Region))
	assert.Equal(t, types.StatusTypeInSync, described.ReplicationStatus[0].Status)
	value, err := replica.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, "created-with-replica", aws.ToString(value.SecretString))
}
