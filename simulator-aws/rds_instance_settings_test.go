package main

import "testing"

func TestRDSCheckStorageHoldsToTheReferenceRules(t *testing.T) {
	for _, c := range []struct {
		engine  string
		storage rdsStorage
		code    string
	}{
		{"postgres", rdsStorage{Type: "gp3", Allocated: 20}, ""},
		{"postgres", rdsStorage{Type: "gp3", Allocated: 19}, "InvalidParameterCombination"},
		{"postgres", rdsStorage{Type: "standard", Allocated: 5}, ""},
		{"oracle-ee", rdsStorage{Type: "standard", Allocated: 5}, "InvalidParameterCombination"},
		{"sqlserver-ex", rdsStorage{Type: "gp2", Allocated: 16385}, "InvalidParameterCombination"},
		{"db2-se", rdsStorage{Type: "gp2", Allocated: 100}, "StorageTypeNotSupported"},
		{"mysql", rdsStorage{Type: "magnetic", Allocated: 100}, "StorageTypeNotSupported"},
		{"mysql", rdsStorage{Type: "io2", Allocated: 100}, "InvalidParameterCombination"},
		{"mysql", rdsStorage{Type: "io2", Allocated: 100, Iops: 1000}, ""},
		{"mysql", rdsStorage{Type: "io1", Allocated: 99, Iops: 1000}, "InvalidParameterCombination"},
		{"mysql", rdsStorage{Type: "gp2", Allocated: 100, Iops: 1000}, "InvalidParameterCombination"},
		{"mysql", rdsStorage{Type: "io1", Allocated: 100, Iops: 1000, Throughput: 125}, "InvalidParameterCombination"},
		{"mysql", rdsStorage{Type: "gp3", Allocated: 100, Iops: 3000, Throughput: 125}, ""},
		{"mariadb", rdsStorage{Type: "io1", Allocated: 200, Iops: 100}, ""},
		{"mariadb", rdsStorage{Type: "io1", Allocated: 200, Iops: 99}, "InvalidParameterCombination"},
		{"sqlserver-se", rdsStorage{Type: "io1", Allocated: 200, Iops: 100}, "InvalidParameterCombination"},
		{"postgres", rdsStorage{Type: "io1", Allocated: 100, Iops: 5001}, "InvalidParameterCombination"},
	} {
		if code, message := rdsCheckStorage(c.engine, c.storage); code != c.code {
			t.Errorf("%s %+v: got %q (%s), want %q", c.engine, c.storage, code, message, c.code)
		}
	}
}

func TestRDSGrowsByTenthRoundsSmallIncreasesUp(t *testing.T) {
	for _, c := range []struct {
		engine                     string
		current, requested, result int
	}{
		{"postgres", 1000, 1050, 1100},
		{"postgres", 1000, 1200, 1200},
		{"postgres", 1000, 900, 900},
		{"postgres", 1000, 1000, 1000},
		{"postgres", 0, 1000, 1000},
		{"mysql", 21, 22, 24},
		{"sqlserver-ex", 1000, 1050, 1050},
	} {
		if got := rdsGrowsByTenth(c.engine, c.current, c.requested); got != c.result {
			t.Errorf("%s %d to %d: got %d, want %d", c.engine, c.current, c.requested, got, c.result)
		}
	}
}

func TestRDSValidInstanceIdentifier(t *testing.T) {
	for id, valid := range map[string]bool{
		"mydbinstance": true, "a": true, "db-1": true,
		"": false, "1db": false, "db-": false, "db--1": false, "db_1": false,
		"a234567890123456789012345678901234567890123456789012345678901234": false,
	} {
		if rdsValidInstanceIdentifier(id) != valid {
			t.Errorf("%q: want valid=%t", id, valid)
		}
	}
}

func TestRDSDefaultOptionGroupNames(t *testing.T) {
	for _, c := range []struct{ engine, version, name string }{
		{"postgres", "16.15", "default:postgres-16"},
		{"mysql", "8.0.46", "default:mysql-8-0"},
		{"mariadb", "11.4.13", "default:mariadb-11-4"},
		{"oracle-ee", "19.0.0.0.ru-2024-10.rur-2024-10.r1", "default:oracle-ee-19"},
	} {
		if got := rdsDefaultOptionGroupName(c.engine, c.version); got != c.name {
			t.Errorf("%s %s: got %s, want %s", c.engine, c.version, got, c.name)
		}
	}
}
