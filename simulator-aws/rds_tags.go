package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func handleRDSAddTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if _, ok := rdsReplicatedBackupIn(rdsRequestRegion(r), arn); ok {
		rdsReplicatedBackups.Update(arn, func(b *RDSInstanceAutomatedBackup) {
			b.Tags = mergeTags(b.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsInstances.Update(inst.DBInstanceIdentifier, func(i *RDSInstance) {
			i.Tags = mergeTags(i.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
			s.Tags = mergeTags(s.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsClusters.Update(cl.DBClusterIdentifier, func(c *RDSCluster) {
			c.Tags = mergeTags(c.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsSubnetGroups.Update(sg.DBSubnetGroupName, func(g *RDSSubnetGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsParamGroups.Update(pg.DBParameterGroupName, func(g *RDSParamGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsClusterSnapshots.Update(cs.DBClusterSnapshotIdentifier, func(s *RDSClusterSnapshot) {
			s.Tags = mergeTags(s.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsClusterParamGroups.Update(cpg.DBClusterParameterGroupName, func(g *RDSClusterParamGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsOptionGroups.Update(og.OptionGroupName, func(g *RDSOptionGroup) {
			g.Tags = mergeTags(g.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	if res, ok := findRDSCapacityReservationByARN(arn); ok {
		rdsCapacityReservations.Update(res.Identifier, func(stored *RDSCapacityReservation) {
			stored.Tags = mergeTags(stored.Tags, parseAWSQueryTagMap(r, "Tags.Tag"))
		})
		rdsXMLResponse(w, "AddTagsToResource", "", sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func handleRDSListTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if replica, ok := rdsReplicatedBackupIn(rdsRequestRegion(r), arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(replica.Tags), sim.RequestID(r.Context()))
		return
	}
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(inst.Tags), sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(snap.Tags), sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cl.Tags), sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(sg.Tags), sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(pg.Tags), sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cs.Tags), sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(cpg.Tags), sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(og.Tags), sim.RequestID(r.Context()))
		return
	}
	if res, ok := findRDSCapacityReservationByARN(arn); ok {
		rdsXMLResponse(w, "ListTagsForResource", renderRDSTagList(res.Tags), sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func renderRDSTagList(tags map[string]string) string {
	var b strings.Builder
	b.WriteString("<TagList>")
	for k, v := range tags {
		fmt.Fprintf(&b, "<Tag><Key>%s</Key><Value>%s</Value></Tag>", xmlEscape(k), xmlEscape(v))
	}
	b.WriteString("</TagList>")
	return b.String()
}

func handleRDSRemoveTags(w http.ResponseWriter, r *http.Request) {
	arn := r.FormValue("ResourceName")
	if _, ok := rdsReplicatedBackupIn(rdsRequestRegion(r), arn); ok {
		rdsReplicatedBackups.Update(arn, func(b *RDSInstanceAutomatedBackup) {
			removeAWSQueryTags(b.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	inst, ok := findRDSByARN(arn)
	if ok {
		rdsInstances.Update(inst.DBInstanceIdentifier, func(i *RDSInstance) {
			removeAWSQueryTags(i.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	snap, ok := findRDSSnapshotByARN(arn)
	if ok {
		rdsSnapshots.Update(snap.DBSnapshotIdentifier, func(s *RDSSnapshot) {
			removeAWSQueryTags(s.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cl, ok := findRDSClusterByARN(arn); ok {
		rdsClusters.Update(cl.DBClusterIdentifier, func(c *RDSCluster) {
			removeAWSQueryTags(c.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if sg, ok := findRDSSubnetGroupByARN(arn); ok {
		rdsSubnetGroups.Update(sg.DBSubnetGroupName, func(g *RDSSubnetGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if pg, ok := findRDSParamGroupByARN(arn); ok {
		rdsParamGroups.Update(pg.DBParameterGroupName, func(g *RDSParamGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cs, ok := findRDSClusterSnapshotByARN(arn); ok {
		rdsClusterSnapshots.Update(cs.DBClusterSnapshotIdentifier, func(s *RDSClusterSnapshot) {
			removeAWSQueryTags(s.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if cpg, ok := findRDSClusterParamGroupByARN(arn); ok {
		rdsClusterParamGroups.Update(cpg.DBClusterParameterGroupName, func(g *RDSClusterParamGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if og, ok := findRDSOptionGroupByARN(arn); ok {
		rdsOptionGroups.Update(og.OptionGroupName, func(g *RDSOptionGroup) {
			removeAWSQueryTags(g.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	if res, ok := findRDSCapacityReservationByARN(arn); ok {
		rdsCapacityReservations.Update(res.Identifier, func(stored *RDSCapacityReservation) {
			removeAWSQueryTags(stored.Tags, r)
		})
		rdsXMLResponse(w, "RemoveTagsFromResource", "", sim.RequestID(r.Context()))
		return
	}
	rdsErrorXML(w, rdsTagResourceNotFoundCode(arn), "Resource not found", http.StatusNotFound, sim.RequestID(r.Context()))
}

func findRDSByARN(arn string) (RDSInstance, bool) {
	for _, i := range rdsInstances.List() {
		if i.ARN == arn {
			return i, true
		}
	}
	// Some callers pass the instance identifier directly.
	if i, ok := rdsInstances.Get(arn); ok {
		return i, true
	}
	return RDSInstance{}, false
}

func findRDSSnapshotByARN(arn string) (RDSSnapshot, bool) {
	for _, s := range rdsSnapshots.List() {
		if s.ARN == arn {
			return s, true
		}
	}
	if s, ok := rdsSnapshots.Get(arn); ok {
		return s, true
	}
	return RDSSnapshot{}, false
}

func rdsTagResourceNotFoundCode(arn string) string {
	switch {
	case strings.Contains(arn, ":auto-backup:"):
		return "DBInstanceAutomatedBackupNotFound"
	case strings.Contains(arn, ":cluster-snapshot:"):
		return "DBClusterSnapshotNotFoundFault"
	case strings.Contains(arn, ":snapshot:"):
		return "DBSnapshotNotFound"
	case strings.Contains(arn, ":cluster-pg:"):
		return "DBClusterParameterGroupNotFound"
	case strings.Contains(arn, ":cluster:"):
		return "DBClusterNotFoundFault"
	case strings.Contains(arn, ":subgrp:"):
		return "DBSubnetGroupNotFoundFault"
	case strings.Contains(arn, ":pg:"):
		return "DBParameterGroupNotFound"
	case strings.Contains(arn, ":og:"):
		return "OptionGroupNotFoundFault"
	case strings.Contains(arn, ":db-capacity-reservation:"):
		return "DBCapacityReservationNotFound"
	}
	return "DBInstanceNotFound"
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseAWSQueryTagMap(r *http.Request, prefix string) map[string]string {
	tags := map[string]string{}
	for n := 1; n <= 50; n++ {
		k := r.FormValue(fmt.Sprintf("%s.%d.Key", prefix, n))
		v := r.FormValue(fmt.Sprintf("%s.%d.Value", prefix, n))
		if k == "" {
			break
		}
		tags[k] = v
	}
	return tags
}

func rdsFilterValue(r *http.Request, name string) string {
	for n := 1; n <= 50; n++ {
		prefix := fmt.Sprintf("Filters.Filter.%d", n)
		filterName := r.FormValue(prefix + ".Name")
		if filterName == "" {
			break
		}
		if filterName != name {
			continue
		}
		return r.FormValue(prefix + ".Values.Value.1")
	}
	return ""
}

func mergeTags(dst map[string]string, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func removeAWSQueryTags(tags map[string]string, r *http.Request) {
	for n := 1; n <= 50; n++ {
		k := r.FormValue(fmt.Sprintf("TagKeys.member.%d", n))
		if k == "" {
			break
		}
		delete(tags, k)
	}
}
