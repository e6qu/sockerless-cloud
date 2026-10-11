package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// rdsStorageTypes are the storage types the CreateDBInstance and
// ModifyDBInstance references list.
var rdsStorageTypes = []string{"gp2", "gp3", "io1", "io2", "standard"}

// rdsStorage is an instance's storage as the storage members describe it.
type rdsStorage struct {
	Type       string
	Allocated  int
	Iops       int
	Throughput int
}

func rdsInstanceStorage(i RDSInstance) rdsStorage {
	return rdsStorage{Type: i.StorageType, Allocated: i.AllocatedStorage, Iops: i.Iops, Throughput: i.StorageThroughput}
}

func rdsProvisionedIopsStorage(storageType string) bool {
	return storageType == "io1" || storageType == "io2"
}

// rdsStorageSizeRange is the AllocatedStorage range, in GiB, the
// CreateDBInstance reference states for engine on storageType; supported is
// false for a storage type the engine does not offer, and limited false for
// an engine the reference states no range for.
func rdsStorageSizeRange(engine, storageType string) (minimum, maximum int, supported, limited bool) {
	generalPurpose := storageType == "gp2" || storageType == "gp3"
	provisioned := rdsProvisionedIopsStorage(storageType)
	magnetic := storageType == "standard"
	switch {
	case strings.HasPrefix(engine, "sqlserver-"):
		switch {
		case generalPurpose:
			return 20, 16384, true, true
		case provisioned:
			return 100, 16384, true, true
		case magnetic:
			return 20, 1024, true, true
		}
	case strings.HasPrefix(engine, "db2-"):
		switch {
		case storageType == "gp3":
			return 20, 65536, true, true
		case provisioned:
			return 100, 65536, true, true
		}
		return 0, 0, false, true
	case strings.HasPrefix(engine, "oracle-"):
		switch {
		case generalPurpose:
			return 20, 65536, true, true
		case provisioned:
			return 100, 65536, true, true
		case magnetic:
			return 10, 3072, true, true
		}
	case engine == "mariadb" || engine == "mysql" || engine == "postgres":
		switch {
		case generalPurpose:
			return 20, 65536, true, true
		case provisioned:
			return 100, 65536, true, true
		case magnetic:
			return 5, 3072, true, true
		}
	}
	return 0, 0, true, false
}

// rdsIopsRatioFloor is the smallest multiple of its storage amount an
// instance's Provisioned IOPS may be.
func rdsIopsRatioFloor(engine string) float64 {
	if strings.HasPrefix(engine, "sqlserver-") {
		return 1
	}
	return 0.5
}

// rdsCheckStorage states why storage breaks the rules the CreateDBInstance and
// ModifyDBInstance references state for engine, with the error code.
func rdsCheckStorage(engine string, storage rdsStorage) (code, message string) {
	if !slices.Contains(rdsStorageTypes, storage.Type) {
		return "StorageTypeNotSupported", fmt.Sprintf("Invalid storage type: %s. Valid values are gp2, gp3, io1, io2 and standard.", storage.Type)
	}
	minimum, maximum, supported, limited := rdsStorageSizeRange(engine, storage.Type)
	if !supported {
		return "StorageTypeNotSupported", fmt.Sprintf("Storage type %s isn't supported for engine %s.", storage.Type, engine)
	}
	switch {
	case rdsProvisionedIopsStorage(storage.Type) && storage.Iops == 0:
		return "InvalidParameterCombination", fmt.Sprintf("You must specify Iops for storage type %s.", storage.Type)
	case (storage.Type == "gp2" || storage.Type == "standard") && storage.Iops > 0:
		return "InvalidParameterCombination", fmt.Sprintf("Iops can't be specified for storage type %s.", storage.Type)
	case storage.Throughput > 0 && storage.Type != "gp3":
		return "InvalidParameterCombination", fmt.Sprintf("StorageThroughput applies only to the gp3 storage type, not %s.", storage.Type)
	case limited && storage.Allocated > 0 && (storage.Allocated < minimum || storage.Allocated > maximum):
		return "InvalidParameterCombination", fmt.Sprintf("Invalid storage size for engine name %s and storage type %s: %d", engine, storage.Type, storage.Allocated)
	}
	if storage.Iops > 0 && storage.Allocated > 0 {
		ratio := float64(storage.Iops) / float64(storage.Allocated)
		if floor := rdsIopsRatioFloor(engine); ratio < floor || ratio > 50 {
			return "InvalidParameterCombination", fmt.Sprintf(
				"The IOPS to storage ratio of %d IOPS to %d GiB must be from %g to 50.", storage.Iops, storage.Allocated, floor)
		}
	}
	return "", ""
}

