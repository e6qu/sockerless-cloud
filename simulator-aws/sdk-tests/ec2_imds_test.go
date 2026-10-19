package aws_sdk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The instance metadata service answers an EC2 instance and nothing else.
func TestIMDS_RefusesACallerThatIsNoInstance(t *testing.T) {
	t.Parallel()
	for _, req := range []struct{ method, path string }{
		{http.MethodPut, "/latest/api/token"},
		{http.MethodGet, "/latest/meta-data/instance-id"},
		{http.MethodGet, "/latest/meta-data/iam/security-credentials/"},
		{http.MethodGet, "/latest/dynamic/instance-identity/document"},
	} {
		r, err := http.NewRequestWithContext(ctx, req.method, baseURL+req.path, nil)
		require.NoError(t, err)
		r.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
		resp, err := http.DefaultClient.Do(r)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", req.method, req.path)
	}
}

// The Amazon ECS task metadata endpoint knows no task by an ID it never ran.
func TestECSTaskMetadataV4_UnknownTask(t *testing.T) {
	t.Parallel()
	resp, err := http.Get(baseURL + "/v4/abc123/task")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// An instance reads its own identity, placement, tags and profile from the
// instance metadata service under an IMDSv2 token: a tag's value at
// /latest/meta-data/tags/instance/{key}, and at
// /latest/meta-data/iam/security-credentials/{role} a session of its
// profile's role named for the instance.
func TestIMDS_InstanceReadsItsOwnMetadataAndRoleSession(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	iamc := iamClient()
	name := uniqueName("imds-instance")
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	profile, err := iamc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(name)})
	require.NoError(t, err)
	_, err = iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(name), RoleName: aws.String(name)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamc.RemoveRoleFromInstanceProfile(context.Background(), &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: aws.String(name), RoleName: aws.String(name)})
		_, _ = iamc.DeleteInstanceProfile(context.Background(), &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(name)})
		_, _ = iamc.DeleteRole(context.Background(), &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.217.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.217.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet,
		IamInstanceProfile: &types.IamInstanceProfileSpecification{Name: aws.String(name)},
		MetadataOptions:    &types.InstanceMetadataOptionsRequest{InstanceMetadataTags: types.InstanceMetadataTagsStateEnabled},
		TagSpecifications: []types.TagSpecification{{
			ResourceType: types.ResourceTypeInstance,
			Tags:         []types.Tag{{Key: aws.String("Name"), Value: aws.String(name)}, {Key: aws.String("team"), Value: aws.String("platform")}},
		}},
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	desc := waitForEC2InstanceRunning(t, c, instanceID)
	inst := desc.Reservations[0].Instances[0]
	require.NotNil(t, inst.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, inst.MetadataOptions.HttpTokens,
		"an Amazon Linux 2023 AMI registers ImdsSupport v2.0, so its instances require tokens")
	subnets, err := c.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{aws.ToString(subnet)}})
	require.NoError(t, err)

	s := ssmClient()
	imds := func(path string) string {
		return ssmShell(t, s, instanceID, `token=$(curl -sS -X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
curl -sS -H "X-aws-ec2-metadata-token: $token" "${AWS_EC2_METADATA_SERVICE_ENDPOINT%/}`+path+`"`)
	}
	status := func(args string) string {
		return ssmShell(t, s, instanceID, `curl -sS -o /dev/null -w '%{http_code}' `+args)
	}

	assert.Equal(t, "401", status(`"${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/meta-data/instance-id"`), "a read without a token is refused")
	assert.Equal(t, "401", status(`-H "X-aws-ec2-metadata-token: AQunknown" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/meta-data/instance-id"`))
	assert.Equal(t, "400", status(`-X PUT -H "X-aws-ec2-metadata-token-ttl-seconds: 21601" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token"`), "a TTL over six hours is refused")
	assert.Equal(t, "400", status(`-X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token"`), "a token request without a TTL is refused")

	listing := imds("/latest/meta-data/")
	for _, entry := range []string{"ami-id", "iam/", "instance-id", "instance-type", "local-ipv4", "placement/", "tags/"} {
		assert.Contains(t, strings.Split(listing, "\n"), entry)
	}
	assert.Equal(t, instanceID, imds("/latest/meta-data/instance-id"))
	assert.Equal(t, "t3.micro", imds("/latest/meta-data/instance-type"))
	assert.Equal(t, aws.ToString(inst.ImageId), imds("/latest/meta-data/ami-id"))
	assert.Equal(t, aws.ToString(inst.PrivateIpAddress), imds("/latest/meta-data/local-ipv4"))
	assert.Equal(t, "availability-zone\nregion", imds("/latest/meta-data/placement/"))
	assert.Equal(t, aws.ToString(subnets.Subnets[0].AvailabilityZone), imds("/latest/meta-data/placement/availability-zone"))
	region := imds("/latest/meta-data/placement/region")
	assert.True(t, strings.HasPrefix(aws.ToString(subnets.Subnets[0].AvailabilityZone), region), "zone %s lies in region %s", aws.ToString(subnets.Subnets[0].AvailabilityZone), region)

	assert.Equal(t, "instance", imds("/latest/meta-data/tags/"))
	assert.Equal(t, "Name\nteam", imds("/latest/meta-data/tags/instance"))
	assert.Equal(t, "platform", imds("/latest/meta-data/tags/instance/team"))

	var info struct{ Code, InstanceProfileArn, InstanceProfileId string }
	require.NoError(t, json.Unmarshal([]byte(imds("/latest/meta-data/iam/info")), &info))
	assert.Equal(t, "Success", info.Code)
	assert.Equal(t, aws.ToString(profile.InstanceProfile.Arn), info.InstanceProfileArn)
	assert.Equal(t, aws.ToString(profile.InstanceProfile.InstanceProfileId), info.InstanceProfileId)

	assert.Equal(t, "info\nsecurity-credentials/", imds("/latest/meta-data/iam/"))
	assert.Equal(t, name, imds("/latest/meta-data/iam/security-credentials/"))
	var cred struct{ Code, Type, AccessKeyId, SecretAccessKey, Token, Expiration string }
	require.NoError(t, json.Unmarshal([]byte(imds("/latest/meta-data/iam/security-credentials/"+name)), &cred))
	assert.Equal(t, "Success", cred.Code)
	assert.Equal(t, "AWS-HMAC", cred.Type)

	var document struct{ AccountID, InstanceID, InstanceType, ImageID, PrivateIP, Region, AvailabilityZone string }
	raw := imds("/latest/dynamic/instance-identity/document")
	require.NoError(t, json.Unmarshal([]byte(raw), &document), raw)
	assert.Equal(t, instanceID, document.InstanceID)
	assert.Equal(t, "t3.micro", document.InstanceType)
	assert.Equal(t, aws.ToString(inst.ImageId), document.ImageID)
	assert.Equal(t, aws.ToString(inst.PrivateIpAddress), document.PrivateIP)
	assert.Equal(t, region, document.Region)

	// The session the instance holds is its profile role's, named for the
	// instance, in the instance's own account.
	sessionSTS := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.Credentials = credentials.NewStaticCredentialsProvider(cred.AccessKeyId, cred.SecretAccessKey, cred.Token)
	})
	identity, err := sessionSTS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:sts::"+document.AccountID+":assumed-role/"+name+"/"+instanceID, aws.ToString(identity.Arn))
	assert.Equal(t, strings.Split(aws.ToString(role.Role.Arn), ":")[4], document.AccountID)

	// An instance whose metadata endpoint is turned off reaches nothing there.
	_, err = c.ModifyInstanceMetadataOptions(ctx, &ec2.ModifyInstanceMetadataOptionsInput{
		InstanceId: aws.String(instanceID), HttpEndpoint: types.InstanceMetadataEndpointStateDisabled,
	})
	require.NoError(t, err)
	assert.Equal(t, "404", status(`-X PUT -H "X-aws-ec2-metadata-token-ttl-seconds: 60" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token"`))
}
