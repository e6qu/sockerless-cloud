package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func rdsClusterEndpointARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-endpoint:%s", awsRegion(), awsAccountID(), id)
}

func findRDSClusterEndpointByARN(arn string) (RDSClusterEndpoint, bool) {
	for _, e := range rdsClusterEndpoints.List() {
		if e.ARN == arn {
			return e, true
		}
	}
	if e, ok := rdsClusterEndpoints.Get(arn); ok {
		return e, true
	}
	return RDSClusterEndpoint{}, false
}

func renderRDSClusterEndpoint(e RDSClusterEndpoint) string {
	var b strings.Builder
	b.WriteString("<DBClusterEndpointIdentifier>")
	b.WriteString(xmlEscape(e.DBClusterEndpointIdentifier))
	b.WriteString("</DBClusterEndpointIdentifier>")
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(e.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DBClusterEndpointResourceIdentifier>%s</DBClusterEndpointResourceIdentifier>", xmlEscape(e.DBClusterEndpointResourceIdentifier))
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(e.Endpoint))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(e.Status))
	fmt.Fprintf(&b, "<EndpointType>%s</EndpointType>", xmlEscape(e.EndpointType))
	fmt.Fprintf(&b, "<CustomEndpointType>%s</CustomEndpointType>", xmlEscape(e.CustomEndpointType))
	fmt.Fprintf(&b, "<DBClusterEndpointArn>%s</DBClusterEndpointArn>", xmlEscape(e.ARN))
	b.WriteString("<StaticMembers>")
	for _, m := range e.StaticMembers {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(m))
	}
	b.WriteString("</StaticMembers>")
	b.WriteString("<ExcludedMembers>")
	for _, m := range e.ExcludedMembers {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(m))
	}
	b.WriteString("</ExcludedMembers>")
	return b.String()
}

func handleRDSCreateClusterEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterEndpointIdentifier")
	clusterID := r.FormValue("DBClusterIdentifier")
	endpointType := r.FormValue("EndpointType")
	if id == "" || clusterID == "" || endpointType == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterEndpointIdentifier, DBClusterIdentifier and EndpointType are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusters.Get(clusterID); !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsClusterEndpoints.Get(id); ok {
		rdsErrorXML(w, "DBClusterEndpointAlreadyExistsFault",
			fmt.Sprintf("DBClusterEndpoint %q already exists", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	e := RDSClusterEndpoint{
		DBClusterEndpointIdentifier:         id,
		DBClusterIdentifier:                 clusterID,
		DBClusterEndpointResourceIdentifier: rdsGlobalClusterResourceID(),
		Endpoint:                            fmt.Sprintf("%s.cluster-custom-sim.%s.rds.amazonaws.com", id, awsRegion()),
		Status:                              "available",
		EndpointType:                        "CUSTOM",
		CustomEndpointType:                  endpointType,
		StaticMembers:                       parseRDSIndexedStringList(r, "StaticMembers.member"),
		ExcludedMembers:                     parseRDSIndexedStringList(r, "ExcludedMembers.member"),
		ARN:                                 rdsClusterEndpointARN(id),
		Tags:                                parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsClusterEndpoints.Put(id, e)
	rdsXMLResponse(w, "CreateDBClusterEndpoint", renderRDSClusterEndpoint(e), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusterEndpoints(w http.ResponseWriter, r *http.Request) {
	wantedEndpoint := r.FormValue("DBClusterEndpointIdentifier")
	wantedCluster := r.FormValue("DBClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterEndpoints>")
	for _, e := range rdsClusterEndpoints.List() {
		if wantedEndpoint != "" && e.DBClusterEndpointIdentifier != wantedEndpoint {
			continue
		}
		if wantedCluster != "" && e.DBClusterIdentifier != wantedCluster {
			continue
		}
		matched = true
		b.WriteString("<DBClusterEndpointList>")
		b.WriteString(renderRDSClusterEndpoint(e))
		b.WriteString("</DBClusterEndpointList>")
	}
	if wantedEndpoint != "" && !matched {
		rdsErrorXML(w, "DBClusterEndpointNotFoundFault",
			fmt.Sprintf("DBClusterEndpoint %q not found", wantedEndpoint),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterEndpoints>")
	rdsXMLResponse(w, "DescribeDBClusterEndpoints", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterEndpointIdentifier")
	e, ok := rdsClusterEndpoints.Get(id)
	if !ok {
		e, ok = findRDSClusterEndpointByARN(id)
		if !ok {
			rdsErrorXML(w, "DBClusterEndpointNotFoundFault",
				fmt.Sprintf("DBClusterEndpoint %q not found", id),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsClusterEndpoints.Delete(e.DBClusterEndpointIdentifier)
	e.Status = "deleting"
	rdsXMLResponse(w, "DeleteDBClusterEndpoint", renderRDSClusterEndpoint(e), sim.RequestID(r.Context()))
}
