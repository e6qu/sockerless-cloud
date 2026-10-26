package gcp_sdk_test

import (
	"encoding/json"
	"net/netip"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestDNS_CrossJobResolution runs two Cloud Run jobs with direct VPC egress
// onto one subnetwork, with a private Cloud DNS zone visible to the network.
// The zone's A record names one job's task address, and the other job resolves
// it through the resolver the subnetwork hands its workloads.
func TestDNS_CrossJobResolution(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker CLI required for cross-job DNS test (no fallback): %v", err)
	}

	const project, region = "xjob-dns-project", "us-central1"
	svc := computeService(t)
	createInstanceSubnetwork(t, svc, project, region, "xjob-net", "10.84.0.0/24")

	dnsSvc, err := dns.NewService(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)

	zone, err := dnsSvc.ManagedZones.Create(project, &dns.ManagedZone{
		Name:       "xjob-zone",
		DnsName:    "xjob.local.",
		Visibility: "private",
		PrivateVisibilityConfig: &dns.ManagedZonePrivateVisibilityConfig{
			Networks: []*dns.ManagedZonePrivateVisibilityConfigNetwork{{NetworkUrl: "projects/" + project + "/global/networks/xjob-net"}},
		},
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, zone.Id, "zone should get a numeric ID")
	defer deleteDNSZone(t, dnsSvc, project, "xjob-zone")

	jobsClient, err := run.NewJobsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { jobsClient.Close() })
	execClient, err := run.NewExecutionsRESTClient(ctx,
		option.WithEndpoint(baseURL),
		option.WithTokenSource(simTokenSource()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { execClient.Close() })

	createJob := func(name string, args []string) string {
		createOp, err := jobsClient.CreateJob(ctx, &runpb.CreateJobRequest{
			Parent: "projects/" + project + "/locations/" + region,
			JobId:  name,
			Job: &runpb.Job{
				Template: &runpb.ExecutionTemplate{
					Template: &runpb.TaskTemplate{
						Containers: []*runpb.Container{{
							Image: commandImageName,
							Args:  args,
						}},
						Timeout: durationpb.New(60 * time.Second),
						VpcAccess: &runpb.VpcAccess{
							NetworkInterfaces: []*runpb.VpcAccess_NetworkInterface{{Network: "xjob-net", Subnetwork: "xjob-net"}},
							Egress:            runpb.VpcAccess_PRIVATE_RANGES_ONLY,
						},
					},
				},
			},
		})
		require.NoError(t, err)
		_, err = createOp.Wait(ctx)
		require.NoError(t, err)

		runOp, err := jobsClient.RunJob(ctx, &runpb.RunJobRequest{
			Name: "projects/" + project + "/locations/" + region + "/jobs/" + name,
		})
		require.NoError(t, err)
		// The workload keeps running while the test wires DNS around it, so take the
		// execution from the operation's metadata rather than its completion.
		exec, err := runOp.Metadata()
		require.NoError(t, err)
		require.NotNil(t, exec)
		t.Cleanup(func() {
			cancelOp, err := execClient.CancelExecution(ctx, &runpb.CancelExecutionRequest{Name: exec.Name})
			if err == nil {
				_, err = cancelOp.Wait(ctx)
			}
			if err != nil && status.Code(err) != codes.FailedPrecondition {
				t.Errorf("cancel %s: %v", exec.Name, err)
			}
		})
		return exec.Name
	}

	alphaExec := createJob("alpha", []string{"resolve", "beta.xjob.local", "30", "gcp-cross-job-dns-ok", "10"})
	betaExec := createJob("beta", []string{"sleep", "60"})

	// Cloud Run reports no address for a task, so read it off the engine.
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

	subnet := netip.MustParsePrefix("10.84.0.0/24")
	alphaIP := containerIP(t, alphaContainer)
	betaIP := containerIP(t, betaContainer)
	for _, ip := range []string{alphaIP, betaIP} {
		require.True(t, subnet.Contains(netip.MustParseAddr(ip)), "%s is not an address of the subnetwork the jobs egress into", ip)
	}

	_, err = dnsSvc.ResourceRecordSets.Create(project, "xjob-zone", &dns.ResourceRecordSet{
		Name:    "beta.xjob.local.",
		Type:    "A",
		Ttl:     60,
		Rrdatas: []string{betaIP},
	}).Do()
	require.NoError(t, err)

	// alpha resolves beta's record through its network's resolver once the
	// record exists.
	var logs []byte
	require.Eventually(t, func() bool {
		var err error
		logs, err = exec.Command("docker", "logs", alphaContainer).CombinedOutput()
		return err == nil && strings.Contains(string(logs), "gcp-cross-job-dns-ok")
	}, 30*time.Second, 500*time.Millisecond, "alpha should resolve beta.xjob.local through the private zone: %s", logs)
	assert.Contains(t, string(logs), "gcp-cross-job-dns-ok")
}

// jobContainerName is the engine name of the main container of an execution's
// first task's first attempt, as startCloudRunJobContainers names it.
func jobContainerName(executionName string) string {
	last := executionName
	if idx := strings.LastIndex(executionName, "/"); idx >= 0 {
		last = executionName[idx+1:]
	}
	if len(last) > 12 {
		last = last[:12]
	}
	return "sockerless-sim-gcp-job-" + last + "-0-0"
}

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
