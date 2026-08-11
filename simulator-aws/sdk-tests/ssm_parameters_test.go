package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ssmClient() *ssm.Client {
	return ssm.NewFromConfig(sdkConfig(), func(o *ssm.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestSSMParameter_PutGetDelete pins the canonical Parameter Store
// flow runner setups use: write a config value, fetch it from a
// workflow step, delete it on tear-down.
func TestSSMParameter_PutGetDelete(t *testing.T) {
	c := ssmClient()

	// PUT a String parameter.
	put, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:  aws.String("/runner/test-config"),
		Type:  ssmtypes.ParameterTypeString,
		Value: aws.String("hello"),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), put.Version)

	// GET it back.
	got, err := c.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String("/runner/test-config"),
	})
	require.NoError(t, err)
	require.NotNil(t, got.Parameter)
	assert.Equal(t, "hello", *got.Parameter.Value)
	assert.Equal(t, ssmtypes.ParameterTypeString, got.Parameter.Type)
	assert.Contains(t, *got.Parameter.ARN, "arn:aws:ssm:")
	assert.Equal(t, int64(1), got.Parameter.Version)
	assert.Equal(t, "text", aws.ToString(got.Parameter.DataType))
	assert.NotNil(t, got.Parameter.LastModifiedDate)

	// PUT again without Overwrite=true must fail.
	_, err = c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:  aws.String("/runner/test-config"),
		Type:  ssmtypes.ParameterTypeString,
		Value: aws.String("collision"),
	})
	require.Error(t, err, "second PUT without Overwrite must fail like real Parameter Store")

	// PUT with Overwrite=true should succeed and bump Version.
	rotated, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String("/runner/test-config"),
		Type:      ssmtypes.ParameterTypeString,
		Value:     aws.String("rotated"),
		Overwrite: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), rotated.Version)

	// Cleanup.
	_, err = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{
		Name: aws.String("/runner/test-config"),
	})
	require.NoError(t, err)
}

// TestSSMParameter_GetParametersByPath pins the hierarchy lookup that
// terraform's `aws_ssm_parameters_by_path` data source and runner
// bulk-fetch flows use.
func TestSSMParameter_GetParametersByPath(t *testing.T) {
	c := ssmClient()

	// Seed: /env/prod/db, /env/prod/cache, /env/staging/db
	for _, p := range []struct{ name, val string }{
		{"/env/prod/db", "prod-db"},
		{"/env/prod/cache", "prod-cache"},
		{"/env/staging/db", "staging-db"},
	} {
		_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
			Name:  aws.String(p.name),
			Type:  ssmtypes.ParameterTypeString,
			Value: aws.String(p.val),
		})
		require.NoError(t, err)
		defer c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(p.name)})
	}

	// Non-recursive: returns direct children of /env/prod only.
	flat, err := c.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{
		Path:      aws.String("/env/prod"),
		Recursive: aws.Bool(false),
	})
	require.NoError(t, err)
	names := make(map[string]string)
	for _, p := range flat.Parameters {
		names[*p.Name] = *p.Value
	}
	assert.Equal(t, "prod-db", names["/env/prod/db"])
	assert.Equal(t, "prod-cache", names["/env/prod/cache"])
	assert.NotContains(t, names, "/env/staging/db", "Non-recursive must skip staging branch")

	// Recursive: returns all under /env.
	deep, err := c.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{
		Path:      aws.String("/env"),
		Recursive: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(deep.Parameters), 3, "Recursive must return all 3 seeded parameters")
}

func TestSSMParameter_Describe(t *testing.T) {
	c := ssmClient()
	defer c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String("/desc/p1")})

	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:  aws.String("/desc/p1"),
		Type:  ssmtypes.ParameterTypeString,
		Value: aws.String("v"),
	})
	require.NoError(t, err)

	out, err := c.DescribeParameters(ctx, &ssm.DescribeParametersInput{})
	require.NoError(t, err)
	found := false
	for _, p := range out.Parameters {
		if p.Name != nil && *p.Name == "/desc/p1" {
			found = true
			// Tier is a ParameterMetadata member — it rides
			// DescribeParameters, not the Get* Parameter shape.
			assert.Equal(t, ssmtypes.ParameterTierStandard, p.Tier)
		}
	}
	assert.True(t, found, "DescribeParameters must include the seeded parameter")
}

func TestSSMParameter_DeleteParameters(t *testing.T) {
	c := ssmClient()
	for _, n := range []string{"/del/a", "/del/b"} {
		_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
			Name: aws.String(n), Type: ssmtypes.ParameterTypeString, Value: aws.String("v"),
		})
		require.NoError(t, err)
	}
	out, err := c.DeleteParameters(ctx, &ssm.DeleteParametersInput{
		Names: []string{"/del/a", "/del/b", "/del/missing"},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"/del/a", "/del/b"}, out.DeletedParameters)
	assert.Equal(t, []string{"/del/missing"}, out.InvalidParameters)
}

