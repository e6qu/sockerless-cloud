package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// FederationTokenPath belongs to the console's own auth layer, not a cloud API:
// the browser asks its console for a cloud token, and the console runs the
// Microsoft Entra Workload Identity Federation exchange with the operator's
// Shauth assertion. Microsoft Entra serves no CORS for a client_credentials
// grant, so the exchange runs server-side; everything after the token stays in
// the browser against the cloud's own CORS surfaces.
const FederationTokenPath = "/auth/federation/token"

const federationExchangeTimeout = 30 * time.Second

// Shared so connections to Microsoft Entra pool across exchanges.
var federationExchangeClient = &http.Client{Timeout: federationExchangeTimeout}

// Azure Resource Manager, Log Analytics and Microsoft Graph are separate
// resources, each needing a token scoped to it from the same assertion.
var consoleFederationScopes = map[string]string{
	"arm":   "https://management.azure.com/.default",
	"logs":  "https://api.loganalytics.io/.default",
	"graph": "https://graph.microsoft.com/.default",
}

// An empty endpoint means the console's own origin, the co-served deployment.
// All three come from SOCKERLESS_CONSOLE_FEDERATION_{ENDPOINT,TENANT,CLIENT_ID}.
type consoleFederation struct {
	endpoint string
	tenant   string
	clientID string
}

// Loaded once at startup by buildSimulator; the zero value means federation is
// not configured.
var azureConsoleFederation consoleFederation

// The browser never learns the federation endpoint or client ID — the exchange
// runs server-side — only whether the broker is reachable
// (federationTokenEndpoint) and the tenant the portal's CLI samples show. Empty
// cloud coordinates mean the console's own origin. Without configured
// federation the broker is not advertised and the portal stays in its
// unauthenticated-reads mode.
func azureConsoleOptions(srv *sim.Server) sim.ConsoleOptions {
	return sim.ConsoleOptions{
		Coordinates: func(authenticated bool) map[string]string {
			federationTokenEndpoint := ""
			if authenticated && azureConsoleFederation.configured() {
				federationTokenEndpoint = FederationTokenPath
			}
			return map[string]string{
				"cloudApiEndpoint":        os.Getenv("SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT"),
				"logsApiEndpoint":         os.Getenv("SOCKERLESS_CONSOLE_LOGS_API_ENDPOINT"),
				"graphApiEndpoint":        os.Getenv("SOCKERLESS_CONSOLE_GRAPH_API_ENDPOINT"),
				"federationTokenEndpoint": federationTokenEndpoint,
				"federationTenant":        os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT"),
			}
		},
		AuthRoutes: map[string]http.HandlerFunc{
			"GET " + FederationTokenPath: func(w http.ResponseWriter, r *http.Request) {
				handleFederationToken(srv, w, r)
			},
		},
	}
}

func (f consoleFederation) configured() bool { return f.clientID != "" }

// loadConsoleFederation validates all-or-nothing: an incomplete set is a
// startup error, never filled with a default. Only the endpoint may be empty,
// meaning the console's own origin.
func loadConsoleFederation() (consoleFederation, error) {
	fed := consoleFederation{
		endpoint: strings.TrimRight(os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT"), "/"),
		tenant:   os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT"),
		clientID: os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID"),
	}
	if fed.endpoint == "" && fed.tenant == "" && fed.clientID == "" {
		return consoleFederation{}, nil
	}
	var missing []string
	if fed.clientID == "" {
		missing = append(missing, "SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID")
	}
	if fed.tenant == "" {
		missing = append(missing, "SOCKERLESS_CONSOLE_FEDERATION_TENANT")
	}
	if len(missing) > 0 {
		return consoleFederation{}, fmt.Errorf(
			"incomplete console federation configuration: %s must be set when any SOCKERLESS_CONSOLE_FEDERATION_* coordinate is",
			strings.Join(missing, ", "))
	}
	return fed, nil
}

// handleFederationToken sends the client_credentials grant with a JWT-bearer
// client_assertion, as a federating workload does, and returns only the token
// and its lifetime; the assertion never leaves the server.
func handleFederationToken(srv *sim.Server, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !azureConsoleFederation.configured() {
		writeBrokerJSON(w, http.StatusNotImplemented, map[string]any{
			"error":             "federation_not_configured",
			"error_description": "this deployment has not set SOCKERLESS_CONSOLE_FEDERATION_CLIENT_ID / _TENANT; the console has no cloud identity to federate the operator into",
		})
		return
	}
	scopeName := r.URL.Query().Get("scope")
	scope, known := consoleFederationScopes[scopeName]
	if !known {
		writeBrokerJSON(w, http.StatusBadRequest, map[string]any{
			"error":             "invalid_scope",
			"error_description": fmt.Sprintf("scope must be one of arm, logs, graph; got %q", scopeName),
		})
		return
	}
	assertion, _, _, ok := srv.UIAuth().OperatorAssertion(r)
	if !ok {
		writeBrokerJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}

	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {azureConsoleFederation.clientID},
		"scope":                 {scope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	status, contentType, body, err := exchangeFederationAssertion(srv, r, form)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]any{
			"error":             "federation_exchange_unreachable",
			"error_description": err.Error(),
		})
		return
	}
	if status != http.StatusOK {
		// Relay Microsoft Entra's own status and error body.
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &token); err != nil || token.AccessToken == "" {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]any{
			"error":             "federation_exchange_invalid",
			"error_description": "the token endpoint returned a response that is not a token",
		})
		return
	}
	writeBrokerJSON(w, http.StatusOK, map[string]any{
		"access_token": token.AccessToken,
		"token_type":   token.TokenType,
		"expires_in":   token.ExpiresIn,
	})
}

// An explicit endpoint is reached over HTTP like any confidential client; the
// empty one is served in-process through the full handler chain, the path an
// external request takes.
func exchangeFederationAssertion(srv *sim.Server, r *http.Request, form url.Values) (status int, contentType string, body []byte, err error) {
	tokenPath := "/" + azureConsoleFederation.tenant + "/oauth2/v2.0/token"
	encoded := form.Encode()
	if azureConsoleFederation.endpoint != "" {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			azureConsoleFederation.endpoint+tokenPath, strings.NewReader(encoded))
		if err != nil {
			return 0, "", nil, err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := federationExchangeClient.Do(request)
		if err != nil {
			return 0, "", nil, err
		}
		defer func() { _ = response.Body.Close() }()
		payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return 0, "", nil, err
		}
		return response.StatusCode, response.Header.Get("Content-Type"), payload, nil
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, tokenPath, strings.NewReader(encoded))
	if err != nil {
		return 0, "", nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Host = r.Host
	recorder := &federationResponseRecorder{header: make(http.Header), status: http.StatusOK}
	srv.ServeHTTP(recorder, request)
	return recorder.status, recorder.header.Get("Content-Type"), recorder.body.Bytes(), nil
}

type federationResponseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *federationResponseRecorder) Header() http.Header { return r.header }
func (r *federationResponseRecorder) WriteHeader(code int) {
	r.status = code
}
func (r *federationResponseRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func writeBrokerJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
