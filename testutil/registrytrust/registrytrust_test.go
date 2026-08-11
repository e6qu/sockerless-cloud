package registrytrust

import (
	"context"
	"strings"
	"testing"
)

func TestConfigureLoopbackHTTPRegistryRejectsNonLoopbackCoordinate(t *testing.T) {
	cleanup, err := ConfigureLoopbackHTTPRegistry(context.Background(), "registry.example.com:5000")
	if err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Fatalf("non-loopback registry result = cleanup %v, error %v", cleanup != nil, err)
	}
}
