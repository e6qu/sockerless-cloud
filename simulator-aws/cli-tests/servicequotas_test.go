package aws_cli_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliServiceQuota struct {
	ServiceCode string  `json:"ServiceCode"`
	ServiceName string  `json:"ServiceName"`
	QuotaCode   string  `json:"QuotaCode"`
	QuotaName   string  `json:"QuotaName"`
	QuotaArn    string  `json:"QuotaArn"`
	Value       float64 `json:"Value"`
	Unit        string  `json:"Unit"`
	Adjustable  bool    `json:"Adjustable"`
	UsageMetric *struct {
		MetricNamespace  string            `json:"MetricNamespace"`
		MetricName       string            `json:"MetricName"`
		MetricDimensions map[string]string `json:"MetricDimensions"`
	} `json:"UsageMetric"`
}

// TestServiceQuotasCLI_CatalogAndIncrease lists services and quotas, and
// raises a quota through a request Service Quotas approves.
func TestServiceQuotasCLI_CatalogAndIncrease(t *testing.T) {
	t.Parallel()
	var services struct {
		Services []struct{ ServiceCode, ServiceName string }
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-services", "--output", "json")), &services)
	codes := map[string]string{}
	for _, s := range services.Services {
		codes[s.ServiceCode] = s.ServiceName
	}
	assert.Equal(t, "AWS Lambda", codes["lambda"])
	assert.Equal(t, "Amazon ECR", codes["ecr"])

	var def struct{ Quota cliServiceQuota }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-aws-default-service-quota", "--service-code", "ecr", "--quota-code", "L-CFEB8E8D", "--output", "json")), &def)
	assert.Equal(t, "Registered repositories", def.Quota.QuotaName)
	assert.Equal(t, 100000.0, def.Quota.Value)
	require.NotNil(t, def.Quota.UsageMetric)
	assert.Equal(t, "ResourceCount", def.Quota.UsageMetric.MetricName)
	assert.Equal(t, "RepositoryCount", def.Quota.UsageMetric.MetricDimensions["Resource"])

	var defaults struct{ Quotas []cliServiceQuota }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-aws-default-service-quotas", "--service-code", "iam", "--output", "json")), &defaults)
	assert.Len(t, defaults.Quotas, 9)

	var applied struct{ Quotas []cliServiceQuota }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-service-quotas", "--service-code", "ecr", "--quota-code", "L-03A36CE1", "--output", "json")), &applied)
	require.Len(t, applied.Quotas, 1)
	assert.Equal(t, "Images per repository", applied.Quotas[0].QuotaName)

	var current struct{ Quota cliServiceQuota }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-service-quota", "--service-code", "ecr", "--quota-code", "L-03A36CE1", "--output", "json")), &current)
	desired := current.Quota.Value + 1000
	var requested struct {
		RequestedQuota struct {
			Id, Status   string
			DesiredValue float64
		}
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "request-service-quota-increase", "--service-code", "ecr", "--quota-code", "L-03A36CE1",
		"--desired-value", strconv.FormatFloat(desired, 'f', -1, 64), "--output", "json")), &requested)
	assert.Equal(t, "PENDING", requested.RequestedQuota.Status)
	var change struct {
		RequestedQuota struct{ Id, Status string }
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-requested-service-quota-change", "--request-id", requested.RequestedQuota.Id, "--output", "json")), &change)
	assert.Equal(t, "APPROVED", change.RequestedQuota.Status)
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-service-quota", "--service-code", "ecr", "--quota-code", "L-03A36CE1", "--output", "json")), &current)
	assert.Equal(t, desired, current.Quota.Value)

	var history struct {
		RequestedQuotas []struct{ Id string }
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-requested-service-quota-change-history", "--service-code", "ecr", "--output", "json")), &history)
	assert.NotEmpty(t, history.RequestedQuotas)
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-requested-service-quota-change-history-by-quota", "--service-code", "ecr",
		"--quota-code", "L-03A36CE1", "--output", "json")), &history)
	assert.NotEmpty(t, history.RequestedQuotas)

	out := runCLIExpectError(t, awsCLI("service-quotas", "create-support-case", "--request-id", requested.RequestedQuota.Id))
	assert.Contains(t, out, "InvalidResourceStateException")
	out = runCLIExpectError(t, awsCLI("service-quotas", "get-service-quota", "--service-code", "ecr", "--quota-code", "L-00000000"))
	assert.Contains(t, out, "NoSuchResourceException")

	arn := current.Quota.QuotaArn
	runCLI(t, awsCLI("service-quotas", "tag-resource", "--resource-arn", arn, "--tags", "Key=team,Value=registry"))
	var tags struct{ Tags []struct{ Key, Value string } }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-tags-for-resource", "--resource-arn", arn, "--output", "json")), &tags)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "registry", tags.Tags[0].Value)
	runCLI(t, awsCLI("service-quotas", "untag-resource", "--resource-arn", arn, "--tag-keys", "team"))
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-tags-for-resource", "--resource-arn", arn, "--output", "json")), &tags)
	assert.Empty(t, tags.Tags)
}

