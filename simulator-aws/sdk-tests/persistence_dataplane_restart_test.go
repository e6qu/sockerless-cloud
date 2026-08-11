package aws_sdk_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetworkLoadBalancerDataPlaneSurvivesSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "process")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	ec2API := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	ecsAPI := ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	elbAPI := elbv2.NewFromConfig(cfg, func(o *elbv2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	testCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()
	go servePersistentTCPEcho(backend)
	backendHost, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	require.NoError(t, err)
	backendPort, err := strconv.Atoi(backendPortText)
	require.NoError(t, err)

	vpc, err := ec2API.CreateVpc(testCtx, &ec2.CreateVpcInput{CidrBlock: aws.String("10.191.0.0/16")})
	require.NoError(t, err)
	subnet, err := ec2API.CreateSubnet(testCtx, &ec2.CreateSubnetInput{
		VpcId: vpc.Vpc.VpcId, CidrBlock: aws.String("10.191.1.0/24"),
		AvailabilityZone: aws.String("us-east-1a"),
	})
	require.NoError(t, err)
	firstNetworkInterface, err := ec2API.CreateNetworkInterface(testCtx, &ec2.CreateNetworkInterfaceInput{
		SubnetId: subnet.Subnet.SubnetId,
	})
	require.NoError(t, err)
	firstPrivateAddress := aws.ToString(firstNetworkInterface.NetworkInterface.PrivateIpAddress)
	firstTaskDefinition, err := ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("persistent-task-family"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("application"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, firstTaskDefinition.TaskDefinition.Revision)
	firstDaemonTaskDefinition, err := ecsAPI.RegisterDaemonTaskDefinition(testCtx, &ecs.RegisterDaemonTaskDefinitionInput{
		Family: aws.String("persistent-daemon-family"),
		ContainerDefinitions: []ecstypes.DaemonContainerDefinition{{
			Name: aws.String("agent"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
		}},
	})
	require.NoError(t, err)
	require.Contains(t, aws.ToString(firstDaemonTaskDefinition.DaemonTaskDefinitionArn), "persistent-daemon-family:1")
	loadBalancer, err := elbAPI.CreateLoadBalancer(testCtx, &elbv2.CreateLoadBalancerInput{
		Name:    aws.String("persistent-nlb"),
		Type:    elbv2types.LoadBalancerTypeEnumNetwork,
		Subnets: []string{aws.ToString(subnet.Subnet.SubnetId)},
	})
	require.NoError(t, err)
	loadBalancerARN := aws.ToString(loadBalancer.LoadBalancers[0].LoadBalancerArn)
	targetGroup, err := elbAPI.CreateTargetGroup(testCtx, &elbv2.CreateTargetGroupInput{
		Name:       aws.String("persistent-nlb-targets"),
		Protocol:   elbv2types.ProtocolEnumTcp,
		Port:       aws.Int32(int32(backendPort)),
		VpcId:      vpc.Vpc.VpcId,
		TargetType: elbv2types.TargetTypeEnumIp,
	})
	require.NoError(t, err)
	targetGroupARN := aws.ToString(targetGroup.TargetGroups[0].TargetGroupArn)
	_, err = elbAPI.RegisterTargets(testCtx, &elbv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(targetGroupARN),
		Targets: []elbv2types.TargetDescription{{
			Id: aws.String(backendHost), Port: aws.Int32(int32(backendPort)),
		}},
	})
	require.NoError(t, err)

	listenerProbe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listenerPort := listenerProbe.Addr().(*net.TCPAddr).Port
	require.NoError(t, listenerProbe.Close())
	_, err = elbAPI.CreateListener(testCtx, &elbv2.CreateListenerInput{
		LoadBalancerArn: aws.String(loadBalancerARN),
		Protocol:        elbv2types.ProtocolEnumTcp,
		Port:            aws.Int32(int32(listenerPort)),
		DefaultActions: []elbv2types.Action{{
			Type: elbv2types.ActionTypeEnumForward, TargetGroupArn: aws.String(targetGroupARN),
		}},
	})
	require.NoError(t, err)
	assertPersistentTCPEcho(t, listenerPort, "before-restart")

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "process")
	ec2API = ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	ecsAPI = ecs.NewFromConfig(cfg, func(o *ecs.Options) { o.BaseEndpoint = aws.String(endpoint) })
	elbAPI = elbv2.NewFromConfig(cfg, func(o *elbv2.Options) { o.BaseEndpoint = aws.String(endpoint) })
	described, err := elbAPI.DescribeLoadBalancers(testCtx, &elbv2.DescribeLoadBalancersInput{
		LoadBalancerArns: []string{loadBalancerARN},
	})
	require.NoError(t, err)
	require.Len(t, described.LoadBalancers, 1)
	assertPersistentTCPEcho(t, listenerPort, "after-restart")
	secondNetworkInterface, err := ec2API.CreateNetworkInterface(testCtx, &ec2.CreateNetworkInterfaceInput{
		SubnetId: subnet.Subnet.SubnetId,
	})
	require.NoError(t, err)
	assert.NotEqual(
		t,
		firstPrivateAddress,
		aws.ToString(secondNetworkInterface.NetworkInterface.PrivateIpAddress),
		"Amazon EC2 address allocation must not restart from the beginning of the subnet",
	)
	secondTaskDefinition, err := ecsAPI.RegisterTaskDefinition(testCtx, &ecs.RegisterTaskDefinitionInput{
		Family: aws.String("persistent-task-family"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("application"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
			Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2, secondTaskDefinition.TaskDefinition.Revision)
	secondDaemonTaskDefinition, err := ecsAPI.RegisterDaemonTaskDefinition(testCtx, &ecs.RegisterDaemonTaskDefinitionInput{
		Family: aws.String("persistent-daemon-family"),
		ContainerDefinitions: []ecstypes.DaemonContainerDefinition{{
			Name: aws.String("agent"), Image: aws.String("public.ecr.aws/docker/library/busybox:latest"),
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(secondDaemonTaskDefinition.DaemonTaskDefinitionArn), "persistent-daemon-family:2")
}

func servePersistentTCPEcho(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			line, err := bufio.NewReader(connection).ReadString('\n')
			if err == nil {
				_, _ = connection.Write([]byte("echo:" + line))
			}
		}()
	}
}

func assertPersistentTCPEcho(t *testing.T, port int, message string) {
	t.Helper()
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 5*time.Second)
	require.NoError(t, err)
	defer connection.Close()
	require.NoError(t, connection.SetDeadline(time.Now().Add(5*time.Second)))
	_, err = connection.Write([]byte(message + "\n"))
	require.NoError(t, err)
	line, err := bufio.NewReader(connection).ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "echo:"+message+"\n", line)
}

func TestAmazonRDSDataAndEndpointSurviveSimulatorRestart_SDK(t *testing.T) {
	stateDir := t.TempDir()
	tcpPort, udpPort := persistentSimulatorPorts(t)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", tcpPort)
	cmd := startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	t.Cleanup(func() { shutdownSimulator(cmd) })

	cfg := persistentSDKConfig()
	rdsAPI := rds.NewFromConfig(cfg, func(o *rds.Options) { o.BaseEndpoint = aws.String(endpoint) })
	testCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	instanceID := fmt.Sprintf("persistent-rds-%d", tcpPort)
	const (
		username = "dbadmin"
		password = "PersistentPassword-123!"
		database = "application"
	)
	created, err := rdsAPI.CreateDBInstance(testCtx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceID),
		DBInstanceClass:      aws.String("db.t3.micro"),
		Engine:               aws.String("postgres"),
		AllocatedStorage:     aws.Int32(20),
		MasterUsername:       aws.String(username),
		MasterUserPassword:   aws.String(password),
		DBName:               aws.String(database),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = rdsAPI.DeleteDBInstance(context.Background(), &rds.DeleteDBInstanceInput{
			DBInstanceIdentifier: aws.String(instanceID),
			SkipFinalSnapshot:    aws.Bool(true),
		})
	})
	originalEndpoint := fmt.Sprintf("%s:%d",
		aws.ToString(created.DBInstance.Endpoint.Address),
		aws.ToInt32(created.DBInstance.Endpoint.Port),
	)
	connection := connectPersistentPostgreSQL(t, testCtx, originalEndpoint, username, password, database)
	_, err = connection.Exec(testCtx, `CREATE TABLE persistence (id integer PRIMARY KEY, value text NOT NULL)`)
	require.NoError(t, err)
	_, err = connection.Exec(testCtx, `INSERT INTO persistence (id, value) VALUES (1, 'survived-restart')`)
	require.NoError(t, err)
	require.NoError(t, connection.Close(testCtx))

	shutdownSimulator(cmd)
	cmd = startPersistentSimulator(t, stateDir, tcpPort, udpPort, "docker")
	rdsAPI = rds.NewFromConfig(cfg, func(o *rds.Options) { o.BaseEndpoint = aws.String(endpoint) })
	described, err := rdsAPI.DescribeDBInstances(testCtx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(instanceID),
	})
	require.NoError(t, err)
	require.Len(t, described.DBInstances, 1)
	restoredEndpoint := fmt.Sprintf("%s:%d",
		aws.ToString(described.DBInstances[0].Endpoint.Address),
		aws.ToInt32(described.DBInstances[0].Endpoint.Port),
	)
	assert.Equal(t, originalEndpoint, restoredEndpoint, "the Amazon RDS endpoint coordinate must remain stable")
	connection = connectPersistentPostgreSQL(t, testCtx, restoredEndpoint, username, password, database)
	defer connection.Close(context.Background())
	var value string
	require.NoError(t, connection.QueryRow(testCtx, `SELECT value FROM persistence WHERE id = 1`).Scan(&value))
	assert.Equal(t, "survived-restart", value)
}

func connectPersistentPostgreSQL(
	t *testing.T,
	testCtx context.Context,
	endpoint, username, password, database string,
) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf(
		"postgres://%s:%s@%s/%s?sslmode=require", username, password, endpoint, database,
	))
	require.NoError(t, err)

	// A database that has just come back is still replaying its write-ahead log
	// and answers "the database system is starting up" (SQLSTATE 57P03) until it
	// finishes. That is the server reporting a transient state, not a failure,
	// and it is exactly what a client — or pg_isready — waits through. Connecting
	// once and giving up turns a restart into a flake.
	deadline := time.Now().Add(30 * time.Second)
	var connection *pgx.Conn
	var lastErr error
	for time.Now().Before(deadline) {
		connection, lastErr = pgx.ConnectConfig(testCtx, config)
		if lastErr == nil {
			return connection
		}
		if !postgreSQLIsStartingUp(lastErr) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.NoError(t, lastErr, "PostgreSQL at %s never became ready", endpoint)
	return connection
}

// postgreSQLIsStartingUp reports whether an error is the server saying it is not
// accepting connections yet, either because it is recovering (57P03) or because
// its listener is not up at all.
func postgreSQLIsStartingUp(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03"
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}
