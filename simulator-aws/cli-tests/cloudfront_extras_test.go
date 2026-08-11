package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCloudFront_OriginAccessIdentity_Lifecycle exercises the legacy
// pre-OAC origin access identity via the aws CLI:
// create → get → get-config → update → list → delete.
func TestCloudFront_OriginAccessIdentity_Lifecycle(t *testing.T) {
	caller := "cli-oai-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{"CallerReference": "%s", "Comment": "cli oai"}`, caller)

	out := runCLI(t, awsCLI("cloudfront", "create-cloud-front-origin-access-identity",
		"--cloud-front-origin-access-identity-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		CloudFrontOriginAccessIdentity struct {
			Id                string `json:"Id"`
			S3CanonicalUserId string `json:"S3CanonicalUserId"`
		} `json:"CloudFrontOriginAccessIdentity"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.CloudFrontOriginAccessIdentity.Id)
	require.NotEmpty(t, createResult.CloudFrontOriginAccessIdentity.S3CanonicalUserId)
	require.NotEmpty(t, createResult.ETag)

	id := createResult.CloudFrontOriginAccessIdentity.Id

	getOut := runCLI(t, awsCLI("cloudfront", "get-cloud-front-origin-access-identity", "--id", id, "--output", "json"))
	var getResult struct {
		CloudFrontOriginAccessIdentity struct {
			Id string `json:"Id"`
		} `json:"CloudFrontOriginAccessIdentity"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Equal(t, id, getResult.CloudFrontOriginAccessIdentity.Id)
	require.NotEmpty(t, getResult.ETag)
	etag := getResult.ETag

	runCLI(t, awsCLI("cloudfront", "get-cloud-front-origin-access-identity-config", "--id", id, "--output", "json"))

	updJSON := fmt.Sprintf(`{"CallerReference": "%s", "Comment": "cli oai updated"}`, caller)
	updOut := runCLI(t, awsCLI("cloudfront", "update-cloud-front-origin-access-identity",
		"--id", id, "--if-match", etag,
		"--cloud-front-origin-access-identity-config", updJSON, "--output", "json",
	))
	var updResult struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(updOut), &updResult))
	require.NotEmpty(t, updResult.ETag)

	listOut := runCLI(t, awsCLI("cloudfront", "list-cloud-front-origin-access-identities", "--output", "json"))
	var listResult struct {
		CloudFrontOriginAccessIdentityList struct {
			Items []struct {
				Id string `json:"Id"`
			} `json:"Items"`
		} `json:"CloudFrontOriginAccessIdentityList"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	found := false
	for _, s := range listResult.CloudFrontOriginAccessIdentityList.Items {
		if s.Id == id {
			found = true
		}
	}
	require.True(t, found, "expected new OAI %q in list", id)

	runCLI(t, awsCLI("cloudfront", "delete-cloud-front-origin-access-identity", "--id", id, "--if-match", updResult.ETag))
}

// TestCloudFront_ContinuousDeploymentPolicy_Lifecycle exercises CDP via
// the aws CLI: create → get → get-config → update → list → delete.
func TestCloudFront_ContinuousDeploymentPolicy_Lifecycle(t *testing.T) {
	cfgJSON := `{
		"StagingDistributionDnsNames": {"Quantity": 1, "Items": ["staging.example.com"]},
		"Enabled": true,
		"TrafficConfig": {
			"Type": "SingleWeight",
			"SingleWeightConfig": {"Weight": 0.15}
		}
	}`

	out := runCLI(t, awsCLI("cloudfront", "create-continuous-deployment-policy",
		"--continuous-deployment-policy-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		ContinuousDeploymentPolicy struct {
			Id string `json:"Id"`
		} `json:"ContinuousDeploymentPolicy"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.ContinuousDeploymentPolicy.Id)
	require.NotEmpty(t, createResult.ETag)

	id := createResult.ContinuousDeploymentPolicy.Id

	getOut := runCLI(t, awsCLI("cloudfront", "get-continuous-deployment-policy", "--id", id, "--output", "json"))
	var getResult struct {
		ContinuousDeploymentPolicy struct {
			Id string `json:"Id"`
		} `json:"ContinuousDeploymentPolicy"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Equal(t, id, getResult.ContinuousDeploymentPolicy.Id)
	require.NotEmpty(t, getResult.ETag)
	etag := getResult.ETag

	runCLI(t, awsCLI("cloudfront", "get-continuous-deployment-policy-config", "--id", id, "--output", "json"))

	updJSON := `{
		"StagingDistributionDnsNames": {"Quantity": 1, "Items": ["staging.example.com"]},
		"Enabled": false,
		"TrafficConfig": {
			"Type": "SingleWeight",
			"SingleWeightConfig": {"Weight": 0.25}
		}
	}`
	updOut := runCLI(t, awsCLI("cloudfront", "update-continuous-deployment-policy",
		"--id", id, "--if-match", etag,
		"--continuous-deployment-policy-config", updJSON, "--output", "json",
	))
	var updResult struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(updOut), &updResult))
	require.NotEmpty(t, updResult.ETag)

	listOut := runCLI(t, awsCLI("cloudfront", "list-continuous-deployment-policies", "--output", "json"))
	var listResult struct {
		ContinuousDeploymentPolicyList struct {
			Items []struct {
				ContinuousDeploymentPolicy struct {
					Id string `json:"Id"`
				} `json:"ContinuousDeploymentPolicy"`
			} `json:"Items"`
		} `json:"ContinuousDeploymentPolicyList"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	found := false
	for _, s := range listResult.ContinuousDeploymentPolicyList.Items {
		if s.ContinuousDeploymentPolicy.Id == id {
			found = true
		}
	}
	require.True(t, found, "expected new CDP %q in list", id)

	runCLI(t, awsCLI("cloudfront", "delete-continuous-deployment-policy", "--id", id, "--if-match", updResult.ETag))
}

