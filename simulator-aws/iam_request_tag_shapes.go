package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Where each service carries the tags a request supplies — the fact behind
// aws:RequestTag/${TagKey} and aws:TagKeys.
//
// Services do not agree on the wire shape, and the gate used to read exactly
// one of them: the awsQuery member prefix `Tag` (`Tag.1.Key` / `Tag.1.Value`),
// which is Amazon EC2's CreateTags spelling and nobody else's. Everywhere else
// both keys were silently left unset, so the tag-on-create restriction AWS
// documents could not be expressed at all: a
//
//	"Null": {"aws:RequestTag/owner": "false"}
//
// on rds:CreateDBInstance, or a ForAllValues:StringEquals on aws:TagKeys for
// elasticache:CreateCacheCluster or ecs:CreateCluster, tested a key that was
// never in the context, and a condition on an absent key does not match — so
// the statement meant to force a tag onto every created resource either denied
// every create or allowed every untagged one, depending on which way it was
// written.
//
// The table below is keyed by IAM service prefix and each row cites the
// vendored Smithy model it was read from (specs/cloud-api/aws/*.smithy.json.gz).
// For an awsQuery service the member path follows from two model facts: the
// input member's name (or its `smithy.api#xmlName`), and the list member's
// name — a list whose `member` carries `"smithy.api#xmlName": "Tag"` serializes
// as `Tags.Tag.N`, and a list whose member carries no xmlName takes the
// protocol default `member` and serializes as `Tags.member.N`. Amazon EC2 uses
// ec2Query, which flattens every list and prefers `aws.protocols#ec2QueryName`.
//
// A request that removes tags names only their keys, and AWS reports those
// keys as aws:TagKeys: that is how a policy stops a caller removing one tag
// while letting it remove the rest. Each row also records where its service's
// untagging operation carries that key list.
//
// AWS Lambda (restJson1) and the Amazon S3 control plane (restXml) reach the
// gate through iamEnforceREST, so their rows read the REST body and the
// tagKeys query parameter their models bind.

// iamJSONTagList is a JSON body member holding a list of tag structures,
// together with the key and value member names that service's tag structure
// declares — they are not the same everywhere: Amazon ECS spells them
// `key`/`value`, AWS KMS spells them `TagKey`/`TagValue`.
type iamJSONTagList struct{ member, key, value string }

// iamRequestTagShape is one service's wire shape for the tags a request
// carries. A service uses the forms its own model declares and no others.
type iamRequestTagShape struct {
	// query are awsQuery member paths, each read as <path>.N.Key /
	// <path>.N.Value.
	query []string
	// queryTags reads a query-protocol shape one of the service's own handlers
	// already parses, so there is one parser for it rather than two.
	queryTags func(*http.Request) []EC2Tag
	// jsonList are JSON body members holding a list of tag structures.
	jsonList []iamJSONTagList
	// jsonMap are JSON body members holding a map of tag key to tag value.
	jsonMap []string
	// keyQuery are awsQuery list paths holding tag keys, read as <path>.N.
	keyQuery []string
	// keyJSON are JSON body members holding a list of tag keys.
	keyJSON []string
	// keyURL is the URL query parameter a REST untagging operation repeats
	// once per key.
	keyURL string
	// cborList and keyCBOR are jsonList and keyJSON for a request the service
	// receives over Smithy RPC v2 CBOR.
	cborList []iamJSONTagList
	keyCBOR  []string
}

// iamKeyValueTagList is the tag structure spelling most JSON services declare.
func iamKeyValueTagList(member string) iamJSONTagList {
	return iamJSONTagList{member: member, key: "Key", value: "Value"}
}

