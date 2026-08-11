package aws_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSNSCLI_PlatformApplicationAndEndpoint exercises the mobile-push platform
// application + endpoint CRUD through the aws CLI.
func TestSNSCLI_PlatformApplicationAndEndpoint(t *testing.T) {
	out := runCLI(t, awsCLI("sns", "create-platform-application",
		"--name", "cli-gcm-app",
		"--platform", "GCM",
		"--attributes", "PlatformCredential=provider-credential"))
	var app struct {
		PlatformApplicationArn string `json:"PlatformApplicationArn"`
	}
	parseJSON(t, out, &app)
	require.NotEmpty(t, app.PlatformApplicationArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-platform-application",
			"--platform-application-arn", app.PlatformApplicationArn).Run()
	})

	out = runCLI(t, awsCLI("sns", "get-platform-application-attributes",
		"--platform-application-arn", app.PlatformApplicationArn))
	var getApp struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &getApp)
	assert.Equal(t, "provider-credential", getApp.Attributes["PlatformCredential"])

	runCLI(t, awsCLI("sns", "set-platform-application-attributes",
		"--platform-application-arn", app.PlatformApplicationArn,
		"--attributes", "Enabled=false"))

	out = runCLI(t, awsCLI("sns", "list-platform-applications"))
	assert.Contains(t, out, app.PlatformApplicationArn)

	out = runCLI(t, awsCLI("sns", "create-platform-endpoint",
		"--platform-application-arn", app.PlatformApplicationArn,
		"--token", "device-token-cli"))
	var ep struct {
		EndpointArn string `json:"EndpointArn"`
	}
	parseJSON(t, out, &ep)
	require.NotEmpty(t, ep.EndpointArn)

	out = runCLI(t, awsCLI("sns", "get-endpoint-attributes",
		"--endpoint-arn", ep.EndpointArn))
	var getEp struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &getEp)
	assert.Equal(t, "device-token-cli", getEp.Attributes["Token"])

	runCLI(t, awsCLI("sns", "set-endpoint-attributes",
		"--endpoint-arn", ep.EndpointArn,
		"--attributes", "Enabled=false"))

	out = runCLI(t, awsCLI("sns", "list-endpoints-by-platform-application",
		"--platform-application-arn", app.PlatformApplicationArn))
	var listEp struct {
		Endpoints []struct {
			EndpointArn string `json:"EndpointArn"`
		} `json:"Endpoints"`
	}
	parseJSON(t, out, &listEp)
	require.Len(t, listEp.Endpoints, 1)
	assert.Equal(t, ep.EndpointArn, listEp.Endpoints[0].EndpointArn)

	runCLI(t, awsCLI("sns", "delete-endpoint", "--endpoint-arn", ep.EndpointArn))
	runCLI(t, awsCLI("sns", "delete-platform-application",
		"--platform-application-arn", app.PlatformApplicationArn))
}

// TestSNSCLI_SMSSandboxFailsWithoutCarrier proves the AWS CLI receives a
// fail-loud service error and no state when no SMS carrier exists.
func TestSNSCLI_SMSSandboxFailsWithoutCarrier(t *testing.T) {
	phone := "+12065550111"
	errOut := runCLIExpectError(t, awsCLI("sns", "create-sms-sandbox-phone-number",
		"--phone-number", phone,
		"--language-code", "en-US"))
	assert.Contains(t, errOut, "InternalError")

	out := runCLI(t, awsCLI("sns", "list-sms-sandbox-phone-numbers"))
	assert.NotContains(t, out, phone)

	out = runCLI(t, awsCLI("sns", "get-sms-sandbox-account-status"))
	assert.Contains(t, out, "IsInSandbox")
}

// TestSNSCLI_SMSAttributesAndOptOut exercises the account SMS attribute store,
// the opt-out checks, opt-in, and list-origination-numbers.
func TestSNSCLI_SMSAttributesAndOptOut(t *testing.T) {
	runCLI(t, awsCLI("sns", "set-sms-attributes",
		"--attributes", "DefaultSMSType=Transactional,MonthlySpendLimit=10"))

	out := runCLI(t, awsCLI("sns", "get-sms-attributes"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	assert.Equal(t, "Transactional", attrs.Attributes["DefaultSMSType"])
	assert.Equal(t, "10", attrs.Attributes["MonthlySpendLimit"])

	// Filtered read returns only the requested attribute.
	out = runCLI(t, awsCLI("sns", "get-sms-attributes", "--attributes", "DefaultSMSType"))
	var attrsOne struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrsOne)
	assert.Equal(t, "Transactional", attrsOne.Attributes["DefaultSMSType"])
	_, present := attrsOne.Attributes["MonthlySpendLimit"]
	assert.False(t, present)

	out = runCLI(t, awsCLI("sns", "check-if-phone-number-is-opted-out",
		"--phone-number", "+12065550222"))
	assert.Contains(t, strings.ToLower(out), "false")

	runCLI(t, awsCLI("sns", "list-phone-numbers-opted-out"))

	runCLI(t, awsCLI("sns", "opt-in-phone-number", "--phone-number", "+12065550222"))

	runCLI(t, awsCLI("sns", "list-origination-numbers"))
}

// TestSNSCLI_DataProtectionPolicy stores and reads a per-topic
// data-protection policy via the CLI.
func TestSNSCLI_DataProtectionPolicy(t *testing.T) {
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-dpp-topic"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	policy := `{"Name":"cli-dpp","Description":"","Version":"2021-06-01","Statement":[{"DataDirection":"Inbound","Principal":["*"],"DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Deny":{}}}]}`
	runCLI(t, awsCLI("sns", "put-data-protection-policy",
		"--resource-arn", topic.TopicArn,
		"--data-protection-policy", policy))

	out = runCLI(t, awsCLI("sns", "get-data-protection-policy",
		"--resource-arn", topic.TopicArn))
	var got struct {
		DataProtectionPolicy string `json:"DataProtectionPolicy"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, policy, got.DataProtectionPolicy)
}
