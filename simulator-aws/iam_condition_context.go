package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
	"github.com/fxamacker/cbor/v2"
)

// Resource-scoped + service-specific IAM condition keys. The policy evaluator
// supports the condition operators; this feeds the request's target resource
// into the condition context so tag- and cluster-conditioned grants enforce
// faithfully:
//
//   - aws:ResourceTag/<k> (and the service-prefixed ec2:ResourceTag/<k>) — the
//     tags of the resource the request targets (e.g. the volume DeleteVolume
//     acts on), so a policy allowing the action only on resources carrying a
//     given tag matches when, and only when, the resource carries it.
//   - ecs:cluster — the cluster ARN an ECS task operation targets.
//   - aws:RequestTag/<k> + aws:TagKeys — the tags supplied on a tag-on-create /
//     CreateTags request.

// iamPopulateGlobalConditionKeys adds the global (`aws:`) condition keys that
// derive from the request envelope and the calling principal: the request time,
// transport, user-agent, the principal ARN + its tags, the resource account, and
// MFA state. Service-to-service keys (aws:SourceArn/SourceAccount/CalledVia/
// ViaAWSService) are intentionally absent — the sim originates direct client
// calls, not service-initiated ones, so in real AWS those keys are absent here
// too. aws:PrincipalOrgID is absent because the sim models a single account with
// no Organizations slice.
func iamPopulateGlobalConditionKeys(r *http.Request, akid, principalArn, userName string, ctx map[string][]string) {
	now := time.Now().UTC()
	ctx["aws:CurrentTime"] = []string{now.Format(time.RFC3339)}
	ctx["aws:EpochTime"] = []string{strconv.FormatInt(now.Unix(), 10)}
	ctx["aws:SecureTransport"] = []string{strconv.FormatBool(r.TLS != nil)}
	if ua := r.Header.Get("User-Agent"); ua != "" {
		ctx["aws:UserAgent"] = []string{ua}
	}
	if principalArn != "" {
		ctx["aws:PrincipalArn"] = []string{principalArn}
	}
	ctx["aws:ResourceAccount"] = []string{awsAccountID()}

	// Principal tags (a user's tags, or an assumed role's tags).
	for _, t := range iamPrincipalTags(akid, userName) {
		ctx["aws:PrincipalTag/"+t.Key] = []string{t.Value}
	}

	// MFA: present only for an MFA-authenticated temporary session.
	mfa, age := iamSessionMFA(akid)
	ctx["aws:MultiFactorAuthPresent"] = []string{strconv.FormatBool(mfa)}
	if mfa {
		ctx["aws:MultiFactorAuthAge"] = []string{strconv.FormatInt(age, 10)}
	}
}

// iamPrincipalTags returns the calling principal's tags (user tags for an AKIA
// key; the assumed role's tags for an ASIA session).
func iamPrincipalTags(akid, userName string) []IAMTag {
	if userName != "" {
		if u, ok := iamUsers.Get(userName); ok {
			return u.Tags
		}
	}
	if tc, ok := iamTempCreds.Get(akid); ok && tc.RoleName != "" {
		if role, rok := iamRoles.Get(tc.RoleName); rok {
			return role.Tags
		}
	}
	return nil
}

// iamSessionMFA reports whether the credential is an MFA-authenticated session
// and, if so, its age in seconds.
func iamSessionMFA(akid string) (present bool, ageSeconds int64) {
	tc, ok := iamTempCreds.Get(akid)
	if !ok || !tc.MFA {
		return false, 0
	}
	if created, err := time.Parse(time.RFC3339, tc.CreatedAt); err == nil {
		return true, int64(time.Since(created).Seconds())
	}
	return true, 0
}

// iamPopulateResourceConditionKeys augments ctx with the resource-scoped and
// service-specific condition keys implied by the request.
func iamPopulateResourceConditionKeys(r *http.Request, action string, ctx map[string][]string) {
	service := strings.SplitN(action, ":", 2)[0]
	switch service {
	case "ec2":
		iamPopulateEC2ResourceTags(r, ctx)
	case "ecs":
		iamPopulateECSCluster(r, ctx)
	default:
		// Every other tag-storing sim service resolves the request's target
		// resource into aws:ResourceTag/<k> + <service>:ResourceTag/<k>.
		iamPopulateServiceResourceTags(r, service, ctx)
	}
	iamPopulateRequestTags(r, ctx)
}

