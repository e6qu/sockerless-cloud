package main

import (
	"crypto/rand"
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

// The instance metadata service answers an EC2 instance and nothing else:
// a request is the instance's when it comes from the address the instance
// holds, and everything it answers is that instance's own.

// imdsToken is an IMDSv2 session token: the instance it was issued to and
// when its TTL ends.
type imdsToken struct {
	instanceID string
	expires    time.Time
}

var imdsTokens sync.Map // token -> imdsToken

// imdsMaxTokenTTL is the longest TTL a token request may ask for, six hours.
const imdsMaxTokenTTL = 21600

// imdsInstanceFor is the instance a request comes from, as the record holds
// it now: metadata options and tags change while the instance runs.
func imdsInstanceFor(r *http.Request) (EC2Instance, bool) {
	indexed, ok := imdsInstancesByIP.ForRequest(r)
	if !ok {
		return EC2Instance{}, false
	}
	inst, ok := ec2Instances.Get(indexed.InstanceId)
	return inst, ok
}

// imdsNotFound is the service's answer to a path it does not serve, and to
// any request from an address no instance holds, which the link-local
// service is not reachable from.
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

// imdsRequest admits a metadata read: from an instance whose metadata
// endpoint is enabled, carrying a token issued to it and still live, or none
// when the instance's metadata options leave IMDSv1 on.
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

// ec2InstanceAvailabilityZone is the zone the instance's subnet lies in.
func ec2InstanceAvailabilityZone(inst EC2Instance) string {
	if subnet, ok := ec2Subnets.Get(inst.SubnetId); ok && subnet.AvailabilityZone != "" {
		return subnet.AvailabilityZone
	}
	return awsAvailabilityZone()
}

// ec2InstanceProfileRole is the role the instance's profile names, if the
// instance has a profile with a role.
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

// imdsListing is the meta-data directory an instance sees: the categories it
// has, each served.
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

// imdsInterfaces are the instance's network interfaces in device order.
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

// imdsInterfaceByMAC is the instance's interface with the MAC a path names.
func imdsInterfaceByMAC(inst EC2Instance, mac string) (EC2NetworkInterface, bool) {
	for _, eni := range imdsInterfaces(inst) {
		if ec2InterfaceMAC(eni.NetworkInterfaceId) == strings.ToLower(mac) {
			return eni, true
		}
	}
	return EC2NetworkInterface{}, false
}

// imdsGroupNames names security groups by their ids.
func imdsGroupNames(ids []string) []string {
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		if sg, ok := ec2SecurityGroups.Get(id); ok {
			names = append(names, sg.GroupName)
		}
	}
	return names
}

// imdsInterfaceKeys are the keys an interface's directory holds, each with
// its value; the primary interface of an instance with a public address
// carries that address too.
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
	if eni.NetworkInterfaceId == inst.NetworkInterfaceId && inst.PublicIpAddress != "" {
		keys["public-ipv4s"] = inst.PublicIpAddress
		if name := ec2PublicDNSName(inst); name != "" {
			keys["public-hostname"] = name
		}
	}
	return keys
}

