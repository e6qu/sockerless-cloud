package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// An Amazon RDS blue/green deployment of a DB instance provisions the green
// DB instance the way a restore to the latest restorable time does: its
// volume is a capture of the blue instance's volume, and its engine starts on
// that data with the target engine version, class, storage and DB parameter
// group recorded. The green instance serves its sessions read-only. A
// switchover stops both engines, keeps the blue data under the blue
// identifier with -old1 appended, and gives the green instance the blue
// identifier, ARN and endpoint on top of the blue volume, so the new
// production instance holds every write the blue instance committed.

const (
	rdsBlueGreenProvisioning         = "PROVISIONING"
	rdsBlueGreenAvailable            = "AVAILABLE"
	rdsBlueGreenSwitchoverInProgress = "SWITCHOVER_IN_PROGRESS"
	rdsBlueGreenSwitchoverCompleted  = "SWITCHOVER_COMPLETED"
	rdsBlueGreenSwitchoverFailed     = "SWITCHOVER_FAILED"
	rdsBlueGreenInvalidConfiguration = "INVALID_CONFIGURATION"
	rdsBlueGreenDeleting             = "DELETING"

	// rdsDefaultSwitchoverTimeout is SwitchoverTimeout's documented default,
	// in seconds.
	rdsDefaultSwitchoverTimeout = 300
)

// RDSBlueGreenDeployment is a blue/green deployment of a DB instance. Source
// and Target are the ARNs of the blue and green DB instances; after a
// switchover Source names the renamed blue instance and Target the instance
// now in production.
type RDSBlueGreenDeployment struct {
	BlueGreenDeploymentIdentifier string
	BlueGreenDeploymentName       string
	Source                        string
	Target                        string
	Status                        string
	StatusDetails                 string `json:",omitempty"`
	// UpgradesEngine records that the request named a TargetEngineVersion
	// other than the blue instance's.
	UpgradesEngine bool `json:",omitempty"`
	CreateTime     string
	DeleteTime     string `json:",omitempty"`
	// TargetResourceID is the green instance's DbiResourceId, which the
	// instance keeps when a switchover renames it.
	TargetResourceID string `json:",omitempty"`
	// SwitchoverTimeout and RetiredIdentifier hold a switchover in progress:
	// its time budget in seconds and the identifier the blue instance takes.
	SwitchoverTimeout int    `json:",omitempty"`
	RetiredIdentifier string `json:",omitempty"`
	// DeleteTarget records a deletion's DeleteTarget.
	DeleteTarget bool `json:",omitempty"`
	Tags         map[string]string
}

func rdsBlueGreenDeploymentARN(id string) string {
	return fmt.Sprintf("arn:aws:rds:%s:%s:deployment:%s", awsRegion(), awsAccountID(), id)
}

func rdsBlueGreenEngine(engine string) bool {
	switch strings.ToLower(engine) {
	case "mysql", "mariadb", "postgres":
		return true
	}
	return false
}

// rdsBlueGreenSuffix is the six lowercase letters Amazon RDS appends to a
// green resource's name after -green-.
func rdsBlueGreenSuffix() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("read random bytes: %v", err))
	}
	for i, b := range raw {
		raw[i] = letters[int(b)%len(letters)]
	}
	return string(raw)
}

// rdsGreenIdentifier names the green DB instance of blue, within the
// 63-character identifier limit.
func rdsGreenIdentifier(blue string) string {
	suffix := "-green-" + rdsBlueGreenSuffix()
	if len(blue)+len(suffix) > 63 {
		blue = strings.TrimRight(blue[:63-len(suffix)], "-")
	}
	return blue + suffix
}

// rdsRetiredIdentifier is the identifier a switchover gives the blue
// instance: its own with -old1 appended, or the next free -oldN.
func rdsRetiredIdentifier(blue string) string {
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-old%d", blue, n)
		if _, taken := rdsInstances.Get(candidate); !taken {
			return candidate
		}
	}
}

func rdsInstanceIdentifierFromARN(arn string) string {
	if i := strings.LastIndex(arn, ":db:"); i >= 0 {
		return arn[i+len(":db:"):]
	}
	return arn
}

