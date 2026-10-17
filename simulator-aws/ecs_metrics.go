package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobyclient "github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon ECS sends its service metrics to CloudWatch in 1-minute periods: it
// collects several data points each minute and aggregates them into the
// minute's one datapoint, so a minute's SampleCount counts its samples. A
// service's CPUUtilization is the CPU units its tasks use over the CPU units
// reserved for them, and MemoryUtilization the memory they use over the memory
// reserved for them, both with the ClusterName and ServiceName dimensions.

const ecsMetricNamespace = "AWS/ECS"

// ecsMetricSampleInterval is the gap between two readings of a service's tasks
// within a minute.
const ecsMetricSampleInterval = 15 * time.Second

// ecsServiceMetricKey is one service's metric in one minute.
type ecsServiceMetricKey struct {
	cluster, service, metric string
	minute                   int64
}

// ecsCPUReading is a container's cumulative CPU time at the moment the engine
// read it, from which the next reading's usage rate follows.
type ecsCPUReading struct {
	totalNanos uint64
	at         time.Time
}

var ecsServiceMetrics = struct {
	sync.Mutex
	samples map[ecsServiceMetricKey][]float64
	cpu     map[string]ecsCPUReading
}{samples: map[ecsServiceMetricKey][]float64{}, cpu: map[string]ecsCPUReading{}}

func startECSServiceMetricsPublisher(srv *sim.Server) {
	srv.StartBackground("ECS service metrics publisher", ecsPublishServiceMetrics)
}

func ecsPublishServiceMetrics(ctx context.Context) {
	ticker := time.NewTicker(ecsMetricSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			ecsSampleServiceMetrics(ctx, now)
			ecsFlushServiceMetrics(now.Unix() / 60)
		}
	}
}

// ecsServiceUsage sums one service's use and reservation across its running
// tasks in one sample.
type ecsServiceUsage struct {
	cpuUsed, cpuReserved float64
	memUsed, memReserved float64
	cpuSampled           bool
	memSampled           bool
}

// ecsSampleServiceMetrics reads every running service task's containers from
// the container engine and records each service's utilization for the minute.
func ecsSampleServiceMetrics(ctx context.Context, now time.Time) {
	cli := sim.DockerClient()
	if cli == nil || ecsTasks == nil {
		return
	}
	usage := map[[2]string]*ecsServiceUsage{}
	seen := map[string]bool{}
	for _, task := range ecsTasks.Filter(func(t ECSTask) bool {
		return t.LastStatus == "RUNNING" && strings.HasPrefix(t.Group, "service:")
	}) {
		v, ok := ecsProcessHandles.Load(task.TaskID())
		if !ok {
			continue
		}
		procs, ok := v.(*ecsTaskProcesses)
		if !ok {
			continue
		}
		key := [2]string{ecsClusterNameFromRef(task.ClusterArn), strings.TrimPrefix(task.Group, "service:")}
		u := usage[key]
		if u == nil {
			u = &ecsServiceUsage{}
			usage[key] = u
		}
		cpuReserved, memReserved := ecsTaskReservation(task)
		var cpuUsed, memUsed float64
		cpuKnown, memKnown := true, true
		for _, handle := range procs.Handles {
			if handle == nil || handle.ID() == "" {
				continue
			}
			seen[handle.ID()] = true
			stats, err := ecsReadContainerStats(ctx, cli, handle.ID())
			if err != nil {
				cpuKnown, memKnown = false, false
				continue
			}
			if units, ok := ecsContainerCPUUnits(handle.ID(), stats); ok {
				cpuUsed += units
			} else {
				cpuKnown = false
			}
			memUsed += ecsContainerMemoryMiB(stats)
		}
		if cpuKnown && cpuReserved > 0 {
			u.cpuUsed += cpuUsed
			u.cpuReserved += cpuReserved
			u.cpuSampled = true
		}
		if memKnown && memReserved > 0 {
			u.memUsed += memUsed
			u.memReserved += memReserved
			u.memSampled = true
		}
	}
	minute := now.Unix() / 60
	ecsServiceMetrics.Lock()
	defer ecsServiceMetrics.Unlock()
	for id := range ecsServiceMetrics.cpu {
		if !seen[id] {
			delete(ecsServiceMetrics.cpu, id)
		}
	}
	for key, u := range usage {
		if u.cpuSampled {
			k := ecsServiceMetricKey{key[0], key[1], "CPUUtilization", minute}
			ecsServiceMetrics.samples[k] = append(ecsServiceMetrics.samples[k], 100*u.cpuUsed/u.cpuReserved)
		}
		if u.memSampled {
			k := ecsServiceMetricKey{key[0], key[1], "MemoryUtilization", minute}
			ecsServiceMetrics.samples[k] = append(ecsServiceMetrics.samples[k], 100*u.memUsed/u.memReserved)
		}
	}
}

