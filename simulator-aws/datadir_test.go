package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSliceDataRootsFollowSimDataDir(t *testing.T) {
	cases := []struct {
		name        string
		overrideEnv string
		subdir      string
		fallback    string
		root        func() string
	}{
		{"EFS", "SIM_EFS_DATA_DIR", "efs", "sockerless-sim-efs", efsHostRoot},
		{"EBS", "SIM_EBS_DATA_DIR", "ebs", "sockerless-sim-ebs", ebsHostRoot},
		{"AmplifyBuildCache", "", "amplify-cache", "sockerless-amplify-cache", amplifyBuildCacheRoot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.overrideEnv != "" {
				t.Setenv(tc.overrideEnv, "")
			}

			t.Setenv("SIM_DATA_DIR", "")
			if got, want := tc.root(), filepath.Join(os.TempDir(), tc.fallback); got != want {
				t.Errorf("without SIM_DATA_DIR: root = %q, want %q", got, want)
			}

			t.Setenv("SIM_DATA_DIR", filepath.Join("/srv", "sim-state"))
			if got, want := tc.root(), filepath.Join("/srv", "sim-state", tc.subdir); got != want {
				t.Errorf("with SIM_DATA_DIR: root = %q, want %q", got, want)
			}

			if tc.overrideEnv != "" {
				override := filepath.Join("/mnt", "slice-override")
				t.Setenv(tc.overrideEnv, override)
				if got := tc.root(); got != override {
					t.Errorf("with %s: root = %q, want %q", tc.overrideEnv, got, override)
				}
			}
		})
	}
}