// rdsResolveParameterGroup checks that a DB parameter group a request names
// exists. The default groups Amazon RDS owns exist in every account.
func rdsResolveParameterGroup(name string) bool {
	if name == "" || strings.HasPrefix(name, "default.") {
		return true
	}
	_, ok := rdsParamGroups.Get(name)
	return ok
}

func rdsBlueGreenTaskStatus(status string) string {
	switch status {
	case rdsBlueGreenProvisioning:
		return "IN_PROGRESS"
	case rdsBlueGreenInvalidConfiguration:
		return "FAILED"
	default:
		return "COMPLETED"
	}
}

func rdsSwitchoverDetailStatus(d RDSBlueGreenDeployment) string {
	_, sourceFound := findRDSByARN(d.Source)
	_, targetFound := findRDSByARN(d.Target)
	switch {
	case d.Status == rdsBlueGreenSwitchoverInProgress, d.Status == rdsBlueGreenSwitchoverCompleted,
		d.Status == rdsBlueGreenSwitchoverFailed:
		return d.Status
	case !sourceFound:
		return "MISSING_SOURCE"
	case !targetFound:
		return "MISSING_TARGET"
	case d.Status == rdsBlueGreenAvailable:
		return rdsBlueGreenAvailable
	default:
		return rdsBlueGreenProvisioning
	}
}

func renderRDSBlueGreenDeploymentMembers(d RDSBlueGreenDeployment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<BlueGreenDeploymentIdentifier>%s</BlueGreenDeploymentIdentifier>", xmlEscape(d.BlueGreenDeploymentIdentifier))
	fmt.Fprintf(&b, "<BlueGreenDeploymentName>%s</BlueGreenDeploymentName>", xmlEscape(d.BlueGreenDeploymentName))
	fmt.Fprintf(&b, "<Source>%s</Source>", xmlEscape(d.Source))
	fmt.Fprintf(&b, "<Target>%s</Target>", xmlEscape(d.Target))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(d.Status))
	if d.StatusDetails != "" {
		fmt.Fprintf(&b, "<StatusDetails>%s</StatusDetails>", xmlEscape(d.StatusDetails))
	}
	fmt.Fprintf(&b, "<CreateTime>%s</CreateTime>", xmlEscape(d.CreateTime))
	if d.DeleteTime != "" {
		fmt.Fprintf(&b, "<DeleteTime>%s</DeleteTime>", xmlEscape(d.DeleteTime))
	}
	b.WriteString("<SwitchoverDetails><member>")
	fmt.Fprintf(&b, "<SourceMember>%s</SourceMember>", xmlEscape(d.Source))
	fmt.Fprintf(&b, "<TargetMember>%s</TargetMember>", xmlEscape(d.Target))
	fmt.Fprintf(&b, "<Status>%s</Status>", rdsSwitchoverDetailStatus(d))
	b.WriteString("</member></SwitchoverDetails>")
	task := rdsBlueGreenTaskStatus(d.Status)
	b.WriteString("<Tasks>")
	fmt.Fprintf(&b, "<member><Name>CREATING_READ_REPLICA_OF_SOURCE</Name><Status>%s</Status></member>", task)
	if d.UpgradesEngine {
		fmt.Fprintf(&b, "<member><Name>DB_ENGINE_VERSION_UPGRADE</Name><Status>%s</Status></member>", task)
	}
	b.WriteString("</Tasks>")
	b.WriteString(renderRDSTagList(d.Tags))
	return b.String()
}

func renderRDSBlueGreenDeployment(d RDSBlueGreenDeployment) string {
	return "<BlueGreenDeployment>" + renderRDSBlueGreenDeploymentMembers(d) + "</BlueGreenDeployment>"
}

func rdsFindBlueGreenDeployment(id string) (RDSBlueGreenDeployment, bool) {
	if d, ok := rdsBlueGreenDeployments.Get(id); ok {
		return d, true
	}
	// The identifier is not case-sensitive.
	for _, d := range rdsBlueGreenDeployments.List() {
		if strings.EqualFold(d.BlueGreenDeploymentIdentifier, id) {
			return d, true
		}
	}
	return RDSBlueGreenDeployment{}, false
}

func handleRDSCreateBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	fail := func(code, message string, status int) { rdsErrorXML(w, code, message, status, requestID) }
	name, source := r.FormValue("BlueGreenDeploymentName"), r.FormValue("Source")
	if name == "" || source == "" {
		fail("MissingParameter", "BlueGreenDeploymentName and Source are required", http.StatusBadRequest)
		return
	}
	for _, d := range rdsBlueGreenDeployments.List() {
		if strings.EqualFold(d.BlueGreenDeploymentName, name) {
			fail("BlueGreenDeploymentAlreadyExistsFault",
				fmt.Sprintf("A blue/green deployment named %s already exists.", name), http.StatusBadRequest)
			return
		}
	}
	if fields := strings.SplitN(source, ":", 7); len(fields) == 7 && fields[5] == "cluster" {
		if _, ok := findRDSClusterByARN(source); !ok {
			fail("DBClusterNotFoundFault", fmt.Sprintf("DBCluster %s not found.", source), http.StatusNotFound)
			return
		}
		fail("SourceClusterNotSupportedFault",
			fmt.Sprintf("The source DB cluster %s isn't supported for a blue/green deployment.", source), http.StatusBadRequest)
		return
	}
	blue, ok := findRDSByARN(source)
	if !ok {
		fail("DBInstanceNotFound", fmt.Sprintf("DBInstance %s not found.", source), http.StatusNotFound)
		return
	}
	notSupported := func(reason string) {
		fail("SourceDatabaseNotSupportedFault",
			fmt.Sprintf("The source DB instance %s isn't supported for a blue/green deployment: %s", blue.DBInstanceIdentifier, reason),
			http.StatusBadRequest)
	}
	switch {
	case blue.DBClusterIdentifier != "":
		notSupported("it is a member of DB cluster " + blue.DBClusterIdentifier + ".")
		return
	case !rdsBlueGreenEngine(blue.Engine):
		notSupported("the " + blue.Engine + " engine has no blue/green deployments.")
		return
	case blue.ReadReplicaSource != "":
		notSupported("it is a read replica.")
		return
	case blue.BackupRetentionPeriod == 0:
		notSupported("automated backups are turned off.")
		return
	}
	if blue.DBInstanceStatus != "available" {
		fail("InvalidDBInstanceState",
			fmt.Sprintf("Instance %s is not in available state.", blue.DBInstanceIdentifier), http.StatusBadRequest)
		return
	}
	for _, d := range rdsBlueGreenDeployments.List() {
		if d.Source == blue.ARN && d.Status != rdsBlueGreenSwitchoverCompleted {
			fail("InvalidDBInstanceState",
				fmt.Sprintf("DB instance %s is already the source of blue/green deployment %s.", blue.DBInstanceIdentifier, d.BlueGreenDeploymentIdentifier),
				http.StatusBadRequest)
			return
		}
	}
	if r.FormValue("TargetDBClusterParameterGroupName") != "" {
		fail("InvalidParameterCombination",
			"TargetDBClusterParameterGroupName applies to a blue/green deployment of a DB cluster.", http.StatusBadRequest)
		return
	}
	engineVersion := blue.EngineVersion
	if v := r.FormValue("TargetEngineVersion"); v != "" {
		resolved, problem := rdsResolveEngineVersion(blue.Engine, v)
		if problem == "" {
			problem = rdsCheckEngineUpgrade(blue.Engine, blue.EngineVersion, resolved, true)
		}
		if problem != "" {
			fail("InvalidParameterCombination", problem, http.StatusBadRequest)
			return
		}
		engineVersion = resolved
	}
	requestedGroup := r.FormValue("TargetDBParameterGroupName")
	if requestedGroup == "" && !strings.HasPrefix(blue.DBParameterGroupName, "default.") {
		requestedGroup = blue.DBParameterGroupName
	}
	paramGroup, code, message := rdsInstanceParameterGroup(requestedGroup, blue.Engine, engineVersion)
	if code != "" {
		fail(code, message, rdsParameterGroupErrorStatus(code))
		return
	}
	class := blue.DBInstanceClass
	if v := r.FormValue("TargetDBInstanceClass"); v != "" {
		class = v
	}
	storage := blue.AllocatedStorage
	if v := r.FormValue("TargetAllocatedStorage"); v != "" {
		storage = atoiOrZero(v)
	}

	id := "bgd-" + strings.ToLower(strings.ReplaceAll(sim.NewUUID(), "-", ""))[:16]
	greenID := rdsGreenIdentifier(blue.DBInstanceIdentifier)
	green := RDSInstance{
		DBInstanceIdentifier:            greenID,
		DbiResourceId:                   rdsResourceID(),
		DBInstanceClass:                 class,
		Engine:                          blue.Engine,
		EngineVersion:                   engineVersion,
		DBInstanceStatus:                "creating",
		MasterUsername:                  blue.MasterUsername,
		DBName:                          blue.DBName,
		AllocatedStorage:                storage,
		Port:                            blue.Port,
		AvailabilityZone:                blue.AvailabilityZone,
		InstanceCreateTime:              time.Now().UTC().Format(time.RFC3339),
		ARN:                             rdsInstanceARN(greenID),
		ReadReplicaSource:               blue.DBInstanceIdentifier,
		Tags:                            mergeTags(nil, blue.Tags),
		MasterUserSecret:                append([]byte(nil), blue.MasterUserSecret...),
		BackendMasterUserSecret:         append([]byte(nil), blue.BackendMasterUserSecret...),
		EnableIAMDatabaseAuthentication: blue.EnableIAMDatabaseAuthentication,
		DeletionProtection:              blue.DeletionProtection,
		BackupRetentionPeriod:           blue.BackupRetentionPeriod,
		PreferredBackupWindow:           blue.PreferredBackupWindow,
		PreferredMaintenanceWindow:      blue.PreferredMaintenanceWindow,
		BackupTarget:                    blue.BackupTarget,
		DBParameterGroupName:            paramGroup,
		AutoMinorVersionUpgrade:         blue.AutoMinorVersionUpgrade,
		BlueGreenDeploymentIdentifier:   id,
		RestoreSourceVolume:             rdsInstanceVolume(blue.DBInstanceIdentifier),
	}
	d := RDSBlueGreenDeployment{
		BlueGreenDeploymentIdentifier: id,
		BlueGreenDeploymentName:       name,
		Source:                        blue.ARN,
		Target:                        green.ARN,
		TargetResourceID:              green.DbiResourceId,
		Status:                        rdsBlueGreenProvisioning,
		UpgradesEngine:                engineVersion != blue.EngineVersion,
		CreateTime:                    time.Now().UTC().Format(time.RFC3339),
		Tags:                          parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsBlueGreenDeployments.Put(id, d)
	rdsInstances.Put(greenID, green)
	rdsInstances.Update(blue.DBInstanceIdentifier, func(i *RDSInstance) {
		i.ReadReplicas = rdsAppendUnique(i.ReadReplicas, greenID)
	})
	bg.Go(func() { rdsFinishInstanceRestore(greenID) })
	rdsXMLResponse(w, "CreateBlueGreenDeployment", renderRDSBlueGreenDeployment(d), requestID)
}

