package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3controltypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/e6qu/sockerless-cloud/testutil/samlidp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The condition keys in this file describe a resource the request reads rather
// than names outright: the daemon task definition a daemon runs, the version
// each entry of a DeleteObjects names, the database a blue/green deployment
// clones, the session an AssumeRole is chained from. A policy written on one of
// them must hold the request to what that resource is.

// notAuthorized reports whether err is the gate's refusal, as opposed to a
// failure of the request after it was authorized.
func notAuthorized(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "not authorized") || errCodeOf(err) == "AccessDenied")
}

// TestECS_DaemonTaskDefinitionSizeScopesTheGrant covers ecs:task-cpu and
// ecs:task-memory on CreateDaemon: the size is the daemon task definition's.
func TestECS_DaemonTaskDefinitionSizeScopesTheGrant(t *testing.T) {
	admin := ecsClient()
	register := func(family, cpu, memory string) string {
		out, err := admin.RegisterDaemonTaskDefinition(ctx, &ecs.RegisterDaemonTaskDefinitionInput{
			Family: aws.String(family), Cpu: aws.String(cpu), Memory: aws.String(memory),
			ContainerDefinitions: []ecstypes.DaemonContainerDefinition{{
				Name: aws.String("agent"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest")}},
		})
		require.NoError(t, err)
		return aws.ToString(out.DaemonTaskDefinitionArn)
	}
	small := register(uniqueName("daemon-small"), "256", "512")
	large := register(uniqueName("daemon-large"), "1024", "2048")

	akid, secret := restrictedCredential(t, "ecs-small-daemons",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ecs:CreateDaemon","Resource":"*",
		  "Condition":{"NumericLessThanEquals":{"ecs:task-cpu":"256","ecs:task-memory":"512"}}}]}`)
	restricted := ecs.NewFromConfig(keyConfig(akid, secret), func(o *ecs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	create := func(definition string) error {
		_, err := restricted.CreateDaemon(ctx, &ecs.CreateDaemonInput{
			DaemonName: aws.String(uniqueName("daemon")), DaemonTaskDefinitionArn: aws.String(definition),
			ClusterArn:           aws.String("daemon-size-absent-cluster"),
			CapacityProviderArns: []string{"arn:aws:ecs:us-east-1:123456789012:capacity-provider/absent"},
		})
		return err
	}
	assert.False(t, notAuthorized(create(small)), "the daemon task definition is the size the grant allows")
	assert.True(t, notAuthorized(create(large)), "a larger daemon task definition is outside the grant")
}

// TestS3_DeleteObjectsVersionScopesEachEntry covers s3:versionid on the
// s3:DeleteObjectVersion a DeleteObjects entry is authorized as: each entry is
// held to the version it names, and a refused entry is that entry's
// AccessDenied in the DeleteResult.
func TestS3_DeleteObjectsVersionScopesEachEntry(t *testing.T) {
	admin := s3Client()
	bucket := uniqueName("delete-objects-versions")
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	const granted = "3HL4kqtJlcpXroDTDmJ.rmSpXd3dIbrHY"

	akid, secret := restrictedCredential(t, "s3-delete-one-version",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:DeleteObjectVersion",
		  "Resource":"arn:aws:s3:::`+bucket+`/*","Condition":{"StringEquals":{"s3:versionid":"`+granted+`"}}}]}`)
	restricted := s3.NewFromConfig(keyConfig(akid, secret), func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseURL)
		o.UsePathStyle = true
	})
	refused := func(version string) bool {
		out, err := restricted.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(bucket),
			Delete: &s3types.Delete{Objects: []s3types.ObjectIdentifier{{Key: aws.String("report"), VersionId: aws.String(version)}}}})
		require.NoError(t, err)
		for _, entry := range out.Errors {
			if aws.ToString(entry.Code) == "AccessDenied" {
				return true
			}
		}
		return false
	}
	assert.False(t, refused(granted), "the entry names the version the grant allows")
	assert.True(t, refused("UIORUnfndfiufdisojhr398493jfdkjFJjkndnqUifhnw89493jJFJ"),
		"an entry naming another version is refused")
}

