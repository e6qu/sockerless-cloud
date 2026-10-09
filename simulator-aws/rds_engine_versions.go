package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// rdsEngineVersion is a DB engine version the simulator offers, with the
// image of the engine's own release of that version, which every DB
// instance on the version runs.
type rdsEngineVersion struct {
	Engine        string
	EngineVersion string
	MajorVersion  string
	Family        string
	Image         string
	// Default marks the version CreateDBInstance picks when the request names
	// none, and the one a major version alone resolves to is the newest.
	Default bool
}

// rdsEngineVersions lists the RDS for PostgreSQL, RDS for MySQL and RDS for
// MariaDB versions the simulator runs, newest first within each engine. The
// Aurora rows run the community release their major version is compatible
// with.
var rdsEngineVersions = []rdsEngineVersion{
	{"postgres", "17.11", "17", "postgres17", "public.ecr.aws/docker/library/postgres:17.11-alpine", false},
	{"postgres", "16.15", "16", "postgres16", "public.ecr.aws/docker/library/postgres:16.15-alpine", true},
	{"postgres", "16.14", "16", "postgres16", "public.ecr.aws/docker/library/postgres:16.14-alpine", false},
	{"mysql", "8.0.46", "8.0", "mysql8.0", "public.ecr.aws/docker/library/mysql:8.0.46", true},
	{"mariadb", "11.4.13", "11.4", "mariadb11.4", "public.ecr.aws/docker/library/mariadb:11.4.13", true},
	{"aurora-postgresql", "16.6", "16", "aurora-postgresql16", "public.ecr.aws/docker/library/postgres:16.15-alpine", true},
	{"aurora-mysql", "8.0.mysql_aurora.3.07.0", "8.0", "aurora-mysql8.0", "public.ecr.aws/docker/library/mysql:8.0.46", true},
}

// rdsVersionedEngine reports whether the simulator runs engine versions of
// engine one image each and refuses a version it does not offer. Aurora
// clusters run the one image of their row whatever version they record.
func rdsVersionedEngine(engine string) bool {
	switch engine {
	case "postgres", "mysql", "mariadb":
		return true
	}
	return false
}

func rdsDefaultEngineVersion(engine string) string {
	for _, row := range rdsEngineVersions {
		if row.Engine == engine && row.Default {
			return row.EngineVersion
		}
	}
	switch engine {
	case "oracle-se2", "oracle-ee":
		return "19.0.0.0.ru-2024-10.rur-2024-10.r1"
	case "sqlserver-ex", "sqlserver-web", "sqlserver-se", "sqlserver-ee":
		return "16.00.4150.1.v1"
	}
	return ""
}

// rdsFindEngineVersion resolves version of engine as CreateDBInstance does: an
// exact version, or a major version alone, which names that major's newest
// version.
func rdsFindEngineVersion(engine, version string) (rdsEngineVersion, bool) {
	for _, row := range rdsEngineVersions {
		if row.Engine == engine && (row.EngineVersion == version || row.MajorVersion == version) {
			return row, true
		}
	}
	return rdsEngineVersion{}, false
}

// rdsResolveEngineVersion is the version a request for engine runs: the
// default when the request names none, and otherwise the version it names,
// which must be one the simulator offers.
func rdsResolveEngineVersion(engine, requested string) (string, string) {
	if requested == "" {
		return rdsDefaultEngineVersion(engine), ""
	}
	if !rdsVersionedEngine(engine) {
		return requested, ""
	}
	row, ok := rdsFindEngineVersion(engine, requested)
	if !ok {
		return "", fmt.Sprintf("Cannot find version %s for %s", requested, engine)
	}
	return row.EngineVersion, ""
}

// rdsCheckEngineUpgrade answers why an instance on from cannot move to to, or
// "" when it can: Amazon RDS upgrades and never downgrades, and a major
// version upgrade needs AllowMajorVersionUpgrade.
func rdsCheckEngineUpgrade(engine, from, to string, allowMajor bool) string {
	if !rdsVersionedEngine(engine) || from == to {
		return ""
	}
	current, currentOK := rdsFindEngineVersion(engine, from)
	target, targetOK := rdsFindEngineVersion(engine, to)
	if !currentOK || !targetOK {
		return fmt.Sprintf("Cannot find version %s for %s", to, engine)
	}
	if rdsCompareVersions(target.EngineVersion, current.EngineVersion) < 0 {
		return fmt.Sprintf("Cannot upgrade %s from %s to %s", engine, current.EngineVersion, target.EngineVersion)
	}
	if target.MajorVersion != current.MajorVersion {
		if !allowMajor {
			return "The AllowMajorVersionUpgrade flag must be present when upgrading to a new major version."
		}
		if engine == "postgres" {
			return fmt.Sprintf("Cannot upgrade %s from %s to %s: the simulator runs no PostgreSQL major version upgrade.",
				engine, current.EngineVersion, target.EngineVersion)
		}
	}
	return ""
}

