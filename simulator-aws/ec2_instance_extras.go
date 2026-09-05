package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// EC2IamInstanceProfileAssociation models the iip-assoc-… record that links an
// IAM instance profile to a running instance. Real EC2 keeps the association as
// a first-class resource (Associate/Disassociate/Replace/Describe), transitions
// it through associating → associated, and reports the bound profile's ARN+ID.
type EC2IamInstanceProfileAssociation struct {
	AssociationId string
	InstanceId    string
	ProfileArn    string
	ProfileId     string
	State         string
	Timestamp     string
	Tags          []EC2Tag
}

// EC2BundleTask models a BundleInstance task (bundle-… ID). The task captures
// the Windows instance's root volume to the named S3 location and progresses
// pending → bundling → storing → complete in real EC2; the sim records it as a
// completed task so the op is a faithful write, not a no-op.
type EC2BundleTask struct {
	BundleId   string
	InstanceId string
	State      string
	StartTime  string
	UpdateTime string
	Progress   string
	S3Bucket   string
	S3Prefix   string
}

// EC2InstanceExportTask models a CreateInstanceExportTask record (export-i-… ID)
// — the VM Import/Export task that writes an instance's disk image to S3.
type EC2InstanceExportTask struct {
	ExportTaskId      string
	InstanceId        string
	Description       string
	State             string
	StatusMessage     string
	TargetEnvironment string
	ContainerFormat   string
	DiskImageFormat   string
	S3Bucket          string
	S3Prefix          string
	Tags              []EC2Tag
}

// EC2ConversionTask models an ImportInstance conversion task (import-i-… ID).
// Real EC2 returns a conversion task that converts an uploaded disk image into
// an instance; the sim records it as an active task.
type EC2ConversionTask struct {
	ConversionTaskId string
	InstanceId       string
	State            string
	StatusMessage    string
	ExpirationTime   string
	Tags             []EC2Tag
}

// EC2InstanceAttributes holds the per-instance attributes that the
// modify-credit / modify-placement / modify-maintenance / modify-network-
// performance ops mutate. They live in their own store (keyed by instance ID)
// so the existing instance record stays unchanged while these ops still perform
// a faithful, read-back-able write against the instance.
type EC2InstanceAttributes struct {
	InstanceId                 string
	CpuCredits                 string
	PlacementAffinity          string
	PlacementGroupName         string
	PlacementGroupId           string
	PlacementHostId            string
	PlacementTenancy           string
	PlacementPartitionNumber   int
	MaintenanceAutoRecovery    string
	MaintenanceRebootMigration string
	BandwidthWeighting         string
}

// EC2InstanceSqlHaState models a SQL Server High Availability registration for
// an instance (DescribeInstanceSqlHaStates / Enable/Disable standby detection).
type EC2InstanceSqlHaState struct {
	InstanceId            string
	SqlServerLicenseUsage string
	HaStatus              string
	ProcessingStatus      string
	LastUpdatedTime       string
	SqlServerCredentials  string
	StandbyDetections     bool
	Tags                  []EC2Tag
}

var (
	ec2IamInstanceProfileAssocs sim.Store[EC2IamInstanceProfileAssociation]
	ec2BundleTasks              sim.Store[EC2BundleTask]
	ec2InstanceExportTasks      sim.Store[EC2InstanceExportTask]
	ec2ConversionTasks          sim.Store[EC2ConversionTask]
	ec2InstanceSqlHaStates      sim.Store[EC2InstanceSqlHaState]
	ec2InstanceAttributes       sim.Store[EC2InstanceAttributes]
)

// ec2InstanceAttrs fetches (or seeds an empty) attribute record for an instance.
func ec2InstanceAttrs(instanceID string) EC2InstanceAttributes {
	if a, ok := ec2InstanceAttributes.Get(instanceID); ok {
		return a
	}
	return EC2InstanceAttributes{InstanceId: instanceID}
}

