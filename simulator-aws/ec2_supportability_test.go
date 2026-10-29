package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type supportabilityResponse struct {
	Scores []struct {
		AvailabilityZoneID string `xml:"availabilityZoneId"`
		InstanceType       string `xml:"instanceType"`
		Score              string `xml:"score"`
	} `xml:"capacityManagerSupportabilityScoreSet>item"`
	Aggregate string `xml:"aggregateScore"`
}

func supportabilityScores(t *testing.T, amount int, unit string) supportabilityResponse {
	t.Helper()
	form := url.Values{
		"CapacityAmount":               {strconv.Itoa(amount)},
		"CapacityUnit":                 {unit},
		"MarketType":                   {"on-demand"},
		"CapacityAvailabilityZoneId.1": {ec2AvailabilityZoneId(awsAvailabilityZones()[0])},
		"InstanceType.1":               {"t3.micro"},
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handleGetCapacityManagerSupportabilityScores(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out supportabilityResponse
	require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Scores, 1)
	return out
}

// An 8-vCPU, 16 GiB host holds four t3.micro (2 vCPUs, 1 GiB each), and a
// running one leaves room for three.
func TestEC2SupportabilityScoresReadTheHostsFreeCapacity(t *testing.T) {
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	previousInstances := ec2Instances
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	previousCeiling := ecsPlacementCeiling
	ecsPlacementCeiling = func() ecsTaskSize { return ecsTaskSize{MemoryMiB: 16384, CPUUnits: 8192} }
	t.Cleanup(func() {
		ecsPlacementCeiling = previousCeiling
		ec2Instances = previousInstances
	})

	assert.Equal(t, "high", supportabilityScores(t, 1, "instances").Scores[0].Score)
	assert.Equal(t, "medium", supportabilityScores(t, 2, "instances").Scores[0].Score)
	assert.Equal(t, "medium", supportabilityScores(t, 4, "instances").Scores[0].Score)
	assert.Equal(t, "low", supportabilityScores(t, 5, "instances").Scores[0].Score)
	assert.Equal(t, "medium", supportabilityScores(t, 8, "vcpus").Scores[0].Score)

	ec2Instances.Put("i-running", EC2Instance{InstanceId: "i-running", InstanceType: "t3.micro", State: "running"})
	ec2Instances.Put("i-stopped", EC2Instance{InstanceId: "i-stopped", InstanceType: "t3.micro", State: "stopped"})
	got := supportabilityScores(t, 4, "instances")
	assert.Equal(t, "low", got.Scores[0].Score, "a running instance holds its share of the host")
	assert.Equal(t, "low", got.Aggregate)
	assert.Equal(t, "high", supportabilityScores(t, 1, "instances").Scores[0].Score)
}
