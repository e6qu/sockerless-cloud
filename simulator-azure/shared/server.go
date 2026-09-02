package simulator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	uiauth "github.com/e6qu/sockerless-cloud/ui-auth"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// ServiceHandler handles requests for a single simulated cloud service.
type ServiceHandler interface {
	// ServiceName returns the display name of the service (e.g., "ECS", "CloudRun").
	ServiceName() string
}

// Server is the main simulator HTTP server.
type Server struct {
	config Config
	logger zerolog.Logger
	mux    *http.ServeMux
	// routed is the routing core: the mux plus the middlewares that must run
	// after a request has been claimed. WrapHandler wraps this, so a
	// host-addressed data plane intercepts ahead of the generic path handlers
	// while still being observed by the outer chain built in finalHandler.
	routed http.Handler
	// handler is the outermost chain, built on first use. It is read on every
	// request and written by the first request to arrive, so it is guarded:
	// without the lock two concurrent first requests both saw nil, both built
	// a chain, and both wrote it — twelve of the thirteen races the detector
	// found in this repository, and a live defect rather than a test artefact,
	// since a real deployment's first two requests race exactly the same way.
	handlerMu sync.RWMutex
	handler   http.Handler
	db        *sql.DB         // nil when persistence disabled
	tracker   *ProcessTracker // nil when persistence disabled
	uiAuth    *uiauth.Auth
	// federation carries the console's Microsoft Entra Workload Identity
	// Federation coordinates for the server-side broker (see
	// federation_broker.go). Zero value = federation not configured.
	federation consoleFederation

	// routePatterns records every mux pattern registered through
	// Handle/HandleFunc, in registration order. The spec-conformance
	// tests validate this table against the vendored cloud API specs
	// (specs/cloud-api/), so all service registration must go through
	// those two methods rather than the raw mux.
	routePatterns []string

	// uiRoot redirects a visitor at the bare origin to the console. It is set
	// whenever a console is registered, including when a service owns the API
	// root and the redirect therefore cannot be mounted on the mux; that
	// service hands the request here via ServeUIRoot when the request is not
	// addressed to it. nil when no console is registered.
	uiRoot http.HandlerFunc
}

