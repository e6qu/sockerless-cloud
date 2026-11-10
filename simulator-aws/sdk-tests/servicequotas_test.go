package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serviceQuotasClient() *servicequotas.Client {
	return servicequotas.NewFromConfig(sdkConfig(), func(o *servicequotas.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestServiceQuotas_CatalogSDK reads the quotas the vendored General
// Reference pages publish, by service and code, with their defaults.
func TestServiceQuotas_CatalogSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()

	services := map[string]string{}
	pages := servicequotas.NewListServicesPaginator(c, &servicequotas.ListServicesInput{MaxResults: aws.Int32(10)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Services), 10)
		for _, s := range page.Services {
			services[aws.ToString(s.ServiceCode)] = aws.ToString(s.ServiceName)
		}
	}
	assert.Equal(t, "AWS Lambda", services["lambda"])
	assert.Equal(t, "Amazon Relational Database Service", services["rds"])
	assert.Equal(t, "AWS Fargate", services["fargate"])

	def, err := c.GetAWSDefaultServiceQuota(ctx, &servicequotas.GetAWSDefaultServiceQuotaInput{
		ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Concurrent executions", aws.ToString(def.Quota.QuotaName))
	assert.Equal(t, 1000.0, aws.ToFloat64(def.Quota.Value))
	assert.Equal(t, "Count", aws.ToString(def.Quota.Unit))
	assert.True(t, def.Quota.Adjustable)
	assert.Equal(t, "arn:aws:servicequotas:us-east-1:123456789012:lambda/L-B99A9384", aws.ToString(def.Quota.QuotaArn))

	rate, err := c.GetAWSDefaultServiceQuota(ctx, &servicequotas.GetAWSDefaultServiceQuotaInput{
		ServiceCode: aws.String("monitoring"), QuotaCode: aws.String("L-5E141212"),
	})
	require.NoError(t, err)
	assert.Equal(t, "Rate of GetMetricData requests", aws.ToString(rate.Quota.QuotaName))
	require.NotNil(t, rate.Quota.Period)
	assert.Equal(t, sqtypes.PeriodUnitSecond, rate.Quota.Period.PeriodUnit)
	require.NotNil(t, rate.Quota.UsageMetric)
	assert.Equal(t, "AWS/Usage", aws.ToString(rate.Quota.UsageMetric.MetricNamespace))
	assert.Equal(t, "CallCount", aws.ToString(rate.Quota.UsageMetric.MetricName))
	assert.Equal(t, map[string]string{"Class": "None", "Resource": "GetMetricData", "Service": "CloudWatch", "Type": "API"},
		rate.Quota.UsageMetric.MetricDimensions)

	vcpu, err := c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String("ec2"), QuotaCode: aws.String("L-1216C47A"),
	})
	require.NoError(t, err)
	assert.Equal(t, 5.0, aws.ToFloat64(vcpu.Quota.Value))
	assert.Equal(t, sqtypes.AppliedLevelEnumAccount, vcpu.Quota.QuotaAppliedAtLevel)
	require.NotNil(t, vcpu.Quota.UsageMetric)
	assert.Equal(t, "Maximum", aws.ToString(vcpu.Quota.UsageMetric.MetricStatisticRecommendation))

	var codes []string
	quotas := servicequotas.NewListAWSDefaultServiceQuotasPaginator(c, &servicequotas.ListAWSDefaultServiceQuotasInput{
		ServiceCode: aws.String("rds"), MaxResults: aws.Int32(5),
	})
	for quotas.HasMorePages() {
		page, err := quotas.NextPage(ctx)
		require.NoError(t, err)
		for _, q := range page.Quotas {
			codes = append(codes, aws.ToString(q.QuotaCode))
		}
	}
	assert.Contains(t, codes, "L-7B6409FD")
	assert.Len(t, codes, 19)

	listed, err := c.ListServiceQuotas(ctx, &servicequotas.ListServiceQuotasInput{
		ServiceCode: aws.String("rds"), QuotaCode: aws.String("L-7B6409FD"),
	})
	require.NoError(t, err)
	require.Len(t, listed.Quotas, 1)
	assert.Equal(t, 40.0, aws.ToFloat64(listed.Quotas[0].Value))

	_, err = c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-00000000")})
	var missing *sqtypes.NoSuchResourceException
	require.ErrorAs(t, err, &missing)
	_, err = c.ListServiceQuotas(ctx, &servicequotas.ListServiceQuotasInput{ServiceCode: aws.String("no-such-service")})
	require.ErrorAs(t, err, &missing)
}

// TestServiceQuotas_IncreaseAppliesSDK raises AWS Lambda's Concurrent
// executions quota: the request is approved, the applied value grows, and
// AWS Lambda's account settings and throttling read the new value.
func TestServiceQuotas_IncreaseAppliesSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()
	before, err := c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384")})
	require.NoError(t, err)
	desired := aws.ToFloat64(before.Quota.Value) + 100

	requested, err := c.RequestServiceQuotaIncrease(ctx, &servicequotas.RequestServiceQuotaIncreaseInput{
		ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384"), DesiredValue: aws.Float64(desired),
	})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.RequestStatusPending, requested.RequestedQuota.Status)
	id := aws.ToString(requested.RequestedQuota.Id)

	change, err := c.GetRequestedServiceQuotaChange(ctx, &servicequotas.GetRequestedServiceQuotaChangeInput{RequestId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.RequestStatusApproved, change.RequestedQuota.Status)
	assert.Equal(t, desired, aws.ToFloat64(change.RequestedQuota.DesiredValue))

	applied, err := c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384")})
	require.NoError(t, err)
	assert.Equal(t, desired, aws.ToFloat64(applied.Quota.Value))
	def, err := c.GetAWSDefaultServiceQuota(ctx, &servicequotas.GetAWSDefaultServiceQuotaInput{ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384")})
	require.NoError(t, err)
	assert.Equal(t, 1000.0, aws.ToFloat64(def.Quota.Value), "the default does not reflect increases")

	settings, err := lambdaClient().GetAccountSettings(ctx, &lambda.GetAccountSettingsInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, settings.AccountLimit.ConcurrentExecutions, int32(desired), "AWS Lambda enforces the applied quota")

	_, err = c.RequestServiceQuotaIncrease(ctx, &servicequotas.RequestServiceQuotaIncreaseInput{
		ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384"), DesiredValue: aws.Float64(1),
	})
	var illegal *sqtypes.IllegalArgumentException
	require.ErrorAs(t, err, &illegal, "a request must exceed the applied value")

	_, err = c.CreateSupportCase(ctx, &servicequotas.CreateSupportCaseInput{RequestId: aws.String(id)})
	var state *sqtypes.InvalidResourceStateException
	require.ErrorAs(t, err, &state, "an approved request takes no support case")
	_, err = c.CreateSupportCase(ctx, &servicequotas.CreateSupportCaseInput{RequestId: aws.String("0000000000000000")})
	var missing *sqtypes.NoSuchResourceException
	require.ErrorAs(t, err, &missing)

	history, err := c.ListRequestedServiceQuotaChangeHistory(ctx, &servicequotas.ListRequestedServiceQuotaChangeHistoryInput{
		ServiceCode: aws.String("lambda"), Status: sqtypes.RequestStatusApproved,
	})
	require.NoError(t, err)
	assert.True(t, containsRequest(history.RequestedQuotas, id))
	byQuota, err := c.ListRequestedServiceQuotaChangeHistoryByQuota(ctx, &servicequotas.ListRequestedServiceQuotaChangeHistoryByQuotaInput{
		ServiceCode: aws.String("lambda"), QuotaCode: aws.String("L-B99A9384"),
	})
	require.NoError(t, err)
	assert.True(t, containsRequest(byQuota.RequestedQuotas, id))
}

func containsRequest(requests []sqtypes.RequestedServiceQuotaChange, id string) bool {
	for _, r := range requests {
		if aws.ToString(r.Id) == id {
			return true
		}
	}
	return false
}

// TestServiceQuotas_TagsSDK tags an applied quota by its ARN.
func TestServiceQuotas_TagsSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()
	arn := "arn:aws:servicequotas:us-east-1:123456789012:ecr/L-CFEB8E8D"
	_, err := c.TagResource(ctx, &servicequotas.TagResourceInput{ResourceARN: aws.String(arn),
		Tags: []sqtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}, {Key: aws.String("env"), Value: aws.String("test")}}})
	require.NoError(t, err)
	_, err = c.UntagResource(ctx, &servicequotas.UntagResourceInput{ResourceARN: aws.String(arn), TagKeys: []string{"env"}})
	require.NoError(t, err)
	tags, err := c.ListTagsForResource(ctx, &servicequotas.ListTagsForResourceInput{ResourceARN: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "team", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "platform", aws.ToString(tags.Tags[0].Value))

	_, err = c.ListTagsForResource(ctx, &servicequotas.ListTagsForResourceInput{ResourceARN: aws.String("arn:aws:servicequotas:us-east-1:123456789012:ecr/L-00000000")})
	var missing *sqtypes.NoSuchResourceException
	require.ErrorAs(t, err, &missing)
}

