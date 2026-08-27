package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// EC2InstanceConnectEndpoint models an EC2 Instance Connect Endpoint (eice-…) —
// the VPC-resident endpoint that brokers SSH/RDP to instances without a public
// IP. Real EC2 provisions an elastic network interface in the endpoint's subnet
// and transitions the endpoint through create-in-progress → create-complete.
type EC2InstanceConnectEndpoint struct {
	InstanceConnectEndpointId string
	SubnetId                  string
	VpcId                     string
	AvailabilityZone          string
	AvailabilityZoneId        string
	State                     string
	PreserveClientIp          bool
	IpAddressType             string
	SecurityGroupIds          []string
	NetworkInterfaceIds       []string
	DnsName                   string
	FipsDnsName               string
	OwnerId                   string
	CreatedAt                 string
	Tags                      []EC2Tag
}

// EC2InstanceStatusReport records a ReportInstanceStatus submission. Real EC2
// keeps the report against the instance to feed scheduled-event and health
// dashboards; the sim persists it so the op is a faithful CRUD write, not a
// no-op.
type EC2InstanceStatusReport struct {
	InstanceId  string
	Status      string
	ReasonCodes []string
	StartTime   string
	EndTime     string
	Description string
}

// EC2AccountInstanceSettings holds the account-level (per-Region) instance
// management knobs that real EC2 stores at the account level rather than on any
// single instance: serial-console access, IMDS defaults, and the set of tag
// keys registered for instance-event notifications. Keyed in its store by the
// AWS account ID so a SOCKERLESS_AWS_ACCOUNT_ID override scopes correctly.
type EC2AccountInstanceSettings struct {
	AccountId                string
	SerialConsoleAccess      bool
	EventNotificationTagKeys []string
	IncludeAllTagsOfInstance bool
	// IMDS account-level defaults. Empty MetadataConfigured means the account
	// has no defaults set (real EC2 returns an empty accountLevel element).
	MetadataConfigured   bool
	MetadataHttpTokens   string
	MetadataHopLimit     int
	MetadataHttpEndpoint string
	MetadataInstanceTags string
}

var (
	ec2InstanceConnectEndpoints sim.Store[EC2InstanceConnectEndpoint]
	ec2InstanceStatusReports    sim.Store[EC2InstanceStatusReport]
	ec2AccountInstanceSettings  sim.Store[EC2AccountInstanceSettings]
)

// registerEC2InstanceMgmt registers the EC2 instance-management family:
// Instance Connect endpoints, instance-event-notification attributes,
// serial-console access, IMDS account defaults, monitoring, and the
// reboot/report/reset/classic-link instance operations.
func registerEC2InstanceMgmt(r *sim.AWSQueryRouter, srv *sim.Server) {
	ec2InstanceConnectEndpoints = sim.MakeStore[EC2InstanceConnectEndpoint](srv.DB(), "ec2_instance_connect_endpoints")
	ec2InstanceStatusReports = sim.MakeStore[EC2InstanceStatusReport](srv.DB(), "ec2_instance_status_reports")
	ec2AccountInstanceSettings = sim.MakeStore[EC2AccountInstanceSettings](srv.DB(), "ec2_account_instance_settings")

	// EC2 Instance Connect endpoints
	r.Register("CreateInstanceConnectEndpoint", handleCreateInstanceConnectEndpoint)
	r.Register("DescribeInstanceConnectEndpoints", handleDescribeInstanceConnectEndpoints)
	r.Register("ModifyInstanceConnectEndpoint", handleModifyInstanceConnectEndpoint)
	r.Register("DeleteInstanceConnectEndpoint", handleDeleteInstanceConnectEndpoint)

	// Instance-event-notification attributes (account-level registered tag keys)
	r.Register("RegisterInstanceEventNotificationAttributes", handleRegisterInstanceEventNotificationAttributes)
	r.Register("DeregisterInstanceEventNotificationAttributes", handleDeregisterInstanceEventNotificationAttributes)
	r.Register("DescribeInstanceEventNotificationAttributes", handleDescribeInstanceEventNotificationAttributes)

	// Serial-console access (account-level flag)
	r.Register("EnableSerialConsoleAccess", handleEnableSerialConsoleAccess)
	r.Register("DisableSerialConsoleAccess", handleDisableSerialConsoleAccess)
	r.Register("GetSerialConsoleAccessStatus", handleGetSerialConsoleAccessStatus)

	// IMDS account-level defaults
	r.Register("GetInstanceMetadataDefaults", handleGetInstanceMetadataDefaults)
	r.Register("ModifyInstanceMetadataDefaults", handleModifyInstanceMetadataDefaults)

	// Monitoring
	r.Register("MonitorInstances", handleMonitorInstances)
	r.Register("UnmonitorInstances", handleUnmonitorInstances)

	// Instance lifecycle / status
	r.Register("RebootInstances", handleRebootInstances)
	r.Register("ReportInstanceStatus", handleReportInstanceStatus)
	r.Register("ResetInstanceAttribute", handleResetInstanceAttribute)

	// EC2-Classic (honest-empty: the sim runs only in a VPC)
	r.Register("DescribeClassicLinkInstances", handleDescribeClassicLinkInstances)
}

