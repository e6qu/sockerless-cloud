package main

import (
	"fmt"
	"net/http"
	"strings"
)

// Each AWS Organizations operation names, in the model's documentation, which
// accounts can call it: the management account only, the management account
// or a delegated administrator, any account in the organization, or (for the
// operations that declare no AWSOrganizationsNotInUseException) an account in
// no organization at all. The Organizations User Guide's "Delegated
// administrator for AWS services that work with Organizations" lists the
// read-only operations a delegated administrator for any service can call, and
// "Create a resource-based delegation policy" lists the operations the
// organization's resource-based delegation policy can open to a member
// account.

type orgCallerScope int

const (
	// orgAnyCaller admits an account in no organization too.
	orgAnyCaller orgCallerScope = iota
	orgAnyAccount
	orgMemberAccountOnly
	// orgDelegatedReadOnly admits a delegated administrator for any service,
	// and a member account the delegation policy allows.
	orgDelegatedReadOnly
	// orgDelegable admits a member account the delegation policy allows.
	orgDelegable
	orgManagementOnly
)

var orgCallerScopes = map[string]orgCallerScope{
	"CreateOrganization":       orgAnyCaller,
	"AcceptHandshake":          orgAnyCaller,
	"CancelHandshake":          orgAnyCaller,
	"DeclineHandshake":         orgAnyCaller,
	"DescribeHandshake":        orgAnyCaller,
	"ListHandshakesForAccount": orgAnyCaller,

	"DescribeOrganization":    orgAnyAccount,
	"DescribeEffectivePolicy": orgAnyAccount,

	"LeaveOrganization": orgMemberAccountOnly,

	"DescribeAccount":                        orgDelegatedReadOnly,
	"DescribeCreateAccountStatus":            orgDelegatedReadOnly,
	"DescribeOrganizationalUnit":             orgDelegatedReadOnly,
	"DescribePolicy":                         orgDelegatedReadOnly,
	"DescribeResourcePolicy":                 orgDelegatedReadOnly,
	"ListAccounts":                           orgDelegatedReadOnly,
	"ListAccountsForParent":                  orgDelegatedReadOnly,
	"ListAccountsWithInvalidEffectivePolicy": orgDelegatedReadOnly,
	"ListAWSServiceAccessForOrganization":    orgDelegatedReadOnly,
	"ListChildren":                           orgDelegatedReadOnly,
	"ListCreateAccountStatus":                orgDelegatedReadOnly,
	"ListDelegatedAdministrators":            orgDelegatedReadOnly,
	"ListDelegatedServicesForAccount":        orgDelegatedReadOnly,
	"ListEffectivePolicyValidationErrors":    orgDelegatedReadOnly,
	"ListHandshakesForOrganization":          orgDelegatedReadOnly,
	"ListOrganizationalUnitsForParent":       orgDelegatedReadOnly,
	"ListParents":                            orgDelegatedReadOnly,
	"ListPolicies":                           orgDelegatedReadOnly,
	"ListPoliciesForTarget":                  orgDelegatedReadOnly,
	"ListRoots":                              orgDelegatedReadOnly,
	"ListTagsForResource":                    orgDelegatedReadOnly,
	"ListTargetsForPolicy":                   orgDelegatedReadOnly,

	"AttachPolicy":      orgDelegable,
	"CreatePolicy":      orgDelegable,
	"DeletePolicy":      orgDelegable,
	"DetachPolicy":      orgDelegable,
	"DisablePolicyType": orgDelegable,
	"EnablePolicyType":  orgDelegable,
	"TagResource":       orgDelegable,
	"UntagResource":     orgDelegable,
	"UpdatePolicy":      orgDelegable,

	"CloseAccount":                     orgManagementOnly,
	"CreateAccount":                    orgManagementOnly,
	"CreateGovCloudAccount":            orgManagementOnly,
	"CreateOrganizationalUnit":         orgManagementOnly,
	"DeleteOrganization":               orgManagementOnly,
	"DeleteOrganizationalUnit":         orgManagementOnly,
	"DeleteResourcePolicy":             orgManagementOnly,
	"DeregisterDelegatedAdministrator": orgManagementOnly,
	"DisableAWSServiceAccess":          orgManagementOnly,
	"EnableAWSServiceAccess":           orgManagementOnly,
	"EnableAllFeatures":                orgManagementOnly,
	"InviteAccountToOrganization":      orgManagementOnly,
	"MoveAccount":                      orgManagementOnly,
	"PutResourcePolicy":                orgManagementOnly,
	"RegisterDelegatedAdministrator":   orgManagementOnly,
	"RemoveAccountFromOrganization":    orgManagementOnly,
	"UpdateOrganizationalUnit":         orgManagementOnly,
	// A responsibility transfer is an arrangement between two organizations'
	// management accounts.
	"InviteOrganizationToTransferResponsibility": orgManagementOnly,
	"DescribeResponsibilityTransfer":             orgManagementOnly,
	"UpdateResponsibilityTransfer":               orgManagementOnly,
	"TerminateResponsibilityTransfer":            orgManagementOnly,
	"ListInboundResponsibilityTransfers":         orgManagementOnly,
	"ListOutboundResponsibilityTransfers":        orgManagementOnly,
}

