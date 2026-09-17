package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("iam", iamPopulateIAMRequestConditionKeys)
}

// iamPopulateIAMRequestConditionKeys adds the AWS Identity and Access
// Management keys that name what a service-linked role, a delegation request,
// an access report or a service-specific credential is for.
func iamPopulateIAMRequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	switch operation {
	case "CreateServiceLinkedRole":
		iamSetConditionValues(ctx, "iam:AWSServiceName", r.FormValue("AWSServiceName"))
	case "CreateDelegationRequest":
		iamSetConditionValues(ctx, "iam:TemplateArn", r.FormValue("Permissions.PolicyTemplateArn"))
		iamSetConditionValues(ctx, "iam:NotificationChannel", r.FormValue("NotificationChannel"))
		iamSetConditionValues(ctx, "iam:DelegationDuration", r.FormValue("SessionDuration"))
	case "ListDelegationRequests":
		iamSetConditionValues(ctx, "iam:DelegationRequestOwner", r.FormValue("OwnerId"))
	case "GenerateOrganizationsAccessReport":
		iamSetConditionValues(ctx, "iam:OrganizationsPolicyId", r.FormValue("OrganizationsPolicyId"))
	case "CreateServiceSpecificCredential":
		iamSetConditionValues(ctx, "iam:ServiceSpecificCredentialServiceName", r.FormValue("ServiceName"))
		iamSetConditionValues(ctx, "iam:ServiceSpecificCredentialAgeDays", r.FormValue("CredentialAgeDays"))
	case "DeleteServiceSpecificCredential", "ResetServiceSpecificCredential", "UpdateServiceSpecificCredential":
		// These requests name the credential by id; its service is a fact of
		// the stored credential.
		if credential, ok := iamServiceCreds.Get(r.FormValue("ServiceSpecificCredentialId")); ok {
			iamSetConditionValues(ctx, "iam:ServiceSpecificCredentialServiceName", credential.ServiceName)
		}
	}
}
