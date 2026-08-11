package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// resetCredentialStores re-creates the in-memory credential stores the SigV4
// gate resolves against, so each case starts from a known set.
func resetCredentialStores() {
	iamAccessKeys = sim.MakeStore[IAMAccessKey](nil, "iam_access_keys")
	iamTempCreds = sim.MakeStore[IAMTempCred](nil, "iam_temp_creds")
}

// TestSigV4_KnownVector validates the signing math against the AWS SigV4
// test-suite "get-vanilla" canonical request: a GET / to example.amazonaws.com
// signed with the documented AKIDEXAMPLE credential. The canonical-request hash
// (bb579772…) is the value AWS documents for get-vanilla, confirming the
// canonicalization; the signature is cross-checked against a standalone
// reference implementation of the SigV4 algorithm — an oracle independent of
// this code — for the string-to-sign and signing-key derivation.
func TestSigV4_KnownVector(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.amazonaws.com/", nil)
	r.Header.Set("X-Amz-Date", "20150830T123600Z")

	cred := credScope{accessKeyID: "AKIDEXAMPLE", date: "20150830", region: "us-east-1", service: "service"}
	const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	canonReq := sigv4CanonicalRequest(r, []string{"host", "x-amz-date"}, "", emptyPayloadHash, true)
	if h := hexSHA256([]byte(canonReq)); h != "bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63" {
		t.Fatalf("canonical request hash mismatch: %s\ncanonical request:\n%s", h, canonReq)
	}

	got := sigv4Signature("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", cred, "20150830T123600Z", canonReq)
	const want = "ea21d6f05e96a897f6000a1a293f0a5bf0f92a00343409e820dce329ca6365ea"
	if got != want {
		t.Fatalf("get-vanilla signature mismatch:\n got  %s\n want %s", got, want)
	}
}

// signForTest signs r with the given credential using the production canonical
// builders and installs the Authorization header, mirroring what an SDK does.
func signForTest(r *http.Request, cred credScope, secret string, sessionToken string) {
	amzDate := cred.date + "T000000Z"
	r.Header.Set("X-Amz-Date", amzDate)
	const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	r.Header.Set("X-Amz-Content-Sha256", emptyPayloadHash)
	if sessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", sessionToken)
	}
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if sessionToken != "" {
		signed = append(signed, "x-amz-security-token")
	}
	canonReq := sigv4CanonicalRequest(r, signed, sigv4CanonicalQuery(r.URL.Query(), false), emptyPayloadHash, true)
	sig := sigv4Signature(secret, cred, amzDate, canonReq)
	scope := cred.accessKeyID + "/" + cred.date + "/" + cred.region + "/" + cred.service + "/aws4_request"
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+scope+
		", SignedHeaders="+strings.Join(signed, ";")+", Signature="+sig)
}

// signSeedControlPlane signs a control-plane request with the seeded
// administrator credential, the way any real SDK/CLI client does. In-process
// tests that drive the POST / dispatcher directly use it so their requests
// authenticate through the SigV4 gate.
func signSeedControlPlane(r *http.Request) {
	signForTest(r, credScope{accessKeyID: seedAdminAccessKey, date: "20250101", region: "us-east-1", service: "aws"}, seedAdminSecretKey, "")
}

func TestSigV4_ValidLongTermKey(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAEXAMPLEKEYID0001", IAMAccessKey{
		AccessKeyId: "AKIAEXAMPLEKEYID0001", SecretAccessKey: "secret-one", Status: "Active",
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "AKIAEXAMPLEKEYID0001", date: "20250101", region: "us-east-1", service: "ecs"}, "secret-one", "")

	res, serr := sigv4Verify(r, nil)
	if serr != nil {
		t.Fatalf("expected valid signature, got error kind %d (%s)", serr.kind, serr.message)
	}
	if res != sigv4Verified {
		t.Fatalf("expected sigv4Verified, got %d", res)
	}
}

func TestSigV4_TamperedSignature(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAEXAMPLEKEYID0001", IAMAccessKey{
		AccessKeyId: "AKIAEXAMPLEKEYID0001", SecretAccessKey: "secret-one", Status: "Active",
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "AKIAEXAMPLEKEYID0001", date: "20250101", region: "us-east-1", service: "ecs"}, "secret-one", "")
	// Flip the signature to a wrong (but well-formed) value.
	auth := r.Header.Get("Authorization")
	r.Header.Set("Authorization", auth[:strings.Index(auth, "Signature=")+len("Signature=")]+strings.Repeat("0", 64))

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrSignatureMismatch {
		t.Fatalf("expected SignatureDoesNotMatch, got %+v", serr)
	}
}

