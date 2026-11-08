package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Every version the table runs is one the vendored enum lists, on the image
// of the release its installed version names.
func TestSQLEngineVersionsRunTheirOwnPinnedRelease(t *testing.T) {
	file, err := os.Open("../specs/cloud-api/gcp/sqladmin-v1beta4.discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var discovery struct {
		Schemas map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.NewDecoder(reader).Decode(&discovery); err != nil {
		t.Fatal(err)
	}
	enum := discovery.Schemas["DatabaseInstance"].Properties["databaseVersion"].Enum
	for _, version := range sqlEngineVersions {
		if !slices.Contains(enum, version.DatabaseVersion) {
			t.Errorf("%s is not a databaseVersion the API defines", version.DatabaseVersion)
		}
		if !strings.HasPrefix(version.InstalledVersion, version.DatabaseVersion) {
			t.Errorf("%s reports installed version %s", version.DatabaseVersion, version.InstalledVersion)
		}
		var parts []string
		for _, n := range version.release() {
			parts = append(parts, strconv.Itoa(n))
		}
		tag := ":" + strings.Join(parts, ".")
		if !strings.HasSuffix(version.Image, tag) && !strings.HasSuffix(version.Image, tag+"-alpine") {
			t.Errorf("%s runs %s, not the image of release %s", version.DatabaseVersion, version.Image, tag[1:])
		}
	}
}

// Cloud SQL upgrades to a later version of the same engine, MySQL one major
// version at a time.
func TestSQLVersionChangesMoveForwardOnly(t *testing.T) {
	for _, c := range []struct {
		from, to string
		allowed  bool
	}{
		{"POSTGRES_15", "POSTGRES_16", true},
		{"POSTGRES_16", "POSTGRES_15", false},
		{"POSTGRES_15", "MYSQL_8_0", false},
		{"MYSQL_5_7", "MYSQL_8_0", true},
		{"MYSQL_8_0", "MYSQL_8_4", true},
		{"MYSQL_5_7", "MYSQL_8_4", false},
		{"MYSQL_8_4", "MYSQL_8_0", false},
	} {
		if got := sqlCheckVersionChange(c.from, c.to) == ""; got != c.allowed {
			t.Errorf("%s to %s: allowed %t, want %t", c.from, c.to, got, c.allowed)
		}
	}
}

// MySQL 8.0 starts with mysql_native_password as its default plugin, 8.4 loads
// the plugin for accounts an 8.0 engine created, and 5.7 takes neither option.
func TestSQLMySQLEngineArgumentsFollowTheRelease(t *testing.T) {
	for version, want := range map[string][]string{
		"MYSQL_5_7": nil,
		"MYSQL_8_0": {"--default-authentication-plugin=mysql_native_password"},
		"MYSQL_8_4": {"--mysql-native-password=ON"},
	} {
		row, ok := sqlFindEngineVersion(version)
		if !ok {
			t.Fatalf("%s has no engine", version)
		}
		if got := sqlEngine(row).Args; !slices.Equal(got, want) {
			t.Errorf("%s starts with %q, want %q", version, got, want)
		}
	}
}