// TestCloudFront_MonitoringSubscription_Lifecycle exercises the
// per-distribution monitoring subscription via the aws CLI:
// create → get → delete.
func TestCloudFront_MonitoringSubscription_Lifecycle(t *testing.T) {
	distID := cliCreateTestDistribution(t)

	subJSON := `{"RealtimeMetricsSubscriptionConfig": {"RealtimeMetricsSubscriptionStatus": "Enabled"}}`
	runCLI(t, awsCLI("cloudfront", "create-monitoring-subscription",
		"--distribution-id", distID,
		"--monitoring-subscription", subJSON, "--output", "json",
	))

	getOut := runCLI(t, awsCLI("cloudfront", "get-monitoring-subscription",
		"--distribution-id", distID, "--output", "json"))
	var getResult struct {
		MonitoringSubscription struct {
			RealtimeMetricsSubscriptionConfig struct {
				RealtimeMetricsSubscriptionStatus string `json:"RealtimeMetricsSubscriptionStatus"`
			} `json:"RealtimeMetricsSubscriptionConfig"`
		} `json:"MonitoringSubscription"`
	}
	require.NoError(t, json.Unmarshal([]byte(getOut), &getResult))
	require.Equal(t, "Enabled", getResult.MonitoringSubscription.RealtimeMetricsSubscriptionConfig.RealtimeMetricsSubscriptionStatus)

	runCLI(t, awsCLI("cloudfront", "delete-monitoring-subscription", "--distribution-id", distID))
}

// cliCreateTestDistribution provisions a minimal distribution via the aws
// CLI and returns its id, for the monitoring-subscription test.
func cliCreateTestDistribution(t *testing.T) string {
	t.Helper()
	caller := "cli-ms-dist-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"CallerReference": "%s",
		"Comment": "cli monitoring sub dist",
		"Enabled": true,
		"Origins": {
			"Quantity": 1,
			"Items": [
				{
					"Id": "o1",
					"DomainName": "example.com",
					"CustomOriginConfig": {
						"HTTPPort": 80,
						"HTTPSPort": 443,
						"OriginProtocolPolicy": "http-only",
						"OriginSslProtocols": {"Quantity": 1, "Items": ["TLSv1.2"]}
					}
				}
			]
		},
		"DefaultCacheBehavior": {
			"TargetOriginId": "o1",
			"ViewerProtocolPolicy": "allow-all",
			"AllowedMethods": {
				"Quantity": 2,
				"Items": ["GET", "HEAD"],
				"CachedMethods": {"Quantity": 2, "Items": ["GET", "HEAD"]}
			},
			"ForwardedValues": {"QueryString": false, "Cookies": {"Forward": "none"}},
			"MinTTL": 0
		},
		"ViewerCertificate": {"CloudFrontDefaultCertificate": true},
		"Restrictions": {"GeoRestriction": {"RestrictionType": "none", "Quantity": 0}}
	}`, caller)

	out := runCLI(t, awsCLI("cloudfront", "create-distribution",
		"--distribution-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		Distribution struct {
			Id string `json:"Id"`
		} `json:"Distribution"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.Distribution.Id)
	return createResult.Distribution.Id
}
