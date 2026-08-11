package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// EC2FpgaImage models an Amazon FPGA Image (AFI). CreateFpgaImage records a
// deterministic metadata record (afi-… global id agfi-…) that DescribeFpgaImages
// echoes back; the attribute set (description / name / loadPermissions) is
// mutated by ModifyFpgaImageAttribute and read by DescribeFpgaImageAttribute.
type EC2FpgaImage struct {
	FpgaImageId       string
	FpgaImageGlobalId string
	Name              string
	Description       string
	ShellVersion      string
	State             string
	CreateTime        string
	UpdateTime        string
	OwnerId           string
	Public            bool
	LoadPermissions   []EC2LoadPermission
	SourceFpgaImageId string
	Tags              []EC2Tag
}

// EC2LoadPermission is one (UserId | Group) grant on an FPGA image.
type EC2LoadPermission struct {
	UserId string
	Group  string
}

// EC2AllowedImagesSettings is the account-level Allowed AMIs settings singleton:
// the enablement state (enabled / audit-mode / disabled) plus the image-criteria
// allow-list. Stored under a fixed key because it is account-wide state.
type EC2AllowedImagesSettings struct {
	State    string // enabled | audit-mode | disabled
	Criteria []EC2ImageCriterion
}

// EC2ImageCriterion is one allow-list entry (image providers / marketplace
// product codes / image names) within the Allowed AMIs settings.
type EC2ImageCriterion struct {
	ImageProviders          []string
	MarketplaceProductCodes []string
	ImageNames              []string
}

// EC2StoreImageTask records a CreateStoreImageTask: an AMI store to an S3 bucket
// that DescribeStoreImageTasks reads back. The sim has no live S3-export backend,
// so the task is recorded as completed.
type EC2StoreImageTask struct {
	AmiId         string
	Bucket        string
	S3ObjectKey   string
	TaskStartTime string
	State         string
	Progress      int
}

const ec2AllowedImagesSettingsKey = "account"

var (
	ec2FpgaImages             sim.Store[EC2FpgaImage]
	ec2AllowedImagesSettings  sim.Store[EC2AllowedImagesSettings]
	ec2ImageBlockPublicAccess sim.Store[string]
	ec2ImageDeregProtection   sim.Store[string]
	ec2StoreImageTasks        sim.Store[EC2StoreImageTask]
)

// registerEC2ImagesFpga registers the FPGA-image, AMI access-control, and
// bundle/conversion/store-image-task ec2Query actions.
func registerEC2ImagesFpga(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2FpgaImages = sim.MakeStore[EC2FpgaImage](srv.DB(), "ec2_fpga_images")
	ec2AllowedImagesSettings = sim.MakeStore[EC2AllowedImagesSettings](srv.DB(), "ec2_allowed_images_settings")
	ec2ImageBlockPublicAccess = sim.MakeStore[string](srv.DB(), "ec2_image_block_public_access")
	ec2ImageDeregProtection = sim.MakeStore[string](srv.DB(), "ec2_image_dereg_protection")
	ec2StoreImageTasks = sim.MakeStore[EC2StoreImageTask](srv.DB(), "ec2_store_image_tasks")

	// FPGA images, AMI access-control (allowed-images settings, block-public-
	// access, deregistration protection), and bundle/conversion/store-image tasks.
	for action, h := range map[string]http.HandlerFunc{
		"CreateFpgaImage":                             handleCreateFpgaImage,
		"DescribeFpgaImages":                          handleDescribeFpgaImages,
		"CopyFpgaImage":                               handleCopyFpgaImage,
		"DeleteFpgaImage":                             handleDeleteFpgaImage,
		"DescribeFpgaImageAttribute":                  handleDescribeFpgaImageAttribute,
		"ModifyFpgaImageAttribute":                    handleModifyFpgaImageAttribute,
		"ResetFpgaImageAttribute":                     handleResetFpgaImageAttribute,
		"EnableAllowedImagesSettings":                 handleEnableAllowedImagesSettings,
		"DisableAllowedImagesSettings":                handleDisableAllowedImagesSettings,
		"GetAllowedImagesSettings":                    handleGetAllowedImagesSettings,
		"ReplaceImageCriteriaInAllowedImagesSettings": handleReplaceImageCriteriaInAllowedImagesSettings,
		"EnableImageBlockPublicAccess":                handleEnableImageBlockPublicAccess,
		"DisableImageBlockPublicAccess":               handleDisableImageBlockPublicAccess,
		"GetImageBlockPublicAccessState":              handleGetImageBlockPublicAccessState,
		"EnableImageDeregistrationProtection":         handleEnableImageDeregistrationProtection,
		"DisableImageDeregistrationProtection":        handleDisableImageDeregistrationProtection,
		"DescribeBundleTasks":                         handleDescribeBundleTasks,
		"CancelBundleTask":                            handleCancelBundleTask,
		"DescribeConversionTasks":                     handleDescribeConversionTasks,
		"CancelConversionTask":                        handleCancelConversionTask,
		"CreateStoreImageTask":                        handleCreateStoreImageTask,
		"DescribeStoreImageTasks":                     handleDescribeStoreImageTasks,
	} {
		r.Register(action, h)
	}
}

