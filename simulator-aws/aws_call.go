package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One AWS service reaches another through that service's API, as Amazon
// EventBridge, EventBridge Scheduler and AWS Step Functions do: the request
// takes the same path a client's does, so the receiving service authorizes
// it, records it in AWS CloudTrail and answers in its own shapes.

// awsActor is who a call is made as: an AWS service acting for one of its
// resources, either as its own principal (Role empty), which the target's
// resource policy must admit, or as a session of a role the service is
// trusted to assume, whose identity policies the target evaluates.
type awsActor struct {
	// Service is the calling service's principal, such as
	// events.amazonaws.com.
	Service string
	// Source is the resource the service acts for, such as a rule's ARN.
	Source string
	// Role is the ARN of the role the call is made as.
	Role string
	// Session names the role session, as the service names it.
	Session string
}

// awsCallHost is the host an in-process request names; the signature covers
// it like any other header.
const awsCallHost = "aws.simulator.internal"

// awsCallError is a call the target service refused, in its own error code.
type awsCallError struct {
	Status  int
	Code    string
	Message string
}

func (e *awsCallError) Error() string { return e.Code + ": " + e.Message }

// awsCall sends a request to the service signing as signingService and
// returns the status and body it answered. A role the service cannot assume
// fails before anything is sent, as AWS fails the assumption.
func awsCall(ctx context.Context, actor awsActor, signingService, method, path string, header http.Header, body []byte) (int, []byte, error) {
	if body == nil {
		body = []byte{}
	}
	ctx = sim.WithCaller(sfnAWSServer.RequestContext(ctx), sim.Caller{Service: actor.Service, Source: actor.Source, Account: awsAccountID()})
	req, err := http.NewRequestWithContext(ctx, method, "http://"+awsCallHost+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if actor.Role != "" {
		cred, err := awsAssumeServiceRole(actor)
		if err != nil {
			return http.StatusForbidden, nil, &awsCallError{Status: http.StatusForbidden, Code: "AccessDeniedException", Message: err.Error()}
		}
		sigv4Sign(req, body, cred, signingService, time.Now())
	}
	response := sfnAWSServer.Call(req)
	return response.StatusCode, response.Body.Bytes(), nil
}

// awsAssumeServiceRole returns the session of actor.Role the service holds,
// once the role's trust policy lets the service assume it.
func awsAssumeServiceRole(actor awsActor) (IAMTempCred, error) {
	roleName := iamRoleNameFromArn(actor.Role)
	role, ok := iamRoles.Get(roleName)
	if !ok || role.Arn != actor.Role {
		return IAMTempCred{}, fmt.Errorf("IAM role %s does not exist", actor.Role)
	}
	trust, err := parseIAMPolicy(role.AssumeRolePolicyDocument)
	if err != nil {
		return IAMTempCred{}, fmt.Errorf("IAM role %s has an invalid trust policy: %w", actor.Role, err)
	}
	src := iamServiceSource{Service: actor.Service, SourceArn: actor.Source, SourceAccount: awsAccountID()}
	if decision, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{trust}, "sts:AssumeRole", actor.Role,
		"service:"+actor.Service, iamServiceInitiatedConditionContext(src)); decision != "allowed" {
		return IAMTempCred{}, fmt.Errorf("%s is not authorized to perform: sts:AssumeRole on resource: %s", actor.Service, actor.Role)
	}
	session := actor.Session
	if session == "" {
		session = strings.SplitN(actor.Service, ".", 2)[0]
	}
	return roleSessionCredential(roleHolder("service", actor.Service+"|"+actor.Role), role.RoleName, session, time.Now().UTC()), nil
}

// awsCallJSON calls an operation of a JSON-protocol service, named by its
// target (AmazonSQS.SendMessage), with input as the request.
func awsCallJSON(ctx context.Context, actor awsActor, signingService, target, contentType string, input any) (int, []byte, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return 0, nil, err
	}
	header := http.Header{"Content-Type": {contentType}, "X-Amz-Target": {target}}
	return awsCall(ctx, actor, signingService, http.MethodPost, "/", header, body)
}

// awsCallQuery calls an operation of a query-protocol service.
func awsCallQuery(ctx context.Context, actor awsActor, signingService, action, version string, form url.Values) (int, []byte, error) {
	form.Set("Action", action)
	form.Set("Version", version)
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	return awsCall(ctx, actor, signingService, http.MethodPost, "/", header, []byte(form.Encode()))
}
