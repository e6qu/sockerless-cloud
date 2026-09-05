package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SigV4 request-authentication gate.
//
// Before this gate, the simulator read the caller's access-key id straight out
// of the cleartext `Authorization: AWS4-HMAC-SHA256 Credential=<AKID>/…` field
// and never checked the `Signature=` component. Any client could therefore
// present any registered principal's access-key id and be treated as that
// principal — impersonation with no proof of possession of the secret.
//
// sigv4Verify closes that hole by recomputing the SigV4 signature the way the
// real AWS front end does: it rebuilds the canonical request from the actual
// request (method, canonical URI, canonical query, the signed headers, and the
// payload hash the client declared), derives the SigV4 signing key from the
// principal's stored secret access key, and constant-time-compares the result
// with the presented signature. A tampered signature, an unknown or inactive
// key, an expired temporary credential, or a session-token mismatch is
// rejected with the same error the real service returns.
//
// The gate runs at the two authorization chokepoints that resolve a request to
// a registered principal: the control-plane dispatcher (POST /) and the S3 REST
// handlers. Both are the same places call-time IAM authorization already runs;
// authentication (this gate) runs first, then authorization.

// sigv4Result reports whether a request carried a credential at all. Whether an
// absent credential is fatal depends on the surface (the control plane requires
// one; S3 permits anonymous access to public objects), so that decision is left
// to the caller.
type sigv4Result int

const (
	sigv4NoCredential sigv4Result = iota // request presented no SigV4 credential
	sigv4Verified                        // credential present and signature valid
)

// sigv4ErrKind classifies an authentication failure so each calling surface can
// render it in its own wire shape (awsJson / EC2 query XML / IAM-STS query XML /
// S3 XML) with the matching error code.
type sigv4ErrKind int

const (
	sigErrSignatureMismatch  sigv4ErrKind = iota // signature did not verify
	sigErrInvalidClientToken                     // unknown or inactive credential
	sigErrExpiredToken                           // temporary credential past expiry
)

type sigv4Error struct {
	kind    sigv4ErrKind
	message string
}

const (
	sigMsgMismatch   = "The request signature we calculated does not match the signature you provided. Check your AWS Secret Access Key and signing method."
	sigMsgInvalidTok = "The security token included in the request is invalid."
	sigMsgExpiredTok = "The security token included in the request is expired."
)

// credScope is the parsed `AKID/date/region/service/aws4_request` credential
// scope from an Authorization header or an X-Amz-Credential query parameter.
type credScope struct {
	accessKeyID string
	date        string // yyyymmdd
	region      string
	service     string
}

func parseCredScope(s string) (credScope, bool) {
	parts := strings.Split(s, "/")
	if len(parts) < 5 || parts[0] == "" || parts[4] != "aws4_request" {
		return credScope{}, false
	}
	return credScope{accessKeyID: parts[0], date: parts[1], region: parts[2], service: parts[3]}, true
}

// sigv4Verify authenticates a request. body is the already-buffered request body
// used only to compute the payload hash when the request omits the
// x-amz-content-sha256 header; callers that must not consume a streaming body
// (S3 uploads) pass nil and rely on the header, which S3 clients always send.
//
// aws-sdk-go-v2's v4 signer double-URI-encodes the canonical path for every
// service except S3 (S3's client middleware sets DisableURIPathEscaping, the
// one exemption the SigV4 spec itself carves out); this is sigv4Verify's
// default (doubleEncodePath=true). Call sigv4VerifyS3 for the S3 REST gate,
// the sole single-encode surface.
func sigv4Verify(r *http.Request, body []byte) (sigv4Result, *sigv4Error) {
	return sigv4VerifyEncoding(r, body, true)
}

// sigv4VerifyS3 is sigv4Verify for the S3 REST data plane, whose client
// signer does not double-encode the canonical URI.
func sigv4VerifyS3(r *http.Request, body []byte) (sigv4Result, *sigv4Error) {
	return sigv4VerifyEncoding(r, body, false)
}

func sigv4VerifyEncoding(r *http.Request, body []byte, doubleEncodePath bool) (sigv4Result, *sigv4Error) {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != "" || q.Get("X-Amz-Signature") != "" {
		return sigv4VerifyPresigned(r, q, doubleEncodePath)
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256") {
		return sigv4NoCredential, nil
	}
	return sigv4VerifyHeader(r, auth, body, doubleEncodePath)
}

