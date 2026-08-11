package aws_sdk_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lambdaCreateFn(t *testing.T, c *lambda.Client, name string) string {
	t.Helper()
	out, err := c.CreateFunction(context.Background(), &lambda.CreateFunctionInput{
		FunctionName: aws.String(name),
		Role:         aws.String("arn:aws:iam::123456789012:role/r"),
		PackageType:  types.PackageTypeImage,
		Code: &types.FunctionCode{
			ImageUri: aws.String("local/image:1"),
		},
	})
	require.NoError(t, err)
	return aws.ToString(out.FunctionArn)
}

// TestLambda_PublishVersion creates a function, publishes versions
// monotonically, and lists them. Each PublishVersion returns 201 with
// a Version field; the Nth call gives "N". ListVersionsByFunction
// includes $LATEST followed by published versions.
func TestLambda_PublishVersion(t *testing.T) {
	c := lambdaClient()
	ctx := context.Background()
	name := "subres-publish"
	lambdaCreateFn(t, c, name)

	for i := 1; i <= 3; i++ {
		v, err := c.PublishVersion(ctx, &lambda.PublishVersionInput{
			FunctionName: aws.String(name),
		})
		require.NoError(t, err)
		require.Equal(t, strings.TrimSpace(aws.ToString(v.Version)),
			[]string{"1", "2", "3"}[i-1])
		assert.True(t, strings.HasSuffix(aws.ToString(v.FunctionArn),
			":"+aws.ToString(v.Version)), "version ARN must end with :version")
	}

	list, err := c.ListVersionsByFunction(ctx, &lambda.ListVersionsByFunctionInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)
	require.Len(t, list.Versions, 4)
	assert.Equal(t, "$LATEST", aws.ToString(list.Versions[0].Version))
	for i, version := range list.Versions[1:] {
		assert.Equal(t, []string{"1", "2", "3"}[i], aws.ToString(version.Version))
	}
}

// TestLambda_ListFunctions_FunctionVersionAll verifies that ListFunctions
// with FunctionVersion=ALL returns both $LATEST and every published version,
// while the default call returns only $LATEST.
func TestLambda_ListFunctions_FunctionVersionAll(t *testing.T) {
	c := lambdaClient()
	ctx := context.Background()
	name := "listfn-all-ver"
	lambdaCreateFn(t, c, name)

	_, err := c.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(name)})
	require.NoError(t, err)
	_, err = c.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(name)})
	require.NoError(t, err)

	// Default: only $LATEST for this function.
	defList, err := c.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	require.NoError(t, err)
	var defForName *types.FunctionConfiguration
	for i := range defList.Functions {
		if aws.ToString(defList.Functions[i].FunctionName) == name {
			defForName = &defList.Functions[i]
		}
	}
	require.NotNil(t, defForName, "default list must include the function")
	assert.Equal(t, "$LATEST", aws.ToString(defForName.Version), "default list must show $LATEST only")

	// FunctionVersion=ALL: should include $LATEST + 2 published versions for this function.
	all, err := c.ListFunctions(ctx, &lambda.ListFunctionsInput{
		FunctionVersion: types.FunctionVersionAll,
	})
	require.NoError(t, err)
	var seen []string
	for _, fc := range all.Functions {
		if aws.ToString(fc.FunctionName) == name {
			seen = append(seen, aws.ToString(fc.Version))
		}
	}
	assert.Contains(t, seen, "$LATEST", "FunctionVersion=ALL must include $LATEST")
	assert.Contains(t, seen, "1", "FunctionVersion=ALL must include published version 1")
	assert.Contains(t, seen, "2", "FunctionVersion=ALL must include published version 2")
}