// TestServiceQuotas_AutoManagementSDK starts, reads, updates and stops
// Automatic Management, which offers only the NotifyOnly opt-in type.
func TestServiceQuotas_AutoManagementSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()
	_, err := c.StartAutoManagement(ctx, &servicequotas.StartAutoManagementInput{
		OptInLevel: sqtypes.OptInLevelAccount, OptInType: sqtypes.OptInTypeNotifyAndAdjust,
	})
	var illegal *sqtypes.IllegalArgumentException
	require.ErrorAs(t, err, &illegal)

	_, err = c.StartAutoManagement(ctx, &servicequotas.StartAutoManagementInput{
		OptInLevel: sqtypes.OptInLevelAccount, OptInType: sqtypes.OptInTypeNotifyOnly,
		ExclusionList: map[string][]string{"lambda": {"L-B99A9384"}},
	})
	require.NoError(t, err)
	config, err := c.GetAutoManagementConfiguration(ctx, &servicequotas.GetAutoManagementConfigurationInput{})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.OptInStatusEnabled, config.OptInStatus)
	assert.Equal(t, sqtypes.OptInTypeNotifyOnly, config.OptInType)
	require.Len(t, config.ExclusionList["lambda"], 1)
	assert.Equal(t, "Concurrent executions", aws.ToString(config.ExclusionList["lambda"][0].QuotaName))

	_, err = c.UpdateAutoManagement(ctx, &servicequotas.UpdateAutoManagementInput{ExclusionList: map[string][]string{}})
	require.NoError(t, err)
	config, err = c.GetAutoManagementConfiguration(ctx, &servicequotas.GetAutoManagementConfigurationInput{})
	require.NoError(t, err)
	assert.Empty(t, config.ExclusionList)

	_, err = c.StopAutoManagement(ctx, &servicequotas.StopAutoManagementInput{})
	require.NoError(t, err)
	config, err = c.GetAutoManagementConfiguration(ctx, &servicequotas.GetAutoManagementConfigurationInput{})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.OptInStatusDisabled, config.OptInStatus)
	_, err = c.UpdateAutoManagement(ctx, &servicequotas.UpdateAutoManagementInput{OptInType: sqtypes.OptInTypeNotifyOnly})
	var missing *sqtypes.NoSuchResourceException
	require.ErrorAs(t, err, &missing)
}

