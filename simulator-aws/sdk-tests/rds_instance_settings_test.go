package aws_sdk_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/smithy-go"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rdsPresentedCertificate is the server certificate a PostgreSQL client sees
// when it connects to endpoint over TLS.
func rdsPresentedCertificate(t *testing.T, ctx context.Context, endpoint string) *x509.Certificate {
	t.Helper()
	config, err := pgx.ParseConfig(fmt.Sprintf("postgres://%s@%s/%s?sslmode=require", restoreSourceUsername, endpoint, restoreSourceDatabase))
	require.NoError(t, err)
	config.Password = restoreSourcePassword
	var presented *x509.Certificate
	config.TLSConfig = &tls.Config{
		InsecureSkipVerify: true, // test-only CA coordinate: the test reads the chain instead
		MinVersion:         tls.VersionTLS12,
		VerifyConnection: func(state tls.ConnectionState) error {
			presented = state.PeerCertificates[0]
			return nil
		},
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	require.NoError(t, err)
	require.NoError(t, conn.Close(ctx))
	require.NotNil(t, presented)
	return presented
}

func rdsOptionGroupStatuses(instance types.DBInstance) map[string]string {
	statuses := map[string]string{}
	for _, membership := range instance.OptionGroupMemberships {
		statuses[aws.ToString(membership.OptionGroupName)] = aws.ToString(membership.Status)
	}
	return statuses
}

// ModifyDBInstance holds MultiAZ, the storage members, the DB subnet group,
// the option group and the CA certificate for the maintenance window,
// reporting them under PendingModifiedValues and the option group memberships
// meanwhile, and ApplyImmediately applies them: the endpoint then presents a
// server certificate issued by the new CA. The storage members are checked
// against the rules the reference states, and a group or certificate that
// does not exist is refused with its own error.
func TestRDS_ModifyDBInstanceStorageGroupsAndCertificate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := rdsClient()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "postgres"}
	subnetGroup := uniqueName("sdk-settings-subnets")
	_, err := c.CreateDBSubnetGroup(ctx, &rds.CreateDBSubnetGroupInput{
		DBSubnetGroupName:        aws.String(subnetGroup),
		DBSubnetGroupDescription: aws.String("instance settings"),
		SubnetIds:                []string{"subnet-aaaa1111", "subnet-bbbb2222"},
	})
	require.NoError(t, err)
	optionGroup := uniqueName("sdk-settings-options")
	_, err = c.CreateOptionGroup(ctx, &rds.CreateOptionGroupInput{
		OptionGroupName: aws.String(optionGroup), EngineName: aws.String("postgres"),
		MajorEngineVersion: aws.String("16"), OptionGroupDescription: aws.String("instance settings"),
	})
	require.NoError(t, err)
	mysqlOptions := uniqueName("sdk-settings-mysql-options")
	_, err = c.CreateOptionGroup(ctx, &rds.CreateOptionGroupInput{
		OptionGroupName: aws.String(mysqlOptions), EngineName: aws.String("mysql"),
		MajorEngineVersion: aws.String("8.0"), OptionGroupDescription: aws.String("another engine"),
	})
	require.NoError(t, err)

	id := uniqueName("sdk-settings")
	created, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
		DBInstanceIdentifier:  aws.String(id),
		DBInstanceClass:       aws.String("db.t3.micro"),
		Engine:                aws.String("postgres"),
		EngineVersion:         aws.String("16.15"),
		AllocatedStorage:      aws.Int32(100),
		MasterUsername:        aws.String(restoreSourceUsername),
		MasterUserPassword:    aws.String(restoreSourcePassword),
		DBName:                aws.String(restoreSourceDatabase),
		BackupRetentionPeriod: aws.Int32(0),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = c.DeleteDBInstance(ctx, &rds.DeleteDBInstanceInput{DBInstanceIdentifier: aws.String(id), SkipFinalSnapshot: aws.Bool(true)})
		_ = rds.NewDBInstanceDeletedWaiter(c, func(o *rds.DBInstanceDeletedWaiterOptions) {
			o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
		}).Wait(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)}, 5*time.Minute)
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{OptionGroupName: aws.String(optionGroup)})
		_, _ = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{OptionGroupName: aws.String(mysqlOptions)})
		_, _ = c.DeleteDBSubnetGroup(ctx, &rds.DeleteDBSubnetGroupInput{DBSubnetGroupName: aws.String(subnetGroup)})
	})
	certificates, err := c.DescribeCertificates(ctx, &rds.DescribeCertificatesInput{})
	require.NoError(t, err)
	assert.Equal(t, "gp3", aws.ToString(created.DBInstance.StorageType), "Amazon RDS gives an instance without a storage type gp3")
	assert.False(t, aws.ToBool(created.DBInstance.MultiAZ))
	assert.Equal(t, map[string]string{"default:postgres-16": "in-sync"}, rdsOptionGroupStatuses(*created.DBInstance))
	assert.Equal(t, aws.ToString(certificates.DefaultCertificateForNewLaunches), aws.ToString(created.DBInstance.CACertificateIdentifier))
	f.waitAvailable(id)

	refused := []struct {
		input *rds.ModifyDBInstanceInput
		code  string
	}{
		{&rds.ModifyDBInstanceInput{StorageType: aws.String("io1")}, "InvalidParameterCombination"},
		{&rds.ModifyDBInstanceInput{StorageType: aws.String("magnetic")}, "StorageTypeNotSupported"},
		{&rds.ModifyDBInstanceInput{StorageType: aws.String("io1"), Iops: aws.Int32(1000), AllocatedStorage: aws.Int32(100), StorageThroughput: aws.Int32(500)}, "InvalidParameterCombination"},
		{&rds.ModifyDBInstanceInput{StorageType: aws.String("io1"), Iops: aws.Int32(10000), AllocatedStorage: aws.Int32(100)}, "InvalidParameterCombination"},
		{&rds.ModifyDBInstanceInput{StorageType: aws.String("io1"), Iops: aws.Int32(1000)}, "InvalidParameterCombination"},
		{&rds.ModifyDBInstanceInput{DBSubnetGroupName: aws.String("sdk-settings-no-such-group")}, "DBSubnetGroupNotFoundFault"},
		{&rds.ModifyDBInstanceInput{OptionGroupName: aws.String("sdk-settings-no-such-group")}, "OptionGroupNotFoundFault"},
		{&rds.ModifyDBInstanceInput{OptionGroupName: aws.String(mysqlOptions)}, "InvalidParameterCombination"},
		{&rds.ModifyDBInstanceInput{CACertificateIdentifier: aws.String("rds-ca-no-such-ca")}, "CertificateNotFound"},
	}
	for _, refusal := range refused {
		refusal.input.DBInstanceIdentifier = aws.String(id)
		_, err := c.ModifyDBInstance(ctx, refusal.input)
		assertAWSAPIErrorCode(t, err, refusal.code)
	}

	held, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier:    aws.String(id),
		MultiAZ:                 aws.Bool(true),
		StorageType:             aws.String("io1"),
		Iops:                    aws.Int32(1000),
		AllocatedStorage:        aws.Int32(100),
		DBSubnetGroupName:       aws.String(subnetGroup),
		OptionGroupName:         aws.String(optionGroup),
		CACertificateIdentifier: aws.String("rds-ca-ecc384-g1"),
	})
	require.NoError(t, err)
	instance := *held.DBInstance
	pending := instance.PendingModifiedValues
	assert.False(t, aws.ToBool(instance.MultiAZ))
	assert.Equal(t, "gp3", aws.ToString(instance.StorageType))
	assert.Nil(t, instance.DBSubnetGroup)
	assert.True(t, aws.ToBool(pending.MultiAZ))
	assert.Equal(t, "io1", aws.ToString(pending.StorageType))
	assert.Equal(t, int32(1000), aws.ToInt32(pending.Iops))
	assert.Equal(t, subnetGroup, aws.ToString(pending.DBSubnetGroupName))
	assert.Equal(t, "rds-ca-ecc384-g1", aws.ToString(pending.CACertificateIdentifier))
	assert.Equal(t, map[string]string{"default:postgres-16": "pending-maintenance-removal", optionGroup: "pending-maintenance-apply"},
		rdsOptionGroupStatuses(instance))
	_, err = c.DeleteOptionGroup(ctx, &rds.DeleteOptionGroupInput{OptionGroupName: aws.String(optionGroup)})
	assertAWSAPIErrorCode(t, err, "InvalidOptionGroupStateFault")

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), ApplyImmediately: aws.Bool(true)})
	require.NoError(t, err)
	instance = f.waitAvailable(id)
	assert.True(t, aws.ToBool(instance.MultiAZ))
	assert.Equal(t, "io1", aws.ToString(instance.StorageType))
	assert.Equal(t, int32(1000), aws.ToInt32(instance.Iops))
	require.NotNil(t, instance.DBSubnetGroup)
	assert.Equal(t, subnetGroup, aws.ToString(instance.DBSubnetGroup.DBSubnetGroupName))
	assert.Equal(t, map[string]string{optionGroup: "in-sync"}, rdsOptionGroupStatuses(instance))
	assert.Equal(t, "rds-ca-ecc384-g1", aws.ToString(instance.CACertificateIdentifier))
	require.NotNil(t, instance.CertificateDetails)
	assert.Equal(t, "rds-ca-ecc384-g1", aws.ToString(instance.CertificateDetails.CAIdentifier))
	assert.Nil(t, instance.PendingModifiedValues.StorageType)
	assert.Nil(t, instance.PendingModifiedValues.CACertificateIdentifier)

	endpoint := net.JoinHostPort(aws.ToString(instance.Endpoint.Address), fmt.Sprint(aws.ToInt32(instance.Endpoint.Port)))
	presented := rdsPresentedCertificate(t, ctx, endpoint)
	assert.Equal(t, "rds-ca-ecc384-g1", presented.Issuer.CommonName, "the endpoint presents a certificate the new CA issued")
	assert.IsType(t, &ecdsa.PublicKey{}, presented.PublicKey)
	assert.Equal(t, presented.NotAfter.UTC(), aws.ToTime(instance.CertificateDetails.ValidTill).UTC())

	grown, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), Iops: aws.Int32(1050), AllocatedStorage: aws.Int32(100), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1100), aws.ToInt32(grown.DBInstance.Iops), "an increase below a tenth rounds up to a tenth")

	gp3, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), StorageType: aws.String("gp3"), Iops: aws.Int32(3000),
		StorageThroughput: aws.Int32(500), AllocatedStorage: aws.Int32(100), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "gp3", aws.ToString(gp3.DBInstance.StorageType))
	assert.Equal(t, int32(500), aws.ToInt32(gp3.DBInstance.StorageThroughput))

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), StorageType: aws.String("gp2"), ApplyImmediately: aws.Bool(true),
	})
	assertAWSAPIErrorCode(t, err, "InvalidParameterCombination")
	gp2, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{
		DBInstanceIdentifier: aws.String(id), StorageType: aws.String("gp2"), Iops: aws.Int32(0), ApplyImmediately: aws.Bool(true),
	})
	require.NoError(t, err)
	assert.Equal(t, "gp2", aws.ToString(gp2.DBInstance.StorageType))
	assert.Nil(t, gp2.DBInstance.Iops)
	assert.Nil(t, gp2.DBInstance.StorageThroughput, "leaving gp3 drops the storage throughput")
}

