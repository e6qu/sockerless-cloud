package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// This file implements EC2 ec2Query slices for Dedicated Hosts, Instance Event
// Windows, image attributes + lifecycle, snapshot attributes + lifecycle, and
// the VPC ClassicLink / endpoint-connection / block-public-access families.
// Each handler is faithful CRUD over a real sim.Store; DescribeImageAttribute /
// DescribeSnapshotAttribute layer per-resource attribute state onto the
// existing AMI / snapshot stores.

// EC2Host models a Dedicated Host (h-…). Real AllocateHosts reserves dedicated
// hardware for a single instance family in one AZ; the host is "available"
// until instances launch onto it.
type EC2Host struct {
	HostId                             string
	AutoPlacement                      string
	AvailabilityZone                   string
	AvailabilityZoneId                 string
	State                              string
	InstanceFamily                     string
	InstanceType                       string
	HostRecovery                       string
	HostMaintenance                    string
	AllowsMultipleInstanceTypes        bool
	OwnerId                            string
	ClientToken                        string
	AllocationTime                     string
	ReleaseTime                        string
	MemberOfServiceLinkedResourceGroup bool
	OutpostArn                         string
	AssetId                            string
	Cores                              int
	Sockets                            int
	TotalVCpus                         int
	MacHost                            bool
	MacOSVersions                      []string
	Tags                               []EC2Tag
}

// EC2InstanceEventWindow models a maintenance event window (iew-…) — a set of
// weekly time ranges (or a cron expression) plus an association target
// (instance ids, dedicated-host ids, or instance tags).
type EC2InstanceEventWindow struct {
	InstanceEventWindowId string
	Name                  string
	CronExpression        string
	State                 string
	TimeRanges            []EC2EventWindowTimeRange
	TargetInstanceIds     []string
	TargetDedicatedHosts  []string
	TargetTags            []EC2Tag
	Tags                  []EC2Tag
}

type EC2EventWindowTimeRange struct {
	StartWeekDay string
	StartHour    int
	EndWeekDay   string
	EndHour      int
}

// EC2ImageAttributes holds the mutable per-AMI attributes (launchPermission /
// description) that ModifyImageAttribute sets and DescribeImageAttribute reads.
// Keyed by ImageId in its store.
type EC2ImageAttributes struct {
	ImageId          string
	Description      string
	LaunchPermUsers  []string
	LaunchPermGroups []string
	BootMode         string
	TpmSupport       string
}

// EC2SnapshotAttributes holds the mutable createVolumePermission state for a
// snapshot (ModifySnapshotAttribute / DescribeSnapshotAttribute).
type EC2SnapshotAttributes struct {
	SnapshotId  string
	PermUsers   []string
	PermGroups  []string
	LockState   string
	LockExpires string
}

// EC2VpcClassicLink holds the per-VPC ClassicLink + ClassicLink-DNS flags.
type EC2VpcClassicLink struct {
	VpcId          string
	ClassicLink    bool
	ClassicLinkDns bool
}

// EC2VpcEndpointConnection models a pending/accepted interface-endpoint
// connection to a VPC endpoint service (vpce-conn-…).
type EC2VpcEndpointConnection struct {
	VpcEndpointConnectionId string
	ServiceId               string
	VpcEndpointId           string
	VpcEndpointOwner        string
	VpcEndpointState        string
	IpAddressType           string
	VpcEndpointRegion       string
	CreationTimestamp       string
	PayerResponsibilities   []EC2PayerResponsibilityEntry
	Tags                    []EC2Tag
}

// EC2ConnectionNotification models a VPC-endpoint connection notification
// (vpce-nfn-…) — an SNS topic notified on connection events.
type EC2ConnectionNotification struct {
	ConnectionNotificationId    string
	ServiceId                   string
	VpcEndpointId               string
	ConnectionNotificationType  string
	ConnectionNotificationArn   string
	ConnectionEvents            []string
	ConnectionNotificationState string
	ServiceRegion               string
}

// EC2VpcBpaExclusion models a VPC Block Public Access exclusion
// (vpcbpa-exclude-…) carving a VPC/subnet out of the account BPA policy.
type EC2VpcBpaExclusion struct {
	ExclusionId                  string
	InternetGatewayExclusionMode string
	ResourceArn                  string
	State                        string
	Reason                       string
	CreationTimestamp            string
	LastUpdateTimestamp          string
	DeletionTimestamp            string
	Tags                         []EC2Tag
}

var (
	ec2Hosts             sim.Store[EC2Host]
	ec2EventWindows      sim.Store[EC2InstanceEventWindow]
	ec2ImageAttrs        sim.Store[EC2ImageAttributes]
	ec2SnapshotAttrs     sim.Store[EC2SnapshotAttributes]
	ec2VpcClassicLinks   sim.Store[EC2VpcClassicLink]
	ec2VpcEndpointConns  sim.Store[EC2VpcEndpointConnection]
	ec2ConnNotifications sim.Store[EC2ConnectionNotification]
	ec2VpcBpaExclusions  sim.Store[EC2VpcBpaExclusion]
	// ec2VpcBpaOptions holds the single account-level VPC Block Public Access
	// options record (real AWS keeps one per account-region).
	ec2VpcBpaOptions sim.Store[EC2VpcBpaOptions]
)

// EC2VpcBpaOptions is the account-level Block Public Access policy.
type EC2VpcBpaOptions struct {
	State                    string
	InternetGatewayBlockMode string
	Reason                   string
	LastUpdateTimestamp      string
	ManagedBy                string
	ExclusionsAllowed        string
}

func registerEC2HostsImagesVpc(r *AWSQueryRouter, srv *sim.Server) {
	ec2Hosts = sim.MakeStore[EC2Host](srv.DB(), "ec2_hosts")
	ec2EventWindows = sim.MakeStore[EC2InstanceEventWindow](srv.DB(), "ec2_instance_event_windows")
	ec2ImageAttrs = sim.MakeStore[EC2ImageAttributes](srv.DB(), "ec2_image_attributes")
	ec2SnapshotAttrs = sim.MakeStore[EC2SnapshotAttributes](srv.DB(), "ec2_snapshot_attributes")
	ec2VpcClassicLinks = sim.MakeStore[EC2VpcClassicLink](srv.DB(), "ec2_vpc_classic_links")
	ec2VpcEndpointConns = sim.MakeStore[EC2VpcEndpointConnection](srv.DB(), "ec2_vpc_endpoint_connections")
	ec2ConnNotifications = sim.MakeStore[EC2ConnectionNotification](srv.DB(), "ec2_connection_notifications")
	ec2VpcBpaExclusions = sim.MakeStore[EC2VpcBpaExclusion](srv.DB(), "ec2_vpc_bpa_exclusions")
	ec2VpcBpaOptions = sim.MakeStore[EC2VpcBpaOptions](srv.DB(), "ec2_vpc_bpa_options")

	// Dedicated Hosts
	r.Register("AllocateHosts", handleAllocateHosts)
	r.Register("DescribeHosts", handleDescribeHosts)
	r.Register("ModifyHosts", handleModifyHosts)
	r.Register("ReleaseHosts", handleReleaseHosts)
	r.Register("DescribeMacHosts", handleDescribeMacHosts)

	// Instance Event Windows
	r.Register("CreateInstanceEventWindow", handleCreateInstanceEventWindow)
	r.Register("DescribeInstanceEventWindows", handleDescribeInstanceEventWindows)
	r.Register("ModifyInstanceEventWindow", handleModifyInstanceEventWindow)
	r.Register("DeleteInstanceEventWindow", handleDeleteInstanceEventWindow)
	r.Register("AssociateInstanceEventWindow", handleAssociateInstanceEventWindow)
	r.Register("DisassociateInstanceEventWindow", handleDisassociateInstanceEventWindow)

	// Image attributes + lifecycle
	r.Register("DescribeImageAttribute", handleDescribeImageAttribute)
	r.Register("ModifyImageAttribute", handleModifyImageAttribute)
	r.Register("ResetImageAttribute", handleResetImageAttribute)
	r.Register("DisableImage", handleDisableImage)
	r.Register("EnableImage", handleEnableImage)
	r.Register("ExportImage", handleExportImage)
	r.Register("ImportImage", handleImportImage)
	r.Register("CreateRestoreImageTask", handleCreateRestoreImageTask)
	r.Register("RestoreImageFromRecycleBin", handleRestoreImageFromRecycleBin)

	// Snapshot attributes + lifecycle
	r.Register("DescribeSnapshotAttribute", handleDescribeSnapshotAttribute)
	r.Register("ModifySnapshotAttribute", handleModifySnapshotAttribute)
	r.Register("ResetSnapshotAttribute", handleResetSnapshotAttribute)
	r.Register("DescribeSnapshotTierStatus", handleDescribeSnapshotTierStatus)
	r.Register("LockSnapshot", handleLockSnapshot)
	r.Register("UnlockSnapshot", handleUnlockSnapshot)
	r.Register("ImportSnapshot", handleImportSnapshot)

	// VPC ClassicLink
	r.Register("DescribeVpcClassicLink", handleDescribeVpcClassicLink)
	r.Register("EnableVpcClassicLink", handleEnableVpcClassicLink)
	r.Register("DisableVpcClassicLink", handleDisableVpcClassicLink)
	r.Register("DescribeVpcClassicLinkDnsSupport", handleDescribeVpcClassicLinkDnsSupport)
	r.Register("EnableVpcClassicLinkDnsSupport", handleEnableVpcClassicLinkDnsSupport)
	r.Register("DisableVpcClassicLinkDnsSupport", handleDisableVpcClassicLinkDnsSupport)

	// VPC endpoint connections + notifications
	r.Register("DescribeVpcEndpointConnections", handleDescribeVpcEndpointConnections)
	r.Register("AcceptVpcEndpointConnections", handleAcceptVpcEndpointConnections)
	r.Register("RejectVpcEndpointConnections", handleRejectVpcEndpointConnections)
	r.Register("CreateVpcEndpointConnectionNotification", handleCreateVpcEndpointConnectionNotification)
	r.Register("DescribeVpcEndpointConnectionNotifications", handleDescribeVpcEndpointConnectionNotifications)
	r.Register("ModifyVpcEndpointConnectionNotification", handleModifyVpcEndpointConnectionNotification)
	r.Register("DeleteVpcEndpointConnectionNotifications", handleDeleteVpcEndpointConnectionNotifications)

	// VPC Block Public Access
	r.Register("CreateVpcBlockPublicAccessExclusion", handleCreateVpcBlockPublicAccessExclusion)
	r.Register("DescribeVpcBlockPublicAccessExclusions", handleDescribeVpcBlockPublicAccessExclusions)
	r.Register("ModifyVpcBlockPublicAccessExclusion", handleModifyVpcBlockPublicAccessExclusion)
	r.Register("DeleteVpcBlockPublicAccessExclusion", handleDeleteVpcBlockPublicAccessExclusion)
	r.Register("ModifyVpcBlockPublicAccessOptions", handleModifyVpcBlockPublicAccessOptions)
	r.Register("DescribeVpcBlockPublicAccessOptions", handleDescribeVpcBlockPublicAccessOptions)
}

