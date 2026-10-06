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
// Compute Engine creates when its API is enabled. Every API is enabled on a new
// project here, so the account exists as soon as the project does, and
// Terraform's google_compute_default_service_account reads it back from IAM.
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
}