// rdsAwaitRenamed waits until the instance answers to newID and is available.
func rdsAwaitRenamed(t *testing.T, ctx context.Context, c *rds.Client, newID string) types.DBInstance {
	t.Helper()
	out, err := rds.NewDBInstanceAvailableWaiter(c, func(o *rds.DBInstanceAvailableWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
		o.Retryable = func(_ context.Context, _ *rds.DescribeDBInstancesInput, out *rds.DescribeDBInstancesOutput, err error) (bool, error) {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) && apiErr.ErrorCode() == "DBInstanceNotFound" {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return aws.ToString(out.DBInstances[0].DBInstanceStatus) != "available", nil
		}
	}).WaitForOutput(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(newID)}, 5*time.Minute)
	require.NoError(t, err, "DB instance %s must become available", newID)
	return out.DBInstances[0]
}

// DBPortNumber restarts the engine on the new port whatever ApplyImmediately
// says, and NewDBInstanceIdentifier renames the instance at the maintenance
// window or, with ApplyImmediately, at once: the ARN and the endpoint follow
// the new identifier and the data stays.
func TestRDS_ModifyDBInstanceMovesThePortAndRenames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := rdsClient()
	f := rdsInstanceFixture{t: t, ctx: ctx, client: c, family: "postgres"}
	id := uniqueName("sdk-rename")
	newID := uniqueName("sdk-renamed")
	other := uniqueName("sdk-rename-other")
	for _, instanceID := range []string{id, other} {
		_, err := c.CreateDBInstance(ctx, &rds.CreateDBInstanceInput{
			DBInstanceIdentifier:  aws.String(instanceID),
			DBInstanceClass:       aws.String("db.t3.micro"),
			Engine:                aws.String("postgres"),
			AllocatedStorage:      aws.Int32(20),
			MasterUsername:        aws.String(restoreSourceUsername),
			MasterUserPassword:    aws.String(restoreSourcePassword),
			DBName:                aws.String(restoreSourceDatabase),
			BackupRetentionPeriod: aws.Int32(0),
		})
		require.NoError(t, err)
		f.cleanup(instanceID)
	}
	f.cleanup(newID)
	f.connect(id).exec(t, `CREATE TABLE ledger (entry text); INSERT INTO ledger VALUES ('before the move')`)
	before := f.waitAvailable(id)

	_, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), DBPortNumber: aws.Int32(80)})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), NewDBInstanceIdentifier: aws.String("1-starts-with-a-digit")})
	assertAWSAPIErrorCode(t, err, "InvalidParameterValue")
	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), NewDBInstanceIdentifier: aws.String(other)})
	assertAWSAPIErrorCode(t, err, "DBInstanceAlreadyExists")

	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := free.Addr().(*net.TCPAddr).Port
	require.NoError(t, free.Close())
	moved, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), DBPortNumber: aws.Int32(int32(port))})
	require.NoError(t, err)
	assert.Equal(t, "modifying", aws.ToString(moved.DBInstance.DBInstanceStatus), "a port change restarts the engine at once")
	assert.Equal(t, int32(port), aws.ToInt32(moved.DBInstance.PendingModifiedValues.Port))
	after := f.waitAvailable(id)
	assert.Equal(t, int32(port), aws.ToInt32(after.Endpoint.Port))
	assert.Nil(t, after.PendingModifiedValues.Port)
	assert.Equal(t, []string{"before the move"}, f.connect(id).entries(t))
	oldEndpoint := net.JoinHostPort(aws.ToString(before.Endpoint.Address), fmt.Sprint(aws.ToInt32(before.Endpoint.Port)))
	if conn, dialErr := net.DialTimeout("tcp", oldEndpoint, time.Second); dialErr == nil {
		_ = conn.Close()
		t.Fatalf("the old endpoint %s still accepts connections", oldEndpoint)
	}

	held, err := c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), NewDBInstanceIdentifier: aws.String(newID)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(held.DBInstance.DBInstanceIdentifier), "a rename waits for the maintenance window")
	assert.Equal(t, newID, aws.ToString(held.DBInstance.PendingModifiedValues.DBInstanceIdentifier))

	_, err = c.ModifyDBInstance(ctx, &rds.ModifyDBInstanceInput{DBInstanceIdentifier: aws.String(id), ApplyImmediately: aws.Bool(true)})
	require.NoError(t, err)
	renamed := rdsAwaitRenamed(t, ctx, c, newID)
	assert.Equal(t, strings.TrimSuffix(aws.ToString(before.DBInstanceArn), id)+newID, aws.ToString(renamed.DBInstanceArn))
	assert.Equal(t, aws.ToString(before.DbiResourceId), aws.ToString(renamed.DbiResourceId))
	assert.Nil(t, renamed.PendingModifiedValues.DBInstanceIdentifier)
	_, err = c.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: aws.String(id)})
	assertAWSAPIErrorCode(t, err, "DBInstanceNotFound")
	assert.Equal(t, []string{"before the move"}, f.connect(newID).entries(t), "the renamed instance keeps its data")
}
