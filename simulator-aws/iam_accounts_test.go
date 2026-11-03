package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// TestIAMMemberAccountEntitiesPersist stores a role in a member account and
// reads it back after the simulator's state is reopened, as a restart does.
// The management account's store never sees it, and an account the
// simulator does not host holds nothing.
func TestIAMMemberAccountEntitiesPersist(t *testing.T) {
	db0, roles, accounts := iamStoreDB, iamRoles, orgAccounts
	t.Cleanup(func() {
		iamStoreDB, iamRoles, orgAccounts = db0, roles, accounts
		for _, scope := range iamAccountScopes {
			scope.reset()
		}
	})
	const member = "210987654321"
	iamRoles = sim.NewStateStore[IAMRole]()
	orgAccounts = sim.NewStateStore[OrgAccount]()
	orgAccounts.Put(member, OrgAccount{Id: member, Status: "ACTIVE"})

	stateDir := t.TempDir()
	open := func() {
		db, err := sim.OpenDB(stateDir)
		if err != nil {
			t.Fatalf("open simulator state: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		iamStoreDB = db
		for _, scope := range iamAccountScopes {
			scope.reset()
		}
	}
	open()
	orgCreateAccountAccessRole(member, orgAccountAccessRoleName)
	if err := iamStoreDB.Close(); err != nil {
		t.Fatalf("close simulator state: %v", err)
	}

	open()
	role, ok := iamRolesIn(member).Get(orgAccountAccessRoleName)
	if !ok || role.Arn != "arn:aws:iam::"+member+":role/"+orgAccountAccessRoleName {
		t.Fatalf("member role after reopen: %+v %v", role, ok)
	}
	if _, ok := iamRolePoliciesIn(member).Get(orgAccountAccessRoleName + "/" + orgAccountAccessPolicyName); !ok {
		t.Fatal("the role's AdministratorAccess policy did not persist")
	}
	if _, ok := iamRolesIn(awsAccountID()).Get(orgAccountAccessRoleName); ok {
		t.Fatal("the management account holds the member account's role")
	}
	if got, ok := iamRoleByArn("arn:aws:iam::" + member + ":role/" + orgAccountAccessRoleName); !ok || got.RoleId != role.RoleId {
		t.Fatalf("role by ARN: %+v %v", got, ok)
	}
	if _, ok := iamRoleByArn("arn:aws:iam::000011112222:role/" + orgAccountAccessRoleName); ok {
		t.Fatal("an account the simulator does not host holds a role")
	}
}
