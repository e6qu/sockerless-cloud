package main

import (
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The hosting data plane resolves a branch's newest successful job, that job's
// files, and — for domain verification and certificate validation — a CNAME in
// any hosted zone. Each ran over a whole store per request and is now indexed,
// so each is checked here against the scan it replaced rather than against a
// hand-written expectation that could drift with it.

func seedAmplifyIndexStores(t *testing.T) {
	t.Helper()
	amplifyJobs = sim.MakeStore[amplifyStoredJob](nil, "test_index_amplify_jobs")
	amplifyArtifacts = sim.MakeStore[amplifyStoredArtifact](nil, "test_index_amplify_artifacts")
	t.Cleanup(func() { amplifyJobs, amplifyArtifacts = nil, nil })

	put := func(app, branch, jobID string, status AmplifyJobStatus, start float64) {
		job := amplifyStoredJob{AppId: app, BranchName: branch}
		job.Job.Summary.JobId = jobID
		job.Job.Summary.Status = status
		job.Job.Summary.StartTime = start
		amplifyJobs.Put(app+"/"+branch+"/"+jobID, job)
	}
	// Two applications share a branch name, and one branch holds a newer
	// failed job than its newest successful one.
	put("app-1", "main", "1", AmplifyJobStatusSucceed, 1)
	put("app-1", "main", "2", AmplifyJobStatusSucceed, 3)
	put("app-1", "main", "3", AmplifyJobStatusFailed, 4)
	put("app-1", "release", "4", AmplifyJobStatusSucceed, 5)
	put("app-2", "main", "5", AmplifyJobStatusSucceed, 6)

	artifact := func(app, branch, jobID, name string, hosted bool) {
		stored := amplifyStoredArtifact{
			AppId: app, BranchName: branch, JobId: jobID,
			Key: app + "/" + branch + "/" + jobID + "/" + name, HostedContent: hosted,
		}
		stored.Artifact.ArtifactFileName = name
		amplifyArtifacts.Put(stored.Key, stored)
	}
	artifact("app-1", "main", "2", "index.html", true)
	artifact("app-1", "main", "2", "app.js", true)
	artifact("app-1", "main", "2", "e2e-report.json", false)
	artifact("app-2", "main", "5", "index.html", true)
}

func TestAmplifyHostingLookupsMatchTheirScans(t *testing.T) {
	seedAmplifyIndexStores(t)

	// The scan the index replaced: every job in the process, filtered.
	scanLatest := func(app, branch string) (amplifyStoredJob, bool) {
		var jobs []amplifyStoredJob
		for _, j := range amplifyJobs.List() {
			if j.AppId == app && j.BranchName == branch && j.Job.Summary.Status == AmplifyJobStatusSucceed {
				jobs = append(jobs, j)
			}
		}
		if len(jobs) == 0 {
			return amplifyStoredJob{}, false
		}
		sort.Slice(jobs, func(i, k int) bool {
			si, sk := jobs[i].Job.Summary, jobs[k].Job.Summary
			if si.StartTime != sk.StartTime {
				return si.StartTime > sk.StartTime
			}
			return si.JobId > sk.JobId
		})
		return jobs[0], true
	}

	for _, c := range []struct{ app, branch string }{
		{"app-1", "main"}, {"app-1", "release"}, {"app-2", "main"},
		{"app-1", "absent"}, {"absent", "main"},
	} {
		want, wantOK := scanLatest(c.app, c.branch)
		got, gotOK := amplifyLatestSucceededJob(c.app, c.branch)
		if wantOK != gotOK || want.Job.Summary.JobId != got.Job.Summary.JobId {
			t.Errorf("%s/%s: scan %v/%q, index %v/%q",
				c.app, c.branch, wantOK, want.Job.Summary.JobId, gotOK, got.Job.Summary.JobId)
		}
	}

	// The newest successful job wins over a newer failed one, and a second
	// call is unaffected by the first having sorted its rows.
	for range 2 {
		job, ok := amplifyLatestSucceededJob("app-1", "main")
		if !ok || job.Job.Summary.JobId != "2" {
			t.Fatalf("app-1/main must serve job 2, got %q (%v)", job.Job.Summary.JobId, ok)
		}
	}

	// Artifacts: only the addressed job's hosted content, never a sibling
	// application's or a job's end-to-end test output.
	names := map[string]bool{}
	for _, a := range amplifyHostedArtifactsByJob.LookupAll(amplifyArtifacts,
		amplifyJobKey("app-1", "main", "2"),
		func(a amplifyStoredArtifact) []string {
			if !a.HostedContent {
				return nil
			}
			return []string{amplifyJobKey(a.AppId, a.BranchName, a.JobId)}
		}) {
		names[a.Artifact.ArtifactFileName] = true
	}
	if len(names) != 2 || !names["index.html"] || !names["app.js"] {
		t.Errorf("a job's hosted content must be its own two files, got %v", names)
	}
}

func TestRoute53CNAMELookupMatchesTheZoneScan(t *testing.T) {
	r53Zones = sim.MakeStore[r53StoredZone](nil, "test_index_route53_zones")
	t.Cleanup(func() { r53Zones = nil })

	zone := func(name string, records ...R53ResourceRecordSet) {
		stored := r53StoredZone{Records: records}
		stored.Zone.Name = name
		r53Zones.Put(name, stored)
	}
	cname := func(name, value string) R53ResourceRecordSet {
		return R53ResourceRecordSet{
			Name: name, Type: "CNAME",
			ResourceRecords: &R53ResourceRecords{Items: []R53ResourceRecord{{Value: value}}},
		}
	}
	// Two zones carry a record of the same name, and the trailing dots and
	// letter case differ from what a caller asks with.
	zone("example.com.",
		cname("_acm-challenge.example.com.", "validation.acm-validations.aws."),
		R53ResourceRecordSet{Name: "www.example.com.", Type: "A"})
	zone("other.test.",
		cname("_acm-challenge.example.com.", "someone-elses-value."))
	zone("sub.example.com.",
		cname("app.sub.example.com.", "cloudfront.net."))

	// Certificate validation: the record must be found whatever zone holds
	// it, and the value must match.
	if !acmDNSRecordMatches("_ACM-Challenge.Example.com", "validation.acm-validations.aws") {
		t.Error("the validation record exists and must be found, case- and dot-insensitively")
	}
	if acmDNSRecordMatches("_acm-challenge.example.com", "not-the-value") {
		t.Error("a record with a different value must not validate")
	}
	if acmDNSRecordMatches("_acm-challenge.absent.test", "validation.acm-validations.aws") {
		t.Error("a name no zone carries must not validate")
	}

	// Domain verification additionally requires the zone to cover the domain,
	// which is what separates it from certificate validation.
	if !amplifyRoute53HasCNAME("app.sub.example.com", "app.sub.example.com", "cloudfront.net") {
		t.Error("the covering zone carries the record and must verify")
	}
	if amplifyRoute53HasCNAME("example.com", "app.sub.example.com", "cloudfront.net") {
		t.Error("a record in a zone that does not cover the domain must not verify")
	}

	// The index answers exactly the set the scan did.
	var want []string
	for _, z := range r53Zones.List() {
		for _, rec := range z.Records {
			if strings.EqualFold(rec.Type, "CNAME") &&
				r53DNSName(rec.Name) == "_acm-challenge.example.com" {
				want = append(want, z.Zone.Name)
			}
		}
	}
	var got []string
	for _, z := range r53ZonesWithCNAME("_acm-challenge.example.com.") {
		got = append(got, z.Zone.Name)
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(want) != 2 || strings.Join(want, ",") != strings.Join(got, ",") {
		t.Errorf("zones carrying the record: scan %v, index %v", want, got)
	}
}