// rdsCompareVersions orders dotted engine versions component by component.
func rdsCompareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(left) || i < len(right); i++ {
		var x, y int
		if i < len(left) {
			x, _ = strconv.Atoi(left[i])
		}
		if i < len(right) {
			y, _ = strconv.Atoi(right[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// rdsUpgradeTargets are the versions an instance on row can upgrade to.
func rdsUpgradeTargets(row rdsEngineVersion) []rdsEngineVersion {
	var targets []rdsEngineVersion
	for _, candidate := range rdsEngineVersions {
		if candidate.Engine != row.Engine || rdsCompareVersions(candidate.EngineVersion, row.EngineVersion) <= 0 {
			continue
		}
		if rdsCheckEngineUpgrade(row.Engine, row.EngineVersion, candidate.EngineVersion, true) == "" {
			targets = append(targets, candidate)
		}
	}
	return targets
}

// rdsEngine is the engine image a DB instance or Aurora cluster on engine and
// version runs.
func rdsEngine(engine, version string) (dbengine.Engine, bool) {
	var preset dbengine.Engine
	switch strings.ToLower(engine) {
	case "postgres", "aurora-postgresql":
		preset = dbengine.Postgres16
	case "mysql", "aurora-mysql":
		preset = dbengine.MySQL80
	case "mariadb":
		preset = dbengine.MariaDB114
	default:
		return dbengine.Engine{}, false
	}
	engine = strings.ToLower(engine)
	if !rdsVersionedEngine(engine) {
		version = rdsDefaultEngineVersion(engine)
	}
	row, ok := rdsFindEngineVersion(engine, version)
	if !ok {
		return dbengine.Engine{}, false
	}
	return preset.WithImage(row.Image), true
}

// rdsParameterGroupFamily is the DB parameter group family of engine at
// version.
func rdsParameterGroupFamily(engine, version string) string {
	if row, ok := rdsFindEngineVersion(engine, version); ok {
		return row.Family
	}
	major, _, _ := strings.Cut(version, ".")
	switch {
	case strings.HasPrefix(engine, "oracle-"):
		return engine + "-" + major
	case strings.HasPrefix(engine, "sqlserver-"):
		parts := strings.SplitN(version, ".", 3)
		if len(parts) >= 2 {
			return engine + "-" + strings.TrimLeft(parts[0], "0") + "." + strings.TrimLeft(parts[1], "0") + "0"
		}
	}
	for _, row := range rdsEngineVersions {
		if row.Engine == engine && row.Default {
			return row.Family
		}
	}
	return engine
}

// rdsDefaultParameterGroupName names the default DB parameter group Amazon RDS
// associates with an instance created without one.
func rdsDefaultParameterGroupName(engine, version string) string {
	return "default." + rdsParameterGroupFamily(engine, version)
}

// rdsEnsureDefaultParameterGroup creates the default DB parameter group of a
// family the first time an instance uses it, as Amazon RDS does in each
// account.
func rdsEnsureDefaultParameterGroup(name string) {
	family, isDefault := strings.CutPrefix(name, "default.")
	if !isDefault {
		return
	}
	if _, ok := rdsParamGroups.Get(name); ok {
		return
	}
	rdsParamGroups.Put(name, RDSParamGroup{
		DBParameterGroupName:   name,
		DBParameterGroupFamily: family,
		Description:            "Default parameter group for " + family,
		ARN:                    rdsParamGroupARN(name),
	})
}

// rdsInstanceParameterGroup resolves the DB parameter group an instance on
// engine and version is created with: the one the request names, which must
// exist and belong to the version's family, or the family's default group.
func rdsInstanceParameterGroup(requested, engine, version string) (name, code, message string) {
	family := rdsParameterGroupFamily(engine, version)
	if requested == "" {
		name = rdsDefaultParameterGroupName(engine, version)
		rdsEnsureDefaultParameterGroup(name)
		return name, "", ""
	}
	if !rdsResolveParameterGroup(requested) {
		return "", "DBParameterGroupNotFound", fmt.Sprintf("DBParameterGroup not found: %s", requested)
	}
	groupFamily := strings.TrimPrefix(requested, "default.")
	if group, ok := rdsParamGroups.Get(requested); ok {
		groupFamily = group.DBParameterGroupFamily
	}
	if rdsVersionedEngine(engine) && groupFamily != family {
		return "", "InvalidParameterCombination", fmt.Sprintf(
			"The parameter group %s with DBParameterGroupFamily %s can't be used for this instance. Use a parameter group with DBParameterGroupFamily %s.",
			requested, groupFamily, family)
	}
	rdsEnsureDefaultParameterGroup(requested)
	return requested, "", ""
}

func handleRDSDescribeEngineVersions(w http.ResponseWriter, r *http.Request) {
	wantEngine := r.FormValue("Engine")
	wantVersion := r.FormValue("EngineVersion")
	wantFamily := r.FormValue("DBParameterGroupFamily")
	defaultOnly := strings.EqualFold(r.FormValue("DefaultOnly"), "true")
	var b strings.Builder
	b.WriteString("<DBEngineVersions>")
	for _, row := range rdsEngineVersions {
		if wantEngine != "" && row.Engine != wantEngine {
			continue
		}
		if wantVersion != "" && row.EngineVersion != wantVersion && row.MajorVersion != wantVersion {
			continue
		}
		if wantFamily != "" && row.Family != wantFamily {
			continue
		}
		if defaultOnly && ((wantVersion == "" && !row.Default) || (wantVersion != "" && row != rdsMajorDefault(row))) {
			continue
		}
		b.WriteString("<DBEngineVersion>")
		fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(row.Engine))
		fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(row.EngineVersion))
		fmt.Fprintf(&b, "<MajorEngineVersion>%s</MajorEngineVersion>", xmlEscape(row.MajorVersion))
		fmt.Fprintf(&b, "<DBParameterGroupFamily>%s</DBParameterGroupFamily>", xmlEscape(row.Family))
		fmt.Fprintf(&b, "<DBEngineDescription>%s</DBEngineDescription>", xmlEscape(rdsEngineDescription(row.Engine)))
		fmt.Fprintf(&b, "<DBEngineVersionDescription>%s</DBEngineVersionDescription>", xmlEscape(rdsEngineDescription(row.Engine)+" "+row.EngineVersion))
		b.WriteString("<Status>available</Status>")
		b.WriteString("<ValidUpgradeTarget>")
		for _, target := range rdsUpgradeTargets(row) {
			major := target.MajorVersion != row.MajorVersion
			b.WriteString("<UpgradeTarget>")
			fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(target.Engine))
			fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(target.EngineVersion))
			fmt.Fprintf(&b, "<Description>%s</Description>", xmlEscape(rdsEngineDescription(target.Engine)+" "+target.EngineVersion))
			fmt.Fprintf(&b, "<AutoUpgrade>%t</AutoUpgrade>", !major && target.Default)
			fmt.Fprintf(&b, "<IsMajorVersionUpgrade>%t</IsMajorVersionUpgrade>", major)
			b.WriteString("</UpgradeTarget>")
		}
		b.WriteString("</ValidUpgradeTarget>")
		b.WriteString("<SupportsReadReplica>true</SupportsReadReplica>")
		b.WriteString("<SupportsLogExportsToCloudwatchLogs>true</SupportsLogExportsToCloudwatchLogs>")
		b.WriteString("</DBEngineVersion>")
	}
	b.WriteString("</DBEngineVersions>")
	rdsXMLResponse(w, "DescribeDBEngineVersions", b.String(), sim.RequestID(r.Context()))
}

