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
	"path/filepath"
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
	routed  http.Handler
	handler http.Handler
	db      *sql.DB         // nil when persistence disabled
	tracker *ProcessTracker // nil when persistence disabled
	uiAuth  *uiauth.Auth

	backgroundCtx    context.Context
	backgroundCancel context.CancelFunc
	backgroundWG     sync.WaitGroup

	// routePatterns records every mux pattern registered through
	// Handle/HandleFunc, in registration order. The spec-conformance
	// tests validate this table against the vendored cloud API specs
	// (specs/cloud-api/), so all service registration must go through
	// those two methods rather than the raw mux.
	routePatterns []string
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
		CookieName: "sockerless_" + cfg.Provider + "_session", ApplicationName: "Sockerless " + strings.ToUpper(cfg.Provider) + " Simulator",
		ReleaseRevision: cfg.ApplicationReleaseRevision,
		InsecureCookies: cfg.UIOIDCInsecureCookies,
	})
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
	routed = AuthPassthroughMiddleware(cfg.Provider)(routed)

	// Initialize Docker/Podman for workload execution. SIM_RUNTIME=process
	// is an explicit API-only startup mode for non-execution service slices.
	runtime := os.Getenv("SIM_RUNTIME")
	if runtime == "" {
		runtime = "docker"
	}
	dataDir := cfg.DataDir
	if cfg.Persist && dataDir == "" {
		dataDir = fmt.Sprintf("/tmp/sockerless-sim-%s", cfg.Provider)
	}
	if cfg.Persist {
		absoluteDataDir, err := filepath.Abs(dataDir)
		if err != nil {
			return nil, fmt.Errorf("resolve persistence directory %s: %w", dataDir, err)
		}
		dataDir = absoluteDataDir
		cfg.DataDir = absoluteDataDir
	}
	if runtime != "process" {
		InitDocker(cfg.Provider, cfg.Persist, dataDir)
		logger.Info().Str("runtime", RuntimeInfo()).Msg("container runtime initialized")
	}

	// Open SQLite database if persistence enabled. No fallback —
	// operator asked for durable state; if we can't deliver, surface
	// the error and let the caller decide (typically log.Fatal in
	// main).
	var db *sql.DB
	var tracker *ProcessTracker
	if cfg.Persist {
		db, err = OpenDB(dataDir)
		if err != nil {
			return nil, fmt.Errorf("open persistence at %s: %w", dataDir, err)
		}
		tracker = NewProcessTracker(dataDir)
		logger.Info().Str("path", dataDir+"/simulator.db").Msg("SQLite persistence enabled")
	}

	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	srv := &Server{
		config:           cfg,
		logger:           logger,
		mux:              mux,
		routed:           routed,
		db:               db,
		tracker:          tracker,
		uiAuth:           uiAuth,
		backgroundCtx:    backgroundCtx,
		backgroundCancel: backgroundCancel,
	}

	return srv, nil
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

// WrapHandler wraps the server's final handler chain. Host-addressed cloud
// data planes use this to route before generic path handlers.
func (s *Server) WrapHandler(middleware func(http.Handler) http.Handler) {
	s.routed = middleware(s.routed)
	s.handler = nil
}

// finalHandler returns the outermost chain, building it on first use after the
// last WrapHandler call. otelhttp is outermost so per-request spans see the
// post-routing path; logging and request-id run inside the span and outside
// every wrapper, so a request a host-addressed data plane claims is observed
// like any other.
func (s *Server) finalHandler() http.Handler {
	if s.handler == nil {
		h := s.routed
		h = LoggingMiddleware(s.logger, s.config.Provider)(h)
		h = RequestIDMiddleware(s.config.Provider)(h)
		s.handler = otelhttp.NewHandler(h, "sockerless-sim-"+s.config.Provider)
	}
	return s.handler
}

// ServeHTTP serves through the same final handler chain as ListenAndServe.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.finalHandler().ServeHTTP(w, r)
}

// Mux returns the underlying ServeMux for direct registration.
func (s *Server) Mux() *http.ServeMux {
	return s.mux
}

// DB returns the SQLite database connection, or nil if persistence is disabled.
func (s *Server) DB() *sql.DB {
	return s.db
}

// Tracker returns the process tracker, or nil if persistence is disabled.
func (s *Server) Tracker() *ProcessTracker {
	return s.tracker
}

// Logger returns the server's logger for use by service handlers.
func (s *Server) Logger() zerolog.Logger {
	return s.logger
}

// StartBackground runs service work under the server lifecycle. The worker
// must return when ctx is cancelled. ListenAndServe cancels and drains every
// registered worker before checkpointing and closing SQLite, so no service can
// query durable state after orderly shutdown has closed the database.
func (s *Server) StartBackground(worker func(context.Context)) {
	s.backgroundWG.Add(1)
	go func() {
		defer s.backgroundWG.Done()
		worker(s.backgroundCtx)
	}()
}

func (s *Server) stopBackground() {
	s.backgroundCancel()
	s.backgroundWG.Wait()
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
		// surfacing as an "i/o timeout" 500 on a slow OCI/S3 transfer on a
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
		s.backgroundCancel()
		if !s.config.Persist {
			CleanupContainers()
		}

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
	s.stopBackground()
	closeErr := CloseDB(s.db)
	s.db = nil
	return errors.Join(err, closeErr)
}

// RegisterUI registers an embedded SPA at /ui/ and redirects GET / to /ui/.
// When a simulated service already owns the API root — S3's ListBuckets is
// "GET /{$}" — the API surface wins: registering the redirect anyway would
// panic the mux at startup, and the UI stays reachable at /ui/ directly.
func (s *Server) RegisterUI(fsys fs.FS) {
	identityEndpoint, logoutEndpoint := "", ""
	if s.uiAuth.Enabled() {
		identityEndpoint, logoutEndpoint = uiauth.SessionPath, uiauth.LogoutPath
		s.uiAuth.Register(s.mux)
	}
	federationSubject := ""
	if s.uiAuth.Enabled() {
		federationSubject = uiauth.FederationSubjectPath
	}
	configHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusOK, map[string]string{
			"identityEndpoint": identityEndpoint,
			"logoutEndpoint":   logoutEndpoint,
			// Cloud data-plane coordinates. Empty means the console's own origin,
			// where the simulator serves the real AWS APIs and its Security Token
			// Service; a deployment against real AWS points these at the AWS
			// hosts and the role to assume. Provisioned the administrator's way
			// and supplied as coordinates — never invented by the simulator.
			"cloudApiEndpoint":   os.Getenv("SOCKERLESS_CONSOLE_CLOUD_API_ENDPOINT"),
			"federationEndpoint": os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_ENDPOINT"),
			"federationAudience": os.Getenv("SOCKERLESS_CONSOLE_FEDERATION_AUDIENCE"),
			"federationSubject":  federationSubject,
		})
	})
	s.mux.Handle("GET /ui/config.json", s.uiAuth.Protect(configHandler))
	s.mux.Handle("GET /ui/", s.uiAuth.Protect(spaHandler(fsys, "/ui/")))
	if !slices.Contains(s.routePatterns, "GET /{$}") {
		s.mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
		})
	}
	s.logger.Info().Msg("UI registered at /ui/")
}

// spaHandler serves a single-page application from the given filesystem.
// Existing files are served directly; all other paths fall back to index.html.
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
	fmt.Fprintf(os.Stderr, "  Sockerless %s Simulator\n", s.config.Provider)
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
