package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/delivery"
)

// Amazon EventBridge invokes Amazon ECS, Amazon Kinesis Data Streams, AWS
// Batch and API destination targets as the target's RoleArn, which must trust
// events.amazonaws.com and allow the call the target makes.

func ebAuthorizeTargetRole(target EBTarget, actions map[string]string) *delivery.Outcome {
	return ebAuthorizeTargetRoleCall(target, nil, actions)
}

// ebAuthorizeTargetRoleCall is ebAuthorizeTargetRole for a call whose request
// EventBridge has built, so the policy's conditions on that request are
// evaluated against it.
func ebAuthorizeTargetRoleCall(target EBTarget, request *http.Request, actions map[string]string) *delivery.Outcome {
	if target.RoleArn == "" {
		outcome := delivery.Permanent(ebTargetError{"AccessDeniedException", "Target " + target.Arn + " requires a RoleArn"})
		return &outcome
	}
	if err := iamValidateServiceRoleCall(target.RoleArn, "events.amazonaws.com", request, actions); err != nil {
		outcome := delivery.Permanent(ebTargetError{"AccessDeniedException", err.Error()})
		return &outcome
	}
	return nil
}

// ebCallOutcome reads a downstream API's answer: throttling and server errors
// are retried, any other refusal is final.
func ebCallOutcome(status int, body []byte) delivery.Outcome {
	if status < 400 {
		return delivery.Delivered()
	}
	code, message := awsJSONError(body)
	err := ebTargetError{code, message}
	if status == http.StatusTooManyRequests || status >= 500 || code == "ThrottlingException" {
		return delivery.Retryable(err).WithStatus(status)
	}
	return delivery.Permanent(err).WithStatus(status)
}

// ebCustomInput reports whether the target shapes its own input rather than
// receiving the whole event.
func ebCustomInput(target EBTarget) bool {
	return target.Input != "" || target.InputPath != "" || len(target.InputTransformer) > 0
}

// ebInvokeECSTarget runs the target's task definition on the cluster the
// target ARN names, started by events-rule/<rule name>. Custom input is the
// RunTask overrides.
func ebInvokeECSTarget(ruleArn string, target EBTarget, input string) delivery.Outcome {
	var params struct {
		TaskDefinitionArn    string          `json:"TaskDefinitionArn"`
		TaskCount            int             `json:"TaskCount"`
		LaunchType           string          `json:"LaunchType"`
		PlatformVersion      string          `json:"PlatformVersion"`
		Group                string          `json:"Group"`
		EnableExecuteCommand bool            `json:"EnableExecuteCommand"`
		PropagateTags        string          `json:"PropagateTags"`
		ReferenceID          string          `json:"ReferenceId"`
		Tags                 []ECSTag        `json:"Tags"`
		CapacityProvider     json.RawMessage `json:"CapacityProviderStrategy"`
		NetworkConfiguration *struct {
			AwsvpcConfiguration *struct {
				Subnets        []string `json:"Subnets"`
				SecurityGroups []string `json:"SecurityGroups"`
				AssignPublicIp string   `json:"AssignPublicIp"`
			} `json:"awsvpcConfiguration"`
		} `json:"NetworkConfiguration"`
	}
	if len(target.EcsParameters) == 0 || json.Unmarshal(target.EcsParameters, &params) != nil || params.TaskDefinitionArn == "" {
		return delivery.Permanent(ebTargetError{"InvalidParameterException", "Target " + target.Arn + " names no EcsParameters.TaskDefinitionArn"})
	}
	body := map[string]any{
		"cluster":        target.Arn,
		"taskDefinition": params.TaskDefinitionArn,
		"count":          max(params.TaskCount, 1),
		"startedBy":      "events-rule/" + ruleArn[strings.LastIndex(ruleArn, "/")+1:],
	}
	for key, value := range map[string]string{
		"launchType": params.LaunchType, "platformVersion": params.PlatformVersion, "group": params.Group,
		"propagateTags": params.PropagateTags, "referenceId": params.ReferenceID,
	} {
		if value != "" {
			body[key] = value
		}
	}
	if params.EnableExecuteCommand {
		body["enableExecuteCommand"] = true
	}
	if len(params.Tags) > 0 {
		body["tags"] = params.Tags
	}
	if params.NetworkConfiguration != nil && params.NetworkConfiguration.AwsvpcConfiguration != nil {
		vpc := params.NetworkConfiguration.AwsvpcConfiguration
		body["networkConfiguration"] = map[string]any{"awsvpcConfiguration": map[string]any{
			"subnets": vpc.Subnets, "securityGroups": vpc.SecurityGroups, "assignPublicIp": vpc.AssignPublicIp,
		}}
	}
	if ebCustomInput(target) {
		var overrides map[string]any
		if err := json.Unmarshal([]byte(input), &overrides); err != nil {
			return delivery.Permanent(ebTargetError{"InvalidParameterException", "The input for an Amazon ECS target is not the JSON of a task override"})
		}
		body["overrides"] = overrides
	}
	if denied := ebAuthorizeTargetRoleCall(target, jsonHandlerRequest(body), map[string]string{"ecs:RunTask": params.TaskDefinitionArn}); denied != nil {
		return *denied
	}
	status, response := callJSONHandler(handleECSRunTask, body)
	if outcome := ebCallOutcome(status, response); !outcome.OK() {
		return outcome
	}
	return ecsRunTaskFailuresOutcome(response)
}

