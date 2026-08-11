package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// EC2 Launch Templates. A launch template is a versioned,
// reusable description of instance launch parameters that an Auto Scaling
// group (or RunInstances) references. The fck-nat NAT-instance Terraform
// module uses aws_launch_template as the ASG's launch config, so the full
// create/read/delete lifecycle must round-trip through terraform-provider-aws.
//
// State is modeled like the other EC2 control-plane resources: a store keyed
// by the generated lt-… ID, each template holding an append-only slice of
// versions with a separately-tracked default. The launch-template *data* is
// echoed back at the exact XML locationNames the aws-sdk-go-v2 ec2 query
// deserializers expect (verified against service/ec2@v1.305.2), so the SDK,
// CLI, and Terraform provider all read back what they submitted with no drift.

type EC2LaunchTemplate struct {
	LaunchTemplateId     string
	LaunchTemplateName   string
	CreateTime           string
	CreatedBy            string
	DefaultVersionNumber int64
	LatestVersionNumber  int64
	Tags                 []EC2Tag
	Versions             []EC2LaunchTemplateVersion
}

type EC2LaunchTemplateVersion struct {
	VersionNumber      int64
	VersionDescription string
	CreateTime         string
	CreatedBy          string
	DefaultVersion     bool
	Data               EC2LaunchTemplateData
}

// EC2LaunchTemplateData mirrors RequestLaunchTemplateData. Scalars are stored
// in their submitted string form ("" means the field was absent and is omitted
// from the response, so unset fields never round-trip as a forced default).
type EC2LaunchTemplateData struct {
	ImageId                           string
	InstanceType                      string
	KeyName                           string
	UserData                          string
	EbsOptimized                      string
	DisableApiTermination             string
	InstanceInitiatedShutdownBehavior string
	IamInstanceProfileName            string
	IamInstanceProfileArn             string
	MonitoringEnabled                 string
	SecurityGroupIds                  []string
	NetworkInterfaces                 []EC2LTNetworkInterface
	BlockDeviceMappings               []EC2LTBlockDeviceMapping
	TagSpecifications                 []EC2LTTagSpecification
	MetadataOptions                   *EC2LTMetadataOptions
	Placement                         *EC2LTPlacement
	CreditSpecification               *EC2LTCreditSpecification
	InstanceMarketOptions             *EC2LTInstanceMarketOptions
}

type EC2LTCreditSpecification struct {
	CpuCredits string
}

type EC2LTInstanceMarketOptions struct {
	MarketType                   string
	MaxPrice                     string
	SpotInstanceType             string
	InstanceInterruptionBehavior string
}

type EC2LTNetworkInterface struct {
	DeviceIndex              string
	AssociatePublicIpAddress string
	DeleteOnTermination      string
	Description              string
	SubnetId                 string
	NetworkCardIndex         string
	InterfaceType            string
	PrivateIpAddress         string
	Groups                   []string
}

type EC2LTBlockDeviceMapping struct {
	DeviceName  string
	VirtualName string
	NoDevice    string
	Ebs         *EC2LTEbs
}

type EC2LTEbs struct {
	VolumeSize          string
	VolumeType          string
	DeleteOnTermination string
	Encrypted           string
	Iops                string
	Throughput          string
	SnapshotId          string
	KmsKeyId            string
}

type EC2LTTagSpecification struct {
	ResourceType string
	Tags         []EC2Tag
}

type EC2LTMetadataOptions struct {
	HttpTokens              string
	HttpPutResponseHopLimit string
	HttpEndpoint            string
	HttpProtocolIpv6        string
	InstanceMetadataTags    string
}

type EC2LTPlacement struct {
	AvailabilityZone string
	GroupName        string
	Tenancy          string
}

var ec2LaunchTemplates sim.Store[EC2LaunchTemplate]

func registerEC2LaunchTemplates(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2LaunchTemplates = sim.MakeStore[EC2LaunchTemplate](srv.DB(), "ec2_launch_templates")
	r.Register("CreateLaunchTemplate", handleCreateLaunchTemplate)
	r.Register("DescribeLaunchTemplates", handleDescribeLaunchTemplates)
	r.Register("DescribeLaunchTemplateVersions", handleDescribeLaunchTemplateVersions)
	r.Register("DeleteLaunchTemplate", handleDeleteLaunchTemplate)
	r.Register("CreateLaunchTemplateVersion", handleCreateLaunchTemplateVersion)
	r.Register("ModifyLaunchTemplate", handleModifyLaunchTemplate)
}

