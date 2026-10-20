package main

import (
	"encoding/json"
	"fmt"
	"strconv"

	"net/http"
	"net/url"
	"strings"

	"cloud.google.com/go/run/apiv2/runpb"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/e6qu/sockerless-cloud/sim/revisions"
)

// A Cloud Run service sends each request to one of its revisions in
// proportion to its traffic targets, and a tagged target's revision answers
// on its own URL, the tag and three dashes before the service's host.

const (
	cloudRunTrafficLatest   trafficAllocationType = "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST"
	cloudRunTrafficRevision trafficAllocationType = "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION"
)

// trafficAllocationType is a traffic target's type. The REST transport of
// the Go clients sends it as its proto number, which maps through the
// API's own table to the name a read answers with.
type trafficAllocationType string

func (e *trafficAllocationType) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*e = trafficAllocationType(s)
		return nil
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return fmt.Errorf("traffic target allocation type %s: %w", data, err)
	}
	name, ok := runpb.TrafficTargetAllocationType_name[int32(n)]
	if !ok {
		return fmt.Errorf("unknown traffic target allocation type %s", data)
	}
	*e = trafficAllocationType(name)
	return nil
}

// settleCloudRunServiceTraffic gives a service without traffic targets all of
// it on its latest revision, as Cloud Run does, and reports where its traffic
// goes in trafficStatuses.
func settleCloudRunServiceTraffic(svc *ServiceV2) {
	if len(svc.Traffic) == 0 {
		svc.Traffic = []TrafficTarget{{Type: cloudRunTrafficLatest, Percent: 100}}
	}
	svc.TrafficStatuses = make([]TrafficTargetStatus, 0, len(svc.Traffic))
	for _, target := range svc.Traffic {
		status := TrafficTargetStatus{Type: target.Type, Revision: target.Revision, Percent: target.Percent, Tag: target.Tag}
		if status.Type == "" {
			status.Type = cloudRunTrafficRevision
		}
		if target.Tag != "" && svc.URI != "" {
			status.URI = cloudRunTagURI(svc.URI, target.Tag)
		}
		svc.TrafficStatuses = append(svc.TrafficStatuses, status)
	}
}

// cloudRunTagURI is the URL a tagged traffic target answers on.
func cloudRunTagURI(serviceURI, tag string) string {
	parsed, err := url.Parse(serviceURI)
	if err != nil {
		return ""
	}
	parsed.Host = tag + "---" + parsed.Host
	return parsed.String()
}

// cloudRunTargetRevision is the bare ID of the revision a traffic target
// names, its latest ready revision for a LATEST target.
func cloudRunTargetRevision(svc ServiceV2, target TrafficTarget) string {
	if target.Type == cloudRunTrafficLatest {
		return svc.LatestReadyRevision[strings.LastIndex(svc.LatestReadyRevision, "/")+1:]
	}
	return target.Revision
}

// cloudRunRoutedRevisions is the set of revisions a service's traffic can
// reach, by weight or by tag.
func cloudRunRoutedRevisions(svc ServiceV2) map[string]bool {
	routed := map[string]bool{}
	for _, target := range svc.Traffic {
		if target.Percent > 0 || target.Tag != "" {
			routed[svc.Name+"/revisions/"+cloudRunTargetRevision(svc, target)] = true
		}
	}
	return routed
}

// cloudRunRequestRevision chooses the revision a request to the service goes
// to: a tagged target's for the tag's URL, else one by traffic weight.
func cloudRunRequestRevision(svc ServiceV2, host string) (RevisionV2, bool) {
	hostname := lbplane.Hostname(host)
	var id string
	if tag, _, tagged := strings.Cut(hostname, "---"); tagged {
		for _, target := range svc.Traffic {
			if target.Tag == tag {
				id = cloudRunTargetRevision(svc, target)
			}
		}
	} else {
		candidates := make([]revisions.Weighted[string], 0, len(svc.Traffic))
		for _, target := range svc.Traffic {
			candidates = append(candidates, revisions.Weighted[string]{Revision: cloudRunTargetRevision(svc, target), Weight: int(target.Percent)})
		}
		id, _ = revisions.Pick(candidates)
	}
	if id == "" || crv2Revisions == nil {
		return RevisionV2{}, false
	}
	return crv2Revisions.Get(svc.Name + "/revisions/" + id)
}

// cloudRunRevisionNameFree refuses a template revision name the service
// cannot take: one without the service's name as its prefix, or one a
// revision of the service already has.
func cloudRunRevisionNameFree(w http.ResponseWriter, store sim.Store[RevisionV2], serviceName, serviceID string, tmpl *RevisionTemplate) bool {
	if tmpl == nil || tmpl.Revision == "" {
		return true
	}
	if !strings.HasPrefix(tmpl.Revision, serviceID+"-") {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"template.revision %q must begin with the service name followed by a hyphen, %q.", tmpl.Revision, serviceID+"-")
		return false
	}
	if _, exists := store.Get(serviceName + "/revisions/" + tmpl.Revision); exists {
		GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
			"Revision named '%s' with different configuration already exists.", tmpl.Revision)
		return false
	}
	return true
}

// cloudRunServiceTrafficValid refuses traffic targets that name a revision
// the service does not have, carry a type that disagrees with what they
// name, or whose percents do not add up to 100. A write that deploys a
// revision named by its template may route to it.
func cloudRunServiceTrafficValid(w http.ResponseWriter, store sim.Store[RevisionV2], serviceName string, traffic []TrafficTarget, tmpl *RevisionTemplate, deploys bool) bool {
	if len(traffic) == 0 {
		return true
	}
	total := int32(0)
	tags := map[string]bool{}
	for i, target := range traffic {
		switch target.Type {
		case cloudRunTrafficLatest:
			if target.Revision != "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "traffic[%d] is of type LATEST and names revision %q.", i, target.Revision)
				return false
			}
		case cloudRunTrafficRevision, "", "TRAFFIC_TARGET_ALLOCATION_TYPE_UNSPECIFIED":
			if target.Revision == "" {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "traffic[%d] names no revision.", i)
				return false
			}
			pending := deploys && tmpl != nil && tmpl.Revision == target.Revision
			if _, exists := store.Get(serviceName + "/revisions/" + target.Revision); !exists && !pending {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Revision %q referenced in traffic not found.", target.Revision)
				return false
			}
		default:
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "traffic[%d].type %q is not a traffic target allocation type.", i, string(target.Type))
			return false
		}
		if target.Percent < 0 || target.Percent > 100 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "traffic[%d].percent %d is not between 0 and 100.", i, target.Percent)
			return false
		}
		if target.Tag != "" {
			if tags[target.Tag] {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "traffic tag %q is used more than once.", target.Tag)
				return false
			}
			tags[target.Tag] = true
		}
		total += target.Percent
	}
	if total != 100 {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", fmt.Sprintf("Traffic percents add up to %d; they must add up to 100.", total))
		return false
	}
	return true
}
