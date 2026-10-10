package main

import (
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// RDSPendingModifications are the changes a ModifyDBInstance without
// ApplyImmediately holds for the instance's next maintenance window, which
// PendingModifiedValues reports until then.
type RDSPendingModifications struct {
	DBInstanceClass         string `json:",omitempty"`
	AllocatedStorage        int    `json:",omitempty"`
	EngineVersion           string `json:",omitempty"`
	BackupRetentionPeriod   *int   `json:",omitempty"`
	MultiAZ                 *bool  `json:",omitempty"`
	StorageType             string `json:",omitempty"`
	Iops                    *int   `json:",omitempty"`
	StorageThroughput       *int   `json:",omitempty"`
	DBSubnetGroupName       string `json:",omitempty"`
	OptionGroupName         string `json:",omitempty"`
	DBInstanceIdentifier    string `json:",omitempty"`
	CACertificateIdentifier string `json:",omitempty"`
	// KeepCertificateUntilRestart records CertificateRotationRestart=false:
	// the endpoint presents the new CA's certificate only from the engine's
	// next restart.
	KeepCertificateUntilRestart bool `json:",omitempty"`
}

// RDSRestartModifications are the changes the engine restart of a modifying
// instance applies, and the status the instance returns to once it has.
type RDSRestartModifications struct {
	Port                 int    `json:",omitempty"`
	DBInstanceIdentifier string `json:",omitempty"`
	// PreviousIdentifier is the identifier a renamed instance's engine and
	// volume still run under until the restart moves them.
	PreviousIdentifier string `json:",omitempty"`
	Status             string `json:",omitempty"`
}

func (p *RDSPendingModifications) empty() bool {
	return p == nil || *p == RDSPendingModifications{}
}

// merge lays later's changes over p's.
func (p *RDSPendingModifications) merge(later RDSPendingModifications) RDSPendingModifications {
	merged := RDSPendingModifications{}
	if p != nil {
		merged = *p
	}
	overlay := func(dst *string, src string) {
		if src != "" {
			*dst = src
		}
	}
	overlay(&merged.DBInstanceClass, later.DBInstanceClass)
	overlay(&merged.EngineVersion, later.EngineVersion)
	overlay(&merged.StorageType, later.StorageType)
	overlay(&merged.DBSubnetGroupName, later.DBSubnetGroupName)
	overlay(&merged.OptionGroupName, later.OptionGroupName)
	overlay(&merged.DBInstanceIdentifier, later.DBInstanceIdentifier)
	if later.CACertificateIdentifier != "" {
		merged.CACertificateIdentifier = later.CACertificateIdentifier
		merged.KeepCertificateUntilRestart = later.KeepCertificateUntilRestart
	}
	if later.AllocatedStorage != 0 {
		merged.AllocatedStorage = later.AllocatedStorage
	}
	if later.BackupRetentionPeriod != nil {
		merged.BackupRetentionPeriod = later.BackupRetentionPeriod
	}
	if later.MultiAZ != nil {
		merged.MultiAZ = later.MultiAZ
	}
	if later.Iops != nil {
		merged.Iops = later.Iops
	}
	if later.StorageThroughput != nil {
		merged.StorageThroughput = later.StorageThroughput
	}
	return merged
}

func renderRDSPendingModifiedValues(i RDSInstance) string {
	var b strings.Builder
	b.WriteString("<PendingModifiedValues>")
	pending := RDSPendingModifications{}
	if i.PendingModifications != nil {
		pending = *i.PendingModifications
	}
	restart := RDSRestartModifications{}
	if i.Restart != nil {
		restart = *i.Restart
	}
	if pending.DBInstanceClass != "" {
		fmt.Fprintf(&b, "<DBInstanceClass>%s</DBInstanceClass>", xmlEscape(pending.DBInstanceClass))
	}
	if pending.AllocatedStorage != 0 {
		fmt.Fprintf(&b, "<AllocatedStorage>%d</AllocatedStorage>", pending.AllocatedStorage)
	}
	if restart.Port != 0 {
		fmt.Fprintf(&b, "<Port>%d</Port>", restart.Port)
	}
	if pending.BackupRetentionPeriod != nil {
		fmt.Fprintf(&b, "<BackupRetentionPeriod>%d</BackupRetentionPeriod>", *pending.BackupRetentionPeriod)
	}
	if pending.MultiAZ != nil {
		fmt.Fprintf(&b, "<MultiAZ>%t</MultiAZ>", *pending.MultiAZ)
	}
	if version := i.PendingEngineVersion; version != "" || pending.EngineVersion != "" {
		if version == "" {
			version = pending.EngineVersion
		}
		fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(version))
	}
	if pending.Iops != nil {
		fmt.Fprintf(&b, "<Iops>%d</Iops>", *pending.Iops)
	}
	if pending.StorageThroughput != nil {
		fmt.Fprintf(&b, "<StorageThroughput>%d</StorageThroughput>", *pending.StorageThroughput)
	}
	if id := restart.DBInstanceIdentifier; id != "" || pending.DBInstanceIdentifier != "" {
		if id == "" {
			id = pending.DBInstanceIdentifier
		}
		fmt.Fprintf(&b, "<DBInstanceIdentifier>%s</DBInstanceIdentifier>", xmlEscape(id))
	}
	if pending.StorageType != "" {
		fmt.Fprintf(&b, "<StorageType>%s</StorageType>", xmlEscape(pending.StorageType))
	}
	if pending.CACertificateIdentifier != "" {
		fmt.Fprintf(&b, "<CACertificateIdentifier>%s</CACertificateIdentifier>", xmlEscape(pending.CACertificateIdentifier))
	}
	if pending.DBSubnetGroupName != "" {
		fmt.Fprintf(&b, "<DBSubnetGroupName>%s</DBSubnetGroupName>", xmlEscape(pending.DBSubnetGroupName))
	}
	b.WriteString("</PendingModifiedValues>")
	return b.String()
}

