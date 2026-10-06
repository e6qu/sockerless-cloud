package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AWS Lambda function URLs. Each URL config is served at the host its
// FunctionUrl names, <url-id>.lambda-url.<region>.on.aws: the request becomes
// a payload format version 2.0 event, the function runs, and its result
// becomes the HTTP response.

// lambdaFunctionURLMaxPayload is the largest request or response body a
// function URL carries in buffered mode.
const lambdaFunctionURLMaxPayload = 6 * 1024 * 1024

// lambdaFunctionURLInvoker serves the requests a function URL host receives.
// The middleware claims a request by its host alone; authorizing and running
// the function is the cost of that request, as it is of an Invoke call.
type lambdaFunctionURLInvoker struct{}

func registerLambdaFunctionURLDataPlane(srv *sim.Server) {
	var invoker lambdaFunctionURLInvoker
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if name, config, ok := lambdaFunctionURLForHost(r.Host); ok {
				invoker.serve(w, r, name, config)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

var lambdaURLConfigsByHost sim.GenerationIndex[LambdaFunctionUrlConfig]

// lambdaFunctionURLForHost finds the function whose URL config names host.
func lambdaFunctionURLForHost(host string) (string, LambdaFunctionUrlConfig, bool) {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	if !strings.Contains(hostname, ".lambda-url.") {
		return "", LambdaFunctionUrlConfig{}, false
	}
	config, ok := lambdaURLConfigsByHost.Lookup(lambdaURLConfigs, hostname, func(c LambdaFunctionUrlConfig) []string {
		parsed, err := url.Parse(c.FunctionUrl)
		if err != nil {
			return nil
		}
		return []string{strings.ToLower(parsed.Hostname())}
	})
	if !ok {
		return "", LambdaFunctionUrlConfig{}, false
	}
	return lambdaFunctionNameFromARN(config.FunctionArn), config, true
}

func lambdaFunctionNameFromARN(arn string) string {
	name := arn[strings.Index(arn, ":function:")+len(":function:"):]
	name, _, _ = strings.Cut(name, ":")
	return name
}

// lambdaFunctionURLTargets is what an invoke through a function URL is
// authorized as: lambda:InvokeFunctionUrl under the URL's auth type, and
// lambda:InvokeFunction with lambda:InvokedViaFunctionUrl set, both on the
// function.
func lambdaFunctionURLTargets(r *http.Request) []iamAuthorizationTarget {
	_, config, ok := lambdaFunctionURLForHost(r.Host)
	if !ok {
		return nil
	}
	return []iamAuthorizationTarget{
		{action: "lambda:InvokeFunctionUrl", resource: config.FunctionArn, context: map[string][]string{
			"lambda:FunctionUrlAuthType": {config.AuthType}, "lambda:FunctionArn": {config.FunctionArn}}},
		{action: "lambda:InvokeFunction", resource: config.FunctionArn,
			context: map[string][]string{"lambda:InvokedViaFunctionUrl": {"true"}}},
	}
}

func lambdaFunctionURLError(w http.ResponseWriter, status int, errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-RequestId", sim.NewUUID())
	if errorType != "" {
		w.Header().Set("X-Amzn-ErrorType", errorType)
	}
	w.WriteHeader(status)
	payload, _ := json.Marshal(map[string]string{"Message": message})
	_, _ = w.Write(payload)
}

// lambdaFunctionURLAuthorized authorizes the request the way the URL's auth
// type says: AWS_IAM requires a SigV4 signature for the lambda service and a
// policy allowing both actions; NONE admits anyone the function's resource
// policy allows.
func lambdaFunctionURLAuthorized(r *http.Request, config LambdaFunctionUrlConfig, body []byte) (lambdaURLCaller, bool) {
	targets := lambdaFunctionURLTargets(r)
	if strings.EqualFold(config.AuthType, "AWS_IAM") {
		result, err := sigv4Verify(r, body)
		if err != nil || result == sigv4NoCredential {
			return lambdaURLCaller{}, false
		}
		akid := iamAccessKeyIDFromRequest(r)
		principal := lambdaURLCallerFor(akid)
		for _, target := range targets {
			allowed, _, registered := iamAuthorizeWithContext(r, target.action, target.resource, target.context)
			if registered && !allowed {
				return lambdaURLCaller{}, false
			}
		}
		return principal, true
	}
	policy := iamResourcePolicyDocsForARN(config.FunctionArn)
	for _, target := range targets {
		ctx := iamRequestConditionContext(r, "", "", "", target.action)
		for key, values := range target.context {
			ctx[key] = values
		}
		if decision, _ := iamEvalDecisionForPrincipal(policy, target.action, target.resource, "anonymous", ctx); decision != "allowed" {
			return lambdaURLCaller{}, false
		}
	}
	return lambdaURLCaller{}, true
}

// lambdaURLCaller is what the authorizer.iam block of a function URL event
// reports about a signed caller.
type lambdaURLCaller struct {
	AccessKey, AccountID, CallerID, UserArn, UserID string
}

func lambdaURLCallerFor(akid string) lambdaURLCaller {
	info := lambdaURLCaller{AccessKey: akid, AccountID: awsAccountID()}
	if tc, ok := iamTempCreds.Get(akid); ok {
		info.UserArn = tc.PrincipalArn
		if role, found := iamRoles.Get(tc.RoleName); found {
			session := tc.PrincipalArn[strings.LastIndex(tc.PrincipalArn, "/")+1:]
			info.UserID = role.RoleId + ":" + session
		}
	} else if key, ok := iamAccessKeys.Get(akid); ok {
		if user, found := iamUsers.Get(key.UserName); found {
			info.UserArn, info.UserID = user.Arn, user.UserId
		}
	}
	info.CallerID = info.UserID
	return info
}

func (lambdaFunctionURLInvoker) serve(w http.ResponseWriter, r *http.Request, name string, config LambdaFunctionUrlConfig) {
	body, err := io.ReadAll(io.LimitReader(r.Body, lambdaFunctionURLMaxPayload+1))
	if err != nil {
		lambdaFunctionURLError(w, http.StatusBadRequest, "", "Bad Request")
		return
	}
	if len(body) > lambdaFunctionURLMaxPayload {
		lambdaFunctionURLError(w, http.StatusRequestEntityTooLarge, "RequestTooLargeException", "Request must be smaller than 6291456 bytes for the InvokeFunction operation")
		return
	}
	cors := lambdaFunctionURLCorsOf(config)
	if r.Method == http.MethodOptions && cors != nil && r.Header.Get("Origin") != "" &&
		r.Header.Get("Access-Control-Request-Method") != "" {
		cors.preflight(w, r)
		return
	}
	principal, ok := lambdaFunctionURLAuthorized(r, config, body)
	if !ok {
		lambdaFunctionURLError(w, http.StatusForbidden, "AccessDeniedException", "Forbidden")
		return
	}
	fn, _, found := lambdaResolveInvocationTarget(name, "")
	if !found {
		lambdaFunctionURLError(w, http.StatusNotFound, "ResourceNotFoundException", "Not Found")
		return
	}
	event := lambdaFunctionURLEvent(r, config, principal, body)
	payload, err := json.Marshal(event)
	if err != nil {
		lambdaFunctionURLError(w, http.StatusInternalServerError, "", "Internal Server Error")
		return
	}
	sim.DeclareWait(r.Context(), lambdaInvokeWaitLimit(fn))
	result, unhandled, _ := invokeLambdaViaRuntimeAPI(sim.LifetimeContext(r.Context()), fn, payload)
	if cors != nil {
		cors.decorate(w, r)
	}
	if unhandled {
		lambdaFunctionURLError(w, http.StatusBadGateway, "", "Internal Server Error")
		return
	}
	if strings.EqualFold(config.InvokeMode, "RESPONSE_STREAM") {
		lambdaFunctionURLWriteStream(w, result)
		return
	}
	lambdaFunctionURLWriteBuffered(w, result)
}

// lambdaFunctionURLEvent is the payload format version 2.0 event AWS Lambda
// hands the function for a request to its URL.
func lambdaFunctionURLEvent(r *http.Request, config LambdaFunctionUrlConfig, principal lambdaURLCaller, body []byte) map[string]any {
	hostname := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		hostname = h
	}
	urlID, _, _ := strings.Cut(hostname, ".")
	headers := map[string]string{}
	var cookies []string
	for key, values := range r.Header {
		lower := strings.ToLower(key)
		if lower == "cookie" {
			for _, value := range values {
				for _, cookie := range strings.Split(value, ";") {
					if cookie = strings.TrimSpace(cookie); cookie != "" {
						cookies = append(cookies, cookie)
					}
				}
			}
			continue
		}
		headers[lower] = strings.Join(values, ",")
	}
	headers["host"] = hostname
	now := time.Now().UTC()
	requestContext := map[string]any{
		"accountId":      "anonymous",
		"apiId":          urlID,
		"authentication": nil,
		"domainName":     hostname,
		"domainPrefix":   urlID,
		"http": map[string]any{
			"method":    r.Method,
			"path":      r.URL.EscapedPath(),
			"protocol":  r.Proto,
			"sourceIp":  iamSourceIP(r),
			"userAgent": r.UserAgent(),
		},
		"requestId": sim.NewUUID(),
		"routeKey":  "$default",
		"stage":     "$default",
		"time":      now.Format("02/Jan/2006:15:04:05 -0700"),
		"timeEpoch": now.UnixMilli(),
	}
	if strings.EqualFold(config.AuthType, "AWS_IAM") {
		requestContext["accountId"] = principal.AccountID
		requestContext["authorizer"] = map[string]any{"iam": map[string]any{
			"accessKey":       principal.AccessKey,
			"accountId":       principal.AccountID,
			"callerId":        principal.CallerID,
			"cognitoIdentity": nil,
			"principalOrgId":  awsOrgID(),
			"userArn":         principal.UserArn,
			"userId":          principal.UserID,
		}}
	}
	event := map[string]any{
		"version":         "2.0",
		"routeKey":        "$default",
		"rawPath":         r.URL.EscapedPath(),
		"rawQueryString":  r.URL.RawQuery,
		"headers":         headers,
		"requestContext":  requestContext,
		"pathParameters":  nil,
		"stageVariables":  nil,
		"isBase64Encoded": false,
	}
	if len(cookies) > 0 {
		event["cookies"] = cookies
	}
	if query := r.URL.Query(); len(query) > 0 {
		parameters := map[string]string{}
		for key, values := range query {
			parameters[key] = strings.Join(values, ",")
		}
		event["queryStringParameters"] = parameters
	}
	if len(body) > 0 {
		if lambdaFunctionURLTextual(r.Header.Get("Content-Type")) {
			event["body"] = string(body)
		} else {
			event["body"] = base64.StdEncoding.EncodeToString(body)
			event["isBase64Encoded"] = true
		}
	}
	return event
}

// lambdaFunctionURLTextual reports whether a body of this content type reaches
// the function as text rather than base64.
func lambdaFunctionURLTextual(contentType string) bool {
	contentType = strings.ToLower(contentType)
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	for _, kind := range []string{"json", "xml", "javascript", "x-www-form-urlencoded"} {
		if strings.Contains(contentType, kind) {
			return true
		}
	}
	return false
}

// lambdaFunctionURLResponse is the response shape a function returns to its
// URL. A function returning JSON without a statusCode is answered with that
// JSON as an application/json body.
type lambdaFunctionURLResponse struct {
	StatusCode      int               `json:"statusCode"`
	Headers         map[string]string `json:"headers"`
	Cookies         []string          `json:"cookies"`
	Body            *string           `json:"body"`
	IsBase64Encoded bool              `json:"isBase64Encoded"`
}

func lambdaFunctionURLWriteBuffered(w http.ResponseWriter, result []byte) {
	var probe map[string]json.RawMessage
	if json.Unmarshal(result, &probe) == nil {
		if _, structured := probe["statusCode"]; structured {
			var response lambdaFunctionURLResponse
			if json.Unmarshal(result, &response) == nil {
				lambdaFunctionURLWriteStructured(w, response)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func lambdaFunctionURLWriteStructured(w http.ResponseWriter, response lambdaFunctionURLResponse) {
	for key, value := range response.Headers {
		w.Header().Set(key, value)
	}
	for _, cookie := range response.Cookies {
		w.Header().Add("Set-Cookie", cookie)
	}
	var body []byte
	if response.Body != nil {
		body = []byte(*response.Body)
		if response.IsBase64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(*response.Body)
			if err != nil {
				lambdaFunctionURLError(w, http.StatusBadGateway, "", "Internal Server Error")
				return
			}
			body = decoded
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := response.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// lambdaFunctionURLStreamDelimiter separates the JSON prelude a streaming
// function writes through awslambda.HttpResponseStream from the body.
var lambdaFunctionURLStreamDelimiter = make([]byte, 8)

func lambdaFunctionURLWriteStream(w http.ResponseWriter, result []byte) {
	if cut := bytes.Index(result, lambdaFunctionURLStreamDelimiter); cut > 0 && result[0] == '{' {
		var prelude lambdaFunctionURLResponse
		if json.Unmarshal(result[:cut], &prelude) == nil {
			rest := string(result[cut+len(lambdaFunctionURLStreamDelimiter):])
			prelude.Body = &rest
			prelude.IsBase64Encoded = false
			if prelude.Headers == nil {
				prelude.Headers = map[string]string{}
			}
			if prelude.Headers["Content-Type"] == "" && prelude.Headers["content-type"] == "" {
				prelude.Headers["Content-Type"] = "application/octet-stream"
			}
			lambdaFunctionURLWriteStructured(w, prelude)
			return
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

// lambdaFunctionURLCors is a URL config's cross-origin resource sharing
// settings.
type lambdaFunctionURLCors struct {
	AllowCredentials bool     `json:"AllowCredentials"`
	AllowHeaders     []string `json:"AllowHeaders"`
	AllowMethods     []string `json:"AllowMethods"`
	AllowOrigins     []string `json:"AllowOrigins"`
	ExposeHeaders    []string `json:"ExposeHeaders"`
	MaxAge           int      `json:"MaxAge"`
}

func lambdaFunctionURLCorsOf(config LambdaFunctionUrlConfig) *lambdaFunctionURLCors {
	if config.Cors == nil {
		return nil
	}
	encoded, err := json.Marshal(config.Cors)
	if err != nil {
		return nil
	}
	var cors lambdaFunctionURLCors
	if json.Unmarshal(encoded, &cors) != nil {
		return nil
	}
	return &cors
}

func lambdaCorsListAllows(list []string, value string) bool {
	for _, entry := range list {
		if entry == "*" || strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

// allowedOrigin is the Access-Control-Allow-Origin a request's origin earns.
func (c *lambdaFunctionURLCors) allowedOrigin(origin string) (string, bool) {
	if origin == "" || !lambdaCorsListAllows(c.AllowOrigins, origin) {
		return "", false
	}
	if lambdaCorsListAllows(c.AllowOrigins, "*") && !c.AllowCredentials {
		return "*", true
	}
	return origin, true
}

// decorate adds the CORS headers an actual cross-origin request receives.
func (c *lambdaFunctionURLCors) decorate(w http.ResponseWriter, r *http.Request) {
	origin, ok := c.allowedOrigin(r.Header.Get("Origin"))
	if !ok {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	if c.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
	if len(c.ExposeHeaders) > 0 {
		w.Header().Set("Access-Control-Expose-Headers", strings.Join(c.ExposeHeaders, ","))
	}
	w.Header().Add("Vary", "Origin")
}

// preflight answers an OPTIONS preflight from the URL's CORS settings,
// without invoking the function.
func (c *lambdaFunctionURLCors) preflight(w http.ResponseWriter, r *http.Request) {
	origin, ok := c.allowedOrigin(r.Header.Get("Origin"))
	method := r.Header.Get("Access-Control-Request-Method")
	if ok && lambdaCorsListAllows(c.AllowMethods, method) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(c.AllowMethods, ","))
		if len(c.AllowHeaders) > 0 {
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(c.AllowHeaders, ","))
		}
		if c.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if c.MaxAge > 0 {
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAge))
		}
		w.Header().Add("Vary", "Origin")
	}
	w.WriteHeader(http.StatusOK)
}