// TestLambda_AliasCRUD exercises the full alias lifecycle.
func TestLambda_AliasCRUD(t *testing.T) {
	c := lambdaClient()
	ctx := context.Background()
	name := "subres-alias"
	lambdaCreateFn(t, c, name)

	_, err := c.PublishVersion(ctx, &lambda.PublishVersionInput{FunctionName: aws.String(name)})
	require.NoError(t, err)

	created, err := c.CreateAlias(ctx, &lambda.CreateAliasInput{
		FunctionName:    aws.String(name),
		Name:            aws.String("live"),
		FunctionVersion: aws.String("1"),
		Description:     aws.String("primary alias"),
	})
	require.NoError(t, err)
	assert.Equal(t, "live", aws.ToString(created.Name))
	assert.Equal(t, "1", aws.ToString(created.FunctionVersion))

	got, err := c.GetAlias(ctx, &lambda.GetAliasInput{
		FunctionName: aws.String(name),
		Name:         aws.String("live"),
	})
	require.NoError(t, err)
	assert.Equal(t, "primary alias", aws.ToString(got.Description))

	listed, err := c.ListAliases(ctx, &lambda.ListAliasesInput{FunctionName: aws.String(name)})
	require.NoError(t, err)
	assert.Len(t, listed.Aliases, 1)

	updated, err := c.UpdateAlias(ctx, &lambda.UpdateAliasInput{
		FunctionName: aws.String(name),
		Name:         aws.String("live"),
		Description:  aws.String("updated description"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated description", aws.ToString(updated.Description))

	_, err = c.DeleteAlias(ctx, &lambda.DeleteAliasInput{
		FunctionName: aws.String(name),
		Name:         aws.String("live"),
	})
	require.NoError(t, err)

	_, err = c.GetAlias(ctx, &lambda.GetAliasInput{
		FunctionName: aws.String(name),
		Name:         aws.String("live"),
	})
	require.Error(t, err, "deleted alias must 404")
}

// TestLambda_AddRemovePermission exercises the resource-policy
// statement CRUD (AddPermission appends, GetPolicy reads the doc,
// RemovePermission deletes by Sid).
func TestLambda_AddRemovePermission(t *testing.T) {
	c := lambdaClient()
	ctx := context.Background()
	name := "subres-perm"
	lambdaCreateFn(t, c, name)

	_, err := c.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(name),
		StatementId:  aws.String("apigw-invoke"),
		Action:       aws.String("lambda:InvokeFunction"),
		Principal:    aws.String("apigateway.amazonaws.com"),
	})
	require.NoError(t, err)

	policy, err := c.GetPolicy(ctx, &lambda.GetPolicyInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(policy.Policy), `"apigw-invoke"`)
	assert.Contains(t, aws.ToString(policy.Policy), `"lambda:InvokeFunction"`)

	_, err = c.RemovePermission(ctx, &lambda.RemovePermissionInput{
		FunctionName: aws.String(name),
		StatementId:  aws.String("apigw-invoke"),
	})
	require.NoError(t, err)

	_, err = c.GetPolicy(ctx, &lambda.GetPolicyInput{FunctionName: aws.String(name)})
	require.Error(t, err, "GetPolicy must 404 when no statements remain")
}

// TestLambda_FunctionUrlConfigCRUD exercises the FunctionUrlConfig
// lifecycle (Create→Get→Update→Delete).
func TestLambda_FunctionUrlConfigCRUD(t *testing.T) {
	c := lambdaClient()
	ctx := context.Background()
	name := "subres-url"
	lambdaCreateFn(t, c, name)

	created, err := c.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String(name),
		AuthType:     types.FunctionUrlAuthTypeNone,
	})
	require.NoError(t, err)
	urlStr := aws.ToString(created.FunctionUrl)
	assert.True(t, strings.HasPrefix(urlStr, "https://"),
		"FunctionUrl must be https://; got %s", urlStr)
	assert.Contains(t, urlStr, ".lambda-url.")

	got, err := c.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, urlStr, aws.ToString(got.FunctionUrl))

	updated, err := c.UpdateFunctionUrlConfig(ctx, &lambda.UpdateFunctionUrlConfigInput{
		FunctionName: aws.String(name),
		AuthType:     types.FunctionUrlAuthTypeAwsIam,
	})
	require.NoError(t, err)
	assert.Equal(t, types.FunctionUrlAuthTypeAwsIam, updated.AuthType)

	_, err = c.DeleteFunctionUrlConfig(ctx, &lambda.DeleteFunctionUrlConfigInput{
		FunctionName: aws.String(name),
	})
	require.NoError(t, err)

	_, err = c.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{
		FunctionName: aws.String(name),
	})
	require.Error(t, err, "deleted URL config must 404")
}
