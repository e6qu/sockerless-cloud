package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

type azureMetadataVM struct {
	VM       VirtualMachine
	NIC      NetworkInterface
	SubnetID string
}

var azureMetadataVMsByIP sync.Map // map[string]azureMetadataVM

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
		keyVaultSuffix := azureEndpointSuffix(azureKeyVaultEndpointURL(r, "metadatavault"), "metadatavault", "vault")

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
	mustMetadataHeader := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Metadata") != "true" {
			http.Error(w, "Required Metadata: true header missing", http.StatusBadRequest)
			return false
		}
		return true
	}
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
		vmMeta, ok := azureMetadataVMForRequest(r)
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
			if len(vmMeta.NIC.Properties.IPConfigurations) > 0 {
				privateIP = vmMeta.NIC.Properties.IPConfigurations[0].Properties.PrivateIPAddress
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
						"ipAddress": []map[string]any{{
							"privateIpAddress": privateIP,
							"publicIpAddress":  "",
						}},
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
		if vmMeta, ok := azureMetadataVMForRequest(r); ok {
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

func azureMetadataVMForRequest(r *http.Request) (azureMetadataVM, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	v, ok := azureMetadataVMsByIP.Load(host)
	if !ok {
		return azureMetadataVM{}, false
	}
	vm, ok := v.(azureMetadataVM)
	return vm, ok
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

// simListenAddr is captured by main() so host translators can wire it
// into workload-host env.
var simListenAddr string

func simHostMetadataAddr() string {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	return workloadCallbackHost() + ":" + port
}

func simHostMetadataPort() (int, error) {
	port := simListenAddr
	if idx := strings.LastIndex(simListenAddr, ":"); idx >= 0 {
		port = simListenAddr[idx+1:]
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, fmt.Errorf("invalid simulator metadata listen port %q", port)
	}
	return n, nil
}

// hostMetadataExtraHosts returns ExtraHosts entries needed for Docker
// workloads to resolve host.docker.internal to the sim's host gateway.
// Real Azure IMDS uses 169.254.169.254 (a link-local IP); workloads
// that hard-code that address need a routing override which Linux
// Docker can't easily express. The Azure SDK respects IDENTITY_ENDPOINT
// + IDENTITY_HEADER + AZURE_INSTANCE_METADATA_ENDPOINT for redirection,
// so SDK-based workloads route via env without needing the link-local.
func hostMetadataExtraHosts() []string {
	host := workloadCallbackHost()
	if host != "host.docker.internal" && host != "host.containers.internal" {
		return nil
	}
	info := strings.ToLower(sim.RuntimeInfo())
	if strings.Contains(info, "podman") {
		if ip := podmanMachineHostIPv4(); ip != "" {
			return []string{
				"host.containers.internal:" + ip,
				"host.docker.internal:" + ip,
			}
		}
		return nil
	}
	return []string{"host.docker.internal:host-gateway"}
}

// hostMetadataEnv returns env vars to inject on every Azure workload
// host so the Azure SDKs route metadata + identity reads to the sim.
// Apply on every ACA / AZF / App Service workload host.
func hostMetadataEnv() map[string]string {
	addr := simHostMetadataAddr()
	return map[string]string{
		// DefaultAzureCredential picks up these two for managed-identity
		// token acquisition (App Service / Container Apps style).
		"IDENTITY_ENDPOINT": "http://" + addr + "/msi/token",
		"IDENTITY_HEADER":   "sim-identity-header",
		// Azure SDK respects this for IMDS instance metadata routing.
		"AZURE_INSTANCE_METADATA_ENDPOINT": "http://" + addr + "/metadata/instance",
	}
}

func workloadCallbackHost() string {
	if runningInsideContainer() {
		if host := firstNonLoopbackIPv4(); host != "" {
			return host
		}
	}
	if strings.Contains(strings.ToLower(sim.RuntimeInfo()), "podman") {
		return "host.containers.internal"
	}
	return "host.docker.internal"
}

func podmanMachineHostIPv4() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "podman", "machine", "ssh", "--", "ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	return parsePodmanMachineHostIPv4(string(out))
}

func parsePodmanMachineHostIPv4(route string) string {
	for _, line := range strings.Split(route, "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "src" {
				continue
			}
			ip := net.ParseIP(fields[i+1]).To4()
			if ip == nil {
				continue
			}
			// Podman machine user-mode networking exposes the macOS host at
			// the final usable address on the VM's host subnet.
			return net.IPv4(ip[0], ip[1], ip[2], 254).String()
		}
	}
	return ""
}

func runningInsideContainer() bool {
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("container") != ""
}

func firstNonLoopbackIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	return ""
}

// mergeEnv returns a new map with all keys from `base` and `extra`,
// where `extra` wins on conflict. Both inputs may be nil.
func mergeEnv(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
