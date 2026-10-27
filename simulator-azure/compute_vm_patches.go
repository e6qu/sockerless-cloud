package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Patch assessment and installation, and image capture, answer from the
// machine: the guest's package manager reports and installs upgrades, and a
// capture copies the disk the machine runs on.

func registerVirtualMachinePatchesAndCapture(srv *sim.Server) {
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"

	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/assessPatches", func(w http.ResponseWriter, r *http.Request) {
		_, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		result, err := azureAssessGuestPatches(r.Context(), id)
		if err != nil {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})

	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/installPatches", func(w http.ResponseWriter, r *http.Request) {
		_, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		var request struct {
			MaximumDuration string `json:"maximumDuration"`
			RebootSetting   string `json:"rebootSetting"`
			LinuxParameters *struct {
				ClassificationsToInclude  []string `json:"classificationsToInclude"`
				PackageNameMasksToInclude []string `json:"packageNameMasksToInclude"`
				PackageNameMasksToExclude []string `json:"packageNameMasksToExclude"`
			} `json:"linuxParameters"`
		}
		if err := sim.ReadJSON(r, &request); err != nil {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		if request.RebootSetting == "" {
			AzureError(w, "InvalidParameter",
				"rebootSetting is required: it decides what happens when a patch needs a restart.",
				http.StatusBadRequest)
			return
		}
		var include, exclude []string
		if request.LinuxParameters != nil {
			include = request.LinuxParameters.PackageNameMasksToInclude
			exclude = request.LinuxParameters.PackageNameMasksToExclude
		}
		result, err := azureInstallGuestPatches(r.Context(), id, include, exclude, request.RebootSetting)
		if err != nil {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})

	srv.HandleFunc("POST "+armBase+"/virtualMachines/{vmName}/capture", func(w http.ResponseWriter, r *http.Request) {
		vm, id, ok := azureLookupVM(w, r)
		if !ok {
			return
		}
		var request struct {
			VhdPrefix                string `json:"vhdPrefix"`
			DestinationContainerName string `json:"destinationContainerName"`
			OverwriteVhds            *bool  `json:"overwriteVhds"`
		}
		if err := sim.ReadJSON(r, &request); err != nil {
			AzureErrorf(w, "InvalidRequestContent", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}
		if request.VhdPrefix == "" || request.DestinationContainerName == "" {
			AzureError(w, "InvalidParameter",
				"vhdPrefix and destinationContainerName are required to name the captured image.",
				http.StatusBadRequest)
			return
		}
		// Azure captures only a generalized machine: a specialized one would carry its
		// host name, keys and logs into every machine created from it.
		if generalized, _ := azureVMGeneralized.Get(id); !generalized {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict,
				"Capture is not allowed on VM %q because it has not been generalized.", id)
			return
		}
		result, err := azureCaptureVMImage(r.Context(), vm, request.VhdPrefix, request.DestinationContainerName)
		if err != nil {
			AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
			return
		}
		sim.WriteJSON(w, http.StatusOK, result)
	})
}

// `apt-get --simulate upgrade` changes nothing, and its Inst lines name the
// package, target version and archive; the archive marks a security update.
func azureAssessGuestPatches(ctx context.Context, vmID string) (map[string]any, error) {
	started := time.Now().UTC()
	stdout, stderr, exitCode, err := azureGuestExec(ctx, vmID, "LC_ALL=C apt-get --simulate --quiet upgrade 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("assess patches on %q: %w", vmID, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("assess patches on %q: the guest's package manager exited %d: %s",
			vmID, exitCode, strings.TrimSpace(string(stderr)))
	}

	patches := azureParseAptUpgradePlan(string(stdout), started)
	security, other := 0, 0
	for _, patch := range patches {
		if azurePatchIsSecurity(patch) {
			security++
			continue
		}
		other++
	}

	// Debian and Ubuntu record a pending restart with this file.
	_, _, rebootExit, err := azureGuestExec(ctx, vmID, "test -f /var/run/reboot-required")
	rebootPending := err == nil && rebootExit == 0

	return map[string]any{
		"status":                        "Succeeded",
		"assessmentActivityId":          azureNetworkEtag(),
		"rebootPending":                 rebootPending,
		"criticalAndSecurityPatchCount": security,
		"otherPatchCount":               other,
		"startDateTime":                 started.Format(time.RFC3339),
		"availablePatches":              patches,
	}, nil
}

// azureParseAptUpgradePlan reads lines of the form:
//
//	Inst libssl3 [3.0.2-0ubuntu1.10] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-security [amd64])
func azureParseAptUpgradePlan(plan string, assessed time.Time) []map[string]any {
	var patches []map[string]any
	for _, line := range strings.Split(plan, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Inst "))
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		version, archive := "", ""
		if open := strings.Index(line, "("); open >= 0 {
			inner := line[open+1:]
			if close := strings.Index(inner, ")"); close >= 0 {
				inner = inner[:close]
			}
			parts := strings.Fields(inner)
			if len(parts) > 0 {
				version = parts[0]
			}
			if len(parts) > 1 {
				archive = parts[1]
			}
		}
		classification := "Other"
		if strings.Contains(strings.ToLower(archive), "security") {
			classification = "Security"
		}
		patches = append(patches, map[string]any{
			"patchId":              name + "_" + version,
			"name":                 name,
			"version":              version,
			"classifications":      []string{classification},
			"rebootBehavior":       "NeverReboots",
			"assessmentState":      "Available",
			"lastModifiedDateTime": assessed.Format(time.RFC3339),
		})
	}
	return patches
}