// handleCreateLaunchTemplateVersion appends a new version to an existing
// template (the aws_launch_template in-place update path). The new version
// becomes the latest but NOT the default — real EC2 keeps the default pinned
// until ModifyLaunchTemplate moves it.
func handleCreateLaunchTemplateVersion(w http.ResponseWriter, r *http.Request) {
	lt, ok := lookupLaunchTemplate(r.FormValue("LaunchTemplateId"), r.FormValue("LaunchTemplateName"))
	if !ok {
		ref := r.FormValue("LaunchTemplateId")
		if ref == "" {
			ref = r.FormValue("LaunchTemplateName")
		}
		ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound",
			fmt.Sprintf("Launch template %s does not exist.", ref), 400)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	lt.LatestVersionNumber++
	version := EC2LaunchTemplateVersion{
		VersionNumber:      lt.LatestVersionNumber,
		VersionDescription: r.FormValue("VersionDescription"),
		CreateTime:         now,
		CreatedBy:          fmt.Sprintf("arn:aws:iam::%s:root", awsAccountID()),
		DefaultVersion:     false,
		Data:               parseLaunchTemplateData(r, "LaunchTemplateData"),
	}
	lt.Versions = append(lt.Versions, version)
	ec2LaunchTemplates.Put(lt.LaunchTemplateId, lt)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateLaunchTemplateVersionResponse %s>
  <requestId>%s</requestId>
  <launchTemplateVersion>%s</launchTemplateVersion>
</CreateLaunchTemplateVersionResponse>`, ec2Xmlns(), generateUUID(), ltVersionFieldsXML(lt, version))
}

// handleModifyLaunchTemplate moves the default version (the second half of an
// aws_launch_template in-place update). DefaultVersion accepts a numeric
// version or the $Latest/$Default aliases.
func handleModifyLaunchTemplate(w http.ResponseWriter, r *http.Request) {
	lt, ok := lookupLaunchTemplate(r.FormValue("LaunchTemplateId"), r.FormValue("LaunchTemplateName"))
	if !ok {
		ref := r.FormValue("LaunchTemplateId")
		if ref == "" {
			ref = r.FormValue("LaunchTemplateName")
		}
		ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound",
			fmt.Sprintf("Launch template %s does not exist.", ref), 400)
		return
	}
	if sel := r.FormValue("SetDefaultVersion"); sel != "" {
		target := lt.DefaultVersionNumber
		switch sel {
		case "$Latest":
			target = lt.LatestVersionNumber
		case "$Default":
			// no-op: already the default
		default:
			n := parseInt64(sel)
			if !ltHasVersion(lt, n) {
				ec2ErrorXML(w, "InvalidLaunchTemplateId.VersionNotFound",
					fmt.Sprintf("Launch template version %s does not exist.", sel), 400)
				return
			}
			target = n
		}
		lt.DefaultVersionNumber = target
		for i := range lt.Versions {
			lt.Versions[i].DefaultVersion = lt.Versions[i].VersionNumber == target
		}
		ec2LaunchTemplates.Put(lt.LaunchTemplateId, lt)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyLaunchTemplateResponse %s>
  <requestId>%s</requestId>
  <launchTemplate>%s</launchTemplate>
</ModifyLaunchTemplateResponse>`, ec2Xmlns(), generateUUID(), ltSummaryXML(lt))
}

func ltHasVersion(lt EC2LaunchTemplate, n int64) bool {
	for _, v := range lt.Versions {
		if v.VersionNumber == n {
			return true
		}
	}
	return false
}

// ec2HasFormPrefix reports whether any submitted form key belongs to a
// flattened list/struct member at the given prefix — the reliable way to
// detect the presence of a nested item like NetworkInterface.2 whose only set
// field might legitimately be a zero-valued scalar ("0").
func ec2HasFormPrefix(r *http.Request, prefix string) bool {
	_ = r.FormValue("") // force ParseForm
	for k := range r.Form {
		if k == prefix || strings.HasPrefix(k, prefix+".") {
			return true
		}
	}
	return false
}

