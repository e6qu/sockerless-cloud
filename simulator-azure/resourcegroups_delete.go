package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ResourceGroups_Delete is a long-running operation: 202 with a Location under
// the subscription's operationresults, then each resource deleted through its
// own provider, then the empty group removed. A group whose resources cannot
// all be deleted stays, and the operation fails.
//
// Real API:
//
//	https://learn.microsoft.com/en-us/rest/api/resources/resource-groups/delete

// A repeated DELETE answers with the operation already running.
var (
	azureResourceGroupDeletesMu sync.Mutex
	azureResourceGroupDeletes   = map[string]string{}
)

func handleResourceGroupDelete(srv *sim.Server, w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rgName := sim.PathParam(r, "resourceGroupName")
	groupID := armResourceGroupID(sub, rgName)
	apiVersion := r.URL.Query().Get("api-version")

	azureResourceGroupDeletesMu.Lock()
	defer azureResourceGroupDeletesMu.Unlock()

	group, ok := azureResourceGroups.Get(groupID)
	if !ok {
		AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound,
			"Resource group '%s' could not be found.", rgName)
		return
	}
	key := strings.ToLower(groupID)
	opID, running := azureResourceGroupDeletes[key]
	if !running {
		group.Properties.ProvisioningState = "Deleting"
		azureResourceGroups.Put(groupID, group)
		host := r.Host
		opID = startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			opErr := deleteAzureResourceGroupResources(srv, sub, rgName, apiVersion, host)
			azureResourceGroupDeletesMu.Lock()
			defer azureResourceGroupDeletesMu.Unlock()
			if opErr == nil {
				azureResourceGroups.Delete(groupID)
			} else {
				azureResourceGroups.Update(groupID, func(g *ResourceGroup) {
					g.Properties.ProvisioningState = "Succeeded"
				})
			}
			delete(azureResourceGroupDeletes, key)
			return opErr
		})
		azureResourceGroupDeletes[key] = opID
	}
	w.Header().Set("Location", fmt.Sprintf("%s://%s/subscriptions/%s/operationresults/%s?api-version=%s",
		azureRequestScheme(r), r.Host, sub, opID, url.QueryEscape(apiVersion)))
	setAzureAsyncOperationRetryAfter(w, opID)
	w.WriteHeader(http.StatusAccepted)
}

// 202 while it runs, 200 once it succeeded, its error once it failed.
func handleSubscriptionOperationResult(w http.ResponseWriter, r *http.Request) {
	opID := sim.PathParam(r, "operationId")
	op, ok := azureAsyncOps.Get(opID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found.", opID)
		return
	}
	switch op.Status {
	case "InProgress":
		w.Header().Set("Location", azureCurrentRequestURL(r))
		w.Header().Set("Retry-After", azureAsyncOperationRetryAfter)
		w.WriteHeader(http.StatusAccepted)
	case "Failed":
		status := http.StatusInternalServerError
		if op.Error != nil && op.Error.Code == "ResourceGroupDeletionBlocked" {
			status = http.StatusConflict
		}
		sim.WriteJSON(w, status, map[string]any{"error": op.Error})
	default:
		w.WriteHeader(http.StatusOK)
	}
}

// A provider refusing because another resource depends on the resource is
// retried after a pass deletes others, so deletes land in dependency order; a
// pass deleting nothing ends with the refusals.
func deleteAzureResourceGroupResources(srv *sim.Server, sub, group, apiVersion, host string) *AsyncOperationError {
	ctx := srv.RequestContext(context.Background())
	for {
		rows := azureEnumerateResources(sub, group)
		if len(rows) == 0 {
			return nil
		}
		// A child's ID extends its parent's, so the longer ID goes first.
		sort.SliceStable(rows, func(i, j int) bool {
			return strings.Count(rows[i].ID, "/") > strings.Count(rows[j].ID, "/")
		})
		refusals := map[string]*AsyncOperationError{}
		for _, row := range rows {
			if opErr := deleteAzureResourceThroughProvider(ctx, srv, row.ID, apiVersion, host); opErr != nil {
				refusals[row.ID] = opErr
			}
		}
		remaining := azureEnumerateResources(sub, group)
		if len(remaining) < len(rows) {
			continue
		}
		ids := make([]string, 0, len(remaining))
		details := make([]map[string]any, 0, len(remaining))
		for _, row := range remaining {
			ids = append(ids, row.ID)
			detail := map[string]any{
				"code":    "ResourceDeletionFailed",
				"target":  row.ID,
				"message": fmt.Sprintf("The resource '%s' was not deleted.", row.ID),
			}
			if refusal, ok := refusals[row.ID]; ok {
				detail["code"] = refusal.Code
				detail["message"] = refusal.Message
			}
			details = append(details, detail)
		}
		return &AsyncOperationError{
			Code: "ResourceGroupDeletionBlocked",
			Message: fmt.Sprintf("Deletion of resource group '%s' failed as resources with identifiers '%s' could not be deleted. The provisioning state of the resource group will be rolled back.",
				group, strings.Join(ids, ",")),
			Details: details,
		}
	}
}

