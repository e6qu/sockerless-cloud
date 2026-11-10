package aws_cli_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_PerformanceInsights drives the pi commands against a PostgreSQL DB
// instance whose real engine runs a session the sampler sees.
func TestRDSCLI_PerformanceInsights(t *testing.T) {
	t.Parallel()
	const id = "cli-pi-postgresql"
	var created struct {
		DBInstance struct {
			DbiResourceId                      string
			PerformanceInsightsEnabled         bool
			PerformanceInsightsRetentionPeriod int
			Endpoint                           struct {
				Address string
				Port    int
			}
		}
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "create-db-instance", "--db-instance-identifier", id, "--db-instance-class", "db.t3.micro",
		"--engine", "postgres", "--allocated-storage", "20", "--master-username", "dbadmin", "--master-user-password", "MasterPassword-123!",
		"--db-name", "application", "--enable-performance-insights", "--performance-insights-retention-period", "31", "--output", "json")), &created)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("rds", "delete-db-instance", "--db-instance-identifier", id, "--skip-final-snapshot"))
	})
	assert.True(t, created.DBInstance.PerformanceInsightsEnabled)
	assert.Equal(t, 31, created.DBInstance.PerformanceInsightsRetentionPeriod)
	resource := created.DBInstance.DbiResourceId
	runCLI(t, awsCLI("rds", "wait", "db-instance-available", "--db-instance-identifier", id))

	testContext, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://dbadmin@%s:%d/application?sslmode=require", created.DBInstance.Endpoint.Address, created.DBInstance.Endpoint.Port))
	require.NoError(t, err)
	config.Password = "MasterPassword-123!"
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	connection, err := pgx.ConnectConfig(testContext, config)
	require.NoError(t, err)
	defer func() { _ = connection.Close(context.Background()) }()

	since := time.Now().Add(-time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := connection.Exec(testContext, "SELECT pg_sleep(6) /* cli-pi */")
		done <- err
	}()
	type keysOutput struct {
		Keys []struct {
			Dimensions map[string]string
			Total      float64
		}
	}
	describe := func() keysOutput {
		var out keysOutput
		parseJSON(t, runCLI(t, awsCLI("pi", "describe-dimension-keys", "--service-type", "RDS", "--identifier", resource,
			"--start-time", strconv.FormatInt(since.Unix(), 10), "--end-time", strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10),
			"--metric", "db.load.avg", "--period-in-seconds", "1", "--group-by", "Group=db.sql", "--output", "json")), &out)
		return out
	}
	var keys keysOutput
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for keys = describe(); len(keys.Keys) == 0; keys = describe() {
		select {
		case <-testContext.Done():
			t.Fatal("Performance Insights sampled no load")
		case <-ticker.C:
		}
	}
	require.NoError(t, <-done)
	assert.Equal(t, "SELECT pg_sleep(6) /* cli-pi */", keys.Keys[0].Dimensions["db.sql.statement"])
	until := time.Now().Add(time.Second)
	window := []string{"--start-time", strconv.FormatInt(since.Unix(), 10), "--end-time", strconv.FormatInt(until.Unix(), 10)}

	var metrics struct {
		Identifier string
		MetricList []struct {
			Key        struct{ Metric string }
			DataPoints []struct{ Value float64 }
		}
	}
	parseJSON(t, runCLI(t, awsCLI(append(append([]string{"pi", "get-resource-metrics", "--service-type", "RDS", "--identifier", resource}, window...),
		"--period-in-seconds", "60", "--metric-queries", `[{"Metric":"db.load.max"}]`, "--output", "json")...)), &metrics)
	assert.Equal(t, resource, metrics.Identifier)
	require.Len(t, metrics.MetricList, 1)
	require.NotEmpty(t, metrics.MetricList[0].DataPoints)

	var details struct {
		Dimensions []struct{ Value, Status string }
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "get-dimension-key-details", "--service-type", "RDS", "--identifier", resource, "--group", "db.sql",
		"--group-identifier", keys.Keys[0].Dimensions["db.sql.id"], "--output", "json")), &details)
	require.Len(t, details.Dimensions, 1)
	assert.Equal(t, "AVAILABLE", details.Dimensions[0].Status)

	var dims struct {
		MetricDimensions []struct{ Metric string }
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "list-available-resource-dimensions", "--service-type", "RDS", "--identifier", resource,
		"--metrics", "db.load", "--output", "json")), &dims)
	require.Len(t, dims.MetricDimensions, 1)
	var available struct {
		Metrics []struct{ Metric, Unit string }
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "list-available-resource-metrics", "--service-type", "RDS", "--identifier", resource,
		"--metric-types", "os", "--output", "json")), &available)
	assert.NotEmpty(t, available.Metrics)
	var metadata struct{ Identifier string }
	parseJSON(t, runCLI(t, awsCLI("pi", "get-resource-metadata", "--service-type", "RDS", "--identifier", resource, "--output", "json")), &metadata)
	assert.Equal(t, resource, metadata.Identifier)

	var report struct{ AnalysisReportId string }
	parseJSON(t, runCLI(t, awsCLI(append(append([]string{"pi", "create-performance-analysis-report", "--service-type", "RDS", "--identifier", resource}, window[:2]...),
		"--output", "json")...)), &report)
	var got struct {
		AnalysisReport struct {
			Status   string
			Insights []struct{ InsightType string }
		}
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "get-performance-analysis-report", "--service-type", "RDS", "--identifier", resource,
		"--analysis-report-id", report.AnalysisReportId, "--output", "json")), &got)
	assert.Equal(t, "SUCCEEDED", got.AnalysisReport.Status)
	require.NotEmpty(t, got.AnalysisReport.Insights)
	var recs struct {
		Recommendations []struct{ RecommendationDescription string }
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "list-performance-analysis-report-recommendations", "--service-type", "RDS", "--identifier", resource,
		"--analysis-report-id", report.AnalysisReportId, "--output", "json")), &recs)
	assert.NotEmpty(t, recs.Recommendations)
	var reports struct {
		AnalysisReports []struct{ AnalysisReportId string }
	}
	parseJSON(t, runCLI(t, awsCLI("pi", "list-performance-analysis-reports", "--service-type", "RDS", "--identifier", resource, "--output", "json")), &reports)
	require.Len(t, reports.AnalysisReports, 1)
	arn := fmt.Sprintf("arn:aws:pi:us-east-1:123456789012:perf-reports/RDS/%s/%s", resource, report.AnalysisReportId)
	runCLI(t, awsCLI("pi", "tag-resource", "--service-type", "RDS", "--resource-arn", arn, "--tags", "Key=name,Value=test-tag"))
	var tags struct{ Tags []struct{ Key, Value string } }
	parseJSON(t, runCLI(t, awsCLI("pi", "list-tags-for-resource", "--service-type", "RDS", "--resource-arn", arn, "--output", "json")), &tags)
	require.Len(t, tags.Tags, 1)
	runCLI(t, awsCLI("pi", "untag-resource", "--service-type", "RDS", "--resource-arn", arn, "--tag-keys", "name"))
	runCLI(t, awsCLI("pi", "delete-performance-analysis-report", "--service-type", "RDS", "--identifier", resource,
		"--analysis-report-id", report.AnalysisReportId))
	out := runCLIExpectError(t, awsCLI("pi", "get-performance-analysis-report", "--service-type", "RDS", "--identifier", resource,
		"--analysis-report-id", report.AnalysisReportId))
	assert.Contains(t, out, "InvalidArgumentException")

	var math struct {
		MetricDataResults []struct{ Values []float64 }
	}
	queries := fmt.Sprintf(`[{"Id":"load","Period":60,"Expression":"DB_PERF_INSIGHTS('RDS', '%s', 'db.load.max')"}]`, resource)
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", queries,
		"--start-time", since.Truncate(time.Minute).Format(time.RFC3339), "--end-time", until.Truncate(time.Minute).Add(time.Minute).Format(time.RFC3339),
		"--output", "json")), &math)
	require.Len(t, math.MetricDataResults, 1)
	require.NotEmpty(t, math.MetricDataResults[0].Values)
	assert.GreaterOrEqual(t, math.MetricDataResults[0].Values[0], 1.0)

	var modified struct {
		DBInstance struct{ PerformanceInsightsEnabled bool }
	}
	parseJSON(t, runCLI(t, awsCLI("rds", "modify-db-instance", "--db-instance-identifier", id, "--no-enable-performance-insights", "--output", "json")), &modified)
	assert.False(t, modified.DBInstance.PerformanceInsightsEnabled)
}
