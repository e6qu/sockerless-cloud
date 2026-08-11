package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ssmCLICreateBaseline creates an AMAZON_LINUX_2 patch baseline via the CLI
// and returns its ID, registering a tolerant cleanup.
func ssmCLICreateBaseline(t *testing.T, name string) string {
	t.Helper()
	out := runCLI(t, awsCLI("ssm", "create-patch-baseline",
		"--name", name,
		"--operating-system", "AMAZON_LINUX_2",
		"--output", "json"))
	var res struct {
		BaselineId string `json:"BaselineId"`
	}
	parseJSON(t, out, &res)
	require.NotEmpty(t, res.BaselineId)
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-patch-baseline", "--baseline-id", res.BaselineId).Run()
	})
	return res.BaselineId
}

func TestSSMCLI_PatchGroupRegistration(t *testing.T) {
	blID := ssmCLICreateBaseline(t, "cli-patchgroup-baseline")
	group := "cli-group-" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")

	regOut := runCLI(t, awsCLI("ssm", "register-patch-baseline-for-patch-group",
		"--baseline-id", blID, "--patch-group", group, "--output", "json"))
	var reg struct {
		BaselineId string `json:"BaselineId"`
		PatchGroup string `json:"PatchGroup"`
	}
	parseJSON(t, regOut, &reg)
	assert.Equal(t, blID, reg.BaselineId)
	assert.Equal(t, group, reg.PatchGroup)

	getOut := runCLI(t, awsCLI("ssm", "get-patch-baseline-for-patch-group",
		"--patch-group", group, "--operating-system", "AMAZON_LINUX_2", "--output", "json"))
	var got struct {
		BaselineId      string `json:"BaselineId"`
		OperatingSystem string `json:"OperatingSystem"`
	}
	parseJSON(t, getOut, &got)
	assert.Equal(t, blID, got.BaselineId)
	assert.Equal(t, "AMAZON_LINUX_2", got.OperatingSystem)

	groupsOut := runCLI(t, awsCLI("ssm", "describe-patch-groups", "--output", "json"))
	var groups struct {
		Mappings []struct {
			PatchGroup       string `json:"PatchGroup"`
			BaselineIdentity struct {
				BaselineId string `json:"BaselineId"`
			} `json:"BaselineIdentity"`
		} `json:"Mappings"`
	}
	parseJSON(t, groupsOut, &groups)
	found := false
	for _, m := range groups.Mappings {
		if m.PatchGroup == group {
			found = true
			assert.Equal(t, blID, m.BaselineIdentity.BaselineId)
		}
	}
	assert.True(t, found, "describe-patch-groups must include the registered group")

	stateOut := runCLI(t, awsCLI("ssm", "describe-patch-group-state",
		"--patch-group", group, "--output", "json"))
	var state struct {
		Instances                   int `json:"Instances"`
		InstancesWithMissingPatches int `json:"InstancesWithMissingPatches"`
	}
	parseJSON(t, stateOut, &state)
	assert.Equal(t, 0, state.Instances)

	_ = runCLI(t, awsCLI("ssm", "deregister-patch-baseline-for-patch-group",
		"--baseline-id", blID, "--patch-group", group, "--output", "json"))
}

func TestSSMCLI_AvailablePatches(t *testing.T) {
	blID := ssmCLICreateBaseline(t, "cli-patchread-baseline")

	availOut := runCLI(t, awsCLI("ssm", "describe-available-patches", "--output", "json"))
	var avail struct {
		Patches []any `json:"Patches"`
	}
	parseJSON(t, availOut, &avail)
	assert.NotNil(t, avail.Patches)

	effOut := runCLI(t, awsCLI("ssm", "describe-effective-patches-for-patch-baseline",
		"--baseline-id", blID, "--output", "json"))
	var eff struct {
		EffectivePatches []any `json:"EffectivePatches"`
	}
	parseJSON(t, effOut, &eff)
	assert.NotNil(t, eff.EffectivePatches)

	propsOut := runCLI(t, awsCLI("ssm", "describe-patch-properties",
		"--operating-system", "AMAZON_LINUX_2", "--property", "CLASSIFICATION", "--output", "json"))
	var props struct {
		Properties []map[string]string `json:"Properties"`
	}
	parseJSON(t, propsOut, &props)
	require.NotEmpty(t, props.Properties)
	foundSecurity := false
	for _, p := range props.Properties {
		if p["CLASSIFICATION"] == "Security" {
			foundSecurity = true
		}
	}
	assert.True(t, foundSecurity)

	snapOut := runCLI(t, awsCLI("ssm", "get-deployable-patch-snapshot-for-instance",
		"--instance-id", "i-0123456789abcdef0",
		"--snapshot-id", "11111111-2222-3333-4444-555555555555", "--output", "json"))
	var snap struct {
		SnapshotId          string `json:"SnapshotId"`
		SnapshotDownloadUrl string `json:"SnapshotDownloadUrl"`
	}
	parseJSON(t, snapOut, &snap)
	assert.Equal(t, "11111111-2222-3333-4444-555555555555", snap.SnapshotId)
	assert.Contains(t, snap.SnapshotDownloadUrl, "s3")
}

