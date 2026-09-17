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
	assertPopulatedConditionValues(t, populatedConditionContext(r, "ec2", "ModifyInstanceMetadataDefaults", ""), map[string][]string{
		"ec2:Attribute/HttpTokens":              {"required"},
		"ec2:Attribute/HttpPutResponseHopLimit": {"2"},
		"ec2:Attribute/HttpEndpoint":            {"enabled"},
		"ec2:Attribute/InstanceMetadataTags":    {"disabled"},
		"ec2:Attribute/HttpTokensEnforced":      {"enabled"},
	})
}

func TestEC2ConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	r := queryConditionRequest(url.Values{"Action": {"ModifyInstanceMetadataDefaults"}, "HttpTokens": {"required"}})
	ctx := populatedConditionContext(r, "ec2", "ModifyInstanceMetadataDefaults", "")
	assertPopulatedConditionValues(t, ctx, map[string][]string{"ec2:Attribute/HttpTokens": {"required"}})
	assertConditionKeysAbsent(t, ctx, "ec2:Attribute/HttpEndpoint", "ec2:Attribute/HttpPutResponseHopLimit")

	r = queryConditionRequest(url.Values{"Action": {"ModifyInstanceMetadataOptions"}, "HttpTokens": {"required"}})
	assertConditionKeysAbsent(t, populatedConditionContext(r, "ec2", "ModifyInstanceMetadataOptions", ""),
		"ec2:Attribute/HttpTokens")
}

func TestEC2CreateActionNamesTheCreateThatCarriedTheTags(t *testing.T) {
	runInstances := queryConditionRequest(url.Values{
		"Action":                          {"RunInstances"},
		"ImageId":                         {"ami-1"},
		"TagSpecification.1.ResourceType": {"instance"},
		"TagSpecification.1.Tag.1.Key":    {"Name"},
		"TagSpecification.1.Tag.1.Value":  {"web"},
	})
	assertPopulatedConditionValues(t, populatedConditionContext(runInstances, "ec2", "CreateTags", ""),
		map[string][]string{"ec2:CreateAction": {"RunInstances"}})
	// Amazon EC2 declares the key on CreateTags alone, so the create's own
	// authorization does not carry it.
	assertConditionKeysAbsent(t, populatedConditionContext(runInstances, "ec2", "RunInstances", ""),
		"ec2:CreateAction")

	// The capacity and volume family serializes its tag specifications under
	// the plural member instead.
	createVolume := queryConditionRequest(url.Values{
		"Action":                           {"CreateVolume"},
		"Size":                             {"8"},
		"TagSpecifications.1.ResourceType": {"volume"},
		"TagSpecifications.1.Tag.1.Key":    {"app"},
		"TagSpecifications.1.Tag.1.Value":  {"edd"},
	})
	assertPopulatedConditionValues(t, populatedConditionContext(createVolume, "ec2", "CreateTags", ""),
		map[string][]string{"ec2:CreateAction": {"CreateVolume"}})
}

func TestEC2CreateActionAbsentWithoutATagOnCreate(t *testing.T) {
	// Tagging a resource that already exists creates nothing, so a grant
	// conditioned on the key does not reach it.
	standalone := queryConditionRequest(url.Values{
		"Action":       {"CreateTags"},
		"ResourceId.1": {"i-1"},
		"Tag.1.Key":    {"Name"},
		"Tag.1.Value":  {"web"},
	})
	assertConditionKeysAbsent(t, populatedConditionContext(standalone, "ec2", "CreateTags", ""),
		"ec2:CreateAction")

	// A create that carries no tags is authorized for no tagging at all.
	untagged := queryConditionRequest(url.Values{"Action": {"RunInstances"}, "ImageId": {"ami-1"}})
	assertConditionKeysAbsent(t, populatedConditionContext(untagged, "ec2", "CreateTags", ""),
		"ec2:CreateAction")
}
