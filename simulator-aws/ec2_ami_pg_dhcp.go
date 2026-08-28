package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// EC2Image models a user-registered AMI. The sim has no real machine images, so
// CreateImage / RegisterImage / CopyImage persist a deterministic metadata
// record that DescribeImages then echoes back. Real EC2 AMIs are backed by
// snapshots; CreateImage snapshots the instance's root volume, so we record the
// backing snapshot id and root device.
type EC2Image struct {
	ImageId            string
	Name               string
	Description        string
	State              string
	OwnerId            string
	Public             bool
	Architecture       string
	ImageType          string
	RootDeviceType     string
	RootDeviceName     string
	VirtualizationType string
	Hypervisor         string
	CreationDate       string
	SnapshotId         string
	VolumeSize         int
	SourceInstanceId   string
	SourceImageId      string
	BlockDeviceName    string
	Tags               []EC2Tag
}

// EC2PlacementGroup models an EC2 placement group (cluster/spread/partition).
type EC2PlacementGroup struct {
	GroupName      string
	GroupId        string
	State          string
	Strategy       string
	PartitionCount int
	SpreadLevel    string
	Tags           []EC2Tag
}

// EC2DhcpOptions models a DHCP options set. DhcpConfigurations is an ordered
// list of (key, values) pairs (domain-name, domain-name-servers, etc.).
type EC2DhcpConfig struct {
	Key    string
	Values []string
}

type EC2DhcpOptions struct {
	DhcpOptionsId      string
	OwnerId            string
	DhcpConfigurations []EC2DhcpConfig
	Tags               []EC2Tag
}

var (
	ec2Images          sim.Store[EC2Image]
	ec2PlacementGroups sim.Store[EC2PlacementGroup]
	ec2DhcpOptions     sim.Store[EC2DhcpOptions]
)

func registerEC2AmiPlacementDhcp(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2Images = sim.MakeStore[EC2Image](srv.DB(), "ec2_images")
	ec2PlacementGroups = sim.MakeStore[EC2PlacementGroup](srv.DB(), "ec2_placement_groups")
	ec2DhcpOptions = sim.MakeStore[EC2DhcpOptions](srv.DB(), "ec2_dhcp_options")

	// AMIs
	r.Register("CreateImage", handleCreateImage)
	r.Register("RegisterImage", handleRegisterImage)
	r.Register("DeregisterImage", handleDeregisterImage)
	r.Register("CopyImage", handleCopyImage)

	// Placement groups
	r.Register("CreatePlacementGroup", handleCreatePlacementGroup)
	r.Register("DescribePlacementGroups", handleDescribePlacementGroups)
	r.Register("DeletePlacementGroup", handleDeletePlacementGroup)

	// DHCP option sets
	r.Register("CreateDhcpOptions", handleCreateDhcpOptions)
	r.Register("DescribeDhcpOptions", handleDescribeDhcpOptions)
	r.Register("AssociateDhcpOptions", handleAssociateDhcpOptions)
	r.Register("DeleteDhcpOptions", handleDeleteDhcpOptions)
}

