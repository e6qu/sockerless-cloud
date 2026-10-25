package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/batch"
	batchtypes "github.com/aws/aws-sdk-go-v2/service/batch/types"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	r53types "github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A grant conditioned on route53:ChangeResourceRecordSetsRecordTypes and
// ChangeResourceRecordSetsActions covers only the changes it names.
func TestIAM_Route53ChangeBatchConditionKeys_SDK(t *testing.T) {
	t.Parallel()
	r53 := route53Client()
	zoneName := uniquename.New("rrset-cond") + ".example."
	zone, err := r53.CreateHostedZone(ctx, &route53.CreateHostedZoneInput{
		Name: aws.String(zoneName), CallerReference: aws.String(uniquename.New("rrset-cond")),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = r53.DeleteHostedZone(ctx, &route53.DeleteHostedZoneInput{Id: zone.HostedZone.Id}) })
	zoneID := strings.TrimPrefix(aws.ToString(zone.HostedZone.Id), "/hostedzone/")

	role := route53.NewFromConfig(roleSessionConfig(t, "rrset-cond",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"route53:ChangeResourceRecordSets",`+
			`"Resource":"arn:aws:route53:::hostedzone/`+zoneID+`","Condition":{"ForAllValues:StringEquals":{`+
			`"route53:ChangeResourceRecordSetsRecordTypes":["TXT"],"route53:ChangeResourceRecordSetsActions":["CREATE","UPSERT"]}}}]}`),
		func(o *route53.Options) { o.BaseEndpoint = aws.String(baseURL) })
	change := func(action r53types.ChangeAction, recordType r53types.RRType, value string) error {
		_, err := role.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
			HostedZoneId: zone.HostedZone.Id,
			ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
				Action: action,
				ResourceRecordSet: &r53types.ResourceRecordSet{
					Name: aws.String("app." + zoneName), Type: recordType, TTL: aws.Int64(60),
					ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(value)}},
				},
			}}},
		})
		return err
	}
	assert.Equal(t, "AccessDenied", errCode(t, change(r53types.ChangeActionCreate, r53types.RRTypeA, "192.0.2.1")))
	require.NoError(t, change(r53types.ChangeActionCreate, r53types.RRTypeTxt, `"granted"`))
	assert.Equal(t, "AccessDenied", errCode(t, change(r53types.ChangeActionDelete, r53types.RRTypeTxt, `"granted"`)))
	_, err = r53.ChangeResourceRecordSets(ctx, &route53.ChangeResourceRecordSetsInput{
		HostedZoneId: zone.HostedZone.Id,
		ChangeBatch: &r53types.ChangeBatch{Changes: []r53types.Change{{
			Action: r53types.ChangeActionDelete,
			ResourceRecordSet: &r53types.ResourceRecordSet{
				Name: aws.String("app." + zoneName), Type: r53types.RRTypeTxt, TTL: aws.Int64(60),
				ResourceRecords: []r53types.ResourceRecord{{Value: aws.String(`"granted"`)}},
			},
		}}},
	})
	require.NoError(t, err)
}

// A grant conditioned on batch:Privileged registers only unprivileged job
// definitions.
func TestIAM_BatchJobDefinitionConditionKeys_SDK(t *testing.T) {
	t.Parallel()
	role := batch.NewFromConfig(roleSessionConfig(t, "batch-cond",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"batch:RegisterJobDefinition","Resource":"*",`+
			`"Condition":{"Bool":{"batch:Privileged":"false"}}}]}`),
		func(o *batch.Options) { o.BaseEndpoint = aws.String(baseURL) })
	register := func(privileged bool) (*batch.RegisterJobDefinitionOutput, error) {
		return role.RegisterJobDefinition(ctx, &batch.RegisterJobDefinitionInput{
			JobDefinitionName: aws.String(uniquename.New("batch-cond")),
			Type:              batchtypes.JobDefinitionTypeContainer,
			ContainerProperties: &batchtypes.ContainerProperties{
				Image: aws.String(containerCommandImage), Privileged: aws.Bool(privileged),
				Vcpus: aws.Int32(1), Memory: aws.Int32(512),
			},
		})
	}
	_, err := register(true)
	assert.Equal(t, "AccessDeniedException", errCode(t, err))
	registered, err := register(false)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = batchClient().DeregisterJobDefinition(ctx, &batch.DeregisterJobDefinitionInput{JobDefinition: registered.JobDefinitionArn})
	})
}

