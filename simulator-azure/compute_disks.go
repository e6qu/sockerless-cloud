package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Managed disks, against DiskRP's disk.json (2023-04-02). A disk keeps its
// contents in an engine volume of its own; while a machine holds it, the
// contents live in that machine's disk bay (compute_vm_data_disks.go).

type DiskSku struct {
	Name string `json:"name,omitempty"`
	Tier string `json:"tier,omitempty"`
}

type DiskEncryption struct {
	Type                string `json:"type,omitempty"`
	DiskEncryptionSetID string `json:"diskEncryptionSetId,omitempty"`
}

type ManagedDisk struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Type       string                `json:"type"`
	Location   string                `json:"location"`
	Tags       map[string]string     `json:"tags,omitempty"`
	ManagedBy  string                `json:"managedBy,omitempty"`
	Sku        *DiskSku              `json:"sku,omitempty"`
	Zones      []string              `json:"zones,omitempty"`
	Properties ManagedDiskProperties `json:"properties"`
}

type ManagedDiskProperties struct {
	CreationData               map[string]any  `json:"creationData"`
	DiskSizeGB                 int32           `json:"diskSizeGB,omitempty"`
	DiskSizeBytes              int64           `json:"diskSizeBytes,omitempty"`
	DiskIOPSReadWrite          int64           `json:"diskIOPSReadWrite,omitempty"`
	DiskMBpsReadWrite          int64           `json:"diskMBpsReadWrite,omitempty"`
	DiskState                  string          `json:"diskState,omitempty"`
	ProvisioningState          string          `json:"provisioningState,omitempty"`
	TimeCreated                string          `json:"timeCreated,omitempty"`
	LastOwnershipUpdateTime    string          `json:"LastOwnershipUpdateTime,omitempty"`
	UniqueID                   string          `json:"uniqueId,omitempty"`
	Tier                       string          `json:"tier,omitempty"`
	OSType                     string          `json:"osType,omitempty"`
	HyperVGeneration           string          `json:"hyperVGeneration,omitempty"`
	MaxShares                  int32           `json:"maxShares,omitempty"`
	Encryption                 *DiskEncryption `json:"encryption,omitempty"`
	NetworkAccessPolicy        string          `json:"networkAccessPolicy,omitempty"`
	PublicNetworkAccess        string          `json:"publicNetworkAccess,omitempty"`
	DataAccessAuthMode         string          `json:"dataAccessAuthMode,omitempty"`
	DiskAccessID               string          `json:"diskAccessId,omitempty"`
	BurstingEnabled            *bool           `json:"burstingEnabled,omitempty"`
	OptimizedForFrequentAttach *bool           `json:"optimizedForFrequentAttach,omitempty"`
	SupportsHibernation        *bool           `json:"supportsHibernation,omitempty"`
	SupportedCapabilities      map[string]any  `json:"supportedCapabilities,omitempty"`
	SecurityProfile            map[string]any  `json:"securityProfile,omitempty"`
	PurchasePlan               map[string]any  `json:"purchasePlan,omitempty"`
}

// diskUpdate is DiskUpdate: every member a PATCH may carry, nil when absent.
type diskUpdate struct {
	Tags       map[string]string     `json:"tags"`
	Sku        *DiskSku              `json:"sku"`
	Properties *diskUpdateProperties `json:"properties"`
}

type diskUpdateProperties struct {
	DiskSizeGB                 *int32          `json:"diskSizeGB"`
	DiskIOPSReadWrite          *int64          `json:"diskIOPSReadWrite"`
	DiskMBpsReadWrite          *int64          `json:"diskMBpsReadWrite"`
	Tier                       *string         `json:"tier"`
	OSType                     *string         `json:"osType"`
	MaxShares                  *int32          `json:"maxShares"`
	Encryption                 *DiskEncryption `json:"encryption"`
	NetworkAccessPolicy        *string         `json:"networkAccessPolicy"`
	PublicNetworkAccess        *string         `json:"publicNetworkAccess"`
	DataAccessAuthMode         *string         `json:"dataAccessAuthMode"`
	DiskAccessID               *string         `json:"diskAccessId"`
	BurstingEnabled            *bool           `json:"burstingEnabled"`
	OptimizedForFrequentAttach *bool           `json:"optimizedForFrequentAttach"`
	SupportsHibernation        *bool           `json:"supportsHibernation"`
	SupportedCapabilities      map[string]any  `json:"supportedCapabilities"`
	PurchasePlan               map[string]any  `json:"purchasePlan"`
}

