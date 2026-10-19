package main

import (
	"net/http"
	"strings"
	"time"
)

// sigv4Sign signs r with cred for service in the simulator's region, the way
// the AWS SDKs sign a request, so a service that calls another's API as a
// role presents that role's session like any other client. body is the
// request's whole payload. S3 is the one service whose path is encoded once.
func sigv4Sign(r *http.Request, body []byte, cred IAMTempCred, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	scope := credScope{accessKeyID: cred.AccessKeyID, date: amzDate[:8], region: awsRegion(), service: service}
	payloadHash := hexSHA256(body)
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if cred.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", cred.SessionToken)
	}
	if r.Host == "" {
		r.Host = r.URL.Host
	}
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if cred.SessionToken != "" {
		signed = append(signed, "x-amz-security-token")
	}
	for name := range r.Header {
		if lower := strings.ToLower(name); lower == "content-type" || strings.HasPrefix(lower, "x-amz-target") {
			signed = append(signed, lower)
		}
	}
	signed = sortedUnique(signed)
	canonical := sigv4CanonicalRequest(r, signed, sigv4CanonicalQuery(r.URL.Query(), false), payloadHash, service != "s3")
	signature := sigv4Signature(cred.SecretAccessKey, scope, amzDate, canonical)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cred.AccessKeyID+"/"+scope.date+"/"+scope.region+"/"+service+
		"/aws4_request, SignedHeaders="+strings.Join(signed, ";")+", Signature="+signature)
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := values[:0]
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