const rdsMinutesPerWeek = 7 * 24 * 60

var rdsWeekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// rdsMaintenanceWindow is a weekly window as minutes from Monday 00:00 UTC.
type rdsMaintenanceWindow struct {
	start, length int
}

func rdsParseWeeklyTime(value string) (int, bool) {
	parts := strings.Split(strings.ToLower(value), ":")
	if len(parts) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, false
	}
	day := -1
	for i, name := range rdsWeekdays {
		if parts[0] == name {
			day = i
		}
	}
	hour, hourErr := strconv.Atoi(parts[1])
	minute, minuteErr := strconv.Atoi(parts[2])
	if day < 0 || hourErr != nil || minuteErr != nil || hour > 23 || minute > 59 {
		return 0, false
	}
	return day*24*60 + hour*60 + minute, true
}

// rdsParseMaintenanceWindow reads ddd:hh24:mi-ddd:hh24:mi.
func rdsParseMaintenanceWindow(value string) (rdsMaintenanceWindow, bool) {
	from, to, found := strings.Cut(value, "-")
	start, startOK := rdsParseWeeklyTime(from)
	end, endOK := rdsParseWeeklyTime(to)
	if !found || !startOK || !endOK {
		return rdsMaintenanceWindow{}, false
	}
	return rdsMaintenanceWindow{start: start, length: (end - start + rdsMinutesPerWeek) % rdsMinutesPerWeek}, true
}

func rdsFormatWeeklyTime(minutes int) string {
	minutes = (minutes%rdsMinutesPerWeek + rdsMinutesPerWeek) % rdsMinutesPerWeek
	return fmt.Sprintf("%s:%02d:%02d", rdsWeekdays[minutes/(24*60)], minutes/60%24, minutes%60)
}

