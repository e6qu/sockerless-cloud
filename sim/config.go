// Package simulator provides a shared framework for building cloud service simulators.
//
// Each simulator is an HTTP server that implements a subset of a cloud provider's
// REST API surface. The framework provides common infrastructure: HTTP server,
// request routing, in-memory state management, authentication passthrough,
// and provider-specific error formatting.
package sim

import (
	"crypto/tls"
	"net/http"
	"os"
	"strings"
)

// Config holds the simulator server configuration.
type Config struct {
	// ListenAddr is the address to listen on (e.g., ":4566").
	ListenAddr string

	// TLSCert is the path to the TLS certificate file. Empty disables TLS.
	TLSCert string

	// TLSKey is the path to the TLS private key file.
	TLSKey string

	// CallbackListenAddr is the plain-HTTP address the simulator serves beside
	// a TLS listener for the workloads it places: the instance metadata and
	// identity endpoints their network sidecars forward to, which a container
	// reaches without the operator's certificate authority. It serves the
	// whole handler chain, plain. Set via SIM_CALLBACK_LISTEN_ADDR; empty
	// takes a free port on every interface, and Listen records the bound
	// address here. Without TLS the one listener serves everything and this
	// is ListenAddr.
	CallbackListenAddr string

	// TLSConfigForClient, when set, picks a connection's TLS configuration
	// from its ClientHello. base is a copy of the listener's configuration,
	// carrying its certificate; the hook returns it adjusted, or nil to keep
	// it as it is. A cloud whose front end asks the clients of some hostnames
	// for a certificate decides that here, by the hello's ServerName.
	TLSConfigForClient func(hello *tls.ClientHelloInfo, base *tls.Config) (*tls.Config, error)

	// LogLevel is the zerolog log level (trace, debug, info, warn, error).
	LogLevel string

	// Provider identifies the cloud provider (aws, gcp, azure).
	Provider string

	// DataDir is the directory for persistent storage (SQLite database, PID files).
	// Set via SIM_DATA_DIR. Empty uses a temp directory.
	DataDir string

	// RewriteRequest, when set, runs on every request before routing. It
	// exists for virtual-hosted addressing, where the cloud carries part of the
	// resource identity in the hostname and the router works on paths: the hook
	// maps the one onto the other, and records what the client actually sent so
	// the signature is still verified against that.
	RewriteRequest func(*http.Request)

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
	ApplicationMonitoringToken string
	// ConsoleDir is a built console bundle to serve, for a simulator built
	// without the console embedded. Set via SIM_CONSOLE_DIR.
	ConsoleDir string
	// Placement is the per-service backend table, as SIM_PLACEMENT spells it:
	// `service[:qualifier]=container|microvm`, comma-separated. Empty runs
	// every service on containers.
	Placement string
}

// ConfigFromEnv loads configuration from environment variables.
//
//	SIM_<PROVIDER>_PORT — port to listen on, ahead of SIM_LISTEN_ADDR
//	SIM_LISTEN_ADDR — listen address (default ":" + defaultPort)
//	SIM_TLS_CERT    — TLS certificate file path
//	SIM_TLS_KEY     — TLS private key file path
//	SIM_CALLBACK_LISTEN_ADDR — plain-HTTP address for workload callbacks beside a TLS listener (see Config.CallbackListenAddr)
//	SIM_LOG_LEVEL   — log level (default "info")
//	SIM_UI_OIDC_ISSUER        — Shauth or another OpenID Connect issuer
//	SIM_UI_OIDC_CLIENT_ID     — simulator dashboard relying-party client ID
//	SIM_UI_OIDC_CLIENT_SECRET — simulator dashboard relying-party secret
//	SIM_UI_PUBLIC_URL         — externally visible simulator origin
//	SIM_UI_SESSION_SECRET     — random secret used to sign local sessions
//	APPLICATION_RELEASE_REVISION — immutable deployed release exposed to Shauth validation
//	SIM_MONITORING_TOKEN      — deployment bearer for the application observation
//	SIM_PLACEMENT   — per-service placement backends (see Config.Placement)
//	SIM_CONSOLE_DIR — a built console bundle to serve (see Config.ConsoleDir)
func ConfigFromEnv(provider, defaultPort string) Config {
	listenAddr := EnvOr("SIM_LISTEN_ADDR", ":"+defaultPort)
	if port := os.Getenv("SIM_" + strings.ToUpper(provider) + "_PORT"); port != "" {
		listenAddr = ":" + port
	}
	return Config{
		ListenAddr:                 listenAddr,
		TLSCert:                    os.Getenv("SIM_TLS_CERT"),
		TLSKey:                     os.Getenv("SIM_TLS_KEY"),
		CallbackListenAddr:         os.Getenv("SIM_CALLBACK_LISTEN_ADDR"),
		LogLevel:                   EnvOr("SIM_LOG_LEVEL", "info"),
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
		ApplicationMonitoringToken: os.Getenv("SIM_MONITORING_TOKEN"),
		Placement:                  os.Getenv("SIM_PLACEMENT"),
		ConsoleDir:                 os.Getenv("SIM_CONSOLE_DIR"),
	}
}

// EnvOr reads an environment variable, or fallback when it is unset or empty.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