const orgTargetPrefix = "AWSOrganizationsV20161128."

// orgGateCallers puts every AWS Organizations operation behind its caller
// rule. An operation without one is a programming error.
func orgGateCallers(r *AWSRouter) {
	for _, target := range r.Targets() {
		op, ok := strings.CutPrefix(target, orgTargetPrefix)
		if !ok {
			continue
		}
		scope, ok := orgCallerScopes[op]
		if !ok {
			panic(fmt.Sprintf("AWS Organizations operation %s has no caller rule", op))
		}
		r.Wrap(target, func(next http.HandlerFunc) http.HandlerFunc {
			return func(w http.ResponseWriter, req *http.Request) {
				if orgAdmitCaller(w, req, op, scope) {
					next(w, req)
				}
			}
		})
	}
}

// orgAdmitCaller reports whether the request's account may call op, and
// answers AWS Organizations' own error when it may not. The operation's
// handler answers for an organization that does not exist.
func orgAdmitCaller(w http.ResponseWriter, r *http.Request, op string, scope orgCallerScope) bool {
	if scope == orgAnyCaller {
		return true
	}
	if _, ok := orgOrg.Get(orgSingletonKey); !ok {
		return true
	}
	account := iamRequestAccount(r)
	if iamIsManagementAccount(account) {
		return true
	}
	if _, member := orgAccounts.Get(account); !member {
		AWSError(w, "AWSOrganizationsNotInUseException", "Your account is not a member of an organization.", http.StatusBadRequest)
		return false
	}
	admitted := false
	switch scope {
	case orgAnyAccount, orgMemberAccountOnly:
		admitted = true
	case orgDelegatedReadOnly:
		admitted = orgDelegatedAdministratorAccount(account) || orgDelegationAllows(r, op)
	case orgDelegable:
		admitted = orgDelegationAllows(r, op)
	}
	if !admitted {
		// The model's AccessDeniedException documentation.
		AWSError(w, "AccessDeniedException", "You don't have permissions to perform the requested operation.", http.StatusForbidden)
	}
	return admitted
}

// orgDelegatedAdministratorAccount reports whether account is a delegated
// administrator for any AWS service.
func orgDelegatedAdministratorAccount(account string) bool {
	return len(orgDelegatedAdmins.Filter(func(d OrgDelegatedAdmin) bool { return d.AccountId == account })) > 0
}

// orgDelegationAllows reports whether the organization's resource-based
// delegation policy allows the request's principal op on every resource the
// request names, and no statement of it denies one.
func orgDelegationAllows(r *http.Request, op string) bool {
	rp, ok := orgResourcePolicies.Get(orgSingletonKey)
	if !ok {
		return false
	}
	doc, err := parseIAMPolicy(rp.Content)
	if err != nil {
		panic(fmt.Sprintf("stored AWS Organizations resource policy %s does not parse: %v", rp.Id, err))
	}
	akid := iamAccessKeyIDFromRequest(r)
	caller, ok := iamCallerForAccessKey(akid)
	if !ok {
		return false
	}
	action := "organizations:" + op
	for _, target := range iamAuthorizationTargets(r, action) {
		ctx := iamRequestConditionContext(r, akid, caller.Arn, caller.UserName, target.action)
		for key, values := range target.context {
			ctx[key] = values
		}
		docs := []iamPolicyDoc{doc}
		// A statement naming the member account delegates to it; the gate has
		// already held the caller to its own account's policies.
		switch decision, _, _ := iamEvalResourceDecision(docs, target.action, target.resource, caller.Arn, caller.EntityArn, ctx); {
		case decision == "explicitDeny":
			return false
		case decision != "allowed" && !iamResourcePolicyAdmitsAccount(docs, target.action, target.resource, caller.Arn, ctx):
			return false
		}
	}
	return true
}
