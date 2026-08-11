package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_EventInvokeConfig exercises Put/Get/Update/Delete/List of the
// function event-invoke config (async-invoke retry/age/destinations).
func TestLambda_EventInvokeConfig(t *testing.T) {
	lc := lambdaClient()
	fn := "eic-fn"
	lambdaExtrasFunc(t, lc, fn)

	put, err := lc.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:             aws.String(fn),
		MaximumRetryAttempts:     aws.Int32(1),
		MaximumEventAgeInSeconds: aws.Int32(120),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), aws.ToInt32(put.MaximumRetryAttempts))
	assert.Equal(t, int32(120), aws.ToInt32(put.MaximumEventAgeInSeconds))

	got, err := lc.GetFunctionEventInvokeConfig(ctx, &lambda.GetFunctionEventInvokeConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), aws.ToInt32(got.MaximumRetryAttempts))

	upd, err := lc.UpdateFunctionEventInvokeConfig(ctx, &lambda.UpdateFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(fn),
		MaximumRetryAttempts: aws.Int32(2),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(upd.MaximumRetryAttempts))
	// Update merges: the age set on Put survives.
	assert.Equal(t, int32(120), aws.ToInt32(upd.MaximumEventAgeInSeconds))

	lst, err := lc.ListFunctionEventInvokeConfigs(ctx, &lambda.ListFunctionEventInvokeConfigsInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.FunctionEventInvokeConfigs)

	_, err = lc.DeleteFunctionEventInvokeConfig(ctx, &lambda.DeleteFunctionEventInvokeConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
}

// TestLambda_ProvisionedConcurrency exercises Put/Get/Delete/List of the
// per-qualifier provisioned concurrency config against a published version.
func TestLambda_ProvisionedConcurrency(t *testing.T) {
	lc := lambdaClient()
	fn := "pc-fn"
	lambdaExtrasFunc(t, lc, fn)

	pubv, err := lc.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(fn)})
	require.NoError(t, err)
	ver := aws.ToString(pubv.Version)
	require.NotEmpty(t, ver)

	put, err := lc.PutProvisionedConcurrencyConfig(ctx, &lambda.PutProvisionedConcurrencyConfigInput{
		FunctionName:                    aws.String(fn),
		Qualifier:                       aws.String(ver),
		ProvisionedConcurrentExecutions: aws.Int32(2),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(put.RequestedProvisionedConcurrentExecutions))

	got, err := lc.GetProvisionedConcurrencyConfig(ctx, &lambda.GetProvisionedConcurrencyConfigInput{
		FunctionName: aws.String(fn),
		Qualifier:    aws.String(ver),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), aws.ToInt32(got.AllocatedProvisionedConcurrentExecutions))

	lst, err := lc.ListProvisionedConcurrencyConfigs(ctx, &lambda.ListProvisionedConcurrencyConfigsInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.ProvisionedConcurrencyConfigs)

	_, err = lc.DeleteProvisionedConcurrencyConfig(ctx, &lambda.DeleteProvisionedConcurrencyConfigInput{
		FunctionName: aws.String(fn),
		Qualifier:    aws.String(ver),
	})
	require.NoError(t, err)
}