// TestS3Control_GetAccessPointResourceTagScopesTheGrant covers
// aws:ResourceTag/${TagKey} on GetAccessPoint, which authorizes against "*"
// yet reports the tags of the access point its path names.
func TestS3Control_GetAccessPointResourceTagScopesTheGrant(t *testing.T) {
	admin := s3Client()
	bucket := uniqueName("ap-tagged-bucket")
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	control := s3control.NewFromConfig(sdkConfig(),
		func(o *s3control.Options) { o.BaseEndpoint = aws.String(simEndpoint("s3-control")) })
	create := func(name string, tags []s3controltypes.Tag) {
		_, err := control.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name), Bucket: aws.String(bucket), Tags: tags})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = control.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
				AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
		})
	}
	data, other := uniqueName("ap-data"), uniqueName("ap-other")
	create(data, []s3controltypes.Tag{{Key: aws.String("team"), Value: aws.String("data")}})
	create(other, []s3controltypes.Tag{{Key: aws.String("team"), Value: aws.String("web")}})

	restricted := s3ControlClientWithCreds(restrictedCredential(t, "s3-data-access-points",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetAccessPoint","Resource":"*",
		  "Condition":{"StringEquals":{"aws:ResourceTag/team":"data"}}}]}`))
	get := func(name string) error {
		_, err := restricted.GetAccessPoint(ctx, &s3control.GetAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(name)})
		return err
	}
	assert.NoError(t, get(data), "the access point carries the tag the grant names")
	assert.True(t, notAuthorized(get(other)), "an access point tagged otherwise is refused")
}

// TestRDS_SourceConditionKeysScopeTheGrant covers the Amazon RDS keys a
// request settles from the source it copies: the engine, name, encryption and
// tags of the DB cluster a blue/green deployment clones, the tags of the
// parameter groups it names, the backup target of the DB instance a snapshot
// is taken of, and the storage of the DB cluster a point-in-time restore reads.
func TestRDS_SourceConditionKeysScopeTheGrant(t *testing.T) {
	admin := rdsClient()
	tags := func(team string) []rdstypes.Tag {
		return []rdstypes.Tag{{Key: aws.String("team"), Value: aws.String(team)}}
	}
	cluster := func(team string) string {
		id := uniqueName("source-cluster")
		out, err := admin.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
			DBClusterIdentifier: aws.String(id), Engine: aws.String("mysql"), DatabaseName: aws.String("orders"),
			MasterUsername: aws.String("admin"), DBClusterInstanceClass: aws.String("db.m6gd.large"),
			AllocatedStorage: aws.Int32(100), StorageEncrypted: aws.Bool(true), Tags: tags(team),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{DBClusterIdentifier: aws.String(id),
				SkipFinalSnapshot: aws.Bool(true)})
		})
		return aws.ToString(out.DBCluster.DBClusterArn)
	}
	dataCluster, webCluster := cluster("data"), cluster("web")
	group := uniqueName("green-params")
	_, err := admin.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(group), DBParameterGroupFamily: aws.String("mysql8.0"),
		Description: aws.String("green"), Tags: tags("data"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{
			DBClusterParameterGroupName: aws.String(group)})
	})

	akid, secret := restrictedCredential(t, "rds-source-keys",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"rds:CreateBlueGreenDeployment","Resource":"*","Condition":{
		    "StringEquals":{"rds:DatabaseEngine":"mysql","rds:cluster-tag/team":"data","rds:cluster-pg-tag/team":"data"},
		    "Bool":{"rds:StorageEncrypted":"true","rds:Vpc":"true"}}},
		  {"Effect":"Allow","Action":"rds:RestoreDBClusterToPointInTime","Resource":"*","Condition":{
		    "NumericLessThanEquals":{"rds:StorageSize":"100"}}},
		  {"Effect":"Allow","Action":"rds:CreateDBSnapshot","Resource":"*","Condition":{
		    "StringEquals":{"rds:BackupTarget":"outposts"}}}]}`)
	restricted := rds.NewFromConfig(keyConfig(akid, secret), func(o *rds.Options) { o.BaseEndpoint = aws.String(baseURL) })

	blueGreen := func(source string) error {
		out, err := restricted.CreateBlueGreenDeployment(ctx, &rds.CreateBlueGreenDeploymentInput{
			BlueGreenDeploymentName: aws.String(uniqueName("bg")), Source: aws.String(source),
			TargetDBClusterParameterGroupName: aws.String(group),
		})
		if err == nil {
			t.Cleanup(func() {
				_, _ = admin.DeleteBlueGreenDeployment(ctx, &rds.DeleteBlueGreenDeploymentInput{
					BlueGreenDeploymentIdentifier: out.BlueGreenDeployment.BlueGreenDeploymentIdentifier})
			})
		}
		return err
	}
	assert.NoError(t, blueGreen(dataCluster), "the source is the encrypted MySQL cluster of the team the grant names")
	assert.True(t, notAuthorized(blueGreen(webCluster)), "a source tagged for another team is refused")

	restore := func(source string) error {
		_, err := restricted.RestoreDBClusterToPointInTime(ctx, &rds.RestoreDBClusterToPointInTimeInput{
			DBClusterIdentifier: aws.String(uniqueName("restored")), SourceDBClusterIdentifier: aws.String(source),
			UseLatestRestorableTime: aws.Bool(true),
		})
		return err
	}
	assert.False(t, notAuthorized(restore(dataCluster)), "the source's 100 GiB is within the grant")
	big, err := admin.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(uniqueName("big-cluster")), Engine: aws.String("mysql"),
		MasterUsername: aws.String("admin"), DBClusterInstanceClass: aws.String("db.m6gd.large"),
		AllocatedStorage: aws.Int32(400),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{DBClusterIdentifier: big.DBCluster.DBClusterIdentifier,
			SkipFinalSnapshot: aws.Bool(true)})
	})
	assert.True(t, notAuthorized(restore(aws.ToString(big.DBCluster.DBClusterArn))),
		"a source larger than the grant allows is refused")

	instance := uniqueName("region-backups")
	_, err = admin.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instance), Engine: aws.String("sqlserver-ex"),
		DBInstanceClass: aws.String("db.t3.micro"), AllocatedStorage: aws.Int32(20),
		MasterUsername: aws.String("admin"), MasterUserPassword: aws.String("probe-password-1"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(instance),
			SkipFinalSnapshot: aws.Bool(true)})
	})
	described, err := admin.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(instance)})
	require.NoError(t, err)
	assert.Equal(t, "region", aws.ToString(described.DBInstances[0].BackupTarget),
		"an instance created without a BackupTarget keeps its backups in the Region")
	_, err = restricted.CreateDBSnapshot(ctx, &rds.CreateDBSnapshotInput{
		DBInstanceIdentifier: aws.String(instance), DBSnapshotIdentifier: aws.String(uniqueName("snap"))})
	assert.True(t, notAuthorized(err), "the instance's backups go to the Region, not to an Outpost")
}