// Dedicated Hosts

func handleAllocateHosts(w http.ResponseWriter, r *http.Request) {
	az := r.FormValue("AvailabilityZone")
	if az == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter AvailabilityZone", http.StatusBadRequest)
		return
	}
	family := r.FormValue("InstanceFamily")
	instType := r.FormValue("InstanceType")
	if family == "" && instType == "" {
		ec2ErrorXML(w, "MissingParameter", "Either InstanceFamily or InstanceType is required", http.StatusBadRequest)
		return
	}
	quantity := ec2AtoiOr(r.FormValue("Quantity"), 1)
	if quantity < 1 {
		quantity = 1
	}
	auto := r.FormValue("AutoPlacement")
	if auto == "" {
		auto = "off"
	}
	recovery := r.FormValue("HostRecovery")
	if recovery == "" {
		recovery = "off"
	}
	maintenance := r.FormValue("HostMaintenance")
	if maintenance == "" {
		maintenance = "off"
	}
	tags := parseTags(r)
	var ids []string
	for i := 0; i < quantity; i++ {
		h := EC2Host{
			HostId:                      ec2ID("h"),
			AutoPlacement:               auto,
			AvailabilityZone:            az,
			AvailabilityZoneId:          ec2AvailabilityZoneId(az),
			State:                       "available",
			InstanceFamily:              family,
			InstanceType:                instType,
			HostRecovery:                recovery,
			HostMaintenance:             maintenance,
			AllowsMultipleInstanceTypes: family != "" && instType == "",
			OwnerId:                     ec2Owner(),
			ClientToken:                 r.FormValue("ClientToken"),
			AllocationTime:              ec2NowRFC3339Milli(),
			Cores:                       8,
			Sockets:                     1,
			TotalVCpus:                  16,
			Tags:                        tags,
		}
		ec2Hosts.Put(h.HostId, h)
		ids = append(ids, h.HostId)
	}
	var b strings.Builder
	b.WriteString("<hostIdSet>")
	for _, id := range ids {
		b.WriteString("<item>")
		b.WriteString(id)
		b.WriteString("</item>")
	}
	b.WriteString("</hostIdSet>")
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AllocateHostsResponse %s><requestId>%s</requestId>%s</AllocateHostsResponse>`,
		ec2Xmlns(), generateUUID(), b.String())
}

func ec2HostFieldsXML(h EC2Host) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<autoPlacement>%s</autoPlacement>", h.AutoPlacement)
	fmt.Fprintf(&b, "<availabilityZone>%s</availabilityZone>", h.AvailabilityZone)
	// availableCapacity advertises remaining instance slots for the host's family.
	famType := h.InstanceType
	if famType == "" {
		famType = h.InstanceFamily + ".large"
	}
	fmt.Fprintf(&b, "<availableCapacity><availableInstanceCapacity><item><availableCapacity>%d</availableCapacity><instanceType>%s</instanceType><totalCapacity>%d</totalCapacity></item></availableInstanceCapacity><availableVCpus>%d</availableVCpus></availableCapacity>",
		2, famType, 2, h.TotalVCpus)
	if h.ClientToken != "" {
		fmt.Fprintf(&b, "<clientToken>%s</clientToken>", xmlEscape(h.ClientToken))
	}
	fmt.Fprintf(&b, "<hostId>%s</hostId>", h.HostId)
	fmt.Fprintf(&b, "<hostProperties><cores>%d</cores>", h.Cores)
	if h.InstanceType != "" {
		fmt.Fprintf(&b, "<instanceType>%s</instanceType>", h.InstanceType)
	}
	if h.InstanceFamily != "" {
		fmt.Fprintf(&b, "<instanceFamily>%s</instanceFamily>", h.InstanceFamily)
	}
	fmt.Fprintf(&b, "<sockets>%d</sockets><totalVCpus>%d</totalVCpus></hostProperties>", h.Sockets, h.TotalVCpus)
	// instances list (empty unless instances are launched onto the host).
	b.WriteString("<instances/>")
	fmt.Fprintf(&b, "<state>%s</state>", h.State)
	fmt.Fprintf(&b, "<allocationTime>%s</allocationTime>", h.AllocationTime)
	if h.ReleaseTime != "" {
		fmt.Fprintf(&b, "<releaseTime>%s</releaseTime>", h.ReleaseTime)
	}
	b.WriteString(writeTagSetXML(h.Tags))
	fmt.Fprintf(&b, "<hostRecovery>%s</hostRecovery>", h.HostRecovery)
	fmt.Fprintf(&b, "<allowsMultipleInstanceTypes>%s</allowsMultipleInstanceTypes>", ec2OnOff(h.AllowsMultipleInstanceTypes))
	fmt.Fprintf(&b, "<ownerId>%s</ownerId>", h.OwnerId)
	fmt.Fprintf(&b, "<availabilityZoneId>%s</availabilityZoneId>", h.AvailabilityZoneId)
	fmt.Fprintf(&b, "<memberOfServiceLinkedResourceGroup>%t</memberOfServiceLinkedResourceGroup>", h.MemberOfServiceLinkedResourceGroup)
	fmt.Fprintf(&b, "<hostMaintenance>%s</hostMaintenance>", h.HostMaintenance)
	return b.String()
}

// ec2OnOff renders a host's AllowsMultipleInstanceTypes flag as the on/off enum
// AWS uses for that field.
func ec2OnOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func handleDescribeHosts(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "HostId")
	filters := ec2HostFilters(r)
	var results []EC2Host
	if len(ids) > 0 {
		for _, id := range ids {
			h, ok := ec2Hosts.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidHostID.NotFound", fmt.Sprintf("The host ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, h)
		}
	} else {
		for _, h := range ec2Hosts.List() {
			// DescribeHosts hides released hosts by default.
			if h.State == "released" || h.State == "released-permanent-failure" {
				continue
			}
			if !ec2HostMatchesFilters(h, filters) {
				continue
			}
			results = append(results, h)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].HostId < results[j].HostId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, h := range results {
		items.WriteString("<item>")
		items.WriteString(ec2HostFieldsXML(h))
		items.WriteString("</item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeHostsResponse %s><requestId>%s</requestId><hostSet>%s</hostSet>%s</DescribeHostsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

// ec2HostFilters reads the "Filter.N.Name/Value.M" form DescribeHosts uses (it
// uses the same wire shape as ec2Filters).
func ec2HostFilters(r *http.Request) map[string][]string { return ec2Filters(r) }

func ec2HostMatchesFilters(h EC2Host, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "availability-zone":
			if !ec2StrInValues(h.AvailabilityZone, vals) {
				return false
			}
		case "availability-zone-id":
			if !ec2StrInValues(h.AvailabilityZoneId, vals) {
				return false
			}
		case "instance-type":
			if !ec2StrInValues(h.InstanceType, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(h.State, vals) {
				return false
			}
		case "auto-placement":
			if !ec2StrInValues(h.AutoPlacement, vals) {
				return false
			}
		case "client-token":
			if !ec2StrInValues(h.ClientToken, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, h.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyHosts(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "HostId")
	if len(ids) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter HostId", http.StatusBadRequest)
		return
	}
	auto := r.FormValue("AutoPlacement")
	recovery := r.FormValue("HostRecovery")
	maintenance := r.FormValue("HostMaintenance")
	family := r.FormValue("InstanceFamily")
	instType := r.FormValue("InstanceType")
	var successful []string
	var unsuccessful []ec2UnsuccessfulItem
	for _, id := range ids {
		h, ok := ec2Hosts.Get(id)
		if !ok {
			unsuccessful = append(unsuccessful, ec2UnsuccessfulItem{
				ResourceId: id, Code: "InvalidHostID.NotFound",
				Message: fmt.Sprintf("The host ID %q does not exist", id),
			})
			continue
		}
		if auto != "" {
			h.AutoPlacement = auto
		}
		if recovery != "" {
			h.HostRecovery = recovery
		}
		if maintenance != "" {
			h.HostMaintenance = maintenance
		}
		if family != "" {
			h.InstanceFamily = family
			h.InstanceType = ""
			h.AllowsMultipleInstanceTypes = true
		}
		if instType != "" {
			h.InstanceType = instType
			h.InstanceFamily = ""
			h.AllowsMultipleInstanceTypes = false
		}
		ec2Hosts.Put(id, h)
		successful = append(successful, id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyHostsResponse %s><requestId>%s</requestId>%s%s</ModifyHostsResponse>`,
		ec2Xmlns(), generateUUID(), ec2SuccessfulIDsXML(successful), ec2UnsuccessfulItemsXML(unsuccessful))
}

