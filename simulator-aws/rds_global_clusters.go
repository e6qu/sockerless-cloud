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

// renderRDSGlobalCluster renders a global cluster wrapped in the given
// element name. CreateGlobalCluster et al. return a single
// <GlobalCluster> member, while the DescribeGlobalClusters list element
// is named <GlobalClusterMember> (per the GlobalClusterList xmlName in
// the RDS model).
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
		b.WriteString("<Readers></Readers>")
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
