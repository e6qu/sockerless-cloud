// Package simulator provides a shared framework for building cloud service simulators.
//
// Each simulator is an HTTP server that implements a subset of a cloud provider's
// REST API surface. The framework provides common infrastructure: HTTP server,
// request routing, in-memory state management, authentication passthrough,
// and provider-specific error formatting.
package simulator

import "os"

// Config holds the simulator server configuration.
type Config struct {
	// ListenAddr is the address to listen on (e.g., ":4566").
	ListenAddr string

	// TLSCert is the path to the TLS certificate file. Empty disables TLS.
	TLSCert string

	// TLSKey is the path to the TLS private key file.
	TLSKey string

	// LogLevel is the zerolog log level (trace, debug, info, warn, error).
	LogLevel string

	// Provider identifies the cloud provider (aws, gcp, azure).
	Provider string

	// DataDir is the directory for persistent storage (SQLite database, PID files).
	// Set via SIM_DATA_DIR. Empty uses a temp directory.
	DataDir string

	// Persist enables SQLite-backed state persistence.
	// Set via SIM_PERSIST=true. Default false (in-memory only).
	Persist bool

	// LogWriter, when non-nil, is added alongside the ConsoleWriter
	// in a MultiLevelWriter so each zerolog event also flows to OTel
	// logs via the OTelLogWriter bridge. main.go assigns this from
	// InitObservability when the operator brought up the OTel stack
	// . Unset = today's stderr-only behaviour.
	LogWriter *OTelLogWriter

	UIOIDCIssuer               string
	UIOIDCClientID             string
	UIOIDCClientSecret         string
	UIPublicURL                string
	UISessionSecret            string
	UIOIDCInsecureCookies      bool
	ApplicationReleaseRevision string
}

// ConfigFromEnv loads configuration from environment variables.
//
//	SIM_LISTEN_ADDR — listen address (default ":8443")
//	SIM_TLS_CERT    — TLS certificate file path
//	SIM_TLS_KEY     — TLS private key file path
//	SIM_LOG_LEVEL   — log level (default "info")
//	SIM_UI_OIDC_ISSUER        — Shauth or another OpenID Connect issuer
//	SIM_UI_OIDC_CLIENT_ID     — simulator dashboard relying-party client ID
//	SIM_UI_OIDC_CLIENT_SECRET — simulator dashboard relying-party secret
//	SIM_UI_PUBLIC_URL         — externally visible simulator origin
//	SIM_UI_SESSION_SECRET     — random secret used to sign local sessions
//	APPLICATION_RELEASE_REVISION — immutable deployed release exposed to Shauth validation
func ConfigFromEnv(provider string) Config {
	return Config{
		ListenAddr:                 envOrDefault("SIM_LISTEN_ADDR", ":8443"),
		TLSCert:                    os.Getenv("SIM_TLS_CERT"),
		TLSKey:                     os.Getenv("SIM_TLS_KEY"),
		LogLevel:                   envOrDefault("SIM_LOG_LEVEL", "info"),
		Provider:                   provider,
		DataDir:                    os.Getenv("SIM_DATA_DIR"),
		Persist:                    os.Getenv("SIM_PERSIST") == "true" || os.Getenv("SIM_PERSIST") == "1",
		UIOIDCIssuer:               os.Getenv("SIM_UI_OIDC_ISSUER"),
		UIOIDCClientID:             os.Getenv("SIM_UI_OIDC_CLIENT_ID"),
		UIOIDCClientSecret:         os.Getenv("SIM_UI_OIDC_CLIENT_SECRET"),
		UIPublicURL:                os.Getenv("SIM_UI_PUBLIC_URL"),
		UISessionSecret:            os.Getenv("SIM_UI_SESSION_SECRET"),
		UIOIDCInsecureCookies:      os.Getenv("SIM_UI_INSECURE_COOKIES") == "true",
		ApplicationReleaseRevision: os.Getenv("APPLICATION_RELEASE_REVISION"),
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