var azureDisks sim.Store[ManagedDisk]

// azureDiskSkuMaxGB is the largest size each DiskStorageAccountTypes value
// provisions.
var azureDiskSkuMaxGB = map[string]int32{
	"Standard_LRS":    32767,
	"Premium_LRS":     32767,
	"StandardSSD_LRS": 32767,
	"UltraSSD_LRS":    65536,
	"Premium_ZRS":     32767,
	"StandardSSD_ZRS": 32767,
	"PremiumV2_LRS":   65536,
}

func azureDiskSkuName(name string) (string, bool) {
	for known := range azureDiskSkuMaxGB {
		if strings.EqualFold(known, name) {
			return known, true
		}
	}
	return "", false
}

func azureDiskSkuTier(name string) string {
	switch name {
	case "Premium_LRS", "Premium_ZRS", "PremiumV2_LRS":
		return "Premium"
	case "UltraSSD_LRS":
		return "Ultra"
	}
	return "Standard"
}

func azureDiskFault(code string, status int, format string, args ...any) *azureVMRequestFault {
	return &azureVMRequestFault{code: code, status: status, message: fmt.Sprintf(format, args...)}
}

// azureDiskSizeFault checks a size against the sku's range.
func azureDiskSizeFault(sku string, sizeGB int32) *azureVMRequestFault {
	maxGB := azureDiskSkuMaxGB[sku]
	if sizeGB < 1 || sizeGB > maxGB {
		return azureDiskFault("InvalidParameter", http.StatusBadRequest,
			"The value of parameter diskSizeGB is invalid: a %s disk is between 1 and %d GB, got %d.", sku, maxGB, sizeGB)
	}
	return nil
}

func azureDiskCreateOption(disk ManagedDisk) string {
	option, _ := disk.Properties.CreationData["createOption"].(string)
	return option
}

func azureLookupDisk(id string) (ManagedDisk, bool) {
	if disk, ok := azureDisks.Get(id); ok {
		return disk, true
	}
	found := azureDisks.Filter(func(d ManagedDisk) bool { return strings.EqualFold(d.ID, id) })
	if len(found) == 0 {
		return ManagedDisk{}, false
	}
	return found[0], true
}

// azureDiskHolderState is the power state of the machine holding the disk.
func azureDiskHolderState(disk ManagedDisk) string {
	if disk.ManagedBy == "" {
		return ""
	}
	state, ok := azureVMStates.Get(disk.ManagedBy)
	if !ok {
		return "PowerState/running"
	}
	return state
}

// azureDiskView fills the members Azure derives at read time.
func azureDiskView(disk ManagedDisk) ManagedDisk {
	switch state := azureDiskHolderState(disk); {
	case disk.ManagedBy == "":
		disk.Properties.DiskState = "Unattached"
	case state == "PowerState/deallocated":
		disk.Properties.DiskState = "Reserved"
	default:
		disk.Properties.DiskState = "Attached"
	}
	disk.Properties.DiskSizeBytes = int64(disk.Properties.DiskSizeGB) << 30
	return disk
}