// TestOrganizations_PolicyTypeAndTransferTypeScopeTheGrant covers the policy
// type a listing filters on and the type of the responsibility transfer a
// request names by id.
func TestOrganizations_PolicyTypeAndTransferTypeScopeTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "org-scp-billing",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"organizations:ListPolicies","Resource":"*",
		   "Condition":{"StringEquals":{"organizations:PolicyType":"SERVICE_CONTROL_POLICY"}}},
		  {"Effect":"Allow","Action":"organizations:DescribeResponsibilityTransfer","Resource":"*",
		   "Condition":{"StringEquals":{"organizations:TransferType":"BILLING"}}}]}`)
	restricted := organizations.NewFromConfig(keyConfig(akid, secret),
		func(o *organizations.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.ListPolicies(ctx, &organizations.ListPoliciesInput{Filter: orgtypes.PolicyTypeServiceControlPolicy})
	assert.NoError(t, err, "the listing filters on the policy type the grant names")
	_, err = restricted.ListPolicies(ctx, &organizations.ListPoliciesInput{Filter: orgtypes.PolicyTypeTagPolicy})
	assert.True(t, notAuthorized(err), "a listing of another policy type is refused")

	admin := orgClient()
	_, err = admin.InviteOrganizationToTransferResponsibility(ctx, &organizations.InviteOrganizationToTransferResponsibilityInput{
		Type:           orgtypes.ResponsibilityTransferTypeBilling,
		Target:         &orgtypes.HandshakeParty{Id: aws.String("210987654321"), Type: orgtypes.HandshakePartyTypeAccount},
		SourceName:     aws.String(uniqueName("billing")),
		StartTimestamp: aws.Time(time.Now().Add(time.Hour)),
	})
	require.NoError(t, err)
	transfers, err := admin.ListOutboundResponsibilityTransfers(ctx, &organizations.ListOutboundResponsibilityTransfersInput{
		Type: orgtypes.ResponsibilityTransferTypeBilling})
	require.NoError(t, err)
	require.NotEmpty(t, transfers.ResponsibilityTransfers)
	_, err = restricted.DescribeResponsibilityTransfer(ctx, &organizations.DescribeResponsibilityTransferInput{
		Id: transfers.ResponsibilityTransfers[0].Id})
	assert.NoError(t, err, "the transfer the request names is a billing transfer")
}

// TestGlue_IntegrationResourceTagScopesTheGrant covers aws:ResourceTag on the
// requests that name a zero-ETL integration.
func TestGlue_IntegrationResourceTagScopesTheGrant(t *testing.T) {
	admin := glueClient()
	create := func(team string) string {
		name := uniqueName("integration")
		_, err := admin.CreateIntegration(ctx, &glue.CreateIntegrationInput{
			IntegrationName: aws.String(name),
			SourceArn:       aws.String("arn:aws:rds:us-east-1:123456789012:cluster:orders"),
			TargetArn:       aws.String("arn:aws:glue:us-east-1:123456789012:catalog"),
			Tags:            []gluetypes.Tag{{Key: aws.String("team"), Value: aws.String(team)}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteIntegration(ctx, &glue.DeleteIntegrationInput{IntegrationIdentifier: aws.String(name)})
		})
		return name
	}
	data, web := create("data"), create("web")
	akid, secret := restrictedCredential(t, "glue-data-integrations",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"glue:DescribeIntegrations","Resource":"*",
		  "Condition":{"StringEquals":{"aws:ResourceTag/team":"data"}}}]}`)
	restricted := glue.NewFromConfig(keyConfig(akid, secret), func(o *glue.Options) { o.BaseEndpoint = aws.String(baseURL) })
	describe := func(name string) error {
		_, err := restricted.DescribeIntegrations(ctx, &glue.DescribeIntegrationsInput{IntegrationIdentifier: aws.String(name)})
		return err
	}
	assert.NoError(t, describe(data))
	assert.True(t, notAuthorized(describe(web)))
}

