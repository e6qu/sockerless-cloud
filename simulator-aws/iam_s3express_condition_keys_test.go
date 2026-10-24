package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestS3ExpressConditionKeysReadTheSessionMode(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"ReadOnly", "ReadOnly"},
		{"ReadWrite", "ReadWrite"},
		{"", "ReadWrite"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/?session", nil)
		if tc.header != "" {
			r.Header.Set("x-amz-create-session-mode", tc.header)
		}
		assertPopulatedConditionValues(t, populatedConditionContext(r, "s3express", "CreateSession", ""),
			map[string][]string{"s3express:SessionMode": {tc.want}})
	}
}

func TestS3ExpressConditionKeysReadTheSessionEncryption(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/?session", nil)
	r.Header.Set("x-amz-server-side-encryption", "aws:kms")
	r.Header.Set("x-amz-server-side-encryption-aws-kms-key-id", "arn:aws:kms:us-east-1:123456789012:key/k")
	assertPopulatedConditionValues(t, populatedConditionContext(r, "s3express", "CreateSession", ""), map[string][]string{
		"s3express:x-amz-server-side-encryption":                {"aws:kms"},
		"s3express:x-amz-server-side-encryption-aws-kms-key-id": {"arn:aws:kms:us-east-1:123456789012:key/k"},
	})
	unencrypted := httptest.NewRequest(http.MethodGet, "/?session", nil)
	assertConditionKeysAbsent(t, populatedConditionContext(unencrypted, "s3express", "CreateSession", ""),
		"s3express:x-amz-server-side-encryption", "s3express:x-amz-server-side-encryption-aws-kms-key-id")
}

func TestS3ExpressConditionKeysReadTheBucketLocationAndInventoryFields(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/logs--use1-az4--x-s3", nil)
	assertPopulatedConditionValues(t, populatedConditionContext(r, "s3express", "CreateBucket",
		`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Location><Type>AvailabilityZone</Type>`+
			`<Name>use1-az4</Name></Location><Bucket><Type>Directory</Type></Bucket></CreateBucketConfiguration>`),
		map[string][]string{"s3express:LocationName": {"use1-az4"}})
	assertPopulatedConditionValues(t, populatedConditionContext(r, "s3express", "PutInventoryConfiguration",
		`<InventoryConfiguration><Id>a</Id><OptionalFields><Field>Size</Field><Field>ETag</Field></OptionalFields></InventoryConfiguration>`),
		map[string][]string{"s3express:InventoryAccessibleOptionalFields": {"Size", "ETag"}})
}
