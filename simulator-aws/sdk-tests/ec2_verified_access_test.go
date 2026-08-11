package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEC2_VerifiedAccessLifecycleSDK exercises the Amazon EC2 Verified Access
// control plane end-to-end: an instance, a standalone trust provider that
// attaches/detaches to the instance, a group with a Cedar policy document under
// the instance, and an endpoint with its own policy document under the group.
// It read-backs every resource via the Describe / Get* ops and tears the family
// down.
func TestEC2_VerifiedAccessLifecycleSDK(t *testing.T) {
	c := ec2Client()

	// ---- Instance ----
	inst, err := c.CreateVerifiedAccessInstance(ctx, &ec2.CreateVerifiedAccessInstanceInput{
		Description: aws.String("vai-a"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeVerifiedAccessInstance,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("vai-a")}},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, inst.VerifiedAccessInstance)
	instID := aws.ToString(inst.VerifiedAccessInstance.VerifiedAccessInstanceId)
	require.NotEmpty(t, instID)
	defer func() {
		_, _ = c.DeleteVerifiedAccessInstance(ctx, &ec2.DeleteVerifiedAccessInstanceInput{VerifiedAccessInstanceId: aws.String(instID)})
	}()

	descI, err := c.DescribeVerifiedAccessInstances(ctx, &ec2.DescribeVerifiedAccessInstancesInput{
		VerifiedAccessInstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, descI.VerifiedAccessInstances, 1)
	assert.Equal(t, "vai-a", aws.ToString(descI.VerifiedAccessInstances[0].Description))
	var nameTag string
	for _, tg := range descI.VerifiedAccessInstances[0].Tags {
		if aws.ToString(tg.Key) == "Name" {
			nameTag = aws.ToString(tg.Value)
		}
	}
	assert.Equal(t, "vai-a", nameTag)

	modI, err := c.ModifyVerifiedAccessInstance(ctx, &ec2.ModifyVerifiedAccessInstanceInput{
		VerifiedAccessInstanceId: aws.String(instID),
		Description:              aws.String("vai-a-updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "vai-a-updated", aws.ToString(modI.VerifiedAccessInstance.Description))

	// ---- Trust provider ----
	tp, err := c.CreateVerifiedAccessTrustProvider(ctx, &ec2.CreateVerifiedAccessTrustProviderInput{
		TrustProviderType:     types.TrustProviderTypeUser,
		UserTrustProviderType: types.UserTrustProviderTypeIamIdentityCenter,
		PolicyReferenceName:   aws.String("idc"),
		Description:           aws.String("tp-a"),
	})
	require.NoError(t, err)
	tpID := aws.ToString(tp.VerifiedAccessTrustProvider.VerifiedAccessTrustProviderId)
	require.NotEmpty(t, tpID)
	assert.Equal(t, types.TrustProviderTypeUser, tp.VerifiedAccessTrustProvider.TrustProviderType)
	defer func() {
		_, _ = c.DeleteVerifiedAccessTrustProvider(ctx, &ec2.DeleteVerifiedAccessTrustProviderInput{VerifiedAccessTrustProviderId: aws.String(tpID)})
	}()

	att, err := c.AttachVerifiedAccessTrustProvider(ctx, &ec2.AttachVerifiedAccessTrustProviderInput{
		VerifiedAccessInstanceId:      aws.String(instID),
		VerifiedAccessTrustProviderId: aws.String(tpID),
	})
	require.NoError(t, err)
	require.NotNil(t, att.VerifiedAccessInstance)
	require.Len(t, att.VerifiedAccessInstance.VerifiedAccessTrustProviders, 1)
	assert.Equal(t, tpID, aws.ToString(att.VerifiedAccessInstance.VerifiedAccessTrustProviders[0].VerifiedAccessTrustProviderId))

	descTP, err := c.DescribeVerifiedAccessTrustProviders(ctx, &ec2.DescribeVerifiedAccessTrustProvidersInput{
		VerifiedAccessTrustProviderIds: []string{tpID},
	})
	require.NoError(t, err)
	require.Len(t, descTP.VerifiedAccessTrustProviders, 1)
	assert.Equal(t, "idc", aws.ToString(descTP.VerifiedAccessTrustProviders[0].PolicyReferenceName))

	_, err = c.ModifyVerifiedAccessTrustProvider(ctx, &ec2.ModifyVerifiedAccessTrustProviderInput{
		VerifiedAccessTrustProviderId: aws.String(tpID),
		Description:                   aws.String("tp-a-updated"),
	})
	require.NoError(t, err)

	det, err := c.DetachVerifiedAccessTrustProvider(ctx, &ec2.DetachVerifiedAccessTrustProviderInput{
		VerifiedAccessInstanceId:      aws.String(instID),
		VerifiedAccessTrustProviderId: aws.String(tpID),
	})
	require.NoError(t, err)
	assert.Empty(t, det.VerifiedAccessInstance.VerifiedAccessTrustProviders)

	// ---- Group ----
	const groupPolicy = `permit(principal, action, resource);`
	grp, err := c.CreateVerifiedAccessGroup(ctx, &ec2.CreateVerifiedAccessGroupInput{
		VerifiedAccessInstanceId: aws.String(instID),
		Description:              aws.String("grp-a"),
		PolicyDocument:           aws.String(groupPolicy),
	})
	require.NoError(t, err)
	grpID := aws.ToString(grp.VerifiedAccessGroup.VerifiedAccessGroupId)
	require.NotEmpty(t, grpID)
	assert.NotEmpty(t, aws.ToString(grp.VerifiedAccessGroup.VerifiedAccessGroupArn))
	assert.Equal(t, instID, aws.ToString(grp.VerifiedAccessGroup.VerifiedAccessInstanceId))
	defer func() {
		_, _ = c.DeleteVerifiedAccessGroup(ctx, &ec2.DeleteVerifiedAccessGroupInput{VerifiedAccessGroupId: aws.String(grpID)})
	}()

	descG, err := c.DescribeVerifiedAccessGroups(ctx, &ec2.DescribeVerifiedAccessGroupsInput{
		VerifiedAccessGroupIds: []string{grpID},
	})
	require.NoError(t, err)
	require.Len(t, descG.VerifiedAccessGroups, 1)
	assert.Equal(t, "grp-a", aws.ToString(descG.VerifiedAccessGroups[0].Description))

	gp, err := c.GetVerifiedAccessGroupPolicy(ctx, &ec2.GetVerifiedAccessGroupPolicyInput{
		VerifiedAccessGroupId: aws.String(grpID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(gp.PolicyEnabled))
	assert.Equal(t, groupPolicy, aws.ToString(gp.PolicyDocument))

	const groupPolicy2 = `forbid(principal, action, resource);`
	mgp, err := c.ModifyVerifiedAccessGroupPolicy(ctx, &ec2.ModifyVerifiedAccessGroupPolicyInput{
		VerifiedAccessGroupId: aws.String(grpID),
		PolicyEnabled:         aws.Bool(true),
		PolicyDocument:        aws.String(groupPolicy2),
	})
	require.NoError(t, err)
	assert.Equal(t, groupPolicy2, aws.ToString(mgp.PolicyDocument))

	_, err = c.ModifyVerifiedAccessGroup(ctx, &ec2.ModifyVerifiedAccessGroupInput{
		VerifiedAccessGroupId: aws.String(grpID),
		Description:           aws.String("grp-a-updated"),
	})
	require.NoError(t, err)

	// ---- Endpoint ----
	const endpointPolicy = `permit(principal, action, resource) when { true };`
	ep, err := c.CreateVerifiedAccessEndpoint(ctx, &ec2.CreateVerifiedAccessEndpointInput{
		VerifiedAccessGroupId: aws.String(grpID),
		EndpointType:          types.VerifiedAccessEndpointTypeLoadBalancer,
		AttachmentType:        types.VerifiedAccessEndpointAttachmentTypeVpc,
		ApplicationDomain:     aws.String("app.example.com"),
		EndpointDomainPrefix:  aws.String("my-app"),
		DomainCertificateArn:  aws.String("arn:aws:acm:us-east-1:000000000000:certificate/abc"),
		SecurityGroupIds:      []string{"sg-1234567890abcdef0"},
		Description:           aws.String("ep-a"),
		PolicyDocument:        aws.String(endpointPolicy),
	})
	require.NoError(t, err)
	epID := aws.ToString(ep.VerifiedAccessEndpoint.VerifiedAccessEndpointId)
	require.NotEmpty(t, epID)
	assert.Equal(t, grpID, aws.ToString(ep.VerifiedAccessEndpoint.VerifiedAccessGroupId))
	assert.Equal(t, instID, aws.ToString(ep.VerifiedAccessEndpoint.VerifiedAccessInstanceId))
	assert.Equal(t, types.VerifiedAccessEndpointTypeLoadBalancer, ep.VerifiedAccessEndpoint.EndpointType)
	defer func() {
		_, _ = c.DeleteVerifiedAccessEndpoint(ctx, &ec2.DeleteVerifiedAccessEndpointInput{VerifiedAccessEndpointId: aws.String(epID)})
	}()

	descE, err := c.DescribeVerifiedAccessEndpoints(ctx, &ec2.DescribeVerifiedAccessEndpointsInput{
		VerifiedAccessEndpointIds: []string{epID},
	})
	require.NoError(t, err)
	require.Len(t, descE.VerifiedAccessEndpoints, 1)
	assert.Equal(t, "my-app.app.example.com", aws.ToString(descE.VerifiedAccessEndpoints[0].EndpointDomain))
	require.Len(t, descE.VerifiedAccessEndpoints[0].SecurityGroupIds, 1)

	gep, err := c.GetVerifiedAccessEndpointPolicy(ctx, &ec2.GetVerifiedAccessEndpointPolicyInput{
		VerifiedAccessEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	assert.True(t, aws.ToBool(gep.PolicyEnabled))
	assert.Equal(t, endpointPolicy, aws.ToString(gep.PolicyDocument))

	const endpointPolicy2 = `forbid(principal, action, resource);`
	mep, err := c.ModifyVerifiedAccessEndpointPolicy(ctx, &ec2.ModifyVerifiedAccessEndpointPolicyInput{
		VerifiedAccessEndpointId: aws.String(epID),
		PolicyEnabled:            aws.Bool(true),
		PolicyDocument:           aws.String(endpointPolicy2),
	})
	require.NoError(t, err)
	assert.Equal(t, endpointPolicy2, aws.ToString(mep.PolicyDocument))

	_, err = c.ModifyVerifiedAccessEndpoint(ctx, &ec2.ModifyVerifiedAccessEndpointInput{
		VerifiedAccessEndpointId: aws.String(epID),
		Description:              aws.String("ep-a-updated"),
	})
	require.NoError(t, err)

	tgts, err := c.GetVerifiedAccessEndpointTargets(ctx, &ec2.GetVerifiedAccessEndpointTargetsInput{
		VerifiedAccessEndpointId: aws.String(epID),
	})
	require.NoError(t, err)
	require.Len(t, tgts.VerifiedAccessEndpointTargets, 1)
	assert.Equal(t, epID, aws.ToString(tgts.VerifiedAccessEndpointTargets[0].VerifiedAccessEndpointId))
}

// TestEC2_VerifiedAccessLoggingSDK covers the per-instance access-log
// configuration and the client-config export op.
func TestEC2_VerifiedAccessLoggingSDK(t *testing.T) {
	c := ec2Client()

	inst, err := c.CreateVerifiedAccessInstance(ctx, &ec2.CreateVerifiedAccessInstanceInput{
		Description: aws.String("vai-logging"),
	})
	require.NoError(t, err)
	instID := aws.ToString(inst.VerifiedAccessInstance.VerifiedAccessInstanceId)
	defer func() {
		_, _ = c.DeleteVerifiedAccessInstance(ctx, &ec2.DeleteVerifiedAccessInstanceInput{VerifiedAccessInstanceId: aws.String(instID)})
	}()

	mlc, err := c.ModifyVerifiedAccessInstanceLoggingConfiguration(ctx, &ec2.ModifyVerifiedAccessInstanceLoggingConfigurationInput{
		VerifiedAccessInstanceId: aws.String(instID),
		AccessLogs: &types.VerifiedAccessLogOptions{
			S3: &types.VerifiedAccessLogS3DestinationOptions{
				Enabled:    aws.Bool(true),
				BucketName: aws.String("va-logs"),
				Prefix:     aws.String("logs/"),
			},
			IncludeTrustContext: aws.Bool(true),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, mlc.LoggingConfiguration)
	require.NotNil(t, mlc.LoggingConfiguration.AccessLogs)
	require.NotNil(t, mlc.LoggingConfiguration.AccessLogs.S3)
	assert.True(t, aws.ToBool(mlc.LoggingConfiguration.AccessLogs.S3.Enabled))
	assert.Equal(t, "va-logs", aws.ToString(mlc.LoggingConfiguration.AccessLogs.S3.BucketName))

	desc, err := c.DescribeVerifiedAccessInstanceLoggingConfigurations(ctx, &ec2.DescribeVerifiedAccessInstanceLoggingConfigurationsInput{
		VerifiedAccessInstanceIds: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, desc.LoggingConfigurations, 1)
	require.NotNil(t, desc.LoggingConfigurations[0].AccessLogs)
	assert.True(t, aws.ToBool(desc.LoggingConfigurations[0].AccessLogs.S3.Enabled))

	exp, err := c.ExportVerifiedAccessInstanceClientConfiguration(ctx, &ec2.ExportVerifiedAccessInstanceClientConfigurationInput{
		VerifiedAccessInstanceId: aws.String(instID),
	})
	require.NoError(t, err)
	assert.Equal(t, instID, aws.ToString(exp.VerifiedAccessInstanceId))
	assert.NotEmpty(t, aws.ToString(exp.Region))
}

// TestEC2_TrafficMirrorLifecycleSDK exercises EC2 Traffic Mirroring end-to-end:
// a target, a filter with an ingress rule + mirrored network services, and a
// session binding a source ENI to the target through the filter.
func TestEC2_TrafficMirrorLifecycleSDK(t *testing.T) {
	c := ec2Client()

	// ---- Target ----
	tgt, err := c.CreateTrafficMirrorTarget(ctx, &ec2.CreateTrafficMirrorTargetInput{
		NetworkInterfaceId: aws.String("eni-1234567890abcdef0"),
		Description:        aws.String("tmt-a"),
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeTrafficMirrorTarget,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String("tmt-a")}},
		}},
	})
	require.NoError(t, err)
	tgtID := aws.ToString(tgt.TrafficMirrorTarget.TrafficMirrorTargetId)
	require.NotEmpty(t, tgtID)
	assert.Equal(t, types.TrafficMirrorTargetTypeNetworkInterface, tgt.TrafficMirrorTarget.Type)
	defer func() {
		_, _ = c.DeleteTrafficMirrorTarget(ctx, &ec2.DeleteTrafficMirrorTargetInput{TrafficMirrorTargetId: aws.String(tgtID)})
	}()

	descT, err := c.DescribeTrafficMirrorTargets(ctx, &ec2.DescribeTrafficMirrorTargetsInput{
		TrafficMirrorTargetIds: []string{tgtID},
	})
	require.NoError(t, err)
	require.Len(t, descT.TrafficMirrorTargets, 1)
	assert.Equal(t, "eni-1234567890abcdef0", aws.ToString(descT.TrafficMirrorTargets[0].NetworkInterfaceId))

	// ---- Filter + rule + network services ----
	flt, err := c.CreateTrafficMirrorFilter(ctx, &ec2.CreateTrafficMirrorFilterInput{
		Description: aws.String("tmf-a"),
	})
	require.NoError(t, err)
	fltID := aws.ToString(flt.TrafficMirrorFilter.TrafficMirrorFilterId)
	require.NotEmpty(t, fltID)
	defer func() {
		_, _ = c.DeleteTrafficMirrorFilter(ctx, &ec2.DeleteTrafficMirrorFilterInput{TrafficMirrorFilterId: aws.String(fltID)})
	}()

	rule, err := c.CreateTrafficMirrorFilterRule(ctx, &ec2.CreateTrafficMirrorFilterRuleInput{
		TrafficMirrorFilterId: aws.String(fltID),
		TrafficDirection:      types.TrafficDirectionIngress,
		RuleNumber:            aws.Int32(100),
		RuleAction:            types.TrafficMirrorRuleActionAccept,
		Protocol:              aws.Int32(6),
		DestinationCidrBlock:  aws.String("10.0.0.0/24"),
		SourceCidrBlock:       aws.String("0.0.0.0/0"),
		DestinationPortRange: &types.TrafficMirrorPortRangeRequest{
			FromPort: aws.Int32(80),
			ToPort:   aws.Int32(80),
		},
		Description: aws.String("rule-a"),
	})
	require.NoError(t, err)
	ruleID := aws.ToString(rule.TrafficMirrorFilterRule.TrafficMirrorFilterRuleId)
	require.NotEmpty(t, ruleID)
	assert.Equal(t, int32(100), aws.ToInt32(rule.TrafficMirrorFilterRule.RuleNumber))
	require.NotNil(t, rule.TrafficMirrorFilterRule.DestinationPortRange)
	assert.Equal(t, int32(80), aws.ToInt32(rule.TrafficMirrorFilterRule.DestinationPortRange.FromPort))

	_, err = c.ModifyTrafficMirrorFilterRule(ctx, &ec2.ModifyTrafficMirrorFilterRuleInput{
		TrafficMirrorFilterRuleId: aws.String(ruleID),
		Description:               aws.String("rule-a-updated"),
		RuleNumber:                aws.Int32(200),
	})
	require.NoError(t, err)

	descR, err := c.DescribeTrafficMirrorFilterRules(ctx, &ec2.DescribeTrafficMirrorFilterRulesInput{
		TrafficMirrorFilterId: aws.String(fltID),
	})
	require.NoError(t, err)
	require.Len(t, descR.TrafficMirrorFilterRules, 1)
	assert.Equal(t, int32(200), aws.ToInt32(descR.TrafficMirrorFilterRules[0].RuleNumber))

	mns, err := c.ModifyTrafficMirrorFilterNetworkServices(ctx, &ec2.ModifyTrafficMirrorFilterNetworkServicesInput{
		TrafficMirrorFilterId: aws.String(fltID),
		AddNetworkServices:    []types.TrafficMirrorNetworkService{types.TrafficMirrorNetworkServiceAmazonDns},
	})
	require.NoError(t, err)
	require.Len(t, mns.TrafficMirrorFilter.NetworkServices, 1)
	assert.Equal(t, types.TrafficMirrorNetworkServiceAmazonDns, mns.TrafficMirrorFilter.NetworkServices[0])

	descF, err := c.DescribeTrafficMirrorFilters(ctx, &ec2.DescribeTrafficMirrorFiltersInput{
		TrafficMirrorFilterIds: []string{fltID},
	})
	require.NoError(t, err)
	require.Len(t, descF.TrafficMirrorFilters, 1)
	require.Len(t, descF.TrafficMirrorFilters[0].IngressFilterRules, 1)
	require.Len(t, descF.TrafficMirrorFilters[0].NetworkServices, 1)

	// ---- Session ----
	sess, err := c.CreateTrafficMirrorSession(ctx, &ec2.CreateTrafficMirrorSessionInput{
		TrafficMirrorTargetId: aws.String(tgtID),
		TrafficMirrorFilterId: aws.String(fltID),
		NetworkInterfaceId:    aws.String("eni-aaaaaaaaaaaaaaaaa"),
		SessionNumber:         aws.Int32(1),
		PacketLength:          aws.Int32(8500),
		VirtualNetworkId:      aws.Int32(42),
		Description:           aws.String("tms-a"),
	})
	require.NoError(t, err)
	sessID := aws.ToString(sess.TrafficMirrorSession.TrafficMirrorSessionId)
	require.NotEmpty(t, sessID)
	assert.Equal(t, int32(42), aws.ToInt32(sess.TrafficMirrorSession.VirtualNetworkId))
	assert.Equal(t, int32(8500), aws.ToInt32(sess.TrafficMirrorSession.PacketLength))
	defer func() {
		_, _ = c.DeleteTrafficMirrorSession(ctx, &ec2.DeleteTrafficMirrorSessionInput{TrafficMirrorSessionId: aws.String(sessID)})
	}()

	descS, err := c.DescribeTrafficMirrorSessions(ctx, &ec2.DescribeTrafficMirrorSessionsInput{
		TrafficMirrorSessionIds: []string{sessID},
	})
	require.NoError(t, err)
	require.Len(t, descS.TrafficMirrorSessions, 1)
	assert.Equal(t, tgtID, aws.ToString(descS.TrafficMirrorSessions[0].TrafficMirrorTargetId))

	_, err = c.ModifyTrafficMirrorSession(ctx, &ec2.ModifyTrafficMirrorSessionInput{
		TrafficMirrorSessionId: aws.String(sessID),
		Description:            aws.String("tms-a-updated"),
		SessionNumber:          aws.Int32(2),
	})
	require.NoError(t, err)

	_, err = c.DeleteTrafficMirrorFilterRule(ctx, &ec2.DeleteTrafficMirrorFilterRuleInput{
		TrafficMirrorFilterRuleId: aws.String(ruleID),
	})
	require.NoError(t, err)
}
