package main

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Amazon S3's request-header condition keys.
//
// These are the canonical way an S3 policy constrains how an object is written
// rather than which object it is: refuse a public-read ACL, require server-side
// encryption with a particular key, allow only one storage class, hold a listing
// to one prefix. Each names a header or query parameter of the request, and its
// value is that header, verbatim — nothing is looked up and nothing is derived.
//
// The key is spelled exactly as the header it reads, which is why the table
// below is a list of header names rather than pairs.
var s3RequestHeaderConditionKeys = []string{
	"x-amz-acl",
	"x-amz-grant-full-control",
	"x-amz-grant-read",
	"x-amz-grant-read-acp",
	"x-amz-grant-write",
	"x-amz-grant-write-acp",
	"x-amz-server-side-encryption",
	"x-amz-server-side-encryption-aws-kms-key-id",
	"x-amz-server-side-encryption-customer-algorithm",
	"x-amz-storage-class",
	"x-amz-copy-source",
	"x-amz-metadata-directive",
	"x-amz-website-redirect-location",
}

// s3RequestQueryConditionKeys are the listing parameters a policy narrows a
// browse with: prefix holds a caller to one folder, delimiter and max-keys to
// one shape of answer.
var s3RequestQueryConditionKeys = []string{"prefix", "delimiter", "max-keys"}

// iamPopulateS3RequestConditionKeys adds the keys the request's own headers and
// parameters settle.
func iamPopulateS3RequestConditionKeys(r *http.Request, ctx map[string][]string) {
	for _, header := range s3RequestHeaderConditionKeys {
		if value := r.Header.Get(header); value != "" {
			ctx["s3:"+header] = []string{value}
		}
	}
	query := r.URL.Query()
	for _, parameter := range s3RequestQueryConditionKeys {
		if value := query.Get(parameter); value != "" {
			ctx["s3:"+parameter] = []string{value}
		}
	}
	// s3:if-match is the ETag a conditional write requires the object to have.
	if match := r.Header.Get("If-Match"); match != "" {
		ctx["s3:if-match"] = []string{match}
	}
	// The tags a request puts on the object it writes travel in x-amz-tagging
	// as a query string, which is the form the header carries.
	if tagging := r.Header.Get("x-amz-tagging"); tagging != "" {
		if tags, err := url.ParseQuery(tagging); err == nil && len(tags) > 0 {
			names := make([]string, 0, len(tags))
			for name, values := range tags {
				if len(values) > 0 {
					ctx["s3:RequestObjectTag/"+name] = []string{values[0]}
				}
				names = append(names, name)
			}
			sort.Strings(names)
			ctx["s3:RequestObjectTagKeys"] = names
		}
	}
}

// iamPopulateS3LocationConstraint adds s3:locationconstraint, the region a
// CreateBucket asks for. It is read from the request body, which is where the
// constraint travels, and only for a request that carries one.
func iamPopulateS3LocationConstraint(body []byte, ctx map[string][]string) {
	if len(body) == 0 || !strings.Contains(string(body), "LocationConstraint") {
		return
	}
	var configuration struct {
		LocationConstraint string `xml:"LocationConstraint"`
	}
	if xml.Unmarshal(body, &configuration) != nil || configuration.LocationConstraint == "" {
		return
	}
	ctx["s3:locationconstraint"] = []string{configuration.LocationConstraint}
}