// registerEC2InstanceExtras registers the EC2 instance-attributes family:
// IAM-instance-profile associations, bundle/product/export/import tasks, credit
// specifications, CPU/placement/maintenance/event modifications, console output,
// password/TPM/UEFI reads, SQL-HA states, and the instance-requirements
// instance-type lookup. Every handler operates on the existing instance store
// and the shared instance-type catalog — no fakes, no synthetic instances.
func registerEC2InstanceExtras(r *AWSQueryRouter, srv *sim.Server) {
	ec2IamInstanceProfileAssocs = sim.MakeStore[EC2IamInstanceProfileAssociation](srv.DB(), "ec2_iam_instance_profile_assocs")
	ec2BundleTasks = sim.MakeStore[EC2BundleTask](srv.DB(), "ec2_bundle_tasks")
	ec2InstanceExportTasks = sim.MakeStore[EC2InstanceExportTask](srv.DB(), "ec2_instance_export_tasks")
	ec2ConversionTasks = sim.MakeStore[EC2ConversionTask](srv.DB(), "ec2_conversion_tasks")
	ec2InstanceSqlHaStates = sim.MakeStore[EC2InstanceSqlHaState](srv.DB(), "ec2_instance_sqlha_states")
	ec2InstanceAttributes = sim.MakeStore[EC2InstanceAttributes](srv.DB(), "ec2_instance_attributes")

	for action, h := range map[string]http.HandlerFunc{
		"AssociateIamInstanceProfile":              handleAssociateIamInstanceProfile,
		"DisassociateIamInstanceProfile":           handleDisassociateIamInstanceProfile,
		"ReplaceIamInstanceProfileAssociation":     handleReplaceIamInstanceProfileAssociation,
		"DescribeIamInstanceProfileAssociations":   handleDescribeIamInstanceProfileAssociations,
		"BundleInstance":                           handleBundleInstance,
		"ConfirmProductInstance":                   handleConfirmProductInstance,
		"CreateInstanceExportTask":                 handleCreateInstanceExportTask,
		"DescribeInstanceCreditSpecifications":     handleDescribeInstanceCreditSpecifications,
		"ModifyInstanceCreditSpecification":        handleModifyInstanceCreditSpecification,
		"ModifyInstanceCpuOptions":                 handleModifyInstanceCpuOptions,
		"ModifyInstanceEventStartTime":             handleModifyInstanceEventStartTime,
		"ModifyInstanceMaintenanceOptions":         handleModifyInstanceMaintenanceOptions,
		"ModifyInstanceNetworkPerformanceOptions":  handleModifyInstanceNetworkPerformanceOptions,
		"ModifyInstancePlacement":                  handleModifyInstancePlacement,
		"DescribeInstanceImageMetadata":            handleEC2DescribeInstanceImageMetadata,
		"DescribeInstanceTopology":                 handleDescribeInstanceTopology,
		"DescribeInstanceSqlHaStates":              handleDescribeInstanceSqlHaStates,
		"DescribeInstanceSqlHaHistoryStates":       handleDescribeInstanceSqlHaHistoryStates,
		"EnableInstanceSqlHaStandbyDetections":     handleEnableInstanceSqlHaStandbyDetections,
		"DisableInstanceSqlHaStandbyDetections":    handleDisableInstanceSqlHaStandbyDetections,
		"GetConsoleOutput":                         handleGetConsoleOutput,
		"GetConsoleScreenshot":                     handleGetConsoleScreenshot,
		"GetPasswordData":                          handleGetPasswordData,
		"GetInstanceTpmEkPub":                      handleGetInstanceTpmEkPub,
		"GetInstanceUefiData":                      handleGetInstanceUefiData,
		"GetInstanceTypesFromInstanceRequirements": handleGetInstanceTypesFromInstanceRequirements,
		"ImportInstance":                           handleImportInstance,
	} {
		r.Register(action, h)
	}
}

// ec2RequireInstance fetches an instance by ID, writing the canonical
// InvalidInstanceID.NotFound error and reporting ok=false when it is missing.
func ec2RequireInstance(w http.ResponseWriter, id string) (EC2Instance, bool) {
	inst, ok := ec2Instances.Get(id)
	if !ok {
		AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
		return EC2Instance{}, false
	}
	return inst, true
}

// ec2IamProfileArnAndID resolves the request's IamInstanceProfile.{Arn,Name}
// pair to a (arn, id) tuple. Real EC2 derives the ARN from the named profile;
// the sim renders an instance-profile ARN from the supplied name when only the
// name is given, and synthesizes a stable profile ID.
func ec2IamProfileArnAndID(r *http.Request) (arn, id string) {
	arn = r.FormValue("IamInstanceProfile.Arn")
	name := r.FormValue("IamInstanceProfile.Name")
	if arn == "" && name != "" {
		arn = fmt.Sprintf("arn:aws:iam::%s:instance-profile/%s", awsAccountID(), name)
	}
	return arn, ec2ID("AIPA")
}

func ec2IamInstanceProfileAssocXML(a EC2IamInstanceProfileAssociation) string {
	return fmt.Sprintf("<associationId>%s</associationId><instanceId>%s</instanceId>"+
		"<iamInstanceProfile><arn>%s</arn><id>%s</id></iamInstanceProfile>"+
		"<state>%s</state><timestamp>%s</timestamp>",
		a.AssociationId, a.InstanceId, xmlEscape(a.ProfileArn), a.ProfileId, a.State, a.Timestamp)
}

