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
	entries := []string{"ami-id", "hostname", "instance-id", "instance-type", "local-hostname", "local-ipv4", "placement/"}
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
			return ec2PrivateDNSHostname(inst.PrivateIpAddress) + "." + awsRegion() + ".compute.internal"
		}))
	}
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