var iamRequestTagShapes = map[string]iamRequestTagShape{
	// Amazon EC2 (ec2Query). CreateTagsRequest$Tags carries
	// `"smithy.api#xmlName": "Tag"` and ec2Query flattens lists, so CreateTags
	// and DeleteTags send `Tag.N.Key` — the one shape the gate already read.
	// Tag-on-create instead sends TagSpecifications, whose xmlName is
	// `TagSpecification`, holding a TagList whose xmlName is `Tag`:
	// `TagSpecification.N.Tag.M.Key`. ec2ParseTagSpecs is the simulator's own
	// reader for both serializations of that member.
	"ec2": {query: []string{"Tag"}, queryTags: ec2ParseTagSpecs},

	// Amazon RDS (awsQuery). TagList's member carries
	// `"smithy.api#xmlName": "Tag"` (rds.smithy.json.gz), so every one of the
	// 36 inputs with a Tags member — CreateDBInstanceMessage,
	// AddTagsToResourceMessage, the copies and the restores — sends
	// `Tags.Tag.N.Key`. RDS also declares a newer TagSpecifications member
	// (`TagSpecifications.item.N`), which this simulator's RDS handlers do not
	// read either; it is not listed because nothing here accepts it.
	"rds": {query: []string{"Tags.Tag"}, keyQuery: []string{"TagKeys.member"}},

	// Amazon ElastiCache (awsQuery). Identical to RDS: TagList's member carries
	// `"smithy.api#xmlName": "Tag"` (elasticache.smithy.json.gz,
	// CreateCacheClusterMessage$Tags / AddTagsToResourceMessage$Tags), so the
	// wire form is `Tags.Tag.N.Key` — which elasticache.go already parses that
	// way when it stores them.
	"elasticache": {query: []string{"Tags.Tag"}, keyQuery: []string{"TagKeys.member"}},

	// Elastic Load Balancing v2 (awsQuery). TagList's member carries no
	// xmlName (elastic-load-balancing-v2.smithy.json.gz,
	// CreateLoadBalancerInput$Tags / AddTagsInput$Tags), so the awsQuery
	// default list member name applies: `Tags.member.N.Key`, the same path
	// parseELBv2Tags reads.
	"elasticloadbalancing": {query: []string{"Tags.member"}, keyQuery: []string{"TagKeys.member"}},

	// AWS Identity and Access Management (awsQuery). tagListType's member
	// carries no xmlName (iam.smithy.json.gz, CreateRoleRequest$Tags,
	// TagRoleRequest$Tags and 35 more), so `Tags.member.N.Key` — the path
	// iam_lists.go parses.
	"iam": {query: []string{"Tags.member"}, keyQuery: []string{"TagKeys.member"}},

	// AWS Security Token Service (awsQuery). AssumeRoleRequest$Tags is the
	// session tags a role assumption carries, and tagListType's member carries
	// no xmlName (sts.smithy.json.gz): `Tags.member.N.Key`.
	"sts": {query: []string{"Tags.member"}},

	// Amazon SNS (awsQuery). TagList's member carries no xmlName
	// (sns.smithy.json.gz, CreateTopicInput$Tags / TagResourceRequest$Tags):
	// `Tags.member.N.Key`, the path sns.go parses.
	"sns": {query: []string{"Tags.member"}, keyQuery: []string{"TagKeys.member"}},

	// Amazon EC2 Auto Scaling (awsQuery). The Tags list's member carries no
	// xmlName (auto-scaling.smithy.json.gz, CreateAutoScalingGroupType$Tags /
	// CreateOrUpdateTagsType$Tags): `Tags.member.N.Key`. Its tag structure also
	// carries ResourceId/ResourceType/PropagateAtLaunch, which are not part of
	// the condition key.
	"autoscaling": {query: []string{"Tags.member"}},

	// Amazon CloudWatch. The alarm and rule operations this simulator serves
	// over the query protocol send `Tags.member.N.Key` — cloudwatch_alarms.go
	// and cloudwatch_misc_ops.go parse exactly that — and the TagList member in
	// cloudwatch.smithy.json.gz (PutMetricAlarmInput$Tags, TagResourceInput$Tags)
	// carries no xmlName, so the default `member` is right.
	// The Go SDK speaks Smithy RPC v2 CBOR to Amazon CloudWatch, carrying the
	// same Tags list of `Key`/`Value` and UntagResource's TagKeys.
	"cloudwatch": {
		query: []string{"Tags.member"}, keyQuery: []string{"TagKeys.member"},
		cborList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyCBOR: []string{"TagKeys"},
	},

	// Amazon ECS (awsJson1_1). ecs.smithy.json.gz declares `tags` on
	// CreateCluster, CreateService, RunTask, RegisterTaskDefinition and 8 more,
	// as a list whose Tag structure spells its members `key` and `value` in
	// lower case — the spelling ECSTag mirrors.
	"ecs": {jsonList: []iamJSONTagList{{member: "tags", key: "key", value: "value"}}, keyJSON: []string{"tagKeys"}},

	// Amazon ECR (awsJson1_1). ecr.smithy.json.gz: CreateRepositoryRequest$tags
	// and TagResourceRequest$tags, a list of `Key`/`Value` structures — the
	// member name is lower case, the structure's members are not.
	"ecr": {jsonList: []iamJSONTagList{{member: "tags", key: "Key", value: "Value"}}, keyJSON: []string{"tagKeys"}},

	// Amazon DynamoDB (awsJson1_0). dynamodb.smithy.json.gz:
	// CreateTableInput$Tags and TagResourceInput$Tags, a list of `Key`/`Value`.
	"dynamodb": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS KMS (awsJson1_1). kms.smithy.json.gz: CreateKeyRequest$Tags,
	// ReplicateKeyRequest$Tags and TagResourceRequest$Tags, a list whose Tag
	// structure spells its members `TagKey` and `TagValue` — KMS is the one
	// service here that does not use Key/Value, and KMSTag records that.
	"kms": {jsonList: []iamJSONTagList{{member: "Tags", key: "TagKey", value: "TagValue"}}, keyJSON: []string{"TagKeys"}},

	// Amazon SQS (awsJson1_0). sqs.smithy.json.gz carries tags as a map, not a
	// list, and names the member differently per operation:
	// CreateQueueRequest$tags and TagQueueRequest$Tags.
	"sqs": {jsonMap: []string{"tags", "Tags"}, keyJSON: []string{"TagKeys"}},

	// AWS Step Functions (awsJson1_0). sfn.smithy.json.gz:
	// CreateStateMachineInput$tags, CreateActivityInput$tags,
	// TagResourceInput$tags — a list of `key`/`value`.
	"states": {jsonList: []iamJSONTagList{{member: "tags", key: "key", value: "value"}}, keyJSON: []string{"tagKeys"}},

	// AWS CodeBuild (awsJson1_1). codebuild.smithy.json.gz:
	// CreateProjectInput$tags, CreateFleetInput$tags, CreateReportGroupInput$tags
	// — a list of `key`/`value`.
	"codebuild": {jsonList: []iamJSONTagList{{member: "tags", key: "key", value: "value"}}},

	// Amazon CloudWatch Logs (awsJson1_1). cloudwatch-logs.smithy.json.gz
	// carries tags as a map: CreateLogGroupRequest$tags,
	// TagResourceRequest$tags and 8 more.
	"logs": {jsonMap: []string{"tags"}, keyJSON: []string{"tagKeys", "tags"}},

	// AWS Glue (awsJson1_1). glue.smithy.json.gz carries `Tags` as a map on 28
	// inputs (CreateDatabaseRequest, CreateJobRequest, CreateCrawlerRequest …)
	// and TagResourceRequest spells it `TagsToAdd`. The newer integration
	// operations (CreateIntegrationRequest$Tags) declare the same `Tags` member
	// as a list of `key`/`value` instead, so both of Glue's own declared
	// shapes for that member are read.
	"glue": {
		jsonMap:  []string{"Tags", "TagsToAdd"},
		jsonList: []iamJSONTagList{{member: "Tags", key: "key", value: "value"}},
		keyJSON:  []string{"TagsToRemove"},
	},

	// Amazon EventBridge (awsJson1_1). eventbridge.smithy.json.gz:
	// CreateEventBusRequest$Tags, PutRuleRequest$Tags, TagResourceRequest$Tags
	// — a list of `Key`/`Value`.
	"events": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Systems Manager (awsJson1_1). ssm.smithy.json.gz:
	// CreateActivationRequest$Tags, AddTagsToResourceRequest$Tags and 11 more
	// — a list of `Key`/`Value`.
	"ssm": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Secrets Manager (awsJson1_1). secrets-manager.smithy.json.gz:
	// CreateSecretRequest$Tags, TagResourceRequest$Tags — a list of
	// `Key`/`Value`, the shape SMTag mirrors.
	"secretsmanager": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Organizations (awsJson1_1). organizations.smithy.json.gz:
	// CreateAccountRequest$Tags, CreateOrganizationalUnitRequest$Tags,
	// CreatePolicyRequest$Tags and 5 more — a list of `Key`/`Value`.
	"organizations": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Cloud Map (awsJson1_1). servicediscovery.smithy.json.gz:
	// CreateServiceRequest$Tags, the three CreateNamespace inputs and
	// TagResourceRequest$Tags — a list of `Key`/`Value`.
	"servicediscovery": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS WAFv2 (awsJson1_1). wafv2.smithy.json.gz: CreateWebACLRequest$Tags,
	// CreateIPSetRequest$Tags and 3 more — a list of `Key`/`Value`.
	"wafv2": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Certificate Manager (awsJson1_1). acm.smithy.json.gz:
	// RequestCertificateRequest$Tags, AddTagsToCertificateRequest$Tags — a list
	// of `Key`/`Value`.
	"acm": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// AWS Private CA (awsJson1_1). acm-pca.smithy.json.gz:
	// CreateCertificateAuthorityRequest$Tags, TagCertificateAuthorityRequest$Tags
	// — a list of `Key`/`Value`.
	"acm-pca": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}},

	// Amazon Data Firehose (awsJson1_1). firehose.smithy.json.gz:
	// CreateDeliveryStreamInput$Tags, TagDeliveryStreamInput$Tags — a list of
	// `Key`/`Value`.
	"firehose": {jsonList: []iamJSONTagList{iamKeyValueTagList("Tags")}, keyJSON: []string{"TagKeys"}},

	// Amazon Kinesis (awsJson1_1). kinesis.smithy.json.gz carries tags as a
	// map: CreateStreamInput$Tags, AddTagsToStreamInput$Tags and 3 more.
	"kinesis": {jsonMap: []string{"Tags"}, keyJSON: []string{"TagKeys"}},

	// AWS CloudTrail (awsJson1_1). cloudtrail.smithy.json.gz spells the member
	// `TagsList` on AddTagsRequest, CreateTrailRequest,
	// CreateEventDataStoreRequest and CreateDashboardRequest, and `Tags` on
	// CreateChannelRequest — both lists of `Key`/`Value`.
	"cloudtrail": {jsonList: []iamJSONTagList{iamKeyValueTagList("TagsList"), iamKeyValueTagList("Tags")}},

	// AWS Budgets (awsJson1_1). budgets.smithy.json.gz spells the member
	// `ResourceTags` on CreateBudgetRequest, CreateBudgetActionRequest and
	// TagResourceRequest — a list of `Key`/`Value`.
	"budgets": {jsonList: []iamJSONTagList{iamKeyValueTagList("ResourceTags")}, keyJSON: []string{"ResourceTagKeys"}},

	// Application Auto Scaling (awsJson1_1). application-auto-scaling.smithy.json.gz
	// carries tags as a map: RegisterScalableTargetRequest$Tags,
	// TagResourceRequest$Tags.
	"application-autoscaling": {jsonMap: []string{"Tags"}, keyJSON: []string{"TagKeys"}},

	// AWS Lambda (restJson1). lambda.smithy.json.gz carries tags as a `Tags`
	// map in the body of CreateFunction, CreateEventSourceMapping,
	// CreateCodeSigningConfig, CreateCapacityProvider and TagResource, and
	// UntagResource binds its key list to the repeated `tagKeys` query
	// parameter.
	"lambda": {jsonMap: []string{"Tags"}, keyURL: "tagKeys"},

	// Amazon S3 (restXml). s3.smithy.json.gz carries CreateBucket's tags as
	// CreateBucketConfiguration$Tags, a list of `Tag`. s3-control.smithy.json.gz
	// carries a `Tags` list on CreateAccessPoint, CreateJob, the Access Grants
	// creates, CreateStorageLensGroup, PutJobTagging, the Storage Lens
	// configuration writes and TagResource; UntagResource binds its key list
	// to `tagKeys`.
	"s3": {queryTags: s3RequestTags, keyURL: "tagKeys"},
}