// rdsRequestedStorage is a new instance's storage: what the request asks for
// over base, the storage it starts from, and without a storage type io1 for
// Provisioned IOPS and otherwise gp3.
func rdsRequestedStorage(r *http.Request, engine string, base rdsStorage) (rdsStorage, string, string) {
	storage := base
	if v := r.FormValue("StorageType"); v != "" {
		storage.Type = v
	}
	if v := r.FormValue("Iops"); v != "" {
		storage.Iops = atoiOrZero(v)
	}
	if v := r.FormValue("StorageThroughput"); v != "" {
		storage.Throughput = atoiOrZero(v)
	}
	if rdsIsAurora(engine) {
		if storage.Type != "" || storage.Iops != 0 || storage.Throughput != 0 {
			return rdsStorage{}, "InvalidParameterCombination", "The storage of an Amazon Aurora DB instance is managed by its DB cluster."
		}
		return rdsStorage{}, "", ""
	}
	if storage.Type == "" {
		storage.Type = "gp3"
		if storage.Iops > 0 {
			storage.Type = "io1"
		}
	}
	code, message := rdsCheckStorage(engine, storage)
	return storage, code, message
}

// rdsGrowsByTenth rounds a storage or IOPS increase up to 10% over the
// current value, as the ModifyDBInstance reference states for RDS for Db2,
// MariaDB, MySQL, Oracle and PostgreSQL.
func rdsGrowsByTenth(engine string, current, requested int) int {
	tenthRule := engine == "mariadb" || engine == "mysql" || engine == "postgres" ||
		strings.HasPrefix(engine, "oracle-") || strings.HasPrefix(engine, "db2-")
	if !tenthRule || current <= 0 || requested <= current {
		return requested
	}
	return max(requested, int(math.Ceil(float64(current)*1.1)))
}

// rdsRequestedStorageChanges reads a ModifyDBInstance's storage members over
// the instance's storage as its pending modifications leave it.
func rdsRequestedStorageChanges(r *http.Request, instance RDSInstance) (RDSPendingModifications, string, string) {
	typeValue, iopsValue := r.FormValue("StorageType"), r.FormValue("Iops")
	throughputValue, allocatedValue := r.FormValue("StorageThroughput"), r.FormValue("AllocatedStorage")
	var changes RDSPendingModifications
	if typeValue == "" && iopsValue == "" && throughputValue == "" && allocatedValue == "" {
		return changes, "", ""
	}
	if rdsIsAurora(instance.Engine) {
		if typeValue != "" {
			return changes, "StorageTypeNotSupported", fmt.Sprintf("Storage type %s can't be associated with Amazon Aurora DB instance %s.", typeValue, instance.DBInstanceIdentifier)
		}
		return changes, "InvalidParameterCombination", fmt.Sprintf("The storage of Amazon Aurora DB instance %s is managed by DB cluster %s.", instance.DBInstanceIdentifier, instance.DBClusterIdentifier)
	}
	current := rdsInstanceStorage(instance)
	if pending := instance.PendingModifications; pending != nil {
		probe := instance
		rdsApplyStorage(&probe, *pending)
		current = rdsInstanceStorage(probe)
	}
	target := current
	if typeValue != "" {
		target.Type = typeValue
		if typeValue != current.Type {
			changes.StorageType = typeValue
		}
	}
	if allocatedValue != "" {
		target.Allocated = rdsGrowsByTenth(instance.Engine, current.Allocated, atoiOrZero(allocatedValue))
		changes.AllocatedStorage = target.Allocated
	}
	if iopsValue != "" {
		iops := rdsGrowsByTenth(instance.Engine, current.Iops, atoiOrZero(iopsValue))
		target.Iops = iops
		changes.Iops = &iops
	}
	if throughputValue != "" {
		throughput := atoiOrZero(throughputValue)
		target.Throughput = throughput
		changes.StorageThroughput = &throughput
	} else if target.Type != "gp3" && current.Throughput > 0 {
		target.Throughput = 0
		changes.StorageThroughput = &target.Throughput
	}
	switch {
	case iopsValue != "" && target.Iops > current.Iops && allocatedValue == "":
		return changes, "InvalidParameterCombination", "When you increase the Provisioned IOPS, you must also specify AllocatedStorage."
	case allocatedValue != "" && target.Allocated > current.Allocated && current.Iops > 0 && iopsValue == "":
		return changes, "InvalidParameterCombination", "When you increase the allocated storage of a DB instance with Provisioned IOPS, you must also specify Iops."
	}
	code, message := rdsCheckStorage(instance.Engine, target)
	return changes, code, message
}

