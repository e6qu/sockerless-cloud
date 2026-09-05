package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Per-service aws:ResourceTag/<k> resolution for the IAM enforcement gate.
//
// Real AWS exposes the tags of the resource a request targets as both
// aws:ResourceTag/<k> AND the service-prefixed <service>:ResourceTag/<k> (e.g.
// lambda:ResourceTag/<k>, sqs:ResourceTag/<k>), so a policy conditioned on a
// resource's tag matches only when the targeted resource carries it. EC2 and
// ECS (ecs:cluster) live in iam_condition_context.go; the resolvers here cover
// every other sim service that stores resource tags and has a request→resource
// mapping:
//
//   awsJson  : lambda (REST path), sqs, dynamodb, ecr, logs (CloudWatch Logs),
//              stepfunctions, kms, secretsmanager, kinesis, glue
//   awsQuery : sns, rds, elbv2, elasticache
//   REST     : batch (path arn), s3 (bucket/object tags from the URL path)
//
// Each resolver returns the targeted resource's tags as a flat map; the
// dispatcher then writes both condition-key forms. A resolver that can't find
// the target (no such resource / no identifying param) returns ok=false and the
// gate simply leaves those keys unset — exactly as real AWS does when the
// resource is absent.

// iamPopulateServiceResourceTags resolves the request's target resource tags
// for service prefixes other than ec2/ecs and writes aws:ResourceTag/<k> +
// <service>:ResourceTag/<k>. Returns true when it handled the service (so the
// caller knows resolution was attempted for this prefix).
func iamPopulateServiceResourceTags(r *http.Request, service string, ctx map[string][]string) bool {
	var (
		tags map[string]string
		ok   bool
	)
	switch service {
	case "lambda":
		tags, ok = iamLambdaResourceTags(r)
	case "sqs":
		tags, ok = iamSQSResourceTags(r)
	case "sns":
		tags, ok = iamSNSResourceTags(r)
	case "rds":
		tags, ok = iamRDSResourceTags(r)
	case "elasticloadbalancing":
		tags, ok = iamELBv2ResourceTags(r)
	case "elasticache":
		tags, ok = iamElastiCacheResourceTags(r)
	case "dynamodb":
		tags, ok = iamDynamoDBResourceTags(r)
	case "ecr":
		tags, ok = iamECRResourceTags(r)
	case "logs":
		tags, ok = iamLogsResourceTags(r)
	case "states":
		tags, ok = iamSFNResourceTags(r)
	case "kms":
		tags, ok = iamKMSResourceTags(r)
	case "secretsmanager":
		tags, ok = iamSecretsManagerResourceTags(r)
	case "kinesis":
		tags, ok = iamKinesisResourceTags(r)
	case "glue":
		tags, ok = iamGlueResourceTags(r)
	case "batch":
		tags, ok = iamBatchResourceTags(r)
	case "s3":
		tags, ok = iamS3ResourceTags(r)
	default:
		return false
	}
	if ok {
		for k, v := range tags {
			ctx["aws:ResourceTag/"+k] = []string{v}
			ctx[service+":ResourceTag/"+k] = []string{v}
		}
	}
	return true
}

// iamReadJSONBody reads and restores r.Body so the downstream handler still
// sees it, returning the bytes for an awsJson resolver to inspect.
func iamReadJSONBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return body
}

// ── awsJson services ──────────────────────────────────────────────────

// iamLambdaResourceTags resolves the function the Lambda REST request targets.
// Function-scoped routes carry the name in the {name} path value; the tag-on
// routes carry the function ARN in {arn}.
func iamLambdaResourceTags(r *http.Request) (map[string]string, bool) {
	name := sim.PathParam(r, "name")
	if name == "" {
		name = sim.PathParam(r, "arn")
	}
	if name == "" {
		return nil, false
	}
	if strings.Contains(name, ":function:") {
		name = strings.SplitN(name, ":function:", 2)[1]
	}
	// A function-name path value may itself be an ARN-less qualifier suffix.
	if i := strings.LastIndex(name, ":"); i >= 0 && !strings.HasPrefix(name, "arn:") {
		// Strip a trailing version/alias qualifier (name:qualifier).
		name = name[:i]
	}
	fn, ok := lambdaFunctions.Get(name)
	if !ok {
		return nil, false
	}
	return fn.Tags, true
}