func parseLaunchTemplateData(r *http.Request, prefix string) EC2LaunchTemplateData {
	g := func(suffix string) string { return r.FormValue(prefix + "." + suffix) }
	d := EC2LaunchTemplateData{
		ImageId:                           g("ImageId"),
		InstanceType:                      g("InstanceType"),
		KeyName:                           g("KeyName"),
		UserData:                          g("UserData"),
		EbsOptimized:                      g("EbsOptimized"),
		DisableApiTermination:             g("DisableApiTermination"),
		InstanceInitiatedShutdownBehavior: g("InstanceInitiatedShutdownBehavior"),
		IamInstanceProfileName:            g("IamInstanceProfile.Name"),
		IamInstanceProfileArn:             g("IamInstanceProfile.Arn"),
		MonitoringEnabled:                 g("Monitoring.Enabled"),
		SecurityGroupIds:                  ec2ParamList(r, prefix+".SecurityGroupId"),
	}

	for i := 1; ; i++ {
		np := fmt.Sprintf("%s.NetworkInterface.%d", prefix, i)
		if !ec2HasFormPrefix(r, np) {
			break
		}
		d.NetworkInterfaces = append(d.NetworkInterfaces, EC2LTNetworkInterface{
			DeviceIndex:              r.FormValue(np + ".DeviceIndex"),
			AssociatePublicIpAddress: r.FormValue(np + ".AssociatePublicIpAddress"),
			DeleteOnTermination:      r.FormValue(np + ".DeleteOnTermination"),
			Description:              r.FormValue(np + ".Description"),
			SubnetId:                 r.FormValue(np + ".SubnetId"),
			NetworkCardIndex:         r.FormValue(np + ".NetworkCardIndex"),
			InterfaceType:            r.FormValue(np + ".InterfaceType"),
			PrivateIpAddress:         r.FormValue(np + ".PrivateIpAddress"),
			Groups:                   ec2ParamList(r, np+".SecurityGroupId"),
		})
	}

	for i := 1; ; i++ {
		bp := fmt.Sprintf("%s.BlockDeviceMapping.%d", prefix, i)
		if !ec2HasFormPrefix(r, bp) {
			break
		}
		bdm := EC2LTBlockDeviceMapping{
			DeviceName:  r.FormValue(bp + ".DeviceName"),
			VirtualName: r.FormValue(bp + ".VirtualName"),
			NoDevice:    r.FormValue(bp + ".NoDevice"),
		}
		if ec2HasFormPrefix(r, bp+".Ebs") {
			bdm.Ebs = &EC2LTEbs{
				VolumeSize:          r.FormValue(bp + ".Ebs.VolumeSize"),
				VolumeType:          r.FormValue(bp + ".Ebs.VolumeType"),
				DeleteOnTermination: r.FormValue(bp + ".Ebs.DeleteOnTermination"),
				Encrypted:           r.FormValue(bp + ".Ebs.Encrypted"),
				Iops:                r.FormValue(bp + ".Ebs.Iops"),
				Throughput:          r.FormValue(bp + ".Ebs.Throughput"),
				SnapshotId:          r.FormValue(bp + ".Ebs.SnapshotId"),
				KmsKeyId:            r.FormValue(bp + ".Ebs.KmsKeyId"),
			}
		}
		d.BlockDeviceMappings = append(d.BlockDeviceMappings, bdm)
	}

	for i := 1; ; i++ {
		tp := fmt.Sprintf("%s.TagSpecification.%d", prefix, i)
		if !ec2HasFormPrefix(r, tp) {
			break
		}
		ts := EC2LTTagSpecification{ResourceType: r.FormValue(tp + ".ResourceType")}
		for j := 1; ; j++ {
			key := r.FormValue(fmt.Sprintf("%s.Tag.%d.Key", tp, j))
			if key == "" {
				break
			}
			ts.Tags = append(ts.Tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("%s.Tag.%d.Value", tp, j))})
		}
		d.TagSpecifications = append(d.TagSpecifications, ts)
	}

	if ec2HasFormPrefix(r, prefix+".MetadataOptions") {
		d.MetadataOptions = &EC2LTMetadataOptions{
			HttpTokens:              g("MetadataOptions.HttpTokens"),
			HttpPutResponseHopLimit: g("MetadataOptions.HttpPutResponseHopLimit"),
			HttpEndpoint:            g("MetadataOptions.HttpEndpoint"),
			HttpProtocolIpv6:        g("MetadataOptions.HttpProtocolIpv6"),
			InstanceMetadataTags:    g("MetadataOptions.InstanceMetadataTags"),
		}
	}

	if ec2HasFormPrefix(r, prefix+".Placement") {
		d.Placement = &EC2LTPlacement{
			AvailabilityZone: g("Placement.AvailabilityZone"),
			GroupName:        g("Placement.GroupName"),
			Tenancy:          g("Placement.Tenancy"),
		}
	}

	if ec2HasFormPrefix(r, prefix+".CreditSpecification") {
		d.CreditSpecification = &EC2LTCreditSpecification{CpuCredits: g("CreditSpecification.CpuCredits")}
	}

	if ec2HasFormPrefix(r, prefix+".InstanceMarketOptions") {
		d.InstanceMarketOptions = &EC2LTInstanceMarketOptions{
			MarketType:                   g("InstanceMarketOptions.MarketType"),
			MaxPrice:                     g("InstanceMarketOptions.SpotOptions.MaxPrice"),
			SpotInstanceType:             g("InstanceMarketOptions.SpotOptions.SpotInstanceType"),
			InstanceInterruptionBehavior: g("InstanceMarketOptions.SpotOptions.InstanceInterruptionBehavior"),
		}
	}

	return d
}

