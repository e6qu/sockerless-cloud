package aws_sdk_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cfTestPublicKeyPEM is a placeholder RSA public key in PEM form used to seed a
// CloudFront public key that an FLE profile references.
const cfTestPublicKeyPEM = "-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtest\n-----END PUBLIC KEY-----\n"

// caller returns a unique CallerReference for an SDK round-trip.
func cfExtras2Caller() string {
	return "sdk-extras2-" + time.Now().Format("150405.000000000")
}

// TestCloudFront_FieldLevelEncryption_Lifecycle exercises the field-level
// encryption config CRUD (create → get → get-config → update → list → delete),
// asserting the ETag round-trips as the If-Match precondition.
func TestCloudFront_FieldLevelEncryption_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	caller := cfExtras2Caller()
	create, err := c.CreateFieldLevelEncryptionConfig(ctx, &cloudfront.CreateFieldLevelEncryptionConfigInput{
		FieldLevelEncryptionConfig: &cftypes.FieldLevelEncryptionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk fle config"),
			QueryArgProfileConfig: &cftypes.QueryArgProfileConfig{
				ForwardWhenQueryArgProfileIsUnknown: aws.Bool(true),
			},
			ContentTypeProfileConfig: &cftypes.ContentTypeProfileConfig{
				ForwardWhenContentTypeIsUnknown: aws.Bool(true),
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, create.FieldLevelEncryption)
	id := aws.ToString(create.FieldLevelEncryption.Id)
	require.NotEmpty(t, id)
	etag := aws.ToString(create.ETag)
	require.NotEmpty(t, etag)
	defer func() {
		get, gerr := c.GetFieldLevelEncryption(context.Background(), &cloudfront.GetFieldLevelEncryptionInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteFieldLevelEncryptionConfig(context.Background(), &cloudfront.DeleteFieldLevelEncryptionConfigInput{Id: aws.String(id), IfMatch: get.ETag})
		}
	}()

	get, err := c.GetFieldLevelEncryption(ctx, &cloudfront.GetFieldLevelEncryptionInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, caller, aws.ToString(get.FieldLevelEncryption.FieldLevelEncryptionConfig.CallerReference))
	assert.Equal(t, etag, aws.ToString(get.ETag))

	gc, err := c.GetFieldLevelEncryptionConfig(ctx, &cloudfront.GetFieldLevelEncryptionConfigInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "sdk fle config", aws.ToString(gc.FieldLevelEncryptionConfig.Comment))

	upd, err := c.UpdateFieldLevelEncryptionConfig(ctx, &cloudfront.UpdateFieldLevelEncryptionConfigInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
		FieldLevelEncryptionConfig: &cftypes.FieldLevelEncryptionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk fle config updated"),
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk fle config updated", aws.ToString(upd.FieldLevelEncryption.FieldLevelEncryptionConfig.Comment))
	newETag := aws.ToString(upd.ETag)

	// Stale If-Match must fail the precondition.
	_, err = c.DeleteFieldLevelEncryptionConfig(ctx, &cloudfront.DeleteFieldLevelEncryptionConfigInput{Id: aws.String(id), IfMatch: aws.String(etag)})
	require.Error(t, err)

	list, err := c.ListFieldLevelEncryptionConfigs(ctx, &cloudfront.ListFieldLevelEncryptionConfigsInput{})
	require.NoError(t, err)
	require.NotNil(t, list.FieldLevelEncryptionList)
	found := false
	for _, s := range list.FieldLevelEncryptionList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found, "created config should appear in list")

	_, err = c.DeleteFieldLevelEncryptionConfig(ctx, &cloudfront.DeleteFieldLevelEncryptionConfigInput{Id: aws.String(id), IfMatch: aws.String(newETag)})
	require.NoError(t, err)
}

// TestCloudFront_FieldLevelEncryptionProfile_Lifecycle exercises the FLE
// profile CRUD round-trip.
func TestCloudFront_FieldLevelEncryptionProfile_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// A profile references a public key; create one first.
	pkCaller := cfExtras2Caller()
	pk, err := c.CreatePublicKey(ctx, &cloudfront.CreatePublicKeyInput{
		PublicKeyConfig: &cftypes.PublicKeyConfig{
			CallerReference: aws.String(pkCaller),
			Name:            aws.String("fle-pk-" + time.Now().Format("150405.000")),
			EncodedKey:      aws.String(cfTestPublicKeyPEM),
		},
	})
	require.NoError(t, err)
	pkID := aws.ToString(pk.PublicKey.Id)
	defer func() {
		g, gerr := c.GetPublicKey(context.Background(), &cloudfront.GetPublicKeyInput{Id: aws.String(pkID)})
		if gerr == nil {
			_, _ = c.DeletePublicKey(context.Background(), &cloudfront.DeletePublicKeyInput{Id: aws.String(pkID), IfMatch: g.ETag})
		}
	}()

	caller := cfExtras2Caller()
	create, err := c.CreateFieldLevelEncryptionProfile(ctx, &cloudfront.CreateFieldLevelEncryptionProfileInput{
		FieldLevelEncryptionProfileConfig: &cftypes.FieldLevelEncryptionProfileConfig{
			Name:            aws.String("fle-profile-" + time.Now().Format("150405.000")),
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk fle profile"),
			EncryptionEntities: &cftypes.EncryptionEntities{
				Quantity: aws.Int32(1),
				Items: []cftypes.EncryptionEntity{
					{
						PublicKeyId: aws.String(pkID),
						ProviderId:  aws.String("provider-1"),
						FieldPatterns: &cftypes.FieldPatterns{
							Quantity: aws.Int32(1),
							Items:    []string{"SSN"},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	id := aws.ToString(create.FieldLevelEncryptionProfile.Id)
	require.NotEmpty(t, id)
	etag := aws.ToString(create.ETag)
	defer func() {
		g, gerr := c.GetFieldLevelEncryptionProfile(context.Background(), &cloudfront.GetFieldLevelEncryptionProfileInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteFieldLevelEncryptionProfile(context.Background(), &cloudfront.DeleteFieldLevelEncryptionProfileInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	}()

	get, err := c.GetFieldLevelEncryptionProfile(ctx, &cloudfront.GetFieldLevelEncryptionProfileInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, pkID, aws.ToString(get.FieldLevelEncryptionProfile.FieldLevelEncryptionProfileConfig.EncryptionEntities.Items[0].PublicKeyId))

	gc, err := c.GetFieldLevelEncryptionProfileConfig(ctx, &cloudfront.GetFieldLevelEncryptionProfileConfigInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "sdk fle profile", aws.ToString(gc.FieldLevelEncryptionProfileConfig.Comment))

	upd, err := c.UpdateFieldLevelEncryptionProfile(ctx, &cloudfront.UpdateFieldLevelEncryptionProfileInput{
		Id:      aws.String(id),
		IfMatch: aws.String(etag),
		FieldLevelEncryptionProfileConfig: &cftypes.FieldLevelEncryptionProfileConfig{
			Name:            get.FieldLevelEncryptionProfile.FieldLevelEncryptionProfileConfig.Name,
			CallerReference: aws.String(caller),
			Comment:         aws.String("sdk fle profile updated"),
			EncryptionEntities: &cftypes.EncryptionEntities{
				Quantity: aws.Int32(1),
				Items: []cftypes.EncryptionEntity{
					{
						PublicKeyId:   aws.String(pkID),
						ProviderId:    aws.String("provider-1"),
						FieldPatterns: &cftypes.FieldPatterns{Quantity: aws.Int32(1), Items: []string{"SSN"}},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	newETag := aws.ToString(upd.ETag)

	list, err := c.ListFieldLevelEncryptionProfiles(ctx, &cloudfront.ListFieldLevelEncryptionProfilesInput{})
	require.NoError(t, err)
	require.NotNil(t, list.FieldLevelEncryptionProfileList)
	found := false
	for _, s := range list.FieldLevelEncryptionProfileList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteFieldLevelEncryptionProfile(ctx, &cloudfront.DeleteFieldLevelEncryptionProfileInput{Id: aws.String(id), IfMatch: aws.String(newETag)})
	require.NoError(t, err)
}

// TestCloudFront_KeyValueStore_Lifecycle exercises the key value store CRUD.
func TestCloudFront_KeyValueStore_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "kvs-" + time.Now().Format("150405000")
	create, err := c.CreateKeyValueStore(ctx, &cloudfront.CreateKeyValueStoreInput{
		Name:    aws.String(name),
		Comment: aws.String("sdk kvs"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.KeyValueStore)
	assert.Equal(t, name, aws.ToString(create.KeyValueStore.Name))
	assert.NotEmpty(t, aws.ToString(create.KeyValueStore.ARN))
	etag := aws.ToString(create.ETag)
	require.NotEmpty(t, etag)
	defer func() {
		d, derr := c.DescribeKeyValueStore(context.Background(), &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(name)})
		if derr == nil {
			_, _ = c.DeleteKeyValueStore(context.Background(), &cloudfront.DeleteKeyValueStoreInput{Name: aws.String(name), IfMatch: d.ETag})
		}
	}()

	desc, err := c.DescribeKeyValueStore(ctx, &cloudfront.DescribeKeyValueStoreInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.Equal(t, "sdk kvs", aws.ToString(desc.KeyValueStore.Comment))
	descETag := aws.ToString(desc.ETag)
	require.NotEmpty(t, descETag)

	upd, err := c.UpdateKeyValueStore(ctx, &cloudfront.UpdateKeyValueStoreInput{
		Name:    aws.String(name),
		Comment: aws.String("sdk kvs updated"),
		IfMatch: aws.String(descETag),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk kvs updated", aws.ToString(upd.KeyValueStore.Comment))
	newETag := aws.ToString(upd.ETag)

	list, err := c.ListKeyValueStores(ctx, &cloudfront.ListKeyValueStoresInput{})
	require.NoError(t, err)
	require.NotNil(t, list.KeyValueStoreList)
	found := false
	for _, s := range list.KeyValueStoreList.Items {
		if aws.ToString(s.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteKeyValueStore(ctx, &cloudfront.DeleteKeyValueStoreInput{Name: aws.String(name), IfMatch: aws.String(newETag)})
	require.NoError(t, err)
}

// TestCloudFront_RealtimeLogConfig_Lifecycle exercises the real-time log
// configuration CRUD. These ops identify the resource by Name or ARN in the
// request body rather than a path label.
func TestCloudFront_RealtimeLogConfig_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "rlc-" + time.Now().Format("150405000")
	create, err := c.CreateRealtimeLogConfig(ctx, &cloudfront.CreateRealtimeLogConfigInput{
		Name:         aws.String(name),
		SamplingRate: aws.Int64(50),
		Fields:       []string{"timestamp", "c-ip"},
		EndPoints: []cftypes.EndPoint{
			{
				StreamType: aws.String("Kinesis"),
				KinesisStreamConfig: &cftypes.KinesisStreamConfig{
					RoleARN:   aws.String("arn:aws:iam::123456789012:role/cf-rt-log"),
					StreamARN: aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/cf-rt-log"),
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, create.RealtimeLogConfig)
	arn := aws.ToString(create.RealtimeLogConfig.ARN)
	require.NotEmpty(t, arn)
	defer func() {
		_, _ = c.DeleteRealtimeLogConfig(context.Background(), &cloudfront.DeleteRealtimeLogConfigInput{Name: aws.String(name)})
	}()

	get, err := c.GetRealtimeLogConfig(ctx, &cloudfront.GetRealtimeLogConfigInput{Name: aws.String(name)})
	require.NoError(t, err)
	assert.EqualValues(t, 50, aws.ToInt64(get.RealtimeLogConfig.SamplingRate))
	assert.Len(t, get.RealtimeLogConfig.Fields, 2)

	// Look up by ARN too.
	getByARN, err := c.GetRealtimeLogConfig(ctx, &cloudfront.GetRealtimeLogConfigInput{ARN: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(getByARN.RealtimeLogConfig.Name))

	upd, err := c.UpdateRealtimeLogConfig(ctx, &cloudfront.UpdateRealtimeLogConfigInput{
		Name:         aws.String(name),
		SamplingRate: aws.Int64(75),
		Fields:       []string{"timestamp", "c-ip", "sc-status"},
		EndPoints:    create.RealtimeLogConfig.EndPoints,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 75, aws.ToInt64(upd.RealtimeLogConfig.SamplingRate))

	list, err := c.ListRealtimeLogConfigs(ctx, &cloudfront.ListRealtimeLogConfigsInput{})
	require.NoError(t, err)
	require.NotNil(t, list.RealtimeLogConfigs)
	found := false
	for _, s := range list.RealtimeLogConfigs.Items {
		if aws.ToString(s.Name) == name {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteRealtimeLogConfig(ctx, &cloudfront.DeleteRealtimeLogConfigInput{ARN: aws.String(arn)})
	require.NoError(t, err)
}

// TestCloudFront_StreamingDistribution_Lifecycle exercises the streaming
// distribution CRUD round-trip.
func TestCloudFront_StreamingDistribution_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	caller := cfExtras2Caller()
	mkConfig := func(comment string) *cftypes.StreamingDistributionConfig {
		return &cftypes.StreamingDistributionConfig{
			CallerReference: aws.String(caller),
			Comment:         aws.String(comment),
			Enabled:         aws.Bool(true),
			S3Origin: &cftypes.S3Origin{
				DomainName:           aws.String("example.s3.amazonaws.com"),
				OriginAccessIdentity: aws.String(""),
			},
			TrustedSigners: &cftypes.TrustedSigners{
				Enabled:  aws.Bool(false),
				Quantity: aws.Int32(0),
			},
		}
	}
	create, err := c.CreateStreamingDistribution(ctx, &cloudfront.CreateStreamingDistributionInput{
		StreamingDistributionConfig: mkConfig("sdk streaming"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.StreamingDistribution)
	id := aws.ToString(create.StreamingDistribution.Id)
	require.NotEmpty(t, id)
	assert.NotEmpty(t, aws.ToString(create.StreamingDistribution.DomainName))
	etag := aws.ToString(create.ETag)
	defer func() {
		g, gerr := c.GetStreamingDistribution(context.Background(), &cloudfront.GetStreamingDistributionInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteStreamingDistribution(context.Background(), &cloudfront.DeleteStreamingDistributionInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	}()

	get, err := c.GetStreamingDistribution(ctx, &cloudfront.GetStreamingDistributionInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "sdk streaming", aws.ToString(get.StreamingDistribution.StreamingDistributionConfig.Comment))

	gc, err := c.GetStreamingDistributionConfig(ctx, &cloudfront.GetStreamingDistributionConfigInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "example.s3.amazonaws.com", aws.ToString(gc.StreamingDistributionConfig.S3Origin.DomainName))

	upd, err := c.UpdateStreamingDistribution(ctx, &cloudfront.UpdateStreamingDistributionInput{
		Id:                          aws.String(id),
		IfMatch:                     aws.String(etag),
		StreamingDistributionConfig: mkConfig("sdk streaming updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, "sdk streaming updated", aws.ToString(upd.StreamingDistribution.StreamingDistributionConfig.Comment))
	newETag := aws.ToString(upd.ETag)

	list, err := c.ListStreamingDistributions(ctx, &cloudfront.ListStreamingDistributionsInput{})
	require.NoError(t, err)
	require.NotNil(t, list.StreamingDistributionList)
	found := false
	for _, s := range list.StreamingDistributionList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteStreamingDistribution(ctx, &cloudfront.DeleteStreamingDistributionInput{Id: aws.String(id), IfMatch: aws.String(newETag)})
	require.NoError(t, err)
}

// TestCloudFront_VpcOrigin_Lifecycle exercises the VPC origin CRUD round-trip.
func TestCloudFront_VpcOrigin_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mkConfig := func(name string) *cftypes.VpcOriginEndpointConfig {
		return &cftypes.VpcOriginEndpointConfig{
			Name:                 aws.String(name),
			Arn:                  aws.String("arn:aws:ec2:us-east-1:123456789012:vpc-lattice/service/svc-123"),
			HTTPPort:             aws.Int32(80),
			HTTPSPort:            aws.Int32(443),
			OriginProtocolPolicy: cftypes.OriginProtocolPolicyHttpsOnly,
		}
	}
	name := "vo-" + time.Now().Format("150405000")
	create, err := c.CreateVpcOrigin(ctx, &cloudfront.CreateVpcOriginInput{
		VpcOriginEndpointConfig: mkConfig(name),
	})
	require.NoError(t, err)
	require.NotNil(t, create.VpcOrigin)
	id := aws.ToString(create.VpcOrigin.Id)
	require.NotEmpty(t, id)
	assert.NotEmpty(t, aws.ToString(create.VpcOrigin.Arn))
	etag := aws.ToString(create.ETag)
	defer func() {
		g, gerr := c.GetVpcOrigin(context.Background(), &cloudfront.GetVpcOriginInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteVpcOrigin(context.Background(), &cloudfront.DeleteVpcOriginInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	}()

	get, err := c.GetVpcOrigin(ctx, &cloudfront.GetVpcOriginInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.VpcOrigin.VpcOriginEndpointConfig.Name))

	upd, err := c.UpdateVpcOrigin(ctx, &cloudfront.UpdateVpcOriginInput{
		Id:                      aws.String(id),
		IfMatch:                 aws.String(etag),
		VpcOriginEndpointConfig: mkConfig(name),
	})
	require.NoError(t, err)
	newETag := aws.ToString(upd.ETag)

	list, err := c.ListVpcOrigins(ctx, &cloudfront.ListVpcOriginsInput{})
	require.NoError(t, err)
	require.NotNil(t, list.VpcOriginList)
	found := false
	for _, s := range list.VpcOriginList.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteVpcOrigin(ctx, &cloudfront.DeleteVpcOriginInput{Id: aws.String(id), IfMatch: aws.String(newETag)})
	require.NoError(t, err)
}

// TestCloudFront_AnycastIpList_Lifecycle exercises the Anycast IP list CRUD
// (no update op exists for this resource).
func TestCloudFront_AnycastIpList_Lifecycle(t *testing.T) {
	c := cfClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := "ail-" + time.Now().Format("150405000")
	create, err := c.CreateAnycastIpList(ctx, &cloudfront.CreateAnycastIpListInput{
		Name:    aws.String(name),
		IpCount: aws.Int32(3),
	})
	require.NoError(t, err)
	require.NotNil(t, create.AnycastIpList)
	id := aws.ToString(create.AnycastIpList.Id)
	require.NotEmpty(t, id)
	assert.EqualValues(t, 3, aws.ToInt32(create.AnycastIpList.IpCount))
	etag := aws.ToString(create.ETag)
	defer func() {
		g, gerr := c.GetAnycastIpList(context.Background(), &cloudfront.GetAnycastIpListInput{Id: aws.String(id)})
		if gerr == nil {
			_, _ = c.DeleteAnycastIpList(context.Background(), &cloudfront.DeleteAnycastIpListInput{Id: aws.String(id), IfMatch: g.ETag})
		}
	}()

	get, err := c.GetAnycastIpList(ctx, &cloudfront.GetAnycastIpListInput{Id: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.AnycastIpList.Name))
	assert.Len(t, get.AnycastIpList.AnycastIps, 3)

	list, err := c.ListAnycastIpLists(ctx, &cloudfront.ListAnycastIpListsInput{})
	require.NoError(t, err)
	require.NotNil(t, list.AnycastIpLists)
	found := false
	for _, s := range list.AnycastIpLists.Items {
		if aws.ToString(s.Id) == id {
			found = true
		}
	}
	assert.True(t, found)

	_, err = c.DeleteAnycastIpList(ctx, &cloudfront.DeleteAnycastIpListInput{Id: aws.String(id), IfMatch: aws.String(etag)})
	require.NoError(t, err)
}