// iamSQSResourceTags resolves the queue named by QueueUrl.
func iamSQSResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		QueueUrl string `json:"QueueUrl"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil || req.QueueUrl == "" {
		return nil, false
	}
	q, ok := sqsQueues.Get(queueNameFromURL(req.QueueUrl))
	if !ok {
		return nil, false
	}
	return q.Tags, true
}

// iamDynamoDBResourceTags resolves the table named by ResourceArn.
func iamDynamoDBResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
		TableName   string `json:"TableName"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	if req.ResourceArn != "" {
		if name, _, ok := ddbTableByArn(req.ResourceArn); ok {
			settings, _ := ddbTableSettings.Get(name)
			return smTagsToMap(settings.Tags), true
		}
	}
	if req.TableName != "" {
		if _, ok := ddbTables.Get(req.TableName); ok {
			settings, _ := ddbTableSettings.Get(req.TableName)
			return smTagsToMap(settings.Tags), true
		}
	}
	return nil, false
}

// iamECRResourceTags resolves the repository named by resourceArn.
func iamECRResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		ResourceArn    string `json:"resourceArn"`
		RepositoryName string `json:"repositoryName"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	name := req.RepositoryName
	if name == "" && req.ResourceArn != "" {
		if n, ok := ecrRepoByArn(req.ResourceArn); ok {
			name = n
		}
	}
	if name == "" {
		return nil, false
	}
	repo, ok := ecrRepositories.Get(name)
	if !ok {
		return nil, false
	}
	return smTagsToMap(repo.Tags), true
}

// iamLogsResourceTags resolves the CloudWatch Logs log group named by
// resourceArn (TagResource) or logGroupName (legacy tag ops).
func iamLogsResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		ResourceArn  string `json:"resourceArn"`
		LogGroupName string `json:"logGroupName"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	name := req.LogGroupName
	if name == "" && req.ResourceArn != "" {
		if n, ok := cwLogGroupByArn(req.ResourceArn); ok {
			name = n
		}
	}
	if name == "" {
		return nil, false
	}
	lg, ok := cwLogGroups.Get(name)
	if !ok {
		return nil, false
	}
	return lg.Tags, true
}

// iamSFNResourceTags resolves the state machine named by resourceArn.
func iamSFNResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		ResourceArn     string `json:"resourceArn"`
		StateMachineArn string `json:"stateMachineArn"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	arn := req.ResourceArn
	if arn == "" {
		arn = req.StateMachineArn
	}
	if arn == "" {
		return nil, false
	}
	sm, ok := sfnStateMachines.Get(sfnNameFromARN(arn))
	if !ok {
		return nil, false
	}
	return sfnTagsToMap(sm.Tags), true
}

// iamKMSResourceTags resolves the key named by KeyId (id, ARN, or alias).
func iamKMSResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil || req.KeyId == "" {
		return nil, false
	}
	keyID, ok := resolveKMSKey(req.KeyId)
	if !ok {
		return nil, false
	}
	key, ok := kmsKeys.Get(keyID)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(key.Tags))
	for _, t := range key.Tags {
		out[t.TagKey] = t.TagValue
	}
	return out, true
}

// iamSecretsManagerResourceTags resolves the secret named by SecretId.
func iamSecretsManagerResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		SecretId string `json:"SecretId"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil || req.SecretId == "" {
		return nil, false
	}
	name, ok := resolveSecretKeyForRequest(r, req.SecretId)
	if !ok {
		return nil, false
	}
	s, ok := smSecrets.Get(name)
	if !ok {
		return nil, false
	}
	return smTagsToMap(s.Tags), true
}

