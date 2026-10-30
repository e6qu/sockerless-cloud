package main

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

type azureMetadataVM struct {
	VM       VirtualMachine
	NIC      NetworkInterface
	SubnetID string
}

var azureMetadataVMsByIP workloadhost.MetadataIndex[azureMetadataVM]

// registerMetadata serves the Azure cloud metadata endpoint used by both:
//   - azurestack provider (via ARM_METADATA_HOST): expects JSON array, api-version=2020-06-01
//   - azurerm v3 provider (via ARM_METADATA_HOSTNAME): expects single JSON object, api-version=2022-09-01
//
// The response redirects all Azure service URLs back to the simulator.
func registerMetadata(srv *sim.Server) {
	srv.HandleFunc("GET /metadata/endpoints", func(w http.ResponseWriter, r *http.Request) {
		host := r.Host

		// Detect scheme from the incoming request. If X-Forwarded-Proto is
		// set, honour it; otherwise fall back to whether TLS is active.
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if fp := r.Header.Get("X-Forwarded-Proto"); fp != "" {
			scheme = strings.ToLower(fp)
		}
		baseURL := fmt.Sprintf("%s://%s", scheme, host)
		storageSuffix := azureEndpointSuffix(azureStorageEndpointURL(r, "metadataacct", "blob"), "metadataacct", "blob")
		keyVaultSuffix := keyVaultDNSSuffix(r)

		env := map[string]any{
			"name": "AzureCloud",
			"authentication": map[string]any{
				"loginEndpoint": baseURL,
				"audiences": []string{
					baseURL + "/",
					"https://management.core.windows.net/",
					"https://management.azure.com/",
				},
				"tenant":           "common",
				"identityProvider": "AAD",
			},
			// No trailing slashes — go-azure-sdk prepends this to paths like
			// "/subscriptions/..." and a trailing slash would create "//subscriptions/..."
			// which triggers 301 redirects that change PUT→GET.
			"resourceManager":          baseURL,
			"microsoftGraphResourceId": baseURL + "/",
			"graph":                    baseURL,
			"portal":                   baseURL,
			"gallery":                  baseURL,
			"batch":                    baseURL,
			"suffixes": map[string]any{
				"keyVaultDns":       keyVaultSuffix,
				"storage":           storageSuffix,
				"acrLoginServer":    "localhost",
				"sqlServerHostname": "localhost",
			},
		}

		apiVersion := r.URL.Query().Get("api-version")
		if apiVersion == "2022-09-01" {
			// azurerm v3 (go-azure-sdk): expects a single object
			sim.WriteJSON(w, http.StatusOK, env)
		} else {
			// azurestack / older (go-azure-helpers): expects an array
			sim.WriteJSON(w, http.StatusOK, []any{env})
		}
	})

	// The Instance Metadata Service answers a virtual machine and nothing
	// else, about that machine: a request is the machine's when it comes from
	// the address its network interface holds. Every read requires the
	// `Metadata: true` header.
	registerAzureInstanceAttestation(srv)
	srv.HandleFunc("GET /metadata/versions", handleAzureIMDSVersions)
	srv.HandleFunc("GET /metadata/instance", handleAzureIMDSInstance)
	srv.HandleFunc("GET /metadata/instance/{route...}", handleAzureIMDSInstance)
	srv.HandleFunc("GET /metadata/loadbalancer", handleAzureIMDSLoadBalancer)
	srv.HandleFunc("GET /metadata/loadbalancer/{route...}", handleAzureIMDSLoadBalancer)
	registerAzureScheduledEvents(srv)
}

// handleAzureIMDSVersions lists the supported API versions, oldest first; the
// endpoint takes no version itself.
func handleAzureIMDSVersions(w http.ResponseWriter, r *http.Request) {
	if !mustMetadataHeader(w, r) {
		return
	}
	if _, ok := azureIMDSRequest(w, r); !ok {
		return
	}
	versions := make([]string, len(azureIMDSVersions))
	for i, version := range azureIMDSVersions {
		versions[len(versions)-1-i] = version
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"apiVersions": versions})
}

