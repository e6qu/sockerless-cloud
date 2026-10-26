package aws_sdk_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/amplify"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restAuthRoute is one management operation of a REST-protocol service, sent
// raw: a forged request cannot be built through an SDK, which signs whatever
// it sends.
type restAuthRoute struct {
	name, method, path, signingService string
	body                               []byte
	restXML                            bool
}

var restAuthRoutes = []restAuthRoute{
	{name: "Amplify_ListApps", method: http.MethodGet, path: "/apps", signingService: "amplify"},
	{name: "APIGateway_GetRestApis", method: http.MethodGet, path: "/restapis", signingService: "apigateway"},
	{name: "APIGatewayV2_GetApis", method: http.MethodGet, path: "/v2/apis", signingService: "apigateway"},
	{name: "Batch_DescribeJobQueues", method: http.MethodPost, path: "/v1/describejobqueues", signingService: "batch", body: []byte("{}")},
	{name: "EFS_DescribeFileSystems", method: http.MethodGet, path: "/2015-02-01/file-systems", signingService: "elasticfilesystem"},
	{name: "Scheduler_ListSchedules", method: http.MethodGet, path: "/schedules", signingService: "scheduler"},
	{name: "CloudFront_ListDistributions", method: http.MethodGet, path: "/2020-05-31/distribution", signingService: "cloudfront", restXML: true},
	{name: "Route53_ListHostedZones", method: http.MethodGet, path: "/2013-04-01/hostedzone", signingService: "route53", restXML: true},
}

// restAuthErrorCode reads the error code a refusal carries: the
// X-Amzn-Errortype header of a REST-JSON service, the <Code> of a REST-XML
// one.
func restAuthErrorCode(t *testing.T, route restAuthRoute, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if !route.restXML {
		return resp.Header.Get("X-Amzn-Errortype")
	}
	var envelope struct {
		Code string `xml:"Error>Code"`
	}
	require.NoError(t, xml.Unmarshal(body, &envelope), "REST-XML error body: %s", body)
	return envelope.Code
}

func restAuthSend(t *testing.T, route restAuthRoute, prepare func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, route.method, baseURL+route.path, bytes.NewReader(route.body))
	require.NoError(t, err)
	if route.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	prepare(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestREST_ManagementCallsRequireASignature sends each REST service's list
// operation unsigned, with a forged signature for a registered access key,
// with a well-formed signature under the wrong secret, under an access key no
// one holds, and as a forged presigned URL, and holds each to the refusal AWS's
// front end answers with. The same request signed with the account's key runs.
func TestREST_ManagementCallsRequireASignature(t *testing.T) {
	t.Parallel()
	for _, route := range restAuthRoutes {
		t.Run(route.name, func(t *testing.T) {
			t.Parallel()
			missing, mismatch, unknown := "MissingAuthenticationTokenException", "InvalidSignatureException", "UnrecognizedClientException"
			if route.restXML {
				missing, mismatch, unknown = "MissingAuthenticationToken", "SignatureDoesNotMatch", "InvalidClientTokenId"
			}

			resp := restAuthSend(t, route, func(*http.Request) {})
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "an unsigned request is refused")
			assert.Equal(t, missing, restAuthErrorCode(t, route, resp))

			resp = restAuthSend(t, route, func(req *http.Request) {
				req.Header.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
				req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/"+time.Now().UTC().Format("20060102")+
					"/us-east-1/"+route.signingService+"/aws4_request, SignedHeaders=host;x-amz-date, Signature=deadbeef")
			})
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a forged signature is refused")
			assert.Equal(t, mismatch, restAuthErrorCode(t, route, resp))

			resp = restAuthSend(t, route, func(req *http.Request) {
				signRawSigV4Creds(t, req, route.signingService, sha256Hex(route.body), "test", "not-the-real-secret")
			})
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a signature under the wrong secret is refused")
			assert.Equal(t, mismatch, restAuthErrorCode(t, route, resp))

			resp = restAuthSend(t, route, func(req *http.Request) {
				signRawSigV4Creds(t, req, route.signingService, sha256Hex(route.body), "AKIANOSUCHKEY0000000", "secret")
			})
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "an unknown access key is refused")
			assert.Equal(t, unknown, restAuthErrorCode(t, route, resp))

			resp = restAuthSend(t, route, func(req *http.Request) {
				q := req.URL.Query()
				q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
				q.Set("X-Amz-Credential", "test/"+time.Now().UTC().Format("20060102")+"/us-east-1/"+route.signingService+"/aws4_request")
				q.Set("X-Amz-Date", time.Now().UTC().Format("20060102T150405Z"))
				q.Set("X-Amz-SignedHeaders", "host")
				q.Set("X-Amz-Signature", "deadbeef")
				req.URL.RawQuery = q.Encode()
			})
			assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a forged presigned URL is refused")
			assert.Equal(t, mismatch, restAuthErrorCode(t, route, resp))

			resp = restAuthSend(t, route, func(req *http.Request) {
				signRawSigV4(t, req, route.signingService, sha256Hex(route.body))
			})
			body, _ := io.ReadAll(resp.Body)
			assert.Equal(t, http.StatusOK, resp.StatusCode, "a signed request runs: %s", body)
		})
	}
}

