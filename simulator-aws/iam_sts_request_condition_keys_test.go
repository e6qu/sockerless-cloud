package main

import "testing"

func TestSTSRequestConditionKeysReadTheQueryParameters(t *testing.T) {
	cases := []struct {
		name, operation string
		form            map[string]string
		want            map[string][]string
	}{
		{"assume role", "AssumeRole", map[string]string{
			"RoleArn":                    "arn:aws:iam::123456789012:role/app",
			"RoleSessionName":            "deploy",
			"ExternalId":                 "partner-42",
			"SourceIdentity":             "alice",
			"TransitiveTagKeys.member.1": "Project",
			"TransitiveTagKeys.member.2": "CostCenter",
		}, map[string][]string{
			"sts:RoleSessionName":   {"deploy"},
			"sts:ExternalId":        {"partner-42"},
			"sts:SourceIdentity":    {"alice"},
			"sts:TransitiveTagKeys": {"Project", "CostCenter"},
		}},
		{"assume role, bare", "AssumeRole", map[string]string{
			"RoleArn":         "arn:aws:iam::123456789012:role/app",
			"RoleSessionName": "deploy",
		}, map[string][]string{"sts:RoleSessionName": {"deploy"}}},
		{"web identity", "AssumeRoleWithWebIdentity", map[string]string{
			"RoleArn": "arn:aws:iam::123456789012:role/app", "RoleSessionName": "ci", "WebIdentityToken": "t",
		}, map[string][]string{"sts:RoleSessionName": {"ci"}}},
		{"web identity token", "GetWebIdentityToken", map[string]string{
			"Audience.member.1": "https://api.example.com",
			"Audience.member.2": "https://other.example.com",
			"DurationSeconds":   "600",
			"SigningAlgorithm":  "ES384",
		}, map[string][]string{
			"sts:IdentityTokenAudience": {"https://api.example.com", "https://other.example.com"},
			"sts:DurationSeconds":       {"600"},
			"sts:SigningAlgorithm":      {"ES384"},
		}},
		{"assume root", "AssumeRoot", map[string]string{
			"TargetPrincipal":   "111122223333",
			"TaskPolicyArn.arn": "arn:aws:iam::aws:policy/root-task/IAMDeleteRootUserCredentials",
		}, map[string][]string{
			"sts:TaskPolicyArn": {"arn:aws:iam::aws:policy/root-task/IAMDeleteRootUserCredentials"},
		}},
		{"undeclared action", "GetSessionToken", map[string]string{"DurationSeconds": "900", "ExternalId": "x"},
			map[string][]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := map[string]string{"Action": c.operation, "Version": "2011-06-15"}
			for k, v := range c.form {
				form[k] = v
			}
			assertConditionValues(t, requestConditionContext("sts", c.operation, formRequest(form), ""), c.want)
		})
	}
}
