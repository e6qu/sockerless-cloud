package main

// Azure Network Watcher packet captures. The Network Watcher agent extension
// records a capture inside the machine: here the agent is pcapagent, placed in
// the machine's network namespace when a session starts, reading every frame
// that crosses the machine's interfaces. When the session ends, Network
// Watcher moves the file to the storage account with a SAS signed by the
// account's key, to the machine's local path, or to both.

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/simulator-azure/pcapagent/filter"
)

type AzurePacketCaptureStorageLocation struct {
	StorageId   string `json:"storageId,omitempty"`
	StoragePath string `json:"storagePath,omitempty"`
	FilePath    string `json:"filePath,omitempty"`
	LocalPath   string `json:"localPath,omitempty"`
}

// PacketCaptureResultProperties: PacketCaptureParameters plus a provisioning
// state.
type AzurePacketCaptureProperties struct {
	Target                  string                            `json:"target,omitempty"`
	TargetType              string                            `json:"targetType,omitempty"`
	BytesToCapturePerPacket *int64                            `json:"bytesToCapturePerPacket,omitempty"`
	TotalBytesPerSession    *int64                            `json:"totalBytesPerSession,omitempty"`
	TimeLimitInSeconds      *int64                            `json:"timeLimitInSeconds,omitempty"`
	StorageLocation         AzurePacketCaptureStorageLocation `json:"storageLocation"`
	Filters                 []filter.Spec                     `json:"filters"`
	ContinuousCapture       *bool                             `json:"continuousCapture,omitempty"`
	Scope                   map[string]any                    `json:"scope,omitempty"`
	CaptureSettings         map[string]any                    `json:"captureSettings,omitempty"`
	ProvisioningState       string                            `json:"provisioningState,omitempty"`
}

type AzurePacketCapture struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	Etag       string                       `json:"etag,omitempty"`
	Properties AzurePacketCaptureProperties `json:"properties"`

	CaptureStartTime string   `json:"captureStartTime,omitempty"`
	Status           string   `json:"packetCaptureStatus,omitempty"`
	StopReason       string   `json:"stopReason,omitempty"`
	Errors           []string `json:"packetCaptureError,omitempty"`
}

var azurePacketCaptures sim.Store[AzurePacketCapture]

// The defaults PacketCaptureParameters declares.
const (
	packetCaptureDefaultTotalBytes = 1073741824
	packetCaptureDefaultTimeLimit  = 18000
	packetCaptureMaxTimeLimit      = 18000
	packetCaptureMaxBytes          = 4294967295
)

// The Network Watcher agent VM extension a capture needs, by publisher and
// type.
const (
	networkWatcherAgentPublisher = "Microsoft.Azure.NetworkWatcher"
	networkWatcherAgentLinux     = "NetworkWatcherAgentLinux"
)

// Linux machines keep local capture files under this directory.
const packetCaptureLinuxDir = "/var/captures"

// packetCaptureContainer is the blob container Network Watcher writes
// captures to in a storage account.
const packetCaptureContainer = "network-watcher-logs"

