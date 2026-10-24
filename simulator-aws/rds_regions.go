package main

import (
	"fmt"
	"net/http"
)

// region is the Region the instance lives in: the Region its creating request
// was signed for, or the simulator's own for a row that names none.
func (i RDSInstance) region() string {
	return firstNonEmpty(i.Region, awsRegion())
}

func rdsInstanceARNIn(region, id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:db:%s", region, awsAccountID(), id)
}

func rdsInstanceEndpointIn(region, id string) string {
	return fmt.Sprintf("%s.%s.rds.amazonaws.com", id, region)
}

// rdsInstanceIn is the DB instance id names in the Region r is signed for.
func rdsInstanceIn(r *http.Request, id string) (RDSInstance, bool) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.region() != rdsRequestRegion(r) {
		return RDSInstance{}, false
	}
	return instance, true
}

// rdsPlaceInstance sets where an instance a request creates lives: the
// Region the request is signed for, and the ARN and endpoint that Region
// names.
func rdsPlaceInstance(r *http.Request, instance *RDSInstance) {
	region := rdsRequestRegion(r)
	instance.Region = region
	instance.ARN = rdsInstanceARNIn(region, instance.DBInstanceIdentifier)
	instance.Endpoint = rdsInstanceEndpointIn(region, instance.DBInstanceIdentifier)
}