func TestSSMParameter_GetParametersAndRemoveTags(t *testing.T) {
	c := ssmClient()

	for _, p := range []struct{ name, val string }{
		{"/bulk/a", "one"},
		{"/bulk/b", "two"},
	} {
		_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
			Name:  aws.String(p.name),
			Type:  ssmtypes.ParameterTypeString,
			Value: aws.String(p.val),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(p.name)})
		})
	}

	got, err := c.GetParameters(ctx, &ssm.GetParametersInput{
		Names: []string{"/bulk/a", "/bulk/b", "/bulk/missing"},
	})
	require.NoError(t, err)
	values := map[string]string{}
	for _, p := range got.Parameters {
		values[aws.ToString(p.Name)] = aws.ToString(p.Value)
	}
	assert.Equal(t, "one", values["/bulk/a"])
	assert.Equal(t, "two", values["/bulk/b"])
	assert.Equal(t, []string{"/bulk/missing"}, got.InvalidParameters)

	_, err = c.AddTagsToResource(ctx, &ssm.AddTagsToResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String("/bulk/a"),
		Tags: []ssmtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("sdk")},
			{Key: aws.String("team"), Value: aws.String("runner")},
		},
	})
	require.NoError(t, err)
	_, err = c.RemoveTagsFromResource(ctx, &ssm.RemoveTagsFromResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String("/bulk/a"),
		TagKeys:      []string{"team"},
	})
	require.NoError(t, err)
	tags, err := c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String("/bulk/a"),
	})
	require.NoError(t, err)
	tagMap := map[string]string{}
	for _, tag := range tags.TagList {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "sdk", tagMap["env"])
	assert.NotContains(t, tagMap, "team")
}

// TestSSMParameter_ListTagsForResource verifies the ListTagsForResource round
// trip. terraform-provider-aws calls this after PutParameter to populate the
// resource's `tags` attribute; an absent TagList (instead of []) causes the
// provider to treat the parameter as untagged and removes the tags on the next
// plan, causing perpetual drift.
func TestSSMParameter_ListTagsForResource(t *testing.T) {
	c := ssmClient()

	paramName := "/tag-test/list-tags"
	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{
		Name:  aws.String(paramName),
		Type:  ssmtypes.ParameterTypeString,
		Value: aws.String("v"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(paramName)}) })

	// No tags yet — must return an empty (non-nil) TagList.
	emptyOut, err := c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(paramName),
	})
	require.NoError(t, err)
	assert.NotNil(t, emptyOut.TagList, "TagList must be non-nil (empty slice) when no tags are set")
	assert.Empty(t, emptyOut.TagList)

	// Attach tags via AddTagsToResource.
	_, err = c.AddTagsToResource(ctx, &ssm.AddTagsToResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(paramName),
		Tags: []ssmtypes.Tag{
			{Key: aws.String("env"), Value: aws.String("test")},
			{Key: aws.String("owner"), Value: aws.String("ci")},
		},
	})
	require.NoError(t, err)

	// ListTagsForResource must return exactly the attached tags.
	tagOut, err := c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{
		ResourceType: ssmtypes.ResourceTypeForTaggingParameter,
		ResourceId:   aws.String(paramName),
	})
	require.NoError(t, err)
	require.Len(t, tagOut.TagList, 2)
	tagMap := make(map[string]string, len(tagOut.TagList))
	for _, tag := range tagOut.TagList {
		tagMap[*tag.Key] = *tag.Value
	}
	assert.Equal(t, "test", tagMap["env"])
	assert.Equal(t, "ci", tagMap["owner"])
}

func TestSSMParameter_ListTagsForResourceWithoutLeadingSlash(t *testing.T) {
	c := ssmClient()
	paramName := "terraform-compatible-parameter"
	_, err := c.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(paramName), Type: ssmtypes.ParameterTypeString, Value: aws.String("v")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(paramName)}) })
	_, err = c.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{ResourceType: ssmtypes.ResourceTypeForTaggingParameter, ResourceId: aws.String(paramName)})
	require.NoError(t, err)
}

func TestSSM_DescribeParameters_Pagination(t *testing.T) {
	client := ssmClient()
	names := []string{"/pag/ssm/a", "/pag/ssm/b", "/pag/ssm/c"}
	for _, n := range names {
		_, err := client.PutParameter(ctx, &ssm.PutParameterInput{
			Name:      aws.String(n),
			Value:     aws.String("v"),
			Type:      ssmtypes.ParameterTypeString,
			Overwrite: aws.Bool(true),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			client.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(n)})
		})
	}

	seen := map[string]bool{}
	pager := ssm.NewDescribeParametersPaginator(client, &ssm.DescribeParametersInput{MaxResults: aws.Int32(1)})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		require.NoError(t, err)
		for _, p := range page.Parameters {
			seen[aws.ToString(p.Name)] = true
		}
	}
	for _, n := range names {
		assert.True(t, seen[n], "parameter %s should appear via pagination", n)
	}
}