func TestSSMCLI_InstancePatchStates(t *testing.T) {
	out := runCLI(t, awsCLI("ssm", "describe-instance-patch-states",
		"--instance-ids", "i-0123456789abcdef0", "--output", "json"))
	var res struct {
		InstancePatchStates []any `json:"InstancePatchStates"`
	}
	parseJSON(t, out, &res)
	assert.NotNil(t, res.InstancePatchStates)

	patchesOut := runCLI(t, awsCLI("ssm", "describe-instance-patches",
		"--instance-id", "i-0123456789abcdef0", "--output", "json"))
	var patches struct {
		Patches []any `json:"Patches"`
	}
	parseJSON(t, patchesOut, &patches)
	assert.NotNil(t, patches.Patches)

	groupOut := runCLI(t, awsCLI("ssm", "describe-instance-patch-states-for-patch-group",
		"--patch-group", "cli-some-group", "--output", "json"))
	var group struct {
		InstancePatchStates []any `json:"InstancePatchStates"`
	}
	parseJSON(t, groupOut, &group)
	assert.NotNil(t, group.InstancePatchStates)
}

func TestSSMCLI_OpsMetadata(t *testing.T) {
	resourceID := "arn:aws:resource-groups:us-east-1:123456789012:group/cli-app-" +
		strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")

	createOut := runCLI(t, awsCLI("ssm", "create-ops-metadata",
		"--resource-id", resourceID,
		"--metadata", "team={Value=runner},env={Value=prod}",
		"--output", "json"))
	var created struct {
		OpsMetadataArn string `json:"OpsMetadataArn"`
	}
	parseJSON(t, createOut, &created)
	require.NotEmpty(t, created.OpsMetadataArn)
	arn := created.OpsMetadataArn
	t.Cleanup(func() {
		_ = awsCLI("ssm", "delete-ops-metadata", "--ops-metadata-arn", arn).Run()
	})

	getOut := runCLI(t, awsCLI("ssm", "get-ops-metadata", "--ops-metadata-arn", arn, "--output", "json"))
	var got struct {
		ResourceId string `json:"ResourceId"`
		Metadata   map[string]struct {
			Value string `json:"Value"`
		} `json:"Metadata"`
	}
	parseJSON(t, getOut, &got)
	assert.Equal(t, resourceID, got.ResourceId)
	assert.Equal(t, "runner", got.Metadata["team"].Value)

	_ = runCLI(t, awsCLI("ssm", "update-ops-metadata",
		"--ops-metadata-arn", arn,
		"--metadata-to-update", "region={Value=us-east-1}",
		"--keys-to-delete", "env", "--output", "json"))

	got2Out := runCLI(t, awsCLI("ssm", "get-ops-metadata", "--ops-metadata-arn", arn, "--output", "json"))
	var got2 struct {
		Metadata map[string]struct {
			Value string `json:"Value"`
		} `json:"Metadata"`
	}
	parseJSON(t, got2Out, &got2)
	assert.Contains(t, got2.Metadata, "region")
	assert.NotContains(t, got2.Metadata, "env")

	listOut := runCLI(t, awsCLI("ssm", "list-ops-metadata", "--output", "json"))
	var listed struct {
		OpsMetadataList []struct {
			OpsMetadataArn string `json:"OpsMetadataArn"`
		} `json:"OpsMetadataList"`
	}
	parseJSON(t, listOut, &listed)
	found := false
	for _, m := range listed.OpsMetadataList {
		if m.OpsMetadataArn == arn {
			found = true
		}
	}
	assert.True(t, found, "list-ops-metadata must include the created object")

	// GetOpsSummary returns the real shape with an empty entity list.
	summaryOut := runCLI(t, awsCLI("ssm", "get-ops-summary",
		"--result-attributes", "TypeName=AWS:OpsItem", "--output", "json"))
	var summary struct {
		Entities []any `json:"Entities"`
	}
	parseJSON(t, summaryOut, &summary)
	assert.NotNil(t, summary.Entities)
}

