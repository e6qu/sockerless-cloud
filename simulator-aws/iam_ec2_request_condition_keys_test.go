package main

import (
	"net/url"
	"testing"
)

func TestEC2ConditionKeysReadTheMetadataDefaults(t *testing.T) {
	r := queryConditionRequest(url.Values{
		"Action":                  {"ModifyInstanceMetadataDefaults"},
		"Version":                 {"2016-11-15"},
		"HttpTokens":              {"required"},
		"HttpPutResponseHopLimit": {"2"},
		"HttpEndpoint":            {"enabled"},
		"InstanceMetadataTags":    {"disabled"},
		"HttpTokensEnforced":      {"enabled"},
	})
	assertConditionValues(t, requestConditionContext(r, "ec2", "ModifyInstanceMetadataDefaults", ""), map[string][]string{
		"ec2:Attribute/HttpTokens":              {"required"},
		"ec2:Attribute/HttpPutResponseHopLimit": {"2"},
		"ec2:Attribute/HttpEndpoint":            {"enabled"},
		"ec2:Attribute/InstanceMetadataTags":    {"disabled"},
		"ec2:Attribute/HttpTokensEnforced":      {"enabled"},
	})
}

func TestEC2ConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	r := queryConditionRequest(url.Values{"Action": {"ModifyInstanceMetadataDefaults"}, "HttpTokens": {"required"}})
	ctx := requestConditionContext(r, "ec2", "ModifyInstanceMetadataDefaults", "")
	assertConditionValues(t, ctx, map[string][]string{"ec2:Attribute/HttpTokens": {"required"}})
	assertConditionKeysAbsent(t, ctx, "ec2:Attribute/HttpEndpoint", "ec2:Attribute/HttpPutResponseHopLimit")

	r = queryConditionRequest(url.Values{"Action": {"ModifyInstanceMetadataOptions"}, "HttpTokens": {"required"}})
	assertConditionKeysAbsent(t, requestConditionContext(r, "ec2", "ModifyInstanceMetadataOptions", ""),
		"ec2:Attribute/HttpTokens")
}
