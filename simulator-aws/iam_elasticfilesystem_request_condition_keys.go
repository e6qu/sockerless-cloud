package main

import "net/http"

func init() {
	registerIAMRequestConditionPopulator("elasticfilesystem", iamPopulateEFSRequestConditionKeys)
}

// iamPopulateEFSRequestConditionKeys adds the Amazon EFS keys a request settles:
// whether a CreateFileSystem asks for an encrypted file system, and the create
// behind the elasticfilesystem:TagResource authorization that a create carrying
// tags adds.
func iamPopulateEFSRequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	iamSetConditionValues(ctx, "elasticfilesystem:CreateAction",
		iamTagOnCreateOperation(r, operation, "TagResource", len(iamRequestTags(r, "elasticfilesystem")) > 0))

	if operation != "CreateFileSystem" {
		return
	}
	var request struct {
		Encrypted *bool `json:"Encrypted"`
	}
	if iamDecodeJSONRequest(body, &request) {
		iamSetConditionBool(ctx, "elasticfilesystem:Encrypted", request.Encrypted)
	}
}
