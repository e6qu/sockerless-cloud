package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("ecr", iamPopulateECRRequestConditionKeys)
}

// iamPopulateECRRequestConditionKeys adds ecr:AccountSetting, the registry
// setting a request reads or writes, which a policy uses to delegate one
// setting without the others.
func iamPopulateECRRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "GetAccountSetting", "PutAccountSetting":
		ecSetString(ctx, "ecr:AccountSetting", iamRequestParameter(r, body, "name"))
	}
}