// TestCloudWatch_DefaultDatasetResourceTagScopesTheGrant covers the default
// metrics dataset, the only one CloudWatch supports: it exists without being
// created, it carries the tags TagResource gives it, and aws:ResourceTag on
// GetDataset reports them.
func TestCloudWatch_DefaultDatasetResourceTagScopesTheGrant(t *testing.T) {
	admin := cloudwatchClient()
	got, err := admin.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String("default")})
	require.NoError(t, err)
	_, err = admin.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String(
		"arn:aws:cloudwatch:us-east-1:123456789012:dataset/default")})
	require.NoError(t, err, "the dataset is named by its id or its ARN")

	akid, secret := restrictedCredential(t, "cw-tagged-dataset",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudwatch:GetDataset","Resource":"*",
		  "Condition":{"StringEquals":{"aws:ResourceTag/classification":"internal"}}}]}`)
	restricted := cloudwatch.NewFromConfig(keyConfig(akid, secret), func(o *cloudwatch.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
	get := func() error {
		_, err := restricted.GetDataset(ctx, &cloudwatch.GetDatasetInput{DatasetIdentifier: aws.String("default")})
		return err
	}
	_, err = admin.UntagResource(ctx, &cloudwatch.UntagResourceInput{ResourceARN: got.Arn, TagKeys: []string{"classification"}})
	require.NoError(t, err)
	assert.True(t, notAuthorized(get()), "the dataset does not yet carry the tag the grant names")

	_, err = admin.TagResource(ctx, &cloudwatch.TagResourceInput{ResourceARN: got.Arn,
		Tags: []cwtypes.Tag{{Key: aws.String("classification"), Value: aws.String("internal")}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.UntagResource(ctx, &cloudwatch.UntagResourceInput{ResourceARN: got.Arn, TagKeys: []string{"classification"}})
	})
	assert.NoError(t, get(), "once tagged, the dataset is the one the grant names")
}

// TestSTS_ChainedAssumeRoleCarriesTheSAMLSubject covers the SAML claims AWS STS
// keeps on a session for the AssumeRole it chains to: a role trusting the first
// role only for one SAML subject admits that person's session and no other's.
func TestSTS_ChainedAssumeRoleCarriesTheSAMLSubject(t *testing.T) {
	idp, err := samlidp.New("https://idp.chain.example.test/saml")
	require.NoError(t, err)
	admin := iamClient()
	providerName := uniqueName("chain-idp")
	provider, err := admin.CreateSAMLProvider(ctx, &iam.CreateSAMLProviderInput{
		Name: aws.String(providerName), SAMLMetadataDocument: aws.String(idp.Metadata())})
	require.NoError(t, err)
	providerArn := aws.ToString(provider.SAMLProviderArn)
	t.Cleanup(func() {
		_, _ = admin.DeleteSAMLProvider(ctx, &iam.DeleteSAMLProviderInput{SAMLProviderArn: aws.String(providerArn)})
	})
	createRole := func(name, trust string) string {
		role, err := admin.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name),
			AssumeRolePolicyDocument: aws.String(trust)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)}) })
		return aws.ToString(role.Role.Arn)
	}
	federatedName, chainedName := uniqueName("federated"), uniqueName("chained")
	federatedArn := createRole(federatedName, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Federated":"`+providerArn+`"},"Action":"sts:AssumeRoleWithSAML"}]}`)
	chainedArn := createRole(chainedName, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"`+federatedArn+`"},"Action":"sts:AssumeRole",
		"Condition":{"StringEquals":{"saml:sub":"alice@example.test","saml:sub_type":"persistent"}}}]}`)
	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(federatedName),
		PolicyName: aws.String("chain"), PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"` + chainedArn + `"}]}`)})
	require.NoError(t, err)

	chain := func(subject string) error {
		response, err := idp.Response(samlidp.Assertion{Subject: subject, Attributes: map[string][]string{
			samlidp.RoleAttribute:            {federatedArn + "," + providerArn},
			samlidp.RoleSessionNameAttribute: {"session"},
		}})
		require.NoError(t, err)
		federated, err := stsClient().AssumeRoleWithSAML(ctx, &sts.AssumeRoleWithSAMLInput{
			RoleArn: aws.String(federatedArn), PrincipalArn: aws.String(providerArn), SAMLAssertion: aws.String(response)})
		require.NoError(t, err)
		session := sts.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(federated.Credentials.AccessKeyId), aws.ToString(federated.Credentials.SecretAccessKey),
			aws.ToString(federated.Credentials.SessionToken))}, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
		_, err = session.AssumeRole(ctx, &sts.AssumeRoleInput{RoleArn: aws.String(chainedArn),
			RoleSessionName: aws.String("chained")})
		return err
	}
	assert.NoError(t, chain("alice@example.test"), "the chained role trusts this SAML subject")
	err = chain("mallory@example.test")
	require.Error(t, err)
	assert.Equal(t, "AccessDenied", errCodeOf(err), "another subject's session is refused")
}
