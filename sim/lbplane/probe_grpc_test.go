package lbplane

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestProbeGRPCReadsTheHealthService(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	status := health.NewServer()
	healthpb.RegisterHealthServer(server, status)
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(server.Stop)

	status.SetServingStatus("orders", healthpb.HealthCheckResponse_SERVING)
	status.SetServingStatus("billing", healthpb.HealthCheckResponse_NOT_SERVING)
	address := ln.Addr().String()

	require.NoError(t, ProbeGRPC(context.Background(), address, "", time.Second), "the server as a whole serves")
	require.NoError(t, ProbeGRPC(context.Background(), address, "orders", time.Second))
	require.ErrorContains(t, ProbeGRPC(context.Background(), address, "billing", time.Second), "NOT_SERVING")
	require.Error(t, ProbeGRPC(context.Background(), address, "unknown", time.Second), "an unknown service is not serving")
}

func TestProbeHTTPSendsTheProbesHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Probe") != "startup" || r.Host != "app.internal" || r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	status, err := ProbeHTTP(context.Background(), HTTPProbe{
		Scheme: "http", Address: strings.TrimPrefix(srv.URL, "http://"), Host: "app.internal",
		Path: "healthz", Header: http.Header{"X-Probe": {"startup"}},
		Timeout: 5 * time.Second, ConnectTimeout: time.Second, Match: StatusRange(200, 399),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
}