func sigv4VerifyHeader(r *http.Request, auth string, body []byte, doubleEncodePath bool) (sigv4Result, *sigv4Error) {
	cred, signedHeaders, providedSig, ok := parseAuthzHeader(auth)
	if !ok {
		return sigv4NoCredential, &sigv4Error{sigErrSignatureMismatch, sigMsgMismatch}
	}
	secret, serr := sigv4SecretFor(cred.accessKeyID, sigv4PresentedSessionToken(r))
	if serr != nil {
		return sigv4NoCredential, serr
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if amzDate == "" {
		amzDate = r.Header.Get("Date")
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = hexSHA256(body)
	}
	canonReq := sigv4CanonicalRequest(r, signedHeaders, sigv4CanonicalQuery(r.URL.Query(), false), payloadHash, doubleEncodePath)
	computed := sigv4Signature(secret, cred, amzDate, canonReq)
	if !hmac.Equal([]byte(computed), []byte(strings.ToLower(providedSig))) {
		return sigv4NoCredential, &sigv4Error{sigErrSignatureMismatch, sigMsgMismatch}
	}
	return sigv4Verified, nil
}

// sigv4VerifyPresigned authenticates a query-string ("presigned URL") signature,
// the form Amplify and S3 presigners emit: the algorithm, credential scope,
// signed-header list, and signature all travel as X-Amz-* query parameters and
// the payload hash is UNSIGNED-PAYLOAD.
func sigv4VerifyPresigned(r *http.Request, q url.Values, doubleEncodePath bool) (sigv4Result, *sigv4Error) {
	cred, ok := parseCredScope(q.Get("X-Amz-Credential"))
	providedSig := q.Get("X-Amz-Signature")
	amzDate := q.Get("X-Amz-Date")
	signedHeaders := splitSignedHeaders(q.Get("X-Amz-SignedHeaders"))
	if !ok || providedSig == "" || amzDate == "" || len(signedHeaders) == 0 {
		return sigv4NoCredential, &sigv4Error{sigErrSignatureMismatch, sigMsgMismatch}
	}
	secret, serr := sigv4SecretFor(cred.accessKeyID, q.Get("X-Amz-Security-Token"))
	if serr != nil {
		return sigv4NoCredential, serr
	}
	payloadHash := "UNSIGNED-PAYLOAD"
	if h := r.Header.Get("X-Amz-Content-Sha256"); h != "" {
		payloadHash = h
	}
	canonReq := sigv4CanonicalRequest(r, signedHeaders, sigv4CanonicalQuery(q, true), payloadHash, doubleEncodePath)
	computed := sigv4Signature(secret, cred, amzDate, canonReq)
	if !hmac.Equal([]byte(computed), []byte(strings.ToLower(providedSig))) {
		return sigv4NoCredential, &sigv4Error{sigErrSignatureMismatch, sigMsgMismatch}
	}
	return sigv4Verified, nil
}

// parseAuthzHeader splits an `AWS4-HMAC-SHA256 Credential=…, SignedHeaders=…,
// Signature=…` header into its components.
func parseAuthzHeader(auth string) (cred credScope, signedHeaders []string, signature string, ok bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256"))
	var credentialRaw string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "Credential="):
			credentialRaw = strings.TrimPrefix(part, "Credential=")
		case strings.HasPrefix(part, "SignedHeaders="):
			signedHeaders = splitSignedHeaders(strings.TrimPrefix(part, "SignedHeaders="))
		case strings.HasPrefix(part, "Signature="):
			signature = strings.TrimPrefix(part, "Signature=")
		}
	}
	cred, credOK := parseCredScope(credentialRaw)
	if !credOK || len(signedHeaders) == 0 || signature == "" {
		return credScope{}, nil, "", false
	}
	return cred, signedHeaders, signature, true
}

func splitSignedHeaders(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ";")
}

// sigv4SecretFor resolves an access-key id to the secret access key SigV4 must
// verify against. Long-term keys (iamAccessKeys) must be Active; temporary
// credentials (iamTempCreds) must be unexpired and, when they carry a session
// token, the request's presented token must match. An unresolved key is an
// invalid client token.
func sigv4SecretFor(accessKeyID, presentedToken string) (string, *sigv4Error) {
	if key, ok := iamAccessKeys.Get(accessKeyID); ok {
		if key.Status != "Active" {
			return "", &sigv4Error{sigErrInvalidClientToken, sigMsgInvalidTok}
		}
		return key.SecretAccessKey, nil
	}
	if tc, ok := iamTempCreds.Get(accessKeyID); ok {
		if tc.Expiration != "" {
			if exp, err := time.Parse(time.RFC3339, tc.Expiration); err == nil && time.Now().After(exp) {
				return "", &sigv4Error{sigErrExpiredToken, sigMsgExpiredTok}
			}
		}
		if tc.SessionToken != "" && presentedToken != tc.SessionToken {
			return "", &sigv4Error{sigErrInvalidClientToken, sigMsgInvalidTok}
		}
		if tc.SecretAccessKey == "" {
			return "", &sigv4Error{sigErrInvalidClientToken, sigMsgInvalidTok}
		}
		return tc.SecretAccessKey, nil
	}
	return "", &sigv4Error{sigErrInvalidClientToken, sigMsgInvalidTok}
}

