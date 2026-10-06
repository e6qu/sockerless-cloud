package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// s3StorageClasses is the S3 model's StorageClass enum; a test holds it there.
var s3StorageClasses = map[string]bool{
	"STANDARD": true, "REDUCED_REDUNDANCY": true, "STANDARD_IA": true, "ONEZONE_IA": true,
	"INTELLIGENT_TIERING": true, "GLACIER": true, "DEEP_ARCHIVE": true, "OUTPOSTS": true,
	"GLACIER_IR": true, "SNOW": true, "EXPRESS_ONEZONE": true, "FSX_OPENZFS": true,
	"FSX_ONTAP": true, "AWS_BACKUP_WARM": true, "AWS_BACKUP_LOW_COST_WARM": true,
}

// s3ArchiveStorageClasses hold objects that are not readable in real time
// until RestoreObject makes a temporary copy.
var s3ArchiveStorageClasses = map[string]bool{"GLACIER": true, "DEEP_ARCHIVE": true}

// s3RequestedStorageClass reads x-amz-storage-class; an absent header is
// STANDARD.
func s3RequestedStorageClass(r *http.Request) (string, error) {
	class := r.Header.Get("x-amz-storage-class")
	if class == "" {
		return "STANDARD", nil
	}
	if !s3StorageClasses[class] {
		return "", fmt.Errorf("the storage class you specified is not valid: %s", class)
	}
	return class, nil
}

// storageClassOf reads a stored object's class; a row written before the
// simulator recorded classes holds none and was STANDARD.
func (o S3Object) storageClassOf() string {
	if o.StorageClass == "" {
		return "STANDARD"
	}
	return o.StorageClass
}

func (u S3MultipartUpload) storageClassOf() string {
	if u.StorageClass == "" {
		return "STANDARD"
	}
	return u.StorageClass
}

// readable reports whether GetObject may return the object's contents: an
// archived object only while a completed restore has not expired.
func (o S3Object) readable(now time.Time) bool {
	if !s3ArchiveStorageClasses[o.storageClassOf()] {
		return true
	}
	return o.RestoreRequested && !o.RestoreInProgress && o.restoreExpiry().After(now)
}

func (o S3Object) restoreExpiry() time.Time {
	expiry, err := time.Parse(time.RFC3339, o.RestoreExpiryDate)
	if err != nil {
		return time.Time{}
	}
	return expiry
}

// s3SetObjectStateHeaders writes the headers GetObject and HeadObject share:
// x-amz-storage-class for every class but STANDARD, x-amz-restore once a
// restore has been requested, and the version's Object Lock settings.
func s3SetObjectStateHeaders(w http.ResponseWriter, obj S3Object) {
	if class := obj.storageClassOf(); class != "STANDARD" {
		w.Header().Set("x-amz-storage-class", class)
	}
	if obj.RestoreRequested {
		if obj.RestoreInProgress {
			w.Header().Set("x-amz-restore", `ongoing-request="true"`)
		} else {
			w.Header().Set("x-amz-restore", fmt.Sprintf(`ongoing-request="false", expiry-date="%s"`,
				obj.restoreExpiry().UTC().Format(http.TimeFormat)))
		}
	}
	s3SetObjectEncryptionHeaders(w, obj)
	s3SetObjectLockHeaders(w, obj)
}

// s3InvalidObjectState refuses a read of an archived object, naming its class
// as the model's InvalidObjectState error does.
func s3InvalidObjectState(w http.ResponseWriter, r *http.Request, key string, obj S3Object) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusForbidden)
	WriteXMLBody(w, S3ErrorResponse{
		Code:         "InvalidObjectState",
		Message:      "The operation is not valid for the object's storage class",
		Resource:     key,
		RequestID:    sim.RequestID(r.Context()),
		StorageClass: obj.storageClassOf(),
	})
}

// s3RestoreExpiry is the restored copy's expiry: the given number of days
// from now, at the following midnight UTC, as S3 reports it.
func s3RestoreExpiry(now time.Time, days int) time.Time {
	end := now.UTC().AddDate(0, 0, days)
	midnight := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	if midnight.Before(end) {
		midnight = midnight.AddDate(0, 0, 1)
	}
	return midnight
}