// rdsMajorDefault is the newest version of row's major version.
func rdsMajorDefault(row rdsEngineVersion) rdsEngineVersion {
	newest, _ := rdsFindEngineVersion(row.Engine, row.MajorVersion)
	return newest
}

func rdsEngineDescription(engine string) string {
	switch engine {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL Community Edition"
	case "mariadb":
		return "MariaDB Community Edition"
	case "aurora-postgresql":
		return "Aurora (PostgreSQL)"
	case "aurora-mysql":
		return "Aurora MySQL"
	}
	return engine
}

func rdsParameterGroupErrorStatus(code string) int {
	if code == "DBParameterGroupNotFound" {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// rdsEarlierEngineMajors are the major versions an earlier simulator ran every
// instance of an engine on, whatever version it recorded.
var rdsEarlierEngineMajors = map[string]string{"postgres": "16", "mysql": "8.0", "mariadb": "11.4"}

// rdsMigrateEngineVersions rewrites a version an earlier simulator recorded
// and the simulator does not offer to the newest offered version of the major
// version whose engine wrote the data, so the engine that starts on the volume
// is one that reads it.
func rdsMigrateEngineVersions() {
	migrated := func(engine, version string) (string, bool) {
		if !rdsVersionedEngine(engine) {
			return "", false
		}
		if _, ok := rdsFindEngineVersion(engine, version); ok {
			return "", false
		}
		row, ok := rdsFindEngineVersion(engine, rdsEarlierEngineMajors[engine])
		return row.EngineVersion, ok
	}
	for _, instance := range rdsInstances.List() {
		if version, ok := migrated(instance.Engine, instance.EngineVersion); ok {
			rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) { stored.EngineVersion = version })
		}
	}
	for _, snapshot := range rdsSnapshots.List() {
		if version, ok := migrated(snapshot.Engine, snapshot.EngineVersion); ok {
			rdsSnapshots.Update(snapshot.DBSnapshotIdentifier, func(stored *RDSSnapshot) { stored.EngineVersion = version })
		}
	}
	for _, store := range []sim.Store[RDSInstanceAutomatedBackup]{rdsInstanceAutomatedBackups, rdsReplicatedBackups} {
		for _, backup := range store.ListPrefix("") {
			if version, ok := migrated(backup.Item.Engine, backup.Item.EngineVersion); ok {
				store.Update(backup.ID, func(stored *RDSInstanceAutomatedBackup) { stored.EngineVersion = version })
			}
		}
	}
}

