package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDS_DBProxyLifecycle exercises the RDS Proxy surface end to end:
// CreateDBProxy → DescribeDBProxies → ModifyDBProxy → CreateDBProxyEndpoint
// → DescribeDBProxyEndpoints → ModifyDBProxyEndpoint → DescribeDBProxyTargetGroups
// → ModifyDBProxyTargetGroup → RegisterDBProxyTargets → DescribeDBProxyTargets
// → DeregisterDBProxyTargets → DeleteDBProxyEndpoint → DeleteDBProxy.
func TestRDS_DBProxyLifecycle(t *testing.T) {
	c := rdsClient()

	// Backing instance the proxy will target.
	instID := "proxy-target-db"
	_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("mysql"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	proxyName := "sdk-test-proxy"
	createOut, err := c.CreateDBProxy(ctx, &rds.CreateDBProxyInput{
		DBProxyName:  aws.String(proxyName),
		EngineFamily: rdstypes.EngineFamilyMysql,
		RoleArn:      aws.String("arn:aws:iam::123456789012:role/rds-proxy-role"),
		Auth: []rdstypes.UserAuthConfig{
			{AuthScheme: rdstypes.AuthSchemeSecrets, SecretArn: aws.String("arn:aws:secretsmanager:us-east-1:123456789012:secret:db")},
		},
		VpcSubnetIds:        []string{"subnet-1111", "subnet-2222"},
		VpcSecurityGroupIds: []string{"sg-aaaa"},
		RequireTLS:          aws.Bool(true),
		IdleClientTimeout:   aws.Int32(900),
	})
	require.NoError(t, err)
	require.NotNil(t, createOut.DBProxy)
	assert.Equal(t, proxyName, aws.ToString(createOut.DBProxy.DBProxyName))
	assert.Equal(t, "available", string(createOut.DBProxy.Status))
	assert.Equal(t, "MYSQL", aws.ToString(createOut.DBProxy.EngineFamily))
	assert.True(t, aws.ToBool(createOut.DBProxy.RequireTLS))
	assert.Equal(t, int32(900), aws.ToInt32(createOut.DBProxy.IdleClientTimeout))
	assert.NotEmpty(t, aws.ToString(createOut.DBProxy.DBProxyArn))
	assert.NotEmpty(t, aws.ToString(createOut.DBProxy.Endpoint))
	assert.ElementsMatch(t, []string{"subnet-1111", "subnet-2222"}, createOut.DBProxy.VpcSubnetIds)
	t.Cleanup(func() {
		_, _ = c.DeleteDBProxy(ctx, &rds.DeleteDBProxyInput{DBProxyName: aws.String(proxyName)})
	})

	descOut, err := c.DescribeDBProxies(ctx, &rds.DescribeDBProxiesInput{DBProxyName: aws.String(proxyName)})
	require.NoError(t, err)
	require.Len(t, descOut.DBProxies, 1)
	assert.Equal(t, proxyName, aws.ToString(descOut.DBProxies[0].DBProxyName))

	modOut, err := c.ModifyDBProxy(ctx, &rds.ModifyDBProxyInput{
		DBProxyName:       aws.String(proxyName),
		IdleClientTimeout: aws.Int32(600),
		DebugLogging:      aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(600), aws.ToInt32(modOut.DBProxy.IdleClientTimeout))
	assert.True(t, aws.ToBool(modOut.DBProxy.DebugLogging))

	// Proxy endpoint.
	epName := "sdk-test-proxy-ep"
	epOut, err := c.CreateDBProxyEndpoint(ctx, &rds.CreateDBProxyEndpointInput{
		DBProxyName:         aws.String(proxyName),
		DBProxyEndpointName: aws.String(epName),
		VpcSubnetIds:        []string{"subnet-1111", "subnet-2222"},
		TargetRole:          rdstypes.DBProxyEndpointTargetRoleReadOnly,
	})
	require.NoError(t, err)
	require.NotNil(t, epOut.DBProxyEndpoint)
	assert.Equal(t, epName, aws.ToString(epOut.DBProxyEndpoint.DBProxyEndpointName))
	assert.Equal(t, "available", string(epOut.DBProxyEndpoint.Status))
	assert.Equal(t, "READ_ONLY", string(epOut.DBProxyEndpoint.TargetRole))
	t.Cleanup(func() {
		_, _ = c.DeleteDBProxyEndpoint(ctx, &rds.DeleteDBProxyEndpointInput{DBProxyEndpointName: aws.String(epName)})
	})

	epDesc, err := c.DescribeDBProxyEndpoints(ctx, &rds.DescribeDBProxyEndpointsInput{DBProxyEndpointName: aws.String(epName)})
	require.NoError(t, err)
	require.Len(t, epDesc.DBProxyEndpoints, 1)

	epMod, err := c.ModifyDBProxyEndpoint(ctx, &rds.ModifyDBProxyEndpointInput{
		DBProxyEndpointName:    aws.String(epName),
		NewDBProxyEndpointName: aws.String(epName + "-renamed"),
	})
	require.NoError(t, err)
	assert.Equal(t, epName+"-renamed", aws.ToString(epMod.DBProxyEndpoint.DBProxyEndpointName))
	t.Cleanup(func() {
		_, _ = c.DeleteDBProxyEndpoint(ctx, &rds.DeleteDBProxyEndpointInput{DBProxyEndpointName: aws.String(epName + "-renamed")})
	})

	// Target group.
	tgOut, err := c.DescribeDBProxyTargetGroups(ctx, &rds.DescribeDBProxyTargetGroupsInput{DBProxyName: aws.String(proxyName)})
	require.NoError(t, err)
	require.Len(t, tgOut.TargetGroups, 1)
	assert.Equal(t, "default", aws.ToString(tgOut.TargetGroups[0].TargetGroupName))
	assert.True(t, aws.ToBool(tgOut.TargetGroups[0].IsDefault))
	require.NotNil(t, tgOut.TargetGroups[0].ConnectionPoolConfig)

	tgMod, err := c.ModifyDBProxyTargetGroup(ctx, &rds.ModifyDBProxyTargetGroupInput{
		DBProxyName:     aws.String(proxyName),
		TargetGroupName: aws.String("default"),
		ConnectionPoolConfig: &rdstypes.ConnectionPoolConfiguration{
			MaxConnectionsPercent:     aws.Int32(80),
			MaxIdleConnectionsPercent: aws.Int32(40),
			ConnectionBorrowTimeout:   aws.Int32(150),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, tgMod.DBProxyTargetGroup)
	assert.Equal(t, int32(80), aws.ToInt32(tgMod.DBProxyTargetGroup.ConnectionPoolConfig.MaxConnectionsPercent))

	// Targets.
	regOut, err := c.RegisterDBProxyTargets(ctx, &rds.RegisterDBProxyTargetsInput{
		DBProxyName:           aws.String(proxyName),
		DBInstanceIdentifiers: []string{instID},
	})
	require.NoError(t, err)
	require.Len(t, regOut.DBProxyTargets, 1)
	assert.Equal(t, "RDS_INSTANCE", string(regOut.DBProxyTargets[0].Type))

	tDesc, err := c.DescribeDBProxyTargets(ctx, &rds.DescribeDBProxyTargetsInput{DBProxyName: aws.String(proxyName)})
	require.NoError(t, err)
	require.Len(t, tDesc.Targets, 1)

	_, err = c.DeregisterDBProxyTargets(ctx, &rds.DeregisterDBProxyTargetsInput{
		DBProxyName:           aws.String(proxyName),
		DBInstanceIdentifiers: []string{instID},
	})
	require.NoError(t, err)

	tDesc2, err := c.DescribeDBProxyTargets(ctx, &rds.DescribeDBProxyTargetsInput{DBProxyName: aws.String(proxyName)})
	require.NoError(t, err)
	assert.Empty(t, tDesc2.Targets)
}

// TestRDS_RolesAndSecurityGroups exercises IAM-role associations on a DB
// cluster and a DB instance plus the EC2-Classic-style DB security group
// ingress surface.
func TestRDS_RolesAndSecurityGroups(t *testing.T) {
	c := rdsClient()

	clusterID := "roles-cluster"
	_, err := c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-mysql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	clusterRole := "arn:aws:iam::123456789012:role/AuroraAccessRole"
	_, err = c.AddRoleToDBCluster(ctx, &rds.AddRoleToDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		RoleArn:             aws.String(clusterRole),
		FeatureName:         aws.String("s3Import"),
	})
	require.NoError(t, err)
	_, err = c.RemoveRoleFromDBCluster(ctx, &rds.RemoveRoleFromDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		RoleArn:             aws.String(clusterRole),
		FeatureName:         aws.String("s3Import"),
	})
	require.NoError(t, err)

	instID := "roles-instance"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	instRole := "arn:aws:iam::123456789012:role/InstanceAccessRole"
	_, err = c.AddRoleToDBInstance(ctx, &rds.AddRoleToDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		RoleArn:              aws.String(instRole),
		FeatureName:          aws.String("S3_INTEGRATION"),
	})
	require.NoError(t, err)
	_, err = c.RemoveRoleFromDBInstance(ctx, &rds.RemoveRoleFromDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		RoleArn:              aws.String(instRole),
		FeatureName:          aws.String("S3_INTEGRATION"),
	})
	require.NoError(t, err)

	// DB security groups.
	sgName := "sdk-test-dbsg"
	sgOut, err := c.CreateDBSecurityGroup(ctx, &rds.CreateDBSecurityGroupInput{
		DBSecurityGroupName:        aws.String(sgName),
		DBSecurityGroupDescription: aws.String("sdk test db security group"),
	})
	require.NoError(t, err)
	require.NotNil(t, sgOut.DBSecurityGroup)
	assert.Equal(t, sgName, aws.ToString(sgOut.DBSecurityGroup.DBSecurityGroupName))
	t.Cleanup(func() {
		_, _ = c.DeleteDBSecurityGroup(ctx, &rds.DeleteDBSecurityGroupInput{DBSecurityGroupName: aws.String(sgName)})
	})

	authOut, err := c.AuthorizeDBSecurityGroupIngress(ctx, &rds.AuthorizeDBSecurityGroupIngressInput{
		DBSecurityGroupName: aws.String(sgName),
		CIDRIP:              aws.String("10.0.0.0/24"),
	})
	require.NoError(t, err)
	require.Len(t, authOut.DBSecurityGroup.IPRanges, 1)
	assert.Equal(t, "10.0.0.0/24", aws.ToString(authOut.DBSecurityGroup.IPRanges[0].CIDRIP))

	revOut, err := c.RevokeDBSecurityGroupIngress(ctx, &rds.RevokeDBSecurityGroupIngressInput{
		DBSecurityGroupName: aws.String(sgName),
		CIDRIP:              aws.String("10.0.0.0/24"),
	})
	require.NoError(t, err)
	assert.Empty(t, revOut.DBSecurityGroup.IPRanges)

	descSG, err := c.DescribeDBSecurityGroups(ctx, &rds.DescribeDBSecurityGroupsInput{DBSecurityGroupName: aws.String(sgName)})
	require.NoError(t, err)
	require.Len(t, descSG.DBSecurityGroups, 1)
}

