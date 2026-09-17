package main

import (
	"encoding/xml"
	"net/http"
	"strconv"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Amazon S3 condition keys that describe a Batch Operations job or the
// prefix an Access Grants grant or location covers.
//
// These are how an S3 control-plane policy says something about the *work*
// rather than about the ARN: let this role create jobs only for the tagging
// operation, only below a priority its pipeline reserves; let it retag or
// re-prioritise a job only when the job already running is the kind it owns;
// let it hand out grants only inside one prefix. The service reference
// declares every one of them against a control-plane action the simulator now
// serves, and the gate built none of them, so each such policy was evaluated
// against a context missing the key it tests — and a condition on an absent
// key never matches, so the policy denied exactly the request it was written
// to allow.
//
// Two shapes of key live here and they are derived differently. The Request*
// keys are what the call asks for, read from the request itself. The Existing*
// keys and the two scopes are facts about the resource the call names, read
// from the store the handler behind the gate would read. A request that names
// no such resource, or names one that does not exist, leaves its keys unset:
// the call is about to fail with NoSuchJob or a location-not-exists error, and
// a policy that tests the key must deny rather than compare against a value
// this file made up.
func init() {
	registerIAMRequestConditionPopulator("s3", iamPopulateS3ControlConditionKeys)
}

// iamPopulateS3ControlConditionKeys adds the keys the vendored service
// reference declares against each S3 control-plane action, which are exactly
// the actions switched on below:
//
//	"s3:RequestJobOperation"        CreateJob
//	"s3:RequestJobPriority"         CreateJob, UpdateJobPriority
//	"s3:ExistingJobOperation"       DeleteJobTagging, PutJobTagging,
//	                                UpdateJobPriority, UpdateJobStatus
//	"s3:ExistingJobPriority"        the same four
//	"s3:AccessGrantScope"           CreateAccessGrant, DeleteAccessGrant,
//	                                GetAccessGrant
//	"s3:AccessGrantsLocationScope"  CreateAccessGrantsLocation,
//	                                DeleteAccessGrantsLocation,
//	                                GetAccessGrantsLocation,
//	                                UpdateAccessGrantsLocation
func iamPopulateS3ControlConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "CreateJob":
		document := s3ControlConditionDocument(body)
		operationNode, _ := document.Child("Operation")
		iamSetConditionValues(ctx, "s3:RequestJobOperation", s3BatchJobOperationName(operationNode))
		iamSetS3JobPriority(ctx, "s3:RequestJobPriority", document.ChildText("Priority"))
	case "UpdateJobPriority":
		// The new priority travels as the `priority` query parameter, which is
		// where UpdateJobPriorityRequest binds it.
		iamSetS3JobPriority(ctx, "s3:RequestJobPriority", r.URL.Query().Get("priority"))
		iamPopulateS3ExistingJobConditionKeys(r, ctx)
	case "UpdateJobStatus", "PutJobTagging", "DeleteJobTagging":
		// s3:JobSuspendedCause is declared on UpdateJobStatus and is not set
		// here. It is the reason S3 suspended the job, which the service
		// writes and the caller never sends: JobDescriptor carries it as
		// SuspendedCause, "the reason why the specified job was suspended. A
		// job is only suspended if you create it through the Amazon S3
		// console", typed as a plain 1..1024 string with no enumeration. This
		// simulator suspends a job when the create asks for confirmation and
		// records no cause for it, so there is nothing to read; the vendored
		// model publishes no value AWS would have written either. Filling the
		// key in would be inventing the string a policy compares against, and
		// a wrong string matches a policy that must not match.
		iamPopulateS3ExistingJobConditionKeys(r, ctx)
	case "CreateAccessGrant":
		// The grant does not exist yet, so its scope is the one the request
		// asks for: the named location's scope, narrowed by the sub-prefix —
		// the same value the handler stores as the grant's GrantScope.
		document := s3ControlConditionDocument(body)
		location, ok := s3AccessGrantsLocations.Get(
			s3AccessPointKey(s3ControlAccountID(r), document.ChildText("AccessGrantsLocationId")))
		if !ok {
			return
		}
		subPrefix := ""
		if configuration, ok := document.Child("AccessGrantsLocationConfiguration"); ok {
			subPrefix = configuration.ChildText("S3SubPrefix")
		}
		iamSetConditionValues(ctx, "s3:AccessGrantScope",
			s3AccessGrantScope(location.LocationScope, subPrefix))
	case "GetAccessGrant", "DeleteAccessGrant":
		grant, ok := s3AccessGrants.Get(s3AccessPointKey(s3ControlAccountID(r), sim.PathParam(r, "grantId")))
		if !ok {
			return
		}
		iamSetConditionValues(ctx, "s3:AccessGrantScope", grant.GrantScope)
	case "CreateAccessGrantsLocation":
		// The location is being registered, so its scope is the S3 prefix the
		// request carries in LocationScope.
		iamSetConditionValues(ctx, "s3:AccessGrantsLocationScope",
			s3ControlConditionDocument(body).ChildText("LocationScope"))
	case "GetAccessGrantsLocation", "UpdateAccessGrantsLocation", "DeleteAccessGrantsLocation":
		// An update never changes the scope — it replaces the role — so the
		// registered scope is the location's scope for all three.
		location, ok := s3AccessGrantsLocations.Get(
			s3AccessPointKey(s3ControlAccountID(r), sim.PathParam(r, "locationId")))
		if !ok {
			return
		}
		iamSetConditionValues(ctx, "s3:AccessGrantsLocationScope", location.LocationScope)
	}
}

// iamPopulateS3ExistingJobConditionKeys adds the two keys that describe the
// job a request targets, read from the job the request names. A request whose
// job does not exist leaves both unset.
func iamPopulateS3ExistingJobConditionKeys(r *http.Request, ctx map[string][]string) {
	job, ok := s3BatchJobs.Get(s3AccessPointKey(s3ControlAccountID(r), sim.PathParam(r, "jobId")))
	if !ok {
		return
	}
	iamSetConditionValues(ctx, "s3:ExistingJobOperation", s3BatchJobOperationName(job.Operation))
	priority := int64(job.Priority)
	iamSetConditionInt(ctx, "s3:ExistingJobPriority", &priority)
}

// s3BatchJobOperationName is the operation a job runs, which is the name of the
// single member its JobOperation carries — LambdaInvoke, S3PutObjectCopy,
// S3PutObjectTagging and the rest, spelled as the model spells them. A
// document naming none, or naming more than one, settles no operation: which
// of two the service would report is not something this file can decide, so it
// names neither.
func s3BatchJobOperationName(operation s3ControlXMLNode) string {
	if len(operation.Children) != 1 {
		return ""
	}
	return operation.Children[0].Name
}

// iamSetS3JobPriority adds a job-priority key when the request states a
// priority this simulator can read as the number the key is compared as. The
// reference types both priority keys Numeric, so a value that is not a number
// is no priority at all.
func iamSetS3JobPriority(ctx map[string][]string, key, value string) {
	if value == "" {
		return
	}
	priority, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return
	}
	iamSetConditionInt(ctx, key, &priority)
}

// s3ControlConditionDocument reads the request's document for the gate. The
// body arrives already read, so unlike s3ControlGateBody nothing here has to
// put it back.
func s3ControlConditionDocument(body []byte) s3ControlXMLNode {
	var node s3ControlXMLNode
	if len(body) == 0 || xml.Unmarshal(body, &node) != nil {
		return s3ControlXMLNode{}
	}
	return node
}
