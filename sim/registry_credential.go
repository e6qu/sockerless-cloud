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

// RegistryIdentityToken is the credential the engine presents to a registry
// as an identity token: the engine exchanges it through the registry's
// OAuth 2.0 refresh-token grant for an access token, the way a Docker client
// holds the token `az acr login` stored.
func RegistryIdentityToken(token string) string {
	raw, _ := json.Marshal(map[string]string{"identitytoken": token})
	return base64.URLEncoding.EncodeToString(raw)
}
