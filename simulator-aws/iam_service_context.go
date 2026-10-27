package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// For a direct client call aws:ViaAWSService is false and the source keys are
// absent, as in AWS. When an AWS service calls on the principal's behalf (an
// internal delivery such as Amazon SNS to AWS Lambda), the delivery records the
// originating service and source resource; surface them as aws:CalledVia,
// aws:SourceArn and aws:SourceAccount with aws:ViaAWSService=true, and match a
// statement's Principal:{Service:…} against the calling service.

type iamServiceSource struct {
	Service       string // e.g. "sns.amazonaws.com"
	SourceArn     string // the originating resource ARN
	SourceAccount string // the originating resource's account
}

// iamStampServiceInitiation records src on r as the AWS service that originated
// the call (see firehoseAuthorizeCustomerKeyUse). A client's request carries no
// stamp, so it is a direct call.
func iamStampServiceInitiation(r *http.Request, src iamServiceSource) *http.Request {
	return r.WithContext(sim.WithCaller(r.Context(), sim.Caller{Service: src.Service, Source: src.SourceArn, Account: src.SourceAccount}))
}

// iamServiceInitiatedRequest is the request envelope an AWS-service-initiated
// call carries when the sim originates it internally rather than receiving it
// from a client: no client body or headers, the originating service stamped on
// it.
func iamServiceInitiatedRequest(src iamServiceSource) *http.Request {
	r := &http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Path: "/"},
		Header: http.Header{},
		Body:   http.NoBody,
	}
	return iamStampServiceInitiation(r, src)
}

// iamDeliverySource reports the originating-service context, nil for a direct
// client call: nothing stamps a client's request. Internal event delivery
// (snsAttemptLambda) authorizes the target's resource policy with
// iamEvalServiceInitiated; an internal call on the principal's behalf stamps the
// source with iamStampServiceInitiation and goes through the gate.
func iamDeliverySource(r *http.Request) *iamServiceSource {
	if r == nil {
		return nil
	}
	caller, stamped := sim.CallerOf(r.Context())
	if !stamped {
		return nil
	}
	return &iamServiceSource{Service: caller.Service, SourceArn: caller.Source, SourceAccount: caller.Account}
}

// iamAuthorizeServiceDelivery authorizes the originating service against the
// target's resource-based policy before an internal delivery. A target with no
// resource policy denies: AWS requires the target to grant the source service
// permission.
func iamAuthorizeServiceDelivery(targetArn, action string, src iamServiceSource) bool {
	docs := iamResourcePolicyDocsForARN(targetArn)
	if len(docs) == 0 {
		return false
	}
	return iamEvalServiceInitiated(docs, action, targetArn, src)
}

// iamValidateServiceRole proves that an AWS service can assume a configured IAM
// role and that the role's identity policies authorize each action the service
// will perform. Service integrations call it when accepting a role ARN and again
// at delivery, so deleting or narrowing the role takes effect at once.
func iamValidateServiceRole(roleARN, service string, actions map[string]string) error {
	return iamValidateServiceRoleCall(roleARN, service, nil, actions)
}

// iamValidateServiceRoleCall is iamValidateServiceRole for a call the service
// sends as request: the condition keys that request settles, such as the
// ecs:cluster a RunTask names, are part of each action's context. A nil
// request is a call that has no request of its own to read.
func iamValidateServiceRoleCall(roleARN, service string, request *http.Request, actions map[string]string) error {
	roleName := iamRoleNameFromArn(roleARN)
	role, ok := iamRoles.Get(roleName)
	if !ok || role.Arn != roleARN {
		return fmt.Errorf("IAM role %s does not exist", roleARN)
	}
	trust, err := parseIAMPolicy(role.AssumeRolePolicyDocument)
	if err != nil {
		return fmt.Errorf("IAM role %s has an invalid trust policy: %w", roleARN, err)
	}
	src := iamServiceSource{Service: service}
	if decision, _ := iamEvalDecisionForPrincipal(
		[]iamPolicyDoc{trust}, "sts:AssumeRole", roleARN, "service:"+service,
		iamServiceInitiatedConditionContext(src),
	); decision != "allowed" {
		return fmt.Errorf("IAM role %s does not trust %s", roleARN, service)
	}
	docs := iamPolicyDocsForRole(roleName)
	for action, resource := range actions {
		ctx := iamServiceCallConditionContext(src, request, action)
		if decision, _ := iamEvalDecision(docs, action, resource, ctx); decision != "allowed" {
			return fmt.Errorf("IAM role %s does not allow %s on %s", roleARN, action, resource)
		}
	}
	return nil
}

