package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func signedWithTempCred(akid, secret, token string) (sigv4Result, *sigv4Error) {
	r := httptest.NewRequest("POST", "http://sim.local/", nil)
	signForTest(r, credScope{accessKeyID: akid, date: "20250101", region: "us-east-1", service: "ecs"}, secret, token)
	return sigv4Verify(r, nil)
}

// A credential the sweeper has deleted is still refused as expired, because
// its session token carries its own expiration.
func TestPrunedTempCredIsStillRefusedAsExpired(t *testing.T) {
	resetCredentialStores()
	akid, secret, token := stsMintTempCred(time.Now().Add(-time.Minute))
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, SecretAccessKey: secret, SessionToken: token,
		Expiration: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if _, serr := signedWithTempCred(akid, secret, token); serr == nil || serr.kind != sigErrExpiredToken {
		t.Fatalf("stored expired credential: got %+v, want ExpiredToken", serr)
	}

	if swept := stsSweepExpiredTempCreds(time.Now()); swept != 1 {
		t.Fatalf("sweep deleted %d credentials, want 1", swept)
	}
	if _, serr := signedWithTempCred(akid, secret, token); serr == nil || serr.kind != sigErrExpiredToken {
		t.Fatalf("pruned expired credential: got %+v, want ExpiredToken", serr)
	}
}

// Only a token this simulator sealed for this access key reports an
// expiration; anything else is an invalid client token.
func TestUnsealedTokensAreInvalidNotExpired(t *testing.T) {
	resetCredentialStores()
	past := time.Now().Add(-time.Minute)
	akid, secret, token := stsMintTempCred(past)
	otherAKID, _, _ := stsMintTempCred(past)

	tampered := []byte(token)
	tampered[3] = map[bool]byte{true: 'B', false: 'A'}[tampered[3] == 'A']

	for name, tc := range map[string]struct{ akid, token string }{
		"token sealed for another key": {otherAKID, token},
		"tampered expiration":          {akid, string(tampered)},
		"random token":                 {akid, strings.Repeat("A", len(token))},
		"no token":                     {akid, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, serr := signedWithTempCred(tc.akid, secret, tc.token); serr == nil || serr.kind != sigErrInvalidClientToken {
				t.Fatalf("got %+v, want InvalidClientTokenId", serr)
			}
		})
	}
}

func TestSessionTokenKeySurvivesARestart(t *testing.T) {
	db, err := sim.OpenDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previous := stsTokenKeys
	t.Cleanup(func() { stsTokenKeys = previous })

	stsTokenKeys = sim.MakeStore[string](db, "sts_token_keys")
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	akid, _, token := stsMintTempCred(exp)

	stsTokenKeys = sim.MakeStore[string](db, "sts_token_keys")
	got, ok := stsTokenExpiration(akid, token)
	if !ok || !got.Equal(exp) {
		t.Fatalf("after reopening: expiration %v, %v; want %v, true", got, ok, exp)
	}
}

func TestTempCredSweepKeepsLiveCredentials(t *testing.T) {
	resetCredentialStores()
	now := time.Now()
	iamTempCreds.Put("ASIAEXPIRED", IAMTempCred{AccessKeyID: "ASIAEXPIRED", Expiration: now.Add(-time.Second).UTC().Format(time.RFC3339)})
	iamTempCreds.Put("ASIALIVE", IAMTempCred{AccessKeyID: "ASIALIVE", Expiration: now.Add(time.Minute).UTC().Format(time.RFC3339)})
	for _, akid := range []string{"ASIAEXPIRED", "ASIALIVE"} {
		s3ExpressSessions.Put(akid, S3ExpressSession{AccessKeyID: akid})
		s3AccessGrantsCredentials.Put(akid, S3AccessGrantsCredential{AccessKeyID: akid})
	}
	if swept := stsSweepExpiredTempCreds(now); swept != 1 {
		t.Fatalf("sweep deleted %d credentials, want 1", swept)
	}
	if _, ok := iamTempCreds.Get("ASIALIVE"); !ok {
		t.Error("the live credential was deleted")
	}
	if _, ok := s3ExpressSessions.Get("ASIAEXPIRED"); ok {
		t.Error("the S3 Express session of a deleted credential survived")
	}
	if _, ok := s3AccessGrantsCredentials.Get("ASIAEXPIRED"); ok {
		t.Error("the S3 Access Grants record of a deleted credential survived")
	}
	if _, ok := s3ExpressSessions.Get("ASIALIVE"); !ok {
		t.Error("the S3 Express session of a live credential was deleted")
	}
	if _, ok := s3AccessGrantsCredentials.Get("ASIALIVE"); !ok {
		t.Error("the S3 Access Grants record of a live credential was deleted")
	}
}

// The task role credentials endpoint serves a task one credential, as the
// Amazon ECS agent does, and replaces it only when too little of its lifetime
// is left.
func TestECSTaskCredentialIsServedUntilItNeedsRefreshing(t *testing.T) {
	resetCredentialStores()
	previous := ecsTaskCredentials
	ecsTaskCredentials = sim.MakeStore[ecsHeldCredential](nil, "ecs_task_credentials")
	t.Cleanup(func() { ecsTaskCredentials = previous })

	start := time.Now().UTC().Truncate(time.Second)
	first := ecsTaskCredential("task-a", "role-a", start)
	if again := ecsTaskCredential("task-a", "role-a", start.Add(ecsTaskCredentialMinRemaining-time.Minute)); again.AccessKeyID != first.AccessKeyID {
		t.Fatalf("a credential with more than half its lifetime left was replaced")
	}
	refreshed := ecsTaskCredential("task-a", "role-a", start.Add(ecsTaskCredentialMinRemaining+time.Second))
	if refreshed.AccessKeyID == first.AccessKeyID {
		t.Fatal("a credential with less than half its lifetime left was served again")
	}
	if other := ecsTaskCredential("task-b", "role-a", start); other.AccessKeyID == first.AccessKeyID {
		t.Fatal("two tasks were served one credential")
	}
	if switched := ecsTaskCredential("task-a", "role-b", start.Add(ecsTaskCredentialMinRemaining+2*time.Second)); switched.AccessKeyID == refreshed.AccessKeyID {
		t.Fatal("a credential for another role was served")
	}
	if got := iamTempCreds.Len(); got != 4 {
		t.Errorf("%d credentials minted, want 4", got)
	}
}

func TestWAFSampleSweepKeepsTheLastThreeHours(t *testing.T) {
	previous := wafSampledRequests
	wafSampledRequests = sim.MakeStore[wafSampledRequest](nil, "wafv2_sampled_requests")
	t.Cleanup(func() { wafSampledRequests = previous })
	now := time.Now().UTC()
	wafSampledRequests.Put("old", wafSampledRequest{Timestamp: now.Add(-wafSampleRetention - time.Second)})
	wafSampledRequests.Put("recent", wafSampledRequest{Timestamp: now.Add(-wafSampleRetention + time.Minute)})
	if swept := wafSweepSampledRequests(now); swept != 1 {
		t.Fatalf("sweep deleted %d samples, want 1", swept)
	}
	if _, ok := wafSampledRequests.Get("recent"); !ok {
		t.Error("a sample inside the retention window was deleted")
	}
}