// TestRDS_CertificatesAndBackups exercises the certificate surface,
// per-instance and per-cluster automated backups, and DB log files.
func TestRDS_CertificatesAndBackups(t *testing.T) {
	c := rdsClient()

	certs, err := c.DescribeCertificates(ctx, &rds.DescribeCertificatesInput{})
	require.NoError(t, err)
	require.NotEmpty(t, certs.Certificates)
	certID := aws.ToString(certs.Certificates[0].CertificateIdentifier)
	assert.NotEmpty(t, certID)
	assert.Equal(t, "CA", aws.ToString(certs.Certificates[0].CertificateType))

	modCert, err := c.ModifyCertificates(ctx, &rds.ModifyCertificatesInput{
		CertificateIdentifier: aws.String(certID),
	})
	require.NoError(t, err)
	require.NotNil(t, modCert.Certificate)
	assert.True(t, aws.ToBool(modCert.Certificate.CustomerOverride))
	t.Cleanup(func() {
		_, _ = c.ModifyCertificates(ctx, &rds.ModifyCertificatesInput{RemoveCustomerOverride: aws.Bool(true)})
	})

	// Automated backups need an instance and a cluster.
	instID := "backup-instance"
	_, err = c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	ab, err := c.DescribeDBInstanceAutomatedBackups(ctx, &rds.DescribeDBInstanceAutomatedBackupsInput{
		DBInstanceIdentifier: aws.String(instID),
	})
	require.NoError(t, err)
	require.Len(t, ab.DBInstanceAutomatedBackups, 1)
	assert.Equal(t, instID, aws.ToString(ab.DBInstanceAutomatedBackups[0].DBInstanceIdentifier))
	abArn := aws.ToString(ab.DBInstanceAutomatedBackups[0].DBInstanceAutomatedBackupsArn)
	require.NotEmpty(t, abArn)

	_, err = c.DeleteDBInstanceAutomatedBackup(ctx, &rds.DeleteDBInstanceAutomatedBackupInput{
		DBInstanceAutomatedBackupsArn: aws.String(abArn),
	})
	require.NoError(t, err)

	clusterID := "backup-cluster"
	_, err = c.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
		DBClusterIdentifier: aws.String(clusterID),
		Engine:              aws.String("aurora-postgresql"),
		MasterUsername:      aws.String("admin"),
		MasterUserPassword:  aws.String("password123!"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBCluster(ctx, &rds.DeleteDBClusterInput{
			DBClusterIdentifier: aws.String(clusterID),
			SkipFinalSnapshot:   aws.Bool(true),
		})
	})

	cab, err := c.DescribeDBClusterAutomatedBackups(ctx, &rds.DescribeDBClusterAutomatedBackupsInput{
		DBClusterIdentifier: aws.String(clusterID),
	})
	require.NoError(t, err)
	require.Len(t, cab.DBClusterAutomatedBackups, 1)
	cabArn := aws.ToString(cab.DBClusterAutomatedBackups[0].DBClusterAutomatedBackupsArn)
	require.NotEmpty(t, cabArn)
	_, err = c.DeleteDBClusterAutomatedBackup(ctx, &rds.DeleteDBClusterAutomatedBackupInput{
		DbClusterResourceId: cab.DBClusterAutomatedBackups[0].DbClusterResourceId,
	})
	require.NoError(t, err)

	// Log files.
	logs, err := c.DescribeDBLogFiles(ctx, &rds.DescribeDBLogFilesInput{DBInstanceIdentifier: aws.String(instID)})
	require.NoError(t, err)
	require.NotEmpty(t, logs.DescribeDBLogFiles)
	logName := aws.ToString(logs.DescribeDBLogFiles[0].LogFileName)
	require.NotEmpty(t, logName)

	dl, err := c.DownloadDBLogFilePortion(ctx, &rds.DownloadDBLogFilePortionInput{
		DBInstanceIdentifier: aws.String(instID),
		LogFileName:          aws.String(logName),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(dl.LogFileData))
}

