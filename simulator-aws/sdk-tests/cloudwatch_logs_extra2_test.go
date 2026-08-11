package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_AccountPolicyRoundTrip covers PutAccountPolicy, DescribeAccountPolicies,
// and DeleteAccountPolicy for a CloudWatch Logs account-level policy.
func TestLogs_AccountPolicyRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	doc := `{"Rules":[{"DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`
	policyName := "test-account-dp-policy"
	defer cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{
		PolicyName: aws.String(policyName),
		PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
	})

	put, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName:     aws.String(policyName),
		PolicyType:     cwlogtypes.PolicyTypeDataProtectionPolicy,
		PolicyDocument: aws.String(doc),
		Scope:          cwlogtypes.ScopeAll,
	})
	require.NoError(t, err)
	require.NotNil(t, put.AccountPolicy)
	assert.Equal(t, policyName, aws.ToString(put.AccountPolicy.PolicyName))
	assert.Equal(t, cwlogtypes.PolicyTypeDataProtectionPolicy, put.AccountPolicy.PolicyType)

	desc, err := cw.DescribeAccountPolicies(ctx, &cloudwatchlogs.DescribeAccountPoliciesInput{
		PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
	})
	require.NoError(t, err)
	require.NotEmpty(t, desc.AccountPolicies)
	found := false
	for _, p := range desc.AccountPolicies {
		if aws.ToString(p.PolicyName) == policyName {
			found = true
		}
	}
	assert.True(t, found, "account policy should be listed")

	_, err = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{
		PolicyName: aws.String(policyName),
		PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
	})
	require.NoError(t, err)
}

// TestLogs_QueryDefinitionRoundTrip covers PutQueryDefinition, DescribeQueryDefinitions,
// and DeleteQueryDefinition.
func TestLogs_QueryDefinitionRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	put, err := cw.PutQueryDefinition(ctx, &cloudwatchlogs.PutQueryDefinitionInput{
		Name:        aws.String("test-query-def"),
		QueryString: aws.String("fields @timestamp, @message | sort @timestamp desc"),
	})
	require.NoError(t, err)
	id := aws.ToString(put.QueryDefinitionId)
	require.NotEmpty(t, id)
	defer cw.DeleteQueryDefinition(ctx, &cloudwatchlogs.DeleteQueryDefinitionInput{
		QueryDefinitionId: aws.String(id),
	})

	desc, err := cw.DescribeQueryDefinitions(ctx, &cloudwatchlogs.DescribeQueryDefinitionsInput{
		QueryDefinitionNamePrefix: aws.String("test-query"),
	})
	require.NoError(t, err)
	found := false
	for _, d := range desc.QueryDefinitions {
		if aws.ToString(d.QueryDefinitionId) == id {
			found = true
			assert.Equal(t, "test-query-def", aws.ToString(d.Name))
		}
	}
	assert.True(t, found, "query definition should be listed")

	// Update in place via the returned id.
	put2, err := cw.PutQueryDefinition(ctx, &cloudwatchlogs.PutQueryDefinitionInput{
		Name:              aws.String("test-query-def"),
		QueryString:       aws.String("fields @message"),
		QueryDefinitionId: aws.String(id),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(put2.QueryDefinitionId))

	del, err := cw.DeleteQueryDefinition(ctx, &cloudwatchlogs.DeleteQueryDefinitionInput{
		QueryDefinitionId: aws.String(id),
	})
	require.NoError(t, err)
	assert.True(t, del.Success)
}

