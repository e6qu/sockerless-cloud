package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CreateAccount preconfigures the new member account with a role that grants
// the management account administrator permissions in it, named
// OrganizationAccountAccessRole unless the request names another. An account
// that joins by invitation gets no such role.

const (
	orgAccountAccessRoleName   = "OrganizationAccountAccessRole"
	orgAccountAccessPolicyName = "AdministratorAccess"
	orgAdministratorAccess     = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
)

var orgRoleNamePattern = regexp.MustCompile(`^[\w+=,.@-]{1,64}$`)

func orgCreateAccountAccessRole(account, name string) {
	trust := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"%s"},"Action":"sts:AssumeRole"}]}`,
		iamRootArn(awsAccountID()))
	iamRolesIn(account).Put(name, IAMRole{
		RoleName:                 name,
		RoleId:                   "AROA" + strings.ToUpper(sim.NewUUID()[:16]),
		Arn:                      fmt.Sprintf("arn:aws:iam::%s:role/%s", account, name),
		Path:                     "/",
		AssumeRolePolicyDocument: trust,
		CreateDate:               time.Now().UTC().Format(time.RFC3339),
	})
	iamRolePoliciesIn(account).Put(name+"/"+orgAccountAccessPolicyName, IAMRolePolicy{
		RoleName:       name,
		PolicyName:     orgAccountAccessPolicyName,
		PolicyDocument: orgAdministratorAccess,
	})
}
