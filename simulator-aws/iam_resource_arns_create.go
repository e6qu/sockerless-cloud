package main

import (
	"strings"
)

// A create names a resource that does not exist yet, so the request carries no
// identifier to derive from. AWS still evaluates the call against the type:
// `ec2:CreateVpc` authorizes against `arn:aws:ec2:<region>:<account>:vpc/*`,
// which a policy scoped to `arn:aws:ec2:*:*:vpc/*` permits.
//
// Falling back to a literal "*" instead is not a smaller answer, it is a
// different one — "*" matches only a policy whose Resource is itself "*", so a
// type-scoped policy that real AWS honours is denied here. Deriving the type
// wildcard closes that gap without inventing an identifier: every segment comes
// from the ARN format AWS publishes, and the identifier is the wildcard the
// service itself evaluates against.
//
// Widening to every declared type would authorize against resources the call
// is not about: an action that creates one resource routinely declares the
// inputs it reads too, and ec2:CreateVpc declares the IPAM pool it draws a
// CIDR from alongside the VPC it mints. So exactly one declared type may
// answer to the operation's own name. Where none does the created type is
// unnamed, and where several do the call is genuinely ambiguous; both derive
// nothing rather than guess.
func iamCreateWildcardARNs(service, op string, types []string, region, account string) []string {
	if !iamOperationCreatesItsResource(op) {
		return nil
	}
	// The declared type must be the thing the operation creates, not its
	// parent and not one of its inputs: CreateStateMachineAlias declares
	// "statemachine", and a wildcard over every state machine is not what
	// creating one alias authorizes against.
	var created string
	for _, candidate := range types {
		if !iamCreatedTypeMatchesOperation(op, candidate) {
			continue
		}
		if created != "" {
			return nil
		}
		created = candidate
	}
	if created == "" {
		return nil
	}
	format, declared := iamResourceARNFormats[service+":"+created]
	if !declared {
		return nil
	}
	// Count the identifier variables only: ${Partition}, ${Region} and
	// ${Account} are filled from the request, not from the resource. A format
	// left naming more than one identifies a child resource whose parent the
	// request does carry, and filling every variable with "*" would discard
	// that parent — those derive through their service's own reader.
	located := strings.NewReplacer(
		"${Partition}", "aws", "${Region}", region, "${Account}", account,
	).Replace(format)
	if len(iamARNVariable.FindAllString(located, -1)) != 1 {
		return nil
	}
	arn := iamFillARNFormat(format, region, account, []string{"*"})
	if arn == "" || strings.HasSuffix(arn, ":") {
		return nil
	}
	return []string{arn}
}

// The verbs that bring a resource into existence.
var iamCreateVerbs = []string{"Create", "Allocate", "Import", "Copy", "Register", "Request", "Purchase"}

// iamOperationCreatesItsResource reports whether the operation brings the
// resource it authorizes against into existence. AWS spells these consistently
// enough to read from the name, and the caller's rule that exactly one
// declared type answer to the operation's name keeps a misread from widening
// the authorization: "Create" and "Allocate" mint a
// resource, "Import", "Copy" and "Register" mint one from something else, and
// "Request" and "Purchase" mint one the service fulfils.
func iamOperationCreatesItsResource(op string) bool {
	for _, prefix := range iamCreateVerbs {
		if strings.HasPrefix(op, prefix) {
			return true
		}
	}
	return false
}

// iamCreatedTypeMatchesOperation reports whether the operation's own noun names
// the declared type. AllocateHosts declares "dedicated-host" and matches;
// CreateStateMachineAlias declares "statemachine" and does not, because the
// alias is what it creates and the state machine is only where it lives.
func iamCreatedTypeMatchesOperation(op, resourceType string) bool {
	noun := op
	for _, prefix := range iamCreateVerbs {
		if trimmed, found := strings.CutPrefix(op, prefix); found {
			noun = trimmed
			break
		}
	}
	noun = strings.TrimSuffix(strings.ToLower(noun), "s")
	bare := strings.NewReplacer("-", "", "_", "", "/", "").Replace(strings.ToLower(resourceType))
	bare = strings.TrimSuffix(bare, "s")
	if noun == "" || bare == "" {
		return false
	}
	return noun == bare || strings.HasSuffix(bare, noun) || strings.HasSuffix(noun, bare)
}
