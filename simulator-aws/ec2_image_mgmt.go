package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// This file implements the EC2 AMI / image-management family on top of the
// existing AMI (ec2Images) and instance (ec2Instances) stores. Each feature
// that carries state the EC2Image record does not (watermarks, deprecation,
// fast-launch, usage reports, export/import tasks, recycle-bin) gets its own
// store so DescribeX faithfully echoes what the mutating op recorded, exactly
// as real EC2 does.

// EC2ImageWatermark is one watermark attached to an AMI by AttachImageWatermark.
type EC2ImageWatermark struct {
	WatermarkKey            string
	WatermarkName           string
	ImageId                 string
	SourceImageRegion       string
	SourceImageId           string
	SourceImageCreationTime string
	WatermarkCreationTime   string
}

// EC2ImageUsageReport is an image-usage report resource produced by
// CreateImageUsageReport over a single AMI. The report settles to "available"
// and DescribeImageUsageReportEntries enumerates per-resource-type usage counts.
type EC2ImageUsageReport struct {
	ReportId       string
	ImageId        string
	ResourceTypes  []EC2ImageUsageResourceType
	AccountIds     []string
	State          string
	StateReason    string
	CreationTime   string
	ExpirationTime string
	Tags           []EC2Tag
}

// EC2ImageUsageResourceType records one resource type the report scans, with the
// per-account usage count it found, used to render both the report's
// resourceTypeSet and the report's entries.
type EC2ImageUsageResourceType struct {
	ResourceType string
	UsageCount   int64
	AccountId    string
}

// EC2ExportImageTask is an AMI->S3 export task created by ExportImage. The sim
// has no disk image to write, so the task settles to "completed" immediately,
// mirroring how a client polls DescribeExportImageTasks until completion.
type EC2ExportImageTask struct {
	ExportImageTaskId string
	ImageId           string
	Description       string
	DiskImageFormat   string
	RoleName          string
	S3Bucket          string
	S3Prefix          string
	Status            string
	StatusMessage     string
	Progress          string
	Tags              []EC2Tag
}

// EC2ImportImageTask is an S3->AMI import task created by ImportImage. It settles
// to "completed", at which point a backing AMI id is recorded so a client can
// register/launch the imported image.
type EC2ImportImageTask struct {
	ImportTaskId  string
	ImageId       string
	Architecture  string
	Description   string
	Hypervisor    string
	Platform      string
	LicenseType   string
	Encrypted     bool
	Status        string
	StatusMessage string
	Progress      string
	SnapshotId    string
	DeviceName    string
	Format        string
	DiskImageSize float64
	S3Bucket      string
	S3Key         string
	Url           string
	BootMode      string
	Tags          []EC2Tag
}

// EC2FastLaunch is the Windows-AMI fast-launch configuration set by
// EnableFastLaunch. The sim settles the config straight to "enabled"; disabling
// removes the row.
type EC2FastLaunch struct {
	ImageId               string
	ResourceType          string
	TargetResourceCount   int
	LaunchTemplateId      string
	LaunchTemplateName    string
	LaunchTemplateVersion string
	MaxParallelLaunches   int
	OwnerId               string
	State                 string
	StateTransitionReason string
	StateTransitionTime   string
}

// EC2RecycleBinImage is an AMI sitting in the Recycle Bin (a retention-rule
// holding pen a deregistered AMI lands in). The store is honest-empty unless an
// AMI was sent to the bin.
type EC2RecycleBinImage struct {
	ImageId             string
	Name                string
	Description         string
	RecycleBinEnterTime string
	RecycleBinExitTime  string
}

// EC2ImageDeprecation records the DeprecationTime EnableImageDeprecation set on
// an AMI (DisableImageDeprecation clears it). Kept beside ec2Images because the
// EC2Image record has no deprecation field.
type EC2ImageDeprecation struct {
	ImageId         string
	DeprecationTime string
}

