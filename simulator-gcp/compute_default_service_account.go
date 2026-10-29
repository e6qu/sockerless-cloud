package main

import (
	"fmt"
	"strings"
)

// computeDefaultServiceAccountDomain is the domain of the service account
// Compute Engine creates in every project that has its API enabled.
const computeDefaultServiceAccountDomain = "developer.gserviceaccount.com"

// computeDefaultServiceAccountEmail is the Compute Engine default service
// account of the project Cloud Resource Manager numbers projectNumber.
func computeDefaultServiceAccountEmail(projectNumber string) string {
	return projectNumber + "-compute@" + computeDefaultServiceAccountDomain
}

// iamEnsureComputeDefaultServiceAccount gives a project the service account
// Compute Engine creates when its API is enabled. Every API is enabled on a
// new project here, so the account exists with the project.
func iamEnsureComputeDefaultServiceAccount(p CRMProject) {
	number := strings.TrimPrefix(p.Name, "projects/")
	email := computeDefaultServiceAccountEmail(number)
	name := fmt.Sprintf("projects/%s/serviceAccounts/%s", p.ProjectId, email)
	if _, ok := iamServiceAccounts.Get(name); ok {
		return
	}
	iamServiceAccounts.Put(name, GCPServiceAccount{
		Name:        name,
		ProjectId:   p.ProjectId,
		UniqueId:    gcpNumericID(21),
		Email:       email,
		DisplayName: "Compute Engine default service account",
	})
	iamSeedProjectGrants(p)
}

// iamSeedProjectGrants grants what Google grants a project when its APIs are
// enabled: the Editor role to the Compute Engine default service account
// and the Google APIs Service Agent (compute-service-accounts.md.gz), and to
// each served service's agent the role iam-service-agents.md.gz names.
func iamSeedProjectGrants(p CRMProject) {
	number := strings.TrimPrefix(p.Name, "projects/")
	gcpIAMPolicyWrites.Lock()
	defer gcpIAMPolicyWrites.Unlock()
	key := "project/" + p.ProjectId
	policy, _ := gcpProjectPolicies.Get(key)
	policy.Bindings = append(policy.Bindings, IAMBinding{Role: gcpRoleEditor, Members: []string{
		"serviceAccount:" + computeDefaultServiceAccountEmail(number),
		"serviceAccount:" + number + "@cloudservices.gserviceaccount.com",
	}})
	policy.Bindings = append(policy.Bindings, gcpProjectServiceAgents(number)...)
	policy.Etag = gcpPolicyETag()
	if policy.Version == 0 {
		policy.Version = 1
	}
	gcpProjectPolicies.Put(key, policy)
}
