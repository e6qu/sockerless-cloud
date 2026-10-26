package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerKMSMultiRegion(r *AWSRouter, srv *sim.Server) {
	r.Register("TrentService.RetireGrant", handleKMSRetireGrant)
	r.Register("TrentService.ListRetirableGrants", handleKMSListRetirableGrants)
	r.Register("TrentService.ReplicateKey", handleKMSReplicateKey)
	r.Register("TrentService.UpdatePrimaryRegion", handleKMSUpdatePrimaryRegion)
	r.Register("TrentService.GetKeyLastUsage", handleKMSGetKeyLastUsage)
}

// kmsMultiRegionConfig builds the MultiRegionConfiguration AWS KMS attaches to a
// multi-region key: the primary's Region and ARN and the replica Regions.
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

// handleKMSRetireGrant retires a grant named by GrantToken or by GrantId and
// KeyId. Retiring deletes the grant, like RevokeGrant, but the
// RetiringPrincipal authorizes it.
func handleKMSRetireGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GrantToken string `json:"GrantToken"`
		GrantId    string `json:"GrantId"`
		KeyId      string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.GrantToken == "" && req.GrantId == "" {
		AWSError(w, "ValidationException",
			"Either GrantToken or GrantId+KeyId is required", http.StatusBadRequest)
		return
	}
	if req.GrantId != "" {
		// Retire by GrantId (+ KeyId). The KeyId, when present, must match.
		grant, ok := kmsGrants.Get(req.GrantId)
		if !ok {
			AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
				"Grant %q does not exist", req.GrantId)
			return
		}
		if req.KeyId != "" {
			keyId, ok := resolveKMSKey(req.KeyId)
			if !ok || keyId != grant.KeyId {
				AWSErrorf(w, "NotFoundException", http.StatusBadRequest,
					"Grant %q is not on key %q", req.GrantId, req.KeyId)
				return
			}
		}
		kmsGrants.Delete(req.GrantId)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	// The simulator's grant tokens are not bound to a grant, so a token-only
	// RetireGrant retires nothing and succeeds.
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
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.RetiringPrincipal == "" {
		AWSError(w, "ValidationException", "RetiringPrincipal is required", http.StatusBadRequest)
		return
	}
	var matched []KMSGrant
	for _, g := range kmsGrants.List() {
		if g.RetiringPrincipal == req.RetiringPrincipal {
			matched = append(matched, g)
		}
	}
	page, next, pageOK := awsKeyPage(w, kmsBadToken, matched, func(it KMSGrant) string { return it.GrantId }, req.Marker, req.Limit, 100)
	if !pageOK {
		return
	}
	resp := map[string]any{"Grants": page, "Truncated": next != ""}
	if next != "" {
		resp["NextMarker"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// handleKMSReplicateKey replicates a multi-region key into another Region. The
// replica shares the primary's KeyId and returns KeyMetadata scoped to the
// replica Region.
func handleKMSReplicateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId         string   `json:"KeyId"`
		ReplicaRegion string   `json:"ReplicaRegion"`
		Description   string   `json:"Description"`
		Policy        string   `json:"Policy"`
		Tags          []KMSTag `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !key.MultiRegion {
		AWSErrorf(w, "UnsupportedOperationException", http.StatusBadRequest,
			"%s is not a multi-region key.", kmsKeyArn(keyId))
		return
	}
	if req.ReplicaRegion == "" {
		AWSError(w, "ValidationException", "ReplicaRegion is required", http.StatusBadRequest)
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
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
		return
	}
	keyId, ok := kmsResolveOr404(w, r, req.KeyId)
	if !ok {
		return
	}
	key, _ := kmsKeys.Get(keyId)
	if !key.MultiRegion {
		AWSErrorf(w, "UnsupportedOperationException", http.StatusBadRequest,
			"%s is not a multi-region key.", kmsKeyArn(keyId))
		return
	}
	if req.PrimaryRegion == "" {
		AWSError(w, "ValidationException", "PrimaryRegion is required", http.StatusBadRequest)
		return
	}
	kmsKeys.Update(keyId, func(k *KMSKey) {
		k.PrimaryRegion = req.PrimaryRegion
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleKMSGetKeyLastUsage reports the key's most recent successful
// cryptographic operation; a key unused since tracking began omits
// KeyLastUsage, as AWS KMS does.
func handleKMSGetKeyLastUsage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidRequest", "Invalid request body", http.StatusBadRequest)
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
			"KmsRequestId":      sim.NewUUID(),
			"CloudTrailEventId": sim.NewUUID(),
		}
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}
