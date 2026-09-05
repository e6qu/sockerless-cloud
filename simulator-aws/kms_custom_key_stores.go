package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// KMS custom key stores. A custom key store binds a KMS key store to an
// external HSM cluster (AWS CloudHSM) or an external key manager
// (EXTERNAL_KEY_STORE / XKS). The sim models the control-plane CRUD +
// connection-state machine real KMS exposes; there is no real HSM, so
// Connect/Disconnect flip ConnectionState deterministically rather than
// reaching out to a cluster.

// KMSCustomKeyStore mirrors the CustomKeyStoresListEntry wire shape.
type KMSCustomKeyStore struct {
	CustomKeyStoreId   string  `json:"CustomKeyStoreId"`
	CustomKeyStoreName string  `json:"CustomKeyStoreName"`
	CustomKeyStoreType string  `json:"CustomKeyStoreType"`
	CloudHsmClusterId  string  `json:"CloudHsmClusterId,omitempty"`
	ConnectionState    string  `json:"ConnectionState"`
	CreationDate       float64 `json:"CreationDate"`
}

var kmsCustomKeyStores sim.Store[KMSCustomKeyStore]

func registerKMSCustomKeyStores(r *AWSRouter, srv *sim.Server) {
	kmsCustomKeyStores = sim.MakeStore[KMSCustomKeyStore](srv.DB(), "kms_custom_key_stores")
	r.Register("TrentService.CreateCustomKeyStore", handleKMSCreateCustomKeyStore)
	r.Register("TrentService.DescribeCustomKeyStores", handleKMSDescribeCustomKeyStores)
	r.Register("TrentService.ConnectCustomKeyStore", handleKMSConnectCustomKeyStore)
	r.Register("TrentService.DisconnectCustomKeyStore", handleKMSDisconnectCustomKeyStore)
	r.Register("TrentService.UpdateCustomKeyStore", handleKMSUpdateCustomKeyStore)
	r.Register("TrentService.DeleteCustomKeyStore", handleKMSDeleteCustomKeyStore)
}

func handleKMSCreateCustomKeyStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreName string `json:"CustomKeyStoreName"`
		CustomKeyStoreType string `json:"CustomKeyStoreType"`
		CloudHsmClusterId  string `json:"CloudHsmClusterId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.CustomKeyStoreName == "" {
		AWSError(w, "ValidationException", "CustomKeyStoreName is required", http.StatusBadRequest)
		return
	}
	for _, ks := range kmsCustomKeyStores.List() {
		if ks.CustomKeyStoreName == req.CustomKeyStoreName {
			AWSErrorf(w, "CustomKeyStoreNameInUseException", http.StatusBadRequest,
				"A custom key store named %q already exists.", req.CustomKeyStoreName)
			return
		}
	}
	cksType := req.CustomKeyStoreType
	if cksType == "" {
		cksType = "AWS_CLOUDHSM"
	}
	id := "cks-" + generateUUID()
	ks := KMSCustomKeyStore{
		CustomKeyStoreId:   id,
		CustomKeyStoreName: req.CustomKeyStoreName,
		CustomKeyStoreType: cksType,
		CloudHsmClusterId:  req.CloudHsmClusterId,
		// Real KMS creates a custom key store in the DISCONNECTED state; the
		// operator must call ConnectCustomKeyStore to bring it online.
		ConnectionState: "DISCONNECTED",
		CreationDate:    float64(time.Now().Unix()),
	}
	kmsCustomKeyStores.Put(id, ks)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"CustomKeyStoreId": id,
	})
}

func handleKMSDescribeCustomKeyStores(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreId   string `json:"CustomKeyStoreId"`
		CustomKeyStoreName string `json:"CustomKeyStoreName"`
		Limit              int    `json:"Limit"`
		Marker             string `json:"Marker"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	var matched []map[string]any
	for _, ks := range kmsCustomKeyStores.List() {
		if req.CustomKeyStoreId != "" && ks.CustomKeyStoreId != req.CustomKeyStoreId {
			continue
		}
		if req.CustomKeyStoreName != "" && ks.CustomKeyStoreName != req.CustomKeyStoreName {
			continue
		}
		entry := map[string]any{
			"CustomKeyStoreId":   ks.CustomKeyStoreId,
			"CustomKeyStoreName": ks.CustomKeyStoreName,
			"CustomKeyStoreType": ks.CustomKeyStoreType,
			"ConnectionState":    ks.ConnectionState,
			"CreationDate":       ks.CreationDate,
		}
		if ks.CloudHsmClusterId != "" {
			entry["CloudHsmClusterId"] = ks.CloudHsmClusterId
		}
		matched = append(matched, entry)
	}
	if (req.CustomKeyStoreId != "" || req.CustomKeyStoreName != "") && len(matched) == 0 {
		AWSErrorf(w, "CustomKeyStoreNotFoundException", http.StatusBadRequest,
			"No custom key store matched the request.")
		return
	}
	page, next := awsPageExplicit(matched, req.Marker, req.Limit)
	resp := map[string]any{"CustomKeyStores": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// kmsResolveCustomKeyStore looks up a custom key store by id, writing the
// canonical not-found error on miss.
func kmsResolveCustomKeyStore(w http.ResponseWriter, id string) (KMSCustomKeyStore, bool) {
	ks, ok := kmsCustomKeyStores.Get(id)
	if !ok {
		AWSErrorf(w, "CustomKeyStoreNotFoundException", http.StatusBadRequest,
			"Custom key store %q does not exist.", id)
		return KMSCustomKeyStore{}, false
	}
	return ks, true
}

func handleKMSConnectCustomKeyStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreId string `json:"CustomKeyStoreId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := kmsResolveCustomKeyStore(w, req.CustomKeyStoreId); !ok {
		return
	}
	kmsCustomKeyStores.Update(req.CustomKeyStoreId, func(ks *KMSCustomKeyStore) {
		ks.ConnectionState = "CONNECTED"
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKMSDisconnectCustomKeyStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreId string `json:"CustomKeyStoreId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if _, ok := kmsResolveCustomKeyStore(w, req.CustomKeyStoreId); !ok {
		return
	}
	kmsCustomKeyStores.Update(req.CustomKeyStoreId, func(ks *KMSCustomKeyStore) {
		ks.ConnectionState = "DISCONNECTED"
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKMSUpdateCustomKeyStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreId      string `json:"CustomKeyStoreId"`
		NewCustomKeyStoreName string `json:"NewCustomKeyStoreName"`
		CloudHsmClusterId     string `json:"CloudHsmClusterId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	ks, ok := kmsResolveCustomKeyStore(w, req.CustomKeyStoreId)
	if !ok {
		return
	}
	// Real KMS requires the custom key store to be DISCONNECTED before update.
	if ks.ConnectionState != "DISCONNECTED" {
		AWSErrorf(w, "CustomKeyStoreInvalidStateException", http.StatusBadRequest,
			"The custom key store must be DISCONNECTED to update it.")
		return
	}
	kmsCustomKeyStores.Update(req.CustomKeyStoreId, func(k *KMSCustomKeyStore) {
		if req.NewCustomKeyStoreName != "" {
			k.CustomKeyStoreName = req.NewCustomKeyStoreName
		}
		if req.CloudHsmClusterId != "" {
			k.CloudHsmClusterId = req.CloudHsmClusterId
		}
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleKMSDeleteCustomKeyStore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CustomKeyStoreId string `json:"CustomKeyStoreId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	ks, ok := kmsResolveCustomKeyStore(w, req.CustomKeyStoreId)
	if !ok {
		return
	}
	if ks.ConnectionState == "CONNECTED" {
		AWSErrorf(w, "CustomKeyStoreInvalidStateException", http.StatusBadRequest,
			"The custom key store must be DISCONNECTED before deletion.")
		return
	}
	kmsCustomKeyStores.Delete(req.CustomKeyStoreId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
