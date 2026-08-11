package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Deriving the resource ARN a request names, for the services whose requests
// carry it.
//
// The work splits in two, and only one half is ours. Which resource type an
// action authorizes against is AWS's answer: iamActionResourceTypes is
// generated from the vendored AWS Service Reference
// (specs/cloud-api/aws/service-reference/), the same data the Service
// Authorization Reference publishes. This file supplies the other half —
// pulling the resource's identifier out of the request the SDK actually sent,
// which no specification can state — and assembles the ARN in the shape the
// reference declares for that type.
//
// Getting it wrong in either direction denies a call the policy allows: an
// action the gate leaves undivined is authorized against a literal "*", which
// matches only a policy whose Resource is itself "*".

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
	// Every service here spells its tagging operations the same way: the
	// resource is named by its own ARN, which needs no assembly. This is also
	// what resolves the tagging actions' long resource-type lists — TagResource
	// accepts nine ECS types, and the ARN says which one this call means.
	if a := iamRequestARNField(r); a != "" {
		return []string{a}
	}
	switch service {
	case "autoscaling":
		return iamAutoScalingResourceARNs(r, types)
	case "cloudtrail":
		return iamCloudTrailResourceARNs(r, types, region, account)
	case "dynamodb":
		return iamDynamoDBResourceARNs(r, types, region, account)
	case "ec2":
		return iamEC2ResourceARNs(r, types, region, account)
	case "ecs":
		return iamECSResourceARNs(r, op, types, arn)
	case "elasticache":
		return iamElastiCacheResourceARNs(r, types, region, account)
	case "events":
		return iamEventBridgeResourceARNs(r, types, region, account)
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
		return iamStatesResourceARNs(r, types)
	case "cloudwatch":
		return iamCloudWatchResourceARNs(r, types, region, account)
	case "elasticloadbalancing":
		return iamELBv2ResourceARNs(r, types, region, account)
	case "organizations":
		return iamOrganizationsResourceARNs(r, types)
	case "rds":
		return iamRDSResourceARNs(r, types, region, account)
	case "ssm":
		return iamSSMResourceARNs(r, types, region, account)
	case "codebuild":
		return iamCodeBuildResourceARNs(r, types, arn)
	case "wafv2":
		return iamWAFv2ResourceARNs(r, op, types)
	case "iam":
		return iamIAMResourceARNs(r, types)
	}
	return nil
}

// ===== AWS Identity and Access Management =====