// imdsBlockDevices maps the instance's block devices to their device names:
// the AMI's root device as ami and root, and each further Amazon EBS volume
// attached as ebsN in attachment order.
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
	// An IMDSv2 token is issued to the instance asking, for the TTL it asks
	// for; a request without a TTL, or one a proxy forwarded, is refused.
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

	leaf := func(path string, value func(EC2Instance) (string, bool)) {
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
	always := func(f func(EC2Instance) string) func(EC2Instance) (string, bool) {
		return func(inst EC2Instance) (string, bool) { return f(inst), true }
	}

	leaf("/latest/meta-data/{$}", always(imdsListing))
	leaf("/latest/meta-data/instance-id", always(func(inst EC2Instance) string { return inst.InstanceId }))
	leaf("/latest/meta-data/instance-type", always(func(inst EC2Instance) string { return inst.InstanceType }))
	leaf("/latest/meta-data/ami-id", always(func(inst EC2Instance) string { return inst.ImageId }))
	leaf("/latest/meta-data/local-ipv4", always(func(inst EC2Instance) string { return inst.PrivateIpAddress }))
	for _, name := range []string{"hostname", "local-hostname"} {
		leaf("/latest/meta-data/"+name, always(func(inst EC2Instance) string {
			return ec2PrivateDNSName(inst.PrivateIpAddress)
		}))
	}
	leaf("/latest/meta-data/ami-launch-index", always(func(inst EC2Instance) string { return strconv.Itoa(inst.AmiLaunchIndex) }))
	leaf("/latest/meta-data/reservation-id", always(func(inst EC2Instance) string { return inst.ReservationId }))
	leaf("/latest/meta-data/instance-action", always(func(EC2Instance) string { return "none" }))
	leaf("/latest/meta-data/instance-life-cycle", always(func(inst EC2Instance) string {
		if inst.InstanceLifecycle == "" {
			return "on-demand"
		}
		return inst.InstanceLifecycle
	}))
	leaf("/latest/meta-data/mac", always(ec2InstanceMAC))
	leaf("/latest/meta-data/security-groups", always(func(inst EC2Instance) string {
		return strings.Join(imdsGroupNames(inst.SecurityGroupIds), "\n")
	}))
	leaf("/latest/meta-data/public-ipv4", func(inst EC2Instance) (string, bool) {
		return inst.PublicIpAddress, inst.PublicIpAddress != ""
	})
	leaf("/latest/meta-data/public-hostname", func(inst EC2Instance) (string, bool) {
		name := ec2PublicDNSName(inst)
		return name, name != ""
	})
	leaf("/latest/meta-data/services/{$}", always(func(EC2Instance) string { return "domain\npartition" }))
	leaf("/latest/meta-data/services/domain", always(func(EC2Instance) string { return "amazonaws.com" }))
	leaf("/latest/meta-data/services/partition", always(func(EC2Instance) string { return "aws" }))
	leaf("/latest/meta-data/block-device-mapping/{$}", always(func(inst EC2Instance) string {
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
	leaf("/latest/meta-data/network/{$}", always(func(EC2Instance) string { return "interfaces/" }))
	leaf("/latest/meta-data/network/interfaces/{$}", always(func(EC2Instance) string { return "macs/" }))
	leaf("/latest/meta-data/network/interfaces/macs/{$}", always(func(inst EC2Instance) string {
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
	leaf("/latest/meta-data/placement/{$}", always(func(EC2Instance) string { return "availability-zone\nregion" }))
	leaf("/latest/meta-data/placement/region", always(func(EC2Instance) string { return awsRegion() }))
	leaf("/latest/meta-data/placement/availability-zone", always(ec2InstanceAvailabilityZone))

	// An instance launched with a key pair lists it under public-keys, as
	// index=name, with its OpenSSH public key beneath the index.
	keyPair := func(inst EC2Instance) (EC2KeyPair, bool) {
		if inst.KeyName == "" {
			return EC2KeyPair{}, false
		}
		kp, ok := ec2KeyPairs.Get(inst.KeyName)
		return kp, ok && kp.PublicKey != ""
	}
	leaf("/latest/meta-data/public-keys/{$}", func(inst EC2Instance) (string, bool) {
		kp, ok := keyPair(inst)
		return "0=" + kp.KeyName, ok
	})
	leaf("/latest/meta-data/public-keys/0/{$}", func(inst EC2Instance) (string, bool) {
		_, ok := keyPair(inst)
		return "openssh-key", ok
	})
	leaf("/latest/meta-data/public-keys/0/openssh-key", func(inst EC2Instance) (string, bool) {
		kp, ok := keyPair(inst)
		return kp.PublicKey + "\n", ok
	})

	// An instance with a profile reads its profile and its role's
	// credentials, a session of the role named for the instance.
	leaf("/latest/meta-data/iam/{$}", func(inst EC2Instance) (string, bool) {
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
	leaf("/latest/meta-data/iam/security-credentials/{$}", func(inst EC2Instance) (string, bool) {
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

	// An instance whose metadata options allow tags in the metadata reads
	// its tag keys and values.
	leaf("/latest/meta-data/tags/{$}", func(inst EC2Instance) (string, bool) {
		return "instance", ec2InstanceTagsEnabled(inst)
	})
	leaf("/latest/meta-data/tags/instance", func(inst EC2Instance) (string, bool) {
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

	// The instance identity document describes the instance; the AWS SDKs
	// read the region from it.
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