// rdsApplyStorage applies the storage members of changes to instance.
func rdsApplyStorage(instance *RDSInstance, changes RDSPendingModifications) {
	if changes.AllocatedStorage != 0 {
		instance.AllocatedStorage = changes.AllocatedStorage
	}
	if changes.StorageType != "" {
		instance.StorageType = changes.StorageType
	}
	if changes.Iops != nil {
		instance.Iops = *changes.Iops
	}
	if changes.StorageThroughput != nil {
		instance.StorageThroughput = *changes.StorageThroughput
	}
}

// rdsOptionGroupMajorVersion is the major engine version an option group of
// engine at version is for.
func rdsOptionGroupMajorVersion(engine, version string) string {
	if row, ok := rdsFindEngineVersion(engine, version); ok {
		return row.MajorVersion
	}
	parts := strings.Split(version, ".")
	if strings.HasPrefix(engine, "sqlserver-") && len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return parts[0]
}

// rdsDefaultOptionGroupName is the default option group of engine at
// version, default:<engine>-<major version with dashes>.
func rdsDefaultOptionGroupName(engine, version string) string {
	return "default:" + engine + "-" + strings.ReplaceAll(rdsOptionGroupMajorVersion(engine, version), ".", "-")
}

// rdsEnsureDefaultOptionGroup creates an engine version's default option
// group the first time an instance uses it, as Amazon RDS does in each
// account.
func rdsEnsureDefaultOptionGroup(engine, version string) string {
	name := rdsDefaultOptionGroupName(engine, version)
	if _, ok := rdsOptionGroups.Get(name); !ok {
		major := rdsOptionGroupMajorVersion(engine, version)
		rdsOptionGroups.Put(name, RDSOptionGroup{
			OptionGroupName:                       name,
			OptionGroupDescription:                "Default option group for " + engine + " " + major,
			EngineName:                            engine,
			MajorEngineVersion:                    major,
			AllowsVpcAndNonVpcInstanceMemberships: true,
			ARN:                                   rdsOptionGroupARN(name),
		})
	}
	return name
}

// rdsInstanceOptionGroup resolves the option group a request names for an
// instance of engine at version: one that exists and is for that engine and
// major version.
func rdsInstanceOptionGroup(requested, engine, version string) (string, string, string) {
	if requested == rdsDefaultOptionGroupName(engine, version) {
		return rdsEnsureDefaultOptionGroup(engine, version), "", ""
	}
	group, ok := rdsOptionGroups.Get(requested)
	if !ok {
		return "", "OptionGroupNotFoundFault", fmt.Sprintf("Option group %s not found.", requested)
	}
	major := rdsOptionGroupMajorVersion(engine, version)
	if group.EngineName != engine || group.MajorEngineVersion != major {
		return "", "InvalidParameterCombination", fmt.Sprintf(
			"Option group %s is for %s %s and can't be associated with a %s %s DB instance.",
			requested, group.EngineName, group.MajorEngineVersion, engine, major)
	}
	return requested, "", ""
}

