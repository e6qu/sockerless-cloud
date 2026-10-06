package aws_sdk_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A function URL is reached at the https URL GetFunctionUrlConfig reports. The
// suite's transport resolves that host to the simulator, and the request goes
// over plain HTTP, which is the one coordinate that differs.

// lambdaFunctionURLRequest builds a request to path under a function URL,
// signed for the lambda service when akid is set.
func lambdaFunctionURLRequest(t *testing.T, functionURL, method, path, body, akid, secret string) *http.Request {
	t.Helper()
	target := strings.Replace(strings.TrimSuffix(functionURL, "/"), "https://", "http://", 1) + path
	request, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	if akid != "" {
		sum := sha256.Sum256([]byte(body))
		require.NoError(t, v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: akid, SecretAccessKey: secret},
			request, hex.EncodeToString(sum[:]), "lambda", "us-east-1", time.Now()))
	}
	return request
}

func lambdaFunctionURLDo(t *testing.T, request *http.Request) (int, http.Header, []byte) {
	t.Helper()
	response, err := (&http.Client{Transport: simHTTPClient.GetTransport()}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, response.Header, body
}

func lambdaFunctionURLFunction(t *testing.T, authType lambdatypes.FunctionUrlAuthType, cors *lambdatypes.Cors) (string, string) {
	t.Helper()
	lc := lambdaClient()
	name := uniqueName("url-fn")
	created, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(name),
		Role:          aws.String("arn:aws:iam::123456789012:role/test-role"),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(name)}) })
	config, err := lc.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String(name), AuthType: authType, Cors: cors})
	require.NoError(t, err)
	return aws.ToString(created.FunctionArn), aws.ToString(config.FunctionUrl)
}

// TestLambda_FunctionURLIAMInvokeNeedsBothActions covers an AWS_IAM function
// URL: the request is signed for the lambda service, authorized as both
// lambda:InvokeFunctionUrl and lambda:InvokeFunction, and handed to the
// function as a payload format version 2.0 event whose response comes back as
// the HTTP response. A grant scoped by lambda:InvokedViaFunctionUrl lets the
// URL invoke the function and nothing else.
func TestLambda_FunctionURLIAMInvokeNeedsBothActions(t *testing.T) {
	functionArn, functionURL := lambdaFunctionURLFunction(t, lambdatypes.FunctionUrlAuthTypeAwsIam, nil)

	urlOnly, urlOnlySecret := restrictedCredential(t, "url-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:InvokeFunctionUrl","Resource":"`+functionArn+`",
		  "Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"AWS_IAM"}}}]}`)
	status, _, body := lambdaFunctionURLDo(t, lambdaFunctionURLRequest(t, functionURL, http.MethodPost, "/orders?id=7",
		`{"order":7}`, urlOnly, urlOnlySecret))
	assert.Equal(t, http.StatusForbidden, status, "lambda:InvokeFunction is required as well")
	assert.JSONEq(t, `{"Message":"Forbidden"}`, string(body))

	status, _, _ = lambdaFunctionURLDo(t, lambdaFunctionURLRequest(t, functionURL, http.MethodGet, "/", "", "", ""))
	assert.Equal(t, http.StatusForbidden, status, "an AWS_IAM URL refuses an unsigned request")

	viaURL, viaURLSecret := restrictedCredential(t, "url-and-invoke",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"lambda:InvokeFunctionUrl","Resource":"`+functionArn+`"},
		  {"Effect":"Allow","Action":"lambda:InvokeFunction","Resource":"`+functionArn+`",
		   "Condition":{"Bool":{"lambda:InvokedViaFunctionUrl":"true"}}}]}`)
	status, header, body := lambdaFunctionURLDo(t, lambdaFunctionURLRequest(t, functionURL, http.MethodPost,
		"/orders?id=7", `{"order":7}`, viaURL, viaURLSecret))
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Equal(t, "application/json", header.Get("Content-Type"),
		"a function returning JSON without a statusCode is answered with that JSON")
	var event struct {
		Version        string            `json:"version"`
		RawPath        string            `json:"rawPath"`
		RawQueryString string            `json:"rawQueryString"`
		Body           string            `json:"body"`
		IsBase64       bool              `json:"isBase64Encoded"`
		Headers        map[string]string `json:"headers"`
		RequestContext struct {
			HTTP struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"http"`
			Authorizer struct {
				IAM struct {
					AccessKey string `json:"accessKey"`
					UserArn   string `json:"userArn"`
				} `json:"iam"`
			} `json:"authorizer"`
		} `json:"requestContext"`
	}
	require.NoError(t, json.Unmarshal(body, &event))
	assert.Equal(t, "2.0", event.Version)
	assert.Equal(t, "/orders", event.RawPath)
	assert.Equal(t, "id=7", event.RawQueryString)
	assert.Equal(t, `{"order":7}`, event.Body)
	assert.False(t, event.IsBase64, "a JSON body reaches the function as text")
	assert.Equal(t, http.MethodPost, event.RequestContext.HTTP.Method)
	assert.Equal(t, viaURL, event.RequestContext.Authorizer.IAM.AccessKey)
	assert.Contains(t, event.RequestContext.Authorizer.IAM.UserArn, ":user/url-and-invoke")

	direct := lambda.NewFromConfig(keyConfig(viaURL, viaURLSecret), func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err := direct.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(functionArn), Payload: []byte(`{}`)})
	require.Error(t, err, "the grant admits the function URL only")
	assert.True(t, notAuthorized(err) || errCodeOf(err) == "AccessDeniedException", err.Error())
}