// ec2AccountSettings returns the account's instance-management settings,
// seeding a zero-valued record (serial console disabled, no IMDS defaults, no
// registered tag keys) the first time it's read — matching a fresh AWS account.
func ec2AccountSettings() EC2AccountInstanceSettings {
	id := awsAccountID()
	if s, ok := ec2AccountInstanceSettings.Get(id); ok {
		return s
	}
	return EC2AccountInstanceSettings{AccountId: id}
}

func ec2PutAccountSettings(s EC2AccountInstanceSettings) {
	s.AccountId = awsAccountID()
	ec2AccountInstanceSettings.Put(s.AccountId, s)
}

func handleCreateInstanceConnectEndpoint(w http.ResponseWriter, r *http.Request) {
	subnetID := r.FormValue("SubnetId")
	if subnetID == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter SubnetId", http.StatusBadRequest)
		return
	}
	subnet, ok := ec2Subnets.Get(subnetID)
	if !ok {
		ec2ErrorXML(w, "InvalidSubnetID.NotFound", fmt.Sprintf("The subnet ID '%s' does not exist", subnetID), http.StatusBadRequest)
		return
	}
	ipType := r.FormValue("IpAddressType")
	if ipType == "" {
		ipType = "ipv4"
	}
	id := ec2ID("eice")
	// Real EC2 auto-creates an elastic network interface for the endpoint in
	// the target subnet. Provision one from the same store DescribeNetworkInterfaces
	// reads, so the endpoint's networkInterfaceIdSet references a real ENI.
	eniID := ec2ID("eni")
	eniIP, err := AllocateSubnetIP(subnetID)
	if err != nil {
		ec2ErrorXML(w, "InsufficientFreeAddressesInSubnet", fmt.Sprintf("The subnet '%s' does not have enough free addresses: %v", subnetID, err), http.StatusBadRequest)
		return
	}
	ec2NetworkInterfaces.Put(eniID, EC2NetworkInterface{
		NetworkInterfaceId: eniID,
		SubnetId:           subnetID,
		VpcId:              subnet.VpcId,
		PrivateIpAddress:   eniIP,
		Status:             "in-use",
		Description:        "EC2 Instance Connect Endpoint " + id,
		InterfaceType:      "ec2_instance_connect_endpoint",
		SecurityGroupIds:   ec2ParamList(r, "SecurityGroupId"),
		OwnerId:            ec2Owner(),
	})
	eice := EC2InstanceConnectEndpoint{
		InstanceConnectEndpointId: id,
		SubnetId:                  subnetID,
		VpcId:                     subnet.VpcId,
		AvailabilityZone:          subnet.AvailabilityZone,
		AvailabilityZoneId:        subnet.AvailabilityZoneId,
		State:                     "create-complete",
		PreserveClientIp:          r.FormValue("PreserveClientIp") == "true",
		IpAddressType:             ipType,
		SecurityGroupIds:          ec2ParamList(r, "SecurityGroupId"),
		NetworkInterfaceIds:       []string{eniID},
		DnsName:                   id + "." + subnet.VpcId + ".ec2-instance-connect-endpoint." + awsRegion() + ".amazonaws.com",
		FipsDnsName:               id + "." + subnet.VpcId + ".fips.ec2-instance-connect-endpoint." + awsRegion() + ".amazonaws.com",
		OwnerId:                   ec2Owner(),
		CreatedAt:                 ec2NowRFC3339Milli(),
		Tags:                      parseTags(r),
	}
	ec2InstanceConnectEndpoints.Put(id, eice)
	clientToken := ""
	if ct := r.FormValue("ClientToken"); ct != "" {
		clientToken = fmt.Sprintf("<clientToken>%s</clientToken>", xmlEscape(ct))
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateInstanceConnectEndpointResponse %s><requestId>%s</requestId><instanceConnectEndpoint>%s</instanceConnectEndpoint>%s</CreateInstanceConnectEndpointResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceConnectEndpointFieldsXML(eice), clientToken)
}