// rdsSettleBlueGreenProvisioning lands a provisioning deployment AVAILABLE
// once its green instance is available, and INVALID_CONFIGURATION when the
// green instance came up any other way.
func rdsSettleBlueGreenProvisioning(green RDSInstance) {
	if green.BlueGreenDeploymentIdentifier == "" || rdsInstanceBringingUp(green.DBInstanceStatus) {
		return
	}
	rdsBlueGreenDeployments.Update(green.BlueGreenDeploymentIdentifier, func(d *RDSBlueGreenDeployment) {
		if d.Status != rdsBlueGreenProvisioning || d.Target != green.ARN {
			return
		}
		if green.DBInstanceStatus == "available" {
			d.Status = rdsBlueGreenAvailable
			return
		}
		d.Status = rdsBlueGreenInvalidConfiguration
		d.StatusDetails = fmt.Sprintf("The green DB instance %s is %s.", green.DBInstanceIdentifier, green.DBInstanceStatus)
	})
}

func handleRDSDescribeBlueGreenDeployments(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	wantID := r.FormValue("BlueGreenDeploymentIdentifier")
	ids := rdsFilterValues(r, "blue-green-deployment-identifier")
	names := rdsFilterValues(r, "blue-green-deployment-name")
	sources := rdsFilterValues(r, "source")
	targets := rdsFilterValues(r, "target")
	var b strings.Builder
	matched := false
	b.WriteString("<BlueGreenDeployments>")
	for _, d := range rdsBlueGreenDeployments.List() {
		if wantID != "" && !strings.EqualFold(d.BlueGreenDeploymentIdentifier, wantID) {
			continue
		}
		if !rdsFilterMatches(ids, d.BlueGreenDeploymentIdentifier) || !rdsFilterMatches(names, d.BlueGreenDeploymentName) ||
			!rdsFilterMatches(sources, d.Source) || !rdsFilterMatches(targets, d.Target) {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderRDSBlueGreenDeploymentMembers(d))
		b.WriteString("</member>")
	}
	b.WriteString("</BlueGreenDeployments>")
	if wantID != "" && !matched {
		rdsErrorXML(w, "BlueGreenDeploymentNotFoundFault",
			fmt.Sprintf("BlueGreenDeployment %s not found.", wantID), http.StatusNotFound, requestID)
		return
	}
	rdsXMLResponse(w, "DescribeBlueGreenDeployments", b.String(), requestID)
}

func handleRDSSwitchoverBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := r.FormValue("BlueGreenDeploymentIdentifier")
	d, ok := rdsFindBlueGreenDeployment(id)
	if !ok {
		rdsErrorXML(w, "BlueGreenDeploymentNotFoundFault",
			fmt.Sprintf("BlueGreenDeployment %s not found.", id), http.StatusNotFound, requestID)
		return
	}
	timeout := rdsDefaultSwitchoverTimeout
	if v := r.FormValue("SwitchoverTimeout"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 30 {
			rdsErrorXML(w, "InvalidParameterValue",
				fmt.Sprintf("SwitchoverTimeout %s must be a whole number of seconds of at least 30.", v), http.StatusBadRequest, requestID)
			return
		}
		timeout = parsed
	}
	blue, blueFound := findRDSByARN(d.Source)
	green, greenFound := findRDSByARN(d.Target)
	refuse := func(reason string) {
		rdsErrorXML(w, "InvalidBlueGreenDeploymentStateFault",
			fmt.Sprintf("Blue/green deployment %s can't be switched over: %s", d.BlueGreenDeploymentIdentifier, reason),
			http.StatusBadRequest, requestID)
	}
	switch {
	case d.Status != rdsBlueGreenAvailable:
		refuse("its status is " + d.Status + ".")
		return
	case !blueFound || !greenFound:
		refuse("a DB instance of the deployment no longer exists.")
		return
	case blue.DBInstanceStatus != "available" || green.DBInstanceStatus != "available":
		refuse("its DB instances must be available.")
		return
	}
	started := false
	rdsBlueGreenDeployments.Update(d.BlueGreenDeploymentIdentifier, func(stored *RDSBlueGreenDeployment) {
		if stored.Status != rdsBlueGreenAvailable {
			return
		}
		stored.Status = rdsBlueGreenSwitchoverInProgress
		stored.StatusDetails = ""
		stored.SwitchoverTimeout = timeout
		stored.RetiredIdentifier = rdsRetiredIdentifier(blue.DBInstanceIdentifier)
		d = *stored
		started = true
	})
	if !started {
		refuse("another request changed it.")
		return
	}
	deploymentID := d.BlueGreenDeploymentIdentifier
	bg.Go(func() { rdsRunSwitchover(deploymentID) })
	rdsXMLResponse(w, "SwitchoverBlueGreenDeployment", renderRDSBlueGreenDeployment(d), requestID)
}

