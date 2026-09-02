package main

import (
	"reflect"
	"testing"
)

// An ARN the request carries names its resource outright, wherever the
// service's own shape nests it — and only when it is an ARN of a type the
// action declares.
func TestIAMResourceARNs_ANestedARNNamesADeclaredType(t *testing.T) {
	const region, account = "us-east-1", "123456789012"
	const webACL = "arn:aws:wafv2:us-east-1:123456789012:regional/webacl/prod/0123"

	t.Run("an ARN nested in a structure is found", func(t *testing.T) {
		r := iamJSONRequest("AWSWAF_20190729.PutLoggingConfiguration",
			`{"LoggingConfiguration":{"ResourceArn":"`+webACL+`","LogDestinationConfigs":[]}}`)
		got := iamARNsNamingADeclaredType(r, "wafv2", []string{"webacl"}, region, account)
		if !reflect.DeepEqual(got, []string{webACL}) {
			t.Errorf("derived %v, want [%s]", got, webACL)
		}
	})

	t.Run("an ARN of a type the action does not declare is not the resource", func(t *testing.T) {
		// A logging configuration names where the logs go. Authorizing the
		// call against the log destination would grant past what was asked.
		r := iamJSONRequest("AWSWAF_20190729.PutLoggingConfiguration",
			`{"LoggingConfiguration":{"ResourceArn":"`+webACL+`",`+
				`"LogDestinationConfigs":["arn:aws:firehose:us-east-1:123456789012:deliverystream/waf"]}}`)
		got := iamARNsNamingADeclaredType(r, "wafv2", []string{"webacl"}, region, account)
		if !reflect.DeepEqual(got, []string{webACL}) {
			t.Errorf("derived %v, want only the web ACL", got)
		}
	})

	t.Run("a key beside a resource is never the resource", func(t *testing.T) {
		r := iamJSONRequest("AWSWAF_20190729.PutLoggingConfiguration",
			`{"KmsKeyId":"arn:aws:kms:us-east-1:123456789012:key/0123abcd"}`)
		if got := iamARNsNamingADeclaredType(r, "wafv2", []string{"webacl"}, region, account); got != nil {
			t.Errorf("derived %v from a request naming only a key, want nothing", got)
		}
	})

	t.Run("an action declaring no type derives nothing", func(t *testing.T) {
		r := iamJSONRequest("AWSWAF_20190729.PutLoggingConfiguration",
			`{"LoggingConfiguration":{"ResourceArn":"`+webACL+`"}}`)
		if got := iamARNsNamingADeclaredType(r, "wafv2", nil, region, account); got != nil {
			t.Errorf("derived %v with no declared type, want nothing", got)
		}
	})
}

// The acceptance test is the published format, so an ARN that merely starts
// the same way is not one.
func TestIAMARNFormatMatcher_ReadsThePublishedShape(t *testing.T) {
	matcher := iamARNFormatMatcher(
		"arn:${Partition}:wafv2:${Region}:${Account}:${Scope}/webacl/${Name}/${Id}")
	if matcher == nil {
		t.Fatal("no matcher built for a published format")
	}
	for _, arn := range []string{
		"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/prod/0123",
		"arn:aws-cn:wafv2:cn-north-1:123456789012:global/webacl/prod/0123",
	} {
		if !matcher.MatchString(arn) {
			t.Errorf("%s did not match its own format", arn)
		}
	}
	for _, arn := range []string{
		// A rule group is not a web ACL.
		"arn:aws:wafv2:us-east-1:123456789012:regional/rulegroup/prod/0123",
		// Another service entirely.
		"arn:aws:kms:us-east-1:123456789012:key/0123abcd",
		// Short of the format's own segments.
		"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/prod",
		// Past them.
		"arn:aws:wafv2:us-east-1:123456789012:regional/webacl/prod/0123/extra",
	} {
		if matcher.MatchString(arn) {
			t.Errorf("%s matched a format it is not", arn)
		}
	}
	// An identifier with no format is not a matcher.
	if iamARNFormatMatcher("not-an-arn/${Name}") != nil {
		t.Error("built a matcher from something that is not an ARN format")
	}
}
