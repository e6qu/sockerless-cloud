package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// AWS services keep their own buckets in an account AWS does not publish; the
// caller's account need only differ from it.
const awsServiceAccountID = "000000000000"

// Its secret lives only in this process, so no caller can sign into a
// service-owned bucket, and a URL signed before a restart stops verifying, as
// an expired service session's would.
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