// iamPopulateEC2ResourceTags resolves the tags of the EC2 resource the request
// targets and exposes them as aws:ResourceTag/<k> and ec2:ResourceTag/<k>.
func iamPopulateEC2ResourceTags(r *http.Request, ctx map[string][]string) {
	tags, ok := iamEC2RequestResourceTags(r)
	if !ok {
		return
	}
	for _, t := range tags {
		ctx["aws:ResourceTag/"+t.Key] = []string{t.Value}
		ctx["ec2:ResourceTag/"+t.Key] = []string{t.Value}
	}
}

// iamEC2RequestResourceTags returns the tags of the first EC2 resource the
// request references by id (volume / snapshot / instance / network interface).
func iamEC2RequestResourceTags(r *http.Request) ([]EC2Tag, bool) {
	for _, param := range []string{"VolumeId", "SnapshotId", "InstanceId", "InstanceId.1", "NetworkInterfaceId", "ResourceId", "ResourceId.1"} {
		id := r.FormValue(param)
		if id == "" {
			continue
		}
		switch {
		case strings.HasPrefix(id, "vol-"):
			if v, ok := ec2Volumes.Get(id); ok {
				return v.Tags, true
			}
		case strings.HasPrefix(id, "snap-"):
			if s, ok := ec2Snapshots.Get(id); ok {
				return s.Tags, true
			}
		case strings.HasPrefix(id, "i-"):
			if i, ok := ec2Instances.Get(id); ok {
				return i.Tags, true
			}
		case strings.HasPrefix(id, "eni-"):
			if e, ok := ec2NetworkInterfaces.Get(id); ok {
				return e.Tags, true
			}
		}
	}
	return nil, false
}

// iamPopulateECSCluster exposes ecs:cluster (the targeted cluster's ARN) for an
// ECS task operation. ECS is awsJson, so the cluster lives in the request body;
// the body is read and restored so the downstream handler still sees it.
func iamPopulateECSCluster(r *http.Request, ctx map[string][]string) {
	if r.Body == nil {
		return
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return
	}
	var req struct {
		Cluster string `json:"cluster"`
	}
	if json.Unmarshal(body, &req) != nil {
		return
	}
	name := req.Cluster
	if name == "" {
		name = "default"
	}
	arn := name
	if !strings.HasPrefix(name, "arn:") {
		arn = ecsArn("cluster", name)
	}
	ctx["ecs:cluster"] = []string{arn}
}

// iamPopulateRequestTags exposes aws:RequestTag/<k> + aws:TagKeys from the tags
// supplied on a tag-on-create / CreateTags request (Tag.N.Key/Value form).
func iamPopulateRequestTags(r *http.Request, ctx map[string][]string) {
	tags := parseIndexedTags(r, "Tag")
	if len(tags) == 0 {
		return
	}
	var keys []string
	for _, t := range tags {
		ctx["aws:RequestTag/"+t.Key] = []string{t.Value}
		keys = append(keys, t.Key)
	}
	ctx["aws:TagKeys"] = keys
}

