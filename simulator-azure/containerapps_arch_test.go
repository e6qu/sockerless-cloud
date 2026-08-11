package main

import (
	"os"
	"strings"
	"testing"
)

func TestContainerAppsDoNotHardcodeDockerPlatform(t *testing.T) {
	for _, path := range []string{"containerapps.go", "containerapps_apps.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(src), `Architecture: "linux/arm64"`) {
			t.Fatalf("%s hardcodes Docker platform; derive it from the local image manifest", path)
		}
	}
}
