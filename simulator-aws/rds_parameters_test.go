package main

import (
	"testing"
	"time"
)

// TestRDSParameterCatalogImages holds the vendored catalogs to the engines the
// simulator runs: each family's catalog comes from the image of the family's
// newest offered version, and every family the simulator runs has one.
func TestRDSParameterCatalogImages(t *testing.T) {
	newest := map[string]rdsEngineVersion{}
	for _, row := range rdsEngineVersions {
		if !rdsVersionedEngine(row.Engine) {
			continue
		}
		if current, seen := newest[row.Family]; !seen || rdsCompareVersions(row.EngineVersion, current.EngineVersion) > 0 {
			newest[row.Family] = row
		}
	}
	for family, row := range newest {
		catalog, ok := rdsParameterCatalogs[family]
		if !ok {
			t.Errorf("no parameter catalog for %s; run scripts/capture-rds-parameter-catalogs.go", family)
			continue
		}
		if catalog.Image != row.Image {
			t.Errorf("%s catalog was captured from %s, but the family's newest version runs %s", family, catalog.Image, row.Image)
		}
	}
	for family := range rdsParameterCatalogs {
		if _, ok := newest[family]; !ok {
			t.Errorf("catalog %s names no family the simulator runs", family)
		}
	}
	for _, family := range []string{"aurora-postgresql16", "aurora-mysql8.0"} {
		if _, ok := rdsFamilyParameterCatalog(family); !ok {
			t.Errorf("no parameter catalog for %s", family)
		}
	}
}

func TestRDSParameterValueAllowed(t *testing.T) {
	catalog := rdsParameterCatalogs["postgres16"]
	for _, testCase := range []struct {
		name, value string
		allowed     bool
	}{
		{"work_mem", "8192", true},
		{"work_mem", "8MB", false},
		{"work_mem", "1", false},
		{"log_min_duration_statement", "-1", true},
		{"autovacuum", "0", true},
		{"autovacuum", "maybe", false},
		{"log_statement", "ddl", true},
		{"log_statement", "everything", false},
		{"random_page_cost", "1.5", true},
		{"application_name", "anything", true},
	} {
		p, ok := catalog.lookup(testCase.name)
		if !ok {
			t.Fatalf("postgres16 has no %s", testCase.name)
		}
		if got := rdsParameterValueAllowed(p, testCase.value); got != testCase.allowed {
			t.Errorf("%s=%s allowed=%t, want %t (AllowedValues %q)", testCase.name, testCase.value, got, testCase.allowed, p.AllowedValues)
		}
	}
	if p, _ := catalog.lookup("archive_mode"); p.IsModifiable {
		t.Error("archive_mode serves the simulator's automated backups and must not be modifiable")
	}
}

func TestRDSMaintenanceWindow(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 20, 0, time.UTC) // a Wednesday
	for _, testCase := range []struct {
		window string
		delay  time.Duration
	}{
		{"wed:12:01-wed:12:31", 40 * time.Second},
		{"wed:11:50-wed:12:40", 0},
		{"wed:11:50-wed:12:20", 7*24*time.Hour - 10*time.Minute - 20*time.Second},
		{"tue:23:00-wed:00:00", 6*24*time.Hour + 11*time.Hour - 20*time.Second},
		{"sun:23:45-mon:00:15", 4*24*time.Hour + 11*time.Hour + 44*time.Minute + 40*time.Second},
	} {
		delay, ok := rdsNextMaintenance(testCase.window, now)
		if !ok || delay != testCase.delay {
			t.Errorf("%s: delay %s (%t), want %s", testCase.window, delay, ok, testCase.delay)
		}
	}
	for window, problem := range map[string]bool{
		"mon:03:00-mon:03:30": false,
		"Mon:03:00-Mon:03:29": true,
		"mon:07:30-mon:08:00": true,
		"mon:3:00-mon:03:30":  true,
		"sun:23:45-mon:00:15": false,
	} {
		if got := rdsCheckMaintenanceWindow(window, "07:00-09:00") != ""; got != problem {
			t.Errorf("%s: refused=%t, want %t", window, got, problem)
		}
	}
	for range 50 {
		window := rdsDefaultMaintenanceWindow("07:00-09:00")
		if problem := rdsCheckMaintenanceWindow(window, "07:00-09:00"); problem != "" {
			t.Fatalf("default window %s: %s", window, problem)
		}
	}
}