// TestLogs_ResourcePolicyRoundTrip covers PutResourcePolicy, DescribeResourcePolicies,
// and DeleteResourcePolicy.
func TestLogs_ResourcePolicyRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	name := "test-resource-policy"
	doc := `{"Version":"2012-10-17","Statement":[{"Sid":"Route53","Effect":"Allow","Principal":{"Service":"route53.amazonaws.com"},"Action":"logs:PutLogEvents","Resource":"*"}]}`
	defer cw.DeleteResourcePolicy(ctx, &cloudwatchlogs.DeleteResourcePolicyInput{PolicyName: aws.String(name)})

	put, err := cw.PutResourcePolicy(ctx, &cloudwatchlogs.PutResourcePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(doc),
	})
	require.NoError(t, err)
	require.NotNil(t, put.ResourcePolicy)
	assert.Equal(t, name, aws.ToString(put.ResourcePolicy.PolicyName))

	desc, err := cw.DescribeResourcePolicies(ctx, &cloudwatchlogs.DescribeResourcePoliciesInput{})
	require.NoError(t, err)
	found := false
	for _, p := range desc.ResourcePolicies {
		if aws.ToString(p.PolicyName) == name {
			found = true
		}
	}
	assert.True(t, found, "resource policy should be listed")

	_, err = cw.DeleteResourcePolicy(ctx, &cloudwatchlogs.DeleteResourcePolicyInput{PolicyName: aws.String(name)})
	require.NoError(t, err)
}

// TestLogs_DestinationRoundTrip covers PutDestination, DescribeDestinations,
// PutDestinationPolicy, and DeleteDestination for a cross-account destination.
func TestLogs_DestinationRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	name := "test-destination"
	defer cw.DeleteDestination(ctx, &cloudwatchlogs.DeleteDestinationInput{DestinationName: aws.String(name)})

	put, err := cw.PutDestination(ctx, &cloudwatchlogs.PutDestinationInput{
		DestinationName: aws.String(name),
		TargetArn:       aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/logs"),
		RoleArn:         aws.String("arn:aws:iam::123456789012:role/CWLtoKinesisRole"),
	})
	require.NoError(t, err)
	require.NotNil(t, put.Destination)
	assert.Equal(t, name, aws.ToString(put.Destination.DestinationName))
	require.NotEmpty(t, aws.ToString(put.Destination.Arn))

	_, err = cw.PutDestinationPolicy(ctx, &cloudwatchlogs.PutDestinationPolicyInput{
		DestinationName: aws.String(name),
		AccessPolicy:    aws.String(`{"Version":"2012-10-17","Statement":[]}`),
	})
	require.NoError(t, err)

	desc, err := cw.DescribeDestinations(ctx, &cloudwatchlogs.DescribeDestinationsInput{
		DestinationNamePrefix: aws.String("test-dest"),
	})
	require.NoError(t, err)
	found := false
	for _, d := range desc.Destinations {
		if aws.ToString(d.DestinationName) == name {
			found = true
			assert.NotEmpty(t, aws.ToString(d.AccessPolicy))
		}
	}
	assert.True(t, found, "destination should be listed")

	_, err = cw.DeleteDestination(ctx, &cloudwatchlogs.DeleteDestinationInput{DestinationName: aws.String(name)})
	require.NoError(t, err)
}

