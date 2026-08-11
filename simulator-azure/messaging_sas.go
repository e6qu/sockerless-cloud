package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Shared Access Signature verification for the Service Bus and Event Hubs
// data planes. Both services authenticate a caller with a token of the form
//
//	SharedAccessSignature sr=<audience>&sig=<signature>&se=<expiry>&skn=<rule>
//
// where the audience is the URL-escaped, lowercased resource URI, the expiry
// is a Unix timestamp, and the signature is the URL-escaped base64 of
// HMAC-SHA256(key, "<audience>\n<expiry>"). `skn` names an authorization rule
// at the namespace or at the addressed entity; either of that rule's two keys
// signs a valid token, so a rotated key takes effect immediately and the
// pre-rotation key stops working.
//
// AMQP callers present the token through the CBS `$cbs` put-token handshake;
// HTTP callers present it in the Authorization header. Both paths verify here.

// sasAuthError describes why a token was refused, in the vocabulary the real
// services use in their 401 detail and CBS status-description.
type sasAuthError struct {
	Condition   string
	Description string
}

func (e *sasAuthError) Error() string { return e.Description }

var (
	errSASMalformed = &sasAuthError{
		Condition:   "amqp:unauthorized-access",
		Description: "MalformedToken: The provided token is malformed or otherwise invalid.",
	}
	errSASExpired = &sasAuthError{
		Condition:   "amqp:unauthorized-access",
		Description: "ExpiredToken: The token has expired.",
	}
	errSASInvalidSignature = &sasAuthError{
		Condition:   "amqp:unauthorized-access",
		Description: "InvalidSignature: The token has an invalid signature.",
	}
)

func errSASUnknownRule(keyName string) *sasAuthError {
	return &sasAuthError{
		Condition:   "amqp:unauthorized-access",
		Description: fmt.Sprintf("InvalidSignature: Cannot find a SharedAccessKey with name %q.", keyName),
	}
}

// sasToken is a parsed SharedAccessSignature. rawResource and rawExpiry are
// kept verbatim because the signature covers the token's own encoding of
// them, not a re-encoding of the decoded values.
type sasToken struct {
	rawResource string
	rawExpiry   string
	resource    string
	signature   string
	keyName     string
}

// parseSASToken splits a SharedAccessSignature into its fields without
// decoding the parts the signature covers.
func parseSASToken(token string) (sasToken, error) {
	trimmed := strings.TrimSpace(token)
	const scheme = "SharedAccessSignature "
	if len(trimmed) < len(scheme) || !strings.EqualFold(trimmed[:len(scheme)], scheme) {
		return sasToken{}, errSASMalformed
	}
	var out sasToken
	for _, pair := range strings.Split(trimmed[len(scheme):], "&") {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "sr":
			out.rawResource = value
		case "sig":
			decoded, err := url.QueryUnescape(value)
			if err != nil {
				return sasToken{}, errSASMalformed
			}
			out.signature = decoded
		case "se":
			out.rawExpiry = value
		case "skn":
			decoded, err := url.QueryUnescape(value)
			if err != nil {
				return sasToken{}, errSASMalformed
			}
			out.keyName = decoded
		}
	}
	if out.rawResource == "" || out.signature == "" || out.rawExpiry == "" || out.keyName == "" {
		return sasToken{}, errSASMalformed
	}
	resource, err := url.QueryUnescape(out.rawResource)
	if err != nil {
		return sasToken{}, errSASMalformed
	}
	out.resource = resource
	return out, nil
}

// sasEntityPathFromAudience reduces a token audience to the entity path it
// addresses, relative to the namespace host. `amqps://ns.servicebus.host:port/
// myqueue` yields "myqueue"; a bare namespace audience yields "". HTTP callers
// sign the full request URL, so the query string is dropped — it selects an
// API version, not a resource.
func sasEntityPathFromAudience(audience string) string {
	rest := audience
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Trim(rest, "/")
}