// rdsUpgradeInstanceEngine moves an upgrading instance's engine to its pending
// version: the engine stops, and the release of the new version starts on the
// instance's volume, which the MySQL and MariaDB servers upgrade in place and
// a PostgreSQL server of the same major version reads as it is. An engine that
// does not come up on the new version lands the instance failed.
func rdsUpgradeInstanceEngine(id string) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.DBInstanceStatus != "upgrading" || instance.PendingEngineVersion == "" {
		return
	}
	err := rdsStopDataPlane(id, false)
	if err == nil {
		// An earlier process may have left the previous version's engine.
		rdsRemoveEngineContainers("Amazon RDS "+id, map[string]string{"sockerless-rds-instance": id})
		instance.EngineVersion, instance.PendingEngineVersion = instance.PendingEngineVersion, ""
		err = rdsStartInstanceEngine(&instance)
	}
	if err == nil {
		if plane, served := rdsLoadDataPlane(id); served {
			err = plane.engine.Ensure()
		}
	}
	status := "available"
	if err != nil {
		log.Printf("Amazon RDS %s: upgrade to %s: %v", id, instance.EngineVersion, err)
		status = "failed"
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != instance.DbiResourceId || stored.DBInstanceStatus != "upgrading" {
			return
		}
		stored.EngineVersion, stored.PendingEngineVersion = instance.EngineVersion, ""
		stored.Endpoint, stored.Port = instance.Endpoint, instance.Port
		stored.DBInstanceStatus = status
	})
}

// rdsEnsureOfferedDefaultParameterGroup creates the default DB parameter
// group name names when it is the default group of a family the simulator
// offers, which exists in every account whether or not an instance uses it.
func rdsEnsureOfferedDefaultParameterGroup(name string) {
	family, isDefault := strings.CutPrefix(name, "default.")
	if !isDefault {
		return
	}
	for _, row := range rdsEngineVersions {
		if row.Family == family {
			rdsEnsureDefaultParameterGroup(name)
			return
		}
	}
}

// rdsOrderableVersions are the versions DescribeOrderableDBInstanceOptions
// answers for: the requested one when the simulator offers it, or every
// version it offers of engine.
func rdsOrderableVersions(engine, requested string) []string {
	if !rdsVersionedEngine(engine) {
		if requested == "" {
			requested = rdsDefaultEngineVersion(engine)
		}
		return []string{requested}
	}
	var versions []string
	for _, row := range rdsEngineVersions {
		if row.Engine == engine && (requested == "" || row.EngineVersion == requested) {
			versions = append(versions, row.EngineVersion)
		}
	}
	return versions
}
