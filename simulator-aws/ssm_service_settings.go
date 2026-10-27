package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// AWS Systems Manager service settings are account-level switches such as
// /ssm/parameter-store/default-parameter-tier. A setting always has an
// AWS-managed default; UpdateServiceSetting sets a customer value and
// ResetServiceSetting restores the default.

type SSMServiceSetting struct {
	SettingId        string  `json:"SettingId"`
	SettingValue     string  `json:"SettingValue"`
	LastModifiedDate float64 `json:"LastModifiedDate"`
	LastModifiedUser string  `json:"LastModifiedUser"`
	ARN              string  `json:"ARN"`
	Status           string  `json:"Status"`
}

var ssmServiceSettings sim.Store[SSMServiceSetting]

func registerSSMServiceSettings(r *AWSRouter, srv *sim.Server) {
	ssmServiceSettings = sim.MakeStore[SSMServiceSetting](srv.DB(), "ssm_service_settings")

	r.Register("AmazonSSM.GetServiceSetting", handleSSMGetServiceSetting)
	r.Register("AmazonSSM.UpdateServiceSetting", handleSSMUpdateServiceSetting)
	r.Register("AmazonSSM.ResetServiceSetting", handleSSMResetServiceSetting)
}

func ssmServiceSettingARN(settingID string) string {
	return "arn:aws:ssm:" + awsRegion() + ":" + awsAccountID() + ":servicesetting" + ensureLeadingSlash(settingID)
}

// ssmDefaultServiceSettingValue returns the AWS-managed default of a setting
// never customized. Systems Manager answers ServiceSettingNotFound for an
// unknown setting ID; the simulator gives it the default "false".
func ssmDefaultServiceSettingValue(settingID string) string {
	switch {
	case strings.HasSuffix(settingID, "/default-parameter-tier"):
		return "Standard"
	case strings.HasSuffix(settingID, "/high-throughput-enabled"):
		return "false"
	case strings.HasSuffix(settingID, "/activation-tier"):
		return "standard"
	default:
		return "false"
	}
}

// ssmEffectiveServiceSetting returns the customized setting, Status
// "Customized", or the AWS-managed default, Status "Default".
func ssmEffectiveServiceSetting(settingID string) SSMServiceSetting {
	if s, ok := ssmServiceSettings.Get(settingID); ok {
		return s
	}
	return SSMServiceSetting{
		SettingId:        settingID,
		SettingValue:     ssmDefaultServiceSettingValue(settingID),
		LastModifiedDate: 0,
		LastModifiedUser: "System",
		ARN:              ssmServiceSettingARN(settingID),
		Status:           "Default",
	}
}

func ssmServiceSettingWire(s SSMServiceSetting) map[string]any {
	return map[string]any{
		"SettingId":        s.SettingId,
		"SettingValue":     s.SettingValue,
		"LastModifiedDate": s.LastModifiedDate,
		"LastModifiedUser": s.LastModifiedUser,
		"ARN":              s.ARN,
		"Status":           s.Status,
	}
}

func handleSSMGetServiceSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SettingId string `json:"SettingId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.SettingId == "" {
		AWSError(w, "ValidationException", "SettingId is required", http.StatusBadRequest)
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ServiceSetting": ssmServiceSettingWire(ssmEffectiveServiceSetting(req.SettingId)),
	})
}

func handleSSMUpdateServiceSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SettingId    string `json:"SettingId"`
		SettingValue string `json:"SettingValue"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.SettingId == "" || req.SettingValue == "" {
		AWSError(w, "ValidationException", "SettingId and SettingValue are required", http.StatusBadRequest)
		return
	}
	ssmServiceSettings.Put(req.SettingId, SSMServiceSetting{
		SettingId:        req.SettingId,
		SettingValue:     req.SettingValue,
		LastModifiedDate: float64(time.Now().Unix()),
		LastModifiedUser: "arn:aws:iam::" + awsAccountID() + ":root",
		ARN:              ssmServiceSettingARN(req.SettingId),
		Status:           "Customized",
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleSSMResetServiceSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SettingId string `json:"SettingId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "ValidationException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.SettingId == "" {
		AWSError(w, "ValidationException", "SettingId is required", http.StatusBadRequest)
		return
	}
	ssmServiceSettings.Delete(req.SettingId)
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"ServiceSetting": ssmServiceSettingWire(ssmEffectiveServiceSetting(req.SettingId)),
	})
}
