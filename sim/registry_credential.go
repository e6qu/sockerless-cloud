package sim

import (
	"encoding/base64"
	"encoding/json"
)

// RegistryCredential renders a username and password as the X-Registry-Auth
// value the engine API takes on an image pull: the engine's AuthConfig, JSON
// in URL-safe base64.
func RegistryCredential(username, password string) string {
	raw, _ := json.Marshal(map[string]string{"username": username, "password": password})
	return base64.URLEncoding.EncodeToString(raw)
}
