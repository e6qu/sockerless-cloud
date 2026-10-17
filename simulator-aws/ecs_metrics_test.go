package main

import (
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A task with a task size reserves that size; one without reserves its
// containers' cpu and memoryReservation, or memory where no soft limit is set.
func TestECSTaskReservationPrefersTheTaskSize(t *testing.T) {
	cpu, mem := ecsTaskReservation(ECSTask{Cpu: "512", Memory: "1024"})
	if cpu != 512 || mem != 1024 {
		t.Fatalf("task size reserves %v CPU units and %v MiB, want 512 and 1024", cpu, mem)
	}
	cpu, mem = ecsTaskReservation(ECSTask{Containers: []ECSTaskContainer{
		{Cpu: "128", Memory: "512", MemoryReservation: "256"},
		{Cpu: "64", Memory: "128"},
	}})
	if cpu != 192 || mem != 384 {
		t.Fatalf("containers reserve %v CPU units and %v MiB, want 192 and 256+128", cpu, mem)
	}
}

// A container's CPU units are the CPU time it used between two readings over
// the time between them, 1,024 to a core; the first reading sets the baseline.
func TestECSContainerCPUUnitsFromTwoReadings(t *testing.T) {
	ecsServiceMetrics.cpu = map[string]ecsCPUReading{}
	start := time.Now()
	first := mobycontainer.StatsResponse{Read: start}
	first.CPUStats.CPUUsage.TotalUsage = 1_000_000_000
	if _, ok := ecsContainerCPUUnits("c1", first); ok {
		t.Fatal("the first reading reports a rate, want only a baseline")
	}
	second := mobycontainer.StatsResponse{Read: start.Add(10 * time.Second)}
	second.CPUStats.CPUUsage.TotalUsage = 6_000_000_000
	units, ok := ecsContainerCPUUnits("c1", second)
	if !ok || units != 512 {
		t.Fatalf("5 s of CPU over 10 s reads %v CPU units (%v), want half a core, 512", units, ok)
	}
}

// A minute's samples reach CloudWatch together once the minute has closed,
// with the service's ClusterName and ServiceName.
func TestECSServiceMetricsReportClosedMinutes(t *testing.T) {
	cwMetrics = sim.MakeStore[[]CWMetricDatum](nil, "cw_metrics")
	cwMetricStreams = sim.MakeStore[CWMetricStream](nil, "cw_metric_streams")
	ecsServiceMetrics.samples = map[ecsServiceMetricKey][]float64{
		{"prod", "web", "CPUUtilization", 100}: {40, 60},
		{"prod", "web", "CPUUtilization", 101}: {80},
	}
	ecsFlushServiceMetrics(101)
	key := metricsKey(ecsMetricNamespace, "CPUUtilization",
		[]CWDimension{{Name: "ClusterName", Value: "prod"}, {Name: "ServiceName", Value: "web"}})
	reported, _ := cwMetrics.Get(key)
	if len(reported) != 2 || reported[0].Timestamp != 6000 || reported[0].Unit != "Percent" {
		t.Fatalf("closed minute reports %+v, want its two samples at 6000", reported)
	}
	if len(ecsServiceMetrics.samples) != 1 {
		t.Fatalf("the open minute's samples went out early: %v left", ecsServiceMetrics.samples)
	}
}
