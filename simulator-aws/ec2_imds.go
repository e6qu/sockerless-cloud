package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The metadata service answers only instances: a request belongs to the
// instance holding its source address, and gets only that instance's data.

type imdsToken struct {
	instanceID string
	expires    time.Time
}

var imdsTokens sync.Map

// Six hours.
const imdsMaxTokenTTL = 21600

// Read the record fresh: metadata options and tags change while the instance
// runs.
func imdsInstanceFor(r *http.Request) (EC2Instance, bool) {
	indexed, ok := imdsInstancesByIP.ForRequest(r)
	if !ok {
		return EC2Instance{}, false
	}
	inst, ok := ec2Instances.Get(indexed.InstanceId)
	return inst, ok
}

// Also the answer to an address no instance holds; the link-local service is
// unreachable from there.
func imdsNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="iso-8859-1"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN"
		 "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">
 <head>
  <title>404 - Not Found</title>
 </head>
 <body>
  <h1>404 - Not Found</h1>
 </body>
</html>
`)
}

func imdsUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, `<?xml version="1.0" encoding="iso-8859-1"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN"
		 "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">
 <head>
  <title>401 - Unauthorized</title>
 </head>
 <body>
  <h1>401 - Unauthorized</h1>
 </body>
</html>
`)
}

// Admit a read from an instance whose endpoint is enabled, with a live token
// issued to it, or with none when its options leave IMDSv1 on.
func imdsRequest(w http.ResponseWriter, r *http.Request) (EC2Instance, bool) {
	inst, ok := imdsInstanceFor(r)
	if !ok || strings.EqualFold(inst.MetadataHttpEndpoint, "disabled") {
		imdsNotFound(w)
		return EC2Instance{}, false
	}
	token := r.Header.Get("X-aws-ec2-metadata-token")
	if token == "" {
		if strings.EqualFold(inst.MetadataHttpTokens, "required") {
			imdsUnauthorized(w)
			return EC2Instance{}, false
		}
		return inst, true
	}
	held, ok := imdsTokens.Load(token)
	issued, _ := held.(imdsToken)
	if !ok || issued.instanceID != inst.InstanceId || time.Now().After(issued.expires) {
		imdsUnauthorized(w)
		return EC2Instance{}, false
	}
	return inst, true
}

func imdsText(w http.ResponseWriter, s string) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(s))
}

func imdsJSON(w http.ResponseWriter, v any) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write(body)
}

func ec2InstanceAvailabilityZone(inst EC2Instance) string {
	if subnet, ok := ec2Subnets.Get(inst.SubnetId); ok && subnet.AvailabilityZone != "" {
		return subnet.AvailabilityZone
	}
	return awsAvailabilityZone()
}

func ec2InstanceProfileRole(inst EC2Instance) (IAMInstanceProfile, string, bool) {
	name := inst.IamInstanceProfileName
	if name == "" && inst.IamInstanceProfileArn != "" {
		name = inst.IamInstanceProfileArn[strings.LastIndex(inst.IamInstanceProfileArn, "/")+1:]
	}
	if name == "" {
		return IAMInstanceProfile{}, "", false
	}
	profile, ok := iamInstanceProfiles.Get(name)
	if !ok || profile.RoleName == "" {
		return profile, "", false
	}
	return profile, profile.RoleName, true
}

func ec2InstanceTagsEnabled(inst EC2Instance) bool {
	return strings.EqualFold(inst.MetadataInstanceTags, "enabled")
}

func imdsListing(inst EC2Instance) string {
	entries := []string{"ami-id", "ami-launch-index", "block-device-mapping/", "hostname", "instance-action",
		"instance-id", "instance-life-cycle", "instance-type", "local-hostname", "local-ipv4", "mac", "network/",
		"placement/", "reservation-id", "security-groups", "services/"}
	if ec2PublicDNSName(inst) != "" {
		entries = append(entries, "public-hostname")
	}
	if inst.PublicIpAddress != "" {
		entries = append(entries, "public-ipv4")
	}
	if _, _, ok := ec2InstanceProfileRole(inst); ok {
		entries = append(entries, "iam/")
	}
	if inst.KeyName != "" {
		entries = append(entries, "public-keys/")
	}
	if ec2InstanceTagsEnabled(inst) {
		entries = append(entries, "tags/")
	}
	sort.Strings(entries)
	return strings.Join(entries, "\n")
}

// In device order.
func imdsInterfaces(inst EC2Instance) []EC2NetworkInterface {
	var out []EC2NetworkInterface
	for _, eni := range ec2NetworkInterfaces.List() {
		if eni.InstanceId == inst.InstanceId {
			out = append(out, eni)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeviceIndex < out[j].DeviceIndex })
	return out
}

func imdsInterfaceByMAC(inst EC2Instance, mac string) (EC2NetworkInterface, bool) {
	for _, eni := range imdsInterfaces(inst) {
		if ec2InterfaceMAC(eni.NetworkInterfaceId) == strings.ToLower(mac) {
			return eni, true
		}
	}
	return EC2NetworkInterface{}, false
}

func imdsGroupNames(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if sg, ok := ec2SecurityGroups.Get(id); ok {
			names = append(names, sg.GroupName)
		}
	}
	return names
}

// The primary interface of an instance with a public address carries that
// address too.
func imdsInterfaceKeys(inst EC2Instance, eni EC2NetworkInterface) map[string]string {
	keys := map[string]string{
		"device-number":      strconv.Itoa(eni.DeviceIndex),
		"interface-id":       eni.NetworkInterfaceId,
		"local-hostname":     ec2PrivateDNSName(eni.PrivateIpAddress),
		"local-ipv4s":        strings.Join(append([]string{eni.PrivateIpAddress}, eni.SecondaryPrivateIps...), "\n"),
		"mac":                ec2InterfaceMAC(eni.NetworkInterfaceId),
		"network-card-index": "0",
		"owner-id":           eni.OwnerId,
		"security-group-ids": strings.Join(eni.SecurityGroupIds, "\n"),
		"security-groups":    strings.Join(imdsGroupNames(eni.SecurityGroupIds), "\n"),
		"subnet-id":          eni.SubnetId,
		"vpc-id":             eni.VpcId,
	}
	if subnet, ok := ec2Subnets.Get(eni.SubnetId); ok {
		keys["subnet-ipv4-cidr-block"] = subnet.CidrBlock
	}
	if vpc, ok := ec2Vpcs.Get(eni.VpcId); ok {
		keys["vpc-ipv4-cidr-block"] = vpc.CidrBlock
		keys["vpc-ipv4-cidr-blocks"] = vpc.CidrBlock
	}
	if associations := imdsPublicAddresses(inst, eni); len(associations) > 0 {
		public := sortedKeys(associations)
		keys["public-ipv4s"] = strings.Join(public, "\n")
		keys["ipv4-associations/"] = strings.Join(public, "\n")
		if name := ec2PublicDNSName(inst); name != "" && eni.NetworkInterfaceId == inst.NetworkInterfaceId {
			keys["public-hostname"] = name
		}
	}
	return keys
}

// Each public IPv4 maps to its private address: the launch-time public address
// of the primary interface, and the Elastic IPs of the interface or instance.
func imdsPublicAddresses(inst EC2Instance, eni EC2NetworkInterface) map[string]string {
	associations := map[string]string{}
	primary := eni.NetworkInterfaceId == inst.NetworkInterfaceId
	if primary && inst.PublicIpAddress != "" {
		associations[inst.PublicIpAddress] = eni.PrivateIpAddress
	}
	for _, eip := range ec2ElasticIPs.List() {
		if eip.AssociationId == "" {
			continue
		}
		if eip.NetworkInterfaceId != eni.NetworkInterfaceId && (!primary || eip.NetworkInterfaceId != "" || eip.InstanceId != inst.InstanceId) {
			continue
		}
		private := eip.PrivateIpAddress
		if private == "" {
			private = eni.PrivateIpAddress
		}
		associations[eip.PublicIp] = private
	}
	return associations
}

// Disabled unless the instance enabled it.
func ec2MetadataHttpProtocolIpv6(inst EC2Instance) string {
	if inst.MetadataHttpProtocolIpv6 == "" {
		return "disabled"
	}
	return inst.MetadataHttpProtocolIpv6
}

// The AMI's root device appears as ami and root, each further Amazon EBS
// volume as ebsN in attachment order.
func imdsBlockDevices(inst EC2Instance) map[string]string {
	devices := map[string]string{"ami": inst.RootDeviceName, "root": inst.RootDeviceName}
	var attached []EC2VolumeAttachment
	for _, vol := range ec2Volumes.List() {
		for _, att := range vol.Attachments {
			if att.InstanceId == inst.InstanceId && att.Device != inst.RootDeviceName && att.State == "attached" {
				attached = append(attached, att)
			}
		}
	}
	sort.Slice(attached, func(i, j int) bool { return attached[i].AttachTime < attached[j].AttachTime })
	for i, att := range attached {
		devices["ebs"+strconv.Itoa(i+1)] = strings.TrimPrefix(att.Device, "/dev/")
	}
	return devices
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func registerEC2IMDS(srv *sim.Server) {
	// Issue a token to the asking instance for its TTL; refuse a request without
	// a TTL or one a proxy forwarded.
	srv.HandleFunc("PUT /latest/api/token", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsInstanceFor(r)
		if !ok || strings.EqualFold(inst.MetadataHttpEndpoint, "disabled") {
			imdsNotFound(w)
			return
		}
		if r.Header.Get("X-Forwarded-For") != "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		ttl, err := strconv.Atoi(r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"))
		if err != nil || ttl < 1 || ttl > imdsMaxTokenTTL {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		now := time.Now()
		imdsTokens.Range(func(key, held any) bool {
			if issued, ok := held.(imdsToken); !ok || now.After(issued.expires) {
				imdsTokens.Delete(key)
			}
			return true
		})
		buf := make([]byte, 28)
		_, _ = rand.Read(buf)
		token := "AQ" + hex.EncodeToString(buf)
		imdsTokens.Store(token, imdsToken{instanceID: inst.InstanceId, expires: now.Add(time.Duration(ttl) * time.Second)})
		w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", strconv.Itoa(ttl))
		imdsText(w, token)
	})

	imdsLeaf(srv, "/latest/meta-data/{$}", imdsAlways(imdsListing))
	imdsLeaf(srv, "/latest/meta-data/instance-id", imdsAlways(func(inst EC2Instance) string { return inst.InstanceId }))
	imdsLeaf(srv, "/latest/meta-data/instance-type", imdsAlways(func(inst EC2Instance) string { return inst.InstanceType }))
	imdsLeaf(srv, "/latest/meta-data/ami-id", imdsAlways(func(inst EC2Instance) string { return inst.ImageId }))
	imdsLeaf(srv, "/latest/meta-data/local-ipv4", imdsAlways(func(inst EC2Instance) string { return inst.PrivateIpAddress }))
	for _, name := range []string{"hostname", "local-hostname"} {
		imdsLeaf(srv, "/latest/meta-data/"+name, imdsAlways(func(inst EC2Instance) string {
			return ec2PrivateDNSName(inst.PrivateIpAddress)
		}))
	}
	imdsLeaf(srv, "/latest/meta-data/ami-launch-index", imdsAlways(func(inst EC2Instance) string { return strconv.Itoa(inst.AmiLaunchIndex) }))
	imdsLeaf(srv, "/latest/meta-data/reservation-id", imdsAlways(func(inst EC2Instance) string { return inst.ReservationId }))
	imdsLeaf(srv, "/latest/meta-data/instance-action", imdsAlways(func(EC2Instance) string { return "none" }))
	imdsLeaf(srv, "/latest/meta-data/instance-life-cycle", imdsAlways(func(inst EC2Instance) string {
		if inst.InstanceLifecycle == "" {
			return "on-demand"
		}
		return inst.InstanceLifecycle
	}))
	imdsLeaf(srv, "/latest/meta-data/mac", imdsAlways(ec2InstanceMAC))
	imdsLeaf(srv, "/latest/meta-data/security-groups", imdsAlways(func(inst EC2Instance) string {
		return strings.Join(imdsGroupNames(inst.SecurityGroupIds), "\n")
	}))
	imdsLeaf(srv, "/latest/meta-data/public-ipv4", func(inst EC2Instance) (string, bool) {
		return inst.PublicIpAddress, inst.PublicIpAddress != ""
	})
	imdsLeaf(srv, "/latest/meta-data/public-hostname", func(inst EC2Instance) (string, bool) {
		name := ec2PublicDNSName(inst)
		return name, name != ""
	})
	imdsLeaf(srv, "/latest/meta-data/services/{$}", imdsAlways(func(EC2Instance) string { return "domain\npartition" }))
	imdsLeaf(srv, "/latest/meta-data/services/domain", imdsAlways(func(EC2Instance) string { return "amazonaws.com" }))
	imdsLeaf(srv, "/latest/meta-data/services/partition", imdsAlways(func(EC2Instance) string { return "aws" }))
	imdsLeaf(srv, "/latest/meta-data/block-device-mapping/{$}", imdsAlways(func(inst EC2Instance) string {
		return strings.Join(sortedKeys(imdsBlockDevices(inst)), "\n")
	}))
	srv.HandleFunc("GET /latest/meta-data/block-device-mapping/{device}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		name, ok := imdsBlockDevices(inst)[sim.PathParam(r, "device")]
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, name)
	})
	// The user data as launched, base64 decoded; 404 when the instance has none.
	srv.HandleFunc("GET /latest/user-data", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		data, err := base64.StdEncoding.DecodeString(inst.UserData)
		if inst.UserData == "" || err != nil {
			imdsNotFound(w)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	})
	registerEC2IMDSNetwork(srv)
	registerEC2IMDSIdentity(srv)
}

// 404 where the asking instance settles no value.
func imdsLeaf(srv *sim.Server, path string, value func(EC2Instance) (string, bool)) {
	srv.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		v, ok := value(inst)
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, v)
	})
}

func imdsAlways(f func(EC2Instance) string) func(EC2Instance) (string, bool) {
	return func(inst EC2Instance) (string, bool) { return f(inst), true }
}

func registerEC2IMDSNetwork(srv *sim.Server) {
	imdsLeaf(srv, "/latest/meta-data/network/{$}", imdsAlways(func(EC2Instance) string { return "interfaces/" }))
	imdsLeaf(srv, "/latest/meta-data/network/interfaces/{$}", imdsAlways(func(EC2Instance) string { return "macs/" }))
	imdsLeaf(srv, "/latest/meta-data/network/interfaces/macs/{$}", imdsAlways(func(inst EC2Instance) string {
		var macs []string
		for _, eni := range imdsInterfaces(inst) {
			macs = append(macs, ec2InterfaceMAC(eni.NetworkInterfaceId)+"/")
		}
		return strings.Join(macs, "\n")
	}))
	srv.HandleFunc("GET /latest/meta-data/network/interfaces/macs/{mac}/{$}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		eni, ok := imdsInterfaceByMAC(inst, sim.PathParam(r, "mac"))
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, strings.Join(sortedKeys(imdsInterfaceKeys(inst, eni)), "\n"))
	})
	srv.HandleFunc("GET /latest/meta-data/network/interfaces/macs/{mac}/{key}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		eni, ok := imdsInterfaceByMAC(inst, sim.PathParam(r, "mac"))
		if !ok {
			imdsNotFound(w)
			return
		}
		value, ok := imdsInterfaceKeys(inst, eni)[sim.PathParam(r, "key")]
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, value)
	})
	srv.HandleFunc("GET /latest/meta-data/network/interfaces/macs/{mac}/ipv4-associations/{$}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		eni, ok := imdsInterfaceByMAC(inst, sim.PathParam(r, "mac"))
		if !ok {
			imdsNotFound(w)
			return
		}
		listing, ok := imdsInterfaceKeys(inst, eni)["ipv4-associations/"]
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, listing)
	})
	srv.HandleFunc("GET /latest/meta-data/network/interfaces/macs/{mac}/ipv4-associations/{ip}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		eni, ok := imdsInterfaceByMAC(inst, sim.PathParam(r, "mac"))
		if !ok {
			imdsNotFound(w)
			return
		}
		private, ok := imdsPublicAddresses(inst, eni)[sim.PathParam(r, "ip")]
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsText(w, private)
	})
	imdsLeaf(srv, "/latest/meta-data/placement/{$}", imdsAlways(func(EC2Instance) string { return "availability-zone\nregion" }))
	imdsLeaf(srv, "/latest/meta-data/placement/region", imdsAlways(func(EC2Instance) string { return awsRegion() }))
	imdsLeaf(srv, "/latest/meta-data/placement/availability-zone", imdsAlways(ec2InstanceAvailabilityZone))

	// A key pair lists under public-keys as index=name, with its OpenSSH key
	// beneath the index.
	keyPair := func(inst EC2Instance) (EC2KeyPair, bool) {
		if inst.KeyName == "" {
			return EC2KeyPair{}, false
		}
		kp, ok := ec2KeyPairs.Get(inst.KeyName)
		return kp, ok && kp.PublicKey != ""
	}
	imdsLeaf(srv, "/latest/meta-data/public-keys/{$}", func(inst EC2Instance) (string, bool) {
		kp, ok := keyPair(inst)
		return "0=" + kp.KeyName, ok
	})
	imdsLeaf(srv, "/latest/meta-data/public-keys/0/{$}", func(inst EC2Instance) (string, bool) {
		_, ok := keyPair(inst)
		return "openssh-key", ok
	})
	imdsLeaf(srv, "/latest/meta-data/public-keys/0/openssh-key", func(inst EC2Instance) (string, bool) {
		kp, ok := keyPair(inst)
		return kp.PublicKey + "\n", ok
	})
}

func registerEC2IMDSIdentity(srv *sim.Server) {
	// The role's credentials are a session named for the instance.
	imdsLeaf(srv, "/latest/meta-data/iam/{$}", func(inst EC2Instance) (string, bool) {
		_, _, ok := ec2InstanceProfileRole(inst)
		return "info\nsecurity-credentials/", ok
	})
	srv.HandleFunc("GET /latest/meta-data/iam/info", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		profile, _, ok := ec2InstanceProfileRole(inst)
		if !ok {
			imdsNotFound(w)
			return
		}
		imdsJSON(w, map[string]string{
			"Code":               "Success",
			"LastUpdated":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			"InstanceProfileArn": profile.Arn,
			"InstanceProfileId":  profile.InstanceProfileId,
		})
	})
	imdsLeaf(srv, "/latest/meta-data/iam/security-credentials/{$}", func(inst EC2Instance) (string, bool) {
		_, role, ok := ec2InstanceProfileRole(inst)
		return role, ok
	})
	srv.HandleFunc("GET /latest/meta-data/iam/security-credentials/{role}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		_, role, ok := ec2InstanceProfileRole(inst)
		if !ok || role != sim.PathParam(r, "role") {
			imdsNotFound(w)
			return
		}
		now := time.Now().UTC()
		cred := roleSessionCredential("ec2:"+inst.InstanceId, role, inst.InstanceId, now)
		imdsJSON(w, map[string]string{
			"Code":            "Success",
			"LastUpdated":     now.Format("2006-01-02T15:04:05Z"),
			"Type":            "AWS-HMAC",
			"AccessKeyId":     cred.AccessKeyID,
			"SecretAccessKey": cred.SecretAccessKey,
			"Token":           cred.SessionToken,
			"Expiration":      cred.Expiration,
		})
	})

	// Only when the metadata options allow tags in metadata.
	imdsLeaf(srv, "/latest/meta-data/tags/{$}", func(inst EC2Instance) (string, bool) {
		return "instance", ec2InstanceTagsEnabled(inst)
	})
	imdsLeaf(srv, "/latest/meta-data/tags/instance", func(inst EC2Instance) (string, bool) {
		if !ec2InstanceTagsEnabled(inst) {
			return "", false
		}
		keys := make([]string, 0, len(inst.Tags))
		for _, tag := range inst.Tags {
			keys = append(keys, tag.Key)
		}
		sort.Strings(keys)
		return strings.Join(keys, "\n"), true
	})
	srv.HandleFunc("GET /latest/meta-data/tags/instance/{key}", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		if ec2InstanceTagsEnabled(inst) {
			for _, tag := range inst.Tags {
				if tag.Key == sim.PathParam(r, "key") {
					imdsText(w, tag.Value)
					return
				}
			}
		}
		imdsNotFound(w)
	})

	// The AWS SDKs read the Region from the identity document.
	srv.HandleFunc("GET /latest/dynamic/instance-identity/document", func(w http.ResponseWriter, r *http.Request) {
		inst, ok := imdsRequest(w, r)
		if !ok {
			return
		}
		arch := inst.Architecture
		if arch == "" {
			arch = "x86_64"
		}
		imdsJSON(w, map[string]any{
			"accountId":               awsAccountID(),
			"architecture":            arch,
			"availabilityZone":        ec2InstanceAvailabilityZone(inst),
			"billingProducts":         nil,
			"devpayProductCodes":      nil,
			"marketplaceProductCodes": nil,
			"imageId":                 inst.ImageId,
			"instanceId":              inst.InstanceId,
			"instanceType":            inst.InstanceType,
			"kernelId":                nil,
			"pendingTime":             inst.LaunchTime,
			"privateIp":               inst.PrivateIpAddress,
			"ramdiskId":               nil,
			"region":                  awsRegion(),
			"version":                 "2017-09-30",
		})
	})
}
