package sim

import (
	"context"
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
	// Start, when set, starts what serves beside the HTTP port, with the
	// configuration's CallbackListenAddr resolved to the address the server
	// bound. The stop it returns runs once the HTTP server has shut down and
	// its workers have returned, before the database closes.
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
	// The listeners bind before Start, so what starts beside the HTTP port
	// knows the address the workloads call back on.
	if err := srv.Listen(); err != nil {
		srv.StopBackground()
		return fmt.Errorf("simulator startup: %w", err)
	}
	cfg.CallbackListenAddr = srv.CallbackListenAddr()
	if p.Start != nil {
		stop, err := p.Start(srv, cfg)
		if err != nil {
			srv.StopBackground()
			srv.closeListeners()
			return fmt.Errorf("simulator startup: %w", err)
		}
		srv.OnStop(stop)
	}
	return srv.Serve()
}
