package main

import (
	"net/url"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func iamConditionContext(form url.Values) map[string][]string {
	form.Set("Version", "2010-05-08")
	return requestConditionContext(queryConditionRequest(form), "iam", form.Get("Action"), "")
}

func TestIAMConditionKeysReadTheRequestParameters(t *testing.T) {
	for _, tc := range []struct {
		form url.Values
		want map[string][]string
	}{
		{url.Values{"Action": {"CreateServiceLinkedRole"}, "AWSServiceName": {"elasticbeanstalk.amazonaws.com"}},
			map[string][]string{"iam:AWSServiceName": {"elasticbeanstalk.amazonaws.com"}}},
		{url.Values{
			"Action":                        {"CreateDelegationRequest"},
			"OwnerAccountId":                {"123456789012"},
			"Permissions.PolicyTemplateArn": {"arn:aws:iam::aws:delegation-template/partner/t"},
			"NotificationChannel":           {"arn:aws:sns:us-east-1:210987654321:delegations"},
			"SessionDuration":               {"7200"},
		}, map[string][]string{
			"iam:TemplateArn":         {"arn:aws:iam::aws:delegation-template/partner/t"},
			"iam:NotificationChannel": {"arn:aws:sns:us-east-1:210987654321:delegations"},
			"iam:DelegationDuration":  {"7200"},
		}},
		{url.Values{"Action": {"ListDelegationRequests"}, "OwnerId": {"arn:aws:iam::123456789012:root"}},
			map[string][]string{"iam:DelegationRequestOwner": {"arn:aws:iam::123456789012:root"}}},
		{url.Values{
			"Action":                {"GenerateOrganizationsAccessReport"},
			"EntityPath":            {"o-a1b2c3d4e5/r-f6g7h8i9j0example"},
			"OrganizationsPolicyId": {"p-FullAWSAccess"},
		}, map[string][]string{"iam:OrganizationsPolicyId": {"p-FullAWSAccess"}}},
		{url.Values{
			"Action":            {"CreateServiceSpecificCredential"},
			"UserName":          {"u"},
			"ServiceName":       {"bedrock.amazonaws.com"},
			"CredentialAgeDays": {"30"},
		}, map[string][]string{
			"iam:ServiceSpecificCredentialServiceName": {"bedrock.amazonaws.com"},
			"iam:ServiceSpecificCredentialAgeDays":     {"30"},
		}},
	} {
		t.Run(tc.form.Get("Action"), func(t *testing.T) {
			assertConditionValues(t, iamConditionContext(tc.form), tc.want)
		})
	}
}

// The requests that act on an existing service-specific credential name it by
// id, and the key is the service the stored credential belongs to.
func TestIAMConditionKeysReadTheStoredCredentialService(t *testing.T) {
	previous := iamServiceCreds
	t.Cleanup(func() { iamServiceCreds = previous })
	AwaitSimulatorBackground()
	iamServiceCreds = sim.MakeStore[IAMServiceSpecificCredential](nil, "iam_service_specific_credentials")
	iamServiceCreds.Put("ACCAEXAMPLE", IAMServiceSpecificCredential{
		ServiceSpecificCredentialId: "ACCAEXAMPLE",
		UserName:                    "u",
		ServiceName:                 "codecommit.amazonaws.com",
	})

	for _, action := range []string{"DeleteServiceSpecificCredential", "ResetServiceSpecificCredential", "UpdateServiceSpecificCredential"} {
		ctx := iamConditionContext(url.Values{
			"Action":                      {action},
			"UserName":                    {"u"},
			"ServiceSpecificCredentialId": {"ACCAEXAMPLE"},
		})
		assertConditionValues(t, ctx, map[string][]string{
			"iam:ServiceSpecificCredentialServiceName": {"codecommit.amazonaws.com"},
		})
	}

	ctx := iamConditionContext(url.Values{
		"Action":                      {"DeleteServiceSpecificCredential"},
		"ServiceSpecificCredentialId": {"ACCAMISSING"},
	})
	assertConditionKeysAbsent(t, ctx, "iam:ServiceSpecificCredentialServiceName")
}

func TestIAMConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := iamConditionContext(url.Values{"Action": {"CreateDelegationRequest"}, "OwnerAccountId": {"123456789012"}})
	assertConditionKeysAbsent(t, ctx, "iam:TemplateArn", "iam:NotificationChannel", "iam:DelegationDuration")
	ctx = iamConditionContext(url.Values{"Action": {"CreateServiceSpecificCredential"}, "UserName": {"u"}})
	assertConditionKeysAbsent(t, ctx, "iam:ServiceSpecificCredentialAgeDays")
	ctx = iamConditionContext(url.Values{"Action": {"GetRole"}, "AWSServiceName": {"ec2.amazonaws.com"}})
	assertConditionKeysAbsent(t, ctx, "iam:AWSServiceName")
}