// iamPopulateServiceConditionKeys adds the service-prefixed condition keys the
// request itself determines.
//
// A service condition key is how AWS scopes an action whose resource the
// request does not name: a policy allows cloudwatch:PutMetricData only where
// `cloudwatch:namespace` equals one value, or an Amazon EC2 action only in one
// `ec2:Region`. The evaluator matches conditions by key name and the simulator
// populated only the global `aws:` keys, so every such policy was evaluated
// against a context missing the key it tests — and a StringEquals on a key that
// is not there does not match, so the policy denied the very request it was
// written to allow.
//
// Only keys the request itself settles are added. A key whose value is a fact
// about a resource the simulator would have to look up, or about a caller
// relationship it does not model, is left absent, which is what AWS does with a
// key that does not apply.
func iamPopulateServiceConditionKeys(r *http.Request, action string, body []byte, ctx map[string][]string) {
	service, name, _ := strings.Cut(action, ":")

	// ec2:Region is the region the request was made in, which is the same fact
	// aws:RequestedRegion carries. It is the most-declared action condition key
	// in the vendored service references by a wide margin.
	if region := iamRequestedRegion(r); region != "" {
		switch service {
		case "ec2":
			ctx["ec2:Region"] = []string{region}
		}
	}

	// The Amazon S3 keys below describe how the request was signed and carried,
	// which the request states outright.
	if service == "s3" {
		if r.TLS != nil {
			ctx["s3:TlsVersion"] = []string{iamTLSVersionName(r.TLS.Version)}
		}
		authorization := r.Header.Get("Authorization")
		switch {
		case strings.HasPrefix(authorization, "AWS4-HMAC-SHA256"):
			ctx["s3:authType"] = []string{"REST-HEADER"}
			ctx["s3:signatureversion"] = []string{"AWS4-HMAC-SHA256"}
		case r.URL.Query().Get("X-Amz-Signature") != "":
			ctx["s3:authType"] = []string{"REST-QUERY-STRING"}
			ctx["s3:signatureversion"] = []string{"AWS4-HMAC-SHA256"}
		}
		if digest := r.Header.Get("x-amz-content-sha256"); digest != "" {
			ctx["s3:x-amz-content-sha256"] = []string{digest}
		}
		// s3:signatureAge is how long ago the request was signed, in
		// milliseconds — the fact a policy tests to refuse a long-lived
		// presigned URL.
		if signed := iamSigV4SigningTime(r); !signed.IsZero() {
			age := time.Since(signed).Milliseconds()
			if age < 0 {
				age = 0
			}
			ctx["s3:signatureAge"] = []string{strconv.FormatInt(age, 10)}
		}
		ctx["s3:ResourceAccount"] = []string{awsAccountID()}
		iamPopulateS3RequestConditionKeys(r, ctx)
		iamPopulateS3LocationConstraint(body, ctx)
		// s3:versionid is the object version the request names, which is how a
		// policy grants a read of the current object and not of its history.
		if version := r.URL.Query().Get("versionId"); version != "" {
			ctx["s3:versionid"] = []string{version}
		}
	}

	// cloudwatch:namespace is the namespace the metrics in the request are
	// written to, and it is the documented way to scope PutMetricData — an
	// action whose resource type the request never names.
	if service == "cloudwatch" && name == "PutMetricData" {
		if namespace := cloudWatchRequestNamespace(r, body); namespace != "" {
			ctx["cloudwatch:namespace"] = []string{namespace}
		}
	}

	// kms:CallerAccount is the account the caller belongs to, which for this
	// simulator is the account it serves.
	if service == "kms" {
		ctx["kms:CallerAccount"] = []string{awsAccountID()}
		// The encryption context the request supplied, which is how a policy
		// grants a decrypt only for data encrypted under one context.
		iamPopulateKMSEncryptionContext(body, ctx)
	}

	if service == "dynamodb" {
		iamPopulateDynamoDBConditionKeys(r, action, body, ctx)
	}

	// kms:EncryptionAlgorithm is the algorithm the request asks the key to use,
	// which a policy pins so a key is never used with a weaker one.
	if service == "kms" {
		if algorithm := iamRequestParameter(r, body, "EncryptionAlgorithm"); algorithm != "" {
			ctx["kms:EncryptionAlgorithm"] = []string{algorithm}
		}
	}

	// rds:PubliclyAccessible is whether the request asks for an instance
	// reachable from the internet, which a policy refuses outright.
	if service == "rds" {
		if public := iamRequestParameter(r, body, "PubliclyAccessible"); public != "" {
			ctx["rds:PubliclyAccessible"] = []string{public}
		}
	}

	// The AWS Auto Scaling target a request is about: which service's resource
	// it scales, and which dimension of it.
	if service == "application-autoscaling" {
		if namespace := iamRequestParameter(r, body, "ServiceNamespace"); namespace != "" {
			ctx["application-autoscaling:service-namespace"] = []string{namespace}
		}
		if dimension := iamRequestParameter(r, body, "ScalableDimension"); dimension != "" {
			ctx["application-autoscaling:scalable-dimension"] = []string{dimension}
		}
	}

	// iam:PolicyARN is the managed policy a request attaches or detaches, which
	// is how an administrator delegates policy attachment for one policy only.
	if service == "iam" {
		if arn := iamRequestParameter(r, body, "PolicyArn"); arn != "" {
			ctx["iam:PolicyARN"] = []string{arn}
		}
	}

	// lambda:FunctionUrlAuthType is the authentication a function URL is
	// configured with, which a policy pins so no URL is ever left open.
	if service == "lambda" {
		if authType := iamRequestParameter(r, body, "AuthType"); authType != "" {
			ctx["lambda:FunctionUrlAuthType"] = []string{authType}
		}
	}

	// lambda:FunctionArn is the function an event-source mapping or a function
	// URL is about — the request names it directly where it creates one, and
	// names the mapping whose function it is everywhere else.
	if service == "lambda" {
		if arn := iamLambdaRequestFunctionARN(r, body); arn != "" {
			ctx["lambda:FunctionArn"] = []string{arn}
		}
	}

	// servicediscovery:ServiceCreatedByAccount is the account that created the
	// AWS Cloud Map service the request names. Every service here was created
	// through the account this simulator serves, and a service that does not
	// exist settles no key.
	if service == "servicediscovery" {
		if id := iamRequestParameter(r, body, "Id"); id != "" {
			if _, ok := cmServices.Get(id); ok {
				ctx["servicediscovery:ServiceCreatedByAccount"] = []string{awsAccountID()}
			}
		}
	}

	// The AWS Organizations account transfer a request is about: which way it
	// moves and what kind it is, both stated by the request.
	if service == "organizations" {
		if direction := iamRequestParameter(r, body, "TransferDirection"); direction != "" {
			ctx["organizations:TransferDirection"] = []string{direction}
		}
		if kind := iamRequestParameter(r, body, "TransferType"); kind != "" {
			ctx["organizations:TransferType"] = []string{kind}
		}
	}

	// ssm:DocumentType is the kind of document the request is about, read from
	// the document it names — a policy uses it to allow Automation runbooks and
	// not Command documents, or the reverse.
	if service == "ssm" {
		if name := iamRequestParameter(r, body, "Name"); name != "" {
			if document, ok := ssmDocuments.Get(name); ok && document.DocumentType != "" {
				ctx["ssm:DocumentType"] = []string{document.DocumentType}
			}
		}
	}

	// events:creatorAccount is the account that created the rule the request
	// names. Every rule here was created through the account this simulator
	// serves, so that is the answer for a rule that exists, and there is none
	// for a rule that does not.
	if service == "events" {
		if name := iamRequestParameter(r, body, "Name"); name != "" {
			// A rule is stored under its bus and its name, and a request that
			// names no bus is about the default one.
			key := ebRuleKey(iamRequestParameter(r, body, "EventBusName"), name)
			if _, ok := ebRules.Get(key); ok {
				ctx["events:creatorAccount"] = []string{awsAccountID()}
			}
		}
	}

	// states:StateMachineQualifier is the version or alias the request names,
	// which is what a policy scopes on to allow a call against one published
	// version and not another.
	if service == "states" {
		if qualifier := sfnRequestQualifier(r, body); qualifier != "" {
			ctx["states:StateMachineQualifier"] = []string{qualifier}
		}
	}

	// ecs:propagate-tags is where the request asks Amazon ECS to copy tags
	// from, which a policy uses to require that they come from the service.
	if service == "ecs" {
		if propagate := iamRequestParameter(r, body, "propagateTags"); propagate != "" {
			ctx["ecs:propagate-tags"] = []string{propagate}
		}
		iamPopulateECSConditionKeys(r, body, ctx)
	}

	// rds:ManageMasterUserPassword is whether the request asks Amazon RDS to
	// manage the master password in AWS Secrets Manager, which a policy uses to
	// require that a password never be supplied by hand.
	if service == "rds" {
		if managed := iamRequestParameter(r, body, "ManageMasterUserPassword"); managed != "" {
			ctx["rds:ManageMasterUserPassword"] = []string{managed}
		}
	}

	// iam:PermissionsBoundary is the boundary policy the request asks to
	// attach, which is how an administrator delegates user creation while
	// requiring every created principal to carry a boundary.
	if service == "iam" {
		if boundary := iamRequestParameter(r, body, "PermissionsBoundary"); boundary != "" {
			ctx["iam:PermissionsBoundary"] = []string{boundary}
		}
	}

	// The Amazon RDS request tags, in the spelling RDS declares for them.
	if service == "rds" {
		for _, tag := range parseIndexedTags(r, "Tags.Tag") {
			ctx["rds:req-tag/"+tag.Key] = []string{tag.Value}
		}
	}

	if service == "secretsmanager" {
		iamPopulateSecretsManagerConditionKeys(r, body, ctx)
	}

	if service == "s3" {
		iamPopulateS3ObjectConditionKeys(r, ctx)
		// The access point a request was addressed through, which is how a
		// policy grants a read only when it arrives through one front door.
		// A request signed with credentials S3 Access Grants issued names the
		// instance that issued them, which is how a policy grants access only
		// to callers who came through Access Grants.
		if issued, ok := s3AccessGrantsCredentials.Get(iamAccessKeyIDFromRequest(r)); ok {
			ctx["s3:AccessGrantsInstanceArn"] = []string{issued.InstanceARN}
		}
		if ap, addressed := s3RequestAccessPoint(r); addressed {
			ctx["s3:DataAccessPointArn"] = []string{s3AccessPointARN(ap.AccountID, ap.Name)}
			ctx["s3:DataAccessPointAccount"] = []string{ap.AccountID}
			ctx["s3:AccessPointNetworkOrigin"] = []string{s3AccessPointNetworkOrigin(ap)}
		}
	}

	// kms:RequestAlias is the alias the request named the key by, which is how
	// a policy grants use of a key through one alias and not another. It is
	// absent when the request named the key some other way, which is what AWS
	// does with a key that does not apply.
	if service == "kms" {
		if keyID := iamRequestParameter(r, body, "KeyId"); strings.HasPrefix(keyID, "alias/") {
			ctx["kms:RequestAlias"] = []string{keyID}
		}
	}

	// organizations:PolicyType is the kind of policy the request is about: the
	// request states it outright where it creates or enables one, and names the
	// policy whose type it is everywhere else.
	if service == "organizations" {
		if policyType := iamOrganizationsRequestPolicyType(r, body); policyType != "" {
			ctx["organizations:PolicyType"] = []string{policyType}
		}
	}
}