// ---- Rendering (exact aws-sdk-go-v2 ec2 query locationNames) ----

func ltOptEl(b *strings.Builder, name, val string) {
	if val != "" {
		fmt.Fprintf(b, "<%s>%s</%s>", name, xmlEscape(val), name)
	}
}

func ltDataXML(d EC2LaunchTemplateData) string {
	var b strings.Builder
	ltOptEl(&b, "imageId", d.ImageId)
	ltOptEl(&b, "instanceType", d.InstanceType)
	ltOptEl(&b, "keyName", d.KeyName)
	ltOptEl(&b, "userData", d.UserData)
	ltOptEl(&b, "ebsOptimized", d.EbsOptimized)
	ltOptEl(&b, "disableApiTermination", d.DisableApiTermination)
	ltOptEl(&b, "instanceInitiatedShutdownBehavior", d.InstanceInitiatedShutdownBehavior)
	if d.IamInstanceProfileName != "" || d.IamInstanceProfileArn != "" {
		b.WriteString("<iamInstanceProfile>")
		ltOptEl(&b, "arn", d.IamInstanceProfileArn)
		ltOptEl(&b, "name", d.IamInstanceProfileName)
		b.WriteString("</iamInstanceProfile>")
	}
	if d.MonitoringEnabled != "" {
		fmt.Fprintf(&b, "<monitoring><enabled>%s</enabled></monitoring>", d.MonitoringEnabled)
	}
	if len(d.SecurityGroupIds) > 0 {
		b.WriteString("<securityGroupIdSet>")
		for _, g := range d.SecurityGroupIds {
			fmt.Fprintf(&b, "<item>%s</item>", xmlEscape(g))
		}
		b.WriteString("</securityGroupIdSet>")
	}
	if len(d.NetworkInterfaces) > 0 {
		b.WriteString("<networkInterfaceSet>")
		for _, ni := range d.NetworkInterfaces {
			b.WriteString("<item>")
			ltOptEl(&b, "deviceIndex", ni.DeviceIndex)
			ltOptEl(&b, "associatePublicIpAddress", ni.AssociatePublicIpAddress)
			ltOptEl(&b, "deleteOnTermination", ni.DeleteOnTermination)
			ltOptEl(&b, "description", ni.Description)
			ltOptEl(&b, "subnetId", ni.SubnetId)
			ltOptEl(&b, "networkCardIndex", ni.NetworkCardIndex)
			ltOptEl(&b, "interfaceType", ni.InterfaceType)
			ltOptEl(&b, "privateIpAddress", ni.PrivateIpAddress)
			if len(ni.Groups) > 0 {
				b.WriteString("<groupSet>")
				for _, g := range ni.Groups {
					fmt.Fprintf(&b, "<groupId>%s</groupId>", xmlEscape(g))
				}
				b.WriteString("</groupSet>")
			}
			b.WriteString("</item>")
		}
		b.WriteString("</networkInterfaceSet>")
	}
	if len(d.BlockDeviceMappings) > 0 {
		b.WriteString("<blockDeviceMappingSet>")
		for _, bdm := range d.BlockDeviceMappings {
			b.WriteString("<item>")
			ltOptEl(&b, "deviceName", bdm.DeviceName)
			ltOptEl(&b, "virtualName", bdm.VirtualName)
			ltOptEl(&b, "noDevice", bdm.NoDevice)
			if bdm.Ebs != nil {
				b.WriteString("<ebs>")
				ltOptEl(&b, "snapshotId", bdm.Ebs.SnapshotId)
				ltOptEl(&b, "volumeSize", bdm.Ebs.VolumeSize)
				ltOptEl(&b, "volumeType", bdm.Ebs.VolumeType)
				ltOptEl(&b, "deleteOnTermination", bdm.Ebs.DeleteOnTermination)
				ltOptEl(&b, "iops", bdm.Ebs.Iops)
				ltOptEl(&b, "throughput", bdm.Ebs.Throughput)
				ltOptEl(&b, "encrypted", bdm.Ebs.Encrypted)
				ltOptEl(&b, "kmsKeyId", bdm.Ebs.KmsKeyId)
				b.WriteString("</ebs>")
			}
			b.WriteString("</item>")
		}
		b.WriteString("</blockDeviceMappingSet>")
	}
	if len(d.TagSpecifications) > 0 {
		b.WriteString("<tagSpecificationSet>")
		for _, ts := range d.TagSpecifications {
			b.WriteString("<item>")
			ltOptEl(&b, "resourceType", ts.ResourceType)
			b.WriteString(writeTagSetXML(ts.Tags))
			b.WriteString("</item>")
		}
		b.WriteString("</tagSpecificationSet>")
	}
	if d.MetadataOptions != nil {
		b.WriteString("<metadataOptions>")
		ltOptEl(&b, "httpTokens", d.MetadataOptions.HttpTokens)
		ltOptEl(&b, "httpPutResponseHopLimit", d.MetadataOptions.HttpPutResponseHopLimit)
		ltOptEl(&b, "httpEndpoint", d.MetadataOptions.HttpEndpoint)
		ltOptEl(&b, "httpProtocolIpv6", d.MetadataOptions.HttpProtocolIpv6)
		ltOptEl(&b, "instanceMetadataTags", d.MetadataOptions.InstanceMetadataTags)
		b.WriteString("</metadataOptions>")
	}
	if d.Placement != nil {
		b.WriteString("<placement>")
		ltOptEl(&b, "availabilityZone", d.Placement.AvailabilityZone)
		ltOptEl(&b, "groupName", d.Placement.GroupName)
		ltOptEl(&b, "tenancy", d.Placement.Tenancy)
		b.WriteString("</placement>")
	}
	if d.CreditSpecification != nil {
		b.WriteString("<creditSpecification>")
		ltOptEl(&b, "cpuCredits", d.CreditSpecification.CpuCredits)
		b.WriteString("</creditSpecification>")
	}
	if d.InstanceMarketOptions != nil {
		b.WriteString("<instanceMarketOptions>")
		ltOptEl(&b, "marketType", d.InstanceMarketOptions.MarketType)
		if d.InstanceMarketOptions.MaxPrice != "" || d.InstanceMarketOptions.SpotInstanceType != "" || d.InstanceMarketOptions.InstanceInterruptionBehavior != "" {
			b.WriteString("<spotOptions>")
			ltOptEl(&b, "maxPrice", d.InstanceMarketOptions.MaxPrice)
			ltOptEl(&b, "spotInstanceType", d.InstanceMarketOptions.SpotInstanceType)
			ltOptEl(&b, "instanceInterruptionBehavior", d.InstanceMarketOptions.InstanceInterruptionBehavior)
			b.WriteString("</spotOptions>")
		}
		b.WriteString("</instanceMarketOptions>")
	}
	return b.String()
}