// -------------------- FPGA images --------------------

// ec2FpgaTagsXML renders an FPGA image's tags. The FpgaImage shape names the
// element <tags> (not <tagSet>), so it cannot reuse writeTagSetXML.
func ec2FpgaTagsXML(tags []EC2Tag) string {
	if len(tags) == 0 {
		return "<tags/>"
	}
	var b strings.Builder
	b.WriteString("<tags>")
	for _, t := range tags {
		fmt.Fprintf(&b, "<item><key>%s</key><value>%s</value></item>", xmlEscape(t.Key), xmlEscape(t.Value))
	}
	b.WriteString("</tags>")
	return b.String()
}

func ec2FpgaLoadPermissionsXML(perms []EC2LoadPermission) string {
	var b strings.Builder
	b.WriteString("<loadPermissions>")
	for _, p := range perms {
		b.WriteString("<item>")
		if p.UserId != "" {
			fmt.Fprintf(&b, "<userId>%s</userId>", p.UserId)
		}
		if p.Group != "" {
			fmt.Fprintf(&b, "<group>%s</group>", p.Group)
		}
		b.WriteString("</item>")
	}
	b.WriteString("</loadPermissions>")
	return b.String()
}

func ec2FpgaImageXML(img EC2FpgaImage) string {
	desc := ""
	if img.Description != "" {
		desc = fmt.Sprintf("<description>%s</description>", xmlEscape(img.Description))
	}
	name := ""
	if img.Name != "" {
		name = fmt.Sprintf("<name>%s</name>", xmlEscape(img.Name))
	}
	return fmt.Sprintf(`<item><fpgaImageId>%s</fpgaImageId><fpgaImageGlobalId>%s</fpgaImageGlobalId>%s%s<shellVersion>%s</shellVersion><state><code>%s</code></state><createTime>%s</createTime><updateTime>%s</updateTime><ownerId>%s</ownerId><public>%t</public><dataRetentionSupport>false</dataRetentionSupport>%s</item>`,
		img.FpgaImageId, img.FpgaImageGlobalId, name, desc, img.ShellVersion, img.State,
		img.CreateTime, img.UpdateTime, img.OwnerId, img.Public, ec2FpgaTagsXML(img.Tags))
}