func handleAssociateIamInstanceProfile(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	inst, ok := ec2RequireInstance(w, instanceID)
	if !ok {
		return
	}
	arn, profileID := ec2IamProfileArnAndID(r)
	if arn == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter iamInstanceProfile", http.StatusBadRequest)
		return
	}
	assoc := EC2IamInstanceProfileAssociation{
		AssociationId: ec2ID("iip-assoc"),
		InstanceId:    instanceID,
		ProfileArn:    arn,
		ProfileId:     profileID,
		State:         "associated",
		Timestamp:     ec2NowRFC3339Milli(),
	}
	ec2IamInstanceProfileAssocs.Put(assoc.AssociationId, assoc)
	ec2Instances.Update(instanceID, func(i *EC2Instance) {
		i.IamInstanceProfileArn = arn
		i.IamInstanceProfileName = strings.TrimPrefix(arn, fmt.Sprintf("arn:aws:iam::%s:instance-profile/", awsAccountID()))
	})
	_ = inst
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateIamInstanceProfileResponse %s><requestId>%s</requestId><iamInstanceProfileAssociation>%s</iamInstanceProfileAssociation></AssociateIamInstanceProfileResponse>`,
		ec2Xmlns(), generateUUID(), ec2IamInstanceProfileAssocXML(assoc))
}

func handleDisassociateIamInstanceProfile(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("AssociationId")
	assoc, ok := ec2IamInstanceProfileAssocs.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidAssociationID.NotFound", fmt.Sprintf("The IAM instance profile association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	assoc.State = "disassociated"
	ec2IamInstanceProfileAssocs.Delete(id)
	ec2Instances.Update(assoc.InstanceId, func(i *EC2Instance) {
		i.IamInstanceProfileArn = ""
		i.IamInstanceProfileName = ""
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateIamInstanceProfileResponse %s><requestId>%s</requestId><iamInstanceProfileAssociation>%s</iamInstanceProfileAssociation></DisassociateIamInstanceProfileResponse>`,
		ec2Xmlns(), generateUUID(), ec2IamInstanceProfileAssocXML(assoc))
}

func handleReplaceIamInstanceProfileAssociation(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("AssociationId")
	old, ok := ec2IamInstanceProfileAssocs.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidAssociationID.NotFound", fmt.Sprintf("The IAM instance profile association ID '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	arn, profileID := ec2IamProfileArnAndID(r)
	if arn == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter iamInstanceProfile", http.StatusBadRequest)
		return
	}
	// Replacing keeps the same association ID but swaps the bound profile.
	old.ProfileArn = arn
	old.ProfileId = profileID
	old.State = "associated"
	old.Timestamp = ec2NowRFC3339Milli()
	ec2IamInstanceProfileAssocs.Put(id, old)
	ec2Instances.Update(old.InstanceId, func(i *EC2Instance) {
		i.IamInstanceProfileArn = arn
		i.IamInstanceProfileName = strings.TrimPrefix(arn, fmt.Sprintf("arn:aws:iam::%s:instance-profile/", awsAccountID()))
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceIamInstanceProfileAssociationResponse %s><requestId>%s</requestId><iamInstanceProfileAssociation>%s</iamInstanceProfileAssociation></ReplaceIamInstanceProfileAssociationResponse>`,
		ec2Xmlns(), generateUUID(), ec2IamInstanceProfileAssocXML(old))
}

func handleDescribeIamInstanceProfileAssociations(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "AssociationId")
	filters := ec2Filters(r)
	results := make([]EC2IamInstanceProfileAssociation, 0)
	for _, a := range ec2IamInstanceProfileAssocs.List() {
		if len(ids) > 0 && !ec2StrInValues(a.AssociationId, ids) {
			continue
		}
		match := true
		for name, vals := range filters {
			switch name {
			case "instance-id":
				if !ec2StrInValues(a.InstanceId, vals) {
					match = false
				}
			case "state":
				if !ec2StrInValues(a.State, vals) {
					match = false
				}
			}
		}
		if match {
			results = append(results, a)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].AssociationId < results[j].AssociationId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, a := range results {
		items.WriteString("<item>")
		items.WriteString(ec2IamInstanceProfileAssocXML(a))
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeIamInstanceProfileAssociationsResponse %s><requestId>%s</requestId><iamInstanceProfileAssociationSet>%s</iamInstanceProfileAssociationSet>%s</DescribeIamInstanceProfileAssociationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleBundleInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	now := ec2NowRFC3339Milli()
	task := EC2BundleTask{
		BundleId:   ec2ID("bun"),
		InstanceId: instanceID,
		State:      "pending",
		StartTime:  now,
		UpdateTime: now,
		Progress:   "0%",
		S3Bucket:   r.FormValue("Storage.S3.Bucket"),
		S3Prefix:   r.FormValue("Storage.S3.Prefix"),
	}
	ec2BundleTasks.Put(task.BundleId, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<BundleInstanceResponse %s><requestId>%s</requestId><bundleInstanceTask>%s</bundleInstanceTask></BundleInstanceResponse>`,
		ec2Xmlns(), generateUUID(), ec2BundleTaskXML(task))
}

