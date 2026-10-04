package main

import (
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// iamTagOnCreateTargets is the further authorization AWS performs when a
// resource-creating request carries tags: the service's tagging action, on the
// resource being created ("If tags are specified in the resource-creating
// action, Amazon performs additional authorization on the ec2:CreateTags
// action"). The populators then set <service>:CreateAction on that check to the
// create's name, which is what lets a grant allow tagging on creation without
// allowing a caller to retag an existing resource. A create that carries no
// tags authorizes no tagging.
func iamTagOnCreateTargets(r *http.Request, service, operation string, resources []string) []iamAuthorizationTarget {
	tagging := iamTagOnCreateActions[service+":"+operation]
	if len(tagging) == 0 || len(iamRequestTags(r, service)) == 0 {
		return nil
	}
	var targets []iamAuthorizationTarget
	for _, action := range tagging {
		arns := iamMintedResourceARNs(r, service, operation)
		if len(arns) == 0 {
			arns = iamTaggedResourceARNs(r, service, action, resources)
		}
		for _, arn := range arns {
			targets = append(targets, iamAuthorizationTarget{action: service + ":" + action, resource: arn})
		}
	}
	return targets
}

// iamTaggedResourceARNs is what the tagging check authorizes against: the
// resources the create names that the tagging action can tag. Amazon EC2 says
// which resource each tag specification is for, and the resource does not
// exist yet, so each specification's type authorizes as that type's wildcard —
// the "instance/*" a RunInstances tagging grant is written against, never the
// image or subnet the launch also names.
func iamTaggedResourceARNs(r *http.Request, service, action string, resources []string) []string {
	if service == "ec2" {
		if arns := iamEC2TagSpecificationARNs(r); len(arns) > 0 {
			return arns
		}
	}
	var matchers []*regexp.Regexp
	for _, resourceType := range iamActionResourceTypes[service+":"+action] {
		if matcher := iamARNFormatMatcher(iamResourceARNFormats[service+":"+resourceType]); matcher != nil {
			matchers = append(matchers, matcher)
		}
	}
	var out []string
	for _, arn := range resources {
		for _, matcher := range matchers {
			if matcher.MatchString(arn) {
				out = append(out, arn)
				break
			}
		}
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

// iamMintedResourceARNs is the resource a create mints where the generic
// derivation does not reach it: the created type's ARN under the parent the
// create authorizes against, with the identifier the service assigns as the
// wildcard, or the AWS Lambda function its body names.
func iamMintedResourceARNs(r *http.Request, service, operation string) []string {
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	child := func(parent, kind, minted string) []string {
		prefix, path, ok := strings.Cut(parent, ":"+kind+"/")
		if !ok || path == "" {
			return nil
		}
		return []string{prefix + ":" + minted + "/" + path + "/*"}
	}
	switch service + ":" + operation {
	case "elasticloadbalancing:CreateListener":
		return child(r.FormValue("LoadBalancerArn"), "loadbalancer", "listener")
	case "elasticloadbalancing:CreateRule":
		return child(r.FormValue("ListenerArn"), "listener", "listener-rule")
	case "ecs:RunTask", "ecs:StartTask":
		return []string{"arn:aws:ecs:" + region + ":" + awsAccountID() + ":task/" + iamECSClusterName(r) + "/*"}
	case "lambda:CreateFunction":
		if arn := lambdaCreatedFunctionARN(r); arn != "" {
			return []string{arn}
		}
	}
	return nil
}

// iamEC2TagSpecificationARNs reads the resource type of each tag specification
// that carries a tag, in both serializations Amazon EC2 accepts
// (TagSpecification.N and, for the capacity and fleet family,
// TagSpecifications.N), as that type's wildcard ARN.
func iamEC2TagSpecificationARNs(r *http.Request) []string {
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	seen := map[string]bool{}
	var out []string
	for _, member := range []string{"TagSpecification", "TagSpecifications"} {
		for i := 1; ; i++ {
			base := member + "." + strconv.Itoa(i)
			resourceType := r.FormValue(base + ".ResourceType")
			if resourceType == "" && r.FormValue(base+".Tag.1.Key") == "" {
				break
			}
			if resourceType == "" || r.FormValue(base+".Tag.1.Key") == "" {
				continue
			}
			for _, arn := range iamTypeWildcardARN("ec2", resourceType, region, awsAccountID()) {
				if !seen[arn] {
					seen[arn] = true
					out = append(out, arn)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// iamIsTagOnCreateCheck reports whether authorizing action for this request is
// the tagging check a create adds rather than the request's own action.
func iamIsTagOnCreateCheck(r *http.Request, action string) bool {
	service, tagging, _ := strings.Cut(action, ":")
	operation := iamRequestWireOperation(r)
	return operation != "" && operation != tagging &&
		slices.Contains(iamTagOnCreateActions[service+":"+operation], tagging)
}
