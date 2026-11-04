package main

import (
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Service control policies cap what the IAM users, roles and root user of a
// member account can do, as the AWS Organizations User Guide's "SCP
// evaluation" describes: every level from the organization root through each
// OU down to the account itself must allow an action, and a deny at any level
// refuses it. They govern no principal of the management account and no
// service-linked role.

const orgSCPType = "SERVICE_CONTROL_POLICY"

// orgSCPEnabled reports whether the organization's root enables service
// control policies.
func orgSCPEnabled() bool {
	root, ok := orgRoots.Get(orgRootID())
	return ok && !root.ServiceControlPoliciesDisabled
}

// orgAttachFullAWSAccess attaches the AWS managed FullAWSAccess policy to a
// new root, OU or account, which AWS Organizations does for every entity it
// creates while the root enables service control policies.
func orgAttachFullAWSAccess(targetID string) {
	if !orgSCPEnabled() {
		return
	}
	orgPolicyAttachments.Put(orgAttachmentKey(orgFullAWSAccessPolicyID, targetID),
		OrgPolicyAttachment{PolicyId: orgFullAWSAccessPolicyID, TargetId: targetID})
}

// orgSCPTargets is every root, OU and account of the organization.
func orgSCPTargets() []string {
	targets := []string{orgRootID()}
	for _, ou := range orgOUs.List() {
		targets = append(targets, ou.Id)
	}
	for _, a := range orgAccounts.List() {
		targets = append(targets, a.Id)
	}
	return targets
}

// orgAttachmentsByTarget answers which policies a root, OU or account has
// attached, which the IAM gate reads for every request of a member account.
var orgAttachmentsByTarget sim.GenerationIndex[OrgPolicyAttachment]

// orgSCPsAttachedTo is the service control policies attached to targetID.
func orgSCPsAttachedTo(targetID string) []OrgPolicy {
	var out []OrgPolicy
	for _, at := range orgAttachmentsByTarget.LookupAll(orgPolicyAttachments, targetID, func(a OrgPolicyAttachment) []string {
		return []string{a.TargetId}
	}) {
		if p, ok := orgPolicies.Get(at.PolicyId); ok && p.Type == orgSCPType {
			out = append(out, p)
		}
	}
	return out
}

// orgAttachFullAWSAccessToUncovered attaches FullAWSAccess to every entity
// that has no service control policy while the root enables them. AWS
// Organizations refuses to detach an entity's last one, so an entity without
// any was stored before the simulator kept the policy on every entity it
// creates.
func orgAttachFullAWSAccessToUncovered() {
	if _, ok := orgOrg.Get(orgSingletonKey); !ok || !orgSCPEnabled() {
		return
	}
	for _, target := range orgSCPTargets() {
		if len(orgSCPsAttachedTo(target)) == 0 {
			orgAttachFullAWSAccess(target)
		}
	}
}

// orgEnableSCPs enables service control policies in the root, attaching
// FullAWSAccess to the root and every OU and account.
func orgEnableSCPs(root *OrgRoot) {
	root.ServiceControlPoliciesDisabled = false
	orgRoots.Put(root.Id, *root)
	for _, target := range orgSCPTargets() {
		orgAttachFullAWSAccess(target)
	}
}

// orgDisableSCPs disables service control policies in the root, detaching
// every one from every entity: re-enabling them starts again from
// FullAWSAccess alone.
func orgDisableSCPs(root *OrgRoot) {
	root.ServiceControlPoliciesDisabled = true
	orgRoots.Put(root.Id, *root)
	for _, at := range orgPolicyAttachments.List() {
		if p, ok := orgPolicies.Get(at.PolicyId); ok && p.Type == orgSCPType {
			orgPolicyAttachments.Delete(orgAttachmentKey(at.PolicyId, at.TargetId))
		}
	}
}

// orgDetachLeavesNoSCP reports whether detaching policyID from targetID
// would leave the target with no service control policy, which AWS
// Organizations refuses while the root enables them.
func orgDetachLeavesNoSCP(policyID, targetID string) bool {
	p, ok := orgPolicies.Get(policyID)
	if !ok || p.Type != orgSCPType || !orgSCPEnabled() {
		return false
	}
	return len(orgSCPsAttachedTo(targetID)) <= 1
}

// orgDetachAll removes every policy attachment of a target leaving the
// organization.
func orgDetachAll(targetID string) {
	for _, at := range orgPolicyAttachments.List() {
		if at.TargetId == targetID {
			orgPolicyAttachments.Delete(orgAttachmentKey(at.PolicyId, at.TargetId))
		}
	}
}

// orgSCPDecision evaluates the service control policies that govern a
// principal of account for action on resource. It returns "allowed" when
// every level from the root down to the account allows the action and none
// denies it, "explicitDeny" when one denies it, and "implicitDeny" when a
// level allows it nowhere. A principal no service control policy governs is
// "allowed".
func orgSCPDecision(caller iamCaller, action, resource string, ctx map[string][]string) string {
	if iamIsManagementAccount(caller.Account) || caller.ServiceLinked || orgOrg == nil {
		return "allowed"
	}
	if _, ok := orgOrg.Get(orgSingletonKey); !ok || !orgSCPEnabled() {
		return "allowed"
	}
	if _, member := orgAccounts.Get(caller.Account); !member {
		return "allowed"
	}
	decision := "allowed"
	for _, target := range orgPathToRoot(caller.Account) {
		var docs []iamPolicyDoc
		for _, p := range orgSCPsAttachedTo(target) {
			doc, err := parseIAMPolicy(p.Content)
			if err != nil {
				panic(fmt.Sprintf("stored service control policy %s does not parse: %v", p.Id, err))
			}
			docs = append(docs, doc)
		}
		switch d, _ := iamEvalDecision(docs, action, resource, ctx); d {
		case "explicitDeny":
			return d
		case "allowed":
		default:
			decision = "implicitDeny"
		}
	}
	return decision
}
