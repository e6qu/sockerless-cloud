package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ecrJSONRequest builds the awsJson request the AWS SDK sends to Amazon Elastic
// Container Registry (ECR): the operation lives in X-Amz-Target and the region
// the resource ARN is derived from comes out of the SigV4 credential scope.
func ecrJSONRequest(op, body string) *http.Request {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AmazonEC2ContainerRegistry_V20150921."+op)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/ecr/aws4_request, SignedHeaders=host;x-amz-target, Signature=00")
	return r
}

// TestIAMResourceARNs_ECR pins the repository ARNs the enforcement gate derives
// from an ECR request. Deriving "*" instead of the repository ARN is what makes
// a repository-scoped grant unmatchable, so every repository-naming shape has
// to resolve.
func TestIAMResourceARNs_ECR(t *testing.T) {
	const prefix = "arn:aws:ecr:us-east-1:123456789012:repository/"
	cases := []struct {
		name string
		op   string
		body string
		want []string
	}{
		{
			"DescribeImages names its repository",
			"DescribeImages", `{"repositoryName":"edd-dev/control-plane"}`,
			[]string{prefix + "edd-dev/control-plane"},
		},
		{
			"ListImages names its repository",
			"ListImages", `{"repositoryName":"edd-dev/edd-base"}`,
			[]string{prefix + "edd-dev/edd-base"},
		},
		{
			"BatchGetImage names its repository",
			"BatchGetImage", `{"repositoryName":"edd-dev/ssh-gateway","imageIds":[{"imageTag":"latest"}]}`,
			[]string{prefix + "edd-dev/ssh-gateway"},
		},
		{
			"PutImage names its repository",
			"PutImage", `{"repositoryName":"edd-dev/golden/omnibus","imageManifest":"{}"}`,
			[]string{prefix + "edd-dev/golden/omnibus"},
		},
		{
			"GetDownloadUrlForLayer names its repository",
			"GetDownloadUrlForLayer", `{"repositoryName":"app","layerDigest":"sha256:00"}`,
			[]string{prefix + "app"},
		},
		{
			"InitiateLayerUpload names its repository",
			"InitiateLayerUpload", `{"repositoryName":"app"}`,
			[]string{prefix + "app"},
		},
		{
			"CreateRepository names the repository it creates",
			"CreateRepository", `{"repositoryName":"edd-dev/new"}`,
			[]string{prefix + "edd-dev/new"},
		},
		{
			"BatchDeleteImage names its repository",
			"BatchDeleteImage", `{"repositoryName":"app","imageIds":[{"imageDigest":"sha256:00"}]}`,
			[]string{prefix + "app"},
		},
		{
			"SetRepositoryPolicy names its repository",
			"SetRepositoryPolicy", `{"repositoryName":"app","policyText":"{}"}`,
			[]string{prefix + "app"},
		},
		{
			"DescribeRepositories authorizes every filtered repository",
			"DescribeRepositories", `{"repositoryNames":["edd-dev/control-plane","edd-dev/ssh-gateway"]}`,
			[]string{prefix + "edd-dev/control-plane", prefix + "edd-dev/ssh-gateway"},
		},
		{
			"tagging operations carry the repository ARN itself",
			"ListTagsForResource", `{"resourceArn":"` + prefix + `edd-dev/control-plane"}`,
			[]string{prefix + "edd-dev/control-plane"},
		},
		{
			"unfiltered DescribeRepositories targets the whole registry",
			"DescribeRepositories", `{}`,
			[]string{"*"},
		},
		{
			"GetAuthorizationToken targets the whole registry",
			"GetAuthorizationToken", `{}`,
			[]string{"*"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ecrJSONRequest(tc.op, tc.body)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatalf("%s: request not classified as an IAM action", tc.op)
			}
			if want := "ecr:" + tc.op; action != want {
				t.Fatalf("action = %q, want %q", action, want)
			}
			got := iamResourceARNsForRequest(r, action)
			if len(got) != len(tc.want) {
				t.Fatalf("resources = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("resources = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestIAMEnforce_ECRRepositoryScopedGrant evaluates a repository-scoped ECR
// policy end to end — request → derived resource ARN → policy decision. IAM's
// `*` spans `/`, so a prefix grant covers a nested repository name; a
// repository outside the prefix stays denied.
func TestIAMEnforce_ECRRepositoryScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Sid":"ImageConsoleEcrRead",
		"Effect":"Allow",
		"Action":["ecr:ListImages","ecr:DescribeImages","ecr:BatchGetImage"],
		"Resource":"arn:aws:ecr:us-east-1:123456789012:repository/edd-dev/*"}]}`)
	cases := []struct {
		name string
		op   string
		repo string
		want string
	}{
		{"repository under the granted prefix", "DescribeImages", "edd-dev/control-plane", "allowed"},
		{"nested repository under the granted prefix", "DescribeImages", "edd-dev/golden/omnibus", "allowed"},
		{"ListImages under the granted prefix", "ListImages", "edd-dev/edd-base", "allowed"},
		{"repository outside the granted prefix", "DescribeImages", "other/control-plane", "implicitDeny"},
		{"the prefix is not a bare substring match", "DescribeImages", "edd-dev-staging/app", "implicitDeny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ecrJSONRequest(tc.op, `{"repositoryName":"`+tc.repo+`"}`)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatalf("%s: request not classified as an IAM action", tc.op)
			}
			for _, resource := range iamResourceARNsForRequest(r, action) {
				got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resource, nil)
				if got != tc.want {
					t.Fatalf("%s on %s (resource %s) = %s, want %s", action, tc.repo, resource, got, tc.want)
				}
			}
		})
	}
}

// secretsManagerJSONRequest builds the awsJson request the AWS SDK sends to AWS
// Secrets Manager: the operation lives in X-Amz-Target and the region the
// resource ARN is derived from comes out of the SigV4 credential scope.
func secretsManagerJSONRequest(op, body string) *http.Request {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "secretsmanager."+op)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=ASIAEXAMPLECREDENTIAL/20260801/us-east-1/secretsmanager/aws4_request, SignedHeaders=host;x-amz-target, Signature=00")
	return r
}

// TestIAMResourceARNs_SecretsManager pins the secret ARNs the enforcement gate
// derives. CreateSecret names the secret it is about to create in Name rather
// than SecretId; deriving "*" from it is what makes a grant scoped to a name
// prefix unmatchable.
func TestIAMResourceARNs_SecretsManager(t *testing.T) {
	const prefix = "arn:aws:secretsmanager:us-east-1:123456789012:secret:"
	cases := []struct {
		name string
		op   string
		body string
		want []string
	}{
		{
			"CreateSecret names its secret in Name",
			"CreateSecret", `{"Name":"edd/workspace/ws-abc/agent","SecretString":"x"}`,
			[]string{prefix + "edd/workspace/ws-abc/agent"},
		},
		{
			"GetSecretValue names its secret in SecretId",
			"GetSecretValue", `{"SecretId":"edd-dev/AUTH_SECRET"}`,
			[]string{prefix + "edd-dev/AUTH_SECRET"},
		},
		{
			"PutSecretValue names its secret in SecretId",
			"PutSecretValue", `{"SecretId":"edd/workspace/ws-abc/agent","SecretString":"x"}`,
			[]string{prefix + "edd/workspace/ws-abc/agent"},
		},
		{
			"a SecretId that is already an ARN is used as it stands",
			"DescribeSecret", `{"SecretId":"` + prefix + `edd/workspace/ws-abc/agent-AbCdEf"}`,
			[]string{prefix + "edd/workspace/ws-abc/agent-AbCdEf"},
		},
		{
			"ListSecrets targets every secret",
			"ListSecrets", `{}`,
			[]string{"*"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := secretsManagerJSONRequest(tc.op, tc.body)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatalf("%s: request not classified as an IAM action", tc.op)
			}
			if want := "secretsmanager:" + tc.op; action != want {
				t.Fatalf("action = %q, want %q", action, want)
			}
			got := iamResourceARNsForRequest(r, action)
			if len(got) != len(tc.want) {
				t.Fatalf("resources = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("resources = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestIAMEnforce_SecretsManagerPrefixScopedGrant evaluates the exact grant an
// application holds to stash per-workspace tokens — request → derived resource
// ARN → policy decision. Before the Name derivation existed, CreateSecret
// resolved to "*" and this grant was denied, which failed every workspace
// launch while reporting a credential error rather than an authorization one.
func TestIAMEnforce_SecretsManagerPrefixScopedGrant(t *testing.T) {
	doc := mustDoc(t, `{"Version":"2012-10-17","Statement":[{
		"Sid":"ManageWorkspaceAgentSecrets",
		"Effect":"Allow",
		"Action":["secretsmanager:CreateSecret","secretsmanager:PutSecretValue","secretsmanager:DeleteSecret"],
		"Resource":"arn:aws:secretsmanager:us-east-1:123456789012:secret:edd/workspace/*"}]}`)
	cases := []struct {
		name string
		op   string
		body string
		want string
	}{
		{"CreateSecret inside the granted prefix", "CreateSecret", `{"Name":"edd/workspace/ws-abc/agent","SecretString":"x"}`, "allowed"},
		{"nested name inside the granted prefix", "CreateSecret", `{"Name":"edd/workspace/ws-abc/connection/token","SecretString":"x"}`, "allowed"},
		{"PutSecretValue inside the granted prefix", "PutSecretValue", `{"SecretId":"edd/workspace/ws-abc/agent","SecretString":"x"}`, "allowed"},
		{"CreateSecret outside the granted prefix", "CreateSecret", `{"Name":"other/ws-abc/agent","SecretString":"x"}`, "implicitDeny"},
		{"the prefix is not a bare substring match", "CreateSecret", `{"Name":"edd/workspace-staging/agent","SecretString":"x"}`, "implicitDeny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := secretsManagerJSONRequest(tc.op, tc.body)
			action, ok := iamActionForRequest(r)
			if !ok {
				t.Fatalf("%s: request not classified as an IAM action", tc.op)
			}
			for _, resource := range iamResourceARNsForRequest(r, action) {
				got, _ := iamEvalDecision([]iamPolicyDoc{doc}, action, resource, nil)
				if got != tc.want {
					t.Fatalf("%s (resource %s) = %s, want %s", action, resource, got, tc.want)
				}
			}
		})
	}
}
