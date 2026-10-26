package main

import (
	"encoding/json"
	"net/http"
)

func init() {
	registerIAMRequestConditionPopulator("acm-pca", iamPopulateACMPCARequestConditionKeys)
}

// iamPopulateACMPCARequestConditionKeys adds acm-pca:TemplateArn, the template
// IssueCertificate asks the authority to sign under, so a policy can allow
// end-entity certificates and refuse a subordinate CA. A request naming no
// template sets nothing: AWS applies its default template, and a policy that
// requires a named one refuses it.
func iamPopulateACMPCARequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	if operation != "IssueCertificate" {
		return
	}
	var request struct {
		TemplateArn string `json:"TemplateArn"`
	}
	if json.Unmarshal(body, &request) != nil {
		return
	}
	iamSetConditionValues(ctx, "acm-pca:TemplateArn", request.TemplateArn)
}
