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
	if operation != "ModifyInstanceMetadataDefaults" {
		return
	}
	for _, attribute := range ec2InstanceMetadataDefaultAttributes {
		iamSetConditionValues(ctx, "ec2:Attribute/"+attribute, r.FormValue(attribute))
	}
}
