package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// A directory bucket's operations are authorized as the Amazon S3 User Guide's
// table for directory buckets lists them, in the s3express namespace.
func TestS3ExpressOperationsAreAuthorizedAsTheTableLists(t *testing.T) {
	const bucket = "logs--use1-az4--x-s3"
	expressARN := "arn:aws:s3express:us-east-1:" + awsAccountID() + ":bucket/" + bucket
	request := func(method, target string, session bool) *http.Request {
		r := httptest.NewRequest(method, target, nil)
		r.SetPathValue("bucket", bucket)
		if session {
			r.Header.Set(s3ExpressSessionTokenHeader, "token")
		}
		return r
	}
	for name, tc := range map[string]struct {
		r         *http.Request
		operation string
		want      []string
	}{
		"a create is s3express:CreateBucket": {
			request(http.MethodPut, "/"+bucket, false), "CreateBucket",
			[]string{"s3express:CreateBucket " + expressARN}},
		"an inventory delete is s3express:PutInventoryConfiguration": {
			request(http.MethodDelete, "/"+bucket+"?inventory&id=a", false), "DeleteBucketInventoryConfiguration",
			[]string{"s3express:PutInventoryConfiguration " + expressARN}},
		"a session is s3express:CreateSession": {
			request(http.MethodGet, "/"+bucket+"?session", false), "CreateSession",
			[]string{"s3express:CreateSession " + expressARN}},
		"an object read signed without a session is s3express:CreateSession": {
			request(http.MethodGet, "/"+bucket+"/a", false), "GetObject",
			[]string{"s3express:CreateSession " + expressARN}},
		"an object read in a session is authorized by the session alone": {
			request(http.MethodGet, "/"+bucket+"/a", true), "GetObject", []string{}},
		"ListDirectoryBuckets is s3express:ListAllMyDirectoryBuckets": {
			httptest.NewRequest(http.MethodGet, "/?x-id=ListDirectoryBuckets", nil), "ListBuckets",
			[]string{"s3express:ListAllMyDirectoryBuckets *"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := targetStrings(s3AuthorizationTargets(tc.r, tc.operation, []string{s3RequestResourceARN(tc.r)}))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
		})
	}

	general := httptest.NewRequest(http.MethodPut, "/logs", nil)
	general.SetPathValue("bucket", "logs")
	if got := targetStrings(s3AuthorizationTargets(general, "CreateBucket", []string{"arn:aws:s3:::logs"})); !reflect.DeepEqual(got, []string{"s3:CreateBucket arn:aws:s3:::logs"}) {
		t.Errorf("a general purpose bucket's create = %v, want s3:CreateBucket", got)
	}
}
