package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

func s3ConditionContext(r *http.Request, body string) map[string][]string {
	operation := s3ObjectOperationName(r, nil)
	if r.PathValue("key") == "" {
		operation = s3BucketOperationName(r, nil)
	}
	return requestConditionContext(r, "s3", operation, body)
}

func s3ObjectConditionRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.SetPathValue("bucket", "b")
	r.SetPathValue("key", "k")
	return r
}

func TestS3ConditionKeysReadObjectLockAndConditionalWriteHeaders(t *testing.T) {
	for _, target := range []string{"/b/k", "/b/k?uploads"} {
		method := http.MethodPut
		if target == "/b/k?uploads" {
			method = http.MethodPost
		}
		r := s3ObjectConditionRequest(method, target)
		r.Header.Set("If-None-Match", "*")
		r.Header.Set("x-amz-object-lock-mode", "GOVERNANCE")
		r.Header.Set("x-amz-object-lock-retain-until-date", "2030-01-01T00:00:00Z")
		r.Header.Set("x-amz-object-lock-legal-hold", "ON")
		r.Header.Set("x-amz-object-lock-event-hold", "ON")
		r.Header.Set("x-amz-object-lock-event-hold-duration-days", "90")
		assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
			"s3:if-none-match":                        {"*"},
			"s3:object-lock-mode":                     {"GOVERNANCE"},
			"s3:object-lock-retain-until-date":        {"2030-01-01T00:00:00Z"},
			"s3:object-lock-legal-hold":               {"ON"},
			"s3:object-lock-event-hold":               {"ON"},
			"s3:object-lock-event-hold-duration-days": {"90"},
		})
	}

	r := s3ObjectConditionRequest(http.MethodPut, "/b/k")
	r.Header.Set("x-amz-copy-source", "/src/k")
	r.Header.Set("x-amz-object-annotation-directive", "COPY")
	assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
		"s3:x-amz-object-annotation-directive": {"COPY"},
	})
}

func TestS3ConditionKeysReadObjectLockBodies(t *testing.T) {
	r := s3ObjectConditionRequest(http.MethodPut, "/b/k?retention")
	ctx := s3ConditionContext(r, `<Retention xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
		<Mode>COMPLIANCE</Mode><RetainUntilDate>2031-06-01T00:00:00Z</RetainUntilDate>
		<EventHold>OFF</EventHold><EventHoldDuration><Days>30</Days></EventHoldDuration>
	</Retention>`)
	assertConditionValues(t, ctx, map[string][]string{
		"s3:object-lock-mode":                     {"COMPLIANCE"},
		"s3:object-lock-retain-until-date":        {"2031-06-01T00:00:00Z"},
		"s3:object-lock-event-hold":               {"OFF"},
		"s3:object-lock-event-hold-duration-days": {"30"},
	})

	r = s3ObjectConditionRequest(http.MethodPut, "/b/k?legal-hold")
	ctx = s3ConditionContext(r, `<LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>OFF</Status></LegalHold>`)
	assertConditionValues(t, ctx, map[string][]string{"s3:object-lock-legal-hold": {"OFF"}})
}

func TestS3ConditionKeysReadAnnotationRequests(t *testing.T) {
	r := s3ObjectConditionRequest(http.MethodGet, "/b/k?annotation&annotation-prefix=review/&max-annotation-results=25")
	assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
		"s3:annotation-prefix":      {"review/"},
		"s3:max-annotation-results": {"25"},
	})

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		r = s3ObjectConditionRequest(method, "/b/k?annotation&annotationName=a&versionId=v1")
		r.Header.Set("x-amz-object-if-match", `"etag"`)
		assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
			"s3:x-amz-object-if-match": {`"etag"`},
		})
	}
}

func TestS3ConditionKeysReadBucketRequests(t *testing.T) {
	r := httptest.NewRequest(http.MethodPut, "/b", nil)
	r.SetPathValue("bucket", "b")
	r.Header.Set("x-amz-bucket-namespace", "account-regional")
	r.Header.Set("x-amz-object-ownership", "BucketOwnerEnforced")
	assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
		"s3:x-amz-bucket-namespace": {"account-regional"},
		"s3:x-amz-object-ownership": {"BucketOwnerEnforced"},
	})

	r = httptest.NewRequest(http.MethodPut, "/b?inventory&id=weekly", nil)
	r.SetPathValue("bucket", "b")
	ctx := s3ConditionContext(r, `<InventoryConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
		<Id>weekly</Id><IsEnabled>true</IsEnabled><IncludedObjectVersions>All</IncludedObjectVersions>
		<Schedule><Frequency>Weekly</Frequency></Schedule>
		<OptionalFields><Field>Size</Field><Field>ObjectOwner</Field></OptionalFields>
	</InventoryConfiguration>`)
	assertConditionValues(t, ctx, map[string][]string{
		"s3:InventoryAccessibleOptionalFields": {"Size", "ObjectOwner"},
	})
}

func TestS3ConditionKeysReadTheAccessPointTags(t *testing.T) {
	previous := s3ControlResourceTags
	t.Cleanup(func() { s3ControlResourceTags = previous })
	AwaitSimulatorBackground()
	s3ControlResourceTags = sim.MakeStore[map[string]string](nil, "s3_control_resource_tags")
	ap := S3AccessPoint{Name: "finance", AccountID: "123456789012", Bucket: "b"}
	s3ControlResourceTags.Put(s3AccessPointARN(ap.AccountID, ap.Name), map[string]string{"team": "ledger"})

	r := httptest.NewRequest(http.MethodGet, "/b?uploads", nil)
	r.SetPathValue("bucket", "b")
	r = r.WithContext(context.WithValue(r.Context(), s3AccessPointContextKey{}, ap))
	assertConditionValues(t, s3ConditionContext(r, ""), map[string][]string{
		"s3:AccessPointTag/team": {"ledger"},
	})

	// The same listing addressed to the bucket itself came through no access
	// point.
	r = httptest.NewRequest(http.MethodGet, "/b?uploads", nil)
	r.SetPathValue("bucket", "b")
	assertConditionKeysAbsent(t, s3ConditionContext(r, ""), "s3:AccessPointTag/team")
}

func TestS3ConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := s3ConditionContext(s3ObjectConditionRequest(http.MethodPut, "/b/k"), "")
	assertConditionKeysAbsent(t, ctx, "s3:if-none-match", "s3:object-lock-mode",
		"s3:object-lock-retain-until-date", "s3:object-lock-legal-hold",
		"s3:object-lock-event-hold", "s3:object-lock-event-hold-duration-days",
		"s3:x-amz-object-annotation-directive")

	// A conditional read is not a conditional write.
	r := s3ObjectConditionRequest(http.MethodGet, "/b/k")
	r.Header.Set("If-None-Match", `"etag"`)
	assertConditionKeysAbsent(t, s3ConditionContext(r, ""), "s3:if-none-match")

	r = s3ObjectConditionRequest(http.MethodPut, "/b/k?retention")
	ctx = s3ConditionContext(r, `<Retention><Mode>GOVERNANCE</Mode></Retention>`)
	assertConditionKeysAbsent(t, ctx, "s3:object-lock-event-hold-duration-days", "s3:object-lock-retain-until-date")
}