// rdsRunSwitchover switches a deployment over, and runs again from the top
// for a switchover a previous process left in progress: each step checks
// whether it already ran.
func rdsRunSwitchover(deploymentID string) {
	d, ok := rdsBlueGreenDeployments.Get(deploymentID)
	if !ok || d.Status != rdsBlueGreenSwitchoverInProgress {
		return
	}
	blueID := rdsInstanceIdentifierFromARN(d.Source)
	greenID := rdsInstanceIdentifierFromARN(d.Target)
	retiredID := d.RetiredIdentifier
	fail := func(details string) {
		log.Printf("Amazon RDS blue/green deployment %s: %s", deploymentID, details)
		rdsBlueGreenDeployments.Update(deploymentID, func(stored *RDSBlueGreenDeployment) {
			stored.Status = rdsBlueGreenSwitchoverFailed
			stored.StatusDetails = details
			stored.SwitchoverTimeout, stored.RetiredIdentifier = 0, ""
		})
	}

	blue, blueFound := rdsInstances.Get(blueID)
	green, greenFound := rdsInstances.Get(greenID)
	if swapped := blueFound && blue.DbiResourceId == d.TargetResourceID; swapped {
		if greenFound && green.DbiResourceId == d.TargetResourceID {
			rdsInstances.Delete(greenID)
		}
	} else {
		if !blueFound || !greenFound {
			fail("a DB instance of the deployment no longer exists.")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(d.SwitchoverTimeout)*time.Second)
		err := rdsSwitchoverVolumes(ctx, blueID, greenID, retiredID)
		cancel()
		if err != nil {
			rdsRestartBlueGreenPlanes(blueID, greenID)
			fail(fmt.Sprintf("Switchover was rolled back: %v", err))
			return
		}
		rdsSwapBlueGreenRecords(blue, green, retiredID)
	}
	rdsRemoveInstanceVolume(greenID)

	var problems []string
	for _, id := range []string{blueID, retiredID} {
		instance, ok := rdsInstances.Get(id)
		if !ok {
			continue
		}
		if err := rdsStopDataPlane(id, false); err != nil {
			problems = append(problems, fmt.Sprintf("DB instance %s: %v", id, err))
			continue
		}
		if err := rdsStartInstanceEngine(&instance); err != nil {
			problems = append(problems, fmt.Sprintf("DB instance %s: %v", id, err))
			continue
		}
		rdsInstances.Update(id, func(stored *RDSInstance) { stored.Endpoint, stored.Port = instance.Endpoint, instance.Port })
	}
	rdsTakeFirstInstanceBackup(blueID)
	production, _ := rdsInstances.Get(blueID)
	retired, _ := rdsInstances.Get(retiredID)
	rdsBlueGreenDeployments.Update(deploymentID, func(stored *RDSBlueGreenDeployment) {
		stored.Source, stored.Target = retired.ARN, production.ARN
		stored.SwitchoverTimeout, stored.RetiredIdentifier = 0, ""
		if len(problems) > 0 {
			stored.Status = rdsBlueGreenSwitchoverFailed
			stored.StatusDetails = strings.Join(problems, "; ")
			return
		}
		stored.Status = rdsBlueGreenSwitchoverCompleted
	})
}

// rdsSwitchoverVolumes stops both engines and moves the blue data to the
// retired identifier's volume, leaving the blue identifier's volume — the
// one the green instance takes over — holding everything the blue instance
// committed.
func rdsSwitchoverVolumes(ctx context.Context, blueID, greenID, retiredID string) error {
	for _, id := range []string{blueID, greenID} {
		if err := rdsStopDataPlane(id, false); err != nil {
			return fmt.Errorf("stop DB instance %s: %w", id, err)
		}
	}
	if sim.RequireContainerRuntime("switching over an RDS blue/green deployment") != nil {
		return nil
	}
	for _, id := range []string{blueID, greenID, retiredID} {
		rdsRemoveEngineContainers("Amazon RDS "+id, map[string]string{"sockerless-rds-instance": id})
	}
	sim.RemoveVolumeSettled(rdsInstanceVolume(retiredID), "rds")
	if !sim.VolumeExists(rdsInstanceVolume(blueID)) {
		return nil
	}
	if _, err := sim.SnapshotVolume(ctx, rdsInstanceVolume(blueID), rdsInstanceVolume(retiredID)); err != nil {
		sim.RemoveVolumeSettled(rdsInstanceVolume(retiredID), "rds")
		if ctx.Err() != nil {
			return fmt.Errorf("the switchover did not finish within its timeout")
		}
		return err
	}
	return nil
}

