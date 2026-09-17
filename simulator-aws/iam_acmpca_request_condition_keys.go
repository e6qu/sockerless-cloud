package main

import (
	"encoding/json"
	"net/http"
)

func init() {
	registerIAMRequestConditionPopulator("acm-pca", iamPopulateACMPCARequestConditionKeys)
}

// iamPopulateACMPCARequestConditionKeys adds the one key AWS Private CA
// declares on a request: the certificate template an IssueCertificate call
// asks the authority to sign under, which is how a policy allows an issuer to
// mint end-entity certificates and not a subordinate CA. A request that names
// no template settles nothing — AWS applies its default template, and a policy
// that requires a named one is meant to refuse.
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