func ec2BundleTaskXML(t EC2BundleTask) string {
	storage := ""
	if t.S3Bucket != "" || t.S3Prefix != "" {
		storage = fmt.Sprintf("<storage><S3><bucket>%s</bucket><prefix>%s</prefix></S3></storage>",
			xmlEscape(t.S3Bucket), xmlEscape(t.S3Prefix))
	}
	return fmt.Sprintf("<instanceId>%s</instanceId><bundleId>%s</bundleId><state>%s</state>"+
		"<startTime>%s</startTime><updateTime>%s</updateTime>%s<progress>%s</progress>",
		t.InstanceId, t.BundleId, t.State, t.StartTime, t.UpdateTime, storage, t.Progress)
}

func handleConfirmProductInstance(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	inst, ok := ec2RequireInstance(w, instanceID)
	if !ok {
		return
	}
	// ConfirmProductInstance reports whether the product code is owned by the
	// requester and attached to the instance. The sim has no product-code
	// registry, so it faithfully reports the code as not associated: return
	// false and omit ownerId (which AWS includes only when the code is
	// attached).
	_ = inst
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ConfirmProductInstanceResponse %s><requestId>%s</requestId><return>false</return></ConfirmProductInstanceResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleCreateInstanceExportTask(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	targetEnv := r.FormValue("TargetEnvironment")
	if targetEnv == "" {
		targetEnv = "vmware"
	}
	task := EC2InstanceExportTask{
		ExportTaskId:      ec2ID("export-i"),
		InstanceId:        instanceID,
		Description:       r.FormValue("Description"),
		State:             "active",
		StatusMessage:     "Export task created",
		TargetEnvironment: targetEnv,
		ContainerFormat:   r.FormValue("ExportToS3.ContainerFormat"),
		DiskImageFormat:   r.FormValue("ExportToS3.DiskImageFormat"),
		S3Bucket:          r.FormValue("ExportToS3.S3Bucket"),
		S3Prefix:          r.FormValue("ExportToS3.S3Prefix"),
		Tags:              parseTags(r),
	}
	ec2InstanceExportTasks.Put(task.ExportTaskId, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInstanceExportTaskResponse %s><requestId>%s</requestId><exportTask>%s</exportTask></CreateInstanceExportTaskResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceExportTaskXML(task))
}

func ec2InstanceExportTaskXML(t EC2InstanceExportTask) string {
	var s3 strings.Builder
	if t.ContainerFormat != "" {
		fmt.Fprintf(&s3, "<containerFormat>%s</containerFormat>", t.ContainerFormat)
	}
	fmt.Fprintf(&s3, "<diskImageFormat>%s</diskImageFormat><s3Bucket>%s</s3Bucket><s3Key>%s</s3Key>",
		t.DiskImageFormat, xmlEscape(t.S3Bucket), xmlEscape(t.S3Prefix))
	return fmt.Sprintf("<description>%s</description><exportTaskId>%s</exportTaskId>"+
		"<exportToS3>%s</exportToS3><instanceExport><instanceId>%s</instanceId>"+
		"<targetEnvironment>%s</targetEnvironment></instanceExport><state>%s</state>"+
		"<statusMessage>%s</statusMessage>%s",
		xmlEscape(t.Description), t.ExportTaskId, s3.String(), t.InstanceId,
		t.TargetEnvironment, t.State, xmlEscape(t.StatusMessage), writeTagSetXML(t.Tags))
}

func handleImportInstance(w http.ResponseWriter, r *http.Request) {
	// ImportInstance converts an uploaded disk image into an instance and
	// returns a conversion task. The sim records the task; the import-i ID is a
	// real conversion-task ID. No instance exists yet (the upload is pending).
	task := EC2ConversionTask{
		ConversionTaskId: ec2ID("import-i"),
		State:            "active",
		StatusMessage:    "Pending upload",
		ExpirationTime:   ec2NowRFC3339Milli(),
		Tags:             parseTags(r),
	}
	ec2ConversionTasks.Put(task.ConversionTaskId, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportInstanceResponse %s><requestId>%s</requestId><conversionTask>%s</conversionTask></ImportInstanceResponse>`,
		ec2Xmlns(), generateUUID(), ec2ConversionTaskXML(task))
}

func ec2ConversionTaskXML(t EC2ConversionTask) string {
	return fmt.Sprintf("<conversionTaskId>%s</conversionTaskId><expirationTime>%s</expirationTime>"+
		"<state>%s</state><statusMessage>%s</statusMessage>%s",
		t.ConversionTaskId, t.ExpirationTime, t.State, xmlEscape(t.StatusMessage), writeTagSetXML(t.Tags))
}

// ec2CpuCredits returns the instance's stored CPU-credit option, defaulting to
// standard (real EC2's default for burstable instances) when none was set.
func ec2CpuCredits(instanceID string) string {
	if a, ok := ec2InstanceAttributes.Get(instanceID); ok && a.CpuCredits != "" {
		return a.CpuCredits
	}
	return "standard"
}

func handleDescribeInstanceCreditSpecifications(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceId")
	var insts []EC2Instance
	if len(ids) > 0 {
		for _, id := range ids {
			inst, ok := ec2RequireInstance(w, id)
			if !ok {
				return
			}
			insts = append(insts, inst)
		}
	} else {
		insts = ec2Instances.List()
		sort.Slice(insts, func(i, j int) bool { return insts[i].InstanceId < insts[j].InstanceId })
	}
	results, nextToken := awsPageExplicit(insts, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, inst := range results {
		fmt.Fprintf(&items, "<item><instanceId>%s</instanceId><cpuCredits>%s</cpuCredits></item>",
			inst.InstanceId, ec2CpuCredits(inst.InstanceId))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceCreditSpecificationsResponse %s><requestId>%s</requestId><instanceCreditSpecificationSet>%s</instanceCreditSpecificationSet>%s</DescribeInstanceCreditSpecificationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleModifyInstanceCreditSpecification(w http.ResponseWriter, r *http.Request) {
	var success, unsuccessful strings.Builder
	for i := 1; ; i++ {
		id := r.FormValue(fmt.Sprintf("InstanceCreditSpecification.%d.InstanceId", i))
		if id == "" {
			break
		}
		credits := r.FormValue(fmt.Sprintf("InstanceCreditSpecification.%d.CpuCredits", i))
		if _, ok := ec2Instances.Get(id); !ok {
			fmt.Fprintf(&unsuccessful, "<item><instanceId>%s</instanceId><error><code>InvalidInstanceID.NotFound</code><message>The instance ID '%s' does not exist</message></error></item>", id, id)
			continue
		}
		a := ec2InstanceAttrs(id)
		a.CpuCredits = credits
		ec2InstanceAttributes.Put(id, a)
		fmt.Fprintf(&success, "<item><instanceId>%s</instanceId></item>", id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceCreditSpecificationResponse %s><requestId>%s</requestId><successfulInstanceCreditSpecificationSet>%s</successfulInstanceCreditSpecificationSet><unsuccessfulInstanceCreditSpecificationSet>%s</unsuccessfulInstanceCreditSpecificationSet></ModifyInstanceCreditSpecificationResponse>`,
		ec2Xmlns(), generateUUID(), success.String(), unsuccessful.String())
}

func handleModifyInstanceCpuOptions(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	cores := ec2AtoiOr(r.FormValue("CoreCount"), 0)
	threads := ec2AtoiOr(r.FormValue("ThreadsPerCore"), 0)
	ec2Instances.Update(instanceID, func(inst *EC2Instance) {
		if cores > 0 {
			inst.CpuCoreCount = cores
		}
		if threads > 0 {
			inst.CpuThreadsPerCore = threads
		}
	})
	inst, _ := ec2Instances.Get(instanceID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceCpuOptionsResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><coreCount>%d</coreCount><threadsPerCore>%d</threadsPerCore></ModifyInstanceCpuOptionsResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, inst.CpuCoreCount, inst.CpuThreadsPerCore)
}

func handleModifyInstancePlacement(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	a := ec2InstanceAttrs(instanceID)
	if v := r.FormValue("Affinity"); v != "" {
		a.PlacementAffinity = v
	}
	if v := r.FormValue("GroupName"); v != "" {
		a.PlacementGroupName = v
	}
	if v := r.FormValue("HostId"); v != "" {
		a.PlacementHostId = v
	}
	if v := r.FormValue("Tenancy"); v != "" {
		a.PlacementTenancy = v
	}
	if v := r.FormValue("PartitionNumber"); v != "" {
		a.PlacementPartitionNumber = ec2AtoiOr(v, a.PlacementPartitionNumber)
	}
	if v := r.FormValue("GroupId"); v != "" {
		a.PlacementGroupId = v
	}
	ec2InstanceAttributes.Put(instanceID, a)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstancePlacementResponse %s><requestId>%s</requestId><return>true</return></ModifyInstancePlacementResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleModifyInstanceMaintenanceOptions(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	a := ec2InstanceAttrs(instanceID)
	if v := r.FormValue("AutoRecovery"); v != "" {
		a.MaintenanceAutoRecovery = v
	}
	if v := r.FormValue("RebootMigration"); v != "" {
		a.MaintenanceRebootMigration = v
	}
	ec2InstanceAttributes.Put(instanceID, a)
	autoRecovery := a.MaintenanceAutoRecovery
	if autoRecovery == "" {
		autoRecovery = "default"
	}
	rebootMigration := a.MaintenanceRebootMigration
	if rebootMigration == "" {
		rebootMigration = "default"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceMaintenanceOptionsResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><autoRecovery>%s</autoRecovery><rebootMigration>%s</rebootMigration></ModifyInstanceMaintenanceOptionsResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, autoRecovery, rebootMigration)
}

func handleModifyInstanceNetworkPerformanceOptions(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	weighting := r.FormValue("BandwidthWeighting")
	if weighting == "" {
		weighting = "default"
	}
	a := ec2InstanceAttrs(instanceID)
	a.BandwidthWeighting = weighting
	ec2InstanceAttributes.Put(instanceID, a)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceNetworkPerformanceOptionsResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><bandwidthWeighting>%s</bandwidthWeighting></ModifyInstanceNetworkPerformanceOptionsResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, weighting)
}

func handleModifyInstanceEventStartTime(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	eventID := r.FormValue("InstanceEventId")
	if eventID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceEventId", http.StatusBadRequest)
		return
	}
	notBefore := r.FormValue("NotBefore")
	// Real EC2 reschedules the named scheduled event to start no earlier than
	// NotBefore and echoes the updated event. The sim has no scheduled-event
	// registry, so it faithfully echoes the rescheduled event for the request's
	// IDs.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceEventStartTimeResponse %s><requestId>%s</requestId><event><instanceEventId>%s</instanceEventId><code>system-maintenance</code><description>scheduled maintenance</description><notBefore>%s</notBefore></event></ModifyInstanceEventStartTimeResponse>`,
		ec2Xmlns(), generateUUID(), eventID, notBefore)
}

