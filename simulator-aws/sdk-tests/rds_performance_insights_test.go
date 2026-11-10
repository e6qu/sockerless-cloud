package aws_sdk_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/pi"
	pitypes "github.com/aws/aws-sdk-go-v2/service/pi/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func piClient() *pi.Client {
	return pi.NewFromConfig(sdkConfig(), func(o *pi.Options) { o.BaseEndpoint = aws.String(baseURL) })
}

// piCreateInstance creates a DB instance with Performance Insights on.
func piCreateInstance(t *testing.T, testContext context.Context, id, engine string) rds.CreateDBInstanceOutput {
	t.Helper()
	rdsAPI := rdsClient()
	created, err := rdsAPI.CreateDBInstance(testContext, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:      aws.String(id),
		DBInstanceClass:           aws.String("db.t3.micro"),
		Engine:                    aws.String(engine),
		AllocatedStorage:          aws.Int32(20),
		MasterUsername:            aws.String("dbadmin"),
		MasterUserPassword:        aws.String("MasterPassword-123!"),
		DBName:                    aws.String("application"),
		EnablePerformanceInsights: aws.Bool(true),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = rdsAPI.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(id), SkipFinalSnapshot: aws.Bool(true),
		})
	})
	waitForRDSInstanceAvailable(t, rdsAPI, testContext, id)
	return *created
}

// piWaitForLoad reads db.load.avg grouped by group once a second, the cadence
// Performance Insights samples at, until a sample shows dimension at value.
func piWaitForLoad(t *testing.T, testContext context.Context, resource string, since time.Time, group, dimension, value string) {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		out, err := piClient().GetResourceMetrics(testContext, &pi.GetResourceMetricsInput{
			ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource),
			StartTime: aws.Time(since), EndTime: aws.Time(time.Now().Add(time.Second)), PeriodInSeconds: aws.Int32(1),
			MetricQueries: []pitypes.MetricQuery{{Metric: aws.String("db.load.avg"), GroupBy: &pitypes.DimensionGroup{Group: aws.String(group)}}},
		})
		require.NoError(t, err)
		for _, series := range out.MetricList {
			if series.Key.Dimensions[dimension] == value && len(series.DataPoints) > 0 {
				return
			}
		}
		select {
		case <-testContext.Done():
			t.Fatalf("no db.load sample with %s = %s", dimension, value)
		case <-ticker.C:
		}
	}
}