// iamRequestTags returns the tags a request carries, read in the shape the
// service it addresses actually sends. A service this simulator serves but
// whose requests carry no tag member, and a request that carries no tags,
// return none — the caller then leaves aws:RequestTag/<k> and aws:TagKeys
// unset, which is what AWS does with a condition key that does not apply.
func iamRequestTags(r *http.Request, service string) []EC2Tag {
	shape, ok := iamRequestTagShapes[service]
	if !ok {
		return nil
	}
	var tags []EC2Tag
	for _, path := range shape.query {
		tags = append(tags, parseIndexedTags(r, path)...)
	}
	if shape.queryTags != nil {
		tags = append(tags, shape.queryTags(r)...)
	}
	if len(shape.jsonList) > 0 || len(shape.jsonMap) > 0 {
		tags = append(tags, iamJSONRequestTags(iamRequestBody(r), shape)...)
	}
	if len(shape.cborList) > 0 {
		if document := iamCBORRequestDocument(r); document != nil {
			for _, list := range shape.cborList {
				entries, _ := document[list.member].([]any)
				for _, entry := range entries {
					fields, _ := entry.(map[string]any)
					key, _ := fields[list.key].(string)
					if key == "" {
						continue
					}
					value, _ := fields[list.value].(string)
					tags = append(tags, EC2Tag{Key: key, Value: value})
				}
			}
		}
	}
	return tags
}