// TestLambda_CodeSigningConfigs exercises the account-level code-signing
// config CRUD plus the per-function attachment ops.
func TestLambda_CodeSigningConfigs(t *testing.T) {
	lc := lambdaClient()
	unconfiguredFn := "csc-unconfigured-fn"
	lambdaExtrasFunc(t, lc, unconfiguredFn)

	unconfigured, err := lc.GetFunctionCodeSigningConfig(ctx, &lambda.GetFunctionCodeSigningConfigInput{
		FunctionName: aws.String(unconfiguredFn),
	})
	require.NoError(t, err)
	assert.Equal(t, unconfiguredFn, aws.ToString(unconfigured.FunctionName))
	assert.Empty(t, aws.ToString(unconfigured.CodeSigningConfigArn))

	cre, err := lc.CreateCodeSigningConfig(ctx, &lambda.CreateCodeSigningConfigInput{
		Description: aws.String("test csc"),
		AllowedPublishers: &lambdatypes.AllowedPublishers{
			SigningProfileVersionArns: []string{
				"arn:aws:signer:us-east-1:123456789012:/signing-profiles/test/abc",
			},
		},
		CodeSigningPolicies: &lambdatypes.CodeSigningPolicies{
			UntrustedArtifactOnDeployment: lambdatypes.CodeSigningPolicyEnforce,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, cre.CodeSigningConfig)
	arn := aws.ToString(cre.CodeSigningConfig.CodeSigningConfigArn)
	require.NotEmpty(t, arn)
	t.Cleanup(func() {
		_, _ = lc.DeleteCodeSigningConfig(ctx, &lambda.DeleteCodeSigningConfigInput{
			CodeSigningConfigArn: aws.String(arn),
		})
	})

	got, err := lc.GetCodeSigningConfig(ctx, &lambda.GetCodeSigningConfigInput{
		CodeSigningConfigArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, "test csc", aws.ToString(got.CodeSigningConfig.Description))

	upd, err := lc.UpdateCodeSigningConfig(ctx, &lambda.UpdateCodeSigningConfigInput{
		CodeSigningConfigArn: aws.String(arn),
		Description:          aws.String("updated csc"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated csc", aws.ToString(upd.CodeSigningConfig.Description))

	lst, err := lc.ListCodeSigningConfigs(ctx, &lambda.ListCodeSigningConfigsInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, lst.CodeSigningConfigs)

	// Per-function attachment.
	fn := "csc-fn"
	lambdaExtrasFunc(t, lc, fn)
	_, err = lc.PutFunctionCodeSigningConfig(ctx, &lambda.PutFunctionCodeSigningConfigInput{
		FunctionName:         aws.String(fn),
		CodeSigningConfigArn: aws.String(arn),
	})
	require.NoError(t, err)

	gfc, err := lc.GetFunctionCodeSigningConfig(ctx, &lambda.GetFunctionCodeSigningConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(gfc.CodeSigningConfigArn))

	_, err = lc.DeleteFunctionCodeSigningConfig(ctx, &lambda.DeleteFunctionCodeSigningConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
}

// TestLambda_RuntimeManagementAndRecursion covers GetRuntimeManagementConfig /
// PutRuntimeManagementConfig and GetFunctionRecursionConfig /
// PutFunctionRecursionConfig.
func TestLambda_RuntimeManagementAndRecursion(t *testing.T) {
	lc := lambdaClient()
	fn := "rtm-fn"
	lambdaExtrasFunc(t, lc, fn)

	// Default runtime management is Auto.
	def, err := lc.GetRuntimeManagementConfig(ctx, &lambda.GetRuntimeManagementConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, lambdatypes.UpdateRuntimeOnAuto, def.UpdateRuntimeOn)

	rtmArn := "arn:aws:lambda:us-east-1::runtime:abc123"
	_, err = lc.PutRuntimeManagementConfig(ctx, &lambda.PutRuntimeManagementConfigInput{
		FunctionName:      aws.String(fn),
		UpdateRuntimeOn:   lambdatypes.UpdateRuntimeOnManual,
		RuntimeVersionArn: aws.String(rtmArn),
	})
	require.NoError(t, err)

	got, err := lc.GetRuntimeManagementConfig(ctx, &lambda.GetRuntimeManagementConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, lambdatypes.UpdateRuntimeOnManual, got.UpdateRuntimeOn)
	assert.Equal(t, rtmArn, aws.ToString(got.RuntimeVersionArn))

	// Recursion config: defaults Terminate, can be set to Allow.
	rdef, err := lc.GetFunctionRecursionConfig(ctx, &lambda.GetFunctionRecursionConfigInput{
		FunctionName: aws.String(fn),
	})
	require.NoError(t, err)
	assert.Equal(t, lambdatypes.RecursiveLoopTerminate, rdef.RecursiveLoop)

	rput, err := lc.PutFunctionRecursionConfig(ctx, &lambda.PutFunctionRecursionConfigInput{
		FunctionName:  aws.String(fn),
		RecursiveLoop: lambdatypes.RecursiveLoopAllow,
	})
	require.NoError(t, err)
	assert.Equal(t, lambdatypes.RecursiveLoopAllow, rput.RecursiveLoop)
}

// TestLambda_AccountSettings covers GetAccountSettings.
func TestLambda_AccountSettings(t *testing.T) {
	lc := lambdaClient()
	got, err := lc.GetAccountSettings(ctx, &lambda.GetAccountSettingsInput{})
	require.NoError(t, err)
	require.NotNil(t, got.AccountLimit)
	require.NotNil(t, got.AccountUsage)
	assert.NotZero(t, got.AccountLimit.ConcurrentExecutions)
}

// TestLambda_LayerVersionPermissions covers Add/Get/Remove of a layer-version
// resource policy statement.
func TestLambda_LayerVersionPermissions(t *testing.T) {
	lc := lambdaClient()
	layer := "perm-layer"

	pub, err := lc.PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{
		LayerName: aws.String(layer),
		Content:   &lambdatypes.LayerVersionContentInput{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	ver := pub.Version
	require.NotZero(t, ver)
	t.Cleanup(func() {
		_, _ = lc.DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{
			LayerName: aws.String(layer), VersionNumber: aws.Int64(ver),
		})
	})

	add, err := lc.AddLayerVersionPermission(ctx, &lambda.AddLayerVersionPermissionInput{
		LayerName:     aws.String(layer),
		VersionNumber: aws.Int64(ver),
		StatementId:   aws.String("share-org"),
		Action:        aws.String("lambda:GetLayerVersion"),
		Principal:     aws.String("123456789012"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(add.Statement))
	assert.NotEmpty(t, aws.ToString(add.RevisionId))

	pol, err := lc.GetLayerVersionPolicy(ctx, &lambda.GetLayerVersionPolicyInput{
		LayerName: aws.String(layer), VersionNumber: aws.Int64(ver),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(pol.Policy), "share-org")

	_, err = lc.RemoveLayerVersionPermission(ctx, &lambda.RemoveLayerVersionPermissionInput{
		LayerName:     aws.String(layer),
		VersionNumber: aws.Int64(ver),
		StatementId:   aws.String("share-org"),
	})
	require.NoError(t, err)
}