var (
	ec2ImageWatermarks   sim.Store[EC2ImageWatermark]
	ec2ImageUsageReports sim.Store[EC2ImageUsageReport]
	ec2ExportImageTasks  sim.Store[EC2ExportImageTask]
	ec2ImportImageTasks  sim.Store[EC2ImportImageTask]
	ec2FastLaunchImages  sim.Store[EC2FastLaunch]
	ec2RecycleBinImages  sim.Store[EC2RecycleBinImage]
	ec2ImageDeprecations sim.Store[EC2ImageDeprecation]
)

// ec2NowMillisXML formats now as the millisecond-precision UTC timestamp EC2
// emits for MillisecondDateTime members (e.g. 2024-01-01T00:00:00.000Z).
func ec2NowMillisXML() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func registerEC2ImageMgmt(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2ImageWatermarks = sim.MakeStore[EC2ImageWatermark](srv.DB(), "ec2_image_watermarks")
	ec2ImageUsageReports = sim.MakeStore[EC2ImageUsageReport](srv.DB(), "ec2_image_usage_reports")
	ec2ExportImageTasks = sim.MakeStore[EC2ExportImageTask](srv.DB(), "ec2_export_image_tasks")
	ec2ImportImageTasks = sim.MakeStore[EC2ImportImageTask](srv.DB(), "ec2_import_image_tasks")
	ec2FastLaunchImages = sim.MakeStore[EC2FastLaunch](srv.DB(), "ec2_fast_launch_images")
	ec2RecycleBinImages = sim.MakeStore[EC2RecycleBinImage](srv.DB(), "ec2_recycle_bin_images")
	ec2ImageDeprecations = sim.MakeStore[EC2ImageDeprecation](srv.DB(), "ec2_image_deprecations")

	for action, h := range map[string]http.HandlerFunc{
		"AttachImageWatermark":            handleAttachImageWatermark,
		"DetachImageWatermark":            handleDetachImageWatermark,
		"CancelImageLaunchPermission":     handleCancelImageLaunchPermission,
		"CreateImageUsageReport":          handleCreateImageUsageReport,
		"DeleteImageUsageReport":          handleDeleteImageUsageReport,
		"DescribeImageUsageReports":       handleDescribeImageUsageReports,
		"DescribeImageUsageReportEntries": handleDescribeImageUsageReportEntries,
		"DescribeExportImageTasks":        handleDescribeExportImageTasks,
		"DescribeImportImageTasks":        handleDescribeImportImageTasks,
		"EnableFastLaunch":                handleEnableFastLaunch,
		"DisableFastLaunch":               handleDisableFastLaunch,
		"DescribeFastLaunchImages":        handleDescribeFastLaunchImages,
		"DescribeImageReferences":         handleDescribeImageReferences,
		"GetImageAncestry":                handleGetImageAncestry,
		"EnableImageDeprecation":          handleEnableImageDeprecation,
		"DisableImageDeprecation":         handleDisableImageDeprecation,
		"ListImagesInRecycleBin":          handleListImagesInRecycleBin,
	} {
		r.Register(action, h)
	}
}

// ec2RequireImage resolves an AMI id from the request, writing the standard
// InvalidAMIID error and returning ok=false when it is missing or unknown.
func ec2RequireImage(w http.ResponseWriter, r *http.Request) (EC2Image, bool) {
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return EC2Image{}, false
	}
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return EC2Image{}, false
	}
	return img, true
}

// -------------------- Watermarks --------------------

