package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSNS_PlatformApplicationLifecycle exercises the mobile-push platform
// application CRUD (create → get/list → set → delete) plus the endpoint CRUD
// nested under it.
func TestSNS_PlatformApplicationLifecycle(t *testing.T) {
	c := snsClient()

	create, err := c.CreatePlatformApplication(ctx, &sns.CreatePlatformApplicationInput{
		Name:     aws.String("sdk-gcm-app"),
		Platform: aws.String("GCM"),
		Attributes: map[string]string{
			"PlatformCredential": "fake-server-key",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, create.PlatformApplicationArn)
	appARN := *create.PlatformApplicationArn
	assert.Contains(t, appARN, ":app/GCM/sdk-gcm-app")
	t.Cleanup(func() {
		_, _ = c.DeletePlatformApplication(ctx, &sns.DeletePlatformApplicationInput{
			PlatformApplicationArn: aws.String(appARN),
		})
	})

	getApp, err := c.GetPlatformApplicationAttributes(ctx, &sns.GetPlatformApplicationAttributesInput{
		PlatformApplicationArn: aws.String(appARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "fake-server-key", getApp.Attributes["PlatformCredential"])
	assert.Equal(t, "true", getApp.Attributes["Enabled"])

	_, err = c.SetPlatformApplicationAttributes(ctx, &sns.SetPlatformApplicationAttributesInput{
		PlatformApplicationArn: aws.String(appARN),
		Attributes:             map[string]string{"Enabled": "false"},
	})
	require.NoError(t, err)
	getApp2, err := c.GetPlatformApplicationAttributes(ctx, &sns.GetPlatformApplicationAttributesInput{
		PlatformApplicationArn: aws.String(appARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "false", getApp2.Attributes["Enabled"])

	list, err := c.ListPlatformApplications(ctx, &sns.ListPlatformApplicationsInput{})
	require.NoError(t, err)
	found := false
	for _, a := range list.PlatformApplications {
		if aws.ToString(a.PlatformApplicationArn) == appARN {
			found = true
		}
	}
	assert.True(t, found, "application should appear in ListPlatformApplications")

	// --- endpoint CRUD under the application ---
	ep, err := c.CreatePlatformEndpoint(ctx, &sns.CreatePlatformEndpointInput{
		PlatformApplicationArn: aws.String(appARN),
		Token:                  aws.String("device-token-123"),
		CustomUserData:         aws.String("user-42"),
	})
	require.NoError(t, err)
	require.NotNil(t, ep.EndpointArn)
	epARN := *ep.EndpointArn
	assert.Contains(t, epARN, ":endpoint/GCM/sdk-gcm-app/")

	// Idempotent on the same token.
	ep2, err := c.CreatePlatformEndpoint(ctx, &sns.CreatePlatformEndpointInput{
		PlatformApplicationArn: aws.String(appARN),
		Token:                  aws.String("device-token-123"),
	})
	require.NoError(t, err)
	assert.Equal(t, epARN, aws.ToString(ep2.EndpointArn), "same token returns the same endpoint")

	getEp, err := c.GetEndpointAttributes(ctx, &sns.GetEndpointAttributesInput{
		EndpointArn: aws.String(epARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "device-token-123", getEp.Attributes["Token"])
	assert.Equal(t, "user-42", getEp.Attributes["CustomUserData"])

	_, err = c.SetEndpointAttributes(ctx, &sns.SetEndpointAttributesInput{
		EndpointArn: aws.String(epARN),
		Attributes:  map[string]string{"Enabled": "false"},
	})
	require.NoError(t, err)

	listEp, err := c.ListEndpointsByPlatformApplication(ctx, &sns.ListEndpointsByPlatformApplicationInput{
		PlatformApplicationArn: aws.String(appARN),
	})
	require.NoError(t, err)
	require.Len(t, listEp.Endpoints, 1)
	assert.Equal(t, epARN, aws.ToString(listEp.Endpoints[0].EndpointArn))
	assert.Equal(t, "false", listEp.Endpoints[0].Attributes["Enabled"])

	_, err = c.DeleteEndpoint(ctx, &sns.DeleteEndpointInput{EndpointArn: aws.String(epARN)})
	require.NoError(t, err)
	listEp2, err := c.ListEndpointsByPlatformApplication(ctx, &sns.ListEndpointsByPlatformApplicationInput{
		PlatformApplicationArn: aws.String(appARN),
	})
	require.NoError(t, err)
	assert.Empty(t, listEp2.Endpoints)

	_, err = c.DeletePlatformApplication(ctx, &sns.DeletePlatformApplicationInput{
		PlatformApplicationArn: aws.String(appARN),
	})
	require.NoError(t, err)
}

// TestSNS_GetEndpointAttributes_NotFound confirms the canonical NotFound shape.
func TestSNS_GetEndpointAttributes_NotFound(t *testing.T) {
	c := snsClient()
	_, err := c.GetEndpointAttributes(ctx, &sns.GetEndpointAttributesInput{
		EndpointArn: aws.String("arn:aws:sns:us-east-1:000000000000:endpoint/GCM/none/x"),
	})
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "does not exist"),
		"expected NotFound, got %v", err)
}

// TestSNS_SMSSandboxFailsWithoutCarrier proves the cloud slice does not
// manufacture or disclose an OTP when no telecommunications carrier exists.
func TestSNS_SMSSandboxFailsWithoutCarrier(t *testing.T) {
	c := snsClient()
	phone := "+12065550100"

	_, err := c.CreateSMSSandboxPhoneNumber(ctx, &sns.CreateSMSSandboxPhoneNumberInput{
		PhoneNumber:  aws.String(phone),
		LanguageCode: snstypes.LanguageCodeStringEnUs,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InternalError")

	listPending, err := c.ListSMSSandboxPhoneNumbers(ctx, &sns.ListSMSSandboxPhoneNumbersInput{})
	require.NoError(t, err)
	assert.False(t, sandboxStatusIs(listPending.PhoneNumbers, phone, snstypes.SMSSandboxPhoneNumberVerificationStatusPending),
		"a failed carrier delivery must not create pending verification state")

	status, err := c.GetSMSSandboxAccountStatus(ctx, &sns.GetSMSSandboxAccountStatusInput{})
	require.NoError(t, err)
	assert.True(t, status.IsInSandbox, "a fresh account is in the SMS sandbox")
}

func sandboxStatusIs(nums []snstypes.SMSSandboxPhoneNumber, phone string, want snstypes.SMSSandboxPhoneNumberVerificationStatus) bool {
	for _, n := range nums {
		if aws.ToString(n.PhoneNumber) == phone {
			return n.Status == want
		}
	}
	return false
}

// TestSNS_SMSAttributesAndOptOut exercises the account-wide SMS attribute
// store, the opt-out list (CheckIfPhoneNumberIsOptedOut / ListPhoneNumbersOptedOut /
// OptInPhoneNumber), and ListOriginationNumbers.
func TestSNS_SMSAttributesAndOptOut(t *testing.T) {
	c := snsClient()

	_, err := c.SetSMSAttributes(ctx, &sns.SetSMSAttributesInput{
		Attributes: map[string]string{
			"DefaultSMSType":    "Transactional",
			"MonthlySpendLimit": "10",
		},
	})
	require.NoError(t, err)

	getAll, err := c.GetSMSAttributes(ctx, &sns.GetSMSAttributesInput{})
	require.NoError(t, err)
	assert.Equal(t, "Transactional", getAll.Attributes["DefaultSMSType"])
	assert.Equal(t, "10", getAll.Attributes["MonthlySpendLimit"])

	// Filtered read returns only the requested attribute.
	getOne, err := c.GetSMSAttributes(ctx, &sns.GetSMSAttributesInput{
		Attributes: []string{"DefaultSMSType"},
	})
	require.NoError(t, err)
	assert.Equal(t, "Transactional", getOne.Attributes["DefaultSMSType"])
	_, present := getOne.Attributes["MonthlySpendLimit"]
	assert.False(t, present, "filtered GetSMSAttributes must not return unrequested attributes")

	// A never-opted-out number reads back as not opted out.
	check, err := c.CheckIfPhoneNumberIsOptedOut(ctx, &sns.CheckIfPhoneNumberIsOptedOutInput{
		PhoneNumber: aws.String("+12065550199"),
	})
	require.NoError(t, err)
	assert.False(t, check.IsOptedOut)

	// The opt-out list starts empty; OptInPhoneNumber is idempotent.
	listOut, err := c.ListPhoneNumbersOptedOut(ctx, &sns.ListPhoneNumbersOptedOutInput{})
	require.NoError(t, err)
	assert.NotContains(t, listOut.PhoneNumbers, "+12065550199")

	_, err = c.OptInPhoneNumber(ctx, &sns.OptInPhoneNumberInput{
		PhoneNumber: aws.String("+12065550199"),
	})
	require.NoError(t, err)

	// ListOriginationNumbers succeeds (a fresh account has none provisioned).
	orig, err := c.ListOriginationNumbers(ctx, &sns.ListOriginationNumbersInput{})
	require.NoError(t, err)
	assert.NotNil(t, orig)
}

// TestSNS_DataProtectionPolicy stores and reads back a per-topic
// data-protection policy.
func TestSNS_DataProtectionPolicy(t *testing.T) {
	c := snsClient()
	tpc, err := c.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sdk-dpp-topic")})
	require.NoError(t, err)
	topicARN := *tpc.TopicArn
	t.Cleanup(func() {
		_, _ = c.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	policy := `{"Name":"sdk-dpp","Description":"","Version":"2021-06-01","Statement":[{"DataDirection":"Inbound","Principal":["*"],"DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Deny":{}}}]}`
	_, err = c.PutDataProtectionPolicy(ctx, &sns.PutDataProtectionPolicyInput{
		ResourceArn:          aws.String(topicARN),
		DataProtectionPolicy: aws.String(policy),
	})
	require.NoError(t, err)

	got, err := c.GetDataProtectionPolicy(ctx, &sns.GetDataProtectionPolicyInput{
		ResourceArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	assert.Equal(t, policy, aws.ToString(got.DataProtectionPolicy))
}
