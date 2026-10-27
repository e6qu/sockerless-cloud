package main

import (
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One AWS Glue Data Catalog export configuration exists per account. The
// simulator applies a change synchronously, so the status settles straight to
// the chosen setting with no observable ENABLING window.

type GlueDataCatalogExportConfiguration struct {
	ExportSetting           string         `json:"exportSetting"`
	Status                  string         `json:"status"`
	EncryptionConfiguration map[string]any `json:"encryptionConfiguration,omitempty"`
	S3TableBucketArn        string         `json:"s3TableBucketArn,omitempty"`
	CreatedAt               float64        `json:"createdAt"`
	UpdatedAt               float64        `json:"updatedAt"`
}

var glueExportConfigurations sim.Store[GlueDataCatalogExportConfiguration]

const glueExportConfigurationKey = "catalog"

func registerGlueCatalogExport(r *AWSRouter, srv *sim.Server) {
	glueExportConfigurations = sim.MakeStore[GlueDataCatalogExportConfiguration](srv.DB(), "glue_export_configurations")
	r.Register("AWSGlue.PutDataCatalogExportConfiguration", handleGluePutDataCatalogExportConfiguration)
	r.Register("AWSGlue.GetDataCatalogExportConfiguration", handleGlueGetDataCatalogExportConfiguration)
}

func handleGluePutDataCatalogExportConfiguration(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ExportSetting           string         `json:"ExportSetting"`
		EncryptionConfiguration map[string]any `json:"EncryptionConfiguration"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidInputException", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ExportSetting != "ENABLED" && req.ExportSetting != "DISABLED" {
		AWSError(w, "InvalidInputException",
			"ExportSetting must be ENABLED or DISABLED", http.StatusBadRequest)
		return
	}
	now := float64(time.Now().Unix())
	configuration, existed := glueExportConfigurations.Get(glueExportConfigurationKey)
	if !existed {
		configuration.CreatedAt = now
	}
	configuration.ExportSetting = req.ExportSetting
	configuration.Status = req.ExportSetting
	configuration.EncryptionConfiguration = req.EncryptionConfiguration
	configuration.UpdatedAt = now
	glueExportConfigurations.Put(glueExportConfigurationKey, configuration)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ExportSetting":           configuration.ExportSetting,
		"EncryptionConfiguration": configuration.EncryptionConfiguration,
	})
}

func handleGlueGetDataCatalogExportConfiguration(w http.ResponseWriter, r *http.Request) {
	configuration, ok := glueExportConfigurations.Get(glueExportConfigurationKey)
	if !ok {
		// With no configuration put, AWS Glue reports the export DISABLED, not a missing resource.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"ExportSetting": "DISABLED",
			"Status":        "DISABLED",
		})
		return
	}
	out := map[string]any{
		"ExportSetting": configuration.ExportSetting,
		"Status":        configuration.Status,
		"CreatedAt":     configuration.CreatedAt,
		"UpdatedAt":     configuration.UpdatedAt,
	}
	if configuration.EncryptionConfiguration != nil {
		out["EncryptionConfiguration"] = configuration.EncryptionConfiguration
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
