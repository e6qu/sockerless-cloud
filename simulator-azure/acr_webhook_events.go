package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Webhook delivery as container-registry-webhook-reference describes it: a
// push or a manifest delete the webhook's actions and scope select POSTs the
// event to its service URI with its custom headers, Content-Type
// application/json unless a custom header sets one, and the registry records
// the request and the endpoint's response as the webhook's event history.

type acrWebhookEvent struct {
	ID        string         `json:"id"`
	WebhookID string         `json:"webhookId"`
	At        time.Time      `json:"at"`
	Request   map[string]any `json:"eventRequestMessage"`
	Response  map[string]any `json:"eventResponseMessage"`
}

var acrWebhookEvents sim.Store[acrWebhookEvent]

const acrWebhookDeliveryTimeout = 30 * time.Second

// acrMaskedHeader is how the event history shows the Authorization header,
// which the registry masks.
const acrMaskedHeader = "*****"

// acrWebhookMatches reports whether a webhook's scope ("repo", "repo:tag" or
// "repo:*"; empty for the whole registry) selects an event on repo:tag.
func acrWebhookMatches(scope, repo, tag string) bool {
	if scope == "" {
		return true
	}
	wantRepo, wantTag, hasTag := strings.Cut(scope, ":")
	if !strings.EqualFold(wantRepo, repo) {
		return false
	}
	return !hasTag || wantTag == "*" || wantTag == tag
}

func acrWebhookRequestInfo(r *http.Request) map[string]any {
	if r == nil {
		return nil
	}
	id := r.Header.Get("X-Ms-Request-Id")
	if id == "" {
		id = sim.NewUUID()
	}
	return map[string]any{"id": id, "addr": r.RemoteAddr, "host": r.Host, "method": r.Method, "useragent": r.UserAgent()}
}

// acrFireWebhooks delivers an event to every enabled webhook of the registry
// that subscribes to the action and whose scope covers the repository and tag.
func acrFireWebhooks(registryID, action, repo, tag string, target map[string]any, r *http.Request) {
	if acrWebhooks == nil {
		return
	}
	prefix := strings.ToLower(registryID) + "/webhooks/"
	for _, wh := range acrWebhooks.Filter(func(wh acrWebhookStored) bool { return strings.HasPrefix(strings.ToLower(wh.ID), prefix) }) {
		if strings.EqualFold(wh.Status, "disabled") {
			continue
		}
		subscribed := false
		for _, a := range wh.Actions {
			if strings.EqualFold(a, action) {
				subscribed = true
			}
		}
		if !subscribed || !acrWebhookMatches(wh.Scope, repo, tag) {
			continue
		}
		content := map[string]any{
			"id":        sim.NewUUID(),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"action":    action,
			"target":    target,
		}
		if info := acrWebhookRequestInfo(r); info != nil {
			content["request"] = info
		}
		hook := wh
		bg.Go(func() { acrDeliverWebhook(hook, content) })
	}
}

// acrDeliverWebhook POSTs one event and records the exchange.
func acrDeliverWebhook(wh acrWebhookStored, content map[string]any) acrWebhookEvent {
	body, _ := json.Marshal(content)
	headers := map[string]string{}
	for k, v := range wh.CustomHeaders {
		headers[k] = v
	}
	hasContentType := false
	for k := range headers {
		if strings.EqualFold(k, "Content-Type") {
			hasContentType = true
		}
	}
	if !hasContentType {
		headers["Content-Type"] = "application/json"
	}
	recorded := map[string]string{}
	for k, v := range headers {
		if strings.EqualFold(k, "Authorization") {
			v = acrMaskedHeader
		}
		recorded[k] = v
	}
	id, _ := content["id"].(string)
	event := acrWebhookEvent{
		ID:        id,
		WebhookID: wh.ID,
		At:        time.Now().UTC(),
		Request: map[string]any{
			"content":    content,
			"headers":    recorded,
			"method":     http.MethodPost,
			"requestUri": wh.ServiceURI,
			"version":    "1.1",
		},
	}
	response := map[string]any{"version": "1.1"}
	req, err := http.NewRequest(http.MethodPost, wh.ServiceURI, bytes.NewReader(body))
	if err == nil {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := &http.Client{Timeout: acrWebhookDeliveryTimeout}
		var resp *http.Response
		if resp, err = client.Do(req); err == nil {
			payload, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			respHeaders := map[string]string{}
			for k := range resp.Header {
				v := resp.Header.Get(k)
				if strings.EqualFold(k, "Authorization") {
					v = acrMaskedHeader
				}
				respHeaders[k] = v
			}
			response["statusCode"] = strconv.Itoa(resp.StatusCode)
			response["reasonPhrase"] = http.StatusText(resp.StatusCode)
			response["headers"] = respHeaders
			response["content"] = string(payload)
		}
	}
	if err != nil {
		response["content"] = err.Error()
	}
	event.Response = response
	acrWebhookEvents.Put(event.ID, event)
	return event
}

// acrWebhookEventList is the webhook's history, newest first.
func acrWebhookEventList(webhookID string) []map[string]any {
	events := acrWebhookEvents.Filter(func(e acrWebhookEvent) bool { return strings.EqualFold(e.WebhookID, webhookID) })
	sort.Slice(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{"id": e.ID, "eventRequestMessage": e.Request, "eventResponseMessage": e.Response})
	}
	return out
}

func acrDropWebhookEvents(webhookID string) {
	for _, e := range acrWebhookEvents.Filter(func(e acrWebhookEvent) bool { return strings.EqualFold(e.WebhookID, webhookID) }) {
		acrWebhookEvents.Delete(e.ID)
	}
}

// Included storage and webhooks per SKU, from container-registry-skus.
var acrSKULimits = map[string]struct{ storageGiB, webhooks int64 }{
	"basic":    {10, 2},
	"standard": {100, 10},
	"premium":  {500, 500},
}

func acrSKULimit(reg Registry) (storage, webhooks int64) {
	name := "basic"
	if reg.Sku != nil && reg.Sku.Name != "" {
		name = strings.ToLower(reg.Sku.Name)
	}
	limits, ok := acrSKULimits[name]
	if !ok {
		limits = acrSKULimits["basic"]
	}
	return limits.storageGiB << 30, limits.webhooks
}

// acrStoredBytes counts each blob and manifest the registry holds once.
func acrStoredBytes(oci *sim.OCIRegistry, registryID string) int64 {
	var total int64
	seen := map[string]bool{}
	for _, b := range oci.Blobs.Filter(func(b sim.OCIBlob) bool { return b.Scope == registryID }) {
		if !seen[b.Digest] {
			seen[b.Digest] = true
			total += int64(len(b.Data))
		}
	}
	for _, m := range oci.Manifests.Filter(func(m sim.OCIManifest) bool { return m.Scope == registryID }) {
		if !seen[m.Digest] {
			seen[m.Digest] = true
			total += int64(len(m.Data))
		}
	}
	return total
}

func acrWebhookCount(registryID string) int64 {
	prefix := strings.ToLower(registryID) + "/webhooks/"
	return int64(len(acrWebhooks.Filter(func(wh acrWebhookStored) bool { return strings.HasPrefix(strings.ToLower(wh.ID), prefix) })))
}
