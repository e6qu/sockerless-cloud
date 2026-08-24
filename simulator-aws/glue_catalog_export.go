package main

import (
	"net/http"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// AWS Glue Data Catalog export configuration: the account-wide switch that
// exports the catalog's metadata to Amazon S3 Tables. One configuration
// exists per account, PutDataCatalogExportConfiguration writes it, and
// GetDataCatalogExportConfiguration reads it back with the status and
// timestamps the write produced. The export's status tracks the setting the
// caller chose — ENABLED settles to ENABLED, DISABLED to DISABLED — because
// this simulator performs the configuration change synchronously and holds no
// intermediate ENABLING window a caller could observe.

// GlueDataCatalogExportConfiguration is the stored account-wide configuration.
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

func registerGlueCatalogExport(r *sim.AWSRouter, srv *sim.Server) {
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
		sim.AWSError(w, "InvalidInputException", "invalid request body", http.StatusBadRequest)
		return
	}
	if req.ExportSetting != "ENABLED" && req.ExportSetting != "DISABLED" {
		sim.AWSError(w, "InvalidInputException",
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
		// No configuration has been put: real Glue reports the export
		// disabled rather than a missing resource.
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
