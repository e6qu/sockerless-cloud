package aws_sdk_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
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

// An instance reads its identity, placement, tags and profile from the
// instance metadata service under an IMDSv2 token: a tag's value at
// /latest/meta-data/tags/instance/{key}, and at
// /latest/meta-data/iam/security-credentials/{role} a session of its profile's
// role named for the instance.
func TestIMDS_InstanceReadsItsOwnMetadataAndRoleSession(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	iamc := iamClient()
	name := uniquename.New("imds-instance")
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	profile, err := iamc.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(name)})
	require.NoError(t, err)
	_, err = iamc.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(name), RoleName: aws.String(name)})
	require.NoError(t, err)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("ssm-managed-node"), PolicyDocument: aws.String(ssmManagedNodePolicy),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamc.DeleteRolePolicy(context.Background(), &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("ssm-managed-node")})
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

	// The instance holds the interface the API reports: its MAC is the guest's,
	// and its private DNS name resolves to its address through the VPC's
	// resolver.
	require.Len(t, inst.NetworkInterfaces, 1)
	eni := inst.NetworkInterfaces[0]
	mac := aws.ToString(eni.MacAddress)
	assert.Equal(t, mac, imds("/latest/meta-data/mac"))
	assert.Equal(t, mac, strings.TrimSpace(ssmShell(t, s, instanceID, "cat /sys/class/net/eth0/address")), "the guest's interface carries the MAC the API reports")
	assert.Equal(t, aws.ToString(inst.PrivateDnsName), imds("/latest/meta-data/local-hostname"))
	assert.Equal(t, aws.ToString(inst.PrivateDnsName), imds("/latest/meta-data/hostname"))
	resolved := strings.Fields(ssmShell(t, s, instanceID, "getent hosts "+aws.ToString(inst.PrivateDnsName)))
	require.NotEmpty(t, resolved, "the VPC's resolver does not answer the instance's private DNS name")
	assert.Equal(t, aws.ToString(inst.PrivateIpAddress), resolved[0])
	assert.Equal(t, "interfaces/", imds("/latest/meta-data/network/"))
	assert.Equal(t, "macs/", imds("/latest/meta-data/network/interfaces/"))
	assert.Equal(t, mac+"/", imds("/latest/meta-data/network/interfaces/macs/"))
	assert.Contains(t, strings.Split(imds(fmt.Sprintf("/latest/meta-data/network/interfaces/macs/%s/", mac)), "\n"), "subnet-ipv4-cidr-block")
	macKey := func(key string) string {
		return imds(fmt.Sprintf("/latest/meta-data/network/interfaces/macs/%s/%s", mac, key))
	}
	assert.Equal(t, aws.ToString(eni.NetworkInterfaceId), macKey("interface-id"))
	assert.Equal(t, "0", macKey("device-number"))
	assert.Equal(t, aws.ToString(subnet), macKey("subnet-id"))
	assert.Equal(t, "10.217.1.0/24", macKey("subnet-ipv4-cidr-block"))
	assert.Equal(t, aws.ToString(vpc.Vpc.VpcId), macKey("vpc-id"))
	assert.Equal(t, "10.217.0.0/16", macKey("vpc-ipv4-cidr-block"))
	assert.Equal(t, aws.ToString(inst.PrivateIpAddress), macKey("local-ipv4s"))

	// An associated Elastic IP address appears under the interface's public
	// addresses, and ipv4-associations maps it to its private address.
	assert.Equal(t, "404", status(`-H "X-aws-ec2-metadata-token: $(curl -sS -X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")" "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/meta-data/network/interfaces/macs/`+mac+`/public-ipv4s"`),
		"an interface without a public address has no public-ipv4s")
	eip, err := c.AllocateAddress(ctx, &ec2.AllocateAddressInput{Domain: types.DomainTypeVpc})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.ReleaseAddress(context.Background(), &ec2.ReleaseAddressInput{AllocationId: eip.AllocationId})
	})
	association, err := c.AssociateAddress(ctx, &ec2.AssociateAddressInput{AllocationId: eip.AllocationId, InstanceId: aws.String(instanceID)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DisassociateAddress(context.Background(), &ec2.DisassociateAddressInput{AssociationId: association.AssociationId})
	})
	assert.Equal(t, aws.ToString(eip.PublicIp), macKey("public-ipv4s"))
	assert.Equal(t, aws.ToString(eip.PublicIp), macKey("ipv4-associations/"))
	assert.Equal(t, aws.ToString(inst.PrivateIpAddress), macKey("ipv4-associations/"+aws.ToString(eip.PublicIp)))
	require.NotEmpty(t, eni.Groups)
	assert.Equal(t, aws.ToString(eni.Groups[0].GroupId), macKey("security-group-ids"))
	assert.Equal(t, aws.ToString(eni.Groups[0].GroupName), imds("/latest/meta-data/security-groups"))
	assert.Equal(t, aws.ToString(desc.Reservations[0].ReservationId), imds("/latest/meta-data/reservation-id"))
	assert.Equal(t, "ami\nroot", imds("/latest/meta-data/block-device-mapping/"))
	assert.Equal(t, aws.ToString(inst.RootDeviceName), imds("/latest/meta-data/block-device-mapping/root"))
	assert.Equal(t, "domain\npartition", imds("/latest/meta-data/services/"))
	assert.Equal(t, "amazonaws.com", imds("/latest/meta-data/services/domain"))
	assert.Equal(t, "aws", imds("/latest/meta-data/services/partition"))
	assert.Equal(t, "none", imds("/latest/meta-data/instance-action"))
	assert.Equal(t, "on-demand", imds("/latest/meta-data/instance-life-cycle"))
	assert.Equal(t, "0", imds("/latest/meta-data/ami-launch-index"))

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

	// The instance's session is its profile role's, named for the instance, in its
	// own account.
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