// TestServiceQuotas_UsageMetricsSDK reads the AWS/Usage metrics the
// simulator's services publish and compares them with their quotas through
// SERVICE_QUOTA and a quota utilization report.
func TestServiceQuotas_UsageMetricsSDK(t *testing.T) {
	t.Parallel()
	cw := cloudwatchClient()
	start := time.Now().UTC().Truncate(time.Minute)
	for range 3 {
		_, err := cw.ListMetrics(ctx, &cloudwatch.ListMetricsInput{Namespace: aws.String("AWS/Usage")})
		require.NoError(t, err)
	}
	repo := uniquename.New("usage-repo")
	_, err := ecrClient().CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: aws.String(repo)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecrClient().DeleteRepository(ctx, &ecr.DeleteRepositoryInput{RepositoryName: aws.String(repo), Force: true})
	})

	usage := func(id, name string, dims map[string]string, stat string) cwtypes.MetricDataQuery {
		var dimensions []cwtypes.Dimension
		for k, v := range dims {
			dimensions = append(dimensions, cwtypes.Dimension{Name: aws.String(k), Value: aws.String(v)})
		}
		return cwtypes.MetricDataQuery{Id: aws.String(id), MetricStat: &cwtypes.MetricStat{
			Metric: &cwtypes.Metric{Namespace: aws.String("AWS/Usage"), MetricName: aws.String(name), Dimensions: dimensions},
			Period: aws.Int32(60), Stat: aws.String(stat),
		}}
	}
	out, err := cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start.Add(-time.Minute)), EndTime: aws.Time(start.Add(2 * time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{
			usage("calls", "CallCount", map[string]string{"Service": "CloudWatch", "Type": "API", "Resource": "ListMetrics", "Class": "None"}, "Sum"),
			{Id: aws.String("callquota"), Expression: aws.String("SERVICE_QUOTA(calls)")},
			usage("repos", "ResourceCount", map[string]string{"Service": "ECR", "Type": "RESOURCE", "Resource": "RepositoryCount"}, "Maximum"),
			{Id: aws.String("repoquota"), Expression: aws.String("SERVICE_QUOTA(repos)")},
		},
	})
	require.NoError(t, err)
	results := map[string]cwtypes.MetricDataResult{}
	for _, r := range out.MetricDataResults {
		results[aws.ToString(r.Id)] = r
	}
	require.NotEmpty(t, results["calls"].Values)
	total := 0.0
	for _, v := range results["calls"].Values {
		total += v
	}
	assert.GreaterOrEqual(t, total, 3.0)
	require.NotEmpty(t, results["callquota"].Values)
	assert.Equal(t, 25.0, results["callquota"].Values[0], "Rate of ListMetrics requests")
	require.NotEmpty(t, results["repos"].Values)
	assert.GreaterOrEqual(t, results["repos"].Values[0], 1.0)
	require.NotEmpty(t, results["repoquota"].Values)
	assert.Equal(t, 100000.0, results["repoquota"].Values[0], "Registered repositories")

	_, err = cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start.Add(-time.Minute)), EndTime: aws.Time(start.Add(2 * time.Minute)),
		MetricDataQueries: []cwtypes.MetricDataQuery{
			{Id: aws.String("m1"), MetricStat: &cwtypes.MetricStat{Metric: &cwtypes.Metric{Namespace: aws.String("Custom/NotUsage"), MetricName: aws.String("X")},
				Period: aws.Int32(60), Stat: aws.String("Sum")}},
			{Id: aws.String("q"), Expression: aws.String("SERVICE_QUOTA(m1)")},
		},
	})
	require.Error(t, err, "SERVICE_QUOTA takes only a usage metric")

	c := serviceQuotasClient()
	report, err := c.StartQuotaUtilizationReport(ctx, &servicequotas.StartQuotaUtilizationReportInput{})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.ReportStatusPending, report.Status)
	got, err := c.GetQuotaUtilizationReport(ctx, &servicequotas.GetQuotaUtilizationReportInput{ReportId: report.ReportId, MaxResults: aws.Int32(1000)})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.ReportStatusCompleted, got.Status)
	found := false
	for i, q := range got.Quotas {
		if i > 0 {
			assert.GreaterOrEqual(t, aws.ToFloat64(got.Quotas[i-1].Utilization), aws.ToFloat64(q.Utilization), "sorted by utilization")
		}
		if aws.ToString(q.QuotaCode) == "L-05D334F0" {
			found = true
			assert.Greater(t, aws.ToFloat64(q.Utilization), 0.0)
			assert.Equal(t, "AWS/Usage", aws.ToString(q.Namespace))
		}
	}
	assert.True(t, found, "the report measures Rate of ListMetrics requests")
	_, err = c.GetQuotaUtilizationReport(ctx, &servicequotas.GetQuotaUtilizationReportInput{ReportId: aws.String("no-such-report")})
	var missing *sqtypes.NoSuchResourceException
	require.ErrorAs(t, err, &missing)
}

