package main

import (
	"bytes"
	"context"
	"debug/elf"
	"runtime"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The engine builds each AWSAuthenticationPlugin as a shared object for its
// own architecture that exports its engine's plugin declarations and needs no
// library the engine image might lack.
func TestRDSAuthPluginBuildsExportTheirEngineDeclarations(t *testing.T) {
	if _, err := sim.InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatalf("container engine: %v", err)
	}
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	declarations := map[string][]string{
		"mysql":   {"_mysql_plugin_interface_version_", "_mysql_sizeof_struct_st_plugin_", "_mysql_plugin_declarations_"},
		"mariadb": {"_maria_plugin_interface_version_", "_maria_sizeof_struct_st_plugin_", "_maria_plugin_declarations_"},
	}
	for flavor, symbols := range declarations {
		data, err := rdsAuthPlugin(context.Background(), flavor)
		if err != nil {
			t.Fatalf("%s: %v", flavor, err)
		}
		file, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", flavor, err)
		}
		if file.Type != elf.ET_DYN || file.Machine != machine {
			t.Errorf("%s plugin is a %s for %s, want a shared object for %s", flavor, file.Type, file.Machine, machine)
		}
		if libraries, err := file.ImportedLibraries(); err != nil || len(libraries) != 0 {
			t.Errorf("%s plugin needs libraries %v (%v)", flavor, libraries, err)
		}
		if imported, err := file.ImportedSymbols(); err != nil || len(imported) != 0 {
			t.Errorf("%s plugin imports symbols %v (%v)", flavor, imported, err)
		}
		exported := map[string]bool{}
		dynamic, err := file.DynamicSymbols()
		if err != nil {
			t.Fatalf("%s: %v", flavor, err)
		}
		for _, symbol := range dynamic {
			exported[symbol.Name] = symbol.Section != elf.SHN_UNDEF
		}
		for _, symbol := range symbols {
			if !exported[symbol] {
				t.Errorf("%s plugin does not export %s", flavor, symbol)
			}
		}
	}
}
