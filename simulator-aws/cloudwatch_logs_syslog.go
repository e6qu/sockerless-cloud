package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// CloudWatch Logs account storage-tier policy and VPC-endpoint syslog
// ingestion configuration. These operations were added to the public Logs
// model after the original slice and complete the current vendored surface.

type CWStorageTierPolicy struct {
	StorageTier     string `json:"storageTier"`
	LastUpdatedTime int64  `json:"lastUpdatedTime"`
}

type CWSyslogConfiguration struct {
	LogGroupArn   string `json:"logGroupArn"`
	SourceType    string `json:"sourceType"`
	VpcEndpointId string `json:"vpcEndpointId,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
}

var (
	cwStorageTierPolicies  sim.Store[CWStorageTierPolicy]
	cwSyslogConfigurations sim.Store[CWSyslogConfiguration]
)

func registerCloudWatchLogsSyslog(r *AWSRouter, srv *sim.Server) {
	cwStorageTierPolicies = sim.MakeStore[CWStorageTierPolicy](srv.DB(), "cw_storage_tier_policies")
	cwSyslogConfigurations = sim.MakeStore[CWSyslogConfiguration](srv.DB(), "cw_syslog_configurations")
	r.Register("Logs_20140328.PutStorageTierPolicy", handleCWPutStorageTierPolicy)
	r.Register("Logs_20140328.GetStorageTierPolicy", handleCWGetStorageTierPolicy)
	r.Register("Logs_20140328.PutSyslogConfiguration", handleCWPutSyslogConfiguration)
	r.Register("Logs_20140328.ListSyslogConfigurations", handleCWListSyslogConfigurations)
	r.Register("Logs_20140328.DeleteSyslogConfiguration", handleCWDeleteSyslogConfiguration)
}

func handleCWPutStorageTierPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		StorageTier string `json:"storageTier"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.StorageTier != "STANDARD" && req.StorageTier != "INTELLIGENT_TIERING" {
		AWSError(w, "InvalidParameterException", "storageTier must be STANDARD or INTELLIGENT_TIERING", http.StatusBadRequest)
		return
	}
	policy := CWStorageTierPolicy{StorageTier: req.StorageTier, LastUpdatedTime: time.Now().UnixMilli()}
	cwStorageTierPolicies.Put(awsAccountID(), policy)
	sim.WriteJSON(w, http.StatusOK, policy)
}

func handleCWGetStorageTierPolicy(w http.ResponseWriter, _ *http.Request) {
	policy, ok := cwStorageTierPolicies.Get(awsAccountID())
	if !ok {
		policy = CWStorageTierPolicy{StorageTier: "STANDARD"}
	}
	sim.WriteJSON(w, http.StatusOK, policy)
}

func cwSyslogKey(logGroupArn, endpointID string) string {
	return logGroupArn + "|" + endpointID
}

func handleCWPutSyslogConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		VpcEndpointId      string `json:"vpcEndpointId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "The specified log group does not exist.", http.StatusNotFound)
		return
	}
	if req.VpcEndpointId != "" {
		if _, ok := ec2VpcEndpoints.Get(req.VpcEndpointId); !ok {
			AWSError(w, "ResourceNotFoundException", "The specified VPC endpoint does not exist.", http.StatusNotFound)
			return
		}
	}
	configuration := CWSyslogConfiguration{
		LogGroupArn: cwLogGroupArn(name), SourceType: "VPCE",
		VpcEndpointId: req.VpcEndpointId, CreatedAt: time.Now().UnixMilli(),
	}
	cwSyslogConfigurations.Put(cwSyslogKey(configuration.LogGroupArn, req.VpcEndpointId), configuration)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleCWListSyslogConfigurations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		VpcEndpointId      string `json:"vpcEndpointId"`
		NextToken          string `json:"nextToken"`
		MaxResults         int    `json:"maxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	logGroupARN := ""
	if req.LogGroupIdentifier != "" {
		name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
		if !ok {
			AWSError(w, "ResourceNotFoundException", "The specified log group does not exist.", http.StatusNotFound)
			return
		}
		logGroupARN = cwLogGroupArn(name)
	}
	configurations := cwSyslogConfigurations.Filter(func(configuration CWSyslogConfiguration) bool {
		return (logGroupARN == "" || configuration.LogGroupArn == logGroupARN) &&
			(req.VpcEndpointId == "" || configuration.VpcEndpointId == req.VpcEndpointId)
	})
	sort.Slice(configurations, func(i, j int) bool {
		return cwSyslogKey(configurations[i].LogGroupArn, configurations[i].VpcEndpointId) <
			cwSyslogKey(configurations[j].LogGroupArn, configurations[j].VpcEndpointId)
	})
	page, next := awsPage(configurations, req.NextToken, req.MaxResults, 50)
	response := map[string]any{"syslogConfigurations": page}
	if next != "" {
		response["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, response)
}

func handleCWDeleteSyslogConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LogGroupIdentifier string `json:"logGroupIdentifier"`
		VpcEndpointId      string `json:"vpcEndpointId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	name, ok := cwResolveLogGroupIdentifier(req.LogGroupIdentifier)
	if !ok {
		AWSError(w, "ResourceNotFoundException", "The specified log group does not exist.", http.StatusNotFound)
		return
	}
	prefix := cwLogGroupArn(name) + "|"
	deleted := false
	for _, configuration := range cwSyslogConfigurations.List() {
		key := cwSyslogKey(configuration.LogGroupArn, configuration.VpcEndpointId)
		if strings.HasPrefix(key, prefix) &&
			(req.VpcEndpointId == "" || strings.HasSuffix(key, "|"+req.VpcEndpointId)) {
			cwSyslogConfigurations.Delete(key)
			deleted = true
		}
	}
	if !deleted {
		AWSError(w, "ResourceNotFoundException", "The specified syslog configuration does not exist.", http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