// TestServiceQuotasCLI_AutoManagementAndReport runs Automatic Management's
// configuration and a quota utilization report.
func TestServiceQuotasCLI_AutoManagementAndReport(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("service-quotas", "start-auto-management", "--opt-in-level", "ACCOUNT", "--opt-in-type", "NotifyOnly"))
	var config struct{ OptInStatus, OptInType string }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-auto-management-configuration", "--output", "json")), &config)
	assert.Equal(t, "ENABLED", config.OptInStatus)
	runCLI(t, awsCLI("service-quotas", "update-auto-management", "--exclusion-list", `{"ecr":["L-CFEB8E8D"]}`))
	runCLI(t, awsCLI("service-quotas", "stop-auto-management"))
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-auto-management-configuration", "--output", "json")), &config)
	assert.Equal(t, "DISABLED", config.OptInStatus)

	var started struct{ ReportId, Status string }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "start-quota-utilization-report", "--output", "json")), &started)
	assert.Equal(t, "PENDING", started.Status)
	var report struct {
		Status     string
		TotalCount int
		Quotas     []struct{ QuotaCode, Namespace string }
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-quota-utilization-report", "--report-id", started.ReportId, "--output", "json")), &report)
	assert.Equal(t, "COMPLETED", report.Status)
	assert.Equal(t, report.TotalCount, len(report.Quotas))
	for _, q := range report.Quotas {
		assert.Equal(t, "AWS/Usage", q.Namespace)
	}
}

// TestServiceQuotasCLI_Template keeps a quota request template for the
// organization.
func TestServiceQuotasCLI_Template(t *testing.T) {
	t.Parallel()
	runCLI(t, awsCLI("organizations", "enable-aws-service-access", "--service-principal", "servicequotas.amazonaws.com"))
	runCLI(t, awsCLI("service-quotas", "put-service-quota-increase-request-into-template", "--service-code", "ecr", "--quota-code", "L-CFEB8E8D",
		"--aws-region", "eu-west-1", "--desired-value", "200000"))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("service-quotas", "delete-service-quota-increase-request-from-template", "--service-code", "ecr",
			"--quota-code", "L-CFEB8E8D", "--aws-region", "eu-west-1"))
	})
	var got struct {
		ServiceQuotaIncreaseRequestInTemplate struct {
			DesiredValue float64
			AwsRegion    string
		}
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-service-quota-increase-request-from-template", "--service-code", "ecr",
		"--quota-code", "L-CFEB8E8D", "--aws-region", "eu-west-1", "--output", "json")), &got)
	assert.Equal(t, 200000.0, got.ServiceQuotaIncreaseRequestInTemplate.DesiredValue)
	var listed struct {
		ServiceQuotaIncreaseRequestInTemplateList []struct{ QuotaCode string }
	}
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "list-service-quota-increase-requests-in-template", "--aws-region", "eu-west-1", "--output", "json")), &listed)
	require.NotEmpty(t, listed.ServiceQuotaIncreaseRequestInTemplateList)

	runCLI(t, awsCLI("service-quotas", "associate-service-quota-template"))
	var association struct{ ServiceQuotaTemplateAssociationStatus string }
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-association-for-service-quota-template", "--output", "json")), &association)
	assert.Equal(t, "ASSOCIATED", association.ServiceQuotaTemplateAssociationStatus)
	runCLI(t, awsCLI("service-quotas", "disassociate-service-quota-template"))
	parseJSON(t, runCLI(t, awsCLI("service-quotas", "get-association-for-service-quota-template", "--output", "json")), &association)
	assert.Equal(t, "DISASSOCIATED", association.ServiceQuotaTemplateAssociationStatus)
	runCLI(t, awsCLI("service-quotas", "delete-service-quota-increase-request-from-template", "--service-code", "ecr",
		"--quota-code", "L-CFEB8E8D", "--aws-region", "eu-west-1"))
	out := runCLIExpectError(t, awsCLI("service-quotas", "get-service-quota-increase-request-from-template", "--service-code", "ecr",
		"--quota-code", "L-CFEB8E8D", "--aws-region", "eu-west-1"))
	assert.Contains(t, out, "NoSuchResourceException")
}

