package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
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
	// A secret's name may be a path; its ARN still ends at the name.
	secret := iamARNFormatMatcher(iamResourceARNFormats["secretsmanager:Secret"])
	if !secret.MatchString("arn:aws:secretsmanager:eu-west-1:123456789012:secret:edd/workspace/ws-1-AbCdEf") {
		t.Error("a secret named by a path did not match the Secret format")
	}
	if secret.MatchString("arn:aws:secretsmanager:eu-west-1:123456789012:secret:a:b") {
		t.Error("a secret identifier spanning a colon matched the Secret format")
	}
	// An identifier with no format is not a matcher.
	if iamARNFormatMatcher("not-an-arn/${Name}") != nil {
		t.Error("built a matcher from something that is not an ARN format")
	}
}

// A tagged CreateSecret is also authorized as secretsmanager:TagResource, on
// the secret it creates: a grant scoped to a name prefix allows both. The
// tagging check fell to "*" for any secret named by a path, so such a grant
// allowed the untagged create and refused the tagged one.
func TestIAMTaggedCreateSecretAuthorizesTaggingOnTheSecret(t *testing.T) {
	body := `{"Name":"edd/workspace/ws-1","SecretString":"x","Tags":[{"Key":"edd:workspace","Value":"ws-1"}]}`
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("X-Amz-Target", "secretsmanager.CreateSecret")
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20261007/eu-west-1/secretsmanager/aws4_request, SignedHeaders=host, Signature=x")
	resources := iamResourceARNsForRequest(r, "secretsmanager:CreateSecret")
	var tagging []string
	for _, target := range iamOperationTargets(r, "secretsmanager", "CreateSecret", resources) {
		if target.action == "secretsmanager:TagResource" {
			tagging = append(tagging, target.resource)
		}
	}
	want := []string{"arn:aws:secretsmanager:eu-west-1:123456789012:secret:edd/workspace/ws-1"}
	if !slices.Equal(tagging, want) {
		t.Fatalf("TagResource authorized on %v, want %v", tagging, want)
	}
}