// ecsRunTaskFailuresOutcome retries a RunTask that started no task: its
// failures, such as a cluster short of capacity, can clear.
func ecsRunTaskFailuresOutcome(response []byte) delivery.Outcome {
	var result struct {
		Tasks    []json.RawMessage `json:"tasks"`
		Failures []struct {
			Arn    string `json:"arn"`
			Reason string `json:"reason"`
			Detail string `json:"detail"`
		} `json:"failures"`
	}
	if json.Unmarshal(response, &result) == nil && len(result.Tasks) == 0 && len(result.Failures) > 0 {
		failure := result.Failures[0]
		return delivery.Retryable(ebTargetError{"RunTaskFailure", strings.TrimSpace(failure.Reason + " " + failure.Detail)})
	}
	return delivery.Delivered()
}

// ebInvokeKinesisTarget puts the input on the stream, partitioned by the
// value at KinesisParameters.PartitionKeyPath or else by the event ID.
func ebInvokeKinesisTarget(target EBTarget, record EBEventRecord, input string) delivery.Outcome {
	if denied := ebAuthorizeTargetRole(target, map[string]string{"kinesis:PutRecord": target.Arn}); denied != nil {
		return *denied
	}
	partitionKey := record.ID
	var params struct {
		PartitionKeyPath string `json:"PartitionKeyPath"`
	}
	if len(target.KinesisParameters) > 0 && json.Unmarshal(target.KinesisParameters, &params) == nil && params.PartitionKeyPath != "" {
		value, ok := ebJSONPath(ebBuildEvent(record), params.PartitionKeyPath)
		if !ok {
			return delivery.Permanent(ebTargetError{"InvalidParameterException", "PartitionKeyPath " + params.PartitionKeyPath + " selects nothing in the event"})
		}
		if text, isText := value.(string); isText {
			partitionKey = text
		} else {
			raw, _ := json.Marshal(value)
			partitionKey = string(raw)
		}
	}
	if _, _, err := kinesisAppendRecord("", target.Arn, []byte(input), partitionKey, ""); err != nil {
		return delivery.Permanent(ebTargetError{"ResourceNotFoundException", "Stream " + target.Arn + " not found"})
	}
	return delivery.Delivered()
}

