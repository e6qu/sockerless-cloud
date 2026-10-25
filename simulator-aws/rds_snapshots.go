package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

func rdsSnapshotARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:snapshot:%s", awsRegion(), awsAccountID(), id)
}

func renderRDSSnapshot(s RDSSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBSnapshot>")
	fmt.Fprintf(&b, "<DBSnapshotIdentifier>%s</DBSnapshotIdentifier>", xmlEscape(s.DBSnapshotIdentifier))
	fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(s.DBInstanceIdentifier))
	fmt.Fprintf(&b, "<DbiResourceId>%s</DbiResourceId>", xmlEscape(s.DbiResourceId))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(s.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(s.EngineVersion))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(s.Status))
	fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", s.AllocatedStorage)
	fmt.Fprintf(&b, "<MasterUsername>%s</MasterUsername>", xmlEscape(s.MasterUsername))
	fmt.Fprintf(&b, "<SnapshotCreateTime>%s</SnapshotCreateTime>", xmlEscape(s.SnapshotCreateTime))
	fmt.Fprintf(&b, "<SnapshotType>%s</SnapshotType>", xmlEscape(s.SnapshotType))
	fmt.Fprintf(&b, "<Port>%d</Port>", s.Port)
	fmt.Fprintf(&b, "<VpcId>%s</VpcId>", xmlEscape(s.VpcId))
	fmt.Fprintf(&b, "<DBSnapshotArn>%s</DBSnapshotArn>", xmlEscape(s.ARN))
	if s.SourceDBSnapshotIdentifier != "" {
		fmt.Fprintf(&b, "<SourceDBSnapshotIdentifier>%s</SourceDBSnapshotIdentifier>", xmlEscape(s.SourceDBSnapshotIdentifier))
	}
	b.WriteString("</DBSnapshot>")
	return b.String()
}

func handleRDSCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	instID := r.FormValue("DBInstanceIdentifier")
	if snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBSnapshotIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	inst, ok := rdsInstanceIn(r, instID)
	if !ok {
		rdsErrorXML(w, "DBInstanceNotFound",
			fmt.Sprintf("DBInstance %q not found", instID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	if rdsRefuseUnavailableInstance(w, r, inst) {
		return
	}
	if _, exists := rdsSnapshots.Get(snapID); exists {
		rdsErrorXML(w, "DBSnapshotAlreadyExists",
			fmt.Sprintf("DBSnapshot %q already exists", snapID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	snap := RDSSnapshot{
		DBSnapshotIdentifier: snapID,
		DBInstanceIdentifier: instID,
		DbiResourceId:        inst.DbiResourceId,
		Engine:               inst.Engine,
		EngineVersion:        inst.EngineVersion,
		// The snapshot answers "creating" and settles asynchronously once
		// the instance's volume is captured — real work now backs the state
		// machine: the capture is copy-on-write where the engine's volume
		// store supports block cloning, and a full copy elsewhere, either way
		// a complete capture of the data.
		Status:             "creating",
		AllocatedStorage:   inst.AllocatedStorage,
		MasterUsername:     inst.MasterUsername,
		DBName:             inst.DBName,
		SnapshotCreateTime: time.Now().UTC().Format(time.RFC3339),
		SnapshotType:       "manual",
		Port:               inst.Port,
		ARN:                rdsSnapshotARN(snapID),
		Tags:               parseAWSQueryTagMap(r, "Tags.Tag"),
		// The master credential travels with the data, as it does in RDS:
		// the restored engine expects the credentials the data was written
		// under.
		MasterUserSecret: append([]byte(nil), inst.MasterUserSecret...),
	}
	rdsSnapshots.Put(snapID, snap)
	bg.Go(func() { rdsCaptureSnapshotData(snapID, instID) })
	rdsXMLResponse(w, "CreateDBSnapshot", renderRDSSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSDescribeSnapshots(w http.ResponseWriter, r *http.Request) {
	filterID := r.FormValue("DBSnapshotIdentifier")
	filterInst := r.FormValue("DBInstanceIdentifier")
	filterType := r.FormValue("SnapshotType")
	filterResource := r.FormValue("DbiResourceId")
	matched := false
	var b strings.Builder
	b.WriteString("<DBSnapshots>")
	for _, s := range rdsSnapshots.List() {
		if filterID != "" && s.DBSnapshotIdentifier != filterID {
			continue
		}
		if filterInst != "" && s.DBInstanceIdentifier != filterInst {
			continue
		}
		if filterType != "" && s.SnapshotType != filterType {
			continue
		}
		if filterResource != "" && s.DbiResourceId != filterResource {
			continue
		}
		matched = true
		b.WriteString(renderRDSSnapshot(s))
	}
	if filterID != "" && !matched {
		rdsErrorXML(w, "DBSnapshotNotFound",
			fmt.Sprintf("DBSnapshot %q not found", filterID),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</DBSnapshots>")
	rdsXMLResponse(w, "DescribeDBSnapshots", b.String(), sim.RequestID(r.Context()))
}

func handleRDSDescribeSnapshotAttributes(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	if snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBSnapshotIdentifier is required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	rdsXMLResponse(w, "DescribeDBSnapshotAttributes", renderRDSSnapshotAttributesResult(snap), sim.RequestID(r.Context()))
}

func handleRDSDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if snap.SnapshotType == "automated" {
		rdsErrorXML(w, "InvalidDBSnapshotState", "Only manual snapshots may be deleted.", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	rdsSnapshots.Delete(snap.DBSnapshotIdentifier)
	// The captured data goes with the record: keeping the volume would leak
	// storage the API says no longer exists. On the modeled tier no volume
	// was ever created, and removing a volume without an engine is refused
	// inside RemoveVolume, so the error is expected and dropped there.
	if sim.RequireContainerRuntime("deleting an RDS snapshot volume") == nil {
		_ = sim.RemoveVolume(rdsSnapshotVolume(snap.DBSnapshotIdentifier))
	}
	// Real RDS returns the snapshot with Status="deleted" in the
	// response (it's the final state machine transition before
	// removal). Match that.
	snap.Status = "deleted"
	rdsXMLResponse(w, "DeleteDBSnapshot", renderRDSSnapshot(snap), sim.RequestID(r.Context()))
}

func handleRDSRestoreFromSnapshot(w http.ResponseWriter, r *http.Request) {
	newInstID := r.FormValue("DBInstanceIdentifier")
	snapID := r.FormValue("DBSnapshotIdentifier")
	if newInstID == "" || snapID == "" {
		rdsErrorXML(w, "MissingParameter",
			"DBInstanceIdentifier and DBSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	releaseIdentifier, claimed := rdsInstanceIdentifiers.claim(newInstID)
	if !claimed {
		rdsErrorXML(w, "DBInstanceAlreadyExists",
			fmt.Sprintf("DBInstance %q already exists", newInstID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	defer releaseIdentifier()
	if snap.Status != "available" {
		rdsErrorXML(w, "InvalidDBSnapshotState",
			fmt.Sprintf("DBSnapshot %q is %s; it must be available to restore from", snapID, snap.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	retention, window, problem := rdsInstanceBackupSettings(r, 1, rdsDefaultBackupWindow)
	if problem != "" {
		rdsErrorXML(w, "InvalidParameterValue", problem, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engineVersion := snap.EngineVersion
	if requested := r.FormValue("EngineVersion"); requested != "" {
		resolved, unknown := rdsResolveEngineVersion(snap.Engine, requested)
		if unknown == "" {
			unknown = rdsCheckEngineUpgrade(snap.Engine, snap.EngineVersion, resolved, true)
		}
		if unknown != "" {
			rdsErrorXML(w, "InvalidParameterCombination", unknown, http.StatusBadRequest, sim.RequestID(r.Context()))
			return
		}
		engineVersion = resolved
	}
	paramGroup, code, message := rdsInstanceParameterGroup(r.FormValue("DBParameterGroupName"), snap.Engine, engineVersion)
	if code != "" {
		rdsErrorXML(w, code, message, rdsParameterGroupErrorStatus(code), sim.RequestID(r.Context()))
		return
	}
	inst := RDSInstance{
		PreferredMaintenanceWindow: rdsDefaultMaintenanceWindow(window),
		DBParameterGroupName:       paramGroup,
		BackupRetentionPeriod:      retention,
		PreferredBackupWindow:      window,
		DBInstanceIdentifier:       newInstID,
		DbiResourceId:              rdsResourceID(),
		DBInstanceClass:            r.FormValue("DBInstanceClass"),
		Engine:                     snap.Engine,
		EngineVersion:              engineVersion,
		DBInstanceStatus:           "creating",
		MasterUsername:             snap.MasterUsername,
		DBName:                     snap.DBName,
		AllocatedStorage:           snap.AllocatedStorage,
		AvailabilityZone:           rdsRequestRegion(r) + "a",
		InstanceCreateTime:         time.Now().UTC().Format(time.RFC3339),
		Region:                     rdsRequestRegion(r),
		ARN:                        rdsInstanceARNIn(rdsRequestRegion(r), newInstID),
		Tags:                       parseAWSQueryTagMap(r, "Tags.Tag"),
		BackupTarget:               rdsRequestedBackupTarget(r),
		// The engine starts with the credentials the captured data was
		// written under, exactly as a restored RDS instance does.
		MasterUserSecret: append([]byte(nil), snap.MasterUserSecret...),
	}
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
	releaseQuota, code, message := rdsClaimInstanceQuota(inst.region(), newInstID)
	if code != "" {
		rdsErrorXML(w, code, message, http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	defer releaseQuota()
	// The captured data is cloned into the new instance's volume before the
	// engine can first start, so the restored engine boots on the snapshot's
	// data. A snapshot taken on the modeled tier has no volume and seeds
	// nothing, leaving the restored instance as modeled as its source.
	if err := rdsCloneSnapshotIntoInstance(snap.DBSnapshotIdentifier, newInstID); err != nil {
		rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
		return
	}
	if len(inst.MasterUserSecret) > 0 {
		_, password, decrypted := kmsDecryptBytes(inst.MasterUserSecret)
		if !decrypted {
			rdsErrorXML(w, "ProvisioningFailure",
				"the snapshot's master-user credential could not be decrypted",
				http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
		if err := rdsInstallDataPlane(&inst, string(password)); err != nil {
			rdsErrorXML(w, "ProvisioningFailure", err.Error(), http.StatusInternalServerError, sim.RequestID(r.Context()))
			return
		}
	} else {
		// A snapshot from the modeled tier carries no credential and gets no
		// data plane, the same as its source had.
		inst.Endpoint = rdsInstanceEndpointIn(inst.region(), newInstID)
		inst.Port = rdsDefaultPort(snap.Engine)
	}
	rdsInstances.Put(newInstID, inst)
	bg.Go(func() { rdsFinishInstanceBringUp(newInstID) })
	rdsXMLResponse(w, "RestoreDBInstanceFromDBSnapshot", renderRDSInstance(inst), sim.RequestID(r.Context()))
}

func handleRDSCopySnapshot(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceDBSnapshotIdentifier")
	targetID := r.FormValue("TargetDBSnapshotIdentifier")
	if srcID == "" || targetID == "" {
		rdsErrorXML(w, "MissingParameter",
			"SourceDBSnapshotIdentifier and TargetDBSnapshotIdentifier are required",
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	src, ok := rdsSnapshots.Get(srcID)
	if !ok {
		src, ok = findRDSSnapshotByARN(srcID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", srcID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	if _, exists := rdsSnapshots.Get(targetID); exists {
		rdsErrorXML(w, "DBSnapshotAlreadyExists",
			fmt.Sprintf("DBSnapshot %q already exists", targetID),
			http.StatusConflict, sim.RequestID(r.Context()))
		return
	}
	// Real RDS refuses to copy a snapshot that is not available; copying one
	// mid-capture would race the source's own data write.
	if src.Status != "available" {
		rdsErrorXML(w, "InvalidDBSnapshotState",
			fmt.Sprintf("DBSnapshot %q is %s; it must be available to copy", srcID, src.Status),
			http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	tags := parseAWSQueryTagMap(r, "Tags.Tag")
	if r.FormValue("CopyTags") == "true" {
		tags = mergeTags(tags, src.Tags)
	}
	copySnap := RDSSnapshot{
		DBSnapshotIdentifier: targetID,
		DBInstanceIdentifier: src.DBInstanceIdentifier,
		DbiResourceId:        src.DbiResourceId,
		Engine:               src.Engine,
		EngineVersion:        src.EngineVersion,
		// The copy carries the DATA, not just the record: it answers
		// "creating" and settles once the source snapshot's volume is cloned
		// into its own, the same asynchronous machine CreateDBSnapshot runs.
		Status:             "creating",
		AllocatedStorage:   src.AllocatedStorage,
		MasterUsername:     src.MasterUsername,
		DBName:             src.DBName,
		SnapshotCreateTime: time.Now().UTC().Format(time.RFC3339),
		SnapshotType:       "manual",
		Port:               src.Port,
		VpcId:              src.VpcId,
		// The master credential travels with the data, exactly as on create:
		// an instance restored from the COPY needs the credentials the data
		// was written under just as much as one restored from the original.
		MasterUserSecret:           append([]byte(nil), src.MasterUserSecret...),
		SourceDBSnapshotIdentifier: src.ARN,
		ARN:                        rdsSnapshotARN(targetID),
		Tags:                       tags,
	}
	rdsSnapshots.Put(targetID, copySnap)
	srcSnapID := src.DBSnapshotIdentifier
	bg.Go(func() { rdsCopySnapshotData(targetID, srcSnapID) })
	rdsXMLResponse(w, "CopyDBSnapshot", renderRDSSnapshot(copySnap), sim.RequestID(r.Context()))
}

func parseRDSIndexedStringList(r *http.Request, prefix string) []string {
	var out []string
	for n := 1; n <= 200; n++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, n))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

func renderRDSSnapshotAttributesResult(snap RDSSnapshot) string {
	var b strings.Builder
	b.WriteString("<DBSnapshotAttributesResult>")
	fmt.Fprintf(&b, "<DBSnapshotIdentifier>%s</DBSnapshotIdentifier>", xmlEscape(snap.DBSnapshotIdentifier))
	b.WriteString("<DBSnapshotAttributes>")
	b.WriteString("<DBSnapshotAttribute><AttributeName>restore</AttributeName><AttributeValues>")
	for _, v := range snap.RestoreAttributeValues {
		fmt.Fprintf(&b, "<AttributeValue>%s</AttributeValue>", xmlEscape(v))
	}
	b.WriteString("</AttributeValues></DBSnapshotAttribute>")
	b.WriteString("</DBSnapshotAttributes>")
	b.WriteString("</DBSnapshotAttributesResult>")
	return b.String()
}

// handleRDSModifySnapshotAttribute adds/removes values for the "restore"
// snapshot attribute (the only attribute RDS supports). The updated
// attribute set is persisted so DescribeDBSnapshotAttributes reads back
// the change.
func handleRDSModifySnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("DBSnapshotIdentifier")
	snap, ok := rdsSnapshots.Get(snapID)
	if !ok {
		snap, ok = findRDSSnapshotByARN(snapID)
		if !ok {
			rdsErrorXML(w, "DBSnapshotNotFound",
				fmt.Sprintf("DBSnapshot %q not found", snapID),
				http.StatusNotFound, sim.RequestID(r.Context()))
			return
		}
	}
	attrName := r.FormValue("AttributeName")
	if attrName != "restore" {
		rdsErrorXML(w, "InvalidParameterValue",
			"AttributeName must be 'restore'", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	toAdd := parseRDSIndexedStringList(r, "ValuesToAdd.AttributeValue")
	toRemove := parseRDSIndexedStringList(r, "ValuesToRemove.AttributeValue")
	rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
		s.RestoreAttributeValues = applyAttributeValueDelta(s.RestoreAttributeValues, toAdd, toRemove)
	})
	updated, _ := rdsSnapshots.Get(snap.DBSnapshotIdentifier)
	rdsXMLResponse(w, "ModifyDBSnapshotAttribute", renderRDSSnapshotAttributesResult(updated), sim.RequestID(r.Context()))
}

func applyAttributeValueDelta(cur, add, remove []string) []string {
	set := map[string]bool{}
	for _, v := range cur {
		set[v] = true
	}
	for _, v := range add {
		set[v] = true
	}
	for _, v := range remove {
		delete(set, v)
	}
	var out []string
	// Preserve insertion order: existing values first (still present),
	// then newly added ones.
	for _, v := range cur {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	for _, v := range add {
		if set[v] {
			out = append(out, v)
			delete(set, v)
		}
	}
	return out
}