// TestLambda_FunctionURLNoneAuthFollowsTheResourcePolicy covers a NONE
// function URL, which admits an unsigned caller exactly when the function's
// resource-based policy allows anyone both actions, and its CORS preflight,
// which the URL answers without invoking the function.
func TestLambda_FunctionURLNoneAuthFollowsTheResourcePolicy(t *testing.T) {
	functionArn, functionURL := lambdaFunctionURLFunction(t, lambdatypes.FunctionUrlAuthTypeNone, &lambdatypes.Cors{
		AllowOrigins: []string{"https://app.example.test"}, AllowMethods: []string{"GET"}, MaxAge: aws.Int32(300)})
	get := func() (int, []byte) {
		status, _, body := lambdaFunctionURLDo(t, lambdaFunctionURLRequest(t, functionURL, http.MethodGet, "/", "", "", ""))
		return status, body
	}
	status, body := get()
	assert.Equal(t, http.StatusForbidden, status, "no resource-based policy admits anyone yet")
	assert.JSONEq(t, `{"Message":"Forbidden"}`, string(body))

	lc := lambdaClient()
	urlStatement, err := lc.AddPermission(ctx, &lambda.AddPermissionInput{FunctionName: aws.String(functionArn),
		StatementId: aws.String("public-url"), Action: aws.String("lambda:InvokeFunctionUrl"), Principal: aws.String("*"),
		FunctionUrlAuthType: lambdatypes.FunctionUrlAuthTypeNone})
	require.NoError(t, err)
	assert.JSONEq(t, `{"Sid":"public-url","Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl",
		"Resource":"`+functionArn+`","Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"NONE"}}}`,
		aws.ToString(urlStatement.Statement))
	status, _ = get()
	assert.Equal(t, http.StatusForbidden, status, "lambda:InvokeFunction is required as well")

	invokeStatement, err := lc.AddPermission(ctx, &lambda.AddPermissionInput{FunctionName: aws.String(functionArn),
		StatementId: aws.String("public-invoke"), Action: aws.String("lambda:InvokeFunction"), Principal: aws.String("*"),
		InvokedViaFunctionUrl: aws.Bool(true)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(invokeStatement.Statement), `"Bool":{"lambda:InvokedViaFunctionUrl":"true"}`)
	status, body = get()
	require.Equal(t, http.StatusOK, status, string(body))
	assert.Contains(t, string(body), `"accountId":"anonymous"`, "an unsigned caller reaches the function anonymously")

	preflight := lambdaFunctionURLRequest(t, functionURL, http.MethodOptions, "/", "", "", "")
	preflight.Header.Set("Origin", "https://app.example.test")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	status, header, _ := lambdaFunctionURLDo(t, preflight)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "https://app.example.test", header.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "300", header.Get("Access-Control-Max-Age"))
}
