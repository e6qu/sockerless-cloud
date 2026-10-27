package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Network Watcher, Azure's regional network diagnostics. A watcher owns flow
// logs and connection monitors, and its on-demand diagnostics
// (network_watcher_diagnostics.go) evaluate the same NSGs, rules and route
// tables the simulator programs onto interfaces, so a verdict read here is the
// verdict a packet gets.

type NetworkWatcher struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherProperties `json:"properties"`
}

type NetworkWatcherProperties struct {
	ProvisioningState string `json:"provisioningState,omitempty"`
}

type NetworkWatcherFlowLog struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherFlowLogProperties `json:"properties"`
}

type NetworkWatcherFlowLogProperties struct {
	TargetResourceID           string                       `json:"targetResourceId,omitempty"`
	TargetResourceGUID         string                       `json:"targetResourceGuid,omitempty"`
	StorageID                  string                       `json:"storageId,omitempty"`
	EnabledFilteringCriteria   string                       `json:"enabledFilteringCriteria,omitempty"`
	RecordTypes                string                       `json:"recordTypes,omitempty"`
	Enabled                    *bool                        `json:"enabled,omitempty"`
	RetentionPolicy            *NetworkWatcherRetention     `json:"retentionPolicy,omitempty"`
	Format                     *NetworkWatcherFlowLogFormat `json:"format,omitempty"`
	FlowAnalyticsConfiguration map[string]any               `json:"flowAnalyticsConfiguration,omitempty"`
	ProvisioningState          string                       `json:"provisioningState,omitempty"`
}

type NetworkWatcherRetention struct {
	Days    int32 `json:"days"`
	Enabled bool  `json:"enabled"`
}

type NetworkWatcherFlowLogFormat struct {
	Type    string `json:"type,omitempty"`
	Version int32  `json:"version,omitempty"`
}

type NetworkWatcherConnectionMonitor struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherConnectionMonitorProperties `json:"properties"`
}

// The test results are published through Azure Monitor, not this resource, so
// it carries the configuration and whether the monitor runs.
type NetworkWatcherConnectionMonitorProperties struct {
	Source                      map[string]any   `json:"source,omitempty"`
	Destination                 map[string]any   `json:"destination,omitempty"`
	AutoStart                   *bool            `json:"autoStart,omitempty"`
	MonitoringIntervalInSeconds *int32           `json:"monitoringIntervalInSeconds,omitempty"`
	Endpoints                   []map[string]any `json:"endpoints,omitempty"`
	TestConfigurations          []map[string]any `json:"testConfigurations,omitempty"`
	TestGroups                  []map[string]any `json:"testGroups,omitempty"`
	Outputs                     []map[string]any `json:"outputs,omitempty"`
	Notes                       string           `json:"notes,omitempty"`
	ProvisioningState           string           `json:"provisioningState,omitempty"`
	StartTime                   string           `json:"startTime,omitempty"`
	MonitoringStatus            string           `json:"monitoringStatus,omitempty"`
	ConnectionMonitorType       string           `json:"connectionMonitorType,omitempty"`
}

var (
	azureNetworkWatchers        sim.Store[NetworkWatcher]
	azureNetworkWatcherFlowLogs sim.Store[NetworkWatcherFlowLog]
	azureConnectionMonitors     sim.Store[NetworkWatcherConnectionMonitor]
)

const (
	azureNetworkWatcherType       = "Microsoft.Network/networkWatchers"
	azureNetworkWatcherFlowLogTyp = "Microsoft.Network/networkWatchers/flowLogs"
	azureConnectionMonitorType    = "Microsoft.Network/networkWatchers/connectionMonitors"
)

func registerNetworkWatchers(srv *sim.Server) {
	azureNetworkWatchers = sim.MakeStore[NetworkWatcher](srv.DB(), "network_watchers")
	azureNetworkWatcherFlowLogs = sim.MakeStore[NetworkWatcherFlowLog](srv.DB(), "network_watcher_flow_logs")
	azureConnectionMonitors = sim.MakeStore[NetworkWatcherConnectionMonitor](srv.DB(), "network_watcher_connection_monitors")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[NetworkWatcher]{
		collection:   "networkWatchers",
		nameParam:    "networkWatcherName",
		resourceType: azureNetworkWatcherType,
		store:        azureNetworkWatchers,
		// Microsoft.Network documents a completed watcher delete as 204, and its
		// client rejects 200.
		deleteStatus: http.StatusNoContent,
		header: func(nw *NetworkWatcher) *azureNetworkResourceHeader {
			return &nw.azureNetworkResourceHeader
		},
		provision: func(_ context.Context, nw *NetworkWatcher, _ *NetworkWatcher) error {
			nw.Properties.ProvisioningState = "Succeeded"
			return nil
		},
		afterDelete: deleteNetworkWatcherChildren,
	})

	registerNetworkWatcherFlowLogs(srv)
	registerNetworkWatcherConnectionMonitors(srv)
	registerNetworkWatcherDiagnostics(srv)
	registerNetworkWatcherPacketCaptures(srv)
}

