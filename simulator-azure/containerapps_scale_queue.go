package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// acaQueueReader reads the length of an Azure Queue scale rule's queue the way
// the rule's scaler does: Get Queue Metadata against the account's Queue
// service, signed with the account key of the connection string a secret
// holds, or carrying a token of the managed identity the rule names.
type acaQueueReader struct {
	appID    string
	identity *SiteIdentity
	secrets  map[string]string
}

func (q acaQueueReader) length(ctx context.Context, rule ContainerAppQueueScaleRule) (int, error) {
	var connection map[string]string
	for _, auth := range rule.Auth {
		if !strings.EqualFold(auth.TriggerParameter, "connection") {
			continue
		}
		value, ok := q.secrets[auth.SecretRef]
		if !ok {
			return 0, fmt.Errorf("the rule's connection names secret %q, which the app does not hold", auth.SecretRef)
		}
		connection = acaConnectionStringFields(value)
	}
	account := rule.AccountName
	if account == "" {
		account = connection["AccountName"]
	}
	if account == "" || rule.QueueName == "" {
		return 0, errors.New("the rule names no storage account or no queue")
	}
	ctx = sim.WithCaller(azureServer.RequestContext(ctx), sim.Caller{Service: "Microsoft.App", Source: q.appID})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/"+rule.QueueName+"?comp=metadata", nil)
	if err != nil {
		return 0, err
	}
	req.Host = account + ".queue.core.windows.net"
	req.Header.Set("x-ms-version", "2021-12-02")
	req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	switch {
	case connection != nil:
		key, err := base64.StdEncoding.DecodeString(connection["AccountKey"])
		if err != nil || len(key) == 0 {
			return 0, errors.New("the rule's connection string carries no account key")
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(blobSharedKeyStringToSign(account, req, false)))
		req.Header.Set("Authorization", "SharedKey "+account+":"+base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	case rule.Identity != "":
		token, err := q.token(rule.Identity)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	default:
		return 0, errors.New("the rule names neither a connection secret nor a managed identity")
	}
	response := azureServer.Call(req)
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the Queue service answered Get Queue Metadata with %d: %s", response.StatusCode, response.Body.String())
	}
	return strconv.Atoi(response.Header().Get("x-ms-approximate-messages-count"))
}

// token is a Microsoft Entra token for Azure Storage of the app's managed
// identity the rule names: "system", or a user-assigned identity's resource
// ID.
func (q acaQueueReader) token(name string) (string, error) {
	if q.identity == nil {
		return "", fmt.Errorf("the rule names identity %q, but the app has no managed identity", name)
	}
	var principalID, clientID, resourceID string
	if strings.EqualFold(name, "system") {
		if !q.identity.systemAssigned() || q.identity.PrincipalID == "" {
			return "", errors.New("the rule names the system-assigned identity, which the app does not have")
		}
		sp, ok := entraServicePrincipalStore.Get(q.identity.PrincipalID)
		if !ok {
			return "", errors.New("the app's system-assigned identity has no service principal")
		}
		principalID, clientID, resourceID = q.identity.PrincipalID, sp.AppID, q.appID
	} else {
		for rid, uai := range q.identity.UserAssignedIdentities {
			if uai != nil && strings.EqualFold(rid, name) {
				principalID, clientID, resourceID = uai.PrincipalID, uai.ClientID, rid
			}
		}
		if principalID == "" {
			return "", fmt.Errorf("the rule names identity %q, which is not assigned to the app", name)
		}
	}
	now := time.Now()
	return mintAzureSimSignedJWT(map[string]any{
		"aud":       "https://storage.azure.com/",
		"iss":       fmt.Sprintf("https://sts.windows.net/%s/", simTenantID),
		"tid":       simTenantID,
		"oid":       principalID,
		"sub":       principalID,
		"appid":     clientID,
		"idtyp":     "app",
		"xms_mirid": resourceID,
		"iat":       now.Unix(),
		"nbf":       now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
		"ver":       "1.0",
	})
}

// acaConnectionStringFields splits a storage connection string into its
// key=value fields.
func acaConnectionStringFields(connection string) map[string]string {
	fields := map[string]string{}
	for _, part := range strings.Split(connection, ";") {
		if name, value, ok := strings.Cut(strings.TrimSpace(part), "="); ok {
			fields[name] = value
		}
	}
	return fields
}
