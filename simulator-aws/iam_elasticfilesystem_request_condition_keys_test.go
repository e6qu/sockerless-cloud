package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// efsConditionRequest is the restJson1 request the Amazon EFS client sends,
// carrying the operation its route names the way the REST gate records it.
func efsConditionRequest(method, path, operation, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return iamWithRESTOperation(r, operation, nil)
}

func TestEFSConditionKeysReadEncrypted(t *testing.T) {
	for body, want := range map[string]string{
		`{"CreationToken":"a","Encrypted":true}`:  "true",
		`{"CreationToken":"a","Encrypted":false}`: "false",
	} {
		r := efsConditionRequest(http.MethodPost, "/2015-02-01/file-systems", "CreateFileSystem", body)
		assertPopulatedConditionValues(t, populatedConditionContext(r, "elasticfilesystem", "CreateFileSystem", body),
			map[string][]string{"elasticfilesystem:Encrypted": {want}})
	}
	body := `{"CreationToken":"a"}`
	r := efsConditionRequest(http.MethodPost, "/2015-02-01/file-systems", "CreateFileSystem", body)
	assertConditionKeysAbsent(t, populatedConditionContext(r, "elasticfilesystem", "CreateFileSystem", body),
		"elasticfilesystem:Encrypted")
}

// A create that carries tags is also authorized as elasticfilesystem:TagResource,
// with elasticfilesystem:CreateAction naming the create; a TagResource call on
// an existing file system names none.
func TestEFSConditionKeysNameTheCreateBehindTagging(t *testing.T) {
	for operation, path := range map[string]string{
		"CreateFileSystem":  "/2015-02-01/file-systems",
		"CreateAccessPoint": "/2015-02-01/access-points",
	} {
		body := `{"FileSystemId":"fs-1","Tags":[{"Key":"team","Value":"a"}]}`
		r := efsConditionRequest(http.MethodPost, path, operation, body)
		assertPopulatedConditionValues(t, populatedConditionContext(r, "elasticfilesystem", "TagResource", body),
			map[string][]string{"elasticfilesystem:CreateAction": {operation}})
		assertConditionKeysAbsent(t, populatedConditionContext(r, "elasticfilesystem", operation, body),
			"elasticfilesystem:CreateAction")
	}

	body := `{"CreationToken":"a"}`
	r := efsConditionRequest(http.MethodPost, "/2015-02-01/file-systems", "CreateFileSystem", body)
	assertConditionKeysAbsent(t, populatedConditionContext(r, "elasticfilesystem", "TagResource", body),
		"elasticfilesystem:CreateAction")

	body = `{"Tags":[{"Key":"team","Value":"a"}]}`
	r = efsConditionRequest(http.MethodPost, "/2015-02-01/resource-tags/fs-1", "TagResource", body)
	assertConditionKeysAbsent(t, populatedConditionContext(r, "elasticfilesystem", "TagResource", body),
		"elasticfilesystem:CreateAction")
}