func sigv4CanonicalRequest(r *http.Request, signedHeaders []string, canonicalQuery, payloadHash string, doubleEncodePath bool) string {
	canonHeaders, signedHeadersStr := sigv4CanonicalHeaders(r, signedHeaders)
	return strings.Join([]string{
		r.Method,
		sigv4CanonicalURI(r, doubleEncodePath),
		canonicalQuery,
		canonHeaders,
		signedHeadersStr,
		payloadHash,
	}, "\n")
}

// sigv4CanonicalURI builds the canonical URI path. Real AWS's SigV4 spec
// double-URI-encodes the path for every service except S3, and
// aws-sdk-go-v2's v4 signer implements exactly that (S3's client middleware
// sets DisableURIPathEscaping; every other service — including the
// control-plane dispatcher at path "/", where the distinction is moot, and
// Lambda's REST control plane, whose ARN path segments contain ':' — does
// not). doubleEncodePath=false reproduces the S3 gate's existing behavior
// unchanged: a single pass of awsURIEncode over the request's decoded path.
// doubleEncodePath=true mirrors the SDK's two passes: r.URL.EscapedPath()
// (the server-observed wire path — Go's http server reconstructs this from
// what the client actually sent) re-encoded a second time, so a literal ':'
// or other reserved character the wire path carried unescaped becomes %3A
// exactly as the client's second signer pass computed it.
func sigv4CanonicalURI(r *http.Request, doubleEncodePath bool) string {
	// A virtual-hosted request on a directory bucket's zonal endpoint was
	// rewritten onto a path the router works in; the client signed the path it
	// sent, and that is the one the signature has to be verified against.
	if signed, ok := s3SignedPath(r); ok {
		return awsURIEncode(signed, false)
	}
	if doubleEncodePath {
		path := r.URL.EscapedPath()
		if path == "" {
			return "/"
		}
		return awsURIEncode(path, false)
	}
	path := r.URL.Path
	if path == "" {
		return "/"
	}
	return awsURIEncode(path, false)
}

// sigv4CanonicalQuery builds the canonical query string: every parameter
// URI-encoded and sorted by name then value. Presigned verification excludes
// the X-Amz-Signature parameter, which is not part of what was signed.
func sigv4CanonicalQuery(q url.Values, excludeSignature bool) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if excludeSignature && k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var pairs []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		encKey := awsURIEncode(k, true)
		for _, v := range vals {
			pairs = append(pairs, encKey+"="+awsURIEncode(v, true))
		}
	}
	return strings.Join(pairs, "&")
}

// sigv4CanonicalHeaders builds the canonical headers block and the
// semicolon-joined signed-header list from exactly the headers the client
// signed. The Host header is taken from r.Host (net/http lifts it off the
// header map); Content-Length from r.ContentLength for the same reason.
func sigv4CanonicalHeaders(r *http.Request, signedHeaders []string) (canonical, signedList string) {
	lower := make([]string, 0, len(signedHeaders))
	for _, h := range signedHeaders {
		lower = append(lower, strings.ToLower(strings.TrimSpace(h)))
	}
	sort.Strings(lower)
	var b strings.Builder
	for _, h := range lower {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(sigv4HeaderValue(r, h))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(lower, ";")
}

func sigv4HeaderValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return canonicalTrim(r.Host)
	case "content-length":
		if r.ContentLength >= 0 {
			return strconv.FormatInt(r.ContentLength, 10)
		}
	}
	vals := r.Header.Values(http.CanonicalHeaderKey(name))
	trimmed := make([]string, len(vals))
	for i, v := range vals {
		trimmed[i] = canonicalTrim(v)
	}
	return strings.Join(trimmed, ",")
}

// canonicalTrim trims leading/trailing whitespace and collapses internal
// whitespace runs to a single space, as SigV4 canonicalization requires.
func canonicalTrim(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// sigv4Signature derives the SigV4 signing key from the secret and computes the
// hex signature over the string-to-sign.
func sigv4Signature(secret string, cred credScope, amzDate, canonicalRequest string) string {
	scope := cred.date + "/" + cred.region + "/" + cred.service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(cred.date))
	kRegion := hmacSHA256(kDate, []byte(cred.region))
	kService := hmacSHA256(kRegion, []byte(cred.service))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	return hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// awsURIEncode percent-encodes per RFC 3986 the way SigV4 requires: the
// unreserved set (A-Z a-z 0-9 - . _ ~) is left as-is, "/" is preserved unless
// encodeSlash is set, and every other byte becomes uppercase percent-hex.
func awsURIEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/':
			if encodeSlash {
				b.WriteString("%2F")
			} else {
				b.WriteByte('/')
			}
		default:
			const hexDigits = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}