// ec2InstanceConnectEndpointArn renders the endpoint ARN
// (arn:aws:ec2:region:account:instance-connect-endpoint/eice-…).
func ec2InstanceConnectEndpointArn(eice EC2InstanceConnectEndpoint) string {
	return fmt.Sprintf("arn:aws:ec2:%s:%s:instance-connect-endpoint/%s", awsRegion(), eice.OwnerId, eice.InstanceConnectEndpointId)
}

func ec2InstanceConnectEndpointFieldsXML(eice EC2InstanceConnectEndpoint) string {
	var nis strings.Builder
	nis.WriteString("<networkInterfaceIdSet>")
	for _, ni := range eice.NetworkInterfaceIds {
		fmt.Fprintf(&nis, "<item>%s</item>", ni)
	}
	nis.WriteString("</networkInterfaceIdSet>")
	var sgs strings.Builder
	sgs.WriteString("<securityGroupIdSet>")
	for _, sg := range eice.SecurityGroupIds {
		fmt.Fprintf(&sgs, "<item>%s</item>", sg)
	}
	sgs.WriteString("</securityGroupIdSet>")
	return fmt.Sprintf(`<ownerId>%s</ownerId><instanceConnectEndpointId>%s</instanceConnectEndpointId>`+
		`<instanceConnectEndpointArn>%s</instanceConnectEndpointArn><state>%s</state>`+
		`<dnsName>%s</dnsName><fipsDnsName>%s</fipsDnsName>%s<vpcId>%s</vpcId>`+
		`<availabilityZone>%s</availabilityZone><availabilityZoneId>%s</availabilityZoneId>`+
		`<createdAt>%s</createdAt><subnetId>%s</subnetId><preserveClientIp>%t</preserveClientIp>`+
		`%s<ipAddressType>%s</ipAddressType>%s`,
		eice.OwnerId, eice.InstanceConnectEndpointId,
		ec2InstanceConnectEndpointArn(eice), eice.State,
		eice.DnsName, eice.FipsDnsName, nis.String(), eice.VpcId,
		eice.AvailabilityZone, eice.AvailabilityZoneId,
		eice.CreatedAt, eice.SubnetId, eice.PreserveClientIp,
		sgs.String(), eice.IpAddressType, writeTagSetXML(eice.Tags))
}

func ec2InstanceConnectEndpointMatchesFilters(eice EC2InstanceConnectEndpoint, filters map[string][]string) bool {
	for name, vals := range filters {
		switch name {
		case "instance-connect-endpoint-id":
			if !ec2StrInValues(eice.InstanceConnectEndpointId, vals) {
				return false
			}
		case "subnet-id":
			if !ec2StrInValues(eice.SubnetId, vals) {
				return false
			}
		case "vpc-id":
			if !ec2StrInValues(eice.VpcId, vals) {
				return false
			}
		case "state":
			if !ec2StrInValues(eice.State, vals) {
				return false
			}
		default:
			if handled, match := ec2TagFilterMatch(name, vals, eice.Tags); handled && !match {
				return false
			}
		}
	}
	return true
}

