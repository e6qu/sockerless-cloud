package gcp_sdk_test

import (
	"google.golang.org/api/googleapi"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compute "google.golang.org/api/compute/v1"
)

// Every policy verb answers with an Operation, which says nothing about what
// changed, so the assertions read back what the policy remembers: a rule at
// its priority, the rule order, an association gone after detach.

func TestCompute_SecurityPolicyRuleLifecycle(t *testing.T) {
	t.Parallel()
	ensureProject(t, "armor-project")
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
	t.Parallel()
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

// orgFirewallPolicy inserts an organization firewall policy under the test
// organization and returns the numeric name Compute Engine allocated it, which
// the insert operation carries as its targetId.
func orgFirewallPolicy(t *testing.T, svc *compute.Service, shortName string) string {
	t.Helper()
	op, err := svc.FirewallPolicies.Insert(&compute.FirewallPolicy{ShortName: shortName}).
		ParentId("organizations/12345").Do()
	require.NoError(t, err)
	require.NotZero(t, op.TargetId)
	return strconv.FormatUint(op.TargetId, 10)
}

func TestCompute_OrganizationFirewallPolicyAssociations(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	name := orgFirewallPolicy(t, svc, "org-policy")

	_, err := svc.FirewallPolicies.AddAssociation(name, &compute.FirewallPolicyAssociation{
		Name:             "to-prod",
		AttachmentTarget: "organizations/12345",
	}).Do()
	require.NoError(t, err)

	association, err := svc.FirewallPolicies.GetAssociation(name).Name("to-prod").Do()
	require.NoError(t, err)
	assert.Equal(t, "organizations/12345", association.AttachmentTarget)

	listed, err := svc.FirewallPolicies.ListAssociations().TargetResource("organizations/12345").Do()
	require.NoError(t, err)
	require.Len(t, listed.Associations, 1)

	_, err = svc.FirewallPolicies.RemoveAssociation(name).Name("to-prod").Do()
	require.NoError(t, err)
	_, err = svc.FirewallPolicies.GetAssociation(name).Name("to-prod").Do()
	require.Error(t, err, "the association was removed, so reading it must fail")
}

func TestCompute_OrganizationFirewallPolicyIdentity(t *testing.T) {
	t.Parallel()
	svc := computeService(t)

	op, err := svc.FirewallPolicies.Insert(&compute.FirewallPolicy{ShortName: "org-identity", Description: "edge"}).
		ParentId("folders/424242").Do()
	require.NoError(t, err)
	assert.Equal(t, "https://www.googleapis.com/compute/v1/locations/global/operations/"+op.Name, op.SelfLink,
		"an organization policy's operation lives under the organization's operations")
	name := strconv.FormatUint(op.TargetId, 10)
	assert.Equal(t, "https://www.googleapis.com/compute/v1/locations/global/firewallPolicies/"+name, op.TargetLink)

	got, err := svc.FirewallPolicies.Get(name).Do()
	require.NoError(t, err)
	assert.Equal(t, name, got.Name, "the name is the numeric ID Compute Engine allocates")
	assert.Equal(t, op.TargetId, got.Id)
	assert.Equal(t, "org-identity", got.ShortName)
	assert.Equal(t, "folders/424242", got.Parent, "the policy records the parentId it was inserted under")
	assert.Equal(t, op.TargetLink, got.SelfLink)

	polled, err := svc.GlobalOrganizationOperations.Get(op.Name).ParentId("folders/424242").Do()
	require.NoError(t, err)
	assert.Equal(t, "DONE", polled.Status)

	listed, err := svc.FirewallPolicies.List().ParentId("folders/424242").Do()
	require.NoError(t, err)
	require.Len(t, listed.Items, 1, "the list is the parent's policies")
	assert.Equal(t, name, listed.Items[0].Name)

	_, err = svc.FirewallPolicies.Insert(&compute.FirewallPolicy{ShortName: "org-identity"}).ParentId("folders/424242").Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 409, apiErr.Code, "a parent holds one policy per short name")

	_, err = svc.FirewallPolicies.Insert(&compute.FirewallPolicy{Description: "no short name"}).ParentId("folders/424242").Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 400, apiErr.Code, "an organization policy needs a short name")
}