func handleAttachImageWatermark(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	wm := EC2ImageWatermark{
		WatermarkKey:            ec2ID("wmk"),
		WatermarkName:           r.FormValue("WatermarkName"),
		ImageId:                 img.ImageId,
		SourceImageRegion:       awsRegion(),
		SourceImageId:           img.ImageId,
		SourceImageCreationTime: img.CreationDate,
		WatermarkCreationTime:   ec2NowMillisXML(),
	}
	ec2ImageWatermarks.Put(wm.WatermarkKey, wm)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AttachImageWatermarkResponse %s><requestId>%s</requestId><watermarkKey>%s</watermarkKey></AttachImageWatermarkResponse>`,
		ec2Xmlns(), generateUUID(), wm.WatermarkKey)
}

func handleDetachImageWatermark(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	key := r.FormValue("WatermarkKey")
	// A specific key detaches that watermark; otherwise all watermarks on the AMI.
	for _, wm := range ec2ImageWatermarks.List() {
		if wm.ImageId != img.ImageId {
			continue
		}
		if key != "" && wm.WatermarkKey != key {
			continue
		}
		ec2ImageWatermarks.Delete(wm.WatermarkKey)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DetachImageWatermarkResponse %s><requestId>%s</requestId><return>true</return></DetachImageWatermarkResponse>`,
		ec2Xmlns(), generateUUID())
}

// -------------------- Launch permission --------------------

// handleCancelImageLaunchPermission resets an AMI's launch permissions to
// private (the cancel primitive that revokes shared-account access).
func handleCancelImageLaunchPermission(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	img.Public = false
	ec2Images.Put(img.ImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CancelImageLaunchPermissionResponse %s><requestId>%s</requestId><return>true</return></CancelImageLaunchPermissionResponse>`,
		ec2Xmlns(), generateUUID())
}

// -------------------- Image usage reports --------------------

func handleCreateImageUsageReport(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	// AccountIds serialize as a flat AccountId list whose member is named UserId
	// (AccountId.UserId.N); the CLI's shorthand emits AccountId.N. Read both.
	accountIds := ec2ParamList(r, "AccountId.UserId")
	if len(accountIds) == 0 {
		accountIds = ec2ParamList(r, "AccountId")
	}
	if len(accountIds) == 0 {
		accountIds = []string{ec2Owner()}
	}
	// Each requested resource type is scanned once per account; the sim derives
	// a real usage count from the instance store (instances launched from this
	// AMI for ec2:Instance), defaulting to 0 for types with no sim resources.
	var resTypes []EC2ImageUsageResourceType
	for i := 1; ; i++ {
		rt := r.FormValue(fmt.Sprintf("ResourceType.%d.ResourceType", i))
		if rt == "" {
			break
		}
		for _, acct := range accountIds {
			resTypes = append(resTypes, EC2ImageUsageResourceType{
				ResourceType: rt,
				UsageCount:   ec2ImageUsageCount(rt, img.ImageId),
				AccountId:    acct,
			})
		}
	}
	if len(resTypes) == 0 {
		for _, acct := range accountIds {
			resTypes = append(resTypes, EC2ImageUsageResourceType{
				ResourceType: "ec2:Instance",
				UsageCount:   ec2ImageUsageCount("ec2:Instance", img.ImageId),
				AccountId:    acct,
			})
		}
	}
	now := time.Now().UTC()
	rep := EC2ImageUsageReport{
		ReportId:       ec2ID("imageusagereport"),
		ImageId:        img.ImageId,
		ResourceTypes:  resTypes,
		AccountIds:     accountIds,
		State:          "available",
		CreationTime:   now.Format("2006-01-02T15:04:05.000Z"),
		ExpirationTime: now.Add(72 * time.Hour).Format("2006-01-02T15:04:05.000Z"),
		Tags:           parseTags(r),
	}
	ec2ImageUsageReports.Put(rep.ReportId, rep)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateImageUsageReportResponse %s><requestId>%s</requestId><reportId>%s</reportId></CreateImageUsageReportResponse>`,
		ec2Xmlns(), generateUUID(), rep.ReportId)
}

// ec2ImageUsageCount derives a real usage count for a resource type from the
// existing stores. ec2:Instance counts non-terminated instances launched from
// the AMI; other types have no sim resources, so the count is 0.
func ec2ImageUsageCount(resourceType, imageID string) int64 {
	if resourceType == "ec2:Instance" {
		var n int64
		for _, inst := range ec2Instances.List() {
			if inst.ImageId == imageID && inst.State != "terminated" {
				n++
			}
		}
		return n
	}
	return 0
}

