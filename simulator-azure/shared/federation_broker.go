package simulator

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
)

// FederationTokenPath is the console authentication layer's server-side
// federation broker. Like /auth/session and /auth/federation-subject it belongs
// to the console's own auth layer, not to any cloud API: the browser asks its
// own console for a cloud token, and the console performs the Microsoft Entra
// Workload Identity Federation exchange server-side with the signed-in
// operator's Shauth assertion. The exchange must run server-side because real
// Microsoft Entra serves no cross-origin response for a client_credentials
// grant — a browser cannot read the token response — while everything after the
// token (Azure Resource Manager, Microsoft Graph, and Log Analytics reads)
// stays browser-side against the cloud's own CORS-serving surfaces.
const FederationTokenPath = "/auth/federation/token"

// federationExchangeTimeout bounds the server-side token exchange.
const federationExchangeTimeout = 30 * time.Second

// federationExchangeClient is shared across broker exchanges so connections
// to the Microsoft Entra endpoint are pooled and reused instead of being
// re-dialed (and re-handshaken) on every token request.
var federationExchangeClient = &http.Client{Timeout: federationExchangeTimeout}

// consoleFederationScopes maps the broker's scope names to the resource scopes
// Microsoft Entra issues tokens for. Azure Resource Manager, Log Analytics,
// and Microsoft Graph are separate resources, each reached with a token scoped
// to it; real Azure issues each from the same federated assertion.
var consoleFederationScopes = map[string]string{
	"arm":   "https://management.azure.com/.default",
	"logs":  "https://api.loganalytics.io/.default",
	"graph": "https://graph.microsoft.com/.default",
}

// consoleFederation holds the coordinates the console's federation broker
// exchanges at: the Microsoft Entra endpoint (empty = the console's own
// origin, the co-served deployment), the directory tenant, and the client ID
// of the identity the console federates as. All three come from
// SOCKERLESS_CONSOLE_FEDERATION_{ENDPOINT,TENANT,CLIENT_ID}.
type consoleFederation struct {
	endpoint string
	tenant   string
	clientID string
}

func (f consoleFederation) configured() bool { return f.clientID != "" }

// loadConsoleFederation reads the federation coordinates from the environment
// and validates them all-or-nothing: setting any federation coordinate is the
// signal a deployment intends to federate, and an incomplete set is a
// deployment error surfaced at startup, never patched with a default. The
// endpoint alone may be empty — that is the deliberate "same origin as the
// console" coordinate for a console co-served with its cloud.
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

// handleFederationToken exchanges the signed-in operator's Shauth assertion
// for a short-lived Azure token at the configured Microsoft Entra coordinate —
// the client_credentials grant with a JWT-bearer client_assertion, exactly the
// request a workload federating into Azure sends — and returns only the token
// and its lifetime to the browser. The assertion never leaves the server.
func (s *Server) handleFederationToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.federation.configured() {
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
	assertion, _, _, ok := s.uiAuth.OperatorAssertion(r)
	if !ok {
		writeBrokerJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false})
		return
	}

	form := url.Values{
		"grant_type":            {"client_credentials"},
		"client_id":             {s.federation.clientID},
		"scope":                 {scope},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
	}
	status, contentType, body, err := s.exchangeFederationAssertion(r, form)
	if err != nil {
		writeBrokerJSON(w, http.StatusBadGateway, map[string]any{
			"error":             "federation_exchange_unreachable",
			"error_description": err.Error(),
		})
		return
	}
	if status != http.StatusOK {
		// A refused exchange is surfaced, not hidden: relay Microsoft Entra's
		// own status and error body so the operator sees the real failure.
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

// exchangeFederationAssertion posts the client_credentials form to the
// Microsoft Entra token endpoint at the configured coordinate. An explicit
// endpoint is reached over HTTP exactly as any confidential client reaches
// Entra; the empty endpoint is the console's own origin — the co-served
// deployment — where the same request is served in-process through the full
// handler chain, the same code path an external client's request takes.
func (s *Server) exchangeFederationAssertion(r *http.Request, form url.Values) (status int, contentType string, body []byte, err error) {
	tokenPath := "/" + s.federation.tenant + "/oauth2/v2.0/token"
	encoded := form.Encode()
	if s.federation.endpoint != "" {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
			s.federation.endpoint+tokenPath, strings.NewReader(encoded))
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
	s.finalHandler().ServeHTTP(recorder, request)
	return recorder.status, recorder.header.Get("Content-Type"), recorder.body.Bytes(), nil
}

// federationResponseRecorder captures the in-process token exchange response.
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