// handleCreateImage creates an AMI from a running/stopped instance. It records
// the instance's root device and a fresh backing snapshot id, mirroring real
// EC2 (CreateImage snapshots the root volume and returns an AMI referencing it).
func handleCreateImage(w http.ResponseWriter, r *http.Request) {
	instanceID := r.FormValue("InstanceId")
	name := r.FormValue("Name")
	if instanceID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter InstanceId", http.StatusBadRequest)
		return
	}
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Name", http.StatusBadRequest)
		return
	}
	inst, ok := ec2Instances.Get(instanceID)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceID.NotFound", fmt.Sprintf("The instance ID %q does not exist", instanceID), http.StatusBadRequest)
		return
	}
	arch := inst.Architecture
	if arch == "" {
		arch = "x86_64"
	}
	rootName := inst.RootDeviceName
	if rootName == "" {
		rootName = "/dev/sda1"
	}
	size := inst.RootVolumeSize
	if size == 0 {
		size = 8
	}
	img := EC2Image{
		ImageId:            ec2ID("ami"),
		Name:               name,
		Description:        r.FormValue("Description"),
		State:              "available",
		OwnerId:            ec2Owner(),
		Public:             false,
		Architecture:       arch,
		ImageType:          "machine",
		RootDeviceType:     "ebs",
		RootDeviceName:     rootName,
		VirtualizationType: "hvm",
		Hypervisor:         "xen",
		CreationDate:       ec2NowRFC3339Milli(),
		SnapshotId:         ec2ID("snap"),
		VolumeSize:         size,
		SourceInstanceId:   instanceID,
		BlockDeviceName:    rootName,
		Tags:               parseTags(r),
	}
	ec2Images.Put(img.ImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateImageResponse %s><requestId>%s</requestId><imageId>%s</imageId></CreateImageResponse>`,
		ec2Xmlns(), generateUUID(), img.ImageId)
}

// handleRegisterImage registers an AMI from a manifest / block device mapping.
func handleRegisterImage(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("Name")
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Name", http.StatusBadRequest)
		return
	}
	arch := r.FormValue("Architecture")
	if arch == "" {
		arch = "x86_64"
	}
	rootName := r.FormValue("RootDeviceName")
	if rootName == "" {
		rootName = "/dev/sda1"
	}
	virtType := r.FormValue("VirtualizationType")
	if virtType == "" {
		virtType = "hvm"
	}
	// A registered AMI may be EBS-backed (block device mapping with a snapshot)
	// or instance-store-backed (ImageLocation manifest, no snapshot).
	snapID := r.FormValue("BlockDeviceMapping.1.Ebs.SnapshotId")
	rootType := "instance-store"
	size := 0
	if snapID != "" {
		rootType = "ebs"
		size = ec2AtoiOr(r.FormValue("BlockDeviceMapping.1.Ebs.VolumeSize"), 8)
	}
	img := EC2Image{
		ImageId:            ec2ID("ami"),
		Name:               name,
		Description:        r.FormValue("Description"),
		State:              "available",
		OwnerId:            ec2Owner(),
		Public:             false,
		Architecture:       arch,
		ImageType:          "machine",
		RootDeviceType:     rootType,
		RootDeviceName:     rootName,
		VirtualizationType: virtType,
		Hypervisor:         "xen",
		CreationDate:       ec2NowRFC3339Milli(),
		SnapshotId:         snapID,
		VolumeSize:         size,
		BlockDeviceName:    rootName,
		Tags:               parseTags(r),
	}
	ec2Images.Put(img.ImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RegisterImageResponse %s><requestId>%s</requestId><imageId>%s</imageId></RegisterImageResponse>`,
		ec2Xmlns(), generateUUID(), img.ImageId)
}

// handleCopyImage copies a source AMI into a new AMI id, preserving its
// metadata (real CopyImage is the cross-region AMI replication primitive).
func handleCopyImage(w http.ResponseWriter, r *http.Request) {
	srcID := r.FormValue("SourceImageId")
	name := r.FormValue("Name")
	if srcID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SourceImageId", http.StatusBadRequest)
		return
	}
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Name", http.StatusBadRequest)
		return
	}
	src, ok := ec2Images.Get(srcID)
	if !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", srcID), http.StatusBadRequest)
		return
	}
	img := src
	img.ImageId = ec2ID("ami")
	img.Name = name
	if d := r.FormValue("Description"); d != "" {
		img.Description = d
	}
	img.SourceImageId = srcID
	img.SourceInstanceId = ""
	img.CreationDate = ec2NowRFC3339Milli()
	if src.SnapshotId != "" {
		img.SnapshotId = ec2ID("snap")
	}
	img.Tags = parseTags(r)
	ec2Images.Put(img.ImageId, img)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CopyImageResponse %s><requestId>%s</requestId><imageId>%s</imageId></CopyImageResponse>`,
		ec2Xmlns(), generateUUID(), img.ImageId)
}

func handleDeregisterImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.FormValue("ImageId")
	if imageID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter ImageId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2Images.Get(imageID); !ok {
		ec2ErrorXML(w, "InvalidAMIID.NotFound", fmt.Sprintf("The image id %q does not exist", imageID), http.StatusBadRequest)
		return
	}
	ec2Images.Delete(imageID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeregisterImageResponse %s><requestId>%s</requestId><return>true</return></DeregisterImageResponse>`, ec2Xmlns(), generateUUID())
}