func handleCreateFpgaImage(w http.ResponseWriter, r *http.Request) {
	now := ec2NowRFC3339Milli()
	img := EC2FpgaImage{
		FpgaImageId:       ec2ID("afi"),
		FpgaImageGlobalId: ec2ID("agfi"),
		Name:              r.FormValue("Name"),
		Description:       r.FormValue("Description"),
		ShellVersion:      "0x04261818",
		State:             "available",
		CreateTime:        now,
		UpdateTime:        now,
		OwnerId:           ec2Owner(),
		Public:            false,
		Tags:              parseTags(r),
	}
	ec2FpgaImages.Put(img.FpgaImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateFpgaImageResponse %s><requestId>%s</requestId><fpgaImageId>%s</fpgaImageId><fpgaImageGlobalId>%s</fpgaImageGlobalId></CreateFpgaImageResponse>`,
		ec2Xmlns(), generateUUID(), img.FpgaImageId, img.FpgaImageGlobalId)
}

func handleDescribeFpgaImages(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "FpgaImageId")
	filters := ec2Filters(r)
	results := make([]EC2FpgaImage, 0)
	for _, img := range ec2FpgaImages.List() {
		if len(ids) > 0 && !ec2StrInValues(img.FpgaImageId, ids) {
			continue
		}
		if !ec2FpgaImageMatchesFilters(img, filters) {
			continue
		}
		results = append(results, img)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].FpgaImageId < results[j].FpgaImageId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, img := range results {
		items.WriteString(ec2FpgaImageXML(img))
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFpgaImagesResponse %s><requestId>%s</requestId><fpgaImageSet>%s</fpgaImageSet>%s</DescribeFpgaImagesResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func ec2FpgaImageMatchesFilters(img EC2FpgaImage, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "fpga-image-id":
			if !ec2StrInValues(img.FpgaImageId, vals) {
				return false
			}
		case "fpga-image-global-id":
			if !ec2StrInValues(img.FpgaImageGlobalId, vals) {
				return false
			}
		case "name":
			if !ec2StrInValues(img.Name, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(img.State, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(img.OwnerId, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, img.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleCopyFpgaImage(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceFpgaImageId")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SourceFpgaImageId", http.StatusBadRequest)
		return
	}
	src, ok := ec2FpgaImages.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidFpgaImageID.NotFound", fmt.Sprintf("The FPGA image id %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	img := src
	img.FpgaImageId = ec2ID("afi")
	img.FpgaImageGlobalId = ec2ID("agfi")
	img.SourceFpgaImageId = srcID
	img.CreateTime = now
	img.UpdateTime = now
	img.LoadPermissions = nil
	img.Public = false
	if v := r.FormValue("Name"); v != "" {
		img.Name = v
	}
	if v := r.FormValue("Description"); v != "" {
		img.Description = v
	}
	ec2FpgaImages.Put(img.FpgaImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CopyFpgaImageResponse %s><requestId>%s</requestId><fpgaImageId>%s</fpgaImageId></CopyFpgaImageResponse>`,
		ec2Xmlns(), generateUUID(), img.FpgaImageId)
}

