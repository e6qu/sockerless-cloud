package main

import (
	"encoding/xml"
	"fmt"
)

// The canonical user ElastiCache acts as on a bucket whose ACL grants it
// access, from the Amazon S3 User Guide's ACL migration prerequisites.
const ecS3CanonicalUser = "540804c33a284a299d2547575ce1010f2312ef3da9b3a053c8bc45bf233e4353"

// ecSnapshotService is the service principal a bucket policy grants
// ElastiCache's snapshot export and seeding to.
func ecSnapshotService() iamServiceSource {
	return iamServiceSource{Service: awsRegion() + ".elasticache-snapshot.amazonaws.com", SourceAccount: awsAccountID()}
}

// ecS3Need is one thing ElastiCache does on a bucket: the bucket ACL
// permission that grants it and the policy action on the resource that does.
type ecS3Need struct {
	Permission string
	Action     string
	Object     string
}

var (
	ecS3ExportNeeds = []ecS3Need{
		{Permission: "READ", Action: "s3:ListBucket"},
		{Permission: "WRITE", Action: "s3:PutObject", Object: "*"},
		{Permission: "READ_ACP", Action: "s3:GetBucketAcl"},
	}
	ecS3SeedNeeds = []ecS3Need{
		{Permission: "READ", Action: "s3:ListBucket"},
		{Permission: "READ_ACP", Action: "s3:GetBucketAcl"},
	}
)

// ecS3Refusal is the message ElastiCache answers when the bucket grants it
// neither through its policy nor through its ACL one of needs, or "".
func ecS3Refusal(bucket string, needs []ecS3Need) string {
	docs := iamResourcePolicyDocsForARN(s3BucketARN(bucket))
	acl, _, _, _ := getStoredBucketSubresource(bucket, "acl")
	aclsApply := !s3BucketOwnerEnforced(bucket)
	for _, need := range needs {
		resource := s3BucketARN(bucket)
		if need.Object != "" {
			resource += "/" + need.Object
		}
		if len(docs) > 0 && iamEvalServiceInitiated(docs, need.Action, resource, ecSnapshotService()) {
			continue
		}
		if aclsApply && ecACLGrantsCanonicalUser(acl, need.Permission) {
			continue
		}
		return fmt.Sprintf("ElastiCache has not been granted %s permissions %s on the S3 Bucket.", need.Permission, bucket)
	}
	return ""
}

// ecS3ObjectRefusal is why ElastiCache may not read an object to seed a
// cache from it, or "".
func ecS3ObjectRefusal(bucket, key string, objectACL []byte) string {
	if message := ecS3Refusal(bucket, ecS3SeedNeeds); message != "" {
		return message
	}
	docs := iamResourcePolicyDocsForARN(s3BucketARN(bucket))
	if len(docs) > 0 && iamEvalServiceInitiated(docs, "s3:GetObject", s3BucketARN(bucket)+"/"+key, ecSnapshotService()) {
		return ""
	}
	if !s3BucketOwnerEnforced(bucket) && ecACLGrantsCanonicalUser(objectACL, "READ") {
		return ""
	}
	return fmt.Sprintf("ElastiCache has not been granted READ permissions %s/%s on the S3 Bucket.", bucket, key)
}

func ecACLGrantsCanonicalUser(acl []byte, permission string) bool {
	if len(acl) == 0 {
		return false
	}
	var policy struct {
		Grants []struct {
			Grantee struct {
				ID string `xml:"ID"`
			} `xml:"Grantee"`
			Permission string `xml:"Permission"`
		} `xml:"AccessControlList>Grant"`
	}
	if xml.Unmarshal(acl, &policy) != nil {
		return false
	}
	for _, grant := range policy.Grants {
		if grant.Grantee.ID == ecS3CanonicalUser && (grant.Permission == permission || grant.Permission == "FULL_CONTROL") {
			return true
		}
	}
	return false
}
