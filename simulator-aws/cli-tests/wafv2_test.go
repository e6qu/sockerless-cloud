package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wafStamp is a unique suffix built only from characters an AWS WAFV2 name
// admits. The model's EntityName pattern is ^[\w\-]+$, so the fractional-
// seconds dot the other suites use would be rejected by the service — and the
// load-balancer names built here forbid it too.
func wafStamp() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
}

func TestWAFv2_WebACL_Lifecycle(t *testing.T) {
	name := "cli-acl-" + wafStamp()
	out := runCLI(t, awsCLI("wafv2", "create-web-acl",
		"--name", name,
		"--scope", "CLOUDFRONT",
		"--default-action", `{"Allow":{}}`,
		"--visibility-config", fmt.Sprintf(`{"SampledRequestsEnabled":true,"CloudWatchMetricsEnabled":true,"MetricName":"%s-metric"}`, name),
		"--output", "json",
	))
	var createResult struct {
		Summary struct {
			Id        string `json:"Id"`
			ARN       string `json:"ARN"`
			LockToken string `json:"LockToken"`
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.Summary.Id)
	require.NotEmpty(t, createResult.Summary.LockToken)

	id := createResult.Summary.Id
	lock := createResult.Summary.LockToken

	getOut := runCLI(t, awsCLI("wafv2", "get-web-acl",
		"--name", name, "--scope", "CLOUDFRONT", "--id", id, "--output", "json",
	))
	var getResult struct {
		WebACL struct {
			Name string `json:"Name"`
		} `json:"WebACL"`
		LockToken string `json:"LockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Equal(t, name, getResult.WebACL.Name)

	runCLI(t, awsCLI("wafv2", "list-web-acls", "--scope", "CLOUDFRONT", "--output", "json"))

	runCLI(t, awsCLI("wafv2", "delete-web-acl",
		"--name", name, "--scope", "CLOUDFRONT", "--id", id,
		"--lock-token", lock,
	))
}

func TestWAFv2_RevenueSurfacesWithoutSettlements_CLI(t *testing.T) {
	start := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	end := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	window := fmt.Sprintf(`{"StartTime":"%s","EndTime":"%s"}`, start, end)

	ranked := runCLI(t, awsCLI("wafv2", "get-revenue-statistics",
		"--statistic-type", "TOP_SOURCES_BY_REVENUE",
		"--group-by", "NAME",
		"--time-window", window,
		"--scope", "CLOUDFRONT",
		"--currency", "USDC",
		"--output", "json"))
	var rankedResult struct {
		SourceStatistics []json.RawMessage `json:"SourceStatistics"`
	}
	require.NoError(t, json.Unmarshal([]byte(ranked), &rankedResult))
	require.Empty(t, rankedResult.SourceStatistics)

	summary := runCLI(t, awsCLI("wafv2", "get-revenue-statistics-summary",
		"--time-window", window,
		"--scope", "CLOUDFRONT",
		"--currency", "USDC",
		"--output", "json"))
	var summaryResult struct {
		RevenueBreakdown struct {
			TotalAmount  string `json:"TotalAmount"`
			TotalSettled int64  `json:"TotalSettled"`
		} `json:"RevenueBreakdown"`
	}
	require.NoError(t, json.Unmarshal([]byte(summary), &summaryResult))
	require.Equal(t, "0", summaryResult.RevenueBreakdown.TotalAmount)
	require.Zero(t, summaryResult.RevenueBreakdown.TotalSettled)

	series := runCLI(t, awsCLI("wafv2", "get-revenue-statistics-time-series",
		"--statistic-type", "DATE_HISTOGRAM",
		"--interval", "HOURLY",
		"--time-window", window,
		"--scope", "CLOUDFRONT",
		"--currency", "USDC",
		"--output", "json"))
	var seriesResult struct {
		DataPoints []json.RawMessage `json:"DataPoints"`
	}
	require.NoError(t, json.Unmarshal([]byte(series), &seriesResult))
	require.Empty(t, seriesResult.DataPoints)

	settlements := runCLI(t, awsCLI("wafv2", "list-settlement-records",
		"--time-window", window,
		"--scope", "CLOUDFRONT",
		"--currency", "USDC",
		"--output", "json"))
	var settlementResult struct {
		Settlements []json.RawMessage `json:"Settlements"`
	}
	require.NoError(t, json.Unmarshal([]byte(settlements), &settlementResult))
	require.Empty(t, settlementResult.Settlements)
}

func TestWAFv2_IPSet_Lifecycle(t *testing.T) {
	name := "cli-ipset-" + wafStamp()
	out := runCLI(t, awsCLI("wafv2", "create-ip-set",
		"--name", name, "--scope", "CLOUDFRONT",
		"--ip-address-version", "IPV4",
		"--addresses", "203.0.113.0/24", "198.51.100.10/32",
		"--output", "json",
	))
	var createResult struct {
		Summary struct {
			Id        string `json:"Id"`
			LockToken string `json:"LockToken"`
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	id := createResult.Summary.Id
	lock := createResult.Summary.LockToken

	runCLI(t, awsCLI("wafv2", "get-ip-set",
		"--name", name, "--scope", "CLOUDFRONT", "--id", id, "--output", "json"))

	runCLI(t, awsCLI("wafv2", "delete-ip-set",
		"--name", name, "--scope", "CLOUDFRONT", "--id", id,
		"--lock-token", lock,
	))
}

func TestWAFv2_LoggingConfiguration_Lifecycle(t *testing.T) {
	name := "cli-log-" + wafStamp()
	out := runCLI(t, awsCLI("wafv2", "create-web-acl",
		"--name", name,
		"--scope", "CLOUDFRONT",
		"--default-action", `{"Allow":{}}`,
		"--visibility-config", fmt.Sprintf(`{"SampledRequestsEnabled":true,"CloudWatchMetricsEnabled":true,"MetricName":"%s-metric"}`, name),
		"--output", "json",
	))
	var createResult struct {
		Summary struct {
			Id        string `json:"Id"`
			ARN       string `json:"ARN"`
			LockToken string `json:"LockToken"`
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	aclARN := createResult.Summary.ARN
	require.NotEmpty(t, aclARN)
	id := createResult.Summary.Id
	lock := createResult.Summary.LockToken
	t.Cleanup(func() {
		_ = awsCLI("wafv2", "delete-web-acl",
			"--name", name, "--scope", "CLOUDFRONT", "--id", id, "--lock-token", lock).Run()
	})

	logDest := "arn:aws:logs:us-east-1:123456789012:log-group:aws-waf-logs-" + name
	loggingConfig := fmt.Sprintf(`{"ResourceArn":"%s","LogDestinationConfigs":["%s"]}`, aclARN, logDest)

	putOut := runCLI(t, awsCLI("wafv2", "put-logging-configuration",
		"--logging-configuration", loggingConfig, "--output", "json"))
	var putResult struct {
		LoggingConfiguration struct {
			ResourceArn           string   `json:"ResourceArn"`
			LogDestinationConfigs []string `json:"LogDestinationConfigs"`
		} `json:"LoggingConfiguration"`
	}
	require.NoError(t, json.Unmarshal([]byte(putOut), &putResult))
	require.Equal(t, aclARN, putResult.LoggingConfiguration.ResourceArn)
	require.Equal(t, []string{logDest}, putResult.LoggingConfiguration.LogDestinationConfigs)

	getOut := runCLI(t, awsCLI("wafv2", "get-logging-configuration",
		"--resource-arn", aclARN, "--output", "json"))
	var getResult struct {
		LoggingConfiguration struct {
			ResourceArn string `json:"ResourceArn"`
		} `json:"LoggingConfiguration"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Equal(t, aclARN, getResult.LoggingConfiguration.ResourceArn)

	runCLI(t, awsCLI("wafv2", "list-logging-configurations",
		"--scope", "CLOUDFRONT", "--output", "json"))

	runCLI(t, awsCLI("wafv2", "delete-logging-configuration",
		"--resource-arn", aclARN, "--output", "json"))
}

// TestWAFv2_APIKeys_And_Capacity exercises the API-key control plane and the
// CheckCapacity estimator: create-api-key, list-api-keys, get-decrypted-api-key,
// check-capacity, delete-api-key.
func TestWAFv2_APIKeys_And_Capacity(t *testing.T) {
	createOut := runCLI(t, awsCLI("wafv2", "create-api-key",
		"--scope", "CLOUDFRONT",
		"--token-domains", "example.com", "app.example.com",
		"--output", "json"))
	var createResult struct {
		APIKey string `json:"APIKey"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &createResult))
	require.NotEmpty(t, createResult.APIKey)
	apiKey := createResult.APIKey
	defer func() {
		wafCleanupCLI(awsCLI("wafv2", "delete-api-key", "--scope", "CLOUDFRONT", "--api-key", apiKey, "--output", "json"))
	}()

	listOut := runCLI(t, awsCLI("wafv2", "list-api-keys",
		"--scope", "CLOUDFRONT", "--output", "json"))
	var listResult struct {
		APIKeySummaries []struct {
			APIKey       string   `json:"APIKey"`
			TokenDomains []string `json:"TokenDomains"`
			Version      int      `json:"Version"`
		} `json:"APIKeySummaries"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	found := false
	for _, s := range listResult.APIKeySummaries {
		if s.APIKey == apiKey {
			found = true
			require.ElementsMatch(t, []string{"example.com", "app.example.com"}, s.TokenDomains)
		}
	}
	require.True(t, found, "list-api-keys must include the created key")

	decOut := runCLI(t, awsCLI("wafv2", "get-decrypted-api-key",
		"--scope", "CLOUDFRONT", "--api-key", apiKey, "--output", "json"))
	var decResult struct {
		TokenDomains []string `json:"TokenDomains"`
	}
	require.NoError(t, json.Unmarshal([]byte(decOut), &decResult))
	require.ElementsMatch(t, []string{"example.com", "app.example.com"}, decResult.TokenDomains)

	rules := `[{"Name":"r1","Priority":0,"Statement":{"ByteMatchStatement":{"SearchString":"YWRtaW4=","FieldToMatch":{"UriPath":{}},"TextTransformations":[{"Priority":0,"Type":"NONE"}],"PositionalConstraint":"CONTAINS"}},"Action":{"Block":{}},"VisibilityConfig":{"SampledRequestsEnabled":false,"CloudWatchMetricsEnabled":false,"MetricName":"r1"}}]`
	capOut := runCLI(t, awsCLI("wafv2", "check-capacity",
		"--scope", "CLOUDFRONT", "--rules", rules, "--output", "json"))
	var capResult struct {
		Capacity int64 `json:"Capacity"`
	}
	require.NoError(t, json.Unmarshal([]byte(capOut), &capResult))
	require.Greater(t, capResult.Capacity, int64(0))

	runCLI(t, awsCLI("wafv2", "delete-api-key",
		"--scope", "CLOUDFRONT", "--api-key", apiKey, "--output", "json"))
}

// TestWAFv2_ManagedRuleGroupCatalog exercises the read-only managed rule group /
// product catalog: list-available-managed-rule-groups,
// list-available-managed-rule-group-versions, describe-managed-rule-group,
// describe-all-managed-products, describe-managed-products-by-vendor.
func TestWAFv2_ManagedRuleGroupCatalog(t *testing.T) {
	listOut := runCLI(t, awsCLI("wafv2", "list-available-managed-rule-groups",
		"--scope", "CLOUDFRONT", "--output", "json"))
	var listResult struct {
		ManagedRuleGroups []struct {
			Name       string `json:"Name"`
			VendorName string `json:"VendorName"`
		} `json:"ManagedRuleGroups"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	hasCommon := false
	for _, g := range listResult.ManagedRuleGroups {
		if g.Name == "AWSManagedRulesCommonRuleSet" && g.VendorName == "AWS" {
			hasCommon = true
		}
	}
	require.True(t, hasCommon)

	verOut := runCLI(t, awsCLI("wafv2", "list-available-managed-rule-group-versions",
		"--scope", "CLOUDFRONT", "--vendor-name", "AWS",
		"--name", "AWSManagedRulesCommonRuleSet", "--output", "json"))
	var verResult struct {
		Versions              []struct{ Name string } `json:"Versions"`
		CurrentDefaultVersion string                  `json:"CurrentDefaultVersion"`
	}
	require.NoError(t, json.Unmarshal([]byte(verOut), &verResult))
	require.NotEmpty(t, verResult.Versions)
	require.NotEmpty(t, verResult.CurrentDefaultVersion)

	descOut := runCLI(t, awsCLI("wafv2", "describe-managed-rule-group",
		"--scope", "CLOUDFRONT", "--vendor-name", "AWS",
		"--name", "AWSManagedRulesCommonRuleSet", "--output", "json"))
	var descResult struct {
		Capacity        int64 `json:"Capacity"`
		Rules           []any `json:"Rules"`
		AvailableLabels []any `json:"AvailableLabels"`
	}
	require.NoError(t, json.Unmarshal([]byte(descOut), &descResult))
	require.Greater(t, descResult.Capacity, int64(0))
	require.NotEmpty(t, descResult.Rules)

	allOut := runCLI(t, awsCLI("wafv2", "describe-all-managed-products",
		"--scope", "CLOUDFRONT", "--output", "json"))
	var allResult struct {
		ManagedProducts []any `json:"ManagedProducts"`
	}
	require.NoError(t, json.Unmarshal([]byte(allOut), &allResult))
	require.NotEmpty(t, allResult.ManagedProducts)

	vendOut := runCLI(t, awsCLI("wafv2", "describe-managed-products-by-vendor",
		"--scope", "CLOUDFRONT", "--vendor-name", "AWS", "--output", "json"))
	var vendResult struct {
		ManagedProducts []struct {
			VendorName string `json:"VendorName"`
		} `json:"ManagedProducts"`
	}
	require.NoError(t, json.Unmarshal([]byte(vendOut), &vendResult))
	require.NotEmpty(t, vendResult.ManagedProducts)
}

// TestWAFv2_ManagedRuleSet exercises the publisher-side managed rule set CRUD:
// list-managed-rule-sets, get-managed-rule-set, put-managed-rule-set-versions,
// update-managed-rule-set-version-expiry-date.
func TestWAFv2_ManagedRuleSet(t *testing.T) {
	listOut := runCLI(t, awsCLI("wafv2", "list-managed-rule-sets",
		"--scope", "CLOUDFRONT", "--output", "json"))
	var listResult struct {
		ManagedRuleSets []struct {
			Id        string `json:"Id"`
			Name      string `json:"Name"`
			LockToken string `json:"LockToken"`
		} `json:"ManagedRuleSets"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	require.NotEmpty(t, listResult.ManagedRuleSets, "the sim seeds a managed rule set per scope")
	seed := listResult.ManagedRuleSets[0]
	id := seed.Id
	name := seed.Name
	rgARN := "arn:aws:wafv2:us-east-1:123456789012:global/rulegroup/backing/" + id

	versions := fmt.Sprintf(`{"Version_2.0":{"AssociatedRuleGroupArn":"%s","ForecastedLifetime":60}}`, rgARN)
	putOut := runCLI(t, awsCLI("wafv2", "put-managed-rule-set-versions",
		"--scope", "CLOUDFRONT", "--name", name, "--id", id,
		"--lock-token", seed.LockToken,
		"--recommended-version", "Version_2.0",
		"--versions-to-publish", versions, "--output", "json"))
	var putResult struct {
		NextLockToken string `json:"NextLockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(putOut), &putResult))
	require.NotEmpty(t, putResult.NextLockToken)

	getOut := runCLI(t, awsCLI("wafv2", "get-managed-rule-set",
		"--scope", "CLOUDFRONT", "--name", name, "--id", id, "--output", "json"))
	var getResult struct {
		ManagedRuleSet struct {
			PublishedVersions map[string]any `json:"PublishedVersions"`
		} `json:"ManagedRuleSet"`
		LockToken string `json:"LockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Contains(t, getResult.ManagedRuleSet.PublishedVersions, "Version_2.0")

	expiry := time.Now().Add(48 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	updOut := runCLI(t, awsCLI("wafv2", "update-managed-rule-set-version-expiry-date",
		"--scope", "CLOUDFRONT", "--name", name, "--id", id,
		"--lock-token", getResult.LockToken,
		"--version-to-expire", "Version_2.0",
		"--expiry-timestamp", expiry, "--output", "json"))
	var updResult struct {
		ExpiringVersion string `json:"ExpiringVersion"`
		NextLockToken   string `json:"NextLockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(updOut), &updResult))
	require.Equal(t, "Version_2.0", updResult.ExpiringVersion)
	require.NotEmpty(t, updResult.NextLockToken)
}

// TestWAFv2_PermissionPolicyAndStats exercises permission-policy CRUD on a rule
// group, the mobile SDK release catalog, delete-firewall-manager-rule-groups,
// get-rate-based-statement-managed-keys, and get-top-path-statistics-by-traffic.
func TestWAFv2_PermissionPolicyAndStats(t *testing.T) {
	ts := wafStamp()

	rgOut := runCLI(t, awsCLI("wafv2", "create-rule-group",
		"--name", "cli-pp-rg-"+ts, "--scope", "CLOUDFRONT", "--capacity", "50",
		"--visibility-config", `{"SampledRequestsEnabled":false,"CloudWatchMetricsEnabled":false,"MetricName":"ppm"}`,
		"--output", "json"))
	var rgResult struct {
		Summary struct {
			Id, ARN, LockToken string
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(rgOut), &rgResult))
	rgARN := rgResult.Summary.ARN
	defer func() {
		wafCleanupCLI(awsCLI("wafv2", "delete-rule-group", "--name", "cli-pp-rg-"+ts, "--scope", "CLOUDFRONT", "--id", rgResult.Summary.Id, "--lock-token", rgResult.Summary.LockToken))
	}()

	policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":["wafv2:GetRuleGroup"],"Resource":"%s"}]}`, rgARN)
	runCLI(t, awsCLI("wafv2", "put-permission-policy",
		"--resource-arn", rgARN, "--policy", policy, "--output", "json"))

	getPP := runCLI(t, awsCLI("wafv2", "get-permission-policy",
		"--resource-arn", rgARN, "--output", "json"))
	var ppResult struct {
		Policy string `json:"Policy"`
	}
	require.NoError(t, json.Unmarshal([]byte(getPP), &ppResult))
	require.Equal(t, policy, ppResult.Policy)

	runCLI(t, awsCLI("wafv2", "delete-permission-policy",
		"--resource-arn", rgARN, "--output", "json"))

	// Mobile SDK release catalog.
	relListOut := runCLI(t, awsCLI("wafv2", "list-mobile-sdk-releases",
		"--platform", "IOS", "--output", "json"))
	var relList struct {
		ReleaseSummaries []struct {
			ReleaseVersion string `json:"ReleaseVersion"`
		} `json:"ReleaseSummaries"`
	}
	require.NoError(t, json.Unmarshal([]byte(relListOut), &relList))
	require.NotEmpty(t, relList.ReleaseSummaries)
	relVer := relList.ReleaseSummaries[0].ReleaseVersion

	getRelOut := runCLI(t, awsCLI("wafv2", "get-mobile-sdk-release",
		"--platform", "IOS", "--release-version", relVer, "--output", "json"))
	var getRel struct {
		MobileSdkRelease struct {
			ReleaseVersion string `json:"ReleaseVersion"`
		} `json:"MobileSdkRelease"`
	}
	require.NoError(t, json.Unmarshal([]byte(getRelOut), &getRel))
	require.Equal(t, relVer, getRel.MobileSdkRelease.ReleaseVersion)

	urlOut := runCLI(t, awsCLI("wafv2", "generate-mobile-sdk-release-url",
		"--platform", "IOS", "--release-version", relVer, "--output", "json"))
	var urlResult struct {
		Url string `json:"Url"`
	}
	require.NoError(t, json.Unmarshal([]byte(urlOut), &urlResult))
	require.NotEmpty(t, urlResult.Url)

	// Web ACL → rate-based keys + traffic stats + firewall-manager delete.
	aclOut := runCLI(t, awsCLI("wafv2", "create-web-acl",
		"--name", "cli-pp-acl-"+ts, "--scope", "CLOUDFRONT",
		"--default-action", `{"Allow":{}}`,
		"--visibility-config", `{"SampledRequestsEnabled":false,"CloudWatchMetricsEnabled":false,"MetricName":"m"}`,
		"--output", "json"))
	var aclResult struct {
		Summary struct {
			Id, ARN, LockToken string
		} `json:"Summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(aclOut), &aclResult))
	aclID := aclResult.Summary.Id
	aclARN := aclResult.Summary.ARN
	aclName := "cli-pp-acl-" + ts

	rbkOut := runCLI(t, awsCLI("wafv2", "get-rate-based-statement-managed-keys",
		"--scope", "CLOUDFRONT", "--web-acl-name", aclName, "--web-acl-id", aclID,
		"--rule-name", "any-rate-rule", "--output", "json"))
	var rbkResult struct {
		ManagedKeysIPV4 struct {
			IPAddressVersion string `json:"IPAddressVersion"`
		} `json:"ManagedKeysIPV4"`
	}
	require.NoError(t, json.Unmarshal([]byte(rbkOut), &rbkResult))
	require.Equal(t, "IPV4", rbkResult.ManagedKeysIPV4.IPAddressVersion)

	start := time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	end := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	statsOut := runCLI(t, awsCLI("wafv2", "get-top-path-statistics-by-traffic",
		"--scope", "CLOUDFRONT", "--web-acl-arn", aclARN,
		"--limit", "10", "--number-of-top-traffic-bots-per-path", "5",
		"--time-window", fmt.Sprintf(`{"StartTime":"%s","EndTime":"%s"}`, start, end),
		"--output", "json"))
	var statsResult struct {
		TotalRequestCount int64 `json:"TotalRequestCount"`
	}
	require.NoError(t, json.Unmarshal([]byte(statsOut), &statsResult))

	// delete-firewall-manager-rule-groups advances the web ACL lock token.
	getACL := runCLI(t, awsCLI("wafv2", "get-web-acl",
		"--name", aclName, "--scope", "CLOUDFRONT", "--id", aclID, "--output", "json"))
	var getACLResult struct {
		LockToken string `json:"LockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(getACL), &getACLResult))

	fmOut := runCLI(t, awsCLI("wafv2", "delete-firewall-manager-rule-groups",
		"--web-acl-arn", aclARN, "--web-acl-lock-token", getACLResult.LockToken, "--output", "json"))
	var fmResult struct {
		NextWebACLLockToken string `json:"NextWebACLLockToken"`
	}
	require.NoError(t, json.Unmarshal([]byte(fmOut), &fmResult))
	require.NotEmpty(t, fmResult.NextWebACLLockToken)

	wafCleanupCLI(awsCLI("wafv2", "delete-web-acl", "--name", aclName, "--scope", "CLOUDFRONT", "--id", aclID, "--lock-token", fmResult.NextWebACLLockToken))
}

// wafCleanupCLI runs a tolerant cleanup CLI command, ignoring any error so a
// deferred teardown never fails the test.
func wafCleanupCLI(cmd *exec.Cmd) {
	_ = cmd.Run()
}
