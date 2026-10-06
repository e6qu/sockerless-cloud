package main

import "testing"

func TestRBACActionMatchesAny(t *testing.T) {
	const getSecret = "Microsoft.KeyVault/vaults/secrets/getSecret/action"
	cases := []struct {
		patterns []string
		want     bool
	}{
		{[]string{getSecret}, true},
		{[]string{"microsoft.keyvault/vaults/secrets/getsecret/action"}, true},
		{[]string{"Microsoft.KeyVault/vaults/secrets/*"}, true},
		{[]string{"Microsoft.KeyVault/vaults/*"}, true},
		{[]string{"*"}, true},
		{[]string{"Microsoft.KeyVault/vaults/*/read"}, false},
		{[]string{"Microsoft.KeyVault/vaults/keys/*"}, false},
		{[]string{"Microsoft.KeyVault/vaults/secrets/readMetadata/action"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := rbacActionMatchesAny(c.patterns, getSecret); got != c.want {
			t.Errorf("%v matching %s = %v, want %v", c.patterns, getSecret, got, c.want)
		}
	}
}
