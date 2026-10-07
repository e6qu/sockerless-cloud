package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Authentication and authorization for the Azure Key Vault data plane.
//
// Key Vault authenticates every request with a Microsoft Entra access token
// issued for the Key Vault resource (https://vault.azure.net) by the vault's
// own tenant, then checks the network rules, then authorizes the operation
// under the vault's permission model: the access policies for a vault without
// enableRbacAuthorization, the caller's Azure RBAC data actions at the vault,
// the object or a scope above for one with it
// (learn.microsoft.com/azure/key-vault/general/security-features). A request
// without a token, or with one Key Vault does not accept, gets 401 with the
// Bearer challenge clients start their token acquisition from; one the network
// rules or the permission model refuse gets 403 Forbidden.

// keyVaultTokenAudiences are the audiences Key Vault accepts: its resource URI
// with and without the trailing slash, and its application ID.
var keyVaultTokenAudiences = map[string]bool{
	"https://vault.azure.net":              true,
	"https://vault.azure.net/":             true,
	"cfa8b339-82a2-471a-a3c9-0fc0be7a4093": true,
}

// keyVaultDNSSuffix is the suffix a vault's host carries after its name, which
// /metadata/endpoints advertises as keyVaultDns ("vault.azure.net" on Azure).
func keyVaultDNSSuffix(r *http.Request) string {
	return azureEndpointSuffix(azureKeyVaultEndpointURL(r, "metadatavault"), "metadatavault")
}

// keyVaultAudienceValid reports whether a token's audience is Key Vault's. A
// client of a cloud whose metadata names another keyVaultDns suffix acquires
// its Key Vault tokens for https://<suffix>, as Azure Stack Hub's clients do,
// so that audience is the Key Vault resource of this simulator's cloud.
func keyVaultAudienceValid(aud string, r *http.Request) bool {
	if keyVaultTokenAudiences[aud] {
		return true
	}
	own := "https://" + keyVaultDNSSuffix(r)
	return aud == own || aud == own+"/"
}

var keyVaultsByName sim.GenerationIndex[KeyVault]

// keyVaultByName resolves the vault a data-plane host names. A vault name is
// its DNS label, so it is globally unique and one row answers.
func keyVaultByName(name string) (KeyVault, bool) {
	if keyVaults == nil {
		return KeyVault{}, false
	}
	return keyVaultsByName.Lookup(keyVaults, strings.ToLower(name), func(v KeyVault) []string {
		return []string{strings.ToLower(v.Name)}
	})
}

// kvCaller is the authenticated principal of a data-plane request.
type kvCaller struct {
	oid, appid, iss string
}

func (c kvCaller) String() string {
	return fmt.Sprintf("appid=%s;oid=%s;iss=%s", c.appid, c.oid, c.iss)
}

// kvDataOp is the permission a data-plane operation needs: an access policy
// permission in one of the three object categories, and the Azure RBAC data
// action checked at the object (or at the vault for a vault-wide operation).
type kvDataOp struct {
	category   string
	permission string
	dataAction string
	object     string
}

