package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Event Grid proves a webhook endpoint wants its events before it delivers
// any. Creating a subscription, or pointing one at a new endpoint, is a
// long-running operation: an Event Grid schema subscription sends the endpoint
// a SubscriptionValidationEvent and succeeds when the response echoes its
// validationCode as validationResponse; an endpoint that answers without the
// echo leaves the subscription AwaitingManualAction until someone GETs the
// event's validationUrl, which stays valid for five minutes. A CloudEvents
// subscription runs the CloudEvents abuse-protection handshake instead: an
// OPTIONS request carrying WebHook-Request-Origin, which the endpoint allows by
// answering WebHook-Allowed-Origin. Any other answer fails the subscription.

const (
	eventGridValidationWindow = 5 * time.Minute
	eventGridRequestOrigin    = "eventgrid.azure.net"
)

// eventGridValidation is a webhook subscription's handshake awaiting a visit to
// its validation URL, keyed by its validation code.
type eventGridValidation struct {
	Code           string    `json:"code"`
	Token          string    `json:"token"`
	SubscriptionID string    `json:"subscriptionId"`
	OperationID    string    `json:"operationId"`
	Expires        time.Time `json:"expires"`
}

var eventGridValidations sim.Store[eventGridValidation]

var eventGridValidationClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func registerEventGridValidation(srv *sim.Server) {
	eventGridValidations = sim.MakeStore[eventGridValidation](srv.DB(), "eventgrid_validations")
	// A handshake's expiry timer died with the process that armed it.
	for _, v := range eventGridValidations.List() {
		eventGridFailValidation(v.SubscriptionID, v.OperationID,
			"The subscription's manual validation was interrupted by a service restart. Create the subscription again.")
		eventGridValidations.Delete(v.Code)
	}
	srv.HandleFunc("GET /eventsubscriptions/{eventSubscriptionName}/validate", handleEventGridValidationURL)
}

// eventGridNeedsValidation reports whether a create or update of es must run
// the handshake: its destination is a webhook it has not already validated.
func eventGridNeedsValidation(es EventGridEventSubscription, prior *EventGridEventSubscription) bool {
	dest, _ := es.Properties["destination"].(map[string]any)
	if endpointType, _ := dest["endpointType"].(string); !strings.EqualFold(endpointType, "WebHook") {
		return false
	}
	if eventGridWebhookEndpoint(es) == "" {
		return false
	}
	if prior == nil || prior.Properties["provisioningState"] != "Succeeded" {
		return true
	}
	return eventGridWebhookEndpoint(*prior) != eventGridWebhookEndpoint(es)
}

// startEventGridValidation runs es's handshake in the background and returns
// the operation that reports it.
func startEventGridValidation(r *http.Request, es EventGridEventSubscription) string {
	opID := sim.NewUUID()
	azureAsyncOps.Put(opID, AsyncOperationStatus{
		Name:      opID,
		Status:    "InProgress",
		StartTime: time.Now().UTC().Format(time.RFC3339Nano),
	})
	code := sim.NewUUID()
	token := sim.NewUUID()
	query := url.Values{"id": {code}, "t": {time.Now().UTC().Format(time.RFC3339Nano)}, "apiVersion": {r.URL.Query().Get("api-version")}, "token": {token}}
	validationURL := fmt.Sprintf("%s://%s/eventsubscriptions/%s/validate?%s", azureRequestScheme(r), r.Host, url.PathEscape(es.Name), query.Encode())
	bg.Go(func() {
		eventGridHandshake(es, opID, code, token, validationURL)
	})
	return opID
}

// writeEventGridValidationAccepted advertises the handshake's operation.
func writeEventGridValidationAccepted(w http.ResponseWriter, r *http.Request, location, opID string) {
	if location == "" {
		location = "global"
	}
	w.Header().Set("Azure-AsyncOperation", azureAsyncOperationHeader(r, sim.PathParam(r, "subscriptionId"),
		"Microsoft.EventGrid", strings.ToLower(strings.ReplaceAll(location, " ", "")), "operationStatuses", opID, r.URL.Query().Get("api-version")))
	setAzureAsyncOperationRetryAfter(w, opID)
}