// iamOrganizationsRequestPolicyType reads the policy type an AWS Organizations
// request is about, either from the type the request states or from the policy
// it names.
func iamOrganizationsRequestPolicyType(r *http.Request, body []byte) string {
	if policyType := iamRequestParameter(r, body, "Type"); policyType != "" {
		return policyType
	}
	if policyType := iamRequestParameter(r, body, "PolicyType"); policyType != "" {
		return policyType
	}
	policyID := iamRequestParameter(r, body, "PolicyId")
	if policyID == "" {
		return ""
	}
	policy, ok := orgPolicies.Get(policyID)
	if !ok {
		return ""
	}
	return policy.Type
}

// iamRequestParameter reads one parameter of a request whichever way the
// service carries it: a query-protocol form value, or a top-level member of an
// awsJson body.
func iamRequestParameter(r *http.Request, body []byte, name string) string {
	if value := r.FormValue(name); value != "" {
		return value
	}
	if len(body) == 0 {
		return ""
	}
	var document map[string]any
	if json.Unmarshal(body, &document) != nil {
		return ""
	}
	value, _ := document[name].(string)
	return value
}

// iamPopulateSecretsManagerConditionKeys adds the keys AWS Secrets Manager
// declares against its actions: the identifier the request named, and the
// facts about the secret it resolves to that a policy scopes on.
func iamPopulateSecretsManagerConditionKeys(r *http.Request, body []byte, ctx map[string][]string) {
	secretID := iamRequestParameter(r, body, "SecretId")
	if secretID == "" {
		return
	}
	ctx["secretsmanager:SecretId"] = []string{secretID}

	name, ok := resolveSecretKeyForRequest(r, secretID)
	if !ok {
		return
	}
	secret, ok := smSecrets.Get(name)
	if !ok {
		return
	}
	// resource/Type is the kind of secret, which Secrets Manager derives from
	// the service that owns it: a secret this project created through the API
	// belongs to no other service, and "other" is what that is.
	ctx["secretsmanager:resource/Type"] = []string{"other"}
	if secret.RotationLambdaARN != "" {
		ctx["secretsmanager:resource/AllowRotationLambdaArn"] = []string{secret.RotationLambdaARN}
	}
	primary := secret.PrimaryRegion
	if primary == "" {
		primary = awsRegion()
	}
	ctx["secretsmanager:SecretPrimaryRegion"] = []string{primary}
}

