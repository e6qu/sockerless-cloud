package main

import (
	"net/http"
)

// awsJson serialises a member under the name the model declares, and AWS
// services disagree on the access-denied message member: about half spell it
// `message`, half `Message`, and a few vary by exception. A client reads its own
// model's spelling, so a denial under the other one arrives with no message.
// TestIAMDenyMessageKeysMatchTheVendoredModels holds this table to the vendored
// models.
var iamDenyMessageKeys = map[string]string{
	"acm":           "Message",
	"amplify":       "message",
	"apigateway":    "message",
	"budgets":       "Message",
	"cloudfront":    "Message",
	"cloudtrail":    "Message",
	"cloudwatch":    "Message",
	"logs":          "message",
	"ecs":           "message",
	"events":        "message",
	"glue":          "Message",
	"kinesis":       "message",
	"lambda":        "Message",
	"organizations": "Message",
	"states":        "message",
	"sns":           "message",
	"sqs":           "message",
	"ssm":           "Message",
}

// iamDenyMessageKey falls back to `message`, the spelling most AWS models
// declare, for a service whose model the project does not vendor.
func iamDenyMessageKey(service string) string {
	if key, ok := iamDenyMessageKeys[service]; ok {
		return key
	}
	return "message"
}

func iamWriteJSONDeny(w http.ResponseWriter, service, code, message string) {
	awsWriteJSONErrorBody(w, awsJSON11, http.StatusForbidden, code, map[string]string{
		"__type":                   code,
		iamDenyMessageKey(service): message,
	})
}

// iamPolicyKind is the kind of policy that decides a refusal, which AWS names
// in the access-denied message.
type iamPolicyKind int

const (
	iamIdentityBasedPolicy iamPolicyKind = iota
	iamResourceBasedPolicy
	iamRoleTrustPolicy
	iamPermissionsBoundary
	iamSessionPolicy
	iamServiceControlPolicy
)

// The IAM User Guide's "Troubleshoot access denied error messages" spells
// each kind two ways: after "because no" for an implicit denial, and after
// "with an explicit deny in" for an explicit one.
var iamPolicyKindPhrases = map[iamPolicyKind][2]string{
	iamIdentityBasedPolicy:  {"identity-based policy", "an identity-based policy"},
	iamResourceBasedPolicy:  {"resource-based policy", "a resource-based policy"},
	iamRoleTrustPolicy:      {"role trust policy", "the role trust policy"},
	iamPermissionsBoundary:  {"permissions boundary", "a permissions boundary"},
	iamSessionPolicy:        {"session policy", "a session policy"},
	iamServiceControlPolicy: {"service control policy", "a service control policy"},
}

// iamDenial is a refusal of Action to Principal and the policy that decided it.
type iamDenial struct {
	Principal string
	Action    string
	Policy    iamPolicyKind
	Explicit  bool
	// Resource and Context are what the refused request was evaluated
	// against, which an encoded authorization message carries.
	Resource string
	Context  map[string][]string
}

// head names the principal, the action and, when the request named one, the
// resource, as every refusal opens.
func (d iamDenial) head() string {
	head := "User: " + d.Principal + " is not authorized to perform: " + d.Action
	if d.Resource != "" && d.Resource != "*" {
		head += " on resource: " + d.Resource
	}
	return head
}

func (d iamDenial) message() string {
	phrases := iamPolicyKindPhrases[d.Policy]
	head := d.head()
	if d.Explicit {
		return head + " with an explicit deny in " + phrases[1]
	}
	return head + " because no " + phrases[0] + " allows the " + d.Action + " action"
}

// iamServicePrincipalDenial refuses an AWS service acting as its own
// principal, which only the target's resource-based policy can admit.
func iamServicePrincipalDenial(src iamServiceSource, action string) iamDenial {
	return iamDenial{Principal: src.Service, Action: action, Policy: iamResourceBasedPolicy}
}

// iamTrustGatedAction reports whether action is authorized against the
// role's trust policy, the resource policy of assuming a role.
func iamTrustGatedAction(action string) bool {
	return action == "sts:AssumeRole" || action == "sts:TagSession" || action == "sts:SetSourceIdentity"
}

func iamResourcePolicyKindFor(action string) iamPolicyKind {
	if iamTrustGatedAction(action) {
		return iamRoleTrustPolicy
	}
	return iamResourceBasedPolicy
}
