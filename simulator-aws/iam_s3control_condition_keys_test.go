package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

const s3ControlConditionAccount = "123456789012"

// s3ControlConditionRequest is a control-plane request as the gate sees it:
// the account in the header every S3 Control operation binds it to, and the
// path values the router has already matched.
func s3ControlConditionRequest(method, target string, pathValues map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("x-amz-account-id", s3ControlConditionAccount)
	for name, value := range pathValues {
		r.SetPathValue(name, value)
	}
	return r
}

// s3ControlConditionStores replaces the control-plane stores the keys are read
// from with empty ones, so each test states the jobs, grants and locations it
// is about.
func s3ControlConditionStores(t *testing.T) {
	t.Helper()
	jobs, grants, locations := s3BatchJobs, s3AccessGrants, s3AccessGrantsLocations
	t.Cleanup(func() {
		s3BatchJobs, s3AccessGrants, s3AccessGrantsLocations = jobs, grants, locations
	})
	AwaitSimulatorBackground()
	s3BatchJobs = sim.MakeStore[S3BatchJob](nil, "s3_batch_jobs")
	s3AccessGrants = sim.MakeStore[S3AccessGrant](nil, "s3_access_grants")
	s3AccessGrantsLocations = sim.MakeStore[S3AccessGrantsLocation](nil, "s3_access_grants_locations")
}

// storeS3ConditionJob puts one job in the store under the key the handlers use.
func storeS3ConditionJob(jobID, operation string, priority int, status string) {
	s3BatchJobs.Put(s3AccessPointKey(s3ControlConditionAccount, jobID), S3BatchJob{
		AccountID: s3ControlConditionAccount, JobID: jobID, Status: status, Priority: priority,
		Operation: s3ControlXMLNode{Name: "Operation", Children: []s3ControlXMLNode{{Name: operation}}},
	})
}

// TestS3ControlConditionKeysReadTheRequestedJob proves the two Request* keys a
// policy caps a new job with: the operation the job is created to run and the
// priority it asks for.
func TestS3ControlConditionKeysReadTheRequestedJob(t *testing.T) {
	s3ControlConditionStores(t)

	create := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs", nil)
	ctx := populatedConditionContext(create, "s3", "CreateJob", `<CreateJobRequest>
		<ConfirmationRequired>false</ConfirmationRequired>
		<Operation><S3PutObjectTagging><TagSet><member><Key>k</Key><Value>v</Value></member></TagSet></S3PutObjectTagging></Operation>
		<Priority>42</Priority>
		<RoleArn>arn:aws:iam::123456789012:role/batch</RoleArn>
	</CreateJobRequest>`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"s3:RequestJobOperation": {"S3PutObjectTagging"},
		"s3:RequestJobPriority":  {"42"},
	})

	// The priority a re-prioritisation asks for is the same key, and it travels
	// as the query parameter UpdateJobPriorityRequest binds it to.
	storeS3ConditionJob("job-1", "LambdaInvoke", 5, "Active")
	update := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs/job-1/priority?priority=900",
		map[string]string{"jobId": "job-1"})
	assertPopulatedConditionValues(t, populatedConditionContext(update, "s3", "UpdateJobPriority", ""),
		map[string][]string{"s3:RequestJobPriority": {"900"}})
}

// TestS3ControlJobConditionKeysDecidePolicies is the defect itself, stated as a
// decision rather than as a context: a policy that lets a role create only
// tagging jobs, and only below the priority its pipeline reserves, allows the
// job it was written for and denies the one it was written against. Before the
// keys were built, the allow was an implicitDeny — the condition tested keys
// that were not in the context, so the policy refused every job.
func TestS3ControlJobConditionKeysDecidePolicies(t *testing.T) {
	s3ControlConditionStores(t)
	const caller = "arn:aws:iam::123456789012:role/batch-operator"
	policy, err := parseIAMPolicy(`{"Version":"2012-10-17","Statement":[{
		"Effect":"Allow","Action":"s3:CreateJob","Resource":"*",
		"Condition":{
			"StringEquals":{"s3:RequestJobOperation":"S3PutObjectTagging"},
			"NumericLessThanEquals":{"s3:RequestJobPriority":"100"}}}]}`)
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}

	job := func(operation string, priority int) string {
		return `<CreateJobRequest><Operation><` + operation + `/></Operation>` +
			`<Priority>` + strconv.Itoa(priority) + `</Priority></CreateJobRequest>`
	}
	for _, want := range []struct {
		body     string
		decision string
		why      string
	}{
		{job("S3PutObjectTagging", 10), "allowed", "the operation and priority the policy permits"},
		{job("S3PutObjectCopy", 10), "implicitDeny", "an operation the policy does not permit"},
		{job("S3PutObjectTagging", 900), "implicitDeny", "a priority above the cap"},
	} {
		r := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs", nil)
		ctx := populatedConditionContext(r, "s3", "CreateJob", want.body)
		got, _ := iamEvalDecisionForPrincipal([]iamPolicyDoc{policy}, "s3:CreateJob", "*", caller, ctx)
		if got != want.decision {
			t.Errorf("CreateJob with %s: decision = %q, want %q", want.why, got, want.decision)
		}
	}
}