// kvDataPlaneOperation names the permission the request's operation needs,
// following each operation's documented permission in the Key Vault REST API.
// It reports false for a path or method the data plane does not serve, which
// the router answers.
func kvDataPlaneOperation(method, path string) (kvDataOp, bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if len(segs) > 1 && segs[len(segs)-1] == "" {
		segs = segs[:len(segs)-1]
	}
	const prefix = "Microsoft.KeyVault/vaults/"
	op := func(category, permission, action, object string) (kvDataOp, bool) {
		return kvDataOp{category: category, permission: permission, dataAction: prefix + action, object: object}, true
	}
	secret := func(permission, action, name string) (kvDataOp, bool) {
		obj := ""
		if name != "" {
			obj = "secrets/" + name
		}
		return op("secrets", permission, "secrets/"+action, obj)
	}
	key := func(permission, action, name string) (kvDataOp, bool) {
		obj := ""
		if name != "" {
			obj = "keys/" + name
		}
		return op("keys", permission, action, obj)
	}
	cert := func(permission, action, name string) (kvDataOp, bool) {
		obj := ""
		if name != "" {
			obj = "certificates/" + name
		}
		return op("certificates", permission, action, obj)
	}
	n := len(segs)
	switch segs[0] {
	case "secrets":
		switch {
		case n == 1 && method == http.MethodGet:
			return secret("list", "readMetadata/action", "")
		case n == 2 && segs[1] == "restore" && method == http.MethodPost:
			return secret("restore", "restore/action", "")
		case n == 2:
			switch method {
			case http.MethodPut:
				return secret("set", "setSecret/action", segs[1])
			case http.MethodGet:
				return secret("get", "getSecret/action", segs[1])
			case http.MethodDelete:
				return secret("delete", "delete", segs[1])
			}
		case n == 3 && segs[2] == "versions" && method == http.MethodGet:
			return secret("list", "readMetadata/action", segs[1])
		case n == 3 && segs[2] == "backup" && method == http.MethodPost:
			return secret("backup", "backup/action", segs[1])
		case n == 3 && method == http.MethodGet:
			return secret("get", "getSecret/action", segs[1])
		case n == 3 && method == http.MethodPatch:
			return secret("set", "update/action", segs[1])
		}
	case "deletedsecrets":
		switch {
		case n == 1 && method == http.MethodGet:
			return secret("list", "readMetadata/action", "")
		case n == 2 && method == http.MethodGet:
			return secret("get", "readMetadata/action", segs[1])
		case n == 2 && method == http.MethodDelete:
			return secret("purge", "purge/action", segs[1])
		case n == 3 && segs[2] == "recover" && method == http.MethodPost:
			return secret("recover", "recover/action", segs[1])
		}
	case "keys":
		switch {
		case n == 1 && method == http.MethodGet:
			return key("list", "keys/read", "")
		case n == 2 && segs[1] == "restore" && method == http.MethodPost:
			return key("restore", "keys/restore/action", "")
		case n == 2 && method == http.MethodPut:
			return key("import", "keys/import/action", segs[1])
		case n == 2 && method == http.MethodGet:
			return key("get", "keys/read", segs[1])
		case n == 2 && method == http.MethodPatch:
			return key("update", "keys/update/action", segs[1])
		case n == 2 && method == http.MethodDelete:
			return key("delete", "keys/delete", segs[1])
		case n == 3 && method == http.MethodPost:
			switch segs[2] {
			case "create":
				return key("create", "keys/create/action", segs[1])
			case "backup":
				return key("backup", "keys/backup/action", segs[1])
			case "rotate":
				return key("rotate", "keys/rotate/action", segs[1])
			}
			return kvKeyCryptoOperation(segs[1], segs[2])
		case n == 3 && segs[2] == "versions" && method == http.MethodGet:
			return key("list", "keys/read", segs[1])
		case n == 3 && segs[2] == "rotationpolicy" && method == http.MethodGet:
			return key("getrotationpolicy", "keyrotationpolicies/read", segs[1])
		case n == 3 && segs[2] == "rotationpolicy" && method == http.MethodPut:
			return key("setrotationpolicy", "keyrotationpolicies/write", segs[1])
		case n == 3 && method == http.MethodGet:
			return key("get", "keys/read", segs[1])
		case n == 3 && method == http.MethodPatch:
			return key("update", "keys/update/action", segs[1])
		case n == 4 && method == http.MethodPost:
			return kvKeyCryptoOperation(segs[1], segs[3])
		case n == 4 && segs[3] == "attestation" && method == http.MethodGet:
			return key("get", "keys/read", segs[1])
		}
	case "deletedkeys":
		switch {
		case n == 1 && method == http.MethodGet:
			return key("list", "keys/read", "")
		case n == 2 && method == http.MethodGet:
			return key("get", "keys/read", segs[1])
		case n == 2 && method == http.MethodDelete:
			return key("purge", "keys/purge/action", segs[1])
		case n == 3 && segs[2] == "recover" && method == http.MethodPost:
			return key("recover", "keys/recover/action", segs[1])
		}
	case "certificates":
		switch {
		case n == 1 && method == http.MethodGet:
			return cert("list", "certificates/read", "")
		case n == 2 && segs[1] == "restore" && method == http.MethodPost:
			return cert("restore", "certificates/restore/action", "")
		case n == 2 && segs[1] == "contacts":
			return cert("managecontacts", "certificatecontacts/write", "")
		case n == 2 && segs[1] == "issuers" && method == http.MethodGet:
			return cert("getissuers", "certificatecas/read", "")
		case n == 3 && segs[1] == "issuers":
			switch method {
			case http.MethodGet:
				return cert("getissuers", "certificatecas/read", "")
			case http.MethodPut, http.MethodPatch:
				return cert("setissuers", "certificatecas/write", "")
			case http.MethodDelete:
				return cert("deleteissuers", "certificatecas/delete", "")
			}
		case n == 2 && method == http.MethodGet:
			return cert("get", "certificates/read", segs[1])
		case n == 2 && method == http.MethodDelete:
			return cert("delete", "certificates/delete", segs[1])
		case n == 3 && method == http.MethodPost:
			switch segs[2] {
			case "create":
				return cert("create", "certificates/create/action", segs[1])
			case "import":
				return cert("import", "certificates/import/action", segs[1])
			case "backup":
				return cert("backup", "certificates/backup/action", segs[1])
			}
		case n == 4 && segs[2] == "pending" && segs[3] == "merge" && method == http.MethodPost:
			return cert("create", "certificates/create/action", segs[1])
		case n == 3 && segs[2] == "versions" && method == http.MethodGet:
			return cert("list", "certificates/read", segs[1])
		case n == 3 && (segs[2] == "pending" || segs[2] == "policy") && method == http.MethodGet:
			return cert("get", "certificates/read", segs[1])
		case n == 3 && segs[2] == "pending" && (method == http.MethodPatch || method == http.MethodDelete):
			return cert("update", "certificates/update/action", segs[1])
		case n == 3 && method == http.MethodGet:
			return cert("get", "certificates/read", segs[1])
		case n == 3 && method == http.MethodPatch:
			return cert("update", "certificates/update/action", segs[1])
		}
	case "deletedcertificates":
		switch {
		case n == 1 && method == http.MethodGet:
			return cert("list", "certificates/read", "")
		case n == 2 && method == http.MethodGet:
			return cert("get", "certificates/read", segs[1])
		case n == 2 && method == http.MethodDelete:
			return cert("purge", "certificates/purge/action", segs[1])
		case n == 3 && segs[2] == "recover" && method == http.MethodPost:
			return cert("recover", "certificates/recover/action", segs[1])
		}
	}
	return kvDataOp{}, false
}