func eventGridHandshake(es EventGridEventSubscription, opID, code, token, validationURL string) {
	endpoint := eventGridWebhookEndpoint(es)
	if schema, _ := es.Properties["eventDeliverySchema"].(string); strings.EqualFold(schema, "CloudEventSchemaV1_0") {
		if err := eventGridAbuseProtectionHandshake(endpoint); err != nil {
			eventGridFailValidation(es.ID, opID, eventGridHandshakeFailure(endpoint, "Http OPTIONS", err))
			return
		}
		eventGridSucceedValidation(es.ID, opID)
		return
	}
	eventGridValidations.Put(code, eventGridValidation{
		Code: code, Token: token, SubscriptionID: es.ID, OperationID: opID,
		Expires: time.Now().Add(eventGridValidationWindow),
	})
	echoed, err := eventGridPostValidationEvent(es, endpoint, code, validationURL)
	switch {
	case err != nil:
		eventGridValidations.Delete(code)
		eventGridFailValidation(es.ID, opID, eventGridHandshakeFailure(endpoint, "Http POST", err))
	case echoed == code:
		eventGridValidations.Delete(code)
		eventGridSucceedValidation(es.ID, opID)
	default:
		eventGridSubscriptions.Update(es.ID, func(sub *EventGridEventSubscription) {
			if sub.Properties["provisioningState"] == "Creating" {
				sub.Properties["provisioningState"] = "AwaitingManualAction"
			}
		})
		bg.AfterFunc(eventGridValidationWindow, func() {
			if _, pending := eventGridValidations.Get(code); !pending {
				return
			}
			eventGridValidations.Delete(code)
			eventGridFailValidation(es.ID, opID, fmt.Sprintf(
				"Webhook validation handshake failed for %s. The validation URL was not visited within %s of the validation request. For troubleshooting, visit https://aka.ms/esvalidation.",
				endpoint, eventGridValidationWindow))
		})
	}
}

// eventGridPostValidationEvent delivers the SubscriptionValidationEvent and
// returns the validationResponse the endpoint echoed, if any. A transport
// failure or a status outside 2xx is an error.
func eventGridPostValidationEvent(es EventGridEventSubscription, endpoint, code, validationURL string) (string, error) {
	payload, err := json.Marshal([]map[string]any{{
		"id":        sim.NewUUID(),
		"topic":     es.Properties["topic"],
		"subject":   "",
		"eventType": "Microsoft.EventGrid.SubscriptionValidationEvent",
		"eventTime": time.Now().UTC().Format(time.RFC3339Nano),
		"data": map[string]any{
			"validationCode": code,
			"validationUrl":  validationURL,
		},
		"dataVersion":     "2",
		"metadataVersion": "1",
	}})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("aeg-event-type", "SubscriptionValidation")
	req.Header.Set("aeg-subscription-name", strings.ToUpper(es.Name))
	req.Header.Set("aeg-delivery-count", "0")
	req.Header.Set("aeg-metadata-version", "1")
	req.Header.Set("aeg-data-version", "2")
	resp, err := eventGridValidationClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("response code %d", resp.StatusCode)
	}
	var answer struct {
		ValidationResponse string `json:"validationResponse"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return "", nil
	}
	return answer.ValidationResponse, nil
}

// eventGridAbuseProtectionHandshake runs the CloudEvents webhook validation:
// the endpoint must allow Event Grid's origin.
func eventGridAbuseProtectionHandshake(endpoint string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodOptions, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("WebHook-Request-Origin", eventGridRequestOrigin)
	resp, err := eventGridValidationClient.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("response code %d", resp.StatusCode)
	}
	for _, allowed := range strings.Split(resp.Header.Get("WebHook-Allowed-Origin"), ",") {
		if allowed = strings.TrimSpace(allowed); allowed == "*" || strings.EqualFold(allowed, eventGridRequestOrigin) {
			return nil
		}
	}
	return fmt.Errorf("the response did not allow origin %s in WebHook-Allowed-Origin", eventGridRequestOrigin)
}

func eventGridHandshakeFailure(endpoint, request string, err error) string {
	return fmt.Sprintf("Webhook validation handshake failed for %s. %s request failed: %v. For troubleshooting, visit https://aka.ms/esvalidation.", endpoint, request, err)
}

func eventGridSucceedValidation(subscriptionID, opID string) {
	eventGridSubscriptions.Update(subscriptionID, func(sub *EventGridEventSubscription) {
		sub.Properties["provisioningState"] = "Succeeded"
	})
	azureAsyncOps.Update(opID, func(op *AsyncOperationStatus) {
		settleAzureAsyncOperation(op, nil, nil)
	})
}

func eventGridFailValidation(subscriptionID, opID, message string) {
	eventGridSubscriptions.Update(subscriptionID, func(sub *EventGridEventSubscription) {
		sub.Properties["provisioningState"] = "Failed"
	})
	azureAsyncOps.Update(opID, func(op *AsyncOperationStatus) {
		settleAzureAsyncOperation(op, nil, &AsyncOperationError{Code: "Url validation", Message: message})
	})
}

// handleEventGridValidationURL completes a manual validation: a GET of the
// validationUrl a SubscriptionValidationEvent carried.
func handleEventGridValidationURL(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("id")
	v, ok := eventGridValidations.Get(code)
	if !ok || v.Token != r.URL.Query().Get("token") || time.Now().After(v.Expires) {
		http.Error(w, "The validation request is invalid or has expired.", http.StatusBadRequest)
		return
	}
	eventGridValidations.Delete(code)
	eventGridSucceedValidation(v.SubscriptionID, v.OperationID)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Webhook successfully validated as a subscription endpoint.")
}
