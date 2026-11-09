package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A data protection policy, account-level or a log group's own, audits and
// masks at ingestion: the data protection guide's "Understanding data
// protection policies", "Custom data identifiers" and "Audit findings
// reports" pages define the document, and the guide's log-delivery pages the
// findings destinations.

const (
	cwDataProtectionMaxChars    = 30720
	cwDataProtectionVersion     = "2021-06-01"
	cwCustomIdentifiersMax      = 10
	cwCustomIdentifierRegexMax  = 200
	cwLogDeliveryService        = "delivery.logs.amazonaws.com"
	cwLogDeliveryResourcePolicy = "AWSLogDeliveryWrite20150319"
)

// cwLogDeliveryPolicies finds the resource policies that name the log
// delivery service.
var cwLogDeliveryPolicies sim.GenerationIndex[CWResourcePolicy]

var (
	cwCustomIdentifierName  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	cwCustomIdentifierRegex = regexp.MustCompile(`^[A-Za-z0-9_#=@/;, '"^$?\[\]{}|\\*+.()-]*$`)
)

type cwNamedDetector struct {
	name     string
	detector cwDataDetector
}

type cwFindingsDestination struct {
	LogGroup       string
	DeliveryStream string
	Bucket         string
}

// cwCompiledProtection is a compiled data protection policy.
type cwCompiledProtection struct {
	name        string
	identifiers []cwNamedDetector
	findings    cwFindingsDestination
}

type cwDataProtectionStatement struct {
	Sid            string                     `json:"Sid"`
	DataIdentifier []string                   `json:"DataIdentifier"`
	Operation      map[string]json.RawMessage `json:"Operation"`
}

