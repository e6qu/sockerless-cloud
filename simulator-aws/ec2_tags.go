package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleCreateTags(w http.ResponseWriter, r *http.Request) {
	resources := ec2ParamList(r, "ResourceId")
	tags := parseIndexedTags(r, "Tag")
	for _, id := range resources {
		if strings.HasPrefix(id, "i-") {
			ec2Instances.Update(id, func(inst *EC2Instance) { inst.Tags = mergeEC2Tags(inst.Tags, tags) })
		}
		if strings.HasPrefix(id, "eni-") {
			ec2NetworkInterfaces.Update(id, func(eni *EC2NetworkInterface) { eni.Tags = mergeEC2Tags(eni.Tags, tags) })
		}
		if strings.HasPrefix(id, "vol-") {
			ec2Volumes.Update(id, func(vol *EC2Volume) { vol.Tags = mergeEC2Tags(vol.Tags, tags) })
		}
		if strings.HasPrefix(id, "snap-") {
			ec2Snapshots.Update(id, func(snap *EC2Snapshot) { snap.Tags = mergeEC2Tags(snap.Tags, tags) })
		}
		if strings.HasPrefix(id, "acl-") {
			ec2NetworkAcls.Update(id, func(acl *EC2NetworkAcl) { acl.Tags = mergeEC2Tags(acl.Tags, tags) })
		}
		if strings.HasPrefix(id, "pcx-") {
			ec2VpcPeerings.Update(id, func(pcx *EC2VpcPeeringConnection) { pcx.Tags = mergeEC2Tags(pcx.Tags, tags) })
		}
		if strings.HasPrefix(id, "pl-") {
			ec2ManagedPrefixLists.Update(id, func(pl *EC2ManagedPrefixList) { pl.Tags = mergeEC2Tags(pl.Tags, tags) })
		}
		if strings.HasPrefix(id, "fl-") {
			ec2FlowLogs.Update(id, func(fl *EC2FlowLog) { fl.Tags = mergeEC2Tags(fl.Tags, tags) })
		}
		if strings.HasPrefix(id, "eigw-") {
			ec2EgressOnlyGateways.Update(id, func(eigw *EC2EgressOnlyInternetGateway) { eigw.Tags = mergeEC2Tags(eigw.Tags, tags) })
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<CreateTagsResponse %s><requestId>%s</requestId><return>true</return></CreateTagsResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDeleteTags(w http.ResponseWriter, r *http.Request) {
	resources := ec2ParamList(r, "ResourceId")
	keys := map[string]bool{}
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("Tag.%d.Key", i))
		if key == "" {
			break
		}
		keys[key] = true
	}
	filter := func(tags []EC2Tag) []EC2Tag {
		var out []EC2Tag
		for _, tag := range tags {
			if !keys[tag.Key] {
				out = append(out, tag)
			}
		}
		return out
	}
	for _, id := range resources {
		if strings.HasPrefix(id, "i-") {
			ec2Instances.Update(id, func(inst *EC2Instance) { inst.Tags = filter(inst.Tags) })
		}
		if strings.HasPrefix(id, "eni-") {
			ec2NetworkInterfaces.Update(id, func(eni *EC2NetworkInterface) { eni.Tags = filter(eni.Tags) })
		}
		if strings.HasPrefix(id, "vol-") {
			ec2Volumes.Update(id, func(vol *EC2Volume) { vol.Tags = filter(vol.Tags) })
		}
		if strings.HasPrefix(id, "snap-") {
			ec2Snapshots.Update(id, func(snap *EC2Snapshot) { snap.Tags = filter(snap.Tags) })
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DeleteTagsResponse %s><requestId>%s</requestId><return>true</return></DeleteTagsResponse>`, ec2Xmlns(), sim.NewUUID())
}

func handleDescribeTags(w http.ResponseWriter, r *http.Request) {
	type tagEntry struct {
		resourceID   string
		resourceType string
		key          string
		value        string
	}
	filters := ec2Filters(r)
	matches := func(entry tagEntry) bool {
		for name, values := range filters {
			matched := false
			for _, value := range values {
				switch {
				case name == "resource-id" && entry.resourceID == value:
					matched = true
				case name == "resource-type" && entry.resourceType == value:
					matched = true
				case name == "key" && entry.key == value:
					matched = true
				case name == "value" && entry.value == value:
					matched = true
				case strings.HasPrefix(name, "tag:") && strings.TrimPrefix(name, "tag:") == entry.key && entry.value == value:
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
		return true
	}
	entries := make([]tagEntry, 0)
	add := func(entry tagEntry) {
		if matches(entry) {
			entries = append(entries, entry)
		}
	}
	for _, inst := range ec2Instances.List() {
		for _, tag := range inst.Tags {
			add(tagEntry{resourceID: inst.InstanceId, resourceType: "instance", key: tag.Key, value: tag.Value})
		}
	}
	for _, eni := range ec2NetworkInterfaces.List() {
		for _, tag := range eni.Tags {
			add(tagEntry{resourceID: eni.NetworkInterfaceId, resourceType: "network-interface", key: tag.Key, value: tag.Value})
		}
	}
	for _, vol := range ec2Volumes.List() {
		for _, tag := range vol.Tags {
			add(tagEntry{resourceID: vol.VolumeId, resourceType: "volume", key: tag.Key, value: tag.Value})
		}
	}
	for _, snap := range ec2Snapshots.List() {
		for _, tag := range snap.Tags {
			add(tagEntry{resourceID: snap.SnapshotId, resourceType: "snapshot", key: tag.Key, value: tag.Value})
		}
	}
	// Stable order keeps the cursor consistent.
	entries, nextToken, pageOK := awsKeyPage(w, ec2BadToken, entries, func(it tagEntry) string { return it.resourceID + "\x00" + it.key }, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
	if !pageOK {
		return
	}

	var items strings.Builder
	for _, entry := range entries {
		fmt.Fprintf(&items, `<item><resourceId>%s</resourceId><resourceType>%s</resourceType><key>%s</key><value>%s</value></item>`,
			entry.resourceID, entry.resourceType, entry.key, entry.value)
	}
	nextTokenXML := ""
	if nextToken != "" {
		nextTokenXML = "<nextToken>" + nextToken + "</nextToken>"
	}
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprintf(w, `<DescribeTagsResponse %s>
  <requestId>%s</requestId>
  <tagSet>%s</tagSet>%s
</DescribeTagsResponse>`, ec2Xmlns(), sim.NewUUID(), items.String(), nextTokenXML)
}

func parseIndexedTags(r *http.Request, prefix string) []EC2Tag {
	var tags []EC2Tag
	for i := 1; ; i++ {
		key := r.FormValue(fmt.Sprintf("%s.%d.Key", prefix, i))
		if key == "" {
			break
		}
		tags = append(tags, EC2Tag{Key: key, Value: r.FormValue(fmt.Sprintf("%s.%d.Value", prefix, i))})
	}
	return tags
}

func mergeEC2Tags(existing, updates []EC2Tag) []EC2Tag {
	byKey := map[string]string{}
	for _, t := range existing {
		byKey[t.Key] = t.Value
	}
	for _, t := range updates {
		byKey[t.Key] = t.Value
	}
	var out []EC2Tag
	for key, value := range byKey {
		out = append(out, EC2Tag{Key: key, Value: value})
	}
	return out
}
