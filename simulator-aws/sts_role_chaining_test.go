package main

import (
	"slices"
	"strings"
	"testing"
)

// TestSTSChainedFederationKeysAreTheOnesAssumeRoleDeclares holds
// stsChainedFederationKeys to the identity-provider keys the vendored Service
// Reference declares on sts:AssumeRole.
func TestSTSChainedFederationKeysAreTheOnesAssumeRoleDeclares(t *testing.T) {
	var declared []string
	for key, actions := range iamDeclaredConditionKeys(t) {
		service, _, _ := strings.Cut(key, ":")
		if service == "aws" || service == "sts" || service == "iam" || !slices.Contains(actions, "sts:AssumeRole") {
			continue
		}
		declared = append(declared, key)
	}
	slices.Sort(declared)
	chained := slices.Clone(stsChainedFederationKeys)
	slices.Sort(chained)
	if !slices.Equal(declared, chained) {
		t.Errorf("sts:AssumeRole declares provider keys %v; stsChainedFederationKeys carries %v", declared, chained)
	}
}

// TestSTSChainedClaimsKeepOnlyTheChainedKeys covers what a SAML session keeps
// for a chained AssumeRole: its subject, subject type and name qualifier, and
// not the claims AWS STS reads only at AssumeRoleWithSAML.
func TestSTSChainedClaimsKeepOnlyTheChainedKeys(t *testing.T) {
	assertion := samlAssertion{Issuer: "https://idp.example/saml", Subject: "alice", SubjectType: "persistent",
		Recipient: "https://signin.aws.amazon.com/saml", ProviderName: "idp"}
	claims := stsChainedClaims(assertion.conditionContext("123456789012"))
	if got := claims["saml:sub"]; len(got) != 1 || got[0] != "alice" {
		t.Errorf("saml:sub = %v, want [alice]", got)
	}
	if got := claims["saml:sub_type"]; len(got) != 1 || got[0] != "persistent" {
		t.Errorf("saml:sub_type = %v, want [persistent]", got)
	}
	for _, key := range []string{"saml:aud", "saml:iss", "saml:doc", "sts:RoleSessionName"} {
		if _, kept := claims[key]; kept {
			t.Errorf("a chained session carries %s, which AWS STS reads only at AssumeRoleWithSAML", key)
		}
	}
}
