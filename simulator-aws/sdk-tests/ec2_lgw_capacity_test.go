package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_LocalGateway drives the full AWS Outposts local gateway control
// plane over the SDK: it discovers the seeded local gateway, then exercises
// route-table, virtual-interface-group, virtual-interface, VPC-association, and
// virtual-interface-group-association CRUD round-trips.
func TestEC2_LocalGateway(t *testing.T) {
	client := ec2Client()

	// Discover the seeded local gateway.
	lgws, err := client.DescribeLocalGateways(ctx, &ec2.DescribeLocalGatewaysInput{})
	require.NoError(t, err)
	require.NotEmpty(t, lgws.LocalGateways, "expected a seeded local gateway")
	lgwID := aws.ToString(lgws.LocalGateways[0].LocalGatewayId)
	assert.Equal(t, "available", aws.ToString(lgws.LocalGateways[0].State))

	// Route table.
	rtOut, err := client.CreateLocalGatewayRouteTable(ctx, &ec2.CreateLocalGatewayRouteTableInput{
		LocalGatewayId: aws.String(lgwID),
		Mode:           types.LocalGatewayRouteTableModeDirectVpcRouting,
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeLocalGatewayRouteTable,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("lgw-rtb")}},
		}},
	})
	require.NoError(t, err)
	rtID := aws.ToString(rtOut.LocalGatewayRouteTable.LocalGatewayRouteTableId)
	assert.Equal(t, lgwID, aws.ToString(rtOut.LocalGatewayRouteTable.LocalGatewayId))
	assert.Equal(t, types.LocalGatewayRouteTableModeDirectVpcRouting, rtOut.LocalGatewayRouteTable.Mode)
	t.Cleanup(func() {
		_, _ = client.DeleteLocalGatewayRouteTable(ctx, &ec2.DeleteLocalGatewayRouteTableInput{LocalGatewayRouteTableId: aws.String(rtID)})
	})

	descRT, err := client.DescribeLocalGatewayRouteTables(ctx, &ec2.DescribeLocalGatewayRouteTablesInput{
		LocalGatewayRouteTableIds: []string{rtID},
	})
	require.NoError(t, err)
	require.Len(t, descRT.LocalGatewayRouteTables, 1)
	assert.Equal(t, rtID, aws.ToString(descRT.LocalGatewayRouteTables[0].LocalGatewayRouteTableId))

	// Virtual interface group.
	vigOut, err := client.CreateLocalGatewayVirtualInterfaceGroup(ctx, &ec2.CreateLocalGatewayVirtualInterfaceGroupInput{
		LocalGatewayId: aws.String(lgwID),
		LocalBgpAsn:    aws.Int32(65000),
	})
	require.NoError(t, err)
	vigID := aws.ToString(vigOut.LocalGatewayVirtualInterfaceGroup.LocalGatewayVirtualInterfaceGroupId)
	assert.EqualValues(t, 65000, aws.ToInt32(vigOut.LocalGatewayVirtualInterfaceGroup.LocalBgpAsn))
	t.Cleanup(func() {
		_, _ = client.DeleteLocalGatewayVirtualInterfaceGroup(ctx, &ec2.DeleteLocalGatewayVirtualInterfaceGroupInput{LocalGatewayVirtualInterfaceGroupId: aws.String(vigID)})
	})

	// Virtual interface (attached to the group).
	vifOut, err := client.CreateLocalGatewayVirtualInterface(ctx, &ec2.CreateLocalGatewayVirtualInterfaceInput{
		LocalGatewayVirtualInterfaceGroupId: aws.String(vigID),
		OutpostLagId:                        aws.String("ola-sim00000000000000"),
		Vlan:                                aws.Int32(101),
		LocalAddress:                        aws.String("10.0.0.1/30"),
		PeerAddress:                         aws.String("10.0.0.2/30"),
		PeerBgpAsn:                          aws.Int32(64513),
	})
	require.NoError(t, err)
	vifID := aws.ToString(vifOut.LocalGatewayVirtualInterface.LocalGatewayVirtualInterfaceId)
	assert.EqualValues(t, 101, aws.ToInt32(vifOut.LocalGatewayVirtualInterface.Vlan))
	assert.Equal(t, "10.0.0.1/30", aws.ToString(vifOut.LocalGatewayVirtualInterface.LocalAddress))
	assert.Equal(t, vigID, aws.ToString(vifOut.LocalGatewayVirtualInterface.LocalGatewayVirtualInterfaceGroupId))
	t.Cleanup(func() {
		_, _ = client.DeleteLocalGatewayVirtualInterface(ctx, &ec2.DeleteLocalGatewayVirtualInterfaceInput{LocalGatewayVirtualInterfaceId: aws.String(vifID)})
	})

	descVif, err := client.DescribeLocalGatewayVirtualInterfaces(ctx, &ec2.DescribeLocalGatewayVirtualInterfacesInput{
		LocalGatewayVirtualInterfaceIds: []string{vifID},
	})
	require.NoError(t, err)
	require.Len(t, descVif.LocalGatewayVirtualInterfaces, 1)

	descVig, err := client.DescribeLocalGatewayVirtualInterfaceGroups(ctx, &ec2.DescribeLocalGatewayVirtualInterfaceGroupsInput{
		LocalGatewayVirtualInterfaceGroupIds: []string{vigID},
	})
	require.NoError(t, err)
	require.Len(t, descVig.LocalGatewayVirtualInterfaceGroups, 1)
	assert.Contains(t, descVig.LocalGatewayVirtualInterfaceGroups[0].LocalGatewayVirtualInterfaceIds, vifID)

	// VPC association.
	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.80.0.0/16")})
	require.NoError(t, err)
	vpcID := aws.ToString(vpc.Vpc.VpcId)
	t.Cleanup(func() { _, _ = client.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: aws.String(vpcID)}) })

	vpcAssocOut, err := client.CreateLocalGatewayRouteTableVpcAssociation(ctx, &ec2.CreateLocalGatewayRouteTableVpcAssociationInput{
		LocalGatewayRouteTableId: aws.String(rtID),
		VpcId:                    aws.String(vpcID),
	})
	require.NoError(t, err)
	vpcAssocID := aws.ToString(vpcAssocOut.LocalGatewayRouteTableVpcAssociation.LocalGatewayRouteTableVpcAssociationId)
	assert.Equal(t, vpcID, aws.ToString(vpcAssocOut.LocalGatewayRouteTableVpcAssociation.VpcId))
	assert.Equal(t, "associated", aws.ToString(vpcAssocOut.LocalGatewayRouteTableVpcAssociation.State))

	descVpcAssoc, err := client.DescribeLocalGatewayRouteTableVpcAssociations(ctx, &ec2.DescribeLocalGatewayRouteTableVpcAssociationsInput{
		LocalGatewayRouteTableVpcAssociationIds: []string{vpcAssocID},
	})
	require.NoError(t, err)
	require.Len(t, descVpcAssoc.LocalGatewayRouteTableVpcAssociations, 1)
	_, err = client.DeleteLocalGatewayRouteTableVpcAssociation(ctx, &ec2.DeleteLocalGatewayRouteTableVpcAssociationInput{
		LocalGatewayRouteTableVpcAssociationId: aws.String(vpcAssocID),
	})
	require.NoError(t, err)

	// Virtual interface group association.
	vigAssocOut, err := client.CreateLocalGatewayRouteTableVirtualInterfaceGroupAssociation(ctx, &ec2.CreateLocalGatewayRouteTableVirtualInterfaceGroupAssociationInput{
		LocalGatewayRouteTableId:            aws.String(rtID),
		LocalGatewayVirtualInterfaceGroupId: aws.String(vigID),
	})
	require.NoError(t, err)
	vigAssocID := aws.ToString(vigAssocOut.LocalGatewayRouteTableVirtualInterfaceGroupAssociation.LocalGatewayRouteTableVirtualInterfaceGroupAssociationId)
	assert.Equal(t, vigID, aws.ToString(vigAssocOut.LocalGatewayRouteTableVirtualInterfaceGroupAssociation.LocalGatewayVirtualInterfaceGroupId))

	descVigAssoc, err := client.DescribeLocalGatewayRouteTableVirtualInterfaceGroupAssociations(ctx, &ec2.DescribeLocalGatewayRouteTableVirtualInterfaceGroupAssociationsInput{
		LocalGatewayRouteTableVirtualInterfaceGroupAssociationIds: []string{vigAssocID},
	})
	require.NoError(t, err)
	require.Len(t, descVigAssoc.LocalGatewayRouteTableVirtualInterfaceGroupAssociations, 1)
	_, err = client.DeleteLocalGatewayRouteTableVirtualInterfaceGroupAssociation(ctx, &ec2.DeleteLocalGatewayRouteTableVirtualInterfaceGroupAssociationInput{
		LocalGatewayRouteTableVirtualInterfaceGroupAssociationId: aws.String(vigAssocID),
	})
	require.NoError(t, err)
}

