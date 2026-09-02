package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// The Compute Engine policy collections: a policy holding an ordered rule list
// and a list of associations.
//
// The assertions are on what the policy remembers — a rule read back at the
// priority it was added under, the order the rules end up in, an association
// that disappears when it is detached — because the verbs all answer with an
// Operation, and an Operation says nothing about whether anything changed.

func TestCompute_SecurityPolicyRuleLifecycle(t *testing.T) {
	svc := computeService(t)
	const project = "armor-project"

	_, err := svc.SecurityPolicies.Insert(project, &compute.SecurityPolicy{
		Name: "edge", Description: "edge protection",
	}).Do()
	require.NoError(t, err)

	// Rules are added out of priority order and must come back in it.
	for _, priority := range []int64{2000, 1000} {
		_, err = svc.SecurityPolicies.AddRule(project, "edge", &compute.SecurityPolicyRule{
			Priority: priority,
			Action:   "deny(403)",
			Match: &compute.SecurityPolicyRuleMatcher{
				VersionedExpr: "SRC_IPS_V1",
				Config:        &compute.SecurityPolicyRuleMatcherConfig{SrcIpRanges: []string{"10.0.0.0/8"}},
			},
		}).Do()
		require.NoError(t, err)
	}

	policy, err := svc.SecurityPolicies.Get(project, "edge").Do()
	require.NoError(t, err)
	require.Len(t, policy.Rules, 2)
	assert.Equal(t, int64(1000), policy.Rules[0].Priority, "a policy's rules are held in priority order")
	assert.Equal(t, int64(2000), policy.Rules[1].Priority)

	rule, err := svc.SecurityPolicies.GetRule(project, "edge").Priority(1000).Do()
	require.NoError(t, err)
	assert.Equal(t, "deny(403)", rule.Action)

	// The same priority twice is a conflict, not a silent replacement.
	_, err = svc.SecurityPolicies.AddRule(project, "edge", &compute.SecurityPolicyRule{
		Priority: 1000, Action: "allow",
	}).Do()
	require.Error(t, err)

	_, err = svc.SecurityPolicies.PatchRule(project, "edge", &compute.SecurityPolicyRule{
		Action: "allow",
	}).Priority(1000).Do()
	require.NoError(t, err)
	rule, err = svc.SecurityPolicies.GetRule(project, "edge").Priority(1000).Do()
	require.NoError(t, err)
	assert.Equal(t, "allow", rule.Action, "the patch reached the stored rule")

	_, err = svc.SecurityPolicies.RemoveRule(project, "edge").Priority(1000).Do()
	require.NoError(t, err)
	policy, err = svc.SecurityPolicies.Get(project, "edge").Do()
	require.NoError(t, err)
	require.Len(t, policy.Rules, 1)
	assert.Equal(t, int64(2000), policy.Rules[0].Priority)

	// A priority that is not there reports itself.
	_, err = svc.SecurityPolicies.GetRule(project, "edge").Priority(1000).Do()
	require.Error(t, err)
}

func TestCompute_RegionalSecurityPolicyIsItsOwnResource(t *testing.T) {
	svc := computeService(t)
	const project, region = "armor-regional", "us-central1"

	_, err := svc.RegionSecurityPolicies.Insert(project, region, &compute.SecurityPolicy{
		Name: "regional-edge",
	}).Do()
	require.NoError(t, err)
	_, err = svc.RegionSecurityPolicies.AddRule(project, region, "regional-edge", &compute.SecurityPolicyRule{
		Priority: 500, Action: "deny(404)",
	}).Do()
	require.NoError(t, err)

	got, err := svc.RegionSecurityPolicies.Get(project, region, "regional-edge").Do()
	require.NoError(t, err)
	require.Len(t, got.Rules, 1)

	// The global collection is a different namespace, so the same name there
	// is a different policy.
	_, err = svc.SecurityPolicies.Get(project, "regional-edge").Do()
	require.Error(t, err)

	// The aggregated read spans the project's scopes:
	//
	//	GET /compute/v1/projects/{project}/aggregated/securityPolicies
	_, err = svc.SecurityPolicies.Insert(project, &compute.SecurityPolicy{Name: "global-edge"}).Do()
	require.NoError(t, err)
	aggregated, err := svc.SecurityPolicies.AggregatedList(project).Do()
	require.NoError(t, err)
	var scopes []string
	for scope := range aggregated.Items {
		scopes = append(scopes, scope)
	}
	assert.Contains(t, scopes, "global")
	assert.Contains(t, scopes, "regions/"+region)
}

func TestCompute_OrganizationFirewallPolicyAssociations(t *testing.T) {
	svc := computeService(t)

	_, err := svc.FirewallPolicies.Insert(&compute.FirewallPolicy{
		Name: "org-policy", ShortName: "org-policy",
	}).Do()
	require.NoError(t, err)

	_, err = svc.FirewallPolicies.AddAssociation("org-policy", &compute.FirewallPolicyAssociation{
		Name:             "to-prod",
		AttachmentTarget: "organizations/12345",
	}).Do()
	require.NoError(t, err)

	association, err := svc.FirewallPolicies.GetAssociation("org-policy").Name("to-prod").Do()
	require.NoError(t, err)
	assert.Equal(t, "organizations/12345", association.AttachmentTarget)

	listed, err := svc.FirewallPolicies.ListAssociations().Do()
	require.NoError(t, err)
	require.Len(t, listed.Associations, 1)

	_, err = svc.FirewallPolicies.RemoveAssociation("org-policy").Name("to-prod").Do()
	require.NoError(t, err)
	_, err = svc.FirewallPolicies.GetAssociation("org-policy").Name("to-prod").Do()
	require.Error(t, err, "the association was removed, so reading it must fail")
}

func TestCompute_OrganizationFirewallPolicyClonesAnotherPolicysRules(t *testing.T) {
	svc := computeService(t)

	_, err := svc.FirewallPolicies.Insert(&compute.FirewallPolicy{
		Name: "source-policy", ShortName: "source-policy",
	}).Do()
	require.NoError(t, err)
	_, err = svc.FirewallPolicies.AddRule("source-policy", &compute.FirewallPolicyRule{
		Priority: 100, Action: "deny", Direction: "INGRESS",
	}).Do()
	require.NoError(t, err)

	_, err = svc.FirewallPolicies.Insert(&compute.FirewallPolicy{
		Name: "target-policy", ShortName: "target-policy",
	}).Do()
	require.NoError(t, err)

	_, err = svc.FirewallPolicies.CloneRules("target-policy").
		SourceFirewallPolicy("source-policy").Do()
	require.NoError(t, err)

	target, err := svc.FirewallPolicies.Get("target-policy").Do()
	require.NoError(t, err)
	require.Len(t, target.Rules, 1, "the clone copied the source's rules rather than acknowledging the call")
	assert.Equal(t, int64(100), target.Rules[0].Priority)

	// Cloning from a policy that does not exist reports that.
	_, err = svc.FirewallPolicies.CloneRules("target-policy").
		SourceFirewallPolicy("absent-policy").Do()
	require.Error(t, err)
}

// The preconfigured WAF expression sets are Google's own catalogue, which this
// simulator has no basis for. It answers a declared NotImplemented rather than
// letting the read beside it swallow the path and report the method as a
// policy that does not exist.
func TestCompute_PreconfiguredExpressionSetsAreADeclaredGap(t *testing.T) {
	svc := computeService(t)
	_, err := svc.SecurityPolicies.ListPreconfiguredExpressionSets("expr-project").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "501")
}