func handleReleaseHosts(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "HostId")
	if len(ids) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter HostId", http.StatusBadRequest)
		return
	}
	var successful []string
	var unsuccessful []ec2UnsuccessfulItem
	for _, id := range ids {
		h, ok := ec2Hosts.Get(id)
		if !ok {
			unsuccessful = append(unsuccessful, ec2UnsuccessfulItem{
				ResourceId: id, Code: "InvalidHostID.NotFound",
				Message: fmt.Sprintf("The host ID %q does not exist", id),
			})
			continue
		}
		h.State = "released"
		h.ReleaseTime = ec2NowRFC3339Milli()
		ec2Hosts.Put(id, h)
		successful = append(successful, id)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReleaseHostsResponse %s><requestId>%s</requestId>%s%s</ReleaseHostsResponse>`,
		ec2Xmlns(), generateUUID(), ec2SuccessfulIDsXML(successful), ec2UnsuccessfulItemsXML(unsuccessful))
}

func handleDescribeMacHosts(w http.ResponseWriter, r *http.Request) {
	filters := ec2Filters(r)
	hostIDs := ec2ParamList(r, "HostId")
	var results []EC2Host
	for _, h := range ec2Hosts.List() {
		if !h.MacHost {
			continue
		}
		if len(hostIDs) > 0 && !ec2StrInValues(h.HostId, hostIDs) {
			continue
		}
		if !ec2HostMatchesFilters(h, filters) {
			continue
		}
		results = append(results, h)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].HostId < results[j].HostId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, h := range results {
		items.WriteString("<item><hostId>")
		items.WriteString(h.HostId)
		items.WriteString("</hostId><macOSLatestSupportedVersionSet>")
		for _, v := range h.MacOSVersions {
			items.WriteString("<item>")
			items.WriteString(xmlEscape(v))
			items.WriteString("</item>")
		}
		items.WriteString("</macOSLatestSupportedVersionSet></item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeMacHostsResponse %s><requestId>%s</requestId><macHostSet>%s</macHostSet>%s</DescribeMacHostsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

// ec2UnsuccessfulItem is the shared UnsuccessfulItem shape returned by the
// batch host / endpoint-connection ops.
type ec2UnsuccessfulItem struct {
	ResourceId string
	Code       string
	Message    string
}

func ec2SuccessfulIDsXML(ids []string) string {
	var b strings.Builder
	b.WriteString("<successful>")
	for _, id := range ids {
		b.WriteString("<item>")
		b.WriteString(id)
		b.WriteString("</item>")
	}
	b.WriteString("</successful>")
	return b.String()
}

func ec2UnsuccessfulItemsXML(items []ec2UnsuccessfulItem) string {
	var b strings.Builder
	b.WriteString("<unsuccessful>")
	for _, it := range items {
		fmt.Fprintf(&b, "<item><error><code>%s</code><message>%s</message></error><resourceId>%s</resourceId></item>",
			it.Code, xmlEscape(it.Message), it.ResourceId)
	}
	b.WriteString("</unsuccessful>")
	return b.String()
}

// Instance Event Windows

// ec2ParseEventWindowTimeRanges reads the indexed TimeRange.N.* request params.
func ec2ParseEventWindowTimeRanges(r *http.Request) []EC2EventWindowTimeRange {
	var ranges []EC2EventWindowTimeRange
	for i := 1; ; i++ {
		sw := r.FormValue(fmt.Sprintf("TimeRange.%d.StartWeekDay", i))
		ew := r.FormValue(fmt.Sprintf("TimeRange.%d.EndWeekDay", i))
		sh := r.FormValue(fmt.Sprintf("TimeRange.%d.StartHour", i))
		eh := r.FormValue(fmt.Sprintf("TimeRange.%d.EndHour", i))
		if sw == "" && ew == "" && sh == "" && eh == "" {
			break
		}
		ranges = append(ranges, EC2EventWindowTimeRange{
			StartWeekDay: sw,
			StartHour:    ec2AtoiOr(sh, 0),
			EndWeekDay:   ew,
			EndHour:      ec2AtoiOr(eh, 0),
		})
	}
	return ranges
}

func handleCreateInstanceEventWindow(w http.ResponseWriter, r *http.Request) {
	cron := r.FormValue("CronExpression")
	ranges := ec2ParseEventWindowTimeRanges(r)
	if cron == "" && len(ranges) == 0 {
		ec2ErrorXML(w, "MissingParameter", "Either CronExpression or TimeRange is required", http.StatusBadRequest)
		return
	}
	ew := EC2InstanceEventWindow{
		InstanceEventWindowId: ec2ID("iew"),
		Name:                  r.FormValue("Name"),
		CronExpression:        cron,
		State:                 "creating",
		TimeRanges:            ranges,
		Tags:                  parseTags(r),
	}
	ec2EventWindows.Put(ew.InstanceEventWindowId, ew)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInstanceEventWindowResponse %s><requestId>%s</requestId><instanceEventWindow>%s</instanceEventWindow></CreateInstanceEventWindowResponse>`,
		ec2Xmlns(), generateUUID(), ec2EventWindowFieldsXML(ew))
}

func ec2EventWindowFieldsXML(ew EC2InstanceEventWindow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<instanceEventWindowId>%s</instanceEventWindowId>", ew.InstanceEventWindowId)
	if len(ew.TimeRanges) > 0 {
		b.WriteString("<timeRangeSet>")
		for _, tr := range ew.TimeRanges {
			fmt.Fprintf(&b, "<item><startWeekDay>%s</startWeekDay><startHour>%d</startHour><endWeekDay>%s</endWeekDay><endHour>%d</endHour></item>",
				tr.StartWeekDay, tr.StartHour, tr.EndWeekDay, tr.EndHour)
		}
		b.WriteString("</timeRangeSet>")
	}
	if ew.Name != "" {
		fmt.Fprintf(&b, "<name>%s</name>", xmlEscape(ew.Name))
	}
	if ew.CronExpression != "" {
		fmt.Fprintf(&b, "<cronExpression>%s</cronExpression>", xmlEscape(ew.CronExpression))
	}
	if len(ew.TargetInstanceIds) > 0 || len(ew.TargetDedicatedHosts) > 0 || len(ew.TargetTags) > 0 {
		b.WriteString("<associationTarget>")
		if len(ew.TargetInstanceIds) > 0 {
			b.WriteString("<instanceIdSet>")
			for _, id := range ew.TargetInstanceIds {
				fmt.Fprintf(&b, "<item>%s</item>", id)
			}
			b.WriteString("</instanceIdSet>")
		}
		b.WriteString(writeTagSetXML(ew.TargetTags))
		if len(ew.TargetDedicatedHosts) > 0 {
			b.WriteString("<dedicatedHostIdSet>")
			for _, id := range ew.TargetDedicatedHosts {
				fmt.Fprintf(&b, "<item>%s</item>", id)
			}
			b.WriteString("</dedicatedHostIdSet>")
		}
		b.WriteString("</associationTarget>")
	}
	fmt.Fprintf(&b, "<state>%s</state>", ew.State)
	b.WriteString(writeTagSetXML(ew.Tags))
	return b.String()
}

