package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Authentication for the Event Grid publish data plane. Real Event Grid
// accepts three credentials on a publish to a custom topic, a domain or a
// partner namespace, and refuses the request when none of them is present or
// valid:
//
//   - an access key, in the `aeg-sas-key` header or the `aeg-sas-key` query
//     parameter, matching either of the two keys listKeys serves;
//   - a Shared Access Signature, in the `aeg-sas-token` header or as
//     `Authorization: SharedAccessSignature <token>`, of the form
//     `r=<resource>&e=<expiry>&s=<signature>` where the resource and the
//     expiry are URL-encoded and the signature is the base64 HMAC-SHA256 of
//     the token's own `r=<resource>&e=<expiry>` prefix under the base64-DECODED
//     access key. That last detail is what separates Event Grid's token from
//     the Service Bus / Event Hubs one verified in messaging_sas.go, which
//     signs "<audience>\n<expiry>" with the UTF-8 bytes of the key;
//   - a Microsoft Entra access token for the Event Grid audience, as
//     `Authorization: Bearer <jwt>`.
//
// A token is valid for every resource its signed resource URI prefixes, and
// stops being valid at its expiry. A key that a regenerateKey call has since
// rotated no longer matches, because the material is derived from the slot's
// current generation.
//
// A resource whose `disableLocalAuth` property is set accepts only the Entra
// credential — the access keys and every signature derived from them are
// refused while it is set.

const (
	eventGridKeyHeader     = "aeg-sas-key"
	eventGridTokenHeader   = "aeg-sas-token"
	eventGridSASScheme     = "SharedAccessSignature"
	eventGridBearerScheme  = "Bearer"
	eventGridEntraAudience = "https://eventgrid.azure.net"
)

// eventGridMissingCredentialMessage is the message real Event Grid returns
// when a publish carries no credential at all.
const eventGridMissingCredentialMessage = "Request must contain one of the following authorization signature: aeg-sas-token, aeg-sas-key."

// eventGridNotAuthorizedMessage is the message real Event Grid returns when a
// credential is present but does not authorize the addressed host — a key that
// matches neither slot, a signature that does not verify, an expired token, a
// token signed for a different resource, or a rejected bearer token.
func eventGridNotAuthorizedMessage(host string) string {
	return fmt.Sprintf("The request authorization key is not authorized for %s.", host)
}

// eventGridUnauthorized writes Event Grid's 401 body: an Unauthorized error
// whose `details` array repeats the code and message of the error itself.
func eventGridUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    "Unauthorized",
			"message": message,
			"details": []map[string]any{{
				"code":    "Unauthorized",
				"message": message,
			}},
		},
	})
}

// eventGridAuthorizePublish authenticates a publish against the addressed
// resource's credentials, writing the 401 the service writes and reporting
// false when it does not authorize.
func eventGridAuthorizePublish(w http.ResponseWriter, r *http.Request, resourceID string, properties map[string]any) bool {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	bearer, isBearer := eventGridSchemeValue(authorization, eventGridBearerScheme)
	if isBearer {
		if !eventGridEntraTokenAuthorizes(bearer) {
			eventGridUnauthorized(w, eventGridNotAuthorizedMessage(r.Host))
			return false
		}
		return true
	}

	key := r.Header.Get(eventGridKeyHeader)
	if key == "" {
		key = r.URL.Query().Get(eventGridKeyHeader)
	}
	token := r.Header.Get(eventGridTokenHeader)
	if token == "" {
		if value, ok := eventGridSchemeValue(authorization, eventGridSASScheme); ok {
			token = value
		}
	}
	if key == "" && token == "" {
		eventGridUnauthorized(w, eventGridMissingCredentialMessage)
		return false
	}
	if eventGridLocalAuthDisabled(properties) {
		eventGridUnauthorized(w, eventGridNotAuthorizedMessage(r.Host))
		return false
	}
	if key != "" && eventGridKeyAuthorizes(resourceID, key) {
		return true
	}
	if token != "" && eventGridSASAuthorizes(resourceID, token, r) {
		return true
	}
	eventGridUnauthorized(w, eventGridNotAuthorizedMessage(r.Host))
	return false
}

// eventGridSchemeValue splits an Authorization header value on its scheme,
// returning the credential when the scheme matches.
func eventGridSchemeValue(authorization, scheme string) (string, bool) {
	if len(authorization) <= len(scheme) || !strings.EqualFold(authorization[:len(scheme)], scheme) {
		return "", false
	}
	if authorization[len(scheme)] != ' ' {
		return "", false
	}
	return strings.TrimSpace(authorization[len(scheme)+1:]), true
}