func deleteAzureResourceThroughProvider(ctx context.Context, srv *sim.Server, id, apiVersion, host string) *AsyncOperationError {
	target := &url.URL{Path: id, RawQuery: url.Values{"api-version": {apiVersion}}.Encode()}
	resp := serveAzureInternal(ctx, srv, http.MethodDelete, target, host)
	switch {
	case resp.Code == http.StatusAccepted:
		return followAzureProviderOperation(ctx, srv, resp, host)
	case resp.Code == http.StatusNotFound || resp.Code < 300:
		return nil
	default:
		return azureProviderRefusal(resp)
	}
}

// Polls Azure-AsyncOperation when given, Location otherwise.
func followAzureProviderOperation(ctx context.Context, srv *sim.Server, accepted *httptest.ResponseRecorder, host string) *AsyncOperationError {
	raw := accepted.Header().Get("Azure-AsyncOperation")
	statusPoll := raw != ""
	if !statusPoll {
		raw = accepted.Header().Get("Location")
	}
	if raw == "" {
		return nil
	}
	target, err := url.Parse(raw)
	if err != nil {
		return &AsyncOperationError{Code: "InternalServerError",
			Message: fmt.Sprintf("The provider named an operation URL that does not parse: %v", err)}
	}
	opID := target.Path[strings.LastIndex(target.Path, "/")+1:]
	lifetime := sim.LifetimeContext(ctx)
	for {
		settled := azureAsyncOpChanges.Changed(opID)
		if _, ours := azureAsyncOps.Get(opID); !ours {
			settled = nil
		}
		poll := serveAzureInternal(ctx, srv, http.MethodGet, target, host)
		if statusPoll {
			if poll.Code != http.StatusOK {
				return azureProviderRefusal(poll)
			}
			var envelope AsyncOperationStatus
			if err := json.Unmarshal(poll.Body.Bytes(), &envelope); err != nil {
				return &AsyncOperationError{Code: "InternalServerError",
					Message: fmt.Sprintf("The provider's operation status does not parse: %v", err)}
			}
			switch strings.ToLower(envelope.Status) {
			case "succeeded":
				return nil
			case "failed", "canceled":
				if envelope.Error != nil {
					return envelope.Error
				}
				return &AsyncOperationError{Code: "OperationFailed",
					Message: fmt.Sprintf("The provider's operation ended %s.", envelope.Status)}
			}
		} else {
			switch {
			case poll.Code == http.StatusAccepted:
			case poll.Code < 300:
				return nil
			default:
				return azureProviderRefusal(poll)
			}
		}
		var retry <-chan time.Time
		if settled == nil {
			// Poll on the cadence the provider's Retry-After asks for.
			retryAfter, err := strconv.Atoi(poll.Header().Get("Retry-After"))
			if err != nil || retryAfter < 1 {
				retryAfter = 1
			}
			retry = time.After(time.Duration(retryAfter) * time.Second)
		}
		select {
		case <-settled:
		case <-retry:
		case <-lifetime.Done():
			return &AsyncOperationError{Code: "OperationInterrupted",
				Message: "The service stopped before the resource's delete completed."}
		}
	}
}

// With the path folding every routed request gets.
func serveAzureInternal(ctx context.Context, srv *sim.Server, method string, target *url.URL, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, method, target.RequestURI(), nil)
	req.Host = host
	azureNormalizeRequestPath(req)
	recorder := httptest.NewRecorder()
	srv.Mux().ServeHTTP(recorder, req)
	return recorder
}

func azureProviderRefusal(resp *httptest.ResponseRecorder) *AsyncOperationError {
	var envelope struct {
		Error *AsyncOperationError `json:"error"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &envelope); err == nil && envelope.Error != nil && envelope.Error.Code != "" {
		return envelope.Error
	}
	return &AsyncOperationError{Code: http.StatusText(resp.Code),
		Message: fmt.Sprintf("The provider answered %d: %s", resp.Code, strings.TrimSpace(resp.Body.String()))}
}
