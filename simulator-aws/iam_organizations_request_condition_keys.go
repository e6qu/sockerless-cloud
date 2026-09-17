package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("organizations", iamPopulateOrganizationsRequestConditionKeys)
}

func iamPopulateOrganizationsRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "DeregisterDelegatedAdministrator", "DisableAWSServiceAccess", "EnableAWSServiceAccess",
		"ListDelegatedAdministrators", "RegisterDelegatedAdministrator":
		if principal := iamRequestParameter(r, body, "ServicePrincipal"); principal != "" {
			ctx["organizations:ServicePrincipal"] = []string{principal}
		}
	}
}
