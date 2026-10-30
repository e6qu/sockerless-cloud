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

// A 2 GiB host holds two t3.micro (1 GiB each); a running or pending instance
// takes its share and a stopped one gives it back. vCPUs overcommit.
func TestEC2HostHoldsAnInstanceByItsMemory(t *testing.T) {
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	previousInstances := ec2Instances
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	previousCeiling := ecsPlacementCeiling
	ecsPlacementCeiling = func() ecsTaskSize { return ecsTaskSize{MemoryMiB: 2048, CPUUnits: 1024} }
	t.Cleanup(func() {
		ecsPlacementCeiling = previousCeiling
		ec2Instances = previousInstances
	})

	assert.True(t, ec2HostHoldsInstanceType("t3.micro", ""), "2 vCPUs on a 1-vCPU host still launch")
	ec2Instances.Put("i-one", EC2Instance{InstanceId: "i-one", InstanceType: "t3.micro", State: "running"})
	assert.True(t, ec2HostHoldsInstanceType("t3.micro", ""))
	ec2Instances.Put("i-two", EC2Instance{InstanceId: "i-two", InstanceType: "t3.micro", State: "pending"})
	assert.False(t, ec2HostHoldsInstanceType("t3.micro", ""), "the host's memory is taken")
	assert.True(t, ec2HostHoldsInstanceType("t3.micro", "i-two"), "an instance being started does not count against itself")
	ec2Instances.Put("i-two", EC2Instance{InstanceId: "i-two", InstanceType: "t3.micro", State: "stopped"})
	assert.True(t, ec2HostHoldsInstanceType("t3.micro", ""), "a stopped instance gives its memory back")
	assert.False(t, ec2HostHoldsInstanceType("u7in-32tb.224xlarge", ""))
}

// On an 8-vCPU, 16 GiB host t3.micro holds four: the score reaches 10 at three
// times the target and falls linearly below it, never under 1.
func TestEC2SpotPlacementScoresReadTheHostsFreeCapacity(t *testing.T) {
	ecsTasks = sim.MakeStore[ECSTask](nil, "ecs_tasks")
	previousInstances := ec2Instances
	ec2Instances = sim.MakeStore[EC2Instance](nil, "ec2_instances")
	previousCeiling := ecsPlacementCeiling
	ecsPlacementCeiling = func() ecsTaskSize { return ecsTaskSize{MemoryMiB: 16384, CPUUnits: 8192} }
	t.Cleanup(func() {
		ecsPlacementCeiling = previousCeiling
		ec2Instances = previousInstances
	})
	score := func(target int, unit string, extra url.Values) supportabilitySpotScores {
		form := url.Values{"TargetCapacity": {strconv.Itoa(target)}, "InstanceType.1": {"t3.micro"}}
		if unit != "" {
			form.Set("TargetCapacityUnitType", unit)
		}
		for k, v := range extra {
			form[k] = v
		}
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		handleGetSpotPlacementScores(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out supportabilitySpotScores
		require.NoError(t, xml.Unmarshal(rec.Body.Bytes(), &out))
		return out
	}
	assert.Equal(t, 10, score(1, "", nil).Scores[0].Score)
	assert.Equal(t, 7, score(2, "units", nil).Scores[0].Score)
	assert.Equal(t, 3, score(4, "units", nil).Scores[0].Score)
	assert.Equal(t, 1, score(100, "units", nil).Scores[0].Score)
	assert.Equal(t, 7, score(4, "vcpu", nil).Scores[0].Score, "four t3.micro hold 8 vCPUs")

	other := score(1, "", url.Values{"RegionName.1": {awsRegion()}, "RegionName.2": {"ap-southeast-7"}})
	require.Len(t, other.Scores, 2)
	assert.Equal(t, 10, other.Scores[0].Score)
	assert.Equal(t, 1, other.Scores[1].Score, "the simulator holds no capacity in a Region it does not serve")

	zones := score(1, "", url.Values{"SingleAvailabilityZone": {"true"}})
	require.Len(t, zones.Scores, len(awsAvailabilityZones()))
	assert.Equal(t, ec2AvailabilityZoneId(awsAvailabilityZones()[0]), zones.Scores[0].AvailabilityZoneID)
}

type supportabilitySpotScores struct {
	Scores []struct {
		Region             string `xml:"region"`
		AvailabilityZoneID string `xml:"availabilityZoneId"`
		Score              int    `xml:"score"`
	} `xml:"spotPlacementScoreSet>item"`
}
