package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A subnet declared inline on a virtual network is created, not dropped.
//
// Real Azure creates the subnets supplied in the `subnets` member of a virtual
// network PUT — it is what `az network vnet create --subnet-name` sends — and
// the simulator only re-collected rows that already existed, so an inline
// subnet was silently dropped and a later read 404ed while the create had
// answered 200.
//
// The proof splits by what the host can realize, and neither half skips:
//
//   - A capable Linux host materializes the subnet's netns fabric and serves
//     the subnet back, exactly as its standalone PUT would.
//   - A host without the netns capabilities refuses the whole request loudly —
//     the same 503 the standalone subnet PUT answers — because a 200 that
//     dropped the subnet is precisely the defect. The one sanctioned skip
//     shape, a kernel capability the host cannot install, decides which half
//     runs; both halves assert, so no host answers green while proving
//     nothing.
func TestVirtualNetworkCreatesItsInlineSubnets(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	t.Cleanup(AwaitAzureAsyncOperations)

	now := time.Now().UTC()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint an ARM token: %v", err)
	}
	arm := func(method, target, body string) *httptest.ResponseRecorder {
		var reader *strings.Reader
		if body != "" {
			reader = strings.NewReader(body)
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	const base = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/inline-subnet-rg"
	if rec := arm(http.MethodPut, base+"?api-version=2023-07-01", `{"location":"eastus"}`); rec.Code >= 300 {
		t.Fatalf("create resource group: status %d: %s", rec.Code, rec.Body.String())
	}

	vnetURL := base + "/providers/Microsoft.Network/virtualNetworks/inline-vnet?api-version=2025-03-01"
	subnetURL := base + "/providers/Microsoft.Network/virtualNetworks/inline-vnet/subnets/inline-subnet?api-version=2025-03-01"
	payload := `{"location":"eastus","properties":{
		"addressSpace":{"addressPrefixes":["10.70.0.0/16"]},
		"subnets":[{"name":"inline-subnet","properties":{"addressPrefix":"10.70.1.0/24"}}]}}`

	created := arm(http.MethodPut, vnetURL, payload)

	if realexec.DetectNetworkCapabilities().Require() != nil {
		// This host cannot realize the netns fabric, and a compute subnet —
		// inline or standalone — is refused rather than half-created. The old
		// behavior was the bug: 200, with the subnet silently gone.
		if created.Code != http.StatusServiceUnavailable {
			t.Fatalf("an inline compute subnet on an incapable host must be refused: status %d: %s",
				created.Code, created.Body.String())
		}
		if !strings.Contains(created.Body.String(), "OperationNotAllowed") {
			t.Fatalf("the refusal must be the standalone subnet PUT's: %s", created.Body.String())
		}
		return
	}

	// A capable host serves the network with its subnet embedded, and the
	// subnet stands on its own resource exactly as if its standalone PUT had
	// created it.
	if created.Code != http.StatusOK {
		t.Fatalf("create the network with its inline subnet: status %d: %s", created.Code, created.Body.String())
	}
	var vnet struct {
		Properties struct {
			Subnets []struct {
				Name       string `json:"name"`
				Properties struct {
					AddressPrefix string `json:"addressPrefix"`
				} `json:"properties"`
			} `json:"subnets"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &vnet); err != nil {
		t.Fatalf("decode the network: %v: %s", err, created.Body.String())
	}
	if len(vnet.Properties.Subnets) != 1 || vnet.Properties.Subnets[0].Name != "inline-subnet" {
		t.Fatalf("the response must embed the inline subnet: %s", created.Body.String())
	}
	if got := vnet.Properties.Subnets[0].Properties.AddressPrefix; got != "10.70.1.0/24" {
		t.Fatalf("the inline subnet must keep its addressPrefix, got %q", got)
	}

	read := arm(http.MethodGet, subnetURL, "")
	if read.Code != http.StatusOK {
		t.Fatalf("the inline subnet must be readable at its own resource: status %d: %s",
			read.Code, read.Body.String())
	}
	if !strings.Contains(read.Body.String(), `"10.70.1.0/24"`) {
		t.Fatalf("the standalone read must carry the declared prefix: %s", read.Body.String())
	}
}