func handleDeleteFpgaImage(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FpgaImageId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter FpgaImageId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2FpgaImages.Get(id); !ok {
		ec2ErrorXML(w, "InvalidFpgaImageID.NotFound", fmt.Sprintf("The FPGA image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2FpgaImages.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteFpgaImageResponse %s><requestId>%s</requestId><return>true</return></DeleteFpgaImageResponse>`, ec2Xmlns(), generateUUID())
}

// ec2FpgaImageAttributeXML renders the FpgaImageAttribute structure scoped to
// the requested attribute name (description / name / loadPermission).
func ec2FpgaImageAttributeXML(img EC2FpgaImage, attr string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<fpgaImageId>%s</fpgaImageId>", img.FpgaImageId)
	switch attr {
	case "name":
		fmt.Fprintf(&b, "<name>%s</name>", xmlEscape(img.Name))
	case "description":
		fmt.Fprintf(&b, "<description>%s</description>", xmlEscape(img.Description))
	case "loadPermission":
		b.WriteString(ec2FpgaLoadPermissionsXML(img.LoadPermissions))
	case "productCodes":
		b.WriteString("<productCodes/>")
	default:
		fmt.Fprintf(&b, "<name>%s</name><description>%s</description>", xmlEscape(img.Name), xmlEscape(img.Description))
		b.WriteString(ec2FpgaLoadPermissionsXML(img.LoadPermissions))
	}
	return b.String()
}

func handleDescribeFpgaImageAttribute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FpgaImageId")
	attr := r.FormValue("Attribute")
	img, ok := ec2FpgaImages.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFpgaImageID.NotFound", fmt.Sprintf("The FPGA image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeFpgaImageAttributeResponse %s><requestId>%s</requestId><fpgaImageAttribute>%s</fpgaImageAttribute></DescribeFpgaImageAttributeResponse>`,
		ec2Xmlns(), generateUUID(), ec2FpgaImageAttributeXML(img, attr))
}

func handleModifyFpgaImageAttribute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FpgaImageId")
	img, ok := ec2FpgaImages.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFpgaImageID.NotFound", fmt.Sprintf("The FPGA image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	if v := r.FormValue("Name"); v != "" {
		img.Name = v
	}
	if v := r.FormValue("Description"); v != "" {
		img.Description = v
	}
	// LoadPermission add/remove (mirrors the EBS-snapshot/AMI loadPermission model).
	op := r.FormValue("OperationType")
	switch op {
	case "add":
		for _, uid := range ec2ParamList(r, "UserId") {
			img.LoadPermissions = ec2AddLoadPermission(img.LoadPermissions, EC2LoadPermission{UserId: uid})
		}
		for _, grp := range ec2ParamList(r, "UserGroup") {
			img.LoadPermissions = ec2AddLoadPermission(img.LoadPermissions, EC2LoadPermission{Group: grp})
			if grp == "all" {
				img.Public = true
			}
		}
		for i := 1; ; i++ {
			uid := r.FormValue(fmt.Sprintf("LoadPermission.Add.Item.%d.UserId", i))
			grp := r.FormValue(fmt.Sprintf("LoadPermission.Add.Item.%d.Group", i))
			if uid == "" && grp == "" {
				break
			}
			img.LoadPermissions = ec2AddLoadPermission(img.LoadPermissions, EC2LoadPermission{UserId: uid, Group: grp})
			if grp == "all" {
				img.Public = true
			}
		}
	case "remove":
		for _, uid := range ec2ParamList(r, "UserId") {
			img.LoadPermissions = ec2RemoveLoadPermission(img.LoadPermissions, EC2LoadPermission{UserId: uid})
		}
		for _, grp := range ec2ParamList(r, "UserGroup") {
			img.LoadPermissions = ec2RemoveLoadPermission(img.LoadPermissions, EC2LoadPermission{Group: grp})
			if grp == "all" {
				img.Public = false
			}
		}
		for i := 1; ; i++ {
			uid := r.FormValue(fmt.Sprintf("LoadPermission.Remove.Item.%d.UserId", i))
			grp := r.FormValue(fmt.Sprintf("LoadPermission.Remove.Item.%d.Group", i))
			if uid == "" && grp == "" {
				break
			}
			img.LoadPermissions = ec2RemoveLoadPermission(img.LoadPermissions, EC2LoadPermission{UserId: uid, Group: grp})
			if grp == "all" {
				img.Public = false
			}
		}
	}
	img.UpdateTime = ec2NowRFC3339Milli()
	ec2FpgaImages.Put(id, img)
	attr := r.FormValue("Attribute")
	if attr == "" {
		attr = "loadPermission"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyFpgaImageAttributeResponse %s><requestId>%s</requestId><fpgaImageAttribute>%s</fpgaImageAttribute></ModifyFpgaImageAttributeResponse>`,
		ec2Xmlns(), generateUUID(), ec2FpgaImageAttributeXML(img, attr))
}

func ec2AddLoadPermission(perms []EC2LoadPermission, p EC2LoadPermission) []EC2LoadPermission {
	for _, e := range perms {
		if e == p {
			return perms
		}
	}
	return append(perms, p)
}

func ec2RemoveLoadPermission(perms []EC2LoadPermission, p EC2LoadPermission) []EC2LoadPermission {
	out := perms[:0]
	for _, e := range perms {
		if e == p {
			continue
		}
		out = append(out, e)
	}
	return out
}

func handleResetFpgaImageAttribute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("FpgaImageId")
	img, ok := ec2FpgaImages.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidFpgaImageID.NotFound", fmt.Sprintf("The FPGA image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	// The only resettable attribute is loadPermission: clears all grants.
	img.LoadPermissions = nil
	img.Public = false
	img.UpdateTime = ec2NowRFC3339Milli()
	ec2FpgaImages.Put(id, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetFpgaImageAttributeResponse %s><requestId>%s</requestId><return>true</return></ResetFpgaImageAttributeResponse>`, ec2Xmlns(), generateUUID())
}

// -------------------- Allowed AMIs settings --------------------

func ec2GetAllowedImagesSettings() EC2AllowedImagesSettings {
	s, ok := ec2AllowedImagesSettings.Get(ec2AllowedImagesSettingsKey)
	if !ok {
		return EC2AllowedImagesSettings{State: "disabled"}
	}
	return s
}

func handleEnableAllowedImagesSettings(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("AllowedImagesSettingsState")
	if state != "enabled" && state != "audit-mode" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid AllowedImagesSettingsState %q", state), http.StatusBadRequest)
		return
	}
	s := ec2GetAllowedImagesSettings()
	s.State = state
	ec2AllowedImagesSettings.Put(ec2AllowedImagesSettingsKey, s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableAllowedImagesSettingsResponse %s><requestId>%s</requestId><allowedImagesSettingsState>%s</allowedImagesSettingsState></EnableAllowedImagesSettingsResponse>`,
		ec2Xmlns(), generateUUID(), state)
}

