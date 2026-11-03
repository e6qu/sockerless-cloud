package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The management account's root user signs with the access key AWS issues it
// from the console, which no API creates; the test holds one as that console
// would have issued it.
const testRootAccessKey = "AKIAROOTCONSOLEKEY01"

func resetRootCredentialState(t *testing.T) {
	keys, temp, users, profiles, certs, devices := iamAccessKeys, iamTempCreds, iamUsers, iamLoginProfiles, iamSigningCerts, iamVirtualMFADevices
	userPolicies := iamUserPolicies
	t.Cleanup(func() {
		iamAccessKeys, iamTempCreds, iamUsers, iamLoginProfiles, iamSigningCerts, iamVirtualMFADevices = keys, temp, users, profiles, certs, devices
		iamUserPolicies = userPolicies
	})
	iamAccessKeys = sim.NewStateStore[IAMAccessKey]()
	iamTempCreds = sim.NewStateStore[IAMTempCred]()
	iamUsers = sim.NewStateStore[IAMUser]()
	iamUserPolicies = sim.NewStateStore[IAMUserPolicy]()
	iamLoginProfiles = sim.NewStateStore[IAMLoginProfile]()
	iamSigningCerts = sim.NewStateStore[IAMSigningCertificate]()
	iamVirtualMFADevices = sim.NewStateStore[IAMVirtualMFADevice]()
	seedRootAdminCredential()
	iamAccessKeys.Put(testRootAccessKey, IAMAccessKey{AccessKeyId: testRootAccessKey, SecretAccessKey: "root-secret",
		Status: "Active", CreateDate: time.Now().UTC().Format(time.RFC3339)})
}