func registerNetworkWatcherPacketCaptures(srv *sim.Server) {
	azurePacketCaptures = sim.MakeStore[AzurePacketCapture](srv.DB(), "network_packet_captures")
	// The agent of a session running when the simulator stopped stopped with
	// it.
	for _, c := range azurePacketCaptures.Filter(func(c AzurePacketCapture) bool { return c.Status == "Running" }) {
		azurePacketCaptures.Update(c.ID, func(capture *AzurePacketCapture) {
			capture.Status = "Error"
			capture.Errors = []string{"AgentStopped"}
		})
	}
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

// packetCaptureSession is a running capture's agent.
type packetCaptureSession struct {
	handle placement.Handle
	// capturing closes once the agent's socket receives.
	capturing chan struct{}
	// ended carries the reason the agent printed when it stopped capturing.
	ended chan string
	// finished closes once the file has been moved and the status recorded.
	finished chan struct{}
}

var packetCaptureSessions sync.Map

//go:embed pcapagent/filter/*.go pcapagent/cmd/pcapagent/*.go
var pcapAgentSource embed.FS

const pcapAgentGoImage = "mirror.gcr.io/library/golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18"

const pcapAgentDockerfile = `FROM ` + pcapAgentGoImage + ` AS build
WORKDIR /src
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/pcapagent ./pcapagent/cmd/pcapagent && mkdir /out/captures

FROM scratch
COPY --from=build /out/pcapagent /pcapagent
COPY --from=build /out/captures /captures
ENTRYPOINT ["/pcapagent"]
`

var pcapAgentBuild = sim.EngineBuild{
	Name:  "sockerless-azure-pcapagent",
	Bases: []string{pcapAgentGoImage},
	Context: func(string) (map[string][]byte, error) {
		files := map[string][]byte{
			"Dockerfile": []byte(pcapAgentDockerfile),
			"go.mod":     []byte("module github.com/e6qu/sockerless-cloud/simulator-azure\n\ngo 1.26\n"),
		}
		err := fs.WalkDir(pcapAgentSource, ".", func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(name, "_test.go") {
				return err
			}
			data, err := pcapAgentSource.ReadFile(name)
			files[name] = data
			return err
		})
		return files, err
	},
}

// packetCaptureFault is a create the service refuses.
type packetCaptureFault struct {
	code    string
	status  int
	message string
}