// TestS3ControlConditionKeysReadTheTargetedJob proves the Existing* keys on
// every action that declares them: the operation and the current priority of
// the job the request names, read from the stored job.
func TestS3ControlConditionKeysReadTheTargetedJob(t *testing.T) {
	s3ControlConditionStores(t)
	storeS3ConditionJob("job-7", "S3PutObjectCopy", 12, "Suspended")

	for _, call := range []struct {
		operation string
		method    string
		target    string
	}{
		{"UpdateJobPriority", http.MethodPost, "/v20180820/jobs/job-7/priority?priority=3"},
		{"UpdateJobStatus", http.MethodPost, "/v20180820/jobs/job-7/status?requestedJobStatus=Ready"},
		{"PutJobTagging", http.MethodPut, "/v20180820/jobs/job-7/tagging"},
		{"DeleteJobTagging", http.MethodDelete, "/v20180820/jobs/job-7/tagging"},
	} {
		t.Run(call.operation, func(t *testing.T) {
			r := s3ControlConditionRequest(call.method, call.target, map[string]string{"jobId": "job-7"})
			assertPopulatedConditionValues(t, populatedConditionContext(r, "s3", call.operation, ""),
				map[string][]string{
					"s3:ExistingJobOperation": {"S3PutObjectCopy"},
					"s3:ExistingJobPriority":  {"12"},
				})
		})
	}
}

// TestS3ControlConditionKeysLeaveTheSuspendedCauseUnset records why the
// seventh key UpdateJobStatus declares is absent. SuspendedCause is written by
// S3, not sent by the caller: the vendored S3 Control model types it as a
// plain 1..1024 string with no enumerated value, and this simulator suspends a
// job awaiting confirmation without recording a cause. There is therefore
// nothing to read, and a made-up string would match policies that must not
// match.
func TestS3ControlConditionKeysLeaveTheSuspendedCauseUnset(t *testing.T) {
	s3ControlConditionStores(t)
	storeS3ConditionJob("job-9", "LambdaInvoke", 1, "Suspended")

	r := s3ControlConditionRequest(http.MethodPost,
		"/v20180820/jobs/job-9/status?requestedJobStatus=Ready&statusUpdateReason=go",
		map[string]string{"jobId": "job-9"})
	ctx := populatedConditionContext(r, "s3", "UpdateJobStatus", "")
	assertPopulatedConditionValues(t, ctx, map[string][]string{"s3:ExistingJobOperation": {"LambdaInvoke"}})
	assertConditionKeysAbsent(t, ctx, "s3:JobSuspendedCause")
}

// TestS3ControlJobConditionKeysAbsentWithoutTheirSubject proves each job key is
// left out rather than guessed: no job in the store, no Existing* keys; no
// priority or no readable operation in the request, no Request* keys.
func TestS3ControlJobConditionKeysAbsentWithoutTheirSubject(t *testing.T) {
	s3ControlConditionStores(t)

	missing := s3ControlConditionRequest(http.MethodPut, "/v20180820/jobs/gone/tagging",
		map[string]string{"jobId": "gone"})
	assertConditionKeysAbsent(t, populatedConditionContext(missing, "s3", "PutJobTagging", ""),
		"s3:ExistingJobOperation", "s3:ExistingJobPriority")

	create := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs", nil)
	assertConditionKeysAbsent(t, populatedConditionContext(create, "s3", "CreateJob",
		`<CreateJobRequest><RoleArn>arn:aws:iam::123456789012:role/batch</RoleArn></CreateJobRequest>`),
		"s3:RequestJobOperation", "s3:RequestJobPriority")

	// A priority that is not a number is no priority: both keys are Numeric in
	// the reference, and a comparison against "soon" is not one AWS would make.
	text := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs/job-1/priority?priority=soon",
		map[string]string{"jobId": "job-1"})
	assertConditionKeysAbsent(t, populatedConditionContext(text, "s3", "UpdateJobPriority", ""),
		"s3:RequestJobPriority")

	// A document naming two operations settles neither.
	two := s3ControlConditionRequest(http.MethodPost, "/v20180820/jobs", nil)
	assertConditionKeysAbsent(t, populatedConditionContext(two, "s3", "CreateJob",
		`<CreateJobRequest><Operation><LambdaInvoke/><S3PutObjectAcl/></Operation></CreateJobRequest>`),
		"s3:RequestJobOperation")
}

