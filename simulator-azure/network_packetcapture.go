package main

// Azure Network Watcher packet captures.
//
// A capture's content is traffic recorded off a machine's interface, so every
// operation here is backed by a real capture session: Create opens a packet
// socket on the target virtual machine's interface, GetStatus reports what that
// session is really doing, and Stop finalises the recorded frames into the
// storage account the capture named — written through the same Blob data plane
// a client reads them back from. A session reported Running with no packets
// behind it would be fiction, which is why a target with no live interface is
// an error rather than an empty capture.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
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

// The documented defaults, applied when a request omits the field.
const (
	packetCaptureDefaultTotalBytes = 1073741824
	packetCaptureDefaultTimeLimit  = 18000
	// packetCaptureDefaultContainer is where a capture lands when the request
	// names a storage account but no path within it.
	packetCaptureDefaultContainer = "network-watcher-logs"
)

var (
	azurePacketCaptures sim.Store[AzurePacketCapture]

	// Live sessions are process-local: a capture is a running socket, and a
	// socket does not survive a restart. The stored resource records what the
	// session did, so a status read after a restart reports the recorded
	// outcome rather than claiming a session that no longer exists.
	azureCaptureMu       sync.Mutex
	azureCaptureSessions = map[string]*realexec.Capture{}
)

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
	return fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkWatchers/%s/packetCaptures/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "networkWatcherName"), sim.PathParam(r, "packetCaptureName"))
}

// packetCaptureTargetInterface resolves the target virtual machine to the
// interface a capture reads frames from: the machine's network interface, and
// the namespace holding the TAP device that carries its traffic.
//
// A machine with no live interface cannot be captured. Returning an empty
// capture instead would be indistinguishable from a real capture that saw no
// traffic, so this reports why it cannot proceed.
func packetCaptureTargetInterface(target string) (namespace, iface string, err error) {
	vm, ok := azureVMs.Get(target)
	if !ok {
		return "", "", fmt.Errorf("target virtual machine %q was not found", target)
	}
	if len(vm.Properties.NetworkProfile.NetworkInterfaces) == 0 {
		return "", "", fmt.Errorf("target virtual machine %q has no network interface to capture", target)
	}
	azureRealMu.Lock()
	defer azureRealMu.Unlock()
	for _, ref := range vm.Properties.NetworkProfile.NetworkInterfaces {
		if tap := azureRealVMNICs[ref.ID]; tap != nil {
			return tap.NetworkNamespace(), tap.TapName, nil
		}
	}
	return "", "", fmt.Errorf(
		"target virtual machine %q has no running interface: a capture records the traffic that crosses one, "+
			"so it cannot start against a machine that is not running on this host", target)
}

func packetCaptureFilters(filters []AzurePacketCaptureFilter) []realexec.CaptureFilter {
	out := make([]realexec.CaptureFilter, 0, len(filters))
	for _, f := range filters {
		out = append(out, realexec.CaptureFilter{
			Protocol:      f.Protocol,
			LocalAddress:  f.LocalIPAddress,
			LocalPort:     f.LocalPort,
			RemoteAddress: f.RemoteIPAddress,
			RemotePort:    f.RemotePort,
		})
	}
	return out
}

func handlePacketCaptureCreate(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	name := sim.PathParam(r, "packetCaptureName")

	var body struct {
		Properties AzurePacketCaptureProperties `json:"properties"`
	}
	if err := sim.ReadJSON(r, &body); err != nil {
		sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	props := body.Properties
	if props.Target == "" {
		sim.AzureErrorf(w, "MissingRequiredParameter", http.StatusBadRequest,
			"The parameter 'target' is required.")
		return
	}
	if props.StorageLocation.StorageId == "" && props.StorageLocation.FilePath == "" &&
		props.StorageLocation.LocalPath == "" {
		sim.AzureErrorf(w, "MissingRequiredParameter", http.StatusBadRequest,
			"The parameter 'storageLocation' is required.")
		return
	}
	if _, exists := azurePacketCaptures.Get(id); exists {
		sim.AzureErrorf(w, "PacketCaptureAlreadyExists", http.StatusConflict,
			"Packet capture %q already exists.", name)
		return
	}
	if props.TotalBytesPerSession == 0 {
		props.TotalBytesPerSession = packetCaptureDefaultTotalBytes
	}
	if props.TimeLimitInSeconds == 0 {
		props.TimeLimitInSeconds = packetCaptureDefaultTimeLimit
	}
	if props.TargetType == "" {
		props.TargetType = "AzureVM"
	}

	namespace, iface, err := packetCaptureTargetInterface(props.Target)
	if err != nil {
		sim.AzureErrorf(w, "PacketCaptureTargetNotReady", http.StatusBadRequest, "%v", err)
		return
	}

	session, err := realexec.StartCapture(realexec.CaptureSpec{
		NamespaceName:  namespace,
		InterfaceName:  iface,
		BytesPerPacket: int(props.BytesToCapturePerPacket),
		TotalBytes:     props.TotalBytesPerSession,
		TimeLimit:      time.Duration(props.TimeLimitInSeconds) * time.Second,
		Filters:        packetCaptureFilters(props.Filters),
	})
	if err != nil {
		sim.AzureErrorf(w, "PacketCaptureFailed", http.StatusBadRequest,
			"could not start the capture: %v", err)
		return
	}

	// storagePath is computed by the service: a request that names only an
	// account gets back where the recording will land, which is what a client
	// reads to know where to fetch it from.
	if props.StorageLocation.StoragePath == "" && props.StorageLocation.StorageId != "" {
		if account := storageAccountNameFromID(props.StorageLocation.StorageId); account != "" {
			props.StorageLocation.StoragePath = fmt.Sprintf(
				"https://%s.blob.core.windows.net/%s/%s.cap",
				account, packetCaptureDefaultContainer, strings.TrimPrefix(id, "/"))
		}
	}

	props.ProvisioningState = "Succeeded"
	capture := AzurePacketCapture{
		ID:               id,
		Name:             name,
		Etag:             azureNetworkEtag(),
		Properties:       props,
		CaptureStartTime: session.Status().StartedAt.Format(time.RFC3339),
		Status:           "Running",
	}
	azurePacketCaptures.Put(id, capture)
	azureCaptureMu.Lock()
	azureCaptureSessions[id] = session
	azureCaptureMu.Unlock()

	sim.WriteJSON(w, http.StatusCreated, packetCaptureResult(capture))
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
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", packetCaptureID(r))
		return
	}
	sim.WriteJSON(w, http.StatusOK, packetCaptureResult(capture))
}

