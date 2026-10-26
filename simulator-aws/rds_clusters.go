package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsClusterARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:cluster:%s", awsRegion(), awsAccountID(), id)
}

func rdsClusterResourceID() string {
	return "cluster-" + strings.ToUpper(strings.ReplaceAll(sim.NewUUID(), "-", ""))[:26]
}

func findRDSClusterByARN(arn string) (RDSCluster, bool) {
	for _, c := range rdsClusters.List() {
		if c.ARN == arn {
			return c, true
		}
	}
	if c, ok := rdsClusters.Get(arn); ok {
		return c, true
	}
	return RDSCluster{}, false
}

func renderRDSCluster(c RDSCluster) string {
	var b strings.Builder
	b.WriteString("<DBCluster>")
	fmt.Fprintf(&b, "<DBClusterIdentifier>%s</DBClusterIdentifier>", xmlEscape(c.DBClusterIdentifier))
	fmt.Fprintf(&b, "<DbClusterResourceId>%s</DbClusterResourceId>", xmlEscape(c.DbClusterResourceId))
	fmt.Fprintf(&b, "<DBClusterArn>%s</DBClusterArn>", xmlEscape(c.ARN))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(c.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(c.EngineVersion))
	fmt.Fprintf(&b, "<EngineMode>%s</EngineMode>", xmlEscape(c.EngineMode))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(c.Status))
	fmt.Fprintf(&b, "<DatabaseName>%s</DatabaseName>", xmlEscape(c.DatabaseName))
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(c.MasterUsername))
	fmt.Fprintf(&b, "<Port>%d</Port>", c.Port)
	fmt.Fprintf(&b, "<Endpoint>%s</Endpoint>", xmlEscape(c.Endpoint))
	fmt.Fprintf(&b, "<ReaderEndpoint>%s</ReaderEndpoint>", xmlEscape(c.ReaderEndpoint))
	fmt.Fprintf(&b, "<DBClusterParameterGroup>%s</DBClusterParameterGroup>", xmlEscape(c.DBClusterParameterGroup))
	fmt.Fprintf(&b, "<DBSubnetGroup>%s</DBSubnetGroup>", xmlEscape(c.DBSubnetGroup))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", c.AllocatedStorage)
	fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", c.BackupRetentionPeriod)
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", c.StorageEncrypted)
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", c.DeletionProtection)
	fmt.Fprintf(&b, "<IAMDatabaseAuthenticationEnabled>%t</IAMDatabaseAuthenticationEnabled>", c.EnableIAMDatabaseAuthentication)
	fmt.Fprintf(&b, "<ClusterCreateTime>%s</ClusterCreateTime>", xmlEscape(c.ClusterCreateTime))
	b.WriteString(renderRDSRestorableWindow(c))
	fmt.Fprintf(&b, "<PreferredBackupWindow>%s</PreferredBackupWindow>", xmlEscape(c.PreferredBackupWindow))
	fmt.Fprintf(&b, "<PreferredMaintenanceWindow>%s</PreferredMaintenanceWindow>", xmlEscape(c.PreferredMaintenanceWindow))
	b.WriteString("<AvailabilityZones>")
	for _, az := range c.AvailabilityZones {
		fmt.Fprintf(&b, "<AvailabilityZone>%s</AvailabilityZone>", xmlEscape(az))
	}
	b.WriteString("</AvailabilityZones>")
	b.WriteString(renderRDSClusterMembers(c.DBClusterIdentifier))
	b.WriteString("</DBCluster>")
	return b.String()
}

func handleRDSCreateCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "DBClusterIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	releaseIdentifier, claimed := rdsClusterIdentifiers.claim(id)
	if !claimed {
		rdsErrorXML(w, "DBClusterAlreadyExistsFault", "DB cluster already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	defer releaseIdentifier()
	engine := r.FormValue("Engine")
	port := rdsDefaultPort(engine)
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	engineVersion := r.FormValue("EngineVersion")
	if engineVersion == "" {
		engineVersion = rdsDefaultEngineVersion(engine)
	}
	engineMode := r.FormValue("EngineMode")
	if engineMode == "" {
		engineMode = "provisioned"
	}
	paramGroup := r.FormValue("DBClusterParameterGroupName")
	if paramGroup == "" {
		paramGroup = "default." + rdsParameterGroupFamily(engine, engineVersion)
	}
	backupRetention := 1
	if v := r.FormValue("BackupRetentionPeriod"); v != "" {
		backupRetention = atoiOrZero(v)
	}
	cl := RDSCluster{
		DBClusterIdentifier:        id,
		DbClusterResourceId:        rdsClusterResourceID(),
		Engine:                     engine,
		EngineVersion:              engineVersion,
		EngineMode:                 engineMode,
		Status:                     "available",
		DatabaseName:               r.FormValue("DatabaseName"),
		MasterUsername:             r.FormValue("MasterUsername"),
		Port:                       port,
		Endpoint:                   fmt.Sprintf("%s.cluster-%s.%s.rds.amazonaws.com", id, "sim", awsRegion()),
		ReaderEndpoint:             fmt.Sprintf("%s.cluster-ro-%s.%s.rds.amazonaws.com", id, "sim", awsRegion()),
		DBClusterParameterGroup:    paramGroup,
		DBSubnetGroup:              r.FormValue("DBSubnetGroupName"),
		AllocatedStorage:           atoiOrZero(r.FormValue("AllocatedStorage")),
		BackupRetentionPeriod:      backupRetention,
		StorageEncrypted:           r.FormValue("StorageEncrypted") == "true",
		DeletionProtection:         r.FormValue("DeletionProtection") == "true",
		ClusterCreateTime:          time.Now().UTC().Format(time.RFC3339),
		AvailabilityZones:          []string{awsRegion() + "a", awsRegion() + "b", awsRegion() + "c"},
		PreferredBackupWindow:      rdsClusterBackupWindow(r),
		PreferredMaintenanceWindow: "mon:00:00-mon:03:00",
		ARN:                        rdsClusterARN(id),
		Tags:                       parseAWSQueryTagMap(r, "Tags.Tag"),

		EnableIAMDatabaseAuthentication: strings.EqualFold(r.FormValue("EnableIAMDatabaseAuthentication"), "true"),
	}
	if _, err := rdsNextBackupTime(cl.PreferredBackupWindow, time.Now()); err != nil {
		rdsErrorXML(w, "InvalidParameterValue", "The backup window must be in the format hh24:mi-hh24:mi.",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if rdsIsAurora(engine) && r.FormValue("MasterUserPassword") == "" {
		rdsErrorXML(w, "InvalidParameterValue",
			"The parameter MasterUserPassword must be provided and must not be blank.",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if err := rdsInstallAuroraDataPlane(&cl, r.FormValue("MasterUserPassword")); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	rdsClusters.Put(id, cl)
	rdsTakeFirstClusterBackup(id)
	rdsXMLResponse(w, "CreateDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}

func handleRDSDescribeClusters(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<DBClusters>")
	for _, c := range rdsClusters.List() {
		if wanted != "" && c.DBClusterIdentifier != wanted && c.ARN != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSCluster(c))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBClusterNotFoundFault",
			fmt.Sprintf("DBCluster %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBClusters>")
	rdsXMLResponse(w, "DescribeDBClusters", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	requestID := sim.RequestID(r.Context())
	cluster, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, requestID)
		return
	}
	if cluster.Status != "available" {
		rdsErrorXML(w, "InvalidDBClusterStateFault",
			fmt.Sprintf("DB cluster %s is not currently in the available state.", id), http.StatusBadRequest, requestID)
		return
	}
	port := cluster.Port
	if v := r.FormValue("Port"); v != "" {
		port = atoiOrZero(v)
		if port < 1150 || port > 65535 {
			rdsErrorXML(w, "InvalidParameterValue",
				fmt.Sprintf("Invalid port %s. The port must be between 1150 and 65535.", v), http.StatusBadRequest, requestID)
			return
		}
	}
	password, changePassword := r.Form["MasterUserPassword"]
	var newPassword string
	if changePassword {
		newPassword = password[0]
		if !rdsValidMasterPassword(newPassword) {
			rdsErrorXML(w, "InvalidParameterValue",
				"The parameter MasterUserPassword is not a valid password. It must contain from 8 to 41 printable ASCII characters other than '/', '\"' and '@'.",
				http.StatusBadRequest, requestID)
			return
		}
	}
	if v := r.FormValue("EngineVersion"); v != "" {
		cluster.EngineVersion = v
	}
	backupsChanged := false
	if v := r.FormValue("BackupRetentionPeriod"); v != "" {
		cluster.BackupRetentionPeriod = atoiOrZero(v)
		backupsChanged = true
	}
	if v := r.FormValue("PreferredBackupWindow"); v != "" {
		if _, err := rdsNextBackupTime(v, time.Now()); err != nil {
			rdsErrorXML(w, "InvalidParameterValue", "The backup window must be in the format hh24:mi-hh24:mi.", http.StatusBadRequest, requestID)
			return
		}
		cluster.PreferredBackupWindow = v
		backupsChanged = true
	}
	if v := r.FormValue("PreferredMaintenanceWindow"); v != "" {
		cluster.PreferredMaintenanceWindow = v
	}
	if v := r.FormValue("DeletionProtection"); v != "" {
		cluster.DeletionProtection = v == "true"
	}
	if v := r.FormValue("EnableIAMDatabaseAuthentication"); v != "" {
		cluster.EnableIAMDatabaseAuthentication = strings.EqualFold(v, "true")
	}
	if changePassword {
		if err := rdsModifyClusterMasterPassword(&cluster, newPassword); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, requestID)
			return
		}
	}
	if port != cluster.Port {
		if rdsIsAurora(cluster.Engine) {
			if err := rdsRebindAuroraPort(&cluster, port); err != nil {
				rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, requestID)
				return
			}
		} else {
			cluster.Port = port
		}
	}
	rdsClusters.Update(id, func(stored *RDSCluster) {
		bases := stored.BaseBackups
		*stored = cluster
		stored.BaseBackups = bases
	})
	if plane, ok := rdsLoadAuroraDataPlane(id); ok && backupsChanged {
		plane.backups.schedule()
		bg.Go(func() {
			if err := plane.backups.expire(time.Now()); err != nil {
				log.Printf("Amazon Aurora %s: expire automated backups: %v", id, err)
			}
		})
		rdsTakeFirstClusterBackup(id)
	}
	cluster, _ = rdsClusters.Get(id)
	rdsXMLResponse(w, "ModifyDBCluster", renderRDSCluster(cluster), requestID)
}

// rdsClusterBackupWindow is a new cluster's PreferredBackupWindow: the one
// the request names, or the simulator's default.
func rdsClusterBackupWindow(r *http.Request) string {
	if window := r.FormValue("PreferredBackupWindow"); window != "" {
		return window
	}
	return rdsDefaultBackupWindow
}

const rdsDefaultBackupWindow = "07:00-09:00"

// rdsValidMasterPassword applies the constraints the RDS API reference states
// for MasterUserPassword: 8 to 41 printable ASCII characters other than '/',
// '"' and '@'.
func rdsValidMasterPassword(password string) bool {
	if len(password) < 8 || len(password) > 41 {
		return false
	}
	for _, c := range password {
		if c < 0x20 || c > 0x7e || c == '/' || c == '"' || c == '@' {
			return false
		}
	}
	return true
}

// rdsModifyClusterMasterPassword seals the new master password into the
// cluster and installs it in the engine behind it: an Aurora cluster's one
// engine, or each member of a Multi-AZ DB cluster.
func rdsModifyClusterMasterPassword(cluster *RDSCluster, newPassword string) error {
	sealed, err := rdsSealMasterPassword(newPassword)
	if err != nil {
		return err
	}
	if !rdsIsAurora(cluster.Engine) {
		for _, member := range rdsClusterMembers(cluster.DBClusterIdentifier) {
			if err := rdsModifyDataPlaneAuthentication(&member, &newPassword); err != nil {
				return err
			}
			rdsInstances.Put(member.DBInstanceIdentifier, member)
		}
		cluster.MasterUserSecret = sealed
		return nil
	}
	if len(cluster.BackendMasterUserSecret) == 0 {
		cluster.BackendMasterUserSecret = append([]byte(nil), cluster.MasterUserSecret...)
	}
	if plane, ok := rdsLoadAuroraDataPlane(cluster.DBClusterIdentifier); ok && plane.engine.Running() {
		// An engine still starting rotates only once it accepts clients.
		if err := plane.engine.Ensure(); err != nil {
			return err
		}
		oldPassword, err := rdsAuroraBackendPassword(*cluster)
		if err != nil {
			return err
		}
		if err := rdsRotateEnginePassword(plane.engine, cluster.MasterUsername, rdsAuroraDatabaseName(*cluster), oldPassword, newPassword); err != nil {
			return err
		}
		cluster.BackendMasterUserSecret = append([]byte(nil), sealed...)
	}
	cluster.MasterUserSecret = sealed
	return nil
}

func handleRDSDeleteCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if cl.Status == "deleting" {
		rdsErrorXML(w, "InvalidDBClusterStateFault",
			fmt.Sprintf("DB cluster %s is already being deleted.", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if cl.DeletionProtection {
		rdsErrorXML(w, "InvalidParameterCombination",
			"Cannot delete protected Cluster, please disable deletion protection and try again.",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	// A deleting member finishes its own deletion.
	var members []RDSInstance
	for _, member := range rdsClusterMembers(id) {
		if member.DBInstanceStatus != "deleting" {
			members = append(members, member)
		}
	}
	// An Aurora cluster's instances are deleted one by one before the
	// cluster; deleting a Multi-AZ DB cluster terminates its members.
	if rdsIsAurora(cl.Engine) && len(members) > 0 {
		rdsErrorXML(w, "InvalidDBClusterStateFault",
			"Cluster cannot be deleted, it still contains DB instances in non-deleting state.",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	skipFinal := strings.EqualFold(r.FormValue("SkipFinalSnapshot"), "true")
	finalSnapID := r.FormValue("FinalDBSnapshotIdentifier")
	if skipFinal && finalSnapID != "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier cannot be specified when SkipFinalSnapshot is true",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if !skipFinal && finalSnapID == "" {
		rdsErrorXML(w, "InvalidParameterCombination",
			"FinalDBSnapshotIdentifier is required unless SkipFinalSnapshot is specified",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if finalSnapID != "" {
		if _, exists := rdsClusterSnapshots.Get(finalSnapID); exists {
			rdsErrorXML(w, "DBClusterSnapshotAlreadyExistsFault",
				fmt.Sprintf("DBClusterSnapshot %q already exists", finalSnapID),
				http.StatusConflict, sim.RequestID(r.Context()))
			return
		}
		if !rdsClusterVolumeSettled(w, r, cl) {
			return
		}
		rdsClusterSnapshots.Put(finalSnapID, rdsNewClusterSnapshot(cl, finalSnapID))
	}
	cl.Status = "deleting"
	cl.RetainAutomatedBackups = strings.EqualFold(r.FormValue("DeleteAutomatedBackups"), "false")
	rdsClusters.Put(id, cl)
	body := renderRDSCluster(cl)
	for _, member := range members {
		rdsInstances.Delete(member.DBInstanceIdentifier)
		// The member is gone either way; rdsStopDataPlane logs the failure.
		_ = rdsStopDataPlane(member.DBInstanceIdentifier, true)
		rdsRemoveInstanceVolume(member.DBInstanceIdentifier)
	}
	resourceID := cl.DbClusterResourceId
	if finalSnapID != "" {
		// The final snapshot captures the cluster volume before the engine
		// and the volume go, and the deleting cluster holds its identifier
		// until then.
		snapID := finalSnapID
		bg.Go(func() {
			rdsCaptureClusterSnapshotData(snapID, id)
			rdsFinishClusterDeletion(id, resourceID)
		})
	} else {
		rdsFinishClusterDeletion(id, resourceID)
	}
	rdsXMLResponse(w, "DeleteDBCluster", body, sim.RequestID(r.Context()))
}

// handleRDSFailoverCluster returns the cluster unchanged: it promotes no Aurora
// Replica. The response carries the full DBCluster, which waiters read.
func handleRDSFailoverCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBClusterIdentifier")
	cl, ok := rdsClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", "DB cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsXMLResponse(w, "FailoverDBCluster", renderRDSCluster(cl), sim.RequestID(r.Context()))
}
