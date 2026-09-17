package main

import (
	"reflect"
	"testing"
)

// AWS Lambda writes the principal element the principal's own kind calls for.
func TestLambdaPermissionPrincipalElements(t *testing.T) {
	for principal, want := range map[string]any{
		"s3.amazonaws.com":                       map[string]any{"Service": "s3.amazonaws.com"},
		"123456789012":                           map[string]any{"AWS": "arn:aws:iam::123456789012:root"},
		"arn:aws:iam::123456789012:role/invoker": map[string]any{"AWS": "arn:aws:iam::123456789012:role/invoker"},
		"*":                                      "*",
	} {
		if got := lambdaPermissionPrincipal(principal); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", principal, got, want)
		}
	}
}
