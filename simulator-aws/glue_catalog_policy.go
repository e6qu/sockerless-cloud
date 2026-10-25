package main

import (
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

func handleGluePutResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PolicyInJson string `json:"PolicyInJson"`
		ResourceArn  string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.PolicyInJson == "" {
		glueWriteError(w, "InvalidInputException", "PolicyInJson is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	now := glueEpochNow()
	create := now
	if existing, ok := glueResourcePols.Get(glueResourcePolicyKey); ok {
		create = existing.CreateTime
	}
	hash := fmt.Sprintf("%x", uuid.NewSHA1(uuid.Nil, []byte(req.PolicyInJson)))
	glueResourcePols.Put(glueResourcePolicyKey, GlueResourcePolicy{
		PolicyInJson: req.PolicyInJson,
		PolicyHash:   hash,
		CreateTime:   create,
		UpdateTime:   now,
	})
	arn := req.ResourceArn
	if arn == "" {
		arn = glueGlueArn("catalog")
	}
	iamPutResourcePolicy(arn, req.PolicyInJson)
	glueWriteJSON(w, http.StatusOK, map[string]any{"PolicyHash": hash})
}

func handleGlueGetResourcePolicy(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	rp, ok := glueResourcePols.Get(glueResourcePolicyKey)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "No resource policy exists")
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"PolicyInJson": rp.PolicyInJson,
		"PolicyHash":   rp.PolicyHash,
		"CreateTime":   rp.CreateTime,
		"UpdateTime":   rp.UpdateTime,
	})
}

func handleGlueDeleteResourcePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceArn string `json:"ResourceArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueResourcePols.Get(glueResourcePolicyKey); !ok {
		glueWriteError(w, "EntityNotFoundException", "No resource policy exists")
		return
	}
	glueResourcePols.Delete(glueResourcePolicyKey)
	arn := req.ResourceArn
	if arn == "" {
		arn = glueGlueArn("catalog")
	}
	iamDeleteResourcePolicy(arn)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGluePutDataCatalogEncryptionSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DataCatalogEncryptionSettings map[string]any `json:"DataCatalogEncryptionSettings"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	settings.DataCatalogEncryptionSettings = req.DataCatalogEncryptionSettings
	glueCatalogSettgs.Put(glueCatalogSettingsKey, settings)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetDataCatalogEncryptionSettings(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	dcs := settings.DataCatalogEncryptionSettings
	if dcs == nil {
		// AWS returns an empty (default-disabled) settings object when none set.
		dcs = map[string]any{
			"EncryptionAtRest":             map[string]any{"CatalogEncryptionMode": "DISABLED"},
			"ConnectionPasswordEncryption": map[string]any{"ReturnConnectionPasswordEncrypted": false},
		}
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"DataCatalogEncryptionSettings": dcs})
}

func handleGlueImportCatalogToGlue(w http.ResponseWriter, r *http.Request) {
	glueMu.Lock()
	defer glueMu.Unlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	now := glueEpochNow()
	settings.ImportCompleted = true
	settings.ImportTime = &now
	settings.ImportedBy = "arn:aws:iam::123456789012:root"
	glueCatalogSettgs.Put(glueCatalogSettingsKey, settings)
	glueWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleGlueGetCatalogImportStatus(w http.ResponseWriter, r *http.Request) {
	glueMu.RLock()
	defer glueMu.RUnlock()

	settings, _ := glueCatalogSettgs.Get(glueCatalogSettingsKey)
	status := map[string]any{"ImportCompleted": settings.ImportCompleted}
	if settings.ImportTime != nil {
		status["ImportTime"] = *settings.ImportTime
	}
	if settings.ImportedBy != "" {
		status["ImportedBy"] = settings.ImportedBy
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"ImportStatus": status})
}
