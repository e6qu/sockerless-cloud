package sim

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestConfigFromEnvListenAddressPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, listen, port, want string
	}{
		{"default port", "", "", ":4566"},
		{"listen address", "127.0.0.1:5000", "", "127.0.0.1:5000"},
		{"provider port wins", "127.0.0.1:5000", "6000", ":6000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SIM_LISTEN_ADDR", tc.listen)
			t.Setenv("SIM_AWS_PORT", tc.port)
			if got := ConfigFromEnv("aws", "4566").ListenAddr; got != tc.want {
				t.Fatalf("listen address = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunStopsAtTheFirstFailingStep(t *testing.T) {
	t.Setenv("SIM_RUNTIME", string(RuntimeModeAPIOnly))
	t.Setenv("SIM_GCP_PORT", "")
	t.Setenv("SIM_LISTEN_ADDR", "127.0.0.1:0")
	refused := errors.New("refused")

	t.Run("configure", func(t *testing.T) {
		built := false
		err := run(Program{
			Provider:    "gcp",
			DefaultPort: "4567",
			Configure:   func(*Config) error { return refused },
			Build:       func(Config) (*Server, error) { built = true; return nil, nil },
		})
		if !errors.Is(err, refused) || built {
			t.Fatalf("err = %v, built = %v; want the configure error before any build", err, built)
		}
	})

	t.Run("configure sees the resolved config", func(t *testing.T) {
		var seen Config
		err := run(Program{
			Provider:    "gcp",
			DefaultPort: "4567",
			Configure:   func(cfg *Config) error { seen = *cfg; return refused },
		})
		if !errors.Is(err, refused) || seen.Provider != "gcp" || seen.ListenAddr != "127.0.0.1:0" {
			t.Fatalf("err = %v, config = %+v", err, seen)
		}
	})

	t.Run("build sees the bound callback address", func(t *testing.T) {
		certPath, keyPath := writeTestServerCertificate(t)
		t.Setenv("SIM_TLS_CERT", certPath)
		t.Setenv("SIM_TLS_KEY", keyPath)
		t.Setenv("SIM_CALLBACK_LISTEN_ADDR", "127.0.0.1:0")
		var seen Config
		err := run(Program{
			Provider:    "gcp",
			DefaultPort: "4567",
			Build:       func(cfg Config) (*Server, error) { seen = cfg; return nil, refused },
		})
		if !errors.Is(err, refused) {
			t.Fatalf("err = %v, want the build error", err)
		}
		host, port, splitErr := net.SplitHostPort(seen.CallbackListenAddr)
		if splitErr != nil || host != "127.0.0.1" || port == "0" {
			t.Fatalf("Build saw CallbackListenAddr %q, want the port the callback listener bound", seen.CallbackListenAddr)
		}
		if conn, dialErr := net.Dial("tcp", seen.CallbackListenAddr); dialErr == nil {
			_ = conn.Close()
			t.Fatal("the callback listener still accepts after a failed build")
		}
	})

	t.Run("build", func(t *testing.T) {
		err := run(Program{
			Provider:    "gcp",
			DefaultPort: "4567",
			Build:       func(Config) (*Server, error) { return nil, refused },
		})
		if !errors.Is(err, refused) || !strings.Contains(err.Error(), "simulator startup") {
			t.Fatalf("err = %v, want the build error", err)
		}
	})

	t.Run("start stops the background workers", func(t *testing.T) {
		var srv *Server
		err := run(Program{
			Provider:    "gcp",
			DefaultPort: "4567",
			Build: func(cfg Config) (*Server, error) {
				var err error
				srv, err = NewServer(cfg)
				return srv, err
			},
			Start: func(*Server, Config) (func(context.Context) error, error) { return nil, refused },
		})
		if !errors.Is(err, refused) {
			t.Fatalf("err = %v, want the start error", err)
		}
		if srv.backgroundCtx.Err() == nil {
			t.Fatal("background workers still run after a failed start")
		}
	})
}
