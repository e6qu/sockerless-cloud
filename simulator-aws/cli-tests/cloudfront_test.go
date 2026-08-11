package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestCloudFront_Distribution_Lifecycle exercises the CloudFront REST+XML
// surface via the real `aws` CLI. The CLI accepts JSON on input (--cli-input-json
// or --distribution-config '{...}') and emits JSON output even though the wire is XML.
func TestCloudFront_Distribution_Lifecycle(t *testing.T) {
	caller := "cli-test-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"CallerReference": "%s",
		"Comment": "cli lifecycle test",
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
						"OriginSslProtocols": {
							"Quantity": 1,
							"Items": ["TLSv1.2"]
						},
						"OriginReadTimeout": 30,
						"OriginKeepaliveTimeout": 5
					},
					"ConnectionAttempts": 3,
					"ConnectionTimeout": 10
				}
			]
		},
		"DefaultCacheBehavior": {
			"TargetOriginId": "o1",
			"ViewerProtocolPolicy": "allow-all",
			"AllowedMethods": {
				"Quantity": 2,
				"Items": ["GET", "HEAD"],
				"CachedMethods": {
					"Quantity": 2,
					"Items": ["GET", "HEAD"]
				}
			},
			"ForwardedValues": {
				"QueryString": false,
				"Cookies": {"Forward": "none"}
			},
			"MinTTL": 0
		},
		"ViewerCertificate": {
			"CloudFrontDefaultCertificate": true
		},
		"Restrictions": {
			"GeoRestriction": {
				"RestrictionType": "none",
				"Quantity": 0
			}
		}
	}`, caller)

	out := runCLI(t, awsCLI("cloudfront", "create-distribution",
		"--distribution-config", cfgJSON,
		"--output", "json",
	))

	var createResult struct {
		Distribution struct {
			Id         string `json:"Id"`
			ARN        string `json:"ARN"`
			Status     string `json:"Status"`
			DomainName string `json:"DomainName"`
		} `json:"Distribution"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.Distribution.Id)
	require.NotEmpty(t, createResult.ETag)
	require.Equal(t, "Deployed", createResult.Distribution.Status)
	require.Contains(t, createResult.Distribution.DomainName, ".cloudfront.net")

	id := createResult.Distribution.Id
	etag := createResult.ETag

	// get
	out = runCLI(t, awsCLI("cloudfront", "get-distribution", "--id", id, "--output", "json"))
	var getResult struct {
		Distribution struct {
			Id string `json:"Id"`
		} `json:"Distribution"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &getResult))
	require.Equal(t, id, getResult.Distribution.Id)

	// list (find ours)
	out = runCLI(t, awsCLI("cloudfront", "list-distributions", "--output", "json"))
	var listResult struct {
		DistributionList struct {
			Items []struct {
				Id string `json:"Id"`
			} `json:"Items"`
		} `json:"DistributionList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listResult))
	found := false
	for _, s := range listResult.DistributionList.Items {
		if s.Id == id {
			found = true
		}
	}
	require.True(t, found, "expected new distribution %q in list", id)

	// disable so we can delete (set Enabled=false via update-distribution)
	getCfgOut := runCLI(t, awsCLI("cloudfront", "get-distribution-config", "--id", id, "--output", "json"))
	var getCfg struct {
		DistributionConfig map[string]interface{} `json:"DistributionConfig"`
		ETag               string                 `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(getCfgOut), &getCfg))
	require.Equal(t, etag, getCfg.ETag)
	getCfg.DistributionConfig["Enabled"] = false
	updatedCfgBytes, _ := json.Marshal(getCfg.DistributionConfig)
	updOut := runCLI(t, awsCLI("cloudfront", "update-distribution",
		"--id", id,
		"--if-match", etag,
		"--distribution-config", string(updatedCfgBytes),
		"--output", "json",
	))
	var updResult struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(updOut), &updResult))
	require.NotEmpty(t, updResult.ETag)
	require.NotEqual(t, etag, updResult.ETag, "ETag must rotate on update")

	// delete
	runCLI(t, awsCLI("cloudfront", "delete-distribution",
		"--id", id,
		"--if-match", updResult.ETag,
	))
}

func TestCloudFront_OAC_Lifecycle(t *testing.T) {
	name := "cli-oac-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"Name": "%s",
		"Description": "cli oac test",
		"SigningProtocol": "sigv4",
		"SigningBehavior": "always",
		"OriginAccessControlOriginType": "s3"
	}`, name)

	out := runCLI(t, awsCLI("cloudfront", "create-origin-access-control",
		"--origin-access-control-config", cfgJSON,
		"--output", "json",
	))
	var createResult struct {
		OriginAccessControl struct {
			Id string `json:"Id"`
		} `json:"OriginAccessControl"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.OriginAccessControl.Id)

	id := createResult.OriginAccessControl.Id
	etag := createResult.ETag

	out = runCLI(t, awsCLI("cloudfront", "list-origin-access-controls", "--output", "json"))
	var listResult struct {
		OriginAccessControlList struct {
			Items []struct {
				Id   string `json:"Id"`
				Name string `json:"Name"`
			} `json:"Items"`
		} `json:"OriginAccessControlList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listResult))
	found := false
	for _, s := range listResult.OriginAccessControlList.Items {
		if s.Id == id && s.Name == name {
			found = true
		}
	}
	require.True(t, found)

	runCLI(t, awsCLI("cloudfront", "delete-origin-access-control",
		"--id", id,
		"--if-match", etag,
	))
}