// sigv4EnforceControlPlane authenticates a control-plane (awsJson / awsQuery)
// request before authorization. Operations AWS accepts unsigned by
// design — the web-identity and SAML assume-role calls, whose trust comes from
// the token in the body rather than a SigV4 signature — are exempt.
func sigv4EnforceControlPlane(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if action, ok := iamActionForRequest(r); ok && sigv4ExemptControlPlaneAction(action) {
		return true
	}
	res, serr := sigv4Verify(r, body)
	if serr != nil {
		sigv4WriteControlPlaneError(w, r, serr)
		return false
	}
	if res == sigv4NoCredential {
		sigv4WriteControlPlaneMissingAuth(w, r)
		return false
	}
	return true
}

func sigv4ExemptControlPlaneAction(action string) bool {
	switch action {
	case "sts:AssumeRoleWithWebIdentity", "sts:AssumeRoleWithSAML":
		return true
	}
	return false
}

func sigv4WriteControlPlaneError(w http.ResponseWriter, r *http.Request, serr *sigv4Error) {
	jsonCode, queryCode := sigv4ErrorCodes(serr.kind)
	if r.Header.Get("X-Amz-Target") != "" {
		AWSError(w, jsonCode, serr.message, http.StatusForbidden)
		return
	}
	if sigv4RequestService(r) == "ec2" {
		ec2ErrorXML(w, queryCode, serr.message, http.StatusForbidden)
		return
	}
	iamErrorXML(w, queryCode, serr.message, http.StatusForbidden)
}

func sigv4WriteControlPlaneMissingAuth(w http.ResponseWriter, r *http.Request) {
	const msg = "Request is missing Authentication Token"
	if r.Header.Get("X-Amz-Target") != "" {
		AWSError(w, "MissingAuthenticationTokenException", msg, http.StatusForbidden)
		return
	}
	if sigv4RequestService(r) == "ec2" {
		ec2ErrorXML(w, "MissingAuthenticationToken", msg, http.StatusForbidden)
		return
	}
	iamErrorXML(w, "MissingAuthenticationToken", msg, http.StatusForbidden)
}

func sigv4ErrorCodes(kind sigv4ErrKind) (jsonCode, queryCode string) {
	switch kind {
	case sigErrSignatureMismatch:
		return "SignatureDoesNotMatch", "SignatureDoesNotMatch"
	case sigErrExpiredToken:
		return "ExpiredTokenException", "ExpiredToken"
	default:
		return "UnrecognizedClientException", "InvalidClientTokenId"
	}
}

// sigv4RequestService reports the AWS service the request targets, from the
// signed credential scope when present, otherwise from the awsQuery/awsJson
// event-source mapping. Used only to pick the error wire shape.
func sigv4RequestService(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if i := strings.Index(auth, "Credential="); i >= 0 {
		if cred, ok := parseCredScope(auth[i+len("Credential="):]); ok {
			return cred.service
		}
	}
	if src, ok := awsEventSource(r); ok {
		return strings.SplitN(src, ".", 2)[0]
	}
	return ""
}

// sigv4WriteS3Error renders an authentication failure in S3's XML shape with the
// S3-specific error codes (S3 uses InvalidAccessKeyId, not InvalidClientTokenId).
func sigv4WriteS3Error(w http.ResponseWriter, r *http.Request, serr *sigv4Error) {
	resource := strings.TrimPrefix(r.URL.Path, "/")
	switch serr.kind {
	case sigErrInvalidClientToken:
		S3ErrorXML(w, "InvalidAccessKeyId",
			"The AWS Access Key Id you provided does not exist in our records.",
			resource, generateUUID(), http.StatusForbidden)
	case sigErrExpiredToken:
		S3ErrorXML(w, "ExpiredToken", "The provided token has expired.",
			resource, generateUUID(), http.StatusForbidden)
	default:
		S3ErrorXML(w, "SignatureDoesNotMatch", serr.message,
			resource, generateUUID(), http.StatusForbidden)
	}
}

// sigv4PresentedSessionToken is the session token the request carries. Amazon
// S3 Express One Zone puts it in its own header rather than in
// X-Amz-Security-Token: a Zonal endpoint request is signed with the temporary
// credentials CreateSession returned and carries the token in
// x-amz-s3session-token, which is what the SDKs send.
func sigv4PresentedSessionToken(r *http.Request) string {
	if token := r.Header.Get(s3ExpressSessionTokenHeader); token != "" {
		return token
	}
	return r.Header.Get("X-Amz-Security-Token")
}
