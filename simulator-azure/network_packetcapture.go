package main

// Azure Network Watcher packet captures.
//
// A capture's content is traffic recorded off a machine's interface by the
// Network Watcher agent inside the machine. No backend runs a machine, so a
// create validates its request and then reports that the capture cannot
// start; a capture reported Running with no packets behind it would be
// fiction. The stored resource and its status, stop and delete operations
// serve whatever captures the store holds.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AzurePacketCaptureFilter mirrors the PacketCaptureFilter definition.
type AzurePacketCaptureFilter struct {
	Protocol        string `json:"protocol,omitempty"`
	LocalIPAddress  string `json:"localIPAddress,omitempty"`
	LocalPort       string `json:"localPort,omitempty"`
	RemoteIPAddress string `json:"remoteIPAddress,omitempty"`
	RemotePort      string `json:"remotePort,omitempty"`
}

// AzurePacketCaptureStorageLocation mirrors PacketCaptureStorageLocation.
type AzurePacketCaptureStorageLocation struct {
	StorageId   string `json:"storageId,omitempty"`
	StoragePath string `json:"storagePath,omitempty"`
	FilePath    string `json:"filePath,omitempty"`
	LocalPath   string `json:"localPath,omitempty"`
}

// AzurePacketCaptureProperties mirrors PacketCaptureResultProperties, which the
// swagger composes from PacketCaptureParameters plus a provisioning state.
type AzurePacketCaptureProperties struct {
	Target                  string                            `json:"target,omitempty"`
	TargetType              string                            `json:"targetType,omitempty"`
	BytesToCapturePerPacket int64                             `json:"bytesToCapturePerPacket"`
	TotalBytesPerSession    int64                             `json:"totalBytesPerSession"`
	TimeLimitInSeconds      int                               `json:"timeLimitInSeconds"`
	StorageLocation         AzurePacketCaptureStorageLocation `json:"storageLocation"`
	Filters                 []AzurePacketCaptureFilter        `json:"filters,omitempty"`
	ContinuousCapture       *bool                             `json:"continuousCapture,omitempty"`
	Scope                   map[string]any                    `json:"scope,omitempty"`
	CaptureSettings         map[string]any                    `json:"captureSettings,omitempty"`
	ProvisioningState       string                            `json:"provisioningState,omitempty"`
}

// AzurePacketCapture is the stored capture resource.
type AzurePacketCapture struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Etag       string                       `json:"etag,omitempty"`
	Properties AzurePacketCaptureProperties `json:"properties"`

	// CaptureStartTime, Status and StopReason record the session's outcome so
	// a status read after the process restarted still reports truthfully
	// rather than inventing a Running session with nothing behind it.
	CaptureStartTime string `json:"captureStartTime,omitempty"`
	Status           string `json:"packetCaptureStatus,omitempty"`
	StopReason       string `json:"stopReason,omitempty"`
	ErrorMessage     string `json:"packetCaptureError,omitempty"`
}

var azurePacketCaptures sim.Store[AzurePacketCapture]

func registerNetworkWatcherPacketCaptures(srv *sim.Server) {
	azurePacketCaptures = sim.MakeStore[AzurePacketCapture](srv.DB(), "network_packet_captures")
	base := azureNetworkArmBase() + "/networkWatchers/{networkWatcherName}/packetCaptures"

	srv.HandleFunc("PUT "+base+"/{packetCaptureName}", handlePacketCaptureCreate)
	srv.HandleFunc("GET "+base+"/{packetCaptureName}", handlePacketCaptureGet)
	srv.HandleFunc("DELETE "+base+"/{packetCaptureName}", handlePacketCaptureDelete)
	srv.HandleFunc("POST "+base+"/{packetCaptureName}/stop", handlePacketCaptureStop)
	srv.HandleFunc("POST "+base+"/{packetCaptureName}/queryStatus", handlePacketCaptureQueryStatus)
	srv.HandleFunc("GET "+base, handlePacketCaptureList)
}

