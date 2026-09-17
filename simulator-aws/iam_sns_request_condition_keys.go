package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("sns", iamPopulateSNSRequestConditionKeys)
}

func iamPopulateSNSRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	if operation != "Subscribe" {
		return
	}
	if protocol := iamRequestParameter(r, body, "Protocol"); protocol != "" {
		ctx["sns:Protocol"] = []string{protocol}
	}
	if endpoint := iamRequestParameter(r, body, "Endpoint"); endpoint != "" {
		ctx["sns:Endpoint"] = []string{endpoint}
	}
}
