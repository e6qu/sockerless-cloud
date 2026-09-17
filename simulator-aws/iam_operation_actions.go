package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// iamOperationTargets turns the operation a request makes into what AWS
// authorizes it as. An operation whose name is an IAM action is that action on
// resources; the rest are the action or actions the Service Reference maps
// them to, some resolved from what the request asks for.
func iamOperationTargets(r *http.Request, service, operation string, resources []string) []iamAuthorizationTarget {
	on := func(action string, arns ...string) []iamAuthorizationTarget {
		targets := make([]iamAuthorizationTarget, 0, len(arns))
		for _, arn := range arns {
			targets = append(targets, iamAuthorizationTarget{action: service + ":" + action, resource: arn})
		}
		return targets
	}
	switch service + ":" + operation {
	case "kms:ReEncrypt":
		source, destination := iamKMSReEncryptKeys(iamRequestBody(r))
		return []iamAuthorizationTarget{
			{action: "kms:ReEncryptFrom", resource: iamKMSKeyARNOrAny(source)},
			{action: "kms:ReEncryptTo", resource: iamKMSKeyARNOrAny(destination)},
		}
	case "dynamodb:TransactWriteItems":
		return iamDynamoDBTransactWriteTargets(r)
	case "dynamodb:ExecuteStatement", "dynamodb:BatchExecuteStatement", "dynamodb:ExecuteTransaction":
		return iamDynamoDBPartiQLTargets(r)
	case "budgets:CreateBudget":
		targets := on("ModifyBudget", resources...)
		var request struct {
			ResourceTags []json.RawMessage `json:"ResourceTags"`
		}
		if json.Unmarshal(iamRequestBody(r), &request) == nil && len(request.ResourceTags) > 0 {
			targets = append(targets, on("TagResource", resources...)...)
		}
		return targets
	}
	if service == "s3" {
		return s3AuthorizationTargets(r, operation, resources)
	}
	if action, ok := iamOperationActions[service+":"+operation]; ok {
		return on(action, resources...)
	}
	return on(operation, resources...)
}

// iamDynamoDBTransactWriteTargets authorizes each item of a transaction as the
// write it makes, on its own table.
func iamDynamoDBTransactWriteTargets(r *http.Request) []iamAuthorizationTarget {
	var request struct {
		TransactItems []map[string]struct {
			TableName string `json:"TableName"`
		} `json:"TransactItems"`
	}
	if json.Unmarshal(iamRequestBody(r), &request) != nil {
		return nil
	}
	actions := map[string]string{"Put": "PutItem", "Update": "UpdateItem", "Delete": "DeleteItem", "ConditionCheck": "ConditionCheckItem"}
	var targets []iamAuthorizationTarget
	seen := map[iamAuthorizationTarget]bool{}
	for _, item := range request.TransactItems {
		for member, write := range item {
			action, ok := actions[member]
			if !ok || write.TableName == "" {
				continue
			}
			target := iamAuthorizationTarget{action: "dynamodb:" + action, resource: iamDynamoDBTableARN(r, write.TableName)}
			if !seen[target] {
				seen[target] = true
				targets = append(targets, target)
			}
		}
	}
	return targets
}