// ebInvokeBatchTarget submits a job to the job queue the target ARN names.
func ebInvokeBatchTarget(target EBTarget) delivery.Outcome {
	var params struct {
		JobDefinition   string          `json:"JobDefinition"`
		JobName         string          `json:"JobName"`
		ArrayProperties json.RawMessage `json:"ArrayProperties"`
		RetryStrategy   json.RawMessage `json:"RetryStrategy"`
	}
	if len(target.BatchParameters) == 0 || json.Unmarshal(target.BatchParameters, &params) != nil || params.JobDefinition == "" || params.JobName == "" {
		return delivery.Permanent(ebTargetError{"ClientException", "Target " + target.Arn + " names no BatchParameters.JobDefinition and JobName"})
	}
	if denied := ebAuthorizeTargetRole(target, map[string]string{"batch:SubmitJob": target.Arn}); denied != nil {
		return *denied
	}
	body := map[string]any{"jobName": params.JobName, "jobQueue": target.Arn, "jobDefinition": params.JobDefinition}
	if len(params.ArrayProperties) > 0 {
		body["arrayProperties"] = lowerFirstKeys(params.ArrayProperties)
	}
	if len(params.RetryStrategy) > 0 {
		body["retryStrategy"] = lowerFirstKeys(params.RetryStrategy)
	}
	status, response := callJSONHandler(handleBatchSubmitJob, body)
	return ebCallOutcome(status, response)
}

// lowerFirstKeys re-spells an EventBridge structure's keys in the camel case
// the AWS Batch API reads.
func lowerFirstKeys(raw json.RawMessage) map[string]any {
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[strings.ToLower(key[:1])+key[1:]] = value
	}
	return out
}

// ebApiDestinationRetryable is how EventBridge reads an API destination's
// answer: it retries 401, 407, 409, 429 and 5xx, and no other code.
func ebApiDestinationRetryable(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusProxyAuthRequired, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return status >= 500
}