func badPacketCapture(format string, args ...any) *packetCaptureFault {
	return &packetCaptureFault{code: "InvalidRequestFormat", status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

func packetCaptureTargetNotReady(format string, args ...any) *packetCaptureFault {
	return &packetCaptureFault{code: "PacketCaptureTargetNotReady", status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

// packetCaptureNormalize fills the declared defaults and checks the request
// against PacketCaptureParameters.
func packetCaptureNormalize(props *AzurePacketCaptureProperties) *packetCaptureFault {
	if props.Target == "" {
		return badPacketCapture("The parameter 'target' is required.")
	}
	if props.TargetType == "" {
		props.TargetType = "AzureVM"
	}
	if !strings.EqualFold(props.TargetType, "AzureVM") {
		return badPacketCapture("targetType %q is not served: only AzureVM targets run here.", props.TargetType)
	}
	if props.ContinuousCapture != nil && *props.ContinuousCapture {
		return badPacketCapture("continuousCapture is not served: the names of a continuous capture's rotated files are not documented.")
	}
	zero := int64(0)
	if props.BytesToCapturePerPacket == nil {
		props.BytesToCapturePerPacket = &zero
	}
	if props.TotalBytesPerSession == nil || *props.TotalBytesPerSession == 0 {
		v := int64(packetCaptureDefaultTotalBytes)
		props.TotalBytesPerSession = &v
	}
	if props.TimeLimitInSeconds == nil || *props.TimeLimitInSeconds == 0 {
		v := int64(packetCaptureDefaultTimeLimit)
		props.TimeLimitInSeconds = &v
	}
	switch {
	case *props.BytesToCapturePerPacket < 0 || *props.BytesToCapturePerPacket > packetCaptureMaxBytes:
		return badPacketCapture("bytesToCapturePerPacket must be between 0 and %d.", int64(packetCaptureMaxBytes))
	case *props.TotalBytesPerSession < 0 || *props.TotalBytesPerSession > packetCaptureMaxBytes:
		return badPacketCapture("totalBytesPerSession must be between 0 and %d.", int64(packetCaptureMaxBytes))
	case *props.TimeLimitInSeconds < 0 || *props.TimeLimitInSeconds > packetCaptureMaxTimeLimit:
		return badPacketCapture("timeLimitInSeconds must be between 0 and %d.", packetCaptureMaxTimeLimit)
	}
	if props.Filters == nil {
		props.Filters = []filter.Spec{}
	}
	if _, err := filter.Parse(props.Filters); err != nil {
		return badPacketCapture("%v", err)
	}
	loc := props.StorageLocation
	if loc.StorageId == "" && loc.FilePath == "" && loc.LocalPath == "" {
		return badPacketCapture("storageLocation needs a storageId, a filePath or a localPath.")
	}
	if loc.FilePath != "" && props.ContinuousCapture != nil {
		return badPacketCapture("filePath is invalid when continuousCapture is provided; use localPath.")
	}
	for _, path := range []string{loc.FilePath, loc.LocalPath} {
		if path == "" {
			continue
		}
		if !strings.HasPrefix(path, packetCaptureLinuxDir+"/") || !strings.HasSuffix(path, ".cap") {
			return badPacketCapture("The local path %q must start with %s and name a *.cap file.", path, packetCaptureLinuxDir)
		}
	}
	return nil
}

// packetCaptureLocalPath is where the file goes on the machine, if anywhere.
func packetCaptureLocalPath(loc AzurePacketCaptureStorageLocation) string {
	if loc.LocalPath != "" {
		return loc.LocalPath
	}
	return loc.FilePath
}

// armIDSegment returns the segment after key in an Azure Resource Manager ID.
func armIDSegment(id, key string) string {
	parts := strings.Split(strings.Trim(id, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], key) {
			return parts[i+1]
		}
	}
	return ""
}

// packetCaptureStorageTarget resolves where in the account the file goes: the
// storagePath named, or the path Network Watcher writes to by default,
// network-watcher-logs/subscriptions/{subscriptionId}/resourcegroups/{storageAccountResourceGroup}/providers/microsoft.compute/virtualmachines/{virtualMachineName}/{year}/{month}/{day}/packetcapture_{UTCcreationTime}.cap.
func packetCaptureStorageTarget(r *http.Request, loc *AzurePacketCaptureStorageLocation, vm VirtualMachine, started time.Time) (account, container, blob string, fault *packetCaptureFault) {
	account = armIDSegment(loc.StorageId, "storageAccounts")
	if azStorageAccounts == nil {
		return "", "", "", badPacketCapture("The storage account %q was not found.", loc.StorageId)
	}
	stored, ok := azStorageAccounts.Get(loc.StorageId)
	if !ok || account == "" {
		return "", "", "", badPacketCapture("The storage account %q was not found.", loc.StorageId)
	}
	if loc.StoragePath != "" {
		u, err := url.Parse(loc.StoragePath)
		if err != nil || u.Path == "" {
			return "", "", "", badPacketCapture("storagePath %q is not a well-formed URI.", loc.StoragePath)
		}
		parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", badPacketCapture("storagePath %q names no container and blob.", loc.StoragePath)
		}
		return stored.Name, parts[0], parts[1], nil
	}
	blob = strings.ToLower(fmt.Sprintf("subscriptions/%s/resourcegroups/%s/providers/microsoft.compute/virtualmachines/%s",
		armIDSegment(vm.ID, "subscriptions"), armIDSegment(loc.StorageId, "resourceGroups"), vm.Name)) +
		fmt.Sprintf("/%s/packetcapture_%s_%03d.cap", started.Format("2006/01/02"), started.Format("15_04_05"), started.Nanosecond()/int(time.Millisecond))
	loc.StoragePath = azureStorageEndpointURL(r, stored.Name, "blob") + packetCaptureContainer + "/" + blob
	return stored.Name, packetCaptureContainer, blob, nil
}

// packetCaptureAgentInstalled reports whether the machine runs the Network
// Watcher agent extension.
func packetCaptureAgentInstalled(vm VirtualMachine) bool {
	prefix := strings.ToLower(vm.ID + "/extensions/")
	return len(azureVMExtensions.Filter(func(e VirtualMachineExtension) bool {
		return strings.HasPrefix(strings.ToLower(e.ID), prefix) &&
			strings.EqualFold(e.Properties.Publisher, networkWatcherAgentPublisher) &&
			strings.EqualFold(e.Properties.Type, networkWatcherAgentLinux) &&
			e.Properties.ProvisioningState == "Succeeded"
	})) > 0
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
	writeFault := func(f *packetCaptureFault) { AzureError(w, f.code, f.message, f.status) }
	if f := packetCaptureNormalize(&props); f != nil {
		writeFault(f)
		return
	}
	if _, exists := azurePacketCaptures.Get(id); exists {
		AzureErrorf(w, "PacketCaptureAlreadyExists", http.StatusConflict,
			"Packet capture %q already exists.", name)
		return
	}
	vm, ok := azureVMs.Get(props.Target)
	if !ok {
		writeFault(packetCaptureTargetNotReady("target virtual machine %q was not found", props.Target))
		return
	}
	if !packetCaptureAgentInstalled(vm) {
		writeFault(packetCaptureTargetNotReady("target virtual machine %q does not run the %s/%s extension packet capture needs",
			props.Target, networkWatcherAgentPublisher, networkWatcherAgentLinux))
		return
	}
	machine, running := azureVMHandle(vm.ID)
	if !running {
		writeFault(packetCaptureTargetNotReady("target virtual machine %q is not running", props.Target))
		return
	}
	started := time.Now().UTC()
	var account, container, blob string
	if props.StorageLocation.StorageId != "" {
		var f *packetCaptureFault
		if account, container, blob, f = packetCaptureStorageTarget(r, &props.StorageLocation, vm, started); f != nil {
			writeFault(f)
			return
		}
	}

	session, err := startPacketCaptureAgent(r.Context(), machine, props)
	if err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "start the capture agent: %v", err)
		return
	}
	props.ProvisioningState = "Succeeded"
	capture := AzurePacketCapture{
		ID:               id,
		Name:             name,
		Etag:             fmt.Sprintf(`W/"%s"`, sim.NewUUID()),
		Properties:       props,
		CaptureStartTime: started.Format(time.RFC3339Nano),
		Status:           "Running",
	}
	azurePacketCaptures.Put(id, capture)
	packetCaptureSessions.Store(id, session)
	bg.WatchThen(func() {
		select {
		case reason := <-session.ended:
			session.ended <- reason
		case <-session.handle.Exited():
		}
	}, func() {
		finishPacketCapture(id, vm.ID, session, account, container, blob)
	})
	sim.WriteJSON(w, http.StatusCreated, packetCaptureResult(capture))
}