// kvKeyCryptoOperation names the permission of a key's cryptographic
// operation. The access policy permissions spell wrap and unwrap wrapKey and
// unwrapKey; the RBAC data actions spell them wrap and unwrap.
func kvKeyCryptoOperation(name, verb string) (kvDataOp, bool) {
	var permission, action string
	switch verb {
	case "encrypt", "decrypt", "sign", "verify", "release":
		permission, action = verb, verb
	case "wrapkey":
		permission, action = "wrapKey", "wrap"
	case "unwrapkey":
		permission, action = "unwrapKey", "unwrap"
	default:
		return kvDataOp{}, false
	}
	return kvDataOp{
		category:   "keys",
		permission: permission,
		dataAction: "Microsoft.KeyVault/vaults/keys/" + action + "/action",
		object:     "keys/" + name,
	}, true
}

// keyVaultGrants reports whether the vault's permission model lets caller
// perform op. A vault with enableRbacAuthorization ignores its access policies
// and grants through role assignments at the object, the vault or a scope
// above; any other vault grants through an access policy in the vault's tenant
// — for a compound identity policy, only to tokens its application obtained —
// that carries the permission. Either names the caller's object ID or a group
// the caller belongs to, directly or through nested groups.
func keyVaultGrants(v KeyVault, caller kvCaller, op kvDataOp) bool {
	if caller.oid == "" {
		return false
	}
	principals := append([]string{caller.oid}, entraTransitiveGroupIDs(caller.oid)...)
	if v.Properties.EnableRbacAuthorization {
		for _, principal := range principals {
			if rbacPrincipalHasDataAction(principal, kvRBACScope(v, op), op.dataAction) {
				return true
			}
		}
		return false
	}
	for _, pol := range v.Properties.AccessPolicies {
		if !slices.ContainsFunc(principals, func(p string) bool { return strings.EqualFold(p, pol.ObjectID) }) {
			continue
		}
		if pol.TenantID != "" && v.Properties.TenantID != "" && !strings.EqualFold(pol.TenantID, v.Properties.TenantID) {
			continue
		}
		if pol.ApplicationID != "" && !strings.EqualFold(pol.ApplicationID, caller.appid) {
			continue
		}
		for _, granted := range pol.Permissions.category(op.category) {
			if strings.EqualFold(granted, op.permission) || strings.EqualFold(granted, "all") {
				return true
			}
		}
	}
	return false
}

