package main

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
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

	// Azure IMDS instance metadata. Real Azure exposes	//
	//   GET http://169.254.169.254/metadata/instance?api-version=2021-02-01
	//
	// returning a {compute, network} document used by:
	//   - DefaultAzureCredential's IMDS probe.
	//   - Workloads that read `compute.subscriptionId`, `compute.location`,
	//     `compute.azEnvironment` for self-discovery.
	// All reads require `Metadata: true` request header.
	registerAzureInstanceAttestation(srv)
	srv.HandleFunc("GET /metadata/instance", func(w http.ResponseWriter, r *http.Request) {
		if !mustMetadataHeader(w, r) {
			return
		}
		sub := r.URL.Query().Get("subscriptionId")
		if sub == "" {
			sub = "00000000-0000-0000-0000-000000000001"
		}
		loc := r.URL.Query().Get("location")
		if loc == "" {
			loc = "westeurope"
		}
		vmMeta, ok := azureMetadataVMsByIP.ForRequest(r)
		if ok {
			sub = azureSubscriptionFromID(vmMeta.VM.ID, sub)
			loc = vmMeta.VM.Location
		}
		computeName := "sim-vm-1"
		resourceGroup := "sim-rg"
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/sim-rg/providers/Microsoft.Compute/virtualMachines/sim-vm-1", sub)
		vmID := "sim-vm-id-0001"
		vmSize := "Standard_DS1_v2"
		privateIP := "10.0.0.4"
		macAddress := "00155DEADBEE"
		subnetAddress := "10.0.0.0"
		subnetPrefix := "24"
		var secondaryIPs []string
		if ok {
			computeName = vmMeta.VM.Name
			resourceGroup = azureResourceGroupFromID(vmMeta.VM.ID, resourceGroup)
			resourceID = vmMeta.VM.ID
			if vmMeta.VM.Properties.VMID != "" {
				vmID = vmMeta.VM.Properties.VMID
			}
			if size, _ := vmMeta.VM.Properties.HardwareProfile["vmSize"].(string); size != "" {
				vmSize = size
			}
			if vmMeta.NIC.Properties.MacAddress != "" {
				macAddress = strings.ReplaceAll(vmMeta.NIC.Properties.MacAddress, "-", "")
			}
			nic := vmMeta.NIC
			if stored, found := azureNICs.Get(nic.ID); found {
				nic = stored
			}
			if len(nic.Properties.IPConfigurations) > 0 {
				privateIP = azurePrimaryIPConfig(nic).Properties.PrivateIPAddress
				for _, secondary := range azureNICSecondaryAddresses(nic) {
					secondaryIPs = append(secondaryIPs, secondary.String())
				}
			}
			if subnet, ok := azureSubnets.Get(vmMeta.SubnetID); ok {
				subnetAddress, subnetPrefix = azureCIDRAddressPrefix(subnet.Properties.AddressPrefix, subnetAddress, subnetPrefix)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"compute": map[string]any{
				"azEnvironment":        "AzurePublicCloud",
				"location":             loc,
				"name":                 computeName,
				"offer":                "UbuntuServer",
				"osType":               "Linux",
				"placementGroupId":     "",
				"platformFaultDomain":  "0",
				"platformUpdateDomain": "0",
				"provider":             "Microsoft.Compute",
				"publisher":            "Canonical",
				"resourceGroupName":    resourceGroup,
				"resourceId":           resourceID,
				"sku":                  "22_04-lts",
				"subscriptionId":       sub,
				"tags":                 "",
				"version":              "22.04.202401010",
				"vmId":                 vmID,
				"vmScaleSetName":       "",
				"vmSize":               vmSize,
				"zone":                 "1",
			},
			"network": map[string]any{
				"interface": []map[string]any{{
					"ipv4": map[string]any{
						"ipAddress": azureIMDSIPAddresses(privateIP, secondaryIPs),
						"subnet": []map[string]any{{
							"address": subnetAddress,
							"prefix":  subnetPrefix,
						}},
					},
					"macAddress": macAddress,
				}},
			},
		})
	})
	srv.HandleFunc("GET /metadata/instance/compute", func(w http.ResponseWriter, r *http.Request) {
		if !mustMetadataHeader(w, r) {
			return
		}
		if vmMeta, ok := azureMetadataVMsByIP.ForRequest(r); ok {
			sub := azureSubscriptionFromID(vmMeta.VM.ID, "00000000-0000-0000-0000-000000000001")
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"location":          vmMeta.VM.Location,
				"subscriptionId":    sub,
				"resourceGroupName": azureResourceGroupFromID(vmMeta.VM.ID, "sim-rg"),
				"name":              vmMeta.VM.Name,
				"vmId":              vmMeta.VM.Properties.VMID,
				"azEnvironment":     "AzurePublicCloud",
			})
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"location":          "westeurope",
			"subscriptionId":    "00000000-0000-0000-0000-000000000001",
			"resourceGroupName": "sim-rg",
			"name":              "sim-vm-1",
			"vmId":              "sim-vm-id-0001",
			"azEnvironment":     "AzurePublicCloud",
		})
	})
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

func azureCIDRAddressPrefix(cidr, defaultAddress, defaultPrefix string) (string, string) {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return defaultAddress, defaultPrefix
	}
	return parts[0], parts[1]
}

// simListenAddr is the listen address main() serves on.
var simListenAddr string

// hostMetadataEnv returns the platform environment of a workload: the
// instance metadata endpoint, and for an App Service or Azure Functions app or
// slot with a managed identity the IDENTITY_ENDPOINT and IDENTITY_HEADER its
// code acquires the identity's tokens with. A workload with no identity gets
// neither, as on App Service.
func hostMetadataEnv(site *Site) (map[string]string, error) {
	addr, err := workloadhost.CallbackAddr(simListenAddr)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"AZURE_INSTANCE_METADATA_ENDPOINT": "http://" + addr + "/metadata/instance",
	}
	if siteHasManagedIdentity(site) {
		header, err := workloadIdentityHeader(site.ID)
		if err != nil {
			return nil, err
		}
		env["IDENTITY_ENDPOINT"] = "http://" + addr + "/msi/token"
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
		// The nonce is the caller's replay protection: it signs what the caller
		// asked to have signed, so a document minted for one challenge cannot
		// answer another.
		nonce := r.URL.Query().Get("nonce")
		signature, err := azureSignAttestedDocument(r, nonce)
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

	srv.HandleFunc("GET /metadata/identity/info", func(w http.ResponseWriter, r *http.Request) {
		if !mustMetadataHeader(w, r) {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"tenantId": simTenantID})
	})
}

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
	subscription, vmID := "00000000-0000-0000-0000-000000000001", "sim-vm-id-0001"
	if vmMeta, ok := azureMetadataVMsByIP.ForRequest(r); ok {
		subscription = azureSubscriptionFromID(vmMeta.VM.ID, subscription)
		vmID = vmMeta.VM.Name
	}
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

// azureIMDSIPAddresses lists an interface's private addresses the way the
// Instance Metadata Service does: the primary IP configuration's first, then
// each secondary's.
func azureIMDSIPAddresses(primary string, secondaries []string) []map[string]any {
	out := []map[string]any{{"privateIpAddress": primary, "publicIpAddress": ""}}
	for _, address := range secondaries {
		out = append(out, map[string]any{"privateIpAddress": address, "publicIpAddress": ""})
	}
	return out
}