// iamCBORRequestDocument decodes the body of a Smithy RPC v2 CBOR request, which
// names its protocol in the Smithy-Protocol header, or is nil for any other.
func iamCBORRequestDocument(r *http.Request) map[string]any {
	if r.Header.Get("Smithy-Protocol") != "rpc-v2-cbor" {
		return nil
	}
	body := iamRequestBody(r)
	if len(body) == 0 {
		return nil
	}
	var document map[string]any
	if cwCBORDecMode.Unmarshal(body, &document) != nil {
		return nil
	}
	return document
}

// iamRequestTagKeys returns the tag keys an untagging request names, in the
// list its service's model declares for them.
func iamRequestTagKeys(r *http.Request, service string) []string {
	shape, ok := iamRequestTagShapes[service]
	if !ok {
		return nil
	}
	var keys []string
	for _, path := range shape.keyQuery {
		for i := 1; ; i++ {
			key := r.FormValue(path + "." + strconv.Itoa(i))
			if key == "" {
				break
			}
			keys = append(keys, key)
		}
	}
	if shape.keyURL != "" {
		keys = append(keys, r.URL.Query()[shape.keyURL]...)
	}
	if len(shape.keyCBOR) > 0 {
		if document := iamCBORRequestDocument(r); document != nil {
			for _, member := range shape.keyCBOR {
				listed, _ := document[member].([]any)
				for _, key := range listed {
					if key, ok := key.(string); ok && key != "" {
						keys = append(keys, key)
					}
				}
			}
		}
	}
	if len(shape.keyJSON) > 0 {
		var document map[string]json.RawMessage
		if body := iamRequestBody(r); len(body) > 0 && json.Unmarshal(body, &document) == nil {
			for _, member := range shape.keyJSON {
				var listed []string
				if raw, present := document[member]; present && json.Unmarshal(raw, &listed) == nil {
					keys = append(keys, listed...)
				}
			}
		}
	}
	return keys
}

