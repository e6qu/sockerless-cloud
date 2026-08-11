package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRDSCLI_StateAndGlobalCluster covers instance/cluster state
// transitions (start/stop/failover, promote-read-replica) and the
// Aurora global cluster family end-to-end through the aws CLI. Grouped
// into one round-trip func (appdata2 carries RDS).
func TestRDSCLI_StateAndGlobalCluster(t *testing.T) {
	// --- DB instance start/stop ---
	instID := "cli-state-pg-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID,
			"--skip-final-snapshot").Run()
	})

	out := runCLI(t, awsCLI("rds", "stop-db-instance", "--db-instance-identifier", instID))
	var stopResp struct {
		DBInstance struct {
			DBInstanceStatus string `json:"DBInstanceStatus"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &stopResp)
	assert.Equal(t, "stopped", stopResp.DBInstance.DBInstanceStatus)

	out = runCLI(t, awsCLI("rds", "start-db-instance", "--db-instance-identifier", instID))
	parseJSON(t, out, &stopResp)
	assert.Equal(t, "available", stopResp.DBInstance.DBInstanceStatus)

	// --- promote-read-replica ---
	replicaID := "cli-state-replica"
	runCLI(t, awsCLI("rds", "create-db-instance-read-replica",
		"--db-instance-identifier", replicaID,
		"--source-db-instance-identifier", instID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", replicaID,
			"--skip-final-snapshot").Run()
	})
	out = runCLI(t, awsCLI("rds", "promote-read-replica", "--db-instance-identifier", replicaID))
	var promResp struct {
		DBInstance struct {
			DBInstanceIdentifier string `json:"DBInstanceIdentifier"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &promResp)
	assert.Equal(t, replicaID, promResp.DBInstance.DBInstanceIdentifier)

	// --- DB cluster start/stop/failover ---
	clusterID := "cli-state-aurora"
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

	out = runCLI(t, awsCLI("rds", "stop-db-cluster", "--db-cluster-identifier", clusterID))
	var clResp struct {
		DBCluster struct {
			Status string `json:"Status"`
		} `json:"DBCluster"`
	}
	parseJSON(t, out, &clResp)
	assert.Equal(t, "stopped", clResp.DBCluster.Status)

	out = runCLI(t, awsCLI("rds", "start-db-cluster", "--db-cluster-identifier", clusterID))
	parseJSON(t, out, &clResp)
	assert.Equal(t, "available", clResp.DBCluster.Status)

	runCLI(t, awsCLI("rds", "failover-db-cluster", "--db-cluster-identifier", clusterID))

	// --- global cluster ---
	gid := "cli-global-cluster"
	out = runCLI(t, awsCLI("rds", "create-global-cluster",
		"--global-cluster-identifier", gid,
		"--engine", "aurora-mysql",
		"--database-name", "appdb",
		"--storage-encrypted"))
	var gcCreate struct {
		GlobalCluster struct {
			GlobalClusterIdentifier string `json:"GlobalClusterIdentifier"`
			Status                  string `json:"Status"`
			Engine                  string `json:"Engine"`
			GlobalClusterArn        string `json:"GlobalClusterArn"`
		} `json:"GlobalCluster"`
	}
	parseJSON(t, out, &gcCreate)
	require.Equal(t, gid, gcCreate.GlobalCluster.GlobalClusterIdentifier)
	assert.Equal(t, "available", gcCreate.GlobalCluster.Status)
	require.NotEmpty(t, gcCreate.GlobalCluster.GlobalClusterArn)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-global-cluster",
			"--global-cluster-identifier", gid).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-global-clusters",
		"--global-cluster-identifier", gid))
	var gcDesc struct {
		GlobalClusters []struct {
			DatabaseName string `json:"DatabaseName"`
		} `json:"GlobalClusters"`
	}
	parseJSON(t, out, &gcDesc)
	require.Len(t, gcDesc.GlobalClusters, 1)
	assert.Equal(t, "appdb", gcDesc.GlobalClusters[0].DatabaseName)

	runCLI(t, awsCLI("rds", "modify-global-cluster",
		"--global-cluster-identifier", gid,
		"--deletion-protection"))
	runCLI(t, awsCLI("rds", "delete-global-cluster",
		"--global-cluster-identifier", gid))
}

