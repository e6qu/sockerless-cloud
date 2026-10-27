package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One Google Cloud service reaches another through that service's API, as
// Eventarc creates its transport topic and subscription with the Pub/Sub API
// and publishes to it: the request takes the same path a client's does, so
// the receiving service authenticates it and answers in its own shapes.

var gcpServer *sim.Server

// gcpServiceAgent is the Google-managed service account a service acts as in
// a project, service-{project number}@gcp-sa-{service}.iam.gserviceaccount.com.
func gcpServiceAgent(projectNumber, service string) string {
	return "service-" + projectNumber + "@gcp-sa-" + service + ".iam.gserviceaccount.com"
}

// gcpCallError is a call the target service refused, in its own status.
type gcpCallError struct {
	Code    int
	Status  string
	Message string
}

func (e *gcpCallError) Error() string { return fmt.Sprintf("%d %s: %s", e.Code, e.Status, e.Message) }

// gcpCallAs sends a request to the simulator's own API as principal, with input
// as the JSON body, and decodes the answer into output when output is
// non-nil. A refusal is a *gcpCallError carrying the service's status.
func gcpCallAs(ctx context.Context, principal, method, path string, input, output any) error {
	var body []byte
	if input != nil {
		var err error
		if body, err = json.Marshal(input); err != nil {
			return err
		}
	}
	ctx = sim.WithCaller(gcpServer.RequestContext(ctx), sim.Caller{Service: principal})
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	now := time.Now()
	req.Header.Set("Authorization", "Bearer "+signAccessToken(principal, now, now.Add(time.Hour)))
	response := gcpServer.Call(req)
	if response.StatusCode >= 400 {
		var failure struct {
			Error struct {
				Code    int    `json:"code"`
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &failure)
		return &gcpCallError{Code: response.StatusCode, Status: failure.Error.Status, Message: failure.Error.Message}
	}
	if output != nil && response.Body.Len() > 0 {
		return json.Unmarshal(response.Body.Bytes(), output)
	}
	return nil
}