// TestSigV4_ForgedPrincipal is the core of the vulnerability: an attacker holding
// its own key signs but puts a victim's access-key id in the Credential field.
// Verification must fail because the signature was computed with the wrong secret.
func TestSigV4_ForgedPrincipal(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAVICTIMADMINKEY001", IAMAccessKey{
		AccessKeyId: "AKIAVICTIMADMINKEY001", SecretAccessKey: "victim-secret", Status: "Active",
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	// Sign with the attacker's secret but claim the victim's scope/key id.
	signForTest(r, credScope{accessKeyID: "AKIAVICTIMADMINKEY001", date: "20250101", region: "us-east-1", service: "ecs"}, "attacker-secret", "")

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrSignatureMismatch {
		t.Fatalf("forged principal must be rejected with SignatureDoesNotMatch, got %+v", serr)
	}
}

func TestSigV4_UnknownKey(t *testing.T) {
	resetCredentialStores()
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "AKIAUNKNOWNKEYID0001", date: "20250101", region: "us-east-1", service: "ecs"}, "whatever", "")

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrInvalidClientToken {
		t.Fatalf("expected InvalidClientToken for unknown key, got %+v", serr)
	}
}

func TestSigV4_InactiveKey(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAINACTIVEKEYID001", IAMAccessKey{
		AccessKeyId: "AKIAINACTIVEKEYID001", SecretAccessKey: "secret-one", Status: "Inactive",
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "AKIAINACTIVEKEYID001", date: "20250101", region: "us-east-1", service: "ecs"}, "secret-one", "")

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrInvalidClientToken {
		t.Fatalf("expected InvalidClientToken for inactive key, got %+v", serr)
	}
}

func TestSigV4_TempCredValid(t *testing.T) {
	resetCredentialStores()
	iamTempCreds.Put("ASIAEXAMPLESESSION01", IAMTempCred{
		AccessKeyID: "ASIAEXAMPLESESSION01", SecretAccessKey: "temp-secret", SessionToken: "the-session-token",
		Expiration: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "ASIAEXAMPLESESSION01", date: "20250101", region: "us-east-1", service: "ecs"}, "temp-secret", "the-session-token")

	if _, serr := sigv4Verify(r, nil); serr != nil {
		t.Fatalf("expected valid temp-cred signature, got %+v", serr)
	}
}

func TestSigV4_TempCredExpired(t *testing.T) {
	resetCredentialStores()
	iamTempCreds.Put("ASIAEXPIREDSESSION01", IAMTempCred{
		AccessKeyID: "ASIAEXPIREDSESSION01", SecretAccessKey: "temp-secret", SessionToken: "tok",
		Expiration: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: "ASIAEXPIREDSESSION01", date: "20250101", region: "us-east-1", service: "ecs"}, "temp-secret", "tok")

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrExpiredToken {
		t.Fatalf("expected ExpiredToken, got %+v", serr)
	}
}

