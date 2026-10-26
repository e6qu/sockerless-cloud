package aws_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	smithy "github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_UnsignedAndWrongSecretRejected expects Lambda's REST control plane
// to answer an unsigned GET /2015-03-31/functions with
// MissingAuthenticationTokenException and one signed with a wrong secret with
// SignatureDoesNotMatch, both in restJson1's error shape (x-amzn-Errortype and
// a JSON body), while a correctly signed call succeeds.
func TestLambda_UnsignedAndWrongSecretRejected(t *testing.T) {
	t.Parallel()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/2015-03-31/functions", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "unsigned ListFunctions must be rejected")
	assert.Equal(t, "MissingAuthenticationTokenException", resp.Header.Get("X-Amzn-Errortype"))

	// A well-formed signature with the wrong secret for the real "test" key id.
	req2, err := http.NewRequest(http.MethodGet, baseURL+"/2015-03-31/functions", nil)
	require.NoError(t, err)
	signRawSigV4Creds(t, req2, "lambda", emptySHA256, "test", "not-the-real-secret")
	resp2, err := http.DefaultClient.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp2.StatusCode, "wrong-secret ListFunctions must be rejected")
	assert.Equal(t, "SignatureDoesNotMatch", resp2.Header.Get("X-Amzn-Errortype"))

	req3, err := http.NewRequest(http.MethodGet, baseURL+"/2015-03-31/functions", nil)
	require.NoError(t, err)
	signRawSigV4(t, req3, "lambda", emptySHA256)
	resp3, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	defer resp3.Body.Close()
	assert.Equal(t, http.StatusOK, resp3.StatusCode, "correctly signed ListFunctions must succeed")
}

// TestLambda_CallTimeIAMEnforcement expects a restricted principal to be denied
// a lambda: action its policy does not grant and allowed the one it does, as
// iam_enforcement_test.go shows for EC2 and ECS.
func TestLambda_CallTimeIAMEnforcement(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	user := uniquename.New("lambda-restricted-user")

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:   aws.String(user),
		PolicyName: aws.String("lambda-least-priv"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Action":"lambda:ListFunctions","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(key.AccessKey.AccessKeyId)
	secret := aws.ToString(key.AccessKey.SecretAccessKey)
	require.NotEmpty(t, akid)
	require.NotEmpty(t, secret)

	restricted := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
	}
	lr := lambda.NewFromConfig(restricted, func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })

	errCode := func(err error) string {
		var ae smithy.APIError
		if errors.As(err, &ae) {
			return ae.ErrorCode()
		}
		return ""
	}

	_, err = lr.ListFunctions(ctx, &lambda.ListFunctionsInput{})
	assert.NoError(t, err, "lambda:ListFunctions is granted and must succeed")

	_, err = lr.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String("should-be-denied"),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Handler:      aws.String("index.handler"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.Error(t, err, "lambda:CreateFunction is not granted and must be denied")
	assert.Equal(t, "AccessDeniedException", errCode(err))
}

// lambdaControlPlaneRoute is one row of the route table below: path is the
// route template lambdaEnforced or lambdaEnforcedDynamic guards (the
// check-simulator-tests route token, e.g. "/2015-03-31/functions/{name}"), and
// url an instance of it with harmless nonexistent names.
type lambdaControlPlaneRoute struct {
	method string
	path   string
	url    string
}