// rdsRestartBlueGreenPlanes brings the endpoints of a switchover that rolled
// back up again on the records as they were.
func rdsRestartBlueGreenPlanes(ids ...string) {
	for _, id := range ids {
		instance, ok := rdsInstances.Get(id)
		if !ok {
			continue
		}
		if _, served := rdsLoadDataPlane(id); served {
			continue
		}
		if err := rdsStartInstanceEngine(&instance); err != nil {
			log.Printf("Amazon RDS %s: restart after a rolled-back switchover: %v", id, err)
		}
	}
}

// rdsSwapBlueGreenRecords renames the blue instance to retiredID and gives
// the green instance the blue identifier, ARN and endpoint, with the blue
// master-user credentials its data was written under. The green's base
// backups belong to the data it held before the switchover, so it takes a new
// first automated backup of the data it serves now.
func rdsSwapBlueGreenRecords(blue, green RDSInstance, retiredID string) {
	retired := blue
	retired.DBInstanceIdentifier = retiredID
	retired.ARN = rdsInstanceARN(retiredID)
	retired.Endpoint = ""
	retired.ReadReplicas = rdsRemoveString(blue.ReadReplicas, green.DBInstanceIdentifier)

	production := green
	production.DBInstanceIdentifier = blue.DBInstanceIdentifier
	production.ARN = blue.ARN
	production.Endpoint, production.Port = blue.Endpoint, blue.Port
	production.ReadReplicaSource = ""
	production.BlueGreenDeploymentIdentifier = ""
	production.MasterUserSecret = append([]byte(nil), blue.MasterUserSecret...)
	production.BackendMasterUserSecret = append([]byte(nil), blue.BackendMasterUserSecret...)
	production.BaseBackups = nil

	rdsInstances.Put(retiredID, retired)
	rdsInstances.Put(blue.DBInstanceIdentifier, production)
	rdsInstances.Delete(green.DBInstanceIdentifier)
	// The blue instance's read replicas keep replicating it under its new
	// identifier.
	for _, replicaID := range retired.ReadReplicas {
		rdsInstances.Update(replicaID, func(stored *RDSInstance) {
			if rdsIsReadReplica(*stored) && stored.ReadReplicaSource == blue.DBInstanceIdentifier {
				stored.ReadReplicaSource = retiredID
			}
		})
	}
	for _, snapshot := range rdsSnapshots.List() {
		switch snapshot.DbiResourceId {
		case blue.DbiResourceId:
			rdsSnapshots.Update(snapshot.DBSnapshotIdentifier, func(s *RDSSnapshot) { s.DBInstanceIdentifier = retiredID })
		case green.DbiResourceId:
			rdsSnapshots.Update(snapshot.DBSnapshotIdentifier, func(s *RDSSnapshot) { s.DBInstanceIdentifier = blue.DBInstanceIdentifier })
		}
	}
}

func handleRDSDeleteBlueGreenDeployment(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	id := r.FormValue("BlueGreenDeploymentIdentifier")
	d, ok := rdsFindBlueGreenDeployment(id)
	if !ok {
		rdsErrorXML(w, "BlueGreenDeploymentNotFoundFault",
			fmt.Sprintf("BlueGreenDeployment %s not found.", id), http.StatusNotFound, requestID)
		return
	}
	deleteTarget := strings.EqualFold(r.FormValue("DeleteTarget"), "true")
	refuse := func(reason string) {
		rdsErrorXML(w, "InvalidBlueGreenDeploymentStateFault",
			fmt.Sprintf("Blue/green deployment %s can't be deleted: %s", d.BlueGreenDeploymentIdentifier, reason),
			http.StatusBadRequest, requestID)
	}
	switch {
	case d.Status == rdsBlueGreenSwitchoverInProgress || d.Status == rdsBlueGreenDeleting:
		refuse("its status is " + d.Status + ".")
		return
	case deleteTarget && d.Status == rdsBlueGreenSwitchoverCompleted:
		refuse("DeleteTarget can't be specified once the switchover has completed.")
		return
	}
	rdsBlueGreenDeployments.Update(d.BlueGreenDeploymentIdentifier, func(stored *RDSBlueGreenDeployment) {
		stored.Status = rdsBlueGreenDeleting
		stored.DeleteTime = time.Now().UTC().Format(time.RFC3339)
		stored.DeleteTarget = deleteTarget
		d = *stored
	})
	deploymentID := d.BlueGreenDeploymentIdentifier
	bg.Go(func() { rdsFinishBlueGreenDeletion(deploymentID) })
	rdsXMLResponse(w, "DeleteBlueGreenDeployment", renderRDSBlueGreenDeployment(d), requestID)
}