// ec2StoredImageXML renders a user-registered AMI as a DescribeImages item.
func ec2StoredImageXML(img EC2Image) string {
	bdm := ""
	if img.SnapshotId != "" {
		bdm = fmt.Sprintf(`<blockDeviceMapping><item><deviceName>%s</deviceName><ebs><snapshotId>%s</snapshotId><volumeSize>%d</volumeSize><deleteOnTermination>true</deleteOnTermination><volumeType>gp3</volumeType></ebs></item></blockDeviceMapping>`,
			img.BlockDeviceName, img.SnapshotId, img.VolumeSize)
	}
	desc := ""
	if img.Description != "" {
		desc = fmt.Sprintf("<description>%s</description>", xmlEscape(img.Description))
	}
	srcInst := ""
	if img.SourceInstanceId != "" {
		srcInst = fmt.Sprintf("<sourceInstanceId>%s</sourceInstanceId>", img.SourceInstanceId)
	}
	srcImg := ""
	if img.SourceImageId != "" {
		srcImg = fmt.Sprintf("<sourceImageId>%s</sourceImageId>", img.SourceImageId)
	}
	return fmt.Sprintf(`<item><imageId>%s</imageId><imageLocation>%s/%s</imageLocation><imageState>%s</imageState><imageOwnerId>%s</imageOwnerId><isPublic>%t</isPublic><architecture>%s</architecture><imageType>%s</imageType><rootDeviceType>%s</rootDeviceType><rootDeviceName>%s</rootDeviceName>%s<virtualizationType>%s</virtualizationType><name>%s</name>%s<creationDate>%s</creationDate><hypervisor>%s</hypervisor>%s%s%s</item>`,
		img.ImageId, img.OwnerId, xmlEscape(img.Name), img.State, img.OwnerId, img.Public,
		img.Architecture, img.ImageType, img.RootDeviceType, img.RootDeviceName, bdm,
		img.VirtualizationType, xmlEscape(img.Name), desc, img.CreationDate, img.Hypervisor,
		srcInst, srcImg, writeTagSetXML(img.Tags))
}

func ec2ImageMatchesFilters(img EC2Image, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "image-id":
			if !ec2StrInValues(img.ImageId, vals) {
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
		case "architecture":
			if !ec2StrInValues(img.Architecture, vals) {
				return false
			}
		case "root-device-type":
			if !ec2StrInValues(img.RootDeviceType, vals) {
				return false
			}
		case "virtualization-type":
			if !ec2StrInValues(img.VirtualizationType, vals) {
				return false
			}
		case "owner-id", "owner-alias":
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

func handleCreatePlacementGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter GroupName", http.StatusBadRequest)
		return
	}
	if _, ok := ec2PlacementGroups.Get(name); ok {
		ec2ErrorXML(w, "InvalidPlacementGroup.Duplicate", fmt.Sprintf("The placement group %q already exists.", name), http.StatusBadRequest)
		return
	}
	strategy := r.FormValue("Strategy")
	if strategy == "" {
		strategy = "cluster"
	}
	pg := EC2PlacementGroup{
		GroupName:      name,
		GroupId:        ec2ID("pg"),
		State:          "available",
		Strategy:       strategy,
		PartitionCount: ec2AtoiOr(r.FormValue("PartitionCount"), 0),
		SpreadLevel:    r.FormValue("SpreadLevel"),
		Tags:           parseTags(r),
	}
	ec2PlacementGroups.Put(name, pg)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreatePlacementGroupResponse %s><requestId>%s</requestId><placementGroup>%s</placementGroup></CreatePlacementGroupResponse>`,
		ec2Xmlns(), generateUUID(), ec2PlacementGroupFieldsXML(pg))
}

func ec2PlacementGroupFieldsXML(pg EC2PlacementGroup) string {
	partition := ""
	if pg.PartitionCount > 0 {
		partition = fmt.Sprintf("<partitionCount>%d</partitionCount>", pg.PartitionCount)
	}
	spread := ""
	if pg.SpreadLevel != "" {
		spread = fmt.Sprintf("<spreadLevel>%s</spreadLevel>", pg.SpreadLevel)
	}
	arn := fmt.Sprintf("arn:aws:ec2:%s:%s:placement-group/%s", awsRegion(), pg.GroupId, pg.GroupName)
	return fmt.Sprintf("<groupName>%s</groupName><state>%s</state><strategy>%s</strategy>%s<groupId>%s</groupId><groupArn>%s</groupArn>%s%s",
		pg.GroupName, pg.State, pg.Strategy, partition, pg.GroupId, arn, spread, writeTagSetXML(pg.Tags))
}

func handleDescribePlacementGroups(w http.ResponseWriter, r *http.Request) {
	names := ec2ParamList(r, "GroupName")
	ids := ec2ParamList(r, "GroupId")
	filters := ec2Filters(r)
	var items strings.Builder
	for _, pg := range ec2PlacementGroups.List() {
		if len(names) > 0 && !ec2StrInValues(pg.GroupName, names) {
			continue
		}
		if len(ids) > 0 && !ec2StrInValues(pg.GroupId, ids) {
			continue
		}
		if !ec2PlacementGroupMatchesFilters(pg, filters) {
			continue
		}
		items.WriteString("<item>")
		items.WriteString(ec2PlacementGroupFieldsXML(pg))
		items.WriteString("</item>")
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribePlacementGroupsResponse %s><requestId>%s</requestId><placementGroupSet>%s</placementGroupSet></DescribePlacementGroupsResponse>`,
		ec2Xmlns(), generateUUID(), items.String())
}