// TestPerformanceInsights_PostgreSQL_SDK samples a real PostgreSQL engine's
// pg_stat_activity: a session sleeping in pg_sleep is DB load waiting on
// Timeout:PgSleep, its statement the top SQL.
func TestPerformanceInsights_PostgreSQL_SDK(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	const id = "pi-postgresql"
	created := piCreateInstance(t, testContext, id, "postgres")
	instance := created.DBInstance
	assert.True(t, aws.ToBool(instance.PerformanceInsightsEnabled))
	assert.Equal(t, int32(7), aws.ToInt32(instance.PerformanceInsightsRetentionPeriod))
	assert.Contains(t, aws.ToString(instance.PerformanceInsightsKMSKeyId), ":kms:")
	resource := aws.ToString(instance.DbiResourceId)

	endpoint := fmt.Sprintf("%s:%d", aws.ToString(instance.Endpoint.Address), aws.ToInt32(instance.Endpoint.Port))
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://dbadmin@%s/application?sslmode=require", endpoint))
	require.NoError(t, err)
	config.Password = "MasterPassword-123!"
	config.TLSConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // test-only CA coordinate
	connection, err := pgx.ConnectConfig(testContext, config)
	require.NoError(t, err)
	defer func() { _ = connection.Close(context.Background()) }()

	since := time.Now().Add(-time.Second)
	sleeping := make(chan error, 1)
	go func() {
		_, err := connection.Exec(testContext, "SELECT pg_sleep(8) /* pi-probe */")
		sleeping <- err
	}()
	piWaitForLoad(t, testContext, resource, since, "db.wait_event", "db.wait_event.name", "Timeout:PgSleep")
	require.NoError(t, <-sleeping)
	until := time.Now().Add(time.Second)

	c := piClient()
	keys, err := c.DescribeDimensionKeys(testContext, &pi.DescribeDimensionKeysInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), Metric: aws.String("db.load.avg"),
		StartTime: aws.Time(since), EndTime: aws.Time(until), PeriodInSeconds: aws.Int32(1),
		GroupBy:     &pitypes.DimensionGroup{Group: aws.String("db.sql")},
		PartitionBy: &pitypes.DimensionGroup{Group: aws.String("db.wait_event_type")},
	})
	require.NoError(t, err)
	require.NotEmpty(t, keys.Keys)
	top := keys.Keys[0]
	assert.Equal(t, "SELECT pg_sleep(8) /* pi-probe */", top.Dimensions["db.sql.statement"])
	assert.Greater(t, aws.ToFloat64(top.Total), 0.0)
	require.NotEmpty(t, keys.PartitionKeys)
	assert.Len(t, top.Partitions, len(keys.PartitionKeys))

	details, err := c.GetDimensionKeyDetails(testContext, &pi.GetDimensionKeyDetailsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), Group: aws.String("db.sql"),
		GroupIdentifier: aws.String(top.Dimensions["db.sql.id"]), RequestedDimensions: []string{"db.sql.statement"},
	})
	require.NoError(t, err)
	require.Len(t, details.Dimensions, 1)
	assert.Equal(t, pitypes.DetailStatusAvailable, details.Dimensions[0].Status)
	assert.Equal(t, "SELECT pg_sleep(8) /* pi-probe */", aws.ToString(details.Dimensions[0].Value))

	byUser, err := c.GetResourceMetrics(testContext, &pi.GetResourceMetricsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), StartTime: aws.Time(since), EndTime: aws.Time(until),
		PeriodInSeconds: aws.Int32(60), PeriodAlignment: pitypes.PeriodAlignmentStartTime,
		MetricQueries: []pitypes.MetricQuery{{Metric: aws.String("db.load.max"), Filter: map[string]string{"db.user.name": "dbadmin"}}},
	})
	require.NoError(t, err)
	require.Len(t, byUser.MetricList, 1)
	require.NotEmpty(t, byUser.MetricList[0].DataPoints)
	assert.GreaterOrEqual(t, aws.ToFloat64(byUser.MetricList[0].DataPoints[0].Value), 1.0)

	dims, err := c.ListAvailableResourceDimensions(testContext, &pi.ListAvailableResourceDimensionsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), Metrics: []string{"db.load"},
	})
	require.NoError(t, err)
	require.Len(t, dims.MetricDimensions, 1)
	var groups []string
	for _, g := range dims.MetricDimensions[0].Groups {
		groups = append(groups, aws.ToString(g.Group))
	}
	assert.Contains(t, groups, "db.application")
	assert.Contains(t, groups, "db.sql_tokenized")

	metrics, err := c.ListAvailableResourceMetrics(testContext, &pi.ListAvailableResourceMetricsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), MetricTypes: []string{"db"}, MaxResults: aws.Int32(25),
	})
	require.NoError(t, err)
	require.NotEmpty(t, metrics.Metrics)
	assert.True(t, strings.HasPrefix(aws.ToString(metrics.Metrics[0].Metric), "db."))
	assert.NotNil(t, metrics.NextToken, "more db counters than one page")

	metadata, err := c.GetResourceMetadata(testContext, &pi.GetResourceMetadataInput{ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource)})
	require.NoError(t, err)
	assert.Equal(t, resource, aws.ToString(metadata.Identifier))

	report, err := c.CreatePerformanceAnalysisReport(testContext, &pi.CreatePerformanceAnalysisReportInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), StartTime: aws.Time(since), EndTime: aws.Time(until.Add(-time.Second)),
		Tags: []pitypes.Tag{{Key: aws.String("purpose"), Value: aws.String("test")}},
	})
	require.NoError(t, err)
	reportID := aws.ToString(report.AnalysisReportId)
	assert.Regexp(t, `^report-[0-9a-f]{17}$`, reportID)
	got, err := c.GetPerformanceAnalysisReport(testContext, &pi.GetPerformanceAnalysisReportInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), AnalysisReportId: aws.String(reportID),
	})
	require.NoError(t, err)
	assert.Equal(t, pitypes.AnalysisStatusSucceeded, got.AnalysisReport.Status)
	require.Len(t, got.AnalysisReport.Insights, 1)
	insight := got.AnalysisReport.Insights[0]
	assert.Equal(t, "HighDBLoad", aws.ToString(insight.InsightType))
	require.NotEmpty(t, insight.SupportingInsights)
	assert.Equal(t, "DominatingSQLs", aws.ToString(insight.SupportingInsights[0].InsightType))
	recs, err := c.ListPerformanceAnalysisReportRecommendations(testContext, &pi.ListPerformanceAnalysisReportRecommendationsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), AnalysisReportId: aws.String(reportID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, recs.Recommendations)
	assert.Contains(t, aws.ToString(recs.Recommendations[0].RecommendationDescription), "Investigate the following SQLs")

	arn := fmt.Sprintf("arn:aws:pi:us-east-1:123456789012:perf-reports/RDS/%s/%s", resource, reportID)
	_, err = c.TagResource(testContext, &pi.TagResourceInput{ServiceType: pitypes.ServiceTypeRds, ResourceARN: aws.String(arn),
		Tags: []pitypes.Tag{{Key: aws.String("owner"), Value: aws.String("dba")}}})
	require.NoError(t, err)
	_, err = c.UntagResource(testContext, &pi.UntagResourceInput{ServiceType: pitypes.ServiceTypeRds, ResourceARN: aws.String(arn), TagKeys: []string{"purpose"}})
	require.NoError(t, err)
	tags, err := c.ListTagsForResource(testContext, &pi.ListTagsForResourceInput{ServiceType: pitypes.ServiceTypeRds, ResourceARN: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "owner", aws.ToString(tags.Tags[0].Key))
	listed, err := c.ListPerformanceAnalysisReports(testContext, &pi.ListPerformanceAnalysisReportsInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), ListTags: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, listed.AnalysisReports, 1)
	assert.Len(t, listed.AnalysisReports[0].Tags, 1)
	_, err = c.DeletePerformanceAnalysisReport(testContext, &pi.DeletePerformanceAnalysisReportInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), AnalysisReportId: aws.String(reportID),
	})
	require.NoError(t, err)
	_, err = c.GetPerformanceAnalysisReport(testContext, &pi.GetPerformanceAnalysisReportInput{
		ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource), AnalysisReportId: aws.String(reportID),
	})
	var invalid *pitypes.InvalidArgumentException
	require.ErrorAs(t, err, &invalid)

	math, err := cloudwatchClient().GetMetricData(testContext, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(since.Truncate(time.Minute)), EndTime: aws.Time(until.Truncate(time.Minute).Add(time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{Id: aws.String("load"), Period: aws.Int32(60),
			Expression: aws.String(fmt.Sprintf("DB_PERF_INSIGHTS('RDS', '%s', 'db.load.max')", resource))}},
	})
	require.NoError(t, err)
	require.Len(t, math.MetricDataResults, 1)
	require.NotEmpty(t, math.MetricDataResults[0].Values)
	assert.GreaterOrEqual(t, math.MetricDataResults[0].Values[0], 1.0)

	modified, err := rdsClient().ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), PerformanceInsightsRetentionPeriod: aws.Int32(93),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(93), aws.ToInt32(modified.DBInstance.PerformanceInsightsRetentionPeriod))
	_, err = rdsClient().ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), PerformanceInsightsRetentionPeriod: aws.Int32(40),
	})
	require.Error(t, err, "a retention is 7, a multiple of 31 up to 713, or 731 days")
	disabled, err := rdsClient().ModifyDBInstance(testContext, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), EnablePerformanceInsights: aws.Bool(false),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(disabled.DBInstance.PerformanceInsightsEnabled))
	_, err = c.GetResourceMetadata(testContext, &pi.GetResourceMetadataInput{ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(resource)})
	require.ErrorAs(t, err, &invalid, "Performance Insights serves only an instance with it on")
}

