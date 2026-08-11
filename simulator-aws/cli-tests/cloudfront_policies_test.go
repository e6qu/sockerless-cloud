package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloudFront_CachePolicy_Lifecycle(t *testing.T) {
	name := "cli-cp-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"Name": "%s",
		"Comment": "cli cache policy",
		"DefaultTTL": 86400,
		"MaxTTL": 31536000,
		"MinTTL": 1,
		"ParametersInCacheKeyAndForwardedToOrigin": {
			"EnableAcceptEncodingGzip": true,
			"EnableAcceptEncodingBrotli": true,
			"HeadersConfig": {"HeaderBehavior": "none"},
			"CookiesConfig": {"CookieBehavior": "none"},
			"QueryStringsConfig": {"QueryStringBehavior": "none"}
		}
	}`, name)

	out := runCLI(t, awsCLI("cloudfront", "create-cache-policy",
		"--cache-policy-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		CachePolicy struct {
			Id string `json:"Id"`
		} `json:"CachePolicy"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.CachePolicy.Id)
	require.NotEmpty(t, createResult.ETag)

	id := createResult.CachePolicy.Id
	etag := createResult.ETag

	runCLI(t, awsCLI("cloudfront", "get-cache-policy", "--id", id, "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-cache-policies", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "delete-cache-policy", "--id", id, "--if-match", etag))
}

func TestCloudFront_OriginRequestPolicy_Lifecycle(t *testing.T) {
	name := "cli-orp-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"Name": "%s",
		"Comment": "cli origin request policy",
		"HeadersConfig": {"HeaderBehavior": "none"},
		"CookiesConfig": {"CookieBehavior": "none"},
		"QueryStringsConfig": {"QueryStringBehavior": "none"}
	}`, name)

	out := runCLI(t, awsCLI("cloudfront", "create-origin-request-policy",
		"--origin-request-policy-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		OriginRequestPolicy struct {
			Id string `json:"Id"`
		} `json:"OriginRequestPolicy"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.OriginRequestPolicy.Id)
	require.NotEmpty(t, createResult.ETag)

	id := createResult.OriginRequestPolicy.Id
	etag := createResult.ETag

	runCLI(t, awsCLI("cloudfront", "get-origin-request-policy", "--id", id, "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "delete-origin-request-policy", "--id", id, "--if-match", etag))
}

func TestCloudFront_ResponseHeadersPolicy_Lifecycle(t *testing.T) {
	name := "cli-rhp-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"Name": "%s",
		"Comment": "cli response headers policy",
		"SecurityHeadersConfig": {
			"ContentTypeOptions": {"Override": true},
			"FrameOptions": {"Override": true, "FrameOption": "DENY"}
		}
	}`, name)

	out := runCLI(t, awsCLI("cloudfront", "create-response-headers-policy",
		"--response-headers-policy-config", cfgJSON, "--output", "json",
	))
	var createResult struct {
		ResponseHeadersPolicy struct {
			Id string `json:"Id"`
		} `json:"ResponseHeadersPolicy"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	require.NotEmpty(t, createResult.ResponseHeadersPolicy.Id)
	require.NotEmpty(t, createResult.ETag)

	id := createResult.ResponseHeadersPolicy.Id
	etag := createResult.ETag

	runCLI(t, awsCLI("cloudfront", "get-response-headers-policy", "--id", id, "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "delete-response-headers-policy", "--id", id, "--if-match", etag))
}
