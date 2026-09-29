package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// awsErrorMessageKeys decodes an awsJson error body and returns the members
// other than __type that carry the message.
func awsErrorMessageKeys(t *testing.T, raw []byte, wantType, wantMessage string) []string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("error body %q is not JSON: %v", raw, err)
	}
	if body["__type"] != wantType {
		t.Fatalf("__type = %q, want %q (body %s)", body["__type"], wantType, raw)
	}
	var keys []string
	for key, value := range body {
		if key == "__type" {
			continue
		}
		if key == "ErrorCode" {
			if value != wantType {
				t.Fatalf("ErrorCode = %q, want %q", value, wantType)
			}
			keys = append(keys, key)
			continue
		}
		if value != wantMessage {
			t.Fatalf("%s = %q, want %q", key, value, wantMessage)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestAWSErrorWritesTheSpellingTheServingServiceModels(t *testing.T) {
	if dynamo, secrets := awsErrorMessageMembers["dynamodb"]["ResourceNotFoundException"], awsErrorMessageMembers["secrets-manager"]["ResourceNotFoundException"]; dynamo == secrets {
		t.Fatalf("the cases below rely on DynamoDB and Secrets Manager spelling ResourceNotFoundException differently; both spell it %q", dynamo)
	}
	credential := func(service string) string {
		return "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260929/us-east-1/" + service + "/aws4_request, SignedHeaders=host;x-amz-date, Signature=00"
	}
	for _, c := range []struct {
		name, target, path, authorization, code string
		want                                    []string
	}{
		{name: "awsJson target", target: "DynamoDB_20120810.DescribeTable", code: "ResourceNotFoundException", want: []string{"message"}},
		{name: "same code, other service", target: "secretsmanager.DescribeSecret", code: "ResourceNotFoundException", want: []string{"Message"}},
		{name: "AWS CLI namespaced target", target: "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.GetTrail", code: "TrailNotFoundException", want: []string{awsErrorMessageMembers["cloudtrail"]["TrailNotFoundException"]}},
		{name: "restJson1 signing name", path: "/2015-02-01/access-points/fsap-0", authorization: credential("elasticfilesystem"), code: "AccessPointNotFound", want: []string{"ErrorCode", "Message"}},
		{name: "presigned credential", path: "/2015-03-31/functions/f/invocations?X-Amz-Credential=AKIDEXAMPLE%2F20260929%2Fus-east-1%2Flambda%2Faws4_request", code: "ResourceNotFoundException", want: []string{awsErrorMessageMembers["lambda"]["ResourceNotFoundException"]}},
		{name: "API Gateway V2 by its /v2/ path", path: "/v2/apis/a1", authorization: credential("apigateway"), code: "NotFoundException", want: []string{"message"}},
		{name: "API Gateway by its own path", path: "/restapis/a1", authorization: credential("apigateway"), code: "NotFoundException", want: []string{"message"}},
		{name: "undeclared error in a uniform model", path: "/2015-02-01/access-points", authorization: credential("elasticfilesystem"), code: "NotAnErrorEFSDeclares", want: []string{"ErrorCode", "Message"}},
		{name: "no modelled service", path: "/2018-06-01/runtime/invocation/next", code: "ResourceNotFoundException", want: []string{"Message", "message"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := c.path
			if path == "" {
				path = "/"
			}
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			if c.target != "" {
				request.Header.Set("X-Amz-Target", c.target)
			}
			if c.authorization != "" {
				request.Header.Set("Authorization", c.authorization)
			}
			recorder := httptest.NewRecorder()
			awsErrorModelMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// A handler reached through the POST / dispatcher writes through
				// its CloudTrail recorder.
				AWSError(&cloudTrailStatusRecorder{ResponseWriter: w}, c.code, "details", http.StatusBadRequest)
			})).ServeHTTP(recorder, request)
			got := awsErrorMessageKeys(t, recorder.Body.Bytes(), c.code, "details")
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("message under %v, want %v", got, c.want)
			}
			if _, left := awsErrorModels.Load(http.ResponseWriter(recorder)); left {
				t.Fatal("the writer's model outlived its request")
			}
		})
	}
}