// ebInvokeApiDestination sends the input to the API destination's endpoint
// with the connection's authorization and the target's HTTP parameters.
func ebInvokeApiDestination(ctx context.Context, target EBTarget, input string) delivery.Outcome {
	name := ebApiDestinationName(target.Arn)
	destination, ok := ebApiDest.Get(name)
	if !ok || destination.Arn != target.Arn {
		return delivery.Permanent(ebTargetError{"ResourceNotFoundException", "API destination " + target.Arn + " does not exist"})
	}
	if denied := ebAuthorizeTargetRole(target, map[string]string{"events:InvokeApiDestination": destination.Arn}); denied != nil {
		return *denied
	}
	connection, ok := ebConnectionByArn(destination.ConnectionArn)
	if !ok || connection.State != "AUTHORIZED" {
		return delivery.Permanent(ebTargetError{"ConnectionNotAuthorized", "Connection " + destination.ConnectionArn + " is not authorized"})
	}
	var httpParams struct {
		PathParameterValues   []string          `json:"PathParameterValues"`
		HeaderParameters      map[string]string `json:"HeaderParameters"`
		QueryStringParameters map[string]string `json:"QueryStringParameters"`
	}
	if len(target.HttpParameters) > 0 {
		_ = json.Unmarshal(target.HttpParameters, &httpParams)
	}
	endpoint := destination.InvocationEndpoint
	for _, value := range httpParams.PathParameterValues {
		endpoint = strings.Replace(endpoint, "*", url.PathEscape(value), 1)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return delivery.Permanent(ebTargetError{"InvalidParameterException", "InvocationEndpoint " + endpoint + " is not a URL"})
	}
	auth, err := ebConnectionAuthorization(ctx, connection)
	if err != nil {
		return delivery.Retryable(ebTargetError{"ConnectionAuthorizationFailed", err.Error()})
	}
	query := parsed.Query()
	for key, value := range auth.query {
		query.Set(key, value)
	}
	for key, value := range httpParams.QueryStringParameters {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	body := []byte(input)
	if destination.HttpMethod == http.MethodGet || destination.HttpMethod == http.MethodHead {
		body = nil
	}
	request, err := http.NewRequestWithContext(ctx, destination.HttpMethod, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return delivery.Permanent(ebTargetError{"InvalidParameterException", err.Error()})
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set("User-Agent", "Amazon/EventBridge/ApiDestinations")
	for key, value := range auth.header {
		request.Header.Set(key, value)
	}
	for key, value := range httpParams.HeaderParameters {
		request.Header.Set(key, value)
	}
	if err := ebAwaitInvocationSlot(ctx, destination); err != nil {
		return delivery.Retryable(ebTargetError{"ThrottlingException", err.Error()})
	}
	client := http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return delivery.Retryable(ebTargetError{"SDK_CLIENT_ERROR", err.Error()})
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if response.StatusCode < 300 {
		return delivery.Delivered().WithStatus(response.StatusCode)
	}
	failure := ebTargetError{fmt.Sprint(response.StatusCode), fmt.Sprintf("%s answered %d", destination.InvocationEndpoint, response.StatusCode)}
	if ebApiDestinationRetryable(response.StatusCode) {
		return delivery.Retryable(failure).WithStatus(response.StatusCode)
	}
	return delivery.Permanent(failure).WithStatus(response.StatusCode)
}

type ebInvocationGate struct {
	mu   sync.Mutex
	next time.Time
}

var (
	ebInvocationGatesMu sync.Mutex
	ebInvocationGates   = map[string]*ebInvocationGate{}
)

func ebInvocationGateFor(destinationArn string) *ebInvocationGate {
	ebInvocationGatesMu.Lock()
	defer ebInvocationGatesMu.Unlock()
	gate, ok := ebInvocationGates[destinationArn]
	if !ok {
		gate = &ebInvocationGate{}
		ebInvocationGates[destinationArn] = gate
	}
	return gate
}

// ebAwaitInvocationSlot holds an invocation until the API destination's
// InvocationRateLimitPerSecond allows it.
func ebAwaitInvocationSlot(ctx context.Context, destination EBApiDestination) error {
	if destination.InvocationRateLimit == nil || *destination.InvocationRateLimit < 1 {
		return nil
	}
	wait := time.Until(ebInvocationGateFor(destination.Arn).reserve(time.Now(), *destination.InvocationRateLimit))
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reserve returns when the next invocation may go out, spacing invocations
// evenly so no second carries more than perSecond of them.
func (gate *ebInvocationGate) reserve(now time.Time, perSecond int32) time.Time {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	slot := gate.next
	if slot.Before(now) {
		slot = now
	}
	gate.next = slot.Add(time.Second / time.Duration(perSecond))
	return slot
}

// ebApiDestinationName reads the name from an API destination ARN,
// arn:aws:events:<region>:<account>:api-destination/<name>[/<uuid>].
func ebApiDestinationName(arn string) string {
	_, rest, _ := strings.Cut(arn, ":api-destination/")
	name, _, _ := strings.Cut(rest, "/")
	return name
}

func ebConnectionByArn(arn string) (EBConnection, bool) {
	for _, connection := range ebConnections.List() {
		if connection.Arn == arn {
			return connection, true
		}
	}
	return EBConnection{}, false
}

type ebAuthorization struct {
	header map[string]string
	query  map[string]string
}

// ebConnectionAuthorization is what a connection adds to each request: an API
// key header, a Basic authorization header, or an OAuth bearer token from the
// client-credentials exchange, plus the connection's invocation parameters.
func ebConnectionAuthorization(ctx context.Context, connection EBConnection) (ebAuthorization, error) {
	type parameter struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	}
	type httpParameters struct {
		HeaderParameters      []parameter `json:"HeaderParameters"`
		QueryStringParameters []parameter `json:"QueryStringParameters"`
		BodyParameters        []parameter `json:"BodyParameters"`
	}
	var params struct {
		ApiKeyAuthParameters *struct {
			ApiKeyName  string `json:"ApiKeyName"`
			ApiKeyValue string `json:"ApiKeyValue"`
		} `json:"ApiKeyAuthParameters"`
		BasicAuthParameters *struct {
			Username string `json:"Username"`
			Password string `json:"Password"`
		} `json:"BasicAuthParameters"`
		OAuthParameters *struct {
			AuthorizationEndpoint string `json:"AuthorizationEndpoint"`
			HttpMethod            string `json:"HttpMethod"`
			ClientParameters      *struct {
				ClientID     string `json:"ClientID"`
				ClientSecret string `json:"ClientSecret"`
			} `json:"ClientParameters"`
			OAuthHttpParameters *httpParameters `json:"OAuthHttpParameters"`
		} `json:"OAuthParameters"`
		InvocationHttpParameters *httpParameters `json:"InvocationHttpParameters"`
	}
	auth := ebAuthorization{header: map[string]string{}, query: map[string]string{}}
	stored, err := ebConnectionParameters(connection)
	if err != nil {
		return auth, err
	}
	if err := json.Unmarshal(stored, &params); err != nil {
		return auth, fmt.Errorf("connection %s authorization parameters: %w", connection.Name, err)
	}
	if params.InvocationHttpParameters != nil {
		for _, p := range params.InvocationHttpParameters.HeaderParameters {
			auth.header[p.Key] = p.Value
		}
		for _, p := range params.InvocationHttpParameters.QueryStringParameters {
			auth.query[p.Key] = p.Value
		}
	}
	switch {
	case params.ApiKeyAuthParameters != nil:
		auth.header[params.ApiKeyAuthParameters.ApiKeyName] = params.ApiKeyAuthParameters.ApiKeyValue
	case params.BasicAuthParameters != nil:
		credentials := params.BasicAuthParameters.Username + ":" + params.BasicAuthParameters.Password
		auth.header["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials))
	case params.OAuthParameters != nil:
		oauth := params.OAuthParameters
		if oauth.ClientParameters == nil {
			return auth, fmt.Errorf("connection %s names no OAuth client", connection.Name)
		}
		endpoint, err := url.Parse(oauth.AuthorizationEndpoint)
		if err != nil {
			return auth, err
		}
		form := url.Values{}
		query := endpoint.Query()
		headers := map[string]string{}
		if oauth.OAuthHttpParameters != nil {
			for _, p := range oauth.OAuthHttpParameters.BodyParameters {
				form.Set(p.Key, p.Value)
			}
			for _, p := range oauth.OAuthHttpParameters.QueryStringParameters {
				query.Set(p.Key, p.Value)
			}
			for _, p := range oauth.OAuthHttpParameters.HeaderParameters {
				headers[p.Key] = p.Value
			}
		}
		endpoint.RawQuery = query.Encode()
		method := oauth.HttpMethod
		if method == "" {
			method = http.MethodPost
		}
		var body io.Reader
		if method != http.MethodGet {
			body = strings.NewReader(form.Encode())
		}
		request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
		if err != nil {
			return auth, err
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		request.SetBasicAuth(oauth.ClientParameters.ClientID, oauth.ClientParameters.ClientSecret)
		client := http.Client{Timeout: 5 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return auth, fmt.Errorf("OAuth token request to %s: %w", oauth.AuthorizationEndpoint, err)
		}
		defer response.Body.Close()
		var token struct {
			AccessToken string `json:"access_token"`
		}
		if response.StatusCode >= 300 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token) != nil || token.AccessToken == "" {
			return auth, fmt.Errorf("OAuth endpoint %s answered %d without an access_token", oauth.AuthorizationEndpoint, response.StatusCode)
		}
		auth.header["Authorization"] = "Bearer " + token.AccessToken
	}
	return auth, nil
}
