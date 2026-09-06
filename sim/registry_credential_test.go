package sim

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestRegistryCredentialIsTheEngineAuthConfig(t *testing.T) {
	raw, err := base64.URLEncoding.DecodeString(RegistryCredential("oauth2accesstoken", "ya29.token"))
	if err != nil {
		t.Fatalf("not URL-safe base64: %v", err)
	}
	var auth struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil {
		t.Fatalf("not an AuthConfig: %v", err)
	}
	if auth.Username != "oauth2accesstoken" || auth.Password != "ya29.token" {
		t.Errorf("decoded %+v", auth)
	}
}
