package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_ProxiesRolesAndExtras drives the RDS proxy / IAM-role /
// security-group / certificate / automated-backup / log-file /
// copy-group / source-identifier / pending-maintenance surface end to
// end through the aws CLI. Grouped into one round-trip func (the appdata
// shard is already heavy).
func TestRDSCLI_ProxiesRolesAndExtras(t *testing.T) {
	// --- backing instance + cluster ---
	instID := "cli-px-target-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID,
			"--skip-final-snapshot").Run()
	})

	clusterID := "cli-px-cluster"
	runCLI(t, awsCLI("rds", "create-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--engine", "aurora-mysql",
		"--master-username", "admin",
		"--master-user-password", "password123!"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster",
			"--db-cluster-identifier", clusterID,
			"--skip-final-snapshot").Run()
	})

	// --- DB proxy ---
	proxyName := "cli-test-proxy"
	out := runCLI(t, awsCLI("rds", "create-db-proxy",
		"--db-proxy-name", proxyName,
		"--engine-family", "MYSQL",
		"--role-arn", "arn:aws:iam::123456789012:role/rds-proxy-role",
		"--auth", "AuthScheme=SECRETS,SecretArn=arn:aws:secretsmanager:us-east-1:123456789012:secret:db",
		"--vpc-subnet-ids", "subnet-1111", "subnet-2222"))
	var createProxy struct {
		DBProxy struct {
			DBProxyName  string `json:"DBProxyName"`
			Status       string `json:"Status"`
			EngineFamily string `json:"EngineFamily"`
			DBProxyArn   string `json:"DBProxyArn"`
		} `json:"DBProxy"`
	}
	parseJSON(t, out, &createProxy)
	require.Equal(t, proxyName, createProxy.DBProxy.DBProxyName)
	assert.Equal(t, "available", createProxy.DBProxy.Status)
	assert.Equal(t, "MYSQL", createProxy.DBProxy.EngineFamily)
	assert.NotEmpty(t, createProxy.DBProxy.DBProxyArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-proxy", "--db-proxy-name", proxyName).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-proxies", "--db-proxy-name", proxyName))
	var descProxy struct {
		DBProxies []struct {
			DBProxyName string `json:"DBProxyName"`
		} `json:"DBProxies"`
	}
	parseJSON(t, out, &descProxy)
	require.Len(t, descProxy.DBProxies, 1)

	runCLI(t, awsCLI("rds", "modify-db-proxy",
		"--db-proxy-name", proxyName,
		"--idle-client-timeout", "600",
		"--debug-logging"))

	// --- proxy endpoint ---
	epName := "cli-test-proxy-ep"
	out = runCLI(t, awsCLI("rds", "create-db-proxy-endpoint",
		"--db-proxy-name", proxyName,
		"--db-proxy-endpoint-name", epName,
		"--vpc-subnet-ids", "subnet-1111", "subnet-2222",
		"--target-role", "READ_ONLY"))
	var createEP struct {
		DBProxyEndpoint struct {
			DBProxyEndpointName string `json:"DBProxyEndpointName"`
			Status              string `json:"Status"`
			TargetRole          string `json:"TargetRole"`
		} `json:"DBProxyEndpoint"`
	}
	parseJSON(t, out, &createEP)
	require.Equal(t, epName, createEP.DBProxyEndpoint.DBProxyEndpointName)
	assert.Equal(t, "available", createEP.DBProxyEndpoint.Status)
	assert.Equal(t, "READ_ONLY", createEP.DBProxyEndpoint.TargetRole)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-proxy-endpoint", "--db-proxy-endpoint-name", epName).Run()
	})

	runCLI(t, awsCLI("rds", "describe-db-proxy-endpoints", "--db-proxy-endpoint-name", epName))
	runCLI(t, awsCLI("rds", "modify-db-proxy-endpoint",
		"--db-proxy-endpoint-name", epName,
		"--new-db-proxy-endpoint-name", epName))

	// --- target group + targets ---
	out = runCLI(t, awsCLI("rds", "describe-db-proxy-target-groups", "--db-proxy-name", proxyName))
	var tgList struct {
		TargetGroups []struct {
			TargetGroupName string `json:"TargetGroupName"`
			IsDefault       bool   `json:"IsDefault"`
		} `json:"TargetGroups"`
	}
	parseJSON(t, out, &tgList)
	require.Len(t, tgList.TargetGroups, 1)
	assert.Equal(t, "default", tgList.TargetGroups[0].TargetGroupName)

	runCLI(t, awsCLI("rds", "modify-db-proxy-target-group",
		"--db-proxy-name", proxyName,
		"--target-group-name", "default",
		"--connection-pool-config", "MaxConnectionsPercent=80,ConnectionBorrowTimeout=150"))

	out = runCLI(t, awsCLI("rds", "register-db-proxy-targets",
		"--db-proxy-name", proxyName,
		"--db-instance-identifiers", instID))
	var reg struct {
		DBProxyTargets []struct {
			Type string `json:"Type"`
		} `json:"DBProxyTargets"`
	}
	parseJSON(t, out, &reg)
	require.Len(t, reg.DBProxyTargets, 1)
	assert.Equal(t, "RDS_INSTANCE", reg.DBProxyTargets[0].Type)

	out = runCLI(t, awsCLI("rds", "describe-db-proxy-targets", "--db-proxy-name", proxyName))
	var tgts struct {
		Targets []struct {
			Type string `json:"Type"`
		} `json:"Targets"`
	}
	parseJSON(t, out, &tgts)
	require.Len(t, tgts.Targets, 1)

	runCLI(t, awsCLI("rds", "deregister-db-proxy-targets",
		"--db-proxy-name", proxyName,
		"--db-instance-identifiers", instID))

	// --- IAM roles ---
	runCLI(t, awsCLI("rds", "add-role-to-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--role-arn", "arn:aws:iam::123456789012:role/AuroraAccessRole",
		"--feature-name", "s3Import"))
	runCLI(t, awsCLI("rds", "remove-role-from-db-cluster",
		"--db-cluster-identifier", clusterID,
		"--role-arn", "arn:aws:iam::123456789012:role/AuroraAccessRole",
		"--feature-name", "s3Import"))
	runCLI(t, awsCLI("rds", "add-role-to-db-instance",
		"--db-instance-identifier", instID,
		"--role-arn", "arn:aws:iam::123456789012:role/InstanceAccessRole",
		"--feature-name", "S3_INTEGRATION"))
	runCLI(t, awsCLI("rds", "remove-role-from-db-instance",
		"--db-instance-identifier", instID,
		"--role-arn", "arn:aws:iam::123456789012:role/InstanceAccessRole",
		"--feature-name", "S3_INTEGRATION"))

	// --- DB security groups ---
	sgName := "cli-test-dbsg"
	runCLI(t, awsCLI("rds", "create-db-security-group",
		"--db-security-group-name", sgName,
		"--db-security-group-description", "cli test db security group"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-security-group", "--db-security-group-name", sgName).Run()
	})
	out = runCLI(t, awsCLI("rds", "authorize-db-security-group-ingress",
		"--db-security-group-name", sgName,
		"--cidrip", "10.0.0.0/24"))
	var authSG struct {
		DBSecurityGroup struct {
			IPRanges []struct {
				CIDRIP string `json:"CIDRIP"`
			} `json:"IPRanges"`
		} `json:"DBSecurityGroup"`
	}
	parseJSON(t, out, &authSG)
	require.Len(t, authSG.DBSecurityGroup.IPRanges, 1)
	assert.Equal(t, "10.0.0.0/24", authSG.DBSecurityGroup.IPRanges[0].CIDRIP)
	runCLI(t, awsCLI("rds", "revoke-db-security-group-ingress",
		"--db-security-group-name", sgName,
		"--cidrip", "10.0.0.0/24"))
	runCLI(t, awsCLI("rds", "describe-db-security-groups", "--db-security-group-name", sgName))

	// --- certificates ---
	out = runCLI(t, awsCLI("rds", "describe-certificates"))
	var certs struct {
		Certificates []struct {
			CertificateIdentifier string `json:"CertificateIdentifier"`
			CertificateType       string `json:"CertificateType"`
		} `json:"Certificates"`
	}
	parseJSON(t, out, &certs)
	require.NotEmpty(t, certs.Certificates)
	certID := certs.Certificates[0].CertificateIdentifier
	runCLI(t, awsCLI("rds", "modify-certificates", "--certificate-identifier", certID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "modify-certificates", "--remove-customer-override").Run()
	})

	// --- automated backups ---
	out = runCLI(t, awsCLI("rds", "describe-db-instance-automated-backups",
		"--db-instance-identifier", instID))
	var ab struct {
		DBInstanceAutomatedBackups []struct {
			DBInstanceAutomatedBackupsArn string `json:"DBInstanceAutomatedBackupsArn"`
		} `json:"DBInstanceAutomatedBackups"`
	}
	parseJSON(t, out, &ab)
	require.Len(t, ab.DBInstanceAutomatedBackups, 1)
	runCLI(t, awsCLI("rds", "delete-db-instance-automated-backup",
		"--db-instance-automated-backups-arn", ab.DBInstanceAutomatedBackups[0].DBInstanceAutomatedBackupsArn))

	out = runCLI(t, awsCLI("rds", "describe-db-cluster-automated-backups",
		"--db-cluster-identifier", clusterID))
	var cab struct {
		DBClusterAutomatedBackups []struct {
			DbClusterResourceId string `json:"DbClusterResourceId"`
		} `json:"DBClusterAutomatedBackups"`
	}
	parseJSON(t, out, &cab)
	require.Len(t, cab.DBClusterAutomatedBackups, 1)
	runCLI(t, awsCLI("rds", "delete-db-cluster-automated-backup",
		"--db-cluster-resource-id", cab.DBClusterAutomatedBackups[0].DbClusterResourceId))

	// --- log files ---
	out = runCLI(t, awsCLI("rds", "describe-db-log-files", "--db-instance-identifier", instID))
	var logs struct {
		DescribeDBLogFiles []struct {
			LogFileName string `json:"LogFileName"`
		} `json:"DescribeDBLogFiles"`
	}
	parseJSON(t, out, &logs)
	require.NotEmpty(t, logs.DescribeDBLogFiles)
	out = runCLI(t, awsCLI("rds", "download-db-log-file-portion",
		"--db-instance-identifier", instID,
		"--log-file-name", logs.DescribeDBLogFiles[0].LogFileName))
	var dl struct {
		LogFileData string `json:"LogFileData"`
	}
	parseJSON(t, out, &dl)
	assert.NotEmpty(t, dl.LogFileData)

	// --- copy groups ---
	srcPG := "cli-copy-src-pg"
	runCLI(t, awsCLI("rds", "create-db-parameter-group",
		"--db-parameter-group-name", srcPG,
		"--db-parameter-group-family", "postgres15",
		"--description", "source pg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-parameter-group", "--db-parameter-group-name", srcPG).Run()
	})
	dstPG := "cli-copy-dst-pg"
	runCLI(t, awsCLI("rds", "copy-db-parameter-group",
		"--source-db-parameter-group-identifier", srcPG,
		"--target-db-parameter-group-identifier", dstPG,
		"--target-db-parameter-group-description", "copied pg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-parameter-group", "--db-parameter-group-name", dstPG).Run()
	})

	srcCPG := "cli-copy-src-cpg"
	runCLI(t, awsCLI("rds", "create-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", srcCPG,
		"--db-parameter-group-family", "aurora-mysql8.0",
		"--description", "source cpg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-parameter-group", "--db-cluster-parameter-group-name", srcCPG).Run()
	})
	dstCPG := "cli-copy-dst-cpg"
	runCLI(t, awsCLI("rds", "copy-db-cluster-parameter-group",
		"--source-db-cluster-parameter-group-identifier", srcCPG,
		"--target-db-cluster-parameter-group-identifier", dstCPG,
		"--target-db-cluster-parameter-group-description", "copied cpg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-parameter-group", "--db-cluster-parameter-group-name", dstCPG).Run()
	})

	srcOG := "cli-copy-src-og"
	runCLI(t, awsCLI("rds", "create-option-group",
		"--option-group-name", srcOG,
		"--engine-name", "mysql",
		"--major-engine-version", "8.0",
		"--option-group-description", "source og"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-option-group", "--option-group-name", srcOG).Run()
	})
	dstOG := "cli-copy-dst-og"
	out = runCLI(t, awsCLI("rds", "copy-option-group",
		"--source-option-group-identifier", srcOG,
		"--target-option-group-identifier", dstOG,
		"--target-option-group-description", "copied og"))
	var cpOG struct {
		OptionGroup struct {
			OptionGroupName string `json:"OptionGroupName"`
			EngineName      string `json:"EngineName"`
		} `json:"OptionGroup"`
	}
	parseJSON(t, out, &cpOG)
	assert.Equal(t, dstOG, cpOG.OptionGroup.OptionGroupName)
	assert.Equal(t, "mysql", cpOG.OptionGroup.EngineName)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-option-group", "--option-group-name", dstOG).Run()
	})

	// --- event subscription source identifiers ---
	subName := "cli-src-id-sub"
	runCLI(t, awsCLI("rds", "create-event-subscription",
		"--subscription-name", subName,
		"--sns-topic-arn", "arn:aws:sns:us-east-1:123456789012:rds-events",
		"--source-type", "db-instance"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-event-subscription", "--subscription-name", subName).Run()
	})
	out = runCLI(t, awsCLI("rds", "add-source-identifier-to-subscription",
		"--subscription-name", subName,
		"--source-identifier", "my-db-instance"))
	var addSub struct {
		EventSubscription struct {
			SourceIdsList []string `json:"SourceIdsList"`
		} `json:"EventSubscription"`
	}
	parseJSON(t, out, &addSub)
	assert.Contains(t, addSub.EventSubscription.SourceIdsList, "my-db-instance")
	runCLI(t, awsCLI("rds", "remove-source-identifier-from-subscription",
		"--subscription-name", subName,
		"--source-identifier", "my-db-instance"))

	// --- pending maintenance actions ---
	out = runCLI(t, awsCLI("rds", "describe-pending-maintenance-actions"))
	var pma struct {
		PendingMaintenanceActions []struct {
			ResourceIdentifier              string `json:"ResourceIdentifier"`
			PendingMaintenanceActionDetails []struct {
				Action string `json:"Action"`
			} `json:"PendingMaintenanceActionDetails"`
		} `json:"PendingMaintenanceActions"`
	}
	parseJSON(t, out, &pma)
	require.NotEmpty(t, pma.PendingMaintenanceActions)
	var resArn, action string
	for _, p := range pma.PendingMaintenanceActions {
		if len(p.PendingMaintenanceActionDetails) > 0 {
			resArn = p.ResourceIdentifier
			action = p.PendingMaintenanceActionDetails[0].Action
			break
		}
	}
	require.NotEmpty(t, resArn)
	require.NotEmpty(t, action)
	runCLI(t, awsCLI("rds", "apply-pending-maintenance-action",
		"--resource-identifier", resArn,
		"--apply-action", action,
		"--opt-in-type", "immediate"))
}
