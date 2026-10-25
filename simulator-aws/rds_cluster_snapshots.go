package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsClusterSnapshotARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster-snapshot:%s", awsRegion(), awsAccountID(), id)
}

func findRDSClusterSnapshotByARN(arn string) (RDSClusterSnapshot, bool) {
	for _, s := range rdsClusterSnapshots.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsClusterSnapshots.Get(arn); ok {
		return s, true
	}
	return RDSClusterSnapshot{}, false
}

func renderRDSClusterSnapshot(s RDSClusterSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBClusterSnapshot>")
	fmt.Fprintf(&b, "<DBClusterSnapshotIdentifier>%s</DBClusterSnapshotIdentifier>", xmlEscape(s.DBClusterSnapshotIdentifier))
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(s.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(s.DbClusterResourceId))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(s.EngineVersion))
	fmt.Fprintf(&b, "<EngineMode>%s</EngineMode>", xmlEscape(s.EngineMode))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", s.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(s.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", s.Port)
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(s.VpcId))
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", s.StorageEncrypted)
	fmt.Fprintf(&b, "<SnapshotCreateTime>%s</SnapshotCreateTime>", xmlEscape(s.SnapshotCreateTime))
	fmt.Fprintf(&b, "<ClusterCreateTime>%s</ClusterCreateTime>", xmlEscape(s.ClusterCreateTime))
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<PercentProgress>%d</PercentProgress>", s.PercentProgress)
	fmt.Fprintf(&b, "<DBClusterSnapshotArn>%s</DBClusterSnapshotArn>", xmlEscape(s.ARN))
	if s.SourceDBClusterSnapshotArn != "" {
		fmt.Fprintf(&b, "<SourceDBClusterSnapshotArn>%s</SourceDBClusterSnapshotArn>", xmlEscape(s.SourceDBClusterSnapshotArn))
	}
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", s.IAMDatabaseAuthentication)
	b.WriteString("<AvailabilityZones>")
	for _, az := range s.AvailabilityZones {
		fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(az))
	}
	b.WriteString("</AvailabilityZones>")
	b.WriteString("</DBClusterSnapshot>")
	return b.String()
}

func handleRDSCreateClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	clusterID := r.FormValue("DBClusterIdentifier")
	if snapID == "" || clusterID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBClusterSnapshotIdentifier and DBClusterIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	cl, ok := rdsClusters.Get(clusterID)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", clusterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if _, exists := rdsClusterSnapshots.Get(snapID); exists {
		rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
			fmt.Sprintf("DBClusterSnapshot %q already exists", snapID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	if !rdsClusterVolumeSettled(w, r, cl) {
		return
	}
	snap := rdsNewClusterSnapshot(cl, snapID)
	snap.Tags = parseAWSQueryTagMap(r, "Tags.Tag")
	rdsClusterSnapshots.Put(snapID, snap)
	bg.Go(func() { rdsCaptureClusterSnapshotData(snapID, clusterID) })
	rdsXMLResponse(w, "CreateDBClusterSnapshot", renderRDSClusterSnapshot(snap), sim.RequestID(r.Context()))
}