func ec2PlacementGroupMatchesFilters(pg EC2PlacementGroup, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "group-name":
			if !ec2StrInValues(pg.GroupName, vals) {
				return false
			}
		case "group-arn":
			arn := fmt.Sprintf("arn:aws:ec2:%s:%s:placement-group/%s", awsRegion(), pg.GroupId, pg.GroupName)
			if !ec2StrInValues(arn, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(pg.State, vals) {
				return false
			}
		case "strategy":
			if !ec2StrInValues(pg.Strategy, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, pg.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDeletePlacementGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("GroupName")
	if name == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter GroupName", http.StatusBadRequest)
		return
	}
	if _, ok := ec2PlacementGroups.Get(name); !ok {
		ec2ErrorXML(w, "InvalidPlacementGroup.Unknown", fmt.Sprintf("The placement group %q does not exist", name), http.StatusBadRequest)
		return
	}
	ec2PlacementGroups.Delete(name)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeletePlacementGroupResponse %s><requestId>%s</requestId><return>true</return></DeletePlacementGroupResponse>`, ec2Xmlns(), generateUUID())
}

// ec2ParseDhcpConfigurations reads the indexed DhcpConfiguration.N.Key /
// DhcpConfiguration.N.Value.M request params into ordered config entries.
func ec2ParseDhcpConfigurations(r *http.Request) []EC2DhcpConfig {
	var configs []EC2DhcpConfig
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("DhcpConfiguration.%d.Key", i))
		if key == "" {
			break
		}
		var vals []string
		for j := 1; ; j++ {
			v := r.FormValue(fmt.Sprintf("DhcpConfiguration.%d.Value.%d", i, j))
			if v == "" {
				break
			}
			vals = append(vals, v)
		}
		configs = append(configs, EC2DhcpConfig{Key: key, Values: vals})
	}
	return configs
}

func handleCreateDhcpOptions(w http.ResponseWriter, r *http.Request) {
	opts := EC2DhcpOptions{
		DhcpOptionsId:      ec2ID("dopt"),
		OwnerId:            ec2Owner(),
		DhcpConfigurations: ec2ParseDhcpConfigurations(r),
		Tags:               parseTags(r),
	}
	ec2DhcpOptions.Put(opts.DhcpOptionsId, opts)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateDhcpOptionsResponse %s><requestId>%s</requestId><dhcpOptions>%s</dhcpOptions></CreateDhcpOptionsResponse>`,
		ec2Xmlns(), generateUUID(), ec2DhcpOptionsFieldsXML(opts))
}

func ec2DhcpOptionsFieldsXML(opts EC2DhcpOptions) string {
	var cfg strings.Builder
	cfg.WriteString("<dhcpConfigurationSet>")
	for _, c := range opts.DhcpConfigurations {
		cfg.WriteString("<item><key>")
		cfg.WriteString(xmlEscape(c.Key))
		cfg.WriteString("</key><valueSet>")
		for _, v := range c.Values {
			cfg.WriteString("<item><value>")
			cfg.WriteString(xmlEscape(v))
			cfg.WriteString("</value></item>")
		}
		cfg.WriteString("</valueSet></item>")
	}
	cfg.WriteString("</dhcpConfigurationSet>")
	return fmt.Sprintf("<dhcpOptionsId>%s</dhcpOptionsId><ownerId>%s</ownerId>%s%s",
		opts.DhcpOptionsId, opts.OwnerId, cfg.String(), writeTagSetXML(opts.Tags))
}