// NewServer creates a new simulator server with the given configuration.
//
// Returns an error when persistence is requested (cfg.Persist=true)
// but the data directory cannot be opened. Operator asked for durable
// state; degrading silently to in-memory would mask misconfiguration
// (bad path, perms, full disk) and produce silent data loss across
// restarts.
func NewServer(cfg Config) (*Server, error) {
	uiAuth, err := uiauth.New(uiauth.Config{
		Issuer: cfg.UIOIDCIssuer, ClientID: cfg.UIOIDCClientID, ClientSecret: cfg.UIOIDCClientSecret,
		PublicURL: cfg.UIPublicURL, SessionSecret: cfg.UISessionSecret,
		CookieName: "sockerless_" + cfg.Provider + "_session", ApplicationName: "Sockerless Cloud " + strings.ToUpper(cfg.Provider) + " Simulator",
		ApplicationSlug: "sockerless-" + cfg.Provider, MonitoringToken: cfg.ApplicationMonitoringToken,
		ReleaseRevision: cfg.ApplicationReleaseRevision,
		InsecureCookies: cfg.UIOIDCInsecureCookies,
	})
	if err != nil {
		return nil, err
	}
	// The console federation broker's coordinates fail loud at startup exactly
	// like the OIDC config above: an operator who sets any
	// SOCKERLESS_CONSOLE_FEDERATION_* coordinate but not all of them has a
	// deployment error, never a silently degraded broker.
	federation, err := loadConsoleFederation()
	if err != nil {
		return nil, err
	}
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}

	var output zerolog.LevelWriter
	consoleW := zerolog.ConsoleWriter{Out: os.Stderr}
	if cfg.LogWriter != nil {
		output = zerolog.MultiLevelWriter(consoleW, cfg.LogWriter)
	} else {
		output = zerolog.MultiLevelWriter(consoleW)
	}
	logger := zerolog.New(output).
		Level(level).
		With().
		Timestamp().
		Str("provider", cfg.Provider).
		Logger()

	mux := http.NewServeMux()
	uiAuth.RegisterMonitoring(mux)

	// Health check endpoint
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		runtime := os.Getenv("SIM_RUNTIME")
		if runtime == "" {
			runtime = "docker"
		}
		WriteJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"provider": cfg.Provider,
			"runtime":  runtime,
			"capabilities": map[string]bool{
				"workloadExecution": runtime != "process",
			},
		})
	})

	// Build middleware chain. otelhttp.NewHandler is outermost so
	// per-request spans see the post-routing path; the existing
	// middlewares run inside the span. Spans emit to a no-op tracer
	// unless main.go calls InitObservability with OTEL_EXPORTER_OTLP_ENDPOINT
	// set in the env.
	var routed http.Handler = mux
	if cfg.Provider == "azure" {
		routed = AzurePathNormalizationMiddleware(routed)
	}
	routed = AuthPassthroughMiddleware(cfg.Provider)(routed)

	// Initialize Docker/Podman for workload execution. SIM_RUNTIME=process
	// is an explicit API-only startup mode for non-execution service slices.
	runtime := os.Getenv("SIM_RUNTIME")
	if runtime == "" {
		runtime = "docker"
	}
	// The state directory is resolved before the engine client is built,
	// because it identifies this simulator to the workload sweep: a run
	// collects what an earlier run over the same state left behind.
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = fmt.Sprintf("/tmp/sockerless-sim-%s", cfg.Provider)
	}
	if runtime != "process" {
		InitDocker(cfg.Provider, dataDir)
		logger.Info().Str("runtime", RuntimeInfo()).Msg("container runtime initialized")
	}

	srv := &Server{
		config:     cfg,
		logger:     logger,
		mux:        mux,
		routed:     routed,
		uiAuth:     uiAuth,
		federation: federation,
	}

	// Persistence opens fail loud — operator asked for durable state.
	if cfg.Persist {
		db, err := OpenDB(dataDir)
		if err != nil {
			return nil, fmt.Errorf("open persistence at %s: %w", dataDir, err)
		}
		srv.db = db
		srv.tracker = NewProcessTracker(dataDir)
		logger.Info().Str("path", dataDir+"/simulator.db").Msg("SQLite persistence enabled")
	}

	return srv, nil
}

// DB returns the SQLite database connection, or nil if persistence is disabled.
func (s *Server) DB() *sql.DB {
	return s.db
}

// Tracker returns the process tracker, or nil if persistence is disabled.
func (s *Server) Tracker() *ProcessTracker {
	return s.tracker
}

// WrapHandler wraps the server's routing core with an additional middleware.
// Host-addressed cloud data planes use this to claim a request ahead of the
// generic path handlers. The wrapper runs inside the observability chain, so
// requests a data plane serves are logged and traced like any other.
func (s *Server) WrapHandler(wrapper func(http.Handler) http.Handler) {
	s.routed = wrapper(s.routed)
	s.handler = nil
}

// ServeHTTP serves through the same final handler chain as ListenAndServe.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.finalHandler().ServeHTTP(w, r)
}

// finalHandler returns the outermost chain, building it on first use after the
// last WrapHandler call. otelhttp is outermost so per-request spans see the
// post-routing path; logging and request-id run inside the span and outside
// every wrapper.
func (s *Server) finalHandler() http.Handler {
	// Every request takes this path, so the built chain is read under the read
	// lock and the build happens once behind the write lock.
	s.handlerMu.RLock()
	built := s.handler
	s.handlerMu.RUnlock()
	if built != nil {
		return built
	}

	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	if s.handler == nil {
		h := s.routed
		h = LoggingMiddleware(s.logger, s.config.Provider)(h)
		h = RequestIDMiddleware(s.config.Provider)(h)
		s.handler = otelhttp.NewHandler(h, "sockerless-sim-"+s.config.Provider)
	}
	return s.handler
}