// ltSummaryXML renders the inner LaunchTemplate fields (no wrapper), shared by
// Create/Delete (wrapped in <launchTemplate>) and Describe (wrapped in <item>).
func ltSummaryXML(lt EC2LaunchTemplate) string {
	return fmt.Sprintf(`<launchTemplateId>%s</launchTemplateId>`+
		`<launchTemplateName>%s</launchTemplateName>`+
		`<createTime>%s</createTime>`+
		`<createdBy>%s</createdBy>`+
		`<defaultVersionNumber>%d</defaultVersionNumber>`+
		`<latestVersionNumber>%d</latestVersionNumber>%s`,
		lt.LaunchTemplateId, xmlEscape(lt.LaunchTemplateName), lt.CreateTime, lt.CreatedBy,
		lt.DefaultVersionNumber, lt.LatestVersionNumber, writeTagSetXML(lt.Tags))
}

// ltVersionFieldsXML renders the inner LaunchTemplateVersion fields (no
// wrapper), shared by DescribeLaunchTemplateVersions (wrapped in <item>) and
// CreateLaunchTemplateVersion (wrapped in <launchTemplateVersion>).
func ltVersionFieldsXML(lt EC2LaunchTemplate, v EC2LaunchTemplateVersion) string {
	return fmt.Sprintf(`<launchTemplateId>%s</launchTemplateId>`+
		`<launchTemplateName>%s</launchTemplateName>`+
		`<versionNumber>%d</versionNumber>`+
		`<versionDescription>%s</versionDescription>`+
		`<createTime>%s</createTime>`+
		`<createdBy>%s</createdBy>`+
		`<defaultVersion>%t</defaultVersion>`+
		`<launchTemplateData>%s</launchTemplateData>`,
		lt.LaunchTemplateId, xmlEscape(lt.LaunchTemplateName), v.VersionNumber,
		xmlEscape(v.VersionDescription), v.CreateTime, v.CreatedBy, v.DefaultVersion, ltDataXML(v.Data))
}