// iamIAMResourceARNs derives the ARNs an IAM request names. IAM is a global
// service and its ARNs carry no region — "arn:aws:iam::<account>:role/<name>" —
// so they are assembled here rather than through the regional builder the
// other services use.
//
// IAM speaks the query protocol, so the identifiers are form parameters. A
// name may carry a path ("/team/", giving "role/team/name"); the API takes the
// path separately on create and folds it into the ARN, which is what the
// resource types call a "NameWithPath".
func iamIAMResourceARNs(r *http.Request, types []string) []string {
	account := awsAccountID()
	build := func(resourceType, path, name string) string {
		path = strings.Trim(path, "/")
		if path != "" {
			path += "/"
		}
		return "arn:aws:iam::" + account + ":" + resourceType + "/" + path + name
	}
	// The operations that act on a policy, an OIDC provider or a SAML provider
	// name it by ARN outright.
	for _, field := range []string{"PolicyArn", "OpenIDConnectProviderArn", "SAMLProviderArn", "PolicySourceArn"} {
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
	return nil
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

// ===== AWS Organizations =====

// iamOrganizationsResourceARNs derives the ARNs an AWS Organizations request
// names.
//
// Organizations is the service whose identifiers say what they identify. Every
// id the model declares carries a literal prefix — "r-" a root, "ou-" an
// organizational unit, "p-" a policy, "h-" a handshake, "rp-" a resource
// policy, "rt-" a responsibility transfer, and a bare twelve digits an account
// — and the members that accept more than one kind are alternations over
// exactly those prefixes: TaggableResourceId admits six, PolicyTargetId three,
// ParentId two. So which resource type a request names is read off the
// identifier, because that is the only way a caller can say it, and therefore
// the way AWS reads it too. Ten of these operations name a resource under a
// member whose own name says nothing about its type — TargetId, ParentId,
// ChildId, ResourceId — and no table of field spellings could type them.
//
// The ARN is then the resource's own. Four types carry an attribute no request
// supplies — a policy's type and whether AWS manages it, what a handshake is
// for, what a transfer moves and which way it moves, and a resource policy's
// assigned id — so those are read from the simulator's state, as Amazon RDS
// resolves a custom engine version. The rest are a function of the identifier
// alone and are built from it, which also covers a request naming a resource
// that does not exist: AWS authorizes such a call and then reports it missing,
// rather than refusing it as unauthorized.
func iamOrganizationsResourceARNs(r *http.Request, types []string) []string {
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

// ===== AWS Private Certificate Authority =====

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

// ===== AWS Cloud Map =====

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
	// The discovery operations address a namespace and a service by name, while
	// their ARNs carry the identifiers AWS assigned. Neither can be assembled
	// from the request, so both are read from the simulator's own state — the
	// same resolution Amazon RDS uses for a custom engine version, and for the
	// same reason: the ARN the gate requests has to be the ARN the resource
	// actually has.
	var out []string
	namespaceName := iamFirstValue(func(f string) []string { return fields[strings.ToLower(f)] }, "NamespaceName")
	serviceName := iamFirstValue(func(f string) []string { return fields[strings.ToLower(f)] }, "ServiceName")
	var namespaceID string
	if namespaceName != "" {
		for _, namespace := range cmNamespaces.List() {
			if namespace.Name == namespaceName {
				namespaceID = namespace.Id
				if namespace.Arn != "" {
					out = append(out, namespace.Arn)
				}
				break
			}
		}
	}
	if serviceName != "" {
		for _, service := range cmServices.List() {
			if service.Name != serviceName {
				continue
			}
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

// ===== Amazon Simple Notification Service =====

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

// ===== Amazon Simple Queue Service =====

// iamSQSResourceARNs derives the queue ARNs an Amazon SQS request names. A
// queue is addressed by its URL rather than by its ARN or its name, and the
// name is the URL's last segment — which is what the queue's ARN ends in.
func iamSQSResourceARNs(r *http.Request, types []string, region, account string) []string {
	if !iamHasType(types, "queue") {
		return nil
	}
	params := iamQueryRequestParameters(r)
	first := func(field string) string {
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

// ===== AWS Secrets Manager =====

// iamSecretsManagerResourceARNs derives the secret ARNs an AWS Secrets Manager
// request names. A secret is addressed by SecretId, which the API accepts as
// either the secret's name or its full ARN, and named in Name at creation —
// where SecretId does not exist yet, so a role holding exactly the grant it
// needs was denied against a policy that plainly granted it (GitHub issue #889).
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

// ===== Amazon Elastic Container Registry =====

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

// ===== Amazon Kinesis Data Streams =====

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

// ===== AWS Step Functions =====

// iamStatesResourceARNs derives the ARNs an AWS Step Functions request names.
// Every one of its resources is addressed by its own ARN — a state machine, an
// execution, an activity, a map run, a version, an alias — and the ARNs the
// reference publishes carry parts no request supplies separately: an execution
// ARN ends in an id AWS assigned, a labelled one carries the map run's label
// inside the state machine segment. So the ARN the caller sent is the ARN to
// authorize against, and there is nothing to assemble.
//
// The one name-addressed case is creation, where the state machine does not
// exist yet and no ARN can be derived at all.
func iamStatesResourceARNs(r *http.Request, types []string) []string {
	fields := iamJSONRequestFields(r)
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

// ===== Elastic Load Balancing =====

// iamELBv2ResourceARNs derives the ARNs an Elastic Load Balancing request
// names. Every one of its resource ARNs carries an identifier AWS assigned —
// "loadbalancer/app/${LoadBalancerName}/${LoadBalancerId}",
// "targetgroup/${TargetGroupName}/${TargetGroupId}" — which no request supplies
// as parts. What a request carries instead is the whole ARN, under a member
// named for the resource, so there is nothing to assemble: the ARN the caller
// sent is the ARN the gate authorizes against.
//
// The classic load balancer is the exception the reference keeps for the
// previous generation, addressed by name alone.
func iamELBv2ResourceARNs(r *http.Request, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
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
	// SetRulePriorities carries each rule's ARN inside a priority entry rather
	// than in a member of its own — "RulePriorities.member.1.RuleArn" — which
	// the flat-member reader drops, its contract being members and not paths.
	// A rule named anywhere in the request is still a rule the call is about,
	// so the raw form is read for those.
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

// ===== AWS Certificate Manager =====

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

// ===== Amazon CloudWatch =====

// iamCloudWatchResourceARNs derives the ARNs an Amazon CloudWatch request
// names. Its resources are addressed by name, and each type's name arrives
// under a member of its own — an alarm's AlarmName, a dashboard's
// DashboardName, an insight rule's RuleName — so filling the published format
// is all it takes.
func iamCloudWatchResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
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

// iamCloudWatchFieldAliases records where the API's spelling differs from the
// reference's variable, read off the vendored model rather than guessed: an
// insight rule's ${InsightRuleName} arrives as RuleName, and a metric stream's
// ${MetricStreamName} simply as Name.
var iamCloudWatchFieldAliases = map[string][]string{
	"InsightRuleName":  {"RuleName"},
	"MetricStreamName": {"Name"},
	"DatasetId":        {"DatasetIdentifier"},
}

// ===== Amazon EC2 Auto Scaling =====

// iamAutoScalingResourceARNs derives the ARNs an Amazon EC2 Auto Scaling
// request names. Both of its resource types carry two identifiers: one AWS
// assigns and one the caller chose —
// "autoScalingGroup:${GroupId}:autoScalingGroupName/${GroupFriendlyName}" — and
// a request supplies only the second. So neither can be assembled from the
// request, and both are read from the simulator's own state, where the ARN each
// resource was given is stored.
//
// That is the same resolution Amazon RDS uses for a custom engine version, and
// for the same reason: the ARN the gate requests has to be the ARN the resource
// actually has, and an ARN built from the parts a request happens to carry
// would not be.
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
		case "launchConfiguration":
			if config, ok := asLaunchConfigurations.Get(first("LaunchConfigurationName")); ok && config.ARN != "" {
				out = append(out, config.ARN)
			}
		}
	}
	return out
}

// ===== AWS CloudTrail =====

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
	fields := iamJSONRequestFields(r)
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
	"ChannelId":        {"Channel"},
	"EventDataStoreId": {"EventDataStore"},
	"DashboardName":    {"DashboardId"},
}

// ===== Amazon DynamoDB =====

// iamDynamoDBResourceARNs derives the ARNs an Amazon DynamoDB request names.
//
// Three of its resource types nest under a table — an index is
// "table/<name>/index/<index>" — and the table-driven derivation fills those
// from the request. Four more are named by their own ARN rather than by a
// table and a name: a backup, an export, an import and a stream, each under a
// member of its own, which needs no assembly.
//
// A transaction or a batch names no table at the top level at all: it carries
// one per item. Those are read in iamResourceARNsForRequest instead, before the
// reference is consulted, because the Service Reference lists neither
// TransactWriteItems nor TransactGetItems and so declares no type to drive them.
func iamDynamoDBResourceARNs(r *http.Request, types []string, region, account string) []string {
	// The operations that act on a backup, an export, an import or a stream
	// name it by ARN outright.
	for _, field := range []string{"BackupArn", "ExportArn", "ImportArn"} {
		if a := iamJSONBodyField(r, field); strings.HasPrefix(a, "arn:") {
			return []string{a}
		}
	}
	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("dynamodb", types, region, account, nil,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// ===== Amazon Elastic Compute Cloud =====

// iamEC2ResourceARNs derives the ARNs an Amazon EC2 request names. EC2 declares
// 112 resource types across 515 actions — too many to transcribe, and a
// transcription would rot — so the derivation is driven by the reference
// itself. Each type's published ARN format ends in the variable naming its
// identifier ("...:volume/${VolumeId}"), and EC2's query protocol carries that
// identifier in a request parameter of the same name.
//
// Filling the published format, rather than assembling a resource path, is what
// keeps the irregular shapes right: an Amazon Machine Image and a snapshot
// carry no account, the Amazon VPC IP Address Manager types carry no region,
// and five of EC2's types name a resource belonging to another service outright
// — a certificate is an AWS Certificate Manager ARN and a role an AWS Identity
// and Access Management one.
//
// Where a parameter is spelled differently from the variable the difference is
// one of two kinds. EC2 drops the resource's own leading word from some of them
// — a security group's ${SecurityGroupId} arrives as GroupId, a dedicated
// host's ${DedicatedHostId} as HostId — which is mechanical. The rest are
// genuine renamings, listed in iamEC2ParameterAliases.
func iamEC2ResourceARNs(r *http.Request, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	return iamTableDrivenARNs("ec2", types, region, account, iamEC2ParameterAliases,
		func(field string) []string { return params[strings.ToLower(field)] })
}

// iamEC2ParameterAliases maps an ARN format's identifier variable to the
// request parameters EC2 actually spells it as, where the two differ by more
// than the mechanical prefix drop. Every entry is a rename the API made and
// the reference did not follow: an endpoint service is addressed as ServiceId,
// a network ACL's ${NaclId} arrives as NetworkAclId, a key pair is named by
// KeyName, and the copy operations name their *source* resource.
//
// TestIAMEC2ParameterAliasesAreRealRequestParameters holds every entry to a
// parameter the vendored Amazon EC2 model declares on an operation that
// authorizes against that resource type, so a guess or a stale rename fails
// rather than silently deriving nothing.
var iamEC2ParameterAliases = map[string][]string{
	"CapacityReservationId":           {"SourceCapacityReservationId"},
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

// iamQueryRequestParameters indexes a query-protocol request's flat parameters
// by lower-cased name, collapsing both encodings a list arrives in so every
// element authorizes separately — terminating three instances must be allowed
// for all three, not only the first.
//
// Amazon EC2's protocol flattens a list to the member's singular name with a
// 1-based index (InstanceId.1, InstanceId.2). The awsQuery protocol Amazon RDS
// speaks boxes it instead (Names.member.1) unless the member is flattened.
// Members of a nested structure (Filters.Filter.1.Name) name no resource and
// are left out.
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

// ===== Deriving an ARN from the format the reference publishes =====

// iamTableDrivenARNs derives the ARNs a request names for a service whose
// resource types are in the generated table, by filling each type's published
// ARN format from the identifiers the request supplies. Everything that differs
// between services lives in the two arguments: aliases carries the renamings
// that service made and the reference did not follow, and lookup reads a field,
// which is the only thing the protocols disagree about — Amazon EC2 and Amazon
// RDS name their identifiers as query parameters, AWS Glue as JSON members.
//
// A format naming no identifier is a constant ARN — AWS Glue's root catalog is
// "arn:aws:glue:<region>:<account>:catalog" and every request that authorizes
// against it names it by existing — and is emitted as it stands.
//
// Two resource types can resolve to the same field, and then the identifier
// belongs to one of them without the request saying which. Sometimes that is
// answerable: RunInstances authorizes against a subnet and a secondary subnet,
// and a SubnetId is the subnet's published variable outright where it reaches
// the secondary only through the prefix-drop rule, so the exact spelling takes
// it. Where the claims are equally strong neither is derived — building both
// would invent an ARN for a resource that does not exist and then require it to
// be allowed, denying a policy that named the real one — and the request's
// other fields still are.
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

// iamVariableFieldNames returns the field spellings an ARN format variable can
// arrive under, most specific first: the variable itself, then that service's
// declared renamings, then the form with the resource's own leading word
// dropped, and finally the plural of each — a batch operation names the same
// resource in a list (AWS Glue's BatchGetJobs sends JobNames).
//
// A declared renaming outranks the prefix drop because it is evidence and the
// drop is a guess: every alias is held to the vendored model by a test, where
// the drop is a rule about spelling that can land on a field meaning something
// else. AWS Glue is where the order shows. A catalog's ${CatalogName} drops to
// Name, which on GetTable is the *table's* name, so ranking the drop first made
// the catalog and the table claim one field and the ambiguity rule then
// discarded both. The catalog's declared CatalogId settles it.
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

// ===== Amazon EventBridge =====

// iamEventBridgeResourceARNs derives the ARNs an Amazon EventBridge request
// names. Its identifiers are ordinary — a connection's ${ConnectionName}
// arrives as Name, which the prefix drop resolves — but two of its resource
// types are one resource seen two ways, and the reference cannot say which.
//
// A rule on the default event bus is "rule/${RuleName}"; a rule on a custom bus
// is "rule/${EventBusName}/${RuleName}". Both end in the same variable, so both
// claim the same request member and the ambiguity rule would discard the pair.
// Which applies is decidable, and not by spelling: a request that names a
// custom bus means the nested form, and one that names none, or names the
// default bus, means the flat one. So the type that cannot apply is dropped
// before the derivation runs, leaving one claim on the member.
//
// Four more types are constants — the built-in targets EventBridge invokes on
// an instance, published as "target/reboot-instance" and its three siblings —
// and two belong to AWS KMS, which filling the published format gets right.
func iamEventBridgeResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	lookup := func(field string) []string { return fields[strings.ToLower(field)] }

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
	return iamTableDrivenARNs("events", applicable, region, account, nil, lookup)
}

// ===== AWS Glue =====

// iamGlueResourceARNs derives the ARNs an AWS Glue request names. Glue speaks
// awsJson, so the identifiers are members of the request body rather than query
// parameters, and it is the service that makes the two general shapes of the
// derivation earn their keep.
//
// Its ARN formats nest: a table is "table/${DatabaseName}/${TableName}" and a
// table version adds a third part, so an ARN is only built when the request
// names every part — a half-filled ARN would carry a literal ${DatabaseName}
// and match no policy. Its root catalog is the other extreme, a format naming
// no identifier at all, so every request that authorizes against the catalog
// names it by existing.
//
// Glue also abbreviates almost every identifier the same way: the reference
// calls a blueprint's identifier ${BlueprintName} and the request member is
// Name, which the prefix drop resolves, and a batch operation sends the plural
// of whichever spelling it uses.
func iamGlueResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
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
		if json.Unmarshal(value, &inner) != nil {
			continue
		}
		for innerName, innerValue := range inner {
			if values, ok := iamJSONStrings(innerValue); ok {
				nested[strings.ToLower(innerName)] = values
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

// ===== Amazon Relational Database Service =====

// iamRDSResourceARNs derives the ARNs an Amazon RDS request names. RDS speaks
// the awsQuery protocol, so the identifiers are form parameters, and it is the
// service where the reference and the API disagree about spelling on almost
// every resource: the reference calls a database instance ${DbInstanceName} and
// a cluster parameter group ${ClusterParameterGroupName} where the API sends
// DBInstanceIdentifier and DBClusterParameterGroupName. Those renamings are the
// whole of iamRDSFieldAliases, and each was read off the vendored model — the
// parameter the operations authorizing against that type actually take —
// rather than derived from the name by a pattern.
//
// Two of the twenty-four types carry an identifier no request names — a custom
// engine version's own id and a proxy target group's — and are resolved through
// the simulator's state instead, so every one of the twenty-four derives.
func iamRDSResourceARNs(r *http.Request, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	lookup := func(field string) []string { return params[strings.ToLower(field)] }

	// Two of Amazon RDS's ARNs carry an identifier no request ever names. A
	// custom engine version is addressed by its engine and version, a proxy
	// target group by its proxy and group name, and each ARN carries an id AWS
	// assigns instead. Those two are resolved through the simulator's own state,
	// which is what makes the derived ARN the one the resource actually has
	// rather than one assembled to look right.
	var stateful []string
	generic := make([]string, 0, len(types))
	for _, resourceType := range types {
		switch resourceType {
		case "cev":
			if cev, ok := rdsCustomEngineVersions.Get(
				rdsCEVKey(iamFirstValue(lookup, "Engine"), iamFirstValue(lookup, "EngineVersion"))); ok {
				stateful = append(stateful, cev.ARN)
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

// ===== AWS Systems Manager =====

// iamSSMResourceARNs derives the ARNs an AWS Systems Manager request names.
// Systems Manager speaks awsJson, so the identifiers are request members, and
// it is the service that makes a variable's *name* carry more than a spelling.
//
// Four of its resource types — a maintenance window, an OpsItem, that item's
// metadata and a service setting — are all published as ${ResourceId}, and the
// request names each differently, so their aliases are keyed by resource type
// rather than by variable. A parameter is published as
// ${ParameterNameWithoutLeadingSlash} and named "/db/password" in every
// request, so the value loses its leading slash on the way into the ARN, which
// is the transformation the variable's own name states.
//
// Four more of its types are another service's resource outright: an instance
// is an Amazon EC2 ARN, a task an Amazon ECS one, a role an IAM one, and a
// bucket an Amazon S3 ARN carrying neither region nor account. Filling the
// published format is what keeps each of those right.
func iamSSMResourceARNs(r *http.Request, types []string, region, account string) []string {
	fields := iamJSONRequestFields(r)
	return iamTableDrivenARNs("ssm", types, region, account, iamSSMFieldAliases,
		func(field string) []string { return fields[strings.ToLower(field)] })
}

// iamSSMFieldAliases maps an ARN format's identifier variable to the request
// members AWS Systems Manager spells it as. An entry keyed "<type>.<variable>"
// answers for that resource type alone.
//
// TestIAMSSMFieldAliasesAreRealRequestMembers holds every entry to a member the
// vendored model declares on an operation authorizing against that type.
var iamSSMFieldAliases = map[string][]string{
	"maintenancewindow.ResourceId":               {"WindowId"},
	"opsitem.ResourceId":                         {"OpsItemId", "OpsItemArn"},
	"opsmetadata.ResourceId":                     {"OpsMetadataArn"},
	"servicesetting.ResourceId":                  {"SettingId"},
	"patchbaseline.PatchBaselineIdResourceId":    {"BaselineId"},
	"parameter.ParameterNameWithoutLeadingSlash": {"Name"},
}

// ===== Amazon ElastiCache =====

// iamElastiCacheResourceARNs derives the ARNs an Amazon ElastiCache request
// names. ElastiCache is the service that needed no renamings at all: every one
// of its twelve resource types is published under the parameter the API sends,
// so the derivation is the published ARN format and the request, with nothing
// hand-written in between.
func iamElastiCacheResourceARNs(r *http.Request, types []string, region, account string) []string {
	params := iamQueryRequestParameters(r)
	return iamTableDrivenARNs("elasticache", types, region, account, nil,
		func(field string) []string { return params[strings.ToLower(field)] })
}

// ===== Amazon Elastic Container Service =====

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

	if iamHasType(types, "task-set") {
		addNamed("task-set/"+cluster+"/"+iamFirstJSONField(r, "service")+"/",
			iamNamesFrom(r, []string{"taskSet"}, []string{"taskSets"}))
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
	}
	if iamHasType(types, "capacity-provider") {
		addNamed("capacity-provider/",
			iamNamesFrom(r, []string{"capacityProvider", "name"}, []string{"capacityProviders"}))
	}
	if iamHasType(types, "task-definition") {
		addNamed("task-definition/", iamECSTaskDefinitionIDs(r))
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

// iamECSClusterName resolves the cluster a request targets, accepting the name
// or the ARN the API accepts interchangeably and defaulting to "default".
func iamECSClusterName(r *http.Request) string {
	name := iamFirstJSONField(r, "cluster", "clusterName")
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

// ===== AWS Key Management Service =====

// iamKMSResourceARNs derives the ARNs an AWS KMS request names. A key is named
// by KeyId, which may already be an ARN and is passed through when it is.
//
// An alias needs one thing said explicitly. Its ARN is "alias/${Alias}", and the
// API's AliasName already carries that prefix — an alias is created and deleted
// as "alias/my-key", not "my-key" — so filling the format with it unchanged
// would produce "alias/alias/my-key", which names nothing. The prefix is
// stripped once so the ARN carries it once.
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

// ===== Amazon CloudWatch Logs =====

// iamLogsResourceARNs derives the ARNs an Amazon CloudWatch Logs request names.
// The service defines two nested resource types and the distinction is not
// cosmetic: a log stream's ARN is the group's with ":log-stream:<name>"
// appended, so a policy granting the group alone does not cover the four
// stream-scoped actions and a policy written "<group-arn>:*" covers those but
// not the group-scoped reads. The gate requests whichever type AWS declares.
func iamLogsResourceARNs(r *http.Request, types []string, arn func(svc, resource string) string) []string {
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

// ===== AWS CodeBuild =====

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
	}
	if iamHasType(types, "fleet") {
		addNamed("fleet/", iamNamesFrom(r, []string{"name"}, []string{"names"}))
	}
	return out
}

// ===== AWS WAFv2 =====

// iamWAFv2ResourceARNs derives the ARNs an AWS WAFv2 request names. WAFv2 is
// the one service here whose resource ARN cannot be assembled from the request
// alone in a general way: the ARN carries the resource's generated id and its
// scope path, and the type is the operation's own suffix (GetIPSet names an
// ipset, GetWebACL a webacl). wafARN builds it exactly as the handlers do, so
// a derived ARN is the ARN the resource actually has.
//
// Operations that reference other entities inside a rule statement —
// CreateWebACL naming rule groups and IP sets — authorize against the entity
// the request creates or reads; the referenced entities are not derived, and
// the gate never widens beyond what the request names.
func iamWAFv2ResourceARNs(r *http.Request, op string, types []string) []string {
	resourceType := ""
	for _, candidate := range []struct{ suffix, resource string }{
		{"WebACL", "webacl"},
		{"IPSet", "ipset"},
		{"RuleGroup", "rulegroup"},
		{"RegexPatternSet", "regexpatternset"},
		{"ManagedRuleSet", "managedruleset"},
	} {
		if strings.HasSuffix(op, candidate.suffix) && iamHasType(types, candidate.resource) {
			resourceType = candidate.resource
			break
		}
	}
	if resourceType == "" {
		return nil
	}
	if a := iamFirstJSONField(r, "ARN", "WebACLArn"); strings.HasPrefix(a, "arn:") {
		return []string{a}
	}
	name, id := iamJSONBodyField(r, "Name"), iamJSONBodyField(r, "Id")
	if name == "" {
		return nil
	}
	return []string{wafARN(iamJSONBodyField(r, "Scope"), resourceType, name, id)}
}