// azureIMDSRequest admits an instance metadata read: one carrying the
// required header, from a virtual machine, answered from the machine's
// record as it is now.
func azureIMDSRequest(w http.ResponseWriter, r *http.Request) (azureMetadataVM, bool) {
	if !mustMetadataHeader(w, r) {
		return azureMetadataVM{}, false
	}
	booted, ok := azureIMDSMachine(r)
	if !ok {
		sim.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
		return azureMetadataVM{}, false
	}
	return booted, true
}

// azureIMDSMachine is the virtual machine a metadata request comes from, with
// its machine and interface records as they are now.
func azureIMDSMachine(r *http.Request) (azureMetadataVM, bool) {
	booted, ok := azureMetadataVMsByIP.ForRequest(r)
	if !ok {
		return azureMetadataVM{}, false
	}
	if vm, found := azureVMs.Get(booted.VM.ID); found {
		booted.VM = vm
	}
	if nic, found := azureNICs.Get(booted.NIC.ID); found {
		booted.NIC = nic
	}
	return booted, true
}

// azureIMDSCompute is the compute section of the instance document.
func azureIMDSCompute(vm azureMetadataVM) map[string]any {
	publisher, offer, sku, version := "", "", "", ""
	if ref := azureVMImageReference(vm.VM); ref != nil {
		publisher, _ = ref["publisher"].(string)
		offer, _ = ref["offer"].(string)
		sku, _ = ref["sku"].(string)
		version, _ = ref["version"].(string)
	}
	size, _ := vm.VM.Properties.HardwareProfile["vmSize"].(string)
	tagKeys := make([]string, 0, len(vm.VM.Tags))
	for key := range vm.VM.Tags {
		tagKeys = append(tagKeys, key)
	}
	sort.Strings(tagKeys)
	tags := make([]string, 0, len(tagKeys))
	tagsList := make([]map[string]string, 0, len(tagKeys))
	for _, key := range tagKeys {
		tags = append(tags, key+":"+vm.VM.Tags[key])
		tagsList = append(tagsList, map[string]string{"name": key, "value": vm.VM.Tags[key]})
	}
	adminUsername, _ := vm.VM.Properties.OSProfile["adminUsername"].(string)
	linux, _ := vm.VM.Properties.OSProfile["linuxConfiguration"].(map[string]any)
	disablePassword, _ := linux["disablePasswordAuthentication"].(bool)
	ssh, _ := linux["ssh"].(map[string]any)
	keys, _ := ssh["publicKeys"].([]any)
	publicKeys := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		entry, _ := k.(map[string]any)
		keyData, _ := entry["keyData"].(string)
		path, _ := entry["path"].(string)
		publicKeys = append(publicKeys, map[string]string{"keyData": keyData, "path": path})
	}
	return map[string]any{
		"azEnvironment": "AzurePublicCloud",
		"location":      vm.VM.Location,
		"name":          vm.VM.Name,
		"offer":         offer,
		"osProfile": map[string]any{
			"adminUsername":                 adminUsername,
			"computerName":                  azureVMComputerName(vm.VM),
			"disablePasswordAuthentication": strconv.FormatBool(disablePassword),
		},
		"osType":               "Linux",
		"placementGroupId":     "",
		"platformFaultDomain":  "0",
		"platformUpdateDomain": "0",
		"provider":             "Microsoft.Compute",
		"publicKeys":           publicKeys,
		"publisher":            publisher,
		"resourceGroupName":    azureResourceGroupFromID(vm.VM.ID, ""),
		"resourceId":           vm.VM.ID,
		"sku":                  sku,
		"subscriptionId":       azureSubscriptionFromID(vm.VM.ID, ""),
		"tags":                 strings.Join(tags, ";"),
		"tagsList":             tagsList,
		"userData":             vm.VM.Properties.UserData,
		"version":              version,
		"vmId":                 vm.VM.Properties.VMID,
		"vmScaleSetName":       "",
		"vmSize":               size,
		"zone":                 "",
	}
}