// Azure Resource Manager deletes a resource's children with it.
func deleteNetworkWatcherChildren(_ context.Context, id string, _ NetworkWatcher) {
	for _, flowLog := range azureNetworkWatcherFlowLogs.Filter(func(f NetworkWatcherFlowLog) bool {
		return strings.HasPrefix(f.ID, id+"/flowLogs/")
	}) {
		azureNetworkWatcherFlowLogs.Delete(flowLog.ID)
	}
	for _, monitor := range azureConnectionMonitors.Filter(func(m NetworkWatcherConnectionMonitor) bool {
		return strings.HasPrefix(m.ID, id+"/connectionMonitors/")
	}) {
		azureConnectionMonitors.Delete(monitor.ID)
	}
}

func networkWatcherID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkWatchers", sim.PathParam(r, "networkWatcherName"))
}

func requireNetworkWatcher(w http.ResponseWriter, r *http.Request) (NetworkWatcher, bool) {
	nw, ok := azureNetworkWatchers.Get(networkWatcherID(r))
	if !ok {
		azureNetworkResourceNotFound(w, azureNetworkWatcherType,
			sim.PathParam(r, "networkWatcherName"), sim.PathParam(r, "resourceGroupName"))
		return NetworkWatcher{}, false
	}
	return nw, true
}

func registerNetworkWatcherFlowLogs(srv *sim.Server) {
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}/flowLogs"
	flowLogID := func(r *http.Request) string {
		return armChildID(networkWatcherID(r), "flowLogs", sim.PathParam(r, "flowLogName"))
	}

	srv.HandleFunc("PUT "+base+"/{flowLogName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		var req NetworkWatcherFlowLog
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.TargetResourceID == "" || req.Properties.StorageID == "" {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: a flow log requires both targetResourceId and storageId.")
			return
		}
		if !networkWatcherFlowLogTargetExists(req.Properties.TargetResourceID) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource %q was not found.", req.Properties.TargetResourceID)
			return
		}
		flowLog := networkWatcherStoreFlowLog(flowLogID(r), sim.PathParam(r, "flowLogName"), req)
		sim.WriteJSON(w, http.StatusOK, flowLog)
	})

	srv.HandleFunc("GET "+base+"/{flowLogName}", func(w http.ResponseWriter, r *http.Request) {
		flowLog, ok := azureNetworkWatcherFlowLogs.Get(flowLogID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureNetworkWatcherFlowLogTyp,
				sim.PathParam(r, "flowLogName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, flowLog)
	})

	srv.HandleFunc("PATCH "+base+"/{flowLogName}", func(w http.ResponseWriter, r *http.Request) {
		id := flowLogID(r)
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureNetworkWatcherFlowLogs.Update(id, func(f *NetworkWatcherFlowLog) { f.Tags = set })
		}) {
			return
		}
		flowLog, _ := azureNetworkWatcherFlowLogs.Get(id)
		sim.WriteJSON(w, http.StatusOK, flowLog)
	})

	srv.HandleFunc("DELETE "+base+"/{flowLogName}", func(w http.ResponseWriter, r *http.Request) {
		azureNetworkWatcherFlowLogs.Delete(flowLogID(r))
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		prefix := networkWatcherID(r) + "/flowLogs/"
		azureWriteList(w, azureNetworkWatcherFlowLogs.Filter(func(f NetworkWatcherFlowLog) bool {
			return strings.HasPrefix(f.ID, prefix)
		}))
	})
}

// The target's resource GUID, the default record shape, and the terminal
// provisioning state.
func networkWatcherStoreFlowLog(id, name string, req NetworkWatcherFlowLog) NetworkWatcherFlowLog {
	flowLog := req
	flowLog.ID = id
	flowLog.Name = name
	flowLog.Type = azureNetworkWatcherFlowLogTyp
	flowLog.Etag = azureNetworkEtag()
	if flowLog.Properties.Enabled == nil {
		enabled := true
		flowLog.Properties.Enabled = &enabled
	}
	if flowLog.Properties.Format == nil {
		flowLog.Properties.Format = &NetworkWatcherFlowLogFormat{Type: "JSON", Version: 2}
	}
	if flowLog.Properties.RetentionPolicy == nil {
		flowLog.Properties.RetentionPolicy = &NetworkWatcherRetention{}
	}
	flowLog.Properties.ProvisioningState = "Succeeded"
	azureNetworkWatcherFlowLogs.Put(id, flowLog)
	return flowLog
}