// s3RequestTags reads the tags an Amazon S3 request carries: a CreateBucket's
// CreateBucketConfiguration, or a control-plane request's `Tags` list under
// its root element, whose entries the model names `Tag`, or the protocol
// default `member` where it names none (CreateJob, PutJobTagging).
func s3RequestTags(r *http.Request) []EC2Tag {
	controlPlane := strings.HasPrefix(r.URL.Path, "/v20180820/")
	createBucket := r.Method == http.MethodPut && r.URL.RawQuery == "" &&
		r.PathValue("bucket") != "" && r.PathValue("key") == ""
	if !controlPlane && !createBucket {
		return nil
	}
	list, ok := s3ControlGateBody(r).Child("Tags")
	if !ok {
		return nil
	}
	var tags []EC2Tag
	for _, child := range list.Children {
		if child.Name != "Tag" && child.Name != "member" {
			continue
		}
		if key := child.ChildText("Key"); key != "" {
			tags = append(tags, EC2Tag{Key: key, Value: child.ChildText("Value")})
		}
	}
	return tags
}

// iamJSONRequestTags reads the tags out of a JSON-protocol request body, in the
// members and structure spellings the service's model declares. Map-carried
// tags are returned in key order so the context a policy is evaluated against
// does not depend on Go's map iteration.
func iamJSONRequestTags(body []byte, shape iamRequestTagShape) []EC2Tag {
	if len(body) == 0 {
		return nil
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(body, &document) != nil {
		return nil
	}
	var tags []EC2Tag
	for _, list := range shape.jsonList {
		raw, present := document[list.member]
		if !present {
			continue
		}
		var entries []map[string]any
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		for _, entry := range entries {
			key, _ := entry[list.key].(string)
			if key == "" {
				continue
			}
			value, _ := entry[list.value].(string)
			tags = append(tags, EC2Tag{Key: key, Value: value})
		}
	}
	for _, member := range shape.jsonMap {
		raw, present := document[member]
		if !present {
			continue
		}
		var pairs map[string]string
		if json.Unmarshal(raw, &pairs) != nil {
			continue
		}
		keys := make([]string, 0, len(pairs))
		for key := range pairs {
			if key != "" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			tags = append(tags, EC2Tag{Key: key, Value: pairs[key]})
		}
	}
	return tags
}