func startPacketCaptureAgent(ctx context.Context, machine placement.Handle, props AzurePacketCaptureProperties) (*packetCaptureSession, error) {
	cli := sim.DockerClient()
	if err := sim.RequireContainerRuntime("running a packet capture agent"); err != nil {
		return nil, err
	}
	image, _, err := sim.EnsureEngineBuild(ctx, cli, pcapAgentBuild)
	if err != nil {
		return nil, err
	}
	platform, err := workload.LocalImagePlatform(ctx, image, "")
	if err != nil {
		return nil, err
	}
	backend, err := azurePlacementBackend(placementVirtualMachines)
	if err != nil {
		return nil, err
	}
	filters, err := json.Marshal(props.Filters)
	if err != nil {
		return nil, err
	}
	session := &packetCaptureSession{capturing: make(chan struct{}), ended: make(chan string, 1), finished: make(chan struct{})}
	var capturing sync.Once
	handle, err := backend.PlaceMember(ctx, machine.ID(), sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Name:         "sockerless-sim-azure-pcap-" + sim.RandomHex(6),
		Command: []string{"/pcapagent", "run", "/captures/capture.cap",
			strconv.FormatInt(*props.BytesToCapturePerPacket, 10),
			strconv.FormatInt(*props.TotalBytesPerSession, 10),
			strconv.FormatInt(*props.TimeLimitInSeconds, 10),
			string(filters)},
		Sandbox: sim.SandboxProfile{
			CapDrop:          []string{"ALL"},
			CapAdd:           []string{"NET_RAW"},
			NoNewPrivileges:  true,
			DenyDockerSocket: true,
			DenyHostNetwork:  true,
		},
	}, sim.FuncSink(func(line sim.LogLine) {
		if line.Stream != "stdout" {
			return
		}
		if strings.TrimSpace(line.Text) == "capturing" {
			capturing.Do(func() { close(session.capturing) })
		}
		if reason, ok := strings.CutPrefix(line.Text, "stopped "); ok {
			select {
			case session.ended <- strings.TrimSpace(reason):
			default:
			}
		}
	}))
	if err != nil {
		return nil, err
	}
	session.handle = handle
	select {
	case <-session.capturing:
		return session, nil
	case <-handle.Exited():
		result := handle.Wait()
		return nil, fmt.Errorf("the capture agent exited with code %d before it captured", result.ExitCode)
	case <-ctx.Done():
		handle.Cancel()
		return nil, ctx.Err()
	}
}