func (p KeyVaultPermissions) category(name string) []string {
	switch name {
	case "keys":
		return p.Keys
	case "secrets":
		return p.Secrets
	case "certificates":
		return p.Certificates
	}
	return nil
}

func kvRBACScope(v KeyVault, op kvDataOp) string {
	if op.object == "" {
		return v.ID
	}
	return v.ID + "/" + op.object
}

// keyVaultAuthorizeDataPlane authenticates and authorizes a request to the
// named vault's data plane, writing Key Vault's refusal and reporting false
// when the request may not proceed.
func keyVaultAuthorizeDataPlane(w http.ResponseWriter, r *http.Request, vaultName string) bool {
	v, ok := keyVaultByName(vaultName)
	if !ok {
		AzureErrorf(w, "VaultNotFound", http.StatusNotFound,
			"The vault '%s' does not exist. A vault's data plane exists only while the vault does.", vaultName)
		return false
	}
	scheme, token, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		kvUnauthorized(w, r, v, "AKV10000: Request is missing a Bearer or PoP token.")
		return false
	}
	claims, err := verifyAzureSimJWT(token)
	if err != nil {
		kvUnauthorized(w, r, v, "Token validation failed: "+err.Error()+".")
		return false
	}
	if aud := azureTokenAudience(claims); !keyVaultAudienceValid(aud, r) {
		kvUnauthorized(w, r, v, fmt.Sprintf("AKV10022: Invalid audience. Expected https://vault.azure.net, found: %s.", aud))
		return false
	}
	caller := kvCaller{}
	caller.oid, _ = claims["oid"].(string)
	caller.appid, _ = claims["appid"].(string)
	caller.iss, _ = claims["iss"].(string)
	issuers := []string{
		"https://sts.windows.net/" + v.Properties.TenantID + "/",
		"https://login.microsoftonline.com/" + v.Properties.TenantID + "/v2.0",
	}
	if caller.iss != issuers[0] && caller.iss != issuers[1] {
		kvUnauthorized(w, r, v, fmt.Sprintf("AKV10032: Invalid issuer. Expected one of %s, found %s.",
			strings.Join(issuers, ", "), caller.iss))
		return false
	}
	if inner, message := kvNetworkRefusal(v, r, caller); inner != "" {
		kvForbidden(w, inner, message)
		return false
	}
	op, ok := kvDataPlaneOperation(r.Method, r.URL.Path)
	if !ok {
		return true
	}
	if keyVaultGrants(v, caller, op) {
		return true
	}
	vaultRef := v.Name + ";location=" + v.Location
	if v.Properties.EnableRbacAuthorization {
		kvForbidden(w, "ForbiddenByRbac", fmt.Sprintf(
			"Caller is not authorized to perform action on resource.\r\n"+
				"If role assignments, deny assignments or role definitions were changed recently, please observe propagation time.\r\n"+
				"Caller: %s\r\nAction: '%s'\r\nResource: '%s'\r\nAssignment: (not found)\r\nDenyAssignmentId: null\r\nDecisionReason: null \r\nVault: %s\r\n",
			caller, op.dataAction, strings.ToLower(kvRBACScope(v, op)), vaultRef))
		return false
	}
	kvForbidden(w, "AccessDenied", fmt.Sprintf(
		"The user, group or application '%s' does not have %s %s permission on key vault '%s'. For help resolving this issue, please see https://go.microsoft.com/fwlink/?linkid=2125287",
		caller, op.category, op.permission, vaultRef))
	return false
}