// azureNewDisk validates a create and builds the disk; source is the disk a
// Copy reads from.
func azureNewDisk(id, name string, req ManagedDisk) (ManagedDisk, *ManagedDisk, *azureVMRequestFault) {
	if req.Location == "" {
		return ManagedDisk{}, nil, azureDiskFault("LocationRequired", http.StatusBadRequest, "The location property is required for this definition.")
	}
	option := azureDiskCreateOption(req)
	if option == "" {
		return ManagedDisk{}, nil, azureDiskFault("InvalidParameter", http.StatusBadRequest, "Required parameter 'creationData.createOption' is missing (null).")
	}
	skuName := "Standard_LRS"
	if req.Sku != nil && req.Sku.Name != "" {
		known, ok := azureDiskSkuName(req.Sku.Name)
		if !ok {
			return ManagedDisk{}, nil, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"The value '%s' of parameter sku.name is invalid.", req.Sku.Name)
		}
		skuName = known
	}
	if req.Properties.MaxShares > 1 {
		return ManagedDisk{}, nil, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator does not share a managed disk between machines (maxShares %d).", req.Properties.MaxShares)
	}
	disk := ManagedDisk{
		ID: id, Name: name, Type: "Microsoft.Compute/disks",
		Location: req.Location, Tags: req.Tags, Zones: req.Zones,
		Sku:        &DiskSku{Name: skuName, Tier: azureDiskSkuTier(skuName)},
		Properties: req.Properties,
	}
	props := &disk.Properties
	var source *ManagedDisk
	switch {
	case strings.EqualFold(option, "Empty"):
		props.CreationData["createOption"] = "Empty"
		if props.DiskSizeGB == 0 {
			return ManagedDisk{}, nil, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"Required parameter 'diskSizeGB' is missing (null): a disk created Empty names its size.")
		}
	case strings.EqualFold(option, "Copy"):
		props.CreationData["createOption"] = "Copy"
		sourceID, _ := props.CreationData["sourceResourceId"].(string)
		if sourceID == "" {
			return ManagedDisk{}, nil, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"Required parameter 'creationData.sourceResourceId' is missing (null).")
		}
		if !strings.Contains(strings.ToLower(sourceID), "/providers/microsoft.compute/disks/") {
			return ManagedDisk{}, nil, azureDiskFault("NotImplemented", http.StatusNotImplemented,
				"The simulator copies a managed disk from another managed disk only; %s is not one.", sourceID)
		}
		found, ok := azureLookupDisk(sourceID)
		if !ok {
			return ManagedDisk{}, nil, azureDiskFault("ResourceNotFound", http.StatusNotFound,
				"The Resource %q referenced by creationData.sourceResourceId was not found.", sourceID)
		}
		source = &found
		props.CreationData["sourceResourceId"] = found.ID
		props.CreationData["sourceUniqueId"] = found.Properties.UniqueID
		if props.DiskSizeGB == 0 {
			props.DiskSizeGB = found.Properties.DiskSizeGB
		}
		if props.DiskSizeGB < found.Properties.DiskSizeGB {
			return ManagedDisk{}, nil, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"The value of parameter diskSizeGB is invalid: a copy of a %d GB disk cannot be %d GB.", found.Properties.DiskSizeGB, props.DiskSizeGB)
		}
		if props.OSType == "" {
			props.OSType = found.Properties.OSType
		}
	default:
		return ManagedDisk{}, nil, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator creates managed disks Empty or as a Copy of another managed disk, not %s.", option)
	}
	if fault := azureDiskSizeFault(skuName, props.DiskSizeGB); fault != nil {
		return ManagedDisk{}, nil, fault
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	props.UniqueID = sim.NewUUID()
	props.TimeCreated = now
	props.DiskState = ""
	props.DiskSizeBytes = 0
	props.LastOwnershipUpdateTime = ""
	if props.Encryption == nil || props.Encryption.Type == "" {
		props.Encryption = &DiskEncryption{Type: "EncryptionAtRestWithPlatformKey"}
	}
	if props.NetworkAccessPolicy == "" {
		props.NetworkAccessPolicy = "AllowAll"
	}
	if props.PublicNetworkAccess == "" {
		props.PublicNetworkAccess = "Enabled"
	}
	return disk, source, nil
}