func packetCaptureID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkWatchers", sim.PathParam(r, "networkWatcherName"), "packetCaptures", sim.PathParam(r, "packetCaptureName"))
}

// packetCaptureTarget names why a capture of the target cannot start: the
// virtual machine is missing, has no interface, or has no interface a capture
// can tap on this host. Returning an empty capture instead would be
// indistinguishable from a real capture that saw no traffic.
func packetCaptureTarget(target string) error {
	vm, ok := azureVMs.Get(target)
	if !ok {
		return fmt.Errorf("target virtual machine %q was not found", target)
	}
	if len(vm.Properties.NetworkProfile.NetworkInterfaces) == 0 {
		return fmt.Errorf("target virtual machine %q has no network interface to capture", target)
	}
	return fmt.Errorf(
		"target virtual machine %q has no interface a capture can tap: a capture records the traffic that "+
			"crosses one, and the container backend running the machine exposes no tap", target)
}

func handlePacketCaptureCreate(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	name := sim.PathParam(r, "packetCaptureName")

	var body struct {
		Properties AzurePacketCaptureProperties `json:"properties"`
	}
	if err := sim.ReadJSON(r, &body); err != nil {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := body.Properties
	if props.Target == "" {
		AzureErrorf(w, "MissingRequiredParameter", http.StatusBadRequest,
			"The parameter 'target' is required.")
		return
	}
	if props.StorageLocation.StorageId == "" && props.StorageLocation.FilePath == "" &&
		props.StorageLocation.LocalPath == "" {
		AzureErrorf(w, "MissingRequiredParameter", http.StatusBadRequest,
			"The parameter 'storageLocation' is required.")
		return
	}
	if _, exists := azurePacketCaptures.Get(id); exists {
		AzureErrorf(w, "PacketCaptureAlreadyExists", http.StatusConflict,
			"Packet capture %q already exists.", name)
		return
	}
	AzureErrorf(w, "PacketCaptureTargetNotReady", http.StatusBadRequest, "%v", packetCaptureTarget(props.Target))
}

// packetCaptureResult renders the resource shape a client reads back, which
// carries the parameters and the provisioning state but not the session's
// runtime status — that belongs to queryStatus.
func packetCaptureResult(c AzurePacketCapture) map[string]any {
	return map[string]any{
		"id":         c.ID,
		"name":       c.Name,
		"etag":       c.Etag,
		"properties": c.Properties,
	}
}

func handlePacketCaptureGet(w http.ResponseWriter, r *http.Request) {
	capture, ok := azurePacketCaptures.Get(packetCaptureID(r))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", packetCaptureID(r))
		return
	}
	sim.WriteJSON(w, http.StatusOK, packetCaptureResult(capture))
}

func handlePacketCaptureList(w http.ResponseWriter, r *http.Request) {
	prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", "networkWatchers", sim.PathParam(r, "networkWatcherName"), "packetCaptures") + "/"
	rows := azurePacketCaptures.Filter(func(c AzurePacketCapture) bool {
		return strings.HasPrefix(c.ID, prefix)
	})
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, packetCaptureResult(c))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handlePacketCaptureQueryStatus(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	capture, ok := azurePacketCaptures.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", id)
		return
	}
	body := map[string]any{
		"id":                  capture.ID,
		"name":                capture.Name,
		"captureStartTime":    capture.CaptureStartTime,
		"packetCaptureStatus": capture.Status,
	}
	if capture.StopReason != "" {
		body["stopReason"] = capture.StopReason
	}
	if capture.ErrorMessage != "" {
		body["packetCaptureError"] = []string{capture.ErrorMessage}
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

func handlePacketCaptureStop(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if !azurePacketCaptures.Update(id, func(capture *AzurePacketCapture) {
		if capture.Status == "Running" {
			capture.Status = "Stopped"
			capture.StopReason = "Requested"
		}
	}) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", id)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handlePacketCaptureDelete(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if _, ok := azurePacketCaptures.Get(id); !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	azurePacketCaptures.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}