// iamServiceTrustedRole returns the role ref names, by ARN or by the bare
// name some services accept, once its trust policy lets service assume it on
// behalf of sourceArn. A role created moments before qualifies: IAM in the
// simulator has no propagation delay.
func iamServiceTrustedRole(ref, service, sourceArn string) (IAMRole, error) {
	role, ok := iamRoles.Get(iamRoleNameFromArn(ref))
	if !ok || (strings.Contains(ref, ":") && role.Arn != ref) {
		return IAMRole{}, fmt.Errorf("IAM role %s does not exist", ref)
	}
	trust, err := parseIAMPolicy(role.AssumeRolePolicyDocument)
	if err != nil {
		return IAMRole{}, fmt.Errorf("IAM role %s has an invalid trust policy: %w", ref, err)
	}
	src := iamServiceSource{Service: service, SourceArn: sourceArn, SourceAccount: awsAccountID()}
	if decision, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{trust}, "sts:AssumeRole", role.Arn,
		"service:"+service, iamServiceInitiatedConditionContext(src)); decision != "allowed" {
		return IAMRole{}, fmt.Errorf("%s is not authorized to perform: sts:AssumeRole on resource: %s", service, role.Arn)
	}
	return role, nil
}

// iamServiceCallConditionContext is the context one action an AWS service
// performs on the principal's behalf is authorized against: the
// service-initiation keys plus the keys the called service settles for a
// service-initiated call (kms:ViaService and kms:GrantIsForAWSResource for AWS
// KMS).
func iamServiceCallConditionContext(src iamServiceSource, request *http.Request, action string) map[string][]string {
	ctx := iamServiceInitiatedConditionContext(src)
	if request == nil {
		service, operation, _ := strings.Cut(action, ":")
		iamRunRequestConditionPopulators(iamServiceInitiatedRequest(src), service, operation, nil, ctx)
		return ctx
	}
	// The populators read and replace the body of the request they are given,
	// so each one reads a copy and the request itself still carries its body
	// to the handler.
	body := iamRequestBody(request)
	r := iamStampServiceInitiation(request, src)
	r.Body = io.NopCloser(bytes.NewReader(body))
	iamPopulateResourceConditionKeys(r, action, ctx)
	iamPopulateServiceConditionKeys(r, action, body, ctx)
	return ctx
}

// iamEvalServiceInitiated reports whether the target's resource-based policy
// admits an AWS-service-initiated call, evaluated with the originating-service
// keys.
func iamEvalServiceInitiated(docs []iamPolicyDoc, action, resource string, src iamServiceSource) bool {
	ctx := iamServiceInitiatedConditionContext(src)
	decision, _ := iamEvalDecisionForPrincipal(docs, action, resource, "service:"+src.Service, ctx)
	return decision == "allowed"
}

// iamServiceInitiatedConditionContext is the condition context a
// service-initiated delivery evaluates against. The source keys live only here:
// a direct client call is not a service, so the request path never carries
// them.
func iamServiceInitiatedConditionContext(src iamServiceSource) map[string][]string {
	ctx := map[string][]string{"aws:ViaAWSService": {"true"}}
	if src.Service != "" {
		ctx["aws:CalledVia"] = []string{src.Service}
	}
	if src.SourceArn != "" {
		ctx["aws:SourceArn"] = []string{src.SourceArn}
	}
	if src.SourceAccount != "" {
		ctx["aws:SourceAccount"] = []string{src.SourceAccount}
	}
	return ctx
}

// iamServiceInitiation returns the originating-service context, or nil for a direct client call.
func iamServiceInitiation(r *http.Request) *iamServiceSource {
	return iamDeliverySource(r)
}

func iamPopulateServiceContext(r *http.Request, ctx map[string][]string) {
	src := iamServiceInitiation(r)
	if src == nil {
		ctx["aws:ViaAWSService"] = []string{"false"}
		return
	}
	ctx["aws:ViaAWSService"] = []string{"true"}
	if src.Service != "" {
		ctx["aws:CalledVia"] = []string{src.Service}
	}
	if src.SourceArn != "" {
		ctx["aws:SourceArn"] = []string{src.SourceArn}
	}
	if src.SourceAccount != "" {
		ctx["aws:SourceAccount"] = []string{src.SourceAccount}
	}
}