// TestLogs_DeliveryRoundTrip covers the full vended-log delivery surface:
// delivery sources, delivery destinations (+ their policies), and the delivery
// that links a source to a destination.
func TestLogs_DeliveryRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	srcName := "test-delivery-source"
	dstName := "test-delivery-destination"
	defer cw.DeleteDeliverySource(ctx, &cloudwatchlogs.DeleteDeliverySourceInput{Name: aws.String(srcName)})
	defer cw.DeleteDeliveryDestination(ctx, &cloudwatchlogs.DeleteDeliveryDestinationInput{Name: aws.String(dstName)})

	// Delivery source.
	srcPut, err := cw.PutDeliverySource(ctx, &cloudwatchlogs.PutDeliverySourceInput{
		Name:        aws.String(srcName),
		LogType:     aws.String("APPLICATION_LOGS"),
		ResourceArn: aws.String("arn:aws:bedrock:us-east-1:123456789012:provisioned-model/abc"),
	})
	require.NoError(t, err)
	require.NotNil(t, srcPut.DeliverySource)
	require.NotEmpty(t, aws.ToString(srcPut.DeliverySource.Arn))

	srcGet, err := cw.GetDeliverySource(ctx, &cloudwatchlogs.GetDeliverySourceInput{Name: aws.String(srcName)})
	require.NoError(t, err)
	assert.Equal(t, srcName, aws.ToString(srcGet.DeliverySource.Name))

	srcList, err := cw.DescribeDeliverySources(ctx, &cloudwatchlogs.DescribeDeliverySourcesInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, srcList.DeliverySources)

	// Delivery destination (an S3 bucket).
	dstPut, err := cw.PutDeliveryDestination(ctx, &cloudwatchlogs.PutDeliveryDestinationInput{
		Name:                    aws.String(dstName),
		DeliveryDestinationType: cwlogtypes.DeliveryDestinationTypeS3,
		OutputFormat:            cwlogtypes.OutputFormatJson,
		DeliveryDestinationConfiguration: &cwlogtypes.DeliveryDestinationConfiguration{
			DestinationResourceArn: aws.String("arn:aws:s3:::my-delivery-bucket"),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, dstPut.DeliveryDestination)
	dstArn := aws.ToString(dstPut.DeliveryDestination.Arn)
	require.NotEmpty(t, dstArn)

	dstGet, err := cw.GetDeliveryDestination(ctx, &cloudwatchlogs.GetDeliveryDestinationInput{Name: aws.String(dstName)})
	require.NoError(t, err)
	assert.Equal(t, dstName, aws.ToString(dstGet.DeliveryDestination.Name))

	dstList, err := cw.DescribeDeliveryDestinations(ctx, &cloudwatchlogs.DescribeDeliveryDestinationsInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, dstList.DeliveryDestinations)

	// Delivery destination policy.
	polDoc := `{"Version":"2012-10-17","Statement":[{"Sid":"Allow","Effect":"Allow","Principal":{"Service":"delivery.logs.amazonaws.com"},"Action":"logs:CreateDelivery","Resource":"*"}]}`
	_, err = cw.PutDeliveryDestinationPolicy(ctx, &cloudwatchlogs.PutDeliveryDestinationPolicyInput{
		DeliveryDestinationName:   aws.String(dstName),
		DeliveryDestinationPolicy: aws.String(polDoc),
	})
	require.NoError(t, err)

	getPol, err := cw.GetDeliveryDestinationPolicy(ctx, &cloudwatchlogs.GetDeliveryDestinationPolicyInput{
		DeliveryDestinationName: aws.String(dstName),
	})
	require.NoError(t, err)
	require.NotNil(t, getPol.Policy)
	assert.Equal(t, polDoc, aws.ToString(getPol.Policy.DeliveryDestinationPolicy))

	// Delivery linking source -> destination.
	created, err := cw.CreateDelivery(ctx, &cloudwatchlogs.CreateDeliveryInput{
		DeliverySourceName:     aws.String(srcName),
		DeliveryDestinationArn: aws.String(dstArn),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Delivery)
	deliveryID := aws.ToString(created.Delivery.Id)
	require.NotEmpty(t, deliveryID)

	got, err := cw.GetDelivery(ctx, &cloudwatchlogs.GetDeliveryInput{Id: aws.String(deliveryID)})
	require.NoError(t, err)
	assert.Equal(t, srcName, aws.ToString(got.Delivery.DeliverySourceName))

	listed, err := cw.DescribeDeliveries(ctx, &cloudwatchlogs.DescribeDeliveriesInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, listed.Deliveries)

	_, err = cw.DeleteDelivery(ctx, &cloudwatchlogs.DeleteDeliveryInput{Id: aws.String(deliveryID)})
	require.NoError(t, err)

	_, err = cw.DeleteDeliveryDestinationPolicy(ctx, &cloudwatchlogs.DeleteDeliveryDestinationPolicyInput{
		DeliveryDestinationName: aws.String(dstName),
	})
	require.NoError(t, err)
}

// TestLogs_AnomalyDetectorRoundTrip covers CreateLogAnomalyDetector,
// GetLogAnomalyDetector, ListLogAnomalyDetectors, and DeleteLogAnomalyDetector.
func TestLogs_AnomalyDetectorRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/anomaly-detector"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	created, err := cw.CreateLogAnomalyDetector(ctx, &cloudwatchlogs.CreateLogAnomalyDetectorInput{
		DetectorName:        aws.String("test-detector"),
		LogGroupArnList:     []string{cwLogGroupArnForTest(group)},
		EvaluationFrequency: cwlogtypes.EvaluationFrequencyOneHour,
	})
	require.NoError(t, err)
	arn := aws.ToString(created.AnomalyDetectorArn)
	require.NotEmpty(t, arn)
	defer cw.DeleteLogAnomalyDetector(ctx, &cloudwatchlogs.DeleteLogAnomalyDetectorInput{
		AnomalyDetectorArn: aws.String(arn),
	})

	got, err := cw.GetLogAnomalyDetector(ctx, &cloudwatchlogs.GetLogAnomalyDetectorInput{
		AnomalyDetectorArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Equal(t, "test-detector", aws.ToString(got.DetectorName))
	assert.Equal(t, cwlogtypes.EvaluationFrequencyOneHour, got.EvaluationFrequency)

	listed, err := cw.ListLogAnomalyDetectors(ctx, &cloudwatchlogs.ListLogAnomalyDetectorsInput{})
	require.NoError(t, err)
	found := false
	for _, d := range listed.AnomalyDetectors {
		if aws.ToString(d.AnomalyDetectorArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "anomaly detector should be listed")

	_, err = cw.DeleteLogAnomalyDetector(ctx, &cloudwatchlogs.DeleteLogAnomalyDetectorInput{
		AnomalyDetectorArn: aws.String(arn),
	})
	require.NoError(t, err)
}

// TestLogs_IndexPolicyRoundTrip covers PutIndexPolicy, DescribeIndexPolicies,
// DescribeFieldIndexes, and DeleteIndexPolicy.
func TestLogs_IndexPolicyRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	group := "/test/index-policy"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})

	put, err := cw.PutIndexPolicy(ctx, &cloudwatchlogs.PutIndexPolicyInput{
		LogGroupIdentifier: aws.String(group),
		PolicyDocument:     aws.String(`{"Fields":["requestId","accountId"]}`),
	})
	require.NoError(t, err)
	require.NotNil(t, put.IndexPolicy)
	assert.Equal(t, group, aws.ToString(put.IndexPolicy.LogGroupIdentifier))

	desc, err := cw.DescribeIndexPolicies(ctx, &cloudwatchlogs.DescribeIndexPoliciesInput{
		LogGroupIdentifiers: []string{group},
	})
	require.NoError(t, err)
	require.Len(t, desc.IndexPolicies, 1)
	assert.Equal(t, group, aws.ToString(desc.IndexPolicies[0].LogGroupIdentifier))

	fi, err := cw.DescribeFieldIndexes(ctx, &cloudwatchlogs.DescribeFieldIndexesInput{
		LogGroupIdentifiers: []string{group},
	})
	require.NoError(t, err)
	assert.Empty(t, fi.FieldIndexes)

	_, err = cw.DeleteIndexPolicy(ctx, &cloudwatchlogs.DeleteIndexPolicyInput{
		LogGroupIdentifier: aws.String(group),
	})
	require.NoError(t, err)
}

// TestLogs_ConfigurationTemplates covers DescribeConfigurationTemplates, which
// returns the delivery configuration templates AWS publishes per log source.
func TestLogs_ConfigurationTemplates(t *testing.T) {
	cw := cwLogsClient()
	resp, err := cw.DescribeConfigurationTemplates(ctx, &cloudwatchlogs.DescribeConfigurationTemplatesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ConfigurationTemplates)
	for _, tpl := range resp.ConfigurationTemplates {
		assert.NotEmpty(t, aws.ToString(tpl.Service))
		assert.NotEmpty(t, string(tpl.DeliveryDestinationType))
	}
}

// cwLogGroupArnForTest builds the ARN the sim assigns to a log group, for use
// as a log anomaly detector target.
func cwLogGroupArnForTest(group string) string {
	out, err := cwLogsClient().DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(group),
	})
	if err == nil {
		for _, g := range out.LogGroups {
			if aws.ToString(g.LogGroupName) == group {
				return aws.ToString(g.Arn)
			}
		}
	}
	return "arn:aws:logs:us-east-1:000000000000:log-group:" + group
}
