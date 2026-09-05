package main

import (
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECS attributes — name/value labels applied to a resource (typically a
// container instance) and used by task-placement constraints. PutAttributes
// applies them, DeleteAttributes removes them, ListAttributes reads them back
// for a cluster filtered by targetType. The sim stores each attribute keyed by
// cluster + targetId + name.

var ecsAttributes sim.Store[ECSAttribute]

func registerECSAttributes(r *AWSRouter, srv *sim.Server) {
	ecsAttributes = sim.MakeStore[ECSAttribute](srv.DB(), "ecs_attributes")

	r.Register("AmazonEC2ContainerServiceV20141113.PutAttributes", handleECSPutAttributes)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteAttributes", handleECSDeleteAttributes)
	r.Register("AmazonEC2ContainerServiceV20141113.ListAttributes", handleECSListAttributes)
}

func ecsAttributeKey(cluster, targetID, name string) string {
	return cluster + "/" + targetID + "/" + name
}

func handleECSPutAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster    string         `json:"cluster"`
		Attributes []ECSAttribute `json:"attributes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if len(req.Attributes) == 0 {
		AWSError(w, "InvalidParameterException", "attributes cannot be empty", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	for _, a := range req.Attributes {
		if a.Name == "" {
			AWSError(w, "InvalidParameterException", "attribute name is required", http.StatusBadRequest)
			return
		}
		stored := a
		stored.Cluster = clusterName
		ecsAttributes.Put(ecsAttributeKey(clusterName, a.TargetId, a.Name), stored)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"attributes": req.Attributes})
}

func handleECSDeleteAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster    string         `json:"cluster"`
		Attributes []ECSAttribute `json:"attributes"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	for _, a := range req.Attributes {
		ecsAttributes.Delete(ecsAttributeKey(clusterName, a.TargetId, a.Name))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"attributes": req.Attributes})
}

func handleECSListAttributes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Cluster        string `json:"cluster"`
		TargetType     string `json:"targetType"`
		AttributeName  string `json:"attributeName"`
		AttributeValue string `json:"attributeValue"`
		MaxResults     int    `json:"maxResults"`
		NextToken      string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if !ecsRequireCluster(w, req.Cluster) {
		return
	}
	if req.TargetType == "" {
		AWSError(w, "InvalidParameterException", "targetType is required", http.StatusBadRequest)
		return
	}
	clusterName := ecsClusterNameFromRef(req.Cluster)
	var attrs []ECSAttribute
	for _, a := range ecsAttributes.List() {
		if a.Cluster != clusterName {
			continue
		}
		if req.AttributeName != "" && a.Name != req.AttributeName {
			continue
		}
		if req.AttributeValue != "" && a.Value != req.AttributeValue {
			continue
		}
		attrs = append(attrs, a)
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	page, next := awsPage(attrs, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"attributes": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