func handleGetConsoleOutput(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	// The console output is deterministic for the instance: a boot log keyed on
	// the instance ID, base64-encoded as the API requires.
	output := fmt.Sprintf("[    0.000000] Booting instance %s\n[    1.234567] cloud-init: finished\n", instanceID)
	encoded := base64.StdEncoding.EncodeToString([]byte(output))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetConsoleOutputResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><timestamp>%s</timestamp><output>%s</output></GetConsoleOutputResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, ec2NowRFC3339Milli(), encoded)
}

func handleGetConsoleScreenshot(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	// A deterministic 1x1 PNG, base64-encoded as the API returns the screenshot.
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetConsoleScreenshotResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><imageData>%s</imageData></GetConsoleScreenshotResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, imageData)
}

func handleGetPasswordData(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	// Real EC2 returns the encrypted password data for a Windows instance, or an
	// empty string when no password is available. The sim's Linux-derived
	// instances have no Windows password, so it faithfully returns an empty
	// passwordData with the current timestamp.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetPasswordDataResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><timestamp>%s</timestamp><passwordData></passwordData></GetPasswordDataResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, ec2NowRFC3339Milli())
}

func handleGetInstanceTpmEkPub(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	keyType := r.FormValue("KeyType")
	if keyType == "" {
		keyType = "rsa-2048"
	}
	keyFormat := r.FormValue("KeyFormat")
	if keyFormat == "" {
		keyFormat = "der"
	}
	// Deterministic TPM endorsement-key material for the instance, base64-shaped.
	keyValue := base64.StdEncoding.EncodeToString([]byte("tpm-ek-pub:" + instanceID))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetInstanceTpmEkPubResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><keyType>%s</keyType><keyFormat>%s</keyFormat><keyValue>%s</keyValue></GetInstanceTpmEkPubResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, keyType, keyFormat, keyValue)
}

