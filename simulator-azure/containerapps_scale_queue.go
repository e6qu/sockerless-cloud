package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// connection is the connection string the rule's `connection` trigger
// parameter takes from a secret, split into its fields, or nil.
func (q acaQueueReader) connection(auths []ContainerAppScaleRuleAuth) (map[string]string, error) {
	for _, auth := range auths {
		if !strings.EqualFold(auth.TriggerParameter, "connection") {
			continue
		}
		value, ok := q.secrets[auth.SecretRef]
		if !ok {
			return nil, fmt.Errorf("the rule's connection names secret %q, which the app does not hold", auth.SecretRef)
		}
		return acaConnectionStringFields(value), nil
	}
	return nil, nil
}

func (q acaQueueReader) length(ctx context.Context, rule ContainerAppQueueScaleRule) (int, error) {
	connection, err := q.connection(rule.Auth)
	if err != nil {
		return 0, err
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

// serviceBusLength reads the active messages of an azure-servicebus rule's
// queue, or of its topic's subscription, from the namespace's management
// endpoint, with a Shared Access Signature the connection string's key signs.
func (q acaQueueReader) serviceBusLength(ctx context.Context, rule ContainerAppCustomScaleRule) (int, error) {
	connection, err := q.connection(rule.Auth)
	if err != nil {
		return 0, err
	}
	if connection == nil {
		return 0, errors.New("the rule names no connection secret, and the Service Bus management endpoint takes a Shared Access Signature")
	}
	endpoint, err := url.Parse(connection["Endpoint"])
	if err != nil || endpoint.Host == "" {
		return 0, fmt.Errorf("the rule's connection string names no endpoint: %q", connection["Endpoint"])
	}
	entity := rule.Metadata["queueName"]
	if entity == "" {
		topic, subscription := rule.Metadata["topicName"], rule.Metadata["subscriptionName"]
		if topic == "" || subscription == "" {
			return 0, errors.New("the rule names neither a queue nor a topic and subscription")
		}
		entity = topic + "/Subscriptions/" + subscription
	}
	// The Azure SDKs sign the escaped, lower-cased audience.
	audience := strings.ToLower(url.QueryEscape("https://" + endpoint.Host + "/" + entity))
	expiry := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(connection["SharedAccessKey"]))
	mac.Write([]byte(audience + "\n" + expiry))
	token := "SharedAccessSignature sr=" + audience + "&sig=" +
		url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil))) + "&se=" + expiry + "&skn=" + connection["SharedAccessKeyName"]

	ctx = sim.WithCaller(azureServer.RequestContext(ctx), sim.Caller{Service: "Microsoft.App", Source: q.appID})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/"+entity+"?api-version=2021-05", nil)
	if err != nil {
		return 0, err
	}
	req.Host = endpoint.Host
	req.Header.Set("Authorization", token)
	response := azureServer.Call(req)
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the Service Bus management endpoint answered %s with %d: %s", entity, response.StatusCode, response.Body.String())
	}
	decoder := xml.NewDecoder(&response.Body)
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			return 0, fmt.Errorf("the description of %s carries no ActiveMessageCount", entity)
		}
		if err != nil {
			return 0, err
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "ActiveMessageCount" {
			var count int
			if err := decoder.DecodeElement(&count, &start); err != nil {
				return 0, err
			}
			return count, nil
		}
	}
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
