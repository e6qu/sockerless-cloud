package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("ec2", iamPopulateEC2RequestConditionKeys)
}

// ec2InstanceMetadataDefaultAttributes are the ModifyInstanceMetadataDefaults
// members. Amazon EC2 exposes each one it receives as ec2:Attribute/<member>,
// which is how a policy holds the account's default to IMDSv2.
var ec2InstanceMetadataDefaultAttributes = []string{
	"HttpTokens",
	"HttpPutResponseHopLimit",
	"HttpEndpoint",
	"InstanceMetadataTags",
	"HttpTokensEnforced",
}

func iamPopulateEC2RequestConditionKeys(r *http.Request, operation string, _ []byte, ctx map[string][]string) {
	// ec2:CreateAction is the operation that created the resource being tagged,
	// which Amazon EC2 declares on CreateTags alone: a request that carries tag
	// specifications is authorized for CreateTags as well as for its own create,
	// and this key is what limits that tagging grant to the create it belongs
	// to. A CreateTags call on an existing resource creates nothing, so it
	// leaves the key unset and a grant conditioned on it does not cover it.
	iamSetConditionValues(ctx, "ec2:CreateAction",
		iamTagOnCreateOperation(r, operation, "CreateTags", len(ec2ParseTagSpecs(r)) > 0))

	if operation != "ModifyInstanceMetadataDefaults" {
		return
	}
	for _, attribute := range ec2InstanceMetadataDefaultAttributes {
		iamSetConditionValues(ctx, "ec2:Attribute/"+attribute, r.FormValue(attribute))
	}
}
