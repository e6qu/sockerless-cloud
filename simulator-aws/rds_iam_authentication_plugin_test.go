package main

import (
	"bytes"
	"debug/elf"
	"testing"
)

// Each embedded AWSAuthenticationPlugin build is a shared object for its
// architecture that exports its engine's plugin declarations and needs no
// library the engine image might lack.
func TestRDSAuthPluginBuildsExportTheirEngineDeclarations(t *testing.T) {
	machines := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	declarations := map[string][]string{
		"mysql":   {"_mysql_plugin_interface_version_", "_mysql_sizeof_struct_st_plugin_", "_mysql_plugin_declarations_"},
		"mariadb": {"_maria_plugin_interface_version_", "_maria_sizeof_struct_st_plugin_", "_maria_plugin_declarations_"},
	}
	for flavor, symbols := range declarations {
		for arch, machine := range machines {
			name := "rds_auth_plugin/" + flavor + "-" + arch + ".so"
			data, err := rdsAuthPlugins.ReadFile(name)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			file, err := elf.NewFile(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if file.Type != elf.ET_DYN || file.Machine != machine {
				t.Errorf("%s is a %s for %s, want a shared object for %s", name, file.Type, file.Machine, machine)
			}
			if libraries, err := file.ImportedLibraries(); err != nil || len(libraries) != 0 {
				t.Errorf("%s needs libraries %v (%v)", name, libraries, err)
			}
			if imported, err := file.ImportedSymbols(); err != nil || len(imported) != 0 {
				t.Errorf("%s imports symbols %v (%v)", name, imported, err)
			}
			exported := map[string]bool{}
			dynamic, err := file.DynamicSymbols()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, symbol := range dynamic {
				exported[symbol.Name] = symbol.Section != elf.SHN_UNDEF
			}
			for _, symbol := range symbols {
				if !exported[symbol] {
					t.Errorf("%s does not export %s", name, symbol)
				}
			}
		}
	}
}