// TestS3ControlConditionKeysReadTheAccessGrantScopes proves the two scope keys
// a policy pens Access Grants in with: the prefix a grant covers and the prefix
// a location manages, on every action the reference declares them against.
func TestS3ControlConditionKeysReadTheAccessGrantScopes(t *testing.T) {
	s3ControlConditionStores(t)
	s3AccessGrantsLocations.Put(s3AccessPointKey(s3ControlConditionAccount, "loc-1"),
		S3AccessGrantsLocation{
			AccountID: s3ControlConditionAccount, LocationID: "loc-1",
			LocationScope: "s3://ledger/*", IAMRoleArn: "arn:aws:iam::123456789012:role/grants",
		})
	s3AccessGrants.Put(s3AccessPointKey(s3ControlConditionAccount, "grant-1"),
		S3AccessGrant{
			AccountID: s3ControlConditionAccount, GrantID: "grant-1", LocationID: "loc-1",
			GrantScope: "s3://ledger/2026/*", Permission: "READ",
		})

	// A grant that does not exist yet is scoped by what the create asks for:
	// the location's scope narrowed by the sub-prefix.
	create := s3ControlConditionRequest(http.MethodPost, "/v20180820/accessgrantsinstance/grant", nil)
	assertPopulatedConditionValues(t, populatedConditionContext(create, "s3", "CreateAccessGrant",
		`<CreateAccessGrantRequest>
			<AccessGrantsLocationId>loc-1</AccessGrantsLocationId>
			<AccessGrantsLocationConfiguration><S3SubPrefix>2026/*</S3SubPrefix></AccessGrantsLocationConfiguration>
			<Permission>READ</Permission>
			<Grantee><GranteeType>IAM</GranteeType><GranteeIdentifier>arn:aws:iam::123456789012:role/reader</GranteeIdentifier></Grantee>
		</CreateAccessGrantRequest>`),
		map[string][]string{"s3:AccessGrantScope": {"s3://ledger/2026/*"}})

	for _, call := range []struct {
		operation string
		method    string
	}{{"GetAccessGrant", http.MethodGet}, {"DeleteAccessGrant", http.MethodDelete}} {
		r := s3ControlConditionRequest(call.method, "/v20180820/accessgrantsinstance/grant/grant-1",
			map[string]string{"grantId": "grant-1"})
		assertPopulatedConditionValues(t, populatedConditionContext(r, "s3", call.operation, ""),
			map[string][]string{"s3:AccessGrantScope": {"s3://ledger/2026/*"}})
	}

	register := s3ControlConditionRequest(http.MethodPost, "/v20180820/accessgrantsinstance/location", nil)
	assertPopulatedConditionValues(t, populatedConditionContext(register, "s3", "CreateAccessGrantsLocation",
		`<CreateAccessGrantsLocationRequest>
			<LocationScope>s3://ledger/*</LocationScope>
			<IAMRoleArn>arn:aws:iam::123456789012:role/grants</IAMRoleArn>
		</CreateAccessGrantsLocationRequest>`),
		map[string][]string{"s3:AccessGrantsLocationScope": {"s3://ledger/*"}})

	for _, call := range []struct {
		operation string
		method    string
	}{
		{"GetAccessGrantsLocation", http.MethodGet},
		{"UpdateAccessGrantsLocation", http.MethodPut},
		{"DeleteAccessGrantsLocation", http.MethodDelete},
	} {
		r := s3ControlConditionRequest(call.method, "/v20180820/accessgrantsinstance/location/loc-1",
			map[string]string{"locationId": "loc-1"})
		assertPopulatedConditionValues(t, populatedConditionContext(r, "s3", call.operation, ""),
			map[string][]string{"s3:AccessGrantsLocationScope": {"s3://ledger/*"}})
	}
}

// TestS3ControlScopeConditionKeysAbsentWithoutTheirSubject proves a scope is
// never guessed: a grant or location that is not there, and a create naming a
// location that is not registered, leave the key out so a policy testing it
// denies.
func TestS3ControlScopeConditionKeysAbsentWithoutTheirSubject(t *testing.T) {
	s3ControlConditionStores(t)

	grant := s3ControlConditionRequest(http.MethodGet, "/v20180820/accessgrantsinstance/grant/gone",
		map[string]string{"grantId": "gone"})
	assertConditionKeysAbsent(t, populatedConditionContext(grant, "s3", "GetAccessGrant", ""),
		"s3:AccessGrantScope")

	location := s3ControlConditionRequest(http.MethodGet, "/v20180820/accessgrantsinstance/location/gone",
		map[string]string{"locationId": "gone"})
	assertConditionKeysAbsent(t, populatedConditionContext(location, "s3", "GetAccessGrantsLocation", ""),
		"s3:AccessGrantsLocationScope")

	create := s3ControlConditionRequest(http.MethodPost, "/v20180820/accessgrantsinstance/grant", nil)
	assertConditionKeysAbsent(t, populatedConditionContext(create, "s3", "CreateAccessGrant",
		`<CreateAccessGrantRequest><AccessGrantsLocationId>gone</AccessGrantsLocationId></CreateAccessGrantRequest>`),
		"s3:AccessGrantScope")

	register := s3ControlConditionRequest(http.MethodPost, "/v20180820/accessgrantsinstance/location", nil)
	assertConditionKeysAbsent(t, populatedConditionContext(register, "s3", "CreateAccessGrantsLocation",
		`<CreateAccessGrantsLocationRequest><IAMRoleArn>arn:aws:iam::123456789012:role/grants</IAMRoleArn></CreateAccessGrantsLocationRequest>`),
		"s3:AccessGrantsLocationScope")
}
