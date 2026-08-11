package main

import (
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// KMS multi-region keys, grant retirement, and last-usage. ReplicateKey /
// UpdatePrimaryRegion implement the multi-region key (MRK) lifecycle;
// RetireGrant / ListRetirableGrants the grant-retirement surface; and
// GetKeyLastUsage the recently-added usage-tracking op. These layer on the
// existing key + grant stores — no new fakery.

func registerKMSMultiRegion(r *sim.AWSRouter, srv *sim.Server) {
	r.Register("TrentService.RetireGrant", handleKMSRetireGrant)
	r.Register("TrentService.ListRetirableGrants", handleKMSListRetirableGrants)
	r.Register("TrentService.ReplicateKey", handleKMSReplicateKey)
	r.Register("TrentService.UpdatePrimaryRegion", handleKMSUpdatePrimaryRegion)
	r.Register("TrentService.GetKeyLastUsage", handleKMSGetKeyLastUsage)
}

// kmsMultiRegionConfig builds the MultiRegionConfiguration block real KMS
// attaches to a multi-region key's metadata: the primary key's region/ARN and
// the replica regions ReplicateKey has produced.
func kmsMultiRegionConfig(key KMSKey) map[string]any {
	mrkType := "PRIMARY"
	if key.PrimaryRegion != "" && key.PrimaryRegion != awsRegion() {
		mrkType = "REPLICA"
	}
	primaryRegion := key.PrimaryRegion
	if primaryRegion == "" {
		primaryRegion = awsRegion()
	}
	replicas := make([]map[string]any, 0, len(key.Replicas))
	for _, region := range key.Replicas {
		replicas = append(replicas, map[string]any{
			"Arn":    "arn:aws:kms:" + region + ":" + awsAccountID() + ":key/" + key.KeyId,
			"Region": region,
		})
	}
	return map[string]any{
		"MultiRegionKeyType": mrkType,
		"PrimaryKey": map[string]any{
			"Arn":    "arn:aws:kms:" + primaryRegion + ":" + awsAccountID() + ":key/" + key.KeyId,
			"Region": primaryRegion,
		},
		"ReplicaKeys": replicas,
	}
}

// handleKMSRetireGrant retires a grant, identified either by GrantToken or by
// GrantId+KeyId. Retiring a grant deletes it (same effect as RevokeGrant but
// authorized via the RetiringPrincipal). Returns an empty body.
func handleKMSRetireGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GrantToken string `json:"GrantToken"`
		GrantId    string `json:"GrantId"`
		KeyId      string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.GrantToken == "" && req.GrantId == "" {
		sim.AWSError(w, "ValidationException",
			"Either GrantToken or GrantId+KeyId is required", http.StatusBadRequest)
		return
	}
	if req.GrantId != "" {
		// Retire by GrantId (+ KeyId). The KeyId, when present, must match.
		grant, ok := kmsGrants.Get(req.GrantId)
		if !ok {
			sim.AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
				"Grant %q does not exist", req.GrantId)
			return
		}
		if req.KeyId != "" {
			keyId, ok := resolveKMSKey(req.KeyId)
			if !ok || keyId != grant.KeyId {
				sim.AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
					"Grant %q is not on key %q", req.GrantId, req.KeyId)
				return
			}
		}
		kmsGrants.Delete(req.GrantId)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// Retire by GrantToken. The sim issues opaque random grant tokens that it
	// doesn't bind to a specific grant, so a token-only RetireGrant is honored
	// as a no-op success (real KMS treats an already-retired/unknown token as
	// idempotent success too).
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSListRetirableGrants returns the grants whose RetiringPrincipal
// matches the supplied principal. Reuses the ListGrantsResponse shape.
func handleKMSListRetirableGrants(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RetiringPrincipal string `json:"RetiringPrincipal"`
		Limit             int    `json:"Limit"`
		Marker            string `json:"Marker"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RetiringPrincipal == "" {
		sim.AWSError(w, "ValidationException", "RetiringPrincipal is required", http.StatusBadRequest)
		return
	}
	var matched []KMSGrant
	for _, g := range kmsGrants.List() {
		if g.RetiringPrincipal == req.RetiringPrincipal {
			matched = append(matched, g)
		}
	}
	sortBy(matched, func(g KMSGrant) string { return g.GrantId })
	page, next := awsPage(matched, req.Marker, req.Limit, 100)
	resp := map[string]any{"Grants": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleKMSReplicateKey creates a replica of a multi-region key in another
// region. The replica shares the primary's KeyId (real KMS MRK semantics) and
// returns its own KeyMetadata scoped to the replica region.
func handleKMSReplicateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId         string   `json:"KeyId"`
		ReplicaRegion string   `json:"ReplicaRegion"`
		Description   string   `json:"Description"`
		Policy        string   `json:"Policy"`
		Tags          []KMSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !key.MultiRegion {
		sim.AWSErrorf(w, "UnsupportedOperationException", http.StatusBadRequest,
			"%s is not a multi-region key.", kmsKeyArn(keyId))
		return
	}
	if req.ReplicaRegion == "" {
		sim.AWSError(w, "ValidationException", "ReplicaRegion is required", http.StatusBadRequest)
		return
	}
	// Record the replica region on the primary so its MultiRegionConfiguration
	// lists the replica.
	already := false
	for _, region := range key.Replicas {
		if region == req.ReplicaRegion {
			already = true
			break
		}
	}
	if !already {
		kmsKeys.Update(keyId, func(k *KMSKey) {
			k.Replicas = append(k.Replicas, req.ReplicaRegion)
		})
	}
	key, _ = kmsKeys.Get(keyId)
	// The replica metadata mirrors the primary but with the replica region's
	// ARN and a MultiRegionConfiguration whose type is REPLICA.
	replicaArn := "arn:aws:kms:" + req.ReplicaRegion + ":" + awsAccountID() + ":key/" + key.KeyId
	description := req.Description
	if description == "" {
		description = key.Description
	}
	replicaMD := map[string]any{
		"KeyId":        key.KeyId,
		"Arn":          replicaArn,
		"AWSAccountId": awsAccountID(),
		"CreationDate": float64(time.Now().Unix()),
		"Description":  description,
		"KeyState":     "Enabled",
		"KeyUsage":     key.KeyUsage,
		"KeyManager":   key.KeyManager,
		"Origin":       key.Origin,
		"KeySpec":      key.Spec,
		"Enabled":      true,
		"MultiRegion":  true,
		"MultiRegionConfiguration": map[string]any{
			"MultiRegionKeyType": "REPLICA",
			"PrimaryKey": map[string]any{
				"Arn":    "arn:aws:kms:" + key.PrimaryRegion + ":" + awsAccountID() + ":key/" + key.KeyId,
				"Region": key.PrimaryRegion,
			},
			"ReplicaKeys": []map[string]any{
				{"Arn": replicaArn, "Region": req.ReplicaRegion},
			},
		},
	}
	resp := map[string]any{
		"ReplicaKeyMetadata": replicaMD,
		"ReplicaPolicy":      req.Policy,
		"ReplicaTags":        req.Tags,
	}
	if req.Policy == "" {
		resp["ReplicaPolicy"] = kmsDefaultKeyPolicyJSON()
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleKMSUpdatePrimaryRegion moves the primary designation of a multi-region
// key to another region. Returns an empty body.
func handleKMSUpdatePrimaryRegion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId         string `json:"KeyId"`
		PrimaryRegion string `json:"PrimaryRegion"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !key.MultiRegion {
		sim.AWSErrorf(w, "UnsupportedOperationException", http.StatusBadRequest,
			"%s is not a multi-region key.", kmsKeyArn(keyId))
		return
	}
	if req.PrimaryRegion == "" {
		sim.AWSError(w, "ValidationException", "PrimaryRegion is required", http.StatusBadRequest)
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.PrimaryRegion = req.PrimaryRegion
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSGetKeyLastUsage reports the most recent successful cryptographic
// operation against a key. If the key has not been used since tracking began,
// the KeyLastUsage element is omitted (real KMS semantics).
func handleKMSGetKeyLastUsage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	resp := map[string]any{
		"KeyId":             kmsKeyArn(keyId),
		"KeyCreationDate":   key.CreationDate,
		"TrackingStartDate": key.CreationDate,
	}
	if key.LastUsedOperation != "" {
		resp["KeyLastUsage"] = map[string]any{
			"Operation":         key.LastUsedOperation,
			"Timestamp":         key.LastUsedDate,
			"KmsRequestId":      generateUUID(),
			"CloudTrailEventId": generateUUID(),
		}
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
