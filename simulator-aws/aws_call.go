package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One AWS service reaches another through its API, as Amazon EventBridge,
// EventBridge Scheduler and AWS Step Functions do: the receiving service
// authorizes the call, records it in AWS CloudTrail and answers in its own
// shapes.

// A service acts either as its own principal (Role empty), which the target's
// resource policy must admit, or as a session of a role it may assume, whose
// identity policies the target evaluates.
type awsActor struct {
	Service string
	Source  string
	Role    string
	Session string
}

// The signature covers the host like any other header.
const awsCallHost = "aws.simulator.internal"

type awsCallError struct {
	Status  int
	Code    string
	Message string
}

func (e *awsCallError) Error() string { return e.Code + ": " + e.Message }

// A role the service cannot assume fails before anything is sent, as on AWS.
func awsCall(ctx context.Context, actor awsActor, signingService, method, path string, header http.Header, body []byte) (int, []byte, error) {
	response, err := awsCallResponse(ctx, actor, signingService, method, path, header, body)
	if err != nil {
		var refused *awsCallError
		if errors.As(err, &refused) {
			return refused.Status, nil, err
		}
		return 0, nil, err
	}
	return response.StatusCode, response.Body.Bytes(), nil
}

// awsCallResponse also returns headers, for REST protocols' output bindings.
func awsCallResponse(ctx context.Context, actor awsActor, signingService, method, path string, header http.Header, body []byte) (*sim.CallResponse, error) {
	if body == nil {
		body = []byte{}
	}
	ctx = sim.WithCaller(sfnAWSServer.RequestContext(ctx), sim.Caller{Service: actor.Service, Source: actor.Source, Account: awsAccountID()})
	req, err := http.NewRequestWithContext(ctx, method, "http://"+awsCallHost+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if actor.Role != "" {
		cred, err := awsAssumeServiceRole(actor)
		if err != nil {
			return nil, &awsCallError{Status: http.StatusForbidden, Code: "AccessDeniedException", Message: err.Error()}
		}
		sigv4Sign(req, body, cred, signingService, time.Now())
	}
	return sfnAWSServer.Call(req), nil
}

func awsAssumeServiceRole(actor awsActor) (IAMTempCred, error) {
	role, err := iamServiceTrustedRole(actor.Role, actor.Service, actor.Source)
	if err != nil {
		return IAMTempCred{}, err
	}
	session := actor.Session
	if session == "" {
		session = strings.SplitN(actor.Service, ".", 2)[0]
	}
	return roleSessionCredential(roleHolder("service", actor.Service+"|"+actor.Role), role.RoleName, session, time.Now().UTC()), nil
}

// target names the operation, e.g. AmazonSQS.SendMessage.
func awsCallJSON(ctx context.Context, actor awsActor, signingService, target, contentType string, input any) (int, []byte, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	header := http.Header{"Content-Type": {contentType}, "X-Amz-Target": {target}}
	return awsCall(ctx, actor, signingService, http.MethodPost, "/", header, body)
}

func awsCallQuery(ctx context.Context, actor awsActor, signingService, action, version string, form url.Values) (int, []byte, error) {
	form.Set("Action", action)
	form.Set("Version", version)
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	return awsCall(ctx, actor, signingService, http.MethodPost, "/", header, []byte(form.Encode()))
}