// azureUpdateDisk applies a DiskUpdate. Azure resizes and re-tiers a disk only
// while no running machine holds it, and only grows it.
func azureUpdateDisk(disk *ManagedDisk, update diskUpdate) *azureVMRequestFault {
	holderRunning := disk.ManagedBy != "" && azureDiskHolderState(*disk) != "PowerState/deallocated"
	if update.Sku != nil && update.Sku.Name != "" {
		known, ok := azureDiskSkuName(update.Sku.Name)
		if !ok {
			return azureDiskFault("InvalidParameter", http.StatusBadRequest, "The value '%s' of parameter sku.name is invalid.", update.Sku.Name)
		}
		if known != disk.Sku.Name && holderRunning {
			return azureDiskFault("OperationNotAllowed", http.StatusConflict,
				"Changing the account type of disk '%s' is allowed only when it is unattached or its VM is deallocated.", disk.Name)
		}
		disk.Sku = &DiskSku{Name: known, Tier: azureDiskSkuTier(known)}
	}
	if update.Tags != nil {
		disk.Tags = update.Tags
	}
	p := update.Properties
	if p == nil {
		return azureDiskSizeFault(disk.Sku.Name, disk.Properties.DiskSizeGB)
	}
	if p.MaxShares != nil && *p.MaxShares > 1 {
		return azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator does not share a managed disk between machines (maxShares %d).", *p.MaxShares)
	}
	if p.DiskSizeGB != nil && *p.DiskSizeGB != disk.Properties.DiskSizeGB {
		if *p.DiskSizeGB < disk.Properties.DiskSizeGB {
			return azureDiskFault("OperationNotAllowed", http.StatusConflict,
				"Disk '%s' is %d GB: a disk can only grow, not shrink to %d GB.", disk.Name, disk.Properties.DiskSizeGB, *p.DiskSizeGB)
		}
		if holderRunning {
			return azureDiskFault("OperationNotAllowed", http.StatusConflict,
				"Disk resizing is allowed only when creating a VM or when the VM is deallocated.")
		}
		disk.Properties.DiskSizeGB = *p.DiskSizeGB
	}
	if fault := azureDiskSizeFault(disk.Sku.Name, disk.Properties.DiskSizeGB); fault != nil {
		return fault
	}
	setString := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setString(&disk.Properties.Tier, p.Tier)
	setString(&disk.Properties.OSType, p.OSType)
	setString(&disk.Properties.NetworkAccessPolicy, p.NetworkAccessPolicy)
	setString(&disk.Properties.PublicNetworkAccess, p.PublicNetworkAccess)
	setString(&disk.Properties.DataAccessAuthMode, p.DataAccessAuthMode)
	setString(&disk.Properties.DiskAccessID, p.DiskAccessID)
	if p.MaxShares != nil {
		disk.Properties.MaxShares = *p.MaxShares
	}
	if p.DiskIOPSReadWrite != nil {
		disk.Properties.DiskIOPSReadWrite = *p.DiskIOPSReadWrite
	}
	if p.DiskMBpsReadWrite != nil {
		disk.Properties.DiskMBpsReadWrite = *p.DiskMBpsReadWrite
	}
	if p.Encryption != nil && p.Encryption.Type != "" {
		disk.Properties.Encryption = p.Encryption
	}
	if p.BurstingEnabled != nil {
		disk.Properties.BurstingEnabled = p.BurstingEnabled
	}
	if p.OptimizedForFrequentAttach != nil {
		disk.Properties.OptimizedForFrequentAttach = p.OptimizedForFrequentAttach
	}
	if p.SupportsHibernation != nil {
		disk.Properties.SupportsHibernation = p.SupportsHibernation
	}
	if p.SupportedCapabilities != nil {
		disk.Properties.SupportedCapabilities = p.SupportedCapabilities
	}
	if p.PurchasePlan != nil {
		disk.Properties.PurchasePlan = p.PurchasePlan
	}
	return nil
}

