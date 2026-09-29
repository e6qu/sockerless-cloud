package sim

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
)

// RegistryError writes the Docker Registry HTTP API v2 error envelope with the
// `Docker-Distribution-Api-Version` header every registry response carries. A
// nil detail is omitted.
func RegistryError(w http.ResponseWriter, status int, code, message string, detail any) {
	entry := map[string]any{"code": code, "message": message}
	if detail != nil {
		entry["detail"] = detail
	}
	w.Header().Set("Docker-Distribution-Api-Version", "registry/2.0")
	WriteJSON(w, status, map[string]any{"errors": []map[string]any{entry}})
}

// BearerChallenge renders the `WWW-Authenticate` value of the Docker Registry
// v2 token flow. Empty service, scope and errCode are omitted, since registries
// differ on which they name.
func BearerChallenge(realm, service, scope, errCode string) string {
	challenge := fmt.Sprintf("Bearer realm=%q", realm)
	if service != "" {
		challenge += fmt.Sprintf(",service=%q", service)
	}
	if scope != "" {
		challenge += fmt.Sprintf(",scope=%q", scope)
	}
	if errCode != "" {
		challenge += fmt.Sprintf(",error=%q", errCode)
	}
	return challenge
}

// BasicChallenge renders the `WWW-Authenticate` value of a registry that takes
// HTTP Basic credentials directly.
func BasicChallenge(realm, service string) string {
	return fmt.Sprintf("Basic realm=%q,service=%q", realm, service)
}

// ParseAuthorization splits an Authorization header into its scheme and
// credential. Compare the scheme case-insensitively (RFC 9110 §11.1).
func ParseAuthorization(header string) (scheme, credential string) {
	scheme, credential, _ = strings.Cut(strings.TrimSpace(header), " ")
	return scheme, strings.TrimSpace(credential)
}

// BasicCredential decodes the base64 `user:password` of an HTTP Basic
// credential.
func BasicCredential(encoded string) (username, password string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", false
	}
	return strings.Cut(string(raw), ":")
}

// RegistryScope is one Docker Registry v2 token scope,
// `<type>:<name>:<action>[,<action>…]`, and one entry of a registry token's
// `access` claim.
type RegistryScope struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// ParseRegistryScope reads one scope. It reports false when the type or name
// is missing; the action list may be empty.
func ParseRegistryScope(value string) (RegistryScope, bool) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return RegistryScope{}, false
	}
	scope := RegistryScope{Type: parts[0], Name: parts[1]}
	for _, action := range strings.Split(parts[2], ",") {
		if action = strings.TrimSpace(action); action != "" {
			scope.Actions = append(scope.Actions, action)
		}
	}
	return scope, true
}

// ParseRegistryScopes reads the scopes of a token request, which a client may
// repeat or separate with spaces. A scope that names no action is dropped.
func ParseRegistryScopes(values []string) []RegistryScope {
	var out []RegistryScope
	for _, value := range values {
		for _, field := range strings.Fields(value) {
			if scope, ok := ParseRegistryScope(field); ok && len(scope.Actions) > 0 {
				out = append(out, scope)
			}
		}
	}
	return out
}

func (s RegistryScope) String() string {
	return s.Type + ":" + s.Name + ":" + strings.Join(s.Actions, ",")
}

// ImageOnPort reports whether an image reference's registry host carries port,
// the one coordinate a relocated registry keeps.
func ImageOnPort(image string, port int) bool {
	host, _, found := strings.Cut(image, "/")
	if !found {
		return false
	}
	i := strings.LastIndex(host, ":")
	return i >= 0 && host[i+1:] == fmt.Sprint(port)
}
