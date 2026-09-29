package gcp_sdk_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
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
func TestDNS_CrossJobResolution(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	project := "xjob-dns-project"

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
	defer dnsSvc.ManagedZones.Delete(project, "xjob-zone").Do()

	// 2. Create two Cloud Run Jobs with long-running containers so we
	// can inspect them + exec into them during the test.
	jobsClient, err := run.NewJobsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { jobsClient.Close() })

	createJob := func(name string, args []string) string {
		createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: "projects/" + project + "/locations/us-central1",
			JobId:  name,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{{
							Image: commandImageName,
							Args:  args,
						}},
						Timeout: durationpb.New(60 * time.Second),
					},
				},
			},
		})
		require.NoError(t, err)
		_, err = createOp.Wait(ctx)
		require.NoError(t, err)

		runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
			Name: "projects/" + project + "/locations/us-central1/jobs/" + name,
		})
		require.NoError(t, err)
		exec, err := runOp.Wait(ctx)
		require.NoError(t, err)
		return exec.Name
	}

	alphaExec := createJob("alpha", []string{"resolve", "beta", "30", "gcp-cross-job-dns-ok", "10"})
	betaExec := createJob("beta", []string{"sleep", "60"})

	// 3. Wait for the Docker containers to exist + inspect their IPs.
	// Container name convention: sockerless-sim-gcp-job-<execID[:12]>
	// where execID is the last path segment of the execution Name.
	alphaContainer := jobContainerName(alphaExec)
	betaContainer := jobContainerName(betaExec)

	require.Eventually(t, func() bool {
		for _, n := range []string{alphaContainer, betaContainer} {
			if err := exec.Command("docker", "inspect", n).Run(); err != nil {
				return false
			}
		}
		return true
	}, 30*time.Second, 300*time.Millisecond, "Docker containers should be running")

	alphaIP := containerIP(t, alphaContainer)
	betaIP := containerIP(t, betaContainer)
	require.NotEmpty(t, alphaIP, "alpha container should have an IP")
	require.NotEmpty(t, betaIP, "beta container should have an IP")

	// 4. Create A records — simulator connects each container to the
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

	// 5. Cross-job DNS lookup: alpha resolves "beta" through the
	// container's own resolver after both A records have connected the
	// containers to the zone's Docker network.
	var logs []byte
	require.Eventually(t, func() bool {
		var err error
		logs, err = exec.Command("docker", "logs", alphaContainer).CombinedOutput()
		return err == nil && strings.Contains(string(logs), "gcp-cross-job-dns-ok")
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve 'beta' via Cloud DNS private zone: %s", logs)
	assert.Contains(t, string(logs), "gcp-cross-job-dns-ok")
}

// jobContainerName derives the simulator's Docker container name from
// a Cloud Run Job execution resource name. Matches the convention in
// simulator-gcp/cloudrunjobs.go (line ~412):
//
//	name := "sockerless-sim-gcp-job-" + execShort[:12]
func jobContainerName(executionName string) string {
	last := executionName
	if idx := strings.LastIndex(executionName, "/"); idx >= 0 {
		last = executionName[idx+1:]
	}
	if len(last) > 12 {
		last = last[:12]
	}
	return "sockerless-sim-gcp-job-" + last
}

// containerIP returns the container's IPv4 on its default Docker
// network. Used by the cross-job DNS test to build A records that
// point at the real container IPs so the simulator's private-zone
// handler can map IP → container → alias.
func containerIP(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("docker", "inspect", name).Output()
	require.NoError(t, err)

	var inspected []struct {
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	require.NoError(t, json.Unmarshal(out, &inspected))
	require.NotEmpty(t, inspected)
	for _, net := range inspected[0].NetworkSettings.Networks {
		if net.IPAddress != "" {
			return net.IPAddress
		}
	}
	return ""
}