// azureDiskUpdateFromPut reads a PUT on an existing disk as a DiskUpdate of
// every writable member it carries.
func azureDiskUpdateFromPut(req ManagedDisk) diskUpdate {
	p := req.Properties
	str := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	props := &diskUpdateProperties{
		Tier: str(p.Tier), OSType: str(p.OSType),
		NetworkAccessPolicy: str(p.NetworkAccessPolicy), PublicNetworkAccess: str(p.PublicNetworkAccess),
		DataAccessAuthMode: str(p.DataAccessAuthMode), DiskAccessID: str(p.DiskAccessID),
		Encryption: p.Encryption, BurstingEnabled: p.BurstingEnabled,
		OptimizedForFrequentAttach: p.OptimizedForFrequentAttach, SupportsHibernation: p.SupportsHibernation,
		SupportedCapabilities: p.SupportedCapabilities, PurchasePlan: p.PurchasePlan,
	}
	if p.DiskSizeGB != 0 {
		props.DiskSizeGB = &p.DiskSizeGB
	}
	if p.MaxShares != 0 {
		props.MaxShares = &p.MaxShares
	}
	if p.DiskIOPSReadWrite != 0 {
		props.DiskIOPSReadWrite = &p.DiskIOPSReadWrite
	}
	if p.DiskMBpsReadWrite != 0 {
		props.DiskMBpsReadWrite = &p.DiskMBpsReadWrite
	}
	tags := req.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	return diskUpdate{Tags: tags, Sku: req.Sku, Properties: props}
}

func writeAzureDiskFault(w http.ResponseWriter, fault *azureVMRequestFault) {
	AzureError(w, fault.code, fault.message, fault.status)
}

// writeAzureDiskAccepted answers a disk write as DiskRP does: 202 with the
// disk as it stands and the operation to poll.
func writeAzureDiskAccepted(w http.ResponseWriter, r *http.Request, disk ManagedDisk, opID string) {
	asyncOperation, monitor := computeOperationURLs(r, sim.PathParam(r, "subscriptionId"), disk.Location, opID)
	writeAzureAsyncCreateHeaders(w, opID, asyncOperation, monitor)
	sim.WriteJSON(w, http.StatusAccepted, azureDiskView(disk))
}

// azureDiskResult is what a disk operation's monitor URL answers once it
// ends: the disk, as az's final-state-via location reads it.
func azureDiskResult(id string, opErr *AsyncOperationError) (json.RawMessage, *AsyncOperationError) {
	if opErr != nil {
		return nil, opErr
	}
	disk, ok := azureDisks.Get(id)
	if !ok {
		return nil, nil
	}
	body, err := json.Marshal(azureDiskView(disk))
	if err != nil {
		return nil, &AsyncOperationError{Code: "InternalExecutionError", Message: err.Error()}
	}
	return body, nil
}

// azureSettleDisk ends a disk's provisioning.
func azureSettleDisk(id string, err error) *AsyncOperationError {
	azureDisks.Update(id, func(d *ManagedDisk) {
		d.Properties.ProvisioningState = "Succeeded"
		if err != nil {
			d.Properties.ProvisioningState = "Failed"
		}
	})
	if err != nil {
		return &AsyncOperationError{Code: "InternalDiskManagementError", Message: err.Error()}
	}
	return nil
}

