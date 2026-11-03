package main

import (
	"net/http"
	"testing"
	"time"
)

const iamDenyEveryoneS3 = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:*",` +
	`"Resource":["arn:aws:s3:::lockout-bucket","arn:aws:s3:::lockout-bucket/*"]}]}`

// iamSeedRootSession records a root session of account, scoped by the root
// task policy taskPolicy, as AssumeRoot would for a member account.
func iamSeedRootSession(t *testing.T, akid, account, taskPolicy string) {
	t.Helper()
	iamTempCreds.Put(akid, IAMTempCred{
		AccessKeyID: akid, PrincipalArn: "arn:aws:iam::" + account + ":root",
		Expiration: time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339), CreatedAt: time.Now().UTC().Format(time.RFC3339),
		UnrestrictedIdentity: true, TaskPolicyArn: taskPolicy,
	})
	t.Cleanup(func() { iamTempCreds.Delete(akid) })
}

func iamLockoutRequest(akid, service, body string) *http.Request {
	return iamProbeSignedRequest(akid, service, "", body)
}

// TestIAMRootUserManagesItsBucketPolicyDespiteItsDeny holds a bucket whose
// policy denies everyone: the root user of the account that owns it still
// reads, replaces and deletes the policy, and nothing else; another account's
// root user does none of it.
func TestIAMRootUserManagesItsBucketPolicyDespiteItsDeny(t *testing.T) {
	srv := buildResourcePolicySim(t)
	if rr := doReq(t, srv, s3SeedReq("PUT", "/lockout-bucket", nil)); rr.Code != http.StatusOK {
		t.Fatalf("CreateBucket: status %d body %s", rr.Code, rr.Body.String())
	}
	iamPutResourcePolicy(s3BucketARN("lockout-bucket"), iamDenyEveryoneS3)

	const owner, member = "ASIAOWNERROOTLOCKOUT", "ASIAMEMBERROOTLOCKOUT"
	iamSeedRootSession(t, owner, awsAccountID(), "arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy")
	iamSeedRootSession(t, member, "210987654321", "arn:aws:iam::aws:policy/root-task/S3UnlockBucketPolicy")

	for _, action := range []string{"s3:GetBucketPolicy", "s3:PutBucketPolicy", "s3:DeleteBucketPolicy"} {
		if allowed, _, _ := iamAuthorize(iamLockoutRequest(owner, "s3", ""), action, s3BucketARN("lockout-bucket")); !allowed {
			t.Errorf("the owning account's root user is refused %s on a bucket whose policy denies everyone", action)
		}
		if allowed, _, _ := iamAuthorize(iamLockoutRequest(member, "s3", ""), action, s3BucketARN("lockout-bucket")); allowed {
			t.Errorf("another account's root user is allowed %s on the bucket", action)
		}
	}
	if allowed, _, _ := iamAuthorize(iamLockoutRequest(owner, "s3", ""), "s3:GetObject", s3BucketARN("lockout-bucket")+"/key"); allowed {
		t.Error("the policy's deny still holds for the owning account's root user beyond managing the policy")
	}
}

// TestIAMRootUserDeletesItsQueuePolicyDespiteItsDeny holds a queue whose
// policy denies everyone: the root user of the account that owns it reads
// the queue's attributes and deletes the policy, but sets no other
// attribute.
func TestIAMRootUserDeletesItsQueuePolicyDespiteItsDeny(t *testing.T) {
	buildResourcePolicySim(t)
	queueArn := sqsQueueARN("lockout-queue")
	iamPutResourcePolicy(queueArn, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"`+queueArn+`"}]}`)
	const owner = "ASIAOWNERROOTQUEUE"
	iamSeedRootSession(t, owner, awsAccountID(), "arn:aws:iam::aws:policy/root-task/SQSUnlockQueuePolicy")

	cases := []struct {
		action, body string
		allowed      bool
	}{
		{"sqs:GetQueueAttributes", `{"QueueUrl":"q","AttributeNames":["Policy"]}`, true},
		{"sqs:SetQueueAttributes", `{"QueueUrl":"q","Attributes":{"Policy":""}}`, true},
		{"sqs:SetQueueAttributes", `{"QueueUrl":"q","Attributes":{"Policy":"{}"}}`, false},
		{"sqs:SetQueueAttributes", `{"QueueUrl":"q","Attributes":{"Policy":"","VisibilityTimeout":"10"}}`, false},
		{"sqs:SendMessage", `{"QueueUrl":"q","MessageBody":"x"}`, false},
	}
	for _, c := range cases {
		allowed, _, _ := iamAuthorize(iamLockoutRequest(owner, "sqs", c.body), c.action, queueArn)
		if allowed != c.allowed {
			t.Errorf("%s %s: allowed = %v, want %v", c.action, c.body, allowed, c.allowed)
		}
	}
}
