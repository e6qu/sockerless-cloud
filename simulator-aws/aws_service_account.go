package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// awsServiceAccountID is the account the AWS services keep their own buckets
// in: AWS Lambda's deployment packages, AWS Amplify's artifacts, AWS Glue's
// system logs, Elastic Load Balancing's trust store contents. AWS does not
// publish these accounts; the caller's account must merely differ from it.
const awsServiceAccountID = "000000000000"

// awsServiceCredential is the credential those services sign their presigned
// URLs with. Its secret exists only in this process, so no caller can sign a
// request into a service-owned bucket, and a URL signed before a restart stops
// verifying the way an expired service session's URL does.
var awsServiceCredential = func() struct{ accessKeyID, secretAccessKey string } {
	id := make([]byte, 8)
	secret := make([]byte, 30)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	if _, err := rand.Read(secret); err != nil {
		panic(err)
	}
	return struct{ accessKeyID, secretAccessKey string }{
		accessKeyID:     "AKIA" + strings.ToUpper(hex.EncodeToString(id)),
		secretAccessKey: hex.EncodeToString(secret),
	}
}()