// TestREST_PresignedManagementCall holds the query form of SigV4 to the same
// verification as the header form: a URL the SDK's signer presigns runs until
// the X-Amz-Expires it was signed for runs out.
func TestREST_PresignedManagementCall(t *testing.T) {
	t.Parallel()
	presign := func(at time.Time) string {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/2013-04-01/hostedzone?X-Amz-Expires=300", nil)
		require.NoError(t, err)
		signed, _, err := v4.NewSigner().PresignHTTP(ctx, aws.Credentials{AccessKeyID: "test", SecretAccessKey: "test"},
			req, emptySHA256, "route53", "us-east-1", at)
		require.NoError(t, err)
		return signed
	}
	get := func(target string) (int, string) {
		resp, err := http.Get(target) //nolint:noctx // the presigned URL carries its own authentication
		require.NoError(t, err)
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	status, body := get(presign(time.Now()))
	assert.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "ListHostedZonesResponse")

	status, body = get(presign(time.Now().Add(-time.Hour)))
	assert.Equal(t, http.StatusForbidden, status, "a presigned URL past its expiry is refused: %s", body)

	tampered, err := url.Parse(presign(time.Now()))
	require.NoError(t, err)
	q := tampered.Query()
	q.Set("maxitems", "1")
	tampered.RawQuery = q.Encode()
	status, body = get(tampered.String())
	assert.Equal(t, http.StatusForbidden, status, "a presigned URL whose query changed after signing is refused: %s", body)
	assert.Contains(t, body, "SignatureDoesNotMatch")
}

// TestREST_SDKWithWrongSecretIsRefused drives each service's own SDK client
// with the account's access key and a wrong secret, and holds the error the
// SDK surfaces to the code AWS answers with; the same client with the right
// secret succeeds.
func TestREST_SDKWithWrongSecretIsRefused(t *testing.T) {
	t.Parallel()
	wrong := sdkConfig()
	wrong.Credentials = credentials.NewStaticCredentialsProvider("test", "not-the-real-secret", "")
	calls := []struct {
		name string
		code string
		run  func(aws.Config) error
	}{
		{"Amplify", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := amplify.NewFromConfig(cfg, func(o *amplify.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListApps(ctx, &amplify.ListAppsInput{})
			return err
		}},
		{"APIGateway", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := apigateway.NewFromConfig(cfg, func(o *apigateway.Options) { o.BaseEndpoint = aws.String(baseURL) }).GetRestApis(ctx, &apigateway.GetRestApisInput{})
			return err
		}},
		{"APIGatewayV2", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := apigatewayv2.NewFromConfig(cfg, func(o *apigatewayv2.Options) { o.BaseEndpoint = aws.String(baseURL) }).GetApis(ctx, &apigatewayv2.GetApisInput{})
			return err
		}},
		{"Batch", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := batch.NewFromConfig(cfg, func(o *batch.Options) { o.BaseEndpoint = aws.String(baseURL) }).DescribeJobQueues(ctx, &batch.DescribeJobQueuesInput{})
			return err
		}},
		{"EFS", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := efs.NewFromConfig(cfg, func(o *efs.Options) { o.BaseEndpoint = aws.String(baseURL) }).DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{})
			return err
		}},
		{"Scheduler", "InvalidSignatureException", func(cfg aws.Config) error {
			_, err := scheduler.NewFromConfig(cfg, func(o *scheduler.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListSchedules(ctx, &scheduler.ListSchedulesInput{})
			return err
		}},
		{"CloudFront", "SignatureDoesNotMatch", func(cfg aws.Config) error {
			_, err := cloudfront.NewFromConfig(cfg, func(o *cloudfront.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListDistributions(ctx, &cloudfront.ListDistributionsInput{})
			return err
		}},
		{"Route53", "SignatureDoesNotMatch", func(cfg aws.Config) error {
			_, err := route53.NewFromConfig(cfg, func(o *route53.Options) { o.BaseEndpoint = aws.String(baseURL) }).ListHostedZones(ctx, &route53.ListHostedZonesInput{})
			return err
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			t.Parallel()
			err := call.run(wrong)
			var apiErr smithy.APIError
			require.True(t, errors.As(err, &apiErr), "a wrong secret is refused: %v", err)
			assert.Equal(t, call.code, apiErr.ErrorCode())
			require.NoError(t, call.run(sdkConfig()), "the account's own key runs the call")
		})
	}
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