// rdsDailyWindow reads a backup window, hh24:mi-hh24:mi, as minutes from
// midnight UTC.
func rdsDailyWindow(value string) (start, length int, ok bool) {
	from, to, found := strings.Cut(value, "-")
	parse := func(s string) (int, bool) {
		hour, minute, cut := strings.Cut(s, ":")
		h, hErr := strconv.Atoi(hour)
		m, mErr := strconv.Atoi(minute)
		return h*60 + m, cut && hErr == nil && mErr == nil && h >= 0 && h < 24 && m >= 0 && m < 60
	}
	start, startOK := parse(from)
	end, endOK := parse(to)
	return start, (end - start + 24*60) % (24 * 60), found && startOK && endOK
}

// overlapsBackupWindow reports whether the window shares a minute with the
// daily backup window on any day.
func (w rdsMaintenanceWindow) overlapsBackupWindow(backupWindow string) bool {
	start, length, ok := rdsDailyWindow(backupWindow)
	if !ok {
		return false
	}
	for day := 0; day < 7; day++ {
		backupStart := day*24*60 + start
		for _, shift := range []int{-rdsMinutesPerWeek, 0, rdsMinutesPerWeek} {
			from := w.start + shift
			if from < backupStart+length && backupStart < from+w.length {
				return true
			}
		}
	}
	return false
}

// rdsCheckMaintenanceWindow states why a PreferredMaintenanceWindow breaks
// the constraints the ModifyDBInstance reference states, or "".
func rdsCheckMaintenanceWindow(value, backupWindow string) string {
	window, ok := rdsParseMaintenanceWindow(value)
	if !ok {
		return "Invalid maintenance window format: " + value + ". The format must be ddd:hh24:mi-ddd:hh24:mi."
	}
	if window.length < 30 {
		return "The maintenance window must be at least 30 minutes."
	}
	if window.overlapsBackupWindow(backupWindow) {
		return "The backup window and maintenance window must not overlap."
	}
	return ""
}

// rdsMaintenanceBlocks are the 8-hour blocks, in UTC minutes from midnight,
// from which Amazon RDS draws each Region's default maintenance windows, as
// the Amazon RDS User Guide lists them.
var rdsMaintenanceBlocks = map[string]int{
	"us-east-1": 3 * 60, "us-east-2": 3 * 60, "us-west-1": 6 * 60, "us-west-2": 6 * 60, "ca-central-1": 3 * 60,
	"sa-east-1": 0, "eu-west-1": 22 * 60, "eu-west-2": 22 * 60, "eu-central-1": 21 * 60,
	"ap-south-1": 16*60 + 30, "ap-southeast-1": 14 * 60, "ap-southeast-2": 12 * 60, "ap-northeast-1": 13 * 60,
}

// rdsDefaultMaintenanceWindow is a 30-minute window Amazon RDS picks at random
// from the Region's 8-hour block on a random day of the week, clear of the
// instance's backup window.
func rdsDefaultMaintenanceWindow(backupWindow string) string {
	blockStart, known := rdsMaintenanceBlocks[awsRegion()]
	blockLength := 8 * 60
	if !known {
		blockStart, blockLength = 0, 24*60
	}
	day := rand.IntN(7)
	var candidates []rdsMaintenanceWindow
	for offset := 0; offset+30 <= blockLength; offset += 30 {
		window := rdsMaintenanceWindow{start: day*24*60 + blockStart + offset, length: 30}
		if !window.overlapsBackupWindow(backupWindow) {
			candidates = append(candidates, window)
		}
	}
	window := rdsMaintenanceWindow{start: day*24*60 + blockStart, length: 30}
	if len(candidates) > 0 {
		window = candidates[rand.IntN(len(candidates))]
	}
	return rdsFormatWeeklyTime(window.start) + "-" + rdsFormatWeeklyTime(window.start+window.length)
}

// rdsRequestedMaintenanceWindow is a new instance's PreferredMaintenanceWindow:
// the one the request names, which must hold to the constraints, or a default.
func rdsRequestedMaintenanceWindow(r *http.Request, backupWindow string) (string, string) {
	value := strings.ToLower(r.FormValue("PreferredMaintenanceWindow"))
	if value == "" {
		return rdsDefaultMaintenanceWindow(backupWindow), ""
	}
	return value, rdsCheckMaintenanceWindow(value, backupWindow)
}