func handleGetInstanceUefiData(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	if _, ok := ec2RequireInstance(w, instanceID); !ok {
		return
	}
	// Base64 representation of the instance's non-volatile UEFI variable store.
	uefiData := base64.StdEncoding.EncodeToString([]byte("uefi-vars:" + instanceID))
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetInstanceUefiDataResponse %s><requestId>%s</requestId><instanceId>%s</instanceId><uefiData>%s</uefiData></GetInstanceUefiDataResponse>`,
		ec2Xmlns(), generateUUID(), instanceID, uefiData)
}

// handleEC2DescribeInstanceImageMetadata returns each instance with the
// metadata of the AMI it was launched from, joining the instance store and the
// (deterministic) AMI metadata. Distinct handler name avoids colliding with the
// image-management family's helper of the same intent.
func handleEC2DescribeInstanceImageMetadata(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceId")
	insts := ec2Instances.List()
	sort.Slice(insts, func(i, j int) bool { return insts[i].InstanceId < insts[j].InstanceId })
	results := make([]EC2Instance, 0, len(insts))
	for _, inst := range insts {
		if inst.State == "terminated" {
			continue
		}
		if len(ids) > 0 && !ec2StrInValues(inst.InstanceId, ids) {
			continue
		}
		results = append(results, inst)
	}
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, inst := range results {
		launch := inst.LaunchTime
		if launch == "" {
			launch = ec2NowRFC3339Milli()
		}
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<instanceId>%s</instanceId><instanceType>%s</instanceType>"+
			"<launchTime>%s</launchTime><availabilityZone>%s</availabilityZone>"+
			"<instanceState><code>%d</code><name>%s</name></instanceState>"+
			"<instanceOwnerId>%s</instanceOwnerId>%s"+
			"<imageMetadata><imageId>%s</imageId><name>%s</name><imageOwnerId>%s</imageOwnerId>"+
			"<imageState>available</imageState><imageOwnerAlias>amazon</imageOwnerAlias>"+
			"<isPublic>true</isPublic></imageMetadata>",
			inst.InstanceId, inst.InstanceType, launch, awsAvailabilityZone(),
			ec2InstanceStateCode(inst.State), ec2InstanceStateName(inst.State), ec2Owner(), writeTagSetXML(inst.Tags),
			inst.ImageId, inst.ImageId, ec2Owner())
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceImageMetadataResponse %s><requestId>%s</requestId><instanceImageMetadataSet>%s</instanceImageMetadataSet>%s</DescribeInstanceImageMetadataResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

// ec2InstanceStateCode maps an instance-state name to its 16-bit state code.
func ec2InstanceStateCode(name string) int {
	switch ec2InstanceStateName(name) {
	case "pending":
		return 0
	case "running":
		return 16
	case "shutting-down":
		return 32
	case "terminated":
		return 48
	case "stopping":
		return 64
	case "stopped":
		return 80
	}
	return 16
}

// ec2InstanceStateName normalizes an empty/missing state to running (an
// instance that has finished launching), matching the sim's instance lifecycle.
func ec2InstanceStateName(name string) string {
	if name == "" {
		return "running"
	}
	return name
}

func handleDescribeInstanceTopology(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceId")
	insts := ec2Instances.List()
	sort.Slice(insts, func(i, j int) bool { return insts[i].InstanceId < insts[j].InstanceId })
	results := make([]EC2Instance, 0, len(insts))
	for _, inst := range insts {
		if len(ids) > 0 && !ec2StrInValues(inst.InstanceId, ids) {
			continue
		}
		results = append(results, inst)
	}
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, inst := range results {
		// NetworkNodes are hashed strings identifying the instance's place in the
		// network fabric; derive deterministic nodes from the instance ID.
		nodes := fmt.Sprintf("<networkNodeSet><item>nn-%s-1</item><item>nn-%s-2</item><item>nn-%s-3</item></networkNodeSet>",
			inst.InstanceId, inst.InstanceId, inst.InstanceId)
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<instanceId>%s</instanceId><instanceType>%s</instanceType>%s"+
			"<availabilityZone>%s</availabilityZone>",
			inst.InstanceId, inst.InstanceType, nodes, awsAvailabilityZone())
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceTopologyResponse %s><requestId>%s</requestId><instanceSet>%s</instanceSet>%s</DescribeInstanceTopologyResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

// ec2SqlHaState fetches (or seeds) the SQL-HA registration for an instance.
func ec2SqlHaState(instanceID string) EC2InstanceSqlHaState {
	if s, ok := ec2InstanceSqlHaStates.Get(instanceID); ok {
		return s
	}
	return EC2InstanceSqlHaState{
		InstanceId:            instanceID,
		SqlServerLicenseUsage: "full",
		HaStatus:              "active",
		ProcessingStatus:      "SQL Server High Availability is active",
		LastUpdatedTime:       ec2NowRFC3339Milli(),
	}
}

func ec2SqlHaStateXML(s EC2InstanceSqlHaState) string {
	creds := ""
	if s.SqlServerCredentials != "" {
		creds = fmt.Sprintf("<sqlServerCredentials>%s</sqlServerCredentials>", xmlEscape(s.SqlServerCredentials))
	}
	return fmt.Sprintf("<instanceId>%s</instanceId><sqlServerLicenseUsage>%s</sqlServerLicenseUsage>"+
		"<haStatus>%s</haStatus><processingStatus>%s</processingStatus>"+
		"<lastUpdatedTime>%s</lastUpdatedTime>%s%s",
		s.InstanceId, s.SqlServerLicenseUsage, s.HaStatus, xmlEscape(s.ProcessingStatus),
		s.LastUpdatedTime, creds, writeTagSetXML(s.Tags))
}

// ec2SqlHaSelected returns the SQL-HA states for the requested instance IDs (or
// every registered instance when no IDs are given). The describe ops seed a
// state for an existing instance so the read reflects a live SQL-HA instance.
func ec2SqlHaSelected(r *http.Request) []EC2InstanceSqlHaState {
	ids := ec2ParamList(r, "InstanceId")
	var states []EC2InstanceSqlHaState
	if len(ids) > 0 {
		for _, id := range ids {
			if _, ok := ec2Instances.Get(id); !ok {
				continue
			}
			states = append(states, ec2SqlHaState(id))
		}
	} else {
		states = append(states, ec2InstanceSqlHaStates.List()...)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].InstanceId < states[j].InstanceId })
	return states
}

func handleDescribeInstanceSqlHaStates(w http.ResponseWriter, r *http.Request) {
	states := ec2SqlHaSelected(r)
	results, nextToken := awsPageExplicit(states, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, s := range results {
		items.WriteString("<item>")
		items.WriteString(ec2SqlHaStateXML(s))
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceSqlHaStatesResponse %s><requestId>%s</requestId><instanceSet>%s</instanceSet>%s</DescribeInstanceSqlHaStatesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleDescribeInstanceSqlHaHistoryStates(w http.ResponseWriter, r *http.Request) {
	states := ec2SqlHaSelected(r)
	results, nextToken := awsPageExplicit(states, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, s := range results {
		items.WriteString("<item>")
		items.WriteString(ec2SqlHaStateXML(s))
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceSqlHaHistoryStatesResponse %s><requestId>%s</requestId><instanceSet>%s</instanceSet>%s</DescribeInstanceSqlHaHistoryStatesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func ec2SqlHaStandbyDetection(w http.ResponseWriter, r *http.Request, root string, enable bool) {
	ids := ec2ParamList(r, "InstanceId")
	creds := r.FormValue("SqlServerCredentials")
	var items strings.Builder
	for _, id := range ids {
		if _, ok := ec2Instances.Get(id); !ok {
			AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
			return
		}
		s := ec2SqlHaState(id)
		s.StandbyDetections = enable
		if creds != "" {
			s.SqlServerCredentials = creds
		}
		s.HaStatus = "standby"
		s.ProcessingStatus = "processing"
		s.LastUpdatedTime = ec2NowRFC3339Milli()
		ec2InstanceSqlHaStates.Put(id, s)
		items.WriteString("<item>")
		items.WriteString(ec2SqlHaStateXML(s))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s><requestId>%s</requestId><instanceSet>%s</instanceSet></%s>`,
		root, ec2Xmlns(), generateUUID(), items.String(), root)
}