func handlePacketCaptureList(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkWatchers/%s/packetCaptures/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		sim.PathParam(r, "networkWatcherName"))
	rows := azurePacketCaptures.Filter(func(c AzurePacketCapture) bool {
		return strings.HasPrefix(c.ID, prefix)
	})
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, packetCaptureResult(c))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// syncPacketCaptureStatus reconciles the stored resource with the live session,
// so a capture that hit its own time or size bound reports that rather than
// still claiming to be Running.
func syncPacketCaptureStatus(id string) AzurePacketCapture {
	capture, _ := azurePacketCaptures.Get(id)
	azureCaptureMu.Lock()
	session := azureCaptureSessions[id]
	azureCaptureMu.Unlock()
	if session == nil {
		return capture
	}
	status := session.Status()
	if status.Running {
		capture.Status = "Running"
	} else {
		capture.Status = "Stopped"
		capture.StopReason = string(status.StopReason)
		if status.Err != nil {
			capture.Status = "Error"
			capture.ErrorMessage = status.Err.Error()
		}
	}
	azurePacketCaptures.Put(id, capture)
	return capture
}

func handlePacketCaptureQueryStatus(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if _, ok := azurePacketCaptures.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", id)
		return
	}
	capture := syncPacketCaptureStatus(id)
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
	capture, ok := azurePacketCaptures.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", id)
		return
	}
	azureCaptureMu.Lock()
	session := azureCaptureSessions[id]
	azureCaptureMu.Unlock()
	if session != nil {
		if err := session.Stop(); err != nil {
			sim.AzureErrorf(w, "PacketCaptureFailed", http.StatusInternalServerError,
				"could not stop the capture: %v", err)
			return
		}
		if err := storePacketCapture(capture, session.Bytes()); err != nil {
			sim.AzureErrorf(w, "PacketCaptureStorageFailed", http.StatusBadRequest,
				"the capture stopped but could not be written to its storage location: %v", err)
			return
		}
		status := session.Status()
		capture.Status = "Stopped"
		capture.StopReason = string(status.StopReason)
		azurePacketCaptures.Put(id, capture)
	}
	w.WriteHeader(http.StatusOK)
}

func handlePacketCaptureDelete(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if _, ok := azurePacketCaptures.Get(id); !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	azureCaptureMu.Lock()
	session := azureCaptureSessions[id]
	delete(azureCaptureSessions, id)
	azureCaptureMu.Unlock()
	if session != nil {
		_ = session.Stop()
	}
	azurePacketCaptures.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}

// storePacketCapture writes the recorded frames where the capture said they
// should go. The bytes land in the storage account's Blob data plane, which is
// the same surface a client downloads them from — so the capture a caller
// retrieves is the capture that was recorded, not a second rendering of it.
func storePacketCapture(capture AzurePacketCapture, pcap []byte) error {
	location := capture.Properties.StorageLocation
	if location.StorageId == "" {
		// A capture with only a file path targets the machine's own disk,
		// which this host does not write into on the machine's behalf.
		return nil
	}
	account := storageAccountNameFromID(location.StorageId)
	if account == "" {
		return fmt.Errorf("storageId %q does not name a storage account", location.StorageId)
	}
	container, blobName := packetCapturePath(location.StoragePath, capture)
	now := time.Now().UTC().Format(http.TimeFormat)
	putBlobObject(BlobObject{
		Account:      account,
		Container:    container,
		Name:         blobName,
		Data:         pcap,
		ContentType:  "application/vnd.tcpdump.pcap",
		BlobType:     "BlockBlob",
		ETag:         azureNetworkEtag(),
		LastModified: now,
		CreationTime: now,
	})
	return nil
}

// storageAccountNameFromID reads the account name out of a storage account's
// resource id, which ends in .../storageAccounts/<name>.
func storageAccountNameFromID(id string) string {
	parts := strings.Split(strings.Trim(id, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "storageAccounts") {
			return parts[i+1]
		}
	}
	return ""
}

// packetCapturePath decides where in the account the capture lands. A request
// that supplies a storage path names the container and blob itself; one that
// does not gets the default container and a blob named for the capture, which
// is what Azure does when only an account is given.
func packetCapturePath(storagePath string, capture AzurePacketCapture) (container, blob string) {
	if storagePath != "" {
		trimmed := storagePath
		if idx := strings.Index(trimmed, "://"); idx >= 0 {
			trimmed = trimmed[idx+3:]
			if slash := strings.Index(trimmed, "/"); slash >= 0 {
				trimmed = trimmed[slash+1:]
			} else {
				trimmed = ""
			}
		}
		trimmed = strings.TrimPrefix(trimmed, "/")
		if container, blob, found := strings.Cut(trimmed, "/"); found && container != "" && blob != "" {
			return container, blob
		}
	}
	return packetCaptureDefaultContainer, strings.TrimPrefix(capture.ID, "/") + ".cap"
}
