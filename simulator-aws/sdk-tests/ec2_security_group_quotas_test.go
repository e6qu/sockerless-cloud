package aws_sdk_test

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ValidateSecurityGroupQuotasForInterface arrived in the Amazon EC2 model on
// 2026-09-04 and the pinned aws-sdk-go-v2 release predates it, so the request
// is signed and posted the way the generated client signs an ec2Query call.
// The answer is computed from the security groups the simulator holds, against
// the Amazon VPC quotas AWS publishes: sixty rules per security group and five
// security groups per network interface.
func validateSecurityGroupQuotas(t *testing.T, groupIDs ...string) (int, string) {
	t.Helper()
	form := url.Values{"Action": {"ValidateSecurityGroupQuotasForInterface"}, "Version": {"2016-11-15"}}
	for i, id := range groupIDs {
		form.Set(fmt.Sprintf("SecurityGroupId.%d", i+1), id)
	}
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, simEndpoint("ec2"), strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signRawSigV4JSON(t, req, "ec2", []byte(body))

	resp, err := simHTTPClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw)
}

func TestEC2_ValidateSecurityGroupQuotasForInterface(t *testing.T) {
	client := ec2Client()

	vpc, err := client.CreateVpc(ctx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.90.0.0/16")})
	require.NoError(t, err)

	sg, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String("sg-quota-probe"),
		Description: aws.String("quota validation probe"),
		VpcId:       vpc.Vpc.VpcId,
	})
	require.NoError(t, err)

	t.Run("a group within its quotas validates", func(t *testing.T) {
		status, body := validateSecurityGroupQuotas(t, *sg.GroupId)
		require.Equal(t, http.StatusOK, status, body)
		assert.Contains(t, body, "<valid>true</valid>",
			"one group carrying no rules is inside both quotas")
	})

	t.Run("a group that does not exist is refused", func(t *testing.T) {
		status, body := validateSecurityGroupQuotas(t, "sg-0000000000000dead")
		assert.GreaterOrEqual(t, status, 400, body)
		assert.Contains(t, body, "InvalidGroup.NotFound")
	})

	t.Run("more groups than an interface may carry does not validate", func(t *testing.T) {
		ids := []string{}
		for i := 0; i < 6; i++ {
			extra, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
				GroupName:   aws.String(fmt.Sprintf("sg-quota-many-%d", i)),
				Description: aws.String("quota validation probe"),
				VpcId:       vpc.Vpc.VpcId,
			})
			require.NoError(t, err)
			ids = append(ids, *extra.GroupId)
		}
		status, body := validateSecurityGroupQuotas(t, ids...)
		require.Equal(t, http.StatusOK, status, body)
		assert.Contains(t, body, "<valid>false</valid>",
			"six groups is past the five a network interface may carry")
	})

	t.Run("a group past its rule quota does not validate", func(t *testing.T) {
		crowded, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
			GroupName:   aws.String("sg-quota-crowded"),
			Description: aws.String("quota validation probe"),
			VpcId:       vpc.Vpc.VpcId,
		})
		require.NoError(t, err)

		// Sixty-one sources in one permission is one past the quota; AWS counts
		// a rule per source, not per permission.
		ranges := make([]types.IpRange, 0, 61)
		for i := 0; i < 61; i++ {
			ranges = append(ranges, types.IpRange{CidrIp: aws.String(fmt.Sprintf("10.%d.%d.0/24", i/10, i%10))})
		}
		_, err = client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: crowded.GroupId,
			IpPermissions: []types.IpPermission{{
				IpProtocol: aws.String("tcp"),
				FromPort:   aws.Int32(443),
				ToPort:     aws.Int32(443),
				IpRanges:   ranges,
			}},
		})
		require.NoError(t, err)

		status, body := validateSecurityGroupQuotas(t, *crowded.GroupId)
		require.Equal(t, http.StatusOK, status, body)
		assert.Contains(t, body, "<valid>false</valid>",
			"sixty-one rules is past the sixty a security group may carry")
	})
}