func azurePatchIsSecurity(patch map[string]any) bool {
	classifications, _ := patch["classifications"].([]string)
	for _, classification := range classifications {
		if strings.EqualFold(classification, "Security") {
			return true
		}
	}
	return false
}

// The caller's masks select packages; none upgrades everything assessed, as
// apt's own upgrade does.
func azureInstallGuestPatches(
	ctx context.Context,
	vmID string,
	include, exclude []string,
	rebootSetting string,
) (map[string]any, error) {
	started := time.Now().UTC()
	available, err := azureAssessGuestPatches(ctx, vmID)
	if err != nil {
		return nil, err
	}
	candidates, _ := available["availablePatches"].([]map[string]any)

	var selected, excluded, notSelected []map[string]any
	for _, patch := range candidates {
		name, _ := patch["name"].(string)
		switch {
		case azureNameMatchesAnyMask(name, exclude):
			excluded = append(excluded, patch)
		case len(include) == 0 || azureNameMatchesAnyMask(name, include):
			selected = append(selected, patch)
		default:
			notSelected = append(notSelected, patch)
		}
	}

	installed, failed := 0, 0
	patchStates := make([]map[string]any, 0, len(selected))
	for _, patch := range selected {
		name, _ := patch["name"].(string)
		_, _, exitCode, err := azureGuestExec(ctx, vmID,
			"DEBIAN_FRONTEND=noninteractive apt-get --quiet --yes --only-upgrade install "+shellQuote(name))
		state := "Installed"
		if err != nil || exitCode != 0 {
			state = "Failed"
			failed++
		} else {
			installed++
		}
		record := map[string]any{}
		for k, v := range patch {
			record[k] = v
		}
		record["patchInstallationState"] = state
		patchStates = append(patchStates, record)
	}

	// Restart when the caller asked, or asked for one if needed and the guest
	// needs one.
	rebootStatus := "NotNeeded"
	pending, _ := available["rebootPending"].(bool)
	if strings.EqualFold(rebootSetting, "Always") ||
		(strings.EqualFold(rebootSetting, "IfRequired") && pending) {
		if _, _, _, err := azureGuestExec(ctx, vmID, "systemctl reboot --no-block || reboot"); err != nil {
			rebootStatus = "Failed"
		} else {
			rebootStatus = "Completed"
		}
	}

	status := "Succeeded"
	if failed > 0 {
		status = "CompletedWithWarnings"
	}
	return map[string]any{
		"status":                    status,
		"installationActivityId":    azureNetworkEtag(),
		"rebootStatus":              rebootStatus,
		"maintenanceWindowExceeded": false,
		"excludedPatchCount":        len(excluded),
		"notSelectedPatchCount":     len(notSelected),
		"pendingPatchCount":         0,
		"installedPatchCount":       installed,
		"failedPatchCount":          failed,
		"patches":                   patchStates,
		"startDateTime":             started.Format(time.RFC3339),
	}, nil
}

// apt-style masks: * stands for any run of characters.
func azureNameMatchesAnyMask(name string, masks []string) bool {
	for _, mask := range masks {
		if azureNameMatchesMask(name, mask) {
			return true
		}
	}
	return false
}

func azureNameMatchesMask(name, mask string) bool {
	if mask == "" {
		return false
	}
	parts := strings.Split(mask, "*")
	if len(parts) == 1 {
		return name == mask
	}
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for i, part := range parts[1 : len(parts)-1] {
		index := strings.Index(rest, part)
		if index < 0 {
			return false
		}
		rest = rest[index+len(part):]
		_ = i
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// azureCaptureVMImage commits the disk as an OCI image and writes its bytes
// into the destination container of the boot diagnostics account, asking the
// guest to flush first: Azure quiesces the file system, as a mid-write copy
// boots into a repair. It returns the deployment template Azure does, whose
// osDisk points at the written blob.
func azureCaptureVMImage(
	ctx context.Context,
	vm VirtualMachine,
	vhdPrefix, container string,
) (map[string]any, error) {
	account, err := azureBootDiagnosticsAccount(vm)
	if err != nil {
		return nil, fmt.Errorf(
			"capturing %q writes the image into a storage account, and the machine names none: %w", vm.ID, err)
	}
	image, err := azureCommitVMImage(ctx, vm, "capture")
	if err != nil {
		return nil, err
	}
	disk, err := azureExportImage(ctx, image.OCI)
	if err != nil {
		return nil, err
	}
	blobName := vhdPrefix + "-osdisk.vhd"
	now := time.Now().UTC().Format(http.TimeFormat)
	if err := putBlobWithContents(BlobObject{
		Account:      account,
		Container:    container,
		Name:         blobName,
		ContentType:  "application/octet-stream",
		BlobType:     "PageBlob",
		ETag:         azureNetworkEtag(),
		LastModified: now,
		CreationTime: now,
	}, disk); err != nil {
		return nil, err
	}

	uri := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", account, container, blobName)
	azureRecordCapturedImage(uri, image)
	return map[string]any{
		"$schema":        "https://schema.management.azure.com/schemas/2015-01-01/deploymentTemplate.json#",
		"contentVersion": "1.0.0.0",
		"parameters": map[string]any{
			"vmName": map[string]any{"type": "string"},
		},
		"resources": []any{
			map[string]any{
				"type":       "Microsoft.Compute/virtualMachines",
				"name":       "[parameters('vmName')]",
				"apiVersion": "2022-03-01",
				"location":   vm.Location,
				"properties": map[string]any{
					"storageProfile": map[string]any{
						"osDisk": map[string]any{
							"createOption": "FromImage",
							"osType":       "Linux",
							"image":        map[string]any{"uri": uri},
						},
					},
				},
			},
		},
	}, nil
}