// rdsFinishBlueGreenDeletion ends a deployment. A green instance the
// deployment has not switched over leaves it: DeleteTarget deletes it, and
// otherwise it stays as a standalone instance that accepts writes.
func rdsFinishBlueGreenDeletion(deploymentID string) {
	d, ok := rdsBlueGreenDeployments.Get(deploymentID)
	if !ok {
		return
	}
	greenID := rdsInstanceIdentifierFromARN(d.Target)
	var green RDSInstance
	member := false
	rdsInstances.Update(greenID, func(i *RDSInstance) {
		if i.BlueGreenDeploymentIdentifier != deploymentID {
			return
		}
		member = true
		i.ReadReplicaSource, i.BlueGreenDeploymentIdentifier = "", ""
		green = *i
	})
	if member {
		blueID := rdsInstanceIdentifierFromARN(d.Source)
		rdsInstances.Update(blueID, func(i *RDSInstance) { i.ReadReplicas = rdsRemoveString(i.ReadReplicas, greenID) })
		if d.DeleteTarget {
			rdsDeleteGreenInstance(greenID)
		} else {
			rdsServeGreenWritable(green)
		}
	}
	rdsBlueGreenDeployments.Delete(deploymentID)
}

// rdsDeleteGreenInstance deletes a green instance as DeleteDBInstance with
// SkipFinalSnapshot does. One whose volume is still being seeded goes once
// rdsFinishInstanceRestore lets go of it.
func rdsDeleteGreenInstance(id string) {
	seeding, resourceID := false, ""
	if !rdsInstances.Update(id, func(stored *RDSInstance) {
		seeding = stored.DBInstanceStatus == "creating" && stored.RestoreSourceVolume != ""
		stored.DBInstanceStatus = "deleting"
		resourceID = stored.DbiResourceId
	}) {
		return
	}
	if !seeding {
		rdsFinishInstanceDeletion(id, resourceID)
	}
}

// rdsServeGreenWritable reopens a former green instance's endpoint for
// sessions that write.
func rdsServeGreenWritable(green RDSInstance) {
	if _, served := rdsLoadDataPlane(green.DBInstanceIdentifier); !served {
		return
	}
	if err := rdsStopDataPlane(green.DBInstanceIdentifier, false); err != nil {
		return
	}
	if err := rdsStartInstanceEngine(&green); err != nil {
		log.Printf("Amazon RDS %s: reopen the endpoint: %v", green.DBInstanceIdentifier, err)
	}
}

// rdsRecoverBlueGreenDeployments resumes what a previous process left in
// flight: a provisioning deployment whose green instance has come up, a
// switchover, and a deletion.
func rdsRecoverBlueGreenDeployments() {
	for _, d := range rdsBlueGreenDeployments.List() {
		id := d.BlueGreenDeploymentIdentifier
		switch d.Status {
		case rdsBlueGreenProvisioning:
			if green, ok := findRDSByARN(d.Target); ok {
				rdsSettleBlueGreenProvisioning(green)
			} else {
				rdsBlueGreenDeployments.Update(id, func(stored *RDSBlueGreenDeployment) {
					stored.Status = rdsBlueGreenInvalidConfiguration
					stored.StatusDetails = "The green DB instance no longer exists."
				})
			}
		case rdsBlueGreenSwitchoverInProgress:
			bg.Go(func() { rdsRunSwitchover(id) })
		case rdsBlueGreenDeleting:
			bg.Go(func() { rdsFinishBlueGreenDeletion(id) })
		}
	}
}
