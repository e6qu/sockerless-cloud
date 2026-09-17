package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("wafv2", iamPopulateWAFv2RequestConditionKeys)
}

// iamPopulateWAFv2RequestConditionKeys adds the AWS WAF keys for a logging
// configuration: where it delivers logs, and whose configuration it is.
func iamPopulateWAFv2RequestConditionKeys(_ *http.Request, operation string, body []byte, ctx map[string][]string) {
	var request struct {
		LogScope             string `json:"LogScope"`
		LoggingConfiguration struct {
			LogDestinationConfigs []string `json:"LogDestinationConfigs"`
			LogScope              string   `json:"LogScope"`
		} `json:"LoggingConfiguration"`
	}
	switch operation {
	case "PutLoggingConfiguration", "GetLoggingConfiguration",
		"DeleteLoggingConfiguration", "ListLoggingConfigurations":
	default:
		return
	}
	if !iamDecodeJSONRequest(body, &request) {
		return
	}
	if operation == "PutLoggingConfiguration" {
		iamSetConditionValues(ctx, "wafv2:LogDestinationResource",
			request.LoggingConfiguration.LogDestinationConfigs...)
		iamSetConditionValues(ctx, "wafv2:LogScope", request.LoggingConfiguration.LogScope)
		return
	}
	iamSetConditionValues(ctx, "wafv2:LogScope", request.LogScope)
}