// Azure collects flows for NSGs, virtual networks, subnets and interfaces, and
// refuses a flow log whose target does not exist.
func networkWatcherFlowLogTargetExists(targetID string) bool {
	if azureNSGs != nil {
		if _, ok := azureNSGs.Get(targetID); ok {
			return true
		}
	}
	if azureVnets != nil {
		if _, ok := azureVnets.Get(targetID); ok {
			return true
		}
	}
	if azureSubnets != nil {
		if _, ok := azureSubnets.Get(targetID); ok {
			return true
		}
	}
	if azureNICs != nil {
		if _, ok := azureNICs.Get(targetID); ok {
			return true
		}
	}
	return false
}

// configureFlowLog and queryFlowLogStatus address a target rather than a named
// flow log, and use the same records the flow log resource serves.
func networkWatcherFlowLogForTarget(watcherID, targetID string) (NetworkWatcherFlowLog, bool) {
	prefix := watcherID + "/flowLogs/"
	for _, flowLog := range azureNetworkWatcherFlowLogs.Filter(func(f NetworkWatcherFlowLog) bool {
		return strings.HasPrefix(f.ID, prefix)
	}) {
		if strings.EqualFold(flowLog.Properties.TargetResourceID, targetID) {
			return flowLog, true
		}
	}
	return NetworkWatcherFlowLog{}, false
}

// A monitor's results are published through Azure Monitor, so this surface
// owns the configuration and the running lifecycle.
func registerNetworkWatcherConnectionMonitors(srv *sim.Server) {
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}/connectionMonitors"
	monitorID := func(r *http.Request) string {
		return armChildID(networkWatcherID(r), "connectionMonitors", sim.PathParam(r, "connectionMonitorName"))
	}

	srv.HandleFunc("PUT "+base+"/{connectionMonitorName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		var req NetworkWatcherConnectionMonitor
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Properties.Endpoints) == 0 && req.Properties.Source == nil {
			AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: a connection monitor requires either endpoints or a source.")
			return
		}
		id := monitorID(r)
		monitor := req
		monitor.ID = id
		monitor.Name = sim.PathParam(r, "connectionMonitorName")
		monitor.Type = azureConnectionMonitorType
		monitor.Etag = azureNetworkEtag()
		monitor.Properties.ProvisioningState = "Succeeded"
		// Endpoints and test groups make a MultiEndpoint monitor; a single source and
		// destination the older shape. The configuration decides, not the caller.
		monitor.Properties.ConnectionMonitorType = "MultiEndpoint"
		if len(req.Properties.Endpoints) == 0 {
			monitor.Properties.ConnectionMonitorType = "SingleSourceDestination"
		}
		// The monitor starts unless asked not to, and records when it did.
		running := req.Properties.AutoStart == nil || *req.Properties.AutoStart
		if existing, ok := azureConnectionMonitors.Get(id); ok {
			monitor.Properties.StartTime = existing.Properties.StartTime
			running = existing.Properties.MonitoringStatus != "Stopped"
		}
		if running {
			monitor.Properties.MonitoringStatus = "Running"
			if monitor.Properties.StartTime == "" {
				monitor.Properties.StartTime = time.Now().UTC().Format(time.RFC3339Nano)
			}
		} else {
			monitor.Properties.MonitoringStatus = "Stopped"
		}
		azureConnectionMonitors.Put(id, monitor)
		sim.WriteJSON(w, http.StatusOK, monitor)
	})

	srv.HandleFunc("GET "+base+"/{connectionMonitorName}", func(w http.ResponseWriter, r *http.Request) {
		monitor, ok := azureConnectionMonitors.Get(monitorID(r))
		if !ok {
			azureNetworkResourceNotFound(w, azureConnectionMonitorType,
				sim.PathParam(r, "connectionMonitorName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, monitor)
	})

	srv.HandleFunc("PATCH "+base+"/{connectionMonitorName}", func(w http.ResponseWriter, r *http.Request) {
		id := monitorID(r)
		if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
			return azureConnectionMonitors.Update(id, func(m *NetworkWatcherConnectionMonitor) { m.Tags = set })
		}) {
			return
		}
		monitor, _ := azureConnectionMonitors.Get(id)
		sim.WriteJSON(w, http.StatusOK, monitor)
	})

	srv.HandleFunc("POST "+base+"/{connectionMonitorName}/stop", func(w http.ResponseWriter, r *http.Request) {
		if !azureConnectionMonitors.Update(monitorID(r), func(m *NetworkWatcherConnectionMonitor) {
			m.Properties.MonitoringStatus = "Stopped"
			m.Etag = azureNetworkEtag()
		}) {
			azureNetworkResourceNotFound(w, azureConnectionMonitorType,
				sim.PathParam(r, "connectionMonitorName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("DELETE "+base+"/{connectionMonitorName}", func(w http.ResponseWriter, r *http.Request) {
		azureConnectionMonitors.Delete(monitorID(r))
		w.WriteHeader(http.StatusNoContent)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		prefix := networkWatcherID(r) + "/connectionMonitors/"
		azureWriteList(w, azureConnectionMonitors.Filter(func(m NetworkWatcherConnectionMonitor) bool {
			return strings.HasPrefix(m.ID, prefix)
		}))
	})
}