// TestRDSCLI_EventSubParamDetailEndpoint covers event subscriptions,
// parameter detail (describe/modify/reset), snapshot attribute sharing,
// DB cluster endpoints, and copy-db-cluster-snapshot end-to-end through
// the aws CLI. Grouped into one round-trip func (appdata2 carries RDS).
func TestRDSCLI_EventSubParamDetailEndpoint(t *testing.T) {
	// --- event subscription ---
	subName := "cli-rds-events"
	out := runCLI(t, awsCLI("rds", "create-event-subscription",
		"--subscription-name", subName,
		"--sns-topic-arn", "arn:aws:sns:us-east-1:123456789012:rds-events",
		"--source-type", "db-instance",
		"--event-categories", "availability", "failure"))
	var subCreate struct {
		EventSubscription struct {
			CustSubscriptionId  string   `json:"CustSubscriptionId"`
			Status              string   `json:"Status"`
			SnsTopicArn         string   `json:"SnsTopicArn"`
			EventCategoriesList []string `json:"EventCategoriesList"`
		} `json:"EventSubscription"`
	}
	parseJSON(t, out, &subCreate)
	require.Equal(t, subName, subCreate.EventSubscription.CustSubscriptionId)
	assert.Equal(t, "active", subCreate.EventSubscription.Status)
	assert.ElementsMatch(t, []string{"availability", "failure"}, subCreate.EventSubscription.EventCategoriesList)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-event-subscription",
			"--subscription-name", subName).Run()
	})

	runCLI(t, awsCLI("rds", "describe-event-subscriptions", "--subscription-name", subName))
	runCLI(t, awsCLI("rds", "modify-event-subscription",
		"--subscription-name", subName, "--no-enabled"))
	runCLI(t, awsCLI("rds", "delete-event-subscription", "--subscription-name", subName))

	// --- parameter detail ---
	pgName := "cli-detail-pg"
	runCLI(t, awsCLI("rds", "create-db-parameter-group",
		"--db-parameter-group-name", pgName,
		"--db-parameter-group-family", "postgres15",
		"--description", "cli detail pg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-parameter-group",
			"--db-parameter-group-name", pgName).Run()
	})

	out = runCLI(t, awsCLI("rds", "describe-db-parameters",
		"--db-parameter-group-name", pgName))
	var paramsResp struct {
		Parameters []struct {
			ParameterName  string `json:"ParameterName"`
			ParameterValue string `json:"ParameterValue"`
		} `json:"Parameters"`
	}
	parseJSON(t, out, &paramsResp)
	require.NotEmpty(t, paramsResp.Parameters)

	runCLI(t, awsCLI("rds", "modify-db-parameter-group",
		"--db-parameter-group-name", pgName,
		"--parameters", "ParameterName=max_connections,ParameterValue=200,ApplyMethod=pending-reboot"))
	out = runCLI(t, awsCLI("rds", "describe-db-parameters",
		"--db-parameter-group-name", pgName))
	parseJSON(t, out, &paramsResp)
	var found bool
	for _, p := range paramsResp.Parameters {
		if p.ParameterName == "max_connections" {
			assert.Equal(t, "200", p.ParameterValue)
			found = true
		}
	}
	assert.True(t, found, "expected modified max_connections")
	runCLI(t, awsCLI("rds", "reset-db-parameter-group",
		"--db-parameter-group-name", pgName, "--reset-all-parameters"))

	cpgName := "cli-detail-cluster-pg"
	runCLI(t, awsCLI("rds", "create-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", cpgName,
		"--db-parameter-group-family", "aurora-mysql8.0",
		"--description", "cli detail cluster pg"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-parameter-group",
			"--db-cluster-parameter-group-name", cpgName).Run()
	})
	runCLI(t, awsCLI("rds", "describe-db-cluster-parameters",
		"--db-cluster-parameter-group-name", cpgName))
	runCLI(t, awsCLI("rds", "modify-db-cluster-parameter-group",
		"--db-cluster-parameter-group-name", cpgName,
		"--parameters", "ParameterName=character_set_server,ParameterValue=latin1,ApplyMethod=immediate"))

	// --- snapshot attribute sharing ---
	instID := "cli-attr-db"
	runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--db-instance-class", "db.t3.micro",
		"--engine", "postgres",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--allocated-storage", "20"))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance",
			"--db-instance-identifier", instID,
			"--skip-final-snapshot").Run()
	})
	snapID := "cli-attr-snap"
	runCLI(t, awsCLI("rds", "create-db-snapshot",
		"--db-snapshot-identifier", snapID,
		"--db-instance-identifier", instID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-snapshot",
			"--db-snapshot-identifier", snapID).Run()
	})
	runCLI(t, awsCLI("rds", "modify-db-snapshot-attribute",
		"--db-snapshot-identifier", snapID,
		"--attribute-name", "restore",
		"--values-to-add", "123456789012", "210987654321"))
	out = runCLI(t, awsCLI("rds", "describe-db-snapshot-attributes",
		"--db-snapshot-identifier", snapID))
	var attrResp struct {
		DBSnapshotAttributesResult struct {
			DBSnapshotAttributes []struct {
				AttributeName   string   `json:"AttributeName"`
				AttributeValues []string `json:"AttributeValues"`
			} `json:"DBSnapshotAttributes"`
		} `json:"DBSnapshotAttributesResult"`
	}
	parseJSON(t, out, &attrResp)
	require.Len(t, attrResp.DBSnapshotAttributesResult.DBSnapshotAttributes, 1)
	assert.Equal(t, "restore", attrResp.DBSnapshotAttributesResult.DBSnapshotAttributes[0].AttributeName)
	assert.ElementsMatch(t, []string{"123456789012", "210987654321"},
		attrResp.DBSnapshotAttributesResult.DBSnapshotAttributes[0].AttributeValues)

	// --- cluster endpoint + copy-db-cluster-snapshot ---
	clusterID := "cli-endpoint-cluster"
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

	epID := "cli-custom-reader"
	out = runCLI(t, awsCLI("rds", "create-db-cluster-endpoint",
		"--db-cluster-endpoint-identifier", epID,
		"--db-cluster-identifier", clusterID,
		"--endpoint-type", "READER",
		"--static-members", "member-1", "member-2"))
	var epCreate struct {
		DBClusterEndpointIdentifier string   `json:"DBClusterEndpointIdentifier"`
		Status                      string   `json:"Status"`
		CustomEndpointType          string   `json:"CustomEndpointType"`
		StaticMembers               []string `json:"StaticMembers"`
	}
	parseJSON(t, out, &epCreate)
	require.Equal(t, epID, epCreate.DBClusterEndpointIdentifier)
	assert.Equal(t, "available", epCreate.Status)
	assert.Equal(t, "READER", epCreate.CustomEndpointType)
	assert.ElementsMatch(t, []string{"member-1", "member-2"}, epCreate.StaticMembers)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-endpoint",
			"--db-cluster-endpoint-identifier", epID).Run()
	})
	runCLI(t, awsCLI("rds", "describe-db-cluster-endpoints",
		"--db-cluster-endpoint-identifier", epID))
	runCLI(t, awsCLI("rds", "delete-db-cluster-endpoint",
		"--db-cluster-endpoint-identifier", epID))

	srcSnap := "cli-src-cluster-snap"
	runCLI(t, awsCLI("rds", "create-db-cluster-snapshot",
		"--db-cluster-snapshot-identifier", srcSnap,
		"--db-cluster-identifier", clusterID))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-snapshot",
			"--db-cluster-snapshot-identifier", srcSnap).Run()
	})
	copySnap := "cli-copy-cluster-snap"
	out = runCLI(t, awsCLI("rds", "copy-db-cluster-snapshot",
		"--source-db-cluster-snapshot-identifier", srcSnap,
		"--target-db-cluster-snapshot-identifier", copySnap))
	var copyResp struct {
		DBClusterSnapshot struct {
			DBClusterSnapshotIdentifier string `json:"DBClusterSnapshotIdentifier"`
			Status                      string `json:"Status"`
		} `json:"DBClusterSnapshot"`
	}
	parseJSON(t, out, &copyResp)
	assert.Equal(t, copySnap, copyResp.DBClusterSnapshot.DBClusterSnapshotIdentifier)
	assert.Equal(t, "available", copyResp.DBClusterSnapshot.Status)
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-cluster-snapshot",
			"--db-cluster-snapshot-identifier", copySnap).Run()
	})
}