func handleDisableAllowedImagesSettings(w http.ResponseWriter, r *http.Request) {
	s := ec2GetAllowedImagesSettings()
	s.State = "disabled"
	ec2AllowedImagesSettings.Put(ec2AllowedImagesSettingsKey, s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableAllowedImagesSettingsResponse %s><requestId>%s</requestId><allowedImagesSettingsState>disabled</allowedImagesSettingsState></DisableAllowedImagesSettingsResponse>`,
		ec2Xmlns(), generateUUID())
}

func ec2ImageCriterionXML(c EC2ImageCriterion) string {
	var b strings.Builder
	b.WriteString("<item>")
	if len(c.ImageProviders) > 0 {
		b.WriteString("<imageProviderSet>")
		for _, p := range c.ImageProviders {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(p))
		}
		b.WriteString("</imageProviderSet>")
	}
	if len(c.MarketplaceProductCodes) > 0 {
		b.WriteString("<marketplaceProductCodeSet>")
		for _, p := range c.MarketplaceProductCodes {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(p))
		}
		b.WriteString("</marketplaceProductCodeSet>")
	}
	if len(c.ImageNames) > 0 {
		b.WriteString("<imageNameSet>")
		for _, n := range c.ImageNames {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(n))
		}
		b.WriteString("</imageNameSet>")
	}
	b.WriteString("</item>")
	return b.String()
}

func handleGetAllowedImagesSettings(w http.ResponseWriter, r *http.Request) {
	s := ec2GetAllowedImagesSettings()
	var crit strings.Builder
	for _, c := range s.Criteria {
		crit.WriteString(ec2ImageCriterionXML(c))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetAllowedImagesSettingsResponse %s><requestId>%s</requestId><state>%s</state><imageCriterionSet>%s</imageCriterionSet><managedBy>account</managedBy></GetAllowedImagesSettingsResponse>`,
		ec2Xmlns(), generateUUID(), s.State, crit.String())
}

// ec2ParseImageCriteria reads the indexed ImageCriterion.N request params.
func ec2ParseImageCriteria(r *http.Request) []EC2ImageCriterion {
	var out []EC2ImageCriterion
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("ImageCriterion.%d.", i)
		providers := ec2CriterionValues(r, prefix+"ImageProvider")
		codes := ec2CriterionValues(r, prefix+"MarketplaceProductCode")
		names := ec2CriterionValues(r, prefix+"ImageName")
		if len(providers) == 0 && len(codes) == 0 && len(names) == 0 {
			break
		}
		out = append(out, EC2ImageCriterion{
			ImageProviders:          providers,
			MarketplaceProductCodes: codes,
			ImageNames:              names,
		})
	}
	return out
}

