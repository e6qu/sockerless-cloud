package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSMParameterCLI_PutGetDelete(t *testing.T) {
	name := "/cli/param/" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
	putOut := runCLI(t, awsCLI("ssm", "put-parameter",
		"--name", name,
		"--type", "String",
		"--value", "first",
		"--output", "json"))
	var putResult struct {
		Version int64 `json:"Version"`
	}
	parseJSON(t, putOut, &putResult)
	require.Equal(t, int64(1), putResult.Version)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-parameter", "--name", name).Run()
	})

	getOut := runCLI(t, awsCLI("ssm", "get-parameter",
		"--name", name,
		"--output", "json"))
	var getResult struct {
		Parameter struct {
			Name    string `json:"Name"`
			Type    string `json:"Type"`
			Value   string `json:"Value"`
			Version int64  `json:"Version"`
			ARN     string `json:"ARN"`
		} `json:"Parameter"`
	}
	parseJSON(t, getOut, &getResult)
	require.Equal(t, name, getResult.Parameter.Name)
	require.Equal(t, "String", getResult.Parameter.Type)
	require.Equal(t, "first", getResult.Parameter.Value)
	require.Equal(t, int64(1), getResult.Parameter.Version)
	require.Contains(t, getResult.Parameter.ARN, ":parameter"+name)

	overwriteOut := runCLI(t, awsCLI("ssm", "put-parameter",
		"--name", name,
		"--type", "String",
		"--value", "second",
		"--overwrite",
		"--output", "json"))
	var overwriteResult struct {
		Version int64 `json:"Version"`
	}
	parseJSON(t, overwriteOut, &overwriteResult)
	require.Equal(t, int64(2), overwriteResult.Version)

	runCLI(t, awsCLI("ssm", "delete-parameter",
		"--name", name,
		"--output", "json"))
}

func TestSSMParameterCLI_ListTagsForResource(t *testing.T) {
	name := "/cli-tag-test/param"
	runCLI(t, awsCLI("ssm", "put-parameter",
		"--name", name,
		"--type", "String",
		"--value", "v",
		"--output", "json"))
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-parameter", "--name", name).Run()
	})

	// No tags yet — TagList must be an empty array, not absent.
	emptyOut := runCLI(t, awsCLI("ssm", "list-tags-for-resource",
		"--resource-type", "Parameter",
		"--resource-id", name,
		"--output", "json"))
	var empty struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	parseJSON(t, emptyOut, &empty)
	assert.NotNil(t, empty.TagList)
	assert.Empty(t, empty.TagList)

	// Add a tag then verify it appears in the list.
	runCLI(t, awsCLI("ssm", "add-tags-to-resource",
		"--resource-type", "Parameter",
		"--resource-id", name,
		"--tags", `Key=stage,Value=prod`,
		"--output", "json"))

	tagOut := runCLI(t, awsCLI("ssm", "list-tags-for-resource",
		"--resource-type", "Parameter",
		"--resource-id", name,
		"--output", "json"))
	var tagged struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	parseJSON(t, tagOut, &tagged)
	require.Len(t, tagged.TagList, 1)
	assert.Equal(t, "stage", tagged.TagList[0].Key)
	assert.Equal(t, "prod", tagged.TagList[0].Value)
}

// TestSSMParameterCLI_ListTagsForResourceWithoutLeadingSlash pins the
// resolution the Terraform provider depends on: a parameter is stored under a
// leading slash, and a resource id given without one still names it. The tag
// read back is what proves the name resolved — an unresolved id would answer
// with an empty list just as convincingly.
func TestSSMParameterCLI_ListTagsForResourceWithoutLeadingSlash(t *testing.T) {
	name := "terraform-compatible-parameter"
	runCLI(t, awsCLI("ssm", "put-parameter", "--name", name, "--type", "String", "--value", "v", "--output", "json"))
	t.Cleanup(func() { _ = awsCLI("ssm", "delete-parameter", "--name", name).Run() })
	runCLI(t, awsCLI("ssm", "add-tags-to-resource",
		"--resource-type", "Parameter", "--resource-id", name,
		"--tags", "Key=owner,Value=terraform", "--output", "json"))

	out := runCLI(t, awsCLI("ssm", "list-tags-for-resource",
		"--resource-type", "Parameter", "--resource-id", name, "--output", "json"))
	var tagged struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	parseJSON(t, out, &tagged)
	require.Len(t, tagged.TagList, 1, "the slashless resource id must resolve to the tagged parameter")
	assert.Equal(t, "owner", tagged.TagList[0].Key)
	assert.Equal(t, "terraform", tagged.TagList[0].Value)

	// A name no parameter carries does not resolve, so the read above is
	// resolution rather than indifference.
	missing := runCLIExpectError(t, awsCLI("ssm", "list-tags-for-resource",
		"--resource-type", "Parameter", "--resource-id", name+"-absent", "--output", "json"))
	assert.Contains(t, missing, "InvalidResourceId")
}
