package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Which resource type an action authorizes against is AWS's answer:
// iamActionResourceTypes is generated from the vendored AWS Service Reference.
// This file pulls the resource's identifier out of the request the SDK sent and
// assembles the ARN in the shape the reference declares. An action the gate
// cannot derive authorizes against a literal "*", which matches only a policy
// whose Resource is "*".

// iamDerivedResourceARNs returns the ARNs a request names, or nil when the
// action declares no resource type (AWS's way of saying it does not support
// resource-level permissions, so the request targets "*") or when this file
// cannot read the resource out of the request.
//
// arn builds "arn:aws:<svc>:<region>:<account>:<resource>" for the request's
// region and the simulator's account.
func iamDerivedResourceARNs(r *http.Request, service, op, region, account string) []string {
	types := iamActionResourceTypes[service+":"+op]
	if len(types) == 0 {
		return nil
	}
	arn := func(svc, resource string) string {
		return "arn:aws:" + svc + ":" + region + ":" + account + ":" + resource
	}
	// Every service here names a tagging target by its own ARN, which also picks the
	// type out of a tagging action's long resource-type list (TagResource accepts
	// nine Amazon ECS types).
	if a := iamRequestARNField(r); a != "" {
		return []string{a}
	}
	if arns := iamServiceResourceARNs(r, service, op, types, region, account, arn); arns != nil {
		return arns
	}
	// Still only where the service's own reader found no identifier: the
	// request may carry the resource's ARN somewhere its own shape nests it,
	// which names the resource outright.
	if arns := iamARNsNamingADeclaredType(r, service, types, region, account); len(arns) > 0 {
		return arns
	}
	// Only where nothing above named a resource: a create names
	// a resource that does not exist yet, and the type wildcard is what AWS
	// evaluates it against. A request that does carry the name — SNS's
	// CreateTopic carries the topic's — derives that above and never reaches
	// here.
	return iamCreateWildcardARNs(service, op, types, region, account)
}

func iamServiceResourceARNs(r *http.Request, service, op string, types []string, region, account string, arn func(string, string) string) []string {
	switch service {
	case "application-autoscaling":
		return iamApplicationAutoScalingResourceARNs(r, types, region, account)
	case "autoscaling":
		return iamAutoScalingResourceARNs(r, types)
	case "cloudtrail":
		return iamCloudTrailResourceARNs(r, types, region, account)
	case "dynamodb":
		return iamDynamoDBResourceARNs(r, types, region, account)
	case "ec2":
		return iamEC2ResourceARNs(r, op, types, region, account)
	case "ecs":
		return iamECSResourceARNs(r, op, types, arn)
	case "elasticache":
		return iamElastiCacheResourceARNs(r, op, types, region, account)
	case "events":
		return iamEventBridgeResourceARNs(r, types, region, account)
	case "firehose":
		return iamFirehoseResourceARNs(r, types, region, account)
	case "glue":
		return iamGlueResourceARNs(r, types, region, account)
	case "kms":
		return iamKMSResourceARNs(r, types, region, account)
	case "logs":
		return iamLogsResourceARNs(r, types, arn)
	case "acm":
		return iamACMResourceARNs(r, types)
	case "acm-pca":
		return iamACMPCAResourceARNs(r, types)
	case "servicediscovery":
		return iamCloudMapResourceARNs(r, types, region, account)
	case "ecr":
		return iamECRResourceARNs(r, types, region, account)
	case "sns":
		return iamSNSResourceARNs(r, types, region, account)
	case "sqs":
		return iamSQSResourceARNs(r, types, region, account)
	case "secretsmanager":
		return iamSecretsManagerResourceARNs(r, types, region, account)
	case "kinesis":
		return iamKinesisResourceARNs(r, types, region, account)
	case "states":
		return iamStatesResourceARNs(r, op, types, region, account)
	case "sts":
		return iamSTSResourceARNs(r, types, account)
	case "cloudwatch":
		return iamCloudWatchResourceARNs(r, types, region, account)
	case "elasticloadbalancing":
		return iamELBv2ResourceARNs(r, types, region, account)
	case "organizations":
		return iamOrganizationsResourceARNs(r, op, types)
	case "budgets":
		return iamBudgetsResourceARNs(r, types, account)
	case "rds":
		return iamRDSResourceARNs(r, op, types, region, account)
	case "ssm":
		return iamSSMResourceARNs(r, types, region, account)
	case "codebuild":
		return iamCodeBuildResourceARNs(r, types, arn)
	case "wafv2":
		return iamWAFv2ResourceARNs(r, op, types)
	case "iam":
		return iamIAMResourceARNs(r, op, types)
	}
	return nil
}