func handleDeleteImageUsageReport(w http.ResponseWriter, r *http.Request) {
	reportID := r.FormValue("ReportId")
	if reportID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ReportId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2ImageUsageReports.Get(reportID); !ok {
		ec2ErrorXML(w, "InvalidImageUsageReportId.NotFound", fmt.Sprintf("The image usage report %q does not exist", reportID), http.StatusBadRequest)
		return
	}
	ec2ImageUsageReports.Delete(reportID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteImageUsageReportResponse %s><requestId>%s</requestId><return>true</return></DeleteImageUsageReportResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeImageUsageReports(w http.ResponseWriter, r *http.Request) {
	reportIDs := ec2ParamList(r, "ReportId")
	imageIDs := ec2ParamList(r, "ImageId")
	results := make([]EC2ImageUsageReport, 0)
	for _, rep := range ec2ImageUsageReports.List() {
		if len(reportIDs) > 0 && !ec2StrInValues(rep.ReportId, reportIDs) {
			continue
		}
		if len(imageIDs) > 0 && !ec2StrInValues(rep.ImageId, imageIDs) {
			continue
		}
		results = append(results, rep)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ReportId < results[j].ReportId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, rep := range results {
		items.WriteString("<item>")
		items.WriteString(ec2ImageUsageReportXML(rep))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImageUsageReportsResponse %s><requestId>%s</requestId><imageUsageReportSet>%s</imageUsageReportSet>%s</DescribeImageUsageReportsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

func ec2ImageUsageReportXML(rep EC2ImageUsageReport) string {
	var rts strings.Builder
	rts.WriteString("<resourceTypeSet>")
	// resourceTypeSet enumerates the distinct resource types the report scans.
	seen := map[string]bool{}
	for _, rt := range rep.ResourceTypes {
		if seen[rt.ResourceType] {
			continue
		}
		seen[rt.ResourceType] = true
		rts.WriteString("<item><resourceType>")
		rts.WriteString(xmlEscape(rt.ResourceType))
		rts.WriteString("</resourceType></item>")
	}
	rts.WriteString("</resourceTypeSet>")
	var accts strings.Builder
	accts.WriteString("<accountIdSet>")
	for _, a := range rep.AccountIds {
		accts.WriteString("<item>")
		accts.WriteString(xmlEscape(a))
		accts.WriteString("</item>")
	}
	accts.WriteString("</accountIdSet>")
	reason := ""
	if rep.StateReason != "" {
		reason = fmt.Sprintf("<stateReason>%s</stateReason>", xmlEscape(rep.StateReason))
	}
	return fmt.Sprintf("<imageId>%s</imageId><reportId>%s</reportId>%s%s<state>%s</state>%s<creationTime>%s</creationTime><expirationTime>%s</expirationTime>%s",
		rep.ImageId, rep.ReportId, rts.String(), accts.String(), rep.State, reason,
		rep.CreationTime, rep.ExpirationTime, writeTagSetXML(rep.Tags))
}

func handleDescribeImageUsageReportEntries(w http.ResponseWriter, r *http.Request) {
	reportIDs := ec2ParamList(r, "ReportId")
	type entry struct {
		ResourceType       string
		ReportId           string
		UsageCount         int64
		AccountId          string
		ImageId            string
		ReportCreationTime string
	}
	results := make([]entry, 0)
	for _, rep := range ec2ImageUsageReports.List() {
		if len(reportIDs) > 0 && !ec2StrInValues(rep.ReportId, reportIDs) {
			continue
		}
		for _, rt := range rep.ResourceTypes {
			results = append(results, entry{
				ResourceType:       rt.ResourceType,
				ReportId:           rep.ReportId,
				UsageCount:         rt.UsageCount,
				AccountId:          rt.AccountId,
				ImageId:            rep.ImageId,
				ReportCreationTime: rep.CreationTime,
			})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].ReportId != results[j].ReportId {
			return results[i].ReportId < results[j].ReportId
		}
		return results[i].ResourceType < results[j].ResourceType
	})
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, e := range results {
		fmt.Fprintf(&items, "<item><resourceType>%s</resourceType><reportId>%s</reportId><usageCount>%d</usageCount><accountId>%s</accountId><imageId>%s</imageId><reportCreationTime>%s</reportCreationTime></item>",
			xmlEscape(e.ResourceType), e.ReportId, e.UsageCount, e.AccountId, e.ImageId, e.ReportCreationTime)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImageUsageReportEntriesResponse %s><requestId>%s</requestId><imageUsageReportEntrySet>%s</imageUsageReportEntrySet>%s</DescribeImageUsageReportEntriesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// -------------------- Export image tasks --------------------
//
// ExportImage / ImportImage themselves are registered by the AMI host file
// (ec2_hosts_images_vpc.go); this file owns the DescribeExportImageTasks /
// DescribeImportImageTasks read side over the export/import task stores.

// ec2ExportImageFieldsXML renders the fields of a DescribeExportImageTasks item
// (minus the wrapping element). An ExportImageTask carries fewer members than
// the ExportImage response that started it: the disk image format and the role
// the export assumes belong to the request and its immediate answer, and Amazon
// EC2 does not report either back on the task.
func ec2ExportImageFieldsXML(t EC2ExportImageTask) string {
	desc := ""
	if t.Description != "" {
		desc = fmt.Sprintf("<description>%s</description>", xmlEscape(t.Description))
	}
	status := ""
	if t.StatusMessage != "" {
		status = fmt.Sprintf("<statusMessage>%s</statusMessage>", xmlEscape(t.StatusMessage))
	}
	s3 := fmt.Sprintf("<s3ExportLocation><s3Bucket>%s</s3Bucket><s3Prefix>%s</s3Prefix></s3ExportLocation>",
		xmlEscape(t.S3Bucket), xmlEscape(t.S3Prefix))
	return fmt.Sprintf("%s<exportImageTaskId>%s</exportImageTaskId><imageId>%s</imageId><progress>%s</progress>%s<status>%s</status>%s%s",
		desc, t.ExportImageTaskId, t.ImageId, t.Progress, s3, t.Status, status, writeTagSetXML(t.Tags))
}

func handleDescribeExportImageTasks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ExportImageTaskId")
	results := make([]EC2ExportImageTask, 0)
	for _, t := range ec2ExportImageTasks.List() {
		if len(ids) > 0 && !ec2StrInValues(t.ExportImageTaskId, ids) {
			continue
		}
		results = append(results, t)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ExportImageTaskId < results[j].ExportImageTaskId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, t := range results {
		items.WriteString("<item>")
		items.WriteString(ec2ExportImageFieldsXML(t))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeExportImageTasksResponse %s><requestId>%s</requestId><exportImageTaskSet>%s</exportImageTaskSet>%s</DescribeExportImageTasksResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// -------------------- Import image tasks --------------------

func ec2ImportImageFieldsXML(t EC2ImportImageTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<architecture>%s</architecture>", t.Architecture)
	if t.Description != "" {
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(t.Description))
	}
	fmt.Fprintf(&b, "<encrypted>%t</encrypted>", t.Encrypted)
	fmt.Fprintf(&b, "<hypervisor>%s</hypervisor>", t.Hypervisor)
	fmt.Fprintf(&b, "<imageId>%s</imageId>", t.ImageId)
	fmt.Fprintf(&b, "<importTaskId>%s</importTaskId>", t.ImportTaskId)
	if t.LicenseType != "" {
		fmt.Fprintf(&b, "<licenseType>%s</licenseType>", xmlEscape(t.LicenseType))
	}
	fmt.Fprintf(&b, "<platform>%s</platform>", t.Platform)
	fmt.Fprintf(&b, "<progress>%s</progress>", t.Progress)
	// snapshotDetailSet describes the imported disk(s).
	b.WriteString("<snapshotDetailSet><item>")
	fmt.Fprintf(&b, "<deviceName>%s</deviceName>", t.DeviceName)
	fmt.Fprintf(&b, "<diskImageSize>%g</diskImageSize>", t.DiskImageSize)
	fmt.Fprintf(&b, "<format>%s</format>", t.Format)
	b.WriteString("<progress>100</progress>")
	fmt.Fprintf(&b, "<snapshotId>%s</snapshotId>", t.SnapshotId)
	b.WriteString("<status>completed</status>")
	if t.Url != "" {
		fmt.Fprintf(&b, "<url>%s</url>", xmlEscape(t.Url))
	}
	if t.S3Bucket != "" || t.S3Key != "" {
		fmt.Fprintf(&b, "<userBucket><s3Bucket>%s</s3Bucket><s3Key>%s</s3Key></userBucket>",
			xmlEscape(t.S3Bucket), xmlEscape(t.S3Key))
	}
	b.WriteString("</item></snapshotDetailSet>")
	fmt.Fprintf(&b, "<status>%s</status>", t.Status)
	if t.StatusMessage != "" {
		fmt.Fprintf(&b, "<statusMessage>%s</statusMessage>", xmlEscape(t.StatusMessage))
	}
	if t.BootMode != "" {
		fmt.Fprintf(&b, "<bootMode>%s</bootMode>", t.BootMode)
	}
	b.WriteString(writeTagSetXML(t.Tags))
	return b.String()
}

func handleDescribeImportImageTasks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ImportTaskId")
	results := make([]EC2ImportImageTask, 0)
	for _, t := range ec2ImportImageTasks.List() {
		if len(ids) > 0 && !ec2StrInValues(t.ImportTaskId, ids) {
			continue
		}
		results = append(results, t)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ImportTaskId < results[j].ImportTaskId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, t := range results {
		items.WriteString("<item>")
		items.WriteString(ec2ImportImageFieldsXML(t))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImportImageTasksResponse %s><requestId>%s</requestId><importImageTaskSet>%s</importImageTaskSet>%s</DescribeImportImageTasksResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// -------------------- Fast launch (Windows AMIs) --------------------

func handleEnableFastLaunch(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	resType := r.FormValue("ResourceType")
	if resType == "" {
		resType = "snapshot"
	}
	fl := EC2FastLaunch{
		ImageId:               img.ImageId,
		ResourceType:          resType,
		TargetResourceCount:   ec2AtoiOr(r.FormValue("SnapshotConfiguration.TargetResourceCount"), 5),
		LaunchTemplateId:      r.FormValue("LaunchTemplate.LaunchTemplateId"),
		LaunchTemplateName:    r.FormValue("LaunchTemplate.LaunchTemplateName"),
		LaunchTemplateVersion: r.FormValue("LaunchTemplate.Version"),
		MaxParallelLaunches:   ec2AtoiOr(r.FormValue("MaxParallelLaunches"), 6),
		OwnerId:               ec2Owner(),
		State:                 "enabled",
		StateTransitionReason: "Client.UserInitiated",
		StateTransitionTime:   ec2NowMillisXML(),
	}
	ec2FastLaunchImages.Put(fl.ImageId, fl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableFastLaunchResponse %s><requestId>%s</requestId>%s</EnableFastLaunchResponse>`,
		ec2Xmlns(), generateUUID(), ec2FastLaunchFieldsXML(fl))
}

func handleDisableFastLaunch(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	fl, ok := ec2FastLaunchImages.Get(img.ImageId)
	if !ok {
		ec2ErrorXML(w, "InvalidRequest", fmt.Sprintf("Fast launch is not enabled for image %q", img.ImageId), http.StatusBadRequest)
		return
	}
	fl.State = "disabling"
	fl.StateTransitionReason = "Client.UserInitiated"
	fl.StateTransitionTime = ec2NowMillisXML()
	ec2FastLaunchImages.Delete(img.ImageId)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableFastLaunchResponse %s><requestId>%s</requestId>%s</DisableFastLaunchResponse>`,
		ec2Xmlns(), generateUUID(), ec2FastLaunchFieldsXML(fl))
}

func ec2FastLaunchFieldsXML(fl EC2FastLaunch) string {
	lt := ""
	if fl.LaunchTemplateId != "" || fl.LaunchTemplateName != "" {
		ltid := ""
		if fl.LaunchTemplateId != "" {
			ltid = fmt.Sprintf("<launchTemplateId>%s</launchTemplateId>", fl.LaunchTemplateId)
		}
		ltname := ""
		if fl.LaunchTemplateName != "" {
			ltname = fmt.Sprintf("<launchTemplateName>%s</launchTemplateName>", xmlEscape(fl.LaunchTemplateName))
		}
		ver := ""
		if fl.LaunchTemplateVersion != "" {
			ver = fmt.Sprintf("<version>%s</version>", xmlEscape(fl.LaunchTemplateVersion))
		}
		lt = fmt.Sprintf("<launchTemplate>%s%s%s</launchTemplate>", ltid, ltname, ver)
	}
	return fmt.Sprintf("<imageId>%s</imageId><resourceType>%s</resourceType><snapshotConfiguration><targetResourceCount>%d</targetResourceCount></snapshotConfiguration>%s<maxParallelLaunches>%d</maxParallelLaunches><ownerId>%s</ownerId><state>%s</state><stateTransitionReason>%s</stateTransitionReason><stateTransitionTime>%s</stateTransitionTime>",
		fl.ImageId, fl.ResourceType, fl.TargetResourceCount, lt, fl.MaxParallelLaunches, fl.OwnerId,
		fl.State, fl.StateTransitionReason, fl.StateTransitionTime)
}

func handleDescribeFastLaunchImages(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ImageId")
	results := make([]EC2FastLaunch, 0)
	for _, fl := range ec2FastLaunchImages.List() {
		if len(ids) > 0 && !ec2StrInValues(fl.ImageId, ids) {
			continue
		}
		results = append(results, fl)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ImageId < results[j].ImageId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, fl := range results {
		items.WriteString("<item>")
		items.WriteString(ec2FastLaunchFieldsXML(fl))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFastLaunchImagesResponse %s><requestId>%s</requestId><fastLaunchImageSet>%s</fastLaunchImageSet>%s</DescribeFastLaunchImagesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// -------------------- Image references / instance metadata --------------------

// handleDescribeImageReferences returns the EC2 resources that reference each
// requested AMI. The sim derives instance references from the instance store.
func handleDescribeImageReferences(w http.ResponseWriter, r *http.Request) {
	imageIDs := ec2ParamList(r, "ImageId")
	type ref struct {
		ImageId      string
		ResourceType string
		Arn          string
	}
	refs := make([]ref, 0)
	for _, inst := range ec2Instances.List() {
		if inst.State == "terminated" {
			continue
		}
		if len(imageIDs) > 0 && !ec2StrInValues(inst.ImageId, imageIDs) {
			continue
		}
		if inst.ImageId == "" {
			continue
		}
		refs = append(refs, ref{
			ImageId:      inst.ImageId,
			ResourceType: "ec2:Instance",
			Arn:          fmt.Sprintf("arn:aws:ec2:%s:%s:instance/%s", awsRegion(), ec2Owner(), inst.InstanceId),
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Arn < refs[j].Arn })
	refs, nextToken := awsPageExplicit(refs, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, rf := range refs {
		fmt.Fprintf(&items, "<item><imageId>%s</imageId><resourceType>%s</resourceType><arn>%s</arn></item>",
			rf.ImageId, rf.ResourceType, xmlEscape(rf.Arn))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImageReferencesResponse %s><requestId>%s</requestId><imageReferenceSet>%s</imageReferenceSet>%s</DescribeImageReferencesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// DescribeInstanceImageMetadata is registered by the instance-extras file
// (ec2_instance_extras.go); this file owns the rest of the image-management
// family.

// -------------------- Image ancestry --------------------

// handleGetImageAncestry walks an AMI's copy chain (SourceImageId) back to its
// root, returning one entry per ancestor (newest first).
func handleGetImageAncestry(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	var items strings.Builder
	cur := img
	seen := map[string]bool{}
	for !seen[cur.ImageId] {
		seen[cur.ImageId] = true
		src := ""
		if cur.SourceImageId != "" {
			src = fmt.Sprintf("<sourceImageId>%s</sourceImageId><sourceImageRegion>%s</sourceImageRegion>", cur.SourceImageId, awsRegion())
		}
		creation := cur.CreationDate
		if creation == "" {
			creation = ec2NowMillisXML()
		}
		fmt.Fprintf(&items, "<item><creationDate>%s</creationDate><imageId>%s</imageId>%s</item>",
			creation, cur.ImageId, src)
		if cur.SourceImageId == "" {
			break
		}
		parent, ok := ec2Images.Get(cur.SourceImageId)
		if !ok {
			break
		}
		cur = parent
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetImageAncestryResponse %s><requestId>%s</requestId><imageAncestryEntrySet>%s</imageAncestryEntrySet></GetImageAncestryResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

// -------------------- Image deprecation --------------------

func handleEnableImageDeprecation(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	deprecateAt := r.FormValue("DeprecateAt")
	if deprecateAt == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter DeprecateAt", http.StatusBadRequest)
		return
	}
	ec2ImageDeprecations.Put(img.ImageId, EC2ImageDeprecation{ImageId: img.ImageId, DeprecationTime: deprecateAt})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableImageDeprecationResponse %s><requestId>%s</requestId><return>true</return></EnableImageDeprecationResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDisableImageDeprecation(w http.ResponseWriter, r *http.Request) {
	img, ok := ec2RequireImage(w, r)
	if !ok {
		return
	}
	ec2ImageDeprecations.Delete(img.ImageId)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableImageDeprecationResponse %s><requestId>%s</requestId><return>true</return></DisableImageDeprecationResponse>`,
		ec2Xmlns(), generateUUID())
}

// -------------------- Recycle bin --------------------

func handleListImagesInRecycleBin(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ImageId")
	results := make([]EC2RecycleBinImage, 0)
	for _, img := range ec2RecycleBinImages.List() {
		if len(ids) > 0 && !ec2StrInValues(img.ImageId, ids) {
			continue
		}
		results = append(results, img)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ImageId < results[j].ImageId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, img := range results {
		desc := ""
		if img.Description != "" {
			desc = fmt.Sprintf("<description>%s</description>", xmlEscape(img.Description))
		}
		fmt.Fprintf(&items, "<item><imageId>%s</imageId><name>%s</name>%s<recycleBinEnterTime>%s</recycleBinEnterTime><recycleBinExitTime>%s</recycleBinExitTime></item>",
			img.ImageId, xmlEscape(img.Name), desc, img.RecycleBinEnterTime, img.RecycleBinExitTime)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ListImagesInRecycleBinResponse %s><requestId>%s</requestId><imageSet>%s</imageSet>%s</ListImagesInRecycleBinResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), ec2NextTokenXML(nextToken))
}

// ec2NextTokenXML renders an optional <nextToken> element, omitted when empty.
func ec2NextTokenXML(token string) string {
	if token == "" {
		return ""
	}
	return "<nextToken>" + token + "</nextToken>"
}