func callQueryAs(akid string, handler http.HandlerFunc, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+akid+"/20260101/us-east-1/iam/aws4_request, SignedHeaders=host, Signature=0")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// TestRootUserAccessKeys has the management account's root user manage its
// own access keys and signing certificates: the IAM credential operations
// act on the root user when they name no user, and what they return names
// none.
func TestRootUserAccessKeys(t *testing.T) {
	resetRootCredentialState(t)
	caller, ok := iamCallerForAccessKey(testRootAccessKey)
	if !ok || !caller.Root || !caller.Unrestricted || caller.Arn != iamRootArn(awsAccountID()) {
		t.Fatalf("root key resolves to %+v", caller)
	}

	rec := callQueryAs(testRootAccessKey, handleIAMCreateAccessKey, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<UserName>") {
		t.Fatalf("root CreateAccessKey: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Key string `xml:"CreateAccessKeyResult>AccessKey>AccessKeyId"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Key == "" {
		t.Fatalf("root CreateAccessKey body: %v %s", err, rec.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleIAMListAccessKeys, nil)
	if !strings.Contains(rec.Body.String(), created.Key) || !strings.Contains(rec.Body.String(), testRootAccessKey) ||
		strings.Contains(rec.Body.String(), seedAdminAccessKey+"<") {
		t.Fatalf("root ListAccessKeys lists the root user's keys only: %s", rec.Body.String())
	}
	rec = callQueryAs(seedAdminAccessKey, handleIAMListAccessKeys, nil)
	if strings.Contains(rec.Body.String(), created.Key) || !strings.Contains(rec.Body.String(), "<AccessKeyId>"+seedAdminAccessKey+"<") {
		t.Fatalf("an IAM user's ListAccessKeys with no UserName lists its own keys: %s", rec.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleIAMUpdateAccessKey, url.Values{"AccessKeyId": {created.Key}, "Status": {"Inactive"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("root UpdateAccessKey: %d %s", rec.Code, rec.Body.String())
	}
	if key, _ := iamAccessKeys.Get(created.Key); key.Status != "Inactive" {
		t.Fatalf("key status %q", key.Status)
	}
	rec = callQueryAs(seedAdminAccessKey, handleIAMDeleteAccessKey, url.Values{"AccessKeyId": {created.Key}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an IAM user deleting the root user's key without naming it: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQueryAs(testRootAccessKey, handleIAMGetAccessKeyLastUsed, url.Values{"AccessKeyId": {created.Key}})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<UserName>") {
		t.Fatalf("root GetAccessKeyLastUsed: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQueryAs(testRootAccessKey, handleIAMDeleteAccessKey, url.Values{"AccessKeyId": {created.Key}})
	if rec.Code != http.StatusOK {
		t.Fatalf("root DeleteAccessKey: %d %s", rec.Code, rec.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleIAMGetUser, nil)
	if !strings.Contains(rec.Body.String(), "<Arn>"+iamRootArn(awsAccountID())+"</Arn>") ||
		!strings.Contains(rec.Body.String(), "<UserId>"+awsAccountID()+"</UserId>") {
		t.Fatalf("root GetUser: %s", rec.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleIAMUploadSigningCertificate, url.Values{"CertificateBody": {"-----BEGIN CERTIFICATE-----"}})
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<UserName>") {
		t.Fatalf("root UploadSigningCertificate: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQueryAs(testRootAccessKey, handleIAMListSigningCertificates, nil)
	if strings.Count(rec.Body.String(), "<CertificateId>") != 1 {
		t.Fatalf("root ListSigningCertificates: %s", rec.Body.String())
	}
	rec = callQueryAs(seedAdminAccessKey, handleIAMListSigningCertificates, nil)
	if strings.Contains(rec.Body.String(), "<CertificateId>") {
		t.Fatalf("an IAM user lists no root certificate: %s", rec.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleIAMCreateLoginProfile, url.Values{"Password": {"Root-Passw0rd!"}})
	if rec.Code != http.StatusBadRequest || xmlErrorCode(t, rec) != "ValidationError" {
		t.Fatalf("a root user's password is created only from an AssumeRoot session: %d %s", rec.Code, rec.Body.String())
	}
}

// TestRootUserSessionToken holds a root user's GetSessionToken session to an
// hour: by default, and for a longer request.
func TestRootUserSessionToken(t *testing.T) {
	resetRootCredentialState(t)
	expiration := func(form url.Values) time.Time {
		t.Helper()
		rec := callQueryAs(testRootAccessKey, handleSTSGetSessionToken, form)
		if rec.Code != http.StatusOK {
			t.Fatalf("root GetSessionToken %v: %d %s", form, rec.Code, rec.Body.String())
		}
		var out struct {
			Expiration time.Time `xml:"GetSessionTokenResult>Credentials>Expiration"`
		}
		if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Expiration
	}
	within := func(got time.Time, d time.Duration) bool {
		return got.Sub(time.Now().Add(d)).Abs() < time.Minute
	}
	if got := expiration(nil); !within(got, time.Hour) {
		t.Fatalf("default root session expires %v", got)
	}
	if got := expiration(url.Values{"DurationSeconds": {"7200"}}); !within(got, time.Hour) {
		t.Fatalf("a two-hour root session expires %v", got)
	}
	if got := expiration(url.Values{"DurationSeconds": {"1800"}}); !within(got, 30*time.Minute) {
		t.Fatalf("a half-hour root session expires %v", got)
	}
	rec := callQueryAs(testRootAccessKey, handleSTSGetSessionToken, url.Values{"DurationSeconds": {"899"}})
	if rec.Code != http.StatusBadRequest || xmlErrorCode(t, rec) != "ValidationError" {
		t.Fatalf("a root session under 900 seconds: %d %s", rec.Code, rec.Body.String())
	}
	user := callQueryAs(seedAdminAccessKey, handleSTSGetSessionToken, nil)
	var userSession struct {
		Expiration time.Time `xml:"GetSessionTokenResult>Credentials>Expiration"`
	}
	if err := xml.Unmarshal(user.Body.Bytes(), &userSession); err != nil || !within(userSession.Expiration, 12*time.Hour) {
		t.Fatalf("an IAM user's session lasts twelve hours by default: %v %s", err, user.Body.String())
	}

	rec = callQueryAs(testRootAccessKey, handleSTSAssumeRoot, url.Values{
		"TargetPrincipal": {"111122223333"}, "TaskPolicyArn.arn": {"arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy"}})
	if rec.Code != http.StatusForbidden || xmlErrorCode(t, rec) != "AccessDenied" {
		t.Fatalf("a root user cannot call AssumeRoot: %d %s", rec.Code, rec.Body.String())
	}
	rec = callQueryAs(testRootAccessKey, handleGetCallerIdentity, nil)
	if !strings.Contains(rec.Body.String(), "<Arn>"+iamRootArn(awsAccountID())+"</Arn>") {
		t.Fatalf("root GetCallerIdentity: %s", rec.Body.String())
	}
}