func TestSigV4_TempCredSessionTokenMismatch(t *testing.T) {
	resetCredentialStores()
	iamTempCreds.Put("ASIAMISMATCHSESSION1", IAMTempCred{
		AccessKeyID: "ASIAMISMATCHSESSION1", SecretAccessKey: "temp-secret", SessionToken: "the-real-token",
		Expiration: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	// Signs correctly but presents a different session token.
	signForTest(r, credScope{accessKeyID: "ASIAMISMATCHSESSION1", date: "20250101", region: "us-east-1", service: "ecs"}, "temp-secret", "a-forged-token")

	_, serr := sigv4Verify(r, nil)
	if serr == nil || serr.kind != sigErrInvalidClientToken {
		t.Fatalf("expected InvalidClientToken for session-token mismatch, got %+v", serr)
	}
}

// TestSigV4_PresignedQuery exercises the presigned-URL (query-string signature)
// path: a signature carried in X-Amz-Signature over a canonical query that
// excludes it, with an UNSIGNED-PAYLOAD payload hash.
func TestSigV4_PresignedQuery(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAPRESIGNKEYID0001", IAMAccessKey{
		AccessKeyId: "AKIAPRESIGNKEYID0001", SecretAccessKey: "presign-secret", Status: "Active",
	})
	cred := credScope{accessKeyID: "AKIAPRESIGNKEYID0001", date: "20250101", region: "us-east-1", service: "s3"}
	amzDate := cred.date + "T000000Z"
	scope := cred.accessKeyID + "/" + cred.date + "/" + cred.region + "/" + cred.service + "/aws4_request"

	// Build the request with the presign query params (signature excluded).
	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", "900")
	q.Set("X-Amz-SignedHeaders", "host")
	r := httptest.NewRequest("GET", "http://sim.local/bucket/object.txt?"+q.Encode(), nil)

	canonReq := sigv4CanonicalRequest(r, []string{"host"}, sigv4CanonicalQuery(r.URL.Query(), true), "UNSIGNED-PAYLOAD", true)
	sig := sigv4Signature("presign-secret", cred, amzDate, canonReq)

	// Re-issue with the computed signature appended.
	q.Set("X-Amz-Signature", sig)
	signed := httptest.NewRequest("GET", "http://sim.local/bucket/object.txt?"+q.Encode(), nil)
	if _, serr := sigv4Verify(signed, nil); serr != nil {
		t.Fatalf("expected valid presigned signature, got %+v", serr)
	}

	// A tampered presigned signature is rejected.
	q.Set("X-Amz-Signature", strings.Repeat("0", 64))
	tampered := httptest.NewRequest("GET", "http://sim.local/bucket/object.txt?"+q.Encode(), nil)
	if _, serr := sigv4Verify(tampered, nil); serr == nil || serr.kind != sigErrSignatureMismatch {
		t.Fatalf("expected SignatureDoesNotMatch for tampered presigned URL, got %+v", serr)
	}
}

// TestSigV4_ExemptWebIdentityUnsigned confirms the control-plane gate lets an
// unsigned AssumeRoleWithWebIdentity through: its trust comes from the token in
// the body, not a SigV4 signature.
func TestSigV4_ExemptWebIdentityUnsigned(t *testing.T) {
	resetCredentialStores()
	form := "Action=AssumeRoleWithWebIdentity&Version=2011-06-15"
	r := httptest.NewRequest("POST", "http://sim.local/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := httptest.NewRecorder()
	if !sigv4EnforceControlPlane(w, r, []byte(form)) {
		t.Fatalf("AssumeRoleWithWebIdentity must be exempt from SigV4, got status %d", w.Code)
	}
}

// TestSigV4_ControlPlaneMissingAuth confirms an unsigned control-plane call to a
// signed operation is rejected rather than silently trusted.
func TestSigV4_ControlPlaneMissingAuth(t *testing.T) {
	resetCredentialStores()
	r := httptest.NewRequest("POST", "http://sim.local/", strings.NewReader("{}"))
	r.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.RunTask")

	w := httptest.NewRecorder()
	if sigv4EnforceControlPlane(w, r, []byte("{}")) {
		t.Fatal("unsigned RunTask must be rejected")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "MissingAuthenticationToken") {
		t.Fatalf("expected MissingAuthenticationToken, got body %q", w.Body.String())
	}
}

// TestSigV4_ControlPlaneForgedRejected exercises the full control-plane gate:
// a forged-principal request is rejected before authorization.
func TestSigV4_ControlPlaneForgedRejected(t *testing.T) {
	resetCredentialStores()
	iamAccessKeys.Put("AKIAVICTIMADMINKEY001", IAMAccessKey{
		AccessKeyId: "AKIAVICTIMADMINKEY001", SecretAccessKey: "victim-secret", Status: "Active",
	})
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	r.Header.Set("X-Amz-Target", "AmazonEC2ContainerServiceV20141113.RunTask")
	signForTest(r, credScope{accessKeyID: "AKIAVICTIMADMINKEY001", date: "20250101", region: "us-east-1", service: "ecs"}, "not-the-victim-secret", "")

	w := httptest.NewRecorder()
	if sigv4EnforceControlPlane(w, r, nil) {
		t.Fatal("forged control-plane request must be rejected")
	}
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "SignatureDoesNotMatch") {
		t.Fatalf("expected 403 SignatureDoesNotMatch, got %d body %q", w.Code, w.Body.String())
	}
}
