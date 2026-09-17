package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("sts", iamPopulateSTSRequestConditionKeys)
}

func iamPopulateSTSRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	set := func(key, member string) {
		if value := iamRequestParameter(r, body, member); value != "" {
			ctx[key] = []string{value}
		}
	}
	list := func(key, member string) {
		if values := snsListMembers(r, member); len(values) > 0 {
			ctx[key] = values
		}
	}
	switch operation {
	case "AssumeRole":
		set("sts:RoleSessionName", "RoleSessionName")
		set("sts:ExternalId", "ExternalId")
		set("sts:SourceIdentity", "SourceIdentity")
		list("sts:TransitiveTagKeys", "TransitiveTagKeys")
	case "AssumeRoleWithWebIdentity":
		set("sts:RoleSessionName", "RoleSessionName")
	case "GetWebIdentityToken":
		set("sts:DurationSeconds", "DurationSeconds")
		set("sts:SigningAlgorithm", "SigningAlgorithm")
		list("sts:IdentityTokenAudience", "Audience")
	case "AssumeRoot":
		set("sts:TaskPolicyArn", "TaskPolicyArn.arn")
	}
}