func handleDescribeInstanceEventWindows(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceEventWindowId")
	filters := ec2Filters(r)
	var results []EC2InstanceEventWindow
	if len(ids) > 0 {
		for _, id := range ids {
			ew, ok := ec2EventWindows.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidInstanceEventWindowId.NotFound", fmt.Sprintf("The instance event window %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, ew)
		}
	} else {
		for _, ew := range ec2EventWindows.List() {
			if !ec2EventWindowMatchesFilters(ew, filters) {
				continue
			}
			results = append(results, ew)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].InstanceEventWindowId < results[j].InstanceEventWindowId })
	nextToken := ""
	if len(ids) == 0 {
		results, nextToken = awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var items strings.Builder
	for _, ew := range results {
		items.WriteString("<item>")
		items.WriteString(ec2EventWindowFieldsXML(ew))
		items.WriteString("</item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceEventWindowsResponse %s><requestId>%s</requestId><instanceEventWindowSet>%s</instanceEventWindowSet>%s</DescribeInstanceEventWindowsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

func ec2EventWindowMatchesFilters(ew EC2InstanceEventWindow, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "instance-event-window-id":
			if !ec2StrInValues(ew.InstanceEventWindowId, vals) {
				return false
			}
		case "event-window-name":
			if !ec2StrInValues(ew.Name, vals) {
				return false
			}
		case "cron-expression":
			if !ec2StrInValues(ew.CronExpression, vals) {
				return false
			}
		case "instance-id":
			matched := false
			for _, id := range ew.TargetInstanceIds {
				if ec2StrInValues(id, vals) {
					matched = true
				}
			}
			if !matched {
				return false
			}
		case "dedicated-host-id":
			matched := false
			for _, id := range ew.TargetDedicatedHosts {
				if ec2StrInValues(id, vals) {
					matched = true
				}
			}
			if !matched {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, ew.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleModifyInstanceEventWindow(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceEventWindowId")
	ew, ok := ec2EventWindows.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceEventWindowId.NotFound", fmt.Sprintf("The instance event window %q does not exist", id), http.StatusBadRequest)
		return
	}
	if n := r.FormValue("Name"); n != "" {
		ew.Name = n
	}
	if cron := r.FormValue("CronExpression"); cron != "" {
		ew.CronExpression = cron
		ew.TimeRanges = nil
	}
	if ranges := ec2ParseEventWindowTimeRanges(r); len(ranges) > 0 {
		ew.TimeRanges = ranges
		ew.CronExpression = ""
	}
	ew.State = "active"
	ec2EventWindows.Put(id, ew)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceEventWindowResponse %s><requestId>%s</requestId><instanceEventWindow>%s</instanceEventWindow></ModifyInstanceEventWindowResponse>`,
		ec2Xmlns(), generateUUID(), ec2EventWindowFieldsXML(ew))
}

func handleDeleteInstanceEventWindow(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceEventWindowId")
	if _, ok := ec2EventWindows.Get(id); !ok {
		ec2ErrorXML(w, "InvalidInstanceEventWindowId.NotFound", fmt.Sprintf("The instance event window %q does not exist", id), http.StatusBadRequest)
		return
	}
	ec2EventWindows.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteInstanceEventWindowResponse %s><requestId>%s</requestId><instanceEventWindowState><instanceEventWindowId>%s</instanceEventWindowId><state>deleting</state></instanceEventWindowState></DeleteInstanceEventWindowResponse>`,
		ec2Xmlns(), generateUUID(), id)
}

func handleAssociateInstanceEventWindow(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceEventWindowId")
	ew, ok := ec2EventWindows.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceEventWindowId.NotFound", fmt.Sprintf("The instance event window %q does not exist", id), http.StatusBadRequest)
		return
	}
	for _, iid := range ec2ParamList(r, "AssociationTarget.InstanceId") {
		if !ec2StrInValues(iid, ew.TargetInstanceIds) {
			ew.TargetInstanceIds = append(ew.TargetInstanceIds, iid)
		}
	}
	for _, hid := range ec2ParamList(r, "AssociationTarget.DedicatedHostId") {
		if !ec2StrInValues(hid, ew.TargetDedicatedHosts) {
			ew.TargetDedicatedHosts = append(ew.TargetDedicatedHosts, hid)
		}
	}
	// Instance tags target: AssociationTarget.InstanceTag.N.Key/Value
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("AssociationTarget.InstanceTag.%d.Key", i))
		if key == "" {
			break
		}
		ew.TargetTags = append(ew.TargetTags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("AssociationTarget.InstanceTag.%d.Value", i))})
	}
	ew.State = "active"
	ec2EventWindows.Put(id, ew)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateInstanceEventWindowResponse %s><requestId>%s</requestId><instanceEventWindow>%s</instanceEventWindow></AssociateInstanceEventWindowResponse>`,
		ec2Xmlns(), generateUUID(), ec2EventWindowFieldsXML(ew))
}

func handleDisassociateInstanceEventWindow(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceEventWindowId")
	ew, ok := ec2EventWindows.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceEventWindowId.NotFound", fmt.Sprintf("The instance event window %q does not exist", id), http.StatusBadRequest)
		return
	}
	remInst := ec2ParamList(r, "AssociationTarget.InstanceId")
	if len(remInst) > 0 {
		var kept []string
		for _, iid := range ew.TargetInstanceIds {
			if !ec2StrInValues(iid, remInst) {
				kept = append(kept, iid)
			}
		}
		ew.TargetInstanceIds = kept
	}
	remHost := ec2ParamList(r, "AssociationTarget.DedicatedHostId")
	if len(remHost) > 0 {
		var kept []string
		for _, hid := range ew.TargetDedicatedHosts {
			if !ec2StrInValues(hid, remHost) {
				kept = append(kept, hid)
			}
		}
		ew.TargetDedicatedHosts = kept
	}
	ec2EventWindows.Put(id, ew)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisassociateInstanceEventWindowResponse %s><requestId>%s</requestId><instanceEventWindow>%s</instanceEventWindow></DisassociateInstanceEventWindowResponse>`,
		ec2Xmlns(), generateUUID(), ec2EventWindowFieldsXML(ew))
}

// Image attributes + lifecycle