func TestAWSRouterHandlerScopesAnInProcessCallToItsService(t *testing.T) {
	router := NewAWSRouter()
	router.Register("secretsmanager.DescribeSecret", func(w http.ResponseWriter, _ *http.Request) {
		AWSError(w, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.", http.StatusBadRequest)
	})
	handler, ok := router.Handler("secretsmanager.DescribeSecret")
	if !ok {
		t.Fatal("registered target not found")
	}
	recorder := httptest.NewRecorder()
	handler(recorder, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
	got := awsErrorMessageKeys(t, recorder.Body.Bytes(), "ResourceNotFoundException", "Secrets Manager can't find the specified secret.")
	if strings.Join(got, ",") != "Message" {
		t.Fatalf("message under %v, want [Message]", got)
	}
}

// Every signing name more than one vendored model shares has a path rule for
// each of them, so no signed request is left without its model.
func TestAWSModelPathPrefixesCoverEverySharedSigningName(t *testing.T) {
	for signingName, models := range awsModelsBySigningName {
		if len(models) < 2 {
			continue
		}
		covered := map[string]bool{}
		for _, rule := range awsModelPathPrefixes[signingName] {
			covered[rule.model] = true
		}
		for _, model := range models {
			if !covered[model] {
				t.Errorf("signing name %s: model %s has no path rule in awsModelPathPrefixes", signingName, model)
			}
		}
		if len(covered) != len(models) {
			t.Errorf("signing name %s: path rules name %v, the models are %v", signingName, covered, models)
		}
	}
}

// Through the whole simulator, a signed request's error carries the message
// only under its own service's modelled member.
func TestSimulatorErrorsSpellTheMessageAsEachServiceModels(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	t.Setenv("SIM_DNS_PORT", "0")
	srv, _, _, err := buildSimulator(sim.Config{Provider: "aws", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	for _, c := range []struct {
		signingName, method, path, target, body, code string
		want                                          string
	}{
		{"dynamodb", http.MethodPost, "/", "DynamoDB_20120810.DescribeTable", `{"TableName":"no-such-table"}`, "ResourceNotFoundException", "message"},
		{"secretsmanager", http.MethodPost, "/", "secretsmanager.DescribeSecret", `{"SecretId":"no-such-secret"}`, "ResourceNotFoundException", "Message"},
		{"elasticfilesystem", http.MethodDelete, "/2015-02-01/access-points/fsap-00000000", "", "", "AccessPointNotFound", "Message"},
	} {
		t.Run(c.target+c.path, func(t *testing.T) {
			payload := []byte(c.body)
			request := httptest.NewRequest(c.method, c.path, bytes.NewReader(payload))
			if c.target != "" {
				request.Header.Set("X-Amz-Target", c.target)
				request.Header.Set("Content-Type", "application/x-amz-json-1.1")
			}
			sfnSignInternalAWSRequest(request, c.signingName, payload)
			recorder := httptest.NewRecorder()
			srv.ServeHTTP(recorder, request)
			if recorder.Code < http.StatusBadRequest {
				t.Fatalf("status %d, want an error: %s", recorder.Code, recorder.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("error body %q: %v", recorder.Body.String(), err)
			}
			if typ, _ := body["__type"].(string); !strings.HasSuffix(typ, c.code) {
				t.Fatalf("__type = %v, want %s (body %s)", body["__type"], c.code, recorder.Body.String())
			}
			other := map[string]string{"message": "Message", "Message": "message"}[c.want]
			if message, _ := body[c.want].(string); message == "" {
				t.Fatalf("no message under %q: %s", c.want, recorder.Body.String())
			}
			if _, both := body[other]; both {
				t.Fatalf("message also under %q, which the model does not declare: %s", other, recorder.Body.String())
			}
			if _, carries := awsErrorCodeMembers[awsModelsBySigningName[c.signingName][0]][c.code]; carries && body["ErrorCode"] != c.code {
				t.Fatalf("ErrorCode = %v, want %s: %s", body["ErrorCode"], c.code, recorder.Body.String())
			}
		})
	}
}
