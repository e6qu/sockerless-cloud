package aws_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliDBCapacityReservation struct {
	DBCapacityReservationName                string `json:"DBCapacityReservationName"`
	DBCapacityReservationIdentifier          string `json:"DBCapacityReservationIdentifier"`
	DBCapacityReservationArn                 string `json:"DBCapacityReservationArn"`
	State                                    string `json:"State"`
	LicenseModel                             string `json:"LicenseModel"`
	ReservedCapacityPerAvailabilityZone      int    `json:"ReservedCapacityPerAvailabilityZone"`
	EndDate                                  string `json:"EndDate"`
	AvailableCapacityPerAvailabilityZoneList []struct {
		AvailabilityZone  string `json:"AvailabilityZone"`
		AvailableCapacity int    `json:"AvailableCapacity"`
	} `json:"AvailableCapacityPerAvailabilityZoneList"`
}

func (r cliDBCapacityReservation) available() map[string]int {
	available := map[string]int{}
	for _, zone := range r.AvailableCapacityPerAvailabilityZoneList {
		available[zone.AvailabilityZone] = zone.AvailableCapacity
	}
	return available
}

// TestRDSCLI_DBCapacityReservations reserves capacity for RDS for Oracle DB
// instances through the aws CLI, launches a DB instance into it, and covers
// describe-db-capacity-reservations, modify-db-capacity-reservation and
// cancel-db-capacity-reservation.
func TestRDSCLI_DBCapacityReservations(t *testing.T) {
	t.Parallel()
	name := "cli-dbcr-oracle"
	out := runCLI(t, awsCLI("rds", "create-db-capacity-reservation",
		"--db-capacity-reservation-name", name,
		"--db-instance-class", "db.m5.large",
		"--engine", "oracle-ee",
		"--capacity-per-availability-zone", "1",
		"--availability-zones", "us-east-1a", "us-east-1b",
		"--tags", "Key=team,Value=data"))
	var created struct {
		DBCapacityReservation cliDBCapacityReservation `json:"DBCapacityReservation"`
	}
	parseJSON(t, out, &created)
	res := created.DBCapacityReservation
	id := res.DBCapacityReservationIdentifier
	require.True(t, strings.HasPrefix(id, "dbcr-"), "identifier %s", id)
	assert.Equal(t, "active", res.State)
	assert.Equal(t, "bring-your-own-license", res.LicenseModel)
	assert.Equal(t, map[string]int{"us-east-1a": 1, "us-east-1b": 1}, res.available())
	assert.True(t, strings.HasSuffix(res.DBCapacityReservationArn, ":db-capacity-reservation:"+id))

	runCLIExpectError(t, awsCLI("rds", "create-db-capacity-reservation",
		"--db-capacity-reservation-name", name,
		"--db-instance-class", "db.m5.large",
		"--engine", "oracle-ee",
		"--capacity-per-availability-zone", "1",
		"--availability-zones", "us-east-1a"))

	instID := "cli-dbcr-oracle-db"
	out = runCLI(t, awsCLI("rds", "create-db-instance",
		"--db-instance-identifier", instID,
		"--engine", "oracle-ee",
		"--db-instance-class", "db.m5.large",
		"--allocated-storage", "20",
		"--master-username", "admin",
		"--master-user-password", "password123!",
		"--availability-zone", "us-east-1a",
		"--db-capacity-reservation-identifier", name))
	t.Cleanup(func() {
		_ = awsCLI("rds", "delete-db-instance", "--db-instance-identifier", instID, "--skip-final-snapshot").Run()
	})
	var launched struct {
		DBInstance struct {
			DBCapacityReservationIdentifier string `json:"DBCapacityReservationIdentifier"`
			AvailabilityZone                string `json:"AvailabilityZone"`
		} `json:"DBInstance"`
	}
	parseJSON(t, out, &launched)
	assert.Equal(t, id, launched.DBInstance.DBCapacityReservationIdentifier)
	assert.Equal(t, "us-east-1a", launched.DBInstance.AvailabilityZone)

	end := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	out = runCLI(t, awsCLI("rds", "modify-db-capacity-reservation",
		"--db-capacity-reservation-identifier", id,
		"--capacity-per-availability-zone", "2",
		"--end-date-type", "limited",
		"--end-date", end))
	var modified struct {
		DBCapacityReservation cliDBCapacityReservation `json:"DBCapacityReservation"`
	}
	parseJSON(t, out, &modified)
	assert.Equal(t, 2, modified.DBCapacityReservation.ReservedCapacityPerAvailabilityZone)
	assert.Equal(t, map[string]int{"us-east-1a": 1, "us-east-1b": 2}, modified.DBCapacityReservation.available())
	assert.NotEmpty(t, modified.DBCapacityReservation.EndDate)

	out = runCLI(t, awsCLI("rds", "describe-db-capacity-reservations",
		"--filters", "Name=db-capacity-reservation-name,Values="+name, "Name=state,Values=active"))
	var described struct {
		DBCapacityReservations []cliDBCapacityReservation `json:"DBCapacityReservations"`
	}
	parseJSON(t, out, &described)
	require.Len(t, described.DBCapacityReservations, 1)
	assert.Equal(t, id, described.DBCapacityReservations[0].DBCapacityReservationIdentifier)

	out = runCLI(t, awsCLI("rds", "cancel-db-capacity-reservation", "--db-capacity-reservation-identifier", name))
	var cancelled struct {
		DBCapacityReservation cliDBCapacityReservation `json:"DBCapacityReservation"`
	}
	parseJSON(t, out, &cancelled)
	assert.Equal(t, "cancelled", cancelled.DBCapacityReservation.State)
	assert.Equal(t, map[string]int{"us-east-1a": 0, "us-east-1b": 0}, cancelled.DBCapacityReservation.available())

	refused := runCLIExpectError(t, awsCLI("rds", "cancel-db-capacity-reservation", "--db-capacity-reservation-identifier", id))
	assert.Contains(t, refused, "InvalidDBCapacityReservationState")
	missing := runCLIExpectError(t, awsCLI("rds", "describe-db-capacity-reservations",
		"--db-capacity-reservation-identifier", "dbcr-00000000000000000000000000000000"))
	assert.Contains(t, missing, "DBCapacityReservationNotFound")
}