// sasRuleCandidates returns the authorization rules a token's `skn` can name
// for the addressed namespace and entity: the rule of that name scoped to the
// entity, and the rule of that name scoped to the namespace. Both Service Bus
// and Event Hubs rules are considered because both services share the
// `{namespace}.servicebus.*` host.
func sasRuleCandidates(namespace, entityPath, keyName string) []string {
	nsSuffix := "/namespaces/" + namespace + "/authorizationRules/" + keyName
	// A subscription path (`topic/subscriptions/sub`) is authorized by a rule
	// on its topic; the entity segment is the first path element.
	entity := entityPath
	if i := strings.Index(entity, "/"); i >= 0 {
		entity = entity[:i]
	}
	var entitySuffixes []string
	if entity != "" {
		for _, kind := range []string{"queues", "topics", "eventhubs"} {
			entitySuffixes = append(entitySuffixes,
				"/namespaces/"+namespace+"/"+kind+"/"+entity+"/authorizationRules/"+keyName)
		}
	}
	matches := func(id string) bool {
		if strings.HasSuffix(id, nsSuffix) {
			return true
		}
		for _, suffix := range entitySuffixes {
			if strings.HasSuffix(id, suffix) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, rule := range sbAuthRules.List() {
		if matches(rule.ID) {
			out = append(out, rule.ID)
		}
	}
	for _, rule := range ehAuthRules.List() {
		if matches(rule.ID) {
			out = append(out, rule.ID)
		}
	}
	return out
}

// verifyMessagingSAS authenticates a Shared Access Signature against the
// authorization rules of the addressed namespace and returns the audience the
// token grants. The signature is checked against the rule's CURRENT key
// material for both slots, so a rotated key authenticates and the key it
// replaced does not.
func verifyMessagingSAS(namespace, token string) (audience string, err error) {
	parsed, err := parseSASToken(token)
	if err != nil {
		return "", err
	}
	expiryUnix, convErr := strconv.ParseInt(strings.TrimSpace(parsed.rawExpiry), 10, 64)
	if convErr != nil {
		return "", errSASMalformed
	}
	if time.Now().UTC().After(time.Unix(expiryUnix, 0).UTC()) {
		return "", errSASExpired
	}
	candidates := sasRuleCandidates(namespace, sasEntityPathFromAudience(parsed.resource), parsed.keyName)
	if len(candidates) == 0 {
		return "", errSASUnknownRule(parsed.keyName)
	}
	stringToSign := parsed.rawResource + "\n" + parsed.rawExpiry
	for _, ruleID := range candidates {
		for _, slot := range []string{"primary", "secondary"} {
			if sasSignatureMatches(azureKeyMaterial32(ruleID, slot), stringToSign, parsed.signature) {
				return parsed.resource, nil
			}
		}
	}
	return "", errSASInvalidSignature
}

// sasSignatureMatches recomputes the token signature and compares it in
// constant time.
func sasSignatureMatches(key, stringToSign, signature string) bool {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(stringToSign))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// sasAudienceCoversEntity reports whether a granted audience authorizes an
// entity path. A namespace-scoped audience covers every entity beneath it; an
// entity-scoped audience covers that entity and its subscriptions.
func sasAudienceCoversEntity(audience, entityPath string) bool {
	return sasPathCovers(sasEntityPathFromAudience(audience), entityPath)
}

// sasAudienceCoversManagement reports whether a granted audience authorizes a
// management operation naming an entity. Clients negotiate the claim for an
// entity's management link (`<entity>/$management`), and that claim authorizes
// management operations on the entity itself.
func sasAudienceCoversManagement(audience, entityPath string) bool {
	granted := sasEntityPathFromAudience(audience)
	granted = strings.TrimSuffix(strings.TrimSuffix(granted, "/$management"), "/$Management")
	return sasPathCovers(granted, entityPath)
}

// sasPathCovers reports whether a granted entity path authorizes a requested
// one: an empty grant is namespace-wide, and a grant covers the paths nested
// beneath it.
func sasPathCovers(granted, entityPath string) bool {
	if granted == "" {
		return true
	}
	entity := strings.Trim(entityPath, "/")
	return strings.EqualFold(granted, entity) ||
		strings.HasPrefix(strings.ToLower(entity), strings.ToLower(granted)+"/")
}
