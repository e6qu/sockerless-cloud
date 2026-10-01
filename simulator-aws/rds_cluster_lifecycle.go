package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// rdsClusterMembers lists a cluster's DB instances, the writer first: Amazon
// Aurora makes the first instance created in a cluster its writer.
func rdsClusterMembers(clusterID string) []RDSInstance {
	members := rdsInstances.Filter(func(i RDSInstance) bool { return i.DBClusterIdentifier == clusterID })
	sort.Slice(members, func(a, b int) bool {
		if members[a].InstanceCreateTime != members[b].InstanceCreateTime {
			return members[a].InstanceCreateTime < members[b].InstanceCreateTime
		}
		return members[a].DBInstanceIdentifier < members[b].DBInstanceIdentifier
	})
	return members
}

func renderRDSClusterMembers(clusterID string) string {
	var b strings.Builder
	b.WriteString("<DBClusterMembers>")
	for index, member := range rdsClusterMembers(clusterID) {
		b.WriteString("<DBClusterMember>")
		fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(member.DBInstanceIdentifier))
		fmt.Fprintf(&b, "<IsClusterWriter>%t</IsClusterWriter>", index == 0)
		b.WriteString("<DBClusterParameterGroupStatus>in-sync</DBClusterParameterGroupStatus>")
		b.WriteString("<PromotionTier>1</PromotionTier>")
		b.WriteString("</DBClusterMember>")
	}
	b.WriteString("</DBClusterMembers>")
	return b.String()
}

// rdsRequireClusterState answers InvalidDBClusterStateFault unless the cluster
// is in the one state the lifecycle action runs from.
func rdsRequireClusterState(w http.ResponseWriter, r *http.Request, cluster RDSCluster, required, action string) bool {
	if cluster.Status == required {
		return true
	}
	rdsErrorXML(w, "InvalidDBClusterStateFault",
		fmt.Sprintf("DB cluster %s is not in %s state and cannot be %s.", cluster.DBClusterIdentifier, required, action),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return false
}

// rdsSetClusterStatus moves the cluster and every member instance to status.
func rdsSetClusterStatus(clusterID, status string) {
	rdsClusters.Update(clusterID, func(c *RDSCluster) { c.Status = status })
	for _, member := range rdsClusterMembers(clusterID) {
		rdsInstances.Update(member.DBInstanceIdentifier, func(i *RDSInstance) { i.DBInstanceStatus = status })
	}
}

func handleRDSStopCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cluster, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if !rdsRequireClusterState(w, r, cluster, "available", "stopped") {
		return
	}
	// Amazon Aurora answers stopping and lands the cluster stopped once every
	// member instance's engine has stopped.
	rdsSetClusterStatus(id, "stopping")
	updated, _ := rdsClusters.Get(id)
	bg.Go(func() { rdsFinishClusterStop(id) })
	rdsXMLResponse(w, "StopDBCluster", renderRDSCluster(updated), sim.RequestID(r.Context()))
}

func handleRDSStartCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cluster, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if !rdsRequireClusterState(w, r, cluster, "stopped", "started") {
		return
	}
	rdsSetClusterStatus(id, "starting")
	updated, _ := rdsClusters.Get(id)
	bg.Go(func() { rdsFinishClusterStart(id) })
	rdsXMLResponse(w, "StartDBCluster", renderRDSCluster(updated), sim.RequestID(r.Context()))
}

// rdsFinishClusterStop stops each member's engine and lands the member
// stopped once its engine has; the cluster lands stopped when all have. A
// member whose engine fails to stop keeps the cluster stopping.
func rdsFinishClusterStop(clusterID string) {
	stopped := true
	for _, member := range rdsClusterMembers(clusterID) {
		if member.DBInstanceStatus != "stopping" {
			continue
		}
		rdsFinishStop(member.DBInstanceIdentifier)
		if current, _ := rdsInstances.Get(member.DBInstanceIdentifier); current.DBInstanceStatus != "stopped" {
			stopped = false
		}
	}
	if stopped && rdsStopAuroraDataPlane(clusterID, false) != nil {
		stopped = false
	}
	if stopped {
		rdsClusters.Update(clusterID, func(c *RDSCluster) {
			if c.Status == "stopping" {
				c.Status = "stopped"
			}
		})
	}
}

// rdsFinishClusterStart starts each member's engine and lands the member
// available once it runs; the cluster lands available when all do. A member
// whose engine fails to start keeps the cluster starting.
func rdsFinishClusterStart(clusterID string) {
	cluster, ok := rdsClusters.Get(clusterID)
	if !ok {
		return
	}
	if _, installed := rdsLoadAuroraDataPlane(clusterID); !installed && len(cluster.MasterUserSecret) > 0 {
		if err := rdsInstallAuroraDataPlane(&cluster, ""); err != nil {
			log.Printf("Amazon Aurora cluster %s: start the cluster volume's engine: %v", clusterID, err)
			return
		}
		rdsClusters.Update(clusterID, func(c *RDSCluster) {
			c.Endpoint, c.ReaderEndpoint = cluster.Endpoint, cluster.ReaderEndpoint
		})
	}
	started := true
	for _, member := range rdsClusterMembers(clusterID) {
		if member.DBInstanceStatus != "starting" {
			continue
		}
		if err := rdsStartInstanceEngine(&member); err != nil {
			log.Printf("Amazon RDS cluster %s: start DB instance %s: %v", clusterID, member.DBInstanceIdentifier, err)
			started = false
			continue
		}
		rdsInstances.Update(member.DBInstanceIdentifier, func(i *RDSInstance) {
			i.Endpoint, i.Port = member.Endpoint, member.Port
			i.MasterUserSecret, i.BackendMasterUserSecret = member.MasterUserSecret, member.BackendMasterUserSecret
			if i.DBInstanceStatus == "starting" {
				i.DBInstanceStatus = "available"
			}
		})
	}
	if started {
		rdsClusters.Update(clusterID, func(c *RDSCluster) {
			if c.Status == "starting" {
				c.Status = "available"
			}
		})
	}
}

// rdsRecoverClusterTransitions finishes the stops, starts, restores and
// snapshots a previous process took but did not see through.
func rdsRecoverClusterTransitions() {
	capturing := map[string]bool{}
	for _, snapshot := range rdsClusterSnapshots.List() {
		if snapshot.Status == "creating" {
			capturing[snapshot.DbClusterResourceId] = true
		}
	}
	for _, cluster := range rdsClusters.List() {
		id := cluster.DBClusterIdentifier
		switch cluster.Status {
		case "deleting":
			// A deletion with a final snapshot resumes after the capture
			// rdsRecoverClusterSnapshots resumes.
			if resourceID := cluster.DbClusterResourceId; !capturing[resourceID] {
				bg.Go(func() { rdsFinishClusterDeletion(id, resourceID) })
			}
		case "stopping":
			bg.Go(func() { rdsFinishClusterStop(id) })
		case "starting":
			bg.Go(func() { rdsFinishClusterStart(id) })
		case "creating":
			bg.Go(func() { rdsFinishClusterRestore(id) })
		}
	}
	rdsRecoverClusterSnapshots()
}