// TestRDS_CopyGroupsAndSourceIdentifiers exercises CopyDBParameterGroup,
// CopyDBClusterParameterGroup, CopyOptionGroup, and the event
// subscription source-identifier add/remove surface.
func TestRDS_CopyGroupsAndSourceIdentifiers(t *testing.T) {
	c := rdsClient()

	// DB parameter group copy.
	srcPG := "copy-src-pg"
	_, err := c.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String(srcPG),
		DBParameterGroupFamily: aws.String("postgres15"),
		Description:            aws.String("source pg"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(srcPG)})
	})
	dstPG := "copy-dst-pg"
	cpPG, err := c.CopyDBParameterGroup(ctx, &rds.CopyDBParameterGroupInput{
		SourceDBParameterGroupIdentifier:  aws.String(srcPG),
		TargetDBParameterGroupIdentifier:  aws.String(dstPG),
		TargetDBParameterGroupDescription: aws.String("copied pg"),
	})
	require.NoError(t, err)
	assert.Equal(t, dstPG, aws.ToString(cpPG.DBParameterGroup.DBParameterGroupName))
	assert.Equal(t, "postgres15", aws.ToString(cpPG.DBParameterGroup.DBParameterGroupFamily))
	t.Cleanup(func() {
		_, _ = c.DeleteDBParameterGroup(ctx, &rds.DeleteDBParameterGroupInput{DBParameterGroupName: aws.String(dstPG)})
	})

	// DB cluster parameter group copy.
	srcCPG := "copy-src-cpg"
	_, err = c.CreateDBClusterParameterGroup(ctx, &rds.CreateDBClusterParameterGroupInput{
		DBClusterParameterGroupName: aws.String(srcCPG),
		DBParameterGroupFamily:      aws.String("aurora-mysql8.0"),
		Description:                 aws.String("source cpg"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(srcCPG)})
	})
	dstCPG := "copy-dst-cpg"
	cpCPG, err := c.CopyDBClusterParameterGroup(ctx, &rds.CopyDBClusterParameterGroupInput{
		SourceDBClusterParameterGroupIdentifier:  aws.String(srcCPG),
		TargetDBClusterParameterGroupIdentifier:  aws.String(dstCPG),
		TargetDBClusterParameterGroupDescription: aws.String("copied cpg"),
	})
	require.NoError(t, err)
	assert.Equal(t, dstCPG, aws.ToString(cpCPG.DBClusterParameterGroup.DBClusterParameterGroupName))
	t.Cleanup(func() {
		_, _ = c.DeleteDBClusterParameterGroup(ctx, &rds.DeleteDBClusterParameterGroupInput{DBClusterParameterGroupName: aws.String(dstCPG)})
	})

	// Option group copy.
	srcOG := "copy-src-og"
	_, err = c.CreateOptionGroup(ctx, &rds.CreateOptionGroupInput{
		OptionGroupName:        aws.String(srcOG),
		EngineName:             aws.String("mysql"),
		MajorEngineVersion:     aws.String("8.0"),
		OptionGroupDescription: aws.String("source og"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{OptionGroupName: aws.String(srcOG)})
	})
	dstOG := "copy-dst-og"
	cpOG, err := c.CopyOptionGroup(ctx, &rds.CopyOptionGroupInput{
		SourceOptionGroupIdentifier:  aws.String(srcOG),
		TargetOptionGroupIdentifier:  aws.String(dstOG),
		TargetOptionGroupDescription: aws.String("copied og"),
	})
	require.NoError(t, err)
	assert.Equal(t, dstOG, aws.ToString(cpOG.OptionGroup.OptionGroupName))
	assert.Equal(t, "mysql", aws.ToString(cpOG.OptionGroup.EngineName))
	t.Cleanup(func() {
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{OptionGroupName: aws.String(dstOG)})
	})

	// Event subscription source identifiers.
	subName := "src-id-sub"
	_, err = c.CreateEventSubscription(ctx, &rds.CreateEventSubscriptionInput{
		SubscriptionName: aws.String(subName),
		SnsTopicArn:      aws.String("arn:aws:sns:us-east-1:123456789012:rds-events"),
		SourceType:       aws.String("db-instance"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteEventSubscription(ctx, &rds.DeleteEventSubscriptionInput{SubscriptionName: aws.String(subName)})
	})
	addOut, err := c.AddSourceIdentifierToSubscription(ctx, &rds.AddSourceIdentifierToSubscriptionInput{
		SubscriptionName: aws.String(subName),
		SourceIdentifier: aws.String("my-db-instance"),
	})
	require.NoError(t, err)
	require.NotNil(t, addOut.EventSubscription)
	assert.Contains(t, addOut.EventSubscription.SourceIdsList, "my-db-instance")

	remOut, err := c.RemoveSourceIdentifierFromSubscription(ctx, &rds.RemoveSourceIdentifierFromSubscriptionInput{
		SubscriptionName: aws.String(subName),
		SourceIdentifier: aws.String("my-db-instance"),
	})
	require.NoError(t, err)
	assert.NotContains(t, remOut.EventSubscription.SourceIdsList, "my-db-instance")
}