// ec2IndexedList reads prefix.1, prefix.2, … into a slice.
func ec2IndexedList(r *http.Request, prefix string) []string {
	var out []string
	for j := 1; ; j++ {
		v := r.FormValue(fmt.Sprintf("%s.%d", prefix, j))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// ec2CriterionValues reads a criterion's string list, accepting both wire
// shapes EC2 clients emit for the same list: aws-sdk-go-v2 serializes the
// nested list as prefix.Item.N, while botocore (the aws CLI) flattens it as
// prefix.N.
func ec2CriterionValues(r *http.Request, prefix string) []string {
	if v := ec2IndexedList(r, prefix+".Item"); len(v) > 0 {
		return v
	}
	return ec2IndexedList(r, prefix)
}

func handleReplaceImageCriteriaInAllowedImagesSettings(w http.ResponseWriter, r *http.Request) {
	s := ec2GetAllowedImagesSettings()
	s.Criteria = ec2ParseImageCriteria(r)
	ec2AllowedImagesSettings.Put(ec2AllowedImagesSettingsKey, s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReplaceImageCriteriaInAllowedImagesSettingsResponse %s><requestId>%s</requestId><return>true</return></ReplaceImageCriteriaInAllowedImagesSettingsResponse>`,
		ec2Xmlns(), generateUUID())
}

// -------------------- Image block public access --------------------

func ec2GetImageBlockPublicAccessState() string {
	s, ok := ec2ImageBlockPublicAccess.Get(ec2AllowedImagesSettingsKey)
	if !ok {
		return "unblocked"
	}
	return s
}

func handleEnableImageBlockPublicAccess(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("ImageBlockPublicAccessState")
	if state != "block-new-sharing" {
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid ImageBlockPublicAccessState %q", state), http.StatusBadRequest)
		return
	}
	ec2ImageBlockPublicAccess.Put(ec2AllowedImagesSettingsKey, state)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableImageBlockPublicAccessResponse %s><requestId>%s</requestId><imageBlockPublicAccessState>%s</imageBlockPublicAccessState></EnableImageBlockPublicAccessResponse>`,
		ec2Xmlns(), generateUUID(), state)
}

func handleDisableImageBlockPublicAccess(w http.ResponseWriter, r *http.Request) {
	ec2ImageBlockPublicAccess.Put(ec2AllowedImagesSettingsKey, "unblocked")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableImageBlockPublicAccessResponse %s><requestId>%s</requestId><imageBlockPublicAccessState>unblocked</imageBlockPublicAccessState></DisableImageBlockPublicAccessResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleGetImageBlockPublicAccessState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetImageBlockPublicAccessStateResponse %s><requestId>%s</requestId><imageBlockPublicAccessState>%s</imageBlockPublicAccessState><managedBy>account</managedBy></GetImageBlockPublicAccessStateResponse>`,
		ec2Xmlns(), generateUUID(), ec2GetImageBlockPublicAccessState())
}

// -------------------- Image deregistration protection --------------------

func handleEnableImageDeregistrationProtection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ImageId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Images.Get(id); !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	state := "enabled"
	if r.FormValue("WithCooldown") == "true" {
		state = "enabled-with-cooldown"
	}
	ec2ImageDeregProtection.Put(id, state)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableImageDeregistrationProtectionResponse %s><requestId>%s</requestId><return>%s</return></EnableImageDeregistrationProtectionResponse>`,
		ec2Xmlns(), generateUUID(), state)
}

func handleDisableImageDeregistrationProtection(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ImageId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Images.Get(id); !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2ImageDeregProtection.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableImageDeregistrationProtectionResponse %s><requestId>%s</requestId><return>disabled</return></DisableImageDeregistrationProtectionResponse>`,
		ec2Xmlns(), generateUUID())
}

// -------------------- Bundle / conversion tasks --------------------

// handleDescribeBundleTasks returns an honest-empty list: the sim has no live
// instance-store bundling backend, so no bundle tasks ever exist.
func handleDescribeBundleTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeBundleTasksResponse %s><requestId>%s</requestId><bundleInstanceTasksSet/></DescribeBundleTasksResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleCancelBundleTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("BundleId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter BundleId", http.StatusBadRequest)
		return
	}
	// No bundle tasks exist in the sim, so any id is unknown.
	ec2ErrorXML(w, "InvalidBundleID.NotFound", fmt.Sprintf("The bundle task id %q does not exist", id), http.StatusBadRequest)
}

// handleDescribeConversionTasks returns an honest-empty list: the sim has no
// live import/export conversion backend.
func handleDescribeConversionTasks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeConversionTasksResponse %s><requestId>%s</requestId><conversionTasks/></DescribeConversionTasksResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleCancelConversionTask(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ConversionTaskId")
	if id == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ConversionTaskId", http.StatusBadRequest)
		return
	}
	ec2ErrorXML(w, "InvalidConversionTaskId", fmt.Sprintf("The conversion task id %q does not exist", id), http.StatusBadRequest)
}

// -------------------- Store image tasks --------------------

func handleCreateStoreImageTask(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	bucket := r.FormValue("Bucket")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	if bucket == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Bucket", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Images.Get(imageID); !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	objectKey := imageID + ".bin"
	task := EC2StoreImageTask{
		AmiId:         imageID,
		Bucket:        bucket,
		S3ObjectKey:   objectKey,
		TaskStartTime: ec2NowRFC3339Milli(),
		State:         "Completed",
		Progress:      100,
	}
	ec2StoreImageTasks.Put(imageID, task)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateStoreImageTaskResponse %s><requestId>%s</requestId><objectKey>%s</objectKey></CreateStoreImageTaskResponse>`,
		ec2Xmlns(), generateUUID(), objectKey)
}

func handleDescribeStoreImageTasks(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ImageId")
	results := make([]EC2StoreImageTask, 0)
	for _, t := range ec2StoreImageTasks.List() {
		if len(ids) > 0 && !ec2StrInValues(t.AmiId, ids) {
			continue
		}
		results = append(results, t)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].AmiId < results[j].AmiId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, t := range results {
		fmt.Fprintf(&items, `<item><amiId>%s</amiId><taskStartTime>%s</taskStartTime><bucket>%s</bucket><s3objectKey>%s</s3objectKey><progressPercentage>%d</progressPercentage><storeTaskState>%s</storeTaskState></item>`,
			t.AmiId, t.TaskStartTime, t.Bucket, t.S3ObjectKey, t.Progress, t.State)
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeStoreImageTasksResponse %s><requestId>%s</requestId><storeImageTaskResultSet>%s</storeImageTaskResultSet>%s</DescribeStoreImageTasksResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}