// rdsAssignInstanceDefaults gives an instance record what Amazon RDS gives an
// instance created without it: gp3 storage (io1 with Provisioned IOPS), its
// engine version's default option group, and the account's CA for new
// launches. An Aurora instance's storage and options are its cluster's.
func rdsAssignInstanceDefaults(instance *RDSInstance) {
	if instance.CACertificateIdentifier == "" {
		instance.CACertificateIdentifier = rdsDefaultCertificateForNewLaunches()
	}
	if rdsIsAurora(instance.Engine) {
		return
	}
	if instance.StorageType == "" {
		instance.StorageType = "gp3"
		if instance.Iops > 0 {
			instance.StorageType = "io1"
		}
	}
	if instance.OptionGroupName == "" && instance.Engine != "" {
		instance.OptionGroupName = rdsEnsureDefaultOptionGroup(instance.Engine, instance.EngineVersion)
	}
}

// rdsRequestedInstanceSettings reads the members a request that creates an
// instance sets over what the instance starts from: MultiAZ, the storage
// members, the DB subnet group, the option group and the CA certificate.
func rdsRequestedInstanceSettings(r *http.Request, instance *RDSInstance) (string, string) {
	if multiAZ := rdsRequestedBool(r, "MultiAZ"); multiAZ != nil {
		if *multiAZ && r.FormValue("AvailabilityZone") != "" {
			return "InvalidParameterCombination", "Requesting a specific availability zone is not valid for Multi-AZ instances."
		}
		if *multiAZ && rdsIsAurora(instance.Engine) {
			return "InvalidParameterCombination", "The Availability Zones of an Amazon Aurora DB instance are managed by its DB cluster."
		}
		instance.MultiAZ = *multiAZ
	}
	storage, code, message := rdsRequestedStorage(r, instance.Engine, rdsInstanceStorage(*instance))
	if code != "" {
		return code, message
	}
	instance.StorageType, instance.Iops, instance.StorageThroughput = storage.Type, storage.Iops, storage.Throughput
	if name := r.FormValue("DBSubnetGroupName"); name != "" {
		if _, ok := rdsSubnetGroups.Get(name); !ok {
			return "DBSubnetGroupNotFoundFault", fmt.Sprintf("DBSubnetGroup %q not found", name)
		}
		instance.DBSubnetGroupName = name
	}
	if name := r.FormValue("OptionGroupName"); name != "" {
		if rdsIsAurora(instance.Engine) {
			return "InvalidParameterCombination", "Option groups don't apply to Amazon Aurora DB instances."
		}
		group, code, message := rdsInstanceOptionGroup(name, instance.Engine, instance.EngineVersion)
		if code != "" {
			return code, message
		}
		instance.OptionGroupName = group
	}
	if ca := r.FormValue("CACertificateIdentifier"); ca != "" {
		if !rdsCheckCertificate(ca) {
			return "CertificateNotFound", fmt.Sprintf("Certificate %q not found", ca)
		}
		instance.CACertificateIdentifier = ca
	}
	rdsAssignInstanceDefaults(instance)
	return "", ""
}