// TestEC2_CapacityManager drives the Amazon EC2 Capacity Manager enable/disable
// lifecycle, monitored-tag-key updates, data-export CRUD, and the metric reads
// computed from the underlying capacity-reservation store.
func TestEC2_CapacityManager(t *testing.T) {
	client := ec2Client()

	enOut, err := client.EnableCapacityManager(ctx, &ec2.EnableCapacityManagerInput{
		OrganizationsAccess: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, types.CapacityManagerStatusEnabled, enOut.CapacityManagerStatus)
	assert.True(t, aws.ToBool(enOut.OrganizationsAccess))

	attrs, err := client.GetCapacityManagerAttributes(ctx, &ec2.GetCapacityManagerAttributesInput{})
	require.NoError(t, err)
	assert.Equal(t, types.CapacityManagerStatusEnabled, attrs.CapacityManagerStatus)
	assert.Equal(t, types.IngestionStatusIngestionComplete, attrs.IngestionStatus)

	// Monitored tag keys.
	tagOut, err := client.UpdateCapacityManagerMonitoredTagKeys(ctx, &ec2.UpdateCapacityManagerMonitoredTagKeysInput{
		ActivateTagKeys: []string{"team", "cost-center"},
	})
	require.NoError(t, err)
	var activated []string
	for _, k := range tagOut.CapacityManagerTagKeys {
		activated = append(activated, aws.ToString(k.TagKey))
	}
	assert.ElementsMatch(t, []string{"team", "cost-center"}, activated)

	getTags, err := client.GetCapacityManagerMonitoredTagKeys(ctx, &ec2.GetCapacityManagerMonitoredTagKeysInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, getTags.CapacityManagerTagKeys)

	_, err = client.UpdateCapacityManagerMonitoredTagKeys(ctx, &ec2.UpdateCapacityManagerMonitoredTagKeysInput{
		DeactivateTagKeys: []string{"cost-center"},
	})
	require.NoError(t, err)

	// Organizations access toggle.
	orgOut, err := client.UpdateCapacityManagerOrganizationsAccess(ctx, &ec2.UpdateCapacityManagerOrganizationsAccessInput{
		OrganizationsAccess: aws.Bool(false),
	})
	require.NoError(t, err)
	assert.False(t, aws.ToBool(orgOut.OrganizationsAccess))

	// Data export to S3.
	expOut, err := client.CreateCapacityManagerDataExport(ctx, &ec2.CreateCapacityManagerDataExportInput{
		S3BucketName:   aws.String("my-cm-export-bucket"),
		S3BucketPrefix: aws.String("exports/"),
		Schedule:       types.ScheduleHourly,
		OutputFormat:   types.OutputFormatCsv,
	})
	require.NoError(t, err)
	expID := aws.ToString(expOut.CapacityManagerDataExportId)
	require.NotEmpty(t, expID)

	descExp, err := client.DescribeCapacityManagerDataExports(ctx, &ec2.DescribeCapacityManagerDataExportsInput{
		CapacityManagerDataExportIds: []string{expID},
	})
	require.NoError(t, err)
	require.Len(t, descExp.CapacityManagerDataExports, 1)
	assert.Equal(t, "my-cm-export-bucket", aws.ToString(descExp.CapacityManagerDataExports[0].S3BucketName))
	assert.Equal(t, types.ScheduleHourly, descExp.CapacityManagerDataExports[0].Schedule)

	_, err = client.DeleteCapacityManagerDataExport(ctx, &ec2.DeleteCapacityManagerDataExportInput{
		CapacityManagerDataExportId: aws.String(expID),
	})
	require.NoError(t, err)

	// Metric reads — honest values derived from the reservation store. Create a
	// reservation so the dimension/metric sets are non-empty.
	crOut, err := client.CreateCapacityReservation(ctx, &ec2.CreateCapacityReservationInput{
		InstanceType:          aws.String("m5.large"),
		InstancePlatform:      types.CapacityReservationInstancePlatformLinuxUnix,
		AvailabilityZone:      aws.String("us-east-1a"),
		InstanceCount:         aws.Int32(4),
		InstanceMatchCriteria: types.InstanceMatchCriteriaOpen,
	})
	require.NoError(t, err)
	crID := aws.ToString(crOut.CapacityReservation.CapacityReservationId)
	t.Cleanup(func() {
		_, _ = client.CancelCapacityReservation(ctx, &ec2.CancelCapacityReservationInput{CapacityReservationId: aws.String(crID)})
	})

	now := time.Now().UTC()
	metricData, err := client.GetCapacityManagerMetricData(ctx, &ec2.GetCapacityManagerMetricDataInput{
		MetricNames: []types.Metric{types.MetricReservationTotalCount, types.MetricReservationAvgUtilizationInst},
		StartTime:   aws.Time(now.Add(-time.Hour)),
		EndTime:     aws.Time(now),
		Period:      aws.Int32(3600),
	})
	require.NoError(t, err)
	require.NotEmpty(t, metricData.MetricDataResults)
	foundCR := false
	for _, res := range metricData.MetricDataResults {
		if res.Dimension != nil && aws.ToString(res.Dimension.ReservationId) == crID {
			foundCR = true
			assert.NotEmpty(t, res.MetricValues)
		}
	}
	assert.True(t, foundCR, "expected a metric data result for the created reservation")

	dims, err := client.GetCapacityManagerMetricDimensions(ctx, &ec2.GetCapacityManagerMetricDimensionsInput{
		GroupBy:     []types.GroupBy{types.GroupByInstanceType},
		MetricNames: []types.Metric{types.MetricReservationTotalCount},
		StartTime:   aws.Time(now.Add(-time.Hour)),
		EndTime:     aws.Time(now),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, dims.MetricDimensionResults)

	_, err = client.DisableCapacityManager(ctx, &ec2.DisableCapacityManagerInput{})
	require.NoError(t, err)
}

// TestEC2_DeclarativePolicies drives the declarative-policies report lifecycle:
// start → describe (running → complete) → summary, plus the cancel path.
func TestEC2_DeclarativePolicies(t *testing.T) {
	client := ec2Client()

	startOut, err := client.StartDeclarativePoliciesReport(ctx, &ec2.StartDeclarativePoliciesReportInput{
		S3Bucket: aws.String("my-dp-report-bucket"),
		S3Prefix: aws.String("reports/"),
		TargetId: aws.String("r-abcd"),
	})
	require.NoError(t, err)
	reportID := aws.ToString(startOut.ReportId)
	require.NotEmpty(t, reportID)

	descOut, err := client.DescribeDeclarativePoliciesReports(ctx, &ec2.DescribeDeclarativePoliciesReportsInput{
		ReportIds: []string{reportID},
	})
	require.NoError(t, err)
	require.Len(t, descOut.Reports, 1)
	assert.Equal(t, types.ReportStateComplete, descOut.Reports[0].Status)
	assert.Equal(t, "r-abcd", aws.ToString(descOut.Reports[0].TargetId))

	summary, err := client.GetDeclarativePoliciesReportSummary(ctx, &ec2.GetDeclarativePoliciesReportSummaryInput{
		ReportId: aws.String(reportID),
	})
	require.NoError(t, err)
	assert.Equal(t, reportID, aws.ToString(summary.ReportId))
	assert.EqualValues(t, 1, aws.ToInt32(summary.NumberOfAccounts))
	assert.NotEmpty(t, summary.AttributeSummaries)

	// Cancel path on a second report.
	start2, err := client.StartDeclarativePoliciesReport(ctx, &ec2.StartDeclarativePoliciesReportInput{
		S3Bucket: aws.String("my-dp-report-bucket"),
		TargetId: aws.String("ou-1234"),
	})
	require.NoError(t, err)
	cancelOut, err := client.CancelDeclarativePoliciesReport(ctx, &ec2.CancelDeclarativePoliciesReportInput{
		ReportId: start2.ReportId,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(cancelOut.Return))
}

// TestEC2_NetworkPerformance drives the AWS Network Performance metric
// subscription lifecycle and GetAwsNetworkPerformanceData metric-point read.
func TestEC2_NetworkPerformance(t *testing.T) {
	client := ec2Client()

	enOut, err := client.EnableAwsNetworkPerformanceMetricSubscription(ctx, &ec2.EnableAwsNetworkPerformanceMetricSubscriptionInput{
		Source:      aws.String("us-east-1"),
		Destination: aws.String("eu-west-1"),
		Metric:      types.MetricTypeAggregateLatency,
		Statistic:   types.StatisticTypeP50,
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(enOut.Output))

	descOut, err := client.DescribeAwsNetworkPerformanceMetricSubscriptions(ctx, &ec2.DescribeAwsNetworkPerformanceMetricSubscriptionsInput{})
	require.NoError(t, err)
	found := false
	for _, s := range descOut.Subscriptions {
		if aws.ToString(s.Source) == "us-east-1" && aws.ToString(s.Destination) == "eu-west-1" {
			found = true
			assert.Equal(t, types.MetricTypeAggregateLatency, s.Metric)
			assert.Equal(t, types.StatisticTypeP50, s.Statistic)
		}
	}
	assert.True(t, found, "expected the enabled subscription in the list")

	now := time.Now().UTC()
	dataOut, err := client.GetAwsNetworkPerformanceData(ctx, &ec2.GetAwsNetworkPerformanceDataInput{
		StartTime: aws.Time(now.Add(-30 * time.Minute)),
		EndTime:   aws.Time(now),
		DataQueries: []types.DataQuery{{
			Id:          aws.String("q1"),
			Source:      aws.String("us-east-1"),
			Destination: aws.String("eu-west-1"),
			Metric:      types.MetricTypeAggregateLatency,
			Statistic:   types.StatisticTypeP50,
			Period:      types.PeriodTypeFiveMinutes,
		}},
	})
	require.NoError(t, err)
	require.Len(t, dataOut.DataResponses, 1)
	assert.Equal(t, "q1", aws.ToString(dataOut.DataResponses[0].Id))
	assert.Equal(t, "us-east-1", aws.ToString(dataOut.DataResponses[0].Source))
	assert.NotEmpty(t, dataOut.DataResponses[0].MetricPoints)
	for _, p := range dataOut.DataResponses[0].MetricPoints {
		assert.Equal(t, "OK", aws.ToString(p.Status))
	}

	_, err = client.DisableAwsNetworkPerformanceMetricSubscription(ctx, &ec2.DisableAwsNetworkPerformanceMetricSubscriptionInput{
		Source:      aws.String("us-east-1"),
		Destination: aws.String("eu-west-1"),
		Metric:      types.MetricTypeAggregateLatency,
		Statistic:   types.StatisticTypeP50,
	})
	require.NoError(t, err)
}