// packetCaptureReadChunk is how much of the file one read moves.
const packetCaptureReadChunk = 4 << 20

// readPacketCaptureFile reads the agent's file through exec, in base64 so
// the bytes survive the exec transport.
func readPacketCaptureFile(ctx context.Context, agent placement.Handle) ([]byte, error) {
	var data []byte
	for {
		result, err := agent.Exec(ctx, []string{"/pcapagent", "read", "/captures/capture.cap",
			strconv.Itoa(len(data)), strconv.Itoa(packetCaptureReadChunk)})
		if err != nil {
			return nil, err
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("read the capture file: %s", strings.TrimSpace(result.Stderr))
		}
		chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(result.Stdout))
		if err != nil {
			return nil, err
		}
		data = append(data, chunk...)
		if len(chunk) < packetCaptureReadChunk {
			return data, nil
		}
	}
}

// packetCaptureStorageVersion is the service version Network Watcher signs
// its SAS at.
const packetCaptureStorageVersion = "2022-11-02"

// uploadPacketCapture writes the file to the storage account with an account
// SAS the account's key signs, as Network Watcher does: an account without key
// access refuses it.
func uploadPacketCapture(captureID, account, container, blob string, data []byte) error {
	stored := azStorageAccounts.Filter(func(a StorageAccount) bool { return strings.EqualFold(a.Name, account) })
	if len(stored) == 0 {
		return fmt.Errorf("the storage account %q no longer exists", account)
	}
	q := url.Values{}
	q.Set("sv", packetCaptureStorageVersion)
	q.Set("ss", "b")
	q.Set("srt", "co")
	q.Set("sp", "cw")
	q.Set("se", time.Now().UTC().Add(time.Hour).Format("2006-01-02T15:04:05Z"))
	sig, err := storageSignWithKey(stored[0].ID, "key1", blobAccountSASStringToSign(strings.ToLower(account), q))
	if err != nil {
		return err
	}
	q.Set("sig", sig)
	call := func(path string, extra url.Values, header http.Header, body []byte) (int, []byte, error) {
		query := url.Values{}
		for k, v := range q {
			query[k] = v
		}
		for k, v := range extra {
			query[k] = v
		}
		header.Set("X-Ms-Version", packetCaptureStorageVersion)
		header.Set("X-Ms-Date", time.Now().UTC().Format(http.TimeFormat))
		return azureCallAs(context.Background(), "Microsoft.Network", captureID, http.MethodPut,
			path+"?"+query.Encode(), header, body)
	}
	status, answer, err := call("/"+account+"/"+container, url.Values{"restype": {"container"}}, http.Header{}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusConflict {
		return fmt.Errorf("create container %s: Azure Blob Storage answered %d: %s", container, status, answer)
	}
	status, answer, err = call("/"+account+"/"+container+"/"+blob, nil, http.Header{
		"X-Ms-Blob-Type": {"BlockBlob"},
		"Content-Type":   {"application/octet-stream"},
	}, data)
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("put blob %s: Azure Blob Storage answered %d: %s", blob, status, answer)
	}
	return nil
}

// packetCaptureGuestChunk keeps each write's argument under the kernel's
// limit on one argument.
const packetCaptureGuestChunk = 64 << 10

