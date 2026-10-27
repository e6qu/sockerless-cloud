package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The MACsec configuration of a Cloud Interconnect is the caller's keychain
// read back, with the Connectivity Association Key Name (CKN) and key (CAK)
// the service generates for each pre-shared key. The simulator derives both
// from the interconnect and the key, so repeated reads return the same
// keychain.

func registerComputeInterconnectMacsec(srv *sim.Server) {
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getMacsecConfig",
		func(w http.ResponseWriter, r *http.Request) {
			name := sim.PathParam(r, "interconnect")
			key := "projects/" + sim.PathParam(r, "project") + "/global/interconnects/" + name
			held, ok := gcpComputeInterconnects.Get(key)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "interconnect %q not found", name)
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

// computeMacsecKey derives a pre-shared key's 32-byte CKN or 16-byte CAK, as
// hex, from the interconnect and the key, so reads return the same value.
func computeMacsecKey(kind, interconnect, keyName string, size int) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + interconnect + "\x00" + keyName))
	return hex.EncodeToString(sum[:size])
}