func TestSSMCLI_ResourcePolicy(t *testing.T) {
	resourceArn := "arn:aws:ssm:us-east-1:123456789012:opsitemgroup/default"
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":["ssm:GetOpsItem"],"Resource":"*"}]}`

	putOut := runCLI(t, awsCLI("ssm", "put-resource-policy",
		"--resource-arn", resourceArn, "--policy", policy, "--output", "json"))
	var put struct {
		PolicyId   string `json:"PolicyId"`
		PolicyHash string `json:"PolicyHash"`
	}
	parseJSON(t, putOut, &put)
	require.NotEmpty(t, put.PolicyId)
	require.NotEmpty(t, put.PolicyHash)

	getOut := runCLI(t, awsCLI("ssm", "get-resource-policies",
		"--resource-arn", resourceArn, "--output", "json"))
	var got struct {
		Policies []struct {
			PolicyId   string `json:"PolicyId"`
			PolicyHash string `json:"PolicyHash"`
			Policy     string `json:"Policy"`
		} `json:"Policies"`
	}
	parseJSON(t, getOut, &got)
	require.Len(t, got.Policies, 1)
	assert.Equal(t, put.PolicyId, got.Policies[0].PolicyId)
	assert.Contains(t, got.Policies[0].Policy, "ssm:GetOpsItem")

	_ = runCLI(t, awsCLI("ssm", "delete-resource-policy",
		"--resource-arn", resourceArn,
		"--policy-id", put.PolicyId,
		"--policy-hash", put.PolicyHash, "--output", "json"))
}

func TestSSMCLI_ParameterHistory(t *testing.T) {
	name := "/cli/history/" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
	t.Cleanup(func() { _ = awsCLI("ssm", "delete-parameter", "--name", name).Run() })

	for i, v := range []string{"v1", "v2", "v3"} {
		args := []string{"ssm", "put-parameter", "--name", name, "--type", "String", "--value", v, "--output", "json"}
		if i > 0 {
			args = append(args, "--overwrite")
		}
		_ = runCLI(t, awsCLI(args...))
		// Reconcile each version into history.
		_ = runCLI(t, awsCLI("ssm", "get-parameter-history", "--name", name, "--output", "json"))
	}

	histOut := runCLI(t, awsCLI("ssm", "get-parameter-history", "--name", name, "--output", "json"))
	var hist struct {
		Parameters []struct {
			Version int64    `json:"Version"`
			Value   string   `json:"Value"`
			Labels  []string `json:"Labels"`
		} `json:"Parameters"`
	}
	parseJSON(t, histOut, &hist)
	require.GreaterOrEqual(t, len(hist.Parameters), 3)
	values := map[int64]string{}
	for _, p := range hist.Parameters {
		values[p.Version] = p.Value
	}
	assert.Equal(t, "v1", values[1])
	assert.Equal(t, "v3", values[3])
}

func TestSSMCLI_ParameterLabel(t *testing.T) {
	name := "/cli/label/" + strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
	t.Cleanup(func() { _ = awsCLI("ssm", "delete-parameter", "--name", name).Run() })

	_ = runCLI(t, awsCLI("ssm", "put-parameter", "--name", name, "--type", "String", "--value", "a", "--output", "json"))
	_ = runCLI(t, awsCLI("ssm", "get-parameter-history", "--name", name, "--output", "json"))

	lblOut := runCLI(t, awsCLI("ssm", "label-parameter-version",
		"--name", name, "--labels", "prod", "--output", "json"))
	var lbl struct {
		ParameterVersion int64    `json:"ParameterVersion"`
		InvalidLabels    []string `json:"InvalidLabels"`
	}
	parseJSON(t, lblOut, &lbl)
	assert.Equal(t, int64(1), lbl.ParameterVersion)

	unOut := runCLI(t, awsCLI("ssm", "unlabel-parameter-version",
		"--name", name, "--parameter-version", "1", "--labels", "prod", "--output", "json"))
	var un struct {
		RemovedLabels []string `json:"RemovedLabels"`
	}
	parseJSON(t, unOut, &un)
	assert.Contains(t, un.RemovedLabels, "prod")
}