func cwCompileDataProtection(doc string) (*cwCompiledProtection, error) {
	if n := len([]rune(doc)); n > cwDataProtectionMaxChars {
		return nil, fmt.Errorf("the data protection policy document is %d characters; the limit is %d", n, cwDataProtectionMaxChars)
	}
	var parsed struct {
		Name          string `json:"Name"`
		Description   string `json:"Description"`
		Version       string `json:"Version"`
		Configuration *struct {
			CustomDataIdentifier []struct {
				Name  string `json:"Name"`
				Regex string `json:"Regex"`
			} `json:"CustomDataIdentifier"`
		} `json:"Configuration"`
		Statement []cwDataProtectionStatement `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
		return nil, fmt.Errorf("the data protection policy document is not valid JSON: %v", err)
	}
	if parsed.Name == "" {
		return nil, fmt.Errorf("the data protection policy document must include a Name")
	}
	if parsed.Version != cwDataProtectionVersion {
		return nil, fmt.Errorf("the data protection policy document's Version must be %s", cwDataProtectionVersion)
	}
	out := &cwCompiledProtection{name: parsed.Name}
	custom := map[string]cwDataDetector{}
	if parsed.Configuration != nil {
		ids := parsed.Configuration.CustomDataIdentifier
		if len(ids) > cwCustomIdentifiersMax {
			return nil, fmt.Errorf("a data protection policy can define at most %d custom data identifiers", cwCustomIdentifiersMax)
		}
		for _, id := range ids {
			if !cwCustomIdentifierName.MatchString(id.Name) {
				return nil, fmt.Errorf("invalid custom data identifier name %q: use 1 to 128 letters, digits, underscores and hyphens", id.Name)
			}
			if _, managed := cwManagedDataIdentifiers[id.Name]; managed {
				return nil, fmt.Errorf("custom data identifier %s has the name of a managed data identifier", id.Name)
			}
			if _, dup := custom[id.Name]; dup {
				return nil, fmt.Errorf("custom data identifier %s is defined more than once", id.Name)
			}
			if id.Regex == "" || len([]rune(id.Regex)) > cwCustomIdentifierRegexMax {
				return nil, fmt.Errorf("custom data identifier %s: Regex must be 1 to %d characters", id.Name, cwCustomIdentifierRegexMax)
			}
			if !cwCustomIdentifierRegex.MatchString(id.Regex) {
				return nil, fmt.Errorf("custom data identifier %s: Regex contains an unsupported character", id.Name)
			}
			re, err := regexp.Compile(id.Regex)
			if err != nil {
				return nil, fmt.Errorf("custom data identifier %s: invalid Regex: %v", id.Name, err)
			}
			custom[id.Name] = cwDataDetector{pattern: re}
		}
	}
	if len(parsed.Statement) != 2 {
		return nil, fmt.Errorf("a data protection policy must have exactly two statements: one with the Audit operation and one with the Deidentify operation")
	}
	var audit, deidentify *cwDataProtectionStatement
	for i := range parsed.Statement {
		st := &parsed.Statement[i]
		if len(st.Operation) != 1 {
			return nil, fmt.Errorf("each statement of a data protection policy must have exactly one operation")
		}
		switch {
		case st.Operation["Audit"] != nil && audit == nil:
			audit = st
		case st.Operation["Deidentify"] != nil && deidentify == nil:
			deidentify = st
		default:
			return nil, fmt.Errorf("a data protection policy must have one statement with the Audit operation and one with the Deidentify operation")
		}
	}
	if audit == nil || deidentify == nil {
		return nil, fmt.Errorf("a data protection policy must have one statement with the Audit operation and one with the Deidentify operation")
	}
	var auditOp struct {
		FindingsDestination *struct {
			CloudWatchLogs *struct {
				LogGroup string `json:"LogGroup"`
			} `json:"CloudWatchLogs"`
			Firehose *struct {
				DeliveryStream string `json:"DeliveryStream"`
			} `json:"Firehose"`
			S3 *struct {
				Bucket string `json:"Bucket"`
			} `json:"S3"`
		} `json:"FindingsDestination"`
	}
	if err := json.Unmarshal(audit.Operation["Audit"], &auditOp); err != nil || auditOp.FindingsDestination == nil {
		return nil, fmt.Errorf("the Audit operation must contain a FindingsDestination object")
	}
	if d := auditOp.FindingsDestination.CloudWatchLogs; d != nil {
		out.findings.LogGroup = d.LogGroup
	}
	if d := auditOp.FindingsDestination.Firehose; d != nil {
		out.findings.DeliveryStream = d.DeliveryStream
	}
	if d := auditOp.FindingsDestination.S3; d != nil {
		out.findings.Bucket = d.Bucket
	}
	var deidentifyOp struct {
		MaskConfig *map[string]json.RawMessage `json:"MaskConfig"`
	}
	if err := json.Unmarshal(deidentify.Operation["Deidentify"], &deidentifyOp); err != nil || deidentifyOp.MaskConfig == nil || len(*deidentifyOp.MaskConfig) != 0 {
		return nil, fmt.Errorf("the Deidentify operation must contain an empty MaskConfig object")
	}
	if len(audit.DataIdentifier) == 0 {
		return nil, fmt.Errorf("the Audit statement must list at least one DataIdentifier")
	}
	a, d := append([]string(nil), audit.DataIdentifier...), append([]string(nil), deidentify.DataIdentifier...)
	sort.Strings(a)
	sort.Strings(d)
	if strings.Join(a, "\x00") != strings.Join(d, "\x00") {
		return nil, fmt.Errorf("the DataIdentifier arrays of the Audit and Deidentify statements must match exactly")
	}
	seen := map[string]bool{}
	for _, id := range a {
		if seen[id] {
			continue
		}
		seen[id] = true
		if name, ok := strings.CutPrefix(id, cwDataIdentifierArnPrefix); ok {
			det, managed := cwManagedDataIdentifiers[name]
			if !managed {
				return nil, fmt.Errorf("unknown managed data identifier: %s", id)
			}
			out.identifiers = append(out.identifiers, cwNamedDetector{name: name, detector: det})
			continue
		}
		det, ok := custom[id]
		if !ok {
			return nil, fmt.Errorf("data identifier %s is neither a managed data identifier ARN nor a custom data identifier the policy defines", id)
		}
		out.identifiers = append(out.identifiers, cwNamedDetector{name: id, detector: det})
	}
	return out, nil
}

// cwPrepareFindingsDestinations checks that each destination exists and, as
// the log-delivery guide describes, grants delivery.logs.amazonaws.com what it
// needs where the destination does not already: a CloudWatch Logs resource
// policy, the bucket policy's AWSLogDeliveryAclCheck and AWSLogDeliveryWrite
// statements, the Firehose stream's LogDeliveryEnabled tag. Each change is
// made as the caller and needs the caller's permission for it.
func cwPrepareFindingsDestinations(w http.ResponseWriter, r *http.Request, p *cwCompiledProtection) bool {
	dest := p.findings
	refuse := func(format string, args ...any) bool {
		AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest, format, args...)
		return false
	}
	denied := func(action, resource string) bool {
		denial, allowed, registered := iamAuthorizeDenial(r, action, resource, nil)
		if registered && !allowed {
			iamWriteDeny(w, r, denial)
			return true
		}
		return false
	}
	if dest.LogGroup != "" {
		if _, ok := cwLogGroups.Get(dest.LogGroup); !ok {
			return refuse("The findings destination log group does not exist: %s", dest.LogGroup)
		}
		if !cwLogDeliveryAdmitted(dest.LogGroup) {
			for _, action := range []string{"logs:PutResourcePolicy", "logs:DescribeResourcePolicies", "logs:DescribeLogGroups"} {
				if denied(action, "*") {
					return false
				}
			}
			cwGrantLogDelivery(dest.LogGroup)
		}
	}
	if dest.DeliveryStream != "" {
		stream, ok := firehoseStreams.Get(dest.DeliveryStream)
		if !ok {
			return refuse("The findings destination Firehose stream does not exist: %s", dest.DeliveryStream)
		}
		if stream.Tags["LogDeliveryEnabled"] != "true" {
			if denied("firehose:TagDeliveryStream", firehoseARN(dest.DeliveryStream)) {
				return false
			}
			firehoseStreams.Update(dest.DeliveryStream, func(s *FirehoseDeliveryStream) {
				if s.Tags == nil {
					s.Tags = map[string]string{}
				}
				s.Tags["LogDeliveryEnabled"] = "true"
			})
		}
	}
	if dest.Bucket != "" {
		if _, ok := s3Buckets_.Get(dest.Bucket); !ok {
			return refuse("The findings destination bucket does not exist: %s", dest.Bucket)
		}
		if !cwS3LogDeliveryAdmitted(dest.Bucket) {
			bucketArn := s3BucketARN(dest.Bucket)
			if denied("s3:GetBucketPolicy", bucketArn) || denied("s3:PutBucketPolicy", bucketArn) {
				return false
			}
			if err := cwGrantS3LogDelivery(dest.Bucket); err != nil {
				return refuse("Cannot add the log delivery statements to the policy of bucket %s: %v", dest.Bucket, err)
			}
		}
	}
	return true
}

func cwLogDeliverySource() iamServiceSource {
	return iamServiceSource{Service: cwLogDeliveryService, SourceArn: "arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":*", SourceAccount: awsAccountID()}
}

func cwLogDeliveryStreamArn(group string) string { return cwLogGroupArn(group) + ":log-stream:*" }

// cwLogDeliveryAdmitted reports whether the account's CloudWatch Logs
// resource policies let delivery.logs.amazonaws.com create streams in group
// and put events in them.
func cwLogDeliveryAdmitted(group string) bool {
	var docs []iamPolicyDoc
	for _, p := range cwLogDeliveryPolicies.LookupAll(cwResourcePolicies, cwLogDeliveryService, func(p CWResourcePolicy) []string {
		if strings.Contains(p.PolicyDocument, cwLogDeliveryService) {
			return []string{cwLogDeliveryService}
		}
		return nil
	}) {
		if doc, err := parseIAMPolicy(p.PolicyDocument); err == nil {
			docs = append(docs, doc)
		}
	}
	if len(docs) == 0 {
		return false
	}
	src := cwLogDeliverySource()
	resource := cwLogGroupArn(group) + ":log-stream:" + cwFindingsStreamName(group)
	return iamEvalServiceInitiated(docs, "logs:CreateLogStream", resource, src) &&
		iamEvalServiceInitiated(docs, "logs:PutLogEvents", resource, src)
}

// cwGrantLogDelivery adds group to the AWSLogDeliveryWrite20150319 resource
// policy the log-delivery guide shows, creating the policy if need be.
func cwGrantLogDelivery(group string) {
	type statement struct {
		Sid       string                         `json:"Sid"`
		Effect    string                         `json:"Effect"`
		Principal map[string][]string            `json:"Principal"`
		Action    []string                       `json:"Action"`
		Resource  []string                       `json:"Resource"`
		Condition map[string]map[string][]string `json:"Condition"`
	}
	var doc struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}
	if existing, ok := cwResourcePolicies.Get(cwLogDeliveryResourcePolicy); ok {
		if err := json.Unmarshal([]byte(existing.PolicyDocument), &doc); err != nil {
			doc.Statement = nil
		}
	}
	doc.Version = "2012-10-17"
	resource := cwLogDeliveryStreamArn(group)
	granted := false
	for i, st := range doc.Statement {
		if st.Sid == cwLogDeliveryResourcePolicy {
			doc.Statement[i].Resource = append(st.Resource, resource)
			granted = true
		}
	}
	if !granted {
		doc.Statement = append(doc.Statement, statement{
			Sid:       cwLogDeliveryResourcePolicy,
			Effect:    "Allow",
			Principal: map[string][]string{"Service": {cwLogDeliveryService}},
			Action:    []string{"logs:CreateLogStream", "logs:PutLogEvents"},
			Resource:  []string{resource},
			Condition: map[string]map[string][]string{
				"StringEquals": {"aws:SourceAccount": {awsAccountID()}},
				"ArnLike":      {"aws:SourceArn": {"arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":*"}},
			},
		})
	}
	body, _ := json.Marshal(doc)
	cwResourcePolicies.Put(cwLogDeliveryResourcePolicy, CWResourcePolicy{
		PolicyName: cwLogDeliveryResourcePolicy, PolicyDocument: string(body), LastUpdatedTime: time.Now().UnixMilli(),
	})
}

func cwS3FindingsPrefix() string { return "AWSLogs/" + awsAccountID() + "/" }

func cwS3LogDeliveryContext() map[string][]string {
	ctx := iamServiceInitiatedConditionContext(cwLogDeliverySource())
	ctx["s3:x-amz-acl"] = []string{"bucket-owner-full-control"}
	return ctx
}

func cwS3LogDeliveryAdmitted(bucket string) bool {
	docs := iamResourcePolicyDocsForARN(s3BucketARN(bucket))
	if len(docs) == 0 {
		return false
	}
	principal := "service:" + cwLogDeliveryService
	ctx := cwS3LogDeliveryContext()
	acl, _ := iamEvalDecisionForPrincipal(docs, "s3:GetBucketAcl", s3BucketARN(bucket), principal, ctx)
	put, _ := iamEvalDecisionForPrincipal(docs, "s3:PutObject", s3BucketARN(bucket)+"/"+cwS3FindingsPrefix()+"x", principal, ctx)
	return acl == "allowed" && put == "allowed"
}

// cwGrantS3LogDelivery appends the log-delivery guide's two statements to the
// bucket's policy, or creates the policy with them.
func cwGrantS3LogDelivery(bucket string) error {
	policy := map[string]any{"Version": "2012-10-17", "Id": cwLogDeliveryResourcePolicy}
	if body, _, _, ok := getStoredBucketSubresource(bucket, "policy"); ok && len(body) > 0 {
		policy = map[string]any{}
		if err := json.Unmarshal(body, &policy); err != nil {
			return err
		}
	}
	var statements []any
	switch existing := policy["Statement"].(type) {
	case []any:
		statements = existing
	case map[string]any:
		statements = []any{existing}
	}
	condition := map[string]any{
		"StringEquals": map[string]any{"aws:SourceAccount": []string{awsAccountID()}},
		"ArnLike":      map[string]any{"aws:SourceArn": []string{"arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":*"}},
	}
	writeCondition := map[string]any{
		"StringEquals": map[string]any{"s3:x-amz-acl": "bucket-owner-full-control", "aws:SourceAccount": []string{awsAccountID()}},
		"ArnLike":      map[string]any{"aws:SourceArn": []string{"arn:aws:logs:" + awsRegion() + ":" + awsAccountID() + ":*"}},
	}
	statements = append(statements,
		map[string]any{"Sid": "AWSLogDeliveryAclCheck", "Effect": "Allow", "Principal": map[string]any{"Service": cwLogDeliveryService},
			"Action": "s3:GetBucketAcl", "Resource": s3BucketARN(bucket), "Condition": condition},
		map[string]any{"Sid": "AWSLogDeliveryWrite", "Effect": "Allow", "Principal": map[string]any{"Service": cwLogDeliveryService},
			"Action": "s3:PutObject", "Resource": s3BucketARN(bucket) + "/" + cwS3FindingsPrefix() + "*", "Condition": writeCondition},
	)
	policy["Statement"] = statements
	body, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	key := s3BucketConfigKeyID(bucket, "policy", "")
	s3BucketConfigs.Put(key, S3BucketConfig{Key: key, Bucket: bucket, Subresource: "policy", Body: body, ContentType: "application/json"})
	iamPutResourcePolicy(s3BucketARN(bucket), string(body))
	return nil
}

// cwDataProtectionsFor returns the policies that apply to group: the
// account-level one and the group's own, which the PutAccountPolicy API
// reference makes cumulative.
func cwDataProtectionsFor(group string) []*cwCompiledProtection {
	var docs []string
	for _, p := range cwAccountPoliciesOf(cwPolicyDataProtection) {
		docs = append(docs, p.PolicyDocument)
	}
	if p, ok := cwDataProtection.Get(group); ok {
		docs = append(docs, p.PolicyDocument)
	}
	var out []*cwCompiledProtection
	for _, doc := range docs {
		compiled, err := cwCompileDataProtection(doc)
		if err != nil {
			log.Printf("cloudwatch logs: stored data protection policy no longer compiles: %v", err)
			continue
		}
		out = append(out, compiled)
	}
	return out
}

type cwDetection struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type cwIdentifierFinding struct {
	Name       string        `json:"name"`
	Count      int           `json:"count"`
	Detections []cwDetection `json:"detections"`
}

type cwFindingsReport struct {
	AuditTimestamp  string                `json:"auditTimestamp"`
	ResourceArn     string                `json:"resourceArn"`
	DataIdentifiers []cwIdentifierFinding `json:"dataIdentifiers"`
}

// cwPendingFindings are one policy's reports for one ingested batch.
type cwPendingFindings struct {
	policy  *cwCompiledProtection
	reports []cwFindingsReport
}

// cwProtectIngested audits and masks events as group ingests them. A masked
// event keeps its original beside the masked text, which is what every read
// returns unless the reader asks to unmask; the transformed version is
// masked the same way.
func cwProtectIngested(group string, events []CWLogEvent) ([]cwPendingFindings, int) {
	policies := cwDataProtectionsFor(group)
	if len(policies) == 0 {
		return nil, 0
	}
	pending := make([]cwPendingFindings, len(policies))
	for i, p := range policies {
		pending[i].policy = p
	}
	withFindings := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range events {
		e := &events[i]
		var spans []cwDataSpan
		found := false
		for pi, p := range policies {
			var findings []cwIdentifierFinding
			for _, id := range p.identifiers {
				matches := id.detector.find(e.Message)
				if len(matches) == 0 {
					continue
				}
				f := cwIdentifierFinding{Name: id.name, Count: len(matches)}
				for _, m := range matches {
					start := cwRuneCount(e.Message[:m.start])
					f.Detections = append(f.Detections, cwDetection{Start: start, End: start + cwRuneCount(e.Message[m.start:m.end])})
				}
				findings = append(findings, f)
				spans = append(spans, matches...)
			}
			if len(findings) > 0 {
				found = true
				pending[pi].reports = append(pending[pi].reports, cwFindingsReport{
					AuditTimestamp: now, ResourceArn: cwLogGroupArn(group) + ":*", DataIdentifiers: findings,
				})
			}
		}
		if found {
			withFindings++
			e.Unmasked = e.Message
			e.Message = cwMask(e.Message, spans)
		}
		if e.Transformed != "" {
			var tspans []cwDataSpan
			for _, p := range policies {
				for _, id := range p.identifiers {
					tspans = append(tspans, id.detector.find(e.Transformed)...)
				}
			}
			if len(tspans) > 0 {
				e.UnmaskedTransformed = e.Transformed
				e.Transformed = cwMask(e.Transformed, tspans)
			}
		}
	}
	return pending, withFindings
}

// cwMask replaces each character of the spans with an asterisk, the way the
// guide says masked data reads.
func cwMask(s string, spans []cwDataSpan) string {
	masked := make([]bool, len(s))
	for _, sp := range spans {
		for i := sp.start; i < sp.end; i++ {
			masked[i] = true
		}
	}
	var b strings.Builder
	for i, r := range s {
		if masked[i] {
			b.WriteByte('*')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cwPublishFindings counts the events with findings in LogEventsWithFindings,
// which the metrics page publishes with no dimensions, and delivers each
// policy's reports to its findings destinations as delivery.logs.amazonaws.com.
func cwPublishFindings(group string, pending []cwPendingFindings, withFindings int) {
	if withFindings > 0 {
		cwStoreDatum(CWMetricDatum{Namespace: "AWS/Logs", MetricName: "LogEventsWithFindings", Value: float64(withFindings),
			Timestamp: float64(time.Now().Unix()), Unit: "None"})
	}
	for _, p := range pending {
		if len(p.reports) == 0 {
			continue
		}
		dest := p.policy.findings
		if dest.LogGroup != "" {
			cwDeliverFindingsToLogGroup(group, dest.LogGroup, p.reports)
		}
		if dest.DeliveryStream != "" {
			cwDeliverFindingsToFirehose(dest.DeliveryStream, p.reports)
		}
		if dest.Bucket != "" {
			cwDeliverFindingsToS3(dest.Bucket, p.reports)
		}
	}
}

// cwFindingsStreamName names the stream of a findings destination log group
// after the audited log group.
func cwFindingsStreamName(audited string) string { return audited }

func cwDeliverFindingsToLogGroup(audited, dest string, reports []cwFindingsReport) {
	if _, ok := cwLogGroups.Get(dest); !ok || !cwLogDeliveryAdmitted(dest) {
		log.Printf("cloudwatch logs: data protection findings of %s not delivered to log group %s", audited, dest)
		return
	}
	stream := cwFindingsStreamName(audited)
	cwEnsureLogStream(dest, stream)
	now := time.Now().UnixMilli()
	events := make([]CWLogEvent, 0, len(reports))
	for _, rep := range reports {
		body, _ := json.Marshal(rep)
		events = append(events, CWLogEvent{Timestamp: now, Message: string(body), IngestionTime: now})
	}
	cwAppendLogEvents(cwEventsKey(dest, stream), events, nil)
}

// The service-linked role AWSServiceRoleForLogDelivery writes to the Firehose
// streams tagged LogDeliveryEnabled=true, as the log-delivery guide describes.
func cwDeliverFindingsToFirehose(stream string, reports []cwFindingsReport) {
	s, ok := firehoseStreams.Get(stream)
	if !ok || s.Tags["LogDeliveryEnabled"] != "true" {
		log.Printf("cloudwatch logs: data protection findings not delivered to Firehose stream %s", stream)
		return
	}
	for _, rep := range reports {
		body, _ := json.Marshal(rep)
		if _, _, err := firehoseAddRecord(stream, append(body, '\n')); err != nil {
			log.Printf("cloudwatch logs: data protection findings not delivered to Firehose stream %s: %v", stream, err)
		}
	}
}

func cwDeliverFindingsToS3(bucket string, reports []cwFindingsReport) {
	if _, ok := s3Buckets_.Get(bucket); !ok || !cwS3LogDeliveryAdmitted(bucket) {
		log.Printf("cloudwatch logs: data protection findings not delivered to bucket %s", bucket)
		return
	}
	var body bytes.Buffer
	for _, rep := range reports {
		line, _ := json.Marshal(rep)
		body.Write(line)
		body.WriteByte('\n')
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("%sDataProtection/%s/%s/%s.json", cwS3FindingsPrefix(), awsRegion(), now.Format("2006/01/02/15"), sim.NewUUID())
	if _, err := s3PutServiceObject(bucket, key, body.Bytes(), "application/json", nil); err != nil {
		log.Printf("cloudwatch logs: data protection findings not delivered to bucket %s: %v", bucket, err)
	}
}

// cwRefuseUnmask answers a request to unmask with AccessDeniedException
// unless the caller holds logs:Unmask on every group it reads.
func cwRefuseUnmask(w http.ResponseWriter, r *http.Request, groups ...string) bool {
	for _, g := range groups {
		denial, allowed, registered := iamAuthorizeDenial(r, "logs:Unmask", cwLogGroupArn(g)+":*", nil)
		if registered && !allowed {
			iamWriteDeny(w, r, denial)
			return true
		}
	}
	return false
}
