package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("ssm", iamPopulateSSMRequestConditionKeys)
}

func iamPopulateSSMRequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	request := iamParseBodyMembers(body)
	if request == nil {
		return
	}
	set := request.setters(ctx)
	switch operation {
	case "CreateDocument":
		set.str("ssm:DocumentType", "DocumentType")
	case "StartChangeRequestExecution":
		set.boolean("ssm:AutoApprove", "AutoApprove")
		set.str("ssm:DocumentVersion", "DocumentVersion")
	case "StartAutomationExecution":
		set.str("ssm:DocumentVersion", "DocumentVersion")
	case "PutInventory":
		var types []string
		for _, item := range request.objects("Items") {
			if name, ok := item.str("TypeName"); ok {
				types = append(types, name)
			}
		}
		if len(types) > 0 {
			ctx["ssm:InventoryTypeName"] = types
		}
	case "PutParameter":
		set.boolean("ssm:Overwrite", "Overwrite")
		set.str("ssm:Policies", "Policies")
	case "GetParametersByPath":
		set.boolean("ssm:Recursive", "Recursive")
	case "CreateResourceDataSync", "DeleteResourceDataSync", "ListResourceDataSync", "UpdateResourceDataSync":
		set.str("ssm:SyncType", "SyncType")
	}
}
