package main

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// rdsIdentifierClaims holds the identifiers a create request is provisioning
// under. Amazon RDS records the resource the moment it accepts the request, so
// a second create under the identifier while the first still provisions — an
// SDK retrying a request it stopped waiting for — finds the identifier taken,
// rather than provisioning a second engine on the resource's volume.
type rdsIdentifierClaims struct {
	mu     sync.Mutex
	held   map[string]bool
	stored func(id string) bool
}

// claim takes id for one create request unless a resource or another request
// holds it. The request releases the claim once it stored the resource or
// gave up.
func (c *rdsIdentifierClaims) claim(id string) (release func(), claimed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[id] || c.stored(id) {
		return nil, false
	}
	if c.held == nil {
		c.held = map[string]bool{}
	}
	c.held[id] = true
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.held, id)
	}, true
}

var (
	rdsInstanceIdentifiers = &rdsIdentifierClaims{stored: func(id string) bool { _, ok := rdsInstances.Get(id); return ok }}
	rdsClusterIdentifiers  = &rdsIdentifierClaims{stored: func(id string) bool { _, ok := rdsClusters.Get(id); return ok }}
)

func handleRDSCreate(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "DBInstanceIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	releaseIdentifier, claimed := rdsInstanceIdentifiers.claim(id)
	if !claimed {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			"DB instance already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	defer releaseIdentifier()
	engine := r.FormValue("Engine")
	port := rdsDefaultPort(engine)
	if p := atoiOrZero(r.FormValue("Port")); p > 0 {
		port = p
	}
	az := rdsRequestRegion(r) + "a"
	if v := r.FormValue("AvailabilityZone"); v != "" {
		az = v
	}
	engineVersion, problem := rdsResolveEngineVersion(engine, r.FormValue("EngineVersion"))
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterCombination", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	clusterID := r.FormValue("DBClusterIdentifier")
	var cluster RDSCluster
	if clusterID != "" {
		var ok bool
		cluster, ok = rdsClusters.Get(clusterID)
		if !ok {
			rdsErrorXML(w, "DBClusterNotFoundFault", fmt.Sprintf("DBCluster %s not found.", clusterID), http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
		if !rdsRequireClusterState(w, r, cluster, "available", "given a DB instance") {
			return
		}
	}
	if rdsIsAurora(cluster.Engine) {
		if engine != cluster.Engine {
			rdsErrorXML(w, "InvalidParameterCombination",
				fmt.Sprintf("The engine name requested for your DB instance (%s) doesn't match the engine name of your DB cluster (%s).", engine, cluster.Engine),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		inst := RDSInstance{
			DBInstanceIdentifier:            id,
			DbiResourceId:                   rdsResourceID(),
			DBInstanceClass:                 r.FormValue("DBInstanceClass"),
			Engine:                          cluster.Engine,
			EngineVersion:                   cluster.EngineVersion,
			DBInstanceStatus:                "available",
			MasterUsername:                  cluster.MasterUsername,
			DBName:                          cluster.DatabaseName,
			Port:                            cluster.Port,
			AvailabilityZone:                az,
			InstanceCreateTime:              time.Now().UTC().Format(time.RFC3339),
			Tags:                            parseAWSQueryTagMap(r, "Tags.Tag"),
			EnableIAMDatabaseAuthentication: cluster.EnableIAMDatabaseAuthentication,
			DBClusterIdentifier:             clusterID,
		}
		rdsPlaceInstance(r, &inst)
		groupName, code, message := rdsInstanceParameterGroup(r.FormValue("DBParameterGroupName"), inst.Engine, inst.EngineVersion)
		if code != "" {
			rdsErrorXML(w, code, message, rdsParameterGroupErrorStatus(code), sim.RequestID(r.Context()))
			return
		}
		inst.DBParameterGroupName = groupName
		window, problem := rdsRequestedMaintenanceWindow(r, cluster.PreferredBackupWindow)
		if problem != "" {
			rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		inst.PreferredMaintenanceWindow = window
		if code, message := rdsRequestedInstanceSettings(r, &inst); code != "" {
			rdsErrorXML(w, code, message, rdsInstanceSettingsErrorStatus(code), sim.RequestID(r.Context()))
			return
		}
		releaseCapacity, code, message := rdsTakeLaunchCapacity(r, &inst, r.FormValue("LicenseModel"))
		if code != "" {
			rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), sim.RequestID(r.Context()))
			return
		}
		defer releaseCapacity()
		releaseQuota, code, message := rdsClaimInstanceQuota(inst.region(), id)
		if code != "" {
			rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		defer releaseQuota()
		if err := rdsInstallAuroraInstanceEndpoint(&inst); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		rdsInstances.Put(id, inst)
		rdsXMLResponse(w, "CreateDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
		return
	}
	retention, window, problem := rdsInstanceBackupSettings(r, 1, rdsDefaultBackupWindow)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	paramGroup, code, message := rdsInstanceParameterGroup(r.FormValue("DBParameterGroupName"), engine, engineVersion)
	if code != "" {
		rdsErrorXML(w, code, message, rdsParameterGroupErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	maintenanceWindow, problem := rdsRequestedMaintenanceWindow(r, window)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	inst := RDSInstance{
		PreferredMaintenanceWindow:      maintenanceWindow,
		AutoMinorVersionUpgrade:         rdsRequestedBool(r, "AutoMinorVersionUpgrade"),
		DBParameterGroupName:            paramGroup,
		BackupRetentionPeriod:           retention,
		PreferredBackupWindow:           window,
		DBInstanceIdentifier:            id,
		DbiResourceId:                   rdsResourceID(),
		DBInstanceClass:                 r.FormValue("DBInstanceClass"),
		Engine:                          engine,
		EngineVersion:                   engineVersion,
		DBInstanceStatus:                "creating",
		MasterUsername:                  r.FormValue("MasterUsername"),
		DBName:                          r.FormValue("DBName"),
		AllocatedStorage:                atoiOrZero(r.FormValue("AllocatedStorage")),
		Port:                            port,
		AvailabilityZone:                az,
		InstanceCreateTime:              time.Now().UTC().Format(time.RFC3339),
		Tags:                            parseAWSQueryTagMap(r, "Tags.Tag"),
		EnableIAMDatabaseAuthentication: strings.EqualFold(r.FormValue("EnableIAMDatabaseAuthentication"), "true"),
		DeletionProtection:              strings.EqualFold(r.FormValue("DeletionProtection"), "true"),
		DBClusterIdentifier:             clusterID,
		BackupTarget:                    rdsRequestedBackupTarget(r),
	}
	rdsPlaceInstance(r, &inst)
	if code, message := rdsRequestedInstanceSettings(r, &inst); code != "" {
		rdsErrorXML(w, code, message, rdsInstanceSettingsErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	releaseCapacity, code, message := rdsTakeLaunchCapacity(r, &inst, r.FormValue("LicenseModel"))
	if code != "" {
		rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	defer releaseCapacity()
	releaseQuota, code, message := rdsClaimInstanceQuota(inst.region(), id)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	defer releaseQuota()
	if err := rdsInstallDataPlane(&inst, r.FormValue("MasterUserPassword")); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	rdsInstances.Put(id, inst)
	bg.Go(func() { rdsFinishInstanceBringUp(id) })
	rdsXMLResponse(w, "CreateDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSDescribe(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("DBInstanceIdentifier")
	wantedResourceID := rdsFilterValue(r, "dbi-resource-id")
	matched := false
	region := rdsRequestRegion(r)
	var b strings.Builder
	b.WriteString("<DBInstances>")
	for _, i := range rdsInstances.List() {
		if i.region() != region {
			continue
		}
		if wanted != "" && i.DBInstanceIdentifier != wanted && i.DbiResourceId != wanted {
			continue
		}
		if wantedResourceID != "" && i.DbiResourceId != wantedResourceID {
			continue
		}
		matched = true
		b.WriteString(renderRDSInstance(i))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBInstances>")
	rdsXMLResponse(w, "DescribeDBInstances", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModify(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	instance, ok := rdsInstanceIn(r, id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if rdsRefuseUnavailableInstance(w, r, instance) {
		return
	}
	applyNow := strings.EqualFold(r.FormValue("ApplyImmediately"), "true")
	requested := RDSPendingModifications{DBInstanceClass: r.FormValue("DBInstanceClass")}
	if value := r.FormValue("EngineVersion"); value != "" {
		version, problem := rdsResolveEngineVersion(instance.Engine, value)
		if problem == "" {
			problem = rdsCheckEngineUpgrade(instance.Engine, instance.EngineVersion, version,
				strings.EqualFold(r.FormValue("AllowMajorVersionUpgrade"), "true"))
		}
		if problem != "" {
			rdsErrorXML(w, "InvalidParameterCombination", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		requested.EngineVersion = version
	}
	movePort, code, message := rdsRequestedInstanceChanges(r, instance, &requested)
	if code != "" {
		rdsErrorXML(w, code, message, rdsInstanceSettingsErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	releaseCapacity, code, message := rdsRequestedCapacityReservationChange(r, instance, &requested, applyNow)
	if code != "" {
		rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	defer releaseCapacity()
	if newID := requested.DBInstanceIdentifier; newID != "" {
		releaseNewID, claimed := rdsInstanceIdentifiers.claim(newID)
		if !claimed {
			rdsErrorXML(w, "DBInstanceAlreadyExists", "DB instance already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		defer releaseNewID()
	}
	if autoUpgrade := rdsRequestedBool(r, "AutoMinorVersionUpgrade"); autoUpgrade != nil {
		instance.AutoMinorVersionUpgrade = autoUpgrade
	}
	if value := r.FormValue("DBParameterGroupName"); value != "" {
		version := instance.EngineVersion
		if instance.PendingEngineVersion != "" {
			version = instance.PendingEngineVersion
		}
		if requested.EngineVersion != "" {
			version = requested.EngineVersion
		}
		group, code, message := rdsInstanceParameterGroup(value, instance.Engine, version)
		if code != "" {
			rdsErrorXML(w, code, message, rdsParameterGroupErrorStatus(code), sim.RequestID(r.Context()))
			return
		}
		instance.DBParameterGroupName = group
	}
	if value := r.FormValue("EnableIAMDatabaseAuthentication"); value != "" {
		instance.EnableIAMDatabaseAuthentication = strings.EqualFold(value, "true")
	}
	if value := r.FormValue("DeletionProtection"); value != "" {
		instance.DeletionProtection = strings.EqualFold(value, "true")
	}
	retentionBefore := instance.BackupRetentionPeriod
	if r.FormValue("BackupRetentionPeriod") != "" || r.FormValue("PreferredBackupWindow") != "" {
		retention, window, problem := rdsInstanceBackupSettings(r, instance.BackupRetentionPeriod, instance.PreferredBackupWindow)
		if problem != "" {
			rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		instance.PreferredBackupWindow = window
		// Turning automated backups on or off waits for the maintenance
		// window; a change between two non-zero periods applies at once.
		if (retention == 0) != (instance.BackupRetentionPeriod == 0) {
			requested.BackupRetentionPeriod = &retention
		} else {
			instance.BackupRetentionPeriod = retention
		}
	}
	if value := r.FormValue("PreferredMaintenanceWindow"); value != "" {
		instance.PreferredMaintenanceWindow = strings.ToLower(value)
	}
	if r.FormValue("PreferredMaintenanceWindow") != "" || r.FormValue("PreferredBackupWindow") != "" {
		if problem := rdsCheckMaintenanceWindow(instance.PreferredMaintenanceWindow, instance.PreferredBackupWindow); problem != "" {
			rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
	}
	if applyNow {
		rdsApplyModifications(&instance, instance.PendingModifications.merge(requested))
		instance.PendingModifications = nil
	} else if !requested.empty() {
		merged := instance.PendingModifications.merge(requested)
		instance.PendingModifications = &merged
	}
	if movePort != 0 {
		// A port change restarts the engine whatever ApplyImmediately says.
		instance.Restart = rdsRestartFor(&instance)
		instance.Restart.Port = movePort
		if instance.DBInstanceStatus != "upgrading" {
			instance.DBInstanceStatus = "modifying"
		}
	}
	backupsChanged := retentionBefore != instance.BackupRetentionPeriod || r.FormValue("PreferredBackupWindow") != ""
	var newPassword *string
	if value := r.FormValue("MasterUserPassword"); value != "" {
		if rdsIsReadReplica(instance) && len(instance.MasterUserSecret) > 0 {
			rdsErrorXML(w, "InvalidParameterCombination",
				fmt.Sprintf("The master user password of read replica %s follows its source DB instance %s.", id, instance.ReadReplicaSource),
				http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		newPassword = &value
	}
	if err := rdsModifyDataPlaneAuthentication(&instance, newPassword); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	id = rdsStoreModifiedInstance(id, instance, requested.DBInstanceIdentifier)
	instance, _ = rdsInstances.Get(id)
	if newPassword != nil {
		rdsFollowSourceMasterPassword(instance)
	}
	rdsAfterModifications(id, instance, backupsChanged)
	rdsArmMaintenance(id)
	instance, _ = rdsInstances.Get(id)
	rdsXMLResponse(w, "ModifyDBInstance", renderRDSInstance(instance), sim.RequestID(r.Context()))
}

func handleRDSDelete(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstanceIn(r, id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if inst.DBInstanceStatus == "deleting" {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("Instance %s is already being deleted.", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if inst.DeletionProtection {
		rdsErrorXML(w, "InvalidParameterCombination",
			"Cannot delete protected DB Instance, please disable deletion protection and try again.",
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
	if finalSnapID != "" && inst.DBInstanceStatus == "creating" {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("Instance %s is currently creating - a final snapshot cannot be taken.", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if finalSnapID != "" {
		if _, exists := rdsSnapshots.Get(finalSnapID); exists {
			rdsErrorXML(w, "DBSnapshotAlreadyExists",
				fmt.Sprintf("DBSnapshot %q already exists", finalSnapID),
				http.StatusConflict, sim.RequestID(r.Context()))
			return
		}
		rdsSnapshots.Put(finalSnapID, RDSSnapshot{
			DBSnapshotIdentifier: finalSnapID,
			DBInstanceIdentifier: id,
			DbiResourceId:        inst.DbiResourceId,
			Engine:               inst.Engine,
			EngineVersion:        inst.EngineVersion,
			Status:               "creating",
			AllocatedStorage:     inst.AllocatedStorage,
			MasterUsername:       inst.MasterUsername,
			DBName:               inst.DBName,
			SnapshotCreateTime:   time.Now().UTC().Format(time.RFC3339),
			SnapshotType:         "manual",
			Port:                 inst.Port,
			ARN:                  rdsSnapshotARN(finalSnapID),
			MasterUserSecret:     append([]byte(nil), inst.MasterUserSecret...),
		})
	}
	seeding := false
	rdsInstances.Update(id, func(stored *RDSInstance) {
		seeding = stored.DBInstanceStatus == "creating" &&
			(stored.RestoreSourceVolume != "" || stored.ImportS3Bucket != "" || (rdsIsReadReplica(*stored) && len(stored.MasterUserSecret) > 0))
		stored.DBInstanceStatus = "deleting"
		stored.RetainAutomatedBackups = strings.EqualFold(r.FormValue("DeleteAutomatedBackups"), "false")
		inst = *stored
	})
	resourceID := inst.DbiResourceId
	switch {
	case seeding:
		// rdsFinishInstanceRestore or rdsProvisionReadReplica tears the
		// instance down once its seed lets go of the volume.
	case finalSnapID != "":
		// The final snapshot captures the instance's volume before the data
		// plane and the volume go away, and the deleting instance holds its
		// identifier until then.
		snapID := finalSnapID
		bg.Go(func() {
			rdsCaptureSnapshotData(snapID, id)
			rdsFinishInstanceDeletion(id, resourceID)
		})
	default:
		rdsFinishInstanceDeletion(id, resourceID)
	}
	rdsXMLResponse(w, "DeleteDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSReboot(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	inst, ok := rdsInstanceIn(r, id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	failover := rdsRequestedBool(r, "ForceFailover")
	if failover != nil && *failover && rdsRefuseFailover(w, r, inst) {
		return
	}
	if !rdsRequireInstanceState(w, r, inst, "available", "rebooted") {
		return
	}
	if failover != nil && *failover {
		rdsXMLResponse(w, "RebootDBInstance", renderRDSInstance(rdsBeginFailover(inst)), sim.RequestID(r.Context()))
		return
	}
	if len(inst.MasterUserSecret) > 0 {
		if err := rdsStopDataPlane(id, false); err != nil {
			rdsErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		_, password, decrypted := kmsDecryptBytes(inst.MasterUserSecret)
		if !decrypted {
			rdsErrorXML(w, "ProvisioningFailure", "RDS master-user credential could not be decrypted", http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		inst.EngineParameters = nil
		if err := rdsInstallDataPlane(&inst, string(password)); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		rdsInstances.Put(id, inst)
		rdsTakeFirstInstanceBackup(id)
	}
	rdsRecordInstanceEvent(inst, rdsEventInstanceRestarted)
	rdsXMLResponse(w, "RebootDBInstance", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

// rdsRequireStandaloneInstance answers InvalidDBClusterStateFault for a DB
// cluster member: Amazon RDS starts and stops a cluster's instances only
// through StartDBCluster and StopDBCluster.
func rdsRequireStandaloneInstance(w http.ResponseWriter, r *http.Request, instance RDSInstance, action string) bool {
	if instance.DBClusterIdentifier == "" {
		return true
	}
	rdsErrorXML(w, "InvalidDBClusterStateFault",
		fmt.Sprintf("DB instance %s is a member of DB cluster %s and cannot be %s on its own; use the DB cluster action instead.",
			instance.DBInstanceIdentifier, instance.DBClusterIdentifier, action),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return false
}

// rdsRefuseUnavailableInstance answers InvalidDBInstanceState for an instance
// that takes no such action until it is available: one Amazon RDS is still
// creating, starting, taking the first automated backup of, upgrading or
// promoting, or is deleting.
func rdsRefuseUnavailableInstance(w http.ResponseWriter, r *http.Request, instance RDSInstance) bool {
	switch {
	case instance.DBInstanceStatus == "deleting", instance.DBInstanceStatus == "upgrading", instance.DBInstanceStatus == "modifying",
		instance.DBInstanceStatus == "rebooting":
	case !rdsInstanceBringingUp(instance.DBInstanceStatus):
		return false
	}
	rdsErrorXML(w, "InvalidDBInstanceState",
		fmt.Sprintf("Instance %s is not in available state.", instance.DBInstanceIdentifier),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return true
}

// rdsRequireInstanceState answers InvalidDBInstanceState unless the instance
// is in the one state the lifecycle action runs from, as Amazon RDS does.
func rdsRequireInstanceState(w http.ResponseWriter, r *http.Request, instance RDSInstance, required, action string) bool {
	if instance.DBInstanceStatus == required {
		return true
	}
	rdsErrorXML(w, "InvalidDBInstanceState",
		fmt.Sprintf("Instance %s is not in %s state and cannot be %s.", instance.DBInstanceIdentifier, required, action),
		http.StatusBadRequest, sim.RequestID(r.Context()))
	return false
}

func handleRDSStartInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	if _, ok := rdsInstanceIn(r, id); !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	instance, _ := rdsInstances.Get(id)
	if !rdsRequireStandaloneInstance(w, r, instance, "started") {
		return
	}
	if !rdsRequireInstanceState(w, r, instance, "stopped", "started") {
		return
	}
	releaseCapacity, code, message := rdsTakeStartCapacity(r, &instance)
	if code != "" {
		rdsErrorXML(w, code, message, rdsCapacityErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	defer releaseCapacity()
	if err := rdsStartInstanceEngine(&instance); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	instance.DBInstanceStatus = "starting"
	rdsInstances.Put(id, instance)
	bg.Go(func() { rdsFinishInstanceBringUp(id) })
	rdsXMLResponse(w, "StartDBInstance", renderRDSInstance(instance), sim.RequestID(r.Context()))
}

func handleRDSStopInstance(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("DBInstanceIdentifier")
	instance, ok := rdsInstanceIn(r, id)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound", "DB instance not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if !rdsRequireStandaloneInstance(w, r, instance, "stopped") {
		return
	}
	if !rdsRequireInstanceState(w, r, instance, "available", "stopped") {
		return
	}
	if rdsIsReadReplica(instance) || len(rdsReadReplicasOf(instance)) > 0 {
		rdsErrorXML(w, "InvalidDBInstanceState",
			fmt.Sprintf("Instance %s cannot be stopped: a DB instance that has a read replica, or that is a read replica, can't be stopped.", id),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	// Amazon RDS answers stopping and lands stopped once the engine has shut
	// down, which is when the engine container here has stopped.
	detach := strings.EqualFold(r.FormValue("DetachFromDBCapacityReservation"), "true")
	rdsInstances.Update(id, func(i *RDSInstance) {
		i.DBInstanceStatus = "stopping"
		if detach {
			i.DBCapacityReservationIdentifier = ""
		}
	})
	updated, _ := rdsInstances.Get(id)
	bg.Go(func() { rdsFinishStop(id) })
	rdsXMLResponse(w, "StopDBInstance", renderRDSInstance(updated), sim.RequestID(r.Context()))
}
