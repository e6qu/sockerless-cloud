package gcp_sdk_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudbilling "google.golang.org/api/cloudbilling/v1"
	crmv1 "google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// Cloud Billing's account collection is real control-plane state: created,
// listed, patched, moved between organizations, subaccounted, and linked to
// projects — with getBillingInfo and updateBillingInfo answering from the
// same store, so the two halves of the surface never disagree.
func TestCloudBilling_AccountLifecycleAndProjectLink(t *testing.T) {
	ctx := context.Background()
	svc, err := cloudbilling.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	created, err := svc.BillingAccounts.Create(&cloudbilling.BillingAccount{
		DisplayName: "team-spend",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, created.Name)
	assert.True(t, created.Open, "a created account is open")
	assert.Equal(t, "USD", created.CurrencyCode)

	got, err := svc.BillingAccounts.Get(created.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, "team-spend", got.DisplayName)

	// Patch mutates the display name — the one field the API admits.
	patched, err := svc.BillingAccounts.Patch(created.Name, &cloudbilling.BillingAccount{
		DisplayName: "team-spend-renamed",
	}).UpdateMask("display_name").Do()
	require.NoError(t, err)
	assert.Equal(t, "team-spend-renamed", patched.DisplayName)

	// A subaccount names its master and the master's subaccount listing
	// returns it; the top-level list filter finds it the same way.
	sub, err := svc.BillingAccounts.SubAccounts.Create(created.Name, &cloudbilling.BillingAccount{
		DisplayName: "team-spend-sub",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, created.Name, sub.MasterBillingAccount)
	subs, err := svc.BillingAccounts.SubAccounts.List(created.Name).Do()
	require.NoError(t, err)
	require.Len(t, subs.BillingAccounts, 1)
	filtered, err := svc.BillingAccounts.List().Filter("master_billing_account=" + created.Name).Do()
	require.NoError(t, err)
	require.Len(t, filtered.BillingAccounts, 1)
	assert.Equal(t, sub.Name, filtered.BillingAccounts[0].Name)

	// Moving the account re-parents it under the deployment's organization,
	// and the organization-scoped listing then returns it.
	org := crmTestOrg
	moved, err := svc.BillingAccounts.Move(created.Name, &cloudbilling.MoveBillingAccountRequest{
		DestinationParent: org,
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, org, moved.Parent)
	orgList, err := svc.Organizations.BillingAccounts.List(org).Do()
	require.NoError(t, err)
	found := false
	for _, account := range orgList.BillingAccounts {
		if account.Name == created.Name {
			found = true
		}
	}
	assert.True(t, found, "the organization-scoped listing must return the moved account")

	// Linking a project writes the store getBillingInfo reads.
	crm, err := crmv1.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	const projectID = "billing-link-proj"
	_, err = crm.Projects.Create(&crmv1.Project{ProjectId: projectID}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = crm.Projects.Delete(projectID).Do() })

	info, err := svc.Projects.UpdateBillingInfo("projects/"+projectID, &cloudbilling.ProjectBillingInfo{
		BillingAccountName: created.Name,
	}).Do()
	require.NoError(t, err)
	assert.True(t, info.BillingEnabled)
	assert.Equal(t, created.Name, info.BillingAccountName)

	read, err := svc.Projects.GetBillingInfo("projects/" + projectID).Do()
	require.NoError(t, err)
	assert.True(t, read.BillingEnabled, "the read must see the link the write made")
	assert.Equal(t, created.Name, read.BillingAccountName)

	linked, err := svc.BillingAccounts.Projects.List(created.Name).Do()
	require.NoError(t, err)
	require.Len(t, linked.ProjectBillingInfo, 1)
	assert.Equal(t, projectID, linked.ProjectBillingInfo[0].ProjectId)

	// Unlinking disables billing.
	info, err = svc.Projects.UpdateBillingInfo("projects/"+projectID, &cloudbilling.ProjectBillingInfo{
		BillingAccountName: "",
	}).Do()
	require.NoError(t, err)
	assert.False(t, info.BillingEnabled)

	// The IAM triple rides the per-resource policy store.
	policy, err := svc.BillingAccounts.SetIamPolicy(created.Name, &cloudbilling.SetIamPolicyRequest{
		Policy: &cloudbilling.Policy{Bindings: []*cloudbilling.Binding{{
			Role: "roles/billing.user", Members: []string{"user:dev@example.com"},
		}}},
	}).Do()
	require.NoError(t, err)
	require.Len(t, policy.Bindings, 1)
	gotPolicy, err := svc.BillingAccounts.GetIamPolicy(created.Name).Do()
	require.NoError(t, err)
	require.Len(t, gotPolicy.Bindings, 1)
	assert.Equal(t, "roles/billing.user", gotPolicy.Bindings[0].Role)

	// A missing account is a real 404.
	_, err = svc.BillingAccounts.Get("billingAccounts/000000-000000-000000").Do()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 404, gerr.Code)
}

// The service catalog is the installation's own: the services this simulator
// hosts, under stable identifiers, with SKU lists that are empty because
// this deployment publishes no price sheet — that emptiness is the
// catalog's truth, pinned here so it never becomes fabricated pricing.
func TestCloudBilling_ServiceCatalogIsTheInstallationsOwn(t *testing.T) {
	ctx := context.Background()
	svc, err := cloudbilling.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	services, err := svc.Services.List().Do()
	require.NoError(t, err)
	require.NotEmpty(t, services.Services)
	var compute *cloudbilling.Service
	for _, service := range services.Services {
		require.NotEmpty(t, service.ServiceId)
		require.NotEmpty(t, service.DisplayName)
		if service.DisplayName == "Compute Engine" {
			compute = service
		}
	}
	require.NotNil(t, compute, "the catalog must name the services this simulator hosts")

	skus, err := svc.Services.Skus.List(compute.Name).Do()
	require.NoError(t, err)
	assert.Empty(t, skus.Skus, "no price sheet exists here, and none may be invented")

	_, err = svc.Services.Skus.List("services/0000-0000-0000").Do()
	var gerr *googleapi.Error
	require.ErrorAs(t, err, &gerr)
	assert.Equal(t, 404, gerr.Code)
}