func handleDescribeInstanceConnectEndpoints(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceConnectEndpointId")
	for _, id := range ids {
		if _, ok := ec2InstanceConnectEndpoints.Get(id); !ok {
			ec2ErrorXML(w, "InvalidInstanceConnectEndpointId.NotFound", fmt.Sprintf("The EC2 Instance Connect Endpoint '%s' does not exist", id), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	results := make([]EC2InstanceConnectEndpoint, 0)
	for _, eice := range ec2InstanceConnectEndpoints.List() {
		if len(ids) > 0 && !ec2StrInValues(eice.InstanceConnectEndpointId, ids) {
			continue
		}
		if !ec2InstanceConnectEndpointMatchesFilters(eice, filters) {
			continue
		}
		results = append(results, eice)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].InstanceConnectEndpointId < results[j].InstanceConnectEndpointId
	})
	nextToken := ""
	if len(ids) == 0 {
		results, nextToken = awsPageExplicit(results, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0))
	}
	var items strings.Builder
	for _, eice := range results {
		items.WriteString("<item>")
		items.WriteString(ec2InstanceConnectEndpointFieldsXML(eice))
		items.WriteString("</item>")
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceConnectEndpointsResponse %s><requestId>%s</requestId><instanceConnectEndpointSet>%s</instanceConnectEndpointSet>%s</DescribeInstanceConnectEndpointsResponse>`,
		ec2Xmlns(), generateUUID(), items.String(), nextTokenXML)
}

func handleModifyInstanceConnectEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceConnectEndpointId")
	if _, ok := ec2InstanceConnectEndpoints.Get(id); !ok {
		ec2ErrorXML(w, "InvalidInstanceConnectEndpointId.NotFound", fmt.Sprintf("The EC2 Instance Connect Endpoint '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	ec2InstanceConnectEndpoints.Update(id, func(eice *EC2InstanceConnectEndpoint) {
		if sgs := ec2ParamList(r, "SecurityGroupId"); len(sgs) > 0 {
			eice.SecurityGroupIds = sgs
		}
		if v := r.FormValue("IpAddressType"); v != "" {
			eice.IpAddressType = v
		}
	})
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceConnectEndpointResponse %s><requestId>%s</requestId><return>true</return></ModifyInstanceConnectEndpointResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDeleteInstanceConnectEndpoint(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceConnectEndpointId")
	eice, ok := ec2InstanceConnectEndpoints.Get(id)
	if !ok {
		ec2ErrorXML(w, "InvalidInstanceConnectEndpointId.NotFound", fmt.Sprintf("The EC2 Instance Connect Endpoint '%s' does not exist", id), http.StatusBadRequest)
		return
	}
	// Real EC2 tears down the auto-created ENI and moves the endpoint to
	// delete-complete. Remove the backing ENIs and transition state.
	for _, eniID := range eice.NetworkInterfaceIds {
		ec2NetworkInterfaces.Delete(eniID)
	}
	eice.State = "delete-complete"
	ec2InstanceConnectEndpoints.Delete(id)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteInstanceConnectEndpointResponse %s><requestId>%s</requestId><instanceConnectEndpoint>%s</instanceConnectEndpoint></DeleteInstanceConnectEndpointResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceConnectEndpointFieldsXML(eice))
}

// ec2InstanceTagAttributeXML renders the account's registered tag keys as the
// instanceTagAttribute element shared by the register/deregister/describe ops.
func ec2InstanceTagAttributeXML(s EC2AccountInstanceSettings) string {
	var keys strings.Builder
	keys.WriteString("<instanceTagKeySet>")
	for _, k := range s.EventNotificationTagKeys {
		fmt.Fprintf(&keys, "<item>%s</item>", xmlEscape(k))
	}
	keys.WriteString("</instanceTagKeySet>")
	return fmt.Sprintf("<instanceTagAttribute>%s<includeAllTagsOfInstance>%t</includeAllTagsOfInstance></instanceTagAttribute>",
		keys.String(), s.IncludeAllTagsOfInstance)
}

func handleRegisterInstanceEventNotificationAttributes(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	if v := r.FormValue("InstanceTagAttribute.IncludeAllTagsOfInstance"); v != "" {
		s.IncludeAllTagsOfInstance = v == "true"
		if s.IncludeAllTagsOfInstance {
			// Registering all tags clears any explicit key list, matching AWS.
			s.EventNotificationTagKeys = nil
		}
	}
	for _, k := range ec2ParamList(r, "InstanceTagAttribute.InstanceTagKey") {
		if !ec2StrInValues(k, s.EventNotificationTagKeys) {
			s.EventNotificationTagKeys = append(s.EventNotificationTagKeys, k)
		}
	}
	sort.Strings(s.EventNotificationTagKeys)
	ec2PutAccountSettings(s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RegisterInstanceEventNotificationAttributesResponse %s><requestId>%s</requestId>%s</RegisterInstanceEventNotificationAttributesResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceTagAttributeXML(s))
}

func handleDeregisterInstanceEventNotificationAttributes(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	if v := r.FormValue("InstanceTagAttribute.IncludeAllTagsOfInstance"); v == "true" {
		s.IncludeAllTagsOfInstance = false
	}
	remove := ec2ParamList(r, "InstanceTagAttribute.InstanceTagKey")
	if len(remove) > 0 {
		var kept []string
		for _, k := range s.EventNotificationTagKeys {
			if !ec2StrInValues(k, remove) {
				kept = append(kept, k)
			}
		}
		s.EventNotificationTagKeys = kept
	}
	ec2PutAccountSettings(s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeregisterInstanceEventNotificationAttributesResponse %s><requestId>%s</requestId>%s</DeregisterInstanceEventNotificationAttributesResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceTagAttributeXML(s))
}

func handleDescribeInstanceEventNotificationAttributes(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeInstanceEventNotificationAttributesResponse %s><requestId>%s</requestId>%s</DescribeInstanceEventNotificationAttributesResponse>`,
		ec2Xmlns(), generateUUID(), ec2InstanceTagAttributeXML(s))
}