// iamKinesisResourceTags resolves the stream named by StreamName/StreamARN.
func iamKinesisResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		StreamName string `json:"StreamName"`
		StreamARN  string `json:"StreamARN"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	s, ok := kinesisStreamByNameOrARN(req.StreamName, req.StreamARN)
	if !ok {
		return nil, false
	}
	return s.Tags, true
}

// iamGlueResourceTags resolves the database/job the request targets — the tag
// ops carry ResourceArn; the resource-scoped ops carry DatabaseName / JobName.
func iamGlueResourceTags(r *http.Request) (map[string]string, bool) {
	var req struct {
		ResourceArn  string `json:"ResourceArn"`
		DatabaseName string `json:"DatabaseName"`
		JobName      string `json:"JobName"`
	}
	if json.Unmarshal(iamReadJSONBody(r), &req) != nil {
		return nil, false
	}
	if req.ResourceArn != "" {
		resType, name := glueResourceFromARN(req.ResourceArn)
		switch resType {
		case "database":
			if db, ok := glueDatabases.Get(name); ok {
				return db.Tags, true
			}
		default:
			if job, ok := glueJobs.Get(name); ok {
				return job.Tags, true
			}
		}
		return nil, false
	}
	if req.DatabaseName != "" {
		if db, ok := glueDatabases.Get(req.DatabaseName); ok {
			return db.Tags, true
		}
	}
	if req.JobName != "" {
		if job, ok := glueJobs.Get(req.JobName); ok {
			return job.Tags, true
		}
	}
	return nil, false
}

// ── awsQuery services ─────────────────────────────────────────────────

// iamSNSResourceTags resolves the topic the request targets — the tag ops carry
// ResourceArn, the topic-scoped ops (DeleteTopic, Publish, …) carry TopicArn.
func iamSNSResourceTags(r *http.Request) (map[string]string, bool) {
	arn := r.FormValue("ResourceArn")
	if arn == "" {
		arn = r.FormValue("TopicArn")
	}
	if arn == "" {
		return nil, false
	}
	t, ok := snsTopics.Get(snsTopicNameFromARN(arn))
	if !ok {
		return nil, false
	}
	return t.Tags, true
}

// iamRDSResourceTags resolves the DB instance/snapshot the request targets — the
// tag ops carry ResourceName (an ARN); the instance-scoped ops carry
// DBInstanceIdentifier.
func iamRDSResourceTags(r *http.Request) (map[string]string, bool) {
	if arn := r.FormValue("ResourceName"); arn != "" {
		if i, ok := findRDSByARN(arn); ok {
			return i.Tags, true
		}
		if s, ok := findRDSSnapshotByARN(arn); ok {
			return s.Tags, true
		}
		return nil, false
	}
	if id := r.FormValue("DBInstanceIdentifier"); id != "" {
		if i, ok := rdsInstances.Get(id); ok {
			return i.Tags, true
		}
	}
	return nil, false
}

// iamELBv2ResourceTags resolves the load balancer / target group the request
// targets — the tag ops carry the ResourceArns.N list; resource-scoped ops carry
// the single LoadBalancerArn / TargetGroupArn (ARN-keyed stores).
func iamELBv2ResourceTags(r *http.Request) (map[string]string, bool) {
	arns := queryList(r, "ResourceArns")
	if arn := r.FormValue("LoadBalancerArn"); arn != "" {
		arns = append(arns, arn)
	}
	if arn := r.FormValue("TargetGroupArn"); arn != "" {
		arns = append(arns, arn)
	}
	for _, arn := range arns {
		if arn == "" {
			continue
		}
		if lb, ok := elbv2LoadBalancers.Get(arn); ok {
			return lb.Tags, true
		}
		if tg, ok := elbv2TargetGroups.Get(arn); ok {
			return tg.Tags, true
		}
	}
	return nil, false
}

// iamElastiCacheResourceTags resolves the cluster the request targets — the tag
// ops carry ResourceName (an ARN); the cluster-scoped ops carry CacheClusterId.
func iamElastiCacheResourceTags(r *http.Request) (map[string]string, bool) {
	if arn := r.FormValue("ResourceName"); arn != "" {
		if tags, ok := ecLookupTags(arn); ok {
			return tags, true
		}
		return nil, false
	}
	if id := r.FormValue("CacheClusterId"); id != "" {
		if c, ok := ecClusters.Get(id); ok {
			return c.Tags, true
		}
	}
	return nil, false
}

// ── REST services ─────────────────────────────────────────────────────

// iamBatchResourceTags resolves the compute-environment/job-queue named by the
// {resourceArn} path value (Batch's tag ops are REST: /v1/tags/{resourceArn}).
func iamBatchResourceTags(r *http.Request) (map[string]string, bool) {
	arn := sim.PathParam(r, "resourceArn")
	if arn == "" {
		return nil, false
	}
	tags := batchTagsForARN(arn)
	if len(tags) == 0 {
		return nil, false
	}
	return tags, true
}

// iamS3ResourceTags resolves the tags of the S3 bucket (or object) the request
// targets — object tags from s3ObjectTags, bucket tags from the stored
// `?tagging` subresource XML.
func iamS3ResourceTags(r *http.Request) (map[string]string, bool) {
	bucket := sim.PathParam(r, "bucket")
	if bucket == "" {
		return nil, false
	}
	if key := sim.PathParam(r, "key"); key != "" {
		tags, ok := s3ObjectTags.Get(bucket + "/" + key)
		if ok && len(tags) > 0 {
			return tags, true
		}
		return nil, false
	}
	body, _, _, ok := getStoredBucketSubresource(bucket, "tagging")
	if !ok {
		return nil, false
	}
	var doc struct {
		XMLName xml.Name `xml:"Tagging"`
		TagSet  struct {
			Tags []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Tag"`
		} `xml:"TagSet"`
	}
	if xml.Unmarshal(body, &doc) != nil {
		return nil, false
	}
	tags := make(map[string]string, len(doc.TagSet.Tags))
	for _, t := range doc.TagSet.Tags {
		tags[t.Key] = t.Value
	}
	if len(tags) == 0 {
		return nil, false
	}
	return tags, true
}

// smTagsToMap flattens the []SMTag (Key/Value) shape shared by DynamoDB, ECR,
// and Secrets Manager into a map.
func smTagsToMap(tags []SMTag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[t.Key] = t.Value
	}
	return out
}
