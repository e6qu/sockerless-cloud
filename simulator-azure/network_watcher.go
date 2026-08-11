package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Network Watcher (Microsoft.Network/networkWatchers) is Azure's regional
// network diagnostics service. The watcher resource itself is a per-region
// anchor that owns two kinds of child: the flow logs and connection monitors an
// operator configures, and the on-demand diagnostics
// (network_watcher_diagnostics.go) that answer questions about the network as
// it is right now.
//
// The diagnostics are the part that has to be real, and they are: IP flow
// verify, next hop, the security group view and the network configuration
// diagnostic all evaluate the SAME network security groups, security rules and
// route tables the simulator programs onto the interfaces, so a verdict a
// caller reads here is the verdict a packet gets. Nothing is answered from a
// canned table.

// NetworkWatcher mirrors Microsoft.Network/networkWatchers.
type NetworkWatcher struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherProperties `json:"properties"`
}

// NetworkWatcherProperties holds the watcher's read-only state.
type NetworkWatcherProperties struct {
	ProvisioningState string `json:"provisioningState,omitempty"`
}

// NetworkWatcherFlowLog mirrors Microsoft.Network/networkWatchers/flowLogs — the
// flow log configuration of one target resource.
type NetworkWatcherFlowLog struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherFlowLogProperties `json:"properties"`
}

// NetworkWatcherFlowLogProperties holds where a target's flows are written and
// in what shape.
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

// NetworkWatcherRetention is a flow log's retention policy.
type NetworkWatcherRetention struct {
	Days    int32 `json:"days"`
	Enabled bool  `json:"enabled"`
}

// NetworkWatcherFlowLogFormat is the file format flows are written in.
type NetworkWatcherFlowLogFormat struct {
	Type    string `json:"type,omitempty"`
	Version int32  `json:"version,omitempty"`
}

// NetworkWatcherConnectionMonitor mirrors
// Microsoft.Network/networkWatchers/connectionMonitors.
type NetworkWatcherConnectionMonitor struct {
	azureNetworkResourceHeader
	Properties NetworkWatcherConnectionMonitorProperties `json:"properties"`
}

// NetworkWatcherConnectionMonitorProperties holds the monitor's endpoints, test
// configuration and lifecycle state. The test results themselves are published
// through Azure Monitor rather than through this resource, so what the resource
// carries — and what this simulator therefore carries — is the configuration
// and whether the monitor is running.
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
		// Microsoft.Network documents a completed network watcher delete as
		// 204; its client rejects the 200 the provider's other resources use.
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

// deleteNetworkWatcherChildren removes the flow logs and connection monitors a
// deleted watcher owned; ARM deletes a resource's children with it.
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

// networkWatcherID composes the watcher id a request addresses.
func networkWatcherID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkWatchers/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "networkWatcherName"))
}

// requireNetworkWatcher resolves the watcher a child or diagnostic request is
// addressed to, writing ARM's not-found error when it does not exist.
func requireNetworkWatcher(w http.ResponseWriter, r *http.Request) (NetworkWatcher, bool) {
	nw, ok := azureNetworkWatchers.Get(networkWatcherID(r))
	if !ok {
		azureNetworkResourceNotFound(w, azureNetworkWatcherType,
			sim.PathParam(r, "networkWatcherName"), sim.PathParam(r, "resourceGroupName"))
		return NetworkWatcher{}, false
	}
	return nw, true
}

// registerNetworkWatcherFlowLogs mounts the flow log resource: the record of
// which target's flows go to which storage account, in which format, and for
// how long they are kept.
func registerNetworkWatcherFlowLogs(srv *sim.Server) {
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}/flowLogs"
	flowLogID := func(r *http.Request) string {
		return azureNetworkChildID(networkWatcherID(r), "flowLogs", sim.PathParam(r, "flowLogName"))
	}

	srv.HandleFunc("PUT "+base+"/{flowLogName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		var req NetworkWatcherFlowLog
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.TargetResourceID == "" || req.Properties.StorageID == "" {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
				"The request format was unexpected: a flow log requires both targetResourceId and storageId.")
			return
		}
		if !networkWatcherFlowLogTargetExists(req.Properties.TargetResourceID) {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

// networkWatcherStoreFlowLog writes a flow log record with the read-only state
// the resource provider computes: the target's resource GUID resolved from the
// target itself, the default record shape, and the terminal provisioning state.
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

// networkWatcherFlowLogTargetExists reports whether a flow log's target is a
// resource this simulator holds. Azure collects flows for network security
// groups, virtual networks, subnets and network interfaces, and refuses a flow
// log whose target does not exist.
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

// networkWatcherFlowLogForTarget finds the flow log a watcher holds for one
// target resource. The configureFlowLog and queryFlowLogStatus operations
// address a target rather than a flow log by name, and they read and write the
// very same records the flow log resource serves.
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

// registerNetworkWatcherConnectionMonitors mounts the connection monitor
// resource. A monitor is a standing instruction to test connectivity between
// its endpoints; the results it produces are published through Azure Monitor,
// so what this surface owns is the configuration and the monitor's own
// lifecycle — running until it is stopped.
func registerNetworkWatcherConnectionMonitors(srv *sim.Server) {
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}/connectionMonitors"
	monitorID := func(r *http.Request) string {
		return azureNetworkChildID(networkWatcherID(r), "connectionMonitors", sim.PathParam(r, "connectionMonitorName"))
	}

	srv.HandleFunc("PUT "+base+"/{connectionMonitorName}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requireNetworkWatcher(w, r); !ok {
			return
		}
		var req NetworkWatcherConnectionMonitor
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Properties.Endpoints) == 0 && req.Properties.Source == nil {
			sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
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
		// A monitor built from endpoints and test groups is the multi-endpoint
		// shape; one built from a single source and destination is the older
		// single-source shape. The type follows from the configuration rather
		// than from anything the caller declares.
		monitor.Properties.ConnectionMonitorType = "MultiEndpoint"
		if len(req.Properties.Endpoints) == 0 {
			monitor.Properties.ConnectionMonitorType = "SingleSourceDestination"
		}
		// The monitor starts unless the request asked it not to, and a monitor
		// that has started carries the time it did.
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