// eventGridLocalAuthDisabled reads the resource's disableLocalAuth property,
// which turns off key and Shared Access Signature authentication and leaves
// Microsoft Entra ID as the only accepted credential.
func eventGridLocalAuthDisabled(properties map[string]any) bool {
	switch v := properties["disableLocalAuth"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}

// eventGridKeyAuthorizes reports whether a presented access key matches either
// of the resource's current key slots. The comparison is constant time, and it
// reads the slots' current material, so a slot regenerateKey has rotated
// authenticates under its new value and no longer under its old one.
func eventGridKeyAuthorizes(resourceID, presented string) bool {
	for _, slot := range eventGridKeySlots {
		current := eventGridKeyMaterial(resourceID, slot)
		if hmac.Equal([]byte(current), []byte(presented)) {
			return true
		}
	}
	return false
}

// eventGridSASToken is a parsed Event Grid Shared Access Signature. The
// resource and expiry are kept verbatim as well as decoded, because the
// signature covers the token's own encoding of them rather than a re-encoding.
type eventGridSASToken struct {
	rawResource string
	rawExpiry   string
	resource    string
	expiry      time.Time
	signature   string
}

// eventGridSASExpiryLayouts are the expiry spellings Event Grid's published
// token generators emit: the invariant/en-US .NET `DateTime.ToString()` form
// of the C# sample, and the `datetime.isoformat()` form of the Python one.
var eventGridSASExpiryLayouts = []string{
	"1/2/2006 3:04:05 PM",
	"1/2/2006 3:04 PM",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// parseEventGridSASToken splits `r=<resource>&e=<expiry>&s=<signature>` into
// its fields without decoding the parts the signature covers.
func parseEventGridSASToken(token string) (eventGridSASToken, bool) {
	var out eventGridSASToken
	for _, pair := range strings.Split(strings.TrimSpace(token), "&") {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "r":
			out.rawResource = value
		case "e":
			out.rawExpiry = value
		case "s":
			decoded, err := url.QueryUnescape(value)
			if err != nil {
				return eventGridSASToken{}, false
			}
			out.signature = decoded
		}
	}
	if out.rawResource == "" || out.rawExpiry == "" || out.signature == "" {
		return eventGridSASToken{}, false
	}
	resource, err := url.QueryUnescape(out.rawResource)
	if err != nil {
		return eventGridSASToken{}, false
	}
	out.resource = resource
	expiry, err := url.QueryUnescape(out.rawExpiry)
	if err != nil {
		return eventGridSASToken{}, false
	}
	parsed, ok := parseEventGridSASExpiry(expiry)
	if !ok {
		return eventGridSASToken{}, false
	}
	out.expiry = parsed
	return out, true
}

// eventGridSASSpaceNormaliser folds the no-break spaces .NET renders before
// an AM/PM designator onto the plain space the layouts spell.
var eventGridSASSpaceNormaliser = strings.NewReplacer("\u202f", " ", "\u00a0", " ")

// parseEventGridSASExpiry reads the token's expiry instant, which the token
// carries as a UTC wall-clock string. .NET renders the AM/PM separator as a
// narrow or ordinary no-break space under current ICU data, so both are
// normalised to the plain space the layouts spell.
func parseEventGridSASExpiry(value string) (time.Time, bool) {
	normalised := eventGridSASSpaceNormaliser.Replace(strings.TrimSpace(value))
	for _, layout := range eventGridSASExpiryLayouts {
		if parsed, err := time.Parse(layout, normalised); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// eventGridSASAuthorizes verifies a Shared Access Signature against the
// resource's two current keys: the token must be unexpired, its signed
// resource URI must prefix the request URL, and its signature must recompute
// under one of the keys.
func eventGridSASAuthorizes(resourceID, token string, r *http.Request) bool {
	parsed, ok := parseEventGridSASToken(token)
	if !ok {
		return false
	}
	if time.Now().UTC().After(parsed.expiry) {
		return false
	}
	if !eventGridSASCoversRequest(parsed.resource, r) {
		return false
	}
	stringToSign := "r=" + parsed.rawResource + "&e=" + parsed.rawExpiry
	for _, slot := range eventGridKeySlots {
		if eventGridSASSignatureMatches(eventGridKeyMaterial(resourceID, slot), stringToSign, parsed.signature) {
			return true
		}
	}
	return false
}

// eventGridSASSignatureMatches recomputes a token signature under one access
// key and compares it in constant time. Event Grid signs with the raw bytes of
// the base64-decoded key, so a key that is not valid base64 signs nothing.
func eventGridSASSignatureMatches(key, stringToSign, signature string) bool {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, raw)
	mac.Write([]byte(stringToSign))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// eventGridSASCoversRequest reports whether a token's signed resource URI
// authorizes the request: a signature is valid for every resource the signed
// URI prefixes, so a namespace-level token covers the endpoints beneath it and
// a token signed for another topic covers none of this one.
func eventGridSASCoversRequest(resource string, r *http.Request) bool {
	requested := strings.ToLower(azureRequestScheme(r) + "://" + r.Host + r.URL.Path)
	granted := strings.ToLower(strings.TrimSpace(resource))
	return strings.HasPrefix(strings.TrimRight(requested, "/")+"/", strings.TrimRight(granted, "/")+"/")
}

// eventGridEntraTokenAuthorizes reports whether a bearer token is a live
// Microsoft Entra access token issued for the Event Grid data-plane audience,
// which is the audience the official publisher clients request through the
// scope https://eventgrid.azure.net/.default.
func eventGridEntraTokenAuthorizes(token string) bool {
	claims, err := verifyAzureSimJWT(token)
	if err != nil {
		return false
	}
	audience := azureTokenAudience(claims)
	return audience == eventGridEntraAudience || audience == eventGridEntraAudience+"/"
}
