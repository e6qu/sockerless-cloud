package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A Log Analytics workspace's shared keys.
//
// The keys are the workspace's own: an agent signs its ingestion with one of
// them, so they have to be a pair this workspace holds rather than a constant.
// They are minted when the workspace is first asked for them and kept until
// something replaces them, which is what makes a regeneration observable — the
// keys read back afterwards are not the keys read back before.

// monitorWorkspaceKeys is one workspace's key pair.
type monitorWorkspaceKeys struct {
	WorkspaceID string `json:"workspaceId"`
	Primary     string `json:"primarySharedKey"`
	Secondary   string `json:"secondarySharedKey"`
}

var (
	monitorSharedKeys sim.Store[monitorWorkspaceKeys]
	// monitorWorkspaces is a handle on the workspace store, so the key
	// operations refuse a workspace the subscription does not hold.
	monitorWorkspaces sim.Store[Workspace]
)

// registerMonitorSharedKeys mounts the regeneration beside the read. The read
// itself lives with the rest of the workspace surface in monitor.go and calls
// monitorKeysFor, so both answer from the one pair.
func registerMonitorSharedKeys(srv *sim.Server, armBase string) {
	monitorSharedKeys = sim.MakeStore[monitorWorkspaceKeys](srv.DB(), "monitor_workspace_shared_keys")

	srv.HandleFunc("POST "+armBase+"/workspaces/{workspaceName}/regenerateSharedKey",
		func(w http.ResponseWriter, r *http.Request) {
			resourceID, ok := monitorRequireWorkspace(w, r)
			if !ok {
				return
			}
			// Azure regenerates both keys of the pair here: the operation takes
			// no argument naming one, so there is none to name.
			keys := monitorWorkspaceKeys{
				WorkspaceID: resourceID,
				Primary:     monitorMintSharedKey(),
				Secondary:   monitorMintSharedKey(),
			}
			monitorSharedKeys.Put(resourceID, keys)
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"primarySharedKey":   keys.Primary,
				"secondarySharedKey": keys.Secondary,
			})
		})
}

// monitorRequireWorkspace resolves the addressed workspace, writing the ARM 404
// when the subscription holds no such workspace.
func monitorRequireWorkspace(w http.ResponseWriter, r *http.Request) (string, bool) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "workspaceName")
	resourceID := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s",
		sub, rg, name)
	if monitorWorkspaces == nil {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.",
			name, rg)
		return "", false
	}
	if _, ok := monitorWorkspaces.Get(resourceID); !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"The Resource 'Microsoft.OperationalInsights/workspaces/%s' under resource group '%s' was not found.",
			name, rg)
		return "", false
	}
	return resourceID, true
}

// monitorKeysFor reads a workspace's key pair, minting one the first time it is
// asked for. A workspace has keys from the moment it exists; this is where they
// come into being for a simulator that does not pre-generate them.
func monitorKeysFor(resourceID string) monitorWorkspaceKeys {
	if held, ok := monitorSharedKeys.Get(resourceID); ok {
		return held
	}
	keys := monitorWorkspaceKeys{
		WorkspaceID: resourceID,
		Primary:     monitorMintSharedKey(),
		Secondary:   monitorMintSharedKey(),
	}
	monitorSharedKeys.Put(resourceID, keys)
	return keys
}

// monitorMintSharedKey mints one key. A Log Analytics shared key is 64 random
// bytes in base64, which is what an agent signs with.
func monitorMintSharedKey() string {
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		// crypto/rand does not fail on any platform this runs on; a key that
		// could not be minted must not silently become a predictable one.
		panic("mint Log Analytics shared key: " + err.Error())
	}
	return base64.StdEncoding.EncodeToString(raw)
}
