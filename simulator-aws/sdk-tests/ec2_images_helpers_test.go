package aws_sdk_test

import (
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/require"
)

// The public AMIs the tests launch, resolved through DescribeImages by owner
// and name the way a client finds an AMI on AWS, once per process.
var (
	ec2PublicAMIMu sync.Mutex
	ec2PublicAMIs  = map[string]string{}
)

func ec2PublicAMI(t *testing.T, owner, namePattern string) string {
	t.Helper()
	ec2PublicAMIMu.Lock()
	defer ec2PublicAMIMu.Unlock()
	key := owner + " " + namePattern
	if id, ok := ec2PublicAMIs[key]; ok {
		return id
	}
	out, err := ec2Client().DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{owner},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{namePattern}},
			{Name: aws.String("architecture"), Values: []string{"x86_64"}},
			{Name: aws.String("state"), Values: []string{"available"}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, out.Images, "no public AMI matches %s %s", owner, namePattern)
	id := aws.ToString(out.Images[0].ImageId)
	ec2PublicAMIs[key] = id
	return id
}

// ec2AmazonLinuxAMI is the Amazon Linux 2023 x86_64 AMI.
func ec2AmazonLinuxAMI(t *testing.T) string {
	return ec2PublicAMI(t, "amazon", "al2023-ami-2023.*-kernel-6.1-x86_64")
}

// ec2UbuntuAMI is the Ubuntu 24.04 LTS x86_64 AMI Canonical publishes.
func ec2UbuntuAMI(t *testing.T) string {
	return ec2PublicAMI(t, "099720109477", "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*")
}
