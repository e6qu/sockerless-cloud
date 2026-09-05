package main

import (
	"net/http"
	"sort"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ECS account settings — opt-in/opt-out flags (e.g. serviceLongArnFormat,
// awsvpcTrunking, containerInsights) set per-principal or as the account
// default. Backed by aws_ecs_account_setting_default. The sim stores each
// setting keyed by principal + name; ListAccountSettings reads them back and
// PutAccountSettingDefault writes the account-wide row (principalArn = the
// account root).

// ECSAccountSetting is the stored shape of an account setting.
type ECSAccountSetting struct {
	Name         string `json:"name"`
	Value        string `json:"value"`
	PrincipalArn string `json:"principalArn"`
	Type         string `json:"type"`
}

var ecsAccountSettings sim.Store[ECSAccountSetting]

func registerECSAccount(r *AWSRouter, srv *sim.Server) {
	ecsAccountSettings = sim.MakeStore[ECSAccountSetting](srv.DB(), "ecs_account_settings")

	r.Register("AmazonEC2ContainerServiceV20141113.PutAccountSetting", handleECSPutAccountSetting)
	r.Register("AmazonEC2ContainerServiceV20141113.PutAccountSettingDefault", handleECSPutAccountSettingDefault)
	r.Register("AmazonEC2ContainerServiceV20141113.DeleteAccountSetting", handleECSDeleteAccountSetting)
	r.Register("AmazonEC2ContainerServiceV20141113.ListAccountSettings", handleECSListAccountSettings)
}

func ecsAccountRootArn() string {
	return "arn:aws:iam::" + awsAccountID() + ":root"
}

func ecsAccountSettingKey(principal, name string) string { return principal + "/" + name }

func ecsPutAccountSetting(principal, name, value, settingType string) ECSAccountSetting {
	s := ECSAccountSetting{
		Name:         name,
		Value:        value,
		PrincipalArn: principal,
		Type:         settingType,
	}
	ecsAccountSettings.Put(ecsAccountSettingKey(principal, name), s)
	return s
}

func handleECSPutAccountSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Value        string `json:"value"`
		PrincipalArn string `json:"principalArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Value == "" {
		AWSError(w, "InvalidParameterException", "name and value are required", http.StatusBadRequest)
		return
	}
	principal := req.PrincipalArn
	if principal == "" {
		principal = ecsAccountRootArn()
	}
	s := ecsPutAccountSetting(principal, req.Name, req.Value, "user")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"setting": s})
}

func handleECSPutAccountSettingDefault(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Value == "" {
		AWSError(w, "InvalidParameterException", "name and value are required", http.StatusBadRequest)
		return
	}
	s := ecsPutAccountSetting(ecsAccountRootArn(), req.Name, req.Value, "aws_managed")
	sim.WriteJSON(w, http.StatusOK, map[string]any{"setting": s})
}

func handleECSDeleteAccountSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		PrincipalArn string `json:"principalArn"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		AWSError(w, "InvalidParameterException", "name is required", http.StatusBadRequest)
		return
	}
	principal := req.PrincipalArn
	if principal == "" {
		principal = ecsAccountRootArn()
	}
	key := ecsAccountSettingKey(principal, req.Name)
	s, ok := ecsAccountSettings.Get(key)
	if !ok {
		// Deleting an unset setting still echoes the now-default-shaped row.
		s = ECSAccountSetting{Name: req.Name, PrincipalArn: principal}
	}
	ecsAccountSettings.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"setting": s})
}

func handleECSListAccountSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Value        string `json:"value"`
		PrincipalArn string `json:"principalArn"`
		MaxResults   int    `json:"maxResults"`
		NextToken    string `json:"nextToken"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
		return
	}
	var settings []ECSAccountSetting
	for _, s := range ecsAccountSettings.List() {
		if req.Name != "" && s.Name != req.Name {
			continue
		}
		if req.Value != "" && s.Value != req.Value {
			continue
		}
		if req.PrincipalArn != "" && s.PrincipalArn != req.PrincipalArn {
			continue
		}
		settings = append(settings, s)
	}
	sort.Slice(settings, func(i, j int) bool { return settings[i].Name < settings[j].Name })
	page, next := awsPage(settings, req.NextToken, req.MaxResults, 100)
	out := map[string]any{"settings": page}
	if next != "" {
		out["nextToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, out)
}
