package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iam/v1"
)

func iamUserManagedKeyNames(t *testing.T, svc *iam.Service, account string) []string {
	t.Helper()
	list, err := svc.Projects.ServiceAccounts.Keys.List(account).KeyTypes("USER_MANAGED").Do()
	require.NoError(t, err)
	names := []string{}
	for _, k := range list.Keys {
		names = append(names, k.Name)
	}
	return names
}

// A deleted service account is gone from get and list but restorable by its
// unique ID, and undelete brings it back with that unique ID, its settings and
// its keys.
func TestIAM_UndeleteServiceAccountRestoresItByUniqueID(t *testing.T) {
	svc := iamService(t)
	sa, err := svc.Projects.ServiceAccounts.Create("projects/undelete-project",
		&iam.CreateServiceAccountRequest{
			AccountId:      "undelete-sa",
			ServiceAccount: &iam.ServiceAccount{DisplayName: "Undelete SA", Description: "kept across delete"},
		}).Do()
	require.NoError(t, err)
	key, err := svc.Projects.ServiceAccounts.Keys.Create(sa.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)

	byUniqueID, err := svc.Projects.ServiceAccounts.Get("projects/-/serviceAccounts/" + sa.UniqueId).Do()
	require.NoError(t, err, "get must accept the unique ID under the - wildcard")
	require.Equal(t, sa.Email, byUniqueID.Email)

	_, err = svc.Projects.ServiceAccounts.Delete(sa.Name).Do()
	require.NoError(t, err)
	_, err = svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")
	listed, err := svc.Projects.ServiceAccounts.List("projects/undelete-project").Do()
	require.NoError(t, err)
	assert.NotContains(t, iamAccountNames(listed), sa.Name, "a deleted account is not listed")

	resp, err := svc.Projects.ServiceAccounts.Undelete("projects/-/serviceAccounts/"+sa.UniqueId,
		&iam.UndeleteServiceAccountRequest{}).Do()
	require.NoError(t, err)
	require.NotNil(t, resp.RestoredAccount)
	assert.Equal(t, sa.UniqueId, resp.RestoredAccount.UniqueId, "undelete keeps the unique ID")
	assert.Equal(t, sa.Email, resp.RestoredAccount.Email)
	assert.Equal(t, "Undelete SA", resp.RestoredAccount.DisplayName)
	assert.Equal(t, "kept across delete", resp.RestoredAccount.Description)

	got, err := svc.Projects.ServiceAccounts.Get(sa.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, sa.UniqueId, got.UniqueId)
	assert.Contains(t, iamUserManagedKeyNames(t, svc, sa.Name), key.Name, "undelete restores the account's keys")

	_, err = svc.Projects.ServiceAccounts.Undelete("projects/-/serviceAccounts/"+sa.UniqueId,
		&iam.UndeleteServiceAccountRequest{}).Do()
	requireGoogleAPIRefusal(t, err, 400, "is not deleted", "undelete of a live account")

	_, err = svc.Projects.ServiceAccounts.Undelete("projects/-/serviceAccounts/100000000000000000001",
		&iam.UndeleteServiceAccountRequest{}).Do()
	requireGoogleErr(t, err, 403, "PERMISSION_DENIED")
	_, err = svc.Projects.ServiceAccounts.Undelete("projects/undelete-project/serviceAccounts/100000000000000000001",
		&iam.UndeleteServiceAccountRequest{}).Do()
	requireGoogleErr(t, err, 404, "NOT_FOUND")
}

// Creating an account under the email of a deleted one makes a new account
// with a new unique ID and none of the old one's keys, and the old one cannot
// be restored while the new one holds the email.
func TestIAM_RecreatedServiceAccountIsANewAccount(t *testing.T) {
	svc := iamService(t)
	create := func() *iam.ServiceAccount {
		t.Helper()
		sa, err := svc.Projects.ServiceAccounts.Create("projects/undelete-project",
			&iam.CreateServiceAccountRequest{AccountId: "recreated-sa"}).Do()
		require.NoError(t, err)
		return sa
	}
	original := create()
	key, err := svc.Projects.ServiceAccounts.Keys.Create(original.Name, &iam.CreateServiceAccountKeyRequest{}).Do()
	require.NoError(t, err)
	_, err = svc.Projects.ServiceAccounts.Delete(original.Name).Do()
	require.NoError(t, err)

	replacement := create()
	assert.Equal(t, original.Email, replacement.Email)
	assert.NotEqual(t, original.UniqueId, replacement.UniqueId, "a recreated account gets a new unique ID")
	assert.NotContains(t, iamUserManagedKeyNames(t, svc, replacement.Name), key.Name,
		"a recreated account does not inherit the deleted account's keys")

	_, err = svc.Projects.ServiceAccounts.Undelete("projects/-/serviceAccounts/"+original.UniqueId,
		&iam.UndeleteServiceAccountRequest{}).Do()
	requireGoogleAPIRefusal(t, err, 400, "same email", "undelete while another account holds the email")

	_, err = svc.Projects.ServiceAccounts.Delete(replacement.Name).Do()
	require.NoError(t, err)
	resp, err := svc.Projects.ServiceAccounts.Undelete("projects/-/serviceAccounts/"+original.UniqueId,
		&iam.UndeleteServiceAccountRequest{}).Do()
	require.NoError(t, err)
	assert.Equal(t, original.UniqueId, resp.RestoredAccount.UniqueId)
	got, err := svc.Projects.ServiceAccounts.Get(original.Name).Do()
	require.NoError(t, err)
	assert.Equal(t, original.UniqueId, got.UniqueId, "the email now names the restored account")
}
