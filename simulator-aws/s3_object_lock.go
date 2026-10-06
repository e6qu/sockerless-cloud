package main

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Object Lock. A bucket has Object Lock once CreateBucket carries
// x-amz-bucket-object-lock-enabled or PutObjectLockConfiguration enables it on
// a versioning-enabled bucket; from then on its versioning stays enabled. Each
// object version carries its own retention and legal hold, which keep that
// version from being deleted.

const (
	s3LockGovernance = "GOVERNANCE"
	s3LockCompliance = "COMPLIANCE"
	s3LegalHoldOn    = "ON"
	s3LegalHoldOff   = "OFF"
	// s3LockDateFormat is how S3 writes a retain-until date in the
	// x-amz-object-lock-retain-until-date header and the Retention body.
	s3LockDateFormat = "2006-01-02T15:04:05.000Z"
)

var errS3ObjectLocked = errors.New("access denied because object protected by object lock")

type s3ObjectLockConfiguration struct {
	XMLName           xml.Name          `xml:"ObjectLockConfiguration"`
	Xmlns             string            `xml:"xmlns,attr,omitempty"`
	ObjectLockEnabled string            `xml:"ObjectLockEnabled,omitempty"`
	Rule              *s3ObjectLockRule `xml:"Rule,omitempty"`
}

type s3ObjectLockRule struct {
	DefaultRetention *s3DefaultRetention `xml:"DefaultRetention"`
}

type s3DefaultRetention struct {
	Mode  string `xml:"Mode,omitempty"`
	Days  *int   `xml:"Days,omitempty"`
	Years *int   `xml:"Years,omitempty"`
}

// s3ObjectLock is the retention and legal hold a write asks for in its
// x-amz-object-lock-* headers.
type s3ObjectLock struct {
	Mode            string `json:",omitempty"`
	RetainUntilDate string `json:",omitempty"`
	LegalHold       string `json:",omitempty"`
}

func (lock s3ObjectLock) apply(obj *S3Object) {
	obj.RetentionMode, obj.RetainUntilDate, obj.LegalHoldStatus = lock.Mode, lock.RetainUntilDate, lock.LegalHold
}

// s3BucketObjectLock returns the bucket's Object Lock configuration and
// whether Object Lock is enabled on it.
func s3BucketObjectLock(bucket string) (s3ObjectLockConfiguration, bool) {
	body, _, _, ok := getStoredBucketSubresource(bucket, "object-lock")
	if !ok {
		return s3ObjectLockConfiguration{}, false
	}
	var config s3ObjectLockConfiguration
	if xml.Unmarshal(body, &config) != nil {
		return s3ObjectLockConfiguration{}, false
	}
	return config, config.ObjectLockEnabled == "Enabled"
}

func s3PutBucketConfig(bucket, sub string, body []byte) {
	key := s3BucketConfigKeyID(bucket, sub, "")
	s3BucketConfigs.Put(key, S3BucketConfig{Key: key, Bucket: bucket, Subresource: sub, Body: body, ContentType: "application/xml"})
}

func s3StoreObjectLockConfiguration(bucket string, config s3ObjectLockConfiguration) {
	config.Xmlns = "http://s3.amazonaws.com/doc/2006-03-01/"
	body, err := xml.Marshal(config)
	if err != nil {
		panic("marshal ObjectLockConfiguration: " + err.Error())
	}
	s3PutBucketConfig(bucket, "object-lock", append([]byte(xml.Header), body...))
}

// s3EnableObjectLockAtCreate turns Object Lock on for a bucket CreateBucket
// asked it for, and versioning with it.
func s3EnableObjectLockAtCreate(bucket string) {
	s3StoreObjectLockConfiguration(bucket, s3ObjectLockConfiguration{ObjectLockEnabled: "Enabled"})
	s3PutBucketConfig(bucket, "versioning", []byte(xml.Header+
		`<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`))
}

// s3DefaultRetainUntil is when the bucket's default retention keeps a version
// written at created until.
func s3DefaultRetainUntil(bucket string, created time.Time) (string, string, bool) {
	config, enabled := s3BucketObjectLock(bucket)
	if !enabled || config.Rule == nil || config.Rule.DefaultRetention == nil {
		return "", "", false
	}
	rule := config.Rule.DefaultRetention
	until := created.UTC()
	switch {
	case rule.Days != nil:
		until = until.AddDate(0, 0, *rule.Days)
	case rule.Years != nil:
		until = until.AddDate(*rule.Years, 0, 0)
	default:
		return "", "", false
	}
	return rule.Mode, until.Format(s3LockDateFormat), true
}

