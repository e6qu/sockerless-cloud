package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

const elbv2MetricNamespace = "AWS/ApplicationELB"

// elbv2MetricKey names one datapoint the publisher reports: a metric, the
// dimensions it carries (an empty value leaves that dimension off) and the
// minute it counts.
type elbv2MetricKey struct {
	metric       string
	loadBalancer string
	targetGroup  string
	minute       int64
}

// elbv2RequestMetrics holds what the data plane routed and the publisher has
// not yet reported, so a request adds to a counter instead of writing to
// CloudWatch.
var elbv2RequestMetrics = struct {
	sync.Mutex
	pending map[elbv2MetricKey]float64
}{pending: map[elbv2MetricKey]float64{}}

// elbv2RecordRoutedRequest counts the request an Application Load Balancer
// routed to a target of tg: one RequestCount for the load balancer and for the
// target group behind it, and the request's share of RequestCountPerTarget
// across the targets tg has in service. Each dimension value is "the final
// portion" of the resource's ARN, the form a ResourceLabel joins.
func elbv2RecordRoutedRequest(lbArn string, tg ELBv2TargetGroup, at time.Time) {
	inService := 0
	for _, target := range tg.Targets {
		if elbv2TargetReceivesTraffic(tg, target) {
			inService++
		}
	}
	perTarget := 1.0
	if inService > 1 {
		perTarget = 1 / float64(inService)
	}
	loadBalancer := lbArn[strings.Index(lbArn, ":loadbalancer/")+len(":loadbalancer/"):]
	targetGroup := tg.Arn[strings.LastIndex(tg.Arn, ":")+1:]
	minute := at.Unix() / 60
	elbv2RequestMetrics.Lock()
	elbv2RequestMetrics.pending[elbv2MetricKey{"RequestCount", loadBalancer, "", minute}]++
	elbv2RequestMetrics.pending[elbv2MetricKey{"RequestCount", loadBalancer, targetGroup, minute}]++
	elbv2RequestMetrics.pending[elbv2MetricKey{"RequestCountPerTarget", "", targetGroup, minute}] += perTarget
	elbv2RequestMetrics.pending[elbv2MetricKey{"RequestCountPerTarget", loadBalancer, targetGroup, minute}] += perTarget
	elbv2RequestMetrics.Unlock()
}

func startELBv2RequestMetricsPublisher(srv *sim.Server) {
	srv.StartBackground("ELB request metrics publisher", elbv2PublishRequestMetrics)
}

// elbv2PublishRequestMetrics reports each minute as it closes: Elastic Load
// Balancing measures and sends its metrics in 60-second intervals, and only for
// intervals in which requests flowed.
func elbv2PublishRequestMetrics(ctx context.Context) {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			elbv2FlushRequestMetrics(next.Unix() / 60)
		}
	}
}

// elbv2FlushRequestMetrics reports the requests counted in every minute before
// current to CloudWatch, one datapoint per metric, dimension set and minute.
func elbv2FlushRequestMetrics(current int64) {
	elbv2RequestMetrics.Lock()
	batch := map[elbv2MetricKey]float64{}
	for key, value := range elbv2RequestMetrics.pending {
		if key.minute < current {
			batch[key] = value
			delete(elbv2RequestMetrics.pending, key)
		}
	}
	elbv2RequestMetrics.Unlock()
	for key, value := range batch {
		var dims []CWDimension
		if key.loadBalancer != "" {
			dims = append(dims, CWDimension{Name: "LoadBalancer", Value: key.loadBalancer})
		}
		if key.targetGroup != "" {
			dims = append(dims, CWDimension{Name: "TargetGroup", Value: key.targetGroup})
		}
		cwStoreDatum(CWMetricDatum{
			Namespace:  elbv2MetricNamespace,
			MetricName: key.metric,
			Dimensions: dims,
			Value:      value,
			Unit:       "Count",
			Timestamp:  float64(key.minute * 60),
		})
	}
}
