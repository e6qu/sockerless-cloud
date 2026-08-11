package gcp_sdk_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/dns/v1"
)

// TestDNS_PolicyCRUD round-trips a Cloud DNS server Policy (create/get/list/
// patch/update/delete) through the Google Cloud Go SDK.
func TestDNS_PolicyCRUD(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"

	created, err := svc.Policies.Create(project, &dns.Policy{
		Name:                    "fwd-policy",
		Description:             "forwarding policy",
		EnableInboundForwarding: true,
		EnableLogging:           true,
		AlternativeNameServerConfig: &dns.PolicyAlternativeNameServerConfig{
			TargetNameServers: []*dns.PolicyAlternativeNameServerConfigTargetNameServer{{
				Ipv4Address:    "10.0.0.53",
				ForwardingPath: "private",
			}},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "fwd-policy", created.Name)
	assert.True(t, created.EnableInboundForwarding)
	assert.NotEmpty(t, created.Id)
	require.NotNil(t, created.AlternativeNameServerConfig)
	require.Len(t, created.AlternativeNameServerConfig.TargetNameServers, 1)
	assert.Equal(t, "10.0.0.53", created.AlternativeNameServerConfig.TargetNameServers[0].Ipv4Address)

	got, err := svc.Policies.Get(project, "fwd-policy").Do()
	require.NoError(t, err)
	assert.Equal(t, "fwd-policy", got.Name)
	assert.Equal(t, "forwarding policy", got.Description)

	list, err := svc.Policies.List(project).Do()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(list.Policies), 1)

	patched, err := svc.Policies.Patch(project, "fwd-policy", &dns.Policy{
		Description: "patched description",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, patched.Policy)
	assert.Equal(t, "patched description", patched.Policy.Description)

	updated, err := svc.Policies.Update(project, "fwd-policy", &dns.Policy{
		Description:             "replaced description",
		EnableInboundForwarding: false,
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, updated.Policy)
	assert.Equal(t, "replaced description", updated.Policy.Description)
	assert.False(t, updated.Policy.EnableInboundForwarding)

	err = svc.Policies.Delete(project, "fwd-policy").Do()
	require.NoError(t, err)

	_, err = svc.Policies.Get(project, "fwd-policy").Do()
	require.Error(t, err)
}

// TestDNS_ResponsePolicyAndRulesCRUD round-trips a ResponsePolicy and its
// nested ResponsePolicyRule subresource.
func TestDNS_ResponsePolicyAndRulesCRUD(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"

	rp, err := svc.ResponsePolicies.Create(project, &dns.ResponsePolicy{
		ResponsePolicyName: "rp-1",
		Description:        "response policy",
		Labels:             map[string]string{"team": "net"},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "rp-1", rp.ResponsePolicyName)
	assert.NotEmpty(t, rp.Id)

	gotRP, err := svc.ResponsePolicies.Get(project, "rp-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "response policy", gotRP.Description)
	assert.Equal(t, "net", gotRP.Labels["team"])

	rpList, err := svc.ResponsePolicies.List(project).Do()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rpList.ResponsePolicies), 1)

	patchedRP, err := svc.ResponsePolicies.Patch(project, "rp-1", &dns.ResponsePolicy{
		Description: "patched rp",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, patchedRP.ResponsePolicy)
	assert.Equal(t, "patched rp", patchedRP.ResponsePolicy.Description)

	updatedRP, err := svc.ResponsePolicies.Update(project, "rp-1", &dns.ResponsePolicy{
		ResponsePolicyName: "rp-1",
		Description:        "replaced rp",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, updatedRP.ResponsePolicy)
	assert.Equal(t, "replaced rp", updatedRP.ResponsePolicy.Description)

	// Rules.
	rule, err := svc.ResponsePolicyRules.Create(project, "rp-1", &dns.ResponsePolicyRule{
		RuleName: "rule-1",
		DnsName:  "override.example.com.",
		LocalData: &dns.ResponsePolicyRuleLocalData{
			LocalDatas: []*dns.ResourceRecordSet{{
				Name:    "override.example.com.",
				Type:    "A",
				Ttl:     300,
				Rrdatas: []string{"198.51.100.7"},
			}},
		},
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "rule-1", rule.RuleName)
	require.NotNil(t, rule.LocalData)
	require.Len(t, rule.LocalData.LocalDatas, 1)
	assert.Equal(t, []string{"198.51.100.7"}, rule.LocalData.LocalDatas[0].Rrdatas)

	gotRule, err := svc.ResponsePolicyRules.Get(project, "rp-1", "rule-1").Do()
	require.NoError(t, err)
	assert.Equal(t, "override.example.com.", gotRule.DnsName)

	ruleList, err := svc.ResponsePolicyRules.List(project, "rp-1").Do()
	require.NoError(t, err)
	require.Len(t, ruleList.ResponsePolicyRules, 1)

	patchedRule, err := svc.ResponsePolicyRules.Patch(project, "rp-1", "rule-1", &dns.ResponsePolicyRule{
		Behavior: "bypassResponsePolicy",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, patchedRule.ResponsePolicyRule)
	assert.Equal(t, "bypassResponsePolicy", patchedRule.ResponsePolicyRule.Behavior)

	updatedRule, err := svc.ResponsePolicyRules.Update(project, "rp-1", "rule-1", &dns.ResponsePolicyRule{
		RuleName: "rule-1",
		DnsName:  "override2.example.com.",
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, updatedRule.ResponsePolicyRule)
	assert.Equal(t, "override2.example.com.", updatedRule.ResponsePolicyRule.DnsName)

	err = svc.ResponsePolicyRules.Delete(project, "rp-1", "rule-1").Do()
	require.NoError(t, err)
	_, err = svc.ResponsePolicyRules.Get(project, "rp-1", "rule-1").Do()
	require.Error(t, err)

	err = svc.ResponsePolicies.Delete(project, "rp-1").Do()
	require.NoError(t, err)
}

// TestDNS_DnsKeysReflectDnssecState verifies that a zone with DNSSEC off
// exposes no DnsKeys, and a DNSSEC-on zone exposes the derived KSK + ZSK with
// real key tags and DS digests.
func TestDNS_DnsKeysReflectDnssecState(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"

	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:    "nodnssec-zone",
		DnsName: "nodnssec.example.com.",
	}).Do()
	require.NoError(t, err)

	offKeys, err := svc.DnsKeys.List(project, "nodnssec-zone").Do()
	require.NoError(t, err)
	assert.Empty(t, offKeys.DnsKeys, "DNSSEC-off zone must expose no DnsKeys")

	_, err = svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:    "dnssec-zone",
		DnsName: "dnssec.example.com.",
		DnssecConfig: &dns.ManagedZoneDnsSecConfig{
			State: "on",
		},
	}).Do()
	require.NoError(t, err)

	keys, err := svc.DnsKeys.List(project, "dnssec-zone").Do()
	require.NoError(t, err)
	require.Len(t, keys.DnsKeys, 2, "DNSSEC-on zone exposes KSK + ZSK")

	var ksk *dns.DnsKey
	for _, k := range keys.DnsKeys {
		assert.NotEmpty(t, k.PublicKey)
		assert.NotZero(t, k.KeyTag)
		require.NotEmpty(t, k.Digests)
		assert.NotEmpty(t, k.Digests[0].Digest)
		assert.Equal(t, "ecdsap256sha256", k.Algorithm)
		if k.Type == "keySigning" {
			ksk = k
		}
	}
	require.NotNil(t, ksk, "a key-signing key must be present")

	got, err := svc.DnsKeys.Get(project, "dnssec-zone", ksk.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, ksk.KeyTag, got.KeyTag)
	assert.Equal(t, "keySigning", got.Type)
}

// TestDNS_ManagedZonePatchAndOperations exercises managedZones.patch/update
// (returning an Operation) and the managedZoneOperations audit-log read-back.
func TestDNS_ManagedZonePatchAndOperations(t *testing.T) {
	svc := dnsService(t)
	project := "test-project"

	_, err := svc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:        "ops-zone",
		DnsName:     "ops.example.com.",
		Description: "initial",
	}).Do()
	require.NoError(t, err)

	patchOp, err := svc.ManagedZones.Patch(project, "ops-zone", &dns.ManagedZone{
		Description: "patched",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "done", patchOp.Status)
	assert.Equal(t, "update", patchOp.Type)

	got, err := svc.ManagedZones.Get(project, "ops-zone").Do()
	require.NoError(t, err)
	assert.Equal(t, "patched", got.Description)

	updateOp, err := svc.ManagedZones.Update(project, "ops-zone", &dns.ManagedZone{
		Name:        "ops-zone",
		DnsName:     "ops.example.com.",
		Description: "replaced",
	}).Do()
	require.NoError(t, err)
	assert.Equal(t, "done", updateOp.Status)

	opsList, err := svc.ManagedZoneOperations.List(project, "ops-zone").Do()
	require.NoError(t, err)
	// insert + patch + update = 3 audit-log operations.
	require.GreaterOrEqual(t, len(opsList.Operations), 3)

	first := opsList.Operations[0]
	assert.Equal(t, "insert", first.Type)

	gotOp, err := svc.ManagedZoneOperations.Get(project, "ops-zone", first.Id).Do()
	require.NoError(t, err)
	assert.Equal(t, first.Id, gotOp.Id)
	require.NotNil(t, gotOp.ZoneContext)
	require.NotNil(t, gotOp.ZoneContext.NewValue)
	assert.Equal(t, "ops-zone", gotOp.ZoneContext.NewValue.Name)
}

// TestDNS_GetProject reads the Cloud DNS Project resource (quota/number).
func TestDNS_GetProject(t *testing.T) {
	svc := dnsService(t)
	proj, err := svc.Projects.Get("test-project").Do()
	require.NoError(t, err)
	assert.Equal(t, "test-project", proj.Id)
	assert.NotEmpty(t, proj.Number)
	require.NotNil(t, proj.Quota)
	assert.Greater(t, proj.Quota.ManagedZones, int64(0))
}
