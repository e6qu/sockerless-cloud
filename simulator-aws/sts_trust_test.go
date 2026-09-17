package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestWebIdentityConditionContextMapsTheClaims(t *testing.T) {
	id := webIdentity{
		Provider: IAMOIDCProvider{Arn: "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com", URL: "https://token.actions.githubusercontent.com"},
		Claims: map[string]any{
			"sub": "repo:e6qu/infra:ref:refs/heads/main", "aud": "sts.amazonaws.com",
			"amr":        []any{"authenticated", "github"},
			"repository": "e6qu/infra", "actor": "someone", "unlisted": "never",
		},
	}
	ctx := id.conditionContext("gha")
	for key, want := range map[string][]string{
		"token.actions.githubusercontent.com:sub":        {"repo:e6qu/infra:ref:refs/heads/main"},
		"token.actions.githubusercontent.com:aud":        {"sts.amazonaws.com"},
		"token.actions.githubusercontent.com:oaud":       {"sts.amazonaws.com"},
		"token.actions.githubusercontent.com:amr":        {"authenticated", "github"},
		"token.actions.githubusercontent.com:repository": {"e6qu/infra"},
		"token.actions.githubusercontent.com:actor":      {"someone"},
		"aws:FederatedProvider":                          {id.Provider.Arn},
		"sts:RoleSessionName":                            {"gha"},
	} {
		got := append([]string(nil), ctx[key]...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
	if _, ok := ctx["token.actions.githubusercontent.com:unlisted"]; ok {
		t.Error("a claim AWS STS does not map reached the context")
	}
}

// When the token sets azp, aud is azp and oaud keeps the token's aud.
func TestWebIdentityAudPrefersAzp(t *testing.T) {
	id := webIdentity{
		Provider: IAMOIDCProvider{Arn: "arn:aws:iam::123456789012:oidc-provider/auth.example.test", URL: "https://auth.example.test/"},
		Claims:   map[string]any{"sub": "u", "aud": []any{"client-a"}, "azp": "client-b"},
	}
	ctx := id.conditionContext("s")
	if got := ctx["auth.example.test:aud"]; !reflect.DeepEqual(got, []string{"client-b"}) {
		t.Errorf("aud = %v, want [client-b]", got)
	}
	if got := ctx["auth.example.test:oaud"]; !reflect.DeepEqual(got, []string{"client-a"}) {
		t.Errorf("oaud = %v, want [client-a]", got)
	}
}

func TestWebIdentityProviderTemplates(t *testing.T) {
	for _, tc := range []struct {
		template, name string
		want           bool
	}{
		{"agent.${Domain}.buildkite.dev", "agent.acme.buildkite.dev", true},
		{"agent.${Domain}.buildkite.dev", "agent.buildkite.dev", false},
		{"token.actions.githubusercontent.com/${SubPath}", "token.actions.githubusercontent.com/e6qu", true},
		{"token.actions.githubusercontent.com", "token.actions.githubusercontent.com.evil.test", false},
	} {
		if got := webIdentityProviderMatches(tc.template, tc.name); got != tc.want {
			t.Errorf("%s vs %s = %v, want %v", tc.template, tc.name, got, tc.want)
		}
	}
}

func TestTrustPolicyAdmitsOnlyItsPrincipals(t *testing.T) {
	provider := "arn:aws:iam::123456789012:oidc-provider/auth.example.test"
	role := IAMRole{Arn: "arn:aws:iam::123456789012:role/r", AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"Federated":"` + provider + `"},"Action":"sts:AssumeRoleWithWebIdentity",
		 "Condition":{"StringEquals":{"auth.example.test:sub":"operator"}}},
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/named"},"Action":"sts:AssumeRole"},
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:root"},"Action":"sts:AssumeRole",
		 "Condition":{"StringEquals":{"sts:ExternalId":"expected"}}}]}`}

	ctx := func(pairs ...string) map[string][]string {
		out := map[string][]string{}
		for i := 0; i < len(pairs); i += 2 {
			out[pairs[i]] = []string{pairs[i+1]}
		}
		return out
	}
	for name, tc := range map[string]struct {
		action, caller string
		ctx            map[string][]string
		want           bool
	}{
		"the provider, the right subject": {"sts:AssumeRoleWithWebIdentity", "federated:" + provider, ctx("auth.example.test:sub", "operator"), true},
		"the provider, another subject":   {"sts:AssumeRoleWithWebIdentity", "federated:" + provider, ctx("auth.example.test:sub", "intruder"), false},
		"another provider":                {"sts:AssumeRoleWithWebIdentity", "federated:arn:aws:iam::123456789012:oidc-provider/other.test", ctx("auth.example.test:sub", "operator"), false},
		"the named user":                  {"sts:AssumeRole", "arn:aws:iam::123456789012:user/named", ctx(), true},
		"another user, no external id":    {"sts:AssumeRole", "arn:aws:iam::123456789012:user/other", ctx(), false},
		"another user, the external id":   {"sts:AssumeRole", "arn:aws:iam::123456789012:user/other", ctx("sts:ExternalId", "expected"), true},
		"another account":                 {"sts:AssumeRole", "arn:aws:iam::999999999999:user/other", ctx("sts:ExternalId", "expected"), false},
	} {
		if got := stsTrustAllows(role, tc.action, tc.caller, tc.ctx); got != tc.want {
			t.Errorf("%s: allowed = %v, want %v", name, got, tc.want)
		}
	}
}
