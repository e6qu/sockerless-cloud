package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The MACsec configuration of a Cloud Interconnect.
//
// This is the caller's own configuration read back, not a reading taken off
// Google's equipment. An interconnect carries a keychain the caller wrote — each
// pre-shared key with a name and the time it becomes valid — and the operation
// returns that keychain with the Connectivity Association Key Name (CKN) and
// key (CAK) the service generates for each entry. Generating those is what the
// operation does, so the simulator does it too, deriving each from the
// interconnect and the key it belongs to so that reading the configuration
// twice returns the same keychain.
//
// The diagnostics beside it are a different thing entirely and stay unserved:
// link status, circuit identifiers and LACP state are read off the physical
// equipment at both ends, which this simulator does not have.

// registerComputeInterconnectMacsec mounts the configuration read.
func registerComputeInterconnectMacsec(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getMacsecConfig",
		func(w http.ResponseWriter, r *http.Request) {
			name := sim.PathParam(r, "interconnect")
			key := "projects/" + sim.PathParam(r, "project") + "/global/interconnects/" + name
			held, ok := gcpComputeInterconnects.Get(key)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "interconnect %q not found", name)
				return
			}

			keys := []any{}
			macsec, _ := held["macsec"].(map[string]any)
			preShared, _ := macsec["preSharedKeys"].([]any)
			for _, entry := range preShared {
				configured, _ := entry.(map[string]any)
				keyName, _ := configured["name"].(string)
				generated := map[string]any{
					"name": keyName,
					"ckn":  computeMacsecKey("ckn", key, keyName, 32),
					"cak":  computeMacsecKey("cak", key, keyName, 16),
				}
				if startTime, given := configured["startTime"].(string); given {
					generated["startTime"] = startTime
				}
				keys = append(keys, generated)
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"result": map[string]any{"preSharedKeys": keys},
			})
		})
}

// computeMacsecKey derives one of the two generated values for a pre-shared
// key. A CKN is 32 bytes and a CAK 16, both rendered as hex, and both are
// derived from the interconnect and the key they belong to so that the
// configuration a caller reads does not change between reads.
func computeMacsecKey(kind, interconnect, keyName string, size int) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + interconnect + "\x00" + keyName))
	return hex.EncodeToString(sum[:size])
}