// lambdaControlPlaneRoutes lists every route lambdaEnforced and
// lambdaEnforcedDynamic guard in lambda.go, lambda_extras2.go and
// lambda_extras3.go: the whole Lambda REST control plane except the Runtime API
// (/2018-06-01/runtime/...), which a per-invocation mux serves to the function
// container without SigV4 (see lambda_enforcement.go). A route added there
// needs a row here.
var lambdaControlPlaneRoutes = []lambdaControlPlaneRoute{
	{http.MethodDelete, "/2015-03-31/event-source-mappings/{uuid}", "/2015-03-31/event-source-mappings/11111111-1111-1111-1111-111111111111"},
	{http.MethodDelete, "/2015-03-31/functions/{name}", "/2015-03-31/functions/enforcement-test-fn"},
	{http.MethodDelete, "/2015-03-31/functions/{name}/aliases/{alias}", "/2015-03-31/functions/enforcement-test-fn/aliases/enforcement-test-alias"},
	{http.MethodDelete, "/2015-03-31/functions/{name}/policy/{statement}", "/2015-03-31/functions/enforcement-test-fn/policy/enforcement-test-stmt"},
	{http.MethodDelete, "/2017-03-31/tags/{arn...}", "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodDelete, "/2017-10-31/functions/{name}/concurrency", "/2017-10-31/functions/enforcement-test-fn/concurrency"},
	{http.MethodDelete, "/2018-10-31/layers/{layer}/versions/{version}", "/2018-10-31/layers/enforcement-test-layer/versions/1"},
	{http.MethodDelete, "/2018-10-31/layers/{layer}/versions/{version}/policy/{statement}", "/2018-10-31/layers/enforcement-test-layer/versions/1/policy/enforcement-test-stmt"},
	{http.MethodDelete, "/2019-09-25/functions/{name}/event-invoke-config", "/2019-09-25/functions/enforcement-test-fn/event-invoke-config"},
	{http.MethodDelete, "/2019-09-30/functions/{name}/provisioned-concurrency", "/2019-09-30/functions/enforcement-test-fn/provisioned-concurrency"},
	{http.MethodDelete, "/2020-04-22/code-signing-configs/{arn...}", "/2020-04-22/code-signing-configs/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodDelete, "/2020-06-30/functions/{name}/code-signing-config", "/2020-06-30/functions/enforcement-test-fn/code-signing-config"},
	{http.MethodDelete, "/2021-10-31/functions/{name}/url", "/2021-10-31/functions/enforcement-test-fn/url"},
	{http.MethodDelete, "/2025-11-30/capacity-providers/{cpname}", "/2025-11-30/capacity-providers/enforcement-test-cp"},
	{http.MethodGet, "/2015-03-31/event-source-mappings", "/2015-03-31/event-source-mappings"},
	{http.MethodGet, "/2015-03-31/event-source-mappings/{uuid}", "/2015-03-31/event-source-mappings/11111111-1111-1111-1111-111111111111"},
	{http.MethodGet, "/2015-03-31/functions", "/2015-03-31/functions"},
	{http.MethodGet, "/2015-03-31/functions/", "/2015-03-31/functions/"},
	{http.MethodGet, "/2015-03-31/functions/{name}", "/2015-03-31/functions/enforcement-test-fn"},
	{http.MethodGet, "/2015-03-31/functions/{name}/aliases", "/2015-03-31/functions/enforcement-test-fn/aliases"},
	{http.MethodGet, "/2015-03-31/functions/{name}/aliases/{alias}", "/2015-03-31/functions/enforcement-test-fn/aliases/enforcement-test-alias"},
	{http.MethodGet, "/2015-03-31/functions/{name}/configuration", "/2015-03-31/functions/enforcement-test-fn/configuration"},
	{http.MethodGet, "/2015-03-31/functions/{name}/policy", "/2015-03-31/functions/enforcement-test-fn/policy"},
	{http.MethodGet, "/2015-03-31/functions/{name}/versions", "/2015-03-31/functions/enforcement-test-fn/versions"},
	{http.MethodGet, "/2016-08-19/account-settings", "/2016-08-19/account-settings"},
	{http.MethodGet, "/2016-08-19/account-settings/", "/2016-08-19/account-settings/"},
	{http.MethodGet, "/2017-03-31/tags/{arn...}", "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodGet, "/2018-10-31/layers", "/2018-10-31/layers"},
	{http.MethodGet, "/2018-10-31/layers/{layer}/versions", "/2018-10-31/layers/enforcement-test-layer/versions"},
	{http.MethodGet, "/2018-10-31/layers/{layer}/versions/{version}", "/2018-10-31/layers/enforcement-test-layer/versions/1"},
	{http.MethodGet, "/2018-10-31/layers/{layer}/versions/{version}/policy", "/2018-10-31/layers/enforcement-test-layer/versions/1/policy"},
	{http.MethodGet, "/2019-09-25/functions/{name}/event-invoke-config", "/2019-09-25/functions/enforcement-test-fn/event-invoke-config"},
	{http.MethodGet, "/2019-09-25/functions/{name}/event-invoke-config/list", "/2019-09-25/functions/enforcement-test-fn/event-invoke-config/list"},
	{http.MethodGet, "/2019-09-30/functions/{name}/concurrency", "/2019-09-30/functions/enforcement-test-fn/concurrency"},
	{http.MethodGet, "/2019-09-30/functions/{name}/provisioned-concurrency", "/2019-09-30/functions/enforcement-test-fn/provisioned-concurrency"},
	{http.MethodGet, "/2020-04-22/code-signing-configs", "/2020-04-22/code-signing-configs"},
	{http.MethodGet, "/2020-04-22/code-signing-configs/{arn...}", "/2020-04-22/code-signing-configs/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodGet, "/2020-04-22/code-signing-configs/{arn}/functions", "/2020-04-22/code-signing-configs/arn:aws:lambda:us-east-1:000000000000:test-resource/functions"},
	{http.MethodGet, "/2020-06-30/functions/{name}/code-signing-config", "/2020-06-30/functions/enforcement-test-fn/code-signing-config"},
	{http.MethodGet, "/2021-07-20/functions/{name}/runtime-management-config", "/2021-07-20/functions/enforcement-test-fn/runtime-management-config"},
	{http.MethodGet, "/2021-10-31/functions/{name}/url", "/2021-10-31/functions/enforcement-test-fn/url"},
	{http.MethodGet, "/2021-10-31/functions/{name}/urls", "/2021-10-31/functions/enforcement-test-fn/urls"},
	{http.MethodGet, "/2024-08-31/functions/{name}/recursion-config", "/2024-08-31/functions/enforcement-test-fn/recursion-config"},
	{http.MethodGet, "/2025-11-30/capacity-providers", "/2025-11-30/capacity-providers"},
	{http.MethodGet, "/2025-11-30/capacity-providers/{cpname}", "/2025-11-30/capacity-providers/enforcement-test-cp"},
	{http.MethodGet, "/2025-11-30/capacity-providers/{cpname}/function-versions", "/2025-11-30/capacity-providers/enforcement-test-cp/function-versions"},
	{http.MethodGet, "/2025-11-30/functions/{name}/function-scaling-config", "/2025-11-30/functions/enforcement-test-fn/function-scaling-config"},
	{http.MethodGet, "/2025-12-01/durable-executions/{arn}", "/2025-12-01/durable-executions/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodGet, "/2025-12-01/durable-executions/{arn}/history", "/2025-12-01/durable-executions/arn:aws:lambda:us-east-1:000000000000:test-resource/history"},
	{http.MethodGet, "/2025-12-01/durable-executions/{arn}/state", "/2025-12-01/durable-executions/arn:aws:lambda:us-east-1:000000000000:test-resource/state"},
	{http.MethodGet, "/2025-12-01/functions/{name}/durable-executions", "/2025-12-01/functions/enforcement-test-fn/durable-executions"},
	{http.MethodPost, "/2014-11-13/functions/{name}/invoke-async", "/2014-11-13/functions/enforcement-test-fn/invoke-async"},
	{http.MethodPost, "/2015-03-31/event-source-mappings", "/2015-03-31/event-source-mappings"},
	{http.MethodPost, "/2015-03-31/functions", "/2015-03-31/functions"},
	{http.MethodPost, "/2015-03-31/functions/{name}/aliases", "/2015-03-31/functions/enforcement-test-fn/aliases"},
	{http.MethodPost, "/2015-03-31/functions/{name}/invocations", "/2015-03-31/functions/enforcement-test-fn/invocations"},
	{http.MethodPost, "/2015-03-31/functions/{name}/policy", "/2015-03-31/functions/enforcement-test-fn/policy"},
	{http.MethodPost, "/2015-03-31/functions/{name}/versions", "/2015-03-31/functions/enforcement-test-fn/versions"},
	{http.MethodPost, "/2017-03-31/tags/{arn...}", "/2017-03-31/tags/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodPost, "/2018-10-31/layers/{layer}/versions", "/2018-10-31/layers/enforcement-test-layer/versions"},
	{http.MethodPost, "/2018-10-31/layers/{layer}/versions/{version}/policy", "/2018-10-31/layers/enforcement-test-layer/versions/1/policy"},
	{http.MethodPost, "/2019-09-25/functions/{name}/event-invoke-config", "/2019-09-25/functions/enforcement-test-fn/event-invoke-config"},
	{http.MethodPost, "/2020-04-22/code-signing-configs", "/2020-04-22/code-signing-configs"},
	{http.MethodPost, "/2021-10-31/functions/{name}/url", "/2021-10-31/functions/enforcement-test-fn/url"},
	{http.MethodPost, "/2021-11-15/functions/{name}/response-streaming-invocations", "/2021-11-15/functions/enforcement-test-fn/response-streaming-invocations"},
	{http.MethodPost, "/2025-11-30/capacity-providers", "/2025-11-30/capacity-providers"},
	{http.MethodPost, "/2025-12-01/durable-execution-callbacks/{cbid}/fail", "/2025-12-01/durable-execution-callbacks/enforcement-test-cb/fail"},
	{http.MethodPost, "/2025-12-01/durable-execution-callbacks/{cbid}/heartbeat", "/2025-12-01/durable-execution-callbacks/enforcement-test-cb/heartbeat"},
	{http.MethodPost, "/2025-12-01/durable-execution-callbacks/{cbid}/succeed", "/2025-12-01/durable-execution-callbacks/enforcement-test-cb/succeed"},
	{http.MethodPost, "/2025-12-01/durable-executions/{arn}/checkpoint", "/2025-12-01/durable-executions/arn:aws:lambda:us-east-1:000000000000:test-resource/checkpoint"},
	{http.MethodPost, "/2025-12-01/durable-executions/{arn}/stop", "/2025-12-01/durable-executions/arn:aws:lambda:us-east-1:000000000000:test-resource/stop"},
	{http.MethodPut, "/2015-03-31/event-source-mappings/{uuid}", "/2015-03-31/event-source-mappings/11111111-1111-1111-1111-111111111111"},
	{http.MethodPut, "/2015-03-31/functions/{name}/aliases/{alias}", "/2015-03-31/functions/enforcement-test-fn/aliases/enforcement-test-alias"},
	{http.MethodPut, "/2015-03-31/functions/{name}/code", "/2015-03-31/functions/enforcement-test-fn/code"},
	{http.MethodPut, "/2015-03-31/functions/{name}/configuration", "/2015-03-31/functions/enforcement-test-fn/configuration"},
	{http.MethodPut, "/2017-10-31/functions/{name}/concurrency", "/2017-10-31/functions/enforcement-test-fn/concurrency"},
	{http.MethodPut, "/2019-09-25/functions/{name}/event-invoke-config", "/2019-09-25/functions/enforcement-test-fn/event-invoke-config"},
	{http.MethodPut, "/2019-09-30/functions/{name}/provisioned-concurrency", "/2019-09-30/functions/enforcement-test-fn/provisioned-concurrency"},
	{http.MethodPut, "/2020-04-22/code-signing-configs/{arn...}", "/2020-04-22/code-signing-configs/arn:aws:lambda:us-east-1:000000000000:test-resource"},
	{http.MethodPut, "/2020-06-30/functions/{name}/code-signing-config", "/2020-06-30/functions/enforcement-test-fn/code-signing-config"},
	{http.MethodPut, "/2021-07-20/functions/{name}/runtime-management-config", "/2021-07-20/functions/enforcement-test-fn/runtime-management-config"},
	{http.MethodPut, "/2021-10-31/functions/{name}/url", "/2021-10-31/functions/enforcement-test-fn/url"},
	{http.MethodPut, "/2024-08-31/functions/{name}/recursion-config", "/2024-08-31/functions/enforcement-test-fn/recursion-config"},
	{http.MethodPut, "/2025-11-30/capacity-providers/{cpname}", "/2025-11-30/capacity-providers/enforcement-test-cp"},
	{http.MethodPut, "/2025-11-30/functions/{name}/function-scaling-config", "/2025-11-30/functions/enforcement-test-fn/function-scaling-config"},
}

// TestLambda_AllControlPlaneRoutesRejectUnauthenticated expects every route in
// lambdaControlPlaneRoutes to refuse an unsigned request and one with a wrong
// secret with Lambda's REST-JSON auth error (403, X-Amzn-Errortype).
// Enforcement runs before the handler resolves its resource, so nonexistent
// names still get the auth refusal rather than a 404.
func TestLambda_AllControlPlaneRoutesRejectUnauthenticated(t *testing.T) {
	t.Parallel()
	for _, rt := range lambdaControlPlaneRoutes {
		t.Run(rt.method+"_"+rt.path, func(t *testing.T) {
			req, err := http.NewRequest(rt.method, baseURL+rt.url, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"unsigned %s %s must be rejected", rt.method, rt.url)
			assert.Equal(t, "MissingAuthenticationTokenException", resp.Header.Get("X-Amzn-Errortype"))

			// A well-formed signature with the wrong secret for the real "test" key id.
			req2, err := http.NewRequest(rt.method, baseURL+rt.url, nil)
			require.NoError(t, err)
			signRawSigV4Creds(t, req2, "lambda", emptySHA256, "test", "not-the-real-secret")
			resp2, err := http.DefaultClient.Do(req2)
			require.NoError(t, err)
			defer resp2.Body.Close()
			assert.Equal(t, http.StatusForbidden, resp2.StatusCode,
				"wrong-secret %s %s must be rejected", rt.method, rt.url)
			assert.Equal(t, "SignatureDoesNotMatch", resp2.Header.Get("X-Amzn-Errortype"))
		})
	}
}
