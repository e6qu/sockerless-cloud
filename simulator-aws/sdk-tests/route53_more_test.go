package aws_sdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r53MoreZone creates a hosted zone and returns its bare id, registering a
// tolerant cleanup that deletes the zone after stripping any user records.
func r53MoreZone(t *testing.T, c *route53.Client, ctx context.Context, name string) string {
	t.Helper()
	caller := "sdk-more-" + time.Now().Format("150405.000000000")
	out, err := c.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name:            aws.String(name),
		CallerReference: aws.String(caller),
	})
	require.NoError(t, err)
	id := strings.TrimPrefix(aws.ToString(out.HostedZone.Id), "/hostedzone/")
	t.Cleanup(func() {
		_, _ = c.DeleteHostedZone(context.Background(), &route53.DeleteHostedZoneInput{Id: aws.String(id)})
	})
	return id
}

func TestRoute53_ReusableDelegationSets(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	caller := "sdk-ds-" + time.Now().Format("150405.000000000")
	createOut, err := c.CreateReusableDelegationSet(ctx, &route53.CreateReusableDelegationSetInput{
		CallerReference: aws.String(caller),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.DelegationSet)
	id := strings.TrimPrefix(aws.ToString(createOut.DelegationSet.Id), "/delegationset/")
	require.NotEmpty(t, id)
	assert.Len(t, createOut.DelegationSet.NameServers, 4)
	t.Cleanup(func() {
		_, _ = c.DeleteReusableDelegationSet(context.Background(), &route53.DeleteReusableDelegationSetInput{Id: aws.String(id)})
	})

	getOut, err := c.GetReusableDelegationSet(ctx, &route53.GetReusableDelegationSetInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, caller, aws.ToString(getOut.DelegationSet.CallerReference))

	listOut, err := c.ListReusableDelegationSets(ctx, &route53.ListReusableDelegationSetsInput{})
	require.NoError(t, err)
	found := false
	for _, ds := range listOut.DelegationSets {
		if strings.TrimPrefix(aws.ToString(ds.Id), "/delegationset/") == id {
			found = true
		}
	}
	assert.True(t, found, "expected delegation set %q in list", id)

	limitOut, err := c.GetReusableDelegationSetLimit(ctx, &route53.GetReusableDelegationSetLimitInput{
		DelegationSetId: aws.String(id),
		Type:            r53types.ReusableDelegationSetLimitTypeMaxZonesByReusableDelegationSet,
	})
	require.NoError(t, err)
	require.NotNil(t, limitOut.Limit)
	assert.Equal(t, int64(100), aws.ToInt64(limitOut.Limit.Value))

	_, err = c.DeleteReusableDelegationSet(ctx, &route53.DeleteReusableDelegationSetInput{Id: aws.String(id)})
	require.NoError(t, err)
}

func TestRoute53_CidrCollections(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	caller := "sdk-cidr-" + time.Now().Format("150405.000000000")
	name := "sdk-cidr-coll-" + time.Now().Format("150405")
	createOut, err := c.CreateCidrCollection(ctx, &route53.CreateCidrCollectionInput{
		Name:            aws.String(name),
		CallerReference: aws.String(caller),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.Collection)
	id := aws.ToString(createOut.Collection.Id)
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		// Remove blocks first so the collection becomes deletable.
		_, _ = c.ChangeCidrCollection(context.Background(), &route53.ChangeCidrCollectionInput{
			Id: aws.String(id),
			Changes: []r53types.CidrCollectionChange{{
				LocationName: aws.String("us-west"),
				Action:       r53types.CidrCollectionChangeActionDeleteIfExists,
				CidrList:     []string{"192.0.2.0/24"},
			}},
		})
		_, _ = c.DeleteCidrCollection(context.Background(), &route53.DeleteCidrCollectionInput{Id: aws.String(id)})
	})

	_, err = c.ChangeCidrCollection(ctx, &route53.ChangeCidrCollectionInput{
		Id: aws.String(id),
		Changes: []r53types.CidrCollectionChange{{
			LocationName: aws.String("us-west"),
			Action:       r53types.CidrCollectionChangeActionPut,
			CidrList:     []string{"192.0.2.0/24"},
		}},
	})
	require.NoError(t, err)

	listColls, err := c.ListCidrCollections(ctx, &route53.ListCidrCollectionsInput{})
	require.NoError(t, err)
	foundColl := false
	for _, cc := range listColls.CidrCollections {
		if aws.ToString(cc.Id) == id {
			foundColl = true
		}
	}
	assert.True(t, foundColl, "expected cidr collection %q in list", id)

	locsOut, err := c.ListCidrLocations(ctx, &route53.ListCidrLocationsInput{CollectionId: aws.String(id)})
	require.NoError(t, err)
	foundLoc := false
	for _, l := range locsOut.CidrLocations {
		if aws.ToString(l.LocationName) == "us-west" {
			foundLoc = true
		}
	}
	assert.True(t, foundLoc, "expected us-west location")

	blocksOut, err := c.ListCidrBlocks(ctx, &route53.ListCidrBlocksInput{CollectionId: aws.String(id)})
	require.NoError(t, err)
	require.Len(t, blocksOut.CidrBlocks, 1)
	assert.Equal(t, "192.0.2.0/24", aws.ToString(blocksOut.CidrBlocks[0].CidrBlock))
}