// TestServiceQuotasCLI_UsageMetrics reads AWS/Usage CallCount for the
// CloudWatch calls the CLI made, against the quota SERVICE_QUOTA names.
func TestServiceQuotasCLI_UsageMetrics(t *testing.T) {
	t.Parallel()
	start := time.Now().UTC().Truncate(time.Minute)
	runCLI(t, awsCLI("cloudwatch", "list-metrics", "--namespace", "AWS/Usage", "--output", "json"))
	queries := `[{"Id":"calls","MetricStat":{"Metric":{"Namespace":"AWS/Usage","MetricName":"CallCount","Dimensions":[` +
		`{"Name":"Service","Value":"CloudWatch"},{"Name":"Type","Value":"API"},{"Name":"Resource","Value":"ListMetrics"},{"Name":"Class","Value":"None"}]},` +
		`"Period":60,"Stat":"Sum"}},{"Id":"quota","Expression":"SERVICE_QUOTA(calls)"}]`
	var out struct {
		MetricDataResults []struct {
			Id     string
			Values []float64
		}
	}
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", queries,
		"--start-time", start.Add(-time.Minute).Format(time.RFC3339), "--end-time", start.Add(2*time.Minute).Format(time.RFC3339), "--output", "json")), &out)
	values := map[string][]float64{}
	for _, r := range out.MetricDataResults {
		values[r.Id] = r.Values
	}
	require.NotEmpty(t, values["calls"])
	require.NotEmpty(t, values["quota"])
	assert.Equal(t, 25.0, values["quota"][0])
}

// TestCloudWatchCLI_GetMetricDataPaging pages GetMetricData by
// --max-datapoints with a dynamic label.
func TestCloudWatchCLI_GetMetricDataPaging(t *testing.T) {
	t.Parallel()
	ns := "Custom/CLIPaging" + strings.ReplaceAll(t.Name(), "/", "")
	base := time.Now().UTC().Truncate(time.Minute).Add(-6 * time.Minute)
	for i, v := range []string{"3", "8", "1"} {
		runCLI(t, awsCLI("cloudwatch", "put-metric-data", "--namespace", ns, "--metric-name", "Depth", "--value", v,
			"--timestamp", base.Add(time.Duration(i)*time.Minute).Format(time.RFC3339)))
	}
	queries := `[{"Id":"d","Label":"max ${MAX} sum ${SUM}","MetricStat":{"Metric":{"Namespace":"` + ns + `","MetricName":"Depth"},"Period":60,"Stat":"Maximum"}}]`
	var pages struct {
		MetricDataResults []struct {
			Label      string
			StatusCode string
			Values     []float64
		}
	}
	parseJSON(t, runCLI(t, awsCLI("cloudwatch", "get-metric-data", "--metric-data-queries", queries, "--page-size", "2",
		"--scan-by", "TimestampAscending", "--start-time", base.Format(time.RFC3339), "--end-time", base.Add(3*time.Minute).Format(time.RFC3339),
		"--output", "json")), &pages)
	require.Len(t, pages.MetricDataResults, 2, "the CLI follows NextToken across two pages of two data points")
	first, second := pages.MetricDataResults[0], pages.MetricDataResults[1]
	assert.Equal(t, []float64{3, 8}, first.Values)
	assert.Equal(t, "PartialData", first.StatusCode)
	assert.Equal(t, "max 8 sum 12", first.Label)
	assert.Equal(t, []float64{1}, second.Values)
	assert.Equal(t, "Complete", second.StatusCode)
}