// rdsNextWindowStart is the window's first start after now.
func rdsNextWindowStart(window rdsMaintenanceWindow, now time.Time) time.Time {
	now = now.UTC()
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).
		AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	start := monday.Add(time.Duration(window.start) * time.Minute)
	for !start.After(now) {
		start = start.AddDate(0, 0, 7)
	}
	return start
}

// rdsNextMaintenance is how long from now until the window applies pending
// changes: at once when now lies in the window with at least 30 minutes of it
// left, as the ModifyDBInstance reference requires, and otherwise at its next
// start.
func rdsNextMaintenance(value string, now time.Time) (time.Duration, bool) {
	window, ok := rdsParseMaintenanceWindow(value)
	if !ok {
		return 0, false
	}
	next := rdsNextWindowStart(window, now)
	current := next.AddDate(0, 0, -7)
	if end := current.Add(time.Duration(window.length) * time.Minute); end.Sub(now) >= 30*time.Minute {
		return 0, true
	}
	return next.Sub(now), true
}

var rdsMaintenanceTimers sync.Map

// rdsArmMaintenance arms the instance's maintenance window for its pending
// modifications, replacing a timer an earlier window armed.
func rdsArmMaintenance(id string) {
	if previous, ok := rdsMaintenanceTimers.LoadAndDelete(id); ok {
		if timer, isTimer := previous.(*bg.Timer); isTimer {
			timer.Stop()
		}
	}
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.PendingModifications.empty() {
		return
	}
	delay, ok := rdsNextMaintenance(instance.PreferredMaintenanceWindow, time.Now())
	if !ok {
		log.Printf("Amazon RDS %s: no maintenance window in %q", id, instance.PreferredMaintenanceWindow)
		return
	}
	resourceID := instance.DbiResourceId
	rdsMaintenanceTimers.Store(id, bg.AfterFunc(delay, func() { rdsRunMaintenance(id, resourceID) }))
}

// rdsRunMaintenance applies an instance's pending modifications in its
// maintenance window. An instance that is not available then waits for the
// next window.
func rdsRunMaintenance(id, resourceID string) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.DbiResourceId != resourceID || instance.PendingModifications.empty() {
		return
	}
	if instance.DBInstanceStatus != "available" {
		if window, parsed := rdsParseMaintenanceWindow(instance.PreferredMaintenanceWindow); parsed {
			delay := time.Until(rdsNextWindowStart(window, time.Now()))
			rdsMaintenanceTimers.Store(id, bg.AfterFunc(delay, func() { rdsRunMaintenance(id, resourceID) }))
		}
		return
	}
	pending := *instance.PendingModifications
	instance.PendingModifications = nil
	retentionBefore := instance.BackupRetentionPeriod
	rdsApplyModifications(&instance, pending)
	id = rdsStoreModifiedInstance(id, instance)
	rdsAfterModifications(id, instance, retentionBefore != instance.BackupRetentionPeriod)
}

// rdsStoreModifiedInstance stores a modified instance record — under its new
// identifier when the modification renames it, which takes effect for the
// API at once while the restart moves its engine and data — and returns the
// identifier the instance answers to.
func rdsStoreModifiedInstance(id string, instance RDSInstance) string {
	if restart := instance.Restart; restart != nil && restart.DBInstanceIdentifier != "" && restart.PreviousIdentifier == "" &&
		instance.DBInstanceStatus == "modifying" {
		newID := restart.DBInstanceIdentifier
		if _, taken := rdsInstances.Get(newID); !taken {
			if stored, ok := rdsInstances.Get(id); ok && stored.DbiResourceId == instance.DbiResourceId {
				instance.BaseBackups = stored.BaseBackups
			}
			restart.PreviousIdentifier = id
			rdsRenameInstance(&instance, newID)
			rdsArmMaintenance(id)
			rdsArmMaintenance(newID)
			return newID
		}
		log.Printf("Amazon RDS %s: DB instance %s already exists, so the rename waits", id, newID)
		restart.DBInstanceIdentifier = ""
		if restart.Port == 0 {
			instance.DBInstanceStatus, instance.Restart = restart.Status, nil
		}
		pending := instance.PendingModifications.merge(RDSPendingModifications{DBInstanceIdentifier: newID})
		instance.PendingModifications = &pending
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != instance.DbiResourceId {
			return
		}
		bases := stored.BaseBackups
		*stored = instance
		stored.BaseBackups = bases
	})
	return id
}