// rdsValidInstanceIdentifier reports whether id holds to the constraints the
// ModifyDBInstance reference states for NewDBInstanceIdentifier.
func rdsValidInstanceIdentifier(id string) bool {
	if len(id) == 0 || len(id) > 63 || id[0] < 'a' || id[0] > 'z' || strings.HasSuffix(id, "-") || strings.Contains(id, "--") {
		return false
	}
	for _, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// rdsSQLServerReservedPort reports whether the ModifyDBInstance reference
// states RDS for Microsoft SQL Server cannot listen on port.
func rdsSQLServerReservedPort(port int) bool {
	switch port {
	case 1234, 1434, 3260, 3343, 3389, 47001:
		return true
	}
	return port >= 49152 && port <= 49156
}

// rdsRequestedInstanceChanges reads the ModifyDBInstance members this file
// models into changes, and answers the error Amazon RDS answers for a value
// it refuses. port is the DBPortNumber the endpoint moves to, 0 for none.
func rdsRequestedInstanceChanges(r *http.Request, instance RDSInstance, changes *RDSPendingModifications) (port int, code, message string) {
	storage, code, message := rdsRequestedStorageChanges(r, instance)
	if code != "" {
		return 0, code, message
	}
	changes.AllocatedStorage, changes.StorageType = storage.AllocatedStorage, storage.StorageType
	changes.Iops, changes.StorageThroughput = storage.Iops, storage.StorageThroughput
	if multiAZ := rdsRequestedBool(r, "MultiAZ"); multiAZ != nil {
		if *multiAZ && rdsIsAurora(instance.Engine) {
			return 0, "InvalidParameterCombination", "The Availability Zones of an Amazon Aurora DB instance are managed by its DB cluster."
		}
		changes.MultiAZ = multiAZ
	}
	if name := r.FormValue("DBSubnetGroupName"); name != "" {
		if _, ok := rdsSubnetGroups.Get(name); !ok {
			return 0, "DBSubnetGroupNotFoundFault", fmt.Sprintf("DBSubnetGroup %q not found", name)
		}
		changes.DBSubnetGroupName = name
	}
	if name := r.FormValue("OptionGroupName"); name != "" {
		if rdsIsAurora(instance.Engine) {
			return 0, "InvalidParameterCombination", "Option groups don't apply to Amazon Aurora DB instances."
		}
		version := instance.EngineVersion
		if changes.EngineVersion != "" {
			version = changes.EngineVersion
		}
		group, code, message := rdsInstanceOptionGroup(name, instance.Engine, version)
		if code != "" {
			return 0, code, message
		}
		changes.OptionGroupName = group
	}
	if ca := r.FormValue("CACertificateIdentifier"); ca != "" {
		if !rdsCheckCertificate(ca) {
			return 0, "CertificateNotFound", fmt.Sprintf("Certificate %q not found", ca)
		}
		changes.CACertificateIdentifier = ca
		if restart := rdsRequestedBool(r, "CertificateRotationRestart"); restart != nil && !*restart {
			changes.KeepCertificateUntilRestart = true
		}
	}
	if value := r.FormValue("NewDBInstanceIdentifier"); value != "" {
		newID := strings.ToLower(value)
		if !rdsValidInstanceIdentifier(newID) {
			return 0, "InvalidParameterValue", "The parameter NewDBInstanceIdentifier is not a valid identifier. Identifiers must begin with a letter; must contain only ASCII letters, digits, and hyphens; and must not end with a hyphen or contain two consecutive hyphens."
		}
		if _, taken := rdsInstances.Get(newID); taken && newID != instance.DBInstanceIdentifier {
			return 0, "DBInstanceAlreadyExists", "DB instance already exists"
		}
		if newID != instance.DBInstanceIdentifier {
			changes.DBInstanceIdentifier = newID
		}
	}
	if value := r.FormValue("DBPortNumber"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1150 || port > 65535 || (strings.HasPrefix(instance.Engine, "sqlserver-") && rdsSQLServerReservedPort(port)) {
			return 0, "InvalidParameterValue", fmt.Sprintf("Invalid port %s. The port must be between 1150 and 65535.", value)
		}
		if rdsIsAurora(instance.Engine) && instance.DBClusterIdentifier != "" {
			return 0, "InvalidParameterCombination", fmt.Sprintf(
				"The port of Amazon Aurora DB instance %s is managed by DB cluster %s.", instance.DBInstanceIdentifier, instance.DBClusterIdentifier)
		}
		if port != instance.Port {
			return port, "", ""
		}
	}
	return 0, "", ""
}

// rdsInstanceSettingsErrorStatus is the HTTP status of an error code the
// instance settings answer.
func rdsInstanceSettingsErrorStatus(code string) int {
	if strings.HasSuffix(code, "NotFound") || strings.HasSuffix(code, "NotFoundFault") {
		return http.StatusNotFound
	}
	return rdsParameterGroupErrorStatus(code)
}

// rdsInstanceEndpointServed reports whether the simulator serves the
// endpoint of the instance running as runningID: its own engine, or for an
// Aurora member its cluster's.
func rdsInstanceEndpointServed(instance RDSInstance, runningID string) bool {
	if rdsIsAurora(instance.Engine) && instance.DBClusterIdentifier != "" {
		_, ok := rdsLoadAuroraDataPlane(instance.DBClusterIdentifier)
		return ok
	}
	_, ok := rdsLoadDataPlane(runningID)
	return ok
}

// rdsRenameInstance moves a modifying instance's record to its new
// identifier, and its ARN and endpoint name follow; the restart then moves
// its engine and data. Read replicas, a source's replica list, blue/green
// deployments and custom cluster endpoints that name the instance name it by
// its new identifier.
func rdsRenameInstance(instance *RDSInstance, newID string) {
	oldID := instance.DBInstanceIdentifier
	oldARN := instance.ARN
	instance.DBInstanceIdentifier = newID
	instance.ARN = rdsInstanceARN(newID)
	instance.Endpoint = fmt.Sprintf("%s.%s.rds.amazonaws.com", newID, awsRegion())
	rdsInstances.Put(newID, *instance)
	rdsInstances.Delete(oldID)
	rename := func(ids []string) []string {
		out := slices.Clone(ids)
		for i, id := range out {
			if id == oldID {
				out[i] = newID
			}
		}
		return out
	}
	for _, replicaID := range instance.ReadReplicas {
		rdsInstances.Update(replicaID, func(stored *RDSInstance) {
			if stored.ReadReplicaSource == oldID {
				stored.ReadReplicaSource = newID
			}
		})
	}
	if instance.ReadReplicaSource != "" {
		rdsInstances.Update(instance.ReadReplicaSource, func(stored *RDSInstance) { stored.ReadReplicas = rename(stored.ReadReplicas) })
	}
	for _, d := range rdsBlueGreenDeployments.List() {
		if d.Source == oldARN || d.Target == oldARN {
			rdsBlueGreenDeployments.Update(d.BlueGreenDeploymentIdentifier, func(stored *RDSBlueGreenDeployment) {
				if stored.Source == oldARN {
					stored.Source = instance.ARN
				}
				if stored.Target == oldARN {
					stored.Target = instance.ARN
				}
			})
		}
	}
	for _, e := range rdsClusterEndpoints.List() {
		if slices.Contains(e.StaticMembers, oldID) || slices.Contains(e.ExcludedMembers, oldID) {
			rdsClusterEndpoints.Update(e.DBClusterEndpointIdentifier, func(stored *RDSClusterEndpoint) {
				stored.StaticMembers, stored.ExcludedMembers = rename(stored.StaticMembers), rename(stored.ExcludedMembers)
			})
		}
	}
}

// rdsMoveInstanceVolume moves a stopped engine's data from the old
// identifier's volume to the new one's.
func rdsMoveInstanceVolume(oldID, newID string) error {
	if sim.RequireContainerRuntime("renaming an Amazon RDS DB instance") != nil {
		return nil
	}
	release := rdsDataPlaneStops.Lock(oldID)
	defer release()
	rdsRemoveEngineContainers("Amazon RDS "+oldID, map[string]string{"sockerless-rds-instance": oldID})
	if !sim.VolumeExists(rdsInstanceVolume(oldID)) {
		return nil
	}
	if _, err := sim.SnapshotVolume(context.Background(), rdsInstanceVolume(oldID), rdsInstanceVolume(newID)); err != nil {
		sim.RemoveVolumeSettled(rdsInstanceVolume(newID), "rds")
		return fmt.Errorf("move the data of DB instance %s to %s: %w", oldID, newID, err)
	}
	sim.RemoveVolumeSettled(rdsInstanceVolume(oldID), "rds")
	return nil
}

// renderRDSInstanceSettings renders the DBInstance members of MultiAZ, the
// storage, the DB subnet group, the option group memberships and the CA
// certificate.
func renderRDSInstanceSettings(i RDSInstance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<MultiAZ>%t</MultiAZ>", i.MultiAZ)
	if i.StorageType != "" {
		fmt.Fprintf(&b, "<StorageType>%s</StorageType>", xmlEscape(i.StorageType))
	}
	if i.Iops > 0 {
		fmt.Fprintf(&b, "<Iops>%d</Iops>", i.Iops)
	}
	if i.StorageThroughput > 0 {
		fmt.Fprintf(&b, "<StorageThroughput>%d</StorageThroughput>", i.StorageThroughput)
	}
	if i.DBSubnetGroupName != "" {
		if group, ok := rdsSubnetGroups.Get(i.DBSubnetGroupName); ok {
			b.WriteString(renderRDSSubnetGroup(group))
		}
	}
	pending := ""
	if i.PendingModifications != nil {
		pending = i.PendingModifications.OptionGroupName
	}
	if i.OptionGroupName != "" || pending != "" {
		b.WriteString("<OptionGroupMemberships>")
		membership := func(name, status string) {
			fmt.Fprintf(&b, "<OptionGroupMembership><OptionGroupName>%s</OptionGroupName><Status>%s</Status></OptionGroupMembership>",
				xmlEscape(name), status)
		}
		if pending == "" || pending == i.OptionGroupName {
			membership(i.OptionGroupName, "in-sync")
		} else {
			if i.OptionGroupName != "" {
				membership(i.OptionGroupName, "pending-maintenance-removal")
			}
			membership(pending, "pending-maintenance-apply")
		}
		b.WriteString("</OptionGroupMemberships>")
	}
	if i.CACertificateIdentifier != "" {
		fmt.Fprintf(&b, "<CACertificateIdentifier>%s</CACertificateIdentifier>", xmlEscape(i.CACertificateIdentifier))
	}
	if i.ServerCertificateValidTill != "" {
		fmt.Fprintf(&b, "<CertificateDetails><CAIdentifier>%s</CAIdentifier><ValidTill>%s</ValidTill></CertificateDetails>",
			xmlEscape(i.CACertificateIdentifier), xmlEscape(i.ServerCertificateValidTill))
	}
	return b.String()
}

// rdsInstancesUsing lists the instances match picks.
func rdsInstancesUsing(match func(RDSInstance) bool) []string {
	var ids []string
	for _, instance := range rdsInstances.List() {
		if match(instance) {
			ids = append(ids, instance.DBInstanceIdentifier)
		}
	}
	return ids
}

// rdsRecoverInstanceSettings gives a record an earlier simulator kept without
// the storage type, option group and CA certificate the defaults Amazon RDS
// gives, and resumes a restart a previous process left modifying.
func rdsRecoverInstanceSettings() {
	for _, instance := range rdsInstances.List() {
		probe := instance
		rdsAssignInstanceDefaults(&probe)
		if probe.StorageType != instance.StorageType || probe.OptionGroupName != instance.OptionGroupName ||
			probe.CACertificateIdentifier != instance.CACertificateIdentifier {
			rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
				stored.StorageType, stored.OptionGroupName = probe.StorageType, probe.OptionGroupName
				stored.CACertificateIdentifier = probe.CACertificateIdentifier
			})
		}
		if instance.DBInstanceStatus == "modifying" && instance.Restart != nil {
			id, resourceID := instance.DBInstanceIdentifier, instance.DbiResourceId
			log.Printf("Amazon RDS %s: resume the restart with the modified settings", id)
			bg.Go(func() { rdsRestartModifiedInstance(id, resourceID) })
		}
	}
}
