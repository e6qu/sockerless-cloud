package gcp_sdk_test

import (
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"google.golang.org/api/option"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
	"google.golang.org/grpc"
)

func newLoggingWriteClient(t *testing.T) (*logging.Client, error) {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, simGRPCDialOptions(simTokenSource())...)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { conn.Close() })

	client, err := logging.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
	if err != nil {
		return nil, err
	}
	return client, nil
}

func writeEntry(text string) logging.Entry {
	return logging.Entry{
		Payload: text,
	}
}

func writeEntryAt(text string, timestamp time.Time) logging.Entry {
	return logging.Entry{
		Payload:   text,
		Timestamp: timestamp,
	}
}

func writeEntryWithResource(resourceType, labelValue, text string) logging.Entry {
	labelKey := "job_name"
	if resourceType == "cloud_run_revision" {
		labelKey = "service_name"
	}
	return logging.Entry{
		Payload: text,
		Resource: &mrpb.MonitoredResource{
			Type: resourceType,
			Labels: map[string]string{
				labelKey: labelValue,
			},
		},
	}
}