// A grant conditioned on elasticfilesystem:Encrypted creates only encrypted
// file systems, and a create that carries tags also needs
// elasticfilesystem:TagResource, which elasticfilesystem:CreateAction limits to
// tagging at creation.
func TestIAM_EFSCreateFileSystemConditionKeys_SDK(t *testing.T) {
	t.Parallel()
	createOnly := `{"Effect":"Allow","Action":"elasticfilesystem:CreateFileSystem","Resource":"*",` +
		`"Condition":{"Bool":{"elasticfilesystem:Encrypted":"true"}}}`
	untagged := efs.NewFromConfig(roleSessionConfig(t, "efs-cond", `{"Version":"2012-10-17","Statement":[`+createOnly+`]}`),
		func(o *efs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	create := func(client *efs.Client, encrypted bool, tags []efstypes.Tag) error {
		out, err := client.CreateFileSystem(ctx, &efs.CreateFileSystemInput{
			CreationToken: aws.String(uniquename.New("efs-cond")), Encrypted: aws.Bool(encrypted), Tags: tags,
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = efsClient().DeleteFileSystem(ctx, &efs.DeleteFileSystemInput{FileSystemId: out.FileSystemId})
			})
		}
		return err
	}
	tags := []efstypes.Tag{{Key: aws.String("team"), Value: aws.String("storage")}}
	assert.Equal(t, "AccessDeniedException", errCode(t, create(untagged, false, nil)))
	require.NoError(t, create(untagged, true, nil))
	assert.Equal(t, "AccessDeniedException", errCode(t, create(untagged, true, tags)),
		"a create that carries tags needs elasticfilesystem:TagResource")

	tagging := efs.NewFromConfig(roleSessionConfig(t, "efs-cond-tags", `{"Version":"2012-10-17","Statement":[`+createOnly+`,`+
		`{"Effect":"Allow","Action":"elasticfilesystem:TagResource","Resource":"*",`+
		`"Condition":{"StringEquals":{"elasticfilesystem:CreateAction":"CreateFileSystem"}}}]}`),
		func(o *efs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	require.NoError(t, create(tagging, true, tags))
}

// Amazon API Gateway authorizes CreatePortalProduct as
// apigateway:CreatePortalProduct as well as apigateway:POST, and a grant on the
// first conditioned on apigateway:Request/PortalProductDisplayName covers only
// the display name it names.
func TestIAM_APIGatewayPortalProductConditionKeys_SDK(t *testing.T) {
	t.Parallel()
	postOnly := apigatewayv2.NewFromConfig(roleSessionConfig(t, "apigw-cond-post",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"apigateway:POST","Resource":"*"}]}`),
		func(o *apigatewayv2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	granted := apigatewayv2.NewFromConfig(roleSessionConfig(t, "apigw-cond",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"apigateway:POST","Resource":"*"},`+
			`{"Effect":"Allow","Action":"apigateway:CreatePortalProduct","Resource":"*",`+
			`"Condition":{"StringEquals":{"apigateway:Request/PortalProductDisplayName":"payments"}}}]}`),
		func(o *apigatewayv2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	create := func(client *apigatewayv2.Client, name string) error {
		out, err := client.CreatePortalProduct(ctx, &apigatewayv2.CreatePortalProductInput{DisplayName: aws.String(name)})
		if err == nil {
			t.Cleanup(func() {
				_, _ = apigwv2Client().DeletePortalProduct(ctx, &apigatewayv2.DeletePortalProductInput{PortalProductId: out.PortalProductId})
			})
		}
		return err
	}
	assert.Equal(t, "AccessDeniedException", errCode(t, create(postOnly, "payments")))
	assert.Equal(t, "AccessDeniedException", errCode(t, create(granted, "orders")))
	require.NoError(t, create(granted, "payments"))
}