func s3MissingObjectLock(w http.ResponseWriter, r *http.Request, bucket string) {
	S3ErrorXML(w, "InvalidRequest", "Bucket is missing Object Lock Configuration",
		bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
}

// s3RequireObjectLock refuses an Object Lock operation on a bucket without
// Object Lock, as S3 does.
func s3RequireObjectLock(w http.ResponseWriter, r *http.Request) bool {
	bucket := sim.PathParam(r, "bucket")
	if _, enabled := s3BucketObjectLock(bucket); !enabled {
		s3MissingObjectLock(w, r, bucket)
		return false
	}
	return true
}

// handleS3PutObjectLockConfiguration enables Object Lock on a bucket or sets
// its default retention. Enabling it needs versioning enabled first.
func handleS3PutObjectLockConfiguration(w http.ResponseWriter, r *http.Request, bucket string, body []byte) {
	malformed := func() {
		S3ErrorXML(w, "MalformedXML",
			"The XML you provided was not well-formed or did not validate against our published schema",
			bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
	}
	var config s3ObjectLockConfiguration
	if xml.Unmarshal(body, &config) != nil || config.ObjectLockEnabled != "Enabled" {
		malformed()
		return
	}
	if config.Rule != nil {
		rule := config.Rule.DefaultRetention
		if rule == nil || (rule.Mode != s3LockGovernance && rule.Mode != s3LockCompliance) ||
			(rule.Days == nil) == (rule.Years == nil) {
			malformed()
			return
		}
		period, limit := rule.Days, 36500
		if rule.Years != nil {
			period, limit = rule.Years, 100
		}
		if *period <= 0 {
			S3ErrorXML(w, "InvalidArgument", "Default retention period must be a positive integer value.",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
		if *period > limit {
			S3ErrorXML(w, "InvalidArgument", "Default retention period is too large.",
				bucket, sim.RequestID(r.Context()), http.StatusBadRequest)
			return
		}
	}
	if _, enabled := s3BucketObjectLock(bucket); !enabled && s3VersioningStatus(bucket) != s3VersioningEnabled {
		S3ErrorXML(w, "InvalidBucketState", "Versioning must be 'Enabled' on the bucket to apply a Object Lock configuration",
			bucket, sim.RequestID(r.Context()), http.StatusConflict)
		return
	}
	s3StoreObjectLockConfiguration(bucket, config)
	w.WriteHeader(http.StatusOK)
}

// s3VersioningChangeRefused refuses a PutBucketVersioning that would suspend
// versioning on a bucket with Object Lock.
func s3VersioningChangeRefused(w http.ResponseWriter, r *http.Request, bucket string, body []byte) bool {
	if _, enabled := s3BucketObjectLock(bucket); !enabled {
		return false
	}
	var config struct {
		Status string `xml:"Status"`
	}
	if xml.Unmarshal(body, &config) == nil && config.Status == s3VersioningEnabled {
		return false
	}
	S3ErrorXML(w, "InvalidBucketState", "An Object Lock configuration is present on this bucket, so the versioning state cannot be changed.",
		bucket, sim.RequestID(r.Context()), http.StatusConflict)
	return true
}

// s3RequestedObjectLock reads the x-amz-object-lock-* headers of a write. When
// they ask for something S3 refuses it writes the error and reports false.
func s3RequestedObjectLock(w http.ResponseWriter, r *http.Request, bucket string) (s3ObjectLock, bool) {
	h := r.Header
	lock := s3ObjectLock{
		Mode:            h.Get("x-amz-object-lock-mode"),
		RetainUntilDate: h.Get("x-amz-object-lock-retain-until-date"),
		LegalHold:       h.Get("x-amz-object-lock-legal-hold"),
	}
	if lock == (s3ObjectLock{}) {
		return lock, true
	}
	invalid := func(message string) (s3ObjectLock, bool) {
		S3ErrorXML(w, "InvalidArgument", message, sim.PathParam(r, "key"), sim.RequestID(r.Context()), http.StatusBadRequest)
		return s3ObjectLock{}, false
	}
	if _, enabled := s3BucketObjectLock(bucket); !enabled {
		s3MissingObjectLock(w, r, bucket)
		return s3ObjectLock{}, false
	}
	if (lock.Mode == "") != (lock.RetainUntilDate == "") {
		return invalid("x-amz-object-lock-retain-until-date and x-amz-object-lock-mode must both be supplied")
	}
	if lock.Mode != "" {
		if lock.Mode != s3LockGovernance && lock.Mode != s3LockCompliance {
			return invalid("Unknown wormMode directive.")
		}
		until, err := time.Parse(time.RFC3339, lock.RetainUntilDate)
		if err != nil {
			return invalid("The retain until date must be provided in ISO 8601 format")
		}
		if !until.After(time.Now()) {
			return invalid("The retain until date must be in the future!")
		}
		lock.RetainUntilDate = until.UTC().Format(s3LockDateFormat)
	}
	if lock.LegalHold != "" && lock.LegalHold != s3LegalHoldOn && lock.LegalHold != s3LegalHoldOff {
		return invalid("Legal Hold must be either of 'ON' or 'OFF'")
	}
	return lock, true
}

// s3HasIntegrityCheck reports whether a write carries the Content-MD5 or the
// checksum S3 requires of a PutObject that sets Object Lock parameters.
func s3HasIntegrityCheck(h http.Header) bool {
	if h.Get("Content-MD5") != "" || h.Get("x-amz-sdk-checksum-algorithm") != "" ||
		strings.Contains(strings.ToLower(h.Get("x-amz-trailer")), "x-amz-checksum-") {
		return true
	}
	for name := range h {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-checksum-") {
			return true
		}
	}
	return false
}

// s3LockedUntil is the retain-until date of obj's retention.
func s3LockedUntil(obj S3Object) (time.Time, bool) {
	if obj.RetentionMode == "" {
		return time.Time{}, false
	}
	until, err := time.Parse(time.RFC3339, obj.RetainUntilDate)
	return until, err == nil
}

// s3VersionProtected reports whether the version's legal hold or retention
// keeps it from being deleted: a legal hold always, COMPLIANCE retention until
// its date, and GOVERNANCE retention until its date unless the request
// bypasses governance retention.
func s3VersionProtected(obj S3Object, bypassGovernance bool, now time.Time) bool {
	if obj.LegalHoldStatus == s3LegalHoldOn {
		return true
	}
	until, ok := s3LockedUntil(obj)
	if !ok || !until.After(now) {
		return false
	}
	return obj.RetentionMode == s3LockCompliance || !bypassGovernance
}

// s3RetentionChangeAllowed reports whether a version's retention may become
// mode until (both empty to remove it). An active retention may only be
// extended, or raised from GOVERNANCE to COMPLIANCE, unless it is GOVERNANCE
// and the request bypasses governance retention.
func s3RetentionChangeAllowed(obj S3Object, mode string, until time.Time, bypassGovernance bool, now time.Time) bool {
	current, ok := s3LockedUntil(obj)
	if !ok || !current.After(now) {
		return true
	}
	if obj.RetentionMode == s3LockGovernance && bypassGovernance {
		return true
	}
	if mode == "" || until.Before(current) {
		return false
	}
	return obj.RetentionMode != s3LockCompliance || mode != s3LockGovernance
}

func s3BypassesGovernance(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("x-amz-bypass-governance-retention"), "true")
}

func s3WriteObjectLocked(w http.ResponseWriter, r *http.Request, key string) {
	S3ErrorXML(w, "AccessDenied", "Access Denied because object protected by object lock.",
		key, sim.RequestID(r.Context()), http.StatusForbidden)
}

// s3SetObjectLockHeaders reports a version's retention and legal hold as
// GetObject and HeadObject do.
func s3SetObjectLockHeaders(w http.ResponseWriter, obj S3Object) {
	if obj.RetentionMode != "" {
		w.Header().Set("x-amz-object-lock-mode", obj.RetentionMode)
		w.Header().Set("x-amz-object-lock-retain-until-date", obj.RetainUntilDate)
	}
	if obj.LegalHoldStatus != "" {
		w.Header().Set("x-amz-object-lock-legal-hold", obj.LegalHoldStatus)
	}
}
