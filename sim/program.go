package sim

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// Program is one simulator binary's startup: what Main runs between reading
// the configuration and serving.
type Program struct {
	// Provider names the cloud ("aws", "gcp", "azure"); it selects the
	// SIM_<PROVIDER>_PORT variable and the observability service name.
	Provider string
	// DefaultPort is the port served when neither SIM_<PROVIDER>_PORT nor
	// SIM_LISTEN_ADDR names one.
	DefaultPort string
	// Configure, when set, checks or records the resolved configuration before
	// anything is built.
	Configure func(*Config) error
	// Build constructs the server with every service slice registered.
	Build func(Config) (*Server, error)
	// Start, when set, starts what serves beside the HTTP port. The stop it
	// returns runs once the HTTP server has shut down.
	Start func(*Server, Config) (stop func(context.Context) error, err error)
}

// Main runs a simulator binary: the container reaper when invoked as one, and
// otherwise configuration, observability, the build, the listeners beside the
// HTTP port and the HTTP server, until a signal shuts it down. A step that
// fails ends the process before it serves.
func Main(p Program) {
	if RunContainerReaper() {
		return
	}
	// A simulator started by a test harness must not outlive it. The harness
	// stops it from its own cleanup, which a killed `go test` never reaches,
	// and the reaper waits on the simulator, so both would linger.
	ExitWithParent()
	if err := run(p); err != nil {
		log.Fatal(err)
	}
}

func run(p Program) error {
	cfg := ConfigFromEnv(p.Provider, p.DefaultPort)
	if p.Configure != nil {
		if err := p.Configure(&cfg); err != nil {
			return fmt.Errorf("configure: %w", err)
		}
	}
	obs, err := InitObservability("sockerless-sim-" + p.Provider)
	if err != nil {
		return fmt.Errorf("init observability: %w", err)
	}
	defer func() { _ = obs.Shutdown(context.Background()) }()
	cfg.LogWriter = obs.LogWriter

	srv, err := p.Build(cfg)
	if err != nil {
		return fmt.Errorf("simulator startup: %w", err)
	}
	stop := func(context.Context) error { return nil }
	if p.Start != nil {
		if stop, err = p.Start(srv, cfg); err != nil {
			srv.StopBackground()
			return fmt.Errorf("simulator startup: %w", err)
		}
	}
	serveErr := srv.ListenAndServe()
	return errors.Join(serveErr, stop(context.Background()))
}