func registerManagedDisks(srv *sim.Server) {
	azureDisks = sim.MakeStore[ManagedDisk](srv.DB(), "compute_disks")
	// A copy whose operation died with the previous process never finished.
	for _, disk := range azureDisks.Filter(func(d ManagedDisk) bool { return d.Properties.ProvisioningState == "Updating" }) {
		azureDisks.Update(disk.ID, func(d *ManagedDisk) { d.Properties.ProvisioningState = "Failed" })
	}
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"
	diskID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "disks", sim.PathParam(r, "diskName"))
	}
	logger := srv.Logger()

	srv.HandleFunc("PUT "+armBase+"/disks/{diskName}", func(w http.ResponseWriter, r *http.Request) {
		var req ManagedDisk
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.CreationData == nil {
			req.Properties.CreationData = map[string]any{}
		}
		id := diskID(r)
		if prev, exists := azureLookupDisk(id); exists {
			if requested := azureDiskCreateOption(req); requested != "" && !strings.EqualFold(requested, azureDiskCreateOption(prev)) {
				AzureErrorf(w, "PropertyChangeNotAllowed", http.StatusConflict, "Changing property 'creationData.createOption' is not allowed.")
				return
			}
			updated := prev
			if fault := azureUpdateDisk(&updated, azureDiskUpdateFromPut(req)); fault != nil {
				writeAzureDiskFault(w, fault)
				return
			}
			updated.Properties.ProvisioningState = "Succeeded"
			azureDisks.Put(prev.ID, updated)
			writeAzureDiskAccepted(w, r, updated, issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
				return azureDiskResult(prev.ID, nil)
			}))
			return
		}
		disk, source, fault := azureNewDisk(id, sim.PathParam(r, "diskName"), req)
		if fault != nil {
			writeAzureDiskFault(w, fault)
			return
		}
		disk.Properties.ProvisioningState = "Updating"
		azureDisks.Put(id, disk)
		opID := startAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
			var err error
			if source != nil {
				err = azureCopyDiskData(context.Background(), azureDiskLocationOf(*source), azureDiskLocation{volume: azureDiskVolume(disk)}, false)
				if err != nil {
					logger.Error().Err(err).Str("disk", id).Msg("failed to copy the source disk")
				}
			}
			return azureDiskResult(id, azureSettleDisk(id, err))
		})
		writeAzureDiskAccepted(w, r, disk, opID)
	})

	srv.HandleFunc("PATCH "+armBase+"/disks/{diskName}", func(w http.ResponseWriter, r *http.Request) {
		var update diskUpdate
		if err := sim.ReadJSON(r, &update); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := diskID(r)
		disk, ok := azureLookupDisk(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		if fault := azureUpdateDisk(&disk, update); fault != nil {
			writeAzureDiskFault(w, fault)
			return
		}
		azureDisks.Put(disk.ID, disk)
		writeAzureDiskAccepted(w, r, disk, issueAzureAsyncOperationResult(func() (json.RawMessage, *AsyncOperationError) {
			return azureDiskResult(disk.ID, nil)
		}))
	})

	srv.HandleFunc("GET "+armBase+"/disks/{diskName}", func(w http.ResponseWriter, r *http.Request) {
		id := diskID(r)
		disk, ok := azureLookupDisk(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, azureDiskView(disk))
	})

	// Azure refuses to delete a disk a machine holds; the machine detaches it
	// first, or deletes it with itself when its deleteOption says Delete.
	srv.HandleFunc("DELETE "+armBase+"/disks/{diskName}", func(w http.ResponseWriter, r *http.Request) {
		id := diskID(r)
		disk, ok := azureLookupDisk(id)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if disk.ManagedBy != "" {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "Disk %s is attached to VM %s.", disk.Name, disk.ManagedBy)
			return
		}
		azureDisks.Delete(disk.ID)
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			sim.RemoveVolumeSettled(azureDiskVolume(disk), "azure")
			return nil
		})
		asyncOperation, monitor := computeOperationURLs(r, sim.PathParam(r, "subscriptionId"), disk.Location, opID)
		writeAzureAsyncCreateHeaders(w, opID, asyncOperation, monitor)
		w.WriteHeader(http.StatusAccepted)
	})

	listDisks := func(w http.ResponseWriter, r *http.Request, prefix string) {
		all := azureDisks.Filter(func(d ManagedDisk) bool {
			return strings.HasPrefix(strings.ToLower(d.ID), strings.ToLower(prefix))
		})
		page, next, ok := armPage(w, r, all, func(d ManagedDisk) string { return strings.ToLower(d.ID) })
		if !ok {
			return
		}
		value := make([]ManagedDisk, 0, len(page))
		for _, d := range page {
			value = append(value, azureDiskView(d))
		}
		out := map[string]any{"value": value}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	}
	srv.HandleFunc("GET "+armBase+"/disks", func(w http.ResponseWriter, r *http.Request) {
		listDisks(w, r, armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "disks")+"/")
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/disks", func(w http.ResponseWriter, r *http.Request) {
		listDisks(w, r, armSubscriptionID(sim.PathParam(r, "subscriptionId"))+"/")
	})

	// A shared access signature reads or writes the disk as a VHD page blob,
	// and the simulator keeps a disk as a filesystem, not a VHD.
	for _, verb := range []string{"beginGetAccess", "endGetAccess"} {
		srv.HandleFunc("POST "+armBase+"/disks/{diskName}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
				"The simulator keeps a managed disk as a filesystem, not a VHD, so it grants no access to export or import one (%s).", verb)
		})
	}
}
