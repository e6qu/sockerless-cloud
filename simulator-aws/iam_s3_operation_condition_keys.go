package main

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"time"
)

func init() {
	registerIAMRequestConditionPopulator("s3", iamPopulateS3OperationConditionKeys)
}

// s3VersionAnnotationActions are the annotation operations Amazon S3
// authorizes as their Version action when the request names a version.
var s3VersionAnnotationActions = map[string]string{
	"ListObjectAnnotations":  "ListObjectVersionAnnotations",
	"PutObjectAnnotation":    "PutObjectVersionAnnotation",
	"DeleteObjectAnnotation": "DeleteObjectVersionAnnotation",
}

// s3PutInventoryOperation is the name s3BucketOperationName gives a PUT of the
// inventory subresource, composed the same way.
var s3PutInventoryOperation = "PutBucket" + s3SubresourceOperationSuffix("inventory")

// s3ConditionAction is the IAM action Amazon S3 authorizes an API operation
// as, which is the name the service reference declares the keys against. A
// copy and every step of a multipart upload write an object, so they are
// PutObject.
func s3ConditionAction(r *http.Request, operation string) string {
	switch operation {
	case "CopyObject", "CreateMultipartUpload", "UploadPart", "UploadPartCopy", "CompleteMultipartUpload":
		return "PutObject"
	case "ListMultipartUploads":
		return "ListBucketMultipartUploads"
	}
	if operation == s3PutInventoryOperation {
		return "PutInventoryConfiguration"
	}
	if versioned, ok := s3VersionAnnotationActions[operation]; ok && r.URL.Query().Get("versionId") != "" {
		return versioned
	}
	return operation
}

// iamPopulateS3OperationConditionKeys adds the Amazon S3 keys for Object Lock,
// conditional writes, object annotations, bucket creation, inventory fields,
// and the tags of the access point a request came through.
func iamPopulateS3OperationConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch s3ConditionAction(r, operation) {
	case "PutObject":
		iamSetConditionValues(ctx, "s3:if-none-match", r.Header.Get("If-None-Match"))
		iamSetConditionValues(ctx, "s3:object-lock-mode", r.Header.Get("x-amz-object-lock-mode"))
		iamSetConditionValues(ctx, "s3:object-lock-retain-until-date", r.Header.Get("x-amz-object-lock-retain-until-date"))
		iamSetS3RemainingRetentionDays(ctx, r.Header.Get("x-amz-object-lock-retain-until-date"))
		iamSetConditionValues(ctx, "s3:object-lock-legal-hold", r.Header.Get("x-amz-object-lock-legal-hold"))
		iamSetConditionValues(ctx, "s3:object-lock-event-hold", r.Header.Get("x-amz-object-lock-event-hold"))
		iamSetConditionValues(ctx, "s3:object-lock-event-hold-duration-days",
			r.Header.Get("x-amz-object-lock-event-hold-duration-days"))
		iamSetConditionValues(ctx, "s3:x-amz-object-annotation-directive",
			r.Header.Get("x-amz-object-annotation-directive"))
	case "PutObjectRetention":
		var retention struct {
			Mode              string `xml:"Mode"`
			RetainUntilDate   string `xml:"RetainUntilDate"`
			EventHold         string `xml:"EventHold"`
			EventHoldDuration struct {
				Days *int64 `xml:"Days"`
			} `xml:"EventHoldDuration"`
		}
		if len(body) == 0 || xml.Unmarshal(body, &retention) != nil {
			return
		}
		iamSetConditionValues(ctx, "s3:object-lock-mode", retention.Mode)
		iamSetConditionValues(ctx, "s3:object-lock-retain-until-date", retention.RetainUntilDate)
		iamSetS3RemainingRetentionDays(ctx, retention.RetainUntilDate)
		iamSetConditionValues(ctx, "s3:object-lock-event-hold", retention.EventHold)
		iamSetConditionInt(ctx, "s3:object-lock-event-hold-duration-days", retention.EventHoldDuration.Days)
	case "PutObjectLegalHold":
		var hold struct {
			Status string `xml:"Status"`
		}
		if len(body) == 0 || xml.Unmarshal(body, &hold) != nil {
			return
		}
		iamSetConditionValues(ctx, "s3:object-lock-legal-hold", hold.Status)
	case "ListObjectAnnotations", "ListObjectVersionAnnotations":
		query := r.URL.Query()
		iamSetConditionValues(ctx, "s3:annotation-prefix", query.Get("annotation-prefix"))
		if limit := query.Get("max-annotation-results"); limit != "" {
			if n, err := strconv.ParseInt(limit, 10, 64); err == nil {
				iamSetConditionInt(ctx, "s3:max-annotation-results", &n)
			}
		}
	case "PutObjectAnnotation", "PutObjectVersionAnnotation",
		"DeleteObjectAnnotation", "DeleteObjectVersionAnnotation":
		iamSetConditionValues(ctx, "s3:x-amz-object-if-match", r.Header.Get("x-amz-object-if-match"))
	case "CreateBucket":
		iamSetConditionValues(ctx, "s3:x-amz-bucket-namespace", r.Header.Get("x-amz-bucket-namespace"))
		iamSetConditionValues(ctx, "s3:x-amz-object-ownership", r.Header.Get("x-amz-object-ownership"))
	case "PutInventoryConfiguration":
		var configuration struct {
			Fields []string `xml:"OptionalFields>Field"`
		}
		if len(body) == 0 || xml.Unmarshal(body, &configuration) != nil {
			return
		}
		iamSetConditionValues(ctx, "s3:InventoryAccessibleOptionalFields", configuration.Fields...)
	case "ListBucketMultipartUploads":
		ap, addressed := s3RequestAccessPoint(r)
		if !addressed {
			return
		}
		tags, _ := s3ControlResourceTags.Get(s3AccessPointARN(ap.AccountID, ap.Name))
		for key, value := range tags {
			iamSetConditionValues(ctx, "s3:AccessPointTag/"+key, value)
		}
	}
}

// iamSetS3RemainingRetentionDays turns a requested retain-until date into
// s3:object-lock-remaining-retention-days, the key a bucket policy conditions
// on to bound how long a caller may lock an object ("NumericLessThanEquals":
// {"s3:object-lock-remaining-retention-days": "10"}).
//
// A request without a retain-until date, or with one this simulator cannot
// parse, leaves the key unset: a policy that requires it then denies the
// request, which is the behaviour a retention bound is written for. A date in
// the past is zero days remaining, not a negative count.
//
// The period is rounded UP to whole days, so any part of a day counts as a
// day. AWS documents the key as the remaining retention period in days and
// does not document its rounding, and the IAM policy oracle this repo tests
// against (SimulateCustomPolicy) evaluates a context this simulator supplies,
// so it cannot settle the question either. Rounding up never understates a
// lock: an upper bound written for 10 days rejects 10 days and one second,
// which is the safe direction for a guard whose purpose is to stop a caller
// locking an object for longer than the bucket allows.
func iamSetS3RemainingRetentionDays(ctx map[string][]string, retainUntil string) {
	if retainUntil == "" {
		return
	}
	until, err := time.Parse(time.RFC3339, retainUntil)
	if err != nil {
		return
	}
	remaining := time.Until(until)
	if remaining < 0 {
		remaining = 0
	}
	days := int64(remaining / (24 * time.Hour))
	if remaining%(24*time.Hour) != 0 {
		days++
	}
	iamSetConditionInt(ctx, "s3:object-lock-remaining-retention-days", &days)
}
