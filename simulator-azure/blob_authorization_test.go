package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The data plane refuses what the account's keys did not sign.
//
// It used to refuse nothing: `Authorization: SharedKey …` was read as a routing
// signal and never checked, `sig=` was read as a routing signal and never
// checked, and a request carrying neither was served anyway. Every one of these
// cases therefore passed before, which is what made the credentials the
// simulator issues decorative and left the App Service backup path unable to do
// more than count a signature's parameters.

// blobAuthTestContainer creates one container on an ARM-known account and
// returns the account name.
func blobAuthTestContainer(t *testing.T, srv *sim.Server, account, container string) {
	t.Helper()
	rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create container: status %d: %s", rec.Code, rec.Body.String())
	}
}

// blobAuthTestUnsigned issues a request carrying whatever credential the caller
// supplies, and no other.
func blobAuthTestUnsigned(t *testing.T, srv *sim.Server, method, account, target, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Host = account + ".blob.localhost"
	req.Header.Set("x-ms-version", "2025-01-05")
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func blobAuthTestRequireRefused(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("%s: status = %d, want 403: %s", what, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-ms-error-code"); got != "AuthenticationFailed" {
		t.Fatalf("%s: x-ms-error-code = %q, want AuthenticationFailed", what, got)
	}
	if !strings.Contains(rec.Body.String(), "<Code>AuthenticationFailed</Code>") {
		t.Fatalf("%s: body is not the storage XML refusal: %s", what, rec.Body.String())
	}
}

func TestBlobDataPlaneRefusesWhatTheKeysDidNotSign(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container, blob = "authblobacct", "authcontainer", "secret.txt"
	blobAuthTestContainer(t, srv, account, container)

	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account, "/"+container+"/"+blob, ""),
		"a request with no credential at all")

	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account, "/"+container+"/"+blob,
			"SharedKey "+account+":bm90LWEtc2lnbmF0dXJl"),
		"a Shared Key signature the account's keys did not produce")

	// A signature made with the right algorithm over the wrong string is the
	// case a length or format check would miss.
	req := httptest.NewRequest(http.MethodGet, "/"+container+"/"+blob, nil)
	req.Host = account + ".blob.localhost"
	req.Header.Set("x-ms-version", "2025-01-05")
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	keys, ok := storageAccountKeys(account)
	if !ok {
		t.Fatal("the account serves no keys")
	}
	material, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil {
		t.Fatalf("decode the account key: %v", err)
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte("GET\nthis is not the string the service signs"))
	req.Header.Set("Authorization",
		"SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	blobAuthTestRequireRefused(t, rec, "a signature over the wrong string")

	// And a Shared Key credential naming a different account than the one
	// addressed.
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account, "/"+container+"/"+blob,
			"SharedKey someoneelse:bm90LWEtc2lnbmF0dXJl"),
		"a credential naming another account")
}

// blobAuthTestSAS builds a service Shared Access Signature over a container,
// signed with the account's own key, in the sixteen fields a service signature
// carries.
func blobAuthTestSAS(t *testing.T, account, container, permissions string, expiry time.Time) url.Values {
	t.Helper()
	q := url.Values{}
	q.Set("sv", "2026-06-06")
	q.Set("sr", "c")
	q.Set("sp", permissions)
	q.Set("se", expiry.UTC().Format("2006-01-02T15:04:05Z"))
	keys, ok := storageAccountKeys(account)
	if !ok {
		t.Fatal("the account serves no keys")
	}
	material, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil {
		t.Fatalf("decode the account key: %v", err)
	}
	mac := hmac.New(sha256.New, material)
	_, _ = mac.Write([]byte(blobSASStringToSign("blob", account, container, "", q)))
	q.Set("sig", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return q
}

func TestBlobDataPlaneHonoursAServiceSignature(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container, blob = "sasblobacct", "sascontainer", "signed.txt"
	blobAuthTestContainer(t, srv, account, container)
	if rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob,
		[]byte("signed"), map[string]string{"x-ms-blob-type": "BlockBlob"}); rec.Code != http.StatusCreated {
		t.Fatalf("seed the blob: status %d: %s", rec.Code, rec.Body.String())
	}

	// A read signature serves a read.
	read := blobAuthTestSAS(t, account, container, "rl", time.Now().Add(time.Hour))
	rec := blobAuthTestUnsigned(t, srv, http.MethodGet, account,
		"/"+container+"/"+blob+"?"+read.Encode(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("a valid read signature: status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "signed" {
		t.Fatalf("body = %q, want %q", got, "signed")
	}

	// The same signature does not serve a write: `rl` does not include it.
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodDelete, account,
			"/"+container+"/"+blob+"?"+read.Encode(), ""),
		"a read signature used for a delete")

	// An expired signature is refused however well it is signed.
	expired := blobAuthTestSAS(t, account, container, "rl", time.Now().Add(-time.Minute))
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account,
			"/"+container+"/"+blob+"?"+expired.Encode(), ""),
		"an expired signature")

	// A signature whose fields were edited after signing is refused: the
	// permissions are what the signature covers, not what the URL claims.
	widened := blobAuthTestSAS(t, account, container, "rl", time.Now().Add(time.Hour))
	widened.Set("sp", "racwdl")
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodDelete, account,
			"/"+container+"/"+blob+"?"+widened.Encode(), ""),
		"a signature whose permissions were widened after signing")

	// A signature over another container does not reach this one.
	elsewhere := blobAuthTestSAS(t, account, "othercontainer", "rl", time.Now().Add(time.Hour))
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account,
			"/"+container+"/"+blob+"?"+elsewhere.Encode(), ""),
		"a signature over another container")
}

func TestBlobDataPlaneServesAPublicContainerAnonymously(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container, blob = "publicblobacct", "publiccontainer", "open.txt"
	blobAuthTestContainer(t, srv, account, container)
	if rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob,
		[]byte("open"), map[string]string{"x-ms-blob-type": "BlockBlob"}); rec.Code != http.StatusCreated {
		t.Fatalf("seed the blob: status %d: %s", rec.Code, rec.Body.String())
	}

	// Private by default: an anonymous read reaches nothing.
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account, "/"+container+"/"+blob, ""),
		"an anonymous read of a private container")

	// `blob` exposes the blobs and not the listing.
	if rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"?restype=container&comp=acl", nil,
		map[string]string{"x-ms-blob-public-access": "blob"}); rec.Code != http.StatusOK {
		t.Fatalf("set the public access level: status %d: %s", rec.Code, rec.Body.String())
	}
	rec := blobAuthTestUnsigned(t, srv, http.MethodGet, account, "/"+container+"/"+blob, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("an anonymous read of a blob-public container: status %d: %s", rec.Code, rec.Body.String())
	}
	blobAuthTestRequireRefused(t,
		blobAuthTestUnsigned(t, srv, http.MethodGet, account,
			"/"+container+"?restype=container&comp=list", ""),
		"an anonymous listing of a blob-public container")

	// `container` exposes the listing too.
	if rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"?restype=container&comp=acl", nil,
		map[string]string{"x-ms-blob-public-access": "container"}); rec.Code != http.StatusOK {
		t.Fatalf("widen the public access level: status %d: %s", rec.Code, rec.Body.String())
	}
	if rec := blobAuthTestUnsigned(t, srv, http.MethodGet, account,
		"/"+container+"?restype=container&comp=list", ""); rec.Code != http.StatusOK {
		t.Fatalf("an anonymous listing of a container-public container: status %d: %s", rec.Code, rec.Body.String())
	}
}
