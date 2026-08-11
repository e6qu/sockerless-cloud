package simulator

import "testing"

// parsePlatform — workload arch is carried in the spec, never derived
// from the host. Empty or malformed → error (no silent fallback).
func TestParsePlatform(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"linux/arm64", "linux/arm64", false},
		{"linux/amd64", "linux/amd64", false},
		{"linux/arm/v7", "linux/arm/v7", false},
		{"", "", true},
		{"garbage", "", true},
	}
	for _, tc := range cases {
		got, err := parsePlatform(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parsePlatform(%q) = no err, want err", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePlatform(%q) err = %v, want nil", tc.in, err)
			continue
		}
		flat := got.OS + "/" + got.Architecture
		if got.Variant != "" {
			flat += "/" + got.Variant
		}
		if flat != tc.want {
			t.Errorf("parsePlatform(%q) = %s, want %s", tc.in, flat, tc.want)
		}
	}
}
