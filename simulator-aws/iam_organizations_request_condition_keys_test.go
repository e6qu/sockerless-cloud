package main

import "testing"

func TestOrganizationsRequestConditionKeysReadTheServicePrincipal(t *testing.T) {
	for _, operation := range []string{"DeregisterDelegatedAdministrator", "DisableAWSServiceAccess",
		"EnableAWSServiceAccess", "ListDelegatedAdministrators", "RegisterDelegatedAdministrator"} {
		t.Run(operation, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("organizations", operation,
				`{"AccountId":"111122223333","ServicePrincipal":"config.amazonaws.com"}`),
				map[string][]string{"organizations:ServicePrincipal": {"config.amazonaws.com"}})
		})
	}
	assertConditionValues(t, jsonConditionContext("organizations", "ListDelegatedAdministrators", `{}`),
		map[string][]string{})
	assertConditionValues(t, jsonConditionContext("organizations", "ListAccounts",
		`{"ServicePrincipal":"config.amazonaws.com"}`), map[string][]string{})
}