// ecsTaskReservation is the CPU units and MiB a task reserves: its task size
// when it has one, else the sum of its containers' cpu and memoryReservation
// (or memory, when a container sets no soft limit).
func ecsTaskReservation(task ECSTask) (cpu, mem float64) {
	cpu, _ = strconv.ParseFloat(task.Cpu, 64)
	mem, _ = strconv.ParseFloat(task.Memory, 64)
	if cpu > 0 && mem > 0 {
		return cpu, mem
	}
	var cpuSum, memSum float64
	for _, c := range task.Containers {
		v, _ := strconv.ParseFloat(c.Cpu, 64)
		cpuSum += v
		reserve := c.MemoryReservation
		if reserve == "" {
			reserve = c.Memory
		}
		m, _ := strconv.ParseFloat(reserve, 64)
		memSum += m
	}
	if cpu <= 0 {
		cpu = cpuSum
	}
	if mem <= 0 {
		mem = memSum
	}
	return cpu, mem
}

func ecsReadContainerStats(ctx context.Context, cli *mobyclient.Client, containerID string) (mobycontainer.StatsResponse, error) {
	var stats mobycontainer.StatsResponse
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := cli.ContainerStats(readCtx, containerID, mobyclient.ContainerStatsOptions{})
	if err != nil {
		return stats, err
	}
	defer func() { _ = res.Body.Close() }()
	err = json.NewDecoder(res.Body).Decode(&stats)
	return stats, err
}

// ecsContainerCPUUnits is the CPU a container used since its previous reading,
// in CPU units (1,024 to a vCPU); the first reading of a container only sets
// the baseline.
func ecsContainerCPUUnits(containerID string, stats mobycontainer.StatsResponse) (float64, bool) {
	reading := ecsCPUReading{totalNanos: stats.CPUStats.CPUUsage.TotalUsage, at: stats.Read}
	ecsServiceMetrics.Lock()
	previous, ok := ecsServiceMetrics.cpu[containerID]
	ecsServiceMetrics.cpu[containerID] = reading
	ecsServiceMetrics.Unlock()
	elapsed := reading.at.Sub(previous.at)
	if !ok || elapsed <= 0 || reading.totalNanos < previous.totalNanos {
		return 0, false
	}
	cores := float64(reading.totalNanos-previous.totalNanos) / float64(elapsed.Nanoseconds())
	return cores * 1024, true
}

// ecsContainerMemoryMiB is the memory a container has in use: the engine's
// usage less the inactive file cache the kernel can reclaim, as `docker stats`
// counts it.
func ecsContainerMemoryMiB(stats mobycontainer.StatsResponse) float64 {
	used := stats.MemoryStats.Usage
	inactive := stats.MemoryStats.Stats["inactive_file"]
	if inactive == 0 {
		inactive = stats.MemoryStats.Stats["total_inactive_file"]
	}
	if inactive < used {
		used -= inactive
	}
	return float64(used) / (1024 * 1024)
}

// ecsFlushServiceMetrics reports every minute before current to CloudWatch:
// a minute's samples arrive together once it has closed.
func ecsFlushServiceMetrics(current int64) {
	ecsServiceMetrics.Lock()
	closed := map[ecsServiceMetricKey][]float64{}
	for key, values := range ecsServiceMetrics.samples {
		if key.minute < current {
			closed[key] = values
			delete(ecsServiceMetrics.samples, key)
		}
	}
	ecsServiceMetrics.Unlock()
	for key, values := range closed {
		dims := []CWDimension{{Name: "ClusterName", Value: key.cluster}, {Name: "ServiceName", Value: key.service}}
		for _, value := range values {
			cwStoreDatum(CWMetricDatum{
				Namespace:  ecsMetricNamespace,
				MetricName: key.metric,
				Dimensions: dims,
				Value:      value,
				Unit:       "Percent",
				Timestamp:  float64(key.minute * 60),
			})
		}
	}
}