// TestRDS_PendingMaintenanceActions exercises
// DescribePendingMaintenanceActions and ApplyPendingMaintenanceAction
// against a DB instance resource.
func TestRDS_PendingMaintenanceActions(t *testing.T) {
	c := rdsClient()

	instID := "pma-instance"
	createOut, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		MasterUsername:       aws.String("admin"),
		MasterUserPassword:   aws.String("password123!"),
		AllocatedStorage:     aws.Int32(20),
	})
	require.NoError(t, err)
	instArn := aws.ToString(createOut.DBInstance.DBInstanceArn)
	require.NotEmpty(t, instArn)
	t.Cleanup(func() {
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})

	pma, err := c.DescribePendingMaintenanceActions(ctx, &rds.DescribePendingMaintenanceActionsInput{
		ResourceIdentifier: aws.String(instArn),
	})
	require.NoError(t, err)
	require.Len(t, pma.PendingMaintenanceActions, 1)
	require.NotEmpty(t, pma.PendingMaintenanceActions[0].PendingMaintenanceActionDetails)
	action := aws.ToString(pma.PendingMaintenanceActions[0].PendingMaintenanceActionDetails[0].Action)
	require.NotEmpty(t, action)

	applied, err := c.ApplyPendingMaintenanceAction(ctx, &rds.ApplyPendingMaintenanceActionInput{
		ResourceIdentifier: aws.String(instArn),
		ApplyAction:        aws.String(action),
		OptInType:          aws.String("immediate"),
	})
	require.NoError(t, err)
	require.NotNil(t, applied.ResourcePendingMaintenanceActions)
	require.NotEmpty(t, applied.ResourcePendingMaintenanceActions.PendingMaintenanceActionDetails)
	assert.Equal(t, "immediate", aws.ToString(applied.ResourcePendingMaintenanceActions.PendingMaintenanceActionDetails[0].OptInStatus))
}
