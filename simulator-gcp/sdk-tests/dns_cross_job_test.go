package gcp_sdk_test

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestDNS_CrossJobResolution exercises cross-job DNS on GCP: private
// Cloud DNS zones back a real Docker user-defined network; A records
// added to the zone connect the referenced container to that network
// with the record's short name as DNS alias. Two Cloud Run Jobs on the
// same private zone resolve each other by short hostname via Docker's
// embedded DNS.
//
// Everything the test learns about the jobs it learns through Cloud Logging,
// as a client of the real service would: each job logs its own address, and
// alpha logs the marker once "beta" resolves.
func TestDNS_CrossJobResolution(t *testing.T) {
	// Cloud Logging keeps a deleted job's entries, so each run uses a project
	// of its own rather than read an earlier run's addresses.
	project := fmt.Sprintf("xjob-dns-%d", time.Now().UnixNano()%1_000_000_000)

	// 1. Create the private zone (simulator auto-backs it with a real
	// Docker network).
	dnsSvc, err := dns.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	zone, err := dnsSvc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:       "xjob-zone",
		DnsName:    "xjob.local.",
		Visibility: "private",
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, zone.Id, "zone should get a numeric ID")
	t.Cleanup(func() {
		for _, name := range []string{"alpha.xjob.local.", "beta.xjob.local."} {
			_, _ = dnsSvc.ResourceRecordSets.Delete(project, "xjob-zone", name, "A").Do()
		}
		_ = dnsSvc.ManagedZones.Delete(project, "xjob-zone").Do()
	})

	// 2. Run two Cloud Run Jobs that report their own addresses and stay up
	// while the records are created.
	jobsClient, err := run.NewJobsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { jobsClient.Close() })

	runJob := func(name, script string) {
		jobName := "projects/" + project + "/locations/us-central1/jobs/" + name
		createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: "projects/" + project + "/locations/us-central1",
			JobId:  name,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{{
							Image:   simWorkloadImage,
							Command: []string{"sh", "-c", script},
						}},
						Timeout: durationpb.New(60 * time.Second),
					},
				},
			},
		})
		require.NoError(t, err)
		_, err = createOp.Wait(ctx)
		require.NoError(t, err)
		t.Cleanup(func() {
			if op, err := jobsClient.DeleteJob(ctx, &runpb.DeleteJobRequest{Name: jobName}); err == nil {
				_, _ = op.Wait(ctx)
			}
		})

		runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{Name: jobName})
		require.NoError(t, err)
		_, err = runOp.Wait(ctx)
		require.NoError(t, err)
	}

	const reportAddress = `echo "address=$(hostname -i | cut -d' ' -f1)"; `
	runJob("alpha", reportAddress+
		`for i in $(seq 1 300); do `+
		`if nslookup beta >/dev/null 2>&1; then echo gcp-cross-job-dns-ok; exit 0; fi; `+
		`sleep 0.1; done; echo "beta never resolved" >&2; exit 1`)
	runJob("beta", reportAddress+`sleep 60`)

	alphaIP := jobAddress(t, project, "alpha")
	betaIP := jobAddress(t, project, "beta")

	// 3. Create A records — simulator connects each container to the
	// zone's Docker network with the short name as DNS alias.
	_, err = dnsSvc.ResourceRecordSets.Create(project, "xjob-zone", &dns.ResourceRecordSet{
		Name:    "alpha.xjob.local.",
		Type:    "A",
		Ttl:     60,
		Rrdatas: []string{alphaIP},
	}).Do()
	require.NoError(t, err)
	_, err = dnsSvc.ResourceRecordSets.Create(project, "xjob-zone", &dns.ResourceRecordSet{
		Name:    "beta.xjob.local.",
		Type:    "A",
		Ttl:     60,
		Rrdatas: []string{betaIP},
	}).Do()
	require.NoError(t, err)

	// 4. Cross-job DNS lookup: alpha resolves "beta" through the
	// container's own resolver once both A records have connected the
	// containers to the zone's Docker network.
	waitForProjectJobLogEntries(t, project, "alpha", func(entries []jobLogEntry) bool {
		return containsString(jobLogMessages(entries), "gcp-cross-job-dns-ok")
	})
}

// jobAddress waits for the job's container to log the address it reports and
// returns it.
func jobAddress(t *testing.T, project, job string) string {
	t.Helper()
	var address string
	waitForProjectJobLogEntries(t, project, job, func(entries []jobLogEntry) bool {
		for _, message := range jobLogMessages(entries) {
			if value, ok := strings.CutPrefix(message, "address="); ok && value != "" {
				address = value
				return true
			}
		}
		return false
	})
	require.NotNil(t, net.ParseIP(address), "job %s reported address %q", job, address)
	return address
}
