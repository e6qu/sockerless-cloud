package main

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

// A request the simulator signs as a role session is one its own verifier
// accepts, on a JSON control-plane call and on a REST path whose segments
// carry an ARN; a request altered after signing is refused.
func TestSigV4SignIsAcceptedByTheVerifier(t *testing.T) {
	resetCredentialStores()
	now := time.Now().UTC()
	expiration := now.Add(time.Hour)
	akid, secret, token := stsMintTempCred(expiration)
	cred := IAMTempCred{AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token, RoleName: "r",
		Expiration: expiration.Format(time.RFC3339)}
	iamTempCreds.Put(akid, cred)

	for _, tc := range []struct {
		service, method, target string
		body                    []byte
		header                  map[string]string
	}{
		{"sqs", http.MethodPost, "http://localhost/", []byte(`{"QueueUrl":"q"}`), map[string]string{
			"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": "AmazonSQS.SendMessage"}},
		{"lambda", http.MethodPost, "http://localhost/2015-03-31/functions/arn:aws:lambda:us-east-1:123456789012:function:f/invocations",
			[]byte(`{}`), map[string]string{"X-Amz-Invocation-Type": "Event"}},
	} {
		req, err := http.NewRequest(tc.method, tc.target, bytes.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		sigv4Sign(req, tc.body, cred, tc.service, now)
		if result, serr := sigv4Verify(req, tc.body); serr != nil || result != sigv4Verified {
			t.Fatalf("%s: the verifier refused the simulator's own signature: %v", tc.service, serr)
		}
		if _, serr := sigv4Verify(req, append(tc.body, ' ')); serr == nil {
			t.Fatalf("%s: a body altered after signing was accepted", tc.service)
		}
	}
}