// writePacketCaptureToGuest writes the file at path inside the machine.
func writePacketCaptureToGuest(ctx context.Context, vmID, path string, data []byte) error {
	machine, ok := azureVMHandle(vmID)
	if !ok {
		return fmt.Errorf("virtual machine %q is not running", vmID)
	}
	const script = `set -e; mkdir -p "$(dirname "$1")"; if [ "$2" = first ]; then : > "$1"; fi; printf %s "$3" | base64 -d >> "$1"`
	for offset := 0; offset == 0 || offset < len(data); offset += packetCaptureGuestChunk {
		end := min(offset+packetCaptureGuestChunk, len(data))
		mode := "append"
		if offset == 0 {
			mode = "first"
		}
		result, err := machine.Exec(ctx, []string{"/bin/sh", "-c", script, "sh", path, mode,
			base64.StdEncoding.EncodeToString(data[offset:end])})
		if err != nil {
			return err
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("write %s in the machine: %s", path, strings.TrimSpace(result.Stderr))
		}
	}
	return nil
}

// finishPacketCapture moves the file once the agent stops and records how the
// session ended.
func finishPacketCapture(id, vmID string, session *packetCaptureSession, account, container, blob string) {
	defer close(session.finished)
	defer packetCaptureSessions.Delete(id)
	defer session.handle.Cancel()

	ctx := context.Background()
	reason := ""
	select {
	case reason = <-session.ended:
	default:
	}
	var errs []string
	capture, ok := azurePacketCaptures.Get(id)
	if !ok {
		return
	}
	if reason == "" {
		errs = append(errs, "AgentStopped")
	} else if data, err := readPacketCaptureFile(ctx, session.handle); err != nil {
		errs = append(errs, "CaptureFailed")
	} else {
		if account != "" {
			if err := uploadPacketCapture(id, account, container, blob, data); err != nil {
				errs = append(errs, "StorageFailed")
				logPacketCaptureFailure(id, err)
			}
		}
		if path := packetCaptureLocalPath(capture.Properties.StorageLocation); path != "" {
			if err := writePacketCaptureToGuest(ctx, vmID, path, data); err != nil {
				errs = append(errs, "LocalFileFailed")
				logPacketCaptureFailure(id, err)
			}
		}
	}
	azurePacketCaptures.Update(id, func(c *AzurePacketCapture) {
		if len(errs) > 0 {
			c.Status = "Error"
			c.Errors = errs
			return
		}
		c.Status = "Stopped"
		if reason == "TimeExceeded" {
			c.StopReason = reason
		}
	})
}

func logPacketCaptureFailure(id string, err error) {
	fmt.Fprintf(os.Stderr, "[sim-azure-network-watcher] packet capture %s: %v\n", id, err)
}

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
		"packetCaptureError":  append([]string{}, capture.Errors...),
	}
	if capture.StopReason != "" {
		body["stopReason"] = capture.StopReason
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// stopPacketCaptureSession asks a running agent to stop and waits until the
// file has moved.
func stopPacketCaptureSession(ctx context.Context, id string) error {
	value, ok := packetCaptureSessions.Load(id)
	if !ok {
		return nil
	}
	session, ok := value.(*packetCaptureSession)
	if !ok {
		return fmt.Errorf("packet capture session %s holds %T", id, value)
	}
	if _, err := session.handle.Exec(ctx, []string{"/pcapagent", "stop"}); err != nil {
		select {
		case <-session.finished:
			return nil
		default:
			return err
		}
	}
	select {
	case <-session.finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func handlePacketCaptureStop(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if _, ok := azurePacketCaptures.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource %q was not found.", id)
		return
	}
	if err := stopPacketCaptureSession(r.Context(), id); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "stop the capture: %v", err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Deleting the capture resource leaves its file where the session put it.
func handlePacketCaptureDelete(w http.ResponseWriter, r *http.Request) {
	id := packetCaptureID(r)
	if _, ok := azurePacketCaptures.Get(id); !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := stopPacketCaptureSession(r.Context(), id); err != nil {
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "stop the capture: %v", err)
		return
	}
	azurePacketCaptures.Delete(id)
	w.WriteHeader(http.StatusNoContent)
}