func ltVersionXML(lt EC2LaunchTemplate, v EC2LaunchTemplateVersion) string {
	return "<item>" + ltVersionFieldsXML(lt, v) + "</item>"
}

// ---- Handlers ----

func handleCreateLaunchTemplate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("LaunchTemplateName")
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter launchTemplateName", 400)
		return
	}
	for _, existing := range ec2LaunchTemplates.List() {
		if existing.LaunchTemplateName == name {
			ec2ErrorXML(w, "InvalidLaunchTemplateName.AlreadyExistsException",
				fmt.Sprintf("Launch template name %q is already in use.", name), 400)
			return
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	lt := EC2LaunchTemplate{
		LaunchTemplateId:     ec2ID("lt"),
		LaunchTemplateName:   name,
		CreateTime:           now,
		CreatedBy:            fmt.Sprintf("arn:aws:iam::%s:root", awsAccountID()),
		DefaultVersionNumber: 1,
		LatestVersionNumber:  1,
		Tags:                 parseTags(r),
		Versions: []EC2LaunchTemplateVersion{{
			VersionNumber:      1,
			VersionDescription: r.FormValue("VersionDescription"),
			CreateTime:         now,
			CreatedBy:          fmt.Sprintf("arn:aws:iam::%s:root", awsAccountID()),
			DefaultVersion:     true,
			Data:               parseLaunchTemplateData(r, "LaunchTemplateData"),
		}},
	}
	ec2LaunchTemplates.Put(lt.LaunchTemplateId, lt)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateLaunchTemplateResponse %s>
  <requestId>%s</requestId>
  <launchTemplate>%s</launchTemplate>
</CreateLaunchTemplateResponse>`, ec2Xmlns(), generateUUID(), ltSummaryXML(lt))
}

func handleDescribeLaunchTemplates(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "LaunchTemplateId")
	names := ec2ParamList(r, "LaunchTemplateName")
	filters := ec2Filters(r)

	var matched []EC2LaunchTemplate
	for _, lt := range ec2LaunchTemplates.List() {
		if len(ids) > 0 && !ec2StrInValues(lt.LaunchTemplateId, ids) {
			continue
		}
		if len(names) > 0 && !ec2StrInValues(lt.LaunchTemplateName, names) {
			continue
		}
		if vals, ok := filters["launch-template-name"]; ok && !ec2StrInValues(lt.LaunchTemplateName, vals) {
			continue
		}
		matched = append(matched, lt)
	}
	// A caller naming a specific ID/name that doesn't exist gets the AWS
	// NotFound, not a silent empty set.
	for _, id := range ids {
		if _, ok := ec2LaunchTemplates.Get(id); !ok {
			ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound",
				fmt.Sprintf("Launch template %s does not exist.", id), 400)
			return
		}
	}

	var items strings.Builder
	for _, lt := range matched {
		items.WriteString("<item>" + ltSummaryXML(lt) + "</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeLaunchTemplatesResponse %s>
  <requestId>%s</requestId>
  <launchTemplates>%s</launchTemplates>
</DescribeLaunchTemplatesResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDescribeLaunchTemplateVersions(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LaunchTemplateId")
	name := r.FormValue("LaunchTemplateName")
	lt, ok := lookupLaunchTemplate(id, name)
	if !ok {
		ref := id
		if ref == "" {
			ref = name
		}
		ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound",
			fmt.Sprintf("Launch template %s does not exist.", ref), 400)
		return
	}

	wanted := ec2ParamList(r, "LaunchTemplateVersion")
	minV := parseInt64(r.FormValue("MinVersion"))
	maxV := parseInt64(r.FormValue("MaxVersion"))

	var items strings.Builder
	for _, v := range lt.Versions {
		if !launchTemplateVersionWanted(v, lt, wanted, minV, maxV) {
			continue
		}
		items.WriteString(ltVersionXML(lt, v))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeLaunchTemplateVersionsResponse %s>
  <requestId>%s</requestId>
  <launchTemplateVersionSet>%s</launchTemplateVersionSet>
</DescribeLaunchTemplateVersionsResponse>`, ec2Xmlns(), generateUUID(), items.String())
}

func handleDeleteLaunchTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("LaunchTemplateId")
	name := r.FormValue("LaunchTemplateName")
	lt, ok := lookupLaunchTemplate(id, name)
	if !ok {
		ref := id
		if ref == "" {
			ref = name
		}
		ec2ErrorXML(w, "InvalidLaunchTemplateId.NotFound",
			fmt.Sprintf("Launch template %s does not exist.", ref), 400)
		return
	}
	ec2LaunchTemplates.Delete(lt.LaunchTemplateId)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteLaunchTemplateResponse %s>
  <requestId>%s</requestId>
  <launchTemplate>%s</launchTemplate>
</DeleteLaunchTemplateResponse>`, ec2Xmlns(), generateUUID(), ltSummaryXML(lt))
}

func lookupLaunchTemplate(id, name string) (EC2LaunchTemplate, bool) {
	if id != "" {
		return ec2LaunchTemplates.Get(id)
	}
	if name != "" {
		for _, lt := range ec2LaunchTemplates.List() {
			if lt.LaunchTemplateName == name {
				return lt, true
			}
		}
	}
	return EC2LaunchTemplate{}, false
}

// launchTemplateVersionWanted applies the DescribeLaunchTemplateVersions
// selectors: an explicit version list (numbers plus the $Latest/$Default
// aliases) takes precedence; otherwise every version in the [MinVersion,
// MaxVersion] range is returned (real default behaviour).
func launchTemplateVersionWanted(v EC2LaunchTemplateVersion, lt EC2LaunchTemplate, wanted []string, minV, maxV int64) bool {
	if len(wanted) > 0 {
		for _, sel := range wanted {
			switch sel {
			case "$Latest":
				if v.VersionNumber == lt.LatestVersionNumber {
					return true
				}
			case "$Default":
				if v.VersionNumber == lt.DefaultVersionNumber {
					return true
				}
			default:
				if parseInt64(sel) == v.VersionNumber {
					return true
				}
			}
		}
		return false
	}
	if minV > 0 && v.VersionNumber < minV {
		return false
	}
	if maxV > 0 && v.VersionNumber > maxV {
		return false
	}
	return true
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