func handleEnableSerialConsoleAccess(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	s.SerialConsoleAccess = true
	ec2PutAccountSettings(s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<EnableSerialConsoleAccessResponse %s><requestId>%s</requestId><serialConsoleAccessEnabled>true</serialConsoleAccessEnabled></EnableSerialConsoleAccessResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleDisableSerialConsoleAccess(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	s.SerialConsoleAccess = false
	ec2PutAccountSettings(s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DisableSerialConsoleAccessResponse %s><requestId>%s</requestId><serialConsoleAccessEnabled>false</serialConsoleAccessEnabled></DisableSerialConsoleAccessResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleGetSerialConsoleAccessStatus(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<GetSerialConsoleAccessStatusResponse %s><requestId>%s</requestId><serialConsoleAccessEnabled>%t</serialConsoleAccessEnabled><managedBy>account</managedBy></GetSerialConsoleAccessStatusResponse>`,
		ec2Xmlns(), generateUUID(), s.SerialConsoleAccess)
}

func handleGetInstanceMetadataDefaults(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	w.Header().Set("Content-Type", "text/xml")
	body := "<accountLevel/>"
	if s.MetadataConfigured {
		var fields strings.Builder
		if s.MetadataHttpTokens != "" {
			fmt.Fprintf(&fields, "<httpTokens>%s</httpTokens>", s.MetadataHttpTokens)
		}
		if s.MetadataHopLimit > 0 {
			fmt.Fprintf(&fields, "<httpPutResponseHopLimit>%d</httpPutResponseHopLimit>", s.MetadataHopLimit)
		}
		if s.MetadataHttpEndpoint != "" {
			fmt.Fprintf(&fields, "<httpEndpoint>%s</httpEndpoint>", s.MetadataHttpEndpoint)
		}
		if s.MetadataInstanceTags != "" {
			fmt.Fprintf(&fields, "<instanceMetadataTags>%s</instanceMetadataTags>", s.MetadataInstanceTags)
		}
		fields.WriteString("<managedBy>account</managedBy>")
		body = "<accountLevel>" + fields.String() + "</accountLevel>"
	}
	fmt.Fprintf(w, `<GetInstanceMetadataDefaultsResponse %s><requestId>%s</requestId>%s</GetInstanceMetadataDefaultsResponse>`,
		ec2Xmlns(), generateUUID(), body)
}

func handleModifyInstanceMetadataDefaults(w http.ResponseWriter, r *http.Request) {
	s := ec2AccountSettings()
	if v := r.FormValue("HttpTokens"); v != "" {
		s.MetadataHttpTokens = v
		s.MetadataConfigured = true
	}
	if v := r.FormValue("HttpPutResponseHopLimit"); v != "" {
		s.MetadataHopLimit = ec2AtoiOr(v, s.MetadataHopLimit)
		s.MetadataConfigured = true
	}
	if v := r.FormValue("HttpEndpoint"); v != "" {
		s.MetadataHttpEndpoint = v
		s.MetadataConfigured = true
	}
	if v := r.FormValue("InstanceMetadataTags"); v != "" {
		s.MetadataInstanceTags = v
		s.MetadataConfigured = true
	}
	ec2PutAccountSettings(s)
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ModifyInstanceMetadataDefaultsResponse %s><requestId>%s</requestId><return>true</return></ModifyInstanceMetadataDefaultsResponse>`,
		ec2Xmlns(), generateUUID())
}

// ec2MonitoringResponse flips detailed monitoring on each named instance and
// renders the shared MonitorInstances/UnmonitorInstances instancesSet body.
func ec2MonitoringResponse(w http.ResponseWriter, r *http.Request, root string, enable bool) {
	ids := ec2ParamList(r, "InstanceId")
	for _, id := range ids {
		if _, ok := ec2Instances.Get(id); !ok {
			sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
			return
		}
	}
	var items strings.Builder
	for _, id := range ids {
		ec2Instances.Update(id, func(inst *EC2Instance) { inst.Monitoring = enable })
		state := "disabled"
		if enable {
			// MonitorInstances reports "pending" until detailed monitoring is
			// active; the sim applies it immediately, so report "enabled".
			state = "enabled"
		}
		fmt.Fprintf(&items, "<item><instanceId>%s</instanceId><monitoring><state>%s</state></monitoring></item>", id, state)
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<%s %s><requestId>%s</requestId><instancesSet>%s</instancesSet></%s>`,
		root, ec2Xmlns(), generateUUID(), items.String(), root)
}

func handleMonitorInstances(w http.ResponseWriter, r *http.Request) {
	ec2MonitoringResponse(w, r, "MonitorInstancesResponse", true)
}

func handleUnmonitorInstances(w http.ResponseWriter, r *http.Request) {
	ec2MonitoringResponse(w, r, "UnmonitorInstancesResponse", false)
}

func handleRebootInstances(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceId")
	for _, id := range ids {
		if _, ok := ec2Instances.Get(id); !ok {
			sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
			return
		}
	}
	// Real RebootInstances is an asynchronous in-place restart: the instance
	// stays "running" throughout. The sim keeps the instance running and
	// returns success, faithful to the API contract.
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<RebootInstancesResponse %s><requestId>%s</requestId><return>true</return></RebootInstancesResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleReportInstanceStatus(w http.ResponseWriter, r *http.Request) {
	ids := ec2ParamList(r, "InstanceId")
	if len(ids) == 0 {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Instances", http.StatusBadRequest)
		return
	}
	status := r.FormValue("Status")
	if status == "" {
		ec2ErrorXML(w, "MissingParameter", "The request must contain the parameter Status", http.StatusBadRequest)
		return
	}
	reasons := ec2ParamList(r, "ReasonCode")
	for _, id := range ids {
		if _, ok := ec2Instances.Get(id); !ok {
			sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
			return
		}
		ec2InstanceStatusReports.Put(id, EC2InstanceStatusReport{
			InstanceId:  id,
			Status:      status,
			ReasonCodes: reasons,
			StartTime:   r.FormValue("StartTime"),
			EndTime:     r.FormValue("EndTime"),
			Description: r.FormValue("Description"),
		})
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ReportInstanceStatusResponse %s><requestId>%s</requestId><return>true</return></ReportInstanceStatusResponse>`,
		ec2Xmlns(), generateUUID())
}

func handleResetInstanceAttribute(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("InstanceId")
	attr := r.FormValue("Attribute")
	if _, ok := ec2Instances.Get(id); !ok {
		sim.AWSErrorf(w, "InvalidInstanceID.NotFound", http.StatusBadRequest, "The instance ID %q does not exist", id)
		return
	}
	switch attr {
	case "sourceDestCheck":
		// The default for sourceDestCheck is true; resetting restores it.
		ec2Instances.Update(id, func(inst *EC2Instance) { inst.SourceDestCheck = true })
	case "kernel", "ramdisk":
		// The sim stores no kernel/ramdisk override; the reset is a no-op
		// against the (already-default) value, matching AWS's allowed set.
	default:
		ec2ErrorXML(w, "InvalidParameterValue", fmt.Sprintf("You can only reset the following attributes: kernel | ramdisk | sourceDestCheck. The attribute '%s' is not resettable.", attr), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<ResetInstanceAttributeResponse %s><requestId>%s</requestId><return>true</return></ResetInstanceAttributeResponse>`,
		ec2Xmlns(), generateUUID())
}

// handleDescribeClassicLinkInstances returns an honest-empty set: the sim runs
// only in a VPC (EC2-Classic was retired AWS-wide), so no instances are ever
// ClassicLink-attached.
func handleDescribeClassicLinkInstances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeClassicLinkInstancesResponse %s><requestId>%s</requestId><instancesSet/></DescribeClassicLinkInstancesResponse>`,
		ec2Xmlns(), generateUUID())
}