// rdsApplyModifications applies changes to the instance record: an engine
// version upgrade leaves the instance upgrading, and a class change or a CA
// change on a running engine, a rename and a port move leave it modifying
// until rdsRestartModifiedInstance has restarted the engine with them.
func rdsApplyModifications(instance *RDSInstance, changes RDSPendingModifications) {
	rdsApplyStorage(instance, changes)
	if changes.BackupRetentionPeriod != nil {
		instance.BackupRetentionPeriod = *changes.BackupRetentionPeriod
	}
	if changes.MultiAZ != nil {
		instance.MultiAZ = *changes.MultiAZ
	}
	if changes.DBSubnetGroupName != "" {
		instance.DBSubnetGroupName = changes.DBSubnetGroupName
	}
	if changes.OptionGroupName != "" {
		instance.OptionGroupName = changes.OptionGroupName
	}
	certificateChanged := changes.CACertificateIdentifier != "" && changes.CACertificateIdentifier != instance.CACertificateIdentifier &&
		!changes.KeepCertificateUntilRestart
	if changes.CACertificateIdentifier != "" {
		instance.CACertificateIdentifier = changes.CACertificateIdentifier
	}
	if changes.DBInstanceIdentifier != "" && changes.DBInstanceIdentifier != instance.DBInstanceIdentifier {
		instance.Restart = rdsRestartFor(instance)
		instance.Restart.DBInstanceIdentifier = changes.DBInstanceIdentifier
	}
	classChanged := changes.DBInstanceClass != "" && changes.DBInstanceClass != instance.DBInstanceClass
	if changes.DBInstanceClass != "" {
		instance.DBInstanceClass = changes.DBInstanceClass
	}
	switch version := changes.EngineVersion; {
	case version == "" || version == instance.EngineVersion:
	case !rdsVersionedEngine(instance.Engine) || len(instance.MasterUserSecret) == 0:
		instance.EngineVersion = version
	default:
		instance.PendingEngineVersion = version
		instance.DBInstanceStatus = "upgrading"
		return
	}
	if _, served := rdsLoadDataPlane(instance.DBInstanceIdentifier); (classChanged || certificateChanged) && served {
		instance.DBInstanceStatus = "modifying"
	}
	if instance.Restart != nil {
		instance.DBInstanceStatus = "modifying"
	}
}

// rdsRestartFor is the restart record of an instance a port move or a rename
// restarts, which returns it to the status it has now.
func rdsRestartFor(instance *RDSInstance) *RDSRestartModifications {
	if instance.Restart != nil {
		return instance.Restart
	}
	status := instance.DBInstanceStatus
	if status == "modifying" || status == "upgrading" {
		status = "available"
	}
	return &RDSRestartModifications{Status: status}
}

// rdsAfterModifications starts the work the applied changes left: the engine
// upgrade or the restart with the modified settings, and the automated
// backups a retention change turns on or off.
func rdsAfterModifications(id string, instance RDSInstance, backupsChanged bool) {
	switch instance.DBInstanceStatus {
	case "upgrading":
		bg.Go(func() { rdsUpgradeInstanceEngine(id) })
	case "modifying":
		bg.Go(func() { rdsRestartModifiedInstance(id, instance.DbiResourceId) })
	}
	if plane, ok := rdsLoadDataPlane(id); ok && backupsChanged {
		plane.backups.schedule()
		bg.Go(func() {
			if err := plane.backups.expire(time.Now()); err != nil {
				log.Printf("Amazon RDS %s: expire automated backups: %v", id, err)
			}
		})
		rdsTakeFirstInstanceBackup(id)
	}
}