func handleDescribeDhcpOptions(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "DhcpOptionsId")
	filters := ec2Filters(r)
	results := make([]EC2DhcpOptions, 0)
	if len(ids) > 0 {
		for _, id := range ids {
			opts, ok := ec2DhcpOptions.Get(id)
			if !ok {
				ec2ErrorXML(w, "InvalidDhcpOptionID.NotFound", fmt.Sprintf("The DHCP options set %q does not exist", id), http.StatusBadRequest)
				return
			}
			results = append(results, opts)
		}
	} else {
		for _, opts := range ec2DhcpOptions.List() {
			if !ec2DhcpOptionsMatchesFilters(opts, filters) {
				continue
			}
			results = append(results, opts)
		}
	}
	nextToken := ""
	if len(ids) == 0 {
		sort.Slice(results, func(i, j int) bool { return results[i].DhcpOptionsId < results[j].DhcpOptionsId })
		results, nextToken = awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var items strings.Builder
	for _, opts := range results {
		items.WriteString("<item>")
		items.WriteString(ec2DhcpOptionsFieldsXML(opts))
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeDhcpOptionsResponse %s><requestId>%s</requestId><dhcpOptionsSet>%s</dhcpOptionsSet>%s</DescribeDhcpOptionsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func ec2DhcpOptionsMatchesFilters(opts EC2DhcpOptions, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "dhcp-options-id":
			if !ec2StrInValues(opts.DhcpOptionsId, vals) {
				return false
			}
		case "owner-id":
			if !ec2StrInValues(opts.OwnerId, vals) {
				return false
			}
		case "key":
			found := false
			for _, c := range opts.DhcpConfigurations {
				if ec2StrInValues(c.Key, vals) {
					found = true
				}
			}
			if !found {
				return false
			}
		case "value":
			found := false
			for _, c := range opts.DhcpConfigurations {
				for _, v := range c.Values {
					if ec2StrInValues(v, vals) {
						found = true
					}
				}
			}
			if !found {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, opts.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleAssociateDhcpOptions(w http.ResponseWriter, r *http.Request) {
	optsID := r.FormValue("DhcpOptionsId")
	vpcID := r.FormValue("VpcId")
	if optsID == "" || vpcID == "" {
		ec2ErrorXML(w, "MissingParameter", "DhcpOptionsId and VpcId are required", http.StatusBadRequest)
		return
	}
	vpc, ok := ec2Vpcs.Get(vpcID)
	if !ok {
		ec2ErrorXML(w, "InvalidVpcID.NotFound", fmt.Sprintf("The vpc ID %q does not exist", vpcID), http.StatusBadRequest)
		return
	}
	// "default" detaches the custom set and reverts the VPC to the default
	// options set, matching real AssociateDhcpOptions semantics.
	if optsID != "default" {
		if _, ok := ec2DhcpOptions.Get(optsID); !ok {
			ec2ErrorXML(w, "InvalidDhcpOptionID.NotFound", fmt.Sprintf("The DHCP options set %q does not exist", optsID), http.StatusBadRequest)
			return
		}
	}
	vpc.DhcpOptionsId = optsID
	ec2Vpcs.Put(vpcID, vpc)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<AssociateDhcpOptionsResponse %s><requestId>%s</requestId><return>true</return></AssociateDhcpOptionsResponse>`, ec2Xmlns(), generateUUID())
}

func handleDeleteDhcpOptions(w http.ResponseWriter, r *http.Request) {
	optsID := r.FormValue("DhcpOptionsId")
	if optsID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter DhcpOptionsId", http.StatusBadRequest)
		return
	}
	if _, ok := ec2DhcpOptions.Get(optsID); !ok {
		ec2ErrorXML(w, "InvalidDhcpOptionID.NotFound", fmt.Sprintf("The DHCP options set %q does not exist", optsID), http.StatusBadRequest)
		return
	}
	// Real EC2 rejects deleting an options set still associated with a VPC.
	for _, vpc := range ec2Vpcs.List() {
		if vpc.DhcpOptionsId == optsID {
			ec2ErrorXML(w, "DependencyViolation", fmt.Sprintf("The dhcpOptions %q has dependencies and cannot be deleted.", optsID), http.StatusBadRequest)
			return
		}
	}
	ec2DhcpOptions.Delete(optsID)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteDhcpOptionsResponse %s><requestId>%s</requestId><return>true</return></DeleteDhcpOptionsResponse>`, ec2Xmlns(), generateUUID())
}
