package aws_cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
)

// restAuthCLICalls are list operations of the REST-protocol services, with the
// code AWS's front end answers an unsigned request and a wrong secret with.
var restAuthCLICalls = []struct {
	name             string
	args             []string
	missing, invalid string
}{
	{"Amplify", []string{"amplify", "list-apps"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"APIGateway", []string{"apigateway", "get-rest-apis"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"APIGatewayV2", []string{"apigatewayv2", "get-apis"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"Batch", []string{"batch", "describe-job-queues"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"EFS", []string{"efs", "describe-file-systems"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"Scheduler", []string{"scheduler", "list-schedules"}, "MissingAuthenticationTokenException", "InvalidSignatureException"},
	{"CloudFront", []string{"cloudfront", "list-distributions"}, "MissingAuthenticationToken", "SignatureDoesNotMatch"},
	{"Route53", []string{"route53", "list-hosted-zones"}, "MissingAuthenticationToken", "SignatureDoesNotMatch"},
}

// TestIAM_RESTCallsRefuseUnsignedAndWrongSecretCLI runs each call in the CLI's
// anonymous mode, --no-sign-request, and with a wrong AWS_SECRET_ACCESS_KEY,
// and holds each to its refusal; the account's own key runs it.
func TestIAM_RESTCallsRefuseUnsignedAndWrongSecretCLI(t *testing.T) {
	t.Parallel()
	for _, call := range restAuthCLICalls {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			unsigned := runCLIExpectError(t, awsCLI(append(append([]string{}, call.args...), "--no-sign-request")...))
			if !strings.Contains(unsigned, call.missing) {
				t.Fatalf("an unsigned %s expected %s; got: %s", strings.Join(call.args, " "), call.missing, unsigned)
			}
			wrong := awsCLI(call.args...)
			wrong.Env = withCreds(wrong.Env, "test", "not-the-real-secret")
			refused := runCLIExpectError(t, wrong)
			if !strings.Contains(refused, call.invalid) {
				t.Fatalf("%s under a wrong secret expected %s; got: %s", strings.Join(call.args, " "), call.invalid, refused)
			}
			runCLI(t, awsCLI(call.args...))
		})
	}
}

// TestS3_CLIAnonymousAccessNeedsABucketGrant holds `aws --no-sign-request` to
// what the bucket grants everyone: nothing until a bucket policy grants
// Principal "*" s3:GetObject, and then that read alone.
func TestS3_CLIAnonymousAccessNeedsABucketGrant(t *testing.T) {
	t.Parallel()
	bucket := uniquename.New("cli-anonymous")
	created := runCLIExpectError(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--no-sign-request"))
	if !strings.Contains(created, "AccessDenied") {
		t.Fatalf("an anonymous create-bucket expected AccessDenied; got: %s", created)
	}
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() {
		_ = awsCLI("s3", "rb", "s3://"+bucket, "--force").Run()
	})

	dir := t.TempDir()
	source := filepath.Join(dir, "index.html")
	if err := os.WriteFile(source, []byte("<h1>public</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	put := runCLIExpectError(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "index.html", "--body", source, "--no-sign-request"))
	if !strings.Contains(put, "AccessDenied") {
		t.Fatalf("an anonymous put-object expected AccessDenied; got: %s", put)
	}
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "index.html", "--body", source))

	download := filepath.Join(dir, "download.html")
	get := runCLIExpectError(t, awsCLI("s3api", "get-object", "--bucket", bucket, "--key", "index.html", download, "--no-sign-request"))
	if !strings.Contains(get, "AccessDenied") {
		t.Fatalf("an anonymous get-object without a grant expected AccessDenied; got: %s", get)
	}
	listed := runCLIExpectError(t, awsCLI("s3", "ls", "--no-sign-request"))
	if !strings.Contains(listed, "AccessDenied") {
		t.Fatalf("an anonymous ListBuckets expected AccessDenied; got: %s", listed)
	}

	runCLI(t, awsCLI("s3api", "put-bucket-policy", "--bucket", bucket, "--policy",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::`+bucket+`/*"}]}`))
	runCLI(t, awsCLI("s3api", "get-object", "--bucket", bucket, "--key", "index.html", download, "--no-sign-request"))
	body, err := os.ReadFile(download)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<h1>public</h1>" {
		t.Fatalf("the anonymous read returned %q", body)
	}
	put = runCLIExpectError(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "index.html", "--body", source, "--no-sign-request"))
	if !strings.Contains(put, "AccessDenied") {
		t.Fatalf("an anonymous put-object the policy does not grant expected AccessDenied; got: %s", put)
	}

	wrong := awsCLI("s3api", "list-objects-v2", "--bucket", bucket)
	wrong.Env = withCreds(wrong.Env, "test", "not-the-real-secret")
	if refused := runCLIExpectError(t, wrong); !strings.Contains(refused, "SignatureDoesNotMatch") {
		t.Fatalf("list-objects-v2 under a wrong secret expected SignatureDoesNotMatch; got: %s", refused)
	}
}