// azureIMDSNetwork is the network section of the instance document: the
// machine's interface, the private and public address of each of its IP
// configurations, and its subnet.
func azureIMDSNetwork(vm azureMetadataVM) map[string]any {
	addresses := []map[string]any{}
	for _, ipcfg := range azureIMDSIPConfigurations(vm.NIC) {
		addresses = append(addresses, map[string]any{
			"privateIpAddress": ipcfg.Properties.PrivateIPAddress,
			"publicIpAddress":  azureIPConfigPublicAddress(ipcfg),
		})
	}
	var subnets []map[string]any
	if subnet, ok := azureSubnets.Get(vm.SubnetID); ok {
		if address, prefix, ok := strings.Cut(subnet.Properties.AddressPrefix, "/"); ok {
			subnets = append(subnets, map[string]any{"address": address, "prefix": prefix})
		}
	}
	return map[string]any{
		"interface": []map[string]any{{
			"ipv4": map[string]any{
				"ipAddress": addresses,
				"subnet":    subnets,
			},
			"ipv6":       map[string]any{"ipAddress": []any{}},
			"macAddress": strings.ReplaceAll(vm.NIC.Properties.MacAddress, "-", ""),
		}},
	}
}

// azureIMDSIPConfigurations lists an interface's IP configurations, the
// primary first, then each secondary that holds a private address.
func azureIMDSIPConfigurations(nic NetworkInterface) []NetworkInterfaceIPConfiguration {
	if len(nic.Properties.IPConfigurations) == 0 {
		return nil
	}
	primary := azurePrimaryIPConfigIndex(nic)
	out := []NetworkInterfaceIPConfiguration{nic.Properties.IPConfigurations[primary]}
	for i, ipcfg := range nic.Properties.IPConfigurations {
		if i != primary && ipcfg.Properties.PrivateIPAddress != "" {
			out = append(out, ipcfg)
		}
	}
	return out
}

// azureIPConfigPublicAddress is the address of the public IP address an IP
// configuration is associated with: empty when it has none, or none is
// allocated yet.
func azureIPConfigPublicAddress(ipcfg NetworkInterfaceIPConfiguration) string {
	if ipcfg.Properties.PublicIPAddress == nil || ipcfg.Properties.PublicIPAddress.ID == "" || azurePublicIPs == nil {
		return ""
	}
	pip, ok := azurePublicIPs.Get(ipcfg.Properties.PublicIPAddress.ID)
	if !ok {
		return ""
	}
	return pip.Properties.PublicIPAddress
}

func azureSubscriptionFromID(id, defaultSubscription string) string {
	parts := strings.Split(id, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "subscriptions") {
			return parts[i+1]
		}
	}
	return defaultSubscription
}

func azureResourceGroupFromID(id, defaultResourceGroup string) string {
	parts := strings.Split(id, "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "resourceGroups") {
			return parts[i+1]
		}
	}
	return defaultResourceGroup
}

// simListenAddr is the plain-HTTP listen address the workloads call back on,
// which differs from the API listener's when that one terminates TLS.
var simListenAddr string

// hostMetadataEnv returns the platform environment of an App Service or Azure
// Functions app or slot's workload, or of a workload that is neither when site
// is nil, on a network whose sidecar is at metadataHost.
func hostMetadataEnv(site *Site, metadataHost string) (map[string]string, error) {
	if site == nil {
		return workloadMetadataEnv("", nil, metadataHost)
	}
	return workloadMetadataEnv(site.ID, site.Identity, metadataHost)
}

// workloadMetadataEnv returns the platform environment of the workload of
// resource resourceID: for a workload with a managed identity, the
// IDENTITY_ENDPOINT and IDENTITY_HEADER its code acquires the identity's
// tokens with, served by the sidecar of the workload's network at
// metadataHost, which forwards them to the simulator with the workload's own
// address. A workload with no identity gets neither, as on App Service and
// Container Apps, and none reaches the Instance Metadata Service, which
// answers virtual machines only.
func workloadMetadataEnv(resourceID string, identity *SiteIdentity, metadataHost string) (map[string]string, error) {
	if metadataHost == "" {
		return nil, fmt.Errorf("workload %s: no sidecar serves its metadata", resourceID)
	}
	env := map[string]string{}
	if hasManagedIdentity(identity) {
		header, err := workloadIdentityHeader(resourceID)
		if err != nil {
			return nil, err
		}
		env["IDENTITY_ENDPOINT"] = "http://" + metadataHost + "/msi/token"
		env["IDENTITY_HEADER"] = header
	}
	return env, nil
}

