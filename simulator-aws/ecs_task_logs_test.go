package main

import (
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// awslogsTaskDefinition is a one-container task definition wired to the
// awslogs driver, the configuration every long-running Amazon ECS service in
// the simulator uses to make its workload observable.
func awslogsTaskDefinition(logGroup, streamPrefix string) ECSTaskDefinition {
	return ECSTaskDefinition{
		ContainerDefinitions: []ECSContainerDefinition{{
			Name: "app",
			LogConfiguration: &ECSLogConfiguration{
				LogDriver: "awslogs",
				Options: map[string]string{
					"awslogs-group":         logGroup,
					"awslogs-stream-prefix": streamPrefix,
				},
			},
		}},
	}
}

func resetCloudWatchLogStoresForTest() {
	cwLogGroups = sim.MakeStore[CWLogGroup](nil, "cw_log_groups")
	cwLogStreams = sim.MakeStore[CWLogStream](nil, "cw_log_streams")
	cwLogEvents = sim.MakeStore[[]CWLogEvent](nil, "cw_log_events")
	cwSequences = sim.MakeStore[int64](nil, "cw_sequences")
	cwMetricFilters = sim.MakeStore[CWMetricFilter](nil, "cw_metric_filters")
	cwMetrics = sim.MakeStore[[]CWMetricDatum](nil, "cw_metrics")
	cwMetricStreams = sim.MakeStore[CWMetricStream](nil, "cw_metric_streams")
}

// TestECSTaskLogSinkAdvancesLogStreamIngestionState proves a container's
// stdout is a first-class CloudWatch ingestion. A service task never exits, so
// DescribeLogStreams is the only way to tell an actively writing task from a
// silent one; when the sink appended events without touching the stream
// record, every running service reported the instant its stream was created
// and ordering streams by LastEventTime ranked live tasks as stale.
func TestECSTaskLogSinkAdvancesLogStreamIngestionState(t *testing.T) {
	resetCloudWatchLogStoresForTest()

	const (
		logGroup = "/edd-dev/control-plane"
		taskID   = "0af1c2d3e4f5"
	)
	sink := ecsTaskCloudWatchSink(awslogsTaskDefinition(logGroup, "app"), taskID)
	key := cwEventsKey(logGroup, "app/app/"+taskID)
	created, ok := cwLogStreams.Get(key)
	if !ok {
		t.Fatal("awslogs configuration did not create the task's log stream")
	}

	sink.WriteLog(sim.LogLine{Stream: "stdout", Text: "listening on :3000"})
	sink.WriteLog(sim.LogLine{Stream: "stderr", Text: "OAuthCallbackError"})

	events, _ := cwLogEvents.Get(key)
	if len(events) != 3 {
		t.Fatalf("log stream holds %d events, want the container marker plus both workload lines", len(events))
	}
	last := events[len(events)-1]
	if last.Message != "OAuthCallbackError" {
		t.Fatalf("last event message = %q, want the workload's stderr line", last.Message)
	}

	stream, ok := cwLogStreams.Get(key)
	if !ok {
		t.Fatal("log stream disappeared after the workload wrote to it")
	}
	if stream.LastEventTimestamp != last.Timestamp {
		t.Errorf("stream LastEventTimestamp = %d, want the last ingested event's timestamp %d",
			stream.LastEventTimestamp, last.Timestamp)
	}
	if stream.LastIngestionTime != last.IngestionTime {
		t.Errorf("stream LastIngestionTime = %d, want the last ingestion time %d",
			stream.LastIngestionTime, last.IngestionTime)
	}
	if stream.UploadSequenceToken == created.UploadSequenceToken {
		t.Errorf("stream UploadSequenceToken = %q, want a token issued by the workload's ingestion",
			stream.UploadSequenceToken)
	}
}

// TestECSTaskLogSinkFiresLogGroupMetricFilters proves a metric filter on the
// task's log group matches the container's own output. Alarming on a running
// service's error lines is the reason the group is monitored at all, and a
// filter that only sees PutLogEvents callers never fires for a workload whose
// lines arrive through the awslogs driver.
func TestECSTaskLogSinkFiresLogGroupMetricFilters(t *testing.T) {
	resetCloudWatchLogStoresForTest()

	const (
		logGroup = "/edd-dev/control-plane"
		taskID   = "9bc8a7d6e5f4"
	)
	cwMetricFilters.Put("control-plane-errors", CWMetricFilter{
		FilterName:    "control-plane-errors",
		FilterPattern: "OAuthCallbackError",
		LogGroupName:  logGroup,
		MetricTransformations: []CWMetricTransformation{{
			MetricName:      "ControlPlaneErrors",
			MetricNamespace: "EDD/Dev",
			MetricValue:     "1",
		}},
	})

	sink := ecsTaskCloudWatchSink(awslogsTaskDefinition(logGroup, "app"), taskID)
	sink.WriteLog(sim.LogLine{Stream: "stderr", Text: "OAuthCallbackError: state cookie missing"})

	data, ok := cwMetrics.Get(metricsKey("EDD/Dev", "ControlPlaneErrors", nil))
	if !ok || len(data) != 1 {
		t.Fatalf("metric filter published %d datapoints for the container's stderr line, want 1", len(data))
	}
	if data[0].Value != 1 {
		t.Errorf("metric datum value = %v, want 1", data[0].Value)
	}
}
