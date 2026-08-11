package main

import "testing"

// FuzzIAMSLRName exercises the service-linked-role name derivation with
// arbitrary AWSServiceName form values. A leading-dot / empty leading
// segment must not panic the leading-character slice.
func FuzzIAMSLRName(f *testing.F) {
	f.Add("cloudfront.amazonaws.com", "")
	f.Add("ecs.amazonaws.com", "custom")
	f.Add(".", "")
	f.Add(".x", "suf")
	f.Add("", "")
	f.Add("über.amazonaws.com", "")
	f.Add("a", "")
	f.Fuzz(func(t *testing.T, servicePrincipal, customSuffix string) {
		_ = iamSLRName(servicePrincipal, customSuffix) // must not panic
	})
}