// mustMetadataHeader enforces the header every instance-metadata read requires.
// A request without it is one a browser could have been tricked into making,
// which is exactly what the header exists to stop.
func mustMetadataHeader(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Metadata") != "true" {
		http.Error(w, "Required Metadata: true header missing", http.StatusBadRequest)
		return false
	}
	return true
}

// The two attestation reads the instance metadata service serves beside the
// instance document.
//
// Attested_GetDocument answers with a signed statement of the instance's own
// identity — the document a workload hands a relying party to prove which
// machine it is running on. The signature is real: it is made with the
// simulator's own signing key, the same key it signs its tokens with, so a
// client that trusts this deployment can verify it. That is the coordinate
// difference between this and Azure, whose key chains to a Microsoft root.
//
// Identity_GetInfo answers with the tenant the instance's managed identity
// belongs to, which is the directory this simulator issues that identity from.
func registerAzureInstanceAttestation(srv *sim.Server) {
	srv.HandleFunc("GET /metadata/attested/document", func(w http.ResponseWriter, r *http.Request) {
		if !mustMetadataHeader(w, r) {
			return
		}
		if _, ok := azureIMDSQuery(w, r, azureIMDSAttestedVersions); !ok {
			return
		}
		// The nonce is the caller's replay protection: it signs what the caller
		// asked to have signed, so a document minted for one challenge cannot
		// answer another.
		nonce := r.URL.Query().Get("nonce")
		signature, err := azureSignAttestedDocument(r, nonce)
		if errors.Is(err, errNotAVirtualMachine) {
			sim.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "Not found"})
			return
		}
		if err != nil {
			AzureError(w, "InternalServerError",
				"Could not sign the attested document: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"signature": signature,
			"encoding":  "pkcs7",
		})
	})

	// Identity_GetInfo: the tenant of the requesting machine's system-assigned
	// identity.
	srv.HandleFunc("GET /metadata/identity/info", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata") != "true" {
			imdsIdentityError(w, http.StatusBadRequest, "bad_request_102", "Required metadata header not specified")
			return
		}
		vm, ok := azureIMDSMachine(r)
		if !ok || !vm.VM.Identity.systemAssigned() {
			imdsIdentityError(w, http.StatusBadRequest, "invalid_request", "Identity not found")
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"tenantId": vm.VM.Identity.TenantID})
	})
}

// errNotAVirtualMachine is a metadata request from an address no virtual
// machine holds.
var errNotAVirtualMachine = errors.New("the caller is no virtual machine")

// azureSignAttestedDocument signs the instance's identity, with the caller's
// nonce inside the signed content so the document answers that challenge only.
func azureSignAttestedDocument(r *http.Request, nonce string) (string, error) {
	signer, err := azureSimSigner()
	if err != nil {
		return "", err
	}
	// The instance being attested is the one the request reached, resolved the
	// same way the instance document resolves it — a document attesting some
	// other machine would prove nothing about this one.
	vmMeta, ok := azureMetadataVMsByIP.ForRequest(r)
	if !ok {
		return "", errNotAVirtualMachine
	}
	subscription, vmID := azureSubscriptionFromID(vmMeta.VM.ID, ""), vmMeta.VM.Properties.VMID
	document, err := json.Marshal(map[string]any{
		"nonce":          nonce,
		"plan":           map[string]any{"name": "", "product": "", "publisher": ""},
		"subscriptionId": subscription,
		"vmId":           vmID,
		"timeStamp": map[string]any{
			"createdOn": time.Now().UTC().Format("01/02/06 15:04:05 -0700"),
			"expiresOn": time.Now().UTC().Add(24 * time.Hour).Format("01/02/06 15:04:05 -0700"),
		},
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(document)
	signed, err := signer.Key().Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", err
	}
	// The document travels with its signature, because a verifier needs both:
	// the statement being attested and the proof it was this deployment that
	// made it.
	return base64.StdEncoding.EncodeToString(append(append(document, '.'), signed...)), nil
}