// TestServiceQuotas_EnforcedQuotasSDK reads the quotas the simulator's
// services enforce: Amazon VPC's security group quotas and Amazon RDS's DB
// instances, which DescribeAccountAttributes reports as its maximum.
func TestServiceQuotas_EnforcedQuotasSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()
	for code, want := range map[string]float64{"L-2AFB9258": 5, "L-0EA8095F": 60} {
		q, err := c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("vpc"), QuotaCode: aws.String(code)})
		require.NoError(t, err)
		assert.Equal(t, want, aws.ToFloat64(q.Quota.Value), code)
	}
	attrs, err := rdsClient().DescribeAccountAttributes(ctx, &rds.DescribeAccountAttributesInput{})
	require.NoError(t, err)
	maxima := map[string]int64{}
	for _, q := range attrs.AccountQuotas {
		maxima[aws.ToString(q.AccountQuotaName)] = aws.ToInt64(q.Max)
	}
	assert.Equal(t, int64(40), maxima["DBInstances"])
	assert.Equal(t, int64(15), maxima["ReadReplicasPerMaster"], "Read replicas per primary")
}

// TestServiceQuotas_TemplateSDK keeps a quota request template, associates
// it with the organization, and has it request the increase for an account
// the organization then creates.
func TestServiceQuotas_TemplateSDK(t *testing.T) {
	t.Parallel()
	c := serviceQuotasClient()
	_, err := orgClient().EnableAWSServiceAccess(ctx, &organizations.EnableAWSServiceAccessInput{ServicePrincipal: aws.String("servicequotas.amazonaws.com")})
	require.NoError(t, err)

	put, err := c.PutServiceQuotaIncreaseRequestIntoTemplate(ctx, &servicequotas.PutServiceQuotaIncreaseRequestIntoTemplateInput{
		ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64"), AwsRegion: aws.String("us-east-1"), DesiredValue: aws.Float64(1500),
	})
	require.NoError(t, err)
	assert.Equal(t, "Roles per account", aws.ToString(put.ServiceQuotaIncreaseRequestInTemplate.QuotaName))
	t.Cleanup(func() {
		_, _ = c.DeleteServiceQuotaIncreaseRequestFromTemplate(ctx, &servicequotas.DeleteServiceQuotaIncreaseRequestFromTemplateInput{
			ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64"), AwsRegion: aws.String("us-east-1"),
		})
	})
	got, err := c.GetServiceQuotaIncreaseRequestFromTemplate(ctx, &servicequotas.GetServiceQuotaIncreaseRequestFromTemplateInput{
		ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64"), AwsRegion: aws.String("us-east-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, 1500.0, aws.ToFloat64(got.ServiceQuotaIncreaseRequestInTemplate.DesiredValue))
	listed, err := c.ListServiceQuotaIncreaseRequestsInTemplate(ctx, &servicequotas.ListServiceQuotaIncreaseRequestsInTemplateInput{ServiceCode: aws.String("iam")})
	require.NoError(t, err)
	require.NotEmpty(t, listed.ServiceQuotaIncreaseRequestInTemplateList)

	_, err = c.AssociateServiceQuotaTemplate(ctx, &servicequotas.AssociateServiceQuotaTemplateInput{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DisassociateServiceQuotaTemplate(ctx, &servicequotas.DisassociateServiceQuotaTemplateInput{})
	})
	association, err := c.GetAssociationForServiceQuotaTemplate(ctx, &servicequotas.GetAssociationForServiceQuotaTemplateInput{})
	require.NoError(t, err)
	assert.Equal(t, sqtypes.ServiceQuotaTemplateAssociationStatusAssociated, association.ServiceQuotaTemplateAssociationStatus)

	member := createMemberAccount(t, "sq-member", "")
	creds := memberAdmin(t, member, "OrganizationAccountAccessRole")
	cfg := sdkConfig()
	cfg.Credentials = credentials.NewStaticCredentialsProvider(aws.ToString(creds.AccessKeyId), aws.ToString(creds.SecretAccessKey), aws.ToString(creds.SessionToken))
	memberQuotas := servicequotas.NewFromConfig(cfg, func(o *servicequotas.Options) { o.BaseEndpoint = aws.String(baseURL) })
	roles, err := memberQuotas.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64")})
	require.NoError(t, err)
	assert.Equal(t, 1500.0, aws.ToFloat64(roles.Quota.Value), "the template's increase applies to the new account")
	assert.Contains(t, aws.ToString(roles.Quota.QuotaArn), ":"+member+":")
	history, err := memberQuotas.ListRequestedServiceQuotaChangeHistory(ctx, &servicequotas.ListRequestedServiceQuotaChangeHistoryInput{ServiceCode: aws.String("iam")})
	require.NoError(t, err)
	require.Len(t, history.RequestedQuotas, 1)
	mine, err := c.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64")})
	require.NoError(t, err)
	assert.Equal(t, 1000.0, aws.ToFloat64(mine.Quota.Value), "a template leaves existing accounts alone")
	summary, err := iamClient().GetAccountSummary(ctx, &iam.GetAccountSummaryInput{})
	require.NoError(t, err)
	assert.Equal(t, int32(1000), summary.SummaryMap["RolesQuota"], "IAM reports the quota Service Quotas applies")

	_, err = c.DisassociateServiceQuotaTemplate(ctx, &servicequotas.DisassociateServiceQuotaTemplateInput{})
	require.NoError(t, err)
	_, err = c.DisassociateServiceQuotaTemplate(ctx, &servicequotas.DisassociateServiceQuotaTemplateInput{})
	var unused *sqtypes.ServiceQuotaTemplateNotInUseException
	require.ErrorAs(t, err, &unused)
	_, err = c.DeleteServiceQuotaIncreaseRequestFromTemplate(ctx, &servicequotas.DeleteServiceQuotaIncreaseRequestFromTemplateInput{
		ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64"), AwsRegion: aws.String("us-east-1"),
	})
	require.NoError(t, err)
	_, err = c.GetServiceQuotaIncreaseRequestFromTemplate(ctx, &servicequotas.GetServiceQuotaIncreaseRequestFromTemplateInput{
		ServiceCode: aws.String("iam"), QuotaCode: aws.String("L-FE177D64"), AwsRegion: aws.String("us-east-1"),
	})
	var missing *sqtypes.NoSuchResourceException
	require.True(t, errors.As(err, &missing))
}