// iamBudgetsResourceARNs derives the ARNs an AWS Budgets request names. The
// service is global — its ARN formats carry no region — and it speaks
// awsJson, so a budget and a budget action are named by the BudgetName and
// ActionId members the published formats declare, spelled exactly as the API
// sends them. Creating an action is the exception: its ActionId is a UUID
// AWS assigns, so the create carries nothing to assemble the action ARN
// from. The tagging operations name their target by ResourceARN, which the
// generic ARN-member reader already resolves.
func iamBudgetsResourceARNs(r *http.Request, types []string, account string) []string {
	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("budgets", types, "", account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamIAMResourceARNs derives the ARNs an IAM request names. IAM is global and its
// ARNs carry no region ("arn:aws:iam::<account>:role/<name>"). A name may carry a
// path ("role/team/name"), which the create takes separately; the resource types
// call it NameWithPath.
func iamIAMResourceARNs(r *http.Request, op string, types []string) []string {
	account := awsAccountID()
	build := func(resourceType, path, name string) string {
		path = strings.Trim(path, "/")
		if path != "" {
			path += "/"
		}
		return "arn:aws:iam::" + account + ":" + resourceType + "/" + path + name
	}
	// The operations that act on a policy, an OIDC provider or a SAML provider
	// name it by ARN outright — and so do the access-advisor reads, which take
	// the entity's own ARN under the bare member "Arn" and declare every entity
	// type it could be. The ARN says which one, so there is nothing to choose.
	for _, field := range []string{"Arn", "PolicyArn", "OpenIDConnectProviderArn", "SAMLProviderArn", "PolicySourceArn"} {
		if v := r.FormValue(field); strings.HasPrefix(v, "arn:") {
			return []string{v}
		}
	}
	path := r.FormValue("Path")
	for _, candidate := range []struct{ resourceType, field string }{
		{"role", "RoleName"},
		{"user", "UserName"},
		{"group", "GroupName"},
		{"instance-profile", "InstanceProfileName"},
		{"server-certificate", "ServerCertificateName"},
		{"policy", "PolicyName"},
		{"mfa", "VirtualMFADeviceName"},
	} {
		if !iamHasType(types, candidate.resourceType) {
			continue
		}
		if name := r.FormValue(candidate.field); name != "" {
			return []string{build(candidate.resourceType, path, name)}
		}
	}
	// An MFA device is named by its serial number, which for a virtual device
	// already is the ARN.
	if iamHasType(types, "mfa") {
		if serial := r.FormValue("SerialNumber"); strings.HasPrefix(serial, "arn:") {
			return []string{serial}
		}
	}
	// Changing a password names nobody: IAM authorizes it against the calling user,
	// which only the signature establishes. That holds because main.go runs SigV4
	// verification before the enforcement gate. A caller that is not a user derives
	// nothing.
	if op == "ChangePassword" && iamHasType(types, "user") && iamAccessKeys != nil {
		if key, ok := iamAccessKeys.Get(iamAccessKeyIDFromRequest(r)); ok && key.UserName != "" {
			return []string{build("user", "", key.UserName)}
		}
	}
	// An access key names its user through the simulator's record of whom it was
	// created for; a key the simulator does not hold names no user.
	if iamHasType(types, "user") {
		for _, id := range []string{r.FormValue("AccessKeyId")} {
			if id == "" || iamAccessKeys == nil {
				continue
			}
			if key, ok := iamAccessKeys.Get(id); ok && key.UserName != "" {
				return []string{build("user", "", key.UserName)}
			}
		}
	}
	// A service-linked role deletion is tracked by a task whose id spells the
	// role it is deleting: "task/aws-service-role/<service>/<roleName>/<uuid>".
	// The role is what the deletion authorizes against, and the id states it.
	if iamHasType(types, "role") {
		if name := iamServiceLinkedRoleFromTask(r.FormValue("DeletionTaskId")); name != "" {
			return []string{"arn:aws:iam::" + account + ":role/" + name}
		}
	}
	// A delegation request is named by its own identifier, which every
	// operation over one carries.
	if iamHasType(types, "delegation-request") {
		if id := r.FormValue("DelegationRequestId"); id != "" {
			return []string{"arn:aws:iam::" + account + ":delegation-request/" + id}
		}
	}
	// A role template belongs to AWS rather than to the account, which is why
	// its ARN says "aws" where an account would be. It is named by the service
	// principal it is for, its own name, and the major version — all three, or
	// the ARN names a different template.
	if iamHasType(types, "role-template") {
		principal := r.FormValue("AWSServicePrincipal")
		name := r.FormValue("RoleTemplateName")
		major := r.FormValue("RoleTemplateMajorVersion")
		if principal != "" && name != "" && major != "" {
			return []string{"arn:aws:iam::aws:role-template/" + principal + "/" + name + ":" + major}
		}
	}
	// An organizations access report is named by the entity it covers, which
	// the request carries as the path of that entity in the organization.
	if iamHasType(types, "access-report") {
		if entity := r.FormValue("EntityPath"); entity != "" {
			return []string{"arn:aws:iam::" + account + ":access-report/" + entity}
		}
	}
	// An OpenID Connect provider is named by the issuer it is created for: the
	// URL without its scheme is the provider's name, which is why the ARN of a
	// provider for https://example.com/oidc ends "oidc-provider/example.com/oidc".
	// The create carries the URL and nothing else that names the provider.
	if iamHasType(types, "oidc-provider") {
		if raw := r.FormValue("Url"); raw != "" {
			if name := iamOIDCProviderName(raw); name != "" {
				return []string{"arn:aws:iam::" + account + ":oidc-provider/" + name}
			}
		}
	}
	return nil
}

// iamOIDCProviderName is the provider name IAM derives from an issuer URL: the
// host and path with the scheme and any trailing slash removed. A URL with no
// host names no provider, and guessing one would authorize against a provider
// the request never mentioned.
func iamOIDCProviderName(raw string) string {
	name := raw
	for _, scheme := range []string{"https://", "http://"} {
		if trimmed, found := strings.CutPrefix(name, scheme); found {
			name = trimmed
			break
		}
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

// iamRequestARNField returns the ARN a request names directly. AWS is not
// consistent about the casing across services (ECS and CloudWatch Logs send
// resourceArn, WAFv2 sends ResourceARN, and WAFv2's association operations
// send the governed resource as ResourceArn), so all the real spellings are
// read.
func iamRequestARNField(r *http.Request) string {
	for _, field := range []string{"resourceArn", "ResourceARN", "ResourceArn", "resourceARN"} {
		if v := iamJSONBodyField(r, field); strings.HasPrefix(v, "arn:") {
			return v
		}
	}
	// The query-protocol services name it as a form parameter instead. Amazon
	// RDS is the one that does so under three spellings: its tagging operations
	// send the ARN as ResourceName, its activity streams as ResourceArn, and its
	// maintenance operations as ResourceIdentifier. Only a value that is an ARN
	// is taken, so a parameter of the same name carrying a bare name elsewhere
	// is left to that service's own derivation.
	for _, field := range []string{"ResourceName", "ResourceArn", "ResourceARN", "ResourceIdentifier"} {
		if v := r.FormValue(field); strings.HasPrefix(v, "arn:") {
			return v
		}
	}
	return ""
}

// iamJSONBodyStrings reads a top-level list-of-strings field from an awsJson
// request body, restoring the body for the handler.
func iamJSONBodyStrings(r *http.Request, field string) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	raw, ok := m[field]
	if !ok {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// iamFirstJSONField returns the first of several alternative field names the
// request carries a value for. Operations within one service name the same
// resource differently (ECS CreateService sends serviceName where
// DescribeServices sends services), so the caller lists the spellings rather
// than the gate carrying a per-operation table.
func iamFirstJSONField(r *http.Request, fields ...string) string {
	for _, f := range fields {
		if v := iamJSONBodyField(r, f); v != "" {
			return v
		}
	}
	return ""
}

// iamNamesFrom collects the identifiers a request carries under any of the
// given singular or plural field names, preserving order and dropping
// duplicates so a batch operation authorizes each distinct resource once.
func iamNamesFrom(r *http.Request, singular []string, plural []string) []string {
	var names []string
	seen := map[string]struct{}{}
	add := func(v string) {
		if v == "" {
			return
		}
		if _, dup := seen[v]; dup {
			return
		}
		seen[v] = struct{}{}
		names = append(names, v)
	}
	for _, f := range singular {
		add(iamJSONBodyField(r, f))
	}
	for _, f := range plural {
		for _, v := range iamJSONBodyStrings(r, f) {
			add(v)
		}
	}
	return names
}

// iamHasType reports whether AWS declares resourceType for the action.
func iamHasType(types []string, resourceType string) bool {
	for _, t := range types {
		if t == resourceType {
			return true
		}
	}
	return false
}

// iamOrganizationsResourceARNs derives the ARNs an AWS Organizations request
// names. Every Organizations id carries a type prefix ("r-" root, "ou-"
// organizational unit, "p-" policy, "h-" handshake, "rp-" resource policy, "rt-"
// responsibility transfer; twelve bare digits an account), and multi-kind
// members are alternations over those prefixes, so read the type off the
// identifier, as AWS does. A policy's type and management, a handshake's
// purpose, a transfer's kind and direction and a resource policy's id come from
// the simulator's state; the rest follow from the identifier alone, which also
// covers a resource that does not exist: AWS authorizes such a call and then
// reports it missing.
func iamOrganizationsResourceARNs(r *http.Request, op string, types []string) []string {
	// Creating a policy mints the id, so wildcard only that segment; the
	// organization and the policy type in the request are known, which is narrower
	// than "*".
	if op == "CreatePolicy" && iamHasType(types, "policy") {
		if typ := iamFirstJSONField(r, "Type"); typ != "" {
			return []string{orgPolicyArn("p-*", typ, false)}
		}
	}

	var out []string
	seen := map[string]struct{}{}
	add := func(arn string) {
		if arn == "" {
			return
		}
		if _, dup := seen[arn]; dup {
			return
		}
		seen[arn] = struct{}{}
		out = append(out, arn)
	}

	for field, values := range iamJSONRequestFields(r) {
		// Every identifier member the model declares ends in "Id", and the
		// members that do not end in it are not identifiers.
		if !strings.HasSuffix(field, "id") {
			continue
		}
		for _, value := range values {
			candidates, arn := iamOrganizationsResource(value)
			if arn == "" {
				continue
			}
			// An action authorizes only against the types AWS declares for it,
			// so an identifier of any other type is not this action's resource.
			for _, candidate := range candidates {
				if iamHasType(types, candidate) {
					add(arn)
					break
				}
			}
		}
	}

	// The organization has exactly one resource policy and PutResourcePolicy
	// names no identifier for it, so the one that exists is the one the request
	// is about. A request made before it exists names an ARN AWS has not
	// assigned yet, which is undivinable rather than "*"-shaped.
	if len(out) == 0 && iamHasType(types, "resourcepolicy") {
		if rp, ok := orgResourcePolicies.Get(orgSingletonKey); ok {
			add(rp.Arn)
		}
	}
	sort.Strings(out)
	return out
}

// iamOrganizationsResource reads an AWS Organizations identifier and returns
// the resource types it could name together with that resource's ARN. The
// types are plural only for a policy, which the reference publishes twice —
// "policy" for a customer's and "awspolicy" for an AWS-managed one — and which
// of the two an identifier names is a property of the policy, not of the
// request.
func iamOrganizationsResource(id string) ([]string, string) {
	switch {
	case strings.HasPrefix(id, "r-"):
		return []string{"root"}, orgRootArn(id)
	case strings.HasPrefix(id, "ou-"):
		return []string{"organizationalunit"}, orgOUArn(id)
	case strings.HasPrefix(id, "rp-"):
		return []string{"resourcepolicy"}, orgResourcePolicyArn(id)
	case strings.HasPrefix(id, "rt-"):
		if t, ok := orgResponsibilityTransfers.Get(id); ok {
			return []string{"responsibilitytransfer"}, t.Arn
		}
	case strings.HasPrefix(id, "p-"):
		if p, ok := orgPolicies.Get(id); ok {
			return []string{"policy", "awspolicy"}, p.Arn
		}
	case strings.HasPrefix(id, "h-"):
		if h, ok := orgHandshakes.Get(id); ok {
			return []string{"handshake"}, h.Arn
		}
	case iamOrganizationsAccountID.MatchString(id):
		return []string{"account"}, orgAccountArn(id)
	}
	return nil, ""
}

// iamOrganizationsAccountID matches the one identifier Organizations spells
// without a prefix, which the model declares as exactly twelve digits.
var iamOrganizationsAccountID = regexp.MustCompile(`^\d{12}$`)

// iamACMPCAResourceARNs derives the ARNs an AWS Private Certificate Authority
// request names. A certificate authority's ARN carries an identifier AWS
// assigned, which no request supplies as a part — what a request carries is the
// whole ARN, so there is nothing to assemble.
func iamACMPCAResourceARNs(r *http.Request, types []string) []string {
	fields := iamJSONRequestFields(r)
	for _, field := range []string{"certificateauthorityarn", "resourcearn"} {
		for _, value := range fields[field] {
			if strings.HasPrefix(value, "arn:") {
				return []string{value}
			}
		}
	}
	return nil
}

// iamCloudMapResourceARNs derives the ARNs an AWS Cloud Map request names. A
// namespace and a service are each addressed by an assigned id, which arrives
// under the resource's own member — NamespaceId, ServiceId — or simply as Id on
// the operations that act on one resource and need no qualifier.
func iamCloudMapResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	// The tagging operations name their target by ARN, and which of the two
	// types it is is what the ARN says.
	for _, value := range fields["resourcearn"] {
		if strings.HasPrefix(value, "arn:") {
			return []string{value}
		}
	}
	if arns := iamTableDrivenARNs("servicediscovery", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] }); len(arns) > 0 {
		return arns
	}
	// GetOperation names only the operation's own id; which namespace or
	// service the operation acted on is in the operation record, the
	// simulator's own state, and the record's ARNs are the ones the resources
	// were actually given.
	if opID := iamFirstValue(func(f string) []string { return fields[strings.ToLower(f)] }, "OperationId"); opID != "" {
		if op, ok := cmOperations.Get(opID); ok {
			var out []string
			if op.NamespaceId != "" {
				if namespace, ok := cmNamespaces.Get(op.NamespaceId); ok && namespace.Arn != "" {
					out = append(out, namespace.Arn)
				}
			}
			if op.ServiceId != "" {
				if service, ok := cmServices.Get(op.ServiceId); ok && service.Arn != "" {
					out = append(out, service.Arn)
				}
			}
			if len(out) > 0 {
				sort.Strings(out)
				return out
			}
		}
	}
	// The discovery operations name a namespace and a service, while their ARNs
	// carry assigned ids, so read both from the simulator's state: the gate must
	// request the ARN the resource has.
	var out []string
	namespaceName := iamFirstValue(func(f string) []string { return fields[strings.ToLower(f)] }, "NamespaceName")
	serviceName := iamFirstValue(func(f string) []string { return fields[strings.ToLower(f)] }, "ServiceName")
	var namespaceID string
	if namespaceName != "" {
		if namespace, ok := cmNamespacesByName.Lookup(cmNamespaces, namespaceName, func(n CMNamespace) []string {
			return []string{n.Name}
		}); ok {
			namespaceID = namespace.Id
			if namespace.Arn != "" {
				out = append(out, namespace.Arn)
			}
		}
	}
	if serviceName != "" {
		for _, service := range cmServicesByName.LookupAll(cmServices, serviceName, func(s CMService) []string {
			return []string{s.Name}
		}) {
			if namespaceID != "" && service.NamespaceId != namespaceID {
				continue
			}
			if service.Arn != "" {
				out = append(out, service.Arn)
			}
			break
		}
	}
	sort.Strings(out)
	return out
}

var (
	cmNamespacesByName sim.GenerationIndex[CMNamespace]
	cmServicesByName   sim.GenerationIndex[CMService]
)

// iamSNSResourceARNs derives the topic ARNs an Amazon SNS request names. A
// topic is addressed by its ARN on every operation but creation, where it is
// named — and a topic ARN ends in the name itself, so the one can be built from
// the other.
func iamSNSResourceARNs(r *http.Request, types []string, region, account string) []string {
	if !iamHasType(types, "topic") {
		return nil
	}
	params := iamQueryRequestParameters(r)
	first := func(field string) string {
		return iamFirstValue(func(f string) []string { return params[strings.ToLower(f)] }, field)
	}
	// The tagging and data-protection operations name the topic under
	// ResourceArn instead.
	for _, field := range []string{"TopicArn", "ResourceArn"} {
		if a := first(field); strings.HasPrefix(a, "arn:") {
			return []string{a}
		}
	}
	// A subscription is addressed by its own ARN, which is the topic's with the
	// subscription id appended — and the reference declares the topic as what
	// these authorize against, so the topic is what the gate asks for.
	if a := first("SubscriptionArn"); strings.HasPrefix(a, "arn:") {
		if i := strings.LastIndex(a, ":"); i > len("arn:aws:sns:") {
			return []string{a[:i]}
		}
	}
	if name := first("Name"); name != "" {
		return []string{"arn:aws:sns:" + region + ":" + account + ":" + name}
	}
	return nil
}

// iamSQSResourceARNs derives the queue ARNs an Amazon SQS request names. A
// queue is addressed by its URL rather than by its ARN or its name, and the
// name is the URL's last segment — which is what the queue's ARN ends in.
func iamSQSResourceARNs(r *http.Request, types []string, region, account string) []string {
	if !iamHasType(types, "queue") {
		return nil
	}
	params := iamQueryRequestParameters(r)
	// Amazon SQS speaks both the awsQuery and the awsJson protocol, and a
	// JSON request carries its parameters in the body rather than the form.
	jsonProtocol := r.Header.Get("X-Amz-Target") != ""
	first := func(field string) string {
		if jsonProtocol {
			return iamJSONBodyField(r, field)
		}
		return iamFirstValue(func(f string) []string { return params[strings.ToLower(f)] }, field)
	}
	// The message-move operations name their queues by ARN rather than by URL,
	// and a move is a call about both ends of it.
	var moved []string
	for _, field := range []string{"SourceArn", "DestinationArn"} {
		if a := first(field); strings.HasPrefix(a, "arn:") {
			moved = append(moved, a)
		}
	}
	if len(moved) > 0 {
		return moved
	}
	// Cancelling a move names only its handle; the task record says which queue the
	// task drains, and the reference authorizes the cancel against that source
	// queue.
	if handle := first("TaskHandle"); handle != "" {
		if task, ok := sqsMoveTasks.Get(handle); ok && strings.HasPrefix(task.SourceArn, "arn:") {
			return []string{task.SourceArn}
		}
	}
	name := first("QueueName")
	if url := first("QueueUrl"); url != "" {
		if i := strings.LastIndex(url, "/"); i >= 0 {
			name = url[i+1:]
		}
	}
	if name == "" {
		return nil
	}
	return []string{"arn:aws:sqs:" + region + ":" + account + ":" + name}
}

// iamSecretsManagerResourceARNs derives the secret ARNs an AWS Secrets Manager
// request names: SecretId, which accepts a name or a full ARN, or Name at
// creation, before SecretId exists.
func iamSecretsManagerResourceARNs(r *http.Request, types []string, region, account string) []string {
	if !iamHasType(types, "Secret") {
		return nil
	}
	fields := iamJSONRequestFields(r)
	for _, field := range []string{"secretid", "name"} {
		for _, value := range fields[field] {
			if value == "" {
				continue
			}
			if strings.HasPrefix(value, "arn:") {
				return []string{value}
			}
			return []string{"arn:aws:secretsmanager:" + region + ":" + account + ":secret:" + value}
		}
	}
	return nil
}

// iamECRResourceARNs derives the repository ARNs an Amazon ECR request names.
// A repository is addressed by name — repositoryName on the per-repository
// operations, repositoryNames on the filters and batches — and AWS authorizes
// every one a request names, so a filtered call must be allowed for all of
// them. Registry-wide calls that name none (GetAuthorizationToken, an
// unfiltered DescribeRepositories, the registry policy and replication
// operations) keep the account-level "*".
func iamECRResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	// The tagging operations name the repository by ARN instead, under a
	// member whose spelling varies by service; reading it here rather than by
	// exact key keeps the derivation independent of that.
	var tagged []string
	for _, value := range fields["resourcearn"] {
		if strings.HasPrefix(value, "arn:") {
			tagged = append(tagged, value)
		}
	}
	if len(tagged) > 0 {
		return tagged
	}
	return iamTableDrivenARNs("ecr", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamKinesisResourceARNs derives the ARNs an Amazon Kinesis request names. A
// stream is addressed either by name or by its ARN, and a consumer only by ARN:
// a consumer's own ARN ends in the timestamp at which it was registered, which
// no request supplies and nothing can reconstruct.
func iamKinesisResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	var out []string
	seen := map[string]struct{}{}
	add := func(arn string) {
		if arn == "" {
			return
		}
		if _, dup := seen[arn]; dup {
			return
		}
		seen[arn] = struct{}{}
		out = append(out, arn)
	}
	// The tagging and resource-policy operations name their target by ARN under
	// ResourceARN, and which of the two types it is is what the ARN says.
	for _, field := range []string{"streamarn", "consumerarn", "resourcearn", "keyid"} {
		for _, value := range fields[field] {
			if strings.HasPrefix(value, "arn:") {
				add(value)
			}
		}
	}
	if iamHasType(types, "stream") {
		for _, name := range fields["streamname"] {
			if name != "" && !strings.HasPrefix(name, "arn:") {
				add("arn:aws:kinesis:" + region + ":" + account + ":stream/" + name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// iamStatesResourceARNs derives the ARNs an AWS Step Functions request names.
// Every resource is addressed by its own ARN, which carries parts no request
// supplies separately, so authorize against the ARN the caller sent. Creating a
// state machine or an activity still derives, since both formats end in the name
// the request supplies.
func iamStatesResourceARNs(r *http.Request, op string, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	switch op {
	case "CreateStateMachineAlias":
		// Creating an alias authorizes against the state machine its routing names
		// inside the version ARNs; a version ARN is the machine's with the version
		// appended. Wildcarding the machine would authorize aliasing every state
		// machine.
		var machines []string
		seen := map[string]struct{}{}
		for _, version := range fields["statemachineversionarn"] {
			machine, _, versioned := iamStatesMachineOfVersion(version)
			if !versioned {
				continue
			}
			if _, dup := seen[machine]; dup {
				continue
			}
			seen[machine] = struct{}{}
			machines = append(machines, machine)
		}
		if len(machines) > 0 {
			sort.Strings(machines)
			return machines
		}
	case "CreateStateMachine", "CreateActivity":
		segment := "stateMachine:"
		if op == "CreateActivity" {
			segment = "activity:"
		}
		var created []string
		for _, name := range fields["name"] {
			if name != "" && !strings.HasPrefix(name, "arn:") {
				created = append(created, "arn:aws:states:"+region+":"+account+":"+segment+name)
			}
		}
		if len(created) > 0 {
			sort.Strings(created)
			return created
		}
	}
	var out []string
	seen := map[string]struct{}{}
	for _, field := range []string{
		"statemachinearn", "executionarn", "activityarn", "maprunarn",
		"statemachineversionarn", "statemachinealiasarn", "resourcearn",
	} {
		for _, value := range fields[field] {
			if !strings.HasPrefix(value, "arn:") {
				continue
			}
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// iamSTSResourceARNs derives the ARNs an AWS STS request names. The assume-role
// family names the role by ARN; AssumeRoot names the member account as twelve
// digits for "arn:aws:iam::${Account}:root"; GetFederationToken names the
// federated user, in the caller's account.
func iamSTSResourceARNs(r *http.Request, types []string, account string) []string {
	params := iamQueryRequestParameters(r)
	first := func(field string) string {
		return iamFirstValue(func(f string) []string { return params[strings.ToLower(f)] }, field)
	}
	if iamHasType(types, "role") {
		if a := first("RoleArn"); strings.HasPrefix(a, "arn:") {
			return []string{a}
		}
	}
	if iamHasType(types, "root-user") {
		// The same bare-twelve-digits shape AWS Organizations spells an account
		// as; anything else is rejected by the service before authorization.
		if target := first("TargetPrincipal"); iamOrganizationsAccountID.MatchString(target) {
			return []string{"arn:aws:iam::" + target + ":root"}
		}
	}
	if iamHasType(types, "federated-user") {
		if name := first("Name"); name != "" {
			return []string{"arn:aws:sts::" + account + ":federated-user/" + name}
		}
	}
	return nil
}

// iamELBv2ResourceARNs derives the ARNs an Elastic Load Balancing request names.
// Each ARN carries an id AWS assigned, so a request carries the whole ARN and the
// gate authorizes against it. The classic load balancer, addressed by name
// alone, is the exception.
func iamELBv2ResourceARNs(r *http.Request, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	if arns := iamELBv2CreatedResourceARNs(params, region, account); len(arns) > 0 {
		return arns
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(arn string) {
		if arn == "" {
			return
		}
		if _, dup := seen[arn]; dup {
			return
		}
		seen[arn] = struct{}{}
		out = append(out, arn)
	}
	// A member is read whatever its type, because AWS authorizes every resource
	// a request names: DescribeTargetGroups filtered by load balancer is a call
	// about both.
	for _, field := range []string{
		"loadbalancerarn", "loadbalancerarns",
		"targetgrouparn", "targetgrouparns",
		"listenerarn", "listenerarns",
		"rulearn", "rulearns",
		"truststorearn", "truststorearns",
		"resourcearn", "resourcearns",
	} {
		for _, value := range params[field] {
			if strings.HasPrefix(value, "arn:") {
				add(value)
			}
		}
	}
	// SetRulePriorities nests each rule's ARN in a priority entry
	// ("RulePriorities.member.1.RuleArn"), which the flat-member reader drops, so
	// read the raw form for rule ARNs.
	_ = r.ParseForm()
	for key, values := range r.Form {
		if !strings.HasSuffix(strings.ToLower(key), "rulearn") {
			continue
		}
		for _, value := range values {
			if strings.HasPrefix(value, "arn:") {
				add(value)
			}
		}
	}
	// The classic load balancer is named rather than addressed by ARN.
	if iamHasType(types, "loadbalancer") {
		for _, field := range []string{"loadbalancername", "loadbalancernames"} {
			for _, name := range params[field] {
				if name != "" && !strings.HasPrefix(name, "arn:") {
					add("arn:aws:elasticloadbalancing:" + region + ":" + account + ":loadbalancer/" + name)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// iamACMResourceARNs derives the ARNs an AWS Certificate Manager request names.
// Its resources are addressed by ARN — a certificate's own, an ACME endpoint's,
// and the validations and account bindings nested under an endpoint — so the
// ARN the caller sent is the one to authorize against.
func iamACMResourceARNs(r *http.Request, types []string) []string {
	fields := iamJSONRequestFields(r)
	var out []string
	seen := map[string]struct{}{}
	// The tagging operations name their target by ARN under ResourceArn, and
	// the reference lists every taggable type for them — which of those the ARN
	// names is what the ARN itself says.
	for _, field := range []string{
		"certificatearn", "acmeendpointarn",
		"acmedomainvalidationarn", "acmeexternalaccountbindingarn",
		"resourcearn",
	} {
		for _, value := range fields[field] {
			if !strings.HasPrefix(value, "arn:") {
				continue
			}
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

// iamCloudWatchResourceARNs derives the ARNs an Amazon CloudWatch request
// names. Its resources are addressed by name, and each type's name arrives
// under a member of its own — an alarm's AlarmName, a dashboard's
// DashboardName, an insight rule's RuleName — so filling the published format
// is all it takes.
func iamCloudWatchResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamCloudWatchRequestFields(r)
	// Listing an alarm's mute rules names the alarm, and the rules on it are
	// what the listing authorizes against — each rule records the alarms it
	// covers, so the rules are recovered through an index keyed on them rather
	// than by reading every rule. An alarm nothing mutes has no rule to
	// authorize, which is not the same as authorizing every rule.
	if iamHasType(types, "alarm-mute-rule") && cwAlarmMuteRules != nil {
		var out []string
		for _, alarm := range fields["alarmname"] {
			for _, rule := range iamCloudWatchMuteRulesByAlarm.LookupAll(
				cwAlarmMuteRules, alarm, iamCloudWatchMuteRuleAlarmKeys) {
				if rule.Name != "" {
					out = append(out, "arn:aws:cloudwatch:"+region+":"+account+
						":alarm-mute-rule:"+rule.Name)
				}
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}
	// The tagging operations name their target by ARN and the reference lists
	// every taggable type for them; which one the call is about is what the ARN
	// says, so there is nothing to fill.
	var tagged []string
	for _, value := range fields["resourcearn"] {
		if strings.HasPrefix(value, "arn:") {
			tagged = append(tagged, value)
		}
	}
	if len(tagged) > 0 {
		return tagged
	}
	return iamTableDrivenARNs("cloudwatch", types, region, account, iamCloudWatchFieldAliases,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamCloudWatchRequestFields reads a request's top-level string and
// string-list members by lower-cased name, from the RPC v2 CBOR body the Go
// SDK sends or the JSON body other clients send.
func iamCloudWatchRequestFields(r *http.Request) map[string][]string {
	document := iamCBORRequestDocument(r)
	if document == nil {
		return iamJSONRequestFields(r)
	}
	fields := map[string][]string{}
	for name, value := range document {
		switch value := value.(type) {
		case string:
			fields[strings.ToLower(name)] = []string{value}
		case []any:
			for _, item := range value {
				if s, ok := item.(string); ok {
					fields[strings.ToLower(name)] = append(fields[strings.ToLower(name)], s)
				}
			}
		}
	}
	return fields
}

// iamCloudWatchFieldAliases records where the API's spelling differs from the
// reference's variable, read off the vendored model rather than guessed: an
// insight rule's ${InsightRuleName} arrives as RuleName, and a metric stream's
// ${MetricStreamName} simply as Name.
var iamCloudWatchFieldAliases = map[string][]string{
	"InsightRuleName":  {"RuleName"},
	"MetricStreamName": {"Name"},
	"DatasetId":        {"DatasetIdentifier"},
}

// iamApplicationAutoScalingResourceARNs derives the scalable-target ARNs an
// Application Auto Scaling request names. The service declares one resource
// type and publishes its ARN as "scalable-target/${ResourceId}" — ${ResourceId}
// being the caller-chosen identifier like "service/default/web" that every
// scaling operation sends under the member of the same name — so filling the
// published format is all it takes. The tagging operations name the target by
// its ARN instead — spelled ResourceARN — which needs no assembly; reading it
// here by lower-cased key rather than by exact spelling keeps the derivation
// independent of that, as Amazon ECR's does.
func iamApplicationAutoScalingResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	for _, value := range fields["resourcearn"] {
		if strings.HasPrefix(value, "arn:") {
			return []string{value}
		}
	}
	return iamTableDrivenARNs("application-autoscaling", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamAutoScalingResourceARNs derives the ARNs an Amazon EC2 Auto Scaling request
// names. Both resource types carry an assigned id and a caller-chosen name
// ("autoScalingGroup:${GroupId}:autoScalingGroupName/${GroupFriendlyName}"), and a
// request supplies only the name, so read the stored ARN from the simulator's
// state.
func iamAutoScalingResourceARNs(r *http.Request, types []string) []string {
	params := iamQueryRequestParameters(r)
	first := func(field string) string {
		return iamFirstValue(func(f string) []string { return params[strings.ToLower(f)] }, field)
	}

	var out []string
	for _, resourceType := range types {
		switch resourceType {
		case "autoScalingGroup":
			if group, ok := autoScalingGroups.Get(first("AutoScalingGroupName")); ok && group.ARN != "" {
				out = append(out, group.ARN)
			}
			// Setting an instance's health names the machine, and the group
			// it belongs to is what the call authorizes against. The group
			// records the machines it holds, so it is recovered through an
			// index keyed on them rather than by reading every group.
			for _, instance := range params["instanceid"] {
				if group, ok := iamAutoScalingGroupsByInstance.Lookup(
					autoScalingGroups, instance, iamAutoScalingGroupInstanceKeys); ok && group.ARN != "" {
					out = append(out, group.ARN)
				}
			}
			// The tagging calls name the group inside a tag ("Tags.member.1.ResourceId"),
			// which the flat-parameter reader drops, so read the raw form; a call tagging two
			// groups is about both.
			for _, named := range iamAutoScalingTaggedGroups(r) {
				if group, ok := autoScalingGroups.Get(named); ok && group.ARN != "" {
					out = append(out, group.ARN)
				}
			}
		case "launchConfiguration":
			if config, ok := asLaunchConfigurations.Get(first("LaunchConfigurationName")); ok && config.ARN != "" {
				out = append(out, config.ARN)
			}
		}
	}
	return out
}

// iamCloudTrailResourceARNs derives the ARNs an AWS CloudTrail request names.
// CloudTrail publishes three of its four identifiers under names the API does
// not use — a channel is addressed as Channel, an event data store as
// EventDataStore, a dashboard as DashboardId — so those are aliases. A trail is
// the exception: its ${TrailName} arrives as Name, which the prefix drop
// resolves.
//
// The channel and event-data-store members carry an ARN as often as an
// identifier, and an ARN needs no assembly, so it is taken as it stands.
func iamCloudTrailResourceARNs(r *http.Request, types []string, region, account string) []string {
	// A Lake query names the event data stores it reads in its own statement —
	// the first after FROM, any others after JOIN — and those stores are what
	// running the query authorizes against. Only a token shaped like a store's identifier is
	// taken: a FROM naming anything else is not a store this call reads, and
	// authorizing it would grant past what the query asked for.
	if iamHasType(types, "eventdatastore") {
		if statement := iamJSONBodyField(r, "QueryStatement"); statement != "" {
			var out []string
			seen := map[string]struct{}{}
			for _, match := range iamCloudTrailQueryFrom.FindAllStringSubmatch(statement, -1) {
				id := strings.ToLower(match[1])
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				out = append(out, "arn:aws:cloudtrail:"+region+":"+account+":eventdatastore/"+id)
			}
			if len(out) > 0 {
				sort.Strings(out)
				return out
			}
		}
	}

	fields := iamJSONRequestFields(r)
	// The tagging operations name their target as ResourceId, and ListTags as
	// ResourceIdList — each an ARN despite the name — and an ARN needs no
	// assembly.
	var tagged []string
	for _, member := range []string{"resourceid", "resourceidlist"} {
		for _, value := range fields[member] {
			if strings.HasPrefix(value, "arn:") {
				tagged = append(tagged, value)
			}
		}
	}
	if len(tagged) > 0 {
		sort.Strings(tagged)
		return tagged
	}
	return iamTableDrivenARNs("cloudtrail", types, region, account, iamCloudTrailFieldAliases,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamCloudTrailFieldAliases maps an ARN format's identifier variable to the
// request member AWS CloudTrail spells it as.
//
// TestIAMCloudTrailFieldAliasesAreRealRequestMembers holds every entry to a
// member the vendored model declares on an operation authorizing against that
// resource type.
var iamCloudTrailFieldAliases = map[string][]string{
	"ChannelId": {"Channel"},
	// Insights are read from the trail that produced them, which the request
	// names as the source — by name or by ARN.
	"TrailName":        {"Name", "InsightSource"},
	"EventDataStoreId": {"EventDataStore"},
	"DashboardName":    {"DashboardId"},
}

// iamDynamoDBResourceARNs derives the ARNs an Amazon DynamoDB request names. The
// index-like types nest under a table and the table-driven derivation fills
// them; a backup, export, import and stream are named by their own ARN, as the
// export and import family names the table (TableArn). Transactions and batches
// carry tables per item and are read in iamResourceARNsForRequest.
func iamDynamoDBResourceARNs(r *http.Request, types []string, region, account string) []string {
	// The operations that act on a backup, an export, an import or a stream
	// name it by ARN outright, as the export and import family does the table.
	for _, field := range []string{"BackupArn", "ExportArn", "ImportArn", "TableArn"} {
		if a := iamJSONBodyField(r, field); strings.HasPrefix(a, "arn:") {
			return []string{a}
		}
	}
	// A batch names its tables as RequestItems keys, which no member-value reader
	// finds. DynamoDB authorizes each table the batch touches.
	if iamHasType(types, "table") {
		if tables := iamJSONObjectKeys(r, "RequestItems"); len(tables) > 0 {
			out := make([]string, 0, len(tables))
			for _, table := range tables {
				out = append(out, "arn:aws:dynamodb:"+region+":"+account+":table/"+table)
			}
			sort.Strings(out)
			return out
		}
	}

	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("dynamodb", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamJSONObjectKeys reads the keys of a top-level object member. A service that
// keys a map by the resource — DynamoDB's RequestItems by table name — puts the
// identifier there and nowhere else.
func iamJSONObjectKeys(r *http.Request, field string) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nil
	}
	raw, present := envelope[field]
	if !present {
		return nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// iamEC2ResourceARNs derives the ARNs an Amazon EC2 request names, driven by the
// reference itself: EC2 declares 112 resource types across 515 actions. Each
// published ARN format ends in the variable naming its identifier
// ("...:volume/${VolumeId}"), which the query protocol carries in a parameter of
// the same name. Filling the format keeps the irregular shapes right: an AMI and
// a snapshot carry no account, the Amazon VPC IP Address Manager types no
// region, and some types are another service's ARN. EC2 drops the resource's
// leading word from some parameters (${SecurityGroupId} arrives as GroupId); the
// genuine renamings are in iamEC2ParameterAliases.
func iamEC2ResourceARNs(r *http.Request, op string, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	lookup := func(field string) []string { return params[strings.ToLower(field)] }

	// The tag operations carry ids of mixed types under one member, and EC2 ids state
	// their type in their prefix (i-, vol-, tgw-attach-): the longest prefix wins, an
	// unknown prefix derives nothing, and each id authorizes against its own ARN.
	//
	// The Disassociate/Detach family names an association, and the reference
	// authorizes the parent it belongs to. Resolve the parent through a
	// generation-keyed index over the simulator's state, since this runs per request
	// and the association lives inside its parent's row.
	switch op {
	case "DisassociateRouteTable":
		var out []string
		for _, assoc := range lookup("AssociationId") {
			if rtb, ok := iamEC2RouteTablesByAssociation.Lookup(ec2RouteTables, assoc, iamEC2RouteTableAssociationKeys); ok {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":route-table/"+rtb.RouteTableId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DeleteVpcEndpointConnectionNotifications", "ModifyVpcEndpointConnectionNotification":
		// A notification names neither of the things the action declares — the
		// endpoint and the service it is on — so the record it is keyed by is
		// read for them. Each contributes only if the action declares it: a
		// notification on a service is not what an endpoint-scoped grant
		// covers.
		var out []string
		for _, id := range lookup("ConnectionNotificationId") {
			if ec2ConnNotifications == nil {
				break
			}
			notification, ok := ec2ConnNotifications.Get(id)
			if !ok {
				continue
			}
			if notification.VpcEndpointId != "" && iamHasType(types, "vpc-endpoint") {
				out = append(out, "arn:aws:ec2:"+region+":"+account+
					":vpc-endpoint/"+notification.VpcEndpointId)
			}
			if notification.ServiceId != "" && iamHasType(types, "vpc-endpoint-service") {
				out = append(out, "arn:aws:ec2:"+region+":"+account+
					":vpc-endpoint-service/"+notification.ServiceId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "ModifyInstanceCreditSpecification":
		// InstanceCreditSpecification nests the machines
		// ("InstanceCreditSpecification.1.InstanceId"), which the flat reader drops. Read
		// the raw form for this operation only: loosening the shared reader would make
		// filter members derive too.
		var out []string
		for _, instance := range iamEC2NestedMembers(r, "InstanceCreditSpecification", "InstanceId") {
			out = append(out, "arn:aws:ec2:"+region+":"+account+":instance/"+instance)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "AcceptAddressTransfer":
		// The transfer is accepted by naming the address itself, and the
		// elastic IP that address belongs to is what the call authorizes
		// against. The address is not the ARN's identifier — the allocation is
		// — so the record is recovered through an index keyed on the address.
		var out []string
		for _, address := range lookup("Address") {
			if held, ok := iamEC2ElasticIPsByAddress.Lookup(
				ec2ElasticIPs, address, iamEC2ElasticIPAddressKeys); ok && held.AllocationId != "" {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":elastic-ip/"+held.AllocationId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DisassociateAddress":
		var out []string
		for _, assoc := range lookup("AssociationId") {
			if address, ok := iamEC2ElasticIPsByAssociation.Lookup(ec2ElasticIPs, assoc, iamEC2ElasticIPAssociationKeys); ok {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":elastic-ip/"+address.AllocationId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DisassociateIamInstanceProfile", "ReplaceIamInstanceProfileAssociation":
		// The association record is keyed by the id the request carries and
		// names the machine the profile is bound to, which is what the call
		// authorizes against — so no index is needed, the store answers by key.
		var out []string
		for _, assoc := range lookup("AssociationId") {
			if ec2IamInstanceProfileAssocs == nil {
				break
			}
			if held, ok := ec2IamInstanceProfileAssocs.Get(assoc); ok && held.InstanceId != "" {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":instance/"+held.InstanceId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DisassociateVpcCidrBlock":
		var out []string
		for _, assoc := range lookup("AssociationId") {
			if ec2VpcCidrAssocs == nil {
				break
			}
			held, ok := ec2VpcCidrAssocs.Get(assoc)
			if !ok {
				continue
			}
			resourceType, known := iamEC2TypeForIDPrefix(held.VpcId)
			if !known || !iamHasType(types, resourceType) {
				continue
			}
			out = append(out, "arn:aws:ec2:"+region+":"+account+":"+resourceType+"/"+held.VpcId)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DisassociateSubnetCidrBlock":
		var out []string
		for _, assoc := range lookup("AssociationId") {
			if subnetID, ok := ec2SubnetOfIpv6Association(assoc); ok && iamHasType(types, "subnet") {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":subnet/"+subnetID)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DeleteNetworkInterfacePermission":
		// A permission record names the interface it was granted on, and the
		// interface is what revoking it authorizes against.
		var out []string
		for _, id := range lookup("NetworkInterfacePermissionId") {
			if ec2EniPermissions == nil {
				break
			}
			if perm, ok := ec2EniPermissions.Get(id); ok && perm.NetworkInterfaceId != "" {
				out = append(out, "arn:aws:ec2:"+region+":"+account+
					":network-interface/"+perm.NetworkInterfaceId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	case "DetachNetworkInterface":
		var out []string
		for _, attachment := range lookup("AttachmentId") {
			if eni, ok := iamEC2InterfacesByAttachment.Lookup(ec2NetworkInterfaces, attachment, iamEC2InterfaceAttachmentKeys); ok {
				out = append(out, "arn:aws:ec2:"+region+":"+account+":network-interface/"+eni.NetworkInterfaceId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}

	// Cancelling an import names the task by an id whose prefix says which of
	// the two kinds it is — import-ami- an image import, import-snap- a
	// snapshot import — so the same convention the tag operations read picks
	// between the two types the action declares.
	if op == "CancelImportTask" {
		var out []string
		for _, id := range lookup("ImportTaskId") {
			resourceType, known := iamEC2TypeForIDPrefix(id)
			if !known || !iamHasType(types, resourceType) {
				continue
			}
			out = append(out, "arn:aws:ec2:"+region+":"+account+":"+resourceType+"/"+id)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}

	if op == "CreateTags" || op == "DeleteTags" {
		var out []string
		for _, id := range lookup("ResourceId") {
			resourceType, known := iamEC2TypeForIDPrefix(id)
			if !known {
				continue
			}
			format, declared := iamResourceARNFormats["ec2:"+resourceType]
			if !declared {
				continue
			}
			out = append(out, iamFillARNFormat(format, region, account, []string{id}))
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}
	return iamTableDrivenARNs("ec2", types, region, account, iamEC2ParameterAliases, lookup)
}

// The association-to-parent lookups the Disassociate/Detach family needs,
// answered from generation-keyed indexes so a per-request derivation never
// decodes a whole store.
var (
	iamEC2RouteTablesByAssociation sim.GenerationIndex[EC2RouteTable]
	iamEC2ElasticIPsByAssociation  sim.GenerationIndex[EC2ElasticIP]
	iamEC2InterfacesByAttachment   sim.GenerationIndex[EC2NetworkInterface]
	iamEC2ElasticIPsByAddress      sim.GenerationIndex[EC2ElasticIP]

	// AWS Systems Manager names a maintenance window's execution by an id
	// derived from the window's own, so the window is recoverable from it —
	// but only by asking every window what its execution id would be, which a
	// per-request derivation must not do a store-read at a time.
	iamSSMWindowsByExecution sim.GenerationIndex[SSMMaintenanceWindow]
)

// iamSSMWindowExecutionKeys names a window by the execution id it answers to.
func iamSSMWindowExecutionKeys(window SSMMaintenanceWindow) []string {
	if window.WindowId == "" {
		return nil
	}
	return []string{ssmWindowExecID(window.WindowId)}
}

func iamEC2RouteTableAssociationKeys(rtb EC2RouteTable) []string {
	keys := make([]string, 0, len(rtb.Associations))
	for _, association := range rtb.Associations {
		if association.AssociationId != "" {
			keys = append(keys, association.AssociationId)
		}
	}
	return keys
}

func iamEC2ElasticIPAssociationKeys(address EC2ElasticIP) []string {
	if address.AssociationId == "" {
		return nil
	}
	return []string{address.AssociationId}
}

func iamEC2InterfaceAttachmentKeys(eni EC2NetworkInterface) []string {
	if eni.AttachmentId == "" {
		return nil
	}
	return []string{eni.AttachmentId}
}

// iamEC2IDPrefixTypes maps Amazon EC2's identifier prefixes to the resource
// types the reference declares. Longer prefixes are matched first, so
// tgw-attach- resolves before tgw- and vpce-svc- before vpce-.
var iamEC2IDPrefixTypes = map[string]string{
	"i-":           "instance",
	"vol-":         "volume",
	"snap-":        "snapshot",
	"ami-":         "image",
	"sg-":          "security-group",
	"sgr-":         "security-group-rule",
	"subnet-":      "subnet",
	"vpc-":         "vpc",
	"rtb-":         "route-table",
	"igw-":         "internet-gateway",
	"eigw-":        "egress-only-internet-gateway",
	"acl-":         "network-acl",
	"eni-":         "network-interface",
	"dopt-":        "dhcp-options",
	"eipalloc-":    "elastic-ip",
	"lt-":          "launch-template",
	"nat-":         "natgateway",
	"pcx-":         "vpc-peering-connection",
	"vpce-":        "vpc-endpoint",
	"vpce-svc-":    "vpc-endpoint-service",
	"tgw-":         "transit-gateway",
	"tgw-attach-":  "transit-gateway-attachment",
	"tgw-rtb-":     "transit-gateway-route-table",
	"cgw-":         "customer-gateway",
	"vgw-":         "vpn-gateway",
	"vpn-":         "vpn-connection",
	"fl-":          "vpc-flow-log",
	"ipam-":        "ipam",
	"ipam-pool-":   "ipam-pool",
	"ipam-scope-":  "ipam-scope",
	"key-":         "key-pair",
	"pl-":          "prefix-list",
	"cr-":          "capacity-reservation",
	"crf-":         "capacity-reservation-fleet",
	"fleet-":       "fleet",
	"sfr-":         "spot-fleet-request",
	"sir-":         "spot-instances-request",
	"h-":           "dedicated-host",
	"fpga-":        "fpga-image",
	"tmf-":         "traffic-mirror-filter",
	"tms-":         "traffic-mirror-session",
	"tmt-":         "traffic-mirror-target",
	"iew-":         "instance-event-window",
	"vai-":         "verified-access-instance",
	"vagrp-":       "verified-access-group",
	"vae-":         "verified-access-endpoint",
	"vatp-":        "verified-access-trust-provider",
	"import-ami-":  "import-image-task",
	"import-snap-": "import-snapshot-task",
}

// iamEC2TypeForIDPrefix resolves an identifier to its declared resource type
// by its longest matching prefix.
func iamEC2TypeForIDPrefix(id string) (string, bool) {
	bestType, bestLen := "", 0
	for prefix, resourceType := range iamEC2IDPrefixTypes {
		if strings.HasPrefix(id, prefix) && len(prefix) > bestLen {
			bestType, bestLen = resourceType, len(prefix)
		}
	}
	return bestType, bestLen > 0
}

// iamEC2ParameterAliases maps an ARN format variable to the parameters EC2 spells
// it as, where they differ by more than the prefix drop: renames the API made and
// the reference did not follow, and the copy operations' source resources.
// TestIAMEC2ParameterAliasesAreRealRequestParameters holds every entry to a
// parameter the vendored model declares.
var iamEC2ParameterAliases = map[string][]string{
	"CapacityReservationId": {"SourceCapacityReservationId"},
	// Buying a host reservation names the dedicated hosts it covers, as a set
	// rather than one id.
	"DedicatedHostId":                 {"HostIdSet", "HostId"},
	"CertificateId":                   {"CertificateArn"},
	"DeclarativePoliciesReportId":     {"ReportId"},
	"FpgaImageId":                     {"SourceFpgaImageId"},
	"ImageUsageReportId":              {"ReportId"},
	"ImportImageTaskId":               {"ImportTaskId"},
	"ImportSnapshotTaskId":            {"ImportTaskId"},
	"IpamScopeId":                     {"DestinationIpamScopeId"},
	"Ipv4PoolCoipId":                  {"CoipPoolId", "PoolId"},
	"Ipv4PoolEc2Id":                   {"PoolId"},
	"Ipv6PoolEc2Id":                   {"PoolId"},
	"KeyPairName":                     {"KeyName"},
	"NaclId":                          {"NetworkAclId"},
	"PrefixListId":                    {"DestinationPrefixListId"},
	"ReservationId":                   {"ReservedInstancesId", "ReservedInstanceId"},
	"RoleNameWithPath":                {"RoleArn"},
	"SnapshotId":                      {"SourceSnapshotId"},
	"VolumeId":                        {"SourceVolumeId"},
	"VpcBlockPublicAccessExclusionId": {"ExclusionId"},
	"VpcEndpointServiceId":            {"ServiceId"},
}

// iamQueryRequestParameters indexes a query request's flat parameters by
// lower-cased name, collapsing both list encodings so every element authorizes
// separately: EC2's flattened InstanceId.N and awsQuery's boxed Names.member.N.
// Nested structure members (Filters.Filter.1.Name) name no resource and are left
// out.
func iamQueryRequestParameters(r *http.Request) map[string][]string {
	_ = r.ParseForm()
	byIndex := map[string]map[int]string{}
	for key, values := range r.Form {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		name, index := key, 0
		if dot := strings.LastIndexByte(key, '.'); dot >= 0 {
			n, err := strconv.Atoi(key[dot+1:])
			if err != nil {
				continue
			}
			name, index = key[:dot], n
		}
		name = strings.TrimSuffix(name, ".member")
		if strings.ContainsRune(name, '.') {
			continue
		}
		name = strings.ToLower(name)
		if byIndex[name] == nil {
			byIndex[name] = map[int]string{}
		}
		byIndex[name][index] = values[0]
	}
	params := make(map[string][]string, len(byIndex))
	for name, indexed := range byIndex {
		indices := make([]int, 0, len(indexed))
		for i := range indexed {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		for _, i := range indices {
			params[name] = append(params[name], indexed[i])
		}
	}
	return params
}

// iamTableDrivenARNs fills each declared type's published ARN format from the
// identifiers the request supplies. aliases carries the service's renamings and
// lookup reads a field, the only thing the protocols disagree about. A format
// naming no identifier is a constant ARN (AWS Glue's root catalog). When two
// types resolve to one field the exact spelling wins (RunInstances' SubnetId
// names the subnet, not the secondary subnet); where the claims tie, derive
// neither, since building both would require allowing a resource that does not
// exist.
func iamTableDrivenARNs(service string, types []string, region, account string,
	aliases map[string][]string, lookup func(field string) []string) []string {

	type resolved struct {
		format string
		// variable is the last one the format declares, kept so the value that
		// fills it gets the transformation its name states.
		variable string
		// parents are the values filling every variable but the last: a Glue
		// table's ARN carries its database, which the request names once.
		parents []string
		// field and rank identify the last variable's source, which is the
		// resource's own identifier and the only one that may name several.
		field string
		rank  int
	}
	best := map[string]int{}
	var found []resolved
	var constants []string

	for _, resourceType := range types {
		format, declared := iamResourceARNFormats[service+":"+resourceType]
		if !declared {
			continue
		}
		variables := iamARNFormatVariables(format)
		if len(variables) == 0 {
			constants = append(constants, iamFillARNFormat(format, region, account, nil))
			continue
		}
		// Every variable has to be named. A partially filled ARN would carry a
		// literal "${DatabaseName}" and match nothing.
		parents := make([]string, 0, len(variables)-1)
		complete := true
		for _, variable := range variables[:len(variables)-1] {
			values := iamFirstNamed(lookup, aliases, resourceType, variable)
			if len(values) == 0 {
				complete = false
				break
			}
			parents = append(parents, iamARNValueForVariable(variable, values[0]))
		}
		if !complete {
			continue
		}
		field, rank, named := iamNamingField(lookup, aliases, resourceType, variables[len(variables)-1])
		if !named {
			continue
		}
		if seen, ok := best[field]; !ok || rank < seen {
			best[field] = rank
		}
		found = append(found, resolved{format, variables[len(variables)-1], parents, field, rank})
	}

	contested := map[string]int{}
	for _, f := range found {
		if f.rank == best[f.field] {
			contested[f.field]++
		}
	}

	var out []string
	seen := map[string]struct{}{}
	add := func(resource string) {
		if _, dup := seen[resource]; dup {
			return
		}
		seen[resource] = struct{}{}
		out = append(out, resource)
	}
	for _, f := range found {
		if f.rank != best[f.field] || contested[f.field] > 1 {
			continue
		}
		for _, id := range lookup(f.field) {
			// Some types are named by another service's ARN outright (Amazon
			// EC2's CertificateArn and RoleArn), which needs no assembly.
			if strings.HasPrefix(id, "arn:") {
				add(id)
				continue
			}
			add(iamFillARNFormat(f.format, region, account,
				append(append([]string{}, f.parents...), iamARNValueForVariable(f.variable, id))))
		}
	}
	for _, c := range constants {
		add(c)
	}
	return out
}

// iamFirstNamed returns the values a request carries for one ARN format
// variable, under whichever spelling it supplies.
func iamFirstNamed(lookup func(string) []string, aliases map[string][]string, resourceType, variable string) []string {
	if field, _, ok := iamNamingField(lookup, aliases, resourceType, variable); ok {
		return lookup(field)
	}
	return nil
}

// iamNamingField returns the field a request names an ARN format variable
// under. The rank is that spelling's position in the candidate list, which runs
// most specific first, so a lower rank is the stronger claim on a field two
// resource types both resolve to.
func iamNamingField(lookup func(string) []string, aliases map[string][]string, resourceType, variable string) (string, int, bool) {
	for rank, name := range iamVariableFieldNames(resourceType, variable, aliases) {
		if len(lookup(name)) > 0 {
			return name, rank, true
		}
	}
	return "", 0, false
}

// iamVariableFieldNames returns the spellings an ARN format variable can arrive
// under, most specific first: the variable, the service's declared renamings, the
// leading-word drop, then the plural of each (AWS Glue's BatchGetJobs sends
// JobNames). A declared renaming outranks the drop because a test holds it to the
// model, while the drop is a spelling rule: a Glue catalog's ${CatalogName} drops
// to Name, which on GetTable is the table's name.
func iamVariableFieldNames(resourceType, variable string, aliases map[string][]string) []string {
	names := []string{variable}
	// A variable name is only unique within a resource type: AWS Systems
	// Manager calls the identifier of a maintenance window, an OpsItem, its
	// metadata and a service setting all ${ResourceId}, and the request names
	// each of them differently. An entry keyed "<type>.<variable>" answers for
	// that type alone, so the four do not resolve to one another's field and
	// cancel each other out as an ambiguity.
	names = append(names, aliases[resourceType+"."+variable]...)
	names = append(names, aliases[variable]...)
	if unprefixed := iamUnprefixedVariable(variable); unprefixed != "" {
		names = append(names, unprefixed)
	}
	for _, n := range append([]string{}, names...) {
		names = append(names, n+"s")
	}
	return names
}

// iamPrefixedVariable matches a variable whose leading word names the resource
// itself, capturing the rest — the form the APIs abbreviate to
// (SecurityGroupId → GroupId, PlacementGroupName → GroupName, and AWS Glue's
// BlueprintName → Name).
var iamPrefixedVariable = regexp.MustCompile(`^[A-Z][a-z]+((?:[A-Z][a-z]*)*(?:Id|Name))$`)

func iamUnprefixedVariable(variable string) string {
	if m := iamPrefixedVariable.FindStringSubmatch(variable); m != nil && m[1] != "" {
		return m[1]
	}
	return ""
}

// iamARNVariable matches one ${...} placeholder in a published ARN format.
var iamARNVariable = regexp.MustCompile(`\$\{([A-Za-z0-9]+)\}`)

// iamARNFormatVariables returns the identifiers a published ARN format needs,
// in the order they appear. The partition, region and account are the
// simulator's own and are not among them.
func iamARNFormatVariables(format string) []string {
	var out []string
	for _, m := range iamARNVariable.FindAllStringSubmatch(format, -1) {
		switch m[1] {
		case "Partition", "Region", "Account":
			continue
		}
		out = append(out, m[1])
	}
	return out
}

// iamARNValueForVariable applies the transformation a variable's own name
// states. AWS Systems Manager publishes a parameter's ARN as
// "parameter/${ParameterNameWithoutLeadingSlash}", and a parameter is named
// "/db/password" in every request, so the ARN of that parameter is
// "…:parameter/db/password". Keeping the slash would build an ARN with an empty
// first path segment, matching no policy.
func iamARNValueForVariable(variable, value string) string {
	if strings.HasSuffix(variable, "WithoutLeadingSlash") {
		return strings.TrimPrefix(value, "/")
	}
	return value
}

// iamFillARNFormat completes a published ARN format. The simulator supplies the
// partition, region and account; the values fill the identifier variables in
// order. A format that carries no account or no region has none to supply and
// is left as AWS publishes it.
func iamFillARNFormat(format, region, account string, values []string) string {
	filled := strings.NewReplacer(
		"${Partition}", "aws",
		"${Region}", region,
		"${Account}", account,
	).Replace(format)
	i := 0
	return iamARNVariable.ReplaceAllStringFunc(filled, func(string) string {
		if i >= len(values) {
			return ""
		}
		v := values[i]
		i++
		return v
	})
}

// iamEventBridgeResourceARNs derives the ARNs an Amazon EventBridge request
// names. A rule on the default bus is "rule/${RuleName}" and on a custom bus
// "rule/${EventBusName}/${RuleName}"; both claim the same member, so drop the
// form that cannot apply: a request naming a custom bus means the nested form,
// otherwise the flat one. Four types are constants (the built-in instance
// targets) and two belong to AWS KMS.
func iamEventBridgeResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	lookup := func(field string) []string { return fields[strings.ToLower(field)] }

	// PutEvents names its bus per entry, and AWS authorizes each entry against its
	// bus, as DynamoDB authorizes each transaction item against its table.
	if buses := iamEventBridgeEntryBuses(r, region, account); len(buses) > 0 {
		return buses
	}

	bus := iamFirstValue(lookup, "EventBusName")
	onDefaultBus := bus == "" || bus == "default"
	applicable := make([]string, 0, len(types))
	for _, resourceType := range types {
		if resourceType == "rule-on-custom-event-bus" && onDefaultBus {
			continue
		}
		if resourceType == "rule-on-default-event-bus" && !onDefaultBus {
			continue
		}
		applicable = append(applicable, resourceType)
	}
	return iamTableDrivenARNs("events", applicable, region, account, iamEventBridgeFieldAliases, lookup)
}

// iamEventBridgeFieldAliases maps an ARN format variable to the request members
// Amazon EventBridge spells it as, beyond the prefix drop. The event-bus,
// event-source and api-destination operations address their resource as Name,
// and the rule-target operations the rule as Rule. Each Name entry is scoped to
// its type: CreateEventBus also carries EventSourceName, and an unscoped alias
// would let the event-source type claim the bus's Name. The connection's entry
// is CreateApiDestination's ARN-valued connection member.
// TestIAMEventBridgeFieldAliasesAreRealRequestMembers holds every entry to the
// vendored model.
var iamEventBridgeFieldAliases = map[string][]string{
	"event-bus.EventBusName":             {"Name"},
	"event-source.EventSourceName":       {"Name"},
	"api-destination.ApiDestinationName": {"Name"},
	"rule-on-default-event-bus.RuleName": {"Rule"},
	"rule-on-custom-event-bus.RuleName":  {"Rule"},
	"ConnectionName":                     {"ConnectionArn"},
}

// iamFirehoseResourceARNs derives the delivery-stream ARNs an Amazon Data
// Firehose request names from DeliveryStreamName, creation included: AWS
// authorizes CreateDeliveryStream against the ARN the stream is about to have,
// as Amazon SNS does CreateTopic.
func iamFirehoseResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("firehose", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamGlueResourceARNs derives the ARNs an AWS Glue request names, from awsJson
// members. Its formats nest ("table/${DatabaseName}/${TableName}"), so build an
// ARN only when the request names every part; its root catalog names no
// identifier. Glue abbreviates identifiers by dropping the leading word
// (${BlueprintName} arrives as Name), and a batch sends the plural.
func iamGlueResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	// A dashboard names its resource generically, an id beside its kind; the kind
	// must be one the action declares.
	if kinds := fields["resourcetype"]; len(kinds) > 0 {
		for _, id := range fields["resourceid"] {
			resourceType := strings.ToLower(kinds[0])
			if id == "" || !iamHasType(types, resourceType) {
				continue
			}
			format, declared := iamResourceARNFormats["glue:"+resourceType]
			if !declared {
				continue
			}
			return []string{iamFillARNFormat(format, region, account, []string{id})}
		}
	}
	if iamHasType(types, "dataQualityRuleset") {
		if arns := iamGlueDataQualityRulesetARNs(fields, region, account); len(arns) > 0 {
			return arns
		}
	}
	// The tagging operations name their target by ResourceArn outright, which
	// needs no assembly — the ARN the caller sent is the ARN to authorize
	// against.
	var arns []string
	for _, value := range fields["resourcearn"] {
		if strings.HasPrefix(value, "arn:") {
			arns = append(arns, value)
		}
	}
	if len(arns) > 0 {
		sort.Strings(arns)
		return arns
	}
	return iamTableDrivenARNs("glue", types, region, account, iamGlueFieldAliases,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamGlueFieldAliases maps an ARN format's identifier variable to the request
// members AWS Glue spells it as, where the two differ by more than the
// mechanical prefix drop.
//
// TestIAMGlueFieldAliasesAreRealRequestMembers holds every entry to a member
// the vendored AWS Glue model declares on an operation authorizing against that
// resource type.
var iamGlueFieldAliases = map[string][]string{
	"CatalogName":             {"CatalogId"},
	"ConnectionTypeName":      {"ConnectionType"},
	"IntegrationId":           {"IntegrationIdentifier"},
	"UsageProfileId":          {"Name"},
	"UserDefinedFunctionName": {"FunctionName"},
}

// iamJSONRequestFields indexes an awsJson request body's top-level members by
// lower-cased name, reading a member that carries one identifier and one that
// carries a list the same way, since a batch operation authorizes every entry.
// Members holding anything else name no resource and are left out.
func iamJSONRequestFields(r *http.Request) map[string][]string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil
	}
	fields := make(map[string][]string, len(raw))
	nested := map[string][]string{}
	for name, value := range raw {
		if values, ok := iamJSONStrings(value); ok {
			fields[strings.ToLower(name)] = values
			continue
		}
		// One level down. An API may group an identifier into a structure of
		// its own rather than name it at the top level — AWS Glue addresses a
		// registry as RegistryId{RegistryName} and a schema as
		// SchemaId{SchemaName, RegistryName} — and the member inside is the
		// resource's name however it is wrapped.
		var inner map[string]json.RawMessage
		if json.Unmarshal(value, &inner) == nil {
			for innerName, innerValue := range inner {
				if values, ok := iamJSONStrings(innerValue); ok {
					nested[strings.ToLower(innerName)] = values
				}
			}
			continue
		}
		// A batch spells the same thing as a list of those structures, and an
		// identifier inside one entry is as much the identifier as one at the
		// top level: AWS Systems Manager's CreateAssociationBatch names each
		// document and each machine inside its own entry. Every entry
		// contributes, because a batch over two documents is about both.
		var entries []map[string]json.RawMessage
		if json.Unmarshal(value, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			for innerName, innerValue := range entry {
				if values, ok := iamJSONStrings(innerValue); ok {
					key := strings.ToLower(innerName)
					nested[key] = append(nested[key], values...)
				}
			}
		}
	}
	// A top-level member wins over one found inside a structure: it is the more
	// direct statement of what the request names, and letting a nested member
	// shadow it would derive from whichever the JSON happened to carry.
	for name, values := range nested {
		if _, taken := fields[name]; !taken {
			fields[name] = values
		}
	}
	return fields
}

// iamJSONStrings reads a JSON value that is a non-empty string, or a list of
// them, which is the shape an identifier arrives in.
func iamJSONStrings(value json.RawMessage) ([]string, bool) {
	var one string
	if json.Unmarshal(value, &one) == nil {
		if one == "" {
			return nil, false
		}
		return []string{one}, true
	}
	var many []string
	if json.Unmarshal(value, &many) == nil {
		var kept []string
		for _, v := range many {
			if v != "" {
				kept = append(kept, v)
			}
		}
		return kept, len(kept) > 0
	}
	return nil, false
}

// iamRDSCopyIdentifierPairs names the two ends a copy operation carries, and
// iamRDSCopySegments the ARN segment of the type both ends share.
var iamRDSCopyIdentifierPairs = map[string][2]string{
	"CopyDBSnapshot":              {"SourceDBSnapshotIdentifier", "TargetDBSnapshotIdentifier"},
	"CopyDBClusterSnapshot":       {"SourceDBClusterSnapshotIdentifier", "TargetDBClusterSnapshotIdentifier"},
	"CopyDBParameterGroup":        {"SourceDBParameterGroupIdentifier", "TargetDBParameterGroupIdentifier"},
	"CopyDBClusterParameterGroup": {"SourceDBClusterParameterGroupIdentifier", "TargetDBClusterParameterGroupIdentifier"},
	"CopyOptionGroup":             {"SourceOptionGroupIdentifier", "TargetOptionGroupIdentifier"},
}

var iamRDSCopySegments = map[string]string{
	"CopyDBSnapshot":              "snapshot",
	"CopyDBClusterSnapshot":       "cluster-snapshot",
	"CopyDBParameterGroup":        "pg",
	"CopyDBClusterParameterGroup": "cluster-pg",
	"CopyOptionGroup":             "og",
}

func iamRDSResourceARNs(r *http.Request, op string, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	lookup := func(field string) []string { return params[strings.ToLower(field)] }

	// A cluster's automated backup is named by the cluster's resource id and
	// by nothing else — the model declares no other member — so the backup is
	// resolved through the record the simulator keeps under exactly that id.
	// The store answers by key, with no scan on a path every request pays.
	if iamHasType(types, "cluster-auto-backup") && rdsClusterAutomatedBackups != nil {
		var out []string
		for _, resourceID := range lookup("DbClusterResourceId") {
			if backup, ok := rdsClusterAutomatedBackups.Get(resourceID); ok &&
				backup.DBClusterAutomatedBackupsArn != "" {
				out = append(out, backup.DBClusterAutomatedBackupsArn)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}

	// An ARN the request carries names its resource outright — Amazon RDS sends
	// one under members like SourceDBInstanceArn and
	// DBInstanceAutomatedBackupsArn. It is accepted only when its own resource
	// segment is a type the action declares: a request also carries ARNs for
	// things it is not about, a KMS key among them, and authorizing against
	// those would grant far past what was asked.
	for field, values := range params {
		if !strings.HasSuffix(field, "arn") && !strings.HasSuffix(field, "arns") {
			continue
		}
		for _, value := range values {
			if segment, ok := iamRDSARNResourceSegment(value); ok && iamHasType(types, segment) {
				return []string{value}
			}
		}
	}
	// A copy authorizes both ends. A bare name fills the type's published format,
	// since the target's ARN is determined before the resource exists; a source
	// named by ARN (the cross-region form) is authorized as sent.
	if pair, isCopy := iamRDSCopyIdentifierPairs[op]; isCopy {
		segment := iamRDSCopySegments[op]
		var ends []string
		for _, field := range pair {
			for _, id := range lookup(field) {
				if id == "" {
					continue
				}
				if strings.HasPrefix(id, "arn:") {
					ends = append(ends, id)
					continue
				}
				ends = append(ends, "arn:aws:rds:"+region+":"+account+":"+segment+":"+id)
			}
		}
		if len(ends) > 0 {
			sort.Strings(ends)
			return ends
		}
	}

	// A custom engine version and a proxy target group carry an id AWS assigns that
	// no request names, so resolve them through the simulator's state.
	var stateful []string
	generic := make([]string, 0, len(types))
	for _, resourceType := range types {
		switch resourceType {
		case "cev":
			engine := iamFirstValue(lookup, "Engine")
			version := iamFirstValue(lookup, "EngineVersion")
			// A version the simulator holds has an ARN of its own, which is the
			// exact resource the call is about.
			if rdsCustomEngineVersions != nil {
				if cev, ok := rdsCustomEngineVersions.Get(rdsCEVKey(engine, version)); ok {
					stateful = append(stateful, cev.ARN)
					continue
				}
			}
			// A create names a version that does not exist yet, and the
			// identifier Amazon RDS will assign is the only part of the ARN the
			// request cannot state. The engine and the version it names are not
			// the wildcard, so a grant written for one engine's versions still
			// does not reach another's.
			if engine != "" && version != "" {
				stateful = append(stateful, "arn:aws:rds:"+region+":"+account+
					":cev:"+engine+"/"+version+"/*")
			}
		case "target-group":
			group := iamFirstValue(lookup, "TargetGroupName")
			if group == "" {
				group = "default" // a proxy's only target group, as the API names it
			}
			if tg, ok := rdsProxyTargetGroups.Get(iamFirstValue(lookup, "DBProxyName") + "/" + group); ok {
				stateful = append(stateful, tg.ARN)
			}
		default:
			generic = append(generic, resourceType)
		}
	}
	return append(iamTableDrivenARNs("rds", generic, region, account, iamRDSFieldAliases, lookup), stateful...)
}

// iamFirstValue returns the single value a request carries for a field.
func iamFirstValue(lookup func(string) []string, field string) string {
	if values := lookup(field); len(values) > 0 {
		return values[0]
	}
	return ""
}

// iamRDSFieldAliases maps an ARN format's identifier variable to the request
// parameter Amazon RDS spells it as.
//
// TestIAMRDSFieldAliasesAreRealRequestParameters holds every entry to a
// parameter the vendored model declares on an operation that authorizes
// against that resource type.
var iamRDSFieldAliases = map[string][]string{
	"ClusterParameterGroupName": {"DBClusterParameterGroupName"},
	"ClusterSnapshotName":       {"DBClusterSnapshotIdentifier"},
	"DbClusterEndpoint":         {"DBClusterEndpointIdentifier"},
	"DbClusterInstanceName":     {"DBClusterIdentifier"},
	"DbInstanceName":            {"DBInstanceIdentifier"},
	"DbProxyEndpointId":         {"DBProxyEndpointName"},
	"DbProxyId":                 {"DBProxyName"},
	"DbShardGroupResourceId":    {"DBShardGroupIdentifier"},
	"GlobalCluster":             {"GlobalClusterIdentifier"},
	"ParameterGroupName":        {"DBParameterGroupName"},
	"ReservedDbInstanceName":    {"ReservedDBInstanceId"},
	"SecurityGroupName":         {"DBSecurityGroupName"},
	"SnapshotName":              {"DBSnapshotIdentifier"},
	"SubnetGroupName":           {"DBSubnetGroupName"},
}

// iamSSMResourceARNs derives the ARNs an AWS Systems Manager request names. A
// maintenance window, an OpsItem, its metadata and a service setting are all
// published as ${ResourceId} and named differently, so their aliases key by
// resource type. A parameter is published as ${ParameterNameWithoutLeadingSlash},
// so its leading slash goes. Four types are another service's resource (an EC2
// instance, an ECS task, an IAM role, an S3 bucket), which filling the published
// format gets right.
func iamSSMResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	// The tagging operations name their own resource type, so the type — not
	// the table — decides which ARN the identifier fills. Without that
	// discriminator a bare ResourceId would fill all eleven types the tagging
	// actions declare, authorizing against ten resources the request is not
	// about.
	if arns := iamSSMTaggedResourceARNs(fields, region, account); len(arns) > 0 {
		return arns
	}
	// An instance-scoped read declares both instance types, and the identifier
	// says which: Amazon EC2 assigns "i-", Systems Manager assigns "mi-" to a
	// machine registered with it, and their ARNs are in different services.
	// Filling both would authorize against a managed instance the request never
	// mentioned, and filling neither leaves the read unscoped.
	if iamHasType(types, "instance") || iamHasType(types, "managed-instance") {
		if arns := iamSSMInstanceARNs(fields, region, account); len(arns) > 0 {
			return arns
		}
	}
	// A just-in-time access request names its machines as the values of a
	// target whose key says what they are, and the key is the half that makes
	// them readable: a target keyed on a tag names a tag, not a machine.
	// Flattening the request loses that pairing, so the body is read for this
	// one operation.
	if iamHasType(types, "instance") || iamHasType(types, "managed-instance") {
		if machines := iamSSMTargetedInstances(r); len(machines) > 0 {
			if arns := iamSSMInstanceARNs(
				map[string][]string{"instanceids": machines}, region, account); len(arns) > 0 {
				return arns
			}
		}
	}
	// Reading the default patch baseline names the operating system, and the
	// baseline that default resolves to is what the read authorizes against —
	// the same resolution the read itself performs, so the two cannot disagree.
	if iamHasType(types, "patchbaseline") && ssmDefaultBaselines != nil {
		if systems := fields["operatingsystem"]; len(systems) > 0 && systems[0] != "" {
			id := ssmDefaultBaselineID(systems[0])
			if strings.HasPrefix(id, "arn:") {
				return []string{id}
			}
			return []string{"arn:aws:ssm:" + region + ":" + account + ":patchbaseline/" + id}
		}
	}
	// Cancelling a maintenance window's execution names the execution, and the
	// window is what the call authorizes against. The execution id is derived
	// from the window's own id, so the window is recovered through an index
	// keyed on what each window's execution id would be.
	if iamHasType(types, "maintenancewindow") && ssmWindows != nil {
		var out []string
		for _, execution := range fields["windowexecutionid"] {
			if window, ok := iamSSMWindowsByExecution.Lookup(
				ssmWindows, execution, iamSSMWindowExecutionKeys); ok && window.WindowId != "" {
				out = append(out, "arn:aws:ssm:"+region+":"+account+
					":maintenancewindow/"+window.WindowId)
			}
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out
		}
	}
	return iamTableDrivenARNs("ssm", types, region, account, iamSSMFieldAliases,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamSSMTaggedResourceARNType maps a ResourceTypeForTagging value to the
// resource its ARN names, as the service reference publishes each format.
var iamSSMTaggedResourceARNType = map[string]string{
	"DOCUMENT":          "document",
	"MANAGEDINSTANCE":   "managed-instance",
	"MAINTENANCEWINDOW": "maintenancewindow",
	"PARAMETER":         "parameter",
	"PATCHBASELINE":     "patchbaseline",
	"OPSITEM":           "opsitem",
	"OPSMETADATA":       "opsmetadata",
	"AUTOMATION":        "automation-execution",
	"ASSOCIATION":       "association",
	"CLOUDCONNECTOR":    "cloud-connector",
}

// iamSSMTagTypeKey canonicalises a ResourceTypeForTagging value. The model
// spells the members MANAGED_INSTANCE while a caller sends ManagedInstance, so
// the underscores and the case are both dropped before the lookup.
func iamSSMTagTypeKey(resourceType string) string {
	return strings.ToUpper(strings.ReplaceAll(resourceType, "_", ""))
}

// iamSSMTaggedResourceARNs builds the ARN an AddTagsToResource,
// RemoveTagsFromResource or ListTagsForResource call is about, from the
// ResourceType it names and the ResourceId it carries.
func iamSSMTaggedResourceARNs(fields map[string][]string, region, account string) []string {
	resourceTypes := fields["resourcetype"]
	resourceIDs := fields["resourceid"]
	if len(resourceTypes) == 0 || len(resourceIDs) == 0 {
		return nil
	}
	resource, known := iamSSMTaggedResourceARNType[iamSSMTagTypeKey(resourceTypes[0])]
	if !known {
		// A type the service does not declare names nothing this can build,
		// and guessing one would authorize against the wrong resource.
		return nil
	}
	id := resourceIDs[0]
	if strings.HasPrefix(id, "arn:") {
		return []string{id}
	}
	if resource == "parameter" {
		// The published format is the parameter name without its leading
		// slash; a caller may send it either way.
		id = strings.TrimPrefix(id, "/")
	}
	return []string{"arn:aws:ssm:" + region + ":" + account + ":" + resource + "/" + id}
}

// iamSSMFieldAliases maps an ARN format's identifier variable to the request
// members AWS Systems Manager spells it as. An entry keyed "<type>.<variable>"
// answers for that resource type alone.
//
// TestIAMSSMFieldAliasesAreRealRequestMembers holds every entry to a member the
// vendored model declares on an operation authorizing against that type.
var iamSSMFieldAliases = map[string][]string{
	"maintenancewindow.ResourceId": {"WindowId"},
	// A just-in-time access request is tracked as an OpsItem, which is why the
	// simulator mints its id with the OpsItem prefix and why reading its token
	// authorizes against that item.
	"opsitem.ResourceId":                         {"OpsItemId", "OpsItemArn", "AccessRequestId"},
	"opsmetadata.ResourceId":                     {"OpsMetadataArn"},
	"servicesetting.ResourceId":                  {"SettingId"},
	"patchbaseline.PatchBaselineIdResourceId":    {"BaselineId"},
	"parameter.ParameterNameWithoutLeadingSlash": {"Name", "Path"},
	// A change calendar is a Systems Manager document, and GetCalendarState
	// names the ones it reads under CalendarNames — by name or by ARN.
	"document.DocumentName": {"Name", "DocumentName", "CalendarNames"},
	// The patch and association reads name the instance they are about, which
	// is the resource AWS authorizes them against.
	"managed-instance.InstanceId": {"InstanceId", "InstanceIds", "Target"},
	"instance.InstanceId":         {"InstanceId", "InstanceIds", "Target"},
}

// iamEventBridgeEntryBuses returns the event buses a PutEvents call writes to,
// read from each entry. An entry that names no bus writes to the default one,
// which is what AWS does with it.
func iamEventBridgeEntryBuses(r *http.Request, region, account string) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var req struct {
		Entries []struct {
			EventBusName string `json:"EventBusName"`
		} `json:"Entries"`
	}
	if json.Unmarshal(body, &req) != nil || len(req.Entries) == 0 {
		return nil
	}
	var buses []string
	seen := map[string]bool{}
	for _, entry := range req.Entries {
		name := entry.EventBusName
		if name == "" {
			name = "default"
		}
		arn := name
		if !strings.HasPrefix(name, "arn:") {
			arn = "arn:aws:events:" + region + ":" + account + ":event-bus/" + name
		}
		if seen[arn] {
			continue
		}
		seen[arn] = true
		buses = append(buses, arn)
	}
	return buses
}

// iamElastiCacheResourceARNs derives the ARNs an Amazon ElastiCache request
// names. ElastiCache publishes every resource type under the parameter the API
// sends, so the derivation needs no renamings.
func iamElastiCacheResourceARNs(r *http.Request, op string, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	lookup := func(field string) []string { return params[strings.ToLower(field)] }

	// The tagging operations name their target by ARN, spelled ResourceName the
	// way every query-protocol database service spells it. The ARN says which
	// of the fourteen types the action declares it is, so there is nothing to
	// assemble and nothing to choose.
	for _, value := range lookup("ResourceName") {
		if strings.HasPrefix(value, "arn:") {
			return []string{value}
		}
	}
	// A copy authorizes both of its ends, and the target's ARN is fully
	// determined by the name the request supplies before the snapshot
	// exists — the same argument that derives the Amazon RDS copies.
	if pair, isCopy := iamElastiCacheCopyIdentifierPairs[op]; isCopy {
		segment := iamElastiCacheCopySegments[op]
		var ends []string
		for _, field := range pair {
			for _, id := range lookup(field) {
				if id == "" {
					continue
				}
				ends = append(ends, "arn:aws:elasticache:"+region+":"+account+":"+segment+":"+id)
			}
		}
		if len(ends) > 0 {
			sort.Strings(ends)
			return ends
		}
	}
	// AWS picks the prefix of a global datastore's id when it creates one, so
	// the request cannot name the datastore it makes: that is the type
	// wildcard, authorized alongside the primary replication group it names.
	if op == "CreateGlobalReplicationGroup" {
		arns := iamCreateWildcardARNs("elasticache", op, types, region, account)
		for _, id := range lookup("PrimaryReplicationGroupId") {
			arns = append(arns, "arn:aws:elasticache:"+region+":"+account+":replicationgroup:"+id)
		}
		return arns
	}
	return iamTableDrivenARNs("elasticache", types, region, account, nil, lookup)
}

// iamElastiCacheCopyIdentifierPairs names the two ends a copy operation
// carries, and iamElastiCacheCopySegments the ARN segment of the type both
// ends share.
var iamElastiCacheCopyIdentifierPairs = map[string][2]string{
	"CopySnapshot":                {"SourceSnapshotName", "TargetSnapshotName"},
	"CopyServerlessCacheSnapshot": {"SourceServerlessCacheSnapshotName", "TargetServerlessCacheSnapshotName"},
}

var iamElastiCacheCopySegments = map[string]string{
	"CopySnapshot":                "snapshot",
	"CopyServerlessCacheSnapshot": "serverlesscachesnapshot",
}

// iamECSResourceARNs derives the ARNs an Amazon ECS request names. ECS resource
// ARNs below the cluster embed the cluster in their path
// (task/<cluster>/<id>, service/<cluster>/<name>), so the cluster is resolved
// first; a request that omits it means the "default" cluster, exactly as the
// API does.
func iamECSResourceARNs(r *http.Request, op string, types []string, arn func(svc, resource string) string) []string {
	cluster := iamECSClusterName(r)
	var out []string
	add := func(resource string) {
		out = append(out, arn("ecs", resource))
	}
	// An identifier that already is an ARN is the resource itself; only bare
	// names need assembling.
	addNamed := func(prefix string, names []string) {
		for _, name := range names {
			if strings.HasPrefix(name, "arn:") {
				out = append(out, name)
				continue
			}
			add(prefix + name)
		}
	}

	// The newer families (a daemon with its deployments and revisions, an Amazon ECS
	// Express Mode service, a service deployment or revision) name themselves by
	// ARN. Choose the member by the type the operation authorizes against, never the
	// first ARN in the body: CreateDaemon also carries the task definition's ARN, and
	// authorizing against it would let a policy scoped to the task definition create
	// a daemon.
	for _, family := range []struct {
		resourceType string
		single       []string
		list         []string
	}{
		{"daemon", []string{"daemonArn"}, nil},
		{"daemon-deployment", nil, []string{"daemonDeploymentArns"}},
		{"daemon-revision", nil, []string{"daemonRevisionArns"}},
		{"daemon-task-definition", []string{"daemonTaskDefinitionArn"}, nil},
		{"service-deployment", []string{"serviceDeploymentArn"}, []string{"serviceDeploymentArns"}},
		{"service-revision", nil, []string{"serviceRevisionArns"}},
		{"service", []string{"serviceArn"}, nil},
	} {
		if !iamHasType(types, family.resourceType) {
			continue
		}
		var named []string
		for _, candidate := range iamNamesFrom(r, family.single, family.list) {
			if strings.HasPrefix(candidate, "arn:") {
				named = append(named, candidate)
			}
		}
		if len(named) > 0 {
			return named
		}
	}
	if iamHasType(types, "daemon") {
		// A daemon's ARN is daemon/<cluster>/<name>, so a create — which has no
		// daemon ARN yet — still derives from the cluster and name it supplies.
		addNamed("daemon/"+cluster+"/", iamNamesFrom(r, []string{"daemonName"}, nil))
	}
	if iamHasType(types, "daemon-task-definition") {
		// family:revision, exactly as a task definition is spelled.
		addNamed("daemon-task-definition/", iamNamesFrom(r, []string{"daemonTaskDefinition"}, nil))
	}
	if iamHasType(types, "task-set") {
		service := iamFirstJSONField(r, "service")
		addNamed("task-set/"+cluster+"/"+service+"/",
			iamNamesFrom(r, []string{"taskSet"}, []string{"taskSets"}))
		// Creating a task set names no task set — it does not exist yet — but
		// it does name the service the set will belong to, and Amazon ECS
		// authorizes the create against the task sets of that service. The id
		// is the wildcard, exactly as it is for every other create whose
		// resource the request cannot name; the cluster and service are not,
		// so a grant scoped to one service's task sets still denies another's.
		if op == "CreateTaskSet" && service != "" {
			out = append(out, arn("ecs", "task-set/"+cluster+"/"+service+"/*"))
		}
	}
	if iamHasType(types, "task") {
		addNamed("task/"+cluster+"/", iamNamesFrom(r, []string{"task"}, []string{"tasks"}))
	}
	if iamHasType(types, "service") {
		addNamed("service/"+cluster+"/",
			iamNamesFrom(r, []string{"service", "serviceName"}, []string{"services"}))
	}
	if iamHasType(types, "container-instance") {
		addNamed("container-instance/"+cluster+"/",
			iamNamesFrom(r, []string{"containerInstance"}, []string{"containerInstances"}))
		// PutAttributes and DeleteAttributes name no container instance of
		// their own: each attribute carries the instance it is about as its
		// targetId, which is what the call authorizes against.
		addNamed("container-instance/"+cluster+"/", iamECSAttributeTargets(r))
	}
	if iamHasType(types, "capacity-provider") {
		addNamed("capacity-provider/",
			iamNamesFrom(r, []string{"capacityProvider", "name"}, []string{"capacityProviders"}))
	}
	if iamHasType(types, "task-definition") {
		addNamed("task-definition/", iamECSTaskDefinitionIDs(r))
	}
	if iamHasType(types, "gateway-route") {
		if routeArn := iamFirstJSONField(r, "gatewayRouteArn"); routeArn != "" {
			out = append(out, routeArn)
		}
	}
	if iamHasType(types, "gateway") {
		out = append(out, iamECSGatewayARNs(r, op, arn)...)
	}
	if iamHasType(types, "cluster") {
		// DescribeClusters names them in a list; everything else names one, and
		// an omitted cluster is the default one.
		if named := iamNamesFrom(r, []string{"cluster", "clusterName"}, []string{"clusters"}); len(named) > 0 {
			addNamed("cluster/", named)
		} else if len(out) == 0 {
			add("cluster/" + cluster)
		}
	}
	return out
}

// iamECSGatewayARNs derives the gateway a request names. A gateway's ARN ends
// in an identifier Amazon ECS assigns, so a name resolves through the gateway
// it names, and a create, whose gateway has no identifier yet, is the
// wildcard over it. A route's ARN embeds its gateway's name and identifier.
func iamECSGatewayARNs(r *http.Request, op string, arn func(svc, resource string) string) []string {
	if op == "CreateGateway" {
		if name := iamFirstJSONField(r, "name"); name != "" {
			return []string{arn("ecs", "gateway/"+name+"/*")}
		}
		return nil
	}
	if routeArn := iamFirstJSONField(r, "gatewayRouteArn"); routeArn != "" {
		if i := strings.Index(routeArn, ":gateway-route/"); i >= 0 {
			parts := strings.SplitN(routeArn[i+len(":gateway-route/"):], "/", 3)
			if len(parts) == 3 {
				return []string{arn("ecs", "gateway/"+parts[0]+"/"+parts[1])}
			}
		}
	}
	ref := iamFirstJSONField(r, "gateway")
	switch {
	case ref == "":
		return nil
	case strings.HasPrefix(ref, "arn:"):
		return []string{ref}
	}
	if gateway, ok := ecsResolveGateway(ref); ok {
		return []string{gateway.GatewayArn}
	}
	return []string{arn("ecs", "gateway/"+ref+"/*")}
}

// iamECSClusterName resolves the cluster a request targets, accepting the name
// or the ARN the API accepts interchangeably and defaulting to "default".
func iamECSClusterName(r *http.Request) string {
	// clusterArn is how the daemon family names its cluster; the ARN is
	// reduced to the bare name below, which is what every ECS ARN embeds.
	name := iamFirstJSONField(r, "cluster", "clusterName", "clusterArn")
	if name == "" {
		if clusters := iamJSONBodyStrings(r, "clusters"); len(clusters) > 0 {
			name = clusters[0]
		}
	}
	if name == "" {
		return "default"
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// iamECSTaskDefinitionIDs returns the "<family>:<revision>" identifiers a
// request names. RegisterTaskDefinition is the interesting case: AWS
// authorizes it against the task definition it is about to create, so the
// requested resource carries the revision the call will be assigned — which
// the simulator, owning the revision counter, knows before it assigns it.
func iamECSTaskDefinitionIDs(r *http.Request) []string {
	if ids := iamNamesFrom(r, []string{"taskDefinition"}, []string{"taskDefinitions"}); len(ids) > 0 {
		return ids
	}
	family := iamJSONBodyField(r, "family")
	if family == "" {
		return nil
	}
	ecsRevisionMu.Lock()
	next := ecsRevisions[family] + 1
	ecsRevisionMu.Unlock()
	return []string{family + ":" + strconv.Itoa(next)}
}

// iamKMSResourceARNs derives the ARNs an AWS KMS request names; a KeyId that is
// already an ARN passes through. An alias ARN is "alias/${Alias}" and AliasName
// already carries "alias/", so strip it once rather than build
// "alias/alias/my-key".
func iamKMSResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("kms", types, region, account, iamKMSFieldAliases, func(field string) []string {
		values := fields[strings.ToLower(field)]
		if !strings.EqualFold(field, "AliasName") {
			return values
		}
		stripped := make([]string, 0, len(values))
		for _, v := range values {
			stripped = append(stripped, strings.TrimPrefix(v, "alias/"))
		}
		return stripped
	})
}

// iamKMSFieldAliases maps an ARN format's identifier variable to the request
// member AWS KMS spells it as.
//
// TestIAMKMSFieldAliasesAreRealRequestMembers holds the entry to a member the
// vendored model declares on an operation authorizing against that type.
var iamKMSFieldAliases = map[string][]string{
	"Alias": {"AliasName"},
}

// iamLogsResourceARNs derives the ARNs an Amazon CloudWatch Logs request names.
// The service defines two nested resource types and the distinction is not
// cosmetic: a log stream's ARN is the group's with ":log-stream:<name>"
// appended, so a policy granting the group alone does not cover the four
// stream-scoped actions and a policy written "<group-arn>:*" covers those but
// not the group-scoped reads. The gate requests whichever type AWS declares.
func iamLogsResourceARNs(r *http.Request, types []string, arn func(svc, resource string) string) []string {
	// A log record is fetched by a pointer the service issued, and that pointer
	// says which group the record is in: it is "group|stream|index", which is
	// how the read itself resolves the record. A pointer of any other shape
	// names no group.
	if iamHasType(types, "log-group") {
		if pointer := iamJSONBodyField(r, "logRecordPointer"); pointer != "" {
			if group, _, split := strings.Cut(pointer, "|"); split && group != "" {
				return []string{arn("logs", "log-group:"+group)}
			}
		}
	}
	// Reading a query's results names the query and nothing else, and the log
	// groups it ran over are what the read authorizes against — the query
	// record holds them, under the id the request carries, so the store
	// answers by key with no scan on a path every request pays. A query over
	// two groups authorizes both, because it read both.
	if iamHasType(types, "log-group") && cwQueries != nil {
		if id := iamJSONBodyField(r, "queryId"); id != "" {
			if query, ok := cwQueries.Get(id); ok {
				var out []string
				for _, group := range query.LogGroups {
					if group != "" {
						out = append(out, arn("logs", "log-group:"+group))
					}
				}
				if len(out) > 0 {
					sort.Strings(out)
					return out
				}
			}
		}
	}

	// The families beyond log groups and streams each authorize against a
	// resource type of their own, and every one of their ARNs is
	// "<type>:<name>" over a name, id or ARN the request already carries —
	// the formats the service reference publishes. A request that names one
	// derives it; the type it is authorized against decides which member is
	// read, so an operation whose declared type does not match what it carries
	// derives nothing rather than guessing.
	if named := iamLogsNamedResourceARNs(r, types, arn); len(named) > 0 {
		return named
	}
	groups := iamNamesFrom(r,
		[]string{"logGroupName", "logGroupIdentifier"},
		[]string{"logGroupNames", "logGroupIdentifiers"})
	if len(groups) == 0 {
		return nil
	}
	stream := ""
	if iamHasType(types, "log-stream") {
		stream = iamJSONBodyField(r, "logStreamName")
	}
	var out []string
	for _, group := range groups {
		// A logGroupIdentifier may be the group's ARN rather than its name.
		base := arn("logs", "log-group:"+group)
		if strings.HasPrefix(group, "arn:") {
			base = strings.TrimSuffix(group, ":*")
		}
		if stream != "" {
			base += ":log-stream:" + stream
		}
		out = append(out, base)
	}
	return out
}

// iamLogsNamedResourceARNs builds the ARNs of the Amazon CloudWatch Logs
// resources that are named directly by the request: a delivery, a delivery
// source or destination, a subscription destination, an anomaly detector, a
// lookup table or a scheduled query.
//
// A member that already holds an ARN is used as it stands; a member holding a
// bare name is assembled into the type's published format. The log-group
// families are left to the caller, which reads them from the log-group
// members.
func iamLogsNamedResourceARNs(r *http.Request, types []string, arn func(svc, resource string) string) []string {
	// A resource named by an ARN member: the ARN is the answer, whatever the
	// declared type spells.
	for _, member := range []string{"anomalyDetectorArn", "lookupTableArn", "deliveryDestinationArn"} {
		if v := iamJSONBodyField(r, member); strings.HasPrefix(v, "arn:") {
			out := []string{v}
			// CreateDelivery names a destination by ARN and a source by name,
			// and authorizes against both.
			if source := iamJSONBodyField(r, "deliverySourceName"); source != "" {
				out = append(out, arn("logs", "delivery-source:"+source))
			}
			return out
		}
	}
	// A resource named by a bare name or id, assembled into its own type's
	// format. The declared type selects the member, because several of these
	// spell their identifier "name" or "id".
	named := func(resourceType string, members ...string) []string {
		if !iamHasType(types, resourceType) {
			return nil
		}
		for _, member := range members {
			if v := iamJSONBodyField(r, member); v != "" {
				if strings.HasPrefix(v, "arn:") {
					return []string{v}
				}
				return []string{arn("logs", resourceType+":"+v)}
			}
		}
		return nil
	}
	// Ordered by how specific the member name is, so an operation carrying
	// both a delivery id and a destination name resolves the one its type
	// declares.
	for _, candidate := range [][]string{
		{"delivery", "id"},
		{"delivery-destination", "deliveryDestinationName", "name"},
		{"delivery-source", "deliverySourceName", "name"},
		{"destination", "destinationName"},
		{"lookup-table", "lookupTableName"},
		{"scheduled-query", "identifier"},
	} {
		if out := named(candidate[0], candidate[1:]...); len(out) > 0 {
			return out
		}
	}
	// An anomaly detector created over log groups authorizes against them.
	if arns := iamJSONBodyStrings(r, "logGroupArnList"); len(arns) > 0 {
		return arns
	}
	return nil
}

// iamECSAttributeTargets returns the container instances an attribute request
// is about, read from each attribute's targetId. A target given as an ARN is
// returned as it stands; the caller assembles a bare id.
func iamECSAttributeTargets(r *http.Request) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var req struct {
		Attributes []struct {
			TargetId string `json:"targetId"`
		} `json:"attributes"`
	}
	if json.Unmarshal(body, &req) != nil {
		return nil
	}
	var targets []string
	seen := map[string]bool{}
	for _, attribute := range req.Attributes {
		if attribute.TargetId == "" || seen[attribute.TargetId] {
			continue
		}
		seen[attribute.TargetId] = true
		targets = append(targets, attribute.TargetId)
	}
	return targets
}

// iamCodeBuildResourceARNs derives the ARNs an AWS CodeBuild request names.
// The build-scoped operations authorize against the build's project, and a
// build id is "<projectName>:<uuid>", so the project comes out of the id.
func iamCodeBuildResourceARNs(r *http.Request, types []string, arn func(svc, resource string) string) []string {
	var out []string
	addNamed := func(prefix string, names []string) {
		for _, name := range names {
			if strings.HasPrefix(name, "arn:") {
				out = append(out, name)
				continue
			}
			out = append(out, arn("codebuild", prefix+name))
		}
	}
	if iamHasType(types, "project") {
		names := iamNamesFrom(r, []string{"projectName", "name"}, []string{"names"})
		for _, id := range iamNamesFrom(r, []string{"id"}, []string{"ids"}) {
			if project, _, ok := strings.Cut(id, ":"); ok && project != "" {
				names = append(names, project)
			}
		}
		addNamed("project/", names)
	}
	if iamHasType(types, "report-group") {
		addNamed("report-group/",
			iamNamesFrom(r, []string{"reportGroupArn", "name"}, []string{"reportGroupArns"}))
		// A report belongs to a group, and AWS CodeBuild authorizes the group:
		// a report ARN ends "report/<groupName>:<reportId>", so the group is
		// the segment the report id follows.
		for _, ref := range iamNamesFrom(r, []string{"reportArn"}, []string{"reportArns"}) {
			if group := iamCodeBuildReportGroupName(ref); group != "" {
				addNamed("report-group/", []string{group})
			}
		}
	}
	if iamHasType(types, "fleet") {
		addNamed("fleet/", iamNamesFrom(r, []string{"name"}, []string{"names"}))
	}
	// A command execution belongs to a sandbox, and AWS CodeBuild authorizes
	// the sandbox: both the batch read and the listing name it under
	// sandboxId, and the sandbox reads name it under id.
	if iamHasType(types, "sandbox") {
		addNamed("sandbox/", iamNamesFrom(r, []string{"sandboxId", "id"}, []string{"ids"}))
	}
	// A resource the request names by its own ARN needs no assembly, whatever
	// type it is: CodeBuild spells that member "arn" on the fleet and
	// report-group deletes, and "projectArn" where a project is meant.
	for _, named := range iamNamesFrom(r, []string{"arn", "projectArn"}, []string{"arns"}) {
		if strings.HasPrefix(named, "arn:") {
			out = append(out, named)
		}
	}
	return out
}

// iamCodeBuildReportGroupName is the report group a report belongs to, read off
// the report's own identifier. AWS CodeBuild spells a report
// "<groupName>:<reportId>", and its ARN puts that under "report/", so both
// spellings name the same group. A reference in neither shape names no group,
// and guessing one would authorize against a group the request never mentioned.
func iamCodeBuildReportGroupName(ref string) string {
	if strings.HasPrefix(ref, "arn:") {
		_, tail, found := strings.Cut(ref, ":report/")
		if !found {
			return ""
		}
		ref = tail
	}
	group, _, found := strings.Cut(ref, ":")
	if !found {
		return ""
	}
	return group
}

// iamWAFv2ResourceARNs derives the ARNs an AWS WAFv2 request names. The ARN
// carries the resource's generated id and scope path, and the type is the
// operation's suffix (GetIPSet names an ipset); wafARN builds it as the handlers
// do. Entities a rule statement references are not derived.
func iamWAFv2ResourceARNs(r *http.Request, op string, types []string) []string {
	// An ARN names the resource outright whatever the operation is called; the
	// suffix matching below only picks a type when the request gives a name and
	// scope.
	if a := iamFirstJSONField(r, "ARN", "WebACLArn", "WebAclArn"); strings.HasPrefix(a, "arn:") {
		return []string{a}
	}
	// The collection is named inside the operation name, not only at its end
	// (PutManagedRuleSetVersions). An operation naming two collections derives
	// nothing rather than pick one.
	collections := []struct{ named, resource string }{
		{"WebACL", "webacl"},
		{"IPSet", "ipset"},
		{"RuleGroup", "rulegroup"},
		{"RegexPatternSet", "regexpatternset"},
		{"ManagedRuleSet", "managedruleset"},
	}
	resourceType, named, matches := "", "", 0
	for _, candidate := range collections {
		if !strings.Contains(op, candidate.named) || !iamHasType(types, candidate.resource) {
			continue
		}
		resourceType, named = candidate.resource, candidate.named
		matches++
	}
	if matches > 1 {
		return nil
	}
	if resourceType == "" {
		// An operation whose name says nothing about the collection is still
		// not ambiguous when the action declares a single type — that type is
		// what it is about.
		if len(types) != 1 {
			return nil
		}
		resourceType = types[0]
		for _, candidate := range collections {
			if candidate.resource == resourceType {
				named = candidate.named
			}
		}
	}
	// WAFv2 spells the members plainly where the operation is about one
	// resource, and qualifies them where the request also names another —
	// GetRateBasedStatementManagedKeys carries WebACLName and WebACLId beside
	// the rule key it is asking for.
	name := iamFirstJSONField(r, "Name", named+"Name")
	id := iamFirstJSONField(r, "Id", named+"Id")
	if name == "" {
		return nil
	}
	return []string{wafARN(iamJSONBodyField(r, "Scope"), resourceType, name, id)}
}

// iamGlueDataQualityRulesetARNs resolves the rulesets a data-quality request is
// about, from the run or result it names.
//
// A run that the simulator does not hold names no ruleset, and deriving one
// anyway would authorize against a ruleset the request never touched.
func iamGlueDataQualityRulesetARNs(fields map[string][]string, region, account string) []string {
	arn := func(name string) string {
		return "arn:aws:glue:" + region + ":" + account + ":dataQualityRuleset/" + name
	}
	var out []string
	seen := map[string]struct{}{}
	add := func(name string) {
		if name == "" {
			return
		}
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, arn(name))
	}

	// A ruleset the request names outright — including the one a
	// recommendation run is being started to create, which the caller names
	// and so is as derivable as any other name a create supplies.
	for _, member := range []string{"rulesetname", "rulesetnames", "createdrulesetname"} {
		for _, name := range fields[member] {
			add(name)
		}
	}

	for _, id := range fields["runid"] {
		if glueDQEvalRuns != nil {
			if run, ok := glueDQEvalRuns.Get(id); ok {
				for _, name := range run.RulesetNames {
					add(name)
				}
			}
		}
		// A rule-recommendation run creates a ruleset rather than evaluating
		// one, and the ruleset it created is what a read of the run is about.
		if glueDQRecRuns != nil {
			if run, ok := glueDQRecRuns.Get(id); ok {
				add(run.CreatedRulesetName)
			}
		}
	}
	for _, id := range fields["resultid"] {
		if glueDQResults != nil {
			if result, ok := glueDQResults.Get(id); ok {
				add(result.RulesetName)
			}
		}
	}
	// A data-quality model is asked for by the profile an evaluation wrote its
	// statistics to, and the profile belongs to the ruleset that produced it —
	// which the result records. The result is stored under its own id, so the
	// profile reaches it through an index rather than a scan.
	for _, id := range fields["profileid"] {
		if glueDQResults == nil {
			continue
		}
		if result, ok := glueResultForProfile(id); ok {
			add(result.RulesetName)
		}
	}
	return out
}

// iamSSMInstanceARNs derives the machines an instance-scoped Systems Manager
// read is about, from the identifiers it names. An identifier with neither
// prefix names no machine this can build an ARN for.
func iamSSMInstanceARNs(fields map[string][]string, region, account string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(arn string) {
		if _, dup := seen[arn]; dup {
			return
		}
		seen[arn] = struct{}{}
		out = append(out, arn)
	}
	for _, member := range []string{"instanceid", "instanceids", "target"} {
		for _, id := range fields[member] {
			switch {
			case strings.HasPrefix(id, "i-"):
				add("arn:aws:ec2:" + region + ":" + account + ":instance/" + id)
			case strings.HasPrefix(id, "mi-"):
				add("arn:aws:ssm:" + region + ":" + account + ":managed-instance/" + id)
			}
		}
	}
	return out
}

// iamServiceLinkedRoleFromTask is the role a service-linked-role deletion task
// is deleting, read off the task id. IAM spells it
// "task/aws-service-role/<service>/<roleName>/<uuid>", so the role is the
// path between the service and the task's own identifier — and the ARN keeps
// that path, because a service-linked role lives under /aws-service-role/.
//
// A task id in any other shape names no role; building one from a partial
// match would authorize against a role the request never mentioned.
func iamServiceLinkedRoleFromTask(task string) string {
	rest, found := strings.CutPrefix(task, "task/")
	if !found {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 4 || parts[0] != "aws-service-role" {
		return ""
	}
	// Everything between "aws-service-role" and the trailing task identifier is
	// the role's own path and name.
	return strings.Join(parts[:len(parts)-1], "/")
}

// iamELBv2CreatedResourceARNs derives what a create is about.
//
// Every Elastic Load Balancing v2 ARN ends in a name and an identifier the
// service assigns — "targetgroup/${TargetGroupName}/${TargetGroupId}" — and a
// create names the first and cannot name the second. So the identifier is the
// wildcard and the name is not, which is what keeps a grant written for one
// target group from reaching another.
//
// A load balancer's ARN carries its kind as well, and the request states it:
// an application balancer is "loadbalancer/app/…", a network one "net", a
// gateway one "gwy". A kind the service does not define derives nothing.
func iamELBv2CreatedResourceARNs(params map[string][]string, region, account string) []string {
	first := func(field string) string {
		if values := params[field]; len(values) > 0 {
			return values[0]
		}
		return ""
	}
	name := first("name")
	if name == "" {
		return nil
	}
	arn := func(resource string) []string {
		return []string{"arn:aws:elasticloadbalancing:" + region + ":" + account + ":" + resource}
	}
	switch first("action") {
	case "CreateTargetGroup":
		return arn("targetgroup/" + name + "/*")
	case "CreateTrustStore":
		return arn("truststore/" + name + "/*")
	case "CreateLoadBalancer":
		kind := map[string]string{
			"":            "app", // the API defaults an unstated type to application
			"application": "app",
			"network":     "net",
			"gateway":     "gwy",
		}[first("type")]
		if kind == "" {
			return nil
		}
		return arn("loadbalancer/" + kind + "/" + name + "/*")
	}
	return nil
}

// iamRDSARNResourceSegment is the resource type an Amazon RDS ARN names —
// "db", "cluster", "auto-backup" — read from the segment after the account.
// A value that is not an RDS ARN names no type.
func iamRDSARNResourceSegment(value string) (string, bool) {
	if !strings.HasPrefix(value, "arn:") {
		return "", false
	}
	parts := strings.SplitN(value, ":", 7)
	if len(parts) < 7 || parts[2] != "rds" {
		return "", false
	}
	return parts[5], true
}

// iamAutoScalingTaggedGroups reads the group names a tagging call carries
// inside its tags. Only a tag saying it is on an Auto Scaling group counts: the
// member is generic, and a tag on anything else names something the call is not
// about.
func iamAutoScalingTaggedGroups(r *http.Request) []string {
	_ = r.ParseForm()
	resourceIDs := map[string]string{}
	resourceTypes := map[string]string{}
	for key, values := range r.Form {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		index, member, found := strings.Cut(strings.TrimPrefix(key, "Tags.member."), ".")
		if !found || index == "" {
			continue
		}
		switch member {
		case "ResourceId":
			resourceIDs[index] = values[0]
		case "ResourceType":
			resourceTypes[index] = values[0]
		}
	}
	var out []string
	for index, name := range resourceIDs {
		if resourceTypes[index] == "auto-scaling-group" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// The instance-to-group lookup SetInstanceHealth needs, answered from a
// generation-keyed index so a per-request derivation never decodes every group.
var iamAutoScalingGroupsByInstance sim.GenerationIndex[AutoScalingGroup]

// iamAutoScalingGroupInstanceKeys names a group by each machine it holds.
func iamAutoScalingGroupInstanceKeys(group AutoScalingGroup) []string {
	keys := make([]string, 0, len(group.InstanceIds))
	for _, instance := range group.InstanceIds {
		if instance != "" {
			keys = append(keys, instance)
		}
	}
	return keys
}

// iamEC2ElasticIPAddressKeys names an elastic IP by the address it carries,
// which is what an address transfer names it by.
func iamEC2ElasticIPAddressKeys(address EC2ElasticIP) []string {
	if address.PublicIp == "" {
		return nil
	}
	return []string{address.PublicIp}
}

// iamEC2NestedMembers reads one member out of every entry of a named list in a
// query request: "<list>.<index>.<member>", which is how Amazon EC2 spells a
// list of structures.
func iamEC2NestedMembers(r *http.Request, list, member string) []string {
	_ = r.ParseForm()
	var out []string
	seen := map[string]struct{}{}
	for key, values := range r.Form {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		index, rest, found := strings.Cut(strings.TrimPrefix(key, list+"."), ".")
		if !found || index == "" || rest != member {
			continue
		}
		if _, dup := seen[values[0]]; dup {
			continue
		}
		seen[values[0]] = struct{}{}
		out = append(out, values[0])
	}
	sort.Strings(out)
	return out
}

// iamSSMTargetedInstances reads the machines a request's targets name. AWS
// Systems Manager spells a target as a key and the values it selects, and only
// a target keyed on the instance id names machines.
func iamSSMTargetedInstances(r *http.Request) []string {
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var request struct {
		Targets []struct {
			Key    string   `json:"Key"`
			Values []string `json:"Values"`
		} `json:"Targets"`
	}
	if json.Unmarshal(body, &request) != nil {
		return nil
	}
	var out []string
	for _, target := range request.Targets {
		if !strings.EqualFold(target.Key, "InstanceIds") &&
			!strings.EqualFold(target.Key, "InstanceId") {
			continue
		}
		for _, value := range target.Values {
			if value != "" {
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out
}

// iamCloudTrailQueryFrom finds the event data stores a Lake query reads. A
// query names its first store after FROM and any others after JOIN, and a store
// is identified by a UUID — so a FROM naming anything else, a table alias or a
// subquery, matches nothing and derives nothing.
var iamCloudTrailQueryFrom = regexp.MustCompile(
	`(?i)\b(?:from|join)\s+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)

// iamStatesMachineOfVersion splits a state machine version ARN into the machine
// it is a version of and the version itself. A version ARN is the machine's own
// ARN with ":<version>" appended, so anything without that trailing number is
// not one.
func iamStatesMachineOfVersion(arn string) (machine, version string, ok bool) {
	if !strings.HasPrefix(arn, "arn:") || !strings.Contains(arn, ":stateMachine:") {
		return "", "", false
	}
	cut := strings.LastIndexByte(arn, ':')
	if cut < 0 {
		return "", "", false
	}
	version = arn[cut+1:]
	if version == "" || strings.ContainsFunc(version, func(r rune) bool { return r < '0' || r > '9' }) {
		return "", "", false
	}
	return arn[:cut], version, true
}

// The alarm-to-rule lookup a mute-rule listing needs, answered from a
// generation-keyed index so a per-request derivation never reads every rule.
var iamCloudWatchMuteRulesByAlarm sim.GenerationIndex[CWAlarmMuteRule]

// iamCloudWatchMuteRuleAlarmKeys names a mute rule by each alarm it covers.
func iamCloudWatchMuteRuleAlarmKeys(rule CWAlarmMuteRule) []string {
	keys := make([]string, 0, len(rule.AlarmNames))
	for _, alarm := range rule.AlarmNames {
		if alarm != "" {
			keys = append(keys, alarm)
		}
	}
	return keys
}