// Handle registers a pattern on the server's mux.
func (s *Server) Handle(pattern string, handler http.Handler) {
	s.routePatterns = append(s.routePatterns, pattern)
	s.mux.Handle(pattern, handler)
}

// HandleFunc registers a handler function on the server's mux.
func (s *Server) HandleFunc(pattern string, handler http.HandlerFunc) {
	s.routePatterns = append(s.routePatterns, pattern)
	s.mux.HandleFunc(pattern, handler)
}

// HandleUIFunc registers a simulator operator endpoint that shares the user
// interface's OpenID Connect session boundary. Native cloud API routes must
// continue to use Handle or HandleFunc so their public protocol is unchanged.
func (s *Server) HandleUIFunc(pattern string, handler http.HandlerFunc) {
	s.Handle(pattern, s.uiAuth.Protect(handler))
}

// RoutePatterns returns every pattern registered through Handle /
// HandleFunc, in registration order.
func (s *Server) RoutePatterns() []string {
	return s.routePatterns
}

// Mux returns the underlying ServeMux for direct registration.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// Logger returns the server's logger for use by service handlers.
func (s *Server) Logger() zerolog.Logger {
	return s.logger
}

// ListenAndServe starts the server and blocks until shutdown.
// It listens for SIGTERM and SIGINT for graceful shutdown.
func (s *Server) ListenAndServe() error {
	srv := &http.Server{
		Addr:    s.config.ListenAddr,
		Handler: s.finalHandler(),
		// Bound only the header read (slowloris protection). A fixed
		// ReadTimeout/WriteTimeout caps the WHOLE body, which cuts off large
		// image-layer / build-context uploads + downloads under load —
		// surfacing as an "i/o timeout" 500 on a slow OCI/blob transfer on a
		// loaded CI runner.
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown
	done := make(chan error, 1)
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		s.logger.Info().Str("signal", sig.String()).Msg("shutting down")
		CleanupContainers()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- srv.Shutdown(ctx)
	}()

	// Print startup banner
	s.printBanner()

	// Start server
	var err error
	if s.config.TLSCert != "" && s.config.TLSKey != "" {
		s.logger.Info().
			Str("addr", s.config.ListenAddr).
			Msg("starting HTTPS server")
		err = srv.ListenAndServeTLS(s.config.TLSCert, s.config.TLSKey)
	} else {
		s.logger.Info().
			Str("addr", s.config.ListenAddr).
			Msg("starting HTTP server")
		err = srv.ListenAndServe()
	}

	if err == http.ErrServerClosed {
		err = <-done
	}
	closeErr := CloseDB(s.db)
	s.db = nil
	return errors.Join(err, closeErr)
}

// ServeUIRoot redirects a request at the bare origin to the console, and
// reports whether it did. A service that owns the API root calls this when the
// request is not addressed to its own API — an operator who types the bare
// origin into a browser then reaches the console instead of a bare 404, while
// the service keeps the root for its real clients. It reports false, writing
// nothing, when no console is registered.
func (s *Server) ServeUIRoot(w http.ResponseWriter, r *http.Request) bool {
	if s.uiRoot == nil {
		return false
	}
	s.uiRoot(w, r)
	return true
}

