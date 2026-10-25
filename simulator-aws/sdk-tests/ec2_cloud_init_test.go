package aws_sdk_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An Amazon Linux 2023 instance's cloud-init runs its #cloud-config user
// data: it writes the files, creates the users, gives the default user
// ec2-user the key pair's key the instance metadata service lists, runs
// runcmd as root, and writes its final message to the console.
func TestEC2_InstanceRunsItsCloudConfig(t *testing.T) {
	t.Parallel()
	c := ec2Client()
	keyName := uniquename.New("cloud-init-key")
	_, err := c.ImportKeyPair(ctx, &ec2.ImportKeyPairInput{KeyName: aws.String(keyName), PublicKeyMaterial: []byte(testRSAPublicKey)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = c.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String(keyName)}) })
	const deployKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPAkqz56K/EWsdIxGWKGQ5SxtXw8WhqPshWaZNevDsZv deploy@test"
	userData := "#cloud-config\n" +
		"write_files:\n" +
		"  - path: /etc/app/settings.conf\n" +
		"    content: |\n" +
		"      written by cloud-init\n" +
		"    permissions: '0640'\n" +
		"users:\n" +
		"  - default\n" +
		"  - name: deploy\n" +
		"    groups: wheel\n" +
		"    shell: /bin/bash\n" +
		"    ssh_authorized_keys: ['" + deployKey + "']\n" +
		"runcmd:\n" +
		"  - echo \"runcmd as $(id -un)\" > /var/tmp/runcmd\n"

	vpc, err := c.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.219.0.0/16")})
	require.NoError(t, err)
	subnet := createSubnetFor(t, c, vpc.Vpc.VpcId, "10.219.1.0/24")
	run, err := c.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId: aws.String(ec2AmazonLinuxAMI(t)), InstanceType: types.InstanceTypeT3Micro,
		MinCount: aws.Int32(1), MaxCount: aws.Int32(1), SubnetId: subnet, KeyName: aws.String(keyName),
		IamInstanceProfile: ssmManagedProfileSpec(t),
		UserData:           aws.String(base64.StdEncoding.EncodeToString([]byte(userData))),
	})
	require.NoError(t, err)
	terminateEC2InstancesOnCleanup(t, c, run)
	instanceID := aws.ToString(run.Instances[0].InstanceId)
	waitForEC2InstanceRunning(t, c, instanceID)

	s := ssmClient()
	assert.Equal(t, "written by cloud-init\n640 root\n", ssmShell(t, s, instanceID, "cat /etc/app/settings.conf; stat -c '%a %U' /etc/app/settings.conf"))
	assert.Equal(t, "runcmd as root\n", ssmShell(t, s, instanceID, "cat /var/tmp/runcmd"))
	assert.Equal(t, testRSAPublicKey+"\n", ssmShell(t, s, instanceID, "cat /home/ec2-user/.ssh/authorized_keys"),
		"the default user holds the key pair's key")
	assert.Equal(t, deployKey+"\n", ssmShell(t, s, instanceID, "cat /home/deploy/.ssh/authorized_keys"))
	assert.Equal(t, "ec2-user,deploy\n", ssmShell(t, s, instanceID, "grep '^wheel:' /etc/group | cut -d: -f4"))
	imds := func(path string) string {
		return ssmShell(t, s, instanceID, `token=$(curl -sS -X PUT "${AWS_EC2_METADATA_SERVICE_ENDPOINT}latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 60")
curl -sS -H "X-aws-ec2-metadata-token: $token" "${AWS_EC2_METADATA_SERVICE_ENDPOINT%/}`+path+`"`)
	}
	assert.Equal(t, "0="+keyName, imds("/latest/meta-data/public-keys/"), "the metadata service lists the key pair")
	assert.Equal(t, "openssh-key", imds("/latest/meta-data/public-keys/0/"))
	assert.Equal(t, testRSAPublicKey+"\n", imds("/latest/meta-data/public-keys/0/openssh-key"))
	assert.Contains(t, ssmShell(t, s, instanceID, "cat /run/cloud-init/result.json"), `"errors": []`)

	console, err := c.GetConsoleOutput(ctx, &ec2.GetConsoleOutputInput{InstanceId: aws.String(instanceID)})
	require.NoError(t, err)
	decoded, err := base64.StdEncoding.DecodeString(aws.ToString(console.Output))
	require.NoError(t, err)
	assert.Contains(t, string(decoded), "Cloud-init v. 22.2.2 finished at ")
	assert.Contains(t, string(decoded), "Datasource DataSourceEc2Local.")
	assert.Equal(t, 1, strings.Count(string(decoded), "Cloud-init v. 22.2.2 finished at "), "the console carries each line once")
}