func TestCompute_OrganizationFirewallPolicyClonesAnotherPolicysRules(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	source := orgFirewallPolicy(t, svc, "source-policy")
	_, err := svc.FirewallPolicies.AddRule(source, &compute.FirewallPolicyRule{
		Priority: 100, Action: "deny", Direction: "INGRESS",
	}).Do()
	require.NoError(t, err)
	target := orgFirewallPolicy(t, svc, "target-policy")

	_, err = svc.FirewallPolicies.CloneRules(target).SourceFirewallPolicy(source).Do()
	require.NoError(t, err)

	cloned, err := svc.FirewallPolicies.Get(target).Do()
	require.NoError(t, err)
	require.Len(t, cloned.Rules, 1, "the clone copied the source's rules rather than acknowledging the call")
	assert.Equal(t, int64(100), cloned.Rules[0].Priority)

	// Cloning from a policy that does not exist reports that.
	_, err = svc.FirewallPolicies.CloneRules(target).SourceFirewallPolicy("1").Do()
	require.Error(t, err)
}

func TestCompute_NetworkFirewallPolicyClonesAnotherPolicysRules(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	const project, region = "sdk-fw-clone", "us-central1"
	ensureProject(t, project)

	_, err := svc.NetworkFirewallPolicies.Insert(project, &compute.FirewallPolicy{Name: "clone-source"}).Do()
	require.NoError(t, err)
	_, err = svc.NetworkFirewallPolicies.AddRule(project, "clone-source", &compute.FirewallPolicyRule{
		Priority: 200, Action: "deny", Direction: "INGRESS",
		Match: &compute.FirewallPolicyRuleMatcher{SrcIpRanges: []string{"10.0.0.0/8"}},
	}).Do()
	require.NoError(t, err)
	_, err = svc.NetworkFirewallPolicies.Insert(project, &compute.FirewallPolicy{Name: "clone-target"}).Do()
	require.NoError(t, err)

	source, err := svc.NetworkFirewallPolicies.Get(project, "clone-source").Do()
	require.NoError(t, err)
	_, err = svc.NetworkFirewallPolicies.CloneRules(project, "clone-target").SourceFirewallPolicy(source.SelfLink).Do()
	require.NoError(t, err)
	cloned, err := svc.NetworkFirewallPolicies.Get(project, "clone-target").Do()
	require.NoError(t, err)
	require.Len(t, cloned.Rules, 1)
	assert.Equal(t, int64(200), cloned.Rules[0].Priority)
	assert.Equal(t, []string{"10.0.0.0/8"}, cloned.Rules[0].Match.SrcIpRanges)

	_, err = svc.NetworkFirewallPolicies.CloneRules(project, "clone-target").SourceFirewallPolicy("absent").Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, 404, apiErr.Code, "an unknown source policy is not found")

	_, err = svc.RegionNetworkFirewallPolicies.Insert(project, region, &compute.FirewallPolicy{Name: "regional-source"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionNetworkFirewallPolicies.AddRule(project, region, "regional-source", &compute.FirewallPolicyRule{
		Priority: 300, Action: "allow", Direction: "EGRESS",
		Match: &compute.FirewallPolicyRuleMatcher{DestIpRanges: []string{"0.0.0.0/0"}},
	}).Do()
	require.NoError(t, err)
	_, err = svc.RegionNetworkFirewallPolicies.Insert(project, region, &compute.FirewallPolicy{Name: "regional-target"}).Do()
	require.NoError(t, err)
	_, err = svc.RegionNetworkFirewallPolicies.CloneRules(project, region, "regional-target").SourceFirewallPolicy("regional-source").Do()
	require.NoError(t, err)
	regional, err := svc.RegionNetworkFirewallPolicies.Get(project, region, "regional-target").Do()
	require.NoError(t, err)
	require.Len(t, regional.Rules, 1)
	assert.Equal(t, "EGRESS", regional.Rules[0].Direction)
}

// TestCompute_PreconfiguredExpressionSetsAreNotSwallowedByThePolicyRead: the
// listPreconfiguredExpressionSets read must not fall to the {policy} read
// beside it and answer as a missing policy. The catalogue itself is
// TestCompute_PreconfiguredExpressionSetsAreTheVendoredCatalog's.
func TestCompute_PreconfiguredExpressionSetsAreNotSwallowedByThePolicyRead(t *testing.T) {
	t.Parallel()
	svc := computeService(t)
	response, err := svc.SecurityPolicies.ListPreconfiguredExpressionSets("expr-project").Do()
	require.NoError(t, err, "the catalogue answers rather than being read as a policy name")
	require.NotNil(t, response.PreconfiguredExpressionSets)
	require.NotNil(t, response.PreconfiguredExpressionSets.WafRules)
	assert.NotEmpty(t, response.PreconfiguredExpressionSets.WafRules.ExpressionSets)
}