// kvNetworkRefusal applies the vault's network rules to the request's client
// address, naming the inner error code and message of the refusal, or nothing
// when the rules admit the request. Disabled public network access refuses
// every request that does not arrive through a private endpoint; otherwise a
// Deny default action admits only an address an IP rule names or one inside a
// subnet a virtual network rule names.
func kvNetworkRefusal(v KeyVault, r *http.Request, caller kvCaller) (string, string) {
	vaultRef := v.Name + ";location=" + v.Location
	if strings.EqualFold(v.Properties.PublicNetworkAccess, "Disabled") {
		return "ForbiddenByConnection", fmt.Sprintf(
			"Public network access is disabled and request is not from a trusted service nor via an approved private link.\r\nCaller: %s\r\nVault: %s",
			caller, vaultRef)
	}
	acls := v.Properties.NetworkAcls
	if acls == nil || !strings.EqualFold(acls.DefaultAction, "Deny") {
		return "", ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	client := net.ParseIP(host)
	if client != nil {
		for _, rule := range acls.IPRules {
			if kvAddressInRange(client, rule.Value) {
				return "", ""
			}
		}
		for _, rule := range acls.VirtualNetworkRules {
			if kvAddressInSubnet(client, rule.ID) {
				return "", ""
			}
		}
	}
	return "ForbiddenByFirewall", fmt.Sprintf(
		"Client address is not authorized and caller is not a trusted service.\r\nClient address: %s\r\nCaller: %s\r\nVault: %s",
		host, caller, vaultRef)
}

// kvAddressInRange matches an address against an IP rule, which names one
// address or a CIDR range.
func kvAddressInRange(client net.IP, value string) bool {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "/") {
		ip := net.ParseIP(value)
		return ip != nil && ip.Equal(client)
	}
	_, network, err := net.ParseCIDR(value)
	return err == nil && network.Contains(client)
}

var kvSubnetsByID sim.GenerationIndex[Subnet]

func kvAddressInSubnet(client net.IP, subnetID string) bool {
	if azureSubnets == nil {
		return false
	}
	// ARM resource IDs compare case-insensitively.
	subnet, ok := kvSubnetsByID.Lookup(azureSubnets, strings.ToLower(subnetID), func(s Subnet) []string {
		return []string{strings.ToLower(s.ID)}
	})
	if !ok {
		return false
	}
	prefixes := append([]string{subnet.Properties.AddressPrefix}, subnet.Properties.AddressPrefixes...)
	for _, prefix := range prefixes {
		if prefix != "" && kvAddressInRange(client, prefix) {
			return true
		}
	}
	return false
}

// kvUnauthorized writes Key Vault's 401: the Bearer challenge naming the
// vault's tenant authority and the Key Vault resource, and the error body.
func kvUnauthorized(w http.ResponseWriter, r *http.Request, v KeyVault, message string) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer authorization="%s/%s", resource="https://vault.azure.net"`,
		azureAuthBaseURL(r), v.Properties.TenantID))
	AzureError(w, "Unauthorized", message, http.StatusUnauthorized)
}

// kvForbidden writes Key Vault's 403 Forbidden, whose inner error code names
// what refused the request.
func kvForbidden(w http.ResponseWriter, inner, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":       "Forbidden",
			"message":    message,
			"innererror": map[string]string{"code": inner},
		},
	})
}