func handleDescribeImageAttribute(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	attr := r.FormValue("Attribute")
	if imageID == "" || attr == "" {
		ec2ErrorXML(w, "MissingParameter", "ImageId and Attribute are required", http.StatusBadRequest)
		return
	}
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	ia, _ := ec2ImageAttrs.Get(imageID)
	var body strings.Builder
	fmt.Fprintf(&body, "<imageId>%s</imageId>", imageID)
	switch attr {
	case "description":
		desc := ia.Description
		if desc == "" {
			desc = img.Description
		}
		fmt.Fprintf(&body, "<description><value>%s</value></description>", xmlEscape(desc))
	case "launchPermission":
		body.WriteString("<launchPermission>")
		for _, u := range ia.LaunchPermUsers {
			fmt.Fprintf(&body, "<item><userId>%s</userId></item>", u)
		}
		for _, g := range ia.LaunchPermGroups {
			fmt.Fprintf(&body, "<item><group>%s</group></item>", g)
		}
		body.WriteString("</launchPermission>")
	case "kernel":
		body.WriteString("<kernel><value></value></kernel>")
	case "ramdisk":
		body.WriteString("<ramdisk><value></value></ramdisk>")
	case "sriovNetSupport":
		body.WriteString("<sriovNetSupport><value>simple</value></sriovNetSupport>")
	case "bootMode":
		bm := ia.BootMode
		if bm == "" {
			bm = "uefi"
		}
		fmt.Fprintf(&body, "<bootMode><value>%s</value></bootMode>", bm)
	case "tpmSupport":
		fmt.Fprintf(&body, "<tpmSupport><value>%s</value></tpmSupport>", ia.TpmSupport)
	case "productCodes":
		body.WriteString("<productCodes/>")
	case "blockDeviceMapping":
		body.WriteString("<blockDeviceMapping/>")
	default:
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("Invalid image attribute %q", attr), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeImageAttributeResponse %s><requestId>%s</requestId>%s</DescribeImageAttributeResponse>`,
		ec2Xmlns(), generateUUID(), body.String())
}

func handleModifyImageAttribute(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	ia, _ := ec2ImageAttrs.Get(imageID)
	ia.ImageId = imageID
	attr := r.FormValue("Attribute")
	value := r.FormValue("Value")
	// Description can arrive either as Description.Value (struct form) or
	// Attribute=description + Value (legacy form).
	if d := r.FormValue("Description.Value"); d != "" {
		ia.Description = d
		img.Description = d
		ec2Images.Put(imageID, img)
	} else if attr == "description" && value != "" {
		ia.Description = value
		img.Description = value
		ec2Images.Put(imageID, img)
	}

	// LaunchPermission add/remove. AWS supports OperationType=add|remove with
	// the legacy form (UserGroup.N / UserId.N) and the structured form
	// (LaunchPermission.Add.N.UserId / LaunchPermission.Remove.N.Group).
	applyPerm := func(users, groups []string, add bool) {
		for _, u := range users {
			ia.LaunchPermUsers = ec2StrSetOp(ia.LaunchPermUsers, u, add)
		}
		for _, g := range groups {
			ia.LaunchPermGroups = ec2StrSetOp(ia.LaunchPermGroups, g, add)
			if g == "all" {
				img.Public = add
			}
		}
	}
	if op := r.FormValue("OperationType"); op != "" {
		applyPerm(ec2ParamList(r, "UserId"), ec2ParamList(r, "UserGroup"), op == "add")
		ec2Images.Put(imageID, img)
	}
	applyPerm(ec2IndexedField(r, "LaunchPermission.Add", "UserId"), ec2IndexedField(r, "LaunchPermission.Add", "Group"), true)
	applyPerm(ec2IndexedField(r, "LaunchPermission.Remove", "UserId"), ec2IndexedField(r, "LaunchPermission.Remove", "Group"), false)
	for _, g := range ia.LaunchPermGroups {
		if g == "all" {
			img.Public = true
		}
	}
	ec2Images.Put(imageID, img)
	ec2ImageAttrs.Put(imageID, ia)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyImageAttributeResponse %s><requestId>%s</requestId><return>true</return></ModifyImageAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

// ec2IndexedField reads "<prefix>.N.<field>" values (e.g. the structured
// LaunchPermission.Add.1.UserId form).
func ec2IndexedField(r *http.Request, prefix, field string) []string {
	var out []string
	for i := 1; ; i++ {
		v := r.FormValue(fmt.Sprintf("%s.%d.%s", prefix, i, field))
		if v == "" {
			break
		}
		out = append(out, v)
	}
	return out
}

// ec2StrSetOp adds or removes v from set (idempotently).
func ec2StrSetOp(set []string, v string, add bool) []string {
	if add {
		if !ec2StrInValues(v, set) {
			return append(set, v)
		}
		return set
	}
	var out []string
	for _, x := range set {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func handleResetImageAttribute(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	// ResetImageAttribute only resets launchPermission (the lone resettable
	// AMI attribute), reverting the AMI to private.
	ia, _ := ec2ImageAttrs.Get(imageID)
	ia.ImageId = imageID
	ia.LaunchPermUsers = nil
	ia.LaunchPermGroups = nil
	ec2ImageAttrs.Put(imageID, ia)
	img.Public = false
	ec2Images.Put(imageID, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetImageAttributeResponse %s><requestId>%s</requestId><return>true</return></ResetImageAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDisableImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	img.State = "disabled"
	ec2Images.Put(imageID, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableImageResponse %s><requestId>%s</requestId><return>true</return></DisableImageResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleEnableImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	img, ok := ec2Images.Get(imageID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	img.State = "available"
	ec2Images.Put(imageID, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableImageResponse %s><requestId>%s</requestId><return>true</return></EnableImageResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleExportImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if _, ok := ec2Images.Get(imageID); !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	format := r.FormValue("DiskImageFormat")
	if format == "" {
		format = "VMDK"
	}
	bucket := r.FormValue("S3ExportLocation.S3Bucket")
	prefix := r.FormValue("S3ExportLocation.S3Prefix")
	taskID := ec2ID("export-ami")
	tags := parseTags(r)

	// The task the caller is handed is the task DescribeExportImageTasks
	// answers for, so the identifier this response carries is one a client can
	// actually poll.
	ec2ExportImageTasks.Put(taskID, EC2ExportImageTask{
		ExportImageTaskId: taskID,
		ImageId:           imageID,
		Description:       r.FormValue("Description"),
		DiskImageFormat:   format,
		RoleName:          r.FormValue("RoleName"),
		S3Bucket:          bucket,
		S3Prefix:          prefix,
		Status:            "active",
		Progress:          "0",
		Tags:              tags,
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ExportImageResponse %s><requestId>%s</requestId><exportImageTaskId>%s</exportImageTaskId><imageId>%s</imageId><roleName>%s</roleName><diskImageFormat>%s</diskImageFormat><description>%s</description><s3ExportLocation><s3Bucket>%s</s3Bucket><s3Prefix>%s</s3Prefix></s3ExportLocation><status>active</status><progress>0</progress>%s</ExportImageResponse>`,
		ec2Xmlns(), generateUUID(), taskID, imageID, xmlEscape(r.FormValue("RoleName")), format,
		xmlEscape(r.FormValue("Description")), xmlEscape(bucket), xmlEscape(prefix), writeTagSetXML(tags))
}

func handleImportImage(w http.ResponseWriter, r *http.Request) {
	taskID := ec2ID("import-ami")
	arch := r.FormValue("Architecture")
	if arch == "" {
		arch = "x86_64"
	}
	platform := r.FormValue("Platform")
	if platform == "" {
		platform = "Linux"
	}
	licenseType := ec2Default(r.FormValue("LicenseType"), "BYOL")
	tags := parseTags(r)

	// As with an export, the import task is recorded under the identifier the
	// response names, so DescribeImportImageTasks can answer for it.
	ec2ImportImageTasks.Put(taskID, EC2ImportImageTask{
		ImportTaskId:  taskID,
		Architecture:  arch,
		Description:   r.FormValue("Description"),
		Hypervisor:    "xen",
		Platform:      platform,
		LicenseType:   licenseType,
		Status:        "active",
		StatusMessage: "pending",
		Progress:      "2",
		Format:        r.FormValue("DiskContainer.1.Format"),
		S3Bucket:      r.FormValue("DiskContainer.1.UserBucket.S3Bucket"),
		S3Key:         r.FormValue("DiskContainer.1.UserBucket.S3Key"),
		Url:           r.FormValue("DiskContainer.1.Url"),
		Tags:          tags,
	})

	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportImageResponse %s><requestId>%s</requestId><importTaskId>%s</importTaskId><architecture>%s</architecture><platform>%s</platform><description>%s</description><licenseType>%s</licenseType><hypervisor>xen</hypervisor><status>active</status><statusMessage>pending</statusMessage><progress>2</progress><snapshotDetailSet/>%s</ImportImageResponse>`,
		ec2Xmlns(), generateUUID(), taskID, arch, platform, xmlEscape(r.FormValue("Description")),
		licenseType, writeTagSetXML(tags))
}

func handleCreateRestoreImageTask(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		name = "restored-ami"
	}
	img := EC2Image{
		ImageId:            ec2ID("ami"),
		Name:               name,
		State:              "available",
		OwnerId:            ec2Owner(),
		Architecture:       "x86_64",
		ImageType:          "machine",
		RootDeviceType:     "ebs",
		RootDeviceName:     "/dev/sda1",
		VirtualizationType: "hvm",
		Hypervisor:         "xen",
		CreationDate:       ec2NowRFC3339Milli(),
		Tags:               parseTags(r),
	}
	ec2Images.Put(img.ImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateRestoreImageTaskResponse %s><requestId>%s</requestId><imageId>%s</imageId></CreateRestoreImageTaskResponse>`,
		ec2Xmlns(), generateUUID(), img.ImageId)
}

func handleRestoreImageFromRecycleBin(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if img, ok := ec2Images.Get(imageID); ok {
		img.State = "available"
		ec2Images.Put(imageID, img)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RestoreImageFromRecycleBinResponse %s><requestId>%s</requestId><return>true</return></RestoreImageFromRecycleBinResponse>`,
		ec2Xmlns(), generateUUID())
}

func ec2Default(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Snapshot attributes + lifecycle

func handleDescribeSnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	attr := r.FormValue("Attribute")
	if snapID == "" || attr == "" {
		ec2ErrorXML(w, "MissingParameter", "SnapshotId and Attribute are required", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Snapshots.Get(snapID); !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	sa, _ := ec2SnapshotAttrs.Get(snapID)
	var body strings.Builder
	fmt.Fprintf(&body, "<snapshotId>%s</snapshotId>", snapID)
	if attr == "createVolumePermission" {
		body.WriteString("<createVolumePermission>")
		for _, u := range sa.PermUsers {
			fmt.Fprintf(&body, "<item><userId>%s</userId></item>", u)
		}
		for _, g := range sa.PermGroups {
			fmt.Fprintf(&body, "<item><group>%s</group></item>", g)
		}
		body.WriteString("</createVolumePermission>")
	} else {
		body.WriteString("<productCodes/>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSnapshotAttributeResponse %s><requestId>%s</requestId>%s</DescribeSnapshotAttributeResponse>`,
		ec2Xmlns(), generateUUID(), body.String())
}

func handleModifySnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	if snapID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SnapshotId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Snapshots.Get(snapID); !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	sa, _ := ec2SnapshotAttrs.Get(snapID)
	sa.SnapshotId = snapID
	op := r.FormValue("OperationType")
	add := op == "add"
	// Legacy form: UserId.N / GroupName.N.
	for _, u := range ec2ParamList(r, "UserId") {
		sa.PermUsers = ec2StrSetOp(sa.PermUsers, u, add)
	}
	for _, g := range ec2ParamList(r, "GroupName") {
		sa.PermGroups = ec2StrSetOp(sa.PermGroups, g, add)
	}
	// Structured form: CreateVolumePermission.Add.N / .Remove.N.
	for _, u := range ec2IndexedField(r, "CreateVolumePermission.Add", "UserId") {
		sa.PermUsers = ec2StrSetOp(sa.PermUsers, u, true)
	}
	for _, g := range ec2IndexedField(r, "CreateVolumePermission.Add", "Group") {
		sa.PermGroups = ec2StrSetOp(sa.PermGroups, g, true)
	}
	for _, u := range ec2IndexedField(r, "CreateVolumePermission.Remove", "UserId") {
		sa.PermUsers = ec2StrSetOp(sa.PermUsers, u, false)
	}
	for _, g := range ec2IndexedField(r, "CreateVolumePermission.Remove", "Group") {
		sa.PermGroups = ec2StrSetOp(sa.PermGroups, g, false)
	}
	ec2SnapshotAttrs.Put(snapID, sa)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifySnapshotAttributeResponse %s><requestId>%s</requestId><return>true</return></ModifySnapshotAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleResetSnapshotAttribute(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	if snapID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SnapshotId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Snapshots.Get(snapID); !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	sa, _ := ec2SnapshotAttrs.Get(snapID)
	sa.SnapshotId = snapID
	sa.PermUsers = nil
	sa.PermGroups = nil
	ec2SnapshotAttrs.Put(snapID, sa)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetSnapshotAttributeResponse %s><requestId>%s</requestId><return>true</return></ResetSnapshotAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDescribeSnapshotTierStatus(w http.ResponseWriter, r *http.Request) {
	filters := ec2Filters(r)
	var results []EC2Snapshot
	for _, s := range ec2Snapshots.List() {
		if vals, ok := filters["snapshot-id"]; ok && !ec2StrInValues(s.SnapshotId, vals) {
			continue
		}
		results = append(results, s)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].SnapshotId < results[j].SnapshotId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, s := range results {
		owner := s.OwnerId
		if owner == "" {
			owner = ec2Owner()
		}
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<snapshotId>%s</snapshotId><volumeId>%s</volumeId><status>completed</status><ownerId>%s</ownerId>",
			s.SnapshotId, s.VolumeId, owner)
		items.WriteString(writeTagSetXML(s.Tags))
		items.WriteString("<storageTier>standard</storageTier></item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeSnapshotTierStatusResponse %s><requestId>%s</requestId><snapshotTierStatusSet>%s</snapshotTierStatusSet>%s</DescribeSnapshotTierStatusResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

func handleLockSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	if snapID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SnapshotId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Snapshots.Get(snapID); !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	mode := r.FormValue("LockMode")
	if mode == "" {
		mode = "governance"
	}
	lockState := mode
	if mode == "compliance" {
		// Compliance locks observe a cooloff window before they take effect.
		lockState = "compliance-cooloff"
	}
	created := ec2NowRFC3339Milli()
	sa, _ := ec2SnapshotAttrs.Get(snapID)
	sa.SnapshotId = snapID
	sa.LockState = lockState
	ec2SnapshotAttrs.Put(snapID, sa)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<LockSnapshotResponse %s><requestId>%s</requestId><snapshotId>%s</snapshotId><lockState>%s</lockState><lockDuration>%d</lockDuration><lockCreatedOn>%s</lockCreatedOn><lockDurationStartTime>%s</lockDurationStartTime></LockSnapshotResponse>`,
		ec2Xmlns(), generateUUID(), snapID, lockState, ec2AtoiOr(r.FormValue("LockDuration"), 0), created, created)
}

func handleUnlockSnapshot(w http.ResponseWriter, r *http.Request) {
	snapID := r.FormValue("SnapshotId")
	if snapID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SnapshotId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Snapshots.Get(snapID); !ok {
		ec2ErrorXML(w, "InvalidSnapshot.NotFound", fmt.Sprintf("The snapshot %q does not exist", snapID), http.StatusBadRequest)
		return
	}
	sa, _ := ec2SnapshotAttrs.Get(snapID)
	sa.SnapshotId = snapID
	sa.LockState = ""
	ec2SnapshotAttrs.Put(snapID, sa)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<UnlockSnapshotResponse %s><requestId>%s</requestId><snapshotId>%s</snapshotId></UnlockSnapshotResponse>`,
		ec2Xmlns(), generateUUID(), snapID)
}

func handleImportSnapshot(w http.ResponseWriter, r *http.Request) {
	// ImportSnapshot creates a snapshot from an S3 disk image. Record a real
	// snapshot so subsequent DescribeSnapshots / attribute ops can find it.
	snap := EC2Snapshot{
		SnapshotId:  ec2ID("snap"),
		State:       "completed",
		StartTime:   ec2NowRFC3339Milli(),
		Progress:    "100%",
		Description: r.FormValue("Description"),
		OwnerId:     ec2Owner(),
		VolumeSize:  8,
		Tags:        parseTags(r),
	}
	ec2Snapshots.Put(snap.SnapshotId, snap)
	taskID := ec2ID("import-snap")
	bucket := r.FormValue("DiskContainer.UserBucket.S3Bucket")
	key := r.FormValue("DiskContainer.UserBucket.S3Key")
	format := r.FormValue("DiskContainer.Format")
	if format == "" {
		format = "VMDK"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ImportSnapshotResponse %s><requestId>%s</requestId><importTaskId>%s</importTaskId><description>%s</description><snapshotTaskDetail><snapshotId>%s</snapshotId><status>completed</status><progress>100</progress><diskImageSize>8.0</diskImageSize><format>%s</format><userBucket><s3Bucket>%s</s3Bucket><s3Key>%s</s3Key></userBucket></snapshotTaskDetail>%s</ImportSnapshotResponse>`,
		ec2Xmlns(), generateUUID(), taskID, xmlEscape(snap.Description), snap.SnapshotId, format,
		xmlEscape(bucket), xmlEscape(key), writeTagSetXML(snap.Tags))
}

// VPC ClassicLink

func handleDescribeVpcClassicLink(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcId")
	var results []EC2Vpc
	if len(ids) > 0 {
		for _, id := range ids {
			vpc, ok := ec2Vpcs.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, vpc)
		}
	} else {
		results = ec2Vpcs.List()
	}
	sort.Slice(results, func(i, j int) bool { return results[i].VpcId < results[j].VpcId })
	var items strings.Builder
	for _, vpc := range results {
		cl, _ := ec2VpcClassicLinks.Get(vpc.VpcId)
		items.WriteString("<item>")
		fmt.Fprintf(&items, "<vpcId>%s</vpcId><classicLinkEnabled>%t</classicLinkEnabled>", vpc.VpcId, cl.ClassicLink)
		items.WriteString(writeTagSetXML(vpc.Tags))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcClassicLinkResponse %s><requestId>%s</requestId><vpcSet>%s</vpcSet></DescribeVpcClassicLinkResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2SetVpcClassicLink(w http.ResponseWriter, r *http.Request, op, dns string, enable bool) {
	vpcID := r.FormValue("VpcId")
	if vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter VpcId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Vpcs.Get(vpcID); !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcID), http.StatusBadRequest)
		return
	}
	cl, _ := ec2VpcClassicLinks.Get(vpcID)
	cl.VpcId = vpcID
	switch dns {
	case "dns":
		cl.ClassicLinkDns = enable
	default:
		cl.ClassicLink = enable
	}
	ec2VpcClassicLinks.Put(vpcID, cl)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId><return>true</return></%sResponse>`,
		op, ec2Xmlns(), generateUUID(), op)
}

func handleEnableVpcClassicLink(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcClassicLink(w, r, "EnableVpcClassicLink", "", true)
}

func handleDisableVpcClassicLink(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcClassicLink(w, r, "DisableVpcClassicLink", "", false)
}

func handleEnableVpcClassicLinkDnsSupport(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcClassicLink(w, r, "EnableVpcClassicLinkDnsSupport", "dns", true)
}

func handleDisableVpcClassicLinkDnsSupport(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcClassicLink(w, r, "DisableVpcClassicLinkDnsSupport", "dns", false)
}

func handleDescribeVpcClassicLinkDnsSupport(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "VpcIds")
	if len(ids) == 0 {
		ids = ec2ParamList(r, "VpcId")
	}
	var results []EC2Vpc
	if len(ids) > 0 {
		for _, id := range ids {
			vpc, ok := ec2Vpcs.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, vpc)
		}
	} else {
		results = ec2Vpcs.List()
	}
	sort.Slice(results, func(i, j int) bool { return results[i].VpcId < results[j].VpcId })
	var items strings.Builder
	for _, vpc := range results {
		cl, _ := ec2VpcClassicLinks.Get(vpc.VpcId)
		fmt.Fprintf(&items, "<item><vpcId>%s</vpcId><classicLinkDnsSupported>%t</classicLinkDnsSupported></item>", vpc.VpcId, cl.ClassicLinkDns)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcClassicLinkDnsSupportResponse %s><requestId>%s</requestId><vpcs>%s</vpcs></DescribeVpcClassicLinkDnsSupportResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

// VPC endpoint connections + notifications

func ec2VpcEndpointConnFieldsXML(c EC2VpcEndpointConnection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<serviceId>%s</serviceId>", c.ServiceId)
	fmt.Fprintf(&b, "<vpcEndpointId>%s</vpcEndpointId>", c.VpcEndpointId)
	fmt.Fprintf(&b, "<vpcEndpointOwner>%s</vpcEndpointOwner>", c.VpcEndpointOwner)
	fmt.Fprintf(&b, "<vpcEndpointState>%s</vpcEndpointState>", c.VpcEndpointState)
	fmt.Fprintf(&b, "<creationTimestamp>%s</creationTimestamp>", c.CreationTimestamp)
	if c.IpAddressType != "" {
		fmt.Fprintf(&b, "<ipAddressType>%s</ipAddressType>", c.IpAddressType)
	}
	fmt.Fprintf(&b, "<vpcEndpointConnectionId>%s</vpcEndpointConnectionId>", c.VpcEndpointConnectionId)
	b.WriteString(vpcePayerResponsibilitiesXML(c.PayerResponsibilities))
	b.WriteString(writeTagSetXML(c.Tags))
	if c.VpcEndpointRegion != "" {
		fmt.Fprintf(&b, "<vpcEndpointRegion>%s</vpcEndpointRegion>", c.VpcEndpointRegion)
	}
	return b.String()
}

func handleDescribeVpcEndpointConnections(w http.ResponseWriter, r *http.Request) {
	filters := ec2Filters(r)
	var results []EC2VpcEndpointConnection
	for _, c := range ec2VpcEndpointConns.List() {
		if vals, ok := filters["service-id"]; ok && !ec2StrInValues(c.ServiceId, vals) {
			continue
		}
		if vals, ok := filters["vpc-endpoint-state"]; ok && !ec2StrInValues(c.VpcEndpointState, vals) {
			continue
		}
		results = append(results, c)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].VpcEndpointConnectionId < results[j].VpcEndpointConnectionId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, c := range results {
		items.WriteString("<item>")
		items.WriteString(ec2VpcEndpointConnFieldsXML(c))
		items.WriteString("</item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointConnectionsResponse %s><requestId>%s</requestId><vpcEndpointConnectionSet>%s</vpcEndpointConnectionSet>%s</DescribeVpcEndpointConnectionsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

func ec2SetVpcEndpointConnState(w http.ResponseWriter, r *http.Request, op, newState string) {
	serviceID := r.FormValue("ServiceId")
	if serviceID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ServiceId", http.StatusBadRequest)
		return
	}
	var unsuccessful []ec2UnsuccessfulItem
	for _, vpceID := range ec2ParamList(r, "VpcEndpointId") {
		found := false
		for _, c := range ec2VpcEndpointConns.List() {
			if c.ServiceId == serviceID && c.VpcEndpointId == vpceID {
				c.VpcEndpointState = newState
				ec2VpcEndpointConns.Put(c.VpcEndpointConnectionId, c)
				if endpoint, ok := ec2VpcEndpoints.Get(vpceID); ok {
					endpoint.State = newState
					ec2VpcEndpoints.Put(vpceID, endpoint)
				}
				found = true
			}
		}
		if !found {
			unsuccessful = append(unsuccessful, ec2UnsuccessfulItem{
				ResourceId: vpceID, Code: "InvalidVpcEndpointId.NotFound",
				Message: fmt.Sprintf("The vpc endpoint connection for %q does not exist", vpceID),
			})
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%sResponse %s><requestId>%s</requestId>%s</%sResponse>`,
		op, ec2Xmlns(), generateUUID(), ec2UnsuccessfulItemsXML(unsuccessful), op)
}

func handleAcceptVpcEndpointConnections(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcEndpointConnState(w, r, "AcceptVpcEndpointConnections", "Available")
}

func handleRejectVpcEndpointConnections(w http.ResponseWriter, r *http.Request) {
	ec2SetVpcEndpointConnState(w, r, "RejectVpcEndpointConnections", "Rejected")
}

func ec2ConnNotificationFieldsXML(n EC2ConnectionNotification) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<connectionNotificationId>%s</connectionNotificationId>", n.ConnectionNotificationId)
	if n.ServiceId != "" {
		fmt.Fprintf(&b, "<serviceId>%s</serviceId>", n.ServiceId)
	}
	if n.VpcEndpointId != "" {
		fmt.Fprintf(&b, "<vpcEndpointId>%s</vpcEndpointId>", n.VpcEndpointId)
	}
	fmt.Fprintf(&b, "<connectionNotificationType>%s</connectionNotificationType>", n.ConnectionNotificationType)
	fmt.Fprintf(&b, "<connectionNotificationArn>%s</connectionNotificationArn>", n.ConnectionNotificationArn)
	if len(n.ConnectionEvents) > 0 {
		b.WriteString("<connectionEvents>")
		for _, e := range n.ConnectionEvents {
			fmt.Fprintf(&b, "<item>%s</item>", e)
		}
		b.WriteString("</connectionEvents>")
	}
	fmt.Fprintf(&b, "<connectionNotificationState>%s</connectionNotificationState>", n.ConnectionNotificationState)
	return b.String()
}

func handleCreateVpcEndpointConnectionNotification(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ConnectionNotificationArn")
	if arn == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ConnectionNotificationArn", http.StatusBadRequest)
		return
	}
	events := ec2ParamList(r, "ConnectionEvents")
	if len(events) == 0 {
		events = []string{"Accept", "Connect", "Delete", "Reject"}
	}
	n := EC2ConnectionNotification{
		ConnectionNotificationId:    ec2ID("vpce-nfn"),
		ServiceId:                   r.FormValue("ServiceId"),
		VpcEndpointId:               r.FormValue("VpcEndpointId"),
		ConnectionNotificationType:  "Topic",
		ConnectionNotificationArn:   arn,
		ConnectionEvents:            events,
		ConnectionNotificationState: "Enabled",
		ServiceRegion:               awsRegion(),
	}
	ec2ConnNotifications.Put(n.ConnectionNotificationId, n)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcEndpointConnectionNotificationResponse %s><requestId>%s</requestId><connectionNotification>%s</connectionNotification><clientToken>%s</clientToken></CreateVpcEndpointConnectionNotificationResponse>`,
		ec2Xmlns(), generateUUID(), ec2ConnNotificationFieldsXML(n), generateUUID())
}

func handleDescribeVpcEndpointConnectionNotifications(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ConnectionNotificationId")
	filters := ec2Filters(r)
	var results []EC2ConnectionNotification
	for _, n := range ec2ConnNotifications.List() {
		if id != "" && n.ConnectionNotificationId != id {
			continue
		}
		if vals, ok := filters["service-id"]; ok && !ec2StrInValues(n.ServiceId, vals) {
			continue
		}
		if vals, ok := filters["vpc-endpoint-id"]; ok && !ec2StrInValues(n.VpcEndpointId, vals) {
			continue
		}
		results = append(results, n)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].ConnectionNotificationId < results[j].ConnectionNotificationId
	})
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, n := range results {
		items.WriteString("<item>")
		items.WriteString(ec2ConnNotificationFieldsXML(n))
		items.WriteString("</item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcEndpointConnectionNotificationsResponse %s><requestId>%s</requestId><connectionNotificationSet>%s</connectionNotificationSet>%s</DescribeVpcEndpointConnectionNotificationsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

func handleModifyVpcEndpointConnectionNotification(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ConnectionNotificationId")
	n, ok := ec2ConnNotifications.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidConnectionNotification", fmt.Sprintf("The connection notification %q does not exist", id), http.StatusBadRequest)
		return
	}
	if arn := r.FormValue("ConnectionNotificationArn"); arn != "" {
		n.ConnectionNotificationArn = arn
	}
	if events := ec2ParamList(r, "ConnectionEvents"); len(events) > 0 {
		n.ConnectionEvents = events
	}
	ec2ConnNotifications.Put(id, n)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcEndpointConnectionNotificationResponse %s><requestId>%s</requestId><return>true</return></ModifyVpcEndpointConnectionNotificationResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDeleteVpcEndpointConnectionNotifications(w http.ResponseWriter, r *http.Request) {
	var unsuccessful []ec2UnsuccessfulItem
	for _, id := range ec2ParamList(r, "ConnectionNotificationId") {
		if !ec2ConnNotifications.Delete(id) {
			unsuccessful = append(unsuccessful, ec2UnsuccessfulItem{
				ResourceId: id, Code: "InvalidConnectionNotification",
				Message: fmt.Sprintf("The connection notification %q does not exist", id),
			})
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcEndpointConnectionNotificationsResponse %s><requestId>%s</requestId>%s</DeleteVpcEndpointConnectionNotificationsResponse>`,
		ec2Xmlns(), generateUUID(), ec2UnsuccessfulItemsXML(unsuccessful))
}

// VPC Block Public Access

func ec2VpcBpaExclusionFieldsXML(e EC2VpcBpaExclusion) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<exclusionId>%s</exclusionId>", e.ExclusionId)
	fmt.Fprintf(&b, "<internetGatewayExclusionMode>%s</internetGatewayExclusionMode>", e.InternetGatewayExclusionMode)
	if e.ResourceArn != "" {
		fmt.Fprintf(&b, "<resourceArn>%s</resourceArn>", e.ResourceArn)
	}
	fmt.Fprintf(&b, "<state>%s</state>", e.State)
	if e.Reason != "" {
		fmt.Fprintf(&b, "<reason>%s</reason>", xmlEscape(e.Reason))
	}
	if e.CreationTimestamp != "" {
		fmt.Fprintf(&b, "<creationTimestamp>%s</creationTimestamp>", e.CreationTimestamp)
	}
	if e.LastUpdateTimestamp != "" {
		fmt.Fprintf(&b, "<lastUpdateTimestamp>%s</lastUpdateTimestamp>", e.LastUpdateTimestamp)
	}
	if e.DeletionTimestamp != "" {
		fmt.Fprintf(&b, "<deletionTimestamp>%s</deletionTimestamp>", e.DeletionTimestamp)
	}
	b.WriteString(writeTagSetXML(e.Tags))
	return b.String()
}

func handleCreateVpcBlockPublicAccessExclusion(w http.ResponseWriter, r *http.Request) {
	mode := r.FormValue("InternetGatewayExclusionMode")
	if mode == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InternetGatewayExclusionMode", http.StatusBadRequest)
		return
	}
	resource := r.FormValue("VpcId")
	resType := "vpc"
	if resource == "" {
		resource = r.FormValue("SubnetId")
		resType = "subnet"
	}
	if resource == "" {
		ec2ErrorXML(w, "MissingParameter", "Either VpcId or SubnetId is required", http.StatusBadRequest)
		return
	}
	now := ec2NowRFC3339Milli()
	e := EC2VpcBpaExclusion{
		ExclusionId:                  ec2ID("vpcbpa-exclude"),
		InternetGatewayExclusionMode: mode,
		ResourceArn:                  fmt.Sprintf("arn:aws:ec2:%s:%s:%s/%s", awsRegion(), ec2Owner(), resType, resource),
		State:                        "create-in-progress",
		CreationTimestamp:            now,
		LastUpdateTimestamp:          now,
		Tags:                         parseTags(r),
	}
	ec2VpcBpaExclusions.Put(e.ExclusionId, e)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateVpcBlockPublicAccessExclusionResponse %s><requestId>%s</requestId><vpcBlockPublicAccessExclusion>%s</vpcBlockPublicAccessExclusion></CreateVpcBlockPublicAccessExclusionResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpcBpaExclusionFieldsXML(e))
}

func handleDescribeVpcBlockPublicAccessExclusions(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "ExclusionId")
	filters := ec2Filters(r)
	var results []EC2VpcBpaExclusion
	if len(ids) > 0 {
		for _, id := range ids {
			e, ok := ec2VpcBpaExclusions.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidVpcBlockPublicAccessExclusionId.NotFound", fmt.Sprintf("The exclusion %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, e)
		}
	} else {
		for _, e := range ec2VpcBpaExclusions.List() {
			if vals, ok := filters["state"]; ok && !ec2StrInValues(e.State, vals) {
				continue
			}
			results = append(results, e)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].ExclusionId < results[j].ExclusionId })
	results, nextToken := awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	var items strings.Builder
	for _, e := range results {
		items.WriteString("<item>")
		items.WriteString(ec2VpcBpaExclusionFieldsXML(e))
		items.WriteString("</item>")
	}
	nt := ""
	if nextToken != "" {
		nt = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcBlockPublicAccessExclusionsResponse %s><requestId>%s</requestId><vpcBlockPublicAccessExclusionSet>%s</vpcBlockPublicAccessExclusionSet>%s</DescribeVpcBlockPublicAccessExclusionsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nt)
}

func handleModifyVpcBlockPublicAccessExclusion(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ExclusionId")
	e, ok := ec2VpcBpaExclusions.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcBlockPublicAccessExclusionId.NotFound", fmt.Sprintf("The exclusion %q does not exist", id), http.StatusBadRequest)
		return
	}
	if mode := r.FormValue("InternetGatewayExclusionMode"); mode != "" {
		e.InternetGatewayExclusionMode = mode
	}
	e.State = "update-in-progress"
	e.LastUpdateTimestamp = ec2NowRFC3339Milli()
	ec2VpcBpaExclusions.Put(id, e)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcBlockPublicAccessExclusionResponse %s><requestId>%s</requestId><vpcBlockPublicAccessExclusion>%s</vpcBlockPublicAccessExclusion></ModifyVpcBlockPublicAccessExclusionResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpcBpaExclusionFieldsXML(e))
}

func handleDeleteVpcBlockPublicAccessExclusion(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ExclusionId")
	e, ok := ec2VpcBpaExclusions.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcBlockPublicAccessExclusionId.NotFound", fmt.Sprintf("The exclusion %q does not exist", id), http.StatusBadRequest)
		return
	}
	e.State = "delete-in-progress"
	e.DeletionTimestamp = ec2NowRFC3339Milli()
	ec2VpcBpaExclusions.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteVpcBlockPublicAccessExclusionResponse %s><requestId>%s</requestId><vpcBlockPublicAccessExclusion>%s</vpcBlockPublicAccessExclusion></DeleteVpcBlockPublicAccessExclusionResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpcBpaExclusionFieldsXML(e))
}

const ec2VpcBpaOptionsKey = "default"

func ec2VpcBpaOptionsFieldsXML(o EC2VpcBpaOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<awsAccountId>%s</awsAccountId>", ec2Owner())
	fmt.Fprintf(&b, "<awsRegion>%s</awsRegion>", awsRegion())
	fmt.Fprintf(&b, "<state>%s</state>", o.State)
	fmt.Fprintf(&b, "<internetGatewayBlockMode>%s</internetGatewayBlockMode>", o.InternetGatewayBlockMode)
	if o.Reason != "" {
		fmt.Fprintf(&b, "<reason>%s</reason>", xmlEscape(o.Reason))
	}
	if o.LastUpdateTimestamp != "" {
		fmt.Fprintf(&b, "<lastUpdateTimestamp>%s</lastUpdateTimestamp>", o.LastUpdateTimestamp)
	}
	if o.ManagedBy != "" {
		fmt.Fprintf(&b, "<managedBy>%s</managedBy>", o.ManagedBy)
	}
	if o.ExclusionsAllowed != "" {
		fmt.Fprintf(&b, "<exclusionsAllowed>%s</exclusionsAllowed>", o.ExclusionsAllowed)
	}
	return b.String()
}

func ec2CurrentVpcBpaOptions() EC2VpcBpaOptions {
	o, ok := ec2VpcBpaOptions.Get(ec2VpcBpaOptionsKey)
	if !ok {
		return EC2VpcBpaOptions{
			State:                    "off",
			InternetGatewayBlockMode: "off",
			ManagedBy:                "account",
			ExclusionsAllowed:        "allowed",
		}
	}
	return o
}

func handleModifyVpcBlockPublicAccessOptions(w http.ResponseWriter, r *http.Request) {
	mode := r.FormValue("InternetGatewayBlockMode")
	if mode == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InternetGatewayBlockMode", http.StatusBadRequest)
		return
	}
	o := ec2CurrentVpcBpaOptions()
	o.InternetGatewayBlockMode = mode
	if mode == "off" {
		o.State = "default-state"
	} else {
		o.State = "update-in-progress"
	}
	o.LastUpdateTimestamp = ec2NowRFC3339Milli()
	ec2VpcBpaOptions.Put(ec2VpcBpaOptionsKey, o)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyVpcBlockPublicAccessOptionsResponse %s><requestId>%s</requestId><vpcBlockPublicAccessOptions>%s</vpcBlockPublicAccessOptions></ModifyVpcBlockPublicAccessOptionsResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpcBpaOptionsFieldsXML(o))
}

func handleDescribeVpcBlockPublicAccessOptions(w http.ResponseWriter, r *http.Request) {
	o := ec2CurrentVpcBpaOptions()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeVpcBlockPublicAccessOptionsResponse %s><requestId>%s</requestId><vpcBlockPublicAccessOptions>%s</vpcBlockPublicAccessOptions></DescribeVpcBlockPublicAccessOptionsResponse>`,
		ec2Xmlns(), generateUUID(), ec2VpcBpaOptionsFieldsXML(o))
}
