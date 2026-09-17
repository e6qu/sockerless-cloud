package main

import "testing"

func TestECRConditionKeysReadTheAccountSettingName(t *testing.T) {
	for _, operation := range []string{"GetAccountSetting", "PutAccountSetting"} {
		ctx := jsonServiceConditionContext(t, "ecr", operation, `{"name": "BASIC_SCAN_TYPE_VERSION", "value": "AWS_NATIVE"}`)
		assertServiceConditionContext(t, ctx, map[string][]string{"ecr:AccountSetting": {"BASIC_SCAN_TYPE_VERSION"}})
	}
}

func TestECRConditionKeysAreAbsentForAbsentMembers(t *testing.T) {
	ctx := jsonServiceConditionContext(t, "ecr", "GetAccountSetting", `{}`)
	if len(ctx) != 0 {
		t.Errorf("a request naming no setting settled %v", ctx)
	}
	ctx = jsonServiceConditionContext(t, "ecr", "DescribeRepositories", `{"name": "BASIC_SCAN_TYPE_VERSION"}`)
	if len(ctx) != 0 {
		t.Errorf("DescribeRepositories settled %v", ctx)
	}
}