// iamPopulateS3ObjectConditionKeys adds s3:ExistingObjectTag/<key> — the tags
// already on the object the request targets, which is how a policy grants a
// read of objects somebody tagged one way and not another. The tags are the
// object's own, read at the time of the question.
func iamPopulateS3ObjectConditionKeys(r *http.Request, ctx map[string][]string) {
	bucket := sim.PathParam(r, "bucket")
	key := sim.PathParam(r, "key")
	if bucket == "" || key == "" {
		return
	}
	tags, ok := s3ObjectTags.Get(bucket + "/" + key)
	if !ok {
		return
	}
	for tagKey, tagValue := range tags {
		ctx["s3:ExistingObjectTag/"+tagKey] = []string{tagValue}
	}
}

// iamTLSVersionName spells a TLS version the way s3:TlsVersion does.
func iamTLSVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	}
	return ""
}

// cloudWatchRequestNamespace reads the namespace a PutMetricData call writes
// its metrics to. Amazon CloudWatch serves that operation over two protocols:
// the Query protocol, where the namespace is a form parameter net/http has
// already parsed, and Smithy RPC v2 CBOR, where it is a request member the SDK
// additionally gzip-compresses.
func cloudWatchRequestNamespace(r *http.Request, body []byte) string {
	if namespace := r.FormValue("Namespace"); namespace != "" {
		return namespace
	}
	raw, err := cwDecompress(body)
	if err != nil {
		return ""
	}
	var request struct {
		Namespace string `cbor:"Namespace"`
	}
	if cbor.Unmarshal(raw, &request) != nil {
		return ""
	}
	return request.Namespace
}