// rdsRestartModifiedInstance restarts a modifying instance's engine with its
// modified settings — its DB instance class, its CA certificate, the port its
// endpoint moves to and the identifier it is renamed to — which also applies
// its DB parameter group's pending-reboot parameters, and returns the
// instance to the status it had. An instance that runs no engine takes the
// port and the identifier without a restart.
func rdsRestartModifiedInstance(id, resourceID string) {
	instance, ok := rdsInstances.Get(id)
	if !ok || instance.DbiResourceId != resourceID || instance.DBInstanceStatus != "modifying" {
		return
	}
	if pending := instance.Restart; pending != nil && pending.DBInstanceIdentifier != "" && pending.PreviousIdentifier == "" {
		// An engine upgrade ran first under the old identifier.
		if _, taken := rdsInstances.Get(pending.DBInstanceIdentifier); taken {
			log.Printf("Amazon RDS %s: DB instance %s already exists, so the rename does not apply", id, pending.DBInstanceIdentifier)
			pending.DBInstanceIdentifier = ""
		} else {
			pending.PreviousIdentifier = id
			rdsRenameInstance(&instance, pending.DBInstanceIdentifier)
			rdsArmMaintenance(id)
			id = instance.DBInstanceIdentifier
			rdsArmMaintenance(id)
		}
	}
	restart := RDSRestartModifications{Status: "available"}
	if instance.Restart != nil {
		restart = *instance.Restart
	}
	runningID := id
	if restart.PreviousIdentifier != "" {
		runningID = restart.PreviousIdentifier
	}
	// A restart a previous process left finds no engine running, and starts
	// the one an available instance has.
	served := rdsInstanceEndpointServed(instance, runningID) ||
		(instance.Restart != nil && restart.Status == "available" && !rdsIsAurora(instance.Engine))
	var err error
	if served {
		err = rdsStopDataPlane(runningID, false)
	}
	if restart.Port != 0 {
		instance.Port = restart.Port
	}
	if err == nil && runningID != id && !rdsIsAurora(instance.Engine) {
		err = rdsMoveInstanceVolume(runningID, id)
	}
	if served && err == nil {
		err = rdsStartInstanceEngine(&instance)
	}
	status := restart.Status
	if served {
		status = "available"
	}
	if err != nil {
		log.Printf("Amazon RDS %s: restart with the modified settings: %v", id, err)
		status = "failed"
	}
	rdsInstances.Update(id, func(stored *RDSInstance) {
		if stored.DbiResourceId != resourceID || stored.DBInstanceStatus != "modifying" {
			return
		}
		stored.Endpoint, stored.Port, stored.EngineParameters = instance.Endpoint, instance.Port, instance.EngineParameters
		stored.ServerCertificateValidTill = instance.ServerCertificateValidTill
		stored.Restart = nil
		stored.DBInstanceStatus = status
	})
	if status == "available" {
		rdsTakeFirstInstanceBackup(id)
	}
}

// rdsRecoverMaintenance gives a record an earlier simulator kept without a
// maintenance window its default one, and arms each instance's window for the
// changes it holds.
func rdsRecoverMaintenance() {
	for _, instance := range rdsInstances.List() {
		if instance.PreferredMaintenanceWindow == "" {
			window := rdsDefaultMaintenanceWindow(instance.PreferredBackupWindow)
			rdsInstances.Update(instance.DBInstanceIdentifier, func(stored *RDSInstance) {
				if stored.PreferredMaintenanceWindow == "" {
					stored.PreferredMaintenanceWindow = window
				}
			})
		}
		if !instance.PendingModifications.empty() {
			rdsArmMaintenance(instance.DBInstanceIdentifier)
		}
	}
}