// TestPerformanceInsights_MySQL_SDK samples MySQL's Performance Schema,
// which Performance Insights manages.
func TestPerformanceInsights_MySQL_SDK(t *testing.T) {
	t.Parallel()
	testContext, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	const id = "pi-mysql"
	created := piCreateInstance(t, testContext, id, "mysql")
	instance := created.DBInstance
	resource := aws.ToString(instance.DbiResourceId)
	endpoint := fmt.Sprintf("%s:%d", aws.ToString(instance.Endpoint.Address), aws.ToInt32(instance.Endpoint.Port))
	config := mysql.Config{User: "dbadmin", Passwd: "MasterPassword-123!", Net: "tcp", Addr: endpoint, DBName: "application",
		TLSConfig: "skip-verify", AllowCleartextPasswords: true}
	db, err := sql.Open("mysql", config.FormatDSN())
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.PingContext(testContext))

	since := time.Now().Add(-time.Second)
	sleeping := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(testContext, "SELECT SLEEP(8)")
		sleeping <- err
	}()
	piWaitForLoad(t, testContext, resource, since, "db.sql_tokenized", "db.sql_tokenized.statement", "SELECT `SLEEP` (?)")
	require.NoError(t, <-sleeping)

	metadata, err := piClient().GetResourceMetadata(testContext, &pi.GetResourceMetadataInput{ServiceType: pitypes.ServiceTypeRds, Identifier: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, resource, aws.ToString(metadata.Identifier), "an instance identifier names the instance too")
	require.Contains(t, metadata.Features, "performance_schema")
	assert.Equal(t, pitypes.FeatureStatusEnabled, metadata.Features["performance_schema"].Status)
}
