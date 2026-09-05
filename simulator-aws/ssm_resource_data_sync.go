package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// SSM Resource Data Sync — terraform's `aws_ssm_resource_data_sync`
// resource hits this slice. A sync streams inventory / parameter data to
// an S3 destination (SyncType "SyncToDestination") or aggregates from
// source accounts ("SyncFromSource"). The sim round-trips the nested
// S3Destination / SyncSource structures verbatim as raw JSON.

// SSMResourceDataSync is a configured data sync, keyed by SyncName.
type SSMResourceDataSync struct {
	SyncName             string          `json:"SyncName"`
	SyncType             string          `json:"SyncType"`
	S3Destination        json.RawMessage `json:"S3Destination,omitempty"`
	SyncSource           json.RawMessage `json:"SyncSource,omitempty"`
	SyncCreatedTime      float64         `json:"SyncCreatedTime"`
	SyncLastModifiedTime float64         `json:"SyncLastModifiedTime"`
	LastStatus           string          `json:"LastStatus"`
}

var ssmResourceDataSyncs sim.Store[SSMResourceDataSync]

func registerSSMResourceDataSync(r *AWSRouter, srv *sim.Server) {
	ssmResourceDataSyncs = sim.MakeStore[SSMResourceDataSync](srv.DB(), "ssm_resource_data_syncs")

	r.Register("AmazonSSM.CreateResourceDataSync", handleSSMCreateResourceDataSync)
	r.Register("AmazonSSM.DeleteResourceDataSync", handleSSMDeleteResourceDataSync)
	r.Register("AmazonSSM.ListResourceDataSync", handleSSMListResourceDataSync)
	r.Register("AmazonSSM.UpdateResourceDataSync", handleSSMUpdateResourceDataSync)
}

func ssmRDSKey(syncName, syncType string) string {
	if syncType == "" {
		syncType = "SyncToDestination"
	}
	return syncType + "/" + syncName
}

func handleSSMCreateResourceDataSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName      string          `json:"SyncName"`
		SyncType      string          `json:"SyncType"`
		S3Destination json.RawMessage `json:"S3Destination"`
		SyncSource    json.RawMessage `json:"SyncSource"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.SyncName == "" {
		AWSError(w, "ValidationException", "SyncName is required", http.StatusBadRequest)
		return
	}
	if req.SyncType == "" {
		req.SyncType = "SyncToDestination"
	}
	key := ssmRDSKey(req.SyncName, req.SyncType)
	if _, exists := ssmResourceDataSyncs.Get(key); exists {
		AWSErrorf(w, "ResourceDataSyncAlreadyExistsException", http.StatusBadRequest,
			"The specified sync configuration is currently in use.")
		return
	}
	now := float64(time.Now().Unix())
	ssmResourceDataSyncs.Put(key, SSMResourceDataSync{
		SyncName:             req.SyncName,
		SyncType:             req.SyncType,
		S3Destination:        req.S3Destination,
		SyncSource:           req.SyncSource,
		SyncCreatedTime:      now,
		SyncLastModifiedTime: now,
		LastStatus:           "Successful",
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMDeleteResourceDataSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName string `json:"SyncName"`
		SyncType string `json:"SyncType"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmRDSKey(req.SyncName, req.SyncType)
	if _, ok := ssmResourceDataSyncs.Get(key); !ok {
		AWSErrorf(w, "ResourceDataSyncNotFoundException", http.StatusBadRequest,
			"The specified sync name %q wasn't found.", req.SyncName)
		return
	}
	ssmResourceDataSyncs.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMUpdateResourceDataSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncName   string          `json:"SyncName"`
		SyncType   string          `json:"SyncType"`
		SyncSource json.RawMessage `json:"SyncSource"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	key := ssmRDSKey(req.SyncName, req.SyncType)
	s, ok := ssmResourceDataSyncs.Get(key)
	if !ok {
		AWSErrorf(w, "ResourceDataSyncNotFoundException", http.StatusBadRequest,
			"The specified sync name %q wasn't found.", req.SyncName)
		return
	}
	if req.SyncSource != nil {
		s.SyncSource = req.SyncSource
	}
	s.SyncLastModifiedTime = float64(time.Now().Unix())
	ssmResourceDataSyncs.Put(key, s)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMListResourceDataSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SyncType   string `json:"SyncType"`
		NextToken  string `json:"NextToken"`
		MaxResults int    `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var all []SSMResourceDataSync
	for _, s := range ssmResourceDataSyncs.List() {
		if req.SyncType != "" && s.SyncType != req.SyncType {
			continue
		}
		all = append(all, s)
	}
	sortBy(all, func(s SSMResourceDataSync) string { return s.SyncName })
	page, next := awsPage(all, req.NextToken, req.MaxResults, 50)
	out := make([]map[string]any, 0, len(page))
	for _, s := range page {
		row := map[string]any{
			"SyncName":             s.SyncName,
			"SyncType":             s.SyncType,
			"SyncCreatedTime":      s.SyncCreatedTime,
			"SyncLastModifiedTime": s.SyncLastModifiedTime,
			"LastStatus":           s.LastStatus,
		}
		if len(s.S3Destination) > 0 {
			row["S3Destination"] = s.S3Destination
		}
		if len(s.SyncSource) > 0 {
			row["SyncSource"] = s.SyncSource
		}
		out = append(out, row)
	}
	resp := map[string]any{"ResourceDataSyncItems": out}
	if next != "" {
		resp["NextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