func TestRoute53_DNSSECKeySigningKeys(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zoneID := r53MoreZone(t, c, ctx, "sdk-dnssec.local")
	kskName := "sdk_ksk_" + time.Now().Format("150405")
	kmsArn := "arn:aws:kms:us-east-1:123456789012:key/sdk-route53-dnssec"

	createOut, err := c.CreateKeySigningKey(ctx, &route53.CreateKeySigningKeyInput{
		CallerReference:         aws.String("sdk-ksk-" + time.Now().Format("150405.000000000")),
		HostedZoneId:            aws.String(zoneID),
		KeyManagementServiceArn: aws.String(kmsArn),
		Name:                    aws.String(kskName),
		Status:                  aws.String("ACTIVE"),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.KeySigningKey)
	assert.Equal(t, kskName, aws.ToString(createOut.KeySigningKey.Name))

	_, err = c.EnableHostedZoneDNSSEC(ctx, &route53.EnableHostedZoneDNSSECInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)

	dnssec, err := c.GetDNSSEC(ctx, &route53.GetDNSSECInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	require.NotNil(t, dnssec.Status)
	assert.Equal(t, "SIGNING", aws.ToString(dnssec.Status.ServeSignature))
	require.Len(t, dnssec.KeySigningKeys, 1)

	_, err = c.DeactivateKeySigningKey(ctx, &route53.DeactivateKeySigningKeyInput{HostedZoneId: aws.String(zoneID), Name: aws.String(kskName)})
	require.NoError(t, err)
	_, err = c.ActivateKeySigningKey(ctx, &route53.ActivateKeySigningKeyInput{HostedZoneId: aws.String(zoneID), Name: aws.String(kskName)})
	require.NoError(t, err)

	_, err = c.DisableHostedZoneDNSSEC(ctx, &route53.DisableHostedZoneDNSSECInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	_, err = c.DeleteKeySigningKey(ctx, &route53.DeleteKeySigningKeyInput{HostedZoneId: aws.String(zoneID), Name: aws.String(kskName)})
	require.NoError(t, err)
}

func TestRoute53_TrafficPolicyInstances(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zoneID := r53MoreZone(t, c, ctx, "sdk-tpi.local")

	doc := `{"AWSPolicyFormatVersion":"2015-10-01","RecordType":"A","Endpoints":{"e":{"Type":"value","Value":"192.0.2.1"}},"StartEndpoint":"e"}`
	tpOut, err := c.CreateTrafficPolicy(ctx, &route53.CreateTrafficPolicyInput{
		Name:     aws.String("sdk-tp-" + time.Now().Format("150405")),
		Document: aws.String(doc),
	})
	require.NoError(t, err)
	tpID := aws.ToString(tpOut.TrafficPolicy.Id)
	tpVer := aws.ToInt32(tpOut.TrafficPolicy.Version)
	t.Cleanup(func() {
		_, _ = c.DeleteTrafficPolicy(context.Background(), &route53.DeleteTrafficPolicyInput{Id: aws.String(tpID), Version: aws.Int32(tpVer)})
	})

	// UpdateTrafficPolicyComment
	commentOut, err := c.UpdateTrafficPolicyComment(ctx, &route53.UpdateTrafficPolicyCommentInput{
		Id:      aws.String(tpID),
		Version: aws.Int32(tpVer),
		Comment: aws.String("updated by sdk test"),
	})
	require.NoError(t, err)
	assert.Equal(t, "updated by sdk test", aws.ToString(commentOut.TrafficPolicy.Comment))

	createOut, err := c.CreateTrafficPolicyInstance(ctx, &route53.CreateTrafficPolicyInstanceInput{
		HostedZoneId:         aws.String(zoneID),
		Name:                 aws.String("svc.sdk-tpi.local"),
		TTL:                  aws.Int64(300),
		TrafficPolicyId:      aws.String(tpID),
		TrafficPolicyVersion: aws.Int32(tpVer),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.TrafficPolicyInstance)
	instID := aws.ToString(createOut.TrafficPolicyInstance.Id)
	require.NotEmpty(t, instID)
	t.Cleanup(func() {
		_, _ = c.DeleteTrafficPolicyInstance(context.Background(), &route53.DeleteTrafficPolicyInstanceInput{Id: aws.String(instID)})
	})

	getOut, err := c.GetTrafficPolicyInstance(ctx, &route53.GetTrafficPolicyInstanceInput{Id: aws.String(instID)})
	require.NoError(t, err)
	assert.Equal(t, zoneID, aws.ToString(getOut.TrafficPolicyInstance.HostedZoneId))

	updOut, err := c.UpdateTrafficPolicyInstance(ctx, &route53.UpdateTrafficPolicyInstanceInput{
		Id:                   aws.String(instID),
		TTL:                  aws.Int64(600),
		TrafficPolicyId:      aws.String(tpID),
		TrafficPolicyVersion: aws.Int32(tpVer),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(600), aws.ToInt64(updOut.TrafficPolicyInstance.TTL))

	listOut, err := c.ListTrafficPolicyInstances(ctx, &route53.ListTrafficPolicyInstancesInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(listOut.TrafficPolicyInstances), 1)

	byZone, err := c.ListTrafficPolicyInstancesByHostedZone(ctx, &route53.ListTrafficPolicyInstancesByHostedZoneInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	require.Len(t, byZone.TrafficPolicyInstances, 1)

	byPolicy, err := c.ListTrafficPolicyInstancesByPolicy(ctx, &route53.ListTrafficPolicyInstancesByPolicyInput{
		TrafficPolicyId:      aws.String(tpID),
		TrafficPolicyVersion: aws.Int32(tpVer),
	})
	require.NoError(t, err)
	require.Len(t, byPolicy.TrafficPolicyInstances, 1)

	countOut, err := c.GetTrafficPolicyInstanceCount(ctx, &route53.GetTrafficPolicyInstanceCountInput{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, aws.ToInt32(countOut.TrafficPolicyInstanceCount), int32(1))
}

func TestRoute53_VPCAssociationAuthorizations(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zoneID := r53MoreZone(t, c, ctx, "sdk-vpcauthz.local")
	vpc := &r53types.VPC{VPCRegion: r53types.VPCRegionUsEast1, VPCId: aws.String("vpc-sdk0123456789")}

	createOut, err := c.CreateVPCAssociationAuthorization(ctx, &route53.CreateVPCAssociationAuthorizationInput{
		HostedZoneId: aws.String(zoneID),
		VPC:          vpc,
	})
	require.NoError(t, err)
	assert.Equal(t, "vpc-sdk0123456789", aws.ToString(createOut.VPC.VPCId))

	listOut, err := c.ListVPCAssociationAuthorizations(ctx, &route53.ListVPCAssociationAuthorizationsInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	require.Len(t, listOut.VPCs, 1)
	assert.Equal(t, "vpc-sdk0123456789", aws.ToString(listOut.VPCs[0].VPCId))

	_, err = c.DeleteVPCAssociationAuthorization(ctx, &route53.DeleteVPCAssociationAuthorizationInput{
		HostedZoneId: aws.String(zoneID),
		VPC:          vpc,
	})
	require.NoError(t, err)

	listOut2, err := c.ListVPCAssociationAuthorizations(ctx, &route53.ListVPCAssociationAuthorizationsInput{HostedZoneId: aws.String(zoneID)})
	require.NoError(t, err)
	assert.Empty(t, listOut2.VPCs)
}

func TestRoute53_LimitsAndCheckerRanges(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zoneID := r53MoreZone(t, c, ctx, "sdk-limits.local")

	acctOut, err := c.GetAccountLimit(ctx, &route53.GetAccountLimitInput{Type: r53types.AccountLimitTypeMaxHostedZonesByOwner})
	require.NoError(t, err)
	require.NotNil(t, acctOut.Limit)
	assert.Equal(t, int64(500), aws.ToInt64(acctOut.Limit.Value))

	zoneLimit, err := c.GetHostedZoneLimit(ctx, &route53.GetHostedZoneLimitInput{
		HostedZoneId: aws.String(zoneID),
		Type:         r53types.HostedZoneLimitTypeMaxRrsetsByZone,
	})
	require.NoError(t, err)
	require.NotNil(t, zoneLimit.Limit)
	assert.Equal(t, int64(10000), aws.ToInt64(zoneLimit.Limit.Value))

	ranges, err := c.GetCheckerIpRanges(ctx, &route53.GetCheckerIpRangesInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, ranges.CheckerIpRanges)
}

func TestRoute53_HealthCheckLastFailureAndTags(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	hcOut, err := c.CreateHealthCheck(ctx, &route53.CreateHealthCheckInput{
		CallerReference: aws.String("sdk-hc-fail-" + time.Now().Format("150405.000000000")),
		HealthCheckConfig: &r53types.HealthCheckConfig{
			IPAddress:        aws.String("192.0.2.10"),
			Port:             aws.Int32(80),
			Type:             r53types.HealthCheckTypeHttp,
			ResourcePath:     aws.String("/"),
			RequestInterval:  aws.Int32(30),
			FailureThreshold: aws.Int32(3),
		},
	})
	require.NoError(t, err)
	hcID := aws.ToString(hcOut.HealthCheck.Id)
	t.Cleanup(func() {
		_, _ = c.DeleteHealthCheck(context.Background(), &route53.DeleteHealthCheckInput{HealthCheckId: aws.String(hcID)})
	})

	lastFail, err := c.GetHealthCheckLastFailureReason(ctx, &route53.GetHealthCheckLastFailureReasonInput{HealthCheckId: aws.String(hcID)})
	require.NoError(t, err)
	assert.NotNil(t, lastFail.HealthCheckObservations)

	// Tag the health check then read it via the batched ListTagsForResources.
	_, err = c.ChangeTagsForResource(ctx, &route53.ChangeTagsForResourceInput{
		ResourceType: r53types.TagResourceTypeHealthcheck,
		ResourceId:   aws.String(hcID),
		AddTags:      []r53types.Tag{{Key: aws.String("env"), Value: aws.String("sdk")}},
	})
	require.NoError(t, err)

	tagsOut, err := c.ListTagsForResources(ctx, &route53.ListTagsForResourcesInput{
		ResourceType: r53types.TagResourceTypeHealthcheck,
		ResourceIds:  []string{hcID},
	})
	require.NoError(t, err)
	require.Len(t, tagsOut.ResourceTagSets, 1)
	require.Len(t, tagsOut.ResourceTagSets[0].Tags, 1)
	assert.Equal(t, "env", aws.ToString(tagsOut.ResourceTagSets[0].Tags[0].Key))
}

func TestRoute53_TestDNSAnswerAndZoneUpdates(t *testing.T) {
	c := r53Client()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	zoneID := r53MoreZone(t, c, ctx, "sdk-dnsanswer.local")

	// Create an A record we can resolve via TestDNSAnswer.
	_, err := c.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(zoneID),
		ChangeBatch: &r53types.ChangeBatch{
			Changes: []r53types.Change{{
				Action: r53types.ChangeActionCreate,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name:            aws.String("www.sdk-dnsanswer.local."),
					Type:            r53types.RRTypeA,
					TTL:             aws.Int64(300),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("192.0.2.44")}},
				},
			}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.ChangeResourceRecordSets(context.Background(), &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: aws.String(zoneID),
			ChangeBatch: &r53types.ChangeBatch{
				Changes: []r53types.Change{{
					Action: r53types.ChangeActionDelete,
					ResourceRecordSet: &r53types.ResourceRecordSet{
						Name:            aws.String("www.sdk-dnsanswer.local."),
						Type:            r53types.RRTypeA,
						TTL:             aws.Int64(300),
						ResourceRecords: []r53types.ResourceRecord{{Value: aws.String("192.0.2.44")}},
					},
				}},
			},
		})
	})

	answer, err := c.TestDNSAnswer(ctx, &route53.TestDNSAnswerInput{
		HostedZoneId: aws.String(zoneID),
		RecordName:   aws.String("www.sdk-dnsanswer.local"),
		RecordType:   r53types.RRTypeA,
	})
	require.NoError(t, err)
	assert.Equal(t, "NOERROR", aws.ToString(answer.ResponseCode))
	require.Contains(t, answer.RecordData, "192.0.2.44")

	commentOut, err := c.UpdateHostedZoneComment(ctx, &route53.UpdateHostedZoneCommentInput{
		Id:      aws.String(zoneID),
		Comment: aws.String("updated comment via sdk"),
	})
	require.NoError(t, err)
	require.NotNil(t, commentOut.HostedZone.Config)
	assert.Equal(t, "updated comment via sdk", aws.ToString(commentOut.HostedZone.Config.Comment))

	_, err = c.UpdateHostedZoneFeatures(ctx, &route53.UpdateHostedZoneFeaturesInput{
		HostedZoneId:              aws.String(zoneID),
		EnableAcceleratedRecovery: aws.Bool(true),
	})
	require.NoError(t, err)
}