// iamSigV4SigningTime reads the instant a SigV4 request was signed. Both forms
// of the signature carry it: the X-Amz-Date header on a header-signed request
// and the X-Amz-Date query parameter on a presigned URL.
func iamSigV4SigningTime(r *http.Request) time.Time {
	stamp := r.Header.Get("X-Amz-Date")
	if stamp == "" {
		stamp = r.URL.Query().Get("X-Amz-Date")
	}
	signed, err := time.Parse("20060102T150405Z", stamp)
	if err != nil {
		return time.Time{}
	}
	return signed
}

// iamPopulateKMSEncryptionContext adds the AWS KMS encryption context a request
// supplied: each pair under its own key, and the set of names under
// kms:EncryptionContextKeys, which is how a policy requires that a key be used
// only for data labelled a particular way.
func iamPopulateKMSEncryptionContext(body []byte, ctx map[string][]string) {
	if len(body) == 0 {
		return
	}
	var request struct {
		EncryptionContext map[string]string `json:"EncryptionContext"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.EncryptionContext) == 0 {
		return
	}
	names := make([]string, 0, len(request.EncryptionContext))
	for name, value := range request.EncryptionContext {
		ctx["kms:EncryptionContext:"+name] = []string{value}
		names = append(names, name)
	}
	sort.Strings(names)
	ctx["kms:EncryptionContextKeys"] = names
}

// sfnRequestQualifier reads the version or alias an AWS Step Functions request
// names. A state machine ARN carries it as a final segment after the machine's
// name — `…:stateMachine:name:2` for a version, `…:stateMachine:name:live` for
// an alias — and an unqualified ARN carries none.
func sfnRequestQualifier(r *http.Request, body []byte) string {
	for _, member := range []string{"stateMachineArn", "stateMachineAliasArn", "resourceArn"} {
		arn := iamRequestParameter(r, body, member)
		if arn == "" {
			continue
		}
		machine, found := strings.CutPrefix(arn, "arn:aws:states:")
		if !found {
			continue
		}
		fields := strings.Split(machine, ":")
		// region, account, "stateMachine", name[, qualifier]
		if len(fields) >= 5 && fields[2] == "stateMachine" {
			return fields[4]
		}
	}
	return ""
}

// iamLambdaRequestFunctionARN is the function an AWS Lambda request is about,
// for the operations that carry one: the function URL routes name it in the
// path, an event-source mapping create names it in the body, and the reads and
// writes of an existing mapping name the mapping, whose record holds it.
func iamLambdaRequestFunctionARN(r *http.Request, body []byte) string {
	if name := sim.PathParam(r, "name"); name != "" {
		if strings.HasPrefix(name, "arn:") {
			return name
		}
		return lambdaArn(name)
	}
	if name := iamRequestParameter(r, body, "FunctionName"); name != "" {
		if strings.HasPrefix(name, "arn:") {
			return name
		}
		return lambdaArn(name)
	}
	if uuid := sim.PathParam(r, "uuid"); uuid != "" {
		if mapping, ok := lambdaESMs.Get(uuid); ok {
			return mapping.FunctionArn
		}
	}
	return ""
}