// RegisterUI registers an embedded SPA at /ui/ and redirects GET / to /ui/.
// When a simulated service already owns the API root ("GET /{$}"), the API
// surface wins — registering the redirect anyway would panic the mux at
// startup — and that service delegates the requests it declines to
// ServeUIRoot, so the bare origin still reaches the console.
func (s *Server) RegisterUI(fsys fs.FS) {
	identityEndpoint, logoutEndpoint := "", ""
	federationSubject := ""
	federationTokenEndpoint := ""
	if s.uiAuth.Enabled() {
		identityEndpoint, logoutEndpoint = uiauth.SessionPath, uiauth.LogoutPath
		federationSubject = uiauth.FederationSubjectPath
		s.uiAuth.Register(s.mux)
		// The federation broker (federation_broker.go) is a console auth-layer
		// endpoint like the routes above, registered unprotected — it reads the
		// operator's session itself and answers 401 JSON when there is none,
		// the correct contract for an XHR endpoint the SPA polls rather than a
		// browser-navigated redirect target. It is exposed to the SPA only when
		// a deployment actually configured cloud federation; with no
		// SOCKERLESS_CONSOLE_FEDERATION_* coordinates set, the portal has
		// nothing to federate into and stays in its unauthenticated-reads mode.
		if s.federation.configured() {
			federationTokenEndpoint = FederationTokenPath
		}
		s.mux.HandleFunc("GET "+FederationTokenPath, s.handleFederationToken)
	}
	configHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Cloud data-plane coordinates. Empty means the console's own origin
		// (the simulator); a deployment against real Azure sets them to Azure
		// Resource Manager, Microsoft Entra, and the operator's identity. The
		// console federates and reads real APIs identically either way. The
		// Microsoft Entra Workload Identity Federation exchange itself runs
		// server-side (see federation_broker.go): the browser never learns the
		// federation endpoint, tenant, or client ID, only whether the broker is
		// reachable (federationTokenEndpoint) and, for display purposes only
		// (CLI usage samples), the directory tenant.
		WriteJSON(w, http.StatusOK, map[string]string{
			"identityEndpoint":        identityEndpoint,
			"logoutEndpoint":          logoutEndpoint,
			"cloudApiEndpoint":        os.Getenv("SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT"),
			"logsApiEndpoint":         os.Getenv("SOCKERLESS_CONSOLE_LOGS_API_ENDPOINT"),
			"graphApiEndpoint":        os.Getenv("SOCKERLESS_CONSOLE_GRAPH_API_ENDPOINT"),
			"federationTokenEndpoint": federationTokenEndpoint,
			"federationTenant":        os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_TENANT"),
			"federationSubject":       federationSubject,
		})
	})
	s.mux.Handle("GET /ui/config.json", s.uiAuth.Protect(configHandler))
	s.mux.Handle("GET /ui/", s.uiAuth.Protect(spaHandler(fsys, "/ui/")))
	s.uiRoot = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
	}
	if !slices.Contains(s.routePatterns, "GET /{$}") {
		s.mux.HandleFunc("GET /{$}", s.uiRoot)
	}
	s.logger.Info().Msg("UI registered at /ui/")
}

// spaHandler serves a single-page application from the given filesystem.
func spaHandler(fsys fs.FS, pathPrefix string) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	stripped := http.StripPrefix(pathPrefix, fileServer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, pathPrefix)
		if reqPath == "" {
			reqPath = "."
		}
		reqPath = path.Clean(reqPath)

		f, err := fsys.Open(reqPath)
		if err == nil {
			stat, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !stat.IsDir() {
				stripped.ServeHTTP(w, r)
				return
			}
		}

		indexFile, err := fsys.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer func() { _ = indexFile.Close() }()

		stat, err := indexFile.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}

		readSeeker, ok := indexFile.(interface {
			Read([]byte) (int, error)
			Seek(int64, int) (int64, error)
		})
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, "index.html", stat.ModTime(), readSeeker)
	})
}

func (s *Server) printBanner() {
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Sockerless Cloud %s Simulator\n", s.config.Provider)
	fmt.Fprintf(os.Stderr, "  Listening on %s\n", s.config.ListenAddr)
	switch s.config.Provider {
	case "aws":
		fmt.Fprintf(os.Stderr, "  SDK config: AWS_ENDPOINT_URL=http://localhost%s\n", s.config.ListenAddr)
	case "gcp":
		fmt.Fprintf(os.Stderr, "  SDK config: option.WithEndpoint(\"http://localhost%s\")\n", s.config.ListenAddr)
	case "azure":
		fmt.Fprintf(os.Stderr, "  SDK config: custom cloud.Configuration with endpoint http://localhost%s\n", s.config.ListenAddr)
	}
	fmt.Fprintf(os.Stderr, "\n")
}