// rdsClusterVolumeSettled answers InvalidDBClusterStateFault for a cluster
// whose volume a restore has not finished seeding or a deletion is removing.
func rdsClusterVolumeSettled(w http.ResponseWriter, r *http.Request, cluster RDSCluster) bool {
	if cluster.Status != "creating" && cluster.Status != "incompatible-restore" && cluster.Status != "deleting" {
		return true
	}
	rdsErrorXML(w, "InvalidDBClusterStateFault",
		fmt.Sprintf("DB cluster %s is %s and its data cannot be captured.", cluster.DBClusterIdentifier, cluster.Status),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return false
}

// rdsNewClusterSnapshot is a manual snapshot of cluster, creating until its
// cluster volume is captured.
func rdsNewClusterSnapshot(cluster RDSCluster, snapID string) RDSClusterSnapshot {
	return RDSClusterSnapshot{
		DBClusterSnapshotIdentifier: snapID,
		DBClusterIdentifier:         cluster.DBClusterIdentifier,
		DbClusterResourceId:         cluster.DbClusterResourceId,
		Engine:                      cluster.Engine,
		EngineVersion:               cluster.EngineVersion,
		EngineMode:                  cluster.EngineMode,
		Status:                      "creating",
		DatabaseName:                cluster.DatabaseName,
		IAMDatabaseAuthentication:   cluster.EnableIAMDatabaseAuthentication,
		MasterUserSecret:            append([]byte(nil), cluster.MasterUserSecret...),
		BackendMasterUserSecret:     append([]byte(nil), cluster.BackendMasterUserSecret...),
		AllocatedStorage:            cluster.AllocatedStorage,
		MasterUsername:              cluster.MasterUsername,
		Port:                        cluster.Port,
		StorageEncrypted:            cluster.StorageEncrypted,
		SnapshotCreateTime:          time.Now().UTC().Format(time.RFC3339),
		ClusterCreateTime:           cluster.ClusterCreateTime,
		SnapshotType:                "manual",
		AvailabilityZones:           cluster.AvailabilityZones,
		ARN:                         rdsClusterSnapshotARN(snapID),
	}
}

func handleRDSDescribeClusterSnapshots(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("DBClusterSnapshotIdentifier")
	filterCluster := r.FormValue("DBClusterIdentifier")
	filterType := r.FormValue("SnapshotType")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusterSnapshots>")
	for _, s := range rdsClusterSnapshots.List() {
		if filterID != "" && s.DBClusterSnapshotIdentifier != filterID {
			continue
		}
		if filterCluster != "" && s.DBClusterIdentifier != filterCluster {
			continue
		}
		if filterType != "" && s.SnapshotType != filterType {
			continue
		}
		matched = true
		b.WriteString(renderRDSClusterSnapshot(s))
	}
	if filterID != "" && !matched {
		rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
			fmt.Sprintf("DBClusterSnapshot %q not found", filterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusterSnapshots>")
	rdsXMLResponse(w, "DescribeDBClusterSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDeleteClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBClusterSnapshotIdentifier")
	snap, ok := rdsClusterSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSClusterSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if snap.Status == "creating" || snap.Status == "copying" {
		rdsErrorXML(w, "InvalidDBClusterSnapshotStateFault",
			fmt.Sprintf("Cannot delete the snapshot because it is not in available state, current state: %s", snap.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if snap.SnapshotType == "automated" {
		rdsErrorXML(w, "InvalidDBClusterSnapshotStateFault",
			"Only manual snapshots may be deleted.", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsClusterSnapshots.Delete(snap.DBClusterSnapshotIdentifier)
	sim.RemoveVolumeSettled(rdsClusterSnapshotVolume(snap.DBClusterSnapshotIdentifier), "rds")
	snap.Status = "deleted"
	rdsXMLResponse(w, "DeleteDBClusterSnapshot", renderRDSClusterSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSCopyClusterSnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceDBClusterSnapshotIdentifier")
	targetID := r.FormValue("TargetDBClusterSnapshotIdentifier")
	if srcID == "" || targetID == "" {
		rdsErrorXML(w, "MissingParameter",
			"SourceDBClusterSnapshotIdentifier and TargetDBClusterSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	src, ok := rdsClusterSnapshots.Get(srcID)
	if !ok {
		src, ok = findRDSClusterSnapshotByARN(srcID)
		if !ok {
			rdsErrorXML(w, "DBClusterSnapshotNotFoundFault",
				fmt.Sprintf("DBClusterSnapshot %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsClusterSnapshots.Get(targetID); exists {
		rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
			fmt.Sprintf("DBClusterSnapshot %q already exists", targetID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	if src.Status != "available" {
		rdsErrorXML(w, "InvalidDBClusterSnapshotStateFault",
			fmt.Sprintf("DBClusterSnapshot %q is %s; it must be available to copy", src.DBClusterSnapshotIdentifier, src.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	tags := parseAWSQueryTagMap(r, "Tags.Tag")
	if r.FormValue("CopyTags") == "true" {
		tags = mergeTags(tags, src.Tags)
	}
	cp := src
	cp.DBClusterSnapshotIdentifier = targetID
	cp.Status = "copying"
	cp.StatusReason = ""
	cp.SourceDBClusterSnapshotArn = src.ARN
	cp.SnapshotType = "manual"
	cp.PercentProgress = 0
	cp.SnapshotCreateTime = time.Now().UTC().Format(time.RFC3339)
	cp.ARN = rdsClusterSnapshotARN(targetID)
	cp.Tags = tags
	rdsClusterSnapshots.Put(targetID, cp)
	sourceID := src.DBClusterSnapshotIdentifier
	bg.Go(func() { rdsCopyClusterSnapshotData(targetID, sourceID) })
	rdsXMLResponse(w, "CopyDBClusterSnapshot", renderRDSClusterSnapshot(cp), sim.RequestID(r.Context()))
}
