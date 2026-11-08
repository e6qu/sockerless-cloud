package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func rdsGlobalClusterARN(id string) string {
	return fmt.Sprintf("arn:aws:rds::%s:global-cluster:%s", awsAccountID(), id)
}

func rdsGlobalClusterResourceID() string {
	return "cluster-" + strings.ToUpper(strings.ReplaceAll(sim.NewUUID(), "-", ""))[:26]
}

// renderRDSGlobalCluster renders a global cluster inside elem: the
// single-resource operations return <GlobalCluster>, and DescribeGlobalClusters
// names list elements <GlobalClusterMember> (the GlobalClusterList xmlName).
func renderRDSGlobalCluster(g RDSGlobalCluster, elem string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s>", elem)
	fmt.Fprintf(&b, "<GlobalClusterIdentifier>%s</GlobalClusterIdentifier>", xmlEscape(g.GlobalClusterIdentifier))
	fmt.Fprintf(&b, "<GlobalClusterResourceId>%s</GlobalClusterResourceId>", xmlEscape(g.GlobalClusterResourceId))
	fmt.Fprintf(&b, "<GlobalClusterArn>%s</GlobalClusterArn>", xmlEscape(g.ARN))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	fmt.Fprintf(&b, "<EngineVersion>%s</EngineVersion>", xmlEscape(g.EngineVersion))
	fmt.Fprintf(&b, "<DatabaseName>%s</DatabaseName>", xmlEscape(g.DatabaseName))
	fmt.Fprintf(&b, "<DeletionProtection>%t</DeletionProtection>", g.DeletionProtection)
	fmt.Fprintf(&b, "<StorageEncrypted>%t</StorageEncrypted>", g.StorageEncrypted)
	b.WriteString("<GlobalClusterMembers>")
	for i, arn := range g.Members {
		b.WriteString("<GlobalClusterMember>")
		fmt.Fprintf(&b, "<DBClusterArn>%s</DBClusterArn>", xmlEscape(arn))
		fmt.Fprintf(&b, "<IsWriter>%t</IsWriter>", i == 0)
		b.WriteString("<Readers>")
		if i == 0 {
			for _, reader := range g.Members[1:] {
				fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(reader))
			}
		}
		b.WriteString("</Readers>")
		b.WriteString("</GlobalClusterMember>")
	}
	b.WriteString("</GlobalClusterMembers>")
	fmt.Fprintf(&b, "</%s>", elem)
	return b.String()
}

func handleRDSCreateGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	if id == "" {
		rdsErrorXML(w, "MissingParameter", "GlobalClusterIdentifier is required", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	if _, ok := rdsGlobalClusters.Get(id); ok {
		rdsErrorXML(w, "GlobalClusterAlreadyExistsFault", "Global cluster already exists", http.StatusBadRequest, sim.RequestID(r.Context()))
		return
	}
	engine := r.FormValue("Engine")
	engineVersion := r.FormValue("EngineVersion")
	var members []string
	// CreateGlobalCluster can adopt an existing regional cluster as the
	// primary via SourceDBClusterIdentifier.
	if src := r.FormValue("SourceDBClusterIdentifier"); src != "" {
		if cl, ok := findRDSClusterByARN(src); ok {
			members = append(members, cl.ARN)
			if engine == "" {
				engine = cl.Engine
			}
			if engineVersion == "" {
				engineVersion = cl.EngineVersion
			}
		}
	}
	if engine == "" {
		engine = "aurora-mysql"
	}
	g := RDSGlobalCluster{
		GlobalClusterIdentifier: id,
		GlobalClusterResourceId: rdsGlobalClusterResourceID(),
		Engine:                  engine,
		EngineVersion:           engineVersion,
		Status:                  "available",
		DatabaseName:            r.FormValue("DatabaseName"),
		DeletionProtection:      r.FormValue("DeletionProtection") == "true",
		StorageEncrypted:        r.FormValue("StorageEncrypted") == "true",
		Members:                 members,
		ARN:                     rdsGlobalClusterARN(id),
		Tags:                    parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	rdsGlobalClusters.Put(id, g)
	rdsXMLResponse(w, "CreateGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSDescribeGlobalClusters(w http.ResponseWriter, r *http.Request) {
	wanted := r.FormValue("GlobalClusterIdentifier")
	matched := false
	var b strings.Builder
	b.WriteString("<GlobalClusters>")
	for _, g := range rdsGlobalClusters.List() {
		if wanted != "" && g.GlobalClusterIdentifier != wanted && g.ARN != wanted {
			continue
		}
		matched = true
		b.WriteString(renderRDSGlobalCluster(g, "GlobalClusterMember"))
	}
	if wanted != "" && !matched {
		rdsErrorXML(w, "GlobalClusterNotFoundFault",
			fmt.Sprintf("GlobalCluster %q not found", wanted),
			http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</GlobalClusters>")
	rdsXMLResponse(w, "DescribeGlobalClusters", b.String(), sim.RequestID(r.Context()))
}

func handleRDSModifyGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	if _, ok := rdsGlobalClusters.Get(id); !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", "Global cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	newID := r.FormValue("NewGlobalClusterIdentifier")
	rdsGlobalClusters.Update(id, func(g *RDSGlobalCluster) {
		if v := r.FormValue("EngineVersion"); v != "" {
			g.EngineVersion = v
		}
		if v := r.FormValue("DeletionProtection"); v != "" {
			g.DeletionProtection = v == "true"
		}
		if newID != "" {
			g.GlobalClusterIdentifier = newID
			g.ARN = rdsGlobalClusterARN(newID)
		}
	})
	updated, _ := rdsGlobalClusters.Get(id)
	if newID != "" && newID != id {
		rdsGlobalClusters.Delete(id)
		rdsGlobalClusters.Put(newID, updated)
	}
	rdsXMLResponse(w, "ModifyGlobalCluster", renderRDSGlobalCluster(updated, "GlobalCluster"), sim.RequestID(r.Context()))
}

func handleRDSDeleteGlobalCluster(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("GlobalClusterIdentifier")
	g, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", "Global cluster not found", http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	rdsGlobalClusters.Delete(id)
	g.Status = "deleting"
	rdsXMLResponse(w, "DeleteGlobalCluster", renderRDSGlobalCluster(g, "GlobalCluster"), sim.RequestID(r.Context()))
}

// rdsGlobalSecondaryLimit is how many secondary DB clusters a global database
// takes (aurora-global-database-attaching.md.gz).
const rdsGlobalSecondaryLimit = 10

func rdsARNRegion(arn string) string {
	if parts := strings.SplitN(arn, ":", 5); len(parts) == 5 {
		return parts[3]
	}
	return ""
}

// rdsGlobalClusterForCreate resolves the global database a CreateDBCluster
// joins. The first cluster to join becomes its primary; a later one joins as
// a read-only secondary in a Region no member uses, up to ten secondaries,
// and with the primary's readers no more than fifteen.
func rdsGlobalClusterForCreate(w http.ResponseWriter, r *http.Request, globalID string) (RDSGlobalCluster, bool, bool) {
	requestID := sim.RequestID(r.Context())
	g, ok := rdsGlobalClusters.Get(globalID)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", fmt.Sprintf("GlobalCluster %q not found", globalID), http.StatusNotFound, requestID)
		return RDSGlobalCluster{}, false, false
	}
	if len(g.Members) == 0 {
		return g, false, true
	}
	region := rdsRequestRegion(r)
	for _, member := range g.Members {
		if rdsARNRegion(member) == region {
			rdsErrorXML(w, "InvalidGlobalClusterStateFault",
				fmt.Sprintf("Global cluster %s already has a member DB cluster in %s.", globalID, region), http.StatusBadRequest, requestID)
			return RDSGlobalCluster{}, false, false
		}
	}
	readers := 0
	if primary, ok := findRDSClusterByARN(g.Members[0]); ok {
		readers = max(len(rdsClusterMembers(primary.DBClusterIdentifier))-1, 0)
	}
	secondaries := len(g.Members) - 1
	if secondaries >= rdsGlobalSecondaryLimit || readers+secondaries >= 15 {
		rdsErrorXML(w, "InvalidGlobalClusterStateFault",
			fmt.Sprintf("Global cluster %s cannot take another secondary DB cluster.", globalID), http.StatusBadRequest, requestID)
		return RDSGlobalCluster{}, false, false
	}
	return g, true, true
}

// rdsGlobalMember resolves a global database member named by ARN or
// identifier: DBClusterNotFoundFault when no such cluster exists,
// InvalidGlobalClusterStateFault when it is no member.
func rdsGlobalMember(w http.ResponseWriter, r *http.Request, g RDSGlobalCluster, target string) (int, bool) {
	requestID := sim.RequestID(r.Context())
	cluster, ok := findRDSClusterByARN(target)
	if !ok {
		cluster, ok = rdsClusterIn(r, target)
	}
	if !ok {
		rdsErrorXML(w, "DBClusterNotFoundFault", fmt.Sprintf("DBCluster %s not found.", target), http.StatusNotFound, requestID)
		return 0, false
	}
	for i, member := range g.Members {
		if member == cluster.ARN {
			return i, true
		}
	}
	rdsErrorXML(w, "InvalidGlobalClusterStateFault",
		fmt.Sprintf("DB cluster %s is not a member of global cluster %s.", cluster.ARN, g.GlobalClusterIdentifier), http.StatusBadRequest, requestID)
	return 0, false
}

// rdsPromoteGlobalMember makes the member at index the writer; the old
// primary stays as a read-only secondary.
func rdsPromoteGlobalMember(w http.ResponseWriter, r *http.Request, action string) {
	id := r.FormValue("GlobalClusterIdentifier")
	requestID := sim.RequestID(r.Context())
	g, ok := rdsGlobalClusters.Get(id)
	if !ok {
		rdsErrorXML(w, "GlobalClusterNotFoundFault", fmt.Sprintf("GlobalCluster %q not found", id), http.StatusNotFound, requestID)
		return
	}
	index, ok := rdsGlobalMember(w, r, g, r.FormValue("TargetDbClusterIdentifier"))
	if !ok {
		return
	}
	if index == 0 {
		rdsErrorXML(w, "InvalidGlobalClusterStateFault",
			fmt.Sprintf("DB cluster %s is already the primary of global cluster %s.", g.Members[0], id), http.StatusBadRequest, requestID)
		return
	}
	rdsGlobalClusters.Update(id, func(x *RDSGlobalCluster) {
		promoted := x.Members[index]
		rest := append(append([]string{}, x.Members[:index]...), x.Members[index+1:]...)
		x.Members = append([]string{promoted}, rest...)
	})
	g, _ = rdsGlobalClusters.Get(id)
	rdsXMLResponse(w, action, renderRDSGlobalCluster(g, "GlobalCluster"), requestID)
}