// iamDynamoDBPartiQLTargets authorizes each PartiQL statement as the read or
// write it is, on the table (or index) it names. A statement that does not
// parse is refused by the handler, so it authorizes nothing.
func iamDynamoDBPartiQLTargets(r *http.Request) []iamAuthorizationTarget {
	type statement struct {
		Statement  string           `json:"Statement"`
		Parameters []map[string]any `json:"Parameters"`
	}
	var request struct {
		statement
		Statements         []statement `json:"Statements"`
		TransactStatements []statement `json:"TransactStatements"`
	}
	if json.Unmarshal(iamRequestBody(r), &request) != nil {
		return nil
	}
	all := append([]statement{request.statement}, request.Statements...)
	all = append(all, request.TransactStatements...)
	kinds := map[partiQLKind]string{pqlSelect: "PartiQLSelect", pqlInsert: "PartiQLInsert", pqlUpdate: "PartiQLUpdate", pqlDelete: "PartiQLDelete"}
	var targets []iamAuthorizationTarget
	seen := map[iamAuthorizationTarget]bool{}
	for _, s := range all {
		if s.Statement == "" {
			continue
		}
		parsed, err := parsePartiQL(s.Statement, s.Parameters)
		if err != nil || parsed.Table == "" {
			continue
		}
		resource := iamDynamoDBTableARN(r, parsed.Table)
		if parsed.Kind == pqlSelect && parsed.Index != "" {
			resource += "/index/" + parsed.Index
		}
		target := iamAuthorizationTarget{action: "dynamodb:" + kinds[parsed.Kind], resource: resource}
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

func iamDynamoDBTableARN(r *http.Request, table string) string {
	region := iamRequestedRegion(r)
	if region == "" {
		region = awsRegion()
	}
	return "arn:aws:dynamodb:" + region + ":" + awsAccountID() + ":table/" + table
}

// s3AuthorizationTargets is what Amazon S3 authorizes an API operation as. A
// copy reads its source and writes its destination; a write that sets an ACL,
// tags, a legal hold or a retention also needs the action for that; a batch
// delete is a delete of each key it names.
func s3AuthorizationTargets(r *http.Request, operation string, resources []string) []iamAuthorizationTarget {
	on := func(action string, arns ...string) []iamAuthorizationTarget {
		targets := make([]iamAuthorizationTarget, 0, len(arns))
		for _, arn := range arns {
			targets = append(targets, iamAuthorizationTarget{action: "s3:" + action, resource: arn})
		}
		return targets
	}
	switch operation {
	case "PutObject", "CreateMultipartUpload":
		return append(on("PutObject", resources...), s3WriteSettingTargets(r, resources)...)
	case "CopyObject", "UploadPartCopy":
		targets := on("PutObject", resources...)
		if source, version, ok := s3CopySourceARN(r); ok {
			read := "GetObject"
			if version != "" {
				read = "GetObjectVersion"
			}
			targets = append(targets, on(read, source)...)
		}
		if operation == "CopyObject" {
			targets = append(targets, s3WriteSettingTargets(r, resources)...)
		}
		return targets
	case "HeadObject":
		return on("GetObject", resources...)
	case "ListObjects", "ListObjectsV2":
		return on("ListBucket", resources...)
	case "DeleteObjects":
		return s3DeleteObjectsTargets(r)
	}
	if action, ok := iamOperationActions["s3:"+operation]; ok {
		return on(action, resources...)
	}
	return on(operation, resources...)
}

// s3WriteSettingTargets are the further actions a write needs for the
// settings its headers carry.
func s3WriteSettingTargets(r *http.Request, resources []string) []iamAuthorizationTarget {
	var actions []string
	h := r.Header
	if h.Get("x-amz-acl") != "" || h.Get("x-amz-grant-full-control") != "" || h.Get("x-amz-grant-read") != "" ||
		h.Get("x-amz-grant-read-acp") != "" || h.Get("x-amz-grant-write-acp") != "" {
		actions = append(actions, "PutObjectAcl")
	}
	if h.Get("x-amz-tagging") != "" {
		actions = append(actions, "PutObjectTagging")
	}
	if h.Get("x-amz-object-lock-legal-hold") != "" {
		actions = append(actions, "PutObjectLegalHold")
	}
	if h.Get("x-amz-object-lock-mode") != "" || h.Get("x-amz-object-lock-retain-until-date") != "" {
		actions = append(actions, "PutObjectRetention")
	}
	var targets []iamAuthorizationTarget
	for _, action := range actions {
		for _, arn := range resources {
			targets = append(targets, iamAuthorizationTarget{action: "s3:" + action, resource: arn})
		}
	}
	return targets
}

// s3CopySourceARN reads the object a copy reads from its x-amz-copy-source
// header: "bucket/key", optionally with a versionId query.
func s3CopySourceARN(r *http.Request) (arn, version string, ok bool) {
	source := strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/")
	if source == "" {
		return "", "", false
	}
	path, query, _ := strings.Cut(source, "?")
	if unescaped, err := url.PathUnescape(path); err == nil {
		path = unescaped
	}
	if values, err := url.ParseQuery(query); err == nil {
		version = values.Get("versionId")
	}
	if !strings.Contains(path, "/") {
		return "", "", false
	}
	return "arn:aws:s3:::" + path, version, true
}

// s3DeleteObjectsTargets reads a DeleteObjects body and authorizes a delete of
// each key, the body left in place for the handler.
func s3DeleteObjectsTargets(r *http.Request) []iamAuthorizationTarget {
	bucket := ""
	if arn := s3RequestResourceARN(r); strings.HasPrefix(arn, "arn:aws:s3:::") {
		bucket = strings.TrimPrefix(arn, "arn:aws:s3:::")
	}
	if r.Body == nil || bucket == "" {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var request struct {
		Objects []struct {
			Key       string `xml:"Key"`
			VersionID string `xml:"VersionId"`
		} `xml:"Object"`
	}
	if xml.Unmarshal(body, &request) != nil {
		return nil
	}
	var targets []iamAuthorizationTarget
	for _, object := range request.Objects {
		action := "s3:DeleteObject"
		if object.VersionID != "" {
			action = "s3:DeleteObjectVersion"
		}
		targets = append(targets, iamAuthorizationTarget{action: action, resource: "arn:aws:s3:::" + bucket + "/" + object.Key})
	}
	if strings.EqualFold(r.Header.Get("x-amz-bypass-governance-retention"), "true") {
		targets = append(targets, iamAuthorizationTarget{action: "s3:BypassGovernanceRetention", resource: "arn:aws:s3:::" + bucket})
	}
	return targets
}