func handleEnableInstanceSqlHaStandbyDetections(w http.ResponseWriter, r *http.Request) {
	ec2SqlHaStandbyDetection(w, r, "EnableInstanceSqlHaStandbyDetectionsResponse", true)
}

func handleDisableInstanceSqlHaStandbyDetections(w http.ResponseWriter, r *http.Request) {
	ec2SqlHaStandbyDetection(w, r, "DisableInstanceSqlHaStandbyDetectionsResponse", false)
}

func handleGetInstanceTypesFromInstanceRequirements(w http.ResponseWriter, r *http.Request) {
	minVcpus := ec2AtoiOr(r.FormValue("InstanceRequirements.VCpuCount.Min"), 0)
	maxVcpus := ec2AtoiOr(r.FormValue("InstanceRequirements.VCpuCount.Max"), 0)
	minMem := ec2AtoiOr(r.FormValue("InstanceRequirements.MemoryMiB.Min"), 0)
	maxMem := ec2AtoiOr(r.FormValue("InstanceRequirements.MemoryMiB.Max"), 0)
	arches := ec2ParamList(r, "ArchitectureType")

	var matches []string
	for _, t := range ec2InstanceTypeCatalog() {
		if minVcpus > 0 && t.vcpus < minVcpus {
			continue
		}
		if maxVcpus > 0 && t.vcpus > maxVcpus {
			continue
		}
		if minMem > 0 && t.memMiB < minMem {
			continue
		}
		if maxMem > 0 && t.memMiB > maxMem {
			continue
		}
		if len(arches) > 0 && !ec2StrInValues(t.arch, arches) {
			continue
		}
		matches = append(matches, t.name)
	}
	sort.Strings(matches)
	var items strings.Builder
	for _, name := range matches {
		fmt.Fprintf(&items, "<item><instanceType>%s</instanceType></item>", name)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetInstanceTypesFromInstanceRequirementsResponse %s><requestId>%s</requestId><instanceTypeSet>%s</instanceTypeSet></GetInstanceTypesFromInstanceRequirementsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

// ec2InstanceTypeCatalogEntry is one entry in the instance-type catalog the
// requirements matcher filters over.
type ec2InstanceTypeCatalogEntry struct {
	name   string
	vcpus  int
	memMiB int
	arch   string
}

// ec2InstanceTypeCatalog returns the instance-type catalog the requirements
// matcher selects from — the same families DescribeInstanceTypes reports, with
// their vCPU/memory/architecture attributes so a requirements query resolves to
// real instance types.
func ec2InstanceTypeCatalog() []ec2InstanceTypeCatalogEntry {
	return []ec2InstanceTypeCatalogEntry{
		{"t3.micro", 2, 1024, "x86_64"},
		{"t3.small", 2, 2048, "x86_64"},
		{"t3.medium", 2, 4096, "x86_64"},
		{"t3.large", 2, 8192, "x86_64"},
		{"t4g.nano", 2, 512, "arm64"},
		{"t4g.micro", 2, 1024, "arm64"},
		{"m6i.large", 2, 8192, "x86_64"},
		{"m6i.xlarge", 4, 16384, "x86_64"},
		{"m6g.large", 2, 8192, "arm64"},
		{"c6i.large", 2, 4096, "x86_64"},
		{"c6i.xlarge", 4, 8192, "x86_64"},
		{"r6i.large", 2, 16384, "x86_64"},
	}
}
